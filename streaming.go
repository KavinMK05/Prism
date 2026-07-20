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
	"sync"
	"time"
)

type streamState struct {
	w                 http.ResponseWriter
	flusher           http.Flusher
	canFlush          bool
	contentBlockIndex int
	hasContentBlock   bool
	thinkingBlockOpen bool
	textBlockOpen     bool
	toolUseBlockOpen  bool
	toolCallIndex     int
	totalOutputTokens int
	totalPromptTokens int
	cacheReadTokens   int
	msgID             string
	stopSent          bool
	mu                sync.Mutex
	// Tracking for cumulative Ollama tool-call streaming. Ollama re-emits the
	// full accumulated arguments (a complete, closed JSON object) in every
	// chunk, so the arguments cannot be diffed into input_json_delta suffixes.
	// We buffer the latest complete arguments per tool call and emit the whole
	// call once at finalization, matching Ollama's own single-emission behaviour.
	pendingToolUses []*pendingToolUse
	pendingToolMap  map[string]*pendingToolUse
	// pendingOpenTool buffers a single OpenAI-compatible tool call whose
	// content_block_start has not yet been emitted, because the upstream has
	// not supplied both a function name and an id. Some providers split name
	// and id across separate deltas; emitting content_block_start before the
	// id is known would force a synthetic id that diverges from upstream.
	pendingOpenTool *pendingOpenTool
}

// pendingOpenTool buffers one OpenAI-compatible tool call until both its name
// and id are known, so content_block_start can carry the real (sanitized) id.
type pendingOpenTool struct {
	name string
	id   string
	key  string
	args strings.Builder
}

// pendingToolUse buffers one tool call being streamed from Ollama until
// finalization, when it is emitted as a single tool_use content block.
type pendingToolUse struct {
	name     string
	id       string
	argsJSON string
	flushed  bool
}

func newStreamState(w http.ResponseWriter, flusher http.Flusher, canFlush bool, msgID string) *streamState {
	return &streamState{
		w:              w,
		flusher:        flusher,
		canFlush:       canFlush,
		msgID:          msgID,
		pendingToolMap: make(map[string]*pendingToolUse),
	}
}

func (s *streamState) writeSSE(event string, data interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeSSE(s.w, s.flusher, s.canFlush, event, data)
}

// startPings spawns a goroutine that emits Anthropic "ping" keepalive events
// every 15s until the returned stop function is called. Pings keep the SSE
// connection alive during slow upstream reasoning. The stop function is
// idempotent (safe to defer and call explicitly).
func (s *streamState) startPings() (stop func()) {
	stopCh := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				s.writeSSE("ping", map[string]interface{}{"type": "ping"})
			}
		}
	}()
	return func() { once.Do(func() { close(stopCh) }) }
}

// sendStreamError emits an Anthropic SSE error event mid-stream, used when the
// upstream connection fails after the SSE response has already begun.
func (s *streamState) sendStreamError(errType, message string) {
	s.writeSSE("error", map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    errType,
			"message": message,
		},
	})
}

// flushPendingToolUses emits every buffered tool call exactly once as a
// tool_use content block: content_block_start, a single input_json_delta
// carrying the full arguments JSON, then content_block_stop. Called at
// stream finalization (or when text content arrives after tool calls).
func (s *streamState) flushPendingToolUses() {
	for _, pu := range s.pendingToolUses {
		if pu.flushed {
			continue
		}
		pu.flushed = true
		s.openToolUseBlockWithID(pu.name, pu.id)
		if pu.argsJSON != "" {
			globalStats.AddTokens(1)
			s.writeSSE("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": s.contentBlockIndex,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": pu.argsJSON,
				},
			})
		}
		s.closeBlock("tool_use")
	}
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

// openToolUseBlockWithID opens a tool_use block. If id is non-empty it is used
// as the tool_use ID (e.g. preserving an upstream-provided ID); otherwise a
// globally-unique ID is generated so it cannot collide with tool_use IDs from
// earlier turns in the same conversation.
func (s *streamState) openToolUseBlockWithID(toolName, id string) {
	if s.hasOpenBlock() {
		s.closeBlock("tool_use")
	}
	if id == "" {
		id = generateToolUseID(toolName)
	} else {
		id = sanitizeToolUseID(id)
	}
	log.Printf("[STREAM] Opening tool_use block at index %d", s.contentBlockIndex)
	s.writeSSE("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": s.contentBlockIndex,
		"content_block": map[string]interface{}{
			"type":  "tool_use",
			"id":    id,
			"name":  toolName,
			"input": map[string]interface{}{},
		},
	})
	s.toolUseBlockOpen = true
	s.hasContentBlock = true
	s.toolCallIndex++
}

// emitToolArgsDelta writes one input_json_delta content_block_delta carrying
// a fragment of tool-call arguments for the currently-open tool_use block.
func (s *streamState) emitToolArgsDelta(args string) {
	if args == "" || !s.toolUseBlockOpen {
		return
	}
	s.writeSSE("content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": s.contentBlockIndex,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": args,
		},
	})
}

// flushPendingOpenTool emits the buffered tool call (content_block_start with
// the sanitized real id, or a minted synthetic id if the upstream never sent
// one) followed by a single input_json_delta carrying all buffered arguments.
// Safe to call when no call is buffered (no-op).
func (s *streamState) flushPendingOpenTool() {
	pu := s.pendingOpenTool
	if pu == nil {
		return
	}
	s.pendingOpenTool = nil
	s.openToolUseBlockWithID(pu.name, pu.id)
	s.emitToolArgsDelta(pu.args.String())
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
	if s.stopSent {
		return
	}
	s.stopSent = true
	log.Printf("[STREAM] Sending message_delta with stop_reason=%s, output_tokens=%d", stopReason, outputTokens)
	s.writeSSE("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": s.usagePayload(outputTokens),
	})

	s.writeSSE("message_stop", map[string]interface{}{
		"type": "message_stop",
	})
}

// usagePayload builds the usage object for the terminal message_delta event.
// Anthropic's real API reports input_tokens in message_start and only
// output_tokens in message_delta, but OpenAI streaming delivers usage in the
// final chunk (after message_start was already sent with zeros). We therefore
// carry the real input_tokens here so Claude Code learns the prompt size for
// context-window tracking and auto-compaction. cache_read_input_tokens is
// reported separately (OpenAI's prompt_tokens already includes cached tokens,
// which the caller subtracted before setting totalPromptTokens).
func (s *streamState) usagePayload(outputTokens int) map[string]interface{} {
	usage := map[string]interface{}{
		"input_tokens":  s.totalPromptTokens,
		"output_tokens": outputTokens,
	}
	if s.cacheReadTokens > 0 {
		usage["cache_read_input_tokens"] = s.cacheReadTokens
	}
	return usage
}

func (pr *ProviderRouter) handleStreaming(w http.ResponseWriter, r *http.Request, ollamaReq *OllamaChatRequest, anthroReq *AnthropicRequest, rp *ResolvedProvider) {
	reqStart := time.Now()

	// Dump the original request, translated request, original Ollama response
	// and translated response to disk for debugging. wrapWriter tees every SSE
	// frame we emit to the client into the capture (#4).
	dbg := newTranslationDebugCapture("messages", true, anthroReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", anthroReq)
	dbg.writeJSON("2_translated_request.json", ollamaReq)

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal Ollama request")
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.apiChatURL(), bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (streaming)", upstreamReq.Method, rp.apiChatURL())

	resp, err := pr.client.Do(upstreamReq)
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
	msgID := "msg_" + sanitizeMessageIDFragment(anthroReq.Model)

	state := newStreamState(w, flusher, canFlush, msgID)

	client := detectClient(r)
	defer func() {
		globalStats.RecordRequest(anthroReq.Model, rp.ProviderID, client, state.totalPromptTokens, state.totalOutputTokens, time.Since(reqStart))
	}()

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

	// teeBody mirrors resp.Body into the debug capture (#3 original response);
	// returns resp.Body unchanged when the capture is nil.
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
			state.totalPromptTokens = chunk.PromptEvalCount
		}
		if chunk.EvalCount > state.totalOutputTokens {
			state.totalOutputTokens = chunk.EvalCount
		}

		if chunk.Message.Thinking != "" {
			if !state.thinkingBlockOpen {
				state.openThinkingBlock()
			}
			globalStats.AddTokens(1)
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
				// Ollama's index is the stable identity of a call while it is
				// streaming.  The same function may be called more than once in
				// one response, and some Ollama-compatible servers omit IDs, so
				// using the function name here collapses parallel calls.
				key := ""
				if tc.Function.Index != nil {
					key = fmt.Sprintf("index:%d", *tc.Function.Index)
				}
				if key == "" && tc.ID != "" {
					key = "id:" + tc.ID
				}
				if key == "" {
					key = "name:" + toolName
				}
				// Buffer the tool call. Ollama re-emits the full cumulative
				// arguments (a complete JSON object) in every chunk, so we
				// keep the latest arguments per call and emit the whole call
				// once at finalization rather than diffing into deltas.
				pu, seen := state.pendingToolMap[key]
				if !seen {
					pu = &pendingToolUse{name: toolName, id: tc.ID}
					state.pendingToolMap[key] = pu
					state.pendingToolUses = append(state.pendingToolUses, pu)
				}
				if pu.id == "" && tc.ID != "" {
					pu.id = tc.ID
				}
				if tc.Function.Arguments != nil {
					b, _ := json.Marshal(tc.Function.Arguments)
					pu.argsJSON = string(b)
				}
			}
		}

		if chunk.Message.Content != "" {
			// Emit any buffered tool calls before text so they appear in order.
			state.flushPendingToolUses()
			if state.toolUseBlockOpen {
				state.closeBlock("tool_use")
			}
			if !state.textBlockOpen && !state.thinkingBlockOpen {
				state.openTextBlock()
			}
			if state.textBlockOpen {
				globalStats.AddTokens(1)
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
			// Emit buffered tool calls as tool_use blocks before closing.
			state.flushPendingToolUses()
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

	// If the stream ended without a Done chunk, close any remaining blocks
	// and send an empty text block as fallback. We must still terminate the
	if state.thinkingBlockOpen || state.textBlockOpen || state.toolUseBlockOpen || len(state.pendingToolUses) > 0 {
		// Stream ended without a done chunk: still emit any buffered tool
		// calls so they are not lost, then close remaining blocks.
		state.flushPendingToolUses()
		state.closeAllBlocks()
	}
	if !state.hasContentBlock {
		state.sendEmptyTextBlock()
	}
	state.sendStopReason("end_turn", state.totalOutputTokens)
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
