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

type ollamaStreamState struct {
	w                http.ResponseWriter
	flusher          http.Flusher
	canFlush         bool
	respID           string
	model            string
	outputTokens     int
	inputTokens      int
	thinkingActive   bool
	pendingContent   string
	toolCallsActive  bool
	toolCallIndex    int
	emittedToolCalls map[string]bool // track which tool calls have been emitted by name
}

func (s *ollamaStreamState) writeOpenAISSE(chunk OpenAIStreamChunk) {
	writeOpenAISSE(s.w, s.flusher, s.canFlush, chunk)
}

func (s *ollamaStreamState) closeToolCalls() {
	if s.toolCallsActive {
		s.toolCallsActive = false
	}
}

func (p *Proxy) handleOpenAIInboundOllamaStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest) {
	reqStart := time.Now()

	ollamaReq := translateOpenAIToOllama(openAIReq)

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

	log.Printf("-> %s %s (streaming)", req.Method, p.upstreamURL+"/api/chat")

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
	respID := fmt.Sprintf("chatcmpl-%s", openAIReq.Model)

	state := &ollamaStreamState{
		w:      w,
		flusher: flusher,
		canFlush: canFlush,
		respID: respID,
		model:  openAIReq.Model,
		emittedToolCalls: make(map[string]bool),
	}

	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(openAIReq.Model, p.providerType, client, state.inputTokens, state.outputTokens, time.Since(reqStart))
	}()

	state.writeOpenAISSE(OpenAIStreamChunk{
		ID:     respID,
		Object: "chat.completion.chunk",
		Model:  openAIReq.Model,
		Choices: []OpenAIStreamChoice{
			{
				Index: 0,
				Delta: OpenAIStreamDelta{
					Role: "assistant",
				},
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
			state.inputTokens = chunk.PromptEvalCount
		}
		if chunk.EvalCount > state.outputTokens {
			state.outputTokens = chunk.EvalCount
		}

		if chunk.Message.Thinking != "" {
			if !state.thinkingActive {
				state.thinkingActive = true
			}
			globalStats.AddTokens(1)
			thinking := chunk.Message.Thinking
			state.writeOpenAISSE(OpenAIStreamChunk{
				ID:     respID,
				Object: "chat.completion.chunk",
				Model:  openAIReq.Model,
				Choices: []OpenAIStreamChoice{
					{
						Index: 0,
						Delta: OpenAIStreamDelta{
							ReasoningContent: &thinking,
						},
					},
				},
			})
		}

		hasNonThinkingContent := chunk.Message.Content != "" || len(chunk.Message.ToolCalls) > 0

		if hasNonThinkingContent && state.thinkingActive {
			state.thinkingActive = false
			// Flush any content that was buffered during thinking
			if state.pendingContent != "" {
				state.closeToolCalls()
				pending := state.pendingContent
				state.pendingContent = ""
				state.writeOpenAISSE(OpenAIStreamChunk{
					ID:     respID,
					Object: "chat.completion.chunk",
					Model:  openAIReq.Model,
					Choices: []OpenAIStreamChoice{
						{
							Index: 0,
							Delta: OpenAIStreamDelta{
								Content: &pending,
							},
						},
					},
				})
			}
		}

		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
				toolName := tc.Function.Name
				if toolName == "" {
					continue
				}

				// Use name as dedup key (supports both cumulative and non-cumulative streaming)
				// For multiple calls to the same function, append the index
				dedupKey := toolName
				if state.emittedToolCalls[dedupKey] {
					// If same name was already emitted, check with index suffix
					// (handles multiple calls to the same function)
					idx := 0
					for {
						dedupKey = fmt.Sprintf("%s_%d", toolName, idx)
						if !state.emittedToolCalls[dedupKey] {
							break
						}
						idx++
					}
				}

				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				globalStats.AddTokens(1)
				if !state.toolCallsActive {
					state.toolCallsActive = true
				}
				idx := state.toolCallIndex

				// Use Ollama's native ID if available, otherwise generate one
				toolCallID := tc.ID
				if toolCallID == "" {
					toolCallID = fmt.Sprintf("call_%s_%d", toolName, idx)
				}

				state.writeOpenAISSE(OpenAIStreamChunk{
					ID:     respID,
					Object: "chat.completion.chunk",
					Model:  openAIReq.Model,
					Choices: []OpenAIStreamChoice{
						{
							Index: 0,
							Delta: OpenAIStreamDelta{
								ToolCalls: []OpenAIToolCall{
									{
										Index: &idx,
										ID:    toolCallID,
										Type:  "function",
										Function: OpenAIToolCallFunc{
											Name:      toolName,
											Arguments: string(argsJSON),
										},
									},
								},
							},
						},
					},
				})
				state.toolCallIndex++
				state.emittedToolCalls[dedupKey] = true
			}
		}

		if chunk.Message.Content != "" {
			globalStats.AddTokens(1)
			if state.thinkingActive {
				// Buffer content until thinking ends
				state.pendingContent += chunk.Message.Content
			} else {
				state.closeToolCalls()
				content := chunk.Message.Content
				state.writeOpenAISSE(OpenAIStreamChunk{
					ID:     respID,
					Object: "chat.completion.chunk",
					Model:  openAIReq.Model,
					Choices: []OpenAIStreamChoice{
						{
							Index: 0,
							Delta: OpenAIStreamDelta{
								Content: &content,
							},
						},
					},
				})
			}
		}

		if chunk.Done {
			// Flush any remaining buffered content from thinking phase
			if state.pendingContent != "" {
				pending := state.pendingContent
				state.pendingContent = ""
				state.writeOpenAISSE(OpenAIStreamChunk{
					ID:     respID,
					Object: "chat.completion.chunk",
					Model:  openAIReq.Model,
					Choices: []OpenAIStreamChoice{
						{
							Index: 0,
							Delta: OpenAIStreamDelta{
								Content: &pending,
							},
						},
					},
				})
			}
			state.closeToolCalls()

			finishReason := "stop"
			switch chunk.DoneReason {
			case "length":
				finishReason = "length"
			case "tool_call", "tool_calls":
				finishReason = "tool_calls"
			}

			// Fallback: Ollama typically returns done_reason "stop" even when tool calls
			// are present, so check if we saw any tool call blocks during streaming.
			if finishReason != "tool_calls" && state.toolCallIndex > 0 {
				finishReason = "tool_calls"
			}

			state.writeOpenAISSE(OpenAIStreamChunk{
				ID:     respID,
				Object: "chat.completion.chunk",
				Model:  openAIReq.Model,
				Choices: []OpenAIStreamChoice{
					{
						Index:        0,
						FinishReason: &finishReason,
					},
				},
				Usage: &OpenAIStreamUsage{
					PromptTokens:     state.inputTokens,
					CompletionTokens: state.outputTokens,
					TotalTokens:      state.inputTokens + state.outputTokens,
				},
			})
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

func (p *Proxy) handleOpenAIInboundOpenAIStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest) {
	reqStart := time.Now()
	var liveTokens int
	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(openAIReq.Model, p.providerType, client, 0, liveTokens, time.Since(reqStart))
	}()

	body, err := json.Marshal(openAIReq)
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

	log.Printf("-> %s %s (streaming)", req.Method, p.upstreamURL+"/v1/chat/completions")

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

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		if canFlush {
			flusher.Flush()
		}
		if line == "data: [DONE]" {
			fmt.Fprintf(w, "\n")
			if canFlush {
				flusher.Flush()
			}
			break
		}

		// Track live token progress for stats
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var chunk OpenAIStreamChunk
			if json.Unmarshal([]byte(data), &chunk) == nil {
				if len(chunk.Choices) > 0 {
					choice := chunk.Choices[0]
					if (choice.Delta.Content != nil && *choice.Delta.Content != "") ||
						(choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "") ||
						len(choice.Delta.ToolCalls) > 0 {
						liveTokens++
						globalStats.AddTokens(1)
					}
				}
				if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
					liveTokens = chunk.Usage.CompletionTokens
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream passthrough read error: %v", err)
	}
}

func writeOpenAISSE(w io.Writer, flusher http.Flusher, canFlush bool, chunk OpenAIStreamChunk) {
	dataJSON, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", dataJSON)
	if canFlush {
		flusher.Flush()
	}
}