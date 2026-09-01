package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/stats"
)

func (pr *ProviderRouter) HandleResponsesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteOpenAIError(w, 405, "invalid_request_error", "Only POST is supported")
		return
	}

	var respReq ResponsesAPIRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&respReq); err != nil {
		WriteOpenAIError(w, 400, "invalid_request_error", "Failed to parse request: "+err.Error())
		return
	}

	rp, resolvedModel, err := pr.resolveProviderForModel(respReq.Model)
	if err != nil {
		WriteOpenAIError(w, 500, "server_error", "Failed to resolve provider: "+err.Error())
		return
	}
	respReq.Model = resolvedModel

	client := detectClient(r)
	stats.Global.StartRequest(respReq.Model, rp.ProviderID, client)
	defer stats.Global.EndRequest()

	// Extract tool type / namespace mappings for preserving built-in and
	// namespace tool types in responses. Merge the top-level tools array with
	// any "additional_tools" input items (Codex Desktop "Responses Lite"
	// declares tools inside the input array).
	allTools := collectAllResponseTools(&respReq)
	toolTypes := buildToolTypeMap(allTools)
	toolNamespaces := buildToolNamespaceMap(allTools)

	// Codex provider: forward Responses API directly to chatgpt.com/backend-api/codex/responses
	// (ChatGPT OAuth tokens only work with the backend-api/codex endpoint, not api.openai.com)
	if rp.ProviderType == "codex" {
		pr.handleCodexResponsesAPI(w, r, &respReq, rp)
		return
	}

	// Per-model API routing: if model is configured for "responses" (e.g. Zen muse-spark/gpt/grok)
	// forward Responses API directly to /v1/responses instead of translating to Chat Completions
	if pr.getModelAPI(respReq.Model, rp.ProviderID) == "responses" && rp.ProviderType == "openai" {
		pr.handleGenericResponsesAPI(w, r, &respReq, rp)
		return
	}

	// Pattern A (Ollama/cloud AND OpenAI-compatible paths): intercept built-in
	// web_search tool calls (Codex Desktop / Grok Build with Prism models), run
	// them locally via the SearchRunner, and re-request upstream with the
	// results. Handles both streaming and non-streaming internally, and both
	// Ollama and OpenAI Chat Completions upstreams.
	if pr.handleResponsesWebSearchLoop(w, r, &respReq, rp, toolTypes, toolNamespaces, allTools) {
		return
	}

	if respReq.Stream {
		if rp.ProviderType == "openai" {
			pr.handleResponsesAPIOpenAIStreaming(w, r, &respReq, rp, toolTypes, toolNamespaces)
		} else {
			pr.handleResponsesAPIOllamaStreaming(w, r, &respReq, rp, toolTypes, toolNamespaces)
		}
		return
	}

	if rp.ProviderType == "openai" {
		pr.handleResponsesAPIToOpenAI(w, r, &respReq, rp, toolTypes, toolNamespaces)
	} else {
		pr.handleResponsesAPIToOllama(w, r, &respReq, rp, toolTypes, toolNamespaces)
	}
}

func (pr *ProviderRouter) handleResponsesAPIToOpenAI(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *config.ResolvedProvider, toolTypes map[string]string, toolNamespaces map[string]string) {
	reqStart := time.Now()

	chatReq := translateResponsesAPIToChatCompletions(respReq)

	// Log tools being sent upstream for debugging
	if len(chatReq.Tools) > 0 {
		for _, t := range chatReq.Tools {
			log.Printf("[REQ] sending tool upstream: type=%s name=%s", t.Type, t.Function.Name)
		}
	}

	// Validate reasoning_effort for the model
	chatReq.ReasoningEffort = pr.validateReasoningEffort(chatReq.Model, chatReq.ReasoningEffort)

	body, err := json.Marshal(chatReq)
	if err != nil {
		WriteOpenAIError(w, 500, "server_error", "Failed to marshal request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.ChatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		WriteOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (responses)", req.Method, rp.ChatCompletionsURL())

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		WriteOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		WriteOpenAIUpstreamError(w, resp.StatusCode, respBody)
		return
	}

	var openAIResp OpenAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		WriteOpenAIError(w, 502, "server_error", "Failed to parse upstream response: "+err.Error())
		return
	}

	responsesResp := translateChatCompletionsToResponsesAPI(&openAIResp, respReq, toolTypes, toolNamespaces)

	stats.Global.RecordRequest(respReq.Model, rp.ProviderID, detectClient(r), openAIResp.Usage.PromptTokens, openAIResp.Usage.CompletionTokens, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsesResp)
}

func (pr *ProviderRouter) handleResponsesAPIToOllama(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *config.ResolvedProvider, toolTypes map[string]string, toolNamespaces map[string]string) {
	reqStart := time.Now()

	// Dump the original request, translated request, original Ollama response
	// and translated response to disk when PRISM_DEBUG_RESPONSES is set. All
	// methods are no-ops when the capture is nil (debug disabled).
	dbg := pr.dbgCapture("responses", false, respReq.Model)
	defer dbg.finish()
	w = dbg.wrapWriter(w)

	ollamaReq := translateResponsesAPIToOllama(respReq)
	dbg.writeJSON("1_original_request.json", respReq)
	dbg.writeJSON("2_translated_request.json", ollamaReq)

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		WriteOpenAIError(w, 500, "server_error", "Failed to marshal Ollama request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, rp.ApiChatURL(), bytes.NewReader(body))
	if err != nil {
		WriteOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rp.APIKey)

	log.Printf("-> %s %s (responses)", req.Method, rp.ApiChatURL())

	resp, err := pr.client.Do(req)
	if err != nil {
		log.Printf("[ERR] Upstream request failed: %v", err)
		WriteOpenAIError(w, 502, "server_error", "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	log.Printf("<- %d from upstream", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] Upstream error response: %s", string(respBody))
		WriteOpenAIUpstreamError(w, resp.StatusCode, respBody)
		return
	}

	var ollamaResp OllamaChatResponse
	// teeBody mirrors resp.Body into the debug capture (#3 original response)
	// when enabled; returns resp.Body unchanged otherwise.
	if err := json.NewDecoder(dbg.teeBody(resp.Body)).Decode(&ollamaResp); err != nil {
		WriteOpenAIError(w, 502, "server_error", "Failed to parse Ollama response: "+err.Error())
		return
	}

	responsesResp := translateOllamaToResponsesAPI(&ollamaResp, respReq, toolTypes, toolNamespaces)

	stats.Global.RecordRequest(respReq.Model, rp.ProviderID, detectClient(r), ollamaResp.PromptEvalCount, ollamaResp.EvalCount, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsesResp)
}
