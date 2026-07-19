package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// addCodexHeaders sets the required headers for chatgpt.com/backend-api/codex/responses
func addCodexHeaders(req *http.Request, rp *ResolvedProvider) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)
	if rp.ChatGPTAccountID != "" {
		req.Header.Set("chatgpt-account-id", rp.ChatGPTAccountID)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
}

// normalizeCodexResponsesRequest ensures required fields are present for the Codex backend
func normalizeCodexResponsesRequest(respReq *ResponsesAPIRequest) map[string]interface{} {
	// Strip provider prefix from model name (e.g. "openai/gpt-5.4" -> "gpt-5.4")
	// The Codex backend expects bare model names without provider prefixes.
	modelName := respReq.Model
	if idx := strings.LastIndex(modelName, "/"); idx >= 0 {
		modelName = modelName[idx+1:]
	}

	body := map[string]interface{}{
		"model":  modelName,
		"stream": true, // Always stream upstream; reassemble if client requested non-stream
	}

	// Instructions must be present (even if empty) or the backend returns 400
	if respReq.Instructions != nil {
		body["instructions"] = respReq.Instructions
	} else {
		body["instructions"] = ""
	}

	// Input
	if respReq.Input != nil {
		body["input"] = respReq.Input
	}

	// Tools
	if len(respReq.Tools) > 0 {
		body["tools"] = respReq.Tools
	}

	// Temperature
	if respReq.Temperature != nil {
		body["temperature"] = *respReq.Temperature
	}

	// TopP
	if respReq.TopP != nil {
		body["top_p"] = *respReq.TopP
	}

	// Store defaults to false
	if respReq.Store != nil {
		body["store"] = *respReq.Store
	} else {
		body["store"] = false
	}

	// Reasoning
	if respReq.Reasoning != nil {
		body["reasoning"] = respReq.Reasoning
	}

	// Text format
	if respReq.Text != nil {
		body["text"] = respReq.Text
	}

	// NOTE: max_output_tokens is intentionally NOT forwarded (the backend rejects it)

	return body
}

// handleCodexResponsesAPI forwards a Responses API request directly to
// chatgpt.com/backend-api/codex/responses. For streaming requests, SSE events
// are passed through. For non-streaming requests, we force stream=true upstream
// and reassemble the complete response from SSE events.
func (pr *ProviderRouter) handleCodexResponsesAPI(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *ResolvedProvider) {
	reqStart := time.Now()
	client := detectClient(r)

	// Build the normalized request body (always stream upstream)
	bodyMap := normalizeCodexResponsesRequest(respReq)
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal request: "+err.Error())
		return
	}

	upstreamURL := rp.responsesURL()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	addCodexHeaders(req, rp)

	log.Printf("-> %s %s (codex responses passthrough)", req.Method, upstreamURL)

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Codex upstream request failed: %v", err)
		writeOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from codex upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Codex upstream error: %s", string(respBody))
		writeOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Codex upstream returned status %d: %s", resp.StatusCode, string(respBody)))
		return
	}

	if respReq.Stream {
		pr.passthroughCodexResponsesSSE(w, r, resp, respReq, rp, client, reqStart)
	} else {
		pr.reassembleCodexResponses(w, r, resp, respReq, rp, client, reqStart)
	}
}

// codexKeepAliveInterval is how long the proxy waits without receiving any
// upstream SSE data before emitting a comment frame to the client. This keeps
// the client (and any intermediate proxy) from timing out an otherwise-idle
// stream during slow upstream reasoning / generation. ChatGPT's Codex backend
// can go silent for long stretches while the model plans, which otherwise
// trips a ~30s client idle timeout and produces "context canceled" errors.
const codexKeepAliveInterval = 15 * time.Second

// passthroughCodexResponsesSSE streams SSE events from the Codex backend directly to the client.
func (pr *ProviderRouter) passthroughCodexResponsesSSE(w http.ResponseWriter, r *http.Request, resp *http.Response, respReq *ResponsesAPIRequest, rp *ResolvedProvider, client string, reqStart time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable any buffering proxy 

	flusher, canFlush := w.(http.Flusher)

	var inputTokens, outputTokens int
	defer func() {
		globalStats.RecordRequest(respReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))
	}()

	ctx := r.Context()

	// Read upstream SSE lines in a goroutine so the main loop can also react to
	// client disconnects and idle keepalive timers.
	lineCh := make(chan string, 64)
	errCh := make(chan error, 1)
	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lineCh <- scanner.Text():
			case <-ctx.Done():
				errCh <- scanner.Err()
				return
			}
		}
		errCh <- scanner.Err()
	}()

	// processLine extracts usage and counts tokens for a single SSE line, then
	// writes it through to the client.
	processLine := func(line string) {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data != "[DONE]" {
				// Try to extract usage from response.completed events
				pr.extractCodexUsage(data, &inputTokens, &outputTokens)
				// Count output tokens from text deltas for live stats
				if strings.Contains(data, "output_text.delta") {
					globalStats.AddTokens(1)
				}
			}
		}
		// Write the line directly to the client
		fmt.Fprintf(w, "%s\n", line)
		if canFlush {
			flusher.Flush()
		}
	}

	idle := time.NewTimer(codexKeepAliveInterval)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected (or request canceled). The upstream request
			// shares this context, so its body read is also torn down; nothing
			// more to do here.
			return
		case line, ok := <-lineCh:
			if !ok {
				// Upstream finished. Report a real read error only if the client
				// is still connected — a context-canceled error here just means
				// the client hung up first, which is expected and not a bug.
				if err := <-errCh; err != nil && ctx.Err() == nil {
					log.Printf("[ERR] Codex SSE read error: %v", err)
				}
				return
			}
			processLine(line)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(codexKeepAliveInterval)
		case <-idle.C:
			// Upstream is idle; emit a comment frame so the client connection
			// stays alive. SSE comment frames are ignored by client parsers.
			fmt.Fprintf(w, ": keep-alive %d\n\n", time.Now().UnixMilli())
			if canFlush {
				flusher.Flush()
			}
			idle.Reset(codexKeepAliveInterval)
		}
	}
}

// reassembleCodexResponses collects SSE events from the Codex backend and
// builds a complete Responses API JSON response for non-streaming clients.
func (pr *ProviderRouter) reassembleCodexResponses(w http.ResponseWriter, r *http.Request, resp *http.Response, respReq *ResponsesAPIRequest, rp *ResolvedProvider, client string, reqStart time.Time) {
	var completedResponse map[string]interface{}
	var inputTokens, outputTokens int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		if eventType == "response.completed" {
			if responseObj, ok := event["response"].(map[string]interface{}); ok {
				completedResponse = responseObj
				// Extract usage
				if usage, ok := responseObj["usage"].(map[string]interface{}); ok {
					if it, ok := usage["input_tokens"].(float64); ok {
						inputTokens = int(it)
					}
					if ot, ok := usage["output_tokens"].(float64); ok {
						outputTokens = int(ot)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Codex SSE read error: %v", err)
	}

	globalStats.RecordRequest(respReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))

	if completedResponse == nil {
		writeOpenAIError(w, 502, "server_error", "Failed to reassemble response from Codex backend")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(completedResponse)
}

// extractCodexUsage parses a data line to find usage info in response.completed events
func (pr *ProviderRouter) extractCodexUsage(data string, inputTokens, outputTokens *int) {
	if !strings.Contains(data, "response.completed") {
		return
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return
	}
	if responseObj, ok := event["response"].(map[string]interface{}); ok {
		if usage, ok := responseObj["usage"].(map[string]interface{}); ok {
			if it, ok := usage["input_tokens"].(float64); ok {
				*inputTokens = int(it)
			}
			if ot, ok := usage["output_tokens"].(float64); ok {
				*outputTokens = int(ot)
			}
		}
	}
}
