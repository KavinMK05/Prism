package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Reasoning round-trips through encrypted_content, mirroring Ollama's Responses
// converter: ToResponse/finishReasoning set the field to the plain thinking text
// ("Plain text for now") and FromResponsesRequest reads it back
// (pendingThinking = v.EncryptedContent). summary stays the display digest.
//
// Codex echoes encrypted_content verbatim across turns, so it is the exact
// carrier; summary is only a fallback for items whose producer left the field
// empty (which is what Codex sends back when Prism did not populate it).

const testReplayEncrypted = "the plan I already worked out: patch ServeControl"

// The streaming path must set encrypted_content on the completed reasoning item.
func TestResponsesStreaming_ReasoningItemCarriesEncryptedContent(t *testing.T) {
	chunks := makeOpenAIReasoningChunk("The user", "", "") +
		makeOpenAIReasoningChunk(" wants tools", "", "") +
		makeOpenAIReasoningChunk("", "Done.", "") +
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

	var item map[string]interface{}
	for _, ev := range parseSSEEvents(w.Body.String()) {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			continue
		}
		if payload["type"] != "response.output_item.done" {
			continue
		}
		cand, _ := payload["item"].(map[string]interface{})
		if cand["type"] == "reasoning" {
			item = cand
		}
	}
	if item == nil {
		t.Fatalf("no reasoning item.done emitted\n--- body ---\n%s", w.Body.String())
	}
	if got, _ := item["encrypted_content"].(string); got != "The user wants tools" {
		t.Errorf("encrypted_content = %q, want the accumulated reasoning (the round-trip carrier)", got)
	}
}

// The non-streaming path must set it too, matching Ollama's ToResponse.
func TestTranslateChatCompletionsToResponsesAPI_ReasoningCarriesEncryptedContent(t *testing.T) {
	reasoning := "I already know the answer."
	resp := &OpenAIChatResponse{
		Model: "deepseek-v4.1-flash:cloud",
		Choices: []OpenAIChoice{{
			Index:   0,
			Message: OpenAIChatMessage{Role: "assistant", Content: "42", ReasoningContent: &reasoning},
		}},
	}

	out := translateChatCompletionsToResponsesAPI(resp, &ResponsesAPIRequest{Model: resp.Model}, nil, nil)
	first, ok := out.Output[0].(ResponsesAPIReasoningItem)
	if !ok || first.Type != "reasoning" {
		t.Fatalf("first output item = %#v, want a reasoning item", out.Output[0])
	}
	if first.EncryptedContent != reasoning {
		t.Errorf("EncryptedContent = %q, want %q", first.EncryptedContent, reasoning)
	}
}

// On replay, encrypted_content wins over summary (Ollama reads only the former).
func TestResponsesReasoningReplay_PrefersEncryptedContent(t *testing.T) {
	item := map[string]interface{}{
		"type":              "reasoning",
		"summary":           []interface{}{map[string]interface{}{"type": "summary_text", "text": "a digest of the plan"}},
		"encrypted_content": testReplayEncrypted,
	}
	if got := responsesReasoningReplayText(item); got != testReplayEncrypted {
		t.Errorf("replay text = %q, want the encrypted_content %q", got, testReplayEncrypted)
	}
}

// An empty encrypted_content (what Codex sends back when the producer left the
// field unset) must fall back to the summary rather than replaying nothing.
func TestResponsesReasoningReplay_FallsBackToSummary(t *testing.T) {
	item := map[string]interface{}{
		"type":              "reasoning",
		"summary":           []interface{}{map[string]interface{}{"type": "summary_text", "text": "thinking about it"}},
		"encrypted_content": "",
	}
	if got := responsesReasoningReplayText(item); got != "thinking about it" {
		t.Errorf("replay text = %q, want the summary fallback", got)
	}
}

// A non-reasoning item carries no replay text regardless of its fields.
func TestResponsesReasoningReplay_IgnoresNonReasoningItems(t *testing.T) {
	item := map[string]interface{}{
		"type":              "message",
		"role":              "assistant",
		"encrypted_content": testReplayEncrypted,
	}
	if got := responsesReasoningReplayText(item); got != "" {
		t.Errorf("replay text = %q, want empty for a non-reasoning item", got)
	}
}

// End to end: a reasoning item carrying encrypted_content must attach as
// thinking on the Ollama path even when its summary is empty, which is the case
// Prism previously dropped entirely.
func TestTranslateResponsesInputToOllamaMessages_EmptySummaryUsesEncryptedContent(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "hi"},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": testReplayEncrypted},
		map[string]interface{}{"type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": "done"}}},
	}
	msgs := translateResponsesInputToOllamaMessages(input)
	var asst OllamaMessage
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 0 {
			asst = m
		}
	}
	if asst.Thinking != testReplayEncrypted {
		t.Errorf("thinking = %q, want the encrypted_content replayed", asst.Thinking)
	}
	if asst.Content != "done" {
		t.Errorf("content = %q, want the assistant text preserved", asst.Content)
	}
}

// The Chat Completions path carries it in reasoning_content.
func TestTranslateResponsesInputToChatMessages_EmptySummaryUsesEncryptedContent(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "hi"},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{}, "encrypted_content": testReplayEncrypted},
		map[string]interface{}{"type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": "done"}}},
	}
	msgs := translateResponsesInputToChatMessages(input)
	var got string
	for _, m := range msgs {
		if m.Role == "assistant" && m.ReasoningContent != nil {
			got = *m.ReasoningContent
		}
	}
	if got != testReplayEncrypted {
		t.Errorf("reasoning_content = %q, want the encrypted_content replayed", got)
	}
}
