package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/search"
)

// fakeStaticProvider returns a fixed result list for tests.
type fakeStaticProvider struct {
	results []search.SearchResult
}

func (p *fakeStaticProvider) ID() string          { return "searxng" }
func (p *fakeStaticProvider) DisplayName() string { return "fake" }
func (p *fakeStaticProvider) NeedsKey() bool      { return false }
func (p *fakeStaticProvider) IsManaged() bool     { return false }
func (p *fakeStaticProvider) Search(ctx context.Context, q search.SearchQuery) ([]search.SearchResult, error) {
	return p.results, nil
}
func (p *fakeStaticProvider) Ping(ctx context.Context) error { return nil }

func TestDetectServerWebSearchTool(t *testing.T) {
	req := &AnthropicRequest{
		Tools: []AnthropicTool{
			{Type: "web_search_20250305", Name: "web_search", MaxUses: 3, AllowedDomains: []string{"a.com"}, BlockedDomains: []string{"b.com"}},
			{Name: "get_weather", InputSchema: map[string]interface{}{"type": "object"}},
		},
	}
	allowed, blocked, maxUses, found := detectServerWebSearchTool(req)
	if !found {
		t.Fatal("expected to find server web_search tool")
	}
	if maxUses != 3 || len(allowed) != 1 || allowed[0] != "a.com" || blocked[0] != "b.com" {
		t.Errorf("unexpected: allowed=%v blocked=%v maxUses=%d", allowed, blocked, maxUses)
	}

	req2 := &AnthropicRequest{Tools: []AnthropicTool{{Name: "web_search"}}}
	if _, _, _, found := detectServerWebSearchTool(req2); found {
		t.Fatal("plain function tool must not be detected as server tool")
	}
}

func TestBuildSearchResultsPayload(t *testing.T) {
	results := []search.SearchResult{
		{Title: "T1", URL: "https://1.example", Snippet: "S1", PageAge: "2025"},
		{Title: "T2", URL: "https://2.example"},
	}
	payload := buildSearchResultsPayload("q", results, "")
	if !strings.Contains(payload, "T1") || !strings.Contains(payload, "https://1.example") || !strings.Contains(payload, "S1") {
		t.Errorf("payload missing result content: %q", payload)
	}
	errPayload := buildSearchResultsPayload("q", nil, "boom")
	if !strings.Contains(errPayload, "failed") {
		t.Errorf("error payload missing failure note: %q", errPayload)
	}
}

func TestChunkText(t *testing.T) {
	if got := chunkText("", 10); len(got) != 0 {
		t.Errorf("empty input should yield no chunks, got %v", got)
	}
	if got := chunkText("short", 10); len(got) != 1 || got[0] != "short" {
		t.Errorf("short input: %v", got)
	}
	chunks := chunkText("the quick brown fox jumps over the lazy dog", 15)
	joined := strings.Join(chunks, "")
	if joined != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("chunked join mismatch: %q", joined)
	}
	for _, c := range chunks {
		if len(c) > 15 {
			t.Errorf("chunk exceeds max: %q (len %d)", c, len(c))
		}
	}
}

func TestEmitServerToolWebSearchStream(t *testing.T) {
	pr := &ProviderRouter{}
	req := &AnthropicRequest{Model: "glm-5.1:cloud", Stream: true}
	blocks := []emittedBlock{
		{kind: "server_tool_use", toolID: "srvtoolu_test", query: "golang 1.24"},
		{kind: "web_search_tool_result", toolID: "srvtoolu_test", results: []search.SearchResult{{Title: "Go 1.24", URL: "https://go.example"}}, searchErr: ""},
		{kind: "text", text: "Go 1.24 released with new features."},
	}
	rr := httptest.NewRecorder()
	pr.emitServerToolWebSearchStream(rr, req, blocks, 1, 50)
	body := rr.Body.String()
	for _, want := range []string{
		"event: message_start",
		"server_tool_use",
		"srvtoolu_test",
		"input_json_delta",
		"web_search_tool_result",
		"web_search_result",
		"https://go.example",
		"text_delta",
		"Go 1.24 released",
		"end_turn",
		"web_search_requests",
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandleServerWebSearchLoop simulates one search iteration against a fake
// upstream, verifying the composed stream contains a server_tool_use + result
// and the final text.
func TestHandleServerWebSearchLoop(t *testing.T) {
	// Swap in a fake provider router whose client talks to a test server that
	// returns a web_search tool call on the first request and final text on the
	// second.
	var callCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// First call: model requests a web search.
			json.NewEncoder(w).Encode(OllamaChatResponse{
				Model:      "glm-5.1:cloud",
				Message:    OllamaMessage{Content: "Let me search.", ToolCalls: []OllamaToolCall{{ID: "call_1", Function: OllamaToolCallFunction{Name: "web_search", Arguments: map[string]interface{}{"query": "test query"}}}}},
				Done:       true,
				DoneReason: "tool_call",
			})
			return
		}
		// Second call: final answer after receiving the tool result.
		json.NewEncoder(w).Encode(OllamaChatResponse{
			Model:      "glm-5.1:cloud",
			Message:    OllamaMessage{Content: "Here is the final answer based on the search."},
			Done:       true,
			DoneReason: "stop",
		})
	}))
	defer ts.Close()

	// A router whose catalog knows the model with a restricted effort list, so
	// the validation inside the search loop is actually exercised: "medium"
	// (derived from the thinking budget) must be rewritten to the first allowed
	// value before the request leaves Prism.
	pr := NewRouter(&config.Config{
		DefaultProvider: "custom_test",
		CustomProviders: []*config.ProviderConfig{
			{ID: "custom_test", Name: "Test", BaseURL: ts.URL, APIKey: "test-key"},
		},
	}, &config.ModelRemapping{
		DefaultModel: "deepseek-v4.1-flash:cloud",
		KnownModels: []config.ModelEntry{{
			ID:              "deepseek-v4.1-flash",
			Provider:        "custom_test",
			Reasoning:       true,
			ReasoningEffort: []string{"low", "high", "max"},
		}},
		Aliases: map[string]string{},
	})
	rp := makeTestRP(ts.URL, "ollama")

	req := &AnthropicRequest{
		Model:     "glm-5.1:cloud",
		MaxTokens: 1024,
		Stream:    true,
		Messages:  []AnthropicMessage{{Role: "user", Content: "search the web for test query"}},
		Tools: []AnthropicTool{
			{Type: "web_search_20250305", Name: "web_search"},
		},
	}

	// Seed the runner with a fake provider so runSearch succeeds.
	search.RegisterProviderForTest("searxng", func(_ *search.ProviderConfig, c *http.Client) search.SearchProvider {
		return &fakeStaticProvider{results: []search.SearchResult{{Title: "Test Result", URL: "https://r.example"}}}
	})
	search.Global.Reload(&search.Config{
		Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
		Providers: map[string]*search.ProviderConfig{"searxng": {Enabled: true}},
	})

	httpReq := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(""))
	rr := httptest.NewRecorder()
	handled := pr.handleServerWebSearchLoop(rr, httpReq, req, rp)
	if !handled {
		t.Fatal("expected handleServerWebSearchLoop to handle the request")
	}
	if callCount != 2 {
		t.Errorf("expected 2 upstream calls, got %d", callCount)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"server_tool_use", "web_search_tool_result", "Test Result",
		"Here is the final answer", "end_turn", "message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("composed stream missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandleServerWebSearchLoopChatCompletionsUpstream covers the
// OpenAI-compatible branch added for Ollama Cloud: the typed web_search server
// tool must be rewritten to a function tool, the upstream is reached over
// /v1/chat/completions, the intercepted call is answered with a tool message,
// and any reasoning from a reasoning vendor is replayed as reasoning_content.
func TestHandleServerWebSearchLoopChatCompletionsUpstream(t *testing.T) {
	var bodies []string
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			reasoning := "I should search for that."
			json.NewEncoder(w).Encode(OpenAIChatResponse{
				Model: "deepseek-v4.1-flash:cloud",
				Choices: []OpenAIChoice{{
					Message: OpenAIChatMessage{
						Role:             "assistant",
						Content:          "Let me look that up.",
						ReasoningContent: &reasoning,
						ToolCalls: []OpenAIToolCall{{
							ID:       "call_1",
							Type:     "function",
							Function: OpenAIToolCallFunc{Name: "web_search", Arguments: `{"query":"test query"}`},
						}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: OpenAIUsage{PromptTokens: 100, CompletionTokens: 20},
			})
			return
		}
		json.NewEncoder(w).Encode(OpenAIChatResponse{
			Model: "deepseek-v4.1-flash:cloud",
			Choices: []OpenAIChoice{{
				Message:      OpenAIChatMessage{Role: "assistant", Content: "Final answer from search."},
				FinishReason: "stop",
			}},
			Usage: OpenAIUsage{PromptTokens: 200, CompletionTokens: 30},
		})
	}))
	defer ts.Close()

	// A router whose catalog knows the model with a restricted effort list, so
	// the validation inside the search loop is actually exercised: "medium"
	// (derived from the thinking budget) must be rewritten to the first allowed
	// value before the request leaves Prism.
	pr := NewRouter(&config.Config{
		DefaultProvider: "custom_test",
		CustomProviders: []*config.ProviderConfig{
			{ID: "custom_test", Name: "Test", BaseURL: ts.URL, APIKey: "test-key"},
		},
	}, &config.ModelRemapping{
		DefaultModel: "deepseek-v4.1-flash:cloud",
		KnownModels: []config.ModelEntry{{
			ID:              "deepseek-v4.1-flash",
			Provider:        "custom_test",
			Reasoning:       true,
			ReasoningEffort: []string{"low", "high", "max"},
		}},
		Aliases: map[string]string{},
	})
	rp := makeTestRP(ts.URL, "openai")

	budget := 20000
	req := &AnthropicRequest{
		Model:     "deepseek-v4.1-flash:cloud",
		MaxTokens: 1024,
		Stream:    true,
		Thinking:  &AnthropicThinking{Type: "enabled", BudgetTokens: budget},
		Messages:  []AnthropicMessage{{Role: "user", Content: "search the web for test query"}},
		Tools: []AnthropicTool{
			{Type: "web_search_20250305", Name: "web_search"},
		},
	}

	search.RegisterProviderForTest("searxng", func(_ *search.ProviderConfig, c *http.Client) search.SearchProvider {
		return &fakeStaticProvider{results: []search.SearchResult{{Title: "Test Result", URL: "https://r.example"}}}
	})
	search.Global.Reload(&search.Config{
		Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
		Providers: map[string]*search.ProviderConfig{"searxng": {Enabled: true}},
	})

	httpReq := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(""))
	rr := httptest.NewRecorder()
	if !pr.handleServerWebSearchLoop(rr, httpReq, req, rp) {
		t.Fatal("expected handleServerWebSearchLoop to handle the request")
	}

	if len(paths) != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", len(paths))
	}
	for _, p := range paths {
		if p != "/v1/chat/completions" {
			t.Errorf("upstream path = %q, want /v1/chat/completions", p)
		}
	}

	// First request: the typed server tool must have become a plain function.
	if !strings.Contains(bodies[0], `"name":"web_search"`) {
		t.Errorf("first request missing web_search function tool:\n%s", bodies[0])
	}
	if strings.Contains(bodies[0], "web_search_20250305") {
		t.Errorf("typed server tool leaked upstream:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[0], `"reasoning_effort":"high"`) {
		t.Errorf("reasoning_effort missing from first request:\n%s", bodies[0])
	}

	// Second request: the intercepted call must be answered, and the reasoning
	// vendor's thinking replayed so DeepSeek-style backends accept the history.
	for _, want := range []string{`"tool_call_id":"call_1"`, `"role":"tool"`, `I should search for that.`} {
		if !strings.Contains(bodies[1], want) {
			t.Errorf("second request missing %q:\n%s", want, bodies[1])
		}
	}

	body := rr.Body.String()
	for _, want := range []string{
		"server_tool_use", "web_search_tool_result", "Test Result",
		"Final answer from search.", "message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("composed stream missing %q\n--- body ---\n%s", want, body)
		}
	}
}
