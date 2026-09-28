package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/config"
)

// ollamaCloudTestRouter wires a router whose only provider is Ollama Cloud,
// pointed at a stub upstream. The config is the real one: GetProviderByID decides
// which wire protocol Ollama Cloud speaks, so these tests fail if that provider
// ever regresses to the native /api/chat surface.
func ollamaCloudTestRouter(t *testing.T, upstreamURL string) *ProviderRouter {
	t.Helper()
	cfg := &config.Config{
		DefaultProvider: "ollama_cloud",
		OllamaCloud: &config.ProviderConfig{
			ID:      "ollama_cloud",
			Name:    "Ollama Cloud",
			BaseURL: upstreamURL,
			APIKey:  "test-key",
		},
	}
	remap := &config.ModelRemapping{
		KnownModels: []config.ModelEntry{{
			ID:              "deepseek-v4.1-flash",
			Provider:        "ollama_cloud",
			API:             "chat_completions",
			Reasoning:       true,
			ReasoningEffort: []string{"low", "high", "max"},
			ContextLength:   1048576,
			MaxOutputTokens: 384000,
		}},
	}
	return NewRouter(cfg, remap)
}

func TestOllamaCloudProviderUsesChatCompletions(t *testing.T) {
	resolved := &config.ResolvedProvider{BaseURL: "https://ollama.com"}
	// The base URL has no /v1 suffix, so the OpenAI-compatible path must add it.
	if got, want := resolved.ChatCompletionsURL(), "https://ollama.com/v1/chat/completions"; got != want {
		t.Fatalf("ChatCompletionsURL() = %q, want %q", got, want)
	}

	cfg := &config.Config{OllamaCloud: &config.ProviderConfig{ID: "ollama_cloud", BaseURL: "https://ollama.com"}}
	info, err := cfg.GetProviderByID("ollama_cloud")
	if err != nil {
		t.Fatalf("GetProviderByID: %v", err)
	}
	if info.ProviderType != "openai" {
		t.Fatalf("ProviderType = %q, want \"openai\" (Ollama Cloud is reached over Chat Completions)", info.ProviderType)
	}
}

func TestOllamaCloudAnthropicInboundNonStreaming(t *testing.T) {
	var paths []string
	var sentBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sentBody, _ = io.ReadAll(r.Body)
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "chatcmpl-1",
			"object": "chat.completion",
			"model":  "deepseek-v4.1-flash",
			"choices": []map[string]interface{}{{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": "hello from chat completions"},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17},
		})
	}))
	defer upstream.Close()

	router := ollamaCloudTestRouter(t, upstream.URL)
	body := `{"model":"deepseek-v4.1-flash","max_tokens":64,` +
		`"thinking":{"type":"enabled","budget_tokens":20000},` +
		`"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(paths) != 1 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("upstream paths = %v, want exactly [/v1/chat/completions]", paths)
	}

	var sent struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
		Stream          bool   `json:"stream"`
	}
	if err := json.Unmarshal(sentBody, &sent); err != nil {
		t.Fatalf("upstream body not JSON: %v (%s)", err, sentBody)
	}
	if sent.Model != "deepseek-v4.1-flash" {
		t.Errorf("upstream model = %q", sent.Model)
	}
	if sent.Stream {
		t.Error("non-streaming request must not ask the upstream to stream")
	}
	// thinking.budget_tokens 20000 buckets to "high", and the model allows it.
	if sent.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want \"high\"", sent.ReasoningEffort)
	}

	var resp struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("client response not JSON: %v (%s)", err, w.Body.String())
	}
	if resp.Type != "message" || resp.Role != "assistant" {
		t.Fatalf("client response shape = %+v, want an Anthropic message", resp)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "hello from chat completions" {
		t.Fatalf("content = %+v", resp.Content)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want 12/5", resp.Usage)
	}
}

func TestOllamaCloudAnthropicInboundStreaming(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"hi "}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{"content":"there"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"deepseek-v4.1-flash","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		} {
			w.Write([]byte("data: " + chunk + "\n\n"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	router := ollamaCloudTestRouter(t, upstream.URL)
	body := `{"model":"deepseek-v4.1-flash","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.HandleMessages(w, req)

	if len(paths) != 1 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("upstream paths = %v, want exactly [/v1/chat/completions]", paths)
	}

	events := parseSSEEvents(w.Body.String())
	var text strings.Builder
	var sawMessageStop bool
	for _, e := range events {
		var payload map[string]interface{}
		if json.Unmarshal([]byte(e.Data), &payload) != nil {
			continue
		}
		switch payload["type"] {
		case "content_block_delta":
			if delta, ok := payload["delta"].(map[string]interface{}); ok {
				if t, ok := delta["text"].(string); ok {
					text.WriteString(t)
				}
			}
		case "message_stop":
			sawMessageStop = true
		}
	}
	if text.String() != "hi there" {
		t.Fatalf("streamed text = %q, want %q (events: %s)", text.String(), "hi there", w.Body.String())
	}
	if !sawMessageStop {
		t.Fatalf("stream never emitted message_stop: %s", w.Body.String())
	}
}
