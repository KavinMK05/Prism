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

// responsesEchoFields returns request fields that the Responses API spec
// expects to be echoed back on the response object (response.completed and
// non-streaming responses). Returns nil when nothing needs echoing.
func responsesEchoFields(req *ResponsesAPIRequest) map[string]interface{} {
	if req == nil {
		return nil
	}
	m := map[string]interface{}{}
	if req.Instructions != nil {
		m["instructions"] = req.Instructions
	}
	if req.MaxOutputTokens > 0 {
		m["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.MaxToolCalls > 0 {
		m["max_tool_calls"] = req.MaxToolCalls
	}
	if req.Temperature != nil {
		m["temperature"] = req.Temperature
	}
	if req.TopP != nil {
		m["top_p"] = req.TopP
	}
	if req.Reasoning != nil {
		m["reasoning"] = req.Reasoning
	}
	if req.Text != nil {
		m["text"] = req.Text
	}
	if req.ToolChoice != nil {
		m["tool_choice"] = req.ToolChoice
	}
	if len(req.Tools) > 0 {
		m["tools"] = req.Tools
	}
	if req.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = req.ParallelToolCalls
	}
	if req.PreviousResponseID != "" {
		m["previous_response_id"] = req.PreviousResponseID
	}
	if req.Store != nil {
		m["store"] = req.Store
	}
	if req.ServiceTier != "" {
		m["service_tier"] = req.ServiceTier
	}
	if req.PromptCacheKey != "" {
		m["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.Truncation != nil {
		m["truncation"] = req.Truncation
	}
	if req.User != nil {
		m["user"] = req.User
	}
	if req.Metadata != nil {
		m["metadata"] = req.Metadata
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// mergeResponsesEchoFields copies echoed request fields into a response map
// (used for the streaming response.completed payload).
func mergeResponsesEchoFields(m map[string]interface{}, req *ResponsesAPIRequest) {
	for k, v := range responsesEchoFields(req) {
		m[k] = v
	}
}

func translateChatCompletionsToResponsesAPI(resp *OpenAIChatResponse, req *ResponsesAPIRequest, toolTypes map[string]string, toolNamespaces map[string]string) *ResponsesAPIResponse {
	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	output := []interface{}{}
	outputText := ""
	status := "completed"

	if len(resp.Choices) == 0 {
		return &ResponsesAPIResponse{
			ID:                 respID,
			Object:             "response",
			CreatedAt:          createdAt,
			Model:              resp.Model,
			Status:             status,
			Background:         false,
			Error:              nil,
			IncompleteDetails:  nil,
			Output:             output,
			OutputText:         outputText,
			Usage:              translateOpenAIUsageToResponses(resp.Usage),
			Instructions:       req.Instructions,
			MaxOutputTokens:    req.MaxOutputTokens,
			Temperature:        req.Temperature,
			TopP:               req.TopP,
			Reasoning:          req.Reasoning,
			Text:               req.Text,
			ToolChoice:         req.ToolChoice,
			Tools:              req.Tools,
			ParallelToolCalls:  req.ParallelToolCalls,
			PreviousResponseID: req.PreviousResponseID,
			Store:              req.Store,
			ServiceTier:        req.ServiceTier,
			PromptCacheKey:     req.PromptCacheKey,
			Truncation:         req.Truncation,
			User:               req.User,
			Metadata:           req.Metadata,
		}
	}

	choice := resp.Choices[0]
	msg := choice.Message

	// Handle reasoning content (always first in output). Populate the summary
	// with the actual reasoning text so it is not lost (the previous code
	// emitted an empty summary array).
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		reasoningItem := ResponsesAPIReasoningItem{
			ID:   generateID("rs_"),
			Type: "reasoning",
			Summary: []ResponsesAPIReasoningSummary{{
				Type: "summary_text",
				Text: *msg.ReasoningContent,
			}},
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

	// Only create a message output item when there is text content. When the
	// model returns only tool calls, the Responses API output should contain
	// just the function_call items (no empty assistant message).
	if len(contentParts) > 0 {
		msgItem := ResponsesAPIOutputMessage{
			ID:      generateID("msg_"),
			Type:    "message",
			Status:  "completed",
			Role:    "assistant",
			Content: contentParts,
		}
		output = append(output, msgItem)
	}

	// Handle tool calls -> function_call/custom_tool_call/web_search_call items (always after message)
	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			outputType := resolveToolOutputType(tc.Function.Name, toolTypes)
			log.Printf("[RESP] tool call: name=%s resolved_type=%s toolTypes=%v", tc.Function.Name, outputType, toolTypes)
			callID := tc.ID
			if callID == "" {
				callID = generateID("call_")
			}
			// The Responses API distinguishes the item `id` (fc_*) from the
			// `call_id` (call_*). Keep them distinct so clients can round-trip.
			itemID := "fc_" + callID
			if outputType == "custom_tool_call" {
				rawInput := extractCustomToolInput(tc.Function.Arguments)
				output = append(output, map[string]interface{}{
					"id":      itemID,
					"type":    "custom_tool_call",
					"call_id": callID,
					"name":    tc.Function.Name,
					"input":   rawInput,
					"status":  "completed",
				})
			} else {
				childName, namespace := splitToolNamespace(tc.Function.Name, toolNamespaces)
				funcCall := ResponsesAPIFunctionCallItem{
					ID:        itemID,
					Type:      outputType,
					CallID:    callID,
					Name:      childName,
					Arguments: tc.Function.Arguments,
					Status:    "completed",
					Namespace: namespace,
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
		ID:                 respID,
		Object:             "response",
		CreatedAt:          createdAt,
		Model:              resp.Model,
		Status:             status,
		Background:         false,
		Error:              nil,
		IncompleteDetails:  nil,
		Output:             output,
		OutputText:         outputText,
		Usage:              translateOpenAIUsageToResponses(resp.Usage),
		Instructions:       req.Instructions,
		MaxOutputTokens:    req.MaxOutputTokens,
		Temperature:        req.Temperature,
		TopP:               req.TopP,
		Reasoning:          req.Reasoning,
		Text:               req.Text,
		ToolChoice:         req.ToolChoice,
		Tools:              req.Tools,
		ParallelToolCalls:  req.ParallelToolCalls,
		PreviousResponseID: req.PreviousResponseID,
		Store:              req.Store,
		ServiceTier:        req.ServiceTier,
		PromptCacheKey:     req.PromptCacheKey,
		Truncation:         req.Truncation,
		User:               req.User,
		Metadata:           req.Metadata,
	}
}

func translateOllamaToResponsesAPI(ollama *OllamaChatResponse, req *ResponsesAPIRequest, toolTypes map[string]string, toolNamespaces map[string]string) *ResponsesAPIResponse {
	// Chain: Ollama -> OpenAI Chat Completions -> Responses API
	chatReq := &OpenAIChatRequest{Model: ollama.Model}
	openAIResp := translateOllamaToOpenAI(ollama, chatReq)
	return translateChatCompletionsToResponsesAPI(&openAIResp, req, toolTypes, toolNamespaces)
}

func translateOpenAIUsageToResponses(usage OpenAIUsage) ResponsesAPIUsage {
	inDetails := &ResponsesAPITokensDetails{}
	if usage.PromptTokensDetails != nil {
		inDetails.CachedTokens = usage.PromptTokensDetails.CachedTokens
	}
	return ResponsesAPIUsage{
		InputTokens:         usage.PromptTokens,
		InputTokensDetails:  inDetails,
		OutputTokens:        usage.CompletionTokens,
		OutputTokensDetails: &ResponsesAPITokensDetails{},
		TotalTokens:         usage.TotalTokens,
	}
}

// responsesUsageMap builds a complete Responses API usage object (including
// the input_tokens_details / output_tokens_details nested fields that Grok
// Build's strict Rust client requires) for inline use in streaming events.
func responsesUsageMap(inputTokens, outputTokens int) map[string]interface{} {
	return map[string]interface{}{
		"input_tokens":          inputTokens,
		"input_tokens_details":  map[string]interface{}{"cached_tokens": 0},
		"output_tokens":         outputTokens,
		"output_tokens_details": map[string]interface{}{"reasoning_tokens": 0},
		"total_tokens":          inputTokens + outputTokens,
	}
}

func generateID(prefix string) string {
	return fmt.Sprintf("%s%d_%d", prefix, time.Now().UnixNano(), fastRand())
}

// fastRand returns a pseudo-random int for uniqueness
func fastRand() int {
	return int(time.Now().UnixNano() % 1000000)
}
