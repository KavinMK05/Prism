package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/search"
)

// Ollama Cloud's Chat Completions endpoint reports reasoning in the
// "reasoning" delta field rather than "reasoning_content". These tests pin the
// fallback so /v1/responses (Codex) keeps showing thinking content now that the
// ollama_cloud provider speaks Chat Completions instead of the native /api/chat
// API (which used message.thinking).
//
// Payloads are trimmed captures from https://ollama.com/v1/chat/completions
// with deepseek-v4.1-flash:cloud (2026-09-22): note the field name is
// "reasoning", and "reasoning_content" is never present.

func makeOpenAIReasoningChunk(reasoning, content, finishReason string) string {
	delta := map[string]interface{}{}
	if reasoning != "" {
		delta["reasoning"] = reasoning
	}
	if content != "" {
		delta["content"] = content
	}
	choice := map[string]interface{}{"index": 0, "delta": delta}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	ch := map[string]interface{}{
		"id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 1, "model": "deepseek-v4.1-flash:cloud",
		"choices": []interface{}{choice},
	}
	b, _ := json.Marshal(ch)
	return "data: " + string(b) + "\n"
}

// Thinking deltas must reach the client as reasoning_summary_text.delta events
// and the finished item must land in response.completed's output array.
func TestResponsesStreaming_ReasoningFieldEmitsThinking(t *testing.T) {
	chunks := makeOpenAIReasoningChunk("The", "", "") +
		makeOpenAIReasoningChunk(" user", "", "") +
		makeOpenAIReasoningChunk(" wants", "", "") +
		makeOpenAIReasoningChunk("", "Hi there", "") +
		makeOpenAIReasoningChunk("", "", "stop") +
		"data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(chunks))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	respReq := &ResponsesAPIRequest{Model: "deepseek-v4.1-flash:cloud", Stream: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`))
	w := httptest.NewRecorder()

	router.handleResponsesAPIOpenAIStreaming(w, r, respReq, rp, map[string]string{}, map[string]string{})

	var deltas []string
	var doneText string
	var completedOutput []interface{}
	for _, ev := range parseSSEEvents(w.Body.String()) {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			continue
		}
		switch payload["type"] {
		case "response.reasoning_summary_text.delta":
			text, _ := payload["delta"].(string)
			deltas = append(deltas, text)
		case "response.reasoning_summary_text.done":
			doneText, _ = payload["text"].(string)
		case "response.completed":
			respObj, _ := payload["response"].(map[string]interface{})
			completedOutput, _ = respObj["output"].([]interface{})
		}
	}

	if len(deltas) == 0 {
		t.Fatalf("no reasoning_summary_text.delta events: the \"reasoning\" field is being dropped\n--- body ---\n%s", w.Body.String())
	}
	if got := strings.Join(deltas, ""); got != "The user wants" {
		t.Errorf("reasoning deltas joined = %q, want %q", got, "The user wants")
	}
	if doneText != "The user wants" {
		t.Errorf("reasoning_summary_text.done text = %q, want the accumulated %q", doneText, "The user wants")
	}

	var reasoningItem map[string]interface{}
	for _, it := range completedOutput {
		m, ok := it.(map[string]interface{})
		if !ok || m["type"] != "reasoning" {
			continue
		}
		reasoningItem = m
	}
	if reasoningItem == nil {
		t.Fatalf("no reasoning item in response.completed output: %v", completedOutput)
	}
	summary, _ := reasoningItem["summary"].([]interface{})
	if len(summary) != 1 {
		t.Fatalf("reasoning summary = %v, want exactly one summary_text part", reasoningItem["summary"])
	}
	part, _ := summary[0].(map[string]interface{})
	if part["text"] != "The user wants" {
		t.Errorf("reasoning summary text = %v, want the accumulated reasoning", part["text"])
	}

	if !strings.Contains(w.Body.String(), "Hi there") {
		t.Error("final assistant content was dropped while handling reasoning")
	}
}

// Non-streaming Responses translation must build the same reasoning item.
func TestTranslateChatCompletionsToResponsesAPI_ReasoningField(t *testing.T) {
	resp := &OpenAIChatResponse{
		Model: "deepseek-v4.1-flash:cloud",
		Choices: []OpenAIChoice{{
			Index:        0,
			Message:      OpenAIChatMessage{Role: "assistant", Content: "42"},
			FinishReason: "stop",
		}},
	}
	reasoning := "The answer is 42."
	resp.Choices[0].Message.Reasoning = &reasoning

	out := translateChatCompletionsToResponsesAPI(resp, &ResponsesAPIRequest{Model: resp.Model}, nil, nil)
	if len(out.Output) < 2 {
		t.Fatalf("output = %v, want a reasoning item followed by a message", out.Output)
	}
	first, ok := out.Output[0].(ResponsesAPIReasoningItem)
	if !ok || first.Type != "reasoning" {
		t.Fatalf("first output item = %#v, want a reasoning item", out.Output[0])
	}
	if len(first.Summary) != 1 || first.Summary[0].Text != reasoning {
		t.Errorf("reasoning summary = %+v, want the \"reasoning\" field text", first.Summary)
	}
}

// The search-interception loop reads turn.thinking; a Chat Completions provider
// that uses "reasoning" must not lose it (the loop re-emits it as reasoning
// events around each web_search_call).
func TestHandleResponsesWebSearchLoopOpenAIReasoningField(t *testing.T) {
	var callCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			args, _ := json.Marshal(map[string]string{"query": "golang 1.24"})
			thinking := "I should search for the release notes."
			json.NewEncoder(w).Encode(OpenAIChatResponse{
				Model: "deepseek-v4.1-flash:cloud",
				Choices: []OpenAIChoice{{
					Index: 0,
					Message: OpenAIChatMessage{
						Role:      "assistant",
						Reasoning: &thinking,
						ToolCalls: []OpenAIToolCall{{ID: "call_ws_1", Type: "function", Function: OpenAIToolCallFunc{Name: "web_search", Arguments: string(args)}}},
					},
					FinishReason: "tool_calls",
				}},
				Usage: OpenAIUsage{PromptTokens: 50, CompletionTokens: 10},
			})
			return
		}
		thinking := "The release notes confirm it."
		json.NewEncoder(w).Encode(OpenAIChatResponse{
			Model: "deepseek-v4.1-flash:cloud",
			Choices: []OpenAIChoice{{
				Index:        0,
				Message:      OpenAIChatMessage{Role: "assistant", Content: "Go 1.24 is out.", Reasoning: &thinking},
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
		Model:  "deepseek-v4.1-flash:cloud",
		Stream: true,
		Input:  []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "search for go 1.24"}},
		Tools:  allTools,
	}

	httpReq := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(""))
	rr := httptest.NewRecorder()
	if !pr.handleResponsesWebSearchLoop(rr, httpReq, respReq, rp, map[string]string{"web_search": "web_search"}, map[string]string{}, allTools) {
		t.Fatal("expected handler to take the request")
	}

	body := rr.Body.String()
	for _, want := range []string{
		"response.reasoning_summary_text.delta",
		"I should search for the release notes.",
		"The release notes confirm it.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("openai reasoning stream missing %q\n--- body ---\n%s", want, body)
		}
	}
}
