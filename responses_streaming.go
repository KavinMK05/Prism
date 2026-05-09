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

func (p *Proxy) handleResponsesAPIOpenAIStreaming(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest) {
	reqStart := time.Now()

	chatReq := translateResponsesAPIToChatCompletions(respReq)

	body, err := json.Marshal(chatReq)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s (responses streaming)", req.Method, p.upstreamURL+"/v1/chat/completions")

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	outputIndex := -1 // Will be incremented as items are added
	contentIndex := 0
	var messageItemID string
	var textContentPartID string
	var funcCallItemID string
	var funcCallCallID string
	var funcCallName string
	var accumulatedText string
	var accumulatedArgs string
	var outputTokens int
	var inputTokens int
	var liveOutputTokens int
	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(respReq.Model, p.providerType, client, inputTokens, outputTokens, time.Since(reqStart))
	}()
	var completedEmitted bool
	var reasoningItemID string
	var reasoningActive bool
	var reasoningSummaryIndex int
	var reasoningSummaryPartAdded bool
	var accumulatedReasoning string
	var completedOutput []interface{} // accumulated output items for response.completed
	var completedOutputText string   // accumulated output text for response.completed

	// Emit response.created
	emitResponsesEvent(w, flusher, canFlush, "response.created", map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      respReq.Model,
			"status":     "in_progress",
			"output":     []interface{}{},
			"usage":      map[string]interface{}{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
		},
	})

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

		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Printf("[WARN] Failed to parse OpenAI chunk: %v", err)
			continue
		}

		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				inputTokens = chunk.Usage.PromptTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				outputTokens = chunk.Usage.CompletionTokens
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		currentChunkHasThinking := choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != ""

		// Handle reasoning content
		if currentChunkHasThinking {
			if !reasoningActive {
				reasoningActive = true
				outputIndex++
				reasoningItemID = generateID("rs_")
				reasoningSummaryIndex = 0
				reasoningSummaryPartAdded = false
				accumulatedReasoning = ""
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":      reasoningItemID,
						"type":    "reasoning",
						"summary": []interface{}{},
					},
				})
			}

			// Add summary part on first reasoning chunk
			if !reasoningSummaryPartAdded {
				reasoningSummaryPartAdded = true
				emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.added", map[string]interface{}{
					"type":          "response.reasoning_summary_part.added",
					"output_index":  outputIndex,
					"summary_index": reasoningSummaryIndex,
					"part": map[string]interface{}{
						"type": "summary_text",
						"text": "",
					},
				})
			}

			// Stream the reasoning text as a summary delta
			liveOutputTokens++
			globalStats.AddTokens(1)
			accumulatedReasoning += *choice.Delta.ReasoningContent
			emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_text.delta", map[string]interface{}{
				"type":          "response.reasoning_summary_text.delta",
				"output_index":  outputIndex,
				"summary_index": reasoningSummaryIndex,
				"delta":         *choice.Delta.ReasoningContent,
			})
		}

		if !currentChunkHasThinking && reasoningActive {
			reasoningActive = false

			// Close summary part if it was opened
			if reasoningSummaryPartAdded {
				emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.done", map[string]interface{}{
					"type":          "response.reasoning_summary_part.done",
					"output_index":  outputIndex,
					"summary_index": reasoningSummaryIndex,
					"part": map[string]interface{}{
						"type": "summary_text",
						"text": accumulatedReasoning,
					},
				})
				reasoningSummaryPartAdded = false
			}

			// Close reasoning item
			reasoningItem := map[string]interface{}{
				"id":      reasoningItemID,
				"type":    "reasoning",
				"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": accumulatedReasoning}},
			}
			emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         reasoningItem,
			})
			completedOutput = append(completedOutput, reasoningItem)
			reasoningItemID = ""
		}

		// Handle tool calls - if tool calls arrive, close current message first
		if len(delta.ToolCalls) > 0 {
			// Close text content part if active
			if textContentPartID != "" {
				emitResponsesEvent(w, flusher, canFlush, "response.output_text.done", map[string]interface{}{
					"type":          "response.output_text.done",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"text":          accumulatedText,
				})
				emitResponsesEvent(w, flusher, canFlush, "response.content_part.done", map[string]interface{}{
					"type":          "response.content_part.done",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": map[string]interface{}{
						"type": "output_text",
						"text": accumulatedText,
					},
				})
				textContentPartID = ""
			}

			// Close message item if active
			if messageItemID != "" {
				msgItem := map[string]interface{}{
					"id":      messageItemID,
					"type":    "message",
					"status":  "completed",
					"role":    "assistant",
					"content": []interface{}{map[string]interface{}{"type": "output_text", "text": accumulatedText}},
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         msgItem,
				})
				completedOutput = append(completedOutput, msgItem)
				messageItemID = ""
			}

			for _, tc := range delta.ToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					// Close previous function call if any
					if funcCallItemID != "" {
						emitResponsesEvent(w, flusher, canFlush, "response.function_call_arguments.done", map[string]interface{}{
							"type":         "response.function_call_arguments.done",
							"output_index": outputIndex,
							"call_id":      funcCallCallID,
							"arguments":    accumulatedArgs,
						})
						fcItem := map[string]interface{}{
							"id":        funcCallItemID,
							"type":      "function_call",
							"call_id":   funcCallCallID,
							"name":      funcCallName,
							"arguments": accumulatedArgs,
							"status":    "completed",
						}
						emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
							"type":         "response.output_item.done",
							"output_index": outputIndex,
							"item":         fcItem,
						})
						completedOutput = append(completedOutput, fcItem)
					}

					outputIndex++
					funcCallItemID = generateID("fc_")
					funcCallCallID = tc.ID
					funcCallName = tc.Function.Name
					accumulatedArgs = ""
					emitResponsesEvent(w, flusher, canFlush, "response.output_item.added", map[string]interface{}{
						"type":         "response.output_item.added",
						"output_index": outputIndex,
						"item": map[string]interface{}{
							"id":        funcCallItemID,
							"type":      "function_call",
							"call_id":   tc.ID,
							"name":      tc.Function.Name,
							"arguments": "",
							"status":    "in_progress",
						},
					})
				}

				if tc.Function.Arguments != "" {
					accumulatedArgs += tc.Function.Arguments
					liveOutputTokens++
					globalStats.AddTokens(1)
					if tc.Function.Arguments != "{}" {
						emitResponsesEvent(w, flusher, canFlush, "response.function_call_arguments.delta", map[string]interface{}{
							"type":         "response.function_call_arguments.delta",
							"output_index": outputIndex,
							"call_id":      funcCallCallID,
							"delta":        tc.Function.Arguments,
						})
					}
				}
			}
		}

		// Handle content/text
		if delta.Content != nil && *delta.Content != "" {
			// Start message item if not already started
			if messageItemID == "" {
				outputIndex++
				messageItemID = generateID("msg_")
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":     messageItemID,
						"type":   "message",
						"status": "in_progress",
						"role":   "assistant",
					},
				})
				// Start content part
				contentIndex = 0
				textContentPartID = generateID("cont_")
				emitResponsesEvent(w, flusher, canFlush, "response.content_part.added", map[string]interface{}{
					"type":          "response.content_part.added",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": map[string]interface{}{
						"type": "output_text",
						"text": "",
					},
				})
			}

			accumulatedText += *delta.Content
			completedOutputText += *delta.Content
			liveOutputTokens++
			globalStats.AddTokens(1)
			emitResponsesEvent(w, flusher, canFlush, "response.output_text.delta", map[string]interface{}{
				"type":          "response.output_text.delta",
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"delta":         *delta.Content,
			})
		}

		// Handle finish reason
		if choice.FinishReason != nil {
			status := "completed"
			if *choice.FinishReason == "length" {
				status = "incomplete"
			}

			// Close text content part if active
			if textContentPartID != "" {
				emitResponsesEvent(w, flusher, canFlush, "response.output_text.done", map[string]interface{}{
					"type":          "response.output_text.done",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"text":          accumulatedText,
				})
				emitResponsesEvent(w, flusher, canFlush, "response.content_part.done", map[string]interface{}{
					"type":          "response.content_part.done",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": map[string]interface{}{
						"type": "output_text",
						"text": accumulatedText,
					},
				})
				textContentPartID = ""
			}

			// Close reasoning if active
			if reasoningActive {
				reasoningActive = false

				// Close summary part if open
				if reasoningSummaryPartAdded {
					emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.done", map[string]interface{}{
						"type":          "response.reasoning_summary_part.done",
						"output_index":  outputIndex,
						"summary_index": reasoningSummaryIndex,
						"part": map[string]interface{}{
							"type": "summary_text",
							"text": accumulatedReasoning,
						},
					})
					reasoningSummaryPartAdded = false
				}

				if reasoningItemID != "" {
					reasoningItem := map[string]interface{}{
						"id":      reasoningItemID,
						"type":    "reasoning",
						"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": accumulatedReasoning}},
					}
					emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
						"type":         "response.output_item.done",
						"output_index": outputIndex,
						"item":         reasoningItem,
					})
					completedOutput = append(completedOutput, reasoningItem)
				}
				reasoningItemID = ""
			}

			// Close message item if active
			if messageItemID != "" {
				msgItem := map[string]interface{}{
					"id":      messageItemID,
					"type":    "message",
					"status":  status,
					"role":    "assistant",
					"content": []interface{}{map[string]interface{}{"type": "output_text", "text": accumulatedText}},
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         msgItem,
				})
				completedOutput = append(completedOutput, msgItem)
				messageItemID = ""
			}

			// Close function call if active
			if funcCallItemID != "" {
				emitResponsesEvent(w, flusher, canFlush, "response.function_call_arguments.done", map[string]interface{}{
					"type":         "response.function_call_arguments.done",
					"output_index": outputIndex,
					"call_id":      funcCallCallID,
					"arguments":    accumulatedArgs,
				})
				fcItem := map[string]interface{}{
					"id":        funcCallItemID,
					"type":      "function_call",
					"call_id":   funcCallCallID,
					"name":      funcCallName,
					"arguments": accumulatedArgs,
					"status":    "completed",
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         fcItem,
				})
				completedOutput = append(completedOutput, fcItem)
				funcCallItemID = ""
			}

			// Emit completed with accumulated output
			completedResp := map[string]interface{}{
				"id":          respID,
				"object":      "response",
				"created_at":  createdAt,
				"model":       respReq.Model,
				"status":      status,
				"output":      completedOutput,
				"output_text": completedOutputText,
				"usage": map[string]interface{}{
					"input_tokens":  inputTokens,
					"output_tokens": outputTokens,
					"total_tokens":  inputTokens + outputTokens,
				},
			}
			emitResponsesEvent(w, flusher, canFlush, "response.completed", map[string]interface{}{
				"type":     "response.completed",
				"response": completedResp,
			})
			completedEmitted = true
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
	}

	// Final cleanup if stream ended without finish_reason and not yet completed
	if !completedEmitted {
		// Close text content part if active
		if textContentPartID != "" {
			emitResponsesEvent(w, flusher, canFlush, "response.output_text.done", map[string]interface{}{
				"type":          "response.output_text.done",
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"text":          accumulatedText,
			})
			emitResponsesEvent(w, flusher, canFlush, "response.content_part.done", map[string]interface{}{
				"type":          "response.content_part.done",
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"part": map[string]interface{}{
					"type": "output_text",
					"text": accumulatedText,
				},
			})
		}

		if messageItemID != "" {
			msgItem := map[string]interface{}{
				"id":      messageItemID,
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": []interface{}{map[string]interface{}{"type": "output_text", "text": accumulatedText}},
			}
			emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         msgItem,
			})
			completedOutput = append(completedOutput, msgItem)
		}

		if funcCallItemID != "" {
			fcItem := map[string]interface{}{
				"id":        funcCallItemID,
				"type":      "function_call",
				"call_id":   funcCallCallID,
				"name":      funcCallName,
				"arguments": accumulatedArgs,
				"status":    "completed",
			}
			emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         fcItem,
			})
			completedOutput = append(completedOutput, fcItem)
		}

		// Close reasoning if still active
		if reasoningActive {
			reasoningActive = false

			// Close summary part if open
			if reasoningSummaryPartAdded {
				emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.done", map[string]interface{}{
					"type":          "response.reasoning_summary_part.done",
					"output_index":  outputIndex,
					"summary_index": reasoningSummaryIndex,
					"part": map[string]interface{}{
						"type": "summary_text",
						"text": accumulatedReasoning,
					},
				})
				reasoningSummaryPartAdded = false
			}

			if reasoningItemID != "" {
				reasoningItem := map[string]interface{}{
					"id":      reasoningItemID,
					"type":    "reasoning",
					"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": accumulatedReasoning}},
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         reasoningItem,
				})
				completedOutput = append(completedOutput, reasoningItem)
			}
			reasoningItemID = ""
		}

		emitResponsesEvent(w, flusher, canFlush, "response.completed", map[string]interface{}{
			"type": "response.completed",
			"response": map[string]interface{}{
				"id":          respID,
				"object":      "response",
				"created_at":  createdAt,
				"model":       respReq.Model,
				"status":      "completed",
				"output":      completedOutput,
				"output_text": completedOutputText,
				"usage": map[string]interface{}{
					"input_tokens":  inputTokens,
					"output_tokens": outputTokens,
					"total_tokens":  inputTokens + outputTokens,
				},
			},
		})
	}
}

func (p *Proxy) handleResponsesAPIOllamaStreaming(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest) {
	reqStart := time.Now()

	ollamaReq := translateResponsesAPIToOllama(respReq)

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal Ollama request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s (responses streaming)", req.Method, p.upstreamURL+"/api/chat")

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	outputIndex := -1
	contentIndex := 0
	var messageItemID string
	var contentPartID string
	var funcCallItemID string
	var funcCallCallID string
	var funcCallName string
	var outputTokens int
	var inputTokens int
	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(respReq.Model, p.providerType, client, inputTokens, outputTokens, time.Since(reqStart))
	}()
	var accumulatedText string
	var thinkingActive bool
	var thinkingSummaryIndex int
	var thinkingSummaryPartAdded bool
	var accumulatedReasoning string
	var reasoningItemID string
	var completedEmitted bool
	var toolCallsEmitted int
	var completedOutput []interface{} // accumulated output items for response.completed
	var completedOutputText string   // accumulated output text for response.completed

	// Emit response.created
	emitResponsesEvent(w, flusher, canFlush, "response.created", map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      respReq.Model,
			"status":     "in_progress",
			"output":     []interface{}{},
			"usage":      map[string]interface{}{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
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
			inputTokens = chunk.PromptEvalCount
		}
		if chunk.EvalCount > outputTokens {
			outputTokens = chunk.EvalCount
		}

		// Handle thinking content
		if chunk.Message.Thinking != "" {
			globalStats.AddTokens(1)
			if !thinkingActive {
				thinkingActive = true
				outputIndex++
				reasoningItemID = generateID("rs_")
				thinkingSummaryIndex = 0
				thinkingSummaryPartAdded = false
				accumulatedReasoning = ""
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":      reasoningItemID,
						"type":    "reasoning",
						"summary": []interface{}{},
					},
				})
			}

			// Add summary part on first thinking chunk
			if !thinkingSummaryPartAdded {
				thinkingSummaryPartAdded = true
				emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.added", map[string]interface{}{
					"type":          "response.reasoning_summary_part.added",
					"output_index":  outputIndex,
					"summary_index": thinkingSummaryIndex,
					"part": map[string]interface{}{
						"type": "summary_text",
						"text": "",
					},
				})
			}

			// Stream the thinking text as a summary delta
			accumulatedReasoning += chunk.Message.Thinking
			emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_text.delta", map[string]interface{}{
				"type":          "response.reasoning_summary_text.delta",
				"output_index":  outputIndex,
				"summary_index": thinkingSummaryIndex,
				"delta":         chunk.Message.Thinking,
			})
		}

		hasNonThinkingContent := chunk.Message.Content != "" || len(chunk.Message.ToolCalls) > 0

		if hasNonThinkingContent && thinkingActive {
			thinkingActive = false

			// Close summary part if open
			if thinkingSummaryPartAdded {
				emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.done", map[string]interface{}{
					"type":          "response.reasoning_summary_part.done",
					"output_index":  outputIndex,
					"summary_index": thinkingSummaryIndex,
					"part": map[string]interface{}{
						"type": "summary_text",
						"text": accumulatedReasoning,
					},
				})
				thinkingSummaryPartAdded = false
			}

			// Close reasoning item
			if reasoningItemID != "" {
				reasoningItem := map[string]interface{}{
					"id":      reasoningItemID,
					"type":    "reasoning",
					"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": accumulatedReasoning}},
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         reasoningItem,
				})
				completedOutput = append(completedOutput, reasoningItem)
			}
			reasoningItemID = ""
		}

		// Handle tool calls
		if len(chunk.Message.ToolCalls) > 0 {
			// Close message item if active
			if messageItemID != "" {
				if contentPartID != "" {
					emitResponsesEvent(w, flusher, canFlush, "response.output_text.done", map[string]interface{}{
						"type":          "response.output_text.done",
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"text":          accumulatedText,
					})
					emitResponsesEvent(w, flusher, canFlush, "response.content_part.done", map[string]interface{}{
						"type":          "response.content_part.done",
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"part": map[string]interface{}{
							"type": "output_text",
							"text": accumulatedText,
						},
					})
					contentPartID = ""
				}
				msgItem := map[string]interface{}{
					"id":      messageItemID,
					"type":    "message",
					"status":  "completed",
					"role":    "assistant",
					"content": []interface{}{map[string]interface{}{"type": "output_text", "text": accumulatedText}},
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         msgItem,
				})
				completedOutput = append(completedOutput, msgItem)
				messageItemID = ""
			}

			for i, tc := range chunk.Message.ToolCalls {
				toolName := tc.Function.Name
				if toolName == "" {
					continue
				}
				if i < toolCallsEmitted {
					continue
				}

				globalStats.AddTokens(1)

				outputIndex++
				funcCallItemID = generateID("fc_")
				funcCallCallID = fmt.Sprintf("call_%s_%s", toolName, funcCallItemID)
				funcCallName = toolName

				argsJSON, _ := json.Marshal(tc.Function.Arguments)

				emitResponsesEvent(w, flusher, canFlush, "response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":        funcCallItemID,
						"type":      "function_call",
						"call_id":   funcCallCallID,
						"name":      toolName,
						"arguments": string(argsJSON),
						"status":    "completed",
					},
				})

				emitResponsesEvent(w, flusher, canFlush, "response.function_call_arguments.done", map[string]interface{}{
					"type":         "response.function_call_arguments.done",
					"output_index": outputIndex,
					"call_id":      funcCallCallID,
					"arguments":    string(argsJSON),
				})

				fcItem := map[string]interface{}{
					"id":        funcCallItemID,
					"type":      "function_call",
					"call_id":   funcCallCallID,
					"name":      toolName,
					"arguments": string(argsJSON),
					"status":    "completed",
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         fcItem,
				})
				completedOutput = append(completedOutput, fcItem)
				toolCallsEmitted++
			}
		}

		// Handle content
		if chunk.Message.Content != "" {
			if messageItemID == "" {
				outputIndex++
				messageItemID = generateID("msg_")
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":     messageItemID,
						"type":   "message",
						"status": "in_progress",
						"role":   "assistant",
					},
				})
				contentIndex = 0
				contentPartID = generateID("cont_")
				emitResponsesEvent(w, flusher, canFlush, "response.content_part.added", map[string]interface{}{
					"type":          "response.content_part.added",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": map[string]interface{}{
						"type": "output_text",
						"text": "",
					},
				})
			}

			accumulatedText += chunk.Message.Content
			completedOutputText += chunk.Message.Content
			globalStats.AddTokens(1)
			emitResponsesEvent(w, flusher, canFlush, "response.output_text.delta", map[string]interface{}{
				"type":          "response.output_text.delta",
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"delta":         chunk.Message.Content,
			})
		}

		// Handle done
		if chunk.Done {
			// Close thinking if active
			if thinkingActive {
				thinkingActive = false

				// Close summary part if open
				if thinkingSummaryPartAdded {
					emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.done", map[string]interface{}{
						"type":          "response.reasoning_summary_part.done",
						"output_index":  outputIndex,
						"summary_index": thinkingSummaryIndex,
						"part": map[string]interface{}{
							"type": "summary_text",
							"text": accumulatedReasoning,
						},
					})
					thinkingSummaryPartAdded = false
				}

				if reasoningItemID != "" {
					reasoningItem := map[string]interface{}{
						"id":      reasoningItemID,
						"type":    "reasoning",
						"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": accumulatedReasoning}},
					}
					emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
						"type":         "response.output_item.done",
						"output_index": outputIndex,
						"item":         reasoningItem,
					})
					completedOutput = append(completedOutput, reasoningItem)
				}
				reasoningItemID = ""
			}

			// Close message item if active
			if messageItemID != "" {
				if contentPartID != "" {
					emitResponsesEvent(w, flusher, canFlush, "response.output_text.done", map[string]interface{}{
						"type":          "response.output_text.done",
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"text":          accumulatedText,
					})
					emitResponsesEvent(w, flusher, canFlush, "response.content_part.done", map[string]interface{}{
						"type":          "response.content_part.done",
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"part": map[string]interface{}{
							"type": "output_text",
							"text": accumulatedText,
						},
					})
					contentPartID = ""
				}

				status := "completed"
				if chunk.DoneReason == "length" {
					status = "incomplete"
				}

				msgItem := map[string]interface{}{
					"id":      messageItemID,
					"type":    "message",
					"status":  status,
					"role":    "assistant",
					"content": []interface{}{map[string]interface{}{"type": "output_text", "text": accumulatedText}},
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         msgItem,
				})
				completedOutput = append(completedOutput, msgItem)
				messageItemID = ""
			}

			// Close function call if active
			if funcCallItemID != "" {
				fcItem := map[string]interface{}{
					"id":        funcCallItemID,
					"type":      "function_call",
					"call_id":   funcCallCallID,
					"name":      funcCallName,
					"arguments": "{}",
					"status":    "completed",
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         fcItem,
				})
				completedOutput = append(completedOutput, fcItem)
				funcCallItemID = ""
			}

			// Emit completed with accumulated output
			status := "completed"
			if chunk.DoneReason == "length" {
				status = "incomplete"
			}

			completedResp := map[string]interface{}{
				"id":          respID,
				"object":      "response",
				"created_at":  createdAt,
				"model":       respReq.Model,
				"status":      status,
				"output":      completedOutput,
				"output_text": completedOutputText,
				"usage": map[string]interface{}{
					"input_tokens":  inputTokens,
					"output_tokens": outputTokens,
					"total_tokens":  inputTokens + outputTokens,
				},
			}
			emitResponsesEvent(w, flusher, canFlush, "response.completed", map[string]interface{}{
				"type":     "response.completed",
				"response": completedResp,
			})
			completedEmitted = true
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
	}

	// Final cleanup if stream ended without done
	if !completedEmitted {
		if thinkingActive {
			thinkingActive = false

			// Close summary part if open
			if thinkingSummaryPartAdded {
				emitResponsesEvent(w, flusher, canFlush, "response.reasoning_summary_part.done", map[string]interface{}{
					"type":          "response.reasoning_summary_part.done",
					"output_index":  outputIndex,
					"summary_index": thinkingSummaryIndex,
					"part": map[string]interface{}{
						"type": "summary_text",
						"text": accumulatedReasoning,
					},
				})
				thinkingSummaryPartAdded = false
			}

			if reasoningItemID != "" {
				reasoningItem := map[string]interface{}{
					"id":      reasoningItemID,
					"type":    "reasoning",
					"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": accumulatedReasoning}},
				}
				emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         reasoningItem,
				})
				completedOutput = append(completedOutput, reasoningItem)
			}
			reasoningItemID = ""
		}

		if messageItemID != "" {
			if contentPartID != "" {
				emitResponsesEvent(w, flusher, canFlush, "response.output_text.done", map[string]interface{}{
					"type":          "response.output_text.done",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"text":          accumulatedText,
				})
				emitResponsesEvent(w, flusher, canFlush, "response.content_part.done", map[string]interface{}{
					"type":          "response.content_part.done",
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": map[string]interface{}{
						"type": "output_text",
						"text": accumulatedText,
					},
				})
			}

			msgItem := map[string]interface{}{
				"id":      messageItemID,
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": []interface{}{map[string]interface{}{"type": "output_text", "text": accumulatedText}},
			}
			emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         msgItem,
			})
			completedOutput = append(completedOutput, msgItem)
		}

		if funcCallItemID != "" {
			fcItem := map[string]interface{}{
				"id":        funcCallItemID,
				"type":      "function_call",
				"call_id":   funcCallCallID,
				"name":      funcCallName,
				"arguments": "{}",
				"status":    "completed",
			}
			emitResponsesEvent(w, flusher, canFlush, "response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         fcItem,
			})
			completedOutput = append(completedOutput, fcItem)
		}

		emitResponsesEvent(w, flusher, canFlush, "response.completed", map[string]interface{}{
			"type": "response.completed",
			"response": map[string]interface{}{
				"id":          respID,
				"object":      "response",
				"created_at":  createdAt,
				"model":       respReq.Model,
				"status":      "completed",
				"output":      completedOutput,
				"output_text": completedOutputText,
				"usage": map[string]interface{}{
					"input_tokens":  inputTokens,
					"output_tokens": outputTokens,
					"total_tokens":  inputTokens + outputTokens,
				},
			},
		})
	}
}

func emitResponsesEvent(w http.ResponseWriter, flusher http.Flusher, canFlush bool, event string, data interface{}) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		log.Printf("[WARN] Failed to marshal responses event: %v", err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, dataJSON)
	if canFlush {
		flusher.Flush()
	}
}
