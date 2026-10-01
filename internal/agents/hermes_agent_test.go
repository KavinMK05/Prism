package agents

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"ollama-proxy/internal/config"
)

// decodeHermesConfig parses Hermes's config.yaml into a generic map.
func decodeHermesConfig(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Hermes config: %v", err)
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse Hermes config: %v\n%s", err, data)
	}
	return m
}

// hermesProviderIn returns one of Prism's provider entries, or nil.
func hermesProviderIn(m map[string]interface{}, id string) map[string]interface{} {
	provs, _ := m["providers"].(map[string]interface{})
	if provs == nil {
		return nil
	}
	p, _ := provs[id].(map[string]interface{})
	return p
}

func TestHermesConfigPathPerPlatform(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)

	t.Setenv("HERMES_HOME", filepath.Join(tmp, "custom"))
	if got, want := hermesConfigPath(), filepath.Join(tmp, "custom", "config.yaml"); got != want {
		t.Errorf("hermesConfigPath with HERMES_HOME = %q, want %q", got, want)
	}

	t.Setenv("HERMES_HOME", "")
	got := hermesConfigPath()
	if runtime.GOOS == "windows" {
		if want := filepath.Join(tmp, "hermes", "config.yaml"); got != want {
			t.Errorf("hermesConfigPath = %q, want %q", got, want)
		}
	} else {
		if want := filepath.Join(tmp, ".hermes", "config.yaml"); got != want {
			t.Errorf("hermesConfigPath = %q, want %q", got, want)
		}
	}
}

func TestInstallHermesConfigLifecycle(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud", ContextLength: 131072, MaxOutputTokens: 8192},
		{ID: "second-model", Provider: "ollama_cloud"},
	}}

	cfgPath := hermesConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	// A hand-edited config: comments, another provider, an unrelated key and an
	// MCP server that must all survive byte-for-byte.
	userConfig := `# Hermes configuration
model: local/llama3

providers:
  # my own provider
  local:
    api: http://127.0.0.1:8080/v1
    transport: chat_completions

mcp_servers:
  github:
    url: https://example.com/mcp
`
	if err := os.WriteFile(cfgPath, []byte(userConfig), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if _, err := os.Stat(cfgPath + ".prism-backup"); err != nil {
		t.Errorf("expected a one-time .prism-backup: %v", err)
	}
	for _, want := range []string{"# Hermes configuration", "model: local/llama3", "# my own provider", "github:"} {
		if !strings.Contains(content, want) {
			t.Errorf("user content %q lost after install:\n%s", want, content)
		}
	}
	if strings.Count(content, "\nproviders:") != 1 {
		t.Errorf("expected exactly one top-level providers key:\n%s", content)
	}

	m := decodeHermesConfig(t, cfgPath)
	prism := hermesProviderIn(m, hermesProviderID)
	if prism == nil {
		t.Fatal("prism provider missing after install")
	}
	if prism["api"] != "http://127.0.0.1:11434/v1" {
		t.Errorf("api = %v, want http://127.0.0.1:11434/v1", prism["api"])
	}
	if prism["transport"] != "chat_completions" {
		t.Errorf("transport = %v, want chat_completions", prism["transport"])
	}
	if prism["api_key"] != "prism" {
		t.Errorf("api_key = %v, want prism", prism["api_key"])
	}
	if prism["default_model"] != "ollama_cloud/test-model" {
		t.Errorf("default_model = %v, want ollama_cloud/test-model", prism["default_model"])
	}
	models, _ := prism["models"].(map[string]interface{})
	if len(models) != 2 {
		t.Fatalf("expected 2 models in the prism provider, got %d", len(models))
	}
	first, _ := models["ollama_cloud/test-model"].(map[string]interface{})
	if first["context_length"] != 131072 {
		t.Errorf("context_length = %v, want 131072", first["context_length"])
	}
	if first["max_output_tokens"] != 8192 {
		t.Errorf("max_output_tokens = %v, want 8192", first["max_output_tokens"])
	}
	second, _ := models["ollama_cloud/second-model"].(map[string]interface{})
	if second["context_length"] != 128000 {
		t.Errorf("default context_length = %v, want 128000", second["context_length"])
	}
	if local := hermesProviderIn(m, "local"); local == nil {
		t.Error("user provider lost after install")
	}

	// Re-install is idempotent: byte-identical output, no duplicate region.
	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	again, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != content {
		t.Errorf("re-install changed the file:\n%s\n---\n%s", content, again)
	}

	// Restore removes only Prism's region.
	if err := RestoreHermesConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	m = decodeHermesConfig(t, cfgPath)
	if p := hermesProviderIn(m, hermesProviderID); p != nil {
		t.Error("prism provider still present after restore")
	}
	if local := hermesProviderIn(m, "local"); local == nil {
		t.Error("user provider lost after restore")
	}
	if m["model"] != "local/llama3" {
		t.Error("unrelated key lost after restore")
	}
}

// TestInstallHermesConfigWritesResponsesProvider covers the transport split:
// Hermes's transport is per-provider, so Responses-API models need their own
// prism-responses entry.
func TestInstallHermesConfigWritesResponsesProvider(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "chat-model", Provider: "ollama_cloud"},
		{ID: "codex-model", Provider: "codex-1", API: "responses"},
	}}
	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	m := decodeHermesConfig(t, hermesConfigPath())

	chat := hermesProviderIn(m, hermesProviderID)
	if chat == nil {
		t.Fatal("prism provider missing")
	}
	chatModels, _ := chat["models"].(map[string]interface{})
	if len(chatModels) != 1 {
		t.Errorf("chat provider should list only chat models, got %d", len(chatModels))
	}
	resp := hermesProviderIn(m, hermesResponsesProviderID)
	if resp == nil {
		t.Fatal("prism-responses provider missing")
	}
	if resp["transport"] != "codex_responses" {
		t.Errorf("transport = %v, want codex_responses", resp["transport"])
	}
	respModels, _ := resp["models"].(map[string]interface{})
	if len(respModels) != 1 {
		t.Errorf("responses provider should list only responses models, got %d", len(respModels))
	}
}

func TestInstallHermesConfigAppendsProvidersKey(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	cfgPath := hermesConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("model: local/llama3\n"), 0600); err != nil {
		t.Fatal(err)
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	m := decodeHermesConfig(t, cfgPath)
	if hermesProviderIn(m, hermesProviderID) == nil {
		t.Fatal("prism provider missing after appending providers key")
	}
	if m["model"] != "local/llama3" {
		t.Error("unrelated key lost")
	}
}

// TestInstallHermesConfigRejectsInlineProviders guards the one case that would
// silently clobber the user's providers: an inline mapping on the key's own
// line, which Prism cannot extend without rewriting it.
func TestInstallHermesConfigRejectsInlineProviders(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	cfgPath := hermesConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	original := "providers: {local: {api: http://127.0.0.1:8080/v1}}\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallHermesConfig(11434, remap); err == nil {
		t.Fatal("expected an error for an inline providers value")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("config was modified despite the error:\n%s", data)
	}
}

// TestInstallHermesConfigAcceptsEmptyProvidersMap covers the `providers: {}`
// shape Hermes itself writes for an empty config.
func TestInstallHermesConfigAcceptsEmptyProvidersMap(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	cfgPath := hermesConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("providers: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	m := decodeHermesConfig(t, hermesConfigPath())
	if hermesProviderIn(m, hermesProviderID) == nil {
		t.Fatal("prism provider missing after rewriting an empty providers map")
	}
}

// TestHermesMCPLifecycle covers the two regions sharing one file: installing
// MCP must not disturb the provider region, and vice versa.
func TestHermesMCPLifecycle(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("install provider: %v", err)
	}
	cfgPath := hermesConfigPath()

	if err := InstallAgentMCPConfig("hermes", 11434); err != nil {
		t.Fatalf("install MCP: %v", err)
	}
	m := decodeHermesConfig(t, cfgPath)
	servers, _ := m["mcp_servers"].(map[string]interface{})
	entry, _ := servers[prismMCPEntryName].(map[string]interface{})
	if entry == nil {
		t.Fatal("mcp_servers.prism missing after install")
	}
	if want := AgentMCPURL("hermes", 11434); entry["url"] != want {
		t.Errorf("url = %v, want %v", entry["url"], want)
	}
	headers, _ := entry["headers"].(map[string]interface{})
	if headers["Authorization"] != prismMCPAuthorization {
		t.Errorf("Authorization = %v, want %s", headers["Authorization"], prismMCPAuthorization)
	}
	if entry["enabled"] != true {
		t.Errorf("enabled = %v, want true", entry["enabled"])
	}
	if hermesProviderIn(m, hermesProviderID) == nil {
		t.Error("provider region lost when installing MCP")
	}
	if !AgentMCPActive("hermes") {
		t.Error("AgentMCPActive(hermes) = false after install")
	}

	// Re-install is idempotent.
	afterFirst, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallAgentMCPConfig("hermes", 11434); err != nil {
		t.Fatalf("re-install MCP: %v", err)
	}
	afterSecond, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Errorf("MCP re-install changed the file:\n%s\n---\n%s", afterFirst, afterSecond)
	}

	// A provider re-sync must leave the MCP region alone.
	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("re-install provider: %v", err)
	}
	if !AgentMCPActive("hermes") {
		t.Error("provider re-sync removed the MCP entry")
	}

	// Restore removes only the MCP region.
	if err := RestoreAgentMCPConfig("hermes"); err != nil {
		t.Fatalf("restore MCP: %v", err)
	}
	m = decodeHermesConfig(t, cfgPath)
	if servers, _ := m["mcp_servers"].(map[string]interface{}); servers != nil {
		if _, exists := servers[prismMCPEntryName]; exists {
			t.Error("mcp_servers.prism still present after restore")
		}
	}
	if AgentMCPActive("hermes") {
		t.Error("AgentMCPActive(hermes) = true after restore")
	}
	if hermesProviderIn(m, hermesProviderID) == nil {
		t.Error("provider region lost when restoring MCP")
	}
}

// TestHermesAgentWiring asserts the shared agent plumbing knows about Hermes.
func TestHermesAgentWiring(t *testing.T) {
	if !IsSupportedAgent("hermes") {
		t.Error("hermes missing from supportedAgents")
	}
	if AgentDisplayName("hermes") != "Hermes" {
		t.Errorf("AgentDisplayName = %q", AgentDisplayName("hermes"))
	}
	found := false
	for _, id := range AllAgentIDs() {
		if id == "hermes" {
			found = true
		}
	}
	if !found {
		t.Error("hermes missing from AllAgentIDs")
	}
	if !AgentMCPSupported("hermes") {
		t.Error("hermes missing from mcpSupportedAgents")
	}
	if got, want := AgentMCPConfigPath("hermes"), hermesConfigPath(); got != want {
		t.Errorf("AgentMCPConfigPath = %q, want %q", got, want)
	}
	if got, want := agentConfigPath("hermes"), hermesConfigPath(); got != want {
		t.Errorf("agentConfigPath = %q, want %q", got, want)
	}
}

// TestHermesActiveDetectsProviderRegion pins the YAML branch in IsAgentActive:
// without it Hermes's config is fed to json.Unmarshal and the check silently
// reports inactive.
func TestHermesActiveDetectsProviderRegion(t *testing.T) {
	setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	cfgPath := hermesConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("model: local/llama3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if IsAgentActive("hermes") {
		t.Error("IsAgentActive(hermes) = true with no Prism provider")
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "test-model", Provider: "ollama_cloud"},
	}}
	if err := InstallHermesConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !IsAgentActive("hermes") {
		t.Error("IsAgentActive(hermes) = false after install")
	}
}
