package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

func translateToOpenAI(anthroReq *AnthropicRequest) *OpenAIChatRequest {
	messages := []OpenAIChatMessage{}

	if anthroReq.System != nil {
		sysContent := systemToString(anthroReq.System)
		messages = append(messages, OpenAIChatMessage{
			Role:    "system",
			Content: sysContent,
		})
	}

	for _, msg := range anthroReq.Messages {
		ollamaMsgs := translateMessageToOpenAI(msg)
		messages = append(messages, ollamaMsgs...)
	}

	req := &OpenAIChatRequest{
		Model:       anthroReq.Model,
		Messages:    messages,
		Stream:      anthroReq.Stream,
		MaxTokens:   anthroReq.MaxTokens,
		Temperature: anthroReq.Temperature,
		TopP:        anthroReq.TopP,
	}

	if len(anthroReq.Tools) > 0 {
		req.Tools = translateToolsToOpenAI(anthroReq.Tools)
	}

	return req
}

func translateMessageToOpenAI(msg AnthropicMessage) []OpenAIChatMessage {
	switch content := msg.Content.(type) {
	case string:
		if msg.Role == "tool" {
			return []OpenAIChatMessage{{
				Role:    "tool",
				Content: content,
			}}
		}
		return []OpenAIChatMessage{{
			Role:    msg.Role,
			Content: content,
		}}
	case []interface{}:
		return translateContentBlocksToOpenAI(msg.Role, content)
	default:
		return []OpenAIChatMessage{{
			Role:    msg.Role,
			Content: fmt.Sprintf("%v", content),
		}}
	}
}

func translateContentBlocksToOpenAI(role string, blocks []interface{}) []OpenAIChatMessage {
	textParts := []string{}
	toolCalls := []OpenAIToolCall{}
	var toolResultID string
	toolResultContent := ""

	for _, b := range blocks {
		blockMap, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := blockMap["type"].(string)

		switch blockType {
		case "text":
			if text, ok := blockMap["text"].(string); ok {
				textParts = append(textParts, text)
			}
		case "tool_use":
			id, _ := blockMap["id"].(string)
			name, _ := blockMap["name"].(string)
			input, _ := blockMap["input"].(map[string]interface{})
			argsJSON, _ := json.Marshal(input)
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   id,
				Type: "function",
				Function: OpenAIToolCallFunc{
					Name:      name,
					Arguments: string(argsJSON),
				},
			})
		case "tool_result":
			toolResultID, _ = blockMap["tool_use_id"].(string)
			if c, ok := blockMap["content"].(string); ok {
				toolResultContent = c
			} else if c, ok := blockMap["content"].([]interface{}); ok {
				for _, item := range c {
					if m, ok := item.(map[string]interface{}); ok {
						if t, ok := m["text"].(string); ok {
							toolResultContent += t
						}
					}
				}
			}
		case "image":
			if source, ok := blockMap["source"].(map[string]interface{}); ok {
				if data, ok := source["data"].(string); ok {
					mediaType, _ := source["media_type"].(string)
					if mediaType == "" {
						mediaType = "image/png"
					}
					return []OpenAIChatMessage{{
						Role: role,
						Content: []interface{}{
							map[string]interface{}{
								"type": "image_url",
								"image_url": map[string]interface{}{
									"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
								},
							},
						},
					}}
				}
			}
		}
	}

	if len(toolCalls) > 0 {
		content := ""
		if len(textParts) > 0 {
			content = joinStrings(textParts)
		}
		return []OpenAIChatMessage{{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}}
	}

	if toolResultID != "" {
		return []OpenAIChatMessage{{
			Role:    "tool",
			Content: toolResultContent,
			ToolID:  toolResultID,
		}}
	}

	return []OpenAIChatMessage{{
		Role:    role,
		Content: joinStrings(textParts),
	}}
}

func joinStrings(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}

func translateToolsToOpenAI(tools []AnthropicTool) []OpenAITool {
	result := make([]OpenAITool, len(tools))
	for i, t := range tools {
		result[i] = OpenAITool{
			Type: "function",
			Function: OpenAIToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return result
}

func translateFromOpenAI(resp *OpenAIChatResponse, anthroReq *AnthropicRequest) AnthropicResponse {
	content := []interface{}{}

	if len(resp.Choices) == 0 {
		return AnthropicResponse{
			ID:     fmt.Sprintf("msg_%s", resp.Model),
			Type:   "message",
			Role:   "assistant",
			Model:  resp.Model,
			Content: []interface{}{AnthropicTextBlock{Type: "text", Text: ""}},
		}
	}

	choice := resp.Choices[0]

	if choice.Message.ReasoningContent != "" {
		content = append(content, AnthropicThinkingBlock{
			Type:     "thinking",
			Thinking: choice.Message.ReasoningContent,
		})
	}

	if choice.Message.Content != "" {
		text, ok := choice.Message.Content.(string)
		if ok && text != "" {
			content = append(content, AnthropicTextBlock{Type: "text", Text: text})
		} else if !ok {
			b, _ := json.Marshal(choice.Message.Content)
			content = append(content, AnthropicTextBlock{Type: "text", Text: string(b)})
		}
	}

	for _, tc := range choice.Message.ToolCalls {
		var args map[string]interface{}
		json.Unmarshal([]byte(tc.Function.Arguments), &args)
		content = append(content, AnthropicToolUseBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: args,
		})
	}

	stopReason := "end_turn"
	switch choice.FinishReason {
	case "length":
		stopReason = "max_tokens"
	case "tool_calls", "function_call":
		stopReason = "tool_use"
	}

	inputTokens := 0
	outputTokens := 0
	if resp.Usage.PromptTokens > 0 {
		inputTokens = resp.Usage.PromptTokens
	}
	if resp.Usage.CompletionTokens > 0 {
		outputTokens = resp.Usage.CompletionTokens
	}

	return AnthropicResponse{
		ID:        fmt.Sprintf("msg_%s", resp.Model),
		Type:      "message",
		Role:      "assistant",
		Model:     resp.Model,
		Content:   content,
		StopReason: stopReason,
		Usage: AnthropicUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		},
	}
}

func (p *Proxy) HandleOpenAIMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, 405, "method_not_allowed", "Only POST is supported")
		return
	}

	var anthroReq AnthropicRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&anthroReq); err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("Failed to parse request: %v", err))
		return
	}

	anthroReq.Model = getEffectiveModel(p.modelRemap, anthroReq.Model)

	openAIReq := translateToOpenAI(&anthroReq)

	if anthroReq.Stream {
		p.handleOpenAIStreaming(w, r, openAIReq, &anthroReq)
		return
	}

	p.handleOpenAINonStreaming(w, r, openAIReq, &anthroReq)
}

func (p *Proxy) handleOpenAINonStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, anthroReq *AnthropicRequest) {
	body, err := json.Marshal(openAIReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal OpenAI request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s", req.Method, p.upstreamURL+"/v1/chat/completions")

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Upstream request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeAnthropicError(w, resp.StatusCode, "api_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode))
		return
	}

	var openAIResp OpenAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Failed to parse OpenAI response: %v", err))
		return
	}

	anthroResp := translateFromOpenAI(&openAIResp, anthroReq)

	if len(anthroResp.Content) == 0 {
		anthroResp.Content = []interface{}{AnthropicTextBlock{Type: "text", Text: ""}}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(anthroResp)
}