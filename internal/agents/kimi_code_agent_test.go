package agents

import (
	"strings"
	"testing"

	"ollama-proxy/internal/config"
)

func TestBuildKimiCodeProviderSection(t *testing.T) {
	out := buildKimiCodeProviderSection(8765, true)
	if !strings.Contains(out, "[providers.prism]\n") {
		t.Errorf("missing base provider header:\n%s", out)
	}
	if !strings.Contains(out, "[providers.prism-responses]\n") {
		t.Errorf("missing responses provider header:\n%s", out)
	}
	// Regression: a stray escaped quote produced [providers.prism-responses"],
	// which is invalid TOML and broke the whole Kimi config.
	if strings.Contains(out, "\"]") {
		t.Errorf("generated TOML contains a stray quote:\n%s", out)
	}

	out = buildKimiCodeProviderSection(8765, false)
	if !strings.Contains(out, "[providers.prism]\n") {
		t.Errorf("missing base provider header:\n%s", out)
	}
	if strings.Contains(out, "prism-responses") {
		t.Errorf("responses provider written without responses models:\n%s", out)
	}
}

func TestBuildKimiCodeModelSections_ProviderRouting(t *testing.T) {
	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud", API: "chat_completions"},
		{ID: "muse-spark", Provider: "ollama_cloud", API: "responses"},
	}}

	out := buildKimiCodeModelSections(remap, cfg)

	if !strings.Contains(out, "[models.\"prism-ollama_cloud-glm-5-2-cloud\"]") &&
		!strings.Contains(out, "[models.\"prism-") {
		t.Fatalf("missing model section:\n%s", out)
	}

	// Each model section must reference the provider matching its API.
	sections := strings.Split(out, "[models.\"")
	for _, sec := range sections {
		if strings.Contains(sec, "muse-spark") {
			if !strings.Contains(sec, `provider = "prism-responses"`) {
				t.Errorf("responses model not routed to prism-responses:\n%s", sec)
			}
		}
		if strings.Contains(sec, "glm-5.2:cloud") {
			if !strings.Contains(sec, `provider = "prism"`) {
				t.Errorf("chat model not routed to prism:\n%s", sec)
			}
		}
	}
}