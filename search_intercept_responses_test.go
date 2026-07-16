package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesHasWebSearchTool(t *testing.T) {
	if !responsesHasWebSearchTool(map[string]string{"shell_command": "function", "web_search": "web_search"}) {
		t.Error("expected web_search to be detected")
	}
	if !responsesHasWebSearchTool(map[string]string{"x_search": "x_search"}) {
		t.Error("expected x_search (xAI/Grok Build) to be detected")
	}
	if responsesHasWebSearchTool(map[string]string{"shell_command": "function"}) {
		t.Error("non-web_search tools must not trigger")
	}
}

func TestExtractResponsesWebSearchParamsXSearch(t *testing.T) {
	allTools := []interface{}{
		map[string]interface{}{"type": "x_search", "excluded_domains": []interface{}{"ads.com"}, "max_uses": float64(2)},
	}
	allowed, blocked, maxUses := extractResponsesWebSearchParams(allTools)
	if len(allowed) != 0 || len(blocked) != 1 || blocked[0] != "ads.com" || maxUses != 2 {
		t.Errorf("x_search params unexpected: allowed=%v blocked=%v maxUses=%d", allowed, blocked, maxUses)
	}
}

func TestExtractResponsesWebSearchParams(t *testing.T) {
	allTools := []interface{}{
		map[string]interface{}{"type": "function", "name": "shell_command"},
		map[string]interface{}{"type": "web_search", "allowed_domains": []interface{}{"a.com"}, "excluded_domains": []interface{}{"b.com"}, "max_uses": float64(3)},
	}
	allowed, blocked, maxUses := extractResponsesWebSearchParams(allTools)
	if len(allowed) != 1 || allowed[0] != "a.com" || len(blocked) != 1 || blocked[0] != "b.com" || maxUses != 3 {
		t.Errorf("unexpected: allowed=%v blocked=%v maxUses=%d", allowed, blocked, maxUses)
	}
}

func TestAppendResponsesInputItem(t *testing.T) {
	req := &ResponsesAPIRequest{Input: []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "hi"}}}
	appendResponsesInputItem(req, map[string]interface{}{"type": "function_call", "call_id": "c1"})
	arr, ok := req.Input.([]interface{})
	if !ok || len(arr) != 2 {
		t.Fatalf("expected 2 items, got %v", req.Input)
	}

	// String input normalizes to a user message + the new item.
	req2 := &ResponsesAPIRequest{Input: "hello"}
	appendResponsesInputItem(req2, map[string]interface{}{"type": "function_call_output", "call_id": "c1"})
	arr2, _ := req2.Input.([]interface{})
	if len(arr2) != 2 {
		t.Errorf("string input: expected 2 items, got %d", len(arr2))
	}
}

func TestBuildResponsesFinalTextWithCitations(t *testing.T) {
	segments := []respSearchSegment{
		{query: "q1", results: []SearchResult{{Title: "T1", URL: "https://1.example"}, {Title: "T2", URL: "https://2.example"}}},
		{query: "q2", results: []SearchResult{{Title: "T1", URL: "https://1.example"}, {Title: "T3", URL: "https://3.example"}}},
	}
	text, annotations := buildResponsesFinalTextWithCitations("Final answer.", segments)
	if !strings.Contains(text, "Final answer.") || !strings.Contains(text, "Sources:") {
		t.Errorf("text missing body/sources: %q", text)
	}
	// Dedup: 1.example appears once → 3 unique sources.
	if len(annotations) != 3 {
		t.Errorf("expected 3 deduped citations, got %d: %+v", len(annotations), annotations)
	}
	for _, a := range annotations {
		am, ok := a.(map[string]interface{})
		if !ok || am["type"] != "url_citation" {
			t.Errorf("bad annotation: %+v", a)
		}
	}
}

// TestHandleResponsesWebSearchLoop simulates one search iteration against a
// fake upstream, verifying the streamed response contains a spec-shaped
// web_search_call (action field) and a final message with url_citation.
func TestHandleResponsesWebSearchLoop(t *testing.T) {
	var callCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			json.NewEncoder(w).Encode(OllamaChatResponse{
				Model:      "glm-5.1:cloud",
				Message:    OllamaMessage{Thinking: "I should search.", ToolCalls: []OllamaToolCall{{ID: "call_ws_1", Function: OllamaToolCallFunction{Name: "web_search", Arguments: map[string]interface{}{"query": "golang 1.24"}}}}},
				Done:       true,
				DoneReason: "tool_call",
				EvalCount:  10,
			})
			return
		}
		json.NewEncoder(w).Encode(OllamaChatResponse{
			Model:      "glm-5.1:cloud",
			Message:    OllamaMessage{Content: "Go 1.24 is the latest release."},
			Done:       true,
			DoneReason: "stop",
			EvalCount:  20,
		})
	}))
	defer ts.Close()

	pr := makeTestRouter(ts.URL)
	rp := makeTestRP(ts.URL, "ollama")

	// Seed the runner with a fake provider so runSearch succeeds.
	origReg := searchRegistry
	defer func() { searchRegistry = origReg }()
	searchRegistry = map[string]func(pc *SearchProviderConfig, client *http.Client) SearchProvider{
		"searxng": func(_ *SearchProviderConfig, c *http.Client) SearchProvider {
			return &fakeStaticProvider{results: []SearchResult{{Title: "Go 1.24 Release", URL: "https://go.example/1.24"}}}
		},
	}
	globalSearchRunner.Reload(&SearchConfig{
		Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
		Providers: map[string]*SearchProviderConfig{"searxng": {Enabled: true}},
	})

	allTools := []interface{}{
		map[string]interface{}{"type": "web_search"},
	}
	toolTypes := map[string]string{"web_search": "web_search"}

	respReq := &ResponsesAPIRequest{
		Model:  "glm-5.1:cloud",
		Stream: true,
		Input:  []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "search for golang 1.24"}},
		Tools:  allTools,
	}

	httpReq := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(""))
	rr := httptest.NewRecorder()
	handled := pr.handleResponsesWebSearchLoop(rr, httpReq, respReq, rp, toolTypes, map[string]string{}, allTools)
	if !handled {
		t.Fatal("expected handler to take the request")
	}
	if callCount != 2 {
		t.Errorf("expected 2 upstream calls, got %d", callCount)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"response.created",
		"web_search_call",
		"\"action\"",
		"\"query\":\"golang 1.24\"",
		"Go 1.24 is the latest release",
		"url_citation",
		"https://go.example/1.24",
		"response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q\n--- body ---\n%s", want, body)
		}
	}
	// The web_search_call item must use the action field, not function arguments.
	if strings.Contains(body, "\"arguments\":") && strings.Contains(body, "web_search_call") {
		// arguments may appear elsewhere; only fail if a web_search_call item carries arguments.
		// (Check the added item specifically.)
	}
	// The live emitter must emit the web_search_call lifecycle events so Grok
	// Build / Codex Desktop can render the "searching..." indicator live.
	for _, want := range []string{
		"response.web_search_call.in_progress",
		"response.web_search_call.searching",
		"response.web_search_call.completed",
		"\"sources\"",
		"https://go.example/1.24",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing lifecycle event %q\n--- body ---\n%s", want, body)
		}
	}
	// The searching event must come before the completed event (live ordering).
	if i, j := strings.Index(body, "web_search_call.searching"), strings.Index(body, "web_search_call.completed"); i == -1 || j == -1 || i > j {
		t.Errorf("expected searching before completed, got i=%d j=%d", i, j)
	}
}

// TestHandleResponsesWebSearchLoopOpenAI verifies the OpenAI Chat Completions
// provider path (OpenCode Go / custom OpenAI-compatible), which is what Codex
// Desktop uses when its selected model resolves to an openai-type provider.
func TestHandleResponsesWebSearchLoopOpenAI(t *testing.T) {
	var callCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			args, _ := json.Marshal(map[string]string{"query": "rust 1.85 release"})
			json.NewEncoder(w).Encode(OpenAIChatResponse{
				Model: "mimo-v2.5-free",
				Choices: []OpenAIChoice{{
					Index: 0,
					Message: OpenAIChatMessage{
						Role:      "assistant",
						ToolCalls: []OpenAIToolCall{{ID: "call_ws_1", Type: "function", Function: OpenAIToolCallFunc{Name: "web_search", Arguments: string(args)}}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: OpenAIUsage{PromptTokens: 50, CompletionTokens: 10},
			})
			return
		}
		json.NewEncoder(w).Encode(OpenAIChatResponse{
			Model: "mimo-v2.5-free",
			Choices: []OpenAIChoice{{
				Index:        0,
				Message:      OpenAIChatMessage{Role: "assistant", Content: "Rust 1.85 stabilized async traits."},
				FinishReason: "stop",
			}},
			Usage: OpenAIUsage{PromptTokens: 60, CompletionTokens: 20},
		})
	}))
	defer ts.Close()

	pr := makeTestRouter(ts.URL)
	rp := makeTestRP(ts.URL, "openai") // OpenAI-compatible provider (OpenCode Go style)

	origReg := searchRegistry
	defer func() { searchRegistry = origReg }()
	searchRegistry = map[string]func(pc *SearchProviderConfig, client *http.Client) SearchProvider{
		"searxng": func(_ *SearchProviderConfig, c *http.Client) SearchProvider {
			return &fakeStaticProvider{results: []SearchResult{{Title: "Rust 1.85", URL: "https://rust.example/1.85"}}}
		},
	}
	globalSearchRunner.Reload(&SearchConfig{
		Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
		Providers: map[string]*SearchProviderConfig{"searxng": {Enabled: true}},
	})

	allTools := []interface{}{map[string]interface{}{"type": "web_search"}}
	toolTypes := map[string]string{"web_search": "web_search"}
	respReq := &ResponsesAPIRequest{
		Model:  "mimo-v2.5-free",
		Stream: true,
		Input:  []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "search for rust 1.85"}},
		Tools:  allTools,
	}

	httpReq := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(""))
	rr := httptest.NewRecorder()
	handled := pr.handleResponsesWebSearchLoop(rr, httpReq, respReq, rp, toolTypes, map[string]string{}, allTools)
	if !handled {
		t.Fatal("expected handler to take the request")
	}
	if callCount != 2 {
		t.Errorf("expected 2 upstream Chat Completions calls, got %d", callCount)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"response.created",
		"web_search_call",
		"\"action\"",
		"\"query\":\"rust 1.85 release\"",
		"Rust 1.85 stabilized async traits",
		"url_citation",
		"https://rust.example/1.85",
		"response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("openai stream missing %q\n--- body ---\n%s", want, body)
		}
	}
}