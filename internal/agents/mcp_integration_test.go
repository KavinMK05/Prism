package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentMCPSupported(t *testing.T) {
	home := setTestHomeAndConfigDir(t)
	// Prime Agent resolves its config dir from the environment, and on Windows
	// falls back to probing WSL; point it at a temp dir so the test never
	// touches (or starts) the real one.
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", filepath.Join(home, ".prime", "agent"))
	t.Setenv("PI_CODING_AGENT_DIR", "")

	// Every agent Prism integrates with exposes an MCP config it can write.
	for _, id := range AllAgentIDs() {
		if !AgentMCPSupported(id) {
			t.Errorf("AgentMCPSupported(%q) = false, want true", id)
		}
		if AgentMCPConfigPath(id) == "" {
			t.Errorf("AgentMCPConfigPath(%q) is empty", id)
		}
	}
	for _, id := range []string{"not-an-agent", "gemini-cli"} {
		if AgentMCPSupported(id) {
			t.Errorf("AgentMCPSupported(%q) = true, want false", id)
		}
	}
	if err := InstallAgentMCPConfig("gemini-cli", 11434); err == nil {
		t.Error("installing MCP config for an unsupported agent should fail")
	}
	if err := RestoreAgentMCPConfig("not-an-agent"); err == nil {
		t.Error("restoring MCP config for an unknown agent should fail")
	}
	if AgentMCPActive("gemini-cli") {
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

// jsonMCPAgentCase describes one agent whose MCP entry lives in a JSON config
// file, plus where that agent keeps its servers inside the file.
type jsonMCPAgentCase struct {
	agent string
	// serversPath is the path from the file root to the object of MCP servers.
	serversPath []string
	// unrelated is a key the user (or the agent) already had at the top level,
	// which every write must leave untouched.
	unrelated string
	// wantType is the transport field the entry should declare, or "" when the
	// agent infers HTTP from the url alone.
	wantType string
}

func TestInstallMCPConfigForJSONAgents(t *testing.T) {
	home := setTestHomeAndConfigDir(t)
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", filepath.Join(home, ".prime", "agent"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("KIMI_CODE_HOME", "")

	cases := []jsonMCPAgentCase{
		{agent: "factory-droid", serversPath: []string{"mcpServers"}, unrelated: "theme", wantType: "http"},
		{agent: "omp", serversPath: []string{"mcpServers"}, unrelated: "$schema", wantType: "http"},
		{agent: "zcode", serversPath: []string{"mcp", "servers"}, unrelated: "theme", wantType: "http"},
		{agent: "kimi-code", serversPath: []string{"mcpServers"}, unrelated: "theme"},
		{agent: "pi", serversPath: []string{"mcpServers"}, unrelated: "packages"},
		{agent: "prime-agent", serversPath: []string{"mcpServers"}, unrelated: "theme", wantType: "http"},
	}

	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			path := AgentMCPConfigPath(tc.agent)
			if path == "" {
				t.Fatalf("AgentMCPConfigPath(%q) is empty", tc.agent)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			// The agent's own provider config, so the startup sync treats it as
			// installed.
			markAgentInstalled(t, tc.agent)

			original := map[string]interface{}{tc.unrelated: "keep-me"}
			setNestedPath(original, appendPath(tc.serversPath, "user-server"),
				map[string]interface{}{"url": "https://user.example/mcp"})
			data, err := json.MarshalIndent(original, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}

			if err := InstallAgentMCPConfig(tc.agent, 11434); err != nil {
				t.Fatalf("install: %v", err)
			}
			after := readMapConfig(t, path)
			if after[tc.unrelated] != "keep-me" {
				t.Errorf("install dropped the unrelated %q key: %v", tc.unrelated, after)
			}
			if _, ok := getNestedPath(after, appendPath(tc.serversPath, "user-server")); !ok {
				t.Error("install dropped the user's own MCP server")
			}
			entry := mcpEntryFor(t, after, tc.serversPath)
			if got, want := entry["url"], "http://127.0.0.1:11434/mcp/"+tc.agent; got != want {
				t.Errorf("url = %v, want %v", got, want)
			}
			if tc.wantType == "" {
				if _, has := entry["type"]; has {
					t.Errorf("entry must not declare a transport type (a url alone means HTTP): %v", entry)
				}
			} else if entry["type"] != tc.wantType {
				t.Errorf("type = %v, want %v", entry["type"], tc.wantType)
			}
			headers, _ := entry["headers"].(map[string]interface{})
			if headers["Authorization"] != "Bearer prism" {
				t.Errorf("Authorization = %v (agents must only ever see Prism's own token)", headers["Authorization"])
			}

			if !AgentMCPActive(tc.agent) {
				t.Error("AgentMCPActive should be true after install")
			}
			if backup, err := os.ReadFile(agentBackupPath(path)); err != nil || string(backup) != string(data) {
				t.Errorf("one-time backup missing or wrong (err=%v)", err)
			}

			// Re-syncing is idempotent.
			first, _ := os.ReadFile(path)
			if err := InstallAgentMCPConfig(tc.agent, 11434); err != nil {
				t.Fatalf("second install: %v", err)
			}
			second, _ := os.ReadFile(path)
			if string(first) != string(second) {
				t.Errorf("re-sync changed the file:\n%s\n---\n%s", first, second)
			}

			// A startup sync refreshes the entry to the port Prism is on.
			SyncAllAgentMCP(9999)
			entry = mcpEntryFor(t, readMapConfig(t, path), tc.serversPath)
			if got, want := entry["url"], "http://127.0.0.1:9999/mcp/"+tc.agent; got != want {
				t.Errorf("url after sync = %v, want %v", got, want)
			}

			// Restore removes only Prism's entry.
			if err := RestoreAgentMCPConfig(tc.agent); err != nil {
				t.Fatalf("restore: %v", err)
			}
			restored := readMapConfig(t, path)
			if _, ok := getNestedPath(restored, appendPath(tc.serversPath, prismMCPEntryName)); ok {
				t.Error("prism entry still present after restore")
			}
			if _, ok := getNestedPath(restored, appendPath(tc.serversPath, "user-server")); !ok {
				t.Error("restore removed a user server")
			}
			if restored[tc.unrelated] != "keep-me" {
				t.Error("restore dropped an unrelated key")
			}
			if AgentMCPActive(tc.agent) {
				t.Error("AgentMCPActive should be false after restore")
			}
		})
	}
}

// ZCode is the one agent with a nested servers path (mcp.servers), so the
// objects Prism had to create for its entry must not be left behind as an empty
// shell once the entry is removed.
func TestZcodeMCPPrunesEmptyNesting(t *testing.T) {
	setTestHomeAndConfigDir(t)
	path := AgentMCPConfigPath("zcode")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\n  \"theme\": \"dark\"\n}"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InstallAgentMCPConfig("zcode", 11434); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, ok := getNestedPath(readMapConfig(t, path), []string{"mcp", "servers", prismMCPEntryName}); !ok {
		t.Fatal("mcp.servers.prism missing after install")
	}

	if err := RestoreAgentMCPConfig("zcode"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored := readMapConfig(t, path)
	if _, ok := restored["mcp"]; ok {
		t.Errorf("restore left an empty mcp object behind: %v", restored)
	}
	if restored["theme"] != "dark" {
		t.Error("restore dropped an unrelated key")
	}
}

// Grok Build rewrites ~/.grok/config.toml from parsed data whenever its own
// `grok mcp ...` commands run, which erases every comment - including Prism's
// managed-block markers. Prism's MCP entry must therefore be recognised by
// table name, and re-syncing must not duplicate the table.
func TestGrokBuildMCPTableLifecycle(t *testing.T) {
	home := setTestHomeAndConfigDir(t)
	configPath := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}

	// A user config carrying Prism's model block plus one of the user's own MCP
	// servers.
	userConfig := "[models]\n" +
		"default = \"prism-glm\"\n\n" +
		codexManagedBegin + "\n" +
		"[model.prism-glm]\n" +
		"model = \"ollama_cloud/glm\"\n" +
		"base_url = \"http://127.0.0.1:11434/v1\"\n" +
		codexManagedEnd + "\n\n" +
		"[mcp_servers.mine]\n" +
		"url = \"https://mine.example/mcp\"\n"
	if err := os.WriteFile(configPath, []byte(userConfig), 0600); err != nil {
		t.Fatal(err)
	}

	if err := InstallAgentMCPConfig("grok-build", 11434); err != nil {
		t.Fatalf("install: %v", err)
	}
	content := readFileString(t, configPath)
	for _, want := range []string{
		"[models]", `default = "prism-glm"`,
		"[model.prism-glm]", "[mcp_servers.mine]", `url = "https://mine.example/mcp"`,
		"[mcp_servers." + prismMCPEntryName + "]",
		`url = "http://127.0.0.1:11434/mcp/grok-build"`,
		`headers = { Authorization = "Bearer prism" }`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("install is missing %q:\n%s", want, content)
		}
	}
	if !AgentMCPActive("grok-build") {
		t.Error("grok-build should report the MCP entry as active")
	}

	// Re-installing must not duplicate the table.
	if err := InstallAgentMCPConfig("grok-build", 11434); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	if n := strings.Count(readFileString(t, configPath), "[mcp_servers."+prismMCPEntryName+"]"); n != 1 {
		t.Errorf("Prism's MCP table appears %d times, want 1", n)
	}

	// Detection survives the comment-free rewrite Grok itself performs.
	rewritten := strings.NewReplacer(codexManagedBegin+"\n", "", codexManagedEnd+"\n", "").Replace(readFileString(t, configPath))
	if err := os.WriteFile(configPath, []byte(rewritten), 0600); err != nil {
		t.Fatal(err)
	}
	if !AgentMCPActive("grok-build") {
		t.Error("detection must not depend on comment markers: Grok strips them")
	}
	if err := InstallAgentMCPConfig("grok-build", 11434); err != nil {
		t.Fatalf("install after rewrite: %v", err)
	}
	if n := strings.Count(readFileString(t, configPath), "[mcp_servers."+prismMCPEntryName+"]"); n != 1 {
		t.Errorf("resync after Grok's rewrite left %d Prism tables, want 1", n)
	}

	// Restore removes Prism's table only.
	if err := RestoreAgentMCPConfig("grok-build"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	content = readFileString(t, configPath)
	if strings.Contains(content, "[mcp_servers."+prismMCPEntryName+"]") {
		t.Errorf("Prism's MCP table survived restore:\n%s", content)
	}
	for _, want := range []string{"[mcp_servers.mine]", `url = "https://mine.example/mcp"`, "[model.prism-glm]", `default = "prism-glm"`} {
		if !strings.Contains(content, want) {
			t.Errorf("restore dropped %q:\n%s", want, content)
		}
	}
	if AgentMCPActive("grok-build") {
		t.Error("grok-build should not be active after restore")
	}
	// Restoring twice is a no-op, not an error.
	if err := RestoreAgentMCPConfig("grok-build"); err != nil {
		t.Errorf("second restore: %v", err)
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
	// The gateway rejects unauthenticated requests, and Codex's streamable
	// HTTP transport only reads credentials from these keys.
	if !strings.Contains(content, `http_headers = { Authorization = "Bearer prism" }`) {
		t.Errorf("codex MCP Authorization header missing (the gateway returns 401 without it):\n%s", content)
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

// Codex persists settings of its own — project trust, the Windows sandbox mode
// — by appending tables to ~/.codex/config.toml. Prism's MCP block sits at the
// end of that file, so those tables land inside the markers. Syncing and
// restoring Prism's entry must leave them alone: dropping them is what made
// Codex ask the user to trust the folder and set up the sandbox on every restart.
func TestCodexMCPBlockKeepsTablesCodexWrote(t *testing.T) {
	home := setTestHomeAndConfigDir(t)
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}

	// The shape Codex leaves behind: Prism's block, then the tables Codex wrote
	// for itself, then Prism's end marker.
	codexWritten := "[projects.'c:\\users\\kavin\\documents\\personal projects\\ollama proxy']\n" +
		"trust_level = \"trusted\"\n" +
		"\n" +
		"[windows]\n" +
		"sandbox = \"elevated\"\n"
	userConfig := "approval_policy = \"on-request\"\n\n" +
		mcpManagedBegin + "\n" +
		"[mcp_servers." + prismMCPEntryName + "]\n" +
		"url = \"http://127.0.0.1:11434/mcp/codex\"\n" +
		"http_headers = { Authorization = \"Bearer prism\" }\n" +
		"\n" +
		codexWritten +
		mcpManagedEnd + "\n"
	if err := os.WriteFile(configPath, []byte(userConfig), 0600); err != nil {
		t.Fatal(err)
	}

	// The startup sync rewrites Prism's entry from scratch.
	if err := InstallAgentMCPConfig("codex", 11434); err != nil {
		t.Fatalf("codex install: %v", err)
	}
	content := readFileString(t, configPath)
	for _, want := range []string{"[projects.", `trust_level = "trusted"`, "[windows]", `sandbox = "elevated"`, "approval_policy"} {
		if !strings.Contains(content, want) {
			t.Errorf("the MCP sync dropped Codex's own setting %q:\n%s", want, content)
		}
	}
	if n := strings.Count(content, "[mcp_servers."+prismMCPEntryName+"]"); n != 1 {
		t.Errorf("expected one Prism MCP table, got %d:\n%s", n, content)
	}

	// Restoring Prism's entry must not take them either.
	if err := RestoreAgentMCPConfig("codex"); err != nil {
		t.Fatalf("codex restore: %v", err)
	}
	content = readFileString(t, configPath)
	if strings.Contains(content, "[mcp_servers."+prismMCPEntryName+"]") {
		t.Errorf("Prism's MCP table survived restore:\n%s", content)
	}
	for _, want := range []string{`trust_level = "trusted"`, `sandbox = "elevated"`} {
		if !strings.Contains(content, want) {
			t.Errorf("restore dropped Codex's own setting %q:\n%s", want, content)
		}
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

// markAgentInstalled writes the config file an agent's installed check looks
// for, so the startup sync treats it as present without needing the real CLI.
func markAgentInstalled(t *testing.T, agentID string) {
	t.Helper()
	path := agentConfigPath(agentID)
	if path == "" {
		t.Fatalf("agentConfigPath(%q) is empty", agentID)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// appendPath returns path with extra appended, leaving path untouched.
func appendPath(path []string, extra string) []string {
	out := make([]string, 0, len(path)+1)
	out = append(out, path...)
	return append(out, extra)
}

// setNestedPath stores value at a dotted path, creating intermediate objects.
func setNestedPath(m map[string]interface{}, path []string, value interface{}) {
	cur := m
	for _, key := range path[:len(path)-1] {
		next, ok := cur[key].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			cur[key] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
}

// getNestedPath reads a value at a dotted path.
func getNestedPath(m map[string]interface{}, path []string) (interface{}, bool) {
	var cur interface{} = m
	for _, key := range path {
		obj, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = obj[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// mcpEntryFor returns Prism's MCP entry inside a parsed config, failing the test
// when it is missing or is not an object.
func mcpEntryFor(t *testing.T, m map[string]interface{}, serversPath []string) map[string]interface{} {
	t.Helper()
	value, ok := getNestedPath(m, appendPath(serversPath, prismMCPEntryName))
	if !ok {
		t.Fatalf("prism entry missing at %v: %v", serversPath, m)
	}
	entry, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("prism entry is %T, want an object", value)
	}
	return entry
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
