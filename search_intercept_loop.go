package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// webSearchFunctionSchema is the function-tool schema Prism injects in place of
// Anthropic's typed web_search server tool, so a non-Anthropic upstream model
// can request a search by calling web_search({query}).
var webSearchFunctionSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"query": map[string]interface{}{
			"type":        "string",
			"description": "The web search query.",
		},
	},
	"required": []string{"query"},
}

// detectServerWebSearchTool scans req.Tools for an Anthropic server-side
// web_search tool (Type starts with "web_search"). Returns the domain filters
// and max_uses from the tool definition plus whether one was present.
func detectServerWebSearchTool(req *AnthropicRequest) (allowed, blocked []string, maxUses int, found bool) {
	for _, t := range req.Tools {
		if strings.HasPrefix(t.Type, "web_search") {
			return t.AllowedDomains, t.BlockedDomains, t.MaxUses, true
		}
	}
	return nil, nil, 0, false
}

// hasServerWebSearchTool reports whether the request carries a typed web_search
// server tool (the Pattern A trigger).
func hasServerWebSearchTool(req *AnthropicRequest) bool {
	_, _, _, found := detectServerWebSearchTool(req)
	return found
}

// emittedBlock is one content block collected during the search loop, emitted
// to the client after the loop resolves. Decoupling collection from emission
// keeps the streaming/non-streaming outputs consistent.
type emittedBlock struct {
	kind      string // "thinking", "text", "server_tool_use", "web_search_tool_result"
	text      string
	toolID    string
	query     string
	results   []SearchResult
	searchErr string
}

// handleServerWebSearchLoop implements Pattern A for a normal /v1/messages
// request that carries a typed web_search server tool (ZCode after the
// kind:"anthropic" switch). It rewrites the typed tool to a function tool,
// forwards to upstream non-streaming, intercepts the model's web_search
// tool_use, runs the search locally, appends the results to the conversation,
// and re-requests until the model produces a final answer (or MaxPerTurn is
// hit). The composed server_tool_use + web_search_tool_result + final text
// blocks are streamed to the client exactly as the real Anthropic API would.
//
// Returns true if it handled the request (typed web_search tool present).
func (pr *ProviderRouter) handleServerWebSearchLoop(w http.ResponseWriter, r *http.Request, req *AnthropicRequest, rp *ResolvedProvider) bool {
	allowed, blocked, toolMaxUses, found := detectServerWebSearchTool(req)
	if !found {
		return false
	}

	client := detectClient(r)
	globalStats.StartRequest(req.Model, rp.ProviderID, client)
	defer globalStats.EndRequest()
	reqStart := time.Now()

	dbg := newTranslationDebugCapture("messages", req.Stream, req.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", req)

	// Build a modified request: replace typed web_search tools with a function
	// tool that has a real schema, so the upstream model can call it.
	modReq := *req
	modReq.Tools = make([]AnthropicTool, 0, len(req.Tools))
	replaced := false
	for _, t := range req.Tools {
		if strings.HasPrefix(t.Type, "web_search") {
			modReq.Tools = append(modReq.Tools, AnthropicTool{
				Name:        "web_search",
				Description: "Search the web for current information.",
				InputSchema: webSearchFunctionSchema,
			})
			replaced = true
			continue
		}
		modReq.Tools = append(modReq.Tools, t)
	}
	_ = replaced

	ollamaReq, err := translateRequest(&modReq)
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("Translation error: %v", err))
		return true
	}
	ollamaReq.Stream = false // non-streaming for the search loop

	maxSearches := globalSearchRunner.config().MaxPerTurn
	if maxSearches <= 0 {
		maxSearches = 5
	}
	if toolMaxUses > 0 && toolMaxUses < maxSearches {
		maxSearches = toolMaxUses
	}
	numResults := globalSearchRunner.config().DefaultNumResults
	if numResults <= 0 {
		numResults = 5
	}

	var blocks []emittedBlock
	totalOutputTokens := 0
	searchesPerformed := 0

	for searchesPerformed < maxSearches {
		resp, err := pr.upstreamChat(ollamaReq, rp)
		if err != nil {
			log.Printf("[search] pattern A upstream error: %v", err)
			writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Upstream request failed: %v", err))
			return true
		}
		totalOutputTokens += resp.EvalCount

		if resp.Message.Thinking != "" {
			blocks = append(blocks, emittedBlock{kind: "thinking", text: resp.Message.Thinking})
		}
		if resp.Message.Content != "" {
			blocks = append(blocks, emittedBlock{kind: "text", text: resp.Message.Content})
		}

		// Find a web_search tool call.
		var wsIdx int = -1
		for i := range resp.Message.ToolCalls {
			if resp.Message.ToolCalls[i].Function.Name == "web_search" {
				wsIdx = i
				break
			}
		}
		if wsIdx < 0 {
			// No search requested — this is the final answer.
			break
		}

		tc := &resp.Message.ToolCalls[wsIdx]
		toolID := tc.ID
		if toolID == "" {
			toolID = generateToolUseID("web_search")
			tc.ID = toolID
		}
		query := ""
		if tc.Function.Arguments != nil {
			if qs, ok := tc.Function.Arguments["query"].(string); ok {
				query = qs
			} else {
				query = fmt.Sprint(tc.Function.Arguments["query"])
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		outcome, sErr := runSearch(ctx, SearchQuery{
			Query:          query,
			AllowedDomains: allowed,
			BlockedDomains: blocked,
			NumResults:     numResults,
		})
		cancel()
		var results []SearchResult
		var searchErr string
		if sErr != nil {
			searchErr = sErr.Error()
			log.Printf("[search] pattern A search failed for %q: %v", query, sErr)
		} else {
			results = outcome.Results
		}

		blocks = append(blocks,
			emittedBlock{kind: "server_tool_use", toolID: toolID, query: query},
			emittedBlock{kind: "web_search_tool_result", toolID: toolID, query: query, results: results, searchErr: searchErr},
		)
		searchesPerformed++

		// Append the assistant turn (with the web_search tool call) and the
		// tool result to the conversation, then re-request.
		assistantMsg := OllamaMessage{
			Role:      "assistant",
			Content:   resp.Message.Content,
			ToolCalls: resp.Message.ToolCalls,
		}
		if resp.Message.Thinking != "" {
			assistantMsg.Thinking = resp.Message.Thinking
		}
		ollamaReq.Messages = append(ollamaReq.Messages, assistantMsg)
		ollamaReq.Messages = append(ollamaReq.Messages, OllamaMessage{
			Role:       "tool",
			Content:    buildSearchResultsPayload(query, results, searchErr),
			ToolCallID: toolID,
			ToolName:   "web_search",
		})
	}

	if searchesPerformed >= maxSearches {
		// Exhausted the search budget mid-loop; surface a note so the client
		// isn't left without a terminal text block.
		blocks = append(blocks, emittedBlock{kind: "text", text: fmt.Sprintf("Reached the maximum number of web searches (%d) for this request.", maxSearches)})
	}

	if req.Stream {
		pr.emitServerToolWebSearchStream(w, req, blocks, searchesPerformed, totalOutputTokens)
	} else {
		pr.emitServerToolWebSearchJSON(w, req, blocks, searchesPerformed, totalOutputTokens)
	}

	globalStats.RecordRequest(req.Model, rp.ProviderID, client, 0, totalOutputTokens, time.Since(reqStart))
	return true
}

// upstreamChat performs a single non-streaming Ollama chat request.
func (pr *ProviderRouter) upstreamChat(ollamaReq *OllamaChatRequest, rp *ResolvedProvider) (*OllamaChatResponse, error) {
	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, rp.apiChatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+rp.APIKey)
	resp, err := pr.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &out, nil
}

// buildSearchResultsPayload renders the search results as the tool_result
// content the upstream model consumes on re-request.
func buildSearchResultsPayload(query string, results []SearchResult, searchErr string) string {
	if searchErr != "" {
		return fmt.Sprintf("Web search for %q failed: %s", query, searchErr)
	}
	if len(results) == 0 {
		return fmt.Sprintf("Web search for %q returned no results.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Web search results for %q:\n\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   URL: %s\n", i+1, r.Title, r.URL)
		if r.PageAge != "" {
			fmt.Fprintf(&b, "   Published: %s\n", r.PageAge)
		}
		if r.Snippet != "" {
			snippet := r.Snippet
			if len(snippet) > 500 {
				snippet = snippet[:500] + "…"
			}
			fmt.Fprintf(&b, "   %s\n", snippet)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// emitServerToolWebSearchStream streams the composed blocks as an Anthropic SSE
// response.
func (pr *ProviderRouter) emitServerToolWebSearchStream(w http.ResponseWriter, req *AnthropicRequest, blocks []emittedBlock, searches, outputTokens int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, canFlush := w.(http.Flusher)
	msgID := fmt.Sprintf("msg_%s", req.Model)

	write := func(event string, data interface{}) {
		writeSSE(w, flusher, canFlush, event, data)
	}

	write("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": msgID, "type": "message", "role": "assistant", "model": req.Model,
			"content": []interface{}{}, "stop_reason": nil,
			"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		},
	})

	idx := 0
	hasContent := false
	for _, b := range blocks {
		switch b.kind {
		case "thinking":
			write("content_block_start", map[string]interface{}{"type": "content_block_start", "index": idx, "content_block": map[string]interface{}{"type": "thinking", "thinking": ""}})
			write("content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": idx, "delta": map[string]interface{}{"type": "thinking_delta", "thinking": b.text}})
			write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": idx})
			idx++
			hasContent = true
		case "text":
			write("content_block_start", map[string]interface{}{"type": "content_block_start", "index": idx, "content_block": map[string]interface{}{"type": "text", "text": ""}})
			for _, chunk := range chunkText(b.text, 120) {
				write("content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": idx, "delta": map[string]interface{}{"type": "text_delta", "text": chunk}})
			}
			write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": idx})
			idx++
			hasContent = true
		case "server_tool_use":
			write("content_block_start", map[string]interface{}{"type": "content_block_start", "index": idx, "content_block": map[string]interface{}{"type": "server_tool_use", "id": b.toolID, "name": "web_search", "input": map[string]interface{}{}}})
			inputJSON, _ := json.Marshal(map[string]string{"query": b.query})
			write("content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": idx, "delta": map[string]interface{}{"type": "input_json_delta", "partial_json": string(inputJSON)}})
			write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": idx})
			idx++
			hasContent = true
		case "web_search_tool_result":
			write("content_block_start", map[string]interface{}{"type": "content_block_start", "index": idx, "content_block": map[string]interface{}{"type": "web_search_tool_result", "tool_use_id": b.toolID, "content": buildWebSearchResultBlocks(b.results, b.searchErr)}})
			write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": idx})
			idx++
			hasContent = true
		}
	}

	if !hasContent {
		write("content_block_start", map[string]interface{}{"type": "content_block_start", "index": idx, "content_block": map[string]interface{}{"type": "text", "text": ""}})
		write("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": idx})
		idx++
	}

	usage := map[string]interface{}{"output_tokens": outputTokens}
	if searches > 0 {
		usage["server_tool_use"] = map[string]interface{}{"web_search_requests": searches}
	}
	write("message_delta", map[string]interface{}{"type": "message_delta", "delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": usage})
	write("message_stop", map[string]interface{}{"type": "message_stop"})
}

// emitServerToolWebSearchJSON writes a non-streaming Anthropic response with the
// composed blocks.
func (pr *ProviderRouter) emitServerToolWebSearchJSON(w http.ResponseWriter, req *AnthropicRequest, blocks []emittedBlock, searches, outputTokens int) {
	content := []interface{}{}
	for _, b := range blocks {
		switch b.kind {
		case "thinking":
			content = append(content, map[string]interface{}{"type": "thinking", "thinking": b.text})
		case "text":
			content = append(content, map[string]interface{}{"type": "text", "text": b.text})
		case "server_tool_use":
			content = append(content, map[string]interface{}{"type": "server_tool_use", "id": b.toolID, "name": "web_search", "input": map[string]string{"query": b.query}})
		case "web_search_tool_result":
			content = append(content, map[string]interface{}{"type": "web_search_tool_result", "tool_use_id": b.toolID, "content": buildWebSearchResultBlocks(b.results, b.searchErr)})
		}
	}
	if len(content) == 0 {
		content = append(content, map[string]interface{}{"type": "text", "text": ""})
	}
	usage := AnthropicUsage{OutputTokens: outputTokens}
	resp := AnthropicResponse{
		ID: fmt.Sprintf("msg_%s", req.Model), Type: "message", Role: "assistant",
		Model: req.Model, Content: content, StopReason: "end_turn", Usage: usage,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// chunkText splits s into pieces of at most max runes, breaking on whitespace
// where possible, for simulating streamed text deltas.
func chunkText(s string, max int) []string {
	if s == "" {
		return nil
	}
	if len(s) <= max {
		return []string{s}
	}
	var chunks []string
	for len(s) > max {
		cut := max
		if i := strings.LastIndexAny(s[:cut], " \n\t"); i > 0 {
			cut = i + 1
		}
		chunks = append(chunks, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		chunks = append(chunks, s)
	}
	return chunks
}