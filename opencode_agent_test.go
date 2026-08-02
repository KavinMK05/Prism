package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// decodeOpenCodeConfig parses an opencode.json into a generic map for assertions.
func decodeOpenCodeConfig(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse opencode config: %v", err)
	}
	return m
}

// stringSlice converts a decoded JSON array ([]interface{}) or in-memory
// []string to []string.
func stringSlice(v interface{}) []string {
	switch arr := v.(type) {
	case []string:
		return arr
	case []interface{}:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func opencodeModel(t *testing.T, cfg map[string]interface{}, providerID, modelID string) map[string]interface{} {
	t.Helper()
	providers, _ := cfg["provider"].(map[string]interface{})
	prov, ok := providers[providerID].(map[string]interface{})
	if !ok {
		t.Fatalf("provider %q missing from config", providerID)
	}
	models, _ := prov["models"].(map[string]interface{})
	entry, ok := models[modelID].(map[string]interface{})
	if !ok {
		t.Fatalf("model %q missing from provider %q", modelID, providerID)
	}
	return entry
}

func TestBuildOpencodeModelEntries(t *testing.T) {
	cfg := &Config{DefaultProvider: "ollama_cloud"}
	vision := &ModelCapabilities{Vision: true, ToolCalling: true}
	remap := &ModelRemapping{KnownModels: []ModelEntry{
		{ID: "vision-model", Provider: "ollama_cloud", ContextLength: 200000, MaxOutputTokens: 32000, Capabilities: vision},
		{ID: "text-model", Provider: "ollama_cloud", ContextLength: 0, MaxOutputTokens: 0},
		{ID: "reasoning-default", Provider: "ollama_cloud", Reasoning: true},
		{ID: "reasoning-max", Provider: "ollama_cloud", Reasoning: true, ReasoningEffort: []string{"low", "high", "max"}},
		{ID: "codex-model", Provider: "codex-1"},
	}}
	cfg.OAuthAccounts = []*OAuthAccount{{ID: "codex-1", Provider: "codex"}}

	nonCodex := buildOpencodeModelEntries(remap, cfg, false)
	codex := buildOpencodeModelEntries(remap, cfg, true)

	// Vision model advertises image input.
	entry := nonCodex["vision-model"].(map[string]interface{})
	mods := entry["modalities"].(map[string]interface{})
	in := stringSlice(mods["input"])
	if len(in) != 2 || in[0] != "text" || in[1] != "image" {
		t.Errorf("vision model modalities.input = %v, want [text image]", in)
	}

	// Text-only model advertises text input only.
	entry = nonCodex["text-model"].(map[string]interface{})
	mods = entry["modalities"].(map[string]interface{})
	in = stringSlice(mods["input"])
	if len(in) != 1 || in[0] != "text" {
		t.Errorf("text model modalities.input = %v, want [text]", in)
	}

	// Reasoning model without explicit efforts defaults to low/medium/high (no max).
	entry = nonCodex["reasoning-default"].(map[string]interface{})
	variants := entry["variants"].(map[string]interface{})
	for _, want := range []string{"low", "medium", "high"} {
		if _, ok := variants[want]; !ok {
			t.Errorf("reasoning-default missing variant %q", want)
		}
	}
	if _, ok := variants["max"]; ok {
		t.Error("reasoning-default should not get a max variant by default")
	}
	if _, ok := variants["off"]; ok {
		t.Error("reasoning-default should not get an off variant")
	}

	// Reasoning model listing max explicitly gets it.
	entry = nonCodex["reasoning-max"].(map[string]interface{})
	variants = entry["variants"].(map[string]interface{})
	if _, ok := variants["max"]; !ok {
		t.Error("reasoning-max missing max variant")
	}
	if _, ok := variants["medium"]; ok {
		t.Error("reasoning-max should only get listed variants, got medium")
	}

	// Non-reasoning model has no variants key.
	if _, ok := nonCodex["text-model"].(map[string]interface{})["variants"]; ok {
		t.Error("text-model should not have variants")
	}

	// Codex model goes to the codex-only block, and vice versa.
	if len(codex) != 1 {
		t.Fatalf("expected 1 codex model, got %d", len(codex))
	}
	if _, ok := codex["codex-model"]; !ok {
		t.Error("codex-model missing from codex block")
	}
	if _, ok := nonCodex["codex-model"]; ok {
		t.Error("codex-model leaked into non-codex block")
	}
}

func TestInstallOpencodeConfigLifecycle(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud","oauth_accounts":[{"id":"codex-1","provider":"codex"}]}`)

	remap := &ModelRemapping{KnownModels: []ModelEntry{
		{ID: "vision-model", Provider: "ollama_cloud", ContextLength: 200000, Reasoning: true, Capabilities: &ModelCapabilities{Vision: true}},
		{ID: "codex-model", Provider: "codex-1", ContextLength: 0},
	}}

	cfgPath := filepath.Join(tmp, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing user config with a non-Prism provider block and top-level keys.
	userConfig := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "google/gemini-2.5-flash",
  "plugin": ["@whisperopencode/push"],
  "provider": {
    "google": {
      "models": {
        "gemini-2.5-flash": {
          "limit": {"context": 1048576, "output": 65536},
          "name": "Gemini 2.5 Flash"
        }
      },
      "name": "Google"
    }
  }
}`
	if err := os.WriteFile(cfgPath, []byte(userConfig), 0644); err != nil {
		t.Fatal(err)
	}

	// Install.
	if err := installOpencodeConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	m := decodeOpenCodeConfig(t, cfgPath)

	// Non-Prism content preserved.
	if _, ok := m["plugin"]; !ok {
		t.Error("user plugin key lost after install")
	}
	providers, _ := m["provider"].(map[string]interface{})
	if _, ok := providers["google"]; !ok {
		t.Error("google provider lost after install")
	}

	// Prism block: options carry the client-name header.
	prism := opencodeModel(t, m, opencodeProviderID, "vision-model")
	prismProv := providers[opencodeProviderID].(map[string]interface{})
	opts := prismProv["options"].(map[string]interface{})
	headers, ok := opts["headers"].(map[string]interface{})
	if !ok {
		t.Fatal("prism options missing headers")
	}
	if headers["X-Client-Name"] != "OpenCode" {
		t.Errorf("prism X-Client-Name = %v, want OpenCode", headers["X-Client-Name"])
	}

	// Vision + reasoning parameters present.
	mods := prism["modalities"].(map[string]interface{})
	in := stringSlice(mods["input"])
	if len(in) != 2 || in[1] != "image" {
		t.Errorf("vision-model modalities.input = %v, want [text image]", in)
	}
	variants := prism["variants"].(map[string]interface{})
	if _, ok := variants["high"]; !ok {
		t.Error("vision-model missing reasoning variants")
	}

	// Codex block created with its own header, model routed correctly.
	codex := opencodeModel(t, m, opencodeProviderID+"-codex", "codex-model")
	if _, ok := codex["modalities"]; !ok {
		t.Error("codex-model missing modalities")
	}
	codexProv := providers[opencodeProviderID+"-codex"].(map[string]interface{})
	codexOpts := codexProv["options"].(map[string]interface{})
	codexHeaders := codexOpts["headers"].(map[string]interface{})
	if codexHeaders["X-Client-Name"] != "OpenCode" {
		t.Errorf("prism-codex X-Client-Name = %v, want OpenCode", codexHeaders["X-Client-Name"])
	}

	// Default model is left untouched: the user's choice survives install.
	if m["model"] != "google/gemini-2.5-flash" {
		t.Errorf("model = %v, want google/gemini-2.5-flash preserved", m["model"])
	}

	// Re-install (idempotency): still exactly one prism block, google intact.
	if err := installOpencodeConfig(11434, remap); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	m = decodeOpenCodeConfig(t, cfgPath)
	providers, _ = m["provider"].(map[string]interface{})
	if _, ok := providers["google"]; !ok {
		t.Error("google provider lost on re-install")
	}

	// Restore: prism blocks gone, default model cleared, google preserved.
	if err := restoreOpencodeConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	m = decodeOpenCodeConfig(t, cfgPath)
	providers, _ = m["provider"].(map[string]interface{})
	for _, id := range []string{opencodeProviderID, opencodeProviderID + "-codex"} {
		if _, ok := providers[id]; ok {
			t.Errorf("provider %q still present after restore", id)
		}
	}
	if m["model"] != "google/gemini-2.5-flash" {
		t.Error("model key not preserved after restore")
	}
	if _, ok := providers["google"]; !ok {
		t.Error("google provider lost after restore")
	}
}

func TestInstallOpencodeConfigNoModelsError(t *testing.T) {
	setTestHomeAndConfigDir(t)
	err := installOpencodeConfig(11434, &ModelRemapping{KnownModels: []ModelEntry{}})
	if err == nil {
		t.Fatal("expected error when no models configured")
	}
}
