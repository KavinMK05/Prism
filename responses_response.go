package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// extractCustomToolInput parses the JSON arguments string from a chat-completions
// function call and extracts the raw input value for a custom_tool_call item.
// Custom tools (like apply_patch) use an `input` field (raw string), not
// `arguments` (JSON string). The model emits {"patch": "..."} as arguments;
// we extract the "patch" value to use as the `input`. If parsing fails or the
// field is missing, we fall back to the raw arguments string.
func extractCustomToolInput(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || arguments == "{}" {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		// Not valid JSON — use raw string as input
		return arguments
	}
	// Try common field names: "patch" (apply_patch), "input", "command" (local_shell)
	for _, key := range []string{"patch", "input", "command"} {
		if val, ok := parsed[key]; ok {
			if s, ok := val.(string); ok {
				return s
			}
			// Non-string value — marshal it back
			b, _ := json.Marshal(val)
			return string(b)
		}
	}
	// No known field — return the raw arguments
	return arguments
}

func translateChatCompletionsToResponsesAPI(resp *OpenAIChatResponse, req *ResponsesAPIRequest, toolTypes map[string]string) *ResponsesAPIResponse {
	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	output := []interface{}{}
	outputText := ""
	status := "completed"

	if len(resp.Choices) == 0 {
		return &ResponsesAPIResponse{
			ID:         respID,
			Object:     "response",
			CreatedAt:  createdAt,
			Model:      resp.Model,
			Status:     status,
			Output:     output,
			OutputText: outputText,
			Usage:      translateOpenAIUsageToResponses(resp.Usage),
		}
	}

	choice := resp.Choices[0]
	msg := choice.Message

	// Handle reasoning content (always first in output)
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		reasoningItem := ResponsesAPIReasoningItem{
			ID:      generateID("rs_"),
			Type:    "reasoning",
			Summary: []ResponsesAPIReasoningSummary{},
		}
		output = append(output, reasoningItem)
	}

	// Build message item content parts
	contentParts := []ResponsesAPIContentPart{}
	if msg.Content != nil {
		switch c := msg.Content.(type) {
		case string:
			if c != "" {
				contentParts = append(contentParts, ResponsesAPIContentPart{
					Type: "output_text",
					Text: c,
				})
				outputText += c
			}
		default:
			// Try to marshal as JSON string
			b, _ := json.Marshal(msg.Content)
			text := string(b)
			if text != "" && text != "null" {
				contentParts = append(contentParts, ResponsesAPIContentPart{
					Type: "output_text",
					Text: text,
				})
				outputText += text
			}
		}
	}

	// Always create a message output item (even if only tool calls)
	if len(contentParts) == 0 {
		contentParts = append(contentParts, ResponsesAPIContentPart{
			Type: "output_text",
			Text: "",
		})
	}

	msgItem := ResponsesAPIOutputMessage{
		ID:      generateID("msg_"),
		Type:    "message",
		Status:  "completed",
		Role:    "assistant",
		Content: contentParts,
	}
	output = append(output, msgItem)

	// Handle tool calls -> function_call/custom_tool_call/web_search_call items (always after message)
	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			outputType := resolveToolOutputType(tc.Function.Name, toolTypes)
			log.Printf("[RESP] tool call: name=%s resolved_type=%s toolTypes=%v", tc.Function.Name, outputType, toolTypes)
			itemID := tc.ID
			if itemID == "" {
				itemID = generateID("fc_")
			}
			// For custom_tool_call, use `input` (raw string) instead of `arguments` (JSON string).
			// The model emits {"patch": "..."} as arguments; we extract the raw patch text.
			if outputType == "custom_tool_call" {
				rawInput := extractCustomToolInput(tc.Function.Arguments)
				output = append(output, map[string]interface{}{
					"id":     itemID,
					"type":   "custom_tool_call",
					"call_id": itemID,
					"name":   tc.Function.Name,
					"input":  rawInput,
					"status": "completed",
				})
			} else {
				funcCall := ResponsesAPIFunctionCallItem{
					ID:        itemID,
					Type:      outputType,
					CallID:    itemID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
					Status:    "completed",
				}
				output = append(output, funcCall)
			}
		}
	}

	// Determine status from finish_reason
	if choice.FinishReason == "length" {
		status = "incomplete"
	}

	return &ResponsesAPIResponse{
		ID:         respID,
		Object:     "response",
		CreatedAt:  createdAt,
		Model:      resp.Model,
		Status:     status,
		Output:     output,
		OutputText: outputText,
		Usage:      translateOpenAIUsageToResponses(resp.Usage),
	}
}

func translateOllamaToResponsesAPI(ollama *OllamaChatResponse, req *ResponsesAPIRequest, toolTypes map[string]string) *ResponsesAPIResponse {
	// Chain: Ollama -> OpenAI Chat Completions -> Responses API
	chatReq := &OpenAIChatRequest{Model: ollama.Model}
	openAIResp := translateOllamaToOpenAI(ollama, chatReq)
	return translateChatCompletionsToResponsesAPI(&openAIResp, req, toolTypes)
}

func translateOpenAIUsageToResponses(usage OpenAIUsage) ResponsesAPIUsage {
	return ResponsesAPIUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
}

func generateID(prefix string) string {
	return fmt.Sprintf("%s%d_%d", prefix, time.Now().UnixNano(), fastRand())
}

// fastRand returns a pseudo-random int for uniqueness
func fastRand() int {
	return int(time.Now().UnixNano() % 1000000)
}
