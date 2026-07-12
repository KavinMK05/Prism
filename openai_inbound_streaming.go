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
	emittedToolCalls  map[string]*streamedToolCall // tool calls seen, keyed by identity
	pendingToolCalls  []*streamedToolCall         // tool calls buffered for emission at finalization
}

func (s *ollamaStreamState) writeOpenAISSE(chunk OpenAIStreamChunk) {
	writeOpenAISSE(s.w, s.flusher, s.canFlush, chunk)
}
func (s *ollamaStreamState) closeToolCalls() {
	if s.toolCallsActive {
		// Flush buffered tool calls. This is only called at stream
		// finalization (the done chunk, or post-loop on a dropped stream):
		// because Ollama re-emits the full cumulative arguments on every
		// chunk, flushing mid-stream (e.g. when content arrives) would emit
		// partial arguments and silently drop the fuller cumulative updates
		// that arrive afterwards. Deferring to the end guarantees every call
		// is emitted exactly once with its final, complete arguments.
		s.flushPendingToolCalls()
		s.toolCallsActive = false
	}
}

// streamedToolCall buffers one tool call being streamed from Ollama. Ollama
// re-emits the full cumulative arguments object in every chunk — a complete,
// closed JSON object each time — so the arguments cannot be diffed into
// OpenAI-style deltas (the closing brace breaks any prefix relationship).
// Instead we keep the latest complete arguments per tool call and emit the
// whole call once at finalization, matching Ollama's own OpenAI-compat
// single-emission behaviour and letting clients reconstruct a single call.
type streamedToolCall struct {
	index    int
	id       string
	name     string
	argsJSON string
	flushed  bool
}

// toolCallIdentity returns a stable dedup key for an Ollama tool call.
// Ollama re-emits the same call (with cumulative arguments) across chunks; a
// call's identity is function.index, which Ollama always emits and which is
// distinct even for parallel calls to the same tool. Falling back to id, then
// to the function name, only when index and id are both absent.
func toolCallIdentity(tc OllamaToolCall) string {
	if tc.Function.Index != nil {
		return fmt.Sprintf("idx:%d", *tc.Function.Index)
	}
	if tc.ID != "" {
		return "id:" + tc.ID
	}
	return "name:" + tc.Function.Name
}

// recordToolCall buffers an Ollama tool call. The first time a call identity
// is seen it is assigned a stable index; every chunk updates the buffered
// arguments to the latest complete object. Nothing is emitted yet.
func (s *ollamaStreamState) recordToolCall(tc OllamaToolCall) {
	toolName := tc.Function.Name
	if toolName == "" {
		return
	}
	key := toolCallIdentity(tc)

	var argsStr string
	if tc.Function.Arguments != nil {
		b, _ := json.Marshal(tc.Function.Arguments)
		argsStr = string(b)
	}

	tcall, seen := s.emittedToolCalls[key]
	if !seen {
		idx := s.toolCallIndex
		s.toolCallIndex++
		id := tc.ID
		if id == "" {
			id = generateToolUseID(toolName)
		}
		tcall = &streamedToolCall{index: idx, id: id, name: toolName}
		s.emittedToolCalls[key] = tcall
		s.pendingToolCalls = append(s.pendingToolCalls, tcall)
		if !s.toolCallsActive {
			s.toolCallsActive = true
		}
	}
	tcall.argsJSON = argsStr
}

// flushPendingToolCalls emits every buffered tool call exactly once: a header
// chunk (id + name + empty arguments) followed by a single argument chunk
// carrying the full arguments JSON. Called at stream finalization.
func (s *ollamaStreamState) flushPendingToolCalls() {
	for _, tcall := range s.pendingToolCalls {
		if tcall.flushed {
			continue
		}
		tcall.flushed = true
		created := time.Now().Unix()
		s.writeOpenAISSE(OpenAIStreamChunk{
			ID:      s.respID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   s.model,
			Choices: []OpenAIStreamChoice{{
				Index: 0,
				Delta: OpenAIStreamDelta{
					ToolCalls: []OpenAIToolCall{{
						Index:    &tcall.index,
						ID:       tcall.id,
						Type:     "function",
						Function: OpenAIToolCallFunc{Name: tcall.name, Arguments: ""},
					}},
				},
			}},
		})
		if tcall.argsJSON != "" {
			s.writeOpenAISSE(OpenAIStreamChunk{
				ID:      s.respID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   s.model,
				Choices: []OpenAIStreamChoice{{
					Index: 0,
					Delta: OpenAIStreamDelta{
						ToolCalls: []OpenAIToolCall{{
							Index:    &tcall.index,
							Function: OpenAIToolCallFunc{Arguments: tcall.argsJSON},
						}},
					},
				}},
			})
		}
	}
}

func (pr *ProviderRouter) handleOpenAIInboundOllamaStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, rp *ResolvedProvider) {
	reqStart := time.Now()

	ollamaReq := translateOpenAIToOllama(openAIReq)

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeStreamingOpenAIError(w, 500, "server_error", "Failed to marshal Ollama request", openAIReq.Model)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.apiChatURL(), bytes.NewReader(body))
	if err != nil {
		writeStreamingOpenAIError(w, 500, "server_error", "Failed to create upstream request", openAIReq.Model)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (streaming)", req.Method, rp.apiChatURL())

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeStreamingOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error(), openAIReq.Model)
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeStreamingOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode), openAIReq.Model)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	respID := fmt.Sprintf("chatcmpl-%s", openAIReq.Model)

	createdAt := time.Now().Unix()

	state := &ollamaStreamState{
		w:      w,
		flusher: flusher,
		canFlush: canFlush,
		respID: respID,
		model:  openAIReq.Model,
		emittedToolCalls: make(map[string]*streamedToolCall),
	}

	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(openAIReq.Model, rp.ProviderID, client, state.inputTokens, state.outputTokens, time.Since(reqStart))
	}()

	state.writeOpenAISSE(OpenAIStreamChunk{
		ID:      respID,
		Object:  "chat.completion.chunk",
		Created: createdAt,
		Model:   openAIReq.Model,
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
				ID:      respID,
				Object:  "chat.completion.chunk",
				Created: createdAt,
				Model:   openAIReq.Model,
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
				pending := state.pendingContent
				state.pendingContent = ""
				state.writeOpenAISSE(OpenAIStreamChunk{
					ID:      respID,
					Object:  "chat.completion.chunk",
					Created: createdAt,
					Model:   openAIReq.Model,
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
				globalStats.AddTokens(1)
				state.recordToolCall(tc)
			}
		}

		if chunk.Message.Content != "" {
			globalStats.AddTokens(1)
			if state.thinkingActive {
				// Buffer content until thinking ends
				state.pendingContent += chunk.Message.Content
			} else {
				content := chunk.Message.Content
				state.writeOpenAISSE(OpenAIStreamChunk{
					ID:      respID,
					Object:  "chat.completion.chunk",
					Created: createdAt,
					Model:   openAIReq.Model,
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
			if state.pendingContent != "" {
				pending := state.pendingContent
				state.pendingContent = ""
				state.writeOpenAISSE(OpenAIStreamChunk{
					ID:      respID,
					Object:  "chat.completion.chunk",
					Created: createdAt,
					Model:   openAIReq.Model,
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
				ID:      respID,
				Object:  "chat.completion.chunk",
				Created: createdAt,
				Model:   openAIReq.Model,
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
	// If the stream ended without a done chunk, still emit any buffered tool
	// calls so they are not lost.
	state.closeToolCalls()

	fmt.Fprintf(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

func (pr *ProviderRouter) handleOpenAIInboundOpenAIStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, rp *ResolvedProvider) {
	reqStart := time.Now()
	var liveTokens int
	var inputTokens int
	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(openAIReq.Model, rp.ProviderID, client, inputTokens, liveTokens, time.Since(reqStart))
	}()

	// Validate reasoning_effort for the model
	openAIReq.ReasoningEffort = pr.validateReasoningEffort(openAIReq.Model, openAIReq.ReasoningEffort)

	// Inject stream_options to get usage data from the upstream provider
	openAIReq.StreamOptions = &OpenAIStreamOptions{IncludeUsage: true}

	body, err := json.Marshal(openAIReq)
	if err != nil {
		writeStreamingOpenAIError(w, 500, "server_error", "Failed to marshal request", openAIReq.Model)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		writeStreamingOpenAIError(w, 500, "server_error", "Failed to create upstream request", openAIReq.Model)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (streaming)", req.Method, rp.chatCompletionsURL())

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeStreamingOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error(), openAIReq.Model)
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeStreamingOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode), openAIReq.Model)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	// Fallback values for chunks missing required OpenAI fields.
	// Some OpenAI-compatible providers omit "id", "object", "created",
	// "model", or "choices" in streaming chunks, which causes strict serde
	// clients (e.g. Grok Build) to fail deserialization.
	respID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()

	// Track tool call metadata (id, type, name) by index so we can inject
	// these fields into subsequent argument-delta chunks. In the standard
	// OpenAI streaming format, only the first chunk for a tool call contains
	// id/type/name; subsequent chunks only carry arguments. Some strict
	// clients (e.g. Grok Build) expect these fields on every chunk.
	type toolCallMeta struct {
		id, typ, name string
	}
	toolCalls := make(map[int]*toolCallMeta)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// For data: lines (not [DONE]), ensure the chunk has all required
		// OpenAI chat completion chunk fields and tool call metadata.
		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			data := strings.TrimPrefix(line, "data: ")

			var chunkMap map[string]interface{}
			if json.Unmarshal([]byte(data), &chunkMap) == nil {
				modified := false

				// Inject required top-level fields if missing
				if id, ok := chunkMap["id"].(string); !ok || id == "" {
					chunkMap["id"] = respID
					modified = true
				}
				if obj, ok := chunkMap["object"].(string); !ok || obj == "" {
					chunkMap["object"] = "chat.completion.chunk"
					modified = true
				}
				if _, ok := chunkMap["created"]; !ok {
					chunkMap["created"] = createdAt
					modified = true
				}
				if model, ok := chunkMap["model"].(string); !ok || model == "" {
					chunkMap["model"] = openAIReq.Model
					modified = true
				}
				if _, ok := chunkMap["choices"]; !ok {
					chunkMap["choices"] = []interface{}{}
					modified = true
				}

				// Track and inject tool call metadata (id, type, name)
				if choices, ok := chunkMap["choices"].([]interface{}); ok {
					for _, choice := range choices {
						choiceMap, ok := choice.(map[string]interface{})
						if !ok {
							continue
						}
						delta, ok := choiceMap["delta"].(map[string]interface{})
						if !ok {
							continue
						}
						tcs, ok := delta["tool_calls"].([]interface{})
						if !ok {
							continue
						}
						for _, tc := range tcs {
							tcMap, ok := tc.(map[string]interface{})
							if !ok {
								continue
							}
							idx := -1
							if idxFloat, ok := tcMap["index"].(float64); ok {
								idx = int(idxFloat)
							}
							if idx < 0 {
								continue
							}

							funcObj, _ := tcMap["function"].(map[string]interface{})

							// Capture metadata from first chunk
							tcID, _ := tcMap["id"].(string)
							tcType, _ := tcMap["type"].(string)
							tcName := ""
							if funcObj != nil {
								tcName, _ = funcObj["name"].(string)
							}
							if tcID != "" || tcType != "" || tcName != "" {
								if existing, ok := toolCalls[idx]; ok {
									if tcID != "" {
										existing.id = tcID
									}
									if tcType != "" {
										existing.typ = tcType
									}
									if tcName != "" {
										existing.name = tcName
									}
								} else {
									toolCalls[idx] = &toolCallMeta{id: tcID, typ: tcType, name: tcName}
								}
							}

							// Inject metadata into delta chunks that lack it
							meta := toolCalls[idx]
							if meta != nil {
								if tcID, _ := tcMap["id"].(string); tcID == "" && meta.id != "" {
									tcMap["id"] = meta.id
									modified = true
								}
								if tcType, _ := tcMap["type"].(string); tcType == "" && meta.typ != "" {
									tcMap["type"] = meta.typ
									modified = true
								}
								if funcObj == nil {
									funcObj = map[string]interface{}{}
									tcMap["function"] = funcObj
									modified = true
								}
								if name, _ := funcObj["name"].(string); name == "" && meta.name != "" {
									funcObj["name"] = meta.name
									modified = true
								}
							}
						}
					}
				}

				if modified {
					if modifiedData, mErr := json.Marshal(chunkMap); mErr == nil {
						line = "data: " + string(modifiedData)
					}
				}
			}

			// Track live token progress for stats
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
				if chunk.Usage != nil {
					if chunk.Usage.PromptTokens > 0 {
						inputTokens = chunk.Usage.PromptTokens
					}
					if chunk.Usage.CompletionTokens > 0 {
						liveTokens = chunk.Usage.CompletionTokens
					}
				}
			}
		}

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

// writeStreamingOpenAIError sends an error to the client in SSE format for
// streaming requests. Some strict serde clients (e.g. Grok Build) attempt to
// deserialize the response body as a chat completion chunk even on non-200
// responses, which fails with "missing field `id`" on a plain JSON error.
// Wrapping the error in an SSE chunk with an `id` field lets those clients
// parse the response and detect the error via the `error` field.
func writeStreamingOpenAIError(w http.ResponseWriter, statusCode int, errType string, message string, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(statusCode)

	respID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	errorChunk := map[string]interface{}{
		"id":      respID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []interface{}{},
		"error": map[string]interface{}{
			"message": message,
			"type":    errType,
			"code":    statusCode,
		},
	}
	dataJSON, _ := json.Marshal(errorChunk)
	fmt.Fprintf(w, "data: %s\n\n", dataJSON)
	fmt.Fprintf(w, "data: [DONE]\n\n")

	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}
}