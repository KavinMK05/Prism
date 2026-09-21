package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testReasoningSignature = "gAAAAABm-test-encrypted-reasoning-blob"

// TestTranslateChatCompletionsToResponses_ReasoningReplay verifies that a
// Responses request asks for reasoning.encrypted_content and replays a captured
// signature as a reasoning input item immediately before the turn it produced.
// Without this the model loses its own chain of thought between turns and
// repeats work (the CLIProxyAPI behaviour Prism now mirrors).
func TestTranslateChatCompletionsToResponses_ReasoningReplay(t *testing.T) {
	req := &OpenAIChatRequest{
		Model: "openai/gpt-5.4",
		Messages: []OpenAIChatMessage{
			{Role: "user", Content: "hi"},
			{
				Role:               "assistant",
				Content:            "",
				ReasoningSignature: testReasoningSignature,
				ToolCalls: []OpenAIToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: OpenAIToolCallFunc{
						Name:      "Read",
						Arguments: `{"path":"a.go"}`,
					},
				}},
			},
			{Role: "tool", Content: "ok", ToolID: "call_1"},
		},
	}

	body := translateChatCompletionsToCodexResponses(req, true)
	include, _ := body["include"].([]string)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want [reasoning.encrypted_content]", body["include"])
	}
	input, _ := body["input"].([]interface{})
	reasoningIdx, functionCallIdx := -1, -1
	for i, raw := range input {
		item, _ := raw.(map[string]interface{})
		switch item["type"] {
		case "reasoning":
			if item["encrypted_content"] == testReasoningSignature {
				reasoningIdx = i
			}
		case "function_call":
			if functionCallIdx == -1 {
				functionCallIdx = i
			}
		}
	}
	if reasoningIdx == -1 {
		t.Fatalf("no reasoning item carrying the signature in input: %#v", input)
	}
	if functionCallIdx == -1 || reasoningIdx > functionCallIdx {
		t.Fatalf("reasoning item must precede the function_call it produced: %#v", input)
	}

	// Without reasoning replay neither the include nor the reasoning item appear.
	plain := translateChatCompletionsToCodexResponses(req, false)
	if plain["include"] != nil {
		t.Fatalf("include present without reasoning replay: %#v", plain["include"])
	}
	plainInput, _ := plain["input"].([]interface{})
	for _, raw := range plainInput {
		item, _ := raw.(map[string]interface{})
		if item["type"] == "reasoning" {
			t.Fatalf("reasoning item present without reasoning replay: %#v", item)
		}
	}
}

// TestTranslateToOpenAIForResponses_CapturesThinkingSignature verifies the
// signature is carried on the Responses path and deliberately dropped on the
// Chat Completions path (where it would be meaningless to the upstream).
func TestTranslateToOpenAIForResponses_CapturesThinkingSignature(t *testing.T) {
	req := &AnthropicRequest{
		Model: "gpt-5.4",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "plan", "signature": testReasoningSignature},
				map[string]interface{}{"type": "tool_use", "id": "call_1", "name": "Read", "input": map[string]interface{}{"path": "a.go"}},
			},
		}},
	}

	responsesReq := translateToOpenAIForResponses(req)
	if len(responsesReq.Messages) != 1 {
		t.Fatalf("responses translation messages = %#v", responsesReq.Messages)
	}
	if got := responsesReq.Messages[0].ReasoningSignature; got != testReasoningSignature {
		t.Fatalf("responses translation signature = %q, want %q", got, testReasoningSignature)
	}

	chatReq := translateToOpenAI(req)
	if len(chatReq.Messages) != 1 {
		t.Fatalf("chat translation messages = %#v", chatReq.Messages)
	}
	msg := chatReq.Messages[0]
	if msg.ReasoningSignature != "" || msg.ReasoningContent != nil {
		t.Fatalf("chat translation leaked reasoning: %#v", msg)
	}
}

// TestGenericResponsesToAnthropicStream_EmitsReasoningSignature covers the case
// where the upstream streams no reasoning summary at all (Codex only emits one
// when reasoning.summary is requested): the encrypted_content still has to reach
// Claude Code, so a signature-only thinking block is emitted.
func TestGenericResponsesToAnthropicStream_EmitsReasoningSignature(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.created"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{
		"type": "response.output_item.done",
		"item": map[string]interface{}{"type": "reasoning", "encrypted_content": testReasoningSignature},
	})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.output_text.delta", "delta": "Done"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{
		"type":     "response.completed",
		"response": map[string]interface{}{"status": "completed", "usage": map[string]interface{}{"input_tokens": 1, "output_tokens": 1}},
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Stream: true}
	resp, err := http.Post(upstream.URL, "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	defer resp.Body.Close()

	router.translateGenericResponsesToAnthropicStream(w, req, resp, anthroReq, rp, "test", reqStartForTest())

	events := parseSSEEvents(w.Body.String())
	thinkingStart, signatureDelta, signatureStop := -1, -1, -1
	for i, e := range events {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(e.Data), &m); err != nil {
			continue
		}
		switch m["type"] {
		case "content_block_start":
			block, _ := m["content_block"].(map[string]interface{})
			if block["type"] == "thinking" && thinkingStart == -1 {
				thinkingStart = i
			}
		case "content_block_delta":
			delta, _ := m["delta"].(map[string]interface{})
			if delta["type"] == "signature_delta" && delta["signature"] == testReasoningSignature {
				signatureDelta = i
			}
		case "content_block_stop":
			if signatureDelta != -1 && signatureStop == -1 {
				signatureStop = i
			}
		}
	}

	if signatureDelta == -1 {
		t.Fatalf("no signature_delta carrying the reasoning signature\ngot: %s", w.Body.String())
	}
	if thinkingStart == -1 || thinkingStart > signatureDelta {
		t.Fatalf("signature_delta emitted without a preceding thinking block\ngot: %s", w.Body.String())
	}
	if signatureStop < signatureDelta {
		t.Fatalf("thinking block not closed after signature_delta\ngot: %s", w.Body.String())
	}
}

// TestNormalizeCodexResponsesRequest_IncludesReasoningEncryptedContent verifies
// the Codex passthrough no longer strips the include field (which used to make
// Codex clients lose their reasoning state between turns).
func TestNormalizeCodexResponsesRequest_IncludesReasoningEncryptedContent(t *testing.T) {
	body := normalizeCodexResponsesRequest(&ResponsesAPIRequest{Model: "gpt-5.4"})
	include, _ := body["include"].([]string)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("default include = %#v, want [reasoning.encrypted_content]", body["include"])
	}

	// A client-supplied include is forwarded verbatim.
	body = normalizeCodexResponsesRequest(&ResponsesAPIRequest{
		Model:   "gpt-5.4",
		Include: []string{"reasoning.encrypted_content", "message.output_text.logprobs"},
	})
	include, _ = body["include"].([]string)
	if len(include) != 2 {
		t.Fatalf("forwarded include = %#v, want the client's two values", body["include"])
	}
}

// TestPostResponsesBody_RetriesWithoutIncludeOnRejection verifies the safety
// net: an upstream that rejects the include parameter gets a clean retry with
// neither the include nor any reasoning items, so reasoning replay can never
// turn a working provider into a 400.
func TestPostResponsesBody_RetriesWithoutIncludeOnRejection(t *testing.T) {
	var requests []map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		if len(requests) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"Unknown parameter: 'include'"}}`))
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
			map[string]interface{}{"type": "reasoning", "encrypted_content": testReasoningSignature},
			map[string]interface{}{"type": "message", "role": "user"},
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
}

// TestGenericResponsesToAnthropicJSON_EmitsReasoningSignature covers the
// non-streaming Anthropic response path.
func TestGenericResponsesToAnthropicJSON_EmitsReasoningSignature(t *testing.T) {
	var upstreamBody string
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{
		"type": "response.output_item.done",
		"item": map[string]interface{}{"type": "reasoning", "encrypted_content": testReasoningSignature},
	})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{"type": "response.output_text.delta", "delta": "Hi"})
	upstreamBody += makeResponsesSSEEvent(map[string]interface{}{
		"type":     "response.completed",
		"response": map[string]interface{}{"status": "completed", "usage": map[string]interface{}{"input_tokens": 2, "output_tokens": 1}},
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	anthroReq := &AnthropicRequest{Model: "test", Messages: []AnthropicMessage{{Role: "user", Content: "hi"}}}
	resp, err := http.Post(upstream.URL, "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	defer resp.Body.Close()

	router.translateGenericResponsesToAnthropicJSON(w, req, resp, anthroReq, rp, "test", reqStartForTest())

	var got AnthropicResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode anthropic response: %v\nbody: %s", err, w.Body.String())
	}
	var sawSignature, sawText bool
	for _, block := range got.Content {
		bm, _ := block.(map[string]interface{})
		switch bm["type"] {
		case "thinking":
			if bm["signature"] == testReasoningSignature {
				sawSignature = true
			}
		case "text":
			if bm["text"] == "Hi" {
				sawText = true
			}
		}
	}
	if !sawSignature {
		t.Fatalf("thinking block missing the reasoning signature: %v", got.Content)
	}
	if !sawText {
		t.Fatalf("text block missing: %v", got.Content)
	}
}
