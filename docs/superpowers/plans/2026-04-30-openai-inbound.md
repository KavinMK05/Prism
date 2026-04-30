# OpenAI Inbound API Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenAI Chat Completions API compatibility to Prism so any OpenAI SDK client can connect.

**Architecture:** Four direct pipelines — OpenAI inbound requests route to either Ollama or OpenAI backend based on provider type, with format-specific translation at each boundary. No intermediate canonical format.

**Tech Stack:** Go 1.26, net/http, encoding/json, existing Proxy struct and types

---

### Task 1: Add OpenAI error types to models.go

**Files:**
- Modify: `models.go:241` (append after `OpenAIStreamDelta`)

- [ ] **Step 1: Add `OpenAIErrorResponse` and `OpenAIErrorDetail` types**

Append to `models.go` after line 242:

```go
type OpenAIErrorResponse struct {
	Error OpenAIErrorDetail `json:"error"`
}

type OpenAIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build .`
Expected: SUCCESS (no errors, types defined but unused is fine in Go)

- [ ] **Step 3: Commit**

```bash
git add models.go
git commit -m "Add OpenAI error response types"
```

---

### Task 2: Add writeOpenAIError helper

**Files:**
- Create: `openai_inbound.go` (initial version with just the error helper)

- [ ] **Step 1: Create `openai_inbound.go` with `writeOpenAIError` function**

```go
package main

import (
	"encoding/json"
	"net/http"
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
```

- [ ] **Step 2: Verify it compiles**

Run: `go build .`
Expected: SUCCESS

- [ ] **Step 3: Commit**

```bash
git add openai_inbound.go
git commit -m "Add writeOpenAIError helper for OpenAI error responses"
```

---

### Task 3: Add route registrations in main.go

**Files:**
- Modify: `main.go:64-68` (route registration block)

- [ ] **Step 1: Add OpenAI routes after existing Anthropic routes**

In `main.go`, after the line `mux.HandleFunc("/health", loggingMiddleware(handleHealth))` and before `addr := host + ":" + port`, add:

```go
	mux.HandleFunc("/v1/chat/completions", loggingMiddleware(authMiddleware(proxyAPIKey, proxy.HandleOpenAIChatCompletions)))
	mux.HandleFunc("/v1/models", loggingMiddleware(authMiddleware(proxyAPIKey, proxy.HandleModels)))
```

The full route block should read:

```go
	mux := http.NewServeMux()
	mux.HandleFunc("/", loggingMiddleware(handleRoot))
	mux.HandleFunc("/v1/messages", loggingMiddleware(authMiddleware(proxyAPIKey, proxy.HandleMessages)))
	mux.HandleFunc("/v1/messages/count_tokens", loggingMiddleware(authMiddleware(proxyAPIKey, handleCountTokens)))
	mux.HandleFunc("/health", loggingMiddleware(handleHealth))
	mux.HandleFunc("/v1/chat/completions", loggingMiddleware(authMiddleware(proxyAPIKey, proxy.HandleOpenAIChatCompletions)))
	mux.HandleFunc("/v1/models", loggingMiddleware(authMiddleware(proxyAPIKey, proxy.HandleModels)))
```

- [ ] **Step 2: Verify it compiles (will fail — functions not yet defined)**

Run: `go build .`
Expected: FAIL with `proxy.HandleOpenAIChatCompletions` and `proxy.HandleModels` undefined — this is expected, these are added in later tasks.

---

### Task 4: Implement HandleOpenAIChatCompletions dispatcher

**Files:**
- Modify: `openai_inbound.go` (add dispatcher function)

- [ ] **Step 1: Add `HandleOpenAIChatCompletions` method to Proxy**

Append to `openai_inbound.go`:

```go
func (p *Proxy) HandleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
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

	openAIReq.Model = getEffectiveModel(p.modelRemap, openAIReq.Model)

	if openAIReq.Stream {
		if p.providerType == "openai" {
			p.handleOpenAIInboundOpenAIStreaming(w, r, &openAIReq)
		} else {
			p.handleOpenAIInboundOllamaStreaming(w, r, &openAIReq)
		}
		return
	}

	if p.providerType == "openai" {
		p.handleOpenAIInboundToOpenAI(w, r, &openAIReq)
	} else {
		p.handleOpenAIInboundToOllama(w, r, &openAIReq)
	}
}
```

Note: The streaming functions are defined in Task 8. This is fine as Go resolves all symbols at link time.

- [ ] **Step 2: Verify it compiles (will still fail until all referenced functions exist)**

This is expected — continue to next tasks to fill in the implementations.

---

### Task 5: Implement OpenAI→Ollama non-streaming translation

**Files:**
- Modify: `openai_inbound.go` (add translation functions + handler)

- [ ] **Step 1: Add `handleOpenAIInboundToOllama` method**

Append to `openai_inbound.go`:

```go
func (p *Proxy) handleOpenAIInboundToOllama(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest) {
	ollamaReq := translateOpenAIToOllama(openAIReq)

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

	log.Printf("-> %s %s", req.Method, p.upstreamURL+"/api/chat")

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

	openAIResp := translateOllamaToOpenAI(&ollamaResp, openAIReq)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(openAIResp)
}
```

This requires adding imports at the top of `openai_inbound.go`:

```go
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)
```

Replace the existing import block (which only had `encoding/json` and `net/http`) with this expanded one.

- [ ] **Step 2: Add `translateOpenAIToOllama` function**

Append to `openai_inbound.go`:

```go
func translateOpenAIToOllama(req *OpenAIChatRequest) *OllamaChatRequest {
	messages := []OllamaMessage{}

	for _, msg := range req.Messages {
		messages = append(messages, translateOpenAIMessageToOllama(msg)...)
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

	return ollamaReq
}
```

- [ ] **Step 3: Add `translateOpenAIMessageToOllama` function**

Append to `openai_inbound.go`:

```go
func translateOpenAIMessageToOllama(msg OpenAIChatMessage) []OllamaMessage {
	if msg.Role == "tool" {
		content := ""
		if s, ok := msg.Content.(string); ok {
			content = s
		} else if msg.Content != nil {
			b, _ := json.Marshal(msg.Content)
			content = string(b)
		}
		return []OllamaMessage{{
			Role:    "tool",
			Content: content,
		}}
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
				Function: OllamaToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: args,
				},
			}
		}
		content := ""
		if s, ok := msg.Content.(string); ok {
			content = s
		}
		return []OllamaMessage{{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}}
	}

	switch content := msg.Content.(type) {
	case string:
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: content,
		}}
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
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: strings.Join(textParts, ""),
			Images:  images,
		}}
	default:
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: fmt.Sprintf("%v", content),
		}}
	}
}
```

This requires adding `"strings"` to the import block of `openai_inbound.go`.

- [ ] **Step 4: Add `translateOpenAIToolsToOllama` function**

Append to `openai_inbound.go`:

```go
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
```

- [ ] **Step 5: Add `translateOllamaToOpenAI` function**

Append to `openai_inbound.go`:

```go
func translateOllamaToOpenAI(ollama *OllamaChatResponse, req *OpenAIChatRequest) OpenAIChatResponse {
	msg := OpenAIChatMessage{
		Role: "assistant",
	}

	if ollama.Message.Thinking != "" {
		msg.ReasoningContent = ollama.Message.Thinking
	}

	if ollama.Message.Content != "" {
		msg.Content = ollama.Message.Content
	} else {
		msg.Content = ""
	}

	if len(ollama.Message.ToolCalls) > 0 {
		toolCalls := make([]OpenAIToolCall, len(ollama.Message.ToolCalls))
		for i, tc := range ollama.Message.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Function.Arguments)
			toolCalls[i] = OpenAIToolCall{
				ID:   fmt.Sprintf("call_%s_%d", tc.Function.Name, i),
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
```

- [ ] **Step 6: Verify it compiles**

Run: `go build .`
Expected: FAIL — `handleOpenAIInboundOpenAIStreaming`, `handleOpenAIInboundOllamaStreaming`, `handleOpenAIInboundToOpenAI`, and `HandleModels` still undefined. This is expected.

---

### Task 6: Implement OpenAI→OpenAI pass-through (non-streaming)

**Files:**
- Modify: `openai_inbound.go` (add pass-through handler)

- [ ] **Step 1: Add `handleOpenAIInboundToOpenAI` method**

Append to `openai_inbound.go`:

```go
func (p *Proxy) handleOpenAIInboundToOpenAI(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest) {
	body, err := json.Marshal(openAIReq)
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

	log.Printf("-> %s %s", req.Method, p.upstreamURL+"/v1/chat/completions")

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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeOpenAIError(w, 502, "server_error", "Failed to read upstream response")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build .`
Expected: FAIL — `handleOpenAIInboundOpenAIStreaming`, `handleOpenAIInboundOllamaStreaming`, and `HandleModels` still undefined.

---

### Task 7: Implement /v1/models endpoint

**Files:**
- Modify: `openai_inbound.go` (add HandleModels)

- [ ] **Step 1: Add `HandleModels` method**

Append to `openai_inbound.go`:

```go
func (p *Proxy) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, 405, "invalid_request_error", "Only GET is supported")
		return
	}

	models := []interface{}{}
	seen := map[string]bool{}

	if p.modelRemap != nil {
		for _, m := range p.modelRemap.KnownModels {
			if !seen[m] {
				seen[m] = true
				models = append(models, map[string]interface{}{
					"id":       m,
					"object":   "model",
					"created":  0,
					"owned_by": "ollama-proxy",
				})
			}
		}
		for _, target := range p.modelRemap.Aliases {
			if !seen[target] {
				seen[target] = true
				models = append(models, map[string]interface{}{
					"id":       target,
					"object":   "model",
					"created":  0,
					"owned_by": "ollama-proxy",
				})
			}
		}
		for alias := range p.modelRemap.Aliases {
			if !seen[alias] {
				seen[alias] = true
				models = append(models, map[string]interface{}{
					"id":       alias,
					"object":   "model",
					"created":  0,
					"owned_by": "ollama-proxy",
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
```

- [ ] **Step 2: Verify it compiles**

Run: `go build .`
Expected: FAIL — `handleOpenAIInboundOpenAIStreaming` and `handleOpenAIInboundOllamaStreaming` still undefined.

---

### Task 8: Implement OpenAI inbound streaming — Ollama→OpenAI SSE

**Files:**
- Create: `openai_inbound_streaming.go`

- [ ] **Step 1: Create `openai_inbound_streaming.go` with Ollama→OpenAI streaming handler**

```go
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

func (p *Proxy) handleOpenAIInboundOllamaStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest) {
	ollamaReq := translateOpenAIToOllama(openAIReq)

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

	log.Printf("-> %s %s (streaming)", req.Method, p.upstreamURL+"/api/chat")

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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	respID := fmt.Sprintf("chatcmpl-%s", openAIReq.Model)
	created := 0

	writeOpenAISSE(w, flusher, canFlush, OpenAIStreamChunk{
		ID:     respID,
		Object: "chat.completion.chunk",
		Model:  openAIReq.Model,
		Choices: []OpenAIStreamChoice{
			{
				Index: 0,
				Delta: OpenAIStreamDelta{
					Role: "assistant",
				},
			},
		},
	})

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	outputTokens := 0
	inputTokens := 0
	thinkingActive := false
	toolCallsActive := false
	toolCallIndex := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var chunk OllamaChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}

		if chunk.PromptEvalCount > 0 {
			inputTokens = chunk.PromptEvalCount
		}
		if chunk.EvalCount > 0 {
			outputTokens = chunk.EvalCount
		}

		if chunk.Message.Thinking != "" {
			if !thinkingActive {
				thinkingActive = true
			}
			writeOpenAISSE(w, flusher, canFlush, OpenAIStreamChunk{
				ID:     respID,
				Object: "chat.completion.chunk",
				Model:  openAIReq.Model,
				Choices: []OpenAIStreamChoice{
					{
						Index: 0,
						Delta: OpenAIStreamDelta{
							ReasoningContent: &chunk.Message.Thinking,
						},
					},
				},
			})
		}

		if chunk.Message.Thinking == "" && thinkingActive {
			thinkingActive = false
		}

		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				if !toolCallsActive {
					toolCallsActive = true
					writeOpenAISSE(w, flusher, canFlush, OpenAIStreamChunk{
						ID:     respID,
						Object: "chat.completion.chunk",
						Model:  openAIReq.Model,
						Choices: []OpenAIStreamChoice{
							{
								Index: 0,
								Delta: OpenAIStreamDelta{
									ToolCalls: []OpenAIToolCall{
										{
											ID:   fmt.Sprintf("call_%s_%d", tc.Function.Name, toolCallIndex),
											Type: "function",
											Function: OpenAIToolCallFunc{
												Name:      tc.Function.Name,
												Arguments: string(argsJSON),
											},
										},
									},
								},
							},
						},
					})
					toolCallIndex++
				} else {
					if string(argsJSON) != "{}" {
						writeOpenAISSE(w, flusher, canFlush, OpenAIStreamChunk{
							ID:     respID,
							Object: "chat.completion.chunk",
							Model:  openAIReq.Model,
							Choices: []OpenAIStreamChoice{
								{
									Index: 0,
									Delta: OpenAIStreamDelta{
										ToolCalls: []OpenAIToolCall{
											{
												ID:   fmt.Sprintf("call_%s_%d", tc.Function.Name, toolCallIndex),
												Type: "function",
												Function: OpenAIToolCallFunc{
													Name:      tc.Function.Name,
													Arguments: string(argsJSON),
												},
											},
										},
									},
								},
							},
						})
						toolCallIndex++
					} else {
						writeOpenAISSE(w, flusher, canFlush, OpenAIStreamChunk{
							ID:     respID,
							Object: "chat.completion.chunk",
							Model:  openAIReq.Model,
							Choices: []OpenAIStreamChoice{
								{
									Index: 0,
									Delta: OpenAIStreamDelta{
										ToolCalls: []OpenAIToolCall{
											{
												ID:   fmt.Sprintf("call_%s_%d", tc.Function.Name, toolCallIndex),
												Type: "function",
												Function: OpenAIToolCallFunc{
													Name:      tc.Function.Name,
													Arguments: string(argsJSON),
												},
											},
										},
									},
								},
							},
						})
						toolCallIndex++
					}
				}
			}
		}

		if chunk.Message.Content != "" && !thinkingActive {
			content := chunk.Message.Content
			writeOpenAISSE(w, flusher, canFlush, OpenAIStreamChunk{
				ID:     respID,
				Object: "chat.completion.chunk",
				Model:  openAIReq.Model,
				Choices: []OpenAIStreamChoice{
					{
						Index: 0,
						Delta: OpenAIStreamDelta{
							Content: &content,
						},
					},
				},
			})
		}

		if chunk.Done {
			finishReason := "stop"
			switch chunk.DoneReason {
			case "length":
				finishReason = "length"
			case "tool_call":
				finishReason = "tool_calls"
			}

			writeOpenAISSE(w, flusher, canFlush, OpenAIStreamChunk{
				ID:     respID,
				Object: "chat.completion.chunk",
				Model:  openAIReq.Model,
				Choices: []OpenAIStreamChoice{
					{
						Index:        0,
						FinishReason: &finishReason,
					},
				},
				Usage: &OpenAIStreamUsage{
					PromptTokens:     inputTokens,
					CompletionTokens: outputTokens,
					TotalTokens:      inputTokens + outputTokens,
				},
			})
		}
	}
}

func writeOpenAISSE(w io.Writer, flusher http.Flusher, canFlush bool, chunk OpenAIStreamChunk) {
	dataJSON, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", dataJSON)
	if canFlush {
		flusher.Flush()
	}
}

func strPtr(s string) *string {
	return &s
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build .`
Expected: FAIL — `handleOpenAIInboundOpenAIStreaming` still undefined.

---

### Task 9: Implement OpenAI inbound streaming — OpenAI→OpenAI SSE pass-through

**Files:**
- Modify: `openai_inbound_streaming.go` (add pass-through handler)

- [ ] **Step 1: Add `handleOpenAIInboundOpenAIStreaming` method**

Append to `openai_inbound_streaming.go`:

```go
func (p *Proxy) handleOpenAIInboundOpenAIStreaming(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIChatRequest) {
	body, err := json.Marshal(openAIReq)
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

	log.Printf("-> %s %s (streaming)", req.Method, p.upstreamURL+"/v1/chat/completions")

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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		if canFlush {
			flusher.Flush()
		}
		if line == "data: [DONE]" {
			break
		}
	}
}
```

This requires adding `"fmt"` and `"bufio"` to the import block — but they are already present from the previous task. Verify the import block is:

```go
import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)
```

The `"strings"` import is not needed in this file. If it was added, remove it.

- [ ] **Step 2: Verify full project compiles**

Run: `go build -ldflags="-H windowsgui" -o ollama-proxy.exe .`
Expected: SUCCESS — all symbols resolved, binary produced.

- [ ] **Step 3: Commit all new files**

```bash
git add openai_inbound.go openai_inbound_streaming.go main.go models.go
git commit -m "Add OpenAI inbound API support with direct backend routing"
```

---

### Task 10: Manual integration test

**Files:** None (testing only)

- [ ] **Step 1: Start the proxy**

Run: `.\ollama-proxy.exe --serve`
Expected: Proxy starts on `127.0.0.1:11434`

- [ ] **Step 2: Test /v1/models endpoint**

Run: `curl -s -H "Authorization: Bearer prism" http://127.0.0.1:11434/v1/models`
Expected: JSON response with `object: "list"` and `data` array containing model objects.

- [ ] **Step 3: Test /v1/chat/completions non-streaming**

Run: `curl -s -H "Authorization: Bearer prism" -H "Content-Type: application/json" -d "{\"model\":\"glm-5.1:cloud\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello in one word\"}],\"max_tokens\":10}" http://127.0.0.1:11434/v1/chat/completions`
Expected: OpenAI-format JSON response with `object: "chat.completion"`, `choices[0].message.content`, and `usage`.

- [ ] **Step 4: Test /v1/chat/completions streaming**

Run: `curl -s -H "Authorization: Bearer prism" -H "Content-Type: application/json" -d "{\"model\":\"glm-5.1:cloud\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello in one word\"}],\"max_tokens\":10,\"stream\":true}" http://127.0.0.1:11434/v1/chat/completions`
Expected: SSE stream with `data: {...}` chunks, ending with `data: [DONE]`.

- [ ] **Step 5: Verify existing Anthropic endpoint still works**

Run: `curl -s -H "x-api-key: prism" -H "Content-Type: application/json" -d "{\"model\":\"glm-5.1:cloud\",\"max_tokens\":10,\"messages\":[{\"role\":\"user\",\"content\":\"Say hello\"}]}" http://127.0.0.1:11434/v1/messages`
Expected: Anthropic-format JSON response (unchanged behavior).

- [ ] **Step 6: Test auth rejection on OpenAI endpoint**

Run: `curl -s -H "Authorization: Bearer wrong" -H "Content-Type: application/json" -d "{}" http://127.0.0.1:11434/v1/chat/completions`
Expected: OpenAI-format error response with `error.code: 401`.

- [ ] **Step 7: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "Fix integration issues for OpenAI inbound API"
```