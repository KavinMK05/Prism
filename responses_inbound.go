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

func (p *Proxy) HandleResponsesAPI(w http.ResponseWriter, r *http.Request) {
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

	respReq.Model = getEffectiveModel(p.modelRemap, respReq.Model)

	client := detectClient(r)
	globalStats.StartRequest(respReq.Model, p.providerType, client)
	defer globalStats.EndRequest()

	// Codex provider: translate Responses API to Chat Completions (Codex OAuth tokens
	// don't have api.responses.write scope, so /v1/responses returns 401)
	if p.providerType == "codex" {
		if respReq.Stream {
			p.handleResponsesAPIOpenAIStreaming(w, r, &respReq)
		} else {
			p.handleResponsesAPIToOpenAI(w, r, &respReq)
		}
		return
	}

	if respReq.Stream {
		if p.providerType == "openai" {
			p.handleResponsesAPIOpenAIStreaming(w, r, &respReq)
		} else {
			p.handleResponsesAPIOllamaStreaming(w, r, &respReq)
		}
		return
	}

	if p.providerType == "openai" {
		p.handleResponsesAPIToOpenAI(w, r, &respReq)
	} else {
		p.handleResponsesAPIToOllama(w, r, &respReq)
	}
}

func (p *Proxy) handleResponsesAPIToOpenAI(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest) {
	reqStart := time.Now()

	chatReq := translateResponsesAPIToChatCompletions(respReq)

	body, err := json.Marshal(chatReq)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s (responses)", req.Method, p.upstreamURL+"/v1/chat/completions")

	resp, err := p.client.Do(req)
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

	globalStats.RecordRequest(respReq.Model, p.providerType, detectClient(r), openAIResp.Usage.PromptTokens, openAIResp.Usage.CompletionTokens, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsesResp)
}

func (p *Proxy) handleResponsesAPIToOllama(w http.ResponseWriter, r *http.Request, respReq *ResponsesAPIRequest) {
	reqStart := time.Now()

	ollamaReq := translateResponsesAPIToOllama(respReq)

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to marshal Ollama request")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, 500, "server_error", "Failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	log.Printf("-> %s %s (responses)", req.Method, p.upstreamURL+"/api/chat")

	resp, err := p.client.Do(req)
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

	globalStats.RecordRequest(respReq.Model, p.providerType, detectClient(r), ollamaResp.PromptEvalCount, ollamaResp.EvalCount, time.Since(reqStart))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsesResp)
}
