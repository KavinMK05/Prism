package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ollama-proxy/internal/config"
)

// TestInstallPiConfigLeavesDefaultsAlone is the core guarantee for Pi: Prism
// registers its models in ~/.pi/agent/models.json and nothing else. The
// provider/model Pi starts with must stay whatever the user had.
func TestInstallPiConfigLeavesDefaultsAlone(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	piDir := filepath.Join(tmp, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0755); err != nil {
		t.Fatalf("mkdir pi config dir: %v", err)
	}
	settingsPath := filepath.Join(piDir, "settings.json")
	modelsPath := filepath.Join(piDir, "models.json")
	writePiTestFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"claude-sonnet-4-5","theme":"dark"}`)
	writePiTestFile(t, modelsPath, `{"providers":{"anthropic":{"apiKey":"user-key"}}}`)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud", ContextLength: 200000},
	}}
	if err := InstallPiConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}

	settings := readPiTestJSON(t, settingsPath)
	if settings["defaultProvider"] != "anthropic" {
		t.Errorf("defaultProvider = %v, want anthropic untouched", settings["defaultProvider"])
	}
	if settings["defaultModel"] != "claude-sonnet-4-5" {
		t.Errorf("defaultModel = %v, want claude-sonnet-4-5 untouched", settings["defaultModel"])
	}
	if settings["theme"] != "dark" {
		t.Errorf("unrelated setting lost: %v", settings["theme"])
	}

	models := readPiTestJSON(t, modelsPath)
	providers, _ := models["providers"].(map[string]interface{})
	if _, ok := providers["anthropic"]; !ok {
		t.Error("user provider lost from models.json")
	}
	prismProv, ok := providers["prism"].(map[string]interface{})
	if !ok {
		t.Fatal("prism provider missing from models.json")
	}
	entries, _ := prismProv["models"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("prism models = %d, want 1", len(entries))
	}
	entry, _ := entries[0].(map[string]interface{})
	if entry["id"] != "ollama_cloud/glm-5.2:cloud" {
		t.Errorf("prism model id = %v", entry["id"])
	}
}

// TestInstallPiConfigClearsLegacyPrismDefaultsOnce covers the migration: the
// defaultProvider/defaultModel older Prism versions wrote are removed, but only
// once. A model the user later selects in Pi must survive every re-sync.
func TestInstallPiConfigClearsLegacyPrismDefaultsOnce(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	piDir := filepath.Join(tmp, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0755); err != nil {
		t.Fatalf("mkdir pi config dir: %v", err)
	}
	settingsPath := filepath.Join(piDir, "settings.json")
	legacy := `{"defaultProvider":"prism","defaultModel":"ollama_cloud/glm-5.2:cloud","theme":"dark"}`
	writePiTestFile(t, settingsPath, legacy)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud", ContextLength: 200000},
	}}
	if err := InstallPiConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}

	settings := readPiTestJSON(t, settingsPath)
	if _, ok := settings["defaultProvider"]; ok {
		t.Errorf("legacy defaultProvider survived: %v", settings["defaultProvider"])
	}
	if _, ok := settings["defaultModel"]; ok {
		t.Errorf("legacy defaultModel survived: %v", settings["defaultModel"])
	}
	if settings["theme"] != "dark" {
		t.Errorf("unrelated setting lost: %v", settings["theme"])
	}
	if _, err := os.Stat(piDefaultsMigrationMarker()); err != nil {
		t.Errorf("migration marker not written: %v", err)
	}

	// The user now picks a Prism model in Pi itself. Prism must not clear it on
	// the next startup sync — the cleanup is one-time.
	chosen := `{"defaultProvider":"prism","defaultModel":"ollama_cloud/glm-5.2:cloud","theme":"dark"}`
	writePiTestFile(t, settingsPath, chosen)
	if err := InstallPiConfig(11434, remap); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	settings = readPiTestJSON(t, settingsPath)
	if settings["defaultProvider"] != "prism" || settings["defaultModel"] != "ollama_cloud/glm-5.2:cloud" {
		t.Errorf("user's own choice was cleared: provider=%v model=%v",
			settings["defaultProvider"], settings["defaultModel"])
	}
}

// TestRestorePiConfigClearsDanglingDefaults checks that restore removes the
// Prism providers and any default pointing at one of their models — including a
// bare route key such as "ollama_cloud/glm-5.2:cloud" that carries no "prism"
// prefix and used to be left behind as a dangling pointer.
func TestRestorePiConfigClearsDanglingDefaults(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)
	writePrismTestRemap(t, []config.ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud"},
	})

	piDir := filepath.Join(tmp, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0755); err != nil {
		t.Fatalf("mkdir pi config dir: %v", err)
	}
	settingsPath := filepath.Join(piDir, "settings.json")
	modelsPath := filepath.Join(piDir, "models.json")
	writePiTestFile(t, settingsPath, `{"defaultProvider":"prism","defaultModel":"ollama_cloud/glm-5.2:cloud","theme":"dark"}`)
	writePiTestFile(t, modelsPath, `{"providers":{"prism":{"api":"openai-completions"},"anthropic":{"apiKey":"user-key"}}}`)

	if err := RestorePiConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	settings := readPiTestJSON(t, settingsPath)
	if _, ok := settings["defaultProvider"]; ok {
		t.Errorf("defaultProvider left behind: %v", settings["defaultProvider"])
	}
	if _, ok := settings["defaultModel"]; ok {
		t.Errorf("defaultModel left behind: %v", settings["defaultModel"])
	}
	if settings["theme"] != "dark" {
		t.Errorf("unrelated setting lost: %v", settings["theme"])
	}

	models := readPiTestJSON(t, modelsPath)
	providers, _ := models["providers"].(map[string]interface{})
	if _, ok := providers["prism"]; ok {
		t.Error("prism provider still present after restore")
	}
	if _, ok := providers["anthropic"]; !ok {
		t.Error("user provider lost after restore")
	}
}

// TestPiSettingIsPrismOwned pins the predicate that decides whether a stored
// value is Prism's to clean up or the user's to keep.
func TestPiSettingIsPrismOwned(t *testing.T) {
	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud"},
	}}
	prismOwned := []string{
		"prism",
		"prism-responses",
		"prism-codex",
		"prism/glm-5.2:cloud",
		"prism-responses/gpt-5",
		"ollama_cloud/glm-5.2:cloud",
		"glm-5.2:cloud",
	}
	for _, v := range prismOwned {
		if !piSettingIsPrismOwned(v, remap) {
			t.Errorf("piSettingIsPrismOwned(%q) = false, want true", v)
		}
	}
	userOwned := []string{"", "anthropic", "claude-sonnet-4-5", "openai/gpt-5.1"}
	for _, v := range userOwned {
		if piSettingIsPrismOwned(v, remap) {
			t.Errorf("piSettingIsPrismOwned(%q) = true, want false", v)
		}
	}
}

func writePiTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readPiTestJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// writePrismTestRemap writes model_remapping.json into Prism's config dir for
// every platform-specific path so config.LoadModelRemapping() is predictable.
func writePrismTestRemap(t *testing.T, known []config.ModelEntry) {
	t.Helper()
	payload, err := json.Marshal(&config.ModelRemapping{KnownModels: known, Aliases: map[string]string{}})
	if err != nil {
		t.Fatalf("marshal remap: %v", err)
	}
	home := os.Getenv("HOME")
	for _, dir := range []string{
		filepath.Join(os.Getenv("APPDATA"), "prism"),                   // windows
		filepath.Join(home, "Library", "Application Support", "prism"), // darwin
		filepath.Join(home, ".config", "prism"),                        // linux
	} {
		if dir == filepath.Join("", "prism") || dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "model_remapping.json"), payload, 0600); err != nil {
			t.Fatalf("write remap in %s: %v", dir, err)
		}
	}
}
