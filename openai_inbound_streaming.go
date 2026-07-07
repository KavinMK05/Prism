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
		// Flush any buffered tool calls before closing so they are emitted
		// in order, ahead of any subsequent content.
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

// recordToolCall buffers an Ollama tool call. The first time a call identity
// is seen it is assigned a stable index; every chunk updates the buffered
// arguments to the latest complete object. Nothing is emitted yet.
func (s *ollamaStreamState) recordToolCall(tc OllamaToolCall) {
	toolName := tc.Function.Name
	if toolName == "" {
		return
	}
	key := tc.ID
	if key == "" {
		key = toolName
	}

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
		writeOpenAIError(w, 500, "server_error", "Failed to marshal Ollama request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.apiChatURL(), bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (streaming)", req.Method, rp.apiChatURL())

	resp, err := pr.client.Do(req)
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
				state.closeToolCalls()
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
				state.closeToolCalls()
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
		writeOpenAIError(w, 500, "server_error", "Failed to marshal request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (streaming)", req.Method, rp.chatCompletionsURL())

	resp, err := pr.client.Do(req)
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