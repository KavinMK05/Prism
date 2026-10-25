package proxy

import (
	"testing"
)

// TestCodexResponsesCarriesImageContent verifies that multimodal content reaches
// a Responses/Codex request body. contentToString reads only text parts, so
// before this both a tool result's images and a user turn's pasted screenshots
// were dropped on the whole Claude /v1/messages -> Responses path.
func TestCodexResponsesCarriesImageContent(t *testing.T) {
	const dataURL = "data:image/png;base64," + fixturePNG

	// imageContent is the intermediate Chat Completions shape the Anthropic
	// translators produce for a message with text and one image part.
	imageContent := []interface{}{
		map[string]interface{}{"type": "text", "text": "look at this"},
		map[string]interface{}{
			"type":      "image_url",
			"image_url": map[string]interface{}{"url": dataURL},
		},
	}

	// imagePartsOf pulls the content parts off a body item, whichever JSON type
	// the translator chose (the tool path uses []map[string]interface{}).
	imagePartsOf := func(item map[string]interface{}, key string) []map[string]interface{} {
		t.Helper()
		switch v := item[key].(type) {
		case []map[string]interface{}:
			return v
		case []interface{}:
			out := make([]map[string]interface{}, 0, len(v))
			for _, raw := range v {
				if m, ok := raw.(map[string]interface{}); ok {
					out = append(out, m)
				}
			}
			return out
		}
		t.Fatalf("%s = %#v, want a parts array", key, item[key])
		return nil
	}

	findItem := func(t *testing.T, input []interface{}, itemType string) map[string]interface{} {
		t.Helper()
		for _, raw := range input {
			m, _ := raw.(map[string]interface{})
			if m["type"] == itemType {
				return m
			}
		}
		t.Fatalf("no %q item in input: %#v", itemType, input)
		return nil
	}

	t.Run("tool_output", func(t *testing.T) {
		body := translateChatCompletionsToCodexResponses(&OpenAIChatRequest{
			Model: "test",
			Messages: []OpenAIChatMessage{
				{Role: "assistant", ToolCalls: []OpenAIToolCall{{
					ID: "call_1", Type: "function",
					Function: OpenAIToolCallFunc{Name: "screenshot", Arguments: "{}"},
				}}},
				{Role: "tool", ToolID: "call_1", Content: imageContent},
			},
		}, true)

		item := findItem(t, body["input"].([]interface{}), "function_call_output")
		if item["call_id"] != "call_1" {
			t.Errorf("call_id = %#v, want call_1", item["call_id"])
		}
		parts := imagePartsOf(item, "output")
		var sawText, sawImage bool
		for _, p := range parts {
			switch p["type"] {
			case "input_text":
				sawText = p["text"] == "look at this"
			case "input_image":
				sawImage = p["image_url"] == dataURL
			}
		}
		if !sawText || !sawImage {
			t.Errorf("tool output parts = %#v, want both input_text and input_image", parts)
		}
	})

	t.Run("user_message", func(t *testing.T) {
		body := translateChatCompletionsToCodexResponses(&OpenAIChatRequest{
			Model: "test",
			Messages: []OpenAIChatMessage{
				{Role: "user", Content: imageContent},
			},
		}, true)

		item := findItem(t, body["input"].([]interface{}), "message")
		parts := imagePartsOf(item, "content")
		var sawText, sawImage bool
		for _, p := range parts {
			switch p["type"] {
			case "input_text":
				sawText = p["text"] == "look at this"
			case "input_image":
				sawImage = p["image_url"] == dataURL
			}
		}
		if !sawText || !sawImage {
			t.Errorf("message parts = %#v, want both input_text and input_image", parts)
		}
	})

	// Text-only content must keep the shapes the existing tests pin: a plain
	// string for a tool output, a single input_text part for a message.
	t.Run("text_only_unchanged", func(t *testing.T) {
		body := translateChatCompletionsToCodexResponses(&OpenAIChatRequest{
			Model: "test",
			Messages: []OpenAIChatMessage{
				{Role: "tool", ToolID: "call_9", Content: "plain text"},
				{Role: "user", Content: "hello"},
			},
		}, true)

		input := body["input"].([]interface{})
		if got := findItem(t, input, "function_call_output")["output"]; got != "plain text" {
			t.Errorf("text-only function_call_output.output = %#v, want the plain string", got)
		}
		parts := imagePartsOf(findItem(t, input, "message"), "content")
		if len(parts) != 1 || parts[0]["type"] != "input_text" || parts[0]["text"] != "hello" {
			t.Errorf("text-only message content = %#v, want a single input_text part", parts)
		}
	})
}
