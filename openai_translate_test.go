package main

import (
	"encoding/json"
	"testing"
)

// TestTranslateOpenAIToOllama_ToolConversation exercises a realistic multi-turn
// tool conversation through the OpenAI->Ollama request translation.
func TestTranslateOpenAIToOllama_ToolConversation(t *testing.T) {
	req := &OpenAIChatRequest{
		Model: "m",
		Tools: []OpenAITool{{
			Type: "function",
			Function: OpenAIToolDef{
				Name:        "get_weather",
				Description: "get weather",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
			},
		}},
		Messages: []OpenAIChatMessage{
			{Role: "user", Content: "weather in NYC?"},
			{Role: "assistant", ToolCalls: []OpenAIToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: OpenAIToolCallFunc{
					Name:      "get_weather",
					Arguments: `{"location":"NYC"}`,
				},
			}}},
			{Role: "tool", ToolID: "call_1", Content: `{"temp":72}`},
		},
	}

	ollamaReq := translateOpenAIToOllama(req)
	if len(ollamaReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(ollamaReq.Messages))
	}

	// Message 1: user
	if ollamaReq.Messages[0].Role != "user" || ollamaReq.Messages[0].Content != "weather in NYC?" {
		t.Errorf("msg0: %+v", ollamaReq.Messages[0])
	}

	// Message 2: assistant with tool_calls
	m2 := ollamaReq.Messages[1]
	if m2.Role != "assistant" {
		t.Errorf("msg1 role: %s", m2.Role)
	}
	if len(m2.ToolCalls) != 1 {
		t.Fatalf("msg1 tool_calls len: %d", len(m2.ToolCalls))
	}
	if m2.ToolCalls[0].ID != "call_1" || m2.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("msg1 tool_call: %+v", m2.ToolCalls[0])
	}
	if m2.ToolCalls[0].Function.Arguments["location"] != "NYC" {
		t.Errorf("msg1 args: %+v", m2.ToolCalls[0].Function.Arguments)
	}

	// Message 3: tool result
	m3 := ollamaReq.Messages[2]
	if m3.Role != "tool" || m3.Content != `{"temp":72}` {
		t.Errorf("msg2: %+v", m3)
	}
	if m3.ToolCallID != "call_1" {
		t.Errorf("msg2 tool_call_id: %s", m3.ToolCallID)
	}
	if m3.ToolName != "get_weather" {
		t.Errorf("msg2 tool_name (should resolve from lookup): %s", m3.ToolName)
	}

	// Tools translated
	if len(ollamaReq.Tools) != 1 || ollamaReq.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tools: %+v", ollamaReq.Tools)
	}
}

// TestTranslateOllamaToOpenAI_ToolCalls checks the non-streaming response
// translation produces a valid OpenAI tool_calls response.
func TestTranslateOllamaToOpenAI_ToolCalls(t *testing.T) {
	ollama := &OllamaChatResponse{
		Model: "m",
		Message: OllamaMessage{
			Role: "assistant",
			ToolCalls: []OllamaToolCall{{
				ID: "call_1",
				Function: OllamaToolCallFunction{
					Name:      "get_weather",
					Arguments: map[string]interface{}{"location": "NYC"},
				},
			}},
		},
		DoneReason: "tool_call",
	}

	resp := translateOllamaToOpenAI(ollama, &OpenAIChatRequest{Model: "m"})
	if len(resp.Choices) != 1 {
		t.Fatalf("choices: %d", len(resp.Choices))
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason: %s", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls: %d", len(resp.Choices[0].Message.ToolCalls))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "get_weather" {
		t.Errorf("tool_call: %+v", tc)
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (%s)", err, tc.Function.Arguments)
	}
	if args["location"] != "NYC" {
		t.Errorf("args: %+v", args)
	}
}

// TestTranslateOllamaToOpenAI_ToolCallsMissingID verifies a generated ID is
// supplied when Ollama omits the tool call id.
func TestTranslateOllamaToOpenAI_ToolCallsMissingID(t *testing.T) {
	ollama := &OllamaChatResponse{
		Model: "m",
		Message: OllamaMessage{
			Role: "assistant",
			ToolCalls: []OllamaToolCall{{
				Function: OllamaToolCallFunction{
					Name:      "search",
					Arguments: map[string]interface{}{"q": "x"},
				},
			}},
		},
		DoneReason: "stop",
	}
	resp := translateOllamaToOpenAI(ollama, &OpenAIChatRequest{Model: "m"})
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID == "" {
		t.Error("expected generated tool call id, got empty")
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason should fall back to tool_calls: %s", resp.Choices[0].FinishReason)
	}
}

// TestTranslateOllamaToOpenAI_ContentAndToolCalls verifies content is preserved
// when Ollama returns both content and tool calls.
func TestTranslateOllamaToOpenAI_ContentAndToolCalls(t *testing.T) {
	ollama := &OllamaChatResponse{
		Model: "m",
		Message: OllamaMessage{
			Role:    "assistant",
			Content: "Let me search",
			ToolCalls: []OllamaToolCall{{
				ID: "call_1",
				Function: OllamaToolCallFunction{
					Name:      "search",
					Arguments: map[string]interface{}{"q": "x"},
				},
			}},
		},
		DoneReason: "tool_call",
	}
	resp := translateOllamaToOpenAI(ollama, &OpenAIChatRequest{Model: "m"})
	c, _ := resp.Choices[0].Message.Content.(string)
	if c != "Let me search" {
		t.Errorf("content lost: %v", resp.Choices[0].Message.Content)
	}
}