package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
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

	// Map Anthropic thinking to OpenAI reasoning_effort
	if anthroReq.Thinking != nil {
		req.ReasoningEffort = "medium" // default effort when thinking is enabled
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
	imageParts := []interface{}{}
	toolCalls := []OpenAIToolCall{}
	var thinkingContent string
	type toolResult struct {
		id      string
		content string
	}
	var toolResults []toolResult

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
		case "thinking":
			if thinking, ok := blockMap["thinking"].(string); ok {
				thinkingContent += thinking
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
			tr := toolResult{}
			tr.id, _ = blockMap["tool_use_id"].(string)
			if c, ok := blockMap["content"].(string); ok {
				tr.content = c
			} else if c, ok := blockMap["content"].([]interface{}); ok {
				for _, item := range c {
					if m, ok := item.(map[string]interface{}); ok {
						if t, ok := m["text"].(string); ok {
							tr.content += t
						}
					}
				}
			}
			if tr.id != "" {
				toolResults = append(toolResults, tr)
			}
		case "image":
			if source, ok := blockMap["source"].(map[string]interface{}); ok {
				if data, ok := source["data"].(string); ok {
					mediaType, _ := source["media_type"].(string)
					if mediaType == "" {
						mediaType = "image/png"
					}
					imageParts = append(imageParts, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
						},
					})
				}
			}
		}
	}

	if len(toolCalls) > 0 {
		content := ""
		if len(textParts) > 0 {
			content = joinStrings(textParts)
		}
		msg := OpenAIChatMessage{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}
		if thinkingContent != "" {
			msg.ReasoningContent = &thinkingContent
		}
		return []OpenAIChatMessage{msg}
	}

	if len(toolResults) > 0 {
		var messages []OpenAIChatMessage
		for _, tr := range toolResults {
			messages = append(messages, OpenAIChatMessage{
				Role:    "tool",
				Content: tr.content,
				ToolID:  tr.id,
			})
		}
		if len(textParts) > 0 {
			messages = append(messages, OpenAIChatMessage{
				Role:    "user",
				Content: joinStrings(textParts),
			})
		}
		return messages
	}

	// If we have images, use structured content array
	if len(imageParts) > 0 {
		var contentParts []interface{}
		for _, t := range textParts {
			contentParts = append(contentParts, map[string]interface{}{
				"type": "text",
				"text": t,
			})
		}
		contentParts = append(contentParts, imageParts...)
		return []OpenAIChatMessage{{
			Role:    role,
			Content: contentParts,
		}}
	}

	if thinkingContent != "" {
		msg := OpenAIChatMessage{
			Role:    role,
			Content: joinStrings(textParts),
		}
		msg.ReasoningContent = &thinkingContent
		return []OpenAIChatMessage{msg}
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

	if choice.Message.ReasoningContent != nil && *choice.Message.ReasoningContent != "" {
		content = append(content, AnthropicThinkingBlock{
			Type:     "thinking",
			Thinking: *choice.Message.ReasoningContent,
		})
	}

	if choice.Message.Content != nil && choice.Message.Content != "" {
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

func (pr *ProviderRouter) HandleOpenAIMessages(w http.ResponseWriter, r *http.Request, anthroReq *AnthropicRequest, rp *ResolvedProvider) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, 405, "method_not_allowed", "Only POST is supported")
		return
	}

	// Model and provider are already resolved by the caller

	client := detectClient(r)
	globalStats.StartRequest(anthroReq.Model, rp.ProviderID, client)
	defer globalStats.EndRequest()

	openAIReq := translateToOpenAI(anthroReq)

	if anthroReq.Stream {
		pr.handleOpenAIStreaming(w, r, openAIReq, anthroReq, rp)
		return
	}

	pr.handleOpenAINonStreaming(w, r, openAIReq, anthroReq, rp)
}

func (pr *ProviderRouter) handleOpenAINonStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, anthroReq *AnthropicRequest, rp *ResolvedProvider) {
	reqStart := time.Now()

	// Validate reasoning_effort for the model
	openAIReq.ReasoningEffort = pr.validateReasoningEffort(openAIReq.Model, openAIReq.ReasoningEffort)

	body, err := json.Marshal(openAIReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal OpenAI request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s", req.Method, rp.chatCompletionsURL())

	resp, err := pr.client.Do(req)
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

	globalStats.RecordRequest(anthroReq.Model, rp.ProviderID, detectClient(r), openAIResp.Usage.PromptTokens, openAIResp.Usage.CompletionTokens, time.Since(reqStart))

	if len(anthroResp.Content) == 0 {
		anthroResp.Content = []interface{}{AnthropicTextBlock{Type: "text", Text: ""}}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(anthroResp)
}