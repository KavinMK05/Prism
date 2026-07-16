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

// responsesEmitter wraps an SSE writer with a per-response monotonic
// sequence_number counter. The Responses API spec requires every streaming
// event to carry a sequence_number; inject it here so call sites don't have to.
type responsesEmitter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	canFlush bool
	seq      int
}

// emit writes a Responses SSE event, injecting an incrementing sequence_number
// into map payloads (per the OpenAI Responses streaming-events spec).
func (e *responsesEmitter) emit(event string, data interface{}) {
	if m, ok := data.(map[string]interface{}); ok {
		e.seq++
		m["sequence_number"] = e.seq
	}
	emitResponsesEvent(e.w, e.flusher, e.canFlush, event, data)
}

// buildToolCallItem builds the output item map for a completed tool call.
// For custom_tool_call (e.g. apply_patch), it uses the `input` field with the
// raw extracted text instead of `arguments` (JSON string).
func buildToolCallItem(itemID, outputType, callID, name, namespace, arguments, status string) map[string]interface{} {
	if outputType == "custom_tool_call" {
		return map[string]interface{}{
			"id":      itemID,
			"type":    "custom_tool_call",
			"call_id": callID,
			"name":    name,
			"input":   extractCustomToolInput(arguments),
			"status":  status,
		}
	}
	item := map[string]interface{}{
		"id":        itemID,
		"type":      outputType,
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
		"status":    status,
	}
	if namespace != "" {
		item["namespace"] = namespace
	}
	return item
}

// buildToolCallAddedItem builds the output_item.added item for a new tool call.
// For custom_tool_call, uses `input` instead of `arguments`.
func buildToolCallAddedItem(itemID, outputType, callID, name, namespace string) map[string]interface{} {
	if outputType == "custom_tool_call" {
		return map[string]interface{}{
			"id":      itemID,
			"type":    "custom_tool_call",
			"call_id": callID,
			"name":    name,
			"input":   "",
			"status":  "in_progress",
		}
	}
	item := map[string]interface{}{
		"id":        itemID,
		"type":      outputType,
		"call_id":   callID,
		"name":      name,
		"arguments": "",
		"status":    "in_progress",
	}
	if namespace != "" {
		item["namespace"] = namespace
	}
	return item
}

// streamingFuncCall holds the per-stream-index state for a single tool call
// being assembled from OpenAI Chat Completions streaming deltas. OpenAI
// streams parallel tool calls keyed by `index`: the id+name arrive on the first
// chunk for each index, then later chunks carry only function.arguments
// deltas. Keying by index (instead of a single active call) keeps each call's
// arguments in its own buffer so they don't get mixed together.
type streamingFuncCall struct {
	outputIndex int
	callID      string
	itemID      string
	name        string
	childName   string
	namespace   string
	outputType  string
	argsBuilder strings.Builder
	itemAdded   bool
	itemDone    bool
}

// finalizeFuncCalls emits the function_call_arguments.done / custom_tool_call
// input.done and response.output_item.done events for every tool call tracked
// in funcCalls that has been opened but not yet closed, in stream-index order.
// Returns the closed output items so callers can append them to the
// response.completed `output` aggregation. Safe to call multiple times (the
// itemDone guard makes repeated calls no-ops).
func finalizeFuncCalls(e *responsesEmitter, funcCalls map[int]*streamingFuncCall, funcCallOrder []int) []interface{} {
	var items []interface{}
	for _, idx := range funcCallOrder {
		fc := funcCalls[idx]
		if fc == nil || !fc.itemAdded || fc.itemDone {
			continue
		}
		args := fc.argsBuilder.String()
		emitToolCallDoneEvent(e, fc.outputType, fc.itemID, args, fc.outputIndex)
		fcItem := buildToolCallItem(fc.itemID, fc.outputType, fc.callID, fc.childName, fc.namespace, args, "completed")
		e.emit("response.output_item.done", map[string]interface{}{
			"type":         "response.output_item.done",
			"output_index": fc.outputIndex,
			"item":         fcItem,
		})
		fc.itemDone = true
		items = append(items, fcItem)
	}
	return items
}

// emitToolCallDoneEvent emits the appropriate "done" event for a tool call.
// For custom_tool_call, emits response.custom_tool_call_input.done with `input`.
// For function_call, emits response.function_call_arguments.done with `arguments`.
// Both events carry `item_id` (the function_call/custom_tool_call item id), per spec.
func emitToolCallDoneEvent(e *responsesEmitter, outputType, itemID string, arguments string, outputIndex int) {
	if outputType == "custom_tool_call" {
		e.emit("response.custom_tool_call_input.done", map[string]interface{}{
			"type":         "response.custom_tool_call_input.done",
			"output_index": outputIndex,
			"item_id":      itemID,
			"input":        extractCustomToolInput(arguments),
		})
	} else {
		e.emit("response.function_call_arguments.done", map[string]interface{}{
			"type":         "response.function_call_arguments.done",
			"output_index": outputIndex,
			"item_id":      itemID,
			"arguments":    arguments,
		})
	}
}

// emitToolCallDeltaEvent emits the appropriate delta event for streaming tool arguments.
// For custom_tool_call, deltas are SKIPPED because the upstream chat-completions
// model streams JSON fragments (e.g. {"patch": "..."}) which are not the raw
// tool input. Emitting them as custom_tool_call_input.delta would cause Codex
// Desktop to accumulate JSON fragments as the input, producing invalid patch
// text. Instead, we only emit the done event with the fully extracted input.
// For function_call, emits response.function_call_arguments.delta as normal.
func emitToolCallDeltaEvent(e *responsesEmitter, outputType, itemID string, delta string, outputIndex int) {
	if outputType == "custom_tool_call" {
		// Skip delta events for custom_tool_call — the done event carries
		// the correct extracted input.
		return
	}
	e.emit("response.function_call_arguments.delta", map[string]interface{}{
		"type":         "response.function_call_arguments.delta",
		"output_index": outputIndex,
		"item_id":      itemID,
		"delta":        delta,
	})
}

// responsesOutputTextPart returns a Responses API output_text content part
// including the spec-required annotations and logprobs arrays. The
// CLIProxyAPI reference populates these on every output_text part; strict
// clients key off them, so emit them here rather than as bare {type,text}.
func responsesOutputTextPart(text string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "output_text",
		"annotations": []interface{}{},
		"logprobs":    []interface{}{},
		"text":        text,
	}
}

// emitReasoningClose finalizes an active reasoning stream by emitting the
// three closing events in spec order:
//
//  1. response.reasoning_summary_text.done  (pairs with *_text.delta)
//  2. response.reasoning_summary_part.done  (only when partAdded)
//  3. response.output_item.done             (only when itemID != "")
//
// Previously Prism skipped #1, leaving the .delta stream unterminated. The
// completed reasoning item is appended to completedOutput.
func emitReasoningClose(e *responsesEmitter, itemID string, outputIndex, summaryIndex int, text string, partAdded bool, completedOutput *[]interface{}) {
	e.emit("response.reasoning_summary_text.done", map[string]interface{}{
		"type":          "response.reasoning_summary_text.done",
		"item_id":       itemID,
		"output_index":  outputIndex,
		"summary_index": summaryIndex,
		"text":          text,
	})
	if partAdded {
		e.emit("response.reasoning_summary_part.done", map[string]interface{}{
			"type":          "response.reasoning_summary_part.done",
			"item_id":       itemID,
			"output_index":  outputIndex,
			"summary_index": summaryIndex,
			"part": map[string]interface{}{
				"type": "summary_text",
				"text": text,
			},
		})
	}
	if itemID != "" {
		reasoningItem := map[string]interface{}{
			"id":      itemID,
			"type":    "reasoning",
			"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": text}},
		}
		e.emit("response.output_item.done", map[string]interface{}{
			"type":         "response.output_item.done",
			"output_index": outputIndex,
			"item":         reasoningItem,
		})
		*completedOutput = append(*completedOutput, reasoningItem)
	}
}

func (pr *ProviderRouter) handleResponsesAPIOpenAIStreaming(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *ResolvedProvider, toolTypes map[string]string, toolNamespaces map[string]string) {
	reqStart := time.Now()

	// Dump the original request, translated request, raw upstream response and
	// translated response to disk for debugging (same capture the Ollama path
	// uses). No-ops when the directory cannot be created. wrapWriter tees every
	// SSE frame we emit to the client into the capture.
	dbg := newTranslationDebugCapture("responses", true, respReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)

	chatReq := translateResponsesAPIToChatCompletions(respReq)

	// Log tools being sent upstream for debugging
	if len(chatReq.Tools) > 0 {
		for _, t := range chatReq.Tools {
			log.Printf("[REQ] streaming tool upstream: type=%s name=%s", t.Type, t.Function.Name)
		}
	} else {
		log.Printf("[REQ] streaming: no tools in request (original tools count: %d)", len(respReq.Tools))
	}

	// Validate reasoning_effort for the model
	chatReq.ReasoningEffort = pr.validateReasoningEffort(chatReq.Model, chatReq.ReasoningEffort)

	// Inject stream_options to get usage data from the upstream provider
	if chatReq.Stream {
		chatReq.StreamOptions = &OpenAIStreamOptions{IncludeUsage: true}
	}

	body, err := json.Marshal(chatReq)
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

	log.Printf("-> %s %s (responses streaming)", req.Method, rp.chatCompletionsURL())

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
	e := &responsesEmitter{w: w, flusher: flusher, canFlush: canFlush}

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	outputIndex := -1 // Will be incremented as items are added
	contentIndex := 0
	var messageItemID string
	var textContentPartID string
	funcCalls := map[int]*streamingFuncCall{}
	funcCallOrder := []int{}
	var accumulatedText string
	var outputTokens int
	var inputTokens int
	var liveOutputTokens int
	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(respReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))
	}()
	var completedEmitted bool
	var completionStatus = "completed"
	var reasoningItemID string
	var reasoningActive bool
	var reasoningSummaryIndex int
	var reasoningSummaryPartAdded bool
	var accumulatedReasoning string
	var completedOutput []interface{} // accumulated output items for response.completed
	var completedOutputText string    // accumulated output text for response.completed

	// Emit response.created
	e.emit("response.created", map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      respReq.Model,
			"background": false,
			"error":      nil,
			"status":     "in_progress",
			"output":     []interface{}{},
			"usage":      responsesUsageMap(0, 0),
		},
	})

	// Emit response.in_progress (standard event the CLIProxyAPI reference emits
	// immediately after response.created; strict clients expect it before any
	// output items).
	e.emit("response.in_progress", map[string]interface{}{
		"type": "response.in_progress",
		"response": map[string]interface{}{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      respReq.Model,
			"background": false,
			"error":      nil,
			"status":     "in_progress",
			"output":     []interface{}{},
			"usage":      responsesUsageMap(0, 0),
		},
	})

	scanner := bufio.NewScanner(dbg.teeBody(resp.Body))
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
				e.emit("response.output_item.added", map[string]interface{}{
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
				e.emit("response.reasoning_summary_part.added", map[string]interface{}{
					"type":          "response.reasoning_summary_part.added",
					"item_id":       reasoningItemID,
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
			e.emit("response.reasoning_summary_text.delta", map[string]interface{}{
				"type":          "response.reasoning_summary_text.delta",
				"item_id":       reasoningItemID,
				"output_index":  outputIndex,
				"summary_index": reasoningSummaryIndex,
				"delta":         *choice.Delta.ReasoningContent,
			})
		}

		if !currentChunkHasThinking && reasoningActive {
			reasoningActive = false
			emitReasoningClose(e, reasoningItemID, outputIndex, reasoningSummaryIndex, accumulatedReasoning, reasoningSummaryPartAdded, &completedOutput)
			reasoningSummaryPartAdded = false
			reasoningItemID = ""
		}

		// Handle tool calls - if tool calls arrive, close current message first
		if len(delta.ToolCalls) > 0 {
			// Close text content part if active
			if textContentPartID != "" {
				e.emit("response.output_text.done", map[string]interface{}{
					"type":          "response.output_text.done",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"text":          accumulatedText,
				})
				e.emit("response.content_part.done", map[string]interface{}{
					"type":          "response.content_part.done",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": responsesOutputTextPart(accumulatedText),
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
					"content": []interface{}{responsesOutputTextPart(accumulatedText)},
				}
				e.emit("response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         msgItem,
				})
				completedOutput = append(completedOutput, msgItem)
				messageItemID = ""
			}

			for _, tc := range delta.ToolCalls {
				// Track tool calls per stream-index so parallel calls keep separate
				// argument buffers. OpenAI sends id+name on the first chunk for each
				// index and bare argument deltas on later chunks.
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				fc, exists := funcCalls[idx]
				if !exists {
					fc = &streamingFuncCall{}
					funcCalls[idx] = fc
					funcCallOrder = append(funcCallOrder, idx)
					outputIndex++
					fc.outputIndex = outputIndex
				}
				if tc.ID != "" && fc.callID == "" {
					fc.callID = tc.ID
				}
				if tc.Function.Name != "" && fc.name == "" {
					fc.name = tc.Function.Name
				}
				// Open the item once we know its name (first chunk for this index).
				if !fc.itemAdded && fc.name != "" {
					if fc.callID == "" {
						fc.callID = generateID("call_")
					}
					fc.itemID = "fc_" + fc.callID
					fc.outputType = resolveToolOutputType(fc.name, toolTypes)
					fc.childName, fc.namespace = splitToolNamespace(fc.name, toolNamespaces)
					fc.itemAdded = true
					log.Printf("[RESP] streaming tool call: name=%s idx=%d resolved_type=%s toolTypes=%v", fc.name, idx, fc.outputType, toolTypes)
					e.emit("response.output_item.added", map[string]interface{}{
						"type":         "response.output_item.added",
						"output_index": fc.outputIndex,
						"item":         buildToolCallAddedItem(fc.itemID, fc.outputType, fc.callID, fc.childName, fc.namespace),
					})
				}
				if tc.Function.Arguments != "" {
					fc.argsBuilder.WriteString(tc.Function.Arguments)
					liveOutputTokens++
					globalStats.AddTokens(1)
					if fc.itemAdded && tc.Function.Arguments != "{}" {
						emitToolCallDeltaEvent(e, fc.outputType, fc.itemID, tc.Function.Arguments, fc.outputIndex)
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
				e.emit("response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":      messageItemID,
						"type":    "message",
						"status":  "in_progress",
						"role":    "assistant",
						"content": []interface{}{},
					},
				})
				// Start content part
				contentIndex = 0
				textContentPartID = generateID("cont_")
				e.emit("response.content_part.added", map[string]interface{}{
					"type":          "response.content_part.added",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": responsesOutputTextPart(""),
				})
			}

			accumulatedText += *delta.Content
			completedOutputText += *delta.Content
			liveOutputTokens++
			globalStats.AddTokens(1)
			e.emit("response.output_text.delta", map[string]interface{}{
				"type":          "response.output_text.delta",
				"item_id":          messageItemID,
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
				e.emit("response.output_text.done", map[string]interface{}{
					"type":          "response.output_text.done",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"text":          accumulatedText,
				})
				e.emit("response.content_part.done", map[string]interface{}{
					"type":          "response.content_part.done",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": responsesOutputTextPart(accumulatedText),
				})
				textContentPartID = ""
			}

			// Close reasoning if active
			if reasoningActive {
				reasoningActive = false
				emitReasoningClose(e, reasoningItemID, outputIndex, reasoningSummaryIndex, accumulatedReasoning, reasoningSummaryPartAdded, &completedOutput)
				reasoningSummaryPartAdded = false
				reasoningItemID = ""
			}

			// Close message item if active
			if messageItemID != "" {
				msgItem := map[string]interface{}{
					"id":      messageItemID,
					"type":    "message",
					"status":  status,
					"role":    "assistant",
					"content": []interface{}{responsesOutputTextPart(accumulatedText)},
				}
				e.emit("response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         msgItem,
				})
				completedOutput = append(completedOutput, msgItem)
				messageItemID = ""
			}

			// Close any active function calls (in stream-index order).
			completedOutput = append(completedOutput, finalizeFuncCalls(e, funcCalls, funcCallOrder)...)

			// Defer response.completed to the terminal [DONE] marker so late
			// usage-only chunks (sent by OpenAI-compatible providers after
			// finish_reason when stream_options.include_usage is set) can
			// still populate response.usage. Emitting here would ship
			// usage={0,0,0}. Mirrors the CLIProxyAPI reference.
			completionStatus = status
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Stream read error: %v", err)
	}

	// Final cleanup: emit response.completed. The finish_reason handler only set
	// the completionPending flag (so late usage-only chunks could land); the
	// terminal event is emitted here, after the upstream stream is fully
	// consumed. This also covers streams that ended without a finish_reason.
	if !completedEmitted {
		// Close text content part if active
		if textContentPartID != "" {
			e.emit("response.output_text.done", map[string]interface{}{
				"type":          "response.output_text.done",
				"item_id":          messageItemID,
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"text":          accumulatedText,
			})
			e.emit("response.content_part.done", map[string]interface{}{
				"type":          "response.content_part.done",
				"item_id":          messageItemID,
				"output_index":  outputIndex,
				"content_index": contentIndex,
				"part": responsesOutputTextPart(accumulatedText),
			})
		}

		if messageItemID != "" {
			msgItem := map[string]interface{}{
				"id":      messageItemID,
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": []interface{}{responsesOutputTextPart(accumulatedText)},
			}
			e.emit("response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         msgItem,
			})
			completedOutput = append(completedOutput, msgItem)
		}

		completedOutput = append(completedOutput, finalizeFuncCalls(e, funcCalls, funcCallOrder)...)

		// Close reasoning if still active
		if reasoningActive {
			reasoningActive = false
			emitReasoningClose(e, reasoningItemID, outputIndex, reasoningSummaryIndex, accumulatedReasoning, reasoningSummaryPartAdded, &completedOutput)
			reasoningSummaryPartAdded = false
			reasoningItemID = ""
		}

		completedResp := map[string]interface{}{
			"id":          respID,
			"object":      "response",
			"created_at":  createdAt,
			"model":       respReq.Model,
			"background":  false,
			"error":       nil,
			"status":      completionStatus,
			"output":      completedOutput,
			"output_text": completedOutputText,
			"usage":         responsesUsageMap(inputTokens, outputTokens),
		}
		mergeResponsesEchoFields(completedResp, respReq)
		e.emit("response.completed", map[string]interface{}{
			"type":     "response.completed",
			"response": completedResp,
		})
	}
}

func (pr *ProviderRouter) handleResponsesAPIOllamaStreaming(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *ResolvedProvider, toolTypes map[string]string, toolNamespaces map[string]string) {
	reqStart := time.Now()

	// Dump the original request, translated request, original Ollama response
	// and translated response to disk when PRISM_DEBUG_RESPONSES is set. All
	// methods are no-ops when the capture is nil (debug disabled). wrapWriter
	// tees every SSE frame we emit to the client into the capture.
	dbg := newTranslationDebugCapture("responses", true, respReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)

	ollamaReq := translateResponsesAPIToOllama(respReq)
	dbg.writeJSON("1_original_request.json", respReq)
	dbg.writeJSON("2_translated_request.json", ollamaReq)

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

	log.Printf("-> %s %s (responses streaming)", req.Method, rp.apiChatURL())

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
	e := &responsesEmitter{w: w, flusher: flusher, canFlush: canFlush}

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	outputIndex := -1
	contentIndex := 0
	var messageItemID string
	var contentPartID string
	var funcCallItemID string
	var funcCallCallID string
	var funcCallName string
	var funcCallChildName string
	var funcCallNamespace string
	var funcCallOutputType string
	var outputTokens int
	var inputTokens int
	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(respReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))
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
	var completedOutputText string    // accumulated output text for response.completed

	// Emit response.created
	e.emit("response.created", map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      respReq.Model,
			"background": false,
			"error":      nil,
			"status":     "in_progress",
			"output":     []interface{}{},
			"usage":      responsesUsageMap(0, 0),
		},
	})

	// Emit response.in_progress (standard event the CLIProxyAPI reference emits
	// immediately after response.created; strict clients expect it before any
	// output items).
	e.emit("response.in_progress", map[string]interface{}{
		"type": "response.in_progress",
		"response": map[string]interface{}{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"model":      respReq.Model,
			"background": false,
			"error":      nil,
			"status":     "in_progress",
			"output":     []interface{}{},
			"usage":      responsesUsageMap(0, 0),
		},
	})

	// teeBody mirrors resp.Body into the debug capture (#3 original response)
	// when enabled; returns resp.Body unchanged otherwise.
	scanner := bufio.NewScanner(dbg.teeBody(resp.Body))
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
				e.emit("response.output_item.added", map[string]interface{}{
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
				e.emit("response.reasoning_summary_part.added", map[string]interface{}{
					"type":          "response.reasoning_summary_part.added",
					"item_id":       reasoningItemID,
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
			e.emit("response.reasoning_summary_text.delta", map[string]interface{}{
				"type":          "response.reasoning_summary_text.delta",
				"item_id":       reasoningItemID,
				"output_index":  outputIndex,
				"summary_index": thinkingSummaryIndex,
				"delta":         chunk.Message.Thinking,
			})
		}

		hasNonThinkingContent := chunk.Message.Content != "" || len(chunk.Message.ToolCalls) > 0

		if hasNonThinkingContent && thinkingActive {
			thinkingActive = false
			emitReasoningClose(e, reasoningItemID, outputIndex, thinkingSummaryIndex, accumulatedReasoning, thinkingSummaryPartAdded, &completedOutput)
			thinkingSummaryPartAdded = false
			reasoningItemID = ""
		}

		// Handle tool calls
		if len(chunk.Message.ToolCalls) > 0 {
			// Close message item if active
			if messageItemID != "" {
				if contentPartID != "" {
					e.emit("response.output_text.done", map[string]interface{}{
						"type":          "response.output_text.done",
						"item_id":          messageItemID,
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"text":          accumulatedText,
					})
					e.emit("response.content_part.done", map[string]interface{}{
						"type":          "response.content_part.done",
						"item_id":          messageItemID,
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"part": responsesOutputTextPart(accumulatedText),
					})
					contentPartID = ""
				}
				msgItem := map[string]interface{}{
					"id":      messageItemID,
					"type":    "message",
					"status":  "completed",
					"role":    "assistant",
					"content": []interface{}{responsesOutputTextPart(accumulatedText)},
				}
				e.emit("response.output_item.done", map[string]interface{}{
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
				// Prefer the upstream tool-call id so call_ids stay unique across
				// turns. The previous code synthesized "call_<name>_<outputIndex>"
				// and discarded tc.ID; since outputIndex resets every stream, every
				// turn's first call to the same tool reused the same call_id. That
				// collided ids across the replayed history (30 calls, 3 unique ids),
				// so the upstream could no longer correlate tool outputs to their
				// calls. Fall back to the synthetic id only when the upstream omits
				// one. Mirrors the OpenAI streaming path, the non-streaming path,
				// and the CLIProxyAPI reference.
				funcCallCallID = tc.ID
				if funcCallCallID == "" {
					funcCallCallID = fmt.Sprintf("call_%s_%d", toolName, outputIndex)
				}
				// Responses API distinguishes the item `id` (fc_*) from `call_id` (call_*).
				funcCallItemID = "fc_" + funcCallCallID
				funcCallName = toolName
				funcCallOutputType = resolveToolOutputType(funcCallName, toolTypes)
				funcCallChildName, funcCallNamespace = splitToolNamespace(funcCallName, toolNamespaces)

				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				argsStr := string(argsJSON)

				e.emit("response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item":         buildToolCallAddedItem(funcCallItemID, funcCallOutputType, funcCallCallID, funcCallChildName, funcCallNamespace),
				})

				emitToolCallDoneEvent(e, funcCallOutputType, funcCallItemID, argsStr, outputIndex)

				fcItem := buildToolCallItem(funcCallItemID, funcCallOutputType, funcCallCallID, funcCallChildName, funcCallNamespace, argsStr, "completed")
				e.emit("response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         fcItem,
				})
				completedOutput = append(completedOutput, fcItem)
				// Ollama emits complete tool calls in a single chunk, so close the
				// item immediately and clear the active-call handle to avoid a
				// duplicate output_item.done at stream end.
				funcCallItemID = ""
				toolCallsEmitted++
			}
		}

		// Handle content
		if chunk.Message.Content != "" {
			if messageItemID == "" {
				outputIndex++
				messageItemID = generateID("msg_")
				e.emit("response.output_item.added", map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item": map[string]interface{}{
						"id":      messageItemID,
						"type":    "message",
						"status":  "in_progress",
						"role":    "assistant",
						"content": []interface{}{},
					},
				})
				contentIndex = 0
				contentPartID = generateID("cont_")
				e.emit("response.content_part.added", map[string]interface{}{
					"type":          "response.content_part.added",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": responsesOutputTextPart(""),
				})
			}

			accumulatedText += chunk.Message.Content
			completedOutputText += chunk.Message.Content
			globalStats.AddTokens(1)
			e.emit("response.output_text.delta", map[string]interface{}{
				"type":          "response.output_text.delta",
				"item_id":          messageItemID,
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
				emitReasoningClose(e, reasoningItemID, outputIndex, thinkingSummaryIndex, accumulatedReasoning, thinkingSummaryPartAdded, &completedOutput)
				thinkingSummaryPartAdded = false
				reasoningItemID = ""
			}

			// Close message item if active
			if messageItemID != "" {
				if contentPartID != "" {
					e.emit("response.output_text.done", map[string]interface{}{
						"type":          "response.output_text.done",
						"item_id":          messageItemID,
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"text":          accumulatedText,
					})
					e.emit("response.content_part.done", map[string]interface{}{
						"type":          "response.content_part.done",
						"item_id":          messageItemID,
						"output_index":  outputIndex,
						"content_index": contentIndex,
						"part": responsesOutputTextPart(accumulatedText),
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
					"content": []interface{}{responsesOutputTextPart(accumulatedText)},
				}
				e.emit("response.output_item.done", map[string]interface{}{
					"type":         "response.output_item.done",
					"output_index": outputIndex,
					"item":         msgItem,
				})
				completedOutput = append(completedOutput, msgItem)
				messageItemID = ""
			}

			// Close function call if active
			if funcCallItemID != "" {
				fcItem := buildToolCallItem(funcCallItemID, funcCallOutputType, funcCallCallID, funcCallChildName, funcCallNamespace, "{}", "completed")
				e.emit("response.output_item.done", map[string]interface{}{
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
				"background":  false,
				"error":       nil,
				"status":      status,
				"output":      completedOutput,
				"output_text": completedOutputText,
				"usage":         responsesUsageMap(inputTokens, outputTokens),
			}
			mergeResponsesEchoFields(completedResp, respReq)
			e.emit("response.completed", map[string]interface{}{
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
			emitReasoningClose(e, reasoningItemID, outputIndex, thinkingSummaryIndex, accumulatedReasoning, thinkingSummaryPartAdded, &completedOutput)
			thinkingSummaryPartAdded = false
			reasoningItemID = ""
		}

		if messageItemID != "" {
			if contentPartID != "" {
				e.emit("response.output_text.done", map[string]interface{}{
					"type":          "response.output_text.done",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"text":          accumulatedText,
				})
				e.emit("response.content_part.done", map[string]interface{}{
					"type":          "response.content_part.done",
					"item_id":          messageItemID,
					"output_index":  outputIndex,
					"content_index": contentIndex,
					"part": responsesOutputTextPart(accumulatedText),
				})
			}

			msgItem := map[string]interface{}{
				"id":      messageItemID,
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": []interface{}{responsesOutputTextPart(accumulatedText)},
			}
			e.emit("response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         msgItem,
			})
			completedOutput = append(completedOutput, msgItem)
		}

		if funcCallItemID != "" {
			fcItem := buildToolCallItem(funcCallItemID, funcCallOutputType, funcCallCallID, funcCallChildName, funcCallNamespace, "{}", "completed")
			e.emit("response.output_item.done", map[string]interface{}{
				"type":         "response.output_item.done",
				"output_index": outputIndex,
				"item":         fcItem,
			})
			completedOutput = append(completedOutput, fcItem)
		}

		completedResp := map[string]interface{}{
			"id":          respID,
			"object":      "response",
			"created_at":  createdAt,
			"model":       respReq.Model,
			"background":  false,
			"error":       nil,
			"status":      "completed",
			"output":      completedOutput,
			"output_text": completedOutputText,
			"usage":         responsesUsageMap(inputTokens, outputTokens),
		}
		mergeResponsesEchoFields(completedResp, respReq)
		e.emit("response.completed", map[string]interface{}{
			"type":     "response.completed",
			"response": completedResp,
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
