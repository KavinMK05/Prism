package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// translateChatCompletionsToCodexResponses converts a Chat Completions request
// to a Responses API request body suitable for chatgpt.com/backend-api/codex/responses.
func translateChatCompletionsToCodexResponses(req *OpenAIChatRequest) map[string]interface{} {
	// Strip provider prefix from model name
	modelName := req.Model
	if idx := strings.LastIndex(modelName, "/"); idx >= 0 {
		modelName = modelName[idx+1:]
	}

	body := map[string]interface{}{
		"model":  modelName,
		"stream": true, // Always stream upstream
		"store":  false,
	}

	// Extract system messages as instructions, rest as input
	var instructions []string
	var inputItems []interface{}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			text := contentToString(msg.Content)
			if text != "" {
				instructions = append(instructions, text)
			}
		case "user":
			inputItems = append(inputItems, map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": contentToString(msg.Content)},
				},
			})
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// Assistant message with tool calls → function_call items
				for _, tc := range msg.ToolCalls {
					inputItems = append(inputItems, map[string]interface{}{
						"type":      "function_call",
						"call_id":   tc.ID,
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					})
				}
				// If there's also content, add as assistant message
				text := contentToString(msg.Content)
				if text != "" {
					inputItems = append(inputItems, map[string]interface{}{
						"type": "message",
						"role": "assistant",
						"content": []map[string]interface{}{
							{"type": "output_text", "text": text},
						},
					})
				}
			} else {
				text := contentToString(msg.Content)
				if text != "" {
					inputItems = append(inputItems, map[string]interface{}{
						"type": "message",
						"role": "assistant",
						"content": []map[string]interface{}{
							{"type": "output_text", "text": text},
						},
					})
				}
			}
		case "tool":
			// Tool response → function_call_output
			output := contentToString(msg.Content)
			inputItems = append(inputItems, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": msg.ToolID,
				"output":  output,
			})
		}
	}

	// Set instructions (joined if multiple system messages)
	if len(instructions) > 0 {
		body["instructions"] = strings.Join(instructions, "\n\n")
	} else {
		body["instructions"] = ""
	}

	body["input"] = inputItems

	// Tools
	if len(req.Tools) > 0 {
		var respTools []interface{}
		for _, t := range req.Tools {
			if t.Type == "function" {
				respTools = append(respTools, map[string]interface{}{
					"type":     "function",
					"name":     t.Function.Name,
					"description": t.Function.Description,
					"parameters":  t.Function.Parameters,
				})
			}
		}
		body["tools"] = respTools
	}

	// Temperature
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	// TopP
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}

	// Reasoning
	if req.ReasoningEffort != "" {
		body["reasoning"] = map[string]interface{}{"effort": req.ReasoningEffort}
	}

	// NOTE: max_output_tokens is intentionally NOT forwarded

	return body
}

// contentToString extracts a string from an OpenAI message content field
// which can be either a string or an array of content parts.
func contentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

// handleCodexChatCompletions translates a Chat Completions request to the
// Responses API, sends it to chatgpt.com/backend-api/codex/responses, and
// translates the response back to Chat Completions format.
func (pr *ProviderRouter) handleCodexChatCompletions(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, rp *ResolvedProvider) {
	reqStart := time.Now()
	client := detectClient(r)

	// Translate to Responses API format
	bodyMap := translateChatCompletionsToCodexResponses(openAIReq)
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal request: "+err.Error())
		return
	}

	upstreamURL := rp.responsesURL()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	addCodexHeaders(req, rp)

	log.Printf("-> %s %s (codex chat completions translation)", req.Method, upstreamURL)

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Codex upstream request failed: %v", err)
		writeOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from codex upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Codex upstream error: %s", string(respBody))
		writeOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Codex upstream returned status %d: %s", resp.StatusCode, string(respBody)))
		return
	}

	if openAIReq.Stream {
		pr.translateCodexResponsesToChatCompletionsStream(w, r, resp, openAIReq, rp, client, reqStart)
	} else {
		pr.translateCodexResponsesToChatCompletions(w, r, resp, openAIReq, rp, client, reqStart)
	}
}

// translateCodexResponsesToChatCompletionsStream converts Responses API SSE
// events from the Codex backend to Chat Completions SSE chunks in real-time.
func (pr *ProviderRouter) translateCodexResponsesToChatCompletionsStream(w http.ResponseWriter, r *http.Request, resp *http.Response, openAIReq *OpenAIChatRequest, rp *ResolvedProvider, client string, reqStart time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	chatcmplID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()

	var inputTokens, outputTokens int
	var roleSent bool
	var toolCallIndex int
	var finishReason string

	defer func() {
		globalStats.RecordRequest(openAIReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))
	}()

	// Helper to write a Chat Completions SSE chunk
	writeChunk := func(delta OpenAIStreamDelta, finishReasonStr *string, usage *OpenAIStreamUsage) {
		chunk := OpenAIStreamChunk{
			ID:      chatcmplID,
			Object:  "chat.completion.chunk",
			Created: createdAt,
			Model:   openAIReq.Model,
			Choices: []OpenAIStreamChoice{
				{
					Index:        0,
					Delta:        delta,
					FinishReason: finishReasonStr,
				},
			},
			Usage: usage,
		}
		dataJSON, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", dataJSON)
		if canFlush {
			flusher.Flush()
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "response.created":
			// Send initial role chunk
			if !roleSent {
				role := "assistant"
				writeChunk(OpenAIStreamDelta{Role: role}, nil, nil)
				roleSent = true
			}

		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			if delta != "" {
				globalStats.AddTokens(1)
				writeChunk(OpenAIStreamDelta{Content: &delta}, nil, nil)
			}

		case "response.reasoning_summary_text.delta":
			delta, _ := event["delta"].(string)
			if delta != "" {
				writeChunk(OpenAIStreamDelta{ReasoningContent: &delta}, nil, nil)
			}

		case "response.output_item.added":
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType == "function_call" {
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				idx := toolCallIndex
				toolCallIndex++
				writeChunk(OpenAIStreamDelta{
					ToolCalls: []OpenAIToolCall{
						{
							Index:    &idx,
							ID:       callID,
							Type:     "function",
							Function: OpenAIToolCallFunc{Name: name, Arguments: ""},
						},
					},
				}, nil, nil)
			}

		case "response.function_call_arguments.delta":
			delta, _ := event["delta"].(string)
			if delta != "" {
				idx := toolCallIndex - 1
				if idx < 0 {
					idx = 0
				}
				writeChunk(OpenAIStreamDelta{
					ToolCalls: []OpenAIToolCall{
						{
							Index:    &idx,
							Function: OpenAIToolCallFunc{Arguments: delta},
						},
					},
				}, nil, nil)
			}

		case "response.completed":
			responseObj, _ := event["response"].(map[string]interface{})
			if responseObj != nil {
				// Extract usage
				if usage, ok := responseObj["usage"].(map[string]interface{}); ok {
					if it, ok := usage["input_tokens"].(float64); ok {
						inputTokens = int(it)
					}
					if ot, ok := usage["output_tokens"].(float64); ok {
						outputTokens = int(ot)
					}
				}
				// Determine finish reason
				status, _ := responseObj["status"].(string)
				if status == "incomplete" {
					finishReason = "length"
				} else {
					finishReason = "stop"
				}
			}
			// Send final chunk with finish reason
			fr := finishReason
			writeChunk(OpenAIStreamDelta{}, &fr, &OpenAIStreamUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				TotalTokens:      inputTokens + outputTokens,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Codex SSE read error: %v", err)
	}

	// If no finish reason was sent, send a default one
	if finishReason == "" {
		fr := "stop"
		writeChunk(OpenAIStreamDelta{}, &fr, nil)
	}

	// Send [DONE]
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

// translateCodexResponsesToChatCompletions collects SSE events from the Codex
// backend and builds a complete Chat Completions JSON response.
func (pr *ProviderRouter) translateCodexResponsesToChatCompletions(w http.ResponseWriter, r *http.Request, resp *http.Response, openAIReq *OpenAIChatRequest, rp *ResolvedProvider, client string, reqStart time.Time) {
	var contentText string
	var reasoningText string
	var toolCalls []OpenAIToolCall
	var inputTokens, outputTokens int
	var finishReason = "stop"
	var completedResponse map[string]interface{}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			contentText += delta

		case "response.reasoning_summary_text.delta":
			delta, _ := event["delta"].(string)
			reasoningText += delta

		case "response.output_item.added":
			item, _ := event["item"].(map[string]interface{})
			if item == nil {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType == "function_call" {
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				toolCalls = append(toolCalls, OpenAIToolCall{
					ID:       callID,
					Type:     "function",
					Function: OpenAIToolCallFunc{Name: name, Arguments: ""},
				})
			}

		case "response.function_call_arguments.delta":
			delta, _ := event["delta"].(string)
			if len(toolCalls) > 0 {
				toolCalls[len(toolCalls)-1].Function.Arguments += delta
			}

		case "response.completed":
			responseObj, _ := event["response"].(map[string]interface{})
			if responseObj != nil {
				completedResponse = responseObj
				if usage, ok := responseObj["usage"].(map[string]interface{}); ok {
					if it, ok := usage["input_tokens"].(float64); ok {
						inputTokens = int(it)
					}
					if ot, ok := usage["output_tokens"].(float64); ok {
						outputTokens = int(ot)
					}
				}
				status, _ := responseObj["status"].(string)
				if status == "incomplete" {
					finishReason = "length"
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERR] Codex SSE read error: %v", err)
	}

	// If we have a completed response with output items, extract tool calls from there
	if completedResponse != nil {
		if output, ok := completedResponse["output"].([]interface{}); ok {
			// Clear and rebuild tool calls from the completed response
			toolCalls = nil
			for _, item := range output {
				itemMap, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				itemType, _ := itemMap["type"].(string)
				if itemType == "function_call" {
					callID, _ := itemMap["call_id"].(string)
					name, _ := itemMap["name"].(string)
					args, _ := itemMap["arguments"].(string)
					toolCalls = append(toolCalls, OpenAIToolCall{
						ID:       callID,
						Type:     "function",
						Function: OpenAIToolCallFunc{Name: name, Arguments: args},
					})
				}
			}
		}
	}

	_ = reasoningText // available if needed for reasoning_content in response

	globalStats.RecordRequest(openAIReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))

	// Build Chat Completions response
	chatResp := OpenAIChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Model:   openAIReq.Model,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIChatMessage{
					Role:      "assistant",
					Content:   contentText,
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			TotalTokens:      inputTokens + outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(chatResp)
}
