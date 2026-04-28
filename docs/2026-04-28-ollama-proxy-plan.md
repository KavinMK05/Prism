# ollama-proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a lightweight Go proxy that translates Anthropic Messages API requests to Ollama native API format, enabling Claude Desktop to work directly with Ollama Cloud without LiteLLM.

**Architecture:** A single Go binary listens on `127.0.0.1:11434`, accepts Anthropic `/v1/messages` requests, translates them to Ollama `/api/chat` format, forwards to `https://ollama.com/api/chat`, and translates responses (including SSE streaming) back to Anthropic format.

**Tech Stack:** Go 1.22+, standard library `net/http` for server, no external dependencies.

---

## File Structure

```
C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\
├── main.go           # Entry point, config, server setup, signal handling
├── proxy.go          # Core request/response translation logic
├── streaming.go      # SSE stream translation (Ollama NDJSON → Anthropic SSE)
├── models.go         # All request/response type definitions
├── go.mod            # Go module definition
└── go.sum            # Dependency checksums (auto-generated)
```

---

### Task 1: Initialize Go Module and Define Types

**Files:**
- Create: `C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\go.mod`
- Create: `C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\models.go`

- [ ] **Step 1: Create directory and initialize Go module**

```bash
mkdir "C:\Users\Kavin\.claude\ollama-proxy"
cd "C:\Users\Kavin\.claude\ollama-proxy"
go mod init ollama-proxy
```

- [ ] **Step 2: Write models.go with all type definitions**

```go
package main

type AnthropicRequest struct {
	Model       string                   `json:"model"`
	MaxTokens   int                      `json:"max_tokens"`
	Messages    []AnthropicMessage       `json:"messages"`
	System      interface{}              `json:"system,omitempty"`
	Stream      bool                     `json:"stream"`
	Temperature *float64                 `json:"temperature,omitempty"`
	TopP        *float64                 `json:"top_p,omitempty"`
	TopK        *int                     `json:"top_k,omitempty"`
	StopSequences []string              `json:"stop_sequences,omitempty"`
	Tools       []AnthropicTool          `json:"tools,omitempty"`
	Thinking    *AnthropicThinking       `json:"thinking,omitempty"`
	ToolChoice  interface{}              `json:"tool_choice,omitempty"`
	Metadata    interface{}              `json:"metadata,omitempty"`
}

type AnthropicMessage struct {
	Role    string              `json:"role"`
	Content interface{}         `json:"content"`
}

type AnthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type AnthropicImageBlock struct {
	Type     string `json:"type"`
	Source   AnthropicImageSource `json:"source"`
}

type AnthropicImageSource struct {
	Type      string `json:"type"`
	MediaType  string `json:"media_type"`
	Data       string `json:"data"`
}

type AnthropicToolUseBlock struct {
	Type  string                 `json:"type"`
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

type AnthropicToolResultBlock struct {
	Type      string      `json:"type"`
	ToolUseID string      `json:"tool_use_id"`
	Content   interface{} `json:"content"`
}

type AnthropicThinkingBlock struct {
	Type   string `json:"type"`
	Thinking string `json:"thinking"`
}

type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema interface{}            `json:"input_schema"`
}

type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type AnthropicResponse struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Role         string              `json:"role"`
	Model        string              `json:"model"`
	Content      []interface{}       `json:"content"`
	StopReason   string              `json:"stop_reason"`
	StopSequence *string             `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage      `json:"usage"`
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type AnthropicError struct {
	Type  string              `json:"type"`
	Error AnthropicErrorDetail `json:"error"`
}

type AnthropicErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type OllamaChatRequest struct {
	Model    string           `json:"model"`
	Messages []OllamaMessage  `json:"messages"`
	Tools    []OllamaTool     `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	Options  *OllamaOptions   `json:"options,omitempty"`
	Think    interface{}      `json:"think,omitempty"`
	Format   interface{}      `json:"format,omitempty"`
}

type OllamaMessage struct {
	Role      string                 `json:"role"`
	Content   string                 `json:"content"`
	Images    []string               `json:"images,omitempty"`
	ToolCalls []OllamaToolCall        `json:"tool_calls,omitempty"`
}

type OllamaToolCall struct {
	Function OllamaToolCallFunction `json:"function"`
}

type OllamaToolCallFunction struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type OllamaTool struct {
	Type     string          `json:"type"`
	Function OllamaToolFunc  `json:"function"`
}

type OllamaToolFunc struct {
	Name        string      `json:"name"`
	Description  string     `json:"description,omitempty"`
	Parameters   interface{} `json:"parameters"`
}

type OllamaOptions struct {
	NumPredict int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP       *float64 `json:"top_p,omitempty"`
	TopK       *int     `json:"top_k,omitempty"`
	Stop       []string `json:"stop,omitempty"`
}

type OllamaChatResponse struct {
	Model     string        `json:"model"`
	CreatedAt string        `json:"created_at"`
	Message   OllamaMessage `json:"message"`
	Done      bool          `json:"done"`
	DoneReason string      `json:"done_reason,omitempty"`
	PromptEvalCount int     `json:"prompt_eval_count,omitempty"`
	EvalCount       int     `json:"eval_count,omitempty"`
}

type SSEEvent struct {
	Event string
	Data  string
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd "C:\Users\Kavin\.claude\ollama-proxy"
go build ./...
```

Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\go.mod" "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\models.go"
git commit -m "feat(ollama-proxy): initialize module and define API types"
```

---

### Task 2: Core Request Translation (Anthropic → Ollama)

**Files:**
- Create: `C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\proxy.go`

- [ ] **Step 1: Write proxy.go with request translation and non-streaming response handling**

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Proxy struct {
	upstreamURL string
	apiKey       string
	client      *http.Client
}

func NewProxy(upstreamURL, apiKey string) *Proxy {
	return &Proxy{
		upstreamURL: strings.TrimRight(upstreamURL, "/"),
		apiKey:      apiKey,
		client:      &http.Client{},
	}
}

func (p *Proxy) HandleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, 405, "method_not_allowed", "Only POST is supported")
		return
	}

	var anthroReq AnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&anthroReq); err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("Failed to parse request: %v", err))
		return
	}

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

	resp, err := p.client.Do(req)
	if err != nil {
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Upstream request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		writeAnthropicError(w, resp.StatusCode, "api_error", string(respBody))
		return
	}

	var ollamaResp OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Failed to parse Ollama response: %v", err))
		return
	}

	anthroResp := translateResponse(&ollamaResp, &anthroReq)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(anthroResp)
}

func translateRequest(anthro *AnthropicRequest) (*OllamaChatRequest, error) {
	messages := []OllamaMessage{}

	if anthro.System != nil {
		sysContent := systemToString(anthro.System)
		messages = append(messages, OllamaMessage{Role: "system", Content: sysContent})
	}

	for _, msg := range anthro.Messages {
		ollamaMsgs := translateMessage(msg)
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
	switch content := msg.Content.(type) {
	case string:
		if msg.Role == "tool" {
			return []OllamaMessage{{
				Role:    "tool",
				Content: content,
			}}
		}
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: content,
		}}
	case []interface{}:
		return translateContentBlocks(msg.Role, content)
	default:
		return []OllamaMessage{{
			Role:    msg.Role,
			Content: fmt.Sprintf("%v", content),
		}}
	}
}

func translateContentBlocks(role string, blocks []interface{}) []OllamaMessage {
	textParts := []string{}
	images := []string{}
	toolUseBlocks := []AnthropicToolUseBlock{}
	toolResultBlocks := []AnthropicToolResultBlock{}
	thinkingParts := []string{}

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
			toolResultBlocks = append(toolResultBlocks, AnthropicToolResultBlock{
				Type: "tool_result", ToolUseID: toolUseID, Content: blockMap["content"],
			})
		case "thinking":
			if thinking, ok := blockMap["thinking"].(string); ok {
				thinkingParts = append(thinkingParts, thinking)
			}
		}
	}

	if len(toolUseBlocks) > 0 {
		content := strings.Join(textParts, "")
		toolCalls := make([]OllamaToolCall, len(toolUseBlocks))
		for i, tu := range toolUseBlocks {
			toolCalls[i] = OllamaToolCall{
				Function: OllamaToolCallFunction{
					Name:      tu.Name,
					Arguments: tu.Input,
				},
			}
		}
		return []OllamaMessage{{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}}
	}

	if len(toolResultBlocks) > 0 {
		resultContent := ""
		for _, tr := range toolResultBlocks {
			b, _ := json.Marshal(tr.Content)
			resultContent += string(b)
		}
		return []OllamaMessage{{
			Role:    "tool",
			Content: resultContent,
		}}
	}

	msg := OllamaMessage{Role: role}
	msg.Content = strings.Join(textParts, "")
	msg.Images = images

	return []OllamaMessage{msg}
}

func translateTools(tools []AnthropicTool) []OllamaTool {
	result := make([]OllamaTool, len(tools))
	for i, t := range tools {
		result[i] = OllamaTool{
			Type: "function",
			Function: OllamaToolFunc{
				Name:        t.Name,
				Description:  t.Description,
				Parameters:   t.InputSchema,
			},
		}
	}
	return result
}

func translateResponse(ollama *OllamaChatResponse, anthroReq *AnthropicRequest) AnthropicResponse {
	content := []interface{}{}

	if ollama.Message.Content != "" {
		content = append(content, AnthropicTextBlock{Type: "text", Text: ollama.Message.Content})
	}

	for _, tc := range ollama.Message.ToolCalls {
		content = append(content, AnthropicToolUseBlock{
			Type:  "tool_use",
			ID:    fmt.Sprintf("toolu_%s", tc.Function.Name),
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}

	stopReason := "end_turn"
	switch ollama.DoneReason {
	case "length":
		stopReason = "max_tokens"
	case "tool_call":
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
```

- [ ] **Step 2: Verify it compiles**

```bash
cd "C:\Users\Kavin\.claude\ollama-proxy"
go build ./...
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\proxy.go"
git commit -m "feat(ollama-proxy): add core request/response translation"
```

---

### Task 3: Streaming SSE Translation

**Files:**
- Create: `C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\streaming.go`

- [ ] **Step 1: Write streaming.go with Ollama NDJSON to Anthropic SSE translation**

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"bytes"
	"context"
)

func (p *Proxy) handleStreaming(w http.ResponseWriter, r *http.Request, ollamaReq *OllamaChatRequest, anthroReq *AnthropicRequest) {
	body, err := json.Marshal(ollamaReq)
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to marshal Ollama request")
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.upstreamURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", "Failed to create upstream request")
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("Upstream request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		writeAnthropicError(w, resp.StatusCode, "api_error", string(respBody))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	msgID := fmt.Sprintf("msg_%s", anthroReq.Model)
	contentBlockIndex := 0
	toolCallIndex := 0
	totalInputTokens := 0
	totalOutputTokens := 0
	thinkingBlockOpen := false
	textBlockOpen := false
	toolUseBlockOpen := false

	writeSSE(w, flusher, canFlush, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":       msgID,
			"type":     "message",
			"role":     "assistant",
			"model":    anthroReq.Model,
			"content":  []interface{}{},
			"stop_reason": nil,
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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
			totalInputTokens = chunk.PromptEvalCount
		}
		if chunk.EvalCount > 0 {
			totalOutputTokens += chunk.EvalCount
		}

		if chunk.Message.Thinking != "" {
			if !thinkingBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]interface{}{
						"type": "thinking",
						"thinking": "",
					},
				})
				thinkingBlockOpen = true
			}
			writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": contentBlockIndex,
				"delta": map[string]interface{}{
					"type":     "thinking_delta",
					"thinking": chunk.Message.Thinking,
				},
			})
		}

		if chunk.Message.Thinking == "" && thinkingBlockOpen {
			writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
				"type":  "content_block_stop",
				"index": contentBlockIndex,
			})
			contentBlockIndex++
			thinkingBlockOpen = false
		}

		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
				if !toolUseBlockOpen {
					writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
						"type":  "content_block_start",
						"index": contentBlockIndex,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    fmt.Sprintf("toolu_%s", tc.Function.Name),
							"name":  tc.Function.Name,
							"input": map[string]interface{}{},
						},
					})
					toolUseBlockOpen = true
				}
				if tc.Function.Arguments != nil {
					argsJSON, _ := json.Marshal(tc.Function.Arguments)
					writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": contentBlockIndex,
						"delta": map[string]interface{}{
							"type":          "input_json_delta",
							"partial_json":  string(argsJSON),
						},
					})
				}
			}
		}

		if chunk.Message.Content != "" {
			if !textBlockOpen && !thinkingBlockOpen && !toolUseBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				})
				textBlockOpen = true
			}
			if textBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": contentBlockIndex,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": chunk.Message.Content,
					},
				})
			}
		}

		if chunk.Done {
			if textBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex,
				})
				contentBlockIndex++
				textBlockOpen = false
			}
			if toolUseBlockOpen {
				writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex,
				})
				contentBlockIndex++
				toolUseBlockOpen = false
			}

			stopReason := "end_turn"
			switch chunk.DoneReason {
			case "length":
				stopReason = "max_tokens"
			case "tool_call":
				stopReason = "tool_use"
			}

			writeSSE(w, flusher, canFlush, "message_delta", map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]interface{}{
					"output_tokens": totalOutputTokens,
				},
			})

			writeSSE(w, flusher, canFlush, "message_stop", map[string]interface{}{
				"type": "message_stop",
			})
		}
	}

	if thinkingBlockOpen && !textBlockOpen {
		writeSSE(w, flusher, canFlush, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": contentBlockIndex,
		})
	}
}

func writeSSE(w io.Writer, flusher http.Flusher, canFlush bool, event string, data interface{}) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, dataJSON)
	if canFlush {
		flusher.Flush()
	}
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd "C:\Users\Kavin\.claude\ollama-proxy"
go build ./...
```

Expected: no errors (note: unused imports `_ "context"` and `_ "bytes"` may need removal — fix if compiler flags)

- [ ] **Step 3: Commit**

```bash
git add "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\streaming.go"
git commit -m "feat(ollama-proxy): add SSE streaming translation"
```

---

### Task 4: Server Entry Point and Configuration

**Files:**
- Create: `C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\main.go`

- [ ] **Step 1: Write main.go with server setup, config, and signal handling**

```go
package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	apiKey := os.Getenv("OLLAMA_API_KEY")
	if apiKey == "" {
		log.Fatal("OLLAMA_API_KEY environment variable is required")
	}

	port := os.Getenv("OLLAMA_PROXY_PORT")
	if port == "" {
		port = "11434"
	}

	host := os.Getenv("OLLAMA_PROXY_HOST")
	if host == "" {
		host = "127.0.0.1"
	}

	upstreamURL := os.Getenv("OLLAMA_UPSTREAM_URL")
	if upstreamURL == "" {
		upstreamURL = "https://ollama.com"
	}

	proxy := NewProxy(upstreamURL, apiKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", proxy.HandleMessages)
	mux.HandleFunc("/v1/messages/count_tokens", handleCountTokens)
	mux.HandleFunc("/health", handleHealth)

	addr := host + ":" + port

	log.Printf("ollama-proxy starting on %s → %s", addr, upstreamURL)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	log.Printf("ollama-proxy listening on http://%s", addr)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	server.Close()
}

func handleCountTokens(w http.ResponseWriter, r *http.Request) {
	writeAnthropicError(w, 404, "not_found_error", "Token counting is not supported")
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd "C:\Users\Kavin\.claude\ollama-proxy"
go build ./...
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\main.go"
git commit -m "feat(ollama-proxy): add server entry point with config and signal handling"
```

---

### Task 5: Build Binary and Verify Runtime

**Files:**
- Build output: `C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\ollama-proxy.exe`

- [ ] **Step 1: Build the Windows binary**

```bash
cd "C:\Users\Kavin\.claude\ollama-proxy"
go build -o ollama-proxy.exe .
```

Expected: `ollama-proxy.exe` created, ~5-8MB

- [ ] **Step 2: Test health endpoint without API key**

Start the proxy in background:
```bash
$env:OLLAMA_API_KEY = "test-key"
Start-Process -FilePath ".\ollama-proxy.exe" -NoNewWindow
Start-Sleep -Seconds 1
```

Test health:
```bash
Invoke-RestMethod -Uri "http://127.0.0.1:11434/health"
```
Expected: `{"status":"ok"}`

- [ ] **Step 3: Test count_tokens returns 404**

```bash
try {
    Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/messages/count_tokens" -Method POST
} catch {
    $_.Exception.Response.StatusCode
}
```
Expected: 404

- [ ] **Step 4: Stop the proxy**

```bash
Stop-Process -Name "ollama-proxy" -ErrorAction SilentlyContinue
```

- [ ] **Step 5: Commit**

```bash
git add "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\ollama-proxy.exe"
git commit -m "feat(ollama-proxy): build Windows binary"
```

---

### Task 6: Test with Ollama Cloud API (Real Request)

**Files:** None (testing only)

- [ ] **Step 1: Start proxy with real API key**

```bash
$env:OLLAMA_API_KEY = "c2993f29ad184bd9a92b262d531813c1.RXnkbBhpTACGFMkJD61W_alc"
Start-Process -FilePath "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\ollama-proxy.exe" -NoNewWindow
Start-Sleep -Seconds 1
```

- [ ] **Step 2: Test non-streaming request**

```bash
$body = @{
    model = "glm-5.1:cloud"
    max_tokens = 50
    messages = @(@{ role = "user"; content = "Say hello in one word" })
} | ConvertTo-Json

Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/messages" -Method POST -ContentType "application/json" -Body $body
```

Expected: Anthropic-format JSON response with `type: "message"`, content containing text, `stop_reason: "end_turn"`

- [ ] **Step 3: Test streaming request**

```bash
$body = @{
    model = "glm-5.1:cloud"
    max_tokens = 50
    stream = $true
    messages = @(@{ role = "user"; content = "Count to 3" })
} | ConvertTo-Json

Invoke-WebRequest -Uri "http://127.0.0.1:11434/v1/messages" -Method POST -ContentType "application/json" -Body $body
```

Expected: SSE stream with `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop` events

- [ ] **Step 4: Stop the proxy**

```bash
Stop-Process -Name "ollama-proxy" -ErrorAction SilentlyContinue
```

---

### Task 7: Configure Claude Desktop and Auto-Start

**Files:**
- Modify: `C:\Users\Kavin\.claude\settings.json`
- Modify: `C:\Users\Kavin\AppData\Local\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude-3p\configLibrary\a8494411-6831-45fb-b975-04160105234a.json`
- Create: `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\ollama-proxy.bat`

- [ ] **Step 1: Update Claude Code settings.json to point to proxy**

Change `ANTHROPIC_BASE_URL` from `https://ollama.com` to `http://127.0.0.1:11434` in `C:\Users\Kavin\.claude\settings.json`

- [ ] **Step 2: Update Claude Desktop configLibrary to point to proxy**

Change `inferenceGatewayBaseUrl` from `http://127.0.0.1:4000` to `http://127.0.0.1:11434` in `C:\Users\Kavin\AppData\Local\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude-3p\configLibrary\a8494411-6831-45fb-b975-04160105234a.json`

- [ ] **Step 3: Create auto-start batch file**

Write to `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\ollama-proxy.bat`:

```bat
@echo off
set OLLAMA_API_KEY=c2993f29ad184bd9a92b262d531813c1.RXnkbBhpTACGFMkJD61W_alc
start /B "" "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\ollama-proxy.exe" >> "%APPDATA%\ollama-proxy\proxy.log" 2>&1
```

- [ ] **Step 4: Create log directory**

```bash
mkdir "$env:APPDATA\ollama-proxy" -ErrorAction SilentlyContinue
```

- [ ] **Step 5: Test auto-start by running the bat file**

```bash
cmd /c "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup\ollama-proxy.bat"
Start-Sleep -Seconds 1
Invoke-RestMethod -Uri "http://127.0.0.1:11434/health"
```

Expected: `{"status":"ok"}`

- [ ] **Step 6: Verify Claude Desktop works**

1. Stop LiteLLM if running
2. Start `ollama-proxy.exe` (via bat or manually)
3. Open Claude Desktop
4. Select a model from the dropdown
5. Send a message and verify response comes through

- [ ] **Step 7: Commit config changes**

```bash
git add "C:\Users\Kavin\Documents\Personal Projects\VT\vt-landing\docs\superpowers\specs\2026-04-28-ollama-proxy-design.md"
git commit -m "feat(ollama-proxy): configure Claude Desktop and auto-start"
```

---

### Task 8: Update Design Spec with Final Details

**Files:**
- Modify: `C:\Users\Kavin\Documents\Personal Projects\VT\vt-landing\docs\superpowers\specs\2026-04-28-ollama-proxy-design.md`

- [ ] **Step 1: Add Model Selection section to spec**

Add after the Configuration section heading, documenting the `inferenceModels` mechanism from `configLibrary` and how to add/switch models via Claude Desktop UI.

- [ ] **Step 2: Add Auto-Start section to spec**

Document the Windows Startup folder auto-start mechanism with the `.bat` file.

- [ ] **Step 3: Update File Structure in spec**

Change paths from `~/.claude/ollama-proxy/` to Windows paths `C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\` and add `ollama-proxy.exe` to file list.

- [ ] **Step 4: Update Claude Desktop Settings section**

Change `inferenceGatewayBaseUrl` to point to `http://127.0.0.1:11434` (port 11434 instead of 4000).

- [ ] **Step 5: Commit**

```bash
git add "C:\Users\Kavin\Documents\Personal Projects\VT\vt-landing\docs\superpowers\specs\2026-04-28-ollama-proxy-design.md"
git commit -m "docs(ollama-proxy): update spec with model selection and auto-start"
```
