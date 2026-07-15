package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesNonStreamReasoningAndIDs(t *testing.T) {
	rc := "thinking about the answer"
	msg := OpenAIChatMessage{Role: "assistant", Content: "hello", ReasoningContent: &rc,
		ToolCalls: []OpenAIToolCall{{ID: "call_abc", Type: "function",
			Function: OpenAIToolCallFunc{Name: "do_thing", Arguments: `{"x":1}`}}}}
	resp := &OpenAIChatResponse{ID: "chat_1", Object: "chat.completion", Model: "gpt-test",
		Choices: []OpenAIChoice{{Index: 0, Message: msg, FinishReason: "stop"}},
		Usage:   OpenAIUsage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12}}

	req := &ResponsesAPIRequest{Model: "gpt-test", Instructions: "be helpful",
		MaxOutputTokens: 100, ParallelToolCalls: boolPtr(true), ToolChoice: "auto"}

	out := translateChatCompletionsToResponsesAPI(resp, req, map[string]string{}, map[string]string{})
	b, _ := json.Marshal(out)
	s := string(b)

	checks := map[string]string{
		"reasoning summary text":   `"summary":[{"type":"summary_text","text":"thinking about the answer"`,
		"background field":         `"background":false`,
		"error field null":         `"error":null`,
		"incomplete_details null":  `"incomplete_details":null`,
		"echo instructions":        `"instructions":"be helpful"`,
		"echo max_output_tokens":   `"max_output_tokens":100`,
		"echo parallel_tool_calls": `"parallel_tool_calls":true`,
		"echo tool_choice":         `"tool_choice":"auto"`,
		"distinct fc id":           `"id":"fc_call_abc"`,
		"distinct call_id":         `"call_id":"call_abc"`,
		"output_text":              `"output_text":"hello"`,
	}
	for name, want := range checks {
		if !strings.Contains(s, want) {
			t.Errorf("%s: missing %q\nFULL: %s", name, want, s)
		}
	}
	// Should NOT contain an empty message item with empty output_text when only tool calls...
	// Here we have content "hello", so a message item should exist.
	if !strings.Contains(s, `"type":"message"`) {
		t.Errorf("message item missing:\n%s", s)
	}
}

func TestResponsesNonStreamOnlyToolCallsNoEmptyMessage(t *testing.T) {
	msg := OpenAIChatMessage{Role: "assistant", Content: "",
		ToolCalls: []OpenAIToolCall{{ID: "call_x", Type: "function",
			Function: OpenAIToolCallFunc{Name: "do_it", Arguments: `{}`}}}}
	resp := &OpenAIChatResponse{ID: "c", Object: "chat.completion", Model: "m",
		Choices: []OpenAIChoice{{Index: 0, Message: msg, FinishReason: "tool_calls"}}}
	out := translateChatCompletionsToResponsesAPI(resp, &ResponsesAPIRequest{Model: "m"}, nil, nil)
	b, _ := json.Marshal(out)
	s := string(b)
	if strings.Contains(s, `"type":"message"`) {
		t.Errorf("expected NO empty message item when only tool calls, got:\n%s", s)
	}
	if !strings.Contains(s, `"type":"function_call"`) {
		t.Errorf("expected function_call item, got:\n%s", s)
	}
}

func boolPtr(b bool) *bool { return &b }
