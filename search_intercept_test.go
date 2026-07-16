package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectClaudeCodeWebSearch(t *testing.T) {
	req := &AnthropicRequest{
		Model:     "glm-5.1:cloud",
		MaxTokens: 32000,
		Stream:    true,
		System: []interface{}{
			map[string]interface{}{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.211;"},
			map[string]interface{}{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			map[string]interface{}{"type": "text", "text": "You are an assistant for performing a web search tool use"},
		},
		Messages: []AnthropicMessage{
			{Role: "user", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "Perform a web search for the query: NEET marks 2026"},
			}},
		},
		Tools: []AnthropicTool{{Name: "web_search", InputSchema: nil}},
	}
	q, ok := detectClaudeCodeWebSearch(req)
	if !ok {
		t.Fatal("expected detection to succeed")
	}
	if q != "NEET marks 2026" {
		t.Errorf("query = %q, want %q", q, "NEET marks 2026")
	}
}

func TestDetectClaudeCodeWebSearchRejectsNormalConversation(t *testing.T) {
	req := &AnthropicRequest{
		System: []interface{}{map[string]interface{}{"type": "text", "text": "You are Claude Code."}},
		Messages: []AnthropicMessage{
			{Role: "user", Content: "What is the weather today?"},
		},
		Tools: []AnthropicTool{{Name: "web_search"}},
	}
	if _, ok := detectClaudeCodeWebSearch(req); ok {
		t.Fatal("normal conversation must not be detected as web-search secondary")
	}
}

func TestBuildWebSearchSummaryAndBlocks(t *testing.T) {
	results := []SearchResult{
		{Title: "Result One", URL: "https://example.com/1", Snippet: "A short snippet.", PageAge: "2025-01-01"},
		{Title: "Result Two", URL: "https://example.com/2"},
	}
	summary := buildWebSearchSummary("test query", results, "")
	if !strings.Contains(summary, "test query") || !strings.Contains(summary, "Result One") || !strings.Contains(summary, "https://example.com/1") {
		t.Errorf("summary missing expected content: %q", summary)
	}

	blocks := buildWebSearchResultBlocks(results, "")
	arr, ok := blocks.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map, got %T", blocks)
	}
	if len(arr) != 2 || arr[0]["url"] != "https://example.com/1" || arr[0]["type"] != "web_search_result" {
		t.Errorf("unexpected blocks: %+v", arr)
	}
	if arr[0]["page_age"] != "2025-01-01" {
		t.Errorf("page_age not propagated: %+v", arr[0])
	}

	errBlock := buildWebSearchResultBlocks(nil, "boom")
	if m, ok := errBlock.(map[string]interface{}); !ok || m["type"] != "web_search_tool_result_error" {
		t.Errorf("expected error block, got %+v", errBlock)
	}
}

// TestEmitSyntheticWebSearchStream verifies the SSE event sequence and that the
// server_tool_use / web_search_tool_result / text blocks are present.
func TestEmitSyntheticWebSearchStream(t *testing.T) {
	pr := &ProviderRouter{}
	req := &AnthropicRequest{Model: "glm-5.1:cloud", Stream: true}

	rr := httptest.NewRecorder()
	pr.emitSyntheticWebSearchStream(rr, req, "hello world",
		[]SearchResult{{Title: "T", URL: "https://x.example", Snippet: "S"}}, "", 100)

	body := rr.Body.String()
	for _, want := range []string{
		"event: message_start",
		"server_tool_use",
		"input_json_delta",
		"partial_json",
		"hello world",
		"web_search_tool_result",
		"web_search_result",
		"https://x.example",
		"event: message_delta",
		"end_turn",
		"web_search_requests",
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE stream missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func extractPartialJSON(s string) string { return "" }