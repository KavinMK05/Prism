package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ollama-proxy/internal/config"
)

// decodeEmpryoConfig parses ~/.empryo/config.json into a generic map.
func decodeEmpryoConfig(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Empryo config: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse Empryo config: %v", err)
	}
	return m
}

// empryoProviderIn returns Prism's provider entry from a decoded config, or nil.
func empryoProviderIn(m map[string]interface{}) map[string]interface{} {
	arr, _ := m["providers"].([]interface{})
	for _, item := range arr {
		mp, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := mp["id"].(string); id == empryoProviderID {
			return mp
		}
	}
	return nil
}

// hideRealEmpryo minimizes PATH so an Empryo install on the dev machine cannot
// interfere with binary-detection tests.
func hideRealEmpryo(t *testing.T) {
	t.Helper()
	origPATH := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPATH) })
	if err := os.Setenv("PATH", "/usr/bin:/bin"); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
}

func TestBuildEmpryoModels(t *testing.T) {
	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "vision-model", Provider: "ollama_cloud", ContextLength: 200000, MaxOutputTokens: 32000},
		{ID: "text-model", Provider: "ollama_cloud"},
		{ID: "codex-model", Provider: "codex-1", API: "responses"},
	}}
	cfg.OAuthAccounts = []*config.OAuthAccount{{ID: "codex-1", Provider: "codex"}}

	models := buildEmpryoModels(remap, cfg)
	if len(models) != len(remap.KnownModels) {
		t.Fatalf("expected %d models, got %d", len(remap.KnownModels), len(models))
	}

	// Every model uses its provider-qualified route key as the id.
	want := map[string]bool{
		"ollama_cloud/vision-model": false,
		"ollama_cloud/text-model":   false,
		"codex-1/codex-model":       false,
	}
	for _, m := range models {
		if _, ok := want[m.ID]; !ok {
			t.Errorf("unexpected model id %q", m.ID)
			continue
		}
		want[m.ID] = true
		if !strings.HasPrefix(m.Name, prismManagedTag) {
			t.Errorf("%s name = %q, want the %q tag", m.ID, m.Name, prismManagedTag)
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("model %q missing", id)
		}
	}

	// Declared limits are kept; undeclared ones fall back to Empryo's defaults
	// so its context meter and compaction are not stuck at a wrong window.
	byID := map[string]empryoModel{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if m := byID["ollama_cloud/vision-model"]; m.ContextWindow != 200000 || m.MaxOutputTokens != 32000 {
		t.Errorf("vision-model limits = %d/%d, want 200000/32000", m.ContextWindow, m.MaxOutputTokens)
	}
	if m := byID["ollama_cloud/text-model"]; m.ContextWindow != defaultEmpryoContextWindow || m.MaxOutputTokens != defaultEmpryoMaxOutput {
		t.Errorf("text-model limits = %d/%d, want %d/%d",
			m.ContextWindow, m.MaxOutputTokens, defaultEmpryoContextWindow, defaultEmpryoMaxOutput)
	}

	// Responses/Codex models are listed too: Prism translates Chat Completions
	// to the Responses API for them, which is what lets one custom provider
	// reach every configured model.
	if m := byID["codex-1/codex-model"]; m.ID == "" {
		t.Error("codex-model should be listed for Empryo")
	}
}

func TestInstallEmpryoConfigLifecycle(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud", ContextLength: 131072},
		{ID: "second-model", Provider: "ollama_cloud"},
	}}

	cfgPath := empryoConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing user config: another provider, an MCP server, and unrelated
	// settings that must all survive.
	userConfig := `{
  "theme": "dark",
  "providers": [
    { "id": "together", "baseURL": "https://api.together.xyz/v1", "envVar": "TOGETHER_API_KEY" }
  ],
  "mcpServers": [
    { "name": "github", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] }
  ]
}`
	if err := os.WriteFile(cfgPath, []byte(userConfig), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InstallEmpryoConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	m := decodeEmpryoConfig(t, cfgPath)

	if m["theme"] != "dark" {
		t.Error("unrelated theme key lost after install")
	}
	if _, err := os.Stat(cfgPath + ".prism-backup"); err != nil {
		t.Errorf("expected a one-time .prism-backup: %v", err)
	}

	prism := empryoProviderIn(m)
	if prism == nil {
		t.Fatal("prism provider missing after install")
	}
	if prism["baseURL"] != "http://127.0.0.1:11434/v1" {
		t.Errorf("baseURL = %v, want http://127.0.0.1:11434/v1", prism["baseURL"])
	}
	if prism["envVar"] != empryoKeyEnvVar {
		t.Errorf("envVar = %v, want %s", prism["envVar"], empryoKeyEnvVar)
	}
	if prism["name"] != empryoProviderName {
		t.Errorf("name = %v, want %s", prism["name"], empryoProviderName)
	}
	reasoning, _ := prism["reasoning"].(map[string]interface{})
	if auto, _ := reasoning["auto"].(bool); !auto {
		t.Errorf("reasoning = %v, want auto: true", prism["reasoning"])
	}

	arr, _ := m["providers"].([]interface{})
	if len(arr) != 2 {
		t.Fatalf("expected 2 providers (user + prism), got %d", len(arr))
	}
	if id := empryoProviderEntryID(arr[0]); id != "together" {
		t.Errorf("user provider order changed: first entry is %q", id)
	}

	models, _ := prism["models"].([]interface{})
	if len(models) != 2 {
		t.Fatalf("expected 2 models in the prism provider, got %d", len(models))
	}
	first, _ := models[0].(map[string]interface{})
	if first["id"] != "ollama_cloud/test-model" {
		t.Errorf("model id = %v, want ollama_cloud/test-model", first["id"])
	}
	if first["contextWindow"] != float64(131072) {
		t.Errorf("contextWindow = %v, want 131072", first["contextWindow"])
	}

	// MCP servers are untouched by the provider writer.
	servers, _ := m["mcpServers"].([]interface{})
	if len(servers) != 1 {
		t.Errorf("expected the user's MCP server preserved, got %d entries", len(servers))
	}

	// Re-install is idempotent: still one prism entry, no duplicates.
	if err := InstallEmpryoConfig(11434, remap); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	m = decodeEmpryoConfig(t, cfgPath)
	arr, _ = m["providers"].([]interface{})
	if len(arr) != 2 {
		t.Fatalf("re-install duplicated providers: got %d", len(arr))
	}

	// Restore removes only Prism's entry.
	if err := RestoreEmpryoConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	m = decodeEmpryoConfig(t, cfgPath)
	if p := empryoProviderIn(m); p != nil {
		t.Error("prism provider still present after restore")
	}
	arr, _ = m["providers"].([]interface{})
	if len(arr) != 1 || empryoProviderEntryID(arr[0]) != "together" {
		t.Errorf("user provider lost after restore: %v", m["providers"])
	}
	if m["theme"] != "dark" {
		t.Error("unrelated key lost after restore")
	}
}

// TestRestoreEmpryoConfigDropsEmptyProvidersList covers the common case: Prism
// created the providers list, so restoring should leave the file as it was
// rather than an empty array.
func TestRestoreEmpryoConfigDropsEmptyProvidersList(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallEmpryoConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	cfgPath := empryoConfigPath()
	if _, ok := decodeEmpryoConfig(t, cfgPath)["providers"]; !ok {
		t.Fatal("install should have created the providers list")
	}

	if err := RestoreEmpryoConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	m := decodeEmpryoConfig(t, cfgPath)
	if _, ok := m["providers"]; ok {
		t.Errorf("empty providers list should be dropped, got %v", m["providers"])
	}
}

func TestInstallEmpryoConfigRejectsNonListProviders(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	cfgPath := empryoConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	// A hand-edited config with the wrong shape must not be clobbered.
	if err := os.WriteFile(cfgPath, []byte(`{"providers":{"together":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallEmpryoConfig(11434, remap); err == nil {
		t.Fatal("expected an error for a non-list providers value")
	}
	m := decodeEmpryoConfig(t, cfgPath)
	if _, ok := m["providers"].(map[string]interface{}); !ok {
		t.Errorf("existing providers content was rewritten: %v", m["providers"])
	}
}

func TestInstallEmpryoConfigNoModelsError(t *testing.T) {
	setTestHomeAndConfigDir(t)
	err := InstallEmpryoConfig(11434, &config.ModelRemapping{KnownModels: []config.ModelEntry{}})
	if err == nil {
		t.Fatal("expected error when no models configured")
	}
}

func TestIsEmpryoInstalled(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	hideRealEmpryo(t)
	if isEmpryoInstalled() {
		t.Fatal("isEmpryoInstalled = true with no config file and no binary")
	}
	if _, err := os.Stat(empryoConfigPath()); !os.IsNotExist(err) {
		t.Error("detection must not create a placeholder config")
	}

	// The binary alone is enough: Empryo writes its config on first run.
	binDir := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "empryo")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isEmpryoInstalled() {
		t.Fatal("isEmpryoInstalled = false with the binary on the install path")
	}
	if _, err := os.Stat(empryoConfigPath()); !os.IsNotExist(err) {
		t.Error("binary detection must not create a placeholder config")
	}

	// A config file alone is enough too (binary gone from this PATH).
	_ = os.Remove(binPath)
	cfgDir := filepath.Dir(empryoConfigPath())
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if isEmpryoInstalled() {
		t.Fatal("an empty ~/.empryo directory alone should not count as installed")
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if !isEmpryoInstalled() {
		t.Fatal("isEmpryoInstalled = false with ~/.empryo/config.json present")
	}

	// The desktop app writes sessions and logs into ~/.empryo without ever
	// putting a binary on PATH, so a populated config directory counts too.
	if err := os.Remove(filepath.Join(cfgDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	if !isEmpryoInstalled() {
		t.Fatal("isEmpryoInstalled = false with a populated ~/.empryo directory")
	}
}

func TestIsAgentActiveEmpryo(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	if IsAgentActive("empryo") {
		t.Fatal("IsAgentActive = true before setup")
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallEmpryoConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !IsAgentActive("empryo") {
		t.Fatal("IsAgentActive = false after install")
	}
	if !isEmpryoActive() {
		t.Fatal("isEmpryoActive = false after install")
	}

	if err := RestoreEmpryoConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if IsAgentActive("empryo") {
		t.Fatal("IsAgentActive = true after restore")
	}
	if _, err := os.Stat(empryoConfigPath()); err != nil {
		t.Errorf("config file should still exist after restore: %v", err)
	}
}

func TestEmpryoMCPLifecycleArrayShape(t *testing.T) {
	setTestHomeAndConfigDir(t)
	cfgPath := empryoConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Empryo's documented shape: an array of named servers.
	user := `{"mcpServers":[{"name":"github","command":"npx"}]}`
	if err := os.WriteFile(cfgPath, []byte(user), 0600); err != nil {
		t.Fatal(err)
	}

	if !AgentMCPSupported("empryo") {
		t.Fatal("Empryo should be reported as MCP-capable")
	}
	if err := InstallAgentMCPConfig("empryo", 11434); err != nil {
		t.Fatalf("install MCP: %v", err)
	}
	if !AgentMCPActive("empryo") {
		t.Fatal("AgentMCPActive = false after MCP install")
	}
	if got := AgentMCPConfigPath("empryo"); got != cfgPath {
		t.Errorf("AgentMCPConfigPath = %q, want %q", got, cfgPath)
	}

	m := decodeEmpryoConfig(t, cfgPath)
	servers, _ := m["mcpServers"].([]interface{})
	if len(servers) != 2 {
		t.Fatalf("expected 2 MCP servers after install, got %d", len(servers))
	}
	if name := servers[0].(map[string]interface{})["name"]; name != "github" {
		t.Errorf("user MCP server order changed: first is %v", name)
	}
	prism := servers[1].(map[string]interface{})
	if prism["url"] != "http://127.0.0.1:11434/mcp/empryo" {
		t.Errorf("MCP url = %v", prism["url"])
	}
	if prism["transport"] != "http" {
		t.Errorf("MCP transport = %v, want http", prism["transport"])
	}
	headers, _ := prism["headers"].(map[string]interface{})
	if headers["Authorization"] != prismMCPAuthorization {
		t.Errorf("MCP Authorization = %v, want %s", headers["Authorization"], prismMCPAuthorization)
	}

	// Re-install replaces rather than appends.
	if err := InstallAgentMCPConfig("empryo", 11434); err != nil {
		t.Fatalf("re-install MCP: %v", err)
	}
	m = decodeEmpryoConfig(t, cfgPath)
	servers, _ = m["mcpServers"].([]interface{})
	if len(servers) != 2 {
		t.Fatalf("re-install duplicated MCP entries: got %d", len(servers))
	}

	if err := RestoreAgentMCPConfig("empryo"); err != nil {
		t.Fatalf("restore MCP: %v", err)
	}
	if AgentMCPActive("empryo") {
		t.Fatal("AgentMCPActive = true after MCP restore")
	}
	m = decodeEmpryoConfig(t, cfgPath)
	servers, _ = m["mcpServers"].([]interface{})
	if len(servers) != 1 {
		t.Fatalf("user MCP server lost on restore: %v", m["mcpServers"])
	}
	if name := servers[0].(map[string]interface{})["name"]; name != "github" {
		t.Errorf("wrong MCP server survived restore: %v", name)
	}
}

func TestEmpryoMCPLifecycleObjectShape(t *testing.T) {
	setTestHomeAndConfigDir(t)
	cfgPath := empryoConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Empryo also accepts mcpServers keyed by name; that shape must be kept.
	user := `{"mcpServers":{"github":{"command":"npx"}}}`
	if err := os.WriteFile(cfgPath, []byte(user), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InstallAgentMCPConfig("empryo", 11434); err != nil {
		t.Fatalf("install MCP: %v", err)
	}
	m := decodeEmpryoConfig(t, cfgPath)
	servers, ok := m["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("object-shaped mcpServers was rewritten: %v", m["mcpServers"])
	}
	if _, ok := servers["github"]; !ok {
		t.Error("user MCP server lost after install")
	}
	if _, ok := servers[prismMCPEntryName]; !ok {
		t.Error("prism MCP entry missing after install")
	}
	if !AgentMCPActive("empryo") {
		t.Fatal("AgentMCPActive = false after MCP install")
	}

	if err := RestoreAgentMCPConfig("empryo"); err != nil {
		t.Fatalf("restore MCP: %v", err)
	}
	m = decodeEmpryoConfig(t, cfgPath)
	servers, _ = m["mcpServers"].(map[string]interface{})
	if len(servers) != 1 {
		t.Fatalf("expected only the user's server after restore, got %v", servers)
	}
	if _, ok := servers["github"]; !ok {
		t.Error("user MCP server lost on restore")
	}
}

// TestEmpryoMCPCreatesAndPrunesServersList covers a fresh config: Prism creates
// the array, and restoring drops it again instead of leaving an empty list.
func TestEmpryoMCPCreatesAndPrunesServersList(t *testing.T) {
	setTestHomeAndConfigDir(t)

	if err := InstallAgentMCPConfig("empryo", 8765); err != nil {
		t.Fatalf("install MCP into a missing config: %v", err)
	}
	cfgPath := empryoConfigPath()
	m := decodeEmpryoConfig(t, cfgPath)
	servers, ok := m["mcpServers"].([]interface{})
	if !ok || len(servers) != 1 {
		t.Fatalf("expected one MCP server in a new list, got %v", m["mcpServers"])
	}
	if prism := servers[0].(map[string]interface{}); prism["url"] != "http://127.0.0.1:8765/mcp/empryo" {
		t.Errorf("MCP url = %v", prism["url"])
	}

	if err := RestoreAgentMCPConfig("empryo"); err != nil {
		t.Fatalf("restore MCP: %v", err)
	}
	m = decodeEmpryoConfig(t, cfgPath)
	if _, ok := m["mcpServers"]; ok {
		t.Errorf("empty mcpServers list should be dropped, got %v", m["mcpServers"])
	}
}

func TestEmpryoMCPRejectsNonListServers(t *testing.T) {
	setTestHomeAndConfigDir(t)
	cfgPath := empryoConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"mcpServers":"nope"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallAgentMCPConfig("empryo", 11434); err == nil {
		t.Fatal("expected an error for a non-list mcpServers value")
	}
	m := decodeEmpryoConfig(t, cfgPath)
	if m["mcpServers"] != "nope" {
		t.Errorf("existing value was rewritten: %v", m["mcpServers"])
	}
	// Restore on a shape Prism cannot own is a no-op, not an error.
	if err := RestoreAgentMCPConfig("empryo"); err != nil {
		t.Fatalf("restore on a foreign shape: %v", err)
	}
}

// TestEmpryoConfigPathPerPlatform pins the platform split that made a Windows
// install invisible in Empryo's picker: the global config is
// %LOCALAPPDATA%\Empryo\config.json on Windows, NOT ~/.empryo/config.json (which
// Empryo only ever reads as a per-project config there).
func TestEmpryoConfigPathPerPlatform(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	local := filepath.Join(tmp, "localappdata")
	t.Setenv("LOCALAPPDATA", local)

	got := empryoConfigPath()
	if runtime.GOOS == "windows" {
		if want := filepath.Join(local, "Empryo", "config.json"); got != want {
			t.Errorf("empryoConfigPath = %q, want %q", got, want)
		}
	} else {
		if want := filepath.Join(tmp, ".empryo", "config.json"); got != want {
			t.Errorf("empryoConfigPath = %q, want %q", got, want)
		}
	}
	// The status check and the installer must agree on the file.
	if agentConfigPath("empryo") != got {
		t.Errorf("agentConfigPath(empryo) = %q, want empryoConfigPath() = %q", agentConfigPath("empryo"), got)
	}

	// LOCALAPPDATA is always set on a real Windows session; when it is missing,
	// fall back to the standard profile layout rather than ~/.empryo.
	t.Setenv("LOCALAPPDATA", "")
	got = empryoConfigPath()
	if runtime.GOOS == "windows" {
		if want := filepath.Join(tmp, "AppData", "Local", "Empryo", "config.json"); got != want {
			t.Errorf("empryoConfigPath without LOCALAPPDATA = %q, want %q", got, want)
		}
	}
}

// TestEmpryoAgentWiring asserts the shared agent plumbing knows about Empryo.
func TestEmpryoAgentWiring(t *testing.T) {
	if !IsSupportedAgent("empryo") {
		t.Error("empryo missing from supportedAgents")
	}
	if AgentDisplayName("empryo") != "Empryo" {
		t.Errorf("AgentDisplayName = %q", AgentDisplayName("empryo"))
	}
	found := false
	for _, id := range AllAgentIDs() {
		if id == "empryo" {
			found = true
		}
	}
	if !found {
		t.Error("empryo missing from AllAgentIDs")
	}
}

// empryoMCPDocumentedFields is the field set Empryo's MCP docs define for one
// server entry. Prism's entry must stay inside it: Empryo ignores unknown keys
// silently, so a typo (transportt, header, urls) would leave a server that looks
// configured in Prism and never connects in Empryo.
var empryoMCPDocumentedFields = map[string]bool{
	"name": true, "transport": true, "command": true, "args": true,
	"env": true, "cwd": true, "url": true, "headers": true,
	"timeout": true, "disabled": true, "resident": true, "redirect": true,
}

// assertEmpryoMCPEntry checks one entry against Empryo's documented schema.
func assertEmpryoMCPEntry(t *testing.T, entry map[string]interface{}, port int) {
	t.Helper()
	for key := range entry {
		if !empryoMCPDocumentedFields[key] {
			t.Errorf("entry has key %q, which Empryo's MCP schema does not define", key)
		}
	}
	if entry["name"] != prismMCPEntryName {
		t.Errorf("name = %v, want %q", entry["name"], prismMCPEntryName)
	}
	if entry["transport"] != "http" {
		t.Errorf("transport = %v, want http", entry["transport"])
	}
	if want := AgentMCPURL("empryo", port); entry["url"] != want {
		t.Errorf("url = %v, want %v", entry["url"], want)
	}
	headers, _ := entry["headers"].(map[string]interface{})
	if headers["Authorization"] != prismMCPAuthorization {
		t.Errorf("Authorization = %v, want %s", headers["Authorization"], prismMCPAuthorization)
	}
}

// TestEmpryoMCPEntryMatchesDocumentedSchema pins the entry Prism writes against
// Empryo's published MCP schema, in both shapes Empryo accepts.
func TestEmpryoMCPEntryMatchesDocumentedSchema(t *testing.T) {
	const port = 11434

	t.Run("array shape", func(t *testing.T) {
		setTestHomeAndConfigDir(t)
		if err := InstallAgentMCPConfig("empryo", port); err != nil {
			t.Fatalf("install MCP: %v", err)
		}
		m := decodeEmpryoConfig(t, empryoConfigPath())
		servers, ok := m["mcpServers"].([]interface{})
		if !ok || len(servers) != 1 {
			t.Fatalf("mcpServers = %v, want one entry", m["mcpServers"])
		}
		assertEmpryoMCPEntry(t, servers[0].(map[string]interface{}), port)
	})

	t.Run("object shape", func(t *testing.T) {
		setTestHomeAndConfigDir(t)
		path := empryoConfigPath()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"mcpServers":{"github":{"command":"npx"}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := InstallAgentMCPConfig("empryo", port); err != nil {
			t.Fatalf("install MCP: %v", err)
		}
		m := decodeEmpryoConfig(t, path)
		servers, ok := m["mcpServers"].(map[string]interface{})
		if !ok {
			t.Fatalf("object-shaped mcpServers was rewritten: %v", m["mcpServers"])
		}
		entry, ok := servers[prismMCPEntryName].(map[string]interface{})
		if !ok {
			t.Fatalf("prism entry missing: %v", servers)
		}
		assertEmpryoMCPEntry(t, entry, port)
	})
}
