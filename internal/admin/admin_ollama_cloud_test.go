package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/config"
)

func mustRaw(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

// Fixture literals captured from https://ollama.com/api/show on 2026-09-22.
func TestOllamaContextLength(t *testing.T) {
	cases := []struct {
		name string
		info *ollamaShowInfo
		want int
	}{
		{"nil", nil, 0},
		{
			name: "arch-scoped key",
			info: &ollamaShowInfo{ModelInfo: map[string]json.RawMessage{
				"gptoss.context_length": mustRaw(t, 131072),
			}},
			want: 131072,
		},
		{
			// nemotron-3-ultra has an empty architecture, so the server emits a
			// degenerate ".context_length" key.
			name: "empty architecture key",
			info: &ollamaShowInfo{ModelInfo: map[string]json.RawMessage{
				".context_length":         mustRaw(t, 262144),
				"general.architecture":    mustRaw(t, ""),
				"general.parameter_count": mustRaw(t, 550000000000),
			}},
			want: 262144,
		},
		{
			name: "ignores unrelated suffixes",
			info: &ollamaShowInfo{ModelInfo: map[string]json.RawMessage{
				"general.context_length_note": mustRaw(t, 5),
			}},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ollamaContextLength(tc.info); got != tc.want {
				t.Fatalf("ollamaContextLength() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOllamaReasoningEfforts(t *testing.T) {
	info := &ollamaShowInfo{
		Capabilities: []string{"completion", "thinking", "tools", "vision"},
		Thinking: &ollamaThinking{
			Default: mustRaw(t, "high"),
			Values:  []json.RawMessage{mustRaw(t, false), mustRaw(t, "low"), mustRaw(t, "high"), mustRaw(t, "max")},
		},
	}
	got := ollamaReasoningEfforts(info)
	want := []string{"low", "high", "max"}
	if len(got) != len(want) {
		t.Fatalf("ollamaReasoningEfforts() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ollamaReasoningEfforts() = %v, want %v", got, want)
		}
	}
	// A boolean-only model toggles thinking but exposes no graded levels.
	toggleOnly := &ollamaShowInfo{Thinking: &ollamaThinking{Values: []json.RawMessage{mustRaw(t, false), mustRaw(t, true)}}}
	if got := ollamaReasoningEfforts(toggleOnly); len(got) != 0 {
		t.Fatalf("boolean-only thinking values should yield no graded efforts, got %v", got)
	}
	if !ollamaReasoning(toggleOnly) {
		t.Fatal("a model publishing thinking values should count as reasoning")
	}
}

// TestOllamaCloudDiscoveryServersTheServerEndpoints drives the discovery
// against a stub of GET /api/tags + POST /api/show. The models.dev merge is
// exercised separately by TestMergeOllamaShowResult so this test stays
// hermetic.
func TestOllamaCloudDiscoveryServersTheServerEndpoints(t *testing.T) {
	var showCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"models": []map[string]interface{}{
					{"name": "deepseek-v4.1-flash", "model": "deepseek-v4.1-flash", "digest": "abc"},
					{"name": "gpt-oss:20b", "model": "gpt-oss:20b", "digest": "def"},
				},
			})
		case "/api/show":
			showCalls++
			var body struct {
				Model string `json:"model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Model != "deepseek-v4.1-flash" {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"capabilities": []string{"completion", "thinking", "tools", "vision"},
				"thinking":     map[string]interface{}{"default": "high", "values": []interface{}{false, "low", "high", "max"}},
				"model_info":   map[string]interface{}{"deepseek_v41.context_length": 1048576},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	config.Publish(&config.Config{OllamaCloud: &config.ProviderConfig{ID: "ollama_cloud", BaseURL: srv.URL, APIKey: "test-key"}})
	t.Cleanup(func() { config.Publish(nil) })

	all, err := fetchOllamaCloudSearch("")
	if err != nil {
		t.Fatalf("fetchOllamaCloudSearch: %v", err)
	}
	if len(all) != 2 || all[0]["id"] != "deepseek-v4.1-flash" || all[1]["id"] != "gpt-oss:20b" {
		t.Fatalf("search('') = %v, want both cloud models sorted by id", all)
	}
	filtered, err := fetchOllamaCloudSearch("GPT-OSS")
	if err != nil {
		t.Fatalf("fetchOllamaCloudSearch: %v", err)
	}
	if len(filtered) != 1 || filtered[0]["id"] != "gpt-oss:20b" {
		t.Fatalf("search('GPT-OSS') = %v, want the single gpt-oss:20b entry", filtered)
	}

	// fetchOllamaCloudModelInfo still resolves the models.dev half over the
	// network, so only the server-derived fields are asserted here.
	info, err := fetchOllamaCloudModelInfo("deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("fetchOllamaCloudModelInfo: %v", err)
	}
	if info.ContextLength != 1048576 {
		t.Errorf("ContextLength = %d, want 1048576 from /api/show", info.ContextLength)
	}
	if !info.Reasoning || !info.ToolCall || !info.Vision {
		t.Errorf("capabilities not mapped from /api/show: %+v", info)
	}
	if strings.Join(info.ReasoningEffort, ",") != "low,high,max" {
		t.Errorf("ReasoningEffort = %v, want low,high,max from thinking.values", info.ReasoningEffort)
	}

	// /api/show responses are cached, so a second lookup must not re-request.
	if _, err := fetchOllamaCloudModelInfo("deepseek-v4.1-flash"); err != nil {
		t.Fatalf("second fetchOllamaCloudModelInfo: %v", err)
	}
	if showCalls != 1 {
		t.Errorf("/api/show called %d times, want 1 (cached)", showCalls)
	}
}

// TestMergeOllamaShowResult pins the precedence rule: Ollama's own endpoint
// wins for context length, models.dev only fills what the server omits.
func TestMergeOllamaShowResult(t *testing.T) {
	info := &ollamaShowInfo{
		Capabilities: []string{"completion", "tools"},
		ModelInfo:    map[string]json.RawMessage{"gptoss.context_length": mustRaw(t, 131072)},
	}
	dev := &modelsDevResult{
		ID:               "gpt-oss:20b",
		Name:             "gpt-oss:20b",
		ProviderID:       "ollama-cloud",
		ContextLength:    999999, // must lose to the server value
		MaxOutputTokens:  32768,
		Reasoning:        true, // server says no thinking capability
		StructuredOutput: true,
		ReasoningEffort:  []string{"low", "medium", "high"},
	}
	got := mergeOllamaShowResult("gpt-oss:20b", info, dev)
	if got.ContextLength != 131072 {
		t.Fatalf("ContextLength = %d, want the server's 131072", got.ContextLength)
	}
	if got.MaxOutputTokens != 32768 {
		t.Fatalf("MaxOutputTokens = %d, want models.dev's 32768 (server has no output limit)", got.MaxOutputTokens)
	}
	if got.Name != "gpt-oss:20b" || !got.StructuredOutput {
		t.Fatalf("models.dev display fields not merged: %+v", got)
	}
	if got.Reasoning {
		t.Fatal("server reported no thinking capability; reasoning must not be inferred from models.dev")
	}
	if !got.ToolCall {
		t.Fatal("tool_calling must come from the server's capabilities list")
	}

	// Without a models.dev entry the server data still stands alone.
	bare := mergeOllamaShowResult("mystery-model", info, nil)
	if bare.ContextLength != 131072 || bare.Name != "mystery-model" || bare.MaxOutputTokens != 0 {
		t.Fatalf("bare merge = %+v", bare)
	}
}

func TestMatchModelsDevExactModel(t *testing.T) {
	raw := map[string]json.RawMessage{}
	put := func(k string, v interface{}) { b, _ := json.Marshal(v); raw[k] = b }
	put("ollama-cloud", map[string]interface{}{
		"id":   "ollama-cloud",
		"name": "Ollama Cloud",
		"models": map[string]interface{}{
			"gpt-oss:20b": map[string]interface{}{
				"id": "gpt-oss:20b", "name": "GPT-OSS 20B",
				"limit":      map[string]interface{}{"context": 131072, "output": 32768},
				"tool_call":  true,
				"reasoning":  true,
				"modalities": map[string]interface{}{"input": []string{"text", "image"}},
			},
			"gpt-oss:120b": map[string]interface{}{
				"id": "gpt-oss:120b", "name": "GPT-OSS 120B",
				"limit": map[string]interface{}{"context": 131072, "output": 32768},
			},
		},
	})

	got := matchModelsDevExactModel(raw, "gpt-oss:20b", "ollama_cloud")
	if got == nil {
		t.Fatal("expected an exact match for gpt-oss:20b")
	}
	if got.ID != "gpt-oss:20b" || got.Name != "GPT-OSS 20B" {
		t.Fatalf("exact id match returned the sibling entry: %+v", got)
	}
	if !got.Vision {
		t.Fatal("vision should be derived from modalities.input containing image")
	}
	if matchModelsDevExactModel(raw, "gpt-oss:20b", "some_other_provider") != nil {
		t.Fatal("a provider that does not list the model must not match")
	}
}
