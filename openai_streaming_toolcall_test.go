package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// parseOpenAIToolCallChunks reassembles streamed tool calls by index, returning
// the set of (index, id, name, concatenated-args) tuples the client would see.
func parseOpenAIToolCallChunks(t *testing.T, body string) map[int]struct {
	ID   string
	Name string
	Args string
} {
	byIndex := map[int]struct {
		ID   string
		Name string
		Args string
	}{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var chunk map[string]interface{}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}
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
			idx := 0
			if v, ok := tc["index"].(float64); ok {
				idx = int(v)
			}
			a, exists := byIndex[idx]
			if !exists {
				a = struct {
					ID   string
					Name string
					Args string
				}{}
			}
			if id, ok := tc["id"].(string); ok && id != "" {
				a.ID = id
			}
			if fn, ok := tc["function"].(map[string]interface{}); ok {
				if name, ok := fn["name"].(string); ok && name != "" {
					a.Name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					a.Args += args
				}
			}
			byIndex[idx] = a
		}
	}
	return byIndex
}

// makeToolCallChunk builds an Ollama native /api/chat streaming chunk carrying
// tool_calls. Ollama identifies each call by function.index (always present)
// and re-emits every accumulated call with its full cumulative arguments on
// each chunk — the real on-the-wire format per Ollama's api.ToolCallFunction.
func makeToolCallChunk(t *testing.T, calls []map[string]interface{}, done bool, doneReason string) string {
	chunk := map[string]interface{}{
		"model": "test-model",
		"message": map[string]interface{}{
			"role":       "assistant",
			"content":    "",
			"tool_calls": calls,
		},
		"done": done,
	}
	if done {
		chunk["done_reason"] = doneReason
	}
	b, _ := json.Marshal(chunk)
	return string(b) + "\n"
}

func tcWithIndex(index int, id, name string, args map[string]interface{}) map[string]interface{} {
	fn := map[string]interface{}{
		"index":     index,
		"name":      name,
		"arguments": args,
	}
	tc := map[string]interface{}{
		"type":     "function",
		"function": fn,
	}
	if id != "" {
		tc["id"] = id
	}
	return tc
}

// TestOpenAIInboundOllamaStreaming_TwoSameNameCalls verifies that two parallel
// tool calls to the SAME function name (distinct function.index, no upstream
// ids) are emitted as two separate OpenAI tool calls, not merged into one.
// This reproduces the data-loss bug: the old dedup key was `id || name`, so
// same-name calls collapsed and only the last call's arguments survived.
func TestOpenAIInboundOllamaStreaming_TwoSameNameCalls(t *testing.T) {
	var upstreamBody string
	// Cumulative streaming: chunk 1 has call 0; chunk 2 re-emits call 0 and
	// adds call 1 (same tool name, distinct index).
	upstreamBody += makeToolCallChunk(t, []map[string]interface{}{
		tcWithIndex(0, "", "search", map[string]interface{}{"q": "first"}),
	}, false, "")
	upstreamBody += makeToolCallChunk(t, []map[string]interface{}{
		tcWithIndex(0, "", "search", map[string]interface{}{"q": "first"}),
		tcWithIndex(1, "", "search", map[string]interface{}{"q": "second"}),
	}, false, "")
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
	router.handleOpenAIInboundOllamaStreaming(w, req, &OpenAIChatRequest{Model: "test", Stream: true}, rp)

	byIndex := parseOpenAIToolCallChunks(t, w.Body.String())
	if len(byIndex) != 2 {
		t.Fatalf("expected 2 distinct tool calls, got %d: %+v", len(byIndex), byIndex)
	}
	var args0, args1 map[string]interface{}
	json.Unmarshal([]byte(byIndex[0].Args), &args0)
	json.Unmarshal([]byte(byIndex[1].Args), &args1)
	if args0["q"] != "first" {
		t.Errorf("call 0 args: %+v", args0)
	}
	if args1["q"] != "second" {
		t.Errorf("call 1 args: %+v", args1)
	}
}

// TestOpenAIInboundOllamaStreaming_ContentBetweenToolCallUpdates verifies that
// when Ollama interleaves content between cumulative tool-call argument
// updates, the final emitted tool call carries the FULL final arguments, not
// the partial args captured at a mid-stream flush. The old code flushed tool
// calls when content arrived and then dropped later cumulative updates.
func TestOpenAIInboundOllamaStreaming_ContentBetweenToolCallUpdates(t *testing.T) {
	var upstreamBody string
	// Tool call starts, partial args (index 0)
	upstreamBody += makeToolCallChunk(t, []map[string]interface{}{
		tcWithIndex(0, "", "edit", map[string]interface{}{"file": "a.g"}),
	}, false, "")
	// Content interleaved (old code flushed the buffered tool call here)
	upstreamBody += makeOllamaChunk("test-model", "applying edit", "", false, "")
	// Tool call continues with FULLER cumulative args (same index 0)
	upstreamBody += makeToolCallChunk(t, []map[string]interface{}{
		tcWithIndex(0, "", "edit", map[string]interface{}{"file": "a.go", "old": "A", "new": "B"}),
	}, false, "")
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
	router.handleOpenAIInboundOllamaStreaming(w, req, &OpenAIChatRequest{Model: "test", Stream: true}, rp)

	byIndex := parseOpenAIToolCallChunks(t, w.Body.String())
	if len(byIndex) != 1 {
		t.Fatalf("expected 1 tool call, got %d: %+v", len(byIndex), byIndex)
	}
	a := byIndex[0]
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(a.Args), &args); err != nil {
		t.Fatalf("tool args not valid JSON (%v): %s", err, a.Args)
	}
	if args["file"] != "a.go" || args["old"] != "A" || args["new"] != "B" {
		t.Errorf("expected full final args file=a.go old=A new=B, got %+v (raw: %s)", args, a.Args)
	}
}

// TestOpenAIInboundOllamaStreaming_CumulativeWithIndex verifies the cumulative
// re-emission case still collapses to a single tool call when index is the
// identity (regression guard alongside the existing id-based test).
func TestOpenAIInboundOllamaStreaming_CumulativeWithIndex(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeToolCallChunk(t, []map[string]interface{}{
		tcWithIndex(0, "", "edit", map[string]interface{}{"file": "a.g"}),
	}, false, "")
	upstreamBody += makeToolCallChunk(t, []map[string]interface{}{
		tcWithIndex(0, "", "edit", map[string]interface{}{"file": "a.go"}),
	}, false, "")
	upstreamBody += makeToolCallChunk(t, []map[string]interface{}{
		tcWithIndex(0, "", "edit", map[string]interface{}{"file": "a.go", "old": "A", "new": "B"}),
	}, false, "")
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
	router.handleOpenAIInboundOllamaStreaming(w, req, &OpenAIChatRequest{Model: "test", Stream: true}, rp)

	byIndex := parseOpenAIToolCallChunks(t, w.Body.String())
	if len(byIndex) != 1 {
		t.Fatalf("expected exactly 1 tool call, got %d: %+v", len(byIndex), byIndex)
	}
	a := byIndex[0]
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(a.Args), &args); err != nil {
		t.Fatalf("args not valid JSON (%v): %s", err, a.Args)
	}
	if args["file"] != "a.go" || args["old"] != "A" || args["new"] != "B" {
		t.Errorf("expected file=a.go old=A new=B, got %+v", args)
	}
}