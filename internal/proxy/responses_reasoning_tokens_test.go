package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/search"
)

// ollamaDoneChunkWithThinking builds a terminal native /api/chat chunk with an
// explicit usage pair, so tests control output_tokens independently of the
// thinking text.
func ollamaDoneChunkWithThinking(model, content, thinking string, outputTokens, promptTokens int) string {
	msg := map[string]interface{}{"role": "assistant", "content": content}
	if thinking != "" {
		msg["thinking"] = thinking
	}
	chunk := map[string]interface{}{
		"model":             model,
		"message":           msg,
		"done":              true,
		"done_reason":       "stop",
		"eval_count":        outputTokens,
		"prompt_eval_count": promptTokens,
	}
	b, _ := json.Marshal(chunk)
	return string(b) + "\n"
}

// extractResponsesUsage returns the usage object of the terminal
// response.completed event in a Responses SSE stream.
func extractResponsesUsage(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	usage := responseCompletedUsage(t, body)
	if usage == nil {
		t.Fatal("response.completed carried no usage object")
	}
	return usage
}

// Upstreams that report completion_tokens_details.reasoning_tokens must have
// that number forwarded verbatim (Grok Build's strict client requires the
// field, and the old code hard-coded 0).
func TestResponsesStreaming_ReasoningTokensFromUpstreamDetails(t *testing.T) {
	upstreamBody := makeOpenAIReasoningChunk("thinking hard", "", "") +
		makeOpenAIReasoningChunk("", "final answer", "stop") +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,` +
		`"model":"deepseek-v4.1-flash:cloud","choices":[],"usage":` +
		`{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,` +
		`"prompt_tokens_details":{"cached_tokens":40},` +
		`"completion_tokens_details":{"reasoning_tokens":37}}}` + "\n" +
		"data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	respReq := &ResponsesAPIRequest{Model: "deepseek-v4.1-flash:cloud", Stream: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`))
	w := httptest.NewRecorder()

	router.handleResponsesAPIOpenAIStreaming(w, r, respReq, rp, map[string]string{}, map[string]string{})

	usage := extractResponsesUsage(t, w.Body.String())
	details, _ := usage["output_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("output_tokens_details missing from usage: %#v", usage)
	}
	if got := toIntVal(details["reasoning_tokens"]); got != 37 {
		t.Errorf("reasoning_tokens: got %d, want the upstream 37", got)
	}
	if got := toIntVal(usage["output_tokens"]); got != 50 {
		t.Errorf("output_tokens: got %d, want 50", got)
	}
}

// Ollama Cloud omits completion_tokens_details entirely. The breakdown must
// then be derived from the streamed thinking text (chars/4) instead of the old
// hard-coded 0.
//
// This is the OpenAI-compatible streaming path (Ollama Cloud now speaks Chat
// Completions), so the reasoning arrives as "reasoning" deltas rather than a
// native message.thinking field.
func TestResponsesStreamingOpenAI_ReasoningTokensTrackedFallback(t *testing.T) {
	const thinking = "Let me reason carefully about the answer to this question." // 57 chars
	upstreamBody := makeOpenAIReasoningChunk(thinking, "", "") +
		makeOpenAIReasoningChunk("", "final answer", "stop") +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,` +
		`"model":"deepseek-v4.1-flash:cloud","choices":[],"usage":` +
		`{"prompt_tokens":100,"completion_tokens":200,"total_tokens":300}}` + "\n" +
		"data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	respReq := &ResponsesAPIRequest{Model: "deepseek-v4.1-flash:cloud", Stream: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`))
	w := httptest.NewRecorder()

	router.handleResponsesAPIOpenAIStreaming(w, r, respReq, rp, map[string]string{}, map[string]string{})

	usage := extractResponsesUsage(t, w.Body.String())
	details, _ := usage["output_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("output_tokens_details missing from usage: %#v", usage)
	}
	want := estimatedReasoningTokens(thinking)
	if got := toIntVal(details["reasoning_tokens"]); got != want {
		t.Errorf("reasoning_tokens: got %d, want the tracked %d", got, want)
	}
	if got := toIntVal(details["reasoning_tokens"]); got <= 0 {
		t.Errorf("reasoning_tokens: got %d, want > 0 for a reasoning stream", got)
	}
}

// Ollama's native /api/chat surface reports no split at all; the same fallback
// applies there.
func TestResponsesStreaming_ReasoningTokensTrackedFallback(t *testing.T) {
	const thinking = "Let me reason carefully about the answer to this question." // 57 chars
	upstreamBody := makeOllamaChunk("test-model", "", thinking, false, "") +
		ollamaDoneChunkWithThinking("test-model", "The answer.", "", 500, 900)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	respReq := &ResponsesAPIRequest{Model: "test", Stream: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`))
	w := httptest.NewRecorder()

	router.handleResponsesAPIOllamaStreaming(w, r, respReq, rp, map[string]string{}, map[string]string{})

	usage := extractResponsesUsage(t, w.Body.String())
	details, _ := usage["output_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("output_tokens_details missing from usage: %#v", usage)
	}
	want := estimatedReasoningTokens(thinking)
	if got := toIntVal(details["reasoning_tokens"]); got != want {
		t.Errorf("reasoning_tokens: got %d, want the tracked %d (chars/4 of %d)", got, want, len(thinking))
	}
	if got := toIntVal(details["reasoning_tokens"]); got <= 0 {
		t.Errorf("reasoning_tokens: got %d, want > 0 for a reasoning stream", got)
	}
	if got := toIntVal(usage["output_tokens"]); got != 500 {
		t.Errorf("output_tokens: got %d, want the reported 500", got)
	}
}

// The tracked estimate must never exceed the reported output total: a model
// that streams verbose thinking with a small eval_count must still produce a
// self-consistent breakdown.
func TestResponsesStreaming_ReasoningTokensClampedToOutput(t *testing.T) {
	thinking := strings.Repeat("reasoning text ", 40) // ~600 chars -> ~150 estimated
	upstreamBody := makeOllamaChunk("test-model", "", thinking, false, "") +
		ollamaDoneChunkWithThinking("test-model", "ok", "", 20, 12)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	respReq := &ResponsesAPIRequest{Model: "test", Stream: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`))
	w := httptest.NewRecorder()

	router.handleResponsesAPIOllamaStreaming(w, r, respReq, rp, map[string]string{}, map[string]string{})

	usage := extractResponsesUsage(t, w.Body.String())
	details, _ := usage["output_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("output_tokens_details missing from usage: %#v", usage)
	}
	out := toIntVal(usage["output_tokens"])
	if got := toIntVal(details["reasoning_tokens"]); got != out {
		t.Errorf("reasoning_tokens: got %d, want it clamped to output_tokens=%d", got, out)
	}
}

// The search-interception loop must carry the same reasoning breakdown on its
// response.completed usage (one thinking turn per upstream call).
func TestResponsesSearchLoop_ReasoningTokensTracked(t *testing.T) {
	const thinking = "I should search for the release notes."
	var callCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			args, _ := json.Marshal(map[string]string{"query": "golang 1.24"})
			th := thinking
			json.NewEncoder(w).Encode(OpenAIChatResponse{
				Model: "m",
				Choices: []OpenAIChoice{{
					Index: 0,
					Message: OpenAIChatMessage{
						Role:      "assistant",
						Reasoning: &th,
						ToolCalls: []OpenAIToolCall{{ID: "call_ws_1", Type: "function", Function: OpenAIToolCallFunc{Name: "web_search", Arguments: string(args)}}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: OpenAIUsage{PromptTokens: 50, CompletionTokens: 10},
			})
			return
		}
		json.NewEncoder(w).Encode(OpenAIChatResponse{
			Model: "m",
			Choices: []OpenAIChoice{{
				Index:        0,
				Message:      OpenAIChatMessage{Role: "assistant", Content: "Go 1.24 is out."},
				FinishReason: "stop",
			}},
			Usage: OpenAIUsage{PromptTokens: 60, CompletionTokens: 20},
		})
	}))
	defer ts.Close()

	pr := makeTestRouter(ts.URL)
	rp := makeTestRP(ts.URL, "openai")
	search.RegisterProviderForTest("searxng", func(_ *search.ProviderConfig, c *http.Client) search.SearchProvider {
		return &fakeStaticProvider{results: []search.SearchResult{{Title: "Go 1.24", URL: "https://go.example/1.24"}}}
	})
	search.Global.Reload(&search.Config{
		Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
		Providers: map[string]*search.ProviderConfig{"searxng": {Enabled: true}},
	})

	allTools := []interface{}{map[string]interface{}{"type": "web_search"}}
	respReq := &ResponsesAPIRequest{
		Model:  "m",
		Stream: true,
		Input:  []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "search for go 1.24"}},
		Tools:  allTools,
	}
	httpReq := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(""))
	rr := httptest.NewRecorder()
	if !pr.handleResponsesWebSearchLoop(rr, httpReq, respReq, rp, map[string]string{"web_search": "web_search"}, map[string]string{}, allTools) {
		t.Fatal("expected handler to take the request")
	}

	usage := extractResponsesUsage(t, rr.Body.String())
	details, _ := usage["output_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("output_tokens_details missing from usage: %#v", usage)
	}
	want := estimatedReasoningTokens(thinking)
	if got := toIntVal(details["reasoning_tokens"]); got != want {
		t.Errorf("reasoning_tokens: got %d, want %d", got, want)
	}
}

// When the upstream reports its own completion_tokens_details.reasoning_tokens
// the search-interception loop must forward that number rather than estimating
// from thinking text. The reported value is deliberately different from the
// estimate so the assertion can tell the two paths apart.
func TestResponsesSearchLoop_ReasoningTokensFromUpstream(t *testing.T) {
	const thinking = "I should look this up before answering."
	const reported = 7 // deliberately below estimatedReasoningTokens(thinking)
	var callCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			args, _ := json.Marshal(map[string]string{"query": "go 1.24 release"})
			th := thinking
			json.NewEncoder(w).Encode(OpenAIChatResponse{
				Model: "m",
				Choices: []OpenAIChoice{{
					Index: 0,
					Message: OpenAIChatMessage{
						Role:      "assistant",
						Reasoning: &th,
						ToolCalls: []OpenAIToolCall{{ID: "call_ws_1", Type: "function", Function: OpenAIToolCallFunc{Name: "web_search", Arguments: string(args)}}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: OpenAIUsage{
					PromptTokens:            50,
					CompletionTokens:        40,
					CompletionTokensDetails: &OpenAICompletionTokensDetails{ReasoningTokens: reported},
				},
			})
			return
		}
		json.NewEncoder(w).Encode(OpenAIChatResponse{
			Model: "m",
			Choices: []OpenAIChoice{{
				Index:        0,
				Message:      OpenAIChatMessage{Role: "assistant", Content: "Go 1.24 is out."},
				FinishReason: "stop",
			}},
			Usage: OpenAIUsage{
				PromptTokens:     60,
				CompletionTokens: 20,
			},
		})
	}))
	defer ts.Close()

	pr := makeTestRouter(ts.URL)
	rp := makeTestRP(ts.URL, "openai")
	search.RegisterProviderForTest("searxng", func(_ *search.ProviderConfig, c *http.Client) search.SearchProvider {
		return &fakeStaticProvider{results: []search.SearchResult{{Title: "Go 1.24", URL: "https://go.example/1.24"}}}
	})
	search.Global.Reload(&search.Config{
		Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
		Providers: map[string]*search.ProviderConfig{"searxng": {Enabled: true}},
	})

	allTools := []interface{}{map[string]interface{}{"type": "web_search"}}
	respReq := &ResponsesAPIRequest{
		Model:  "m",
		Stream: true,
		Input:  []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "search for go 1.24"}},
		Tools:  allTools,
	}
	httpReq := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(""))
	rr := httptest.NewRecorder()
	if !pr.handleResponsesWebSearchLoop(rr, httpReq, respReq, rp, map[string]string{"web_search": "web_search"}, map[string]string{}, allTools) {
		t.Fatal("expected handler to take the request")
	}

	usage := extractResponsesUsage(t, rr.Body.String())
	details, _ := usage["output_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("output_tokens_details missing from usage: %#v", usage)
	}
	if got := toIntVal(details["reasoning_tokens"]); got != reported {
		t.Errorf("reasoning_tokens: got %d, want the upstream-reported %d (estimate would be %d)",
			got, reported, estimatedReasoningTokens(thinking))
	}
}

// Non-streaming Responses translation forwards the upstream breakdown, and the
// field is present even when zero (Grok Build's strict client requires it).
func TestResponsesNonStream_ReasoningTokensDetails(t *testing.T) {
	resp := &OpenAIChatResponse{
		Model: "m",
		Choices: []OpenAIChoice{{
			Index:        0,
			Message:      OpenAIChatMessage{Role: "assistant", Content: "hello"},
			FinishReason: "stop",
		}},
		Usage: OpenAIUsage{
			PromptTokens:            100,
			CompletionTokens:        40,
			TotalTokens:             140,
			CompletionTokensDetails: &OpenAICompletionTokensDetails{ReasoningTokens: 33},
		},
	}
	out := translateChatCompletionsToResponsesAPI(resp, &ResponsesAPIRequest{Model: "m"}, nil, nil)
	if out.Usage.OutputTokensDetails == nil {
		t.Fatal("output_tokens_details missing")
	}
	if out.Usage.OutputTokensDetails.ReasoningTokens != 33 {
		t.Errorf("reasoning_tokens: got %d, want 33", out.Usage.OutputTokensDetails.ReasoningTokens)
	}

	// Zero breakdown must still serialize the key.
	zero := &OpenAIChatResponse{
		Model:   "m",
		Choices: []OpenAIChoice{{Index: 0, Message: OpenAIChatMessage{Role: "assistant", Content: "hi"}, FinishReason: "stop"}},
		Usage:   OpenAIUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	outZero := translateChatCompletionsToResponsesAPI(zero, &ResponsesAPIRequest{Model: "m"}, nil, nil)
	b, _ := json.Marshal(outZero.Usage)
	if !strings.Contains(string(b), `"output_tokens_details":{"reasoning_tokens":0}`) {
		t.Errorf("zero reasoning_tokens must stay present, got %s", b)
	}
	// The upstream API only nests reasoning_tokens under output_tokens_details;
	// input_tokens_details must not grow the field just because the output side
	// always serializes it.
	if strings.Contains(string(b), `"input_tokens_details":{"reasoning_tokens"`) {
		t.Errorf("input_tokens_details must not carry reasoning_tokens, got %s", b)
	}
}

// The OpenAI-compatible /v1/chat/completions surface gets the same treatment:
// Ollama-native streaming has no split, so the terminal usage chunk must carry
// a derived completion_tokens_details.reasoning_tokens instead of omitting it.
func TestOpenAIInboundOllamaStreaming_ReasoningTokensDetails(t *testing.T) {
	const thinking = "Let me reason carefully about the answer to this question."
	upstreamBody := makeOllamaChunk("test-model", "", thinking, false, "") +
		ollamaDoneChunkWithThinking("test-model", "Hello", "", 200, 50)

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
	details, _ := usage["completion_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatalf("completion_tokens_details missing from usage chunk: %#v", usage)
	}
	if got, want := toIntVal(details["reasoning_tokens"]), estimatedReasoningTokens(thinking); got != want {
		t.Errorf("completion_tokens_details.reasoning_tokens: got %d, want %d", got, want)
	}
}
