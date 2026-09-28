package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestLiveProbeOllamaCloudModelInfo is a temporary probe (not committed) that
// checks the Ollama-owned discovery path against the real cloud.
func TestLiveProbeOllamaCloudModelInfo(t *testing.T) {
	if os.Getenv("PRISM_LIVE_PROBE") == "" {
		t.Skip("set PRISM_LIVE_PROBE=1 to run")
	}
	for _, id := range []string{"deepseek-v4.1-flash", "gpt-oss:20b", "nemotron-3-ultra"} {
		res, err := FetchModelInfo(id, "ollama_cloud")
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if res == nil {
			t.Logf("%s -> not found", id)
			continue
		}
		t.Logf("%s -> name=%q ctx=%v out=%v reasoning=%v efforts=%v tools=%v vision=%v struct=%v provider=%q",
			id, res.Name, res.ContextLength, res.MaxOutputTokens, res.Reasoning,
			res.ReasoningEffort, res.ToolCall, res.Vision, res.StructuredOutput, res.ProviderID)
	}
}

// TestLiveProbeOllamaCloudModelSearch checks the admin search handler reads the
// live /api/tags catalog rather than models.dev.
func TestLiveProbeOllamaCloudModelSearch(t *testing.T) {
	if os.Getenv("PRISM_LIVE_PROBE") == "" {
		t.Skip("set PRISM_LIVE_PROBE=1 to run")
	}
	rr := httptest.NewRecorder()
	handleModelSearch(rr, httptest.NewRequest(http.MethodGet, "/admin/model-search?provider=ollama_cloud&q=deepseek", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	t.Logf("search results: %.500s", rr.Body.String())

	rr2 := httptest.NewRecorder()
	handleModelSearch(rr2, httptest.NewRequest(http.MethodGet, "/admin/model-search?provider=ollama_cloud&q=", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr2.Code, rr2.Body.String())
	}
	var all []map[string]string
	if err := json.Unmarshal(rr2.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	t.Logf("full catalog entries: %d", len(all))
	if len(all) == 0 {
		t.Error("expected the live Ollama Cloud catalog")
	}
}
