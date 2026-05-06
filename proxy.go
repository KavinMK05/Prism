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

type Proxy struct {
	upstreamURL   string
	apiKey        string
	providerType  string
	modelRemap    *ModelRemapping
	client        *http.Client
}

func NewProxy(upstreamURL, apiKey, providerType string, modelRemap *ModelRemapping) *Proxy {
	return &Proxy{
		upstreamURL:  strings.TrimRight(upstreamURL, "/"),
		apiKey:       apiKey,
		providerType: providerType,
		modelRemap:   modelRemap,
		client:       &http.Client{Timeout: 10 * time.Minute},
	}
}

func (p *Proxy) HandleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, 405, "method_not_allowed", "Only POST is supported")
		return
	}

	if p.providerType == "openai" {
		p.HandleOpenAIMessages(w, r)
		return
	}

	var anthroReq AnthropicRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&anthroReq); err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("Failed to parse request: %v", err))
		return
	}

	anthroReq.Model = getEffectiveModel(p.modelRemap, anthroReq.Model)

	ollamaReq, err := translateRequest(&anthroReq)
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("Translation error: %v", err))
		return
	}

	if anthroReq.Stream {
		p.handleStreaming(w, r, ollamaReq, &anthroReq)
		return
	}

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal Ollama request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s", req.Method, p.upstreamURL+"/api/chat")

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

	var ollamaResp OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Failed to parse Ollama response: %v", err))
		return
	}

	anthroResp := translateResponse(&ollamaResp, &anthroReq)

	if len(anthroResp.Content) == 0 {
		anthroResp.Content = []interface{}{AnthropicTextBlock{Type: "text", Text: ""}}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(anthroResp)
}

// buildToolIDToNameMap builds a mapping from tool_use IDs to function names
// by scanning all assistant messages in the conversation for tool_use blocks.
func buildToolIDToNameMap(messages []AnthropicMessage) map[string]string {
	idToName := map[string]string{}
	for _, msg := range messages {
		blocks, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, b := range blocks {
			blockMap, ok := b.(map[string]interface{})
			if !ok {
				continue
			}
			if blockType, _ := blockMap["type"].(string); blockType == "tool_use" {
				id, _ := blockMap["id"].(string)
				name, _ := blockMap["name"].(string)
				if id != "" && name != "" {
					idToName[id] = name
				}
			}
		}
	}
	return idToName
}

func translateRequest(anthro *AnthropicRequest) (*OllamaChatRequest, error) {
	messages := []OllamaMessage{}

	if anthro.System != nil {
		sysContent := systemToString(anthro.System)
		if sysContent != "" {
			messages = append(messages, OllamaMessage{Role: "system", Content: sysContent})
		}
	}

	// Build a mapping from tool_use IDs to function names for resolving tool_result references
	toolIDToName := buildToolIDToNameMap(anthro.Messages)

	for _, msg := range anthro.Messages {
		ollamaMsgs := translateMessageWithToolLookup(msg, toolIDToName)
		messages = append(messages, ollamaMsgs...)
	}

	req := &OllamaChatRequest{
		Model:    anthro.Model,
		Messages: messages,
		Stream:   anthro.Stream,
	}

	if len(anthro.Tools) > 0 {
		req.Tools = translateTools(anthro.Tools)
	}

	options := &OllamaOptions{}
	hasOptions := false

	if anthro.MaxTokens > 0 {
		options.NumPredict = anthro.MaxTokens
		hasOptions = true
	}
	if anthro.Temperature != nil {
		options.Temperature = anthro.Temperature
		hasOptions = true
	}
	if anthro.TopP != nil {
		options.TopP = anthro.TopP
		hasOptions = true
	}
	if anthro.TopK != nil {
		options.TopK = anthro.TopK
		hasOptions = true
	}
	if len(anthro.StopSequences) > 0 {
		options.Stop = anthro.StopSequences
		hasOptions = true
	}

	if hasOptions {
		req.Options = options
	}

	if anthro.Thinking != nil {
		req.Think = true
	}

	return req, nil
}

func translateMessage(msg AnthropicMessage) []OllamaMessage {
	return translateMessageWithToolLookup(msg, nil)
}

func translateMessageWithToolLookup(msg AnthropicMessage, toolIDToName map[string]string) []OllamaMessage {
	switch content := msg.Content.(type) {
	case string:
		if msg.Role == "tool" {
			ollamaMsg := OllamaMessage{
				Role:    "tool",
				Content: content,
			}
			return []OllamaMessage{ollamaMsg}
		}
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: content,
		}}
	case []interface{}:
		return translateContentBlocksWithToolLookup(msg.Role, content, toolIDToName)
	default:
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: fmt.Sprintf("%v", content),
		}}
	}
}

func translateContentBlocks(role string, blocks []interface{}) []OllamaMessage {
	return translateContentBlocksWithToolLookup(role, blocks, nil)
}

func translateContentBlocksWithToolLookup(role string, blocks []interface{}, toolIDToName map[string]string) []OllamaMessage {
	textParts := []string{}
	images := []string{}
	var thinkingContent string
	toolUseBlocks := []AnthropicToolUseBlock{}
	type toolResult struct {
		toolUseID string
		content   string
		name     string
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
		case "image":
			if source, ok := blockMap["source"].(map[string]interface{}); ok {
				if data, ok := source["data"].(string); ok {
					images = append(images, data)
				}
			}
		case "tool_use":
			id, _ := blockMap["id"].(string)
			name, _ := blockMap["name"].(string)
			input, _ := blockMap["input"].(map[string]interface{})
			toolUseBlocks = append(toolUseBlocks, AnthropicToolUseBlock{
				Type: "tool_use", ID: id, Name: name, Input: input,
			})
		case "tool_result":
			toolUseID, _ := blockMap["tool_use_id"].(string)
			var contentStr string
			if c, ok := blockMap["content"].(string); ok {
				contentStr = c
			} else if c, ok := blockMap["content"].([]interface{}); ok {
				for _, item := range c {
					if m, ok := item.(map[string]interface{}); ok {
						if t, ok := m["text"].(string); ok {
							contentStr += t
						}
					}
				}
			}
			toolResults = append(toolResults, toolResult{
				toolUseID: toolUseID,
				content:   contentStr,
			})
		}
	}

	if len(toolUseBlocks) > 0 {
		content := strings.Join(textParts, "")
		toolCalls := make([]OllamaToolCall, len(toolUseBlocks))
		for i, tu := range toolUseBlocks {
			toolCalls[i] = OllamaToolCall{
				ID: tu.ID,
				Function: OllamaToolCallFunction{
					Name:      tu.Name,
					Arguments: tu.Input,
				},
			}
		}
		msg := OllamaMessage{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}
		if thinkingContent != "" {
			msg.Thinking = thinkingContent
		}
		return []OllamaMessage{msg}
	}

	if len(toolResults) > 0 {
		var messages []OllamaMessage
		for _, tr := range toolResults {
			ollamaMsg := OllamaMessage{
				Role:    "tool",
				Content: tr.content,
			}
			if tr.toolUseID != "" {
				ollamaMsg.ToolCallID = tr.toolUseID
			}
			// Look up tool name from the tool_use blocks in the same message first,
			// then fall back to the conversation-level tool ID -> name mapping
			for _, tu := range toolUseBlocks {
				if tu.ID == tr.toolUseID {
					ollamaMsg.ToolName = tu.Name
					break
				}
			}
			if ollamaMsg.ToolName == "" && toolIDToName != nil {
				if name, ok := toolIDToName[tr.toolUseID]; ok {
					ollamaMsg.ToolName = name
				}
			}
			messages = append(messages, ollamaMsg)
		}
		return messages
	}

	msg := OllamaMessage{Role: role}
	msg.Content = strings.Join(textParts, "")
	msg.Images = images
	if thinkingContent != "" {
		msg.Thinking = thinkingContent
	}

	return []OllamaMessage{msg}
}

func translateTools(tools []AnthropicTool) []OllamaTool {
	result := make([]OllamaTool, len(tools))
	for i, t := range tools {
		result[i] = OllamaTool{
			Type: "function",
			Function: OllamaToolFunc{
				Name:       t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return result
}

func translateResponse(ollama *OllamaChatResponse, anthroReq *AnthropicRequest) AnthropicResponse {
	content := []interface{}{}

	if ollama.Message.Thinking != "" {
		content = append(content, AnthropicThinkingBlock{
			Type:     "thinking",
			Thinking: ollama.Message.Thinking,
		})
	}

	if ollama.Message.Content != "" {
		content = append(content, AnthropicTextBlock{Type: "text", Text: ollama.Message.Content})
	}

	for i, tc := range ollama.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%s_%d", tc.Function.Name, i)
		}
		content = append(content, AnthropicToolUseBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}

	stopReason := "end_turn"
	switch ollama.DoneReason {
	case "length":
		stopReason = "max_tokens"
	case "tool_call", "tool_calls":
		stopReason = "tool_use"
	}
	if len(ollama.Message.ToolCalls) > 0 {
		stopReason = "tool_use"
	}

	return AnthropicResponse{
		ID:         fmt.Sprintf("msg_%s", ollama.Model),
		Type:       "message",
		Role:       "assistant",
		Model:      ollama.Model,
		Content:    content,
		StopReason: stopReason,
		Usage: AnthropicUsage{
			InputTokens:  ollama.PromptEvalCount,
			OutputTokens: ollama.EvalCount,
		},
	}
}

func systemToString(sys interface{}) string {
	switch v := sys.(type) {
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
		return fmt.Sprintf("%v", v)
	}
}

func writeAnthropicError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(AnthropicError{
		Type: "error",
		Error: AnthropicErrorDetail{
			Type:    errType,
			Message: message,
		},
	})
}