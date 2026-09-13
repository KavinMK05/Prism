package agents

import (
	"os"
	"path/filepath"
	"testing"

	"ollama-proxy/internal/config"
)

// ompModelByID finds a model entry in a buildOmpModelEntries result by id.
func ompModelByID(t *testing.T, models []interface{}, id string) map[string]interface{} {
	t.Helper()
	for _, raw := range models {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("model entry is %T, want map", raw)
		}
		if entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("model %q missing from entries", id)
	return nil
}

func TestBuildOmpModelEntriesVisionCompat(t *testing.T) {
	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	vision := &config.ModelCapabilities{Vision: true, ToolCalling: true}
	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "vision-model", Provider: "ollama_cloud", ContextLength: 200000, MaxOutputTokens: 32000, Capabilities: vision},
		{ID: "text-model", Provider: "ollama_cloud"},
		{ID: "codex-vision", Provider: "codex_test1", Capabilities: vision},
	}}

	chatModels := buildOmpModelEntries(remap, cfg, false)
	responsesModels := buildOmpModelEntries(remap, cfg, true)

	// Vision model on the chat-completions provider advertises image input and
	// opts out of OMP's catalog stripImageInput rule (Prism is a proxy).
	entry := ompModelByID(t, chatModels, "ollama_cloud/vision-model")
	in := stringSlice(entry["input"])
	if len(in) != 2 || in[0] != "text" || in[1] != "image" {
		t.Errorf("vision model input = %v, want [text image]", in)
	}
	compat, ok := entry["compat"].(map[string]interface{})
	if !ok {
		t.Fatal("vision model on chat provider must carry a compat block")
	}
	if compat["stripImageInput"] != false {
		t.Errorf("vision model compat.stripImageInput = %v, want false", compat["stripImageInput"])
	}

	// Text-only model keeps text input and gets no compat block.
	entry = ompModelByID(t, chatModels, "ollama_cloud/text-model")
	in = stringSlice(entry["input"])
	if len(in) != 1 || in[0] != "text" {
		t.Errorf("text model input = %v, want [text]", in)
	}
	if _, ok := entry["compat"]; ok {
		t.Error("text-only model must not carry a compat block")
	}

	// Responses models declare image input too, but the responses transport has
	// no vision guard, so no compat block is written for prism-responses.
	if len(responsesModels) != 1 {
		t.Fatalf("responsesModels = %d entries, want 1", len(responsesModels))
	}
	entry = ompModelByID(t, responsesModels, "codex_test1/codex-vision")
	in = stringSlice(entry["input"])
	if len(in) != 2 || in[0] != "text" || in[1] != "image" {
		t.Errorf("responses vision model input = %v, want [text image]", in)
	}
	if _, ok := entry["compat"]; ok {
		t.Error("responses model must not carry a compat block")
	}
}

func TestInstallOmpConfigWritesStripImageInputOptOut(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	vision := &config.ModelCapabilities{Vision: true}
	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "vision-model", Provider: "ollama_cloud", ContextLength: 200000, Capabilities: vision},
		{ID: "text-model", Provider: "ollama_cloud"},
	}}

	if err := InstallOmpConfig(11434, remap); err != nil {
		t.Fatalf("InstallOmpConfig: %v", err)
	}

	// Read the file back the way OMP resolves it, so this covers the YAML
	// round-trip and not just the in-memory entry.
	path := filepath.Join(tmp, ".omp", "agent", "models.yml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("models.yml not written: %v", err)
	}
	m, err := readYAMLConfig(path)
	if err != nil {
		t.Fatalf("read back models.yml: %v", err)
	}
	providers, ok := m["providers"].(map[string]interface{})
	if !ok {
		t.Fatal("providers block missing from models.yml")
	}
	prism, ok := providers["prism"].(map[string]interface{})
	if !ok {
		t.Fatal("prism provider missing from models.yml")
	}
	models, ok := prism["models"].([]interface{})
	if !ok || len(models) != 2 {
		t.Fatalf("prism models = %v, want 2 entries", prism["models"])
	}
	byID := map[string]map[string]interface{}{}
	for _, raw := range models {
		entry := raw.(map[string]interface{})
		byID[entry["id"].(string)] = entry
	}

	visionEntry, ok := byID["ollama_cloud/vision-model"]
	if !ok {
		t.Fatal("vision model missing after round-trip")
	}
	compat, ok := visionEntry["compat"].(map[string]interface{})
	if !ok {
		t.Fatal("vision model lost its compat block in the YAML round-trip")
	}
	if compat["stripImageInput"] != false {
		t.Errorf("persisted compat.stripImageInput = %v, want false", compat["stripImageInput"])
	}

	textEntry, ok := byID["ollama_cloud/text-model"]
	if !ok {
		t.Fatal("text model missing after round-trip")
	}
	if _, ok := textEntry["compat"]; ok {
		t.Error("text-only model must not persist a compat block")
	}
}
