package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// prismManagedTag is the display-name prefix used to tag model entries that
// Prism writes into agent JSON config files (Factory Droid, OpenCode). JSON
// has no comments, so we identify our entries by this prefix instead of the
// TOML managed-block markers used by the Codex Desktop integration.
const prismManagedTag = "[Prism]"

// supportedAgents is the canonical list of agent ids handled by the generic
// /admin/agent/* endpoints and SyncAgents.
var supportedAgents = []string{"claude-code", "factory-droid", "opencode", "zcode", "omp", "grok-build", "pi"}

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
	case "omp":
		return filepath.Join(home, ".omp", "agent", "models.yml")
	case "grok-build":
		return filepath.Join(home, ".grok", "config.toml")
	case "pi":
		return filepath.Join(home, ".pi", "agent", "settings.json")
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
	case "omp":
		return "Oh My Pi"
	case "grok-build":
		return "Grok Build"
	case "pi":
		return "Pi"
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


// lookupBinary reports whether the named executable is reachable, searching
// the process PATH first and then common install directories that GUI apps
// (.app bundles, LaunchAgents) do not inherit. On macOS a GUI app launched
// from Finder/Dock gets only /usr/bin:/bin:/usr/sbin:/sbin, so binaries
// installed by Homebrew (/opt/homebrew/bin), bun (~/.bun/bin), npm/mise
// (~/.local/bin, ~/.local/share/mise/shims) are invisible to exec.LookPath
// and agents are wrongly reported as "not installed".
func lookupBinary(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil && p != "" {
		return p, true
	}
	home, err := os.UserHomeDir()
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".local", "share", "mise", "shims"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".yarn", "bin"),
			filepath.Join(home, ".deno", "bin"),
			filepath.Join(home, ".cargo", "bin"),
		)
	}
	for _, dir := range dirs {
		candidates := []string{filepath.Join(dir, name)}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				filepath.Join(dir, name+".exe"),
				filepath.Join(dir, name+".cmd"),
				filepath.Join(dir, name+".bat"),
			)
		}
		for _, p := range candidates {
			info, err := os.Stat(p)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue // not executable
			}
			return p, true
		}
	}
	return "", false
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
	if agentID == "grok-build" {
		return strings.Contains(string(data), codexManagedBegin)
	}
	var m map[string]interface{}
	if agentID == "omp" {
		if err := yaml.Unmarshal(data, &m); err != nil {
			return false
		}
	} else {
		if err := json.Unmarshal(data, &m); err != nil {
			return false
		}
	}
	switch agentID {
	case "claude-code":
		// Prism writes to settings.local.json (Claude Code doesn't overwrite
		// it); fall back to settings.json for entries from older Prism versions.
		if env, ok := m["env"].(map[string]interface{}); ok {
			if _, set := env["ANTHROPIC_BASE_URL"]; set {
				return true
			}
		}
		if lp := claudeCodeLocalConfigPath(); lp != "" {
			ld, err := os.ReadFile(lp)
			if err == nil {
				var lm map[string]interface{}
				if json.Unmarshal(ld, &lm) == nil {
					if env, ok := lm["env"].(map[string]interface{}); ok {
						if _, set := env["ANTHROPIC_BASE_URL"]; set {
							return true
						}
					}
				}
			}
		}
		return false
	case "factory-droid":
		// Prism writes to settings.local.json (droid doesn't overwrite it);
		// fall back to settings.json for entries from older Prism versions.
		for _, fp := range []string{factoryDroidLocalConfigPath(), p} {
			if fp == "" {
				continue
			}
			fd, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			var fm map[string]interface{}
			if err := json.Unmarshal(fd, &fm); err != nil {
				continue
			}
			if arr, ok := fm["customModels"].([]interface{}); ok {
				if hasPrismModels(arr) {
					return true
				}
			}
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
	case "omp":
		if provs, ok := m["providers"].(map[string]interface{}); ok {
			if _, set := provs[ompProviderID]; set {
				return true
			}
			if _, set := provs[ompProviderID+"-codex"]; set {
				return true
			}
		}
		return false
	case "pi":
		return isPiActive()
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

// customModelKey returns a deduplication key for a customModels entry (the
// "model" field, falling back to the string representation of the entry for
// non-map items). Used to avoid accumulating duplicate non-Prism entries when
// merging customModels from settings.json and settings.local.json.
func customModelKey(entry interface{}) string {
	if mp, ok := entry.(map[string]interface{}); ok {
		if model, _ := mp["model"].(string); model != "" {
			return model
		}
	}
	return fmt.Sprintf("%v", entry)
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
	case "omp":
		return isOmpInstalled()
	case "grok-build":
		return isGrokBuildInstalled()
	case "pi":
		return isPiInstalled()
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
		case "omp":
			syncOmp(port)
		case "grok-build":
			syncGrokBuild(port)
		case "pi":
			syncPi(port)
		}
	}
}
