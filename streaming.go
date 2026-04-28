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

	log.Printf("→ %s %s (streaming)", upstreamReq.Method, p.upstreamURL+"/api/chat")

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		log.Printf("✗ Upstream request failed: %v", err)
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Upstream request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	log.Printf("← %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("✗ Upstream error response: %s", string(respBody))
		writeAnthropicError(w, resp.StatusCode, "api_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	msgID := fmt.Sprintf("msg_%s", anthroReq.Model)
	contentBlockIndex := 0
	totalOutputTokens := 0
	hasContentBlock := false
	thinkingBlockOpen := false
	textBlockOpen := false
	toolUseBlockOpen := false
	pendingText := ""

	writeSSE(w, flusher, canFlush, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":       msgID,
			"type":     "message",
			"role":     "assistant",
			"model":    anthroReq.Model,
			"content":  []interface{}{},
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
			continue
		}

		if chunk.PromptEvalCount > 0 && chunk.Done {
			totalOutputTokens = chunk.EvalCount
		}

		if chunk.Message.Thinking != "" {
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
					"thinking": chunk.Message.Thinking,
				},
			})
		}

		if chunk.Message.Thinking == "" && thinkingBlockOpen {
			writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
				"type":  "content_block_stop",
				"index": contentBlockIndex,
			})
			contentBlockIndex++
			thinkingBlockOpen = false

			if pendingText != "" {
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
				writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": contentBlockIndex,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": pendingText,
					},
				})
				pendingText = ""
			}
		}

		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
			if !toolUseBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]interface{}{
						"type":  "tool_use",
						"id":    fmt.Sprintf("toolu_%s", tc.Function.Name),
						"name":  tc.Function.Name,
						"input": map[string]interface{}{},
					},
				})
				toolUseBlockOpen = true
				hasContentBlock = true
				}
				if tc.Function.Arguments != nil {
					argsJSON, _ := json.Marshal(tc.Function.Arguments)
					writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": contentBlockIndex,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": string(argsJSON),
						},
					})
				}
			}
		}

		if chunk.Message.Content != "" {
			if thinkingBlockOpen {
				pendingText += chunk.Message.Content
			} else {
				if !textBlockOpen && !toolUseBlockOpen {
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
							"text": chunk.Message.Content,
						},
					})
				}
			}
		}

		if chunk.Done {
			if pendingText != "" && !textBlockOpen {
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
				writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": contentBlockIndex,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": pendingText,
					},
				})
				pendingText = ""
			}

			if textBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex,
				})
				contentBlockIndex++
				textBlockOpen = false
			}
			if toolUseBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex,
				})
				contentBlockIndex++
				toolUseBlockOpen = false
			}

			stopReason := "end_turn"
			switch chunk.DoneReason {
			case "length":
				stopReason = "max_tokens"
			case "tool_call":
				stopReason = "tool_use"
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
					"output_tokens": totalOutputTokens,
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

	if pendingText != "" && !textBlockOpen {
		writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": contentBlockIndex,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		})
		writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": contentBlockIndex,
			"delta": map[string]interface{}{
				"type": "text_delta",
				"text": pendingText,
			},
		})
		writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": contentBlockIndex,
		})
		contentBlockIndex++
		hasContentBlock = true
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