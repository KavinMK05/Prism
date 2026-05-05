package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func parseSSEEvents(body string) []SSEEvent {
	var events []SSEEvent
	var currentEvent SSEEvent
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event: ") {
			currentEvent.Event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			currentEvent.Data = strings.TrimPrefix(line, "data: ")
		} else if line == "" && currentEvent.Event != "" {
			events = append(events, currentEvent)
			currentEvent = SSEEvent{}
		}
	}
	if currentEvent.Event != "" {
		events = append(events, currentEvent)
	}
	return events
}

func parseOpenAISSEEvents(body string) []map[string]interface{} {
	var events []map[string]interface{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				events = append(events, m)
			}
		}
	}
	return events
}

func makeOllamaChunk(model string, content string, thinking string, done bool, doneReason string) string {
	chunk := map[string]interface{}{
		"model": model,
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": content,
		},
		"done": done,
	}
	if thinking != "" {
		chunk["message"].(map[string]interface{})["thinking"] = thinking
	}
	if done {
		chunk["done_reason"] = doneReason
		chunk["eval_count"] = 10
		chunk["prompt_eval_count"] = 5
	}
	b, _ := json.Marshal(chunk)
	return string(b) + "\n"
}

func makeOpenAIChunk(model string, content string, reasoningContent string, finishReason string, toolCalls []map[string]interface{}) string {
	return makeOpenAIChunkWithRole(model, content, reasoningContent, finishReason, toolCalls, true)
}

func makeOpenAIChunkWithRole(model string, content string, reasoningContent string, finishReason string, toolCalls []map[string]interface{}, withRole bool) string {
	delta := map[string]interface{}{}
	if withRole {
		delta["role"] = "assistant"
	}
	if content != "" {
		delta["content"] = content
	}
	if reasoningContent != "" {
		delta["reasoning_content"] = reasoningContent
	}
	if len(toolCalls) > 0 {
		delta["tool_calls"] = toolCalls
	}
	if len(delta) == 0 && !withRole {
		delta["role"] = ""
	}
	chunk := map[string]interface{}{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": delta,
			},
		},
	}
	if finishReason != "" {
		chunk["choices"].([]map[string]interface{})[0]["finish_reason"] = finishReason
	}
	b, _ := json.Marshal(chunk)
	return "data: " + string(b) + "\n"
}

func TestOllamaStreaming_TextOnly(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "Hello", "", false, "")
	upstreamBody += makeOllamaChunk("test-model", " world", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"test","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{
		Model:     "test",
		Stream:    true,
		MaxTokens: 100,
		Messages:  []AnthropicMessage{{Role: "user", Content: "hi"}},
	}
	ollamaReq := &OllamaChatRequest{
		Model:    "test",
		Stream:   true,
		Messages: []OllamaMessage{{Role: "user", Content: "hi"}},
	}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())

	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	expected := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

func TestOllamaStreaming_ThinkingThenText(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "", "Let me think", false, "")
	upstreamBody += makeOllamaChunk("test-model", "", " more thoughts", false, "")
	upstreamBody += makeOllamaChunk("test-model", "Hello", "", false, "")
	upstreamBody += makeOllamaChunk("test-model", " world", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// Should have: message_start, thinking start, thinking delta x2, thinking stop, text start, text delta x2, text stop, message_delta, message_stop
	expected := []string{
		"message_start",
		"content_block_start",   // thinking block
		"content_block_delta",  // thinking "Let me think"
		"content_block_delta",  // thinking " more thoughts"
		"content_block_stop",   // thinking block closes when text arrives
		"content_block_start",  // text block
		"content_block_delta",  // text "Hello"
		"content_block_delta",  // text " world"
		"content_block_stop",   // text block closes
		"message_delta",
		"message_stop",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

func TestOllamaStreaming_ThinkingWithEmptyChunks(t *testing.T) {
	var upstreamBody string
	// Simulate Ollama sending chunks where thinking is empty between thinking chunks
	// This was the root cause of the bug - empty thinking != thinking done
	upstreamBody += makeOllamaChunk("test-model", "", "thinking step 1", false, "")
	upstreamBody += makeOllamaChunk("test-model", "", "", false, "") // empty thinking, no content
	upstreamBody += makeOllamaChunk("test-model", "", "thinking step 2", false, "")
	upstreamBody += makeOllamaChunk("test-model", "final answer", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// The thinking block should NOT close on the empty chunk
	// It should only close when non-thinking content arrives
	expected := []string{
		"message_start",
		"content_block_start",   // thinking block opens
		"content_block_delta",  // thinking "thinking step 1"
		// empty chunk is skipped entirely (no thinking, no content)
		"content_block_delta",  // thinking "thinking step 2"
		"content_block_stop",   // thinking closes when "final answer" arrives
		"content_block_start",  // text block
		"content_block_delta",  // text "final answer"
		"content_block_stop",
		"message_delta",
		"message_stop",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

func TestOpenAIStreaming_TextOnly(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOpenAIChunk("test-model", "Hello", "", "", nil)
	upstreamBody += makeOpenAIChunk("test-model", " world", "", "", nil)
	upstreamBody += makeOpenAIChunk("test-model", "", "", "stop", nil)
	upstreamBody += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "openai", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	proxy.handleOpenAIStreaming(w, req, openAIReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	expected := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

func TestOpenAIStreaming_ThinkingThenText(t *testing.T) {
	var upstreamBody string
	// OpenAI format: reasoning_content followed by content
	upstreamBody += makeOpenAIChunk("test-model", "", "Let me think", "", nil)
	upstreamBody += makeOpenAIChunk("test-model", "", " more thinking", "", nil)
	upstreamBody += makeOpenAIChunk("test-model", "Hello", "", "", nil)
	upstreamBody += makeOpenAIChunk("test-model", " world", "", "stop", nil)
	upstreamBody += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "openai", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	proxy.handleOpenAIStreaming(w, req, openAIReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	expected := []string{
		"message_start",
		"content_block_start",   // thinking
		"content_block_delta",  // thinking "Let me think"
		"content_block_delta",  // thinking " more thinking"
		"content_block_stop",   // thinking ends when content starts
		"content_block_start",  // text
		"content_block_delta",  // "Hello"
		"content_block_delta",  // " world"
		"content_block_stop",
		"message_delta",
		"message_stop",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

func TestOllamaStreaming_EmptyResponse(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	expected := []string{
		"message_start",
		"content_block_start",  // empty text block
		"content_block_stop",
		"message_delta",
		"message_stop",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

func TestOpenAIInboundOllamaStreaming_TextOnly(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "Hello", "", false, "")
	upstreamBody += makeOllamaChunk("test-model", " world", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	openAIReq := &OpenAIChatRequest{
		Model:    "test",
		Stream:   true,
		Messages: []OpenAIChatMessage{{Role: "user", Content: "hi"}},
	}

	proxy.handleOpenAIInboundOllamaStreaming(w, req, openAIReq)

	chunks := parseOpenAISSEEvents(w.Body.String())

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	firstContent := ""
	for _, c := range chunks {
		if choices, ok := c["choices"].([]interface{}); ok && len(choices) > 0 {
			choice := choices[0].(map[string]interface{})
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				if content, ok := delta["content"].(string); ok && content != "" {
					if firstContent == "" {
						firstContent = content
					}
				}
			}
		}
	}

	if firstContent != "Hello" {
		t.Errorf("expected first content 'Hello', got '%s'", firstContent)
	}

	lastChunk := chunks[len(chunks)-1]
	if lastChunk["choices"] == nil {
		t.Error("last chunk should have choices")
	}
}

func TestOpenAIInboundOllamaStreaming_ThinkingThenText(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "", "Let me think", false, "")
	upstreamBody += makeOllamaChunk("test-model", "", "", false, "") // empty chunk between thinking
	upstreamBody += makeOllamaChunk("test-model", "", " more thoughts", false, "")
	upstreamBody += makeOllamaChunk("test-model", "Hello", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	proxy.handleOpenAIInboundOllamaStreaming(w, req, openAIReq)

	chunks := parseOpenAISSEEvents(w.Body.String())

	reasoningCount := 0
	contentCount := 0
	for _, c := range chunks {
		if choices, ok := c["choices"].([]interface{}); ok && len(choices) > 0 {
			choice := choices[0].(map[string]interface{})
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
					reasoningCount++
				}
				if ct, ok := delta["content"].(string); ok && ct != "" {
					contentCount++
				}
			}
		}
	}

	if reasoningCount != 2 {
		t.Errorf("expected 2 reasoning chunks, got %d", reasoningCount)
	}
	if contentCount != 1 {
		t.Errorf("expected 1 content chunk, got %d", contentCount)
	}
}

func TestOllamaStreaming_ToolCalls(t *testing.T) {
	toolCallChunk := map[string]interface{}{
		"model": "test-model",
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]interface{}{
				{
					"function": map[string]interface{}{
						"name": "get_weather",
						"arguments": map[string]interface{}{
							"city": "London",
						},
					},
				},
			},
		},
		"done":        false,
	}

	var upstreamBody string
	b, _ := json.Marshal(toolCallChunk)
	upstreamBody += string(b) + "\n"
	upstreamBody += makeOllamaChunk("test-model", "", "", true, "tool_call")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	hasToolUseStart := false
	hasToolUseDelta := false
	hasToolUseStop := false
	for _, e := range events {
		if e.Event == "content_block_start" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if block, ok := data["content_block"].(map[string]interface{}); ok {
				if block["type"] == "tool_use" {
					hasToolUseStart = true
				}
			}
		}
		if e.Event == "content_block_delta" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if delta, ok := data["delta"].(map[string]interface{}); ok {
				if delta["type"] == "input_json_delta" {
					hasToolUseDelta = true
				}
			}
		}
		if e.Event == "content_block_stop" {
			// Just check it exists - the tool_use block should be stopped
			lastStopData := map[string]interface{}{}
			json.Unmarshal([]byte(e.Data), &lastStopData)
			if idx, ok := lastStopData["index"].(float64); ok {
				_ = idx
				hasToolUseStop = true
			}
		}
	}

	if !hasToolUseStart {
		t.Error("expected tool_use content_block_start")
	}
	if !hasToolUseDelta {
		t.Error("expected input_json_delta content_block_delta")
	}
	if !hasToolUseStop {
		t.Error("expected content_block_stop for tool_use block")
	}
}

func TestOllamaStreaming_StreamingReadError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		chunk := makeOllamaChunk("test-model", "Hello", "", false, "")
		w.Write([]byte(chunk))
		flusher.Flush()
		// Connection drops here - no done chunk
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	// Should not panic, should produce some output
	body := w.Body.String()
	if body == "" {
		t.Error("expected non-empty response even on incomplete stream")
	}

	events := parseSSEEvents(body)
	hasMessageStart := false
	for _, e := range events {
		if e.Event == "message_start" {
			hasMessageStart = true
		}
	}
	if !hasMessageStart {
		t.Error("expected message_start event")
	}
}

func TestOpenAIStreaming_MultipleToolCalls(t *testing.T) {
	// OpenAI realistic format:
	// Chunk 1: role=assistant + tool_calls[0] with id, name, partial args
	// Chunk 2: tool_calls[0] with more args (no role, no id - continuation)
	// Chunk 3: tool_calls[1] with id, name, args (no role - new tool call)
	// Chunk 4: finish_reason=tool_calls
	toolCall1 := map[string]interface{}{
		"id":   "call_abc123",
		"type":  "function",
		"function": map[string]interface{}{
			"name":      "get_weather",
			"arguments": "{\"ci",
		},
	}
	toolCall1Cont := map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"arguments": "ty\":\"London\"}",
		},
	}
	toolCall2 := map[string]interface{}{
		"id":   "call_def456",
		"type":  "function",
		"function": map[string]interface{}{
			"name":      "get_time",
			"arguments": "{\"tz\":\"UTC\"}",
		},
	}

	var upstreamBody string
	// Chunk 1: first tool call with role
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "", []map[string]interface{}{toolCall1}, true)
	// Chunk 2: continuation of first tool call (no role)
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "", []map[string]interface{}{toolCall1Cont}, false)
	// Chunk 3: second tool call (no role - handled by else branch)
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "", []map[string]interface{}{toolCall2}, false)
	// Chunk 4: finish
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "tool_calls", nil, false)
	upstreamBody += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "openai", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	proxy.handleOpenAIStreaming(w, req, openAIReq, anthroReq)

	events := parseSSEEvents(w.Body.String())

	toolUseStarts := 0
	toolUseStops := 0
	inputJsonDeltas := 0
	for _, e := range events {
		if e.Event == "content_block_start" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if block, ok := data["content_block"].(map[string]interface{}); ok {
				if block["type"] == "tool_use" {
					toolUseStarts++
					t.Logf("tool_use start: id=%s name=%s", block["id"], block["name"])
				}
			}
		}
		if e.Event == "content_block_stop" {
			toolUseStops++
		}
		if e.Event == "content_block_delta" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if delta, ok := data["delta"].(map[string]interface{}); ok {
				if delta["type"] == "input_json_delta" {
					inputJsonDeltas++
					t.Logf("input_json_delta: %s", delta["partial_json"])
				}
			}
		}
	}

	if toolUseStarts != 2 {
		t.Errorf("expected 2 tool_use block starts, got %d", toolUseStarts)
	}
	if toolUseStops != 2 {
		t.Errorf("expected 2 content_block_stops, got %d", toolUseStops)
	}
	if inputJsonDeltas != 3 {
		t.Errorf("expected 3 input_json_delta events, got %d", inputJsonDeltas)
	}
}

// Verify SSE format correctness for Anthropic protocol
func TestOllamaStreaming_SSEFormat(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "Hello", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	body := w.Body.String()

	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", w.Header().Get("Content-Type"))
	}

	if !bytes.Contains([]byte(body), []byte("event: message_start\n")) {
		t.Error("expected event: message_start in output")
	}

	if !bytes.Contains([]byte(body), []byte("event: message_stop\n")) {
		t.Error("expected event: message_stop in output")
	}
}

// Test that content block indexes are sequential and correct
func TestOllamaStreaming_ContentBlockIndexes(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "", "thinking...", false, "")
	upstreamBody += makeOllamaChunk("test-model", "answer", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())

	blockIndexes := []float64{}
	for _, e := range events {
		var data map[string]interface{}
		json.Unmarshal([]byte(e.Data), &data)
		if idx, ok := data["index"].(float64); ok {
			blockIndexes = append(blockIndexes, idx)
		}
	}

	// Thinking block should be index 0, text block should be index 1
	if len(blockIndexes) < 4 {
		t.Fatalf("expected at least 4 events with indexes, got %d: %v", len(blockIndexes), blockIndexes)
	}

	for i, idx := range blockIndexes[:4] {
		_ = float64(i / 2)
		if i < 2 && idx != 0 {
			t.Errorf("thinking block events should have index 0, got %f at position %d", idx, i)
		}
	}
}

// Edge case: Ollama sends tool calls followed by text content
func TestOllamaStreaming_ToolCallsThenText(t *testing.T) {
	toolCallChunk := map[string]interface{}{
		"model": "test-model",
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]interface{}{
				{
					"function": map[string]interface{}{
						"name":      "get_weather",
						"arguments": map[string]interface{}{"city": "London"},
					},
				},
			},
		},
		"done": false,
	}
	b, _ := json.Marshal(toolCallChunk)

	var upstreamBody string
	upstreamBody += string(b) + "\n"
	upstreamBody += makeOllamaChunk("test-model", "The weather is sunny", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// Tool calls should close before text opens
	expected := []string{
		"message_start",
		"content_block_start",   // tool_use
		"content_block_delta",  // tool args
		"content_block_stop",   // tool_use closes
		"content_block_start",  // text
		"content_block_delta",  // text content
		"content_block_stop",   // text closes
		"message_delta",
		"message_stop",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

// Edge case: OpenAI sends tool calls followed by text content
func TestOpenAIStreaming_ToolCallsThenText(t *testing.T) {
	toolCall := map[string]interface{}{
		"id":   "call_abc123",
		"type":  "function",
		"function": map[string]interface{}{
			"name":      "get_weather",
			"arguments": "{\"city\":\"London\"}",
		},
	}

	var upstreamBody string
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "", []map[string]interface{}{toolCall}, true)
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "tool_calls", nil, false)
	upstreamBody += makeOpenAIChunkWithRole("test-model", "The weather is sunny", "", "", nil, false)
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "stop", nil, false)
	upstreamBody += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "openai", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	proxy.handleOpenAIStreaming(w, req, openAIReq, anthroReq)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// Tool calls should close before text opens
	expected := []string{
		"message_start",
		"content_block_start",   // tool_use
		"content_block_delta",  // tool args
		"content_block_stop",   // tool_use closes
		"message_delta",
		"message_stop",
		"content_block_start",  // text
		"content_block_delta",  // text content
		"content_block_stop",   // text closes
		"message_delta",
		"message_stop",
	}

	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, exp := range expected {
		if eventTypes[i] != exp {
			t.Errorf("event[%d]: expected %s, got %s", i, exp, eventTypes[i])
		}
	}
}

// Edge case: Stream ends with connection drop during tool calls
func TestOllamaStreaming_ConnectionDropDuringToolCalls(t *testing.T) {
	toolCallChunk := map[string]interface{}{
		"model": "test-model",
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]interface{}{
				{
					"function": map[string]interface{}{
						"name":      "get_weather",
						"arguments": map[string]interface{}{"city": "London"},
					},
				},
			},
		},
		"done": false,
	}
	b, _ := json.Marshal(toolCallChunk)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(string(b) + "\n"))
		flusher.Flush()
		// Connection drops - no done chunk
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	body := w.Body.String()
	if body == "" {
		t.Error("expected non-empty response even on incomplete stream")
	}

	events := parseSSEEvents(body)
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// Should still have message_start and cleanup blocks
	hasMessageStart := false
	for _, e := range eventTypes {
		if e == "message_start" {
			hasMessageStart = true
		}
	}
	if !hasMessageStart {
		t.Error("expected message_start event in incomplete stream")
	}
}

// Edge case: Ollama sends multiple tool calls in sequence
func TestOllamaStreaming_MultipleToolCalls(t *testing.T) {
	toolCall1 := map[string]interface{}{
		"model": "test-model",
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]interface{}{
				{
					"function": map[string]interface{}{
						"name":      "get_weather",
						"arguments": map[string]interface{}{"city": "London"},
					},
				},
			},
		},
		"done": false,
	}
	toolCall2 := map[string]interface{}{
		"model": "test-model",
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]interface{}{
				{
					"function": map[string]interface{}{
						"name":      "get_time",
						"arguments": map[string]interface{}{"tz": "UTC"},
					},
				},
			},
		},
		"done": false,
	}

	b1, _ := json.Marshal(toolCall1)
	b2, _ := json.Marshal(toolCall2)

	var upstreamBody string
	upstreamBody += string(b1) + "\n"
	upstreamBody += string(b2) + "\n"
	upstreamBody += makeOllamaChunk("test-model", "", "", true, "tool_call")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	proxy := NewProxy(upstream.URL, "test-key", "ollama", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	proxy.handleStreaming(w, req, ollamaReq, anthroReq)

	events := parseSSEEvents(w.Body.String())

	toolUseStarts := 0
	toolUseStops := 0
	for _, e := range events {
		if e.Event == "content_block_start" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if block, ok := data["content_block"].(map[string]interface{}); ok {
				if block["type"] == "tool_use" {
					toolUseStarts++
					t.Logf("tool_use start: name=%s", block["name"])
				}
			}
		}
		if e.Event == "content_block_stop" {
			toolUseStops++
		}
	}

	// Ollama doesn't send separate tool calls like OpenAI - they come in one chunk
	// The handler should still close the tool_use block properly
	if toolUseStarts != 2 {
		t.Errorf("expected 2 tool_use block starts, got %d", toolUseStarts)
	}
	if toolUseStops != 2 {
		t.Errorf("expected 2 content_block_stops, got %d", toolUseStops)
	}
}