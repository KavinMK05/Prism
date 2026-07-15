package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Bug 1: additional_tools input items must be merged into the tools list ---

func TestCollectAllResponseTools_MergesAdditionalTools(t *testing.T) {
	req := &ResponsesAPIRequest{
		Tools: []interface{}{
			map[string]interface{}{"type": "function", "name": "top_level_fn", "parameters": map[string]interface{}{"type": "object"}},
		},
		Input: []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "hi"},
			map[string]interface{}{
				"type":  "additional_tools",
				"tools": []interface{}{
					map[string]interface{}{"type": "function", "name": "apply_patch"},
					map[string]interface{}{"type": "custom", "name": "freeform"},
				},
			},
		},
	}
	all := collectAllResponseTools(req)
	if len(all) != 3 {
		t.Fatalf("expected 3 merged tools, got %d: %v", len(all), all)
	}
	names := []string{}
	for _, tool := range all {
		if m, ok := tool.(map[string]interface{}); ok {
			names = append(names, getMapString(m, "name"))
		}
	}
	if names[0] != "top_level_fn" || names[1] != "apply_patch" || names[2] != "freeform" {
		t.Errorf("unexpected tool order/names: %v", names)
	}
}

func TestTranslateResponsesAPIToChatCompletions_ForwardsAdditionalTools(t *testing.T) {
	// Codex Desktop "Responses Lite" declares tools inside an additional_tools
	// input item. Without merging, these tools never reach the upstream and the
	// model cannot emit tool calls.
	req := &ResponsesAPIRequest{
		Model: "test",
		Input: []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "do thing"},
			map[string]interface{}{
				"type": "additional_tools",
				"tools": []interface{}{
					map[string]interface{}{"type": "function", "name": "search", "parameters": map[string]interface{}{"type": "object"}},
				},
			},
		},
	}
	chatReq := translateResponsesAPIToChatCompletions(req)
	if len(chatReq.Tools) != 1 {
		t.Fatalf("expected 1 forwarded tool, got %d: %+v", len(chatReq.Tools), chatReq.Tools)
	}
	if chatReq.Tools[0].Function.Name != "search" {
		t.Errorf("expected forwarded tool name 'search', got %q", chatReq.Tools[0].Function.Name)
	}
}

// --- Bug 2: consecutive function_call items must buffer into ONE assistant message ---

func TestTranslateResponsesInputToChatMessages_BuffersParallelToolCalls(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "run both"},
		map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "foo", "arguments": `{"a":1}`},
		map[string]interface{}{"type": "function_call", "call_id": "call_2", "name": "bar", "arguments": `{"b":2}`},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": "r1"},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_2", "output": "r2"},
	}
	msgs := translateResponsesInputToChatMessages(input)

	// Expect: system? none. user, assistant(with 2 tool_calls), tool, tool
	// Find the assistant message carrying tool calls.
	var assistantToolMsg *OpenAIChatMessage
	for i := range msgs {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			if assistantToolMsg != nil {
				t.Fatalf("expected a single assistant message with tool calls, found a second at index %d", i)
			}
			m := msgs[i]
			assistantToolMsg = &m
		}
	}
	if assistantToolMsg == nil {
		t.Fatalf("no assistant tool-call message found; messages=%+v", msgs)
	}
	if len(assistantToolMsg.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls in one assistant message, got %d", len(assistantToolMsg.ToolCalls))
	}
	// Adjacency: the two tool outputs must immediately follow the assistant
	// tool-call message, with no interleaved assistant message in between.
	ai := -1
	for i := range msgs {
		if &msgs[i] == assistantToolMsg || (msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) == 2) {
			ai = i
			break
		}
	}
	if ai < 0 || ai+2 >= len(msgs) {
		t.Fatalf("cannot verify tool adjacency; ai=%d len=%d", ai, len(msgs))
	}
	if msgs[ai+1].Role != "tool" || msgs[ai+2].Role != "tool" {
		t.Errorf("expected tool,tool right after assistant tool-call message; got %s,%s", msgs[ai+1].Role, msgs[ai+2].Role)
	}
}

// --- Bug: assistant message content parts with type "output_text" must not be dropped ---

func TestTranslateResponsesInputToChatMessages_PreservesOutputText(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "hi"},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": "thinking about it"}}},
		map[string]interface{}{"type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": "I was checking the repo URL."}}},
		map[string]interface{}{"type": "message", "role": "user", "content": "ok"},
	}
	msgs := translateResponsesInputToChatMessages(input)
	var asst string
	for _, m := range msgs {
		if m.Role == "assistant" {
			if s, ok := m.Content.(string); ok {
				asst = s
			} else {
				b, _ := json.Marshal(m.Content)
				asst = string(b)
			}
		}
	}
	if !strings.Contains(asst, "I was checking the repo URL.") {
		t.Errorf("assistant output_text content was dropped; assistant content = %q", asst)
	}
}

func TestTranslateResponsesInputToOllamaMessages_PreservesOutputText(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "message", "role": "user", "content": "hi"},
		map[string]interface{}{"type": "reasoning", "summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": "thinking about it"}}},
		map[string]interface{}{"type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": "I was checking the repo URL."}}},
		map[string]interface{}{"type": "message", "role": "user", "content": "ok"},
	}
	msgs := translateResponsesInputToOllamaMessages(input)
	var asst OllamaMessage
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 0 {
			asst = m
		}
	}
	if asst.Content != "I was checking the repo URL." {
		t.Errorf("assistant output_text content was dropped; got content=%q thinking=%q", asst.Content, asst.Thinking)
	}
	// The preceding reasoning should attach as thinking, not clobber content.
	if asst.Thinking != "thinking about it" {
		t.Errorf("expected reasoning carried as thinking, got %q", asst.Thinking)
	}
}

func TestConvertResponsesCustomToolToOllama_PreservesFormatGrammar(t *testing.T) {
	toolMap := map[string]interface{}{
		"type":        "custom",
		"name":         "apply_patch",
		"description":  "Use the `apply_patch` tool to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
		"format": map[string]interface{}{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": "start: begin_patch hunk+ end_patch\r\nbegin_patch: \"*** Begin Patch\" LF\r\nend_patch: \"*** End Patch\" LF?\r\n",
		},
	}
	tool, ok := convertResponsesCustomToolToOllama(toolMap, "")
	if !ok {
		t.Fatalf("expected tool to convert")
	}
	desc := tool.Function.Description
	if !strings.Contains(desc, "*** Begin Patch") {
		t.Errorf("grammar definition dropped from description: %q", desc)
	}
	if !strings.Contains(desc, "do not wrap the patch in JSON") {
		t.Errorf("original description lost: %q", desc)
	}
	// CRLF must be normalized to LF so the model doesn't see literal \r\n.
	if strings.Contains(desc, "\r\n") {
		t.Errorf("CRLF not normalized in description: %q", desc)
	}
}

func TestConvertResponsesCustomToolToOpenAIChat_PreservesFormatGrammar(t *testing.T) {
	toolMap := map[string]interface{}{
		"type":        "custom",
		"name":         "apply_patch",
		"description":  "Use the `apply_patch` tool to edit files.",
		"format": map[string]interface{}{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": "begin_patch: \"*** Begin Patch\" LF",
		},
	}
	tool, ok := convertResponsesCustomToolToOpenAIChat(toolMap, "")
	if !ok {
		t.Fatalf("expected tool to convert")
	}
	if !strings.Contains(tool.Function.Description, "*** Begin Patch") {
		t.Errorf("grammar definition dropped from description: %q", tool.Function.Description)
	}
}

func TestConvertResponsesCustomTool_NoFormatKeepsPlainDescription(t *testing.T) {
	toolMap := map[string]interface{}{
		"type":        "custom",
		"name":         "freeform",
		"description":  "do anything",
	}
	tool, ok := convertResponsesCustomToolToOllama(toolMap, "")
	if !ok {
		t.Fatalf("expected tool to convert")
	}
	if tool.Function.Description != "do anything" {
		t.Errorf("description should be unchanged without format, got %q", tool.Function.Description)
	}
}

func TestTranslateResponsesInputToChatMessages_CustomToolCallWrapsInput(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"type": "custom_tool_call", "call_id": "call_x", "name": "apply_patch", "input": "*** Begin Patch\n+hi\n*** End Patch"},
		map[string]interface{}{"type": "custom_tool_call_output", "call_id": "call_x", "output": "ok"},
	}
	msgs := translateResponsesInputToChatMessages(input)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "assistant" || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("expected assistant with 1 tool call, got %+v", msgs[0])
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(msgs[0].ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if args["input"] != "*** Begin Patch\n+hi\n*** End Patch" {
		t.Errorf("expected wrapped {\"input\": ...}, got %v", args)
	}
	if msgs[1].Role != "tool" || msgs[1].ToolID != "call_x" {
		t.Errorf("expected tool output message, got %+v", msgs[1])
	}
}

// --- Bug 4: custom + namespace tool types (request forwarding) ---

func TestTranslateResponseToolsToChatCompletions_CustomAndNamespace(t *testing.T) {
	tools := []interface{}{
		map[string]interface{}{"type": "custom", "name": "freeform", "description": "do anything"},
		map[string]interface{}{
			"type": "namespace",
			"name": "mcp",
			"tools": []interface{}{
				map[string]interface{}{"type": "function", "name": "child_fn", "parameters": map[string]interface{}{"type": "object"}},
				map[string]interface{}{"type": "custom", "name": "child_custom"},
			},
		},
		map[string]interface{}{"type": "apply_patch"},
	}
	out := translateResponseToolsToChatCompletions(tools)
	// custom -> 1, namespace -> 2 children, apply_patch -> 1 = 4
	if len(out) != 4 {
		t.Fatalf("expected 4 chat tools, got %d: %+v", len(out), out)
	}
	want := map[string]string{
		"freeform":         "function",
		"mcp__child_fn":    "function",
		"mcp__child_custom": "function",
		"apply_patch":      "function",
	}
	for _, tool := range out {
		if _, ok := want[tool.Function.Name]; !ok {
			t.Errorf("unexpected tool name %q", tool.Function.Name)
		}
	}
	// Custom tools must be wrapped with an {"input": string} schema.
	for _, tool := range out {
		if tool.Function.Name == "freeform" || tool.Function.Name == "mcp__child_custom" {
			p, _ := json.Marshal(tool.Function.Parameters)
			ps := string(p)
			if !strings.Contains(ps, `"input"`) || !strings.Contains(ps, `"required":["input"]`) {
				t.Errorf("custom tool %q must have input-string schema, got %s", tool.Function.Name, ps)
			}
		}
	}
}

func TestBuildToolTypeMap_RecognizesCustomAndNamespace(t *testing.T) {
	tools := []interface{}{
		map[string]interface{}{"type": "custom", "name": "freeform"},
		map[string]interface{}{"type": "namespace", "name": "mcp", "tools": []interface{}{
			map[string]interface{}{"type": "function", "name": "child_fn"},
			map[string]interface{}{"type": "custom", "name": "child_custom"},
		}},
		map[string]interface{}{"type": "apply_patch"},
		map[string]interface{}{"type": "web_search_preview"},
	}
	m := buildToolTypeMap(tools)
	if m["freeform"] != "custom" {
		t.Errorf("freeform should map to custom, got %q", m["freeform"])
	}
	if m["mcp__child_fn"] != "function" {
		t.Errorf("mcp__child_fn should map to function, got %q", m["mcp__child_fn"])
	}
	if m["mcp__child_custom"] != "custom" {
		t.Errorf("mcp__child_custom should map to custom, got %q", m["mcp__child_custom"])
	}
	if m["apply_patch"] != "apply_patch" {
		t.Errorf("apply_patch mapping wrong: %q", m["apply_patch"])
	}
	if m["web_search_preview"] != "web_search_preview" {
		t.Errorf("web_search_preview mapping wrong: %q", m["web_search_preview"])
	}
	// resolveToolOutputType must produce the right output item types.
	if resolveToolOutputType("freeform", m) != "custom_tool_call" {
		t.Errorf("freeform -> custom_tool_call expected, got %q", resolveToolOutputType("freeform", m))
	}
	if resolveToolOutputType("mcp__child_custom", m) != "custom_tool_call" {
		t.Errorf("mcp__child_custom -> custom_tool_call expected, got %q", resolveToolOutputType("mcp__child_custom", m))
	}
	if resolveToolOutputType("web_search_preview", m) != "web_search_call" {
		t.Errorf("web_search_preview -> web_search_call expected, got %q", resolveToolOutputType("web_search_preview", m))
	}
	if resolveToolOutputType("mcp__child_fn", m) != "function_call" {
		t.Errorf("mcp__child_fn -> function_call expected, got %q", resolveToolOutputType("mcp__child_fn", m))
	}
}

func TestBuildToolNamespaceMap_AndSplit(t *testing.T) {
	tools := []interface{}{
		map[string]interface{}{"type": "namespace", "name": "mcp", "tools": []interface{}{
			map[string]interface{}{"type": "function", "name": "child_fn"},
			map[string]interface{}{"type": "custom", "name": "child_custom"},
		}},
	}
	ns := buildToolNamespaceMap(tools)
	if ns["mcp__child_fn"] != "mcp" {
		t.Errorf("mcp__child_fn namespace wrong: %q", ns["mcp__child_fn"])
	}
	if ns["mcp__child_custom"] != "mcp" {
		t.Errorf("mcp__child_custom namespace wrong: %q", ns["mcp__child_custom"])
	}
	child, namespace := splitToolNamespace("mcp__child_fn", ns)
	if child != "child_fn" || namespace != "mcp" {
		t.Errorf("split wrong: child=%q namespace=%q", child, namespace)
	}
	// Non-namespaced names pass through unchanged.
	child, namespace = splitToolNamespace("plain_fn", ns)
	if child != "plain_fn" || namespace != "" {
		t.Errorf("plain split wrong: child=%q namespace=%q", child, namespace)
	}
}

// --- Bug 4: response-side namespace split (non-streaming) ---

func TestTranslateChatCompletionsToResponsesAPI_NamespaceSplit(t *testing.T) {
	tools := []interface{}{
		map[string]interface{}{"type": "namespace", "name": "mcp", "tools": []interface{}{
			map[string]interface{}{"type": "function", "name": "child_fn", "parameters": map[string]interface{}{"type": "object"}},
		}},
	}
	toolTypes := buildToolTypeMap(tools)
	toolNamespaces := buildToolNamespaceMap(tools)

	idx0, idx1 := 0, 1
	resp := &OpenAIChatResponse{ID: "c", Object: "chat.completion", Model: "m",
		Choices: []OpenAIChoice{{Index: 0, FinishReason: "tool_calls",
			Message: OpenAIChatMessage{Role: "assistant", ToolCalls: []OpenAIToolCall{
				{ID: "call_a", Index: &idx0, Type: "function", Function: OpenAIToolCallFunc{Name: "mcp__child_fn", Arguments: `{"q":1}`}},
				{ID: "call_b", Index: &idx1, Type: "function", Function: OpenAIToolCallFunc{Name: "plain_fn", Arguments: `{}`}},
			}}}}}
	out := translateChatCompletionsToResponsesAPI(resp, &ResponsesAPIRequest{Model: "m"}, toolTypes, toolNamespaces)
	b, _ := json.Marshal(out)
	s := string(b)
	// namespace child must be split into name + namespace.
	if !strings.Contains(s, `"name":"child_fn"`) {
		t.Errorf("expected name 'child_fn' after split, got:\n%s", s)
	}
	if !strings.Contains(s, `"namespace":"mcp"`) {
		t.Errorf("expected namespace 'mcp', got:\n%s", s)
	}
	if !strings.Contains(s, `"name":"plain_fn"`) {
		t.Errorf("expected name 'plain_fn' preserved, got:\n%s", s)
	}
	// plain_fn must NOT carry a namespace field.
	plainSeg := substringAfter(s, `"name":"plain_fn"`)
	if strings.Contains(plainSeg[:min(len(plainSeg), 40)], `"namespace"`) {
		t.Errorf("plain_fn should not have namespace field")
	}
}

func TestTranslateChatCompletionsToResponsesAPI_CustomToolCallOutput(t *testing.T) {
	tools := []interface{}{map[string]interface{}{"type": "custom", "name": "freeform"}}
	toolTypes := buildToolTypeMap(tools)
	resp := &OpenAIChatResponse{ID: "c", Object: "chat.completion", Model: "m",
		Choices: []OpenAIChoice{{Index: 0, FinishReason: "tool_calls",
			Message: OpenAIChatMessage{Role: "assistant", ToolCalls: []OpenAIToolCall{
				{ID: "call_a", Type: "function", Function: OpenAIToolCallFunc{Name: "freeform", Arguments: `{"input":"hello world"}`}},
			}}}}}
	out := translateChatCompletionsToResponsesAPI(resp, &ResponsesAPIRequest{Model: "m"}, toolTypes, nil)
	b, _ := json.Marshal(out)
	s := string(b)
	if !strings.Contains(s, `"type":"custom_tool_call"`) {
		t.Errorf("expected custom_tool_call output item, got:\n%s", s)
	}
	// The {"input": "..."} wrapper must be unwrapped into the raw input field.
	if !strings.Contains(s, `"input":"hello world"`) {
		t.Errorf("expected unwrapped input 'hello world', got:\n%s", s)
	}
}

// --- Bug 3: streaming parallel tool calls must be tracked by index ---

func makeOpenAIStreamChunk(id string, choices ...map[string]interface{}) string {
	ch := map[string]interface{}{
		"id": id, "object": "chat.completion.chunk", "created": 1, "model": "m",
		"choices": choices,
	}
	b, _ := json.Marshal(ch)
	return "data: " + string(b) + "\n"
}

func TestResponsesStreaming_ParallelToolCallsKeyedByIndex(t *testing.T) {
	// Upstream streams two parallel tool calls whose argument deltas interleave
	// across chunks. Without index-keyed tracking the single accumulator mixes
	// the arguments together.
	chunks := ""
	chunks += makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{
		"tool_calls": []map[string]interface{}{
			{"index": 0, "id": "call_a", "type": "function", "function": map[string]interface{}{"name": "foo", "arguments": ""}},
		}}})
	// foo arg delta #1
	chunks += makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{
		"tool_calls": []map[string]interface{}{
			{"index": 0, "function": map[string]interface{}{"arguments": `{"x`}},
		}}})
	// Open second tool call (index 1)
	chunks += makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{
		"tool_calls": []map[string]interface{}{
			{"index": 1, "id": "call_b", "type": "function", "function": map[string]interface{}{"name": "bar", "arguments": ""}},
		}}})
	// foo arg delta #2 (arrives AFTER bar was opened; must still land on call_a)
	chunks += makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{
		"tool_calls": []map[string]interface{}{
			{"index": 0, "function": map[string]interface{}{"arguments": `":1}`}},
		}}})
	// bar full args in one delta
	chunks += makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{
		"tool_calls": []map[string]interface{}{
			{"index": 1, "function": map[string]interface{}{"arguments": `{"y":2}`}},
		}}})
	// finish
	fr := "tool_calls"
	chunks += makeOpenAIStreamChunk("c1", map[string]interface{}{"index": 0, "delta": map[string]interface{}{}, "finish_reason": fr})
	chunks += "data: [DONE]\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(chunks))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rp := makeTestRP(upstream.URL, "openai")
	respReq := &ResponsesAPIRequest{Model: "test", Stream: true}
	allTools := collectAllResponseTools(respReq)
	toolTypes := buildToolTypeMap(allTools)
	toolNamespaces := buildToolNamespaceMap(allTools)

	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","stream":true,"input":"go"}`))
	w := httptest.NewRecorder()
	router.handleResponsesAPIOpenAIStreaming(w, r, respReq, rp, toolTypes, toolNamespaces)

	body := w.Body.String()
	events := parseOpenAISSEEvents(body)

	// Collect function_call output items from response.completed.
	var completed map[string]interface{}
	for _, e := range events {
		if t, ok := e["type"].(string); ok && t == "response.completed" {
			if resp, ok := e["response"].(map[string]interface{}); ok {
				completed = resp
			}
		}
	}
	if completed == nil {
		t.Fatalf("no response.completed event; body:\n%s", body)
	}
	output, _ := completed["output"].([]interface{})
	if len(output) != 2 {
		t.Fatalf("expected 2 output items, got %d: %v", len(output), output)
	}
	argsByName := map[string]string{}
	for _, item := range output {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "function_call" {
			argsByName[m["name"].(string)] = m["arguments"].(string)
		}
	}
	if argsByName["foo"] != `{"x":1}` {
		t.Errorf("foo arguments scrambled: got %q want {\"x\":1}", argsByName["foo"])
	}
	if argsByName["bar"] != `{"y":2}` {
		t.Errorf("bar arguments scrambled: got %q want {\"y\":2}", argsByName["bar"])
	}
	// There must be exactly two output_item.added events (one per tool call),
	// proving the second call did not collapse into the first.
	added := 0
	for _, e := range events {
		if t, ok := e["type"].(string); ok && t == "response.output_item.added" {
			added++
		}
	}
	// added events: reasoning? none here. message? none (no content). So 2 tool added.
	if added != 2 {
		t.Errorf("expected exactly 2 output_item.added events for tool calls, got %d", added)
	}
}

func substringAfter(s, sep string) string {
	i := strings.Index(s, sep)
	if i < 0 {
		return ""
	}
	return s[i+len(sep):]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}