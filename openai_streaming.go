package main

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
)

func (pr *ProviderRouter) handleOpenAIStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, anthroReq *AnthropicRequest, rp *ResolvedProvider) {
	reqStart := time.Now()
	outputTokens := 0
	inputTokens := 0
	liveOutputTokens := 0
	client := detectClient(r)
	defer func() {
		out := outputTokens
		if out == 0 {
			out = liveOutputTokens
		}
		globalStats.RecordRequest(anthroReq.Model, rp.ProviderID, client, inputTokens, out, time.Since(reqStart))
	}()

	// Validate reasoning_effort for the model
	openAIReq.ReasoningEffort = pr.validateReasoningEffort(openAIReq.Model, openAIReq.ReasoningEffort)

	// Inject stream_options to get usage data from the upstream provider
	openAIReq.StreamOptions = &OpenAIStreamOptions{IncludeUsage: true}

	body, err := json.Marshal(openAIReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal OpenAI request")
		return
	}

	// Capture the complete Anthropic -> OpenAI-compatible streaming hop.
	dbg := newTranslationDebugCapture("messages-openai", true, anthroReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", anthroReq)
	dbg.writeJSON("2_translated_request.json", openAIReq)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (streaming)", req.Method, rp.chatCompletionsURL())

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Upstream request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeAnthropicError(w, resp.StatusCode, "api_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	msgID := "msg_" + sanitizeMessageIDFragment(anthroReq.Model)

	state := newStreamState(w, flusher, canFlush, msgID, 0)

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

	scanner := bufio.NewScanner(dbg.teeBody(resp.Body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	prevChunkHasThinking := false
	// Track the active tool call by its stable provider identity. Some
	// OpenAI-compatible servers repeat the id on every delta, while others
	// only send it on the first delta and rely on index thereafter.
	activeToolIndex := ""
	finishStopReason := ""
	// flushPendingToolWithCount emits a buffered (not-yet-opened) tool call
	// and bumps the live-output-token fallback counter for its args delta.
	flushPendingToolWithCount := func() {
		if state.pendingOpenTool == nil {
			return
		}
		if state.pendingOpenTool.args.Len() > 0 {
			liveOutputTokens++
			globalStats.AddTokens(1)
		}
		state.flushPendingOpenTool()
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Printf("[WARN] Failed to parse OpenAI chunk: %v", err)
			continue
		}

		// Capture usage first: with stream_options.include_usage=true the
		// final usage chunk has choices:[] and must not be skipped.
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				inputTokens = chunk.Usage.PromptTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				outputTokens = chunk.Usage.CompletionTokens
			}
			if chunk.Usage.PromptTokensDetails != nil && chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
				state.cacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			// OpenAI's prompt_tokens already includes cached tokens; Anthropic
			// splits them into input_tokens (non-cached) and
			// cache_read_input_tokens. Subtract so the message_delta.usage we
			// emit does not double-count the cached portion.
			if state.cacheReadTokens > 0 && inputTokens >= state.cacheReadTokens {
				inputTokens -= state.cacheReadTokens
			}
			state.totalPromptTokens = inputTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		reasoningText := ""
		if choice.Delta.ReasoningContent != nil {
			reasoningText = *choice.Delta.ReasoningContent
		}
		if reasoningText == "" && choice.Delta.Reasoning != nil {
			reasoningText = *choice.Delta.Reasoning
		}
		currentChunkHasThinking := reasoningText != ""

		if currentChunkHasThinking {
			liveOutputTokens++
			globalStats.AddTokens(1)
			if !state.thinkingBlockOpen {
				state.openThinkingBlock()
			}
			state.writeSSE("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": state.contentBlockIndex,
				"delta": map[string]interface{}{
					"type":     "thinking_delta",
					"thinking": reasoningText,
				},
			})
		}

		if (prevChunkHasThinking && !currentChunkHasThinking ||
			currentChunkHasThinking && choice.Delta.Content != nil && *choice.Delta.Content != "") && state.thinkingBlockOpen {
			// Some compatible servers put the final reasoning and first text
			// delta in the same chunk. Close reasoning before opening text.
			state.closeBlock("thinking")
		}
		prevChunkHasThinking = currentChunkHasThinking

		if len(choice.Delta.ToolCalls) > 0 {
			for _, tc := range choice.Delta.ToolCalls {
				// Stable identity for this call: prefer upstream id, then index.
				toolKey := tc.ID
				if toolKey == "" && tc.Index != nil {
					toolKey = fmt.Sprintf("index:%d", *tc.Index)
				}

				// A delta carrying a function name starts a new tool call.
				if tc.Function.Name != "" {
					// Finalize any prior buffered (not-yet-opened) call before
					// starting a new one: the upstream moved on without ever
					// sending an id for it, so mint a synthetic id now.
					flushPendingToolWithCount()
					if state.toolUseBlockOpen {
						state.closeBlock("tool_use")
					}

					pu := &pendingOpenTool{name: tc.Function.Name, id: tc.ID, key: toolKey}
					if tc.Function.Arguments != "" {
						pu.args.WriteString(tc.Function.Arguments)
					}
					if tc.ID != "" {
						// Both name and id known: open the block now with the real
						// (sanitized) id and stream any args immediately.
						state.openToolUseBlockWithID(tc.Function.Name, tc.ID)
						if pu.args.Len() > 0 {
							liveOutputTokens++
							globalStats.AddTokens(1)
							state.emitToolArgsDelta(pu.args.String())
						}
					} else {
						// Buffer until the id arrives on a later delta. Some
						// OpenAI-compatible providers split name and id across
						// separate deltas; emitting content_block_start now would
						// force a synthetic id that diverges from upstream.
						state.pendingOpenTool = pu
					}
					if tc.Index != nil {
						activeToolIndex = fmt.Sprintf("index:%d", *tc.Index)
					}
					continue
				}

				// Continuation delta (no name): belongs to the active call.
				// Reject deltas whose index doesn't match the active call when
				// the upstream uses index for continuation identity.
				if tc.Index != nil && activeToolIndex != "" &&
					fmt.Sprintf("index:%d", *tc.Index) != activeToolIndex {
					continue
				}

				args := tc.Function.Arguments
				if state.toolUseBlockOpen {
					// Already-opened active call: stream args incrementally.
					if args != "" {
						liveOutputTokens++
						globalStats.AddTokens(1)
						state.emitToolArgsDelta(args)
					}
				} else if state.pendingOpenTool != nil {
					// Buffered call: append args, and open now if the id has
					// finally arrived on this delta.
					if args != "" {
						state.pendingOpenTool.args.WriteString(args)
					}
					if tc.ID != "" && state.pendingOpenTool.id == "" {
						state.pendingOpenTool.id = tc.ID
					}
					if state.pendingOpenTool.id != "" {
						flushPendingToolWithCount()
					}
				}
			}
		}

		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			activeToolIndex = ""
			liveOutputTokens++
			globalStats.AddTokens(1)
			// Emit any buffered tool call (and close an open tool_use block)
			// before opening a text block so the content isn't silently
			// dropped and tool/text ordering is preserved.
			flushPendingToolWithCount()
			if state.toolUseBlockOpen {
				state.closeBlock("tool_use")
			}
			if !state.textBlockOpen && !state.thinkingBlockOpen {
				state.openTextBlock()
			}
			if state.textBlockOpen {
				state.writeSSE("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": state.contentBlockIndex,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": *choice.Delta.Content,
					},
				})
			}
		}

		if choice.FinishReason != nil {
			flushPendingToolWithCount()
			state.closeAllBlocks()
			state.sendEmptyTextBlock()

			// Record the stop reason but defer message_delta/message_stop
			// until after the loop: with stream_options.include_usage=true
			// the usage chunk arrives AFTER finish_reason, and we need its
			// token counts in the message_delta.usage event.
			finishStopReason = "end_turn"
			switch *choice.FinishReason {
			case "length":
				finishStopReason = "max_tokens"
			case "tool_calls", "function_call":
				finishStopReason = "tool_use"
			}
		}
	}

	streamErrored := false
	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
		state.sendStreamError("api_error", "Stream read error: "+err.Error())
		streamErrored = true
	}
	stopPings()

	if streamErrored {
		return
	}

	// Terminate the SSE stream. If finish_reason was seen we deferred the
	// terminal events so the usage-only chunk could be captured first.
	// If the stream ended without finish_reason (connection drop or [DONE]
	// with no finish_reason), close remaining blocks and emit a fallback
	// stop so clients don't hang waiting for a terminal event.
	if state.thinkingBlockOpen || state.textBlockOpen || state.toolUseBlockOpen || state.pendingOpenTool != nil {
		flushPendingToolWithCount()
		state.closeAllBlocks()
	}
	if !state.hasContentBlock {
		state.sendEmptyTextBlock()
	}
	stopReason := finishStopReason
	if stopReason == "" {
		stopReason = "end_turn"
	}
	out := outputTokens
	if out == 0 {
		out = liveOutputTokens
	}
	state.sendStopReason(stopReason, out)
}
