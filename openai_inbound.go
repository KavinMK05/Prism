package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func writeOpenAIError(w http.ResponseWriter, statusCode int, errType string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(OpenAIErrorResponse{
		Error: OpenAIErrorDetail{
			Message: message,
			Type:    errType,
			Code:    statusCode,
		},
	})
}

func (pr *ProviderRouter) HandleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, 405, "invalid_request_error", "Only POST is supported")
		return
	}

	var openAIReq OpenAIChatRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&openAIReq); err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "Failed to parse request: "+err.Error())
		return
	}

	rp, resolvedModel, err := pr.resolveProviderForModel(openAIReq.Model)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to resolve provider: "+err.Error())
		return
	}
	openAIReq.Model = resolvedModel

	client := detectClient(r)
	globalStats.StartRequest(openAIReq.Model, rp.ProviderID, client)
	defer globalStats.EndRequest()

	if openAIReq.Stream {
		if rp.ProviderType == "openai" || rp.ProviderType == "codex" {
			pr.handleOpenAIInboundOpenAIStreaming(w, r, &openAIReq, rp)
		} else {
			pr.handleOpenAIInboundOllamaStreaming(w, r, &openAIReq, rp)
		}
		return
	}

	if rp.ProviderType == "openai" || rp.ProviderType == "codex" {
		pr.handleOpenAIInboundToOpenAI(w, r, &openAIReq, rp)
	} else {
		pr.handleOpenAIInboundToOllama(w, r, &openAIReq, rp)
	}
}

func (pr *ProviderRouter) handleOpenAIInboundToOllama(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, rp *ResolvedProvider) {
	reqStart := time.Now()

	ollamaReq := translateOpenAIToOllama(openAIReq)

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal Ollama request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.apiChatURL(), bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s", req.Method, rp.apiChatURL())

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode))
		return
	}

	var ollamaResp OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		writeOpenAIError(w, 502, "server_error", "Failed to parse Ollama response: "+err.Error())
		return
	}

	openAIResp := translateOllamaToOpenAI(&ollamaResp, openAIReq)

	globalStats.RecordRequest(openAIReq.Model, rp.ProviderID, detectClient(r), ollamaResp.PromptEvalCount, ollamaResp.EvalCount, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(openAIResp)
}

func (pr *ProviderRouter) handleOpenAIInboundToOpenAI(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest, rp *ResolvedProvider) {
	reqStart := time.Now()

	// Strip reasoning_effort for non-reasoning models on custom providers
	if openAIReq.ReasoningEffort != "" && !pr.isModelReasoning(openAIReq.Model) && rp.ProviderID != "ollama_cloud" && rp.ProviderID != "opencode_go" {
		openAIReq.ReasoningEffort = ""
	}

	body, err := json.Marshal(openAIReq)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s", req.Method, rp.chatCompletionsURL())

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		writeOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		writeOpenAIError(w, resp.StatusCode, "server_error", fmt.Sprintf("Upstream returned status %d", resp.StatusCode))
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeOpenAIError(w, 502, "server_error", "Failed to read upstream response")
		return
	}

	// Extract usage stats from the response before forwarding
	var inputTokens, outputTokens int
	var parsedResp OpenAIChatResponse
	if json.Unmarshal(respBody, &parsedResp) == nil {
		inputTokens = parsedResp.Usage.PromptTokens
		outputTokens = parsedResp.Usage.CompletionTokens
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)

	globalStats.RecordRequest(openAIReq.Model, rp.ProviderID, detectClient(r), inputTokens, outputTokens, time.Since(reqStart))
}

// buildOpenAIToolIDToNameMap builds a mapping from tool_call_id to function name
// by scanning all assistant messages for tool_calls.
func buildOpenAIToolIDToNameMap(messages []OpenAIChatMessage) map[string]string {
	idToName := map[string]string{}
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				idToName[tc.ID] = tc.Function.Name
			}
		}
	}
	return idToName
}

func translateOpenAIToOllama(req *OpenAIChatRequest) *OllamaChatRequest {
	messages := []OllamaMessage{}

	// Build a mapping from tool_call_id to function name for resolving tool result references
	toolIDToName := buildOpenAIToolIDToNameMap(req.Messages)

	for _, msg := range req.Messages {
		messages = append(messages, translateOpenAIMessageToOllamaWithLookup(msg, toolIDToName)...)
	}

	ollamaReq := &OllamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}

	if len(req.Tools) > 0 {
		ollamaReq.Tools = translateOpenAIToolsToOllama(req.Tools)
	}

	options := &OllamaOptions{}
	hasOptions := false

	if req.MaxTokens > 0 {
		options.NumPredict = req.MaxTokens
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

	if req.ReasoningEffort != "" && req.ReasoningEffort != "off" && req.ReasoningEffort != "none" {
		ollamaReq.Think = true
	}

	return ollamaReq
}

func translateOpenAIMessageToOllama(msg OpenAIChatMessage) []OllamaMessage {
	return translateOpenAIMessageToOllamaWithLookup(msg, nil)
}

func translateOpenAIMessageToOllamaWithLookup(msg OpenAIChatMessage, toolIDToName map[string]string) []OllamaMessage {
	if msg.Role == "tool" {
		content := ""
		if s, ok := msg.Content.(string); ok {
			content = s
		} else if msg.Content != nil {
			b, _ := json.Marshal(msg.Content)
			content = string(b)
		}
		ollamaMsg := OllamaMessage{
			Role:    "tool",
			Content: content,
		}
		if msg.ToolID != "" {
			ollamaMsg.ToolCallID = msg.ToolID
		}
		if msg.Name != "" {
			ollamaMsg.ToolName = msg.Name
		} else if toolIDToName != nil {
			if name, ok := toolIDToName[msg.ToolID]; ok {
				ollamaMsg.ToolName = name
			}
		}
		return []OllamaMessage{ollamaMsg}
	}

	if len(msg.ToolCalls) > 0 {
		toolCalls := make([]OllamaToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			var args map[string]interface{}
			if tc.Function.Arguments != "" {
				json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			if args == nil {
				args = map[string]interface{}{}
			}
			toolCalls[i] = OllamaToolCall{
				ID: tc.ID,
				Function: OllamaToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: args,
				},
			}
		}
		content := ""
		switch c := msg.Content.(type) {
		case string:
			content = c
		case []interface{}:
			for _, item := range c {
				if partMap, ok := item.(map[string]interface{}); ok {
					if partType, ok := partMap["type"].(string); ok && partType == "text" {
						if text, ok := partMap["text"].(string); ok {
							content += text
						}
					}
				}
			}
		}
		ollamaMsg := OllamaMessage{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}
		if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
			ollamaMsg.Thinking = *msg.ReasoningContent
		}
		return []OllamaMessage{ollamaMsg}
	}

	switch content := msg.Content.(type) {
	case string:
		ollamaMsg := OllamaMessage{
			Role:    msg.Role,
			Content: content,
		}
		if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
			ollamaMsg.Thinking = *msg.ReasoningContent
		}
		return []OllamaMessage{ollamaMsg}
	case []interface{}:
		textParts := []string{}
		images := []string{}
		for _, item := range content {
			partMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			partType, _ := partMap["type"].(string)
			switch partType {
			case "text":
				if text, ok := partMap["text"].(string); ok {
					textParts = append(textParts, text)
				}
			case "image_url":
				if imageURL, ok := partMap["image_url"].(map[string]interface{}); ok {
					if url, ok := imageURL["url"].(string); ok {
						if strings.HasPrefix(url, "data:") {
							parts := strings.SplitN(url, ",", 2)
							if len(parts) == 2 {
								images = append(images, parts[1])
							}
						}
					}
				}
			}
		}
		ollamaMsg := OllamaMessage{
			Role:    msg.Role,
			Content: strings.Join(textParts, ""),
			Images:  images,
		}
		if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
			ollamaMsg.Thinking = *msg.ReasoningContent
		}
		return []OllamaMessage{ollamaMsg}
	default:
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: fmt.Sprintf("%v", content),
		}}
	}
}

func translateOpenAIToolsToOllama(tools []OpenAITool) []OllamaTool {
	result := make([]OllamaTool, len(tools))
	for i, t := range tools {
		result[i] = OllamaTool{
			Type: "function",
			Function: OllamaToolFunc{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			},
		}
	}
	return result
}

func translateOllamaToOpenAI(ollama *OllamaChatResponse, req *OpenAIChatRequest) OpenAIChatResponse {
	msg := OpenAIChatMessage{
		Role: "assistant",
	}

	if ollama.Message.Thinking != "" {
		msg.ReasoningContent = &ollama.Message.Thinking
	}

	if len(ollama.Message.ToolCalls) > 0 {
		if ollama.Message.Content != "" {
			msg.Content = ollama.Message.Content
		} else {
			msg.Content = nil
		}
	} else if ollama.Message.Content != "" {
		msg.Content = ollama.Message.Content
	} else {
		msg.Content = ""
	}

	if len(ollama.Message.ToolCalls) > 0 {
		toolCalls := make([]OpenAIToolCall, len(ollama.Message.ToolCalls))
		for i, tc := range ollama.Message.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Function.Arguments)
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%s_%d", tc.Function.Name, i)
			}
			toolCalls[i] = OpenAIToolCall{
				ID:   id,
				Type: "function",
				Function: OpenAIToolCallFunc{
					Name:      tc.Function.Name,
					Arguments: string(argsJSON),
				},
			}
		}
		msg.ToolCalls = toolCalls
	}

	finishReason := "stop"
	switch ollama.DoneReason {
	case "length":
		finishReason = "length"
	case "tool_call":
		finishReason = "tool_calls"
	}
	if len(ollama.Message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	return OpenAIChatResponse{
		ID:     fmt.Sprintf("chatcmpl-%s", ollama.Model),
		Object: "chat.completion",
		Model:  ollama.Model,
		Choices: []OpenAIChoice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: finishReason,
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     ollama.PromptEvalCount,
			CompletionTokens: ollama.EvalCount,
			TotalTokens:      ollama.PromptEvalCount + ollama.EvalCount,
		},
	}
}

func (pr *ProviderRouter) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, 405, "invalid_request_error", "Only GET is supported")
		return
	}

	models := []interface{}{}
	seen := map[string]bool{}

	if pr.modelRemap != nil {
		for _, m := range pr.modelRemap.KnownModels {
			if !seen[m.ID] {
				seen[m.ID] = true
				providerName := pr.cfg.getProviderName(m.Provider)
				models = append(models, map[string]interface{}{
					"id":       m.ID,
					"object":   "model",
					"created":  0,
					"owned_by": providerName,
				})
			}
		}
		for _, target := range pr.modelRemap.Aliases {
			if !seen[target] {
				seen[target] = true
				// Find the provider for the target model
				providerName := ""
				for _, km := range pr.modelRemap.KnownModels {
					if km.ID == target {
						providerName = pr.cfg.getProviderName(km.Provider)
						break
					}
				}
				if providerName == "" {
					providerName = pr.cfg.getProviderName(pr.cfg.DefaultProvider)
				}
				models = append(models, map[string]interface{}{
					"id":       target,
					"object":   "model",
					"created":  0,
					"owned_by": providerName,
				})
			}
		}
		for alias := range pr.modelRemap.Aliases {
			if !seen[alias] {
				seen[alias] = true
				// Aliases show the target provider's name
				target := pr.modelRemap.Aliases[alias]
				providerName := ""
				for _, km := range pr.modelRemap.KnownModels {
					if km.ID == target {
						providerName = pr.cfg.getProviderName(km.Provider)
						break
					}
				}
				if providerName == "" {
					providerName = pr.cfg.getProviderName(pr.cfg.DefaultProvider)
				}
				models = append(models, map[string]interface{}{
					"id":       alias,
					"object":   "model",
					"created":  0,
					"owned_by": providerName,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   models,
	})
}

