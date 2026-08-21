package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ollama-proxy/internal/config"
)

func TestStripJSONC(t *testing.T) {
	in := `{
  // line comment
  "language_models": { /* block
comment */ },
  "url": "http://example.com//not-a-comment",
  "trailing": [1, 2, 3,],
}`
	got := stripJSONC(in)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("stripped output is not valid JSON: %v\n%s", err, got)
	}
	lm, ok := m["language_models"].(map[string]interface{})
	if !ok || len(lm) != 0 {
		t.Errorf("language_models should be an empty object, got %v", m["language_models"])
	}
	if url, _ := m["url"].(string); url != "http://example.com//not-a-comment" {
		t.Errorf("string literal with // was corrupted: %q", url)
	}
	if arr, _ := m["trailing"].([]interface{}); len(arr) != 3 {
		t.Errorf("trailing comma array should have 3 items, got %v", m["trailing"])
	}
}

func TestBuildZedModels(t *testing.T) {
	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.OAuthAccounts = []*config.OAuthAccount{{ID: "codex-1", Provider: "codex"}}
	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "vision-model", Provider: "ollama_cloud", ContextLength: 200000, MaxOutputTokens: 32000, Capabilities: &config.ModelCapabilities{Vision: true}},
		{ID: "reasoning-model", Provider: "ollama_cloud", Reasoning: true, ReasoningEffort: []string{"low", "high"}},
		{ID: "codex-model", Provider: "codex-1"},
	}}

	models := buildZedModels(remap, cfg)
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

	byName := map[string]map[string]interface{}{}
	for _, item := range models {
		e := item.(map[string]interface{})
		byName[e["name"].(string)] = e
	}

	vision := byName[prismModelRouteKey(remap.KnownModels[0])]
	caps := vision["capabilities"].(map[string]interface{})
	if caps["images"] != true {
		t.Error("vision model should advertise images capability")
	}
	if caps["chat_completions"] != true {
		t.Error("non-codex model should use chat completions")
	}
	if vision["max_tokens"] != 200000 || vision["max_output_tokens"] != 32000 {
		t.Errorf("vision model limits wrong: %v", vision)
	}

	reasoning := byName[prismModelRouteKey(remap.KnownModels[1])]
	if reasoning["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", reasoning["reasoning_effort"])
	}

	codex := byName[prismModelRouteKey(remap.KnownModels[2])]
	ccaps := codex["capabilities"].(map[string]interface{})
	if ccaps["chat_completions"] != false {
		t.Error("codex model should set chat_completions=false so Zed uses the Responses API")
	}
}

func TestInstallZedConfigLifecycle(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud","oauth_accounts":[{"id":"codex-1","provider":"codex"}]}`)

	// Pre-existing user settings in JSONC form: comments, a custom provider,
	// and a top-level key Prism must preserve.
	settingsPath := filepath.Join(tmp, "Zed", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Fatal(err)
	}
	original := "{\n // my theme\n \"theme\": \"One Dark\",\n \"language_models\": {\n \"openai_compatible\": {\n \"other\": {\"api_url\": \"https://example.com/v1\",},},},}"
	if err := os.WriteFile(settingsPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "text-model", Provider: "ollama_cloud", ContextLength: 128000},
		{ID: "codex-model", Provider: "codex-1"},
	}}
	if err := InstallZedConfig(11434, remap); err != nil {
		t.Fatalf("InstallZedConfig: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("rewritten settings are not valid JSON: %v", err)
	}

	// User keys preserved.
	if m["theme"] != "One Dark" {
		t.Errorf("user theme key lost: %v", m["theme"])
	}
	lm := m["language_models"].(map[string]interface{})
	compat := lm["openai_compatible"].(map[string]interface{})
	if _, ok := compat["other"]; !ok {
		t.Error("existing openai_compatible provider 'other' was removed")
	}

	// Prism block present with expected shape.
	prism, ok := compat[zedProviderID].(map[string]interface{})
	if !ok {
		t.Fatalf("prism provider block missing")
	}
	if prism["api_url"] != "http://127.0.0.1:11434/v1" {
		t.Errorf("api_url = %v", prism["api_url"])
	}
	models := prism["available_models"].([]interface{})
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	// Backup of the original JSONC exists.
	if _, err := os.Stat(settingsPath + ".prism-backup"); err != nil {
		t.Error("one-time backup missing after install")
	}

	// Active detection works.
	if !IsAgentActive("zed") {
		t.Error("IsAgentActive(zed) = false after install")
	}

	// Restore removes only the prism block.
	if err := RestoreZedConfig(); err != nil {
		t.Fatalf("RestoreZedConfig: %v", err)
	}
	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("restored settings are not valid JSON: %v", err)
	}
	if m["theme"] != "One Dark" {
		t.Error("user theme key lost on restore")
	}
	lm = m["language_models"].(map[string]interface{})
	compat = lm["openai_compatible"].(map[string]interface{})
	if _, ok := compat[zedProviderID]; ok {
		t.Error("prism block still present after restore")
	}
	if _, ok := compat["other"]; !ok {
		t.Error("existing provider 'other' lost on restore")
	}
	if IsAgentActive("zed") {
		t.Error("IsAgentActive(zed) = true after restore")
	}
}
