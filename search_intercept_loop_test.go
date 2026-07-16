package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStaticProvider returns a fixed result list for tests.
type fakeStaticProvider struct {
	results []SearchResult
}

func (p *fakeStaticProvider) ID() string          { return "searxng" }
func (p *fakeStaticProvider) DisplayName() string { return "fake" }
func (p *fakeStaticProvider) NeedsKey() bool      { return false }
func (p *fakeStaticProvider) IsManaged() bool     { return false }
func (p *fakeStaticProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
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
	results := []SearchResult{
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
		{kind: "web_search_tool_result", toolID: "srvtoolu_test", results: []SearchResult{{Title: "Go 1.24", URL: "https://go.example"}}, searchErr: ""},
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

	pr := makeTestRouter(ts.URL)
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
	origReg := searchRegistry
	defer func() { searchRegistry = origReg }()
	searchRegistry = map[string]func(pc *SearchProviderConfig, client *http.Client) SearchProvider{
		"searxng": func(_ *SearchProviderConfig, c *http.Client) SearchProvider {
			return &fakeStaticProvider{results: []SearchResult{{Title: "Test Result", URL: "https://r.example"}}}
		},
	}
	globalSearchRunner.Reload(&SearchConfig{
		Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
		Providers: map[string]*SearchProviderConfig{"searxng": {Enabled: true}},
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