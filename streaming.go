package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

type streamState struct {
	w                  http.ResponseWriter
	flusher            http.Flusher
	canFlush           bool
	contentBlockIndex  int
	hasContentBlock    bool
	thinkingBlockOpen  bool
	textBlockOpen      bool
	toolUseBlockOpen   bool
	toolCallIndex      int
	totalOutputTokens  int
	totalPromptTokens  int
	msgID              string
}

func newStreamState(w http.ResponseWriter, flusher http.Flusher, canFlush bool, msgID string) *streamState {
	return &streamState{
		w:         w,
		flusher:   flusher,
		canFlush:  canFlush,
		msgID:     msgID,
	}
}

func (s *streamState) writeSSE(event string, data interface{}) {
	writeSSE(s.w, s.flusher, s.canFlush, event, data)
}

func (s *streamState) closeBlock(blockType string) {
	if !s.hasOpenBlock() {
		return
	}
	log.Printf("[STREAM] Closing %s block at index %d", blockType, s.contentBlockIndex)
	s.writeSSE("content_block_stop", map[string]interface{}{
		"type":  "content_block_stop",
		"index": s.contentBlockIndex,
	})
	s.contentBlockIndex++
	s.thinkingBlockOpen = false
	s.textBlockOpen = false
	s.toolUseBlockOpen = false
}

func (s *streamState) hasOpenBlock() bool {
	return s.thinkingBlockOpen || s.textBlockOpen || s.toolUseBlockOpen
}

func (s *streamState) openThinkingBlock() {
	if s.hasOpenBlock() {
		s.closeBlock("thinking")
	}
	log.Printf("[STREAM] Opening thinking block at index %d", s.contentBlockIndex)
	s.writeSSE("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": s.contentBlockIndex,
		"content_block": map[string]interface{}{
			"type":     "thinking",
			"thinking": "",
		},
	})
	s.thinkingBlockOpen = true
	s.hasContentBlock = true
}

func (s *streamState) openTextBlock() {
	if s.hasOpenBlock() {
		s.closeBlock("text")
	}
	log.Printf("[STREAM] Opening text block at index %d", s.contentBlockIndex)
	s.writeSSE("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": s.contentBlockIndex,
		"content_block": map[string]interface{}{
			"type": "text",
			"text": "",
		},
	})
	s.textBlockOpen = true
	s.hasContentBlock = true
}

func (s *streamState) openToolUseBlock(toolName string) {
	if s.hasOpenBlock() {
		s.closeBlock("tool_use")
	}
	log.Printf("[STREAM] Opening tool_use block at index %d", s.contentBlockIndex)
	s.writeSSE("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": s.contentBlockIndex,
		"content_block": map[string]interface{}{
			"type":  "tool_use",
			"id":    fmt.Sprintf("toolu_%s_%d", toolName, s.toolCallIndex),
			"name":  toolName,
			"input": map[string]interface{}{},
		},
	})
	s.toolUseBlockOpen = true
	s.hasContentBlock = true
	s.toolCallIndex++
}

func (s *streamState) closeAllBlocks() {
	if s.thinkingBlockOpen {
		s.closeBlock("thinking")
	}
	if s.textBlockOpen {
		s.closeBlock("text")
	}
	if s.toolUseBlockOpen {
		s.closeBlock("tool_use")
	}
}

func (s *streamState) sendEmptyTextBlock() {
	if !s.hasContentBlock {
		log.Printf("[STREAM] Adding empty text block (no content)")
		s.writeSSE("content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": s.contentBlockIndex,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		})
		s.writeSSE("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": s.contentBlockIndex,
		})
		s.contentBlockIndex++
		s.hasContentBlock = true
	}
}

func (s *streamState) sendStopReason(stopReason string, outputTokens int) {
	log.Printf("[STREAM] Sending message_delta with stop_reason=%s, output_tokens=%d", stopReason, outputTokens)
	s.writeSSE("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": outputTokens,
		},
	})

	s.writeSSE("message_stop", map[string]interface{}{
		"type": "message_stop",
	})
}

func (p *Proxy) handleStreaming(w http.ResponseWriter, r *http.Request, ollamaReq *OllamaChatRequest, anthroReq *AnthropicRequest) {
	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal Ollama request")
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s (streaming)", upstreamReq.Method, p.upstreamURL+"/api/chat")

	resp, err := p.client.Do(upstreamReq)
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

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var chunk OllamaChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			log.Printf("[WARN] Failed to parse Ollama chunk: %v", err)
			continue
		}

		if chunk.PromptEvalCount > 0 {
			state.totalPromptTokens = chunk.PromptEvalCount
		}
		if chunk.Done {
			state.totalOutputTokens = chunk.EvalCount
		}

		if chunk.Message.Thinking != "" {
			if !state.thinkingBlockOpen {
				state.openThinkingBlock()
			}
			state.writeSSE("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": state.contentBlockIndex,
				"delta": map[string]interface{}{
					"type":     "thinking_delta",
					"thinking": chunk.Message.Thinking,
				},
			})
		}

		hasNonThinkingContent := chunk.Message.Content != "" || len(chunk.Message.ToolCalls) > 0

		if hasNonThinkingContent && state.thinkingBlockOpen {
			state.closeBlock("thinking")
		}

		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
				toolName := tc.Function.Name
				if toolName == "" {
					continue
				}
				if state.toolUseBlockOpen {
					state.closeBlock("tool_use")
				}
				state.openToolUseBlock(toolName)
				if tc.Function.Arguments != nil {
					argsJSON, _ := json.Marshal(tc.Function.Arguments)
					state.writeSSE("content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": state.contentBlockIndex,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": string(argsJSON),
						},
					})
				}
			}
		}

		if chunk.Message.Content != "" {
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
						"text": chunk.Message.Content,
					},
				})
			}
		}

		if chunk.Done {
			state.closeAllBlocks()

			stopReason := "end_turn"
			switch chunk.DoneReason {
			case "length":
				stopReason = "max_tokens"
			case "tool_call", "tool_calls":
				stopReason = "tool_use"
			}

			// Fallback: Ollama typically returns done_reason "stop" even when tool calls
			// are present, so check if we saw any tool use blocks during streaming.
			if stopReason != "tool_use" && state.toolCallIndex > 0 {
				stopReason = "tool_use"
			}

			state.sendEmptyTextBlock()
			state.sendStopReason(stopReason, state.totalOutputTokens)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
	}

	// If the stream ended without a Done chunk, close any remaining blocks
	// and send an empty text block as fallback.
	if state.thinkingBlockOpen || state.textBlockOpen || state.toolUseBlockOpen {
		state.closeAllBlocks()
	}
	if !state.hasContentBlock {
		state.sendEmptyTextBlock()
	}
}

func writeSSE(w io.Writer, flusher http.Flusher, canFlush bool, event string, data interface{}) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, dataJSON)
	if canFlush {
		flusher.Flush()
	}
}