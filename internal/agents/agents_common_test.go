package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"ollama-proxy/internal/config"
)

// TestLookupBinaryFindsAgentOffPath simulates the macOS GUI-app scenario: the
// process PATH is minimal (a .app bundle launched from Finder inherits only
// /usr/bin:/bin), and the agent binary lives in ~/.bun/bin — a directory GUI
// apps don't inherit. lookupBinary must still find it via the curated install
// dirs so the agent isn't wrongly reported as "not installed".
func TestLookupBinaryFindsAgentOffPath(t *testing.T) {
	origPATH := os.Getenv("PATH")
	origHome := homeEnvValue()
	defer func() {
		os.Setenv("PATH", origPATH)
		setHomeEnv(origHome)
	}()

	tmpHome := t.TempDir()
	binDir := filepath.Join(tmpHome, ".bun", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binName := "fake-agent"
	binPath := filepath.Join(binDir, binName)
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	// Minimal PATH so exec.LookPath cannot find it via PATH.
	if err := os.Setenv("PATH", "/usr/bin:/bin"); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
	setHomeEnv(tmpHome)

	p, ok := lookupBinary(binName)
	if !ok || p == "" {
		t.Fatalf("lookupBinary(%q) not found under minimal PATH with ~/.bun/bin fallback", binName)
	}
	if p != binPath {
		t.Fatalf("lookupBinary(%q) = %q, want %q", binName, p, binPath)
	}
}

func homeEnvValue() string {
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	return os.Getenv("USERPROFILE")
}

func setHomeEnv(v string) {
	if runtime.GOOS == "windows" {
		os.Setenv("USERPROFILE", v)
	} else {
		os.Setenv("HOME", v)
	}
}

func TestInitializeAutoSyncMigratesActiveAgentsOnly(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	factoryDir := filepath.Join(tmp, ".factory")
	if err := os.MkdirAll(factoryDir, 0755); err != nil {
		t.Fatalf("mkdir factory config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(factoryDir, "settings.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write factory main config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(factoryDir, "settings.local.json"), []byte(`{"customModels":[{"displayName":"[Prism] Test"}]}`), 0600); err != nil {
		t.Fatalf("write factory config: %v", err)
	}

	if err := InitializeAutoSync(); err != nil {
		t.Fatalf("InitializeAutoSync: %v", err)
	}
	cfg := config.Load()
	if cfg.AgentIntegrations == nil || !cfg.AgentIntegrations.AutoSyncMigrated {
		t.Fatal("auto-sync migration marker was not persisted")
	}
	if !cfg.AgentIntegrations.AutoSync["factory-droid"] {
		t.Fatal("active Factory Droid integration should remain enabled")
	}
	if cfg.AgentIntegrations.AutoSync["opencode"] {
		t.Fatal("inactive OpenCode integration should remain disabled")
	}

	if err := SetAgentAutoSync("factory-droid", false); err != nil {
		t.Fatalf("disable Factory Droid: %v", err)
	}
	if err := InitializeAutoSync(); err != nil {
		t.Fatalf("second InitializeAutoSync: %v", err)
	}
	cfg = config.Load()
	if cfg.AgentIntegrations.AutoSync["factory-droid"] {
		t.Fatal("second migration must not overwrite the user's disabled choice")
	}
}

func TestSetAgentAutoSyncPersistsAndClones(t *testing.T) {
	setTestHomeAndConfigDir(t)
	cfg := config.Load()
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() { config.SetCurrent(nil) })

	if err := SetAgentAutoSync("codex", true); err != nil {
		t.Fatalf("enable Codex: %v", err)
	}
	persisted := config.Load()
	if !persisted.AgentIntegrations.AutoSync["codex"] {
		t.Fatal("Codex auto-sync preference was not persisted")
	}
	if current := config.Current(); current == nil || !current.AgentIntegrations.AutoSync["codex"] {
		t.Fatal("live config was not updated")
	}

	data, err := json.Marshal(persisted.AgentIntegrations)
	if err != nil {
		t.Fatalf("marshal agent config: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("agent integration config unexpectedly serialized empty")
	}
}

func TestSyncAgentsSkipsDisabledFactoryDroid(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	factoryDir := filepath.Join(tmp, ".factory")
	if err := os.MkdirAll(factoryDir, 0755); err != nil {
		t.Fatalf("mkdir factory config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(factoryDir, "settings.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write factory config: %v", err)
	}
	writePrismConfig(t, `{
		"agent_integrations": {
			"auto_sync": {"factory-droid": false},
			"auto_sync_migrated": true
		}
	}`)
	if err := config.SaveModelRemapping(&config.ModelRemapping{
		DefaultModel: "test-model",
		KnownModels:  []config.ModelEntry{{ID: "test-model", Provider: "ollama_cloud"}},
		Aliases:      map[string]string{},
	}); err != nil {
		t.Fatalf("save model remapping: %v", err)
	}

	SyncAgents(11434)

	localPath := filepath.Join(factoryDir, "settings.local.json")
	if data, err := os.ReadFile(localPath); err == nil && string(data) != "" {
		if hasPrismModelsFromJSON(data) {
			t.Fatal("disabled Factory Droid integration was resynchronized")
		}
	}
}

func hasPrismModelsFromJSON(data []byte) bool {
	var cfg map[string]interface{}
	if json.Unmarshal(data, &cfg) != nil {
		return false
	}
	models, _ := cfg["customModels"].([]interface{})
	return hasPrismModels(models)
}

func TestAgentIDRegistryIncludesCodexAndAllSupportedAgents(t *testing.T) {
	ids := allAgentIDs
	if len(ids) != len(supportedAgents)+1 || ids[0] != "codex" {
		t.Fatalf("agent registry = %v, want codex plus supported agents", ids)
	}
	for _, id := range ids {
		if !isKnownAgentID(id) {
			t.Errorf("agent registry id %q is not recognized", id)
		}
	}
}
