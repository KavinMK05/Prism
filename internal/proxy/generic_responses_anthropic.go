package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/stats"
)

// handleGenericResponsesForAnthropic serves an Anthropic /v1/messages request
// whose resolved model is configured with API=="responses" on an openai-type
// provider (e.g. Zen muse-spark/gpt/grok). It translates Anthropic -> Chat
// Completions -> Responses, POSTs to the provider's /v1/responses endpoint
// with standard Bearer auth, and converts the Responses result back into
// Anthropic protocol for the client — unlike handleGenericChatToResponses,
// which answers in OpenAI Chat Completions protocol and must only be used for
// OpenAI-protocol inbound requests.
func (pr *ProviderRouter) handleGenericResponsesForAnthropic(w http.ResponseWriter, r *http.Request, anthroReq *AnthropicRequest, rp *config.ResolvedProvider) {
	client := detectClient(r)
	stats.Global.StartRequest(anthroReq.Model, rp.ProviderID, client)
	defer stats.Global.EndRequest()
	reqStart := time.Now()

	openAIReq := translateToOpenAI(anthroReq)
	// Mirror handleOpenAIStreaming/handleOpenAINonStreaming: strip the effort
	// for non-reasoning models and clamp invalid values before the request is
	// translated to the Responses body.
	openAIReq.ReasoningEffort = pr.validateReasoningEffort(openAIReq.Model, openAIReq.ReasoningEffort)

	bodyMap := translateChatCompletionsToCodexResponses(openAIReq)
	// Unlike the Codex backend (which rejects it), generic /v1/responses
	// endpoints accept max_output_tokens; forward the client's limit.
	if openAIReq.MaxTokens > 0 {
		bodyMap["max_output_tokens"] = openAIReq.MaxTokens
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		WriteAnthropicError(w, 500, "api_error", "Failed to marshal request")
		return
	}

	dbg := pr.dbgCapture("messages-responses", anthroReq.Stream, anthroReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", anthroReq)
	dbg.writeJSON("2_translated_request.json", bodyMap)

	upstreamURL := rp.ResponsesURL()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(bodyBytes))
	if err != nil {
		WriteAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)
	log.Printf("-> %s %s (generic chat -> responses, anthropic inbound)", req.Method, upstreamURL)
	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Generic upstream request failed: %v", err)
		WriteAnthropicError(w, 502, "api_error", fmt.Sprintf("Upstream request failed: %v", err))
		return
	}
	defer resp.Body.Close()
	log.Printf("<- %d from upstream", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		WriteAnthropicUpstreamError(w, resp.StatusCode, respBody)
		return
	}

	if anthroReq.Stream {
		pr.translateGenericResponsesToAnthropicStream(w, r, resp, anthroReq, rp, client, reqStart)
	} else {
		pr.translateGenericResponsesToAnthropicJSON(w, r, resp, anthroReq, rp, client, reqStart)
	}
}

// translateGenericResponsesToAnthropicJSON collects the Responses SSE stream
// from the upstream (always requested with stream=true, mirroring the Codex
// handlers) and answers the Anthropic client with a complete /v1/messages
// JSON response.
func (pr *ProviderRouter) translateGenericResponsesToAnthropicJSON(w http.ResponseWriter, r *http.Request, resp *http.Response, anthroReq *AnthropicRequest, rp *config.ResolvedProvider, client string, reqStart time.Time) {
	chatResp, inputTokens, outputTokens := collectCodexResponsesSSE(resp, anthroReq.Model)
	stats.Global.RecordRequest(anthroReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))

	anthroResp := translateFromOpenAI(&chatResp, anthroReq)
	if len(anthroResp.Content) == 0 {
		anthroResp.Content = []interface{}{AnthropicTextBlock{Type: "text", Text: ""}}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(anthroResp)
}

// translateGenericResponsesToAnthropicStream converts Responses API SSE events
// from a generic /v1/responses endpoint into Anthropic SSE events in real time,
// driving the shared Anthropic stream state machine (same block/delta protocol
// handleOpenAIStreaming emits).
func (pr *ProviderRouter) translateGenericResponsesToAnthropicStream(w http.ResponseWriter, r *http.Request, resp *http.Response, anthroReq *AnthropicRequest, rp *config.ResolvedProvider, client string, reqStart time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	msgID := "msg_" + sanitizeMessageIDFragment(anthroReq.Model)

	state := newStreamState(w, flusher, canFlush, msgID, 0)

	var inputTokens, outputTokens, liveOutputTokens int
	defer func() {
		out := outputTokens
		if out == 0 {
			out = liveOutputTokens
		}
		stats.Global.RecordRequest(anthroReq.Model, rp.ProviderID, client, inputTokens, out, time.Since(reqStart))
	}()

	state.writeSSE("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":          msgID,
			"type":        "message",
			"role":        "assistant",
			"model":       anthroReq.Model,
			"content":     []interface{}{},
			"stop_reason": nil,
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	stopPings := state.startPings()
	defer stopPings()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	stopReason := ""
	streamErrored := false

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

		switch eventType {
		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			if delta == "" {
				continue
			}
			liveOutputTokens++
			stats.Global.AddTokens(1)
			if state.thinkingBlockOpen {
				state.closeBlock("thinking")
			}
			if !state.textBlockOpen {
				state.openTextBlock()
			}
			state.writeSSE("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": state.contentBlockIndex,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": delta,
				},
			})

		case "response.reasoning_summary_text.delta":
			delta, _ := event["delta"].(string)
			if delta == "" {
				continue
			}
			liveOutputTokens++
			stats.Global.AddTokens(1)
			if !state.thinkingBlockOpen && !state.thinkingDone {
				state.openThinkingBlock()
			}
			if state.thinkingBlockOpen {
				state.writeSSE("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": state.contentBlockIndex,
					"delta": map[string]interface{}{
						"type":     "thinking_delta",
						"thinking": delta,
					},
				})
			}

		case "response.output_item.added":
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			if itemType, _ := item["type"].(string); itemType == "function_call" {
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				// Responses names every function_call item up front (call_id +
				// name), so the tool_use block can open immediately with the
				// upstream ID; no pending-call buffering is needed.
				state.openToolUseBlockWithID(name, callID)
			}

		case "response.function_call_arguments.delta":
			delta, _ := event["delta"].(string)
			if delta == "" {
				continue
			}
			liveOutputTokens++
			stats.Global.AddTokens(1)
			state.emitToolArgsDelta(delta)

		case "response.completed":
			responseObj, _ := event["response"].(map[string]interface{})
			if responseObj != nil {
				if usage, ok := responseObj["usage"].(map[string]interface{}); ok {
					if it, ok := usage["input_tokens"].(float64); ok && it > 0 {
						inputTokens = int(it)
					}
					if ot, ok := usage["output_tokens"].(float64); ok && ot > 0 {
						outputTokens = int(ot)
					}
				}
				state.totalPromptTokens = inputTokens
				status, _ := responseObj["status"].(string)
				switch {
				case status == "incomplete":
					stopReason = "max_tokens"
				case state.toolCallIndex > 0:
					stopReason = "tool_use"
				default:
					stopReason = "end_turn"
				}
			}

		case "response.failed", "response.error":
			log.Printf("[ERR] Generic responses stream failed: %s", data)
			state.sendStreamError("api_error", "Upstream responses stream failed")
			streamErrored = true
		}

		if streamErrored {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
		state.sendStreamError("api_error", "Stream read error: "+err.Error())
		return
	}
	stopPings()

	if streamErrored {
		return
	}

	// Terminate the SSE stream even if response.completed never arrived
	// (connection drop): close any dangling blocks, guarantee at least an
	// empty text block, and emit a fallback stop so clients don't hang.
	state.closeAllBlocks()
	state.sendEmptyTextBlock()
	if stopReason == "" {
		stopReason = "end_turn"
	}
	out := outputTokens
	if out == 0 {
		out = liveOutputTokens
	}
	state.sendStopReason(stopReason, out)
}