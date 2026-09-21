package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentMCPSupported(t *testing.T) {
	for _, id := range []string{"claude-code", "codex", "opencode", "zed"} {
		if !AgentMCPSupported(id) {
			t.Errorf("AgentMCPSupported(%q) = false, want true", id)
		}
		if AgentMCPConfigPath(id) == "" {
			t.Errorf("AgentMCPConfigPath(%q) is empty", id)
		}
	}
	for _, id := range []string{"zcode", "omp", "pi", "kimi-code", "not-an-agent"} {
		if AgentMCPSupported(id) {
			t.Errorf("AgentMCPSupported(%q) = true, want false", id)
		}
	}
	if err := InstallAgentMCPConfig("zcode", 11434); err == nil {
		t.Error("installing MCP config for an unsupported agent should fail")
	}
	if err := RestoreAgentMCPConfig("not-an-agent"); err == nil {
		t.Error("restoring MCP config for an unknown agent should fail")
	}
	if AgentMCPActive("zcode") {
		t.Error("agents without MCP support are never active")
	}
	if got, want := AgentMCPURL("claude-code", 8765), "http://127.0.0.1:8765/mcp/claude-code"; got != want {
		t.Errorf("AgentMCPURL = %q, want %q", got, want)
	}
}

func TestInstallClaudeCodeMCPConfigLifecycle(t *testing.T) {
	home := setTestHomeAndConfigDir(t)
	configPath := filepath.Join(home, ".claude.json")
	original := `{
  "theme": "dark",
  "mcpServers": {
    "user-server": {"type": "stdio", "command": "uvx", "args": ["something"]}
  }
}`
	if err := os.WriteFile(configPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InstallAgentMCPConfig("claude-code", 11434); err != nil {
		t.Fatalf("InstallAgentMCPConfig: %v", err)
	}
	after := readMapConfig(t, configPath)
	if after["theme"] != "dark" {
		t.Error("unrelated top-level keys must be preserved")
	}
	servers := after["mcpServers"].(map[string]interface{})
	if _, ok := servers["user-server"]; !ok {
		t.Error("existing MCP servers must not be clobbered")
	}
	prism := servers[prismMCPEntryName].(map[string]interface{})
	if prism["type"] != "http" {
		t.Errorf("expected a streamable HTTP entry (Claude Code rejects a url without a type), got %v", prism["type"])
	}
	if prism["url"] != "http://127.0.0.1:11434/mcp/claude-code" {
		t.Errorf("url = %v", prism["url"])
	}
	headers := prism["headers"].(map[string]interface{})
	if headers["Authorization"] != "Bearer prism" {
		t.Errorf("Authorization = %v (agents must only ever see Prism's own token)", headers["Authorization"])
	}

	if !AgentMCPActive("claude-code") {
		t.Error("AgentMCPActive should be true after install")
	}

	// One-time backup of the user's original file.
	backup, err := os.ReadFile(configPath + ".prism-backup")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != original {
		t.Error("backup should hold the pre-install content")
	}

	// Re-syncing is idempotent.
	first, _ := os.ReadFile(configPath)
	if err := InstallAgentMCPConfig("claude-code", 11434); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Errorf("re-sync changed the file:\n%s\n---\n%s", first, second)
	}

	// Restore removes only Prism's entry.
	if err := RestoreAgentMCPConfig("claude-code"); err != nil {
		t.Fatalf("RestoreAgentMCPConfig: %v", err)
	}
	restored := readMapConfig(t, configPath)
	if restored["theme"] != "dark" {
		t.Error("restore dropped unrelated keys")
	}
	restoredServers := restored["mcpServers"].(map[string]interface{})
	if _, ok := restoredServers[prismMCPEntryName]; ok {
		t.Error("prism entry still present after restore")
	}
	if _, ok := restoredServers["user-server"]; !ok {
		t.Error("restore removed a user server")
	}
	if AgentMCPActive("claude-code") {
		t.Error("AgentMCPActive should be false after restore")
	}
}

func TestInstallOpencodeAndZedMCPConfig(t *testing.T) {
	setTestHomeAndConfigDir(t)

	if err := InstallAgentMCPConfig("opencode", 11434); err != nil {
		t.Fatalf("opencode install: %v", err)
	}
	open := readMapConfig(t, AgentMCPConfigPath("opencode"))
	mcp, _ := open["mcp"].(map[string]interface{})
	entry, ok := mcp[prismMCPEntryName].(map[string]interface{})
	if !ok {
		t.Fatalf("opencode mcp.prism missing: %v", open)
	}
	if entry["type"] != "remote" || entry["enabled"] != true {
		t.Errorf("opencode entry = %v", entry)
	}
	if entry["url"] != "http://127.0.0.1:11434/mcp/opencode" {
		t.Errorf("opencode url = %v", entry["url"])
	}
	if !AgentMCPActive("opencode") {
		t.Error("opencode should be active after install")
	}
	if err := RestoreAgentMCPConfig("opencode"); err != nil {
		t.Fatalf("opencode restore: %v", err)
	}
	if AgentMCPActive("opencode") {
		t.Error("opencode still active after restore")
	}

	// Zed keeps its settings as JSONC; the writer rewrites valid JSON and a
	// user key survives the round trip.
	zedPath := AgentMCPConfigPath("zed")
	if err := os.MkdirAll(filepath.Dir(zedPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zedPath, []byte("{\n  // keep me\n  \"theme\": \"One Dark\",\n  \"context_servers\": {\n    \"other\": {\"url\": \"https://other.example/mcp\"},\n  },\n}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallAgentMCPConfig("zed", 11434); err != nil {
		t.Fatalf("zed install: %v", err)
	}
	zed := readMapConfig(t, zedPath)
	if zed["theme"] != "One Dark" {
		t.Error("zed user settings were lost")
	}
	ctxServers := zed["context_servers"].(map[string]interface{})
	if _, ok := ctxServers["other"]; !ok {
		t.Error("zed user context server was lost")
	}
	zedEntry, ok := ctxServers[prismMCPEntryName].(map[string]interface{})
	if !ok {
		t.Fatalf("zed context_servers.prism missing: %v", ctxServers)
	}
	if zedEntry["url"] != "http://127.0.0.1:11434/mcp/zed" {
		t.Errorf("zed url = %v", zedEntry["url"])
	}
}

func TestCodexMCPManagedBlock(t *testing.T) {
	home := setTestHomeAndConfigDir(t)
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}

	// A user's config that already carries Prism's model-provider block.
	providerBlock := codexManagedBegin + "\n" +
		`model_provider = "prism"` + "\n" +
		codexManagedEnd + "\n"
	userConfig := "approval_policy = \"on-request\"\n" + providerBlock
	if err := os.WriteFile(configPath, []byte(userConfig), 0600); err != nil {
		t.Fatal(err)
	}

	if !IsCodexDesktopInstalled() {
		t.Fatal("codex should be detected as installed once its config exists")
	}
	if err := InstallAgentMCPConfig("codex", 11434); err != nil {
		t.Fatalf("codex install: %v", err)
	}
	content := readFileString(t, configPath)
	if !strings.Contains(content, "[mcp_servers."+prismMCPEntryName+"]") {
		t.Errorf("codex MCP table missing:\n%s", content)
	}
	if !strings.Contains(content, `url = "http://127.0.0.1:11434/mcp/codex"`) {
		t.Errorf("codex MCP url missing:\n%s", content)
	}
	if !strings.Contains(content, "approval_policy") {
		t.Error("user config was dropped")
	}
	if !strings.Contains(content, `model_provider = "prism"`) {
		t.Error("the model-provider block was dropped")
	}
	if !AgentMCPActive("codex") {
		t.Error("codex should report the MCP entry as active")
	}

	// Re-installing must not duplicate the block.
	if err := InstallAgentMCPConfig("codex", 11434); err != nil {
		t.Fatalf("codex re-install: %v", err)
	}
	if n := strings.Count(readFileString(t, configPath), mcpManagedBegin); n != 1 {
		t.Errorf("MCP managed block appears %d times, want 1", n)
	}

	// The two marker pairs are independent: the model-provider installer's
	// strip must not remove the MCP block, and vice versa.
	stripped := stripManagedBlocks(readFileString(t, configPath))
	if !strings.Contains(stripped, mcpManagedBegin) {
		t.Error("stripManagedBlocks (model provider) removed the MCP block")
	}
	if strings.Contains(stripped, codexManagedBegin) {
		t.Error("stripManagedBlocks left its own block behind")
	}
	stripped = stripMCPManagedBlock(readFileString(t, configPath))
	if strings.Contains(stripped, mcpManagedBegin) {
		t.Error("stripMCPManagedBlock left the MCP block behind")
	}
	if !strings.Contains(stripped, codexManagedBegin) {
		t.Error("stripMCPManagedBlock removed the model-provider block")
	}

	// Restore removes the MCP block only.
	if err := RestoreAgentMCPConfig("codex"); err != nil {
		t.Fatalf("codex restore: %v", err)
	}
	content = readFileString(t, configPath)
	if strings.Contains(content, "[mcp_servers."+prismMCPEntryName+"]") {
		t.Error("codex MCP table still present after restore")
	}
	if !strings.Contains(content, `model_provider = "prism"`) {
		t.Error("restore removed the model-provider block")
	}
	if AgentMCPActive("codex") {
		t.Error("codex should not be active after restore")
	}
	// Restoring twice is a no-op, not an error.
	if err := RestoreAgentMCPConfig("codex"); err != nil {
		t.Errorf("second restore: %v", err)
	}
}

func TestSyncAllAgentMCPOnlyRefreshesInstalledEntries(t *testing.T) {
	home := setTestHomeAndConfigDir(t)
	// Make Claude Code look installed without needing the real CLI.
	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InstallAgentMCPConfig("claude-code", 11434); err != nil {
		t.Fatalf("install: %v", err)
	}

	// A startup sync follows the port Prism is actually listening on.
	SyncAllAgentMCP(9999)
	configPath := filepath.Join(home, ".claude.json")
	prism := readMapConfig(t, configPath)["mcpServers"].(map[string]interface{})[prismMCPEntryName].(map[string]interface{})
	if prism["url"] != "http://127.0.0.1:9999/mcp/claude-code" {
		t.Errorf("url after sync = %v", prism["url"])
	}

	// A removed entry must never be resurrected by a background sync: Setup is
	// an explicit action.
	if err := RestoreAgentMCPConfig("claude-code"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	SyncAllAgentMCP(1234)
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("sync modified a config with no Prism entry:\n%s\n---\n%s", before, after)
	}
	if AgentMCPActive("claude-code") {
		t.Error("sync re-added an entry the user removed")
	}
}

func readMapConfig(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return m
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
