package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeTestRouter(upstreamURL string) *ProviderRouter {
	cfg := &Config{
		DefaultProvider: "custom_test",
		CustomProviders: []*ProviderConfig{
			{ID: "custom_test", Name: "Test", BaseURL: upstreamURL, APIKey: "test-key"},
		},
	}
	remap := &ModelRemapping{
		DefaultModel: "test",
		KnownModels:  []ModelEntry{{ID: "test", Provider: "custom_test"}},
		Aliases:      map[string]string{},
	}
	return NewProviderRouter(cfg, remap)
}

func makeTestRP(upstreamURL, providerType string) *ResolvedProvider {
	return &ResolvedProvider{
		BaseURL:      upstreamURL,
		APIKey:       "test-key",
		ProviderType: providerType,
	}
}

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

// makeOllamaToolCallChunk builds an Ollama native /api/chat streaming chunk
// carrying a single tool call. Ollama streams tool calls cumulatively: each
// chunk re-emits the full accumulated arguments object (a complete, closed
// JSON object), growing chunk by chunk until the call is complete.
func makeOllamaToolCallChunk(model, id, toolName string, arguments map[string]interface{}, done bool, doneReason string) string {
	tc := map[string]interface{}{
		"function": map[string]interface{}{
			"name":      toolName,
			"arguments": arguments,
		},
	}
	if id != "" {
		tc["id"] = id
	}
	chunk := map[string]interface{}{
		"model": model,
		"message": map[string]interface{}{
			"role":       "assistant",
			"content":    "",
			"tool_calls": []map[string]interface{}{tc},
		},
		"done": done,
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
		"id":     "chatcmpl-test",
		"object": "chat.completion.chunk",
		"model":  model,
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
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

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// Should have: message_start, thinking start, thinking delta x2, thinking stop, text start, text delta x2, text stop, message_delta, message_stop
	expected := []string{
		"message_start",
		"content_block_start", // thinking block
		"content_block_delta", // thinking "Let me think"
		"content_block_delta", // thinking " more thoughts"
		"content_block_stop",  // thinking block closes when text arrives
		"content_block_start", // text block
		"content_block_delta", // text "Hello"
		"content_block_delta", // text " world"
		"content_block_stop",  // text block closes
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// The thinking block should NOT close on the empty chunk
	// It should only close when non-thinking content arrives
	expected := []string{
		"message_start",
		"content_block_start", // thinking block opens
		"content_block_delta", // thinking "thinking step 1"
		// empty chunk is skipped entirely (no thinking, no content)
		"content_block_delta", // thinking "thinking step 2"
		"content_block_stop",  // thinking closes when "final answer" arrives
		"content_block_start", // text block
		"content_block_delta", // text "final answer"
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	expected := []string{
		"message_start",
		"content_block_start", // thinking
		"content_block_delta", // thinking "Let me think"
		"content_block_delta", // thinking " more thinking"
		"content_block_stop",  // thinking ends when content starts
		"content_block_start", // text
		"content_block_delta", // "Hello"
		"content_block_delta", // " world"
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	expected := []string{
		"message_start",
		"content_block_start", // empty text block
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	openAIReq := &OpenAIChatRequest{
		Model:    "test",
		Stream:   true,
		Messages: []OpenAIChatMessage{{Role: "user", Content: "hi"}},
	}

	router.handleOpenAIInboundOllamaStreaming(w, req, openAIReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIInboundOllamaStreaming(w, req, openAIReq, rp)

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
		"done": false,
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

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
		"type": "function",
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
		"type": "function",
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// Tool calls should close before text opens
	expected := []string{
		"message_start",
		"content_block_start", // tool_use
		"content_block_delta", // tool args
		"content_block_stop",  // tool_use closes
		"content_block_start", // text
		"content_block_delta", // text content
		"content_block_stop",  // text closes
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
		"type": "function",
		"function": map[string]interface{}{
			"name":      "get_weather",
			"arguments": "{\"city\":\"London\"}",
		},
	}

	var upstreamBody string
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "", []map[string]interface{}{toolCall}, true)
	upstreamBody += makeOpenAIChunkWithRole("test-model", "The weather is sunny", "", "", nil, false)
	upstreamBody += makeOpenAIChunkWithRole("test-model", "", "", "stop", nil, false)
	upstreamBody += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	// Tool calls should close before text opens; a single terminating
	// message_delta + message_stop is emitted at finish_reason.
	expected := []string{
		"message_start",
		"content_block_start", // tool_use
		"content_block_delta", // tool args
		"content_block_stop",  // tool_use closes
		"content_block_start", // text
		"content_block_delta", // text content
		"content_block_stop",  // text closes
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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

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

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

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

// Test that stop_reason is "tool_use" when Ollama returns done_reason "stop" with tool calls
// This is the realistic case - Ollama typically doesn't return done_reason "tool_call"
func TestOllamaStreaming_ToolCallsWithStopDoneReason(t *testing.T) {
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
		"done": false,
	}

	var upstreamBody string
	b, _ := json.Marshal(toolCallChunk)
	upstreamBody += string(b) + "\n"
	// Ollama returns done_reason "stop" (not "tool_call") even when tool calls are present
	upstreamBody += makeOllamaChunk("test-model", "", "", true, "stop")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())

	// Verify that the message_delta event contains stop_reason "tool_use"
	for _, e := range events {
		if e.Event == "message_delta" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if delta, ok := data["delta"].(map[string]interface{}); ok {
				if stopReason, ok := delta["stop_reason"].(string); ok {
					if stopReason != "tool_use" {
						t.Errorf("expected stop_reason 'tool_use', got '%s'", stopReason)
					}
				}
			}
		}
	}
}

// Test that a premature stream end (no done chunk) still terminates the SSE
// stream with message_delta + message_stop so clients don't hang.
func TestOllamaStreaming_ConnectionDropSendsMessageStop(t *testing.T) {
	upstreamBody := makeOllamaChunk("test-model", "Hello", "", false, "")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
		flusher.Flush()
		// Connection drops - no done chunk
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "message_stop" {
		t.Fatalf("expected stream to end with message_stop, got %v", eventTypes)
	}
	if len(eventTypes) < 2 || eventTypes[len(eventTypes)-2] != "message_delta" {
		t.Errorf("expected message_delta before message_stop, got %v", eventTypes)
	}

	// Verify stop_reason defaults to end_turn on premature end
	for _, e := range events {
		if e.Event == "message_delta" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if delta, ok := data["delta"].(map[string]interface{}); ok {
				if sr, ok := delta["stop_reason"].(string); ok && sr != "end_turn" {
					t.Errorf("expected stop_reason 'end_turn' on drop, got '%s'", sr)
				}
			}
		}
	}
}

// Test that an OpenAI upstream stream ending without finish_reason still
// terminates with message_delta + message_stop.
func TestOpenAIStreaming_ConnectionDropSendsMessageStop(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOpenAIChunk("test-model", "Hello", "", "", nil)
	// No finish_reason, no [DONE] - connection drops

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
		flusher.Flush()
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "message_stop" {
		t.Fatalf("expected stream to end with message_stop, got %v", eventTypes)
	}
	if len(eventTypes) < 2 || eventTypes[len(eventTypes)-2] != "message_delta" {
		t.Errorf("expected message_delta before message_stop, got %v", eventTypes)
	}
}

// Test that [DONE] without a finish_reason still terminates the stream.
func TestOpenAIStreaming_DoneWithoutFinishReason(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOpenAIChunk("test-model", "Hello", "", "", nil)
	upstreamBody += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	if len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "message_stop" {
		t.Fatalf("expected stream to end with message_stop, got %v", eventTypes)
	}

	for _, e := range events {
		if e.Event == "message_delta" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if delta, ok := data["delta"].(map[string]interface{}); ok {
				if sr, ok := delta["stop_reason"].(string); ok && sr != "end_turn" {
					t.Errorf("expected stop_reason 'end_turn', got '%s'", sr)
				}
			}
		}
	}
}

// Test that the usage-only final chunk (choices:[] + usage) from OpenAI
// streaming with include_usage=true is captured and reported in message_delta.
func TestOpenAIStreaming_UsageOnlyChunk(t *testing.T) {
	contentChunk := makeOpenAIChunk("test-model", "Hello", "", "", nil)
	finishChunk := makeOpenAIChunk("test-model", "", "", "stop", nil)

	usageOnly := map[string]interface{}{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"model":   "test-model",
		"choices": []interface{}{},
		"usage": map[string]interface{}{
			"prompt_tokens":     7,
			"completion_tokens": 12,
			"total_tokens":      19,
		},
	}
	usageJSON, _ := json.Marshal(usageOnly)

	var upstreamBody string
	upstreamBody += contentChunk
	upstreamBody += finishChunk
	upstreamBody += "data: " + string(usageJSON) + "\n"
	upstreamBody += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())

	var outputTokens interface{}
	var foundDelta bool
	for _, e := range events {
		if e.Event == "message_delta" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if usage, ok := data["usage"].(map[string]interface{}); ok {
				outputTokens = usage["output_tokens"]
				foundDelta = true
			}
		}
	}
	if !foundDelta {
		t.Fatal("expected message_delta event with usage")
	}
	if outputTokens != float64(12) {
		t.Errorf("expected output_tokens=12 from usage-only chunk, got %v", outputTokens)
	}
}

// TestOpenAIInboundOllamaStreaming_ToolCallsCumulative reproduces the original
// bug: Ollama streams tool_calls cumulatively (each chunk re-emits the full
// accumulated arguments object), and the proxy must convert that into a single
// OpenAI tool call with a stable index — not one tool call per chunk.
func TestOpenAIInboundOllamaStreaming_ToolCallsCumulative(t *testing.T) {
	var upstreamBody string
	// Four cumulative chunks for one edit tool call; args grow each chunk.
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.g"}, false, "")
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.go"}, false, "")
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.go", "old": "A"}, false, "")
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.go", "old": "A", "new": "B"}, false, "")
	upstreamBody += makeOllamaChunk("test-model", "", "", true, "tool_call")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	openAIReq := &OpenAIChatRequest{Model: "test", Stream: true}
	router.handleOpenAIInboundOllamaStreaming(w, req, openAIReq, rp)

	chunks := parseOpenAISSEEvents(w.Body.String())

	// Accumulate tool-call arguments by index. A correct stream produces
	// exactly ONE tool call (one index) whose concatenated argument deltas
	// form the complete, valid final arguments object.
	type acc struct {
		id   string
		name string
		args string
	}
	byIndex := map[int]*acc{}
	for _, c := range chunks {
		choices, ok := c["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice := choices[0].(map[string]interface{})
		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}
		tcs, ok := delta["tool_calls"].([]interface{})
		if !ok {
			continue
		}
		for _, tci := range tcs {
			tc, ok := tci.(map[string]interface{})
			if !ok {
				continue
			}
			idx := -1
			if v, ok := tc["index"].(float64); ok {
				idx = int(v)
			}
			a := byIndex[idx]
			if a == nil {
				a = &acc{}
				byIndex[idx] = a
			}
			if id, ok := tc["id"].(string); ok && id != "" {
				a.id = id
			}
			if fn, ok := tc["function"].(map[string]interface{}); ok {
				if name, ok := fn["name"].(string); ok && name != "" {
					a.name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					a.args += args
				}
			}
		}
	}

	if len(byIndex) != 1 {
		t.Fatalf("expected exactly 1 tool call (one index), got %d: %+v", len(byIndex), byIndex)
	}
	a := byIndex[0]
	if a.name != "edit" {
		t.Errorf("expected tool name 'edit', got '%s'", a.name)
	}
	if a.id == "" {
		t.Error("expected tool call id to be set")
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(a.args), &got); err != nil {
		t.Fatalf("concatenated argument deltas are not valid JSON (%v): %s", err, a.args)
	}
	if got["file"] != "a.go" || got["old"] != "A" || got["new"] != "B" {
		t.Errorf("reconstructed args mismatch, expected file=a.go old=A new=B, got %v", got)
	}
	// The finish_reason must be tool_calls.
	var finishReason string
	for _, c := range chunks {
		if choices, ok := c["choices"].([]interface{}); ok && len(choices) > 0 {
			choice := choices[0].(map[string]interface{})
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				finishReason = fr
			}
		}
	}
	if finishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls', got '%s'", finishReason)
	}
}

// TestOllamaStreaming_ToolCallsCumulative reproduces the original bug on the
// Anthropic-inbound path: Ollama streams tool_calls cumulatively, and the
// proxy must emit a single tool_use block per call with the full arguments,
// not one block per chunk (which produced garbled/concatenated input JSON).
func TestOllamaStreaming_ToolCallsCumulative(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.g"}, false, "")
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.go"}, false, "")
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.go", "old": "A"}, false, "")
	upstreamBody += makeOllamaToolCallChunk("test-model", "call_1", "edit", map[string]interface{}{"file": "a.go", "old": "A", "new": "B"}, false, "")
	upstreamBody += makeOllamaChunk("test-model", "", "", true, "tool_call")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true, MaxTokens: 100}
	ollamaReq := &OllamaChatRequest{Model: "test", Stream: true}

	router.handleStreaming(w, req, ollamaReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())

	// Collect tool_use blocks (one content_block_start of type tool_use per
	// block) and concatenate input_json_delta partial_json per block index.
	toolUseStarts := 0
	toolUseStops := 0
	inputJSON := map[int]string{}
	currentToolIndex := -1
	for _, e := range events {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
			continue
		}
		switch e.Event {
		case "content_block_start":
			if block, ok := data["content_block"].(map[string]interface{}); ok {
				if block["type"] == "tool_use" {
					toolUseStarts++
					if idx, ok := data["index"].(float64); ok {
						currentToolIndex = int(idx)
					}
				}
			}
		case "content_block_delta":
			if delta, ok := data["delta"].(map[string]interface{}); ok {
				if delta["type"] == "input_json_delta" {
					if pj, ok := delta["partial_json"].(string); ok {
						inputJSON[currentToolIndex] += pj
					}
				}
			}
		case "content_block_stop":
			toolUseStops++
		}
	}

	if toolUseStarts != 1 {
		t.Errorf("expected exactly 1 tool_use content_block_start, got %d", toolUseStarts)
	}
	if toolUseStops < 1 {
		t.Errorf("expected at least 1 content_block_stop, got %d", toolUseStops)
	}
	if len(inputJSON) != 1 {
		t.Fatalf("expected input_json_delta for exactly 1 tool block, got %d: %+v", len(inputJSON), inputJSON)
	}
	// The single tool block's concatenated partial_json must be the complete
	// final arguments object (valid JSON, all keys present).
	var got map[string]interface{}
	joined := inputJSON[0]
	if err := json.Unmarshal([]byte(joined), &got); err != nil {
		t.Fatalf("tool_use input_json is not valid JSON (%v): %s", err, joined)
	}
	if got["file"] != "a.go" || got["old"] != "A" || got["new"] != "B" {
		t.Errorf("reconstructed tool input mismatch, expected file=a.go old=A new=B, got %v", got)
	}
}

// TestOpenAIStreaming_UsageAndCacheTokens verifies the terminal message_delta
// carries input_tokens (non-cached: prompt_tokens minus cached_tokens) and
// cache_read_input_tokens, so Claude Code learns the real prompt size for
// context-window tracking / auto-compaction. Regression for the bug where
// streaming message_delta.usage only ever contained output_tokens.
func TestOpenAIStreaming_UsageAndCacheTokens(t *testing.T) {
	// Final chunk carries usage with cached tokens; prompt_tokens includes
	// the cached portion (OpenAI convention), so input_tokens must be
	// 1000-700=300 and cache_read_input_tokens=700.
	textChunk := `data: {"id":"chatcmpl-u","object":"chat.completion.chunk","model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n"
	finishChunk := `data: {"id":"chatcmpl-u","object":"chat.completion.chunk","model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":5,"total_tokens":1005,"prompt_tokens_details":{"cached_tokens":700}}}` + "\n"
	upstreamBody := textChunk + finishChunk + "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "tencent/hy3:free", Stream: true, MaxTokens: 100}
	openAIReq := &OpenAIChatRequest{Model: "tencent/hy3:free", Stream: true}

	router.handleOpenAIStreaming(w, req, openAIReq, anthroReq, rp)

	events := parseSSEEvents(w.Body.String())
	var deltaUsage map[string]interface{}
	var startUsage map[string]interface{}
	for _, e := range events {
		if e.Event == "message_delta" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			deltaUsage, _ = data["usage"].(map[string]interface{})
		}
		if e.Event == "message_start" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if msg, ok := data["message"].(map[string]interface{}); ok {
				startUsage, _ = msg["usage"].(map[string]interface{})
			}
		}
	}
	if deltaUsage == nil {
		t.Fatal("missing message_delta.usage")
	}
	// input_tokens must be present and equal prompt-cached (300), not 1000.
	inputTokens, ok := deltaUsage["input_tokens"]
	if !ok {
		t.Fatalf("message_delta.usage missing input_tokens: %#v", deltaUsage)
	}
	if toIntVal(inputTokens) != 300 {
		t.Errorf("input_tokens: expected 300 (1000-700 cached), got %v", inputTokens)
	}
	if toIntVal(deltaUsage["output_tokens"]) != 5 {
		t.Errorf("output_tokens: expected 5, got %v", deltaUsage["output_tokens"])
	}
	if toIntVal(deltaUsage["cache_read_input_tokens"]) != 700 {
		t.Errorf("cache_read_input_tokens: expected 700, got %v", deltaUsage["cache_read_input_tokens"])
	}
	// message_start should still report zeros (usage arrives at the end).
	if startUsage == nil || toIntVal(startUsage["input_tokens"]) != 0 {
		t.Errorf("message_start.usage.input_tokens should be 0, got %#v", startUsage)
	}
	// The message id must not contain '/' or ':' (model was tencent/hy3:free).
	for _, e := range events {
		if e.Event == "message_start" {
			var data map[string]interface{}
			json.Unmarshal([]byte(e.Data), &data)
			if msg, ok := data["message"].(map[string]interface{}); ok {
				id, _ := msg["id"].(string)
				if strings.ContainsAny(id, "/:") {
					t.Errorf("message id should not contain / or :, got %q", id)
				}
			}
		}
	}
}

// toIntVal coerces a json.Unmarshal numeric (float64) to int for assertions.
func toIntVal(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
