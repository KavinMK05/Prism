package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// prismManagedTag is the display-name prefix used to tag model entries that
// Prism writes into agent JSON config files (Factory Droid, OpenCode). JSON
// has no comments, so we identify our entries by this prefix instead of the
// TOML managed-block markers used by the Codex Desktop integration.
const prismManagedTag = "[Prism]"

// supportedAgents is the canonical list of agent ids handled by the generic
// /admin/agent/* endpoints and SyncAgents.
var supportedAgents = []string{"claude-code", "factory-droid", "opencode", "zcode"}

// agentConfigPath returns the config file path for the given agent id.
// Returns "" if the home directory cannot be determined or the id is unknown.
func agentConfigPath(agentID string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	switch agentID {
	case "claude-code":
		return filepath.Join(home, ".claude", "settings.json")
	case "factory-droid":
		return filepath.Join(home, ".factory", "settings.json")
	case "opencode":
		return filepath.Join(home, ".config", "opencode", "opencode.json")
	case "zcode":
		return filepath.Join(home, ".zcode", "v2", "config.json")
	}
	return ""
}

// agentDisplayName returns a human-friendly name for an agent id.
func agentDisplayName(agentID string) string {
	switch agentID {
	case "claude-code":
		return "Claude Code"
	case "factory-droid":
		return "Factory Droid"
	case "opencode":
		return "OpenCode"
	case "zcode":
		return "ZCode"
	}
	return agentID
}

// isSupportedAgent reports whether the id is one of the known agents.
func isSupportedAgent(id string) bool {
	for _, a := range supportedAgents {
		if a == id {
			return true
		}
	}
	return false
}

// agentBackupPath returns the one-time backup path for an agent config file.
func agentBackupPath(configPath string) string {
	return configPath + ".prism-backup"
}

// ensureAgentBackup makes a one-time backup of the agent config if no backup
// exists yet. If the original file does not exist, no backup is created.
func ensureAgentBackup(configPath string) {
	backup := agentBackupPath(configPath)
	if _, err := os.Stat(backup); err == nil {
		return // backup already exists
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return // original missing or unreadable; nothing to back up
	}
	_ = os.WriteFile(backup, data, 0600)
}

// restoreAgentFromBackup restores the agent config from the one-time backup,
// if present. Returns nil if there is no backup to restore.
func restoreAgentFromBackup(configPath string) error {
	backup := agentBackupPath(configPath)
	data, err := os.ReadFile(backup)
	if err != nil {
		return nil // no backup; nothing to restore
	}
	return os.WriteFile(configPath, data, 0600)
}

// isAgentConfigInstalled reports whether the agent's config file exists.
func isAgentConfigInstalled(agentID string) bool {
	p := agentConfigPath(agentID)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// isAgentActive reports whether Prism's managed config is present in the
// agent's config file. Detection is agent-specific:
//   - claude-code: env.ANTHROPIC_BASE_URL is set in ~/.claude/settings.json
//   - factory-droid: a [Prism]-tagged entry exists in customModels[]
//   - opencode: a "prism" provider block exists
func isAgentActive(agentID string) bool {
	p := agentConfigPath(agentID)
	if p == "" {
		return false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	switch agentID {
	case "claude-code":
		if env, ok := m["env"].(map[string]interface{}); ok {
			if _, set := env["ANTHROPIC_BASE_URL"]; set {
				return true
			}
		}
		return false
	case "factory-droid":
		if arr, ok := m["customModels"].([]interface{}); ok {
			return hasPrismModels(arr)
		}
		return false
	case "opencode":
		if provs, ok := m["provider"].(map[string]interface{}); ok {
			if _, set := provs["prism"]; set {
				return true
			}
			if _, set := provs["prism-codex"]; set {
				return true
			}
		}
		return false
	case "zcode":
		if provs, ok := m["provider"].(map[string]interface{}); ok {
			if _, set := provs[zcodeProviderID]; set {
				return true
			}
		}
		return false
	}
	return false
}

// readJSONConfig reads and parses a JSON config file into a generic map.
// Returns an empty (non-nil) map if the file does not exist or is empty.
func readJSONConfig(path string) (map[string]interface{}, error) {
	m := map[string]interface{}{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// writeJSONConfig writes a generic map to a JSON file with 2-space indent,
// creating parent directories as needed.
func writeJSONConfig(path string, m map[string]interface{}) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// stripPrismModels removes entries from a JSON array whose "displayName" starts
// with the Prism managed tag. Returns the filtered array and the count removed.
func stripPrismModels(arr []interface{}) ([]interface{}, int) {
	out := make([]interface{}, 0, len(arr))
	removed := 0
	for _, item := range arr {
		if mp, ok := item.(map[string]interface{}); ok {
			if dn, _ := mp["displayName"].(string); strings.HasPrefix(dn, prismManagedTag) {
				removed++
				continue
			}
		}
		out = append(out, item)
	}
	return out, removed
}

// hasPrismModels reports whether the array contains any Prism-tagged entry.
func hasPrismModels(arr []interface{}) bool {
	for _, item := range arr {
		if mp, ok := item.(map[string]interface{}); ok {
			if dn, _ := mp["displayName"].(string); strings.HasPrefix(dn, prismManagedTag) {
				return true
			}
		}
	}
	return false
}

// proxyPortFromEnv resolves the Prism proxy port from PRISM_PORT, falling back
// to 11434 (matching the Codex Desktop integration and main.go defaults).
func proxyPortFromEnv() int {
	p := os.Getenv("PRISM_PORT")
	if p == "" {
		p = "11434"
	}
	return parseIntOr(p, 11434)
}

// ── Per-agent sync (filled in by later phases) ──

// syncClaudeCode is implemented in claude_code.go (Phase 2).
// syncFactoryDroid is implemented in factory_droid.go (Phase 3).
// syncOpencode is implemented in opencode_agent.go (Phase 4).
// syncZcode is implemented in zcode_agent.go.

// agentInstalled reports whether the agent is installed, using the per-agent
// installed check (OpenCode also accepts the binary being on PATH).
func agentInstalled(id string) bool {
	switch id {
	case "claude-code":
		return isClaudeCodeInstalled()
	case "factory-droid":
		return isFactoryDroidInstalled()
	case "opencode":
		return isOpencodeInstalled()
	case "zcode":
		return isZcodeInstalled()
	}
	return false
}

// SyncAgents syncs all supported agent integrations on startup, mirroring the
// SyncCodexDesktop pattern. Agents that are not installed are silently skipped.
func SyncAgents(port int) {
	for _, id := range supportedAgents {
		if !agentInstalled(id) {
			log.Printf("[%s] Not installed, skipping sync", agentDisplayName(id))
			continue
		}
		switch id {
		case "claude-code":
			syncClaudeCode(port)
		case "factory-droid":
			syncFactoryDroid(port)
		case "opencode":
			syncOpencode(port)
		case "zcode":
			syncZcode(port)
		}
	}
}
