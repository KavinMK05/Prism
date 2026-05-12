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

func (p *Proxy) handleOpenAIStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, anthroReq *AnthropicRequest) {
	reqStart := time.Now()
	outputTokens := 0
	inputTokens := 0
	liveOutputTokens := 0
	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(anthroReq.Model, p.providerType, client, inputTokens, outputTokens, time.Since(reqStart))
	}()

	// Inject stream_options to get usage data from the upstream provider
	openAIReq.StreamOptions = &OpenAIStreamOptions{IncludeUsage: true}

	body, err := json.Marshal(openAIReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal OpenAI request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s (streaming)", req.Method, p.upstreamURL+"/v1/chat/completions")

	resp, err := p.client.Do(req)
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
			"id":         msgID,
			"type":       "message",
			"role":       "assistant",
			"model":      anthroReq.Model,
			"content":    []interface{}{},
			"stop_reason": nil,
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	prevChunkHasThinking := false

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

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				inputTokens = chunk.Usage.PromptTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				outputTokens = chunk.Usage.CompletionTokens
			}
		}

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
					state.openToolUseBlock(tc.Function.Name)
				}

				if tc.Function.Arguments != "" && tc.Function.Arguments != "{}" {
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
			if !state.textBlockOpen && !state.thinkingBlockOpen && !state.toolUseBlockOpen {
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

			stopReason := "end_turn"
			switch *choice.FinishReason {
			case "length":
				stopReason = "max_tokens"
			case "tool_calls", "function_call":
				stopReason = "tool_use"
			}

			state.sendEmptyTextBlock()
			state.sendStopReason(stopReason, outputTokens)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
	}

	// If the stream ended without a FinishReason, close any remaining blocks
	// and send an empty text block as fallback.
	if state.thinkingBlockOpen || state.textBlockOpen || state.toolUseBlockOpen {
		state.closeAllBlocks()
	}
	if !state.hasContentBlock {
		state.sendEmptyTextBlock()
	}
}