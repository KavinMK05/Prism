package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ollama-proxy/internal/config"
)

// makeResponsesSSEEvent builds one Responses API SSE data line.
func makeResponsesSSEEvent(v map[string]interface{}) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

func TestGenericResponsesToAnthropicStream_TextThinkingTool(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.created"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.reasoning_summary_text.delta", "delta": "thinking"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.output_text.delta", "delta": "Hello"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.output_text.delta", "delta": " world"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.output_item.added", "item": map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "get_weather"}})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.function_call_arguments.delta", "delta": `{"city":`})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.function_call_arguments.delta", "delta": `"SF"}`})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{
		"type":     "response.completed",
		"response": map[string]interface{}{"status": "completed", "usage": map[string]interface{}{"input_tokens": 10, "output_tokens": 5}},
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true}
	resp, err := http.Post(upstream.URL, "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	defer resp.Body.Close()

	router.translateGenericResponsesToAnthropicStream(w, req, resp, anthroReq, rp, "test", reqStartForTest())

	events := parseSSEEvents(w.Body.String())
	eventTypes := []string{}
	for _, e := range events {
		eventTypes = append(eventTypes, e.Event)
	}

	if eventTypes[0] != "message_start" {
		t.Errorf("first event = %s, want message_start", eventTypes[0])
	}
	if eventTypes[len(eventTypes)-1] != "message_stop" {
		t.Errorf("last event = %s, want message_stop", eventTypes[len(eventTypes)-1])
	}

	// Structural checks: decode every event and look for the required shapes.
	var sawThinking, sawTextHello, sawTextWorld, sawToolStart, sawStopToolUse, sawUsage bool
	var argsJSON strings.Builder
	for _, e := range events {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(e.Data), &m); err != nil {
			continue
		}
		switch m["type"] {
		case "content_block_delta":
			delta, _ := m["delta"].(map[string]interface{})
			switch delta["type"] {
			case "thinking_delta":
				sawThinking = true
			case "text_delta":
				if delta["text"] == "Hello" {
					sawTextHello = true
				}
				if delta["text"] == " world" {
					sawTextWorld = true
				}
			case "input_json_delta":
				if s, ok := delta["partial_json"].(string); ok {
					argsJSON.WriteString(s)
				}
			}
		case "content_block_start":
			block, _ := m["content_block"].(map[string]interface{})
			if block["type"] == "tool_use" && block["name"] == "get_weather" {
				sawToolStart = true
			}
		case "message_delta":
			if mdelta, ok := m["delta"].(map[string]interface{}); ok && m["delta"] != nil {
				if mdelta["stop_reason"] == "tool_use" {
					sawStopToolUse = true
				}
			}
			if usage, ok := m["usage"].(map[string]interface{}); ok && usage["output_tokens"] == float64(5) {
				sawUsage = true
			}
		}
	}
	sawArgsDelta := argsJSON.String() == `{"city":"SF"}`

	checks := []struct {
		name string
		ok   bool
	}{
		{"thinking delta", sawThinking},
		{"text delta Hello", sawTextHello},
		{"text delta world", sawTextWorld},
		{"tool_use block start", sawToolStart},
		{"tool args delta", sawArgsDelta},
		{"stop_reason tool_use", sawStopToolUse},
		{"message_delta usage output_tokens 5", sawUsage},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("SSE stream missing %s\ngot: %s", c.name, w.Body.String())
		}
	}
}

func TestGenericResponsesToAnthropicJSON_NonStreaming(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.output_text.delta", "delta": "Hi"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.output_item.added", "item": map[string]interface{}{"type": "function_call", "call_id": "call_9", "name": "lookup"}})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.function_call_arguments.delta", "delta": `{"q":1}`})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{
		"type":     "response.completed",
		"response": map[string]interface{}{"status": "completed", "usage": map[string]interface{}{"input_tokens": 7, "output_tokens": 3}},
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{
		Model:     "test",
		MaxTokens: 100,
		Messages:  []AnthropicMessage{{Role: "user", Content: "hi"}},
	}
	resp, err := http.Post(upstream.URL, "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	defer resp.Body.Close()

	router.translateGenericResponsesToAnthropicJSON(w, req, resp, anthroReq, rp, "test", reqStartForTest())

	var got AnthropicResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode anthropic response: %v\nbody: %s", err, w.Body.String())
	}
	if got.Type != "message" || got.Role != "assistant" {
		t.Errorf("type/role = %s/%s, want message/assistant", got.Type, got.Role)
	}
	var sawText, sawTool bool
	for _, block := range got.Content {
		bm, _ := block.(map[string]interface{})
		switch bm["type"] {
		case "text":
			if bm["text"] == "Hi" {
				sawText = true
			}
		case "tool_use":
			// translateFromOpenAI sanitizes upstream tool IDs; only require a
			// stable non-empty id carrying the upstream name.
			if bm["name"] == "lookup" && bm["id"] != "" {
				sawTool = true
			}
		}
	}
	if !sawText || !sawTool {
		t.Errorf("content blocks missing text=%v tool=%v: %v", sawText, sawTool, got.Content)
	}
	if got.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", got.StopReason)
	}
	if got.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v, want output_tokens 3", got.Usage)
	}
}

// The per-model routing fix (m1): identical upstream IDs under two providers
// with different API values must resolve per provider, not first-entry-wins.
func TestGetModelAPI_ProviderQualified(t *testing.T) {
	cfg := &config.Config{DefaultProvider: "p1"}
	remap := &config.ModelRemapping{
		KnownModels: []config.ModelEntry{
			{ID: "m", Provider: "p1", API: "chat_completions"},
			{ID: "m", Provider: "p2", API: "responses"},
		},
		Aliases: map[string]string{},
	}
	router := NewRouter(cfg, remap)

	if got := router.getModelAPI("m", "p1"); got != "chat_completions" {
		t.Errorf("getModelAPI(m, p1) = %q, want chat_completions", got)
	}
	if got := router.getModelAPI("m", "p2"); got != "responses" {
		t.Errorf("getModelAPI(m, p2) = %q, want responses", got)
	}
	// Suffix-carrying resolved models still match the right entry.
	if got := router.getModelAPI("m:high", "p2"); got != "responses" {
		t.Errorf("getModelAPI(m:high, p2) = %q, want responses", got)
	}
	// Unknown model falls back to chat_completions.
	if got := router.getModelAPI("unknown", "p1"); got != "chat_completions" {
		t.Errorf("getModelAPI(unknown, p1) = %q, want chat_completions", got)
	}
}

// reqStartForTest supplies a request start timestamp for translator calls.
func reqStartForTest() time.Time { return time.Now() }