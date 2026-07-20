package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTranslateToOpenAI_StopToolChoiceThinking verifies the Anthropic->OpenAI
// request translation now forwards stop_sequences and tool_choice, and maps
// the thinking config to reasoning_effort (instead of a hardcoded "medium").
func TestTranslateToOpenAI_StopToolChoiceThinking(t *testing.T) {
	req := &AnthropicRequest{
		Model:         "m",
		MaxTokens:     100,
		StopSequences: []string{"END", "STOP"},
		Tools: []AnthropicTool{{
			Name:        "get_weather",
			Description: "weather",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: map[string]interface{}{"type": "any"},
		Thinking:   &AnthropicThinking{Type: "disabled"},
	}

	oai := translateToOpenAI(req)

	// stop_sequences forwarded
	stop, ok := oai.Stop.([]string)
	if !ok || len(stop) != 2 || stop[0] != "END" || stop[1] != "STOP" {
		t.Fatalf("expected stop=[END STOP], got %#v", oai.Stop)
	}

	// tool_choice any -> required
	if oai.ToolChoice != "required" {
		t.Fatalf("expected tool_choice=required, got %#v", oai.ToolChoice)
	}

	// thinking.type=disabled -> no reasoning_effort
	if oai.ReasoningEffort != "" {
		t.Fatalf("expected empty reasoning_effort for disabled thinking, got %q", oai.ReasoningEffort)
	}
}

func TestTranslateToOpenAI_InMessageSystemBecomesUserInPlace(t *testing.T) {
	req := &AnthropicRequest{
		Model:  "deepseek-v4-flash-free",
		System: []interface{}{map[string]interface{}{"type": "text", "text": "top system"}},
		Messages: []AnthropicMessage{
			{Role: "user", Content: "hi"},
			{Role: "system", Content: "mid reminder"},
			{Role: "assistant", Content: "ok"},
			{Role: "system", Content: "another reminder"},
		},
	}
	oai := translateToOpenAI(req)

	// The top-level system field is the ONLY system message, at the front.
	// It must NOT absorb the mid-conversation reminders (that would destroy
	// recency — e.g. Claude Code's "user sent a new message while you were
	// working" notice has to stay the last thing the model sees).
	if oai.Messages[0].Role != "system" {
		t.Fatalf("expected first message to be system, got %q", oai.Messages[0].Role)
	}
	sysText, _ := oai.Messages[0].Content.(string)
	if !strings.Contains(sysText, "top system") {
		t.Errorf("front system message lost top-level prompt: %q", sysText)
	}
	if strings.Contains(sysText, "mid reminder") || strings.Contains(sysText, "another reminder") {
		t.Errorf("front system message wrongly absorbed a mid-conversation reminder: %q", sysText)
	}

	// In-message system roles become user messages IN PLACE so their
	// position in the conversation is preserved.
	expectedRoles := []string{"system", "user", "user", "assistant", "user"}
	if len(oai.Messages) != len(expectedRoles) {
		t.Fatalf("expected %d messages, got %d: %v", len(expectedRoles), len(oai.Messages), oai.Messages)
	}
	for i, exp := range expectedRoles {
		if oai.Messages[i].Role != exp {
			t.Errorf("message[%d]: expected role %q, got %q", i, exp, oai.Messages[i].Role)
		}
	}
	// None of the messages after the front system should be a system role.
	for _, m := range oai.Messages[1:] {
		if m.Role == "system" {
			t.Errorf("unexpected mid-conversation system role: %#v", m)
		}
	}
	// The reminders must survive as user content at their original positions.
	mid, _ := oai.Messages[2].Content.(string)
	if !strings.Contains(mid, "mid reminder") {
		t.Errorf("mid reminder not preserved in place: %q", mid)
	}
	another, _ := oai.Messages[4].Content.(string)
	if !strings.Contains(another, "another reminder") {
		t.Errorf("trailing reminder not preserved as the final user message: %q", another)
	}
}

func TestTranslateToOpenAI_StripsAnthropicBillingHeader(t *testing.T) {
	req := &AnthropicRequest{
		Model:  "deepseek-v4-flash-free",
		System: []interface{}{map[string]interface{}{"type": "text", "text": "x-anthropic-billing-header:\nreal system prompt"}},
		Messages: []AnthropicMessage{
			{Role: "user", Content: "hi"},
		},
	}
	oai := translateToOpenAI(req)
	got, _ := oai.Messages[0].Content.(string)
	if strings.Contains(got, "x-anthropic-billing-header") {
		t.Errorf("billing header not stripped: %q", got)
	}
	if !strings.Contains(got, "real system prompt") {
		t.Errorf("real prompt missing: %q", got)
	}
}

func TestTranslateToOpenAI_DropsUnsignedThinkingHistory(t *testing.T) {
	msgs := translateMessageToOpenAI(AnthropicMessage{
		Role: "assistant",
		Content: []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "stale plan", "signature": ""},
		},
	})
	if len(msgs) != 0 {
		t.Fatalf("expected unsigned thinking-only history to be dropped, got %#v", msgs)
	}
}

func TestTranslateToOpenAI_PreservesReasoningForDeepSeekToolHistory(t *testing.T) {
	// DeepSeek/Kimi/MiMo require non-empty reasoning_content on assistant turns
	// that carry tool_use. cc-switch applies the same normalization.
	msgs := translateToOpenAI(&AnthropicRequest{
		Model: "deepseek-v4-flash-free",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "my plan", "signature": ""},
				map[string]interface{}{"type": "tool_use", "id": "t1", "name": "Write", "input": map[string]interface{}{"path": "a"}},
			},
		}},
	}).Messages

	if len(msgs) != 1 {
		t.Fatalf("expected 1 assistant message, got %d", len(msgs))
	}
	if msgs[0].ReasoningContent == nil || *msgs[0].ReasoningContent != "my plan" {
		t.Fatalf("expected reasoning_content=my plan, got %#v", msgs[0].ReasoningContent)
	}
}

func TestTranslateToOpenAI_PlaceholderReasoningWhenThinkingMissing(t *testing.T) {
	msgs := translateToOpenAI(&AnthropicRequest{
		Model: "deepseek-v4-flash-free",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "t1", "name": "Write", "input": map[string]interface{}{}},
			},
		}},
	}).Messages

	if len(msgs) != 1 || msgs[0].ReasoningContent == nil || *msgs[0].ReasoningContent != "tool call" {
		t.Fatalf("expected placeholder reasoning_content=tool call, got %#v", msgs[0].ReasoningContent)
	}
}

func TestTranslateToOpenAI_NoReasoningForNonReasoningModel(t *testing.T) {
	msgs := translateToOpenAI(&AnthropicRequest{
		Model: "claude-sonnet-4",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "my plan", "signature": ""},
				map[string]interface{}{"type": "tool_use", "id": "t1", "name": "Write", "input": map[string]interface{}{}},
			},
		}},
	}).Messages

	if len(msgs) != 1 || msgs[0].ReasoningContent != nil {
		t.Fatalf("expected no reasoning_content for non-reasoning model, got %#v", msgs[0].ReasoningContent)
	}
}

func TestTranslateToOpenAI_ThinkingBudgetMapping(t *testing.T) {
	cases := []struct {
		thinking *AnthropicThinking
		want     string
	}{
		{nil, ""},
		{&AnthropicThinking{Type: "disabled"}, ""},
		{&AnthropicThinking{Type: "enabled", BudgetTokens: 2000}, "low"},
		{&AnthropicThinking{Type: "enabled", BudgetTokens: 8000}, "medium"},
		{&AnthropicThinking{Type: "enabled", BudgetTokens: 32000}, "high"},
		{&AnthropicThinking{Type: "enabled"}, "medium"}, // no budget -> default medium
	}
	for _, c := range cases {
		got := anthropicThinkingToReasoningEffort(c.thinking)
		if got != c.want {
			t.Errorf("thinking=%+v: got %q, want %q", c.thinking, got, c.want)
		}
	}
}

func TestTranslateToOpenAI_ToolChoiceVariants(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want interface{}
	}{
		{"auto", "auto", "auto"},
		{"any", "any", "required"},
		{"none", "none", "none"},
		{"auto obj", map[string]interface{}{"type": "auto"}, "auto"},
		{"tool", map[string]interface{}{"type": "tool", "name": "foo"},
			map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "foo"}}},
		{"nil", nil, nil},
		{"unknown", map[string]interface{}{"type": "weird"}, nil},
	}
	for _, c := range cases {
		got := translateToolChoiceToOpenAI(c.in)
		if !equalJSON(got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, got, c.want)
		}
	}
}

// TestTranslateFromOpenAI_CacheTokens verifies cache_read_input_tokens are
// surfaced and input_tokens exclude the cached portion.
func TestTranslateFromOpenAI_CacheTokens(t *testing.T) {
	resp := &OpenAIChatResponse{
		ID:     "x",
		Object: "chat.completion",
		Model:  "m",
		Choices: []OpenAIChoice{{
			Index:        0,
			Message:      OpenAIChatMessage{Role: "assistant", Content: "hi"},
			FinishReason: "stop",
		}},
		Usage: OpenAIUsage{
			PromptTokens:        1000,
			CompletionTokens:    50,
			TotalTokens:         1050,
			PromptTokensDetails: &OpenAIPromptTokensDetails{CachedTokens: 700},
		},
	}
	out := translateFromOpenAI(resp, &AnthropicRequest{Model: "m"})
	if out.Usage.InputTokens != 300 {
		t.Errorf("input_tokens: got %d, want 300", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens != 50 {
		t.Errorf("output_tokens: got %d, want 50", out.Usage.OutputTokens)
	}
	if out.Usage.CacheReadInputTokens != 700 {
		t.Errorf("cache_read_input_tokens: got %d, want 700", out.Usage.CacheReadInputTokens)
	}
}

// TestTranslateContentBlocks_ToolResultImageAndError verifies the Anthropic->
// Ollama path preserves image parts in tool_result and prefixes is_error.
func TestTranslateContentBlocks_ToolResultImageAndError(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{
			"type":        "tool_result",
			"tool_use_id": "toolu_1",
			"is_error":    true,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "boom"},
				map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type":       "base64",
						"media_type": "image/png",
						"data":       "IMGDATA",
					},
				},
			},
		},
	}
	msgs := translateContentBlocksWithToolLookup("user", blocks, map[string]string{
		"toolu_1": "run_command",
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 ollama message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Role != "tool" {
		t.Fatalf("expected tool role, got %q", m.Role)
	}
	if m.Content != "[tool error] boom" {
		t.Errorf("expected error-prefixed content, got %q", m.Content)
	}
	if len(m.Images) != 1 || m.Images[0] != "IMGDATA" {
		t.Errorf("expected image forwarded, got %#v", m.Images)
	}
	if m.ToolName != "run_command" {
		t.Errorf("expected tool_name, got %q", m.ToolName)
	}
	if m.ToolCallID != "" {
		t.Errorf("expected tool_call_id to be omitted for Ollama, got %q", m.ToolCallID)
	}
}

// TestTranslateRequest_ThinkingDisabled verifies the Ollama path does not
// enable thinking when thinking.type == "disabled".
func TestTranslateRequest_ThinkingDisabled(t *testing.T) {
	anthro := &AnthropicRequest{
		Model:     "m",
		MaxTokens: 10,
		Thinking:  &AnthropicThinking{Type: "disabled"},
	}
	ollamaReq, err := translateRequest(anthro)
	if err != nil {
		t.Fatal(err)
	}
	if ollamaReq.Think != nil && ollamaReq.Think != false {
		t.Errorf("expected thinking disabled, got %#v", ollamaReq.Think)
	}
}

func TestTranslateAnthropicToOllama_PreservesBlocksAndOllamaToolShape(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"type": "thinking", "thinking": "plan", "signature": ""},
		map[string]interface{}{"type": "text", "text": "first turn"},
		map[string]interface{}{"type": "thinking", "thinking": "continue", "signature": ""},
		map[string]interface{}{"type": "text", "text": "second turn"},
		map[string]interface{}{
			"type": "tool_use", "id": "call_1", "name": "read_file",
			"input": map[string]interface{}{"path": "README.md"},
		},
	}

	msgs := translateContentBlocksWithToolLookup("assistant", blocks, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected one assistant message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Content != "first turn\n\nsecond turn" {
		t.Errorf("text blocks were not separated: %q", msg.Content)
	}
	if msg.Thinking != "plan\n\ncontinue" {
		t.Errorf("thinking blocks were not preserved: %q", msg.Thinking)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(msg.ToolCalls))
	}
	call := msg.ToolCalls[0]
	if call.Type != "function" {
		t.Errorf("expected function tool-call type, got %q", call.Type)
	}
	if call.ID != "" {
		t.Errorf("expected Anthropic id to be omitted for Ollama, got %q", call.ID)
	}
	if call.Function.Index == nil || *call.Function.Index != 0 {
		t.Errorf("expected function.index=0, got %#v", call.Function.Index)
	}
}

func TestTranslateAnthropicToOllama_SystemMessageBecomesReminderUser(t *testing.T) {
	msgs := translateMessage(AnthropicMessage{
		Role:    "system",
		Content: []interface{}{map[string]interface{}{"type": "text", "text": "refresh tools"}},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected one reminder message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("expected system reminder to become user, got %q", msgs[0].Role)
	}
	if msgs[0].Content != "<system-reminder>\nrefresh tools\n</system-reminder>" {
		t.Errorf("unexpected reminder content: %q", msgs[0].Content)
	}
}

func equalJSON(a, b interface{}) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}
