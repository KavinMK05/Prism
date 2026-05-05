package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func translateResponsesAPIToChatCompletions(req *ResponsesAPIRequest) *OpenAIChatRequest {
	messages := []OpenAIChatMessage{}

	// Handle instructions -> system message
	if req.Instructions != nil {
		instructions := instructionsToString(req.Instructions)
		if instructions != "" {
			messages = append(messages, OpenAIChatMessage{
				Role:    "system",
				Content: instructions,
			})
		}
	}

	// Handle input
	if req.Input != nil {
		switch input := req.Input.(type) {
		case string:
			if input != "" {
				messages = append(messages, OpenAIChatMessage{
					Role:    "user",
					Content: input,
				})
			}
		case []interface{}:
			for _, item := range input {
				msgs := translateResponseInputItemToChatMessage(item)
				messages = append(messages, msgs...)
			}
		}
	}

	chatReq := &OpenAIChatRequest{
		Model:       req.Model,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	if req.MaxOutputTokens > 0 {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	// Handle tools
	if len(req.Tools) > 0 {
		chatReq.Tools = translateResponseToolsToChatCompletions(req.Tools)
	}

	// Handle reasoning
	if req.Reasoning != nil {
		if effort, ok := req.Reasoning.(string); ok {
			chatReq.ReasoningEffort = effort
		} else if m, ok := req.Reasoning.(map[string]interface{}); ok {
			if e, ok := m["effort"].(string); ok {
				chatReq.ReasoningEffort = e
			}
		}
	}

	// Handle text.format -> response_format
	if req.Text != nil {
		chatReq.ResponseFormat = translateTextFormatToResponseFormat(req.Text)
	}

	return chatReq
}

func translateResponseInputItemToChatMessage(item interface{}) []OpenAIChatMessage {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return nil
	}

	itemType, _ := itemMap["type"].(string)

	switch itemType {
	case "":
		// Fallback: try role-based item
		role, _ := itemMap["role"].(string)
		if role != "" {
			content := ""
			if c, ok := itemMap["content"].(string); ok {
				content = c
			} else if c, ok := itemMap["content"]; ok && c != nil {
				b, _ := json.Marshal(c)
				content = string(b)
			}
			return []OpenAIChatMessage{{Role: role, Content: content}}
		}
	case "message":
		role, _ := itemMap["role"].(string)
		content := ""
		if c, ok := itemMap["content"].(string); ok {
			content = c
		} else if c, ok := itemMap["content"]; ok && c != nil {
			// Handle content as array
			if contentArray, ok := c.([]interface{}); ok {
				// Check if it's an array of content parts
				if len(contentArray) > 0 {
					if _, hasType := contentArray[0].(map[string]interface{})["type"]; hasType {
						return []OpenAIChatMessage{{Role: role, Content: contentArray}}
					}
				}
				// Otherwise treat as simple array
				parts := []string{}
				for _, part := range contentArray {
					if s, ok := part.(string); ok {
						parts = append(parts, s)
					} else if pMap, ok := part.(map[string]interface{}); ok {
						if pType, ok := pMap["type"].(string); ok && (pType == "text" || pType == "input_text") {
							if t, ok := pMap["text"].(string); ok {
								parts = append(parts, t)
							}
						}
					}
				}
				content = strings.Join(parts, "")
			} else {
				b, _ := json.Marshal(c)
				content = string(b)
			}
		}
		return []OpenAIChatMessage{{Role: role, Content: content}}

	case "function_call":
		callID, _ := itemMap["call_id"].(string)
		name, _ := itemMap["name"].(string)
		arguments := ""
		if a, ok := itemMap["arguments"].(string); ok {
			arguments = a
		}
		return []OpenAIChatMessage{{
			Role: "assistant",
			ToolCalls: []OpenAIToolCall{{
				ID:   callID,
				Type: "function",
				Function: OpenAIToolCallFunc{
					Name:      name,
					Arguments: arguments,
				},
			}},
		}}

	case "function_call_output":
		callID, _ := itemMap["call_id"].(string)
		output := ""
		if o, ok := itemMap["output"].(string); ok {
			output = o
		} else if o, ok := itemMap["output"]; ok && o != nil {
			b, _ := json.Marshal(o)
			output = string(b)
		}
		return []OpenAIChatMessage{{
			Role:   "tool",
			ToolID: callID,
			Content: output,
		}}

	case "reasoning":
		// Reasoning items are sent back as context; we can't directly represent them
		// in Chat Completions messages, so we skip them for now.
		return nil

	default:
		// Try role-based fallback
		role, _ := itemMap["role"].(string)
		if role != "" {
			content := ""
			if c, ok := itemMap["content"].(string); ok {
				content = c
			}
			return []OpenAIChatMessage{{Role: role, Content: content}}
		}
	}

	return nil
}

func translateResponseToolsToChatCompletions(tools []interface{}) []OpenAITool {
	result := []OpenAITool{}
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		toolType, _ := toolMap["type"].(string)
		if toolType != "function" {
			continue
		}
		// Internally tagged: {type, name, description, parameters}
		name, _ := toolMap["name"].(string)
		description, _ := toolMap["description"].(string)
		parameters := toolMap["parameters"]

		result = append(result, OpenAITool{
			Type: "function",
			Function: OpenAIToolDef{
				Name:        name,
				Description: description,
				Parameters:  parameters,
			},
		})
	}
	return result
}

func instructionsToString(instructions interface{}) string {
	switch v := instructions.(type) {
	case string:
		return v
	case []interface{}:
		parts := []string{}
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if instructions != nil {
			return fmt.Sprintf("%v", instructions)
		}
	}
	return ""
}

func translateTextFormatToResponseFormat(text interface{}) interface{} {
	textMap, ok := text.(map[string]interface{})
	if !ok {
		return nil
	}
	if format, ok := textMap["format"]; ok {
		return format
	}
	return nil
}

func translateResponsesAPIToOllama(req *ResponsesAPIRequest) *OllamaChatRequest {
	messages := []OllamaMessage{}

	// Handle instructions -> system message
	if req.Instructions != nil {
		instructions := instructionsToString(req.Instructions)
		if instructions != "" {
			messages = append(messages, OllamaMessage{
				Role:    "system",
				Content: instructions,
			})
		}
	}

	// Handle input
	if req.Input != nil {
		switch input := req.Input.(type) {
		case string:
			if input != "" {
				messages = append(messages, OllamaMessage{
					Role:    "user",
					Content: input,
				})
			}
		case []interface{}:
			for _, item := range input {
				msgs := translateResponseInputItemToOllama(item)
				messages = append(messages, msgs...)
			}
		}
	}

	ollamaReq := &OllamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}

	// Handle tools (filter out built-in tools for Ollama)
	if len(req.Tools) > 0 {
		filtered := filterFunctionToolsOnly(req.Tools)
		if len(filtered) > 0 {
			ollamaReq.Tools = translateResponseToolsToOllama(filtered)
		}
	}

	// Handle options
	options := &OllamaOptions{}
	hasOptions := false

	if req.MaxOutputTokens > 0 {
		options.NumPredict = req.MaxOutputTokens
		hasOptions = true
	}
	if req.Temperature != nil {
		options.Temperature = req.Temperature
		hasOptions = true
	}
	if req.TopP != nil {
		options.TopP = req.TopP
		hasOptions = true
	}

	if hasOptions {
		ollamaReq.Options = options
	}

	// Handle reasoning -> think
	if req.Reasoning != nil {
		ollamaReq.Think = true
	}

	// Handle text.format
	if req.Text != nil {
		ollamaReq.Format = translateTextFormatToResponseFormat(req.Text)
	}

	return ollamaReq
}

func translateResponseInputItemToOllama(item interface{}) []OllamaMessage {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return nil
	}

	itemType, _ := itemMap["type"].(string)

	switch itemType {
	case "", "message":
		role, _ := itemMap["role"].(string)
		if role == "" {
			role = "user"
		}
		content := ""
		if c, ok := itemMap["content"].(string); ok {
			content = c
		} else if c, ok := itemMap["content"]; ok && c != nil {
			if contentArray, ok := c.([]interface{}); ok {
				parts := []string{}
				for _, part := range contentArray {
					if s, ok := part.(string); ok {
						parts = append(parts, s)
					} else if pMap, ok := part.(map[string]interface{}); ok {
						if pType, ok := pMap["type"].(string); ok && (pType == "text" || pType == "input_text") {
							if t, ok := pMap["text"].(string); ok {
								parts = append(parts, t)
							}
						}
					}
				}
				content = strings.Join(parts, "")
			} else {
				b, _ := json.Marshal(c)
				content = string(b)
			}
		}
		return []OllamaMessage{{Role: role, Content: content}}

	case "function_call":
		name, _ := itemMap["name"].(string)
		arguments := ""
		if a, ok := itemMap["arguments"].(string); ok {
			arguments = a
		}
		var args map[string]interface{}
		if arguments != "" {
			json.Unmarshal([]byte(arguments), &args)
		}
		if args == nil {
			args = map[string]interface{}{}
		}
		return []OllamaMessage{{
			Role: "assistant",
			ToolCalls: []OllamaToolCall{{
				Function: OllamaToolCallFunction{
					Name:      name,
					Arguments: args,
				},
			}},
		}}

	case "function_call_output":
		callID, _ := itemMap["call_id"].(string)
		output := ""
		if o, ok := itemMap["output"].(string); ok {
			output = o
		} else if o, ok := itemMap["output"]; ok && o != nil {
			b, _ := json.Marshal(o)
			output = string(b)
		}
		ollamaMsg := OllamaMessage{
			Role:    "tool",
			Content: output,
		}
		if callID != "" {
			ollamaMsg.ToolCallID = callID
		}
		return []OllamaMessage{ollamaMsg}

	case "reasoning":
		// Skip reasoning items for Ollama
		return nil

	default:
		role, _ := itemMap["role"].(string)
		if role != "" {
			content := ""
			if c, ok := itemMap["content"].(string); ok {
				content = c
			}
			return []OllamaMessage{{Role: role, Content: content}}
		}
	}

	return nil
}

func translateResponseToolsToOllama(tools []interface{}) []OllamaTool {
	result := []OllamaTool{}
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		toolType, _ := toolMap["type"].(string)
		if toolType != "function" {
			continue
		}
		name, _ := toolMap["name"].(string)
		description, _ := toolMap["description"].(string)
		parameters := toolMap["parameters"]

		result = append(result, OllamaTool{
			Type: "function",
			Function: OllamaToolFunc{
				Name:        name,
				Description: description,
				Parameters:  parameters,
			},
		})
	}
	return result
}

func filterFunctionToolsOnly(tools []interface{}) []interface{} {
	result := []interface{}{}
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		toolType, _ := toolMap["type"].(string)
		if toolType == "function" {
			result = append(result, tool)
		}
	}
	return result
}
