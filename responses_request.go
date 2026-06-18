package main

import (
	"encoding/json"
	"fmt"
	"log"
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
			// Build a call_id -> name mapping from function_call items for tool name lookup
			callIDToName := buildResponsesCallIDToNameMap(input)
			for _, item := range input {
				msgs := translateResponseInputItemToChatMessage(item, callIDToName)
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
			// Normalize non-standard effort values
			switch effort {
			case "enabled", "on", "true":
				chatReq.ReasoningEffort = "medium"
			case "disabled", "off", "false", "none":
				// Don't set reasoning_effort at all
			default:
				chatReq.ReasoningEffort = effort
			}
		} else if m, ok := req.Reasoning.(map[string]interface{}); ok {
			if e, ok := m["effort"].(string); ok {
				switch e {
				case "enabled", "on", "true":
					chatReq.ReasoningEffort = "medium"
				case "disabled", "off", "false", "none":
					// Don't set reasoning_effort
				default:
					chatReq.ReasoningEffort = e
				}
			}
		}
	}

	// Handle text.format -> response_format
	if req.Text != nil {
		chatReq.ResponseFormat = translateTextFormatToResponseFormat(req.Text)
	}

	return chatReq
}

// buildResponsesCallIDToNameMap builds a mapping from call_id to function name
// by scanning function_call items in the input array.
func buildResponsesCallIDToNameMap(items []interface{}) map[string]string {
	idToName := map[string]string{}
	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType == "function_call" {
			callID, _ := itemMap["call_id"].(string)
			name, _ := itemMap["name"].(string)
			if callID != "" && name != "" {
				idToName[callID] = name
			}
		}
	}
	return idToName
}

func translateResponseInputItemToChatMessage(item interface{}, callIDToName map[string]string) []OpenAIChatMessage {
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
		// Handle content
		if c, ok := itemMap["content"]; ok && c != nil {
			if contentStr, ok := c.(string); ok {
				return []OpenAIChatMessage{{Role: role, Content: contentStr}}
			}
			if contentArray, ok := c.([]interface{}); ok {
				// Scan for image/file content parts to decide between string and structured content
				hasMedia := false
				for _, part := range contentArray {
					if pMap, ok := part.(map[string]interface{}); ok {
						pType, _ := pMap["type"].(string)
						if pType == "input_image" || pType == "input_file" || pType == "image_url" {
							hasMedia = true
							break
						}
					}
				}

				if hasMedia {
					// Build structured content array with text + image/file parts
					contentParts := []interface{}{}
					for _, part := range contentArray {
						if s, ok := part.(string); ok {
							contentParts = append(contentParts, map[string]interface{}{
								"type": "text",
								"text": s,
							})
							continue
						}
						pMap, ok := part.(map[string]interface{})
						if !ok {
							continue
						}
						pType, _ := pMap["type"].(string)
						switch pType {
						case "text", "input_text":
							if t, ok := pMap["text"].(string); ok {
								contentParts = append(contentParts, map[string]interface{}{
									"type": "text",
									"text": t,
								})
							}
						case "input_image":
							imageURL, _ := pMap["image_url"].(string)
							if imageURL != "" {
								imageURLObj := map[string]interface{}{
									"url": imageURL,
								}
								if detail, ok := pMap["detail"].(string); ok && detail != "" {
									imageURLObj["detail"] = detail
								}
								contentParts = append(contentParts, map[string]interface{}{
									"type":      "image_url",
									"image_url": imageURLObj,
								})
							}
						case "input_file":
							// input_file can have file_data (data URI) or file_url
							if fileData, ok := pMap["file_data"].(string); ok && fileData != "" {
								contentParts = append(contentParts, map[string]interface{}{
									"type": "image_url",
									"image_url": map[string]interface{}{
										"url": fileData,
									},
								})
							} else if fileURL, ok := pMap["file_url"].(string); ok && fileURL != "" {
								contentParts = append(contentParts, map[string]interface{}{
									"type": "image_url",
									"image_url": map[string]interface{}{
										"url": fileURL,
									},
								})
							}
						case "image_url":
							// Already in OpenAI Chat Completions format, pass through
							contentParts = append(contentParts, pMap)
						}
					}
					return []OpenAIChatMessage{{Role: role, Content: contentParts}}
				}

				// No media — flatten to string
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
				return []OpenAIChatMessage{{Role: role, Content: strings.Join(parts, "")}}
			}
			// Fallback for non-array content
			b, _ := json.Marshal(c)
			return []OpenAIChatMessage{{Role: role, Content: string(b)}}
		}
		return []OpenAIChatMessage{{Role: role, Content: ""}}

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
		msg := OpenAIChatMessage{
			Role:   "tool",
			ToolID: callID,
			Content: output,
		}
		// Look up the function name from the corresponding function_call item
		if callIDToName != nil {
			if name, ok := callIDToName[callID]; ok {
				msg.Name = name
			}
		}
		return []OpenAIChatMessage{msg}

	case "reasoning":
		// Reasoning items contain prior thinking content from the model.
		// We translate them to an assistant message with reasoning_content.
		var summaryText string
		if summary, ok := itemMap["summary"].([]interface{}); ok {
			for _, s := range summary {
				if m, ok := s.(map[string]interface{}); ok {
					if t, ok := m["text"].(string); ok {
						summaryText += t
					}
				}
			}
		}
		if summaryText != "" {
			return []OpenAIChatMessage{{
				Role:             "assistant",
				Content:          "",
				ReasoningContent: &summaryText,
			}}
		}
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
		if toolType == "function" {
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
		} else {
			// Pass through non-function tools (apply_patch, web_search, etc.)
			// so the upstream (Codex/OpenAI) can handle them
			result = append(result, OpenAITool{
				Type: toolType,
				Function: OpenAIToolDef{
					Name:        toolType,
					Description: toolType,
				},
			})
		}
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
			// Build a call_id -> name mapping from function_call items for tool name lookup
			callIDToName := buildResponsesCallIDToNameMap(input)
			for _, item := range input {
				msgs := translateResponseInputItemToOllama(item, callIDToName)
				messages = append(messages, msgs...)
			}
		}
	}

	ollamaReq := &OllamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}

	// Handle tools (Ollama only understands function tools)
	if len(req.Tools) > 0 {
		ollamaReq.Tools = translateResponseToolsToOllama(req.Tools)
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

func translateResponseInputItemToOllama(item interface{}, callIDToName map[string]string) []OllamaMessage {
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

		if c, ok := itemMap["content"]; ok && c != nil {
			if contentStr, ok := c.(string); ok {
				return []OllamaMessage{{Role: role, Content: contentStr}}
			}
			if contentArray, ok := c.([]interface{}); ok {
				textParts := []string{}
				images := []string{}
				for _, part := range contentArray {
					if s, ok := part.(string); ok {
						textParts = append(textParts, s)
						continue
					}
					pMap, ok := part.(map[string]interface{})
					if !ok {
						continue
					}
					pType, _ := pMap["type"].(string)
					switch pType {
					case "text", "input_text":
						if t, ok := pMap["text"].(string); ok {
							textParts = append(textParts, t)
						}
					case "input_image":
						if imageURL, ok := pMap["image_url"].(string); ok && imageURL != "" {
							if strings.HasPrefix(imageURL, "data:") {
								parts := strings.SplitN(imageURL, ",", 2)
								if len(parts) == 2 {
									images = append(images, parts[1])
								}
							} else {
								log.Printf("[WARN] input_image with HTTP URL not supported for Ollama provider, skipping: %s", imageURL)
							}
						}
					case "input_file":
						if fileData, ok := pMap["file_data"].(string); ok && fileData != "" {
							if strings.HasPrefix(fileData, "data:") {
								parts := strings.SplitN(fileData, ",", 2)
								if len(parts) == 2 {
									images = append(images, parts[1])
								}
							} else {
								images = append(images, fileData)
							}
						} else if fileURL, ok := pMap["file_url"].(string); ok && fileURL != "" {
							log.Printf("[WARN] input_file with file_url not supported for Ollama provider, skipping: %s", fileURL)
						}
					}
				}
				msg := OllamaMessage{
					Role:    role,
					Content: strings.Join(textParts, ""),
				}
				if len(images) > 0 {
					msg.Images = images
				}
				return []OllamaMessage{msg}
			}
			// Fallback for non-array content
			b, _ := json.Marshal(c)
			return []OllamaMessage{{Role: role, Content: string(b)}}
		}
		return []OllamaMessage{{Role: role, Content: ""}}

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
		// Look up the function name from the corresponding function_call item
		if callIDToName != nil {
			if name, ok := callIDToName[callID]; ok {
				ollamaMsg.ToolName = name
			}
		}
		return []OllamaMessage{ollamaMsg}

	case "reasoning":
		// Reasoning items contain prior thinking content.
		// Translate to an assistant message with thinking field for Ollama.
		var summaryText string
		if summary, ok := itemMap["summary"].([]interface{}); ok {
			for _, s := range summary {
				if m, ok := s.(map[string]interface{}); ok {
					if t, ok := m["text"].(string); ok {
						summaryText += t
					}
				}
			}
		}
		if summaryText != "" {
			return []OllamaMessage{{
				Role:     "assistant",
				Content:  "",
				Thinking: summaryText,
			}}
		}
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

// buildToolTypeMap extracts a mapping from tool name to tool type from the request's tools array.
// Used to preserve original tool types (apply_patch, web_search, etc.) so responses emit
// the correct output type (custom_tool_call, web_search_call, etc.).
func buildToolTypeMap(tools []interface{}) map[string]string {
	result := map[string]string{}
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		toolType, _ := toolMap["type"].(string)
		if toolType == "" {
			continue
		}
		// Extract name from function.name or name field
		name := toolType
		if fn, ok := toolMap["function"].(map[string]interface{}); ok {
			if fnName, ok := fn["name"].(string); ok && fnName != "" {
				name = fnName
			}
		} else if n, ok := toolMap["name"].(string); ok && n != "" {
			name = n
		}
		result[name] = toolType
	}
	return result
}

// resolveToolOutputType returns the correct Responses API output item type for a tool call
// based on the original tool type from the request.
func resolveToolOutputType(name string, toolTypes map[string]string) string {
	if toolType, ok := toolTypes[name]; ok {
		if toolType == "apply_patch" {
			return "custom_tool_call"
		}
		if strings.HasPrefix(toolType, "web_search") {
			return "web_search_call"
		}
	}
	return "function_call"
}
