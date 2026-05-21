package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func (pr *ProviderRouter) HandleResponsesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, 405, "invalid_request_error", "Only POST is supported")
		return
	}

	var respReq ResponsesAPIRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&respReq); err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "Failed to parse request: "+err.Error())
		return
	}

	rp, resolvedModel, err := pr.resolveProviderForModel(respReq.Model)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to resolve provider: "+err.Error())
		return
	}
	respReq.Model = resolvedModel

	client := detectClient(r)
	globalStats.StartRequest(respReq.Model, rp.ProviderID, client)
	defer globalStats.EndRequest()

	// Codex provider: translate Responses API to Chat Completions (Codex OAuth tokens
	// don't have api.responses.write scope, so /v1/responses returns 401)
	if rp.ProviderType == "codex" {
		if respReq.Stream {
			pr.handleResponsesAPIOpenAIStreaming(w, r, &respReq, rp)
		} else {
			pr.handleResponsesAPIToOpenAI(w, r, &respReq, rp)
		}
		return
	}

	if respReq.Stream {
		if rp.ProviderType == "openai" {
			pr.handleResponsesAPIOpenAIStreaming(w, r, &respReq, rp)
		} else {
			pr.handleResponsesAPIOllamaStreaming(w, r, &respReq, rp)
		}
		return
	}

	if rp.ProviderType == "openai" {
		pr.handleResponsesAPIToOpenAI(w, r, &respReq, rp)
	} else {
		pr.handleResponsesAPIToOllama(w, r, &respReq, rp)
	}
}

func (pr *ProviderRouter) handleResponsesAPIToOpenAI(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *ResolvedProvider) {
	reqStart := time.Now()

	chatReq := translateResponsesAPIToChatCompletions(respReq)

	// Strip reasoning_effort for non-reasoning models on custom providers
	if chatReq.ReasoningEffort != "" && !pr.isModelReasoning(chatReq.Model) && rp.ProviderID != "ollama_cloud" && rp.ProviderID != "opencode_go" {
		chatReq.ReasoningEffort = ""
	}

	body, err := json.Marshal(chatReq)
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

	log.Printf("-> %s %s (responses)", req.Method, rp.chatCompletionsURL())

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

	var openAIResp OpenAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		writeOpenAIError(w, 502, "server_error", "Failed to parse upstream response: "+err.Error())
		return
	}

	responsesResp := translateChatCompletionsToResponsesAPI(&openAIResp, respReq)

	globalStats.RecordRequest(respReq.Model, rp.ProviderID, detectClient(r), openAIResp.Usage.PromptTokens, openAIResp.Usage.CompletionTokens, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsesResp)
}

func (pr *ProviderRouter) handleResponsesAPIToOllama(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest, rp *ResolvedProvider) {
	reqStart := time.Now()

	ollamaReq := translateResponsesAPIToOllama(respReq)

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

	log.Printf("-> %s %s (responses)", req.Method, rp.apiChatURL())

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

	responsesResp := translateOllamaToResponsesAPI(&ollamaResp, respReq)

	globalStats.RecordRequest(respReq.Model, rp.ProviderID, detectClient(r), ollamaResp.PromptEvalCount, ollamaResp.EvalCount, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsesResp)
}
