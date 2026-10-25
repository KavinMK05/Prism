package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Claude Code concatenates consecutive assistant turns into a single message,
// so one assistant turn routinely carries several thinking blocks and several
// tool calls — each thinking block belonging to the call(s) that follow it.
// These tests pin that every signature survives and is replayed in front of the
// call it produced. The previous translation kept only the last signature and
// hoisted a single reasoning item above every call, which left the earlier calls
// with no chain of thought to replay and made the model re-derive work it had
// already done.

const (
	testSignatureA = "gAAAAABm-test-signature-A"
	testSignatureB = "gAAAAABm-test-signature-B"
)

// responsesInputShape reduces a translated Responses body to the ordered
// "reasoning:<sig>" / "call:<id>" sequence a test can compare against.
func responsesInputShape(t *testing.T, body map[string]interface{}) []string {
	t.Helper()
	input, ok := body["input"].([]interface{})
	if !ok {
		t.Fatalf("body has no input array: %#v", body["input"])
	}
	var got []string
	for _, raw := range input {
		item, _ := raw.(map[string]interface{})
		switch item["type"] {
		case "reasoning":
			sig, _ := item["encrypted_content"].(string)
			got = append(got, "reasoning:"+sig)
		case "function_call":
			id, _ := item["call_id"].(string)
			got = append(got, "call:"+id)
		}
	}
	return got
}

func assertShape(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("input item order =\n  %v\nwant\n  %v", got, want)
	}
}

func toolUse(id, name string) map[string]interface{} {
	return map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": map[string]interface{}{"a": "b"},
	}
}

func signatureBlock(sig string) map[string]interface{} {
	// Claude Code echoes a signature-only thinking block on this path: Prism
	// requests no reasoning.summary, so there is no visible thinking text.
	return map[string]interface{}{"type": "thinking", "thinking": "", "signature": sig}
}

// TestCoalescedAssistantTurn_ReplaysEveryReasoningItemInPosition is the
// end-to-end regression: an Anthropic assistant turn built from two
// concatenated turns must produce two reasoning items, each in front of its own
// calls, instead of one item carrying only the last signature.
func TestCoalescedAssistantTurn_ReplaysEveryReasoningItemInPosition(t *testing.T) {
	req := &AnthropicRequest{
		Model: "gpt-5.6-luna",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				signatureBlock(testSignatureA),
				toolUse("call_1", "Read"),
				signatureBlock(testSignatureB),
				toolUse("call_2", "Read"),
				toolUse("call_3", "Bash"),
			},
		}},
	}

	body := translateChatCompletionsToCodexResponses(translateToOpenAIForResponses(req), true)
	assertShape(t, responsesInputShape(t, body), []string{
		"reasoning:" + testSignatureA,
		"call:call_1",
		"reasoning:" + testSignatureB,
		"call:call_2",
		"call:call_3",
	})
}

// TestCoalescedAssistantTurn_KeepsSignaturesSharingAPosition covers two thinking
// blocks emitted before any tool call: both belong ahead of the first call, and
// neither may be dropped.
func TestCoalescedAssistantTurn_KeepsSignaturesSharingAPosition(t *testing.T) {
	req := &AnthropicRequest{
		Model: "gpt-5.6-luna",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				signatureBlock(testSignatureA),
				signatureBlock(testSignatureB),
				toolUse("call_1", "Read"),
			},
		}},
	}

	body := translateChatCompletionsToCodexResponses(translateToOpenAIForResponses(req), true)
	assertShape(t, responsesInputShape(t, body), []string{
		"reasoning:" + testSignatureA,
		"reasoning:" + testSignatureB,
		"call:call_1",
	})
}

// TestTrailingReasoningSignatureIsStillReplayed covers a thinking block that
// follows the turn's last tool call: it has no call to precede, but it still
// belongs to the turn and must reach the upstream rather than being discarded.
func TestTrailingReasoningSignatureIsStillReplayed(t *testing.T) {
	req := &AnthropicRequest{
		Model: "gpt-5.6-luna",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				toolUse("call_1", "Read"),
				signatureBlock(testSignatureA),
			},
		}},
	}

	body := translateChatCompletionsToCodexResponses(translateToOpenAIForResponses(req), true)
	assertShape(t, responsesInputShape(t, body), []string{
		"call:call_1",
		"reasoning:" + testSignatureA,
	})
}

// TestTranslateToOpenAIForResponses_TagsSignaturePosition pins the position tag
// itself: CallIndex is the number of the turn's tool calls that preceded the
// thinking block, which is what lets the Responses translator interleave.
func TestTranslateToOpenAIForResponses_TagsSignaturePosition(t *testing.T) {
	req := &AnthropicRequest{
		Model: "gpt-5.6-luna",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				signatureBlock(testSignatureA),
				toolUse("call_1", "Read"),
				signatureBlock(testSignatureB),
				toolUse("call_2", "Read"),
			},
		}},
	}

	msgs := translateToOpenAIForResponses(req).Messages
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one assistant turn", msgs)
	}
	sigs := msgs[0].ReasoningSignatures
	if len(sigs) != 2 {
		t.Fatalf("kept %d signatures, want 2: %#v", len(sigs), sigs)
	}
	if sigs[0].Signature != testSignatureA || sigs[0].CallIndex != 0 {
		t.Fatalf("signature[0] = %#v, want {A 0}", sigs[0])
	}
	if sigs[1].Signature != testSignatureB || sigs[1].CallIndex != 1 {
		t.Fatalf("signature[1] = %#v, want {B 1}", sigs[1])
	}
}

// TestPostResponsesBody_RetriesOnSignatureRejection covers the safety net for a
// provider that understands the include parameter but cannot accept a replayed
// signature: the 400 blames encrypted_content, not "include", and used to be
// returned to the client as a hard failure. It must instead retry once as a
// plain Responses request.
func TestPostResponsesBody_RetriesOnSignatureRejection(t *testing.T) {
	var requests []map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		if len(requests) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"Invalid value for 'input[2].encrypted_content'"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	bodyMap := map[string]interface{}{
		"model":   "test",
		"include": []string{"reasoning.encrypted_content"},
		"input": []interface{}{
			map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "Read"},
			reasoningInputItem(testSignatureA),
			map[string]interface{}{"type": "function_call", "call_id": "call_2", "name": "Read"},
		},
	}

	resp, err := router.postResponsesBody(httptest.NewRequest(http.MethodPost, "/", nil).Context(), upstream.URL, bodyMap, rp, false, "")
	if err != nil {
		t.Fatalf("postResponsesBody: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after retry", resp.StatusCode)
	}
	if len(requests) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(requests))
	}
	retry := requests[1]
	if retry["include"] != nil {
		t.Fatalf("retry still carries include: %#v", retry["include"])
	}
	input, _ := retry["input"].([]interface{})
	for _, raw := range input {
		item, _ := raw.(map[string]interface{})
		if item["type"] == "reasoning" {
			t.Fatalf("retry still carries a reasoning item: %#v", item)
		}
	}
	// The retry must keep the rest of the request intact: only the reasoning
	// replay is dropped, not the conversation.
	if len(input) != 2 {
		t.Fatalf("retry kept %d input items, want the 2 function_calls: %#v", len(input), input)
	}
}

// TestPostResponsesBody_DoesNotRetryUnrelatedBadRequest guards the retry from
// swallowing failures it cannot fix: a 400 about something else is returned to
// the client unchanged, without a second upstream request.
func TestPostResponsesBody_DoesNotRetryUnrelatedBadRequest(t *testing.T) {
	var requestCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	bodyMap := map[string]interface{}{
		"model":   "test",
		"include": []string{"reasoning.encrypted_content"},
		"input":   []interface{}{map[string]interface{}{"type": "message", "role": "user"}},
	}

	resp, err := router.postResponsesBody(httptest.NewRequest(http.MethodPost, "/", nil).Context(), upstream.URL, bodyMap, rp, false, "")
	if err != nil {
		t.Fatalf("postResponsesBody: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want the upstream 400 passed through", resp.StatusCode)
	}
	if requestCount != 1 {
		t.Fatalf("upstream saw %d requests, want 1 (no retry)", requestCount)
	}
}
