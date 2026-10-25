package proxy

import (
	"bufio"
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

	openAIReq := translateToOpenAIForResponses(anthroReq)
	// Restore reasoning signatures the client could not carry back (Claude Code
	// rewrites historical thinking to redacted_thinking on compaction, which
	// drops the signature); see thinking_replay.go.
	restoreThinkingReplayForAnthropic(r, anthroReq, rp, openAIReq, true, false)
	// Mirror handleOpenAIStreaming/handleOpenAINonStreaming: strip the effort
	// for non-reasoning models and clamp invalid values before the request is
	// translated to the Responses body.
	openAIReq.ReasoningEffort = pr.validateReasoningEffort(openAIReq.Model, openAIReq.ReasoningEffort)

	codexTarget := rp.ProviderType == "codex"
	bodyMap := translateChatCompletionsToCodexResponses(openAIReq, true)
	// Unlike the Codex backend (which rejects it), generic /v1/responses
	// endpoints accept max_output_tokens; forward the client's limit.
	if openAIReq.MaxTokens > 0 && !codexTarget {
		bodyMap["max_output_tokens"] = openAIReq.MaxTokens
	}

	dbg := pr.dbgCapture("messages-responses", anthroReq.Stream, anthroReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", anthroReq)
	dbg.writeJSON("2_translated_request.json", bodyMap)

	resp, err := pr.postResponsesBody(r.Context(), rp.ResponsesURL(), bodyMap, rp, codexTarget, " (generic chat -> responses, anthropic inbound)")
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
	chatResp, inputTokens, outputTokens, cachedTokens := collectCodexResponsesSSE(resp, anthroReq.Model)
	stats.Global.RecordRequest(anthroReq.Model, rp.ProviderID, client, inputTokens, outputTokens, cachedTokens, time.Since(reqStart))

	// Remember the turn's reasoning signature so a later request can replay it;
	// see thinking_replay.go.
	if len(chatResp.Choices) > 0 {
		cacheThinkingReplayFromMessage(claudeCodeSessionID(r, anthroReq.Metadata), thinkingReplayFamily(rp, anthroReq.Model), chatResp.Choices[0].Message)
	}

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

	// Accumulate the completed assistant turn so its reasoning signature can be
	// replayed on a later request; see thinking_replay.go.
	var replayText strings.Builder
	var replayCalls []OpenAIToolCall
	var replaySignature string

	var inputTokens, outputTokens, liveOutputTokens int
	defer func() {
		out := outputTokens
		if out == 0 {
			out = liveOutputTokens
		}
		stats.Global.RecordRequest(anthroReq.Model, rp.ProviderID, client, inputTokens, out, state.cacheReadTokens, time.Since(reqStart))
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
			replayText.WriteString(delta)
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
				replayCalls = append(replayCalls, OpenAIToolCall{ID: callID, Type: "function", Function: OpenAIToolCallFunc{Name: name}})
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

		case "response.output_item.done":
			// A reasoning item's final encrypted_content is only delivered on
			// item.done (item.added carries a pre-content snapshot). Carry it as
			// the thinking block's signature so Claude Code echoes it back and the
			// model keeps its chain of thought across turns.
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			if itemType, _ := item["type"].(string); itemType == "reasoning" {
				sig, _ := item["encrypted_content"].(string)
				if strings.TrimSpace(sig) == "" {
					continue
				}
				replaySignature = sig
				state.thinkingSignature = sig
				if !state.thinkingBlockOpen && !state.hasContentBlock {
					// No reasoning summary was streamed (Codex only emits one when
					// reasoning.summary is requested), so emit a signature-only
					// thinking block. Without it there is no carrier for the model's
					// reasoning and it re-derives work it already did.
					state.openThinkingBlock()
					state.closeBlock("thinking")
				}
			}

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
					if details, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
						if ct, ok := details["cached_tokens"].(float64); ok && ct > 0 {
							state.cacheReadTokens = int(ct)
						}
					}
				}
				// Fallback for providers that only surface reasoning items on the
				// completed response rather than on output_item.done.
				if state.thinkingSignature == "" && !state.hasContentBlock {
					if output, ok := responseObj["output"].([]interface{}); ok {
						for _, rawItem := range output {
							itemMap, ok := rawItem.(map[string]interface{})
							if !ok {
								continue
							}
							if t, _ := itemMap["type"].(string); t == "reasoning" {
								if sig, _ := itemMap["encrypted_content"].(string); strings.TrimSpace(sig) != "" {
									state.thinkingSignature = sig
									replaySignature = sig
									state.openThinkingBlock()
								}
							}
						}
					}
				}
				state.totalPromptTokens = inputTokens
				// Responses input_tokens is the logical total and includes cache
				// hits; Anthropic's input_tokens must not (usagePayload emits the
				// hits as cache_read_input_tokens instead).
				if state.cacheReadTokens > 0 && state.totalPromptTokens >= state.cacheReadTokens {
					state.totalPromptTokens -= state.cacheReadTokens
				}
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

	// Remember this turn's reasoning signature so a later request can replay it
	// (see thinking_replay.go). A Chat Completions client has no field to echo a
	// Responses reasoning item back in, so this cache is its only carrier; the
	// plain assistant text alone is not worth caching.
	if replaySignature != "" {
		cacheThinkingReplayFromMessage(
			claudeCodeSessionID(r, anthroReq.Metadata),
			thinkingReplayFamily(rp, anthroReq.Model),
			OpenAIChatMessage{Role: "assistant", Content: replayText.String(), ToolCalls: replayCalls, ReasoningSignatures: responseReasoningSignatures(replaySignature)},
		)
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
