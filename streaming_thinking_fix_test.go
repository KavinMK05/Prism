package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOllamaStreaming_TextBeforeThinking verifies the text -> thinking
// transition is handled: when a model emits text before its thinking block,
// the text block is closed and the index bumped before the thinking block
// starts, instead of reusing the text block's index. This is the
// ollama/ollama#17101 regression (PR #17102) ported to this proxy's
// StreamConverter equivalent.
func TestOllamaStreaming_TextBeforeThinking(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeOllamaChunk("test-model", "---\n", "", false, "")      // text first
	upstreamBody += makeOllamaChunk("test-model", "", "plan", false, "")       // text -> thinking
	upstreamBody += makeOllamaChunk("test-model", "the answer", "", false, "") // thinking -> text (thinkingDone now true)
	upstreamBody += makeOllamaChunk("test-model", "", "late thinking", false, "")
	upstreamBody += makeOllamaChunk("test-model", "", "", true, "stop") // done

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

	// Collect block starts with their index and type.
	type blockStart struct {
		index int
		btype string
	}
	var starts []blockStart
	var stops []int
	thinkingDeltas := 0
	for _, e := range events {
		switch e.Event {
		case "content_block_start":
			var d struct {
				Index int `json:"index"`
				Block struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			_ = json.Unmarshal([]byte(e.Data), &d)
			starts = append(starts, blockStart{d.Index, d.Block.Type})
		case "content_block_stop":
			var d struct {
				Index int `json:"index"`
			}
			_ = json.Unmarshal([]byte(e.Data), &d)
			stops = append(stops, d.Index)
		case "content_block_delta":
			var d struct {
				Delta struct {
					Type string `json:"type"`
				} `json:"delta"`
			}
			_ = json.Unmarshal([]byte(e.Data), &d)
			if d.Delta.Type == "thinking_delta" {
				thinkingDeltas++
			}
		}
	}

	// Expected block order: text(0), thinking(1), text(2). The "late thinking"
	// chunk after thinkingDone must NOT open a 4th block.
	wantStarts := []blockStart{{0, "text"}, {1, "thinking"}, {2, "text"}}
	if len(starts) != len(wantStarts) {
		t.Fatalf("expected %d block starts, got %d: %+v", len(wantStarts), len(starts), starts)
	}
	for i, want := range wantStarts {
		if starts[i].index != want.index || starts[i].btype != want.btype {
			t.Errorf("block start[%d]: want index=%d type=%s, got index=%d type=%s",
				i, want.index, want.btype, starts[i].index, starts[i].btype)
		}
	}
	// Each block must be stopped, with monotonic distinct indexes 0,1,2.
	wantStops := []int{0, 1, 2}
	if len(stops) != len(wantStops) {
		t.Fatalf("expected %d stops, got %d: %v", len(wantStops), len(stops), stops)
	}
	for i, want := range wantStops {
		if stops[i] != want {
			t.Errorf("stop[%d]: want %d, got %d", i, want, stops[i])
		}
	}
	// Only ONE thinking_delta stream (the "plan"); "late thinking" must be
	// suppressed by the thinkingDone guard.
	if thinkingDeltas != 1 {
		t.Errorf("expected exactly 1 thinking_delta stream, got %d (thinkingDone guard not preventing re-open)", thinkingDeltas)
	}
}

// TestOllamaToolCallFunction_StringArguments verifies tool-call arguments
// delivered as a JSON string (some Ollama-compatible upstreams, e.g. GLM via
// certain routers) are decoded into a proper object rather than being dropped
// or embedded as a raw string. See ollama/ollama#15645.
func TestOllamaToolCallFunction_StringArguments(t *testing.T) {
	// Arguments as a JSON string.
	raw := []byte(`{"name":"TaskUpdate","arguments":"{\"taskId\":\"1\",\"status\":\"completed\"}"}`)
	var f OllamaToolCallFunction
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal string-args: %v", err)
	}
	if f.Name != "TaskUpdate" {
		t.Errorf("name: got %q", f.Name)
	}
	if f.Arguments == nil {
		t.Fatal("arguments nil after decoding string-args")
	}
	if f.Arguments["taskId"] != "1" || f.Arguments["status"] != "completed" {
		t.Errorf("arguments not decoded from string: %#v", f.Arguments)
	}

	// Arguments as a JSON object (the common case) still works.
	raw2 := []byte(`{"name":"Read","arguments":{"file_path":"x.txt","limit":5}}`)
	var f2 OllamaToolCallFunction
	if err := json.Unmarshal(raw2, &f2); err != nil {
		t.Fatalf("unmarshal object-args: %v", err)
	}
	if f2.Arguments["file_path"] != "x.txt" {
		t.Errorf("object-args lost file_path: %#v", f2.Arguments)
	}

	// Empty arguments string is fine (no args).
	raw3 := []byte(`{"name":"List","arguments":""}`)
	var f3 OllamaToolCallFunction
	if err := json.Unmarshal(raw3, &f3); err != nil {
		t.Fatalf("unmarshal empty-string-args: %v", err)
	}
	if f3.Arguments != nil {
		t.Errorf("empty-string-args should yield nil map, got %#v", f3.Arguments)
	}
}