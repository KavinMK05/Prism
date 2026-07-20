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

// isSearchToolType reports whether a tool "type" string names a built-in
// web-search tool that Prism should intercept. OpenAI/Codex use "web_search";
// xAI/Grok Build uses "x_search". Both are represented as web_search_call
// output items in the Responses API (per xAI docs).
func isSearchToolType(t string) bool {
	t = strings.TrimSpace(t)
	return strings.HasPrefix(t, "web_search") || strings.HasPrefix(t, "x_search")
}

// isSearchToolName reports whether a function-call name is a web-search tool
// that Prism intercepted (after the typed tool is rewritten to a function).
func isSearchToolName(name string) bool {
	return name == "web_search" || name == "x_search"
}

// extractSearchQuery pulls the query string from an intercepted search tool
// call's arguments. Prefers "query" (web_search / x_search schema), falls
// back to "input" (legacy xAI x_search default schema), then a stringified
// "query".
func extractSearchQuery(wsCall *normalizedToolCall) string {
	if wsCall.arguments != nil {
		if qs, ok := wsCall.arguments["query"].(string); ok && qs != "" {
			return qs
		}
		if qs, ok := wsCall.arguments["input"].(string); ok && qs != "" {
			return qs
		}
		if qs := fmt.Sprint(wsCall.arguments["query"]); qs != "" && qs != "<nil>" {
			return qs
		}
	}
	return ""
}

// responsesHasWebSearchTool reports whether the request's tool set includes a
// built-in web-search tool (the Pattern A trigger for the Responses path).
func responsesHasWebSearchTool(toolTypes map[string]string) bool {
	for _, t := range toolTypes {
		if isSearchToolType(t) {
			return true
		}
	}
	return false
}

// extractResponsesWebSearchParams scans the raw tools array for a web_search
// built-in tool and returns its domain filters (allowed_domains /
// excluded_domains) and max_uses. Grok Build uses excluded_domains (max 5).
func extractResponsesWebSearchParams(allTools []interface{}) (allowed, blocked []string, maxUses int) {
	for _, tool := range allTools {
		tm, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		t := strings.TrimSpace(getMapString(tm, "type"))
		if !isSearchToolType(t) {
			continue
		}
		if a, ok := tm["allowed_domains"].([]interface{}); ok {
			for _, x := range a {
				if s, ok := x.(string); ok {
					allowed = append(allowed, s)
				}
			}
		}
		if b, ok := tm["excluded_domains"].([]interface{}); ok {
			for _, x := range b {
				if s, ok := x.(string); ok {
					blocked = append(blocked, s)
				}
			}
		}
		if b, ok := tm["blocked_domains"].([]interface{}); ok {
			for _, x := range b {
				if s, ok := x.(string); ok {
					blocked = append(blocked, s)
				}
			}
		}
		if mu, ok := tm["max_uses"].(float64); ok && mu > 0 {
			maxUses = int(mu)
		}
		return
	}
	return nil, nil, 0
}

// appendResponsesInputItem appends one input item to respReq.Input, normalizing
// a string Input into a single user message so the re-request loop can extend
// the conversation with the web_search function_call + function_call_output.
func appendResponsesInputItem(respReq *ResponsesAPIRequest, item map[string]interface{}) {
	var arr []interface{}
	switch v := respReq.Input.(type) {
	case []interface{}:
		arr = v
	case string:
		arr = []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": v}}
	case nil:
		arr = []interface{}{}
	default:
		b, err := json.Marshal(v)
		if err == nil {
			var asArr []interface{}
			if json.Unmarshal(b, &asArr) == nil {
				arr = asArr
			} else {
				arr = []interface{}{v}
			}
		} else {
			arr = []interface{}{v}
		}
	}
	respReq.Input = append(arr, item)
}

// respSearchSegment captures one search iteration's client-facing output.
type respSearchSegment struct {
	reasoning string
	callID    string
	query     string
	results   []SearchResult
	searchErr string
}

// normalizedToolCall is a provider-agnostic tool call: Ollama gives arguments
// as a map, OpenAI as a JSON string — we parse both into a map for the loop.
type normalizedToolCall struct {
	id        string
	name      string
	arguments map[string]interface{}
	argsRaw   string
}

// normalizedTurn is a provider-agnostic non-streaming chat response.
type normalizedTurn struct {
	content      string
	thinking     string
	toolCalls    []normalizedToolCall
	inputTokens  int
	outputTokens int
}

// firstTranslatedResponsesRequest builds the translated upstream request for
// debug logging (#2), picking the right shape for the provider type.
func firstTranslatedResponsesRequest(respReq *ResponsesAPIRequest, rp *ResolvedProvider) interface{} {
	if rp.ProviderType == "openai" {
		cr := translateResponsesAPIToChatCompletions(respReq)
		cr.Stream = false
		cr.StreamOptions = nil
		return cr
	}
	or := translateResponsesAPIToOllama(respReq)
	or.Stream = false
	return or
}

// upstreamResponsesChat performs one non-streaming upstream chat request for
// the Responses web-search loop, dispatching on provider type so both Ollama
// (Ollama Cloud / local) and OpenAI-compatible (OpenCode Go / custom) upstreams
// are supported. respReq is re-translated each call so appended function_call /
// function_call_output input items are picked up on re-request.
func (pr *ProviderRouter) upstreamResponsesChat(respReq *ResponsesAPIRequest, rp *ResolvedProvider) (*normalizedTurn, error) {
	if rp.ProviderType == "openai" {
		chatReq := translateResponsesAPIToChatCompletions(respReq)
		chatReq.Stream = false
		chatReq.StreamOptions = nil
		body, err := json.Marshal(chatReq)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		httpReq, err := http.NewRequest(http.MethodPost, rp.chatCompletionsURL(), bytes.NewReader(body))
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
		var oai OpenAIChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&oai); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		return openAIResponseToNormalized(&oai), nil
	}
	ollamaReq := translateResponsesAPIToOllama(respReq)
	ollamaReq.Stream = false
	oresp, err := pr.upstreamChat(ollamaReq, rp)
	if err != nil {
		return nil, err
	}
	return ollamaResponseToNormalized(oresp), nil
}

func ollamaResponseToNormalized(o *OllamaChatResponse) *normalizedTurn {
	t := &normalizedTurn{
		content:      o.Message.Content,
		thinking:     o.Message.Thinking,
		inputTokens:  o.PromptEvalCount,
		outputTokens: o.EvalCount,
	}
	for _, tc := range o.Message.ToolCalls {
		args := tc.Function.Arguments
		argsRaw, _ := json.Marshal(args)
		t.toolCalls = append(t.toolCalls, normalizedToolCall{
			id:        tc.ID,
			name:      tc.Function.Name,
			arguments: args,
			argsRaw:   string(argsRaw),
		})
	}
	return t
}

func openAIResponseToNormalized(o *OpenAIChatResponse) *normalizedTurn {
	t := &normalizedTurn{
		inputTokens:  o.Usage.PromptTokens,
		outputTokens: o.Usage.CompletionTokens,
	}
	if len(o.Choices) > 0 {
		ch := o.Choices[0]
		t.content = openAIContentToString(ch.Message.Content)
		if ch.Message.ReasoningContent != nil {
			t.thinking = *ch.Message.ReasoningContent
		}
		for _, tc := range ch.Message.ToolCalls {
			args := map[string]interface{}{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			t.toolCalls = append(t.toolCalls, normalizedToolCall{
				id:        tc.ID,
				name:      tc.Function.Name,
				arguments: args,
				argsRaw:   tc.Function.Arguments,
			})
		}
	}
	return t
}

// openAIContentToString extracts plain text from an OpenAI message content
// field (string, nil, or an array of text content parts).
func openAIContentToString(c interface{}) string {
	switch v := c.(type) {
	case string:
		return v
	case []interface{}:
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if s, ok := m["text"].(string); ok {
						b.WriteString(s)
					}
				}
			}
		}
		return b.String()
	}
	return ""
}

// handleResponsesWebSearchLoop implements Pattern A for the OpenAI Responses
// API path (Codex Desktop / Grok Build with Prism Ollama/cloud models). The
// request carries a built-in web_search tool; Prism rewrites it to a function
// tool (already done by translateResponsesAPIToOllama), intercepts the model's
// web_search function call, runs the search locally, injects a
// function_call_output, and re-requests until the model produces a final
// answer. The composed web_search_call + final message (with url_citation
// annotations) is emitted to the client.
//
// Returns true if it handled the request (web_search tool present + search
// enabled). Only handles the Ollama (non-OpenAI, non-Codex) path.
func (pr *ProviderRouter) handleResponsesWebSearchLoop(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *ResolvedProvider, toolTypes map[string]string, toolNamespaces map[string]string, allTools []interface{}) bool {
	if !responsesHasWebSearchTool(toolTypes) || !searchInterceptionEnabled() {
		return false
	}
	allowed, blocked, toolMaxUses := extractResponsesWebSearchParams(allTools)
	client := detectClient(r)
	reqStart := time.Now()

	dbg := newTranslationDebugCapture("responses", respReq.Stream, respReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)
	dbg.writeJSON("1_original_request.json", respReq)

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

	dbg.writeJSON("2_translated_request.json", firstTranslatedResponsesRequest(respReq, rp))

	if respReq.Stream {
		pr.handleResponsesWebSearchStreamLive(w, r, respReq, rp, allowed, blocked, maxSearches, numResults, client, reqStart)
		return true
	}

	// Non-streaming: collect all turns, then emit one composed JSON response.
	var segments []respSearchSegment
	var finalReasoning, finalText string
	var inputTokens, outputTokens int
	var pendingNonSearchCalls []normalizedToolCall
	searches := 0

	for searches < maxSearches {
		turn, err := pr.upstreamResponsesChat(respReq, rp)
		if err != nil {
			log.Printf("[search] responses pattern A upstream error: %v", err)
			writeOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
			return true
		}
		if turn.inputTokens > 0 {
			inputTokens = turn.inputTokens
		}
		outputTokens += turn.outputTokens

		var wsCall *normalizedToolCall
		var nonSearchCalls []normalizedToolCall
		for i := range turn.toolCalls {
			if isSearchToolName(turn.toolCalls[i].name) {
				if wsCall == nil {
					wsCall = &turn.toolCalls[i]
				}
				continue
			}
			nonSearchCalls = append(nonSearchCalls, turn.toolCalls[i])
		}

		if wsCall == nil {
			finalReasoning = turn.thinking
			finalText = turn.content
			pendingNonSearchCalls = nonSearchCalls
			break
		}

		callID := wsCall.id
		if callID == "" {
			callID = fmt.Sprintf("call_%s_%d", wsCall.name, searches+1)
			wsCall.id = callID
		}
		query := extractSearchQuery(wsCall)

		outcome, sErr := runSearch(r.Context(), SearchQuery{
			Query:          query,
			AllowedDomains: allowed,
			BlockedDomains: blocked,
			NumResults:     numResults,
		})
		var results []SearchResult
		var searchErr string
		if sErr != nil {
			searchErr = sErr.Error()
			log.Printf("[search] responses pattern A search failed for %q: %v", query, sErr)
		} else {
			results = outcome.Results
		}
		segments = append(segments, respSearchSegment{
			reasoning: turn.thinking, callID: callID, query: query, results: results, searchErr: searchErr,
		})
		searches++

		// Preserve the model's thinking across the re-request so reasoning-mode
		// providers (e.g. DeepSeek v4, which requires reasoning_content to be
		// passed back in thinking mode) don't reject the request or lose context
		// between search turns.
		if turn.thinking != "" {
			appendResponsesInputItem(respReq, map[string]interface{}{
				"type":    "reasoning",
				"id":      generateID("rs_"),
				"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": turn.thinking}},
			})
		}
		argsJSON, _ := json.Marshal(wsCall.arguments)
		appendResponsesInputItem(respReq, map[string]interface{}{
			"type":      "function_call",
			"id":        "fc_" + callID,
			"call_id":   callID,
			"name":      wsCall.name,
			"arguments": string(argsJSON),
		})
		appendResponsesInputItem(respReq, map[string]interface{}{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  buildSearchResultsPayload(query, results, searchErr),
		})
	}

	// The terminal turn called non-search tools. Forward them to the client so
	// the agent loop continues; skip the synthesized final message since the
	// model hasn't produced a final answer yet.
	if len(pendingNonSearchCalls) == 0 {
		if searches >= maxSearches && finalText == "" {
			finalText = fmt.Sprintf("Reached the maximum number of web searches (%d) for this request.", maxSearches)
		}
		if finalText == "" && len(segments) > 0 {
			last := segments[len(segments)-1]
			finalText = buildWebSearchSummary(last.query, last.results, last.searchErr)
		}
	}

	pr.emitResponsesWebSearchJSON(w, respReq, segments, finalReasoning, finalText, pendingNonSearchCalls, inputTokens, outputTokens)
	globalStats.RecordRequest(respReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))
	return true
}

// handleResponsesWebSearchStreamLive runs the Pattern A search loop while
// streaming Responses SSE events live to the client. Unlike the old buffered
// emitter, this emits each turn's reasoning + the web_search_call lifecycle
// (in_progress -> searching -> [search runs] -> completed) as it happens, with
// real-time flushes so Grok Build / Codex Desktop can render the "searching..."
// indicator while the search is actually running. The final answer's text is
// emitted as chunked deltas after the search phase completes.
func (pr *ProviderRouter) handleResponsesWebSearchStreamLive(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *ResolvedProvider, allowed, blocked []string, maxSearches, numResults int, client string, reqStart time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, canFlush := w.(http.Flusher)
	e := &responsesEmitter{w: w, flusher: flusher, canFlush: canFlush}

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	completedOutput := []interface{}{}
	outputIndex := -1
	var segments []respSearchSegment
	var inputTokens, outputTokens int
	searches := 0

	emitCreated := func(status string) {
		e.emit("response.created", map[string]interface{}{
			"type": "response.created",
			"response": map[string]interface{}{
				"id": respID, "object": "response", "created_at": createdAt, "model": respReq.Model,
				"background": false, "error": nil, "status": status, "output": []interface{}{},
				"usage": responsesUsageMap(0, 0),
			},
		})
		e.emit("response.in_progress", map[string]interface{}{
			"type": "response.in_progress",
			"response": map[string]interface{}{
				"id": respID, "object": "response", "created_at": createdAt, "model": respReq.Model,
				"background": false, "error": nil, "status": "in_progress", "output": []interface{}{},
				"usage": responsesUsageMap(0, 0),
			},
		})
	}
	emitCreated("in_progress")

	emitReasoning := func(text string) {
		if text == "" {
			return
		}
		outputIndex++
		rid := generateID("rs_")
		e.emit("response.output_item.added", map[string]interface{}{
			"type": "response.output_item.added", "output_index": outputIndex,
			"item": map[string]interface{}{"id": rid, "type": "reasoning", "summary": []interface{}{}},
		})
		e.emit("response.reasoning_summary_part.added", map[string]interface{}{
			"type": "response.reasoning_summary_part.added", "item_id": rid, "output_index": outputIndex, "summary_index": 0,
			"part": map[string]interface{}{"type": "summary_text", "text": ""},
		})
		e.emit("response.reasoning_summary_text.delta", map[string]interface{}{
			"type": "response.reasoning_summary_text.delta", "item_id": rid, "output_index": outputIndex, "summary_index": 0, "delta": text,
		})
		emitReasoningClose(e, rid, outputIndex, 0, text, true, &completedOutput)
	}

	// emitWebSearchCall emits the full web_search_call lifecycle for one search.
	// The search itself runs between the "searching" and "completed" events so
	// the client observes a real-time gap while the search executes.
	emitWebSearchCall := func(query string, results []SearchResult) {
		outputIndex++
		wsID := generateID("ws_")
		action := map[string]interface{}{"type": "search", "query": query}
		e.emit("response.output_item.added", map[string]interface{}{
			"type": "response.output_item.added", "output_index": outputIndex,
			"item": map[string]interface{}{"id": wsID, "type": "web_search_call", "status": "in_progress", "action": action},
		})
		e.emit("response.web_search_call.in_progress", map[string]interface{}{
			"type": "response.web_search_call.in_progress", "item_id": wsID, "output_index": outputIndex,
		})
		e.emit("response.web_search_call.searching", map[string]interface{}{
			"type": "response.web_search_call.searching", "item_id": wsID, "output_index": outputIndex,
		})
		if e.canFlush {
			e.flusher.Flush()
		}
		// Search runs here (real-time gap visible to the client).
		// Source shape per OpenAI Responses spec: { type: "url", url }. Grok
		// Build's strict deserializer requires the `type` discriminator and does
		// not accept a `title` field on sources.
		sources := make([]interface{}, 0, len(results))
		for _, res := range results {
			if res.URL == "" {
				continue
			}
			sources = append(sources, map[string]interface{}{"type": "url", "url": res.URL})
		}
		completedAction := map[string]interface{}{"type": "search", "query": query, "sources": sources}
		e.emit("response.web_search_call.completed", map[string]interface{}{
			"type": "response.web_search_call.completed", "item_id": wsID, "output_index": outputIndex,
		})
		wsItem := map[string]interface{}{"id": wsID, "type": "web_search_call", "status": "completed", "action": completedAction}
		e.emit("response.output_item.done", map[string]interface{}{
			"type": "response.output_item.done", "output_index": outputIndex, "item": wsItem,
		})
		completedOutput = append(completedOutput, wsItem)
		if e.canFlush {
			e.flusher.Flush()
		}
	}

	emitFinalMessage := func(text string, annotations []interface{}) {
		outputIndex++
		msgID := generateID("msg_")
		e.emit("response.output_item.added", map[string]interface{}{
			"type": "response.output_item.added", "output_index": outputIndex,
			"item": map[string]interface{}{"id": msgID, "type": "message", "status": "in_progress", "role": "assistant", "content": []interface{}{}},
		})
		e.emit("response.content_part.added", map[string]interface{}{
			"type": "response.content_part.added", "item_id": msgID, "output_index": outputIndex, "content_index": 0,
			"part": responsesOutputTextPart(""),
		})
		for _, chunk := range chunkText(text, 160) {
			e.emit("response.output_text.delta", map[string]interface{}{
				"type": "response.output_text.delta", "item_id": msgID, "output_index": outputIndex, "content_index": 0, "delta": chunk,
			})
			if e.canFlush {
				e.flusher.Flush()
			}
		}
		e.emit("response.output_text.done", map[string]interface{}{
			"type": "response.output_text.done", "item_id": msgID, "output_index": outputIndex, "content_index": 0, "text": text,
		})
		finalPart := map[string]interface{}{"type": "output_text", "text": text, "annotations": annotations, "logprobs": []interface{}{}}
		e.emit("response.content_part.done", map[string]interface{}{
			"type": "response.content_part.done", "item_id": msgID, "output_index": outputIndex, "content_index": 0, "part": finalPart,
		})
		msgItem := map[string]interface{}{"id": msgID, "type": "message", "status": "completed", "role": "assistant", "content": []interface{}{finalPart}}
		e.emit("response.output_item.done", map[string]interface{}{
			"type": "response.output_item.done", "output_index": outputIndex, "item": msgItem,
		})
		completedOutput = append(completedOutput, msgItem)
	}

	// emitFunctionCall forwards a non-search tool call (e.g. read_file,
	// run_terminal_command) to the client as a function_call output item so the
	// agent loop (Grok Build) can execute it and continue. The search intercept
	// must not drop these — dropping them leaves the client with an empty
	// response and the turn ends prematurely, causing the agent to spiral.
	emitFunctionCall := func(tc normalizedToolCall) {
		callID := tc.id
		if callID == "" {
			callID = fmt.Sprintf("call_%s_%d", tc.name, outputIndex+1)
		}
		itemID := "fc_" + callID
		outputIndex++
		e.emit("response.output_item.added", map[string]interface{}{
			"type": "response.output_item.added", "output_index": outputIndex,
			"item": map[string]interface{}{
				"id": itemID, "type": "function_call", "status": "in_progress",
				"call_id": callID, "name": tc.name, "arguments": "",
			},
		})
		e.emit("response.function_call_arguments.done", map[string]interface{}{
			"type": "response.function_call_arguments.done", "output_index": outputIndex,
			"item_id": itemID, "arguments": tc.argsRaw,
		})
		fcItem := map[string]interface{}{
			"id": itemID, "type": "function_call", "status": "completed",
			"call_id": callID, "name": tc.name, "arguments": tc.argsRaw,
		}
		e.emit("response.output_item.done", map[string]interface{}{
			"type": "response.output_item.done", "output_index": outputIndex, "item": fcItem,
		})
		completedOutput = append(completedOutput, fcItem)
		if e.canFlush {
			e.flusher.Flush()
		}
	}
	emitCompleted := func(outputText string) {
		completedResp := map[string]interface{}{
			"id": respID, "object": "response", "created_at": createdAt, "model": respReq.Model,
			"background": false, "error": nil, "status": "completed",
			"output": completedOutput, "output_text": outputText,
			"usage": responsesUsageMap(inputTokens, outputTokens),
		}
		mergeResponsesEchoFields(completedResp, respReq)
		e.emit("response.completed", map[string]interface{}{"type": "response.completed", "response": completedResp})
		if e.canFlush {
			e.flusher.Flush()
		}
		globalStats.RecordRequest(respReq.Model, rp.ProviderID, client, inputTokens, outputTokens, time.Since(reqStart))
	}

	var finalReasoning, finalText string
	// pendingNonSearchCalls captures non-search tool calls (read_file, etc.)
	// from the terminal turn so they can be forwarded to the client instead of
	// being silently dropped (which caused the agent to spiral on an empty
	// response).
	var pendingNonSearchCalls []normalizedToolCall
	for searches < maxSearches {
		turn, err := pr.upstreamResponsesChat(respReq, rp)
		if err != nil {
			log.Printf("[search] responses pattern A upstream error: %v", err)
			return
		}
		if turn.inputTokens > 0 {
			inputTokens = turn.inputTokens
		}
		outputTokens += turn.outputTokens

		var wsCall *normalizedToolCall
		var nonSearchCalls []normalizedToolCall
		for i := range turn.toolCalls {
			if isSearchToolName(turn.toolCalls[i].name) {
				if wsCall == nil {
					wsCall = &turn.toolCalls[i]
				}
				continue
			}
			nonSearchCalls = append(nonSearchCalls, turn.toolCalls[i])
		}
		if wsCall == nil {
			finalReasoning = turn.thinking
			finalText = turn.content
			pendingNonSearchCalls = nonSearchCalls
			break
		}

		callID := wsCall.id
		if callID == "" {
			callID = fmt.Sprintf("call_%s_%d", wsCall.name, searches+1)
			wsCall.id = callID
		}
		query := extractSearchQuery(wsCall)

		emitReasoning(turn.thinking)

		outcome, sErr := runSearch(r.Context(), SearchQuery{
			Query:          query,
			AllowedDomains: allowed,
			BlockedDomains: blocked,
			NumResults:     numResults,
		})
		var results []SearchResult
		var searchErr string
		if sErr != nil {
			searchErr = sErr.Error()
			log.Printf("[search] responses pattern A search failed for %q: %v", query, sErr)
		} else {
			results = outcome.Results
		}
		emitWebSearchCall(query, results)
		segments = append(segments, respSearchSegment{reasoning: turn.thinking, callID: callID, query: query, results: results, searchErr: searchErr})
		searches++

		// Preserve the model's thinking across the re-request so reasoning-mode
		// providers (e.g. DeepSeek v4, which requires reasoning_content to be
		// passed back in thinking mode) don't reject the request or lose context
		// between search turns.
		if turn.thinking != "" {
			appendResponsesInputItem(respReq, map[string]interface{}{
				"type":    "reasoning",
				"id":      generateID("rs_"),
				"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": turn.thinking}},
			})
		}
		argsJSON, _ := json.Marshal(wsCall.arguments)
		appendResponsesInputItem(respReq, map[string]interface{}{
			"type":      "function_call",
			"id":        "fc_" + callID,
			"call_id":   callID,
			"name":      wsCall.name,
			"arguments": string(argsJSON),
		})
		appendResponsesInputItem(respReq, map[string]interface{}{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  buildSearchResultsPayload(query, results, searchErr),
		})
	}

	// The terminal turn called non-search tools (read_file, etc.). Forward them
	// to the client and complete — the agent executes the tools and sends the
	// next request. Do not synthesize a final text message.
	if len(pendingNonSearchCalls) > 0 {
		emitReasoning(finalReasoning)
		for _, tc := range pendingNonSearchCalls {
			emitFunctionCall(tc)
		}
		emitCompleted("")
		return
	}

	if searches >= maxSearches && finalText == "" {
		finalText = fmt.Sprintf("Reached the maximum number of web searches (%d) for this request.", maxSearches)
	}
	if finalText == "" && len(segments) > 0 {
		last := segments[len(segments)-1]
		finalText = buildWebSearchSummary(last.query, last.results, last.searchErr)
	}

	emitReasoning(finalReasoning)
	text, annotations := buildResponsesFinalTextWithCitations(finalText, segments)
	emitFinalMessage(text, annotations)
	emitCompleted(text)
}
// emitResponsesWebSearchJSON writes a non-streaming Responses API response.
func (pr *ProviderRouter) emitResponsesWebSearchJSON(w http.ResponseWriter, respReq *ResponsesAPIRequest, segments []respSearchSegment, finalReasoning, finalText string, pendingNonSearchCalls []normalizedToolCall, inputTokens, outputTokens int) {
	output := []interface{}{}
	addReasoning := func(text string) {
		if text == "" {
			return
		}
		output = append(output, map[string]interface{}{
			"id":      generateID("rs_"),
			"type":    "reasoning",
			"summary": []interface{}{map[string]interface{}{"type": "summary_text", "text": text}},
		})
	}
	for _, seg := range segments {
		addReasoning(seg.reasoning)
		output = append(output, map[string]interface{}{
			"id":     generateID("ws_"),
			"type":   "web_search_call",
			"status": "completed",
			"action": map[string]interface{}{"type": "search", "query": seg.query},
		})
	}
	addReasoning(finalReasoning)

	text, annotations := buildResponsesFinalTextWithCitations(finalText, segments)

	// Forward non-search tool calls (read_file, etc.) so the agent loop
	// continues. When tool calls are present, omit the synthesized final message
	// — the model hasn't produced a final answer yet.
	if len(pendingNonSearchCalls) > 0 {
		for _, tc := range pendingNonSearchCalls {
			callID := tc.id
			if callID == "" {
				callID = fmt.Sprintf("call_%s_%d", tc.name, len(output)+1)
			}
			output = append(output, map[string]interface{}{
				"id":        "fc_" + callID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   callID,
				"name":      tc.name,
				"arguments": tc.argsRaw,
			})
		}
	} else {
		output = append(output, map[string]interface{}{
			"id":     generateID("msg_"),
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []interface{}{map[string]interface{}{
				"type":        "output_text",
				"text":        text,
				"annotations": annotations,
				"logprobs":    []interface{}{},
			}},
		})
	}

	resp := map[string]interface{}{
		"id": fmt.Sprintf("resp_%d", time.Now().UnixNano()), "object": "response",
		"created_at": time.Now().Unix(), "model": respReq.Model,
		"background": false, "error": nil, "status": "completed",
		"output": output, "output_text": text,
		"usage": responsesUsageMap(inputTokens, outputTokens),
	}
	mergeResponsesEchoFields(resp, respReq)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// buildResponsesFinalTextWithCitations appends a Sources section to the final
// answer text and produces url_citation annotations spanning each source line,
// so Codex/Grok Build can render clickable citations.
func buildResponsesFinalTextWithCitations(finalText string, segments []respSearchSegment) (string, []interface{}) {
	// Collect deduplicated sources across all searches.
	type src struct{ title, url string }
	seen := map[string]bool{}
	var sources []src
	for _, seg := range segments {
		for _, r := range seg.results {
			if r.URL == "" || seen[r.URL] {
				continue
			}
			seen[r.URL] = true
			sources = append(sources, src{title: r.Title, url: r.URL})
		}
	}
	if len(sources) == 0 {
		return finalText, []interface{}{}
	}

	var b strings.Builder
	b.WriteString(finalText)
	if !strings.HasSuffix(finalText, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\nSources:\n")
	sourceLineStarts := make([]int, len(sources))
	for i, s := range sources {
		sourceLineStarts[i] = b.Len()
		fmt.Fprintf(&b, "[%d] %s — %s\n", i+1, s.title, s.url)
	}
	text := b.String()

	annotations := make([]interface{}, 0, len(sources))
	for i, s := range sources {
		start := sourceLineStarts[i]
		end := len(text)
		if i+1 < len(sources) {
			end = sourceLineStarts[i+1]
		}
		// Trim the trailing newline from the span.
		if end > start && text[end-1] == '\n' {
			end--
		}
		annotations = append(annotations, map[string]interface{}{
			"type":         "url_citation",
			"url":          s.url,
			"title":        s.title,
			"start_index":  start,
			"end_index":    end,
			"index":        i,
		})
	}
	return text, annotations
}