package main

import (
	"encoding/json"
	"testing"
)

func rmsg(t *testing.T, raw string) AnthropicMessage {
	t.Helper()
	var m AnthropicMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	return m
}

// extractToolUseID pulls the tool_use id out of an Anthropic response content
// slice (mix of typed structs and maps).
func extractToolUseID(t *testing.T, content []interface{}) string {
	t.Helper()
	for _, c := range content {
		m, ok := c.(map[string]interface{})
		if !ok {
			bb, _ := json.Marshal(c)
			json.Unmarshal(bb, &m)
		}
		if m != nil && m["type"] == "tool_use" {
			id, _ := m["id"].(string)
			return id
		}
	}
	t.Fatalf("no tool_use block found in content")
	return ""
}

// TestMessagesHistoryRoundtrip_ToolUseID is a regression test for the
// "/v1/messages" translation path. The proxy must round-trip its own
// assistant responses back into upstream history correctly: the tool_use ID
// it emits in a response must (a) be non-empty and unique, and (2) survive the
// next turn when the client sends back a tool_result referencing it.
//
// Root cause being guarded against: translateFromOpenAI (the OpenAI
// non-streaming response translator) was the only response translator that did
// not mint a unique tool_use ID when the upstream omitted one. It returned
// id:"" to the client; the client's follow-up tool_result then carried
// tool_use_id:""; and translateContentBlocksToOpenAI dropped any tool_result
// whose id was empty. The net effect was that the tool result vanished from
// history and the model re-invoked the tool in a loop. The Ollama path
// (translateResponse) and both streaming paths already minted unique IDs.
func TestMessagesHistoryRoundtrip_ToolUseID(t *testing.T) {
	// --- Ollama path: upstream returns a tool call with NO id ---------------
	ollamaTurn1 := &OllamaChatResponse{
		Model:      "test-model",
		DoneReason: "tool_call",
		Message: OllamaMessage{
			Role: "assistant",
			ToolCalls: []OllamaToolCall{{
				ID: "", // Ollama often omits the id
				Function: OllamaToolCallFunction{
					Name:      "search",
					Arguments: map[string]interface{}{"q": "golang"},
				},
			}},
		},
	}
	anthroOllama := translateResponse(ollamaTurn1, &AnthropicRequest{Model: "test-model"})
	idOllama := extractToolUseID(t, anthroOllama.Content)
	if idOllama == "" {
		t.Fatal("Ollama path: proxy returned a tool_use with empty id")
	}

	// --- OpenAI non-streaming path: upstream returns a tool call with NO id -
	oai := &OpenAIChatResponse{
		Model: "test-model",
		Choices: []OpenAIChoice{{
			Message: OpenAIChatMessage{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{{
					ID:   "", // upstream OpenAI-compatible server omitted the id
					Type: "function",
					Function: OpenAIToolCallFunc{
						Name:      "search",
						Arguments: `{"q":"go"}`,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
	anthroOpenAI := translateFromOpenAI(oai, &AnthropicRequest{Model: "test-model"})
	idOpenAI := extractToolUseID(t, anthroOpenAI.Content)
	if idOpenAI == "" {
		t.Fatal("OpenAI path: proxy returned a tool_use with empty id (root cause not fixed)")
	}
	if idOpenAI == idOllama {
		t.Fatal("tool_use ids collided across responses (not unique)")
	}

	// --- Feed both back as history through the OpenAI request translator -----
	mkHistory := func(id string) []AnthropicMessage {
		asstRaw, _ := json.Marshal(AnthropicMessage{Role: "assistant", Content: anthroOpenAI.Content})
		_ = asstRaw
		// Build the assistant message from the OpenAI response content.
		asst := AnthropicMessage{Role: "assistant", Content: anthroOpenAI.Content}
		asstBytes, _ := json.Marshal(asst)
		return []AnthropicMessage{
			rmsg(t, `{"role":"user","content":"q"}`),
			rmsg(t, string(asstBytes)),
			rmsg(t, `{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"`+id+`","content":"r"}
			]}`),
		}
	}

	t.Run("OpenAI_request_keeps_tool_result", func(t *testing.T) {
		r := translateToOpenAI(&AnthropicRequest{Model: "test-model", Messages: mkHistory(idOpenAI)})
		var asstID, toolID string
		sawTool := false
		for _, m := range r.Messages {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				asstID = m.ToolCalls[0].ID
			}
			if m.Role == "tool" {
				sawTool = true
				toolID = m.ToolID
			}
		}
		if !sawTool {
			t.Fatal("OpenAI request translator DROPPED the tool_result (history broken)")
		}
		if toolID == "" {
			t.Error("tool_result lost its tool_call_id")
		}
		if asstID != toolID {
			t.Errorf("ID mismatch: assistant=%q tool=%q", asstID, toolID)
		}
	})

	t.Run("Ollama_request_keeps_tool_result", func(t *testing.T) {
		hist := mkHistory(idOllama)
		// Replace the assistant content with the Ollama-produced one.
		asst := AnthropicMessage{Role: "assistant", Content: anthroOllama.Content}
		asstBytes, _ := json.Marshal(asst)
		hist[1] = rmsg(t, string(asstBytes))
		r, err := translateRequest(&AnthropicRequest{Model: "test-model", Messages: hist})
		if err != nil {
			t.Fatal(err)
		}
		var asstID, toolID string
		sawTool := false
		for _, m := range r.Messages {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				asstID = m.ToolCalls[0].ID
			}
			if m.Role == "tool" {
				sawTool = true
				toolID = m.ToolCallID
			}
		}
		if !sawTool {
			t.Fatal("Ollama request translator DROPPED the tool_result (history broken)")
		}
		if asstID != toolID {
			t.Errorf("ID mismatch: assistant=%q tool=%q", asstID, toolID)
		}
	})

	// --- Regression: a client that sends a tool_result with an empty id -----
	// (malformed, but losing the content silently is worse than forwarding it).
	t.Run("OpenAI_request_keeps_empty_id_tool_result", func(t *testing.T) {
		hist := []AnthropicMessage{
			rmsg(t, `{"role":"user","content":"q"}`),
			rmsg(t, `{"role":"assistant","content":[{"type":"tool_use","id":"","name":"search","input":{"q":"x"}}]}`),
			rmsg(t, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":"r"}]}`),
		}
		r := translateToOpenAI(&AnthropicRequest{Model: "test-model", Messages: hist})
		sawTool := false
		for _, m := range r.Messages {
			if m.Role == "tool" {
				sawTool = true
			}
		}
		if !sawTool {
			t.Error("OpenAI request translator dropped a tool_result with empty tool_use_id")
		}
	})
}