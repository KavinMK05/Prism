package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// generateToolUseID returns a globally-unique Anthropic-style tool_use ID.
// Uniqueness across turns is critical: if IDs repeat within the conversation
// history a client sends back, tool_result blocks can't be unambiguously
// associated with their tool_use blocks, which causes upstream models to
// miss tool results and re-invoke the same tool in a retry loop.
func generateToolUseID(name string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a time-based value if randomness is unavailable.
		return fmt.Sprintf("toolu_%s_%d", name, time.Now().UnixNano())
	}
	return "toolu_" + hex.EncodeToString(b)
}

// anthropicThinkingEnabled reports whether an Anthropic thinking config
// requests reasoning. It honors thinking.type (disabled/enabled/adaptive);
// a non-nil thinking with no type but a positive budget_tokens is treated as
// enabled. Used for the Ollama path, which only has a boolean think toggle.
func anthropicThinkingEnabled(thinking *AnthropicThinking) bool {
	if thinking == nil {
		return false
	}
	t := thinking.Type
	if t == "" && thinking.BudgetTokens > 0 {
		t = "enabled"
	}
	switch t {
	case "disabled", "":
		return false
	case "enabled", "adaptive":
		return true
	default:
		return false
	}
}

// anthropicThinkingToReasoningEffort maps an Anthropic thinking config to an
// OpenAI reasoning_effort value (low/medium/high). Returns "" when reasoning
// should be disabled (no thinking, or thinking.type == "disabled"). OpenAI's
// reasoning_effort is coarse, so the Anthropic budget_tokens is bucketed.
func anthropicThinkingToReasoningEffort(thinking *AnthropicThinking) string {
	if thinking == nil {
		return ""
	}
	t := thinking.Type
	if t == "" && thinking.BudgetTokens > 0 {
		t = "enabled"
	}
	switch t {
	case "disabled", "":
		return ""
	case "enabled", "adaptive":
		switch {
		case thinking.BudgetTokens <= 0:
			return "medium"
		case thinking.BudgetTokens <= 4000:
			return "low"
		case thinking.BudgetTokens <= 12000:
			return "medium"
		default:
			return "high"
		}
	default:
		return ""
	}
}

type ProviderRouter struct {
	cfg        *Config
	cfgMu      sync.RWMutex
	modelRemap atomic.Pointer[ModelRemapping]
	client     *http.Client
}

func NewProviderRouter(cfg *Config, modelRemap *ModelRemapping) *ProviderRouter {
	pr := &ProviderRouter{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Minute},
	}
	pr.modelRemap.Store(modelRemap)
	return pr
}

// ReloadModelRemapping reloads the model remapping from disk.
func (pr *ProviderRouter) ReloadModelRemapping() {
	pr.modelRemap.Store(loadModelRemapping())
}

// ReloadConfig reloads the config from disk so the proxy picks up OAuth account
// changes, provider changes, etc. without requiring a restart.
func (pr *ProviderRouter) ReloadConfig() {
	pr.cfgMu.Lock()
	pr.cfg = loadConfig()
	pr.cfgMu.Unlock()
}

// getConfig returns the current config in a thread-safe manner.
func (pr *ProviderRouter) getConfig() *Config {
	pr.cfgMu.RLock()
	defer pr.cfgMu.RUnlock()
	return pr.cfg
}

func (pr *ProviderRouter) getModelRemap() *ModelRemapping {
	return pr.modelRemap.Load()
}

// resolveProviderForModel resolves a requested model to a provider and returns the provider info
func (pr *ProviderRouter) resolveProviderForModel(requestedModel string) (*ResolvedProvider, string, error) {
	cfg := pr.getConfig()
	resolvedModel, providerID := resolveModelProvider(cfg, pr.getModelRemap(), requestedModel)
	providerInfo, err := cfg.getProviderByID(providerID)
	if err != nil {
		// Fallback: if the provider ID looks like a Codex OAuth account (codex_ prefix)
		// but doesn't match any configured account, use the first available Codex OAuth account.
		if strings.HasPrefix(providerID, "codex_") && len(cfg.OAuthAccounts) > 0 {
			for _, a := range cfg.OAuthAccounts {
				if a.Provider == "codex" {
					log.Printf("[OAuth] Provider %s not found, falling back to %s (%s)", providerID, a.ID, a.Email)
					providerID = a.ID
					providerInfo, err = cfg.getProviderByID(providerID)
					if err != nil {
						return nil, resolvedModel, err
					}
					break
				}
			}
		}
		if err != nil {
			return nil, resolvedModel, err
		}
	}

	// For OAuth accounts, get a fresh token
	if providerInfo.ProviderType == "codex" {
		for _, a := range cfg.OAuthAccounts {
			if a.ID == providerID {
				token, err := getValidAccessToken(a)
				if err != nil {
					log.Printf("[OAuth] Token refresh failed for %s: %v, using stored token", a.Email, err)
					token = a.AccessToken
				}
				providerInfo.APIKey = token
				// Re-extract chatgpt-account-id from the token as a fallback
				accountID := a.ChatGPTAccountID
				if accountID == "" && token != "" {
					accountID = parseChatGPTAccountID(token)
				}
				return &ResolvedProvider{
					BaseURL:          strings.TrimRight(providerInfo.BaseURL, "/"),
					APIKey:           providerInfo.APIKey,
					ProviderType:     providerInfo.ProviderType,
					ProviderID:       providerID,
					ChatGPTAccountID: accountID,
				}, resolvedModel, nil
			}
		}
	}

	// Normalize base URL: for openai/codex providers, the base URL typically
	// already includes the /v1 prefix (e.g. https://api.groq.com/openai/v1).
	// We append paths like /chat/completions directly, not /v1/chat/completions.
	baseURL := strings.TrimRight(providerInfo.BaseURL, "/")

	return &ResolvedProvider{
		BaseURL:      baseURL,
		APIKey:       providerInfo.APIKey,
		ProviderType: providerInfo.ProviderType,
		ProviderID:   providerID,
	}, resolvedModel, nil
}

// isModelReasoning returns true if the model is marked as a reasoning model in the remap
func (pr *ProviderRouter) isModelReasoning(model string) bool {
	mr := pr.getModelRemap()
	if mr == nil {
		return false
	}
	for _, m := range mr.KnownModels {
		if m.ID == model || strings.HasPrefix(model, m.ID+":") || strings.HasPrefix(model, m.ID+"[") {
			return m.Reasoning
		}
	}
	return false
}

// validateReasoningEffort returns a valid reasoning_effort value for the model.
// If the model is not a reasoning model, it returns "" (strip it).
// If the model is a reasoning model but the effort value is not in the allowed list,
// it defaults to "medium". If the model has no allowed efforts defined, the value
// is passed through as-is.
func (pr *ProviderRouter) validateReasoningEffort(model string, effort string) string {
	if effort == "" {
		return ""
	}
	mr := pr.getModelRemap()
	if mr == nil {
		return ""
	}
	for _, m := range mr.KnownModels {
		if m.ID == model || strings.HasPrefix(model, m.ID+":") || strings.HasPrefix(model, m.ID+"[") {
			if !m.Reasoning {
				return ""
			}
			// If no allowed efforts specified, validate against standard values
			if len(m.ReasoningEffort) == 0 {
				standardEfforts := []string{"low", "medium", "high"}
				for _, valid := range standardEfforts {
					if effort == valid {
						return effort
					}
				}
				// Invalid value for reasoning model, default to medium
				log.Printf("[WARN] Invalid reasoning_effort %q for model %q, defaulting to medium", effort, model)
				return "medium"
			}
			// Check if the effort is in the model's allowed list
			for _, valid := range m.ReasoningEffort {
				if effort == valid {
					return effort
				}
			}
			// Invalid value for reasoning model, default to first allowed effort
			log.Printf("[WARN] Invalid reasoning_effort %q for model %q, defaulting to %q", effort, model, m.ReasoningEffort[0])
			return m.ReasoningEffort[0]
		}
	}
	// Model not in known models — strip reasoning_effort to be safe
	return ""
}

func (pr *ProviderRouter) HandleMessages(w http.ResponseWriter, r *http.Request) {
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

	rp, resolvedModel, err := pr.resolveProviderForModel(anthroReq.Model)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", fmt.Sprintf("Failed to resolve provider: %v", err))
		return
	}
	anthroReq.Model = resolvedModel

	if rp.ProviderType == "openai" || rp.ProviderType == "codex" {
		pr.HandleOpenAIMessages(w, r, &anthroReq, rp)
		return
	}

	client := detectClient(r)
	globalStats.StartRequest(anthroReq.Model, rp.ProviderID, client)
	defer globalStats.EndRequest()
	reqStart := time.Now()

	ollamaReq, err := translateRequest(&anthroReq)
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("Translation error: %v", err))
		return
	}

	if anthroReq.Stream {
		pr.handleStreaming(w, r, ollamaReq, &anthroReq, rp)
		return
	}

	// Dump the original request, translated request, original Ollama response
	// and translated response to disk for debugging. wrapWriter tees the bytes
	// we send back to the client into the capture (#4).
	dbg := newTranslationDebugCapture("messages", false, anthroReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", anthroReq)
	dbg.writeJSON("2_translated_request.json", ollamaReq)

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal Ollama request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.apiChatURL(), bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s", req.Method, rp.apiChatURL())

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

	var ollamaResp OllamaChatResponse
	// teeBody mirrors resp.Body into the debug capture (#3 original response);
	// returns resp.Body unchanged when the capture is nil.
	if err := json.NewDecoder(dbg.teeBody(resp.Body)).Decode(&ollamaResp); err != nil {
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Failed to parse Ollama response: %v", err))
		return
	}

	anthroResp := translateResponse(&ollamaResp, &anthroReq)

	globalStats.RecordRequest(anthroReq.Model, rp.ProviderID, client, ollamaResp.PromptEvalCount, ollamaResp.EvalCount, time.Since(reqStart))

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

	if anthropicThinkingEnabled(anthro.Thinking) {
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
		name      string
		images    []string
		isError   bool
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
			// Do not replay prior-turn thinking to a non-Claude upstream.
			// Anthropic thinking blocks carry a `signature` that clients use to
			// round-trip them; prism is the terminating server (not the Anthropic
			// API) and never signs thinking, so any thinking the client replays has
			// an empty or foreign signature. Forwarding a concatenation of every
			// prior turn's reasoning biases the model — e.g. it re-reads its own
			// stale "I need to run go build" intent and re-proposes the same tool
			// call every turn (an infinite loop). Drop thinking blocks whose
			// signature is empty/missing so the upstream reasons fresh from the
			// tool calls + results. (Matches CLIProxyAPI's
			// shouldMapClaudeThinkingToGPTReasoning, which skips empty-signature
			// thinking when routing Claude history to a non-Claude provider.)
			sig, _ := blockMap["signature"].(string)
			if strings.TrimSpace(sig) != "" {
				if thinking, ok := blockMap["thinking"].(string); ok {
					thinkingContent += thinking
				}
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
			isErr, _ := blockMap["is_error"].(bool)
			var contentStr string
			var resultImages []string
			if c, ok := blockMap["content"].(string); ok {
				contentStr = c
			} else if c, ok := blockMap["content"].([]interface{}); ok {
				for _, item := range c {
					if m, ok := item.(map[string]interface{}); ok {
						switch m["type"] {
						case "text":
							if t, ok := m["text"].(string); ok {
								contentStr += t
							}
						case "image":
							// Forward image parts in tool results (e.g. screenshot
							// tool output). Ollama accepts images on tool messages.
							if src, ok := m["source"].(map[string]interface{}); ok {
								if data, ok := src["data"].(string); ok {
									resultImages = append(resultImages, data)
								}
							}
						}
					}
				}
			}
			if isErr {
				if contentStr == "" {
					contentStr = "[tool error]"
				} else {
					contentStr = "[tool error] " + contentStr
				}
			}
			toolResults = append(toolResults, toolResult{
				toolUseID: toolUseID,
				content:   contentStr,
				images:    resultImages,
				isError:   isErr,
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
			if len(tr.images) > 0 {
				ollamaMsg.Images = tr.images
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
		if len(textParts) > 0 {
			messages = append(messages, OllamaMessage{
				Role:    "user",
				Content: strings.Join(textParts, ""),
			})
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
				Name:        t.Name,
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

	for _, tc := range ollama.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = generateToolUseID(tc.Function.Name)
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
