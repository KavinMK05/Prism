package proxy

import (
	"bufio"
	"bytes"
	"context"
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

// reasoningInputItem builds a Responses reasoning input item from an upstream
// reasoning item's encrypted_content. summary is always empty: Prism does not
// request reasoning.summary, so the encrypted blob is the only carrier of the
// model's chain of thought.
func reasoningInputItem(encryptedContent string) map[string]interface{} {
	return map[string]interface{}{
		"type":              "reasoning",
		"summary":           []interface{}{},
		"encrypted_content": encryptedContent,
	}
}

// responseReasoningSignatures wraps a response-direction signature so it can
// ride on OpenAIChatMessage's signature list. A response carries at most one —
// the last reasoning item's encrypted_content — so there is no position to
// preserve.
func responseReasoningSignatures(signature string) []ReasoningSignature {
	if strings.TrimSpace(signature) == "" {
		return nil
	}
	return []ReasoningSignature{{Signature: signature}}
}

// translateChatCompletionsToCodexResponses converts a Chat Completions request
// to a Responses API request body suitable for chatgpt.com/backend-api/codex/responses.
func translateChatCompletionsToCodexResponses(req *OpenAIChatRequest, includeReasoning bool) map[string]interface{} {
	// Strip provider prefix from model name
	modelName := req.Model
	if idx := strings.LastIndex(modelName, "/"); idx >= 0 {
		modelName = modelName[idx+1:]
	}

	body := map[string]interface{}{
		"model":  modelName,
		"stream": true, // Always stream upstream
		"store":  false,
	}
	if includeReasoning {
		// Ask the upstream to return the encrypted chain of thought. It is
		// carried to the client as a thinking-block signature and replayed as a
		// reasoning input item on the next turn, so the model keeps its own plan
		// across tool calls instead of re-deriving (and repeating) it. Mirrors
		// CLIProxyAPI, which sets the same include for every Codex target.
		body["include"] = []string{"reasoning.encrypted_content"}
	}

	// Extract system messages as instructions, rest as input
	var instructions []string
	var inputItems []interface{}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			text := contentToString(msg.Content)
			if text != "" {
				instructions = append(instructions, text)
			}
		case "user":
			// Content is a plain string for text-only turns, or an OpenAI parts
			// array (text + image_url) when the client sent images. Images become
			// input_image parts so they survive the hop to a Responses upstream;
			// the leading input_text part is always present, as before.
			content := []map[string]interface{}{
				{"type": "input_text", "text": contentToString(msg.Content)},
			}
			content = append(content, inputImageParts(msg.Content)...)
			inputItems = append(inputItems, map[string]interface{}{
				"type":    "message",
				"role":    "user",
				"content": content,
			})
		case "assistant":
			// The Responses API pairs a reasoning item with the tool calls that
			// follow it, so replay each captured signature immediately in front of
			// the call it produced: reasoning → its calls → reasoning → its calls.
			// Hoisting one item above every call (the previous behaviour) left each
			// later call with no chain of thought attached, and the model re-derives
			// the work that produced it. CallIndex is the number of the turn's calls
			// that preceded the thinking block, so signatures arrive non-decreasing.
			pendingReasoning := msg.ReasoningSignatures
			if !includeReasoning {
				pendingReasoning = nil
			}
			emitReasoningThrough := func(callIndex int) {
				for len(pendingReasoning) > 0 && pendingReasoning[0].CallIndex <= callIndex {
					inputItems = append(inputItems, reasoningInputItem(pendingReasoning[0].Signature))
					pendingReasoning = pendingReasoning[1:]
				}
			}
			for i, tc := range msg.ToolCalls {
				emitReasoningThrough(i)
				inputItems = append(inputItems, map[string]interface{}{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
			// A signature positioned after the last call — or on a turn with no
			// calls at all — still belongs to this turn; replay it before the turn's
			// own text.
			emitReasoningThrough(len(msg.ToolCalls))
			text := contentToString(msg.Content)
			if text != "" {
				inputItems = append(inputItems, map[string]interface{}{
					"type": "message",
					"role": "assistant",
					"content": []map[string]interface{}{
						{"type": "output_text", "text": text},
					},
				})
			}
		case "tool":
			// Tool response → function_call_output. A text-only result keeps the
			// plain-string shape; a tool that returned images (a screenshot tool, or
			// an Anthropic tool_result carrying image parts) switches to the parts
			// array so the images reach the model as input_image parts.
			output := contentToString(msg.Content)
			images := inputImageParts(msg.Content)
			if len(images) == 0 {
				inputItems = append(inputItems, map[string]interface{}{
					"type":    "function_call_output",
					"call_id": msg.ToolID,
					"output":  output,
				})
			} else {
				parts := []map[string]interface{}{
					{"type": "input_text", "text": output},
				}
				parts = append(parts, images...)
				inputItems = append(inputItems, map[string]interface{}{
					"type":    "function_call_output",
					"call_id": msg.ToolID,
					"output":  parts,
				})
			}
		}
	}

	// Set instructions (joined if multiple system messages)
	if len(instructions) > 0 {
		body["instructions"] = strings.Join(instructions, "\n\n")
	} else {
		body["instructions"] = ""
	}

	body["input"] = inputItems

	// Tools
	if len(req.Tools) > 0 {
		var respTools []interface{}
		for _, t := range req.Tools {
			if t.Type == "function" {
				respTools = append(respTools, map[string]interface{}{
					"type":        "function",
					"name":        t.Function.Name,
					"description": t.Function.Description,
					"parameters":  t.Function.Parameters,
				})
			}
		}
		body["tools"] = respTools
	}

	// Temperature
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	// TopP
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}

	// Reasoning
	if req.ReasoningEffort != "" {
		body["reasoning"] = map[string]interface{}{"effort": req.ReasoningEffort}
	}

	// NOTE: max_output_tokens is intentionally NOT forwarded here — the ChatGPT
	// Codex backend rejects it. The generic /v1/responses handlers forward it
	// themselves after calling this translator.

	return body
}

// postResponsesBody sends a Responses API request. When the upstream rejects
// the reasoning.encrypted_content include — some OpenAI-compatible /v1/responses
// implementations do not know the parameter — it retries once without it, so
// reasoning replay can never turn an otherwise working provider into a 400.
func (pr *ProviderRouter) postResponsesBody(ctx context.Context, upstreamURL string, bodyMap map[string]interface{}, rp *config.ResolvedProvider, codexHeaders bool, logTag string) (*http.Response, error) {
	send := func(m map[string]interface{}) (*http.Response, error) {
		bodyBytes, errMarshal := json.Marshal(m)
		if errMarshal != nil {
			return nil, errMarshal
		}
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(bodyBytes))
		if errReq != nil {
			return nil, errReq
		}
		if codexHeaders {
			addCodexHeaders(req, rp)
		} else {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+rp.APIKey)
		}
		log.Printf("-> %s %s%s", req.Method, upstreamURL, logTag)
		return pr.client.Do(req)
	}

	resp, err := send(bodyMap)
	if err != nil || resp.StatusCode != http.StatusBadRequest || bodyMap["include"] == nil {
		return resp, err
	}
	rejection, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !isReasoningRejection(rejection) {
		resp.Body = io.NopCloser(bytes.NewReader(rejection))
		return resp, nil
	}
	log.Printf("[WARN] upstream rejected include=reasoning.encrypted_content, retrying without reasoning replay: %s", string(rejection))
	retryBody := make(map[string]interface{}, len(bodyMap))
	for k, v := range bodyMap {
		retryBody[k] = v
	}
	delete(retryBody, "include")
	// A reasoning item is only meaningful alongside the encrypted_content
	// include; drop it too so the retry is a plain Responses request.
	if input, ok := retryBody["input"].([]interface{}); ok {
		filtered := make([]interface{}, 0, len(input))
		for _, raw := range input {
			item, _ := raw.(map[string]interface{})
			if item != nil && item["type"] == "reasoning" {
				continue
			}
			filtered = append(filtered, raw)
		}
		retryBody["input"] = filtered
	}
	return send(retryBody)
}

// isReasoningRejection reports whether a 400 body blames the reasoning replay
// rather than some unrelated part of the request. Providers that do not know
// the parameter name it ("include"); providers that do know it but cannot
// accept a signature name encrypted_content or reasoning instead. Either way
// the caller's retry is a plain Responses request, so a rejected signature can
// never turn an otherwise working provider into a hard failure.
//
// Matching stays narrow on purpose: retrying a request that failed for an
// unrelated reason would only repeat the same error.
func isReasoningRejection(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"include",
		"encrypted_content",
		"reasoning",
		// A replayed signature the upstream cannot validate: Codex answers
		// "thinking_signature_invalid" / "invalid_encrypted_content", Anthropic
		// "invalid signature in thinking block". None of these contain
		// "reasoning", so they need their own markers — without them a stale or
		// foreign signature would turn an otherwise working provider into a hard
		// failure instead of a retry without the replay.
		"thinking_signature_invalid",
		"invalid signature in thinking block",
		"invalid_encrypted_content",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// contentToString extracts a string from an OpenAI message content field
// which can be either a string or an array of content parts.
func contentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

// inputImageParts extracts OpenAI-style image parts from a chat-completions
// `content` value — either the `image_url: "data:…"` string form or the
// `image_url: {"url": …}` object form — and converts them into Responses
// `input_image` parts. Returns nil when the content carries no images, so
// text-only messages keep their existing shape.
//
// This is what carries a tool result's images, and a user turn's pasted
// screenshots, into a Responses/Codex request body. contentToString reads only
// text parts, so without it those images were dropped on the whole
// Claude /v1/messages -> Responses path.
func inputImageParts(content interface{}) []map[string]interface{} {
	items, ok := content.([]interface{})
	if !ok {
		return nil
	}
	var parts []map[string]interface{}
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != "image_url" && t != "input_image" {
			continue
		}
		if url := responsesImageURL(m); url != "" {
			parts = append(parts, map[string]interface{}{"type": "input_image", "image_url": url})
		}
	}
	return parts
}

// handleGenericChatToResponses translates a Chat Completions request to the
// Responses API for generic OpenAI-compatible providers (e.g. Zen muse-spark)
// that expose /v1/responses. Reuses the same translation as Codex but with
// generic Authorization header instead of Codex headers.
func (pr *ProviderRouter) handleGenericChatToResponses(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, rp *config.ResolvedProvider) {
	reqStart := time.Now()
	client := detectClient(r)
	// Restore reasoning signatures the OpenAI protocol cannot carry back (it has
	// no signature field), so the Codex upstream keeps its chain of thought;
	// see thinking_replay.go. Include=reasoning.encrypted_content is only
	// requested when there is a restored signature to replay - a plain request
	// stays byte-identical, and a provider that rejects the include is retried
	// without it by postResponsesBody.
	replayed := restoreThinkingReplayForOpenAI(r, openAIReq, rp)
	bodyMap := translateChatCompletionsToCodexResponses(openAIReq, replayed > 0)
	// Unlike the Codex backend (which rejects it), generic /v1/responses
	// endpoints accept max_output_tokens; forward the client's limit.
	if openAIReq.MaxTokens > 0 {
		bodyMap["max_output_tokens"] = openAIReq.MaxTokens
	}
	resp, err := pr.postResponsesBody(r.Context(), rp.ResponsesURL(), bodyMap, rp, false, " (generic chat -> responses)")
	if err != nil {
		log.Printf("[ERR] Generic upstream request failed: %v", err)
		WriteOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	log.Printf("<- %d from upstream", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		WriteOpenAIUpstreamError(w, resp.StatusCode, respBody)
		return
	}
	if openAIReq.Stream {
		pr.translateCodexResponsesToChatCompletionsStream(w, r, resp, openAIReq, rp, client, reqStart)
	} else {
		pr.translateCodexResponsesToChatCompletions(w, r, resp, openAIReq, rp, client, reqStart)
	}
}

// handleCodexChatCompletions translates a Chat Completions request to the
// Responses API, sends it to chatgpt.com/backend-api/codex/responses, and
// translates the response back to Chat Completions format.
func (pr *ProviderRouter) handleCodexChatCompletions(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, rp *config.ResolvedProvider) {
	reqStart := time.Now()
	client := detectClient(r)

	// Restore reasoning signatures the OpenAI protocol cannot carry back (it has
	// no signature field), so the Codex upstream keeps its chain of thought;
	// see thinking_replay.go. Include=reasoning.encrypted_content is only
	// requested when there is a restored signature to replay - a plain request
	// stays byte-identical, and a provider that rejects the include is retried
	// without it by postResponsesBody.
	replayed := restoreThinkingReplayForOpenAI(r, openAIReq, rp)

	// Translate to Responses API format
	bodyMap := translateChatCompletionsToCodexResponses(openAIReq, replayed > 0)
	resp, err := pr.postResponsesBody(r.Context(), rp.ResponsesURL(), bodyMap, rp, true, " (codex chat completions translation)")
	if err != nil {
		log.Printf("[ERR] Codex upstream request failed: %v", err)
		WriteOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from codex upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Codex upstream error: %s", string(respBody))
		WriteOpenAIUpstreamError(w, resp.StatusCode, respBody)
		return
	}

	if openAIReq.Stream {
		pr.translateCodexResponsesToChatCompletionsStream(w, r, resp, openAIReq, rp, client, reqStart)
	} else {
		pr.translateCodexResponsesToChatCompletions(w, r, resp, openAIReq, rp, client, reqStart)
	}
}

// translateCodexResponsesToChatCompletionsStream converts Responses API SSE
// events from the Codex backend to Chat Completions SSE chunks in real-time.
func (pr *ProviderRouter) translateCodexResponsesToChatCompletionsStream(w http.ResponseWriter, r *http.Request, resp *http.Response, openAIReq *OpenAIChatRequest, rp *config.ResolvedProvider, client string, reqStart time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	chatcmplID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()

	var inputTokens, outputTokens int
	var cachedTokens int
	var roleSent bool
	var toolCallIndex int
	var finishReason string
	// Accumulate the completed assistant turn so its reasoning signature can be
	// replayed on a later request; an OpenAI client has no field to echo a
	// Responses reasoning item back in, so the cache is the only carrier
	// (see thinking_replay.go).
	var replayText strings.Builder
	replayReasoning := strings.Builder{}
	var replayCalls []OpenAIToolCall
	var replaySignature string

	defer func() {
		stats.Global.RecordRequest(openAIReq.Model, rp.ProviderID, client, inputTokens, outputTokens, cachedTokens, time.Since(reqStart))
	}()

	// Helper to write a Chat Completions SSE chunk
	writeChunk := func(delta OpenAIStreamDelta, finishReasonStr *string, usage *OpenAIStreamUsage) {
		chunk := OpenAIStreamChunk{
			ID:      chatcmplID,
			Object:  "chat.completion.chunk",
			Created: createdAt,
			Model:   openAIReq.Model,
			Choices: []OpenAIStreamChoice{
				{
					Index:        0,
					Delta:        delta,
					FinishReason: finishReasonStr,
				},
			},
			Usage: usage,
		}
		dataJSON, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", dataJSON)
		if canFlush {
			flusher.Flush()
		}
	}

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

		switch eventType {
		case "response.created":
			// Send initial role chunk
			if !roleSent {
				role := "assistant"
				writeChunk(OpenAIStreamDelta{Role: role}, nil, nil)
				roleSent = true
			}

		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			if delta != "" {
				stats.Global.AddTokens(1)
				replayText.WriteString(delta)
				writeChunk(OpenAIStreamDelta{Content: &delta}, nil, nil)
			}

		case "response.reasoning_summary_text.delta":
			delta, _ := event["delta"].(string)
			if delta != "" {
				replayReasoning.WriteString(delta)
				writeChunk(OpenAIStreamDelta{ReasoningContent: &delta}, nil, nil)
			}

		case "response.output_item.added":
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType == "function_call" {
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				replayCalls = append(replayCalls, OpenAIToolCall{ID: callID, Type: "function", Function: OpenAIToolCallFunc{Name: name}})
				idx := toolCallIndex
				toolCallIndex++
				writeChunk(OpenAIStreamDelta{
					ToolCalls: []OpenAIToolCall{
						{
							Index:    &idx,
							ID:       callID,
							Type:     "function",
							Function: OpenAIToolCallFunc{Name: name, Arguments: ""},
						},
					},
				}, nil, nil)
			}

		case "response.function_call_arguments.delta":
			delta, _ := event["delta"].(string)
			if delta != "" {
				idx := toolCallIndex - 1
				if idx < 0 {
					idx = 0
				}
				writeChunk(OpenAIStreamDelta{
					ToolCalls: []OpenAIToolCall{
						{
							Index:    &idx,
							Function: OpenAIToolCallFunc{Arguments: delta},
						},
					},
				}, nil, nil)
			}

		case "response.output_item.done":
			// A reasoning item's final encrypted_content is only delivered on
			// item.done; item.added carries a pre-content snapshot. Capture it for
			// the replay cache below.
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			if it, _ := item["type"].(string); it == "reasoning" {
				if sig, _ := item["encrypted_content"].(string); strings.TrimSpace(sig) != "" {
					replaySignature = sig
				}
			}

		case "response.completed":
			responseObj, _ := event["response"].(map[string]interface{})
			if responseObj != nil {
				// Extract usage
				if usage, ok := responseObj["usage"].(map[string]interface{}); ok {
					if it, ok := usage["input_tokens"].(float64); ok {
						inputTokens = int(it)
					}
					if ot, ok := usage["output_tokens"].(float64); ok {
						outputTokens = int(ot)
					}
					if details, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
						if ct, ok := details["cached_tokens"].(float64); ok && ct > 0 {
							cachedTokens = int(ct)
						}
					}
				}
				// Determine finish reason
				status, _ := responseObj["status"].(string)
				if status == "incomplete" {
					finishReason = "length"
				} else {
					finishReason = "stop"
				}
				// Some providers only surface reasoning items on the completed
				// response; take the last encrypted_content seen for the replay cache.
				if output, ok := responseObj["output"].([]interface{}); ok {
					for _, rawItem := range output {
						itemMap, ok := rawItem.(map[string]interface{})
						if !ok {
							continue
						}
						if t, _ := itemMap["type"].(string); t == "reasoning" {
							if sig, _ := itemMap["encrypted_content"].(string); strings.TrimSpace(sig) != "" {
								replaySignature = sig
							}
						}
					}
				}
			}
			// Send final chunk with finish reason
			fr := finishReason
			writeChunk(OpenAIStreamDelta{}, &fr, &OpenAIStreamUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				TotalTokens:      inputTokens + outputTokens,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Codex SSE read error: %v", err)
	}

	// Remember this turn so a later request that lost the signature (the OpenAI
	// protocol has no carrier for it) can replay it; see thinking_replay.go.
	if replaySignature != "" || replayReasoning.Len() > 0 {
		msg := OpenAIChatMessage{Role: "assistant", Content: replayText.String(), ToolCalls: replayCalls, ReasoningSignatures: responseReasoningSignatures(replaySignature)}
		if replayReasoning.Len() > 0 {
			reasoning := replayReasoning.String()
			msg.ReasoningContent = &reasoning
		}
		cacheThinkingReplayFromMessage(openAISessionID(r, openAIReq), thinkingReplayFamily(rp, openAIReq.Model), msg)
	}

	// If no finish reason was sent, send a default one
	if finishReason == "" {
		fr := "stop"
		writeChunk(OpenAIStreamDelta{}, &fr, nil)
	}

	// Send [DONE]
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

// translateCodexResponsesToChatCompletions collects SSE events from the Codex
// backend and builds a complete Chat Completions JSON response.
func (pr *ProviderRouter) translateCodexResponsesToChatCompletions(w http.ResponseWriter, r *http.Request, resp *http.Response, openAIReq *OpenAIChatRequest, rp *config.ResolvedProvider, client string, reqStart time.Time) {
	chatResp, inputTokens, outputTokens, cachedTokens := collectCodexResponsesSSE(resp, openAIReq.Model)

	// Remember the turn's reasoning signature so a later request can replay it;
	// see thinking_replay.go.
	if len(chatResp.Choices) > 0 {
		cacheThinkingReplayFromMessage(openAISessionID(r, openAIReq), thinkingReplayFamily(rp, openAIReq.Model), chatResp.Choices[0].Message)
	}

	stats.Global.RecordRequest(openAIReq.Model, rp.ProviderID, client, inputTokens, outputTokens, cachedTokens, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(chatResp)
}

// collectCodexResponsesSSE drains a Responses API SSE stream (requested with
// stream=true even for non-streaming clients) and accumulates it into a
// complete Chat Completions response. Shared by the Codex Chat Completions
// translator and the generic Anthropic-inbound responses translator.
func collectCodexResponsesSSE(resp *http.Response, model string) (chatResp OpenAIChatResponse, inputTokens, outputTokens, cachedTokens int) {
	var contentText string
	var toolCalls []OpenAIToolCall
	var finishReason = "stop"
	var reasoningSignature string
	var completedResponse map[string]interface{}

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

		switch eventType {
		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			contentText += delta

		case "response.output_item.added":
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType == "function_call" {
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				toolCalls = append(toolCalls, OpenAIToolCall{
					ID:       callID,
					Type:     "function",
					Function: OpenAIToolCallFunc{Name: name, Arguments: ""},
				})
			}

		case "response.function_call_arguments.delta":
			delta, _ := event["delta"].(string)
			if len(toolCalls) > 0 {
				toolCalls[len(toolCalls)-1].Function.Arguments += delta
			}

		case "response.output_item.done":
			// The reasoning item's final encrypted_content is only delivered on
			// item.done; item.added carries a pre-content snapshot.
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			if it, _ := item["type"].(string); it == "reasoning" {
				if sig, _ := item["encrypted_content"].(string); strings.TrimSpace(sig) != "" {
					reasoningSignature = sig
				}
			}

		case "response.completed":
			responseObj, _ := event["response"].(map[string]interface{})
			if responseObj != nil {
				completedResponse = responseObj
				if usage, ok := responseObj["usage"].(map[string]interface{}); ok {
					if it, ok := usage["input_tokens"].(float64); ok {
						inputTokens = int(it)
					}
					if ot, ok := usage["output_tokens"].(float64); ok {
						outputTokens = int(ot)
					}
					if details, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
						if ct, ok := details["cached_tokens"].(float64); ok && ct > 0 {
							cachedTokens = int(ct)
						}
					}
				}
				// Some providers only surface reasoning items on the completed
				// response; take the last encrypted_content seen.
				if output, ok := responseObj["output"].([]interface{}); ok {
					for _, rawItem := range output {
						itemMap, ok := rawItem.(map[string]interface{})
						if !ok {
							continue
						}
						if t, _ := itemMap["type"].(string); t == "reasoning" {
							if sig, _ := itemMap["encrypted_content"].(string); strings.TrimSpace(sig) != "" {
								reasoningSignature = sig
							}
						}
					}
				}
				status, _ := responseObj["status"].(string)
				if status == "incomplete" {
					finishReason = "length"
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Codex SSE read error: %v", err)
	}

	// If we have a completed response with output items, extract tool calls from there
	if completedResponse != nil {
		if output, ok := completedResponse["output"].([]interface{}); ok {
			// Clear and rebuild tool calls from the completed response
			toolCalls = nil
			for _, item := range output {
				itemMap, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				itemType, _ := itemMap["type"].(string)
				if itemType == "function_call" {
					callID, _ := itemMap["call_id"].(string)
					name, _ := itemMap["name"].(string)
					args, _ := itemMap["arguments"].(string)
					toolCalls = append(toolCalls, OpenAIToolCall{
						ID:       callID,
						Type:     "function",
						Function: OpenAIToolCallFunc{Name: name, Arguments: args},
					})
				}
			}
		}
	}

	// A response carrying tool calls must report finish_reason tool_calls so
	// downstream translators (translateFromOpenAI) map it to Anthropic's
	// tool_use stop reason instead of end_turn.
	if len(toolCalls) > 0 && finishReason == "stop" {
		finishReason = "tool_calls"
	}

	return OpenAIChatResponse{
		ID:     fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object: "chat.completion",
		Model:  model,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIChatMessage{
					Role:                "assistant",
					Content:             contentText,
					ToolCalls:           toolCalls,
					ReasoningSignatures: responseReasoningSignatures(reasoningSignature),
				},
				FinishReason: finishReason,
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:        inputTokens,
			CompletionTokens:    outputTokens,
			TotalTokens:         inputTokens + outputTokens,
			PromptTokensDetails: promptTokensDetails(cachedTokens),
		},
	}, inputTokens, outputTokens, cachedTokens
}
