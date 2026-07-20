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

func translateToOpenAI(anthroReq *AnthropicRequest) *OpenAIChatRequest {
	messages := []OpenAIChatMessage{}
	preserveReasoningContent := isReasoningVendorModel(anthroReq.Model)

	if anthroReq.System != nil {
		sysContent := stripLeadingAnthropicBillingHeader(systemToString(anthroReq.System))
		if strings.TrimSpace(sysContent) != "" {
			messages = append(messages, OpenAIChatMessage{
				Role:    "system",
				Content: sysContent,
			})
		}
	}

	for _, msg := range anthroReq.Messages {
		translated := translateMessageToOpenAIWithReasoning(msg, preserveReasoningContent)
		messages = append(messages, translated...)
	}
	// OpenAI Chat implementations commonly support only one system message at
	// the beginning. Claude Code can also emit system reminders inside messages;
	// hoist and merge every system message after translating the full history.
	// This mirrors CC Switch's Anthropic -> OpenAI normalization.
	messages = normalizeOpenAISystemMessages(messages)

	req := &OpenAIChatRequest{
		Model:       anthroReq.Model,
		Messages:    messages,
		Stream:      anthroReq.Stream,
		MaxTokens:   anthroReq.MaxTokens,
		Temperature: anthroReq.Temperature,
		TopP:        anthroReq.TopP,
	}

	if len(anthroReq.StopSequences) > 0 {
		req.Stop = anthroReq.StopSequences
	}

	if len(anthroReq.Tools) > 0 {
		req.Tools = translateToolsToOpenAI(anthroReq.Tools)
		// Only forward tool_choice when tools are present; an OpenAI tool_choice
		// without tools is invalid.
		if tc := translateToolChoiceToOpenAI(anthroReq.ToolChoice); tc != nil {
			req.ToolChoice = tc
		}
	}

	// Map Anthropic thinking config to OpenAI reasoning_effort. Unlike the
	// previous hardcoded "medium", this honors thinking.type (disabled turns
	// reasoning off) and buckets budget_tokens into a discrete effort.
	req.ReasoningEffort = anthropicThinkingToReasoningEffort(anthroReq.Thinking)

	return req
}

// translateToolChoiceToOpenAI maps an Anthropic tool_choice value to the
// OpenAI Chat Completions tool_choice shape. Returns nil when the value is
// absent or unrecognized so the caller can omit the field entirely.
func translateToolChoiceToOpenAI(tc interface{}) interface{} {
	if tc == nil {
		return nil
	}
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "none":
			return "none"
		}
		return nil
	case map[string]interface{}:
		t, _ := v["type"].(string)
		switch t {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "none":
			return "none"
		case "tool":
			name, _ := v["name"].(string)
			return map[string]interface{}{
				"type":     "function",
				"function": map[string]interface{}{"name": name},
			}
		}
		return nil
	}
	return nil
}

func translateMessageToOpenAI(msg AnthropicMessage) []OpenAIChatMessage {
	return translateMessageToOpenAIWithReasoning(msg, false)
}

func translateMessageToOpenAIWithReasoning(msg AnthropicMessage, preserveReasoningContent bool) []OpenAIChatMessage {
	// Anthropic allows system messages inside the messages array (e.g. Claude
	// Code's "The user sent a new message while you were working" reminders,
	// or the "Available agent types" notice). Hoisting these to the front
	// system prompt destroys recency: the latest user input ends up buried at
	// the start of the conversation instead of as the final user turn. Mirror
	// CLIProxyAPI and emit them as user messages in place so their position is
	// preserved. The top-level `system` field (handled in translateToOpenAI)
	// remains the only front-of-conversation system message.
	if msg.Role == "system" {
		text := strings.TrimSpace(systemToString(msg.Content))
		if text == "" {
			return nil
		}
		return []OpenAIChatMessage{{
			Role:    "user",
			Content: text,
		}}
	}

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
		return translateContentBlocksToOpenAIWithReasoning(msg.Role, content, preserveReasoningContent)
	default:
		return []OpenAIChatMessage{{
			Role:    msg.Role,
			Content: fmt.Sprintf("%v", content),
		}}
	}
}

func translateContentBlocksToOpenAI(role string, blocks []interface{}) []OpenAIChatMessage {
	return translateContentBlocksToOpenAIWithReasoning(role, blocks, false)
}

func translateContentBlocksToOpenAIWithReasoning(role string, blocks []interface{}, preserveReasoningContent bool) []OpenAIChatMessage {
	textParts := []string{}
	imageParts := []interface{}{}
	toolCalls := []OpenAIToolCall{}
	var reasoningParts []string
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
			// Most OpenAI-compatible APIs do not understand Anthropic thinking
			// history. DeepSeek/Kimi/MiMo-style endpoints are the exception: they
			// require non-empty reasoning_content on assistant tool-call history.
			// Never forward the Anthropic signature; it is not valid for them.
			if preserveReasoningContent && role == "assistant" {
				if thinking, ok := blockMap["thinking"].(string); ok && strings.TrimSpace(thinking) != "" {
					reasoningParts = append(reasoningParts, thinking)
				}
			}
		case "redacted_thinking":
			// Claude Code encrypts historical thinking into redacted_thinking.
			// DeepSeek/Kimi/MiMo require non-empty reasoning_content on assistant
			// tool-call turns, so mirror cc-switch and inject a minimal placeholder
			// when the real content is unavailable. This is skipped for generic
			// OpenAI-compatible providers (preserveReasoningContent == false).
			if preserveReasoningContent && role == "assistant" {
				reasoningParts = append(reasoningParts, "[redacted thinking]")
			}
		case "tool_use":
			id, _ := blockMap["id"].(string)
			name, _ := blockMap["name"].(string)
			input := map[string]interface{}{}
			if parsed, ok := blockMap["input"].(map[string]interface{}); ok && parsed != nil {
				input = parsed
			}
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
			isErr, _ := blockMap["is_error"].(bool)
			if c, ok := blockMap["content"].(string); ok {
				tr.content = c
			} else if c, ok := blockMap["content"].([]interface{}); ok {
				for _, item := range c {
					if m, ok := item.(map[string]interface{}); ok {
						if t, ok := m["text"].(string); ok {
							tr.content += t
						}
						// OpenAI tool messages only accept string content, so
						// image parts in Anthropic tool_result blocks have no
						// native representation in the OpenAI tool role and are
						// intentionally dropped here (text is preserved).
					}
				}
			}
			if isErr {
				if tr.content == "" {
					tr.content = "[tool error]"
				} else {
					tr.content = "[tool error] " + tr.content
				}
			}
			// Keep the tool_result even when tool_use_id is empty: dropping it
			// silently breaks conversation history (the model never sees the
			// result and re-invokes the tool). An empty id is malformed, but
			// losing the content is worse than forwarding it. The Ollama path
			// (translateContentBlocksWithToolLookup) already keeps these.
			toolResults = append(toolResults, tr)
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
		if preserveReasoningContent && role == "assistant" && len(toolCalls) > 0 {
			reasoning := "tool call"
			if len(reasoningParts) > 0 {
				reasoning = joinStrings(reasoningParts)
			}
			msg.ReasoningContent = &reasoning
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
				Content: openAITextContent(textParts),
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

	// Do not send an empty assistant turn after removing stale thinking. This
	// matches CLIProxyAPI and avoids giving the upstream a fake continuation.
	if role == "assistant" && len(textParts) == 0 && len(imageParts) == 0 {
		return nil
	}

	return []OpenAIChatMessage{{
		Role:    role,
		Content: openAITextContent(textParts),
	}}
}

func isReasoningVendorModel(model string) bool {
	model = strings.ToLower(model)
	for _, hint := range []string{"moonshot", "kimi", "deepseek", "mimo", "xiaomimimo"} {
		if strings.Contains(model, hint) {
			return true
		}
	}
	return false
}

func stripLeadingAnthropicBillingHeader(text string) string {
	const prefix = "x-anthropic-billing-header:"
	if !strings.HasPrefix(text, prefix) {
		return text
	}
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		return strings.TrimLeft(text[i+1:], "\r\n")
	}
	return ""
}

func normalizeOpenAISystemMessages(messages []OpenAIChatMessage) []OpenAIChatMessage {
	var systemParts []string
	nonSystem := make([]OpenAIChatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != "system" {
			nonSystem = append(nonSystem, msg)
			continue
		}
		text := strings.TrimSpace(systemToString(msg.Content))
		if text != "" {
			systemParts = append(systemParts, text)
		}
	}
	if len(systemParts) == 0 {
		return nonSystem
	}
	return append([]OpenAIChatMessage{{
		Role:    "system",
		Content: strings.Join(systemParts, "\n"),
	}}, nonSystem...)
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

// openAITextContent returns the OpenAI content payload for a set of Anthropic
// text blocks. A single text block becomes a plain string (the shape most
// providers expect); multiple blocks become an array of {type:text,text:...}
// parts so distinct user turns stay separable rather than being flattened into
// one newline-joined string (which makes the model treat several historical
// messages as a single current turn).
func openAITextContent(textParts []string) interface{} {
	if len(textParts) == 0 {
		return ""
	}
	if len(textParts) == 1 {
		return textParts[0]
	}
	parts := make([]interface{}, 0, len(textParts))
	for _, t := range textParts {
		parts = append(parts, map[string]interface{}{"type": "text", "text": t})
	}
	return parts
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

// openAIResponseTextBlocks normalizes the two content shapes accepted by
// Chat Completions. A number of OpenAI-compatible providers return an array
// of {type:"text", text:"..."} parts even though the usual response is a
// plain string; serializing that array into the Anthropic text field produces
// visibly broken replies.
func openAIResponseTextBlocks(content interface{}) []interface{} {
	var blocks []interface{}
	switch v := content.(type) {
	case string:
		if v != "" {
			blocks = append(blocks, AnthropicTextBlock{Type: "text", Text: v})
		}
	case []interface{}:
		for _, part := range v {
			if m, ok := part.(map[string]interface{}); ok {
				if typ, _ := m["type"].(string); typ == "text" || typ == "" {
					if text, _ := m["text"].(string); text != "" {
						blocks = append(blocks, AnthropicTextBlock{Type: "text", Text: text})
					}
				}
			}
		}
	}
	return blocks
}

func openAIArgumentsObject(raw string) map[string]interface{} {
	args := map[string]interface{}{}
	if raw == "" {
		return args
	}
	var decoded interface{}
	if json.Unmarshal([]byte(raw), &decoded) == nil {
		if object, ok := decoded.(map[string]interface{}); ok {
			return object
		}
	}
	// Anthropic tool_use.input is an object. Never emit null or a JSON string
	// here: both cause clients to reject the following tool_result turn.
	return args
}

func translateFromOpenAI(resp *OpenAIChatResponse, anthroReq *AnthropicRequest) AnthropicResponse {
	content := []interface{}{}

	if len(resp.Choices) == 0 {
		return AnthropicResponse{
			ID:      "msg_" + sanitizeMessageIDFragment(resp.Model),
			Type:    "message",
			Role:    "assistant",
			Model:   resp.Model,
			Content: []interface{}{AnthropicTextBlock{Type: "text", Text: ""}},
		}
	}

	choice := resp.Choices[0]

	reasoning := ""
	if choice.Message.ReasoningContent != nil {
		reasoning = *choice.Message.ReasoningContent
	}
	if reasoning == "" && choice.Message.Reasoning != nil {
		reasoning = *choice.Message.Reasoning
	}
	if reasoning != "" {
		content = append(content, AnthropicThinkingBlock{
			Type:     "thinking",
			Thinking: reasoning,
		})
	}

	content = append(content, openAIResponseTextBlocks(choice.Message.Content)...)

	for _, tc := range choice.Message.ToolCalls {
		// A few OpenAI-compatible backends occasionally return a phantom
		// parallel call with no function name. Do not expose an unusable empty
		// tool_use block to Claude Code or replay it on the next request.
		if tc.Function.Name == "" {
			continue
		}
		args := openAIArgumentsObject(tc.Function.Arguments)
		id := sanitizeToolUseID(tc.ID)
		if id == "" {
			// Mint a globally-unique ID when the upstream OpenAI-compatible
			// server omits one. Without a stable, unique ID the client
			// cannot associate the tool_result it sends back on the next
			// turn, so the result is dropped from history and the model
			// re-invokes the tool in a retry loop. Mirrors translateResponse
			// (Ollama) and openToolUseBlockWithID (streaming).
			id = generateToolUseID(tc.Function.Name)
		}
		content = append(content, AnthropicToolUseBlock{
			Type:  "tool_use",
			ID:    id,
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

	// OpenAI's prompt_tokens already includes cached tokens; Anthropic splits
	// these into input_tokens (non-cached) and cache_read_input_tokens.
	cacheRead := 0
	if resp.Usage.PromptTokensDetails != nil {
		cacheRead = resp.Usage.PromptTokensDetails.CachedTokens
	}
	if cacheRead > 0 && inputTokens >= cacheRead {
		inputTokens -= cacheRead
	}

	return AnthropicResponse{
		ID:         "msg_" + sanitizeMessageIDFragment(resp.Model),
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		Content:    content,
		StopReason: stopReason,
		Usage: AnthropicUsage{
			InputTokens:          inputTokens,
			OutputTokens:         outputTokens,
			CacheReadInputTokens: cacheRead,
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

	// Capture the complete Anthropic -> OpenAI-compatible translation hop.
	dbg := newTranslationDebugCapture("messages-openai", false, anthroReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", anthroReq)
	dbg.writeJSON("2_translated_request.json", openAIReq)

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
	if err := json.NewDecoder(dbg.teeBody(resp.Body)).Decode(&openAIResp); err != nil {
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
