package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the Responses-API replay translator.
//
// Background: with Codex CLI, replayed tool calls used to reach the upstream in
// a shape the model could not correlate (unqualified namespaced names, tool
// outputs with no call id, orphan outputs sent as role:"tool"), which made the
// model retry the same call until it gave up and fell back to shell/Python
// hacks. Fixtures in testdata/ are trimmed real captures — see
// codex_responses_replay.json's "_source".

type codexReplayFixture struct {
	Source      string                   `json:"_source"`
	Tools       []interface{}            `json:"tools"`
	ReplayInput []map[string]interface{} `json:"replay_input"`
}

func loadCodexReplayFixture(t *testing.T) codexReplayFixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "codex_responses_replay.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f codexReplayFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Tools) == 0 || len(f.ReplayInput) == 0 {
		t.Fatal("fixture is empty")
	}
	return f
}

// item returns a copy of the fixture input item whose "_comment" contains want.
func (f codexReplayFixture) item(t *testing.T, want string) map[string]interface{} {
	t.Helper()
	for _, it := range f.ReplayInput {
		if c, _ := it["_comment"].(string); strings.Contains(c, want) {
			m := make(map[string]interface{}, len(it))
			for k, v := range it {
				m[k] = v
			}
			delete(m, "_comment")
			return m
		}
	}
	t.Fatalf("fixture item matching %q not found", want)
	return nil
}

// replayView is a translator-agnostic view of a translated upstream message.
type replayView struct {
	role      string
	content   string
	toolID    string
	toolName  string
	toolCalls map[string]string // call id -> function name
}

func chatReplayView(msgs []OpenAIChatMessage) []replayView {
	views := make([]replayView, 0, len(msgs))
	for _, m := range msgs {
		v := replayView{role: m.Role, toolID: m.ToolID, toolName: m.Name, toolCalls: map[string]string{}}
		if s, ok := m.Content.(string); ok {
			v.content = s
		}
		for _, tc := range m.ToolCalls {
			v.toolCalls[tc.ID] = tc.Function.Name
		}
		views = append(views, v)
	}
	return views
}

func ollamaReplayView(msgs []OllamaMessage) []replayView {
	views := make([]replayView, 0, len(msgs))
	for _, m := range msgs {
		v := replayView{role: m.Role, content: m.Content, toolID: m.ToolCallID, toolName: m.ToolName, toolCalls: map[string]string{}}
		if v.toolID == "" {
			v.toolID = m.ToolCallID
		}
		for _, tc := range m.ToolCalls {
			v.toolCalls[tc.ID] = tc.Function.Name
		}
		views = append(views, v)
	}
	return views
}

// toolListWithFunctionsNamespace returns the captured Codex tool list plus the
// "Responses Lite" `functions` namespace that carries apply_patch as a custom
// child tool.
func toolListWithFunctionsNamespace(f codexReplayFixture) []interface{} {
	tools := append([]interface{}{}, f.Tools...)
	return append(tools, map[string]interface{}{
		"type": "namespace",
		"name": "functions",
		"tools": []interface{}{
			map[string]interface{}{"type": "custom", "name": "custom_apply_patch"},
		},
	})
}

// --- Gap (a): namespaced tool names must be re-qualified on replay ---

func TestResponsesReplayRequaalifiesNamespacedToolNames(t *testing.T) {
	f := loadCodexReplayFixture(t)
	tools := toolListWithFunctionsNamespace(f)

	call := f.item(t, "namespaced replay")
	customCall := f.item(t, "namespaced freeform replay")
	input := []interface{}{
		call,
		map[string]interface{}{"type": "function_call_output", "call_id": call["call_id"], "output": "done"},
		customCall,
		map[string]interface{}{"type": "custom_tool_call_output", "call_id": customCall["call_id"], "output": "Done!"},
	}
	req := &ResponsesAPIRequest{Model: "test", Input: input, Tools: tools}

	// The upstream tool list only declares the qualified names; an unqualified
	// replay is an unknown function.
	declared := buildToolTypeMap(collectAllResponseTools(req))
	want := map[string]string{
		"call_03ghi": "multi_agent_v1__spawn_agent",
		"call_04jkl": "functions__custom_apply_patch",
	}
	for _, name := range want {
		if declared[name] == "" {
			t.Fatalf("fixture tool list does not declare %q; cannot test re-qualification", name)
		}
	}
	if declared["spawn_agent"] != "" || declared["custom_apply_patch"] != "" {
		t.Fatalf("fixture tool list unexpectedly declares unqualified names")
	}

	t.Run("chat-completions", func(t *testing.T) {
		views := chatReplayView(translateResponsesAPIToChatCompletions(req).Messages)
		assertNamespacedReplay(t, views, want)
	})
	t.Run("ollama", func(t *testing.T) {
		views := ollamaReplayView(translateResponsesAPIToOllama(req).Messages)
		assertNamespacedReplay(t, views, want)
	})
}

func assertNamespacedReplay(t *testing.T, views []replayView, want map[string]string) {
	t.Helper()
	gotCalls := map[string]string{}
	gotToolMsgs := map[string]string{}
	for _, v := range views {
		for id, name := range v.toolCalls {
			gotCalls[id] = name
		}
		if v.role == "tool" {
			gotToolMsgs[v.toolID] = v.toolName
		}
	}
	for id, name := range want {
		if gotCalls[id] != name {
			t.Errorf("replayed assistant tool_call %s name = %q, want qualified %q", id, gotCalls[id], name)
		}
		if gotToolMsgs[id] != name {
			t.Errorf("tool message for %s name = %q, want qualified %q", id, gotToolMsgs[id], name)
		}
	}
}

// --- Gap (b): tool outputs that carry no usable call_id must still pair ---

func TestResponsesReplayPairsOutputsThatLackCallID(t *testing.T) {
	f := loadCodexReplayFixture(t)

	cases := []struct {
		name    string
		comment string
		callID  string
	}{
		// Only the fco_ output-item id is present, so this pairs by FIFO with
		// the single pending call.
		{"fco-item-id-only", "ONLY the fco_", "call_pending"},
		// Codex variants the resolver has to read instead of `call_id`.
		{"camelCase-callId", "camelCase callId", "call_01abc"},
		{"tool_call_id", "tool_call_id instead", "call_02def"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []interface{}{
				map[string]interface{}{
					"type": "function_call", "name": "shell_command",
					"call_id": tc.callID, "arguments": `{"command":"ls"}`,
				},
				f.item(t, tc.comment),
			}
			req := &ResponsesAPIRequest{Model: "test", Input: input}

			for _, path := range []struct {
				name  string
				views []replayView
			}{
				{"chat-completions", chatReplayView(translateResponsesAPIToChatCompletions(req).Messages)},
				{"ollama", ollamaReplayView(translateResponsesAPIToOllama(req).Messages)},
			} {
				toolMsgs := 0
				for _, v := range path.views {
					if v.role != "tool" {
						continue
					}
					toolMsgs++
					if v.toolID != tc.callID {
						t.Errorf("%s: tool message tool_call_id = %q, want %q", path.name, v.toolID, tc.callID)
					}
					if v.toolName != "shell_command" {
						t.Errorf("%s: tool message name = %q, want shell_command", path.name, v.toolName)
					}
				}
				if toolMsgs != 1 {
					t.Errorf("%s: expected exactly 1 tool message, got %d (%+v)", path.name, toolMsgs, path.views)
				}
			}
		})
	}
}

// --- Gap (b): orphan outputs must not become invalid tool messages ---

func TestResponsesReplayOrphanOutputBecomesUserText(t *testing.T) {
	f := loadCodexReplayFixture(t)
	input := []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "carry on"},
		f.item(t, "orphan"),
	}
	req := &ResponsesAPIRequest{Model: "test", Input: input}

	paths := []struct {
		name  string
		views []replayView
	}{
		{"chat-completions", chatReplayView(translateResponsesAPIToChatCompletions(req).Messages)},
		{"ollama", ollamaReplayView(translateResponsesAPIToOllama(req).Messages)},
	}

	for _, path := range paths {
		found := false
		for _, v := range path.views {
			if v.role == "tool" {
				t.Errorf("%s: orphan output became a role:%q message with tool_call_id=%q", path.name, "tool", v.toolID)
			}
			if v.role == "user" && strings.Contains(v.content, "truncated history") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: orphan output text missing from user messages: %+v", path.name, path.views)
		}
	}
}

// --- normalizeResponsesToolCallOutputs must not mutate the client request ---

func TestNormalizeResponsesToolCallOutputsDoesNotMutateInput(t *testing.T) {
	f := loadCodexReplayFixture(t)
	input := []interface{}{
		map[string]interface{}{"type": "function_call", "name": "shell_command", "call_id": "call_x", "arguments": "{}"},
		f.item(t, "ONLY the fco_"),
	}
	before, _ := json.Marshal(input)

	normalized := normalizeResponsesToolCallOutputs(input)

	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatalf("input was mutated:\nbefore: %s\nafter:  %s", before, after)
	}
	if _, ok := input[1].(map[string]interface{})["call_id"]; ok {
		t.Error("original output item gained a call_id (shared map mutated)")
	}
	out, ok := normalized[1].(map[string]interface{})
	if !ok {
		t.Fatalf("normalized item is not a map: %#v", normalized[1])
	}
	if out["call_id"] != "call_x" {
		t.Errorf("normalized output call_id = %v, want call_x", out["call_id"])
	}
}

// --- Original symptom: custom tools must come back as custom_tool_call ---
//
// Codex declares apply_patch as {"type":"custom","format":{"type":"grammar",…}}.
// Emitting function_call instead made Codex answer every tool result with
// "aborted" and retry, so both streaming paths must emit custom_tool_call with
// the raw (unwrapped) input.

const capturedApplyPatch = "*** Begin Patch\n*** Update File: src/StatsPanel.tsx\n@@\n-  const x = 1\n+  const x = 2\n*** End Patch"

func TestResponsesCustomToolEmitsCustomToolCall_OllamaStreaming(t *testing.T) {
	f := loadCodexReplayFixture(t)
	upstreamBody := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		upstreamBody = string(buf)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(makeOllamaToolCallChunk("test", "call_patch", "apply_patch",
			map[string]interface{}{"input": capturedApplyPatch}, false, "")))
		w.Write([]byte(makeOllamaChunk("test", "", "", true, "tool_calls")))
	}))
	defer upstream.Close()

	respReq := &ResponsesAPIRequest{Model: "test", Stream: true, Tools: f.Tools}
	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "ollama")
	allTools := collectAllResponseTools(respReq)
	toolTypes := buildToolTypeMap(allTools)
	if toolTypes["apply_patch"] != "custom" {
		t.Fatalf("fixture tool apply_patch = %q, want custom", toolTypes["apply_patch"])
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","stream":true}`))
	w := httptest.NewRecorder()
	router.handleResponsesAPIOllamaStreaming(w, r, respReq, rp, toolTypes, buildToolNamespaceMap(allTools))

	if !strings.Contains(upstreamBody, `"apply_patch"`) {
		t.Errorf("upstream request did not declare apply_patch; body:\n%s", upstreamBody)
	}
	assertCustomToolCallEmitted(t, w.Body.String())
}

func TestResponsesCustomToolEmitsCustomToolCall_OpenAIStreaming(t *testing.T) {
	f := loadCodexReplayFixture(t)
	chunks := makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{
		"tool_calls": []map[string]interface{}{
			{"index": 0, "id": "call_patch", "type": "function", "function": map[string]interface{}{
				"name": "apply_patch", "arguments": `{"input":` + jsonQuote(capturedApplyPatch) + `}`,
			}},
		},
	}})
	chunks += makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "tool_calls"})
	chunks += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(chunks))
	}))
	defer upstream.Close()

	respReq := &ResponsesAPIRequest{Model: "test", Stream: true, Tools: f.Tools}
	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	allTools := collectAllResponseTools(respReq)
	toolTypes := buildToolTypeMap(allTools)

	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","stream":true}`))
	w := httptest.NewRecorder()
	router.handleResponsesAPIOpenAIStreaming(w, r, respReq, rp, toolTypes, buildToolNamespaceMap(allTools))

	assertCustomToolCallEmitted(t, w.Body.String())
}

func assertCustomToolCallEmitted(t *testing.T, body string) {
	t.Helper()
	events := parseSSEEvents(body)

	var completed map[string]interface{}
	sawInputDone := false
	for _, ev := range events {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			continue
		}
		switch payload["type"] {
		case "response.custom_tool_call_input.done":
			if payload["input"] == capturedApplyPatch {
				sawInputDone = true
			}
		case "response.completed":
			if resp, ok := payload["response"].(map[string]interface{}); ok {
				completed = resp
			}
		}
	}
	if !sawInputDone {
		t.Errorf("missing response.custom_tool_call_input.done with the raw patch; body:\n%s", body)
	}
	if completed == nil {
		t.Fatalf("no response.completed event; body:\n%s", body)
	}
	output, _ := completed["output"].([]interface{})
	for _, it := range output {
		m, ok := it.(map[string]interface{})
		if !ok || m["name"] != "apply_patch" {
			continue
		}
		if m["type"] != "custom_tool_call" {
			t.Errorf("output item type = %v, want custom_tool_call (function_call makes Codex abort the call)", m["type"])
		}
		if m["input"] != capturedApplyPatch {
			t.Errorf("output item input = %q, want the raw patch unwrapped", m["input"])
		}
		if _, hasArgs := m["arguments"]; hasArgs {
			t.Errorf("custom tool item must not carry an arguments field: %v", m)
		}
		return
	}
	t.Fatalf("no apply_patch output item; output: %v", output)
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- Codex view_image: image tool outputs must reach the model ---
//
// A Responses tool output is a string or an array of content parts. Codex's
// view_image returns image parts; reading only `text` made the tool result empty
// and the agent reported "view_image delivers nothing".

const fixturePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

func TestResponsesReplayForwardsImageToolOutputs(t *testing.T) {
	f := loadCodexReplayFixture(t)
	call := f.item(t, "view_image call")
	withText := f.item(t, "view_image result with text")
	imageOnly := f.item(t, "view_image result with image only")

	t.Run("ollama", func(t *testing.T) {
		msgs := translateResponsesAPIToOllama(&ResponsesAPIRequest{
			Model: "test", Input: []interface{}{call, withText},
		}).Messages
		tool := findToolMessage(t, msgs)
		if tool.Content != "Image loaded from src/StatsPanel.tsx.png" {
			t.Errorf("tool content = %q, want the input_text part", tool.Content)
		}
		if len(tool.Images) != 1 || tool.Images[0] != fixturePNG {
			t.Errorf("tool images = %v, want the bare base64 payload", tool.Images)
		}
		if tool.ToolCallID != "call_05mno" {
			t.Errorf("tool_call_id = %q, want call_05mno", tool.ToolCallID)
		}

		// Image-only output: the model must never get an empty tool result.
		msgs2 := translateResponsesAPIToOllama(&ResponsesAPIRequest{
			Model: "test", Input: []interface{}{f.item(t, "view_image call for the image-only"), imageOnly},
		}).Messages
		tool2 := findToolMessage(t, msgs2)
		if strings.TrimSpace(tool2.Content) == "" {
			t.Error("image-only tool output produced an empty tool message")
		}
		if len(tool2.Images) != 1 || tool2.Images[0] != fixturePNG {
			t.Errorf("image-only tool images = %v, want the bare base64 payload (object-form image_url)", tool2.Images)
		}
	})

	t.Run("chat-completions", func(t *testing.T) {
		msgs := translateResponsesAPIToChatCompletions(&ResponsesAPIRequest{
			Model: "test", Input: []interface{}{call, withText},
		}).Messages
		tool := findToolMessage(t, msgs)
		parts, ok := tool.Content.([]interface{})
		if !ok {
			t.Fatalf("content = %#v, want a multimodal parts array", tool.Content)
		}
		var sawText, sawImage bool
		for _, p := range parts {
			pm, _ := p.(map[string]interface{})
			switch pm["type"] {
			case "text":
				sawText = pm["text"] == "Image loaded from src/StatsPanel.tsx.png"
			case "image_url":
				url, _ := pm["image_url"].(map[string]interface{})
				sawImage = url["url"] == "data:image/png;base64,"+fixturePNG
			}
		}
		if !sawText || !sawImage {
			t.Errorf("parts = %#v, want both the text and the data-URL image part", parts)
		}

		// Text-only tool outputs must keep the plain-string shape so nothing
		// changes for the common case.
		plain := translateResponsesAPIToChatCompletions(&ResponsesAPIRequest{
			Model: "test", Input: []interface{}{
				map[string]interface{}{"type": "function_call", "name": "shell_command", "call_id": "call_txt", "arguments": "{}"},
				map[string]interface{}{"type": "function_call_output", "call_id": "call_txt", "output": "plain text"},
			},
		}).Messages
		if got, ok := findToolMessage(t, plain).Content.(string); !ok || got != "plain text" {
			t.Errorf("text-only tool content = %#v, want the plain string \"plain text\"", findToolMessage(t, plain).Content)
		}
	})
}

func findToolMessage[T OllamaMessage | OpenAIChatMessage](t *testing.T, msgs []T) T {
	t.Helper()
	for _, m := range msgs {
		switch v := any(m).(type) {
		case OllamaMessage:
			if v.Role == "tool" {
				return m
			}
		case OpenAIChatMessage:
			if v.Role == "tool" {
				return m
			}
		}
	}
	var zero T
	t.Fatalf("no role:\"tool\" message in %+v", msgs)
	return zero
}
