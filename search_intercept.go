package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// searchInterceptionEnabled reports whether the search runner has at least one
// usable provider. The interception layers gate on this so that, when search is
// fully disabled, requests pass through to the upstream unchanged.
func searchInterceptionEnabled() bool {
	c := globalSearchRunner.config()
	if c == nil {
		return false
	}
	if !globalSearchRunner.enabled(c.Active) {
		return false
	}
	if m := searchCatalogMeta(c.Active); m != nil && m.NeedsKey {
		if has, _ := globalSearchRunner.hasKey(c.Active); !has {
			return false
		}
	}
	return true
}

// detectClaudeCodeWebSearch recognizes Claude Code's WebSearch secondary
// conversation: a dedicated /v1/messages request whose system prompt includes
// "You are an assistant for performing a web search tool use", carrying a single
// web_search tool and a single user message of the form
// "Perform a web search for the query: <Q>". It returns the extracted query.
//
// The real Anthropic API runs the hosted web_search_20250305 server tool for
// this conversation and returns server_tool_use + web_search_tool_result blocks.
// Prism's upstreams (Ollama / OpenAI-compatible) cannot run hosted search, so
// the model just emits a web_search tool_use that goes nowhere. We detect the
// signature and short-circuit with a synthetic response built from our own
// SearchRunner (Pattern B in docs/web-search-interception-deep-dive.md).
func detectClaudeCodeWebSearch(req *AnthropicRequest) (string, bool) {
	sys := systemToString(req.System)
	if !strings.Contains(sys, "You are an assistant for performing a web search tool use") {
		return "", false
	}
	hasWebSearchTool := false
	for _, t := range req.Tools {
		if t.Name == "web_search" {
			hasWebSearchTool = true
			break
		}
	}
	if !hasWebSearchTool {
		return "", false
	}
	if len(req.Messages) != 1 {
		return "", false
	}
	msg := req.Messages[0]
	if msg.Role != "user" {
		return "", false
	}
	text := firstUserMessageText(msg.Content)
	const prefix = "Perform a web search for the query:"
	if !strings.HasPrefix(text, prefix) {
		return "", false
	}
	q := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	q = strings.Trim(q, "\"'")
	return q, true
}

// firstUserMessageText extracts the plain text from an Anthropic message
// content field (string or array of text blocks).
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return []byte(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	return b
}

// firstUserMessageText extracts the plain text from an Anthropic message
// content field (string or array of text blocks).
func firstUserMessageText(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						return text
					}
				}
			}
		}
	}
	return ""
}

// handleClaudeCodeWebSearch short-circuits the secondary conversation: it runs
// the query through the SearchRunner and emits a synthetic Anthropic response
// containing server_tool_use + web_search_tool_result blocks (+ a final text
// summary), matching what the real hosted web_search tool would return.
func (pr *ProviderRouter) handleClaudeCodeWebSearch(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, query string, rp *ResolvedProvider) {
	client := detectClient(r)
	globalStats.StartRequest(req.Model, rp.ProviderID, client)
	defer globalStats.EndRequest()
	reqStart := time.Now()

	// Debug capture so the synthetic turn is logged like a normal one.
	dbg := newTranslationDebugCapture("messages", req.Stream, req.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", req)
	dbg.writeRaw("2_translated_request.json", []byte(fmt.Sprintf(`{"_intercept":"claude-code-websearch","query":%q}`, query)))

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	outcome, err := runSearch(ctx, SearchQuery{Query: query, NumResults: globalSearchRunner.config().DefaultNumResults})

	var results []SearchResult
	var searchErr string
	if err != nil {
		searchErr = err.Error()
		log.Printf("[search] claude-code websearch intercept failed: %v", err)
		results = nil
	} else {
		results = outcome.Results
	}

	tokenEstimate := len(query) / 4 + len(results)*40 + 64

	if req.Stream {
		pr.emitSyntheticWebSearchStream(w, req, query, results, searchErr, tokenEstimate)
	} else {
		pr.emitSyntheticWebSearchJSON(w, req, query, results, searchErr, tokenEstimate)
	}

	globalStats.RecordRequest(req.Model, rp.ProviderID, client, 0, tokenEstimate, time.Since(reqStart))
}

// emitSyntheticWebSearchStream writes an Anthropic SSE stream emulating the
// hosted web_search_20250305 server tool: server_tool_use, web_search_tool_result,
// and a final text block summarizing the results, then message_stop.
func (pr *ProviderRouter) emitSyntheticWebSearchStream(w http.ResponseWriter, req *AnthropicRequest, query string, results []SearchResult, searchErr string, outputTokens int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, canFlush := w.(http.Flusher)
	msgID := fmt.Sprintf("msg_%s", req.Model)
	toolUseID := "srvtoolu_" + hex.EncodeToString(randomBytes(12))

	write := func(event string, data interface{}) {
		writeSSE(w, flusher, canFlush, event, data)
	}

	write("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":          msgID,
			"type":        "message",
			"role":        "assistant",
			"model":       req.Model,
			"content":     []interface{}{},
			"stop_reason": nil,
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	// Block 0: server_tool_use
	write("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]interface{}{
			"type":  "server_tool_use",
			"id":    toolUseID,
			"name":  "web_search",
			"input": map[string]interface{}{},
		},
	})
	inputJSON, _ := json.Marshal(map[string]string{"query": query})
	write("content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": string(inputJSON),
		},
	})
	write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})

	// Block 1: web_search_tool_result
	resultBlocks := buildWebSearchResultBlocks(results, searchErr)
	write("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": 1,
		"content_block": map[string]interface{}{
			"type":        "web_search_tool_result",
			"tool_use_id": toolUseID,
			"content":     resultBlocks,
		},
	})
	write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 1})

	// Block 2: text summary (what Claude would say after searching)
	summary := buildWebSearchSummary(query, results, searchErr)
	write("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": 2,
		"content_block": map[string]interface{}{
			"type": "text",
			"text": "",
		},
	})
	write("content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": 2,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": summary,
		},
	})
	write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 2})

	write("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": outputTokens,
			"server_tool_use": map[string]interface{}{
				"web_search_requests": 1,
			},
		},
	})
	write("message_stop", map[string]interface{}{"type": "message_stop"})
}

// emitSyntheticWebSearchJSON writes a non-streaming Anthropic response with the
// same three content blocks.
func (pr *ProviderRouter) emitSyntheticWebSearchJSON(w http.ResponseWriter, req *AnthropicRequest, query string, results []SearchResult, searchErr string, outputTokens int) {
	toolUseID := "srvtoolu_" + hex.EncodeToString(randomBytes(12))
	content := []interface{}{
		map[string]interface{}{
			"type":  "server_tool_use",
			"id":    toolUseID,
			"name":  "web_search",
			"input": map[string]string{"query": query},
		},
		map[string]interface{}{
			"type":        "web_search_tool_result",
			"tool_use_id": toolUseID,
			"content":     buildWebSearchResultBlocks(results, searchErr),
		},
		map[string]interface{}{
			"type": "text",
			"text": buildWebSearchSummary(query, results, searchErr),
		},
	}
	resp := AnthropicResponse{
		ID:         fmt.Sprintf("msg_%s", req.Model),
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    content,
		StopReason: "end_turn",
		Usage: AnthropicUsage{
			InputTokens:  0,
			OutputTokens: outputTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// buildWebSearchResultBlocks turns normalized SearchResults into Anthropic
// web_search_result content blocks. On failure, returns a single error block
// (matching Anthropic's web_search_tool_result_error shape).
func buildWebSearchResultBlocks(results []SearchResult, searchErr string) interface{} {
	if searchErr != "" {
		return map[string]interface{}{
			"type":       "web_search_tool_result_error",
			"error_code": "unavailable",
		}
	}
	out := make([]map[string]interface{}, 0, len(results))
	for _, r := range results {
		block := map[string]interface{}{
			"type":  "web_search_result",
			"url":   r.URL,
			"title": r.Title,
		}
		if r.PageAge != "" {
			block["page_age"] = r.PageAge
		}
		if r.Snippet != "" {
			block["encrypted_content"] = r.Snippet
		}
		out = append(out, block)
	}
	return out
}

// buildWebSearchSummary produces the final text block Claude would write after
// searching — a compact, citation-friendly listing of the results.
func buildWebSearchSummary(query string, results []SearchResult, searchErr string) string {
	if searchErr != "" {
		return fmt.Sprintf("Web search for %q failed: %s", query, searchErr)
	}
	if len(results) == 0 {
		return fmt.Sprintf("Web search for %q returned no results.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Web search results for %q:\n\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			snippet := r.Snippet
			if len(snippet) > 300 {
				snippet = snippet[:300] + "…"
			}
			fmt.Fprintf(&b, "   %s\n", snippet)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}