package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// makeOllamaDoneChunkWithCache renders the native /api/chat done chunk that
// Ollama 0.33+ and Ollama Cloud return: the logical prompt total plus the
// cache-hit portion. Servers that predate cache reporting omit
// prompt_eval_cached_count entirely, which is what cached == 0 renders here.
func makeOllamaDoneChunkWithCache(model string, prompt, cached, eval int) string {
	chunk := map[string]interface{}{
		"model":             model,
		"message":           map[string]interface{}{"role": "assistant", "content": ""},
		"done":              true,
		"done_reason":       "stop",
		"prompt_eval_count": prompt,
		"eval_count":        eval,
	}
	if cached > 0 {
		chunk["prompt_eval_cached_count"] = cached
	}
	b, _ := json.Marshal(chunk)
	return string(b) + "\n"
}

// cachedChatResponse is the shared fixture: 12836 logical prompt tokens of
// which 12800 came from the prompt cache. Those are the numbers measured
// against Ollama Cloud on 2026-09-21 (deepseek-v4.1-flash).
func cachedChatResponse() *OllamaChatResponse {
	return &OllamaChatResponse{
		Model:                 "deepseek-v4.1-flash",
		Done:                  true,
		PromptEvalCount:       12836,
		PromptEvalCachedCount: 12800,
		EvalCount:             4,
	}
}

// Anthropic splits the logical prompt into input_tokens (non-cached) and
// cache_read_input_tokens. Ollama's prompt_eval_count includes the hits, so the
// split must subtract before reporting.
func TestOllamaCacheTokens_AnthropicNonStreaming(t *testing.T) {
	resp := translateResponse(cachedChatResponse(), &AnthropicRequest{Model: "deepseek-v4.1-flash"})

	if resp.Usage.InputTokens != 36 {
		t.Errorf("input_tokens: got %d, want 36 (12836 prompt - 12800 cached)", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 12800 {
		t.Errorf("cache_read_input_tokens: got %d, want 12800", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.OutputTokens != 4 {
		t.Errorf("output_tokens: got %d, want 4", resp.Usage.OutputTokens)
	}
}

// A server that predates cache reporting must produce exactly the old usage
// object — no cache_read_input_tokens key at all (the field is omitempty).
func TestOllamaCacheTokens_OlderServerUnchanged(t *testing.T) {
	ollama := cachedChatResponse()
	ollama.PromptEvalCachedCount = 0

	resp := translateResponse(ollama, &AnthropicRequest{Model: ollama.Model})
	if resp.Usage.InputTokens != 12836 {
		t.Errorf("input_tokens: got %d, want the full prompt 12836", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 0 {
		t.Errorf("cache_read_input_tokens: got %d, want 0", resp.Usage.CacheReadInputTokens)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "cache_read_input_tokens") {
		t.Errorf("usage should omit cache_read_input_tokens when nothing was cached: %s", body)
	}
}

// OpenAI's prompt_tokens is the logical total and keeps including the hits;
// the hits are reported alongside in prompt_tokens_details.cached_tokens.
func TestOllamaCacheTokens_OpenAINonStreaming(t *testing.T) {
	ollama := cachedChatResponse()
	resp := translateOllamaToOpenAI(ollama, &OpenAIChatRequest{Model: ollama.Model})

	if resp.Usage.PromptTokens != 12836 {
		t.Errorf("prompt_tokens: got %d, want the logical total 12836", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 4 {
		t.Errorf("completion_tokens: got %d, want 4", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 12840 {
		t.Errorf("total_tokens: got %d, want 12840", resp.Usage.TotalTokens)
	}
	if resp.Usage.PromptTokensDetails == nil {
		t.Fatal("prompt_tokens_details is missing")
	}
	if resp.Usage.PromptTokensDetails.CachedTokens != 12800 {
		t.Errorf("prompt_tokens_details.cached_tokens: got %d, want 12800", resp.Usage.PromptTokensDetails.CachedTokens)
	}
}

// The Responses surface rides the OpenAI translation, so it must expose the
// same numbers under input_tokens_details.
func TestOllamaCacheTokens_ResponsesNonStreaming(t *testing.T) {
	ollama := cachedChatResponse()
	resp := translateOllamaToResponsesAPI(ollama, &ResponsesAPIRequest{Model: ollama.Model}, nil, nil)

	if resp.Usage.InputTokens != 12836 {
		t.Errorf("input_tokens: got %d, want the logical total 12836", resp.Usage.InputTokens)
	}
	if resp.Usage.InputTokensDetails == nil {
		t.Fatal("input_tokens_details is missing")
	}
	if resp.Usage.InputTokensDetails.CachedTokens != 12800 {
		t.Errorf("input_tokens_details.cached_tokens: got %d, want 12800", resp.Usage.InputTokensDetails.CachedTokens)
	}
}

// Streaming native -> Anthropic: the done chunk carries both counts, and the
// terminal message_delta must report the split so Claude Code learns the real
// context size and the cache hit rate.
func TestOllamaCacheTokens_AnthropicStreaming(t *testing.T) {
	upstreamBody := makeOllamaChunk("test-model", "hi", "", false, "") +
		makeOllamaDoneChunkWithCache("test-model", 12836, 12800, 4)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"test","stream":true}`))
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

	usage := messageDeltaUsage(t, w.Body.String())
	if got := toIntVal(usage["input_tokens"]); got != 36 {
		t.Errorf("input_tokens: got %d, want 36 (12836 - 12800 cached)", got)
	}
	if got := toIntVal(usage["cache_read_input_tokens"]); got != 12800 {
		t.Errorf("cache_read_input_tokens: got %d, want 12800", got)
	}
	if got := toIntVal(usage["output_tokens"]); got != 4 {
		t.Errorf("output_tokens: got %d, want 4", got)
	}
}

// Streaming native -> OpenAI: the terminal usage chunk must carry
// prompt_tokens_details.cached_tokens.
func TestOllamaCacheTokens_OpenAIStreaming(t *testing.T) {
	upstreamBody := makeOllamaChunk("test-model", "hi", "", false, "") +
		makeOllamaDoneChunkWithCache("test-model", 12836, 12800, 4)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	router.handleOpenAIInboundOllamaStreaming(w, req, &OpenAIChatRequest{Model: "test", Stream: true}, rp)

	usage := lastOpenAIStreamUsage(t, w.Body.String())
	if got := toIntVal(usage["prompt_tokens"]); got != 12836 {
		t.Errorf("prompt_tokens: got %d, want the logical total 12836", got)
	}
	details, _ := usage["prompt_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("prompt_tokens_details missing from usage chunk: %#v", usage)
	}
	if got := toIntVal(details["cached_tokens"]); got != 12800 {
		t.Errorf("prompt_tokens_details.cached_tokens: got %d, want 12800", got)
	}
}

// Streaming native -> Responses: response.completed carries
// input_tokens_details.cached_tokens.
func TestOllamaCacheTokens_ResponsesStreaming(t *testing.T) {
	upstreamBody := makeOllamaChunk("test", "hi", "", false, "") +
		makeOllamaDoneChunkWithCache("test", 12836, 12800, 4)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	respReq := &ResponsesAPIRequest{Model: "test", Stream: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","stream":true}`))
	w := httptest.NewRecorder()

	router.handleResponsesAPIOllamaStreaming(w, r, respReq, rp, map[string]string{}, map[string]string{})

	usage := responseCompletedUsage(t, w.Body.String())
	if got := toIntVal(usage["input_tokens"]); got != 12836 {
		t.Errorf("input_tokens: got %d, want the logical total 12836", got)
	}
	details, _ := usage["input_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("input_tokens_details missing from usage: %#v", usage)
	}
	if got := toIntVal(details["cached_tokens"]); got != 12800 {
		t.Errorf("input_tokens_details.cached_tokens: got %d, want 12800", got)
	}
}

// Literal payloads captured from https://ollama.com/api/chat on 2026-09-21.
// They pin the wire contract this plumbing depends on: the warm run reports
// prompt_eval_cached_count next to the logical total, the cold run omits it.
func TestOllamaCacheTokens_RealCloudPayload(t *testing.T) {
	const warm = `{"model":"deepseek-v4.1-flash","created_at":"2026-09-21T10:53:59.237402911Z",` +
		`"message":{"role":"assistant","content":"","thinking":"We"},"done":true,` +
		`"done_reason":"length","total_duration":454353723,"prompt_eval_count":12836,` +
		`"prompt_eval_cached_count":12800,"eval_count":1}`
	const cold = `{"model":"deepseek-v4.1-flash","created_at":"2026-09-21T10:53:57.920624773Z",` +
		`"message":{"role":"assistant","content":"","thinking":"We"},"done":true,` +
		`"done_reason":"length","total_duration":492727414,"prompt_eval_count":12836,` +
		`"prompt_eval_cached_count":0,"eval_count":1}`
	// Servers older than the cache rollout send no field at all.
	const legacy = `{"model":"glm-5.2:cloud","message":{"role":"assistant","content":"hi"},` +
		`"done":true,"done_reason":"stop","prompt_eval_count":900,"eval_count":10}`

	var warmResp, coldResp, legacyResp OllamaChatResponse
	for name, tc := range map[string]struct {
		payload string
		out     *OllamaChatResponse
	}{
		"warm":   {warm, &warmResp},
		"cold":   {cold, &coldResp},
		"legacy": {legacy, &legacyResp},
	} {
		if err := json.Unmarshal([]byte(tc.payload), tc.out); err != nil {
			t.Fatalf("%s payload: %v", name, err)
		}
	}

	if got := warmResp.cachedPromptTokens(); got != 12800 {
		t.Errorf("warm cachedPromptTokens: got %d, want 12800", got)
	}
	if got := coldResp.cachedPromptTokens(); got != 0 {
		t.Errorf("cold cachedPromptTokens: got %d, want 0", got)
	}
	if got := legacyResp.cachedPromptTokens(); got != 0 {
		t.Errorf("legacy cachedPromptTokens: got %d, want 0", got)
	}

	// The warm payload is a 99.7% cache hit: Anthropic sees a 36-token prompt.
	got := translateResponse(&warmResp, &AnthropicRequest{Model: warmResp.Model})
	if got.Usage.InputTokens != 36 || got.Usage.CacheReadInputTokens != 12800 {
		t.Errorf("warm Anthropic usage: got input=%d cache_read=%d, want 36/12800",
			got.Usage.InputTokens, got.Usage.CacheReadInputTokens)
	}
	// The legacy payload keeps the pre-cache behaviour exactly.
	legacyUsage := translateResponse(&legacyResp, &AnthropicRequest{Model: legacyResp.Model})
	if legacyUsage.Usage.InputTokens != 900 || legacyUsage.Usage.CacheReadInputTokens != 0 {
		t.Errorf("legacy Anthropic usage: got input=%d cache_read=%d, want 900/0",
			legacyUsage.Usage.InputTokens, legacyUsage.Usage.CacheReadInputTokens)
	}
}

// A backend may report a hit count larger than the prompt total (or none at
// all). Clamping keeps input_tokens from going negative.
func TestCachedPromptTokensClamp(t *testing.T) {
	cases := []struct {
		name string
		resp OllamaChatResponse
		want int
	}{
		{"normal", OllamaChatResponse{PromptEvalCount: 100, PromptEvalCachedCount: 40}, 40},
		{"omitted by server", OllamaChatResponse{PromptEvalCount: 100}, 0},
		{"negative", OllamaChatResponse{PromptEvalCount: 100, PromptEvalCachedCount: -5}, 0},
		{"exceeds total", OllamaChatResponse{PromptEvalCount: 100, PromptEvalCachedCount: 140}, 100},
		{"no prompt total", OllamaChatResponse{PromptEvalCachedCount: 40}, 0},
	}
	for _, tc := range cases {
		if got := tc.resp.cachedPromptTokens(); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}

	// Fully cached prompt: input_tokens bottoms out at zero rather than going
	// negative, and the whole prompt is reported as a cache read.
	full := &OllamaChatResponse{Model: "m", PromptEvalCount: 100, PromptEvalCachedCount: 100, EvalCount: 1}
	got := translateResponse(full, &AnthropicRequest{Model: "m"})
	if got.Usage.InputTokens != 0 {
		t.Errorf("input_tokens: got %d, want 0 for a fully cached prompt", got.Usage.InputTokens)
	}
	if got.Usage.CacheReadInputTokens != 100 {
		t.Errorf("cache_read_input_tokens: got %d, want 100", got.Usage.CacheReadInputTokens)
	}
}

// messageDeltaUsage returns the usage object of the terminal message_delta
// event, which is where the Anthropic stream carries input/cache/output counts.
func messageDeltaUsage(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	for _, e := range parseSSEEvents(body) {
		if e.Event != "message_delta" {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(e.Data), &data); err != nil {
			t.Fatalf("message_delta payload is not JSON (%v): %s", err, e.Data)
		}
		if usage, ok := data["usage"].(map[string]interface{}); ok {
			return usage
		}
	}
	t.Fatalf("no message_delta.usage in stream:\n%s", body)
	return nil
}

// lastOpenAIStreamUsage returns the last non-null usage object in an OpenAI
// Chat Completions SSE stream (the usage-only final chunk).
func lastOpenAIStreamUsage(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var found map[string]interface{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if usage, ok := chunk["usage"].(map[string]interface{}); ok && usage != nil {
			found = usage
		}
	}
	if found == nil {
		t.Fatalf("no usage chunk in OpenAI stream:\n%s", body)
	}
	return found
}

// responseCompletedUsage returns the usage object of the response.completed
// event in a Responses API SSE stream.
func responseCompletedUsage(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	for _, e := range parseSSEEvents(body) {
		if e.Event != "response.completed" {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatalf("response.completed payload is not JSON (%v): %s", err, e.Data)
		}
		respObj, _ := payload["response"].(map[string]interface{})
		if respObj == nil {
			continue
		}
		if usage, ok := respObj["usage"].(map[string]interface{}); ok {
			return usage
		}
	}
	t.Fatalf("no response.completed usage in stream:\n%s", body)
	return nil
}
