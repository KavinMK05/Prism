package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/search"
)

const liveModel = "deepseek-v4.1-flash:cloud"

// liveOllamaCloudRouter builds a router pinned to the real Ollama Cloud
// provider using the API key from the user's Prism config, and records every
// upstream URL it touches. Temporary probe (not committed): it talks to the
// live API and only runs with PRISM_LIVE_PROBE=1.
func liveOllamaCloudRouter(t *testing.T) (*ProviderRouter, *[]string) {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no config dir: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "prism", "config.json"))
	if err != nil {
		t.Skipf("no prism config: %v", err)
	}
	var cfgOnDisk struct {
		OllamaCloud *config.ProviderConfig `json:"ollama_cloud"`
	}
	if err := json.Unmarshal(raw, &cfgOnDisk); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfgOnDisk.OllamaCloud == nil || cfgOnDisk.OllamaCloud.APIKey == "" {
		t.Skip("Ollama Cloud API key not configured")
	}
	base := cfgOnDisk.OllamaCloud.BaseURL
	if base == "" {
		base = "https://ollama.com"
	}
	cfg := &config.Config{
		DefaultProvider: "ollama_cloud",
		OllamaCloud: &config.ProviderConfig{
			ID: "ollama_cloud", Name: "Ollama Cloud", BaseURL: base, APIKey: cfgOnDisk.OllamaCloud.APIKey,
		},
	}
	remap := &config.ModelRemapping{
		DefaultModel: liveModel,
		Aliases:      map[string]string{},
		KnownModels:  []config.ModelEntry{{ID: liveModel, Provider: "ollama_cloud"}},
	}
	pr := NewRouter(cfg, remap)
	seen := &[]string{}
	pr.client = &http.Client{Timeout: 5 * time.Minute, Transport: &recordingTransport{seen: seen}}
	return pr, seen
}

type recordingTransport struct {
	mu   sync.Mutex
	seen *[]string
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	*t.seen = append(*t.seen, req.URL.String())
	t.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func postMessages(t *testing.T, pr *ProviderRouter, body string) *httptest.ResponseRecorder {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()
	pr.HandleMessages(rr, httpReq)
	return rr
}

func TestLiveProbeOllamaCloudChatCompletions(t *testing.T) {
	if os.Getenv("PRISM_LIVE_PROBE") == "" {
		t.Skip("set PRISM_LIVE_PROBE=1 to run")
	}
	pr, seen := liveOllamaCloudRouter(t)

	t.Run("nonstreaming", func(t *testing.T) {
		rr := postMessages(t, pr, `{"model":"`+liveModel+`","max_tokens":256,"messages":[{"role":"user","content":"Reply with exactly one word: pong"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(strings.ToLower(rr.Body.String()), "pong") {
			t.Errorf("unexpected body: %s", rr.Body.String())
		}
		t.Logf("upstream URLs: %v", *seen)
		t.Logf("body: %.400s", rr.Body.String())
	})

	t.Run("streaming", func(t *testing.T) {
		rr := postMessages(t, pr, `{"model":"`+liveModel+`","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Count from 1 to 3."}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		for _, want := range []string{"event: message_start", "text_delta", "event: message_stop"} {
			if !strings.Contains(body, want) {
				t.Errorf("stream missing %q\n%.800s", want, body)
			}
		}
	})

	t.Run("patternA_web_search", func(t *testing.T) {
		search.RegisterProviderForTest("searxng", func(_ *search.ProviderConfig, c *http.Client) search.SearchProvider {
			return &fakeStaticProvider{results: []search.SearchResult{
				{Title: "Go Downloads", URL: "https://go.dev/dl/", Snippet: "Latest stable Go release."},
			}}
		})
		search.Global.Reload(&search.Config{
			Active: "searxng", MaxPerTurn: 2, DefaultNumResults: 3,
			Providers: map[string]*search.ProviderConfig{"searxng": {Enabled: true}},
		})
		before := len(*seen)
		rr := postMessages(t, pr, `{
			"model":"`+liveModel+`",
			"max_tokens":512,
			"stream":true,
			"messages":[{"role":"user","content":"Use the web_search tool to look up the latest stable Go release, then tell me the version. You must call web_search first."}],
			"tools":[{"type":"web_search_20250305","name":"web_search"}]
		}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		calls := (*seen)[before:]
		t.Logf("search-loop upstream calls: %v", calls)
		searched := strings.Contains(body, "server_tool_use")
		t.Logf("searched=%v", searched)
		if !strings.Contains(body, "event: message_stop") {
			t.Errorf("stream missing message_stop\n%.800s", body)
		}
		if searched {
			for _, want := range []string{"web_search_tool_result", "go.dev"} {
				if !strings.Contains(body, want) {
					t.Errorf("stream missing %q", want)
				}
			}
		} else {
			t.Logf("model did not call web_search on this run; body: %.600s", body)
		}
	})
}

// TestLiveProbeOllamaCloudInboundSurfaces checks the two other client-facing
// protocols against the live Chat Completions upstream.
func TestLiveProbeOllamaCloudInboundSurfaces(t *testing.T) {
	if os.Getenv("PRISM_LIVE_PROBE") == "" {
		t.Skip("set PRISM_LIVE_PROBE=1 to run")
	}
	pr, seen := liveOllamaCloudRouter(t)

	t.Run("openai_chat_completions", func(t *testing.T) {
		body := `{"model":"` + liveModel + `","max_tokens":64,"messages":[{"role":"user","content":"Reply with exactly one word: pong"}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		rr := httptest.NewRecorder()
		pr.HandleOpenAIChatCompletions(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(strings.ToLower(rr.Body.String()), "pong") {
			t.Errorf("unexpected body: %s", rr.Body.String())
		}
		t.Logf("body: %.400s", rr.Body.String())
	})

	t.Run("responses", func(t *testing.T) {
		body := `{"model":"` + liveModel + `","max_output_tokens":64,"input":"Reply with exactly one word: pong"}`
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		rr := httptest.NewRecorder()
		pr.HandleResponsesAPI(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(strings.ToLower(rr.Body.String()), "pong") {
			t.Errorf("unexpected body: %s", rr.Body.String())
		}
		t.Logf("body: %.400s", rr.Body.String())
	})

	t.Logf("upstream URLs: %v", *seen)
}

// TestLiveProbeOllamaCloudResponsesReasoning pins the regression this thread is
// about: Codex (/v1/responses, streaming) must surface Ollama Cloud Chat
// Completions reasoning. Ollama reports it in the `reasoning` delta field, not
// `reasoning_content`, so this fails if only the latter is read.
func TestLiveProbeOllamaCloudResponsesReasoning(t *testing.T) {
	if os.Getenv("PRISM_LIVE_PROBE") == "" {
		t.Skip("set PRISM_LIVE_PROBE=1 to run")
	}
	pr, _ := liveOllamaCloudRouter(t)

	body := `{"model":"` + liveModel + `","max_output_tokens":2048,"stream":true,` +
		`"reasoning":{"effort":"low"},"input":"Think step by step, then answer: what is 17*23?"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	pr.HandleResponsesAPI(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %.600s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	reasoningDeltas := strings.Count(stream, "response.reasoning_summary_text.delta")
	reasoningDone := strings.Contains(stream, "response.reasoning_summary_text.done")
	t.Logf("reasoning_summary_text.delta events: %d (done event present: %v)", reasoningDeltas, reasoningDone)
	if reasoningDeltas == 0 {
		t.Errorf("no reasoning deltas in the /v1/responses stream: Ollama Cloud's `reasoning` field is being dropped\n%.1200s", stream)
	}
	if !reasoningDone {
		t.Errorf("reasoning stream was not closed with response.reasoning_summary_text.done")
	}
	// The reasoning item must also land in response.completed's output so Codex
	// can replay it.
	if !strings.Contains(stream, `"type":"reasoning"`) {
		t.Errorf("no reasoning output item in response.completed\n%.1200s", stream)
	}
}
