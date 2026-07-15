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
	msgID := fmt.Sprintf("msg_%s", anthroReq.Model)

	state := newStreamState(w, flusher, canFlush, msgID)

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

	prevChunkHasThinking := false
	finishStopReason := ""

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
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		currentChunkHasThinking := choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != ""

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
					"thinking": *choice.Delta.ReasoningContent,
				},
			})
		}

		if prevChunkHasThinking && !currentChunkHasThinking && state.thinkingBlockOpen {
			state.closeBlock("thinking")
		}
		prevChunkHasThinking = currentChunkHasThinking

		if len(choice.Delta.ToolCalls) > 0 {
			for _, tc := range choice.Delta.ToolCalls {
				if tc.ID != "" {
					if state.toolUseBlockOpen {
						state.closeBlock("tool_use")
					}
					state.openToolUseBlockWithID(tc.Function.Name, tc.ID)
				}

				if tc.Function.Arguments != "" {
					liveOutputTokens++
					globalStats.AddTokens(1)
					state.writeSSE("content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": state.contentBlockIndex,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": tc.Function.Arguments,
						},
					})
				}
			}
		}

		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			liveOutputTokens++
			globalStats.AddTokens(1)
			// If a tool_use block is open, close it before opening a text
			// block so the content isn't silently dropped.
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
	if state.thinkingBlockOpen || state.textBlockOpen || state.toolUseBlockOpen {
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
