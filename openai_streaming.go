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
)

func (p *Proxy) handleOpenAIStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, anthroReq *AnthropicRequest) {
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
	contentBlockIndex := 0
	textBlockOpen := false
	toolUseBlocksOpen := map[int]bool{}
	thinkingBlockOpen := false
	hasContentBlock := false

	writeSSE(w, flusher, canFlush, "message_start", map[string]interface{}{
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

	outputTokens := 0

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
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				inputTokens := chunk.Usage.PromptTokens
				_ = inputTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				outputTokens = chunk.Usage.CompletionTokens
			}
		}

		if choice.Delta.Role == "assistant" && len(choice.Delta.ToolCalls) > 0 {
			for _, tc := range choice.Delta.ToolCalls {
				if !toolUseBlocksOpen[contentBlockIndex] {
					argsStr := ""
					if tc.Function.Arguments != "" {
						argsStr = tc.Function.Arguments
					}
					var args map[string]interface{}
					if argsStr != "" {
						json.Unmarshal([]byte(argsStr), &args)
					}
					if args == nil {
						args = map[string]interface{}{}
					}
					writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
						"type":  "content_block_start",
						"index": contentBlockIndex,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    tc.ID,
							"name":  tc.Function.Name,
							"input": map[string]interface{}{},
						},
					})
					toolUseBlocksOpen[contentBlockIndex] = true
					hasContentBlock = true

					if tc.Function.Arguments != "" && tc.Function.Arguments != "{}" {
						writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
							"type":  "content_block_delta",
							"index": contentBlockIndex,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": tc.Function.Arguments,
							},
						})
					}
				} else {
					if tc.Function.Arguments != "" {
						writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
							"type":  "content_block_delta",
							"index": contentBlockIndex,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": tc.Function.Arguments,
							},
						})
					}
				}
			}
		} else if len(choice.Delta.ToolCalls) > 0 {
			for _, tc := range choice.Delta.ToolCalls {
				idx := contentBlockIndex
				if tc.ID != "" {
					if toolUseBlocksOpen[idx] {
						writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
							"type":  "content_block_stop",
							"index": idx,
						})
						delete(toolUseBlocksOpen, idx)
						contentBlockIndex++
						idx = contentBlockIndex
					}

					var args map[string]interface{}
					if tc.Function.Arguments != "" {
						json.Unmarshal([]byte(tc.Function.Arguments), &args)
					}
					if args == nil {
						args = map[string]interface{}{}
					}
					writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    tc.ID,
							"name":  tc.Function.Name,
							"input": map[string]interface{}{},
						},
					})
					toolUseBlocksOpen[idx] = true
					hasContentBlock = true

					if tc.Function.Arguments != "" && tc.Function.Arguments != "{}" {
						writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
							"type":  "content_block_delta",
							"index": idx,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": tc.Function.Arguments,
							},
						})
					}
				} else if tc.Function.Arguments != "" {
					for openIdx := range toolUseBlocksOpen {
						writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
							"type":  "content_block_delta",
							"index": openIdx,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": tc.Function.Arguments,
							},
						})
					}
				}
			}
		}

		if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
			if !thinkingBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]interface{}{
						"type":     "thinking",
						"thinking": "",
					},
				})
				thinkingBlockOpen = true
				hasContentBlock = true
			}
			writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": contentBlockIndex,
				"delta": map[string]interface{}{
					"type":     "thinking_delta",
					"thinking": *choice.Delta.ReasoningContent,
				},
			})
		}

		if (choice.Delta.ReasoningContent == nil || *choice.Delta.ReasoningContent == "") && thinkingBlockOpen {
			writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
				"type":  "content_block_stop",
				"index": contentBlockIndex,
			})
			contentBlockIndex++
			thinkingBlockOpen = false
		}

		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			if !textBlockOpen && !thinkingBlockOpen && len(toolUseBlocksOpen) == 0 {
				writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				})
				textBlockOpen = true
				hasContentBlock = true
			}
			if textBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": contentBlockIndex,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": *choice.Delta.Content,
					},
				})
			}
		}

		if choice.FinishReason != nil {
			stopReason := "end_turn"
			switch *choice.FinishReason {
			case "length":
				stopReason = "max_tokens"
			case "tool_calls", "function_call":
				stopReason = "tool_use"
			}

			if thinkingBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex,
				})
				contentBlockIndex++
				thinkingBlockOpen = false
			}

			if textBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex,
				})
				contentBlockIndex++
				textBlockOpen = false
			}

			for openIdx := range toolUseBlocksOpen {
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": openIdx,
				})
				delete(toolUseBlocksOpen, openIdx)
				contentBlockIndex++
			}

			if !hasContentBlock {
				writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				})
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex,
				})
				contentBlockIndex++
				hasContentBlock = true
			}

			writeSSE(w, flusher, canFlush, "message_delta", map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]interface{}{
					"output_tokens": outputTokens,
				},
			})

			writeSSE(w, flusher, canFlush, "message_stop", map[string]interface{}{
				"type": "message_stop",
			})
		}
	}

	if thinkingBlockOpen {
		writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": contentBlockIndex,
		})
		contentBlockIndex++
		thinkingBlockOpen = false
	}

	if textBlockOpen {
		writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": contentBlockIndex,
		})
		contentBlockIndex++
		textBlockOpen = false
	}

	if !hasContentBlock {
		writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": contentBlockIndex,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		})
		writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": contentBlockIndex,
		})
	}
}