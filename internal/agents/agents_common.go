// Package agents manages Prism's integrations with third-party coding
// agents (Claude Code, Codex Desktop, OpenCode, ZCode, OMP, Grok Build,
// Pi, and Kimi Code): config installation, restore, and status checks.
package agents

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ollama-proxy/internal/config"
)

// prismManagedTag is the display-name prefix used to tag model entries that
// Prism writes into agent JSON config files (Factory Droid, OpenCode). JSON
// has no comments, so we identify our entries by this prefix instead of the
// TOML managed-block markers used by the Codex Desktop integration.
const prismManagedTag = "[Prism]"

// prismModelRouteKey is the identifier exposed to agents. It is intentionally
// different from ModelEntry.ID: Prism resolves this qualified key locally and
// sends only the raw ID to the selected upstream provider.
func prismModelRouteKey(model config.ModelEntry) string {
	return config.ModelRouteKey(model)
}

func prismModelDisplayName(cfg *config.Config, model config.ModelEntry) string {
	provider := cfg.GetProviderName(model.Provider)
	return prismManagedTag + " " + provider + " · " + HumanizeModelID(model.ID)
}

// prismRouteForModelID upgrades a legacy bare model setting to the qualified
// key used by agent integrations. Unknown values are preserved for backwards
// compatibility and will be handled by the proxy's normal fallback logic.
func prismRouteForModelID(remap *config.ModelRemapping, id string) string {
	for _, model := range remap.KnownModels {
		if id == model.ID || id == prismModelRouteKey(model) {
			return prismModelRouteKey(model)
		}
	}
	return id
}

// supportedAgents is the canonical list of agent ids handled by the generic
// /admin/agent/* endpoints and SyncAgents. Codex Desktop is handled by a
// separate endpoint and sync function, but shares the same auto-sync policy.
var supportedAgents = []string{"claude-code", "factory-droid", "opencode", "zcode", "zed", "omp", "grok-build", "pi", "kimi-code"}

var allAgentIDs = append([]string{"codex"}, supportedAgents...)

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
	case "zed":
		// Platform-specific path is resolved by zedConfigPath (agents_common's
		// home-based switch can't express the macOS/Windows locations).
		return zedConfigPath()
	case "omp":
		return filepath.Join(home, ".omp", "agent", "models.yml")
	case "grok-build":
		return filepath.Join(home, ".grok", "config.toml")
	case "pi":
		return filepath.Join(home, ".pi", "agent", "settings.json")
	case "kimi-code":
		// Kimi Code reads its config from $KIMI_CODE_HOME/config.toml, defaulting
		// to ~/.kimi-code/config.toml. The file name is always config.toml.
		if root := os.Getenv("KIMI_CODE_HOME"); root != "" {
			return filepath.Join(root, "config.toml")
		}
		return filepath.Join(home, ".kimi-code", "config.toml")
	}
	return ""
}

// AgentDisplayName returns a human-friendly name for an agent id.
func AgentDisplayName(agentID string) string {
	switch agentID {
	case "claude-code":
		return "Claude Code"
	case "factory-droid":
		return "Factory Droid"
	case "opencode":
		return "OpenCode"
	case "zcode":
		return "ZCode"
	case "zed":
		return "Zed"
	case "omp":
		return "Oh My Pi"
	case "grok-build":
		return "Grok Build"
	case "pi":
		return "Pi"
	case "kimi-code":
		return "Kimi Code"
	}
	return agentID
}

// IsSupportedAgent reports whether the id is one of the known agents.
func IsSupportedAgent(id string) bool {
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
			// OpenCode official installer script target.
			filepath.Join(home, ".opencode", "bin"),
			// Go-installed binaries.
			filepath.Join(home, "go", "bin"),
		)
	}
	// npm global installs land here on Windows (%APPDATA%\npm\opencode.cmd).
	if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
		dirs = append(dirs, filepath.Join(cfgDir, "npm"))
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

// IsAgentActive reports whether Prism's managed config is present in the
// agent's config file. Detection is agent-specific:
//   - claude-code: env.ANTHROPIC_BASE_URL is set in ~/.claude/settings.json
//   - factory-droid: a [Prism]-tagged entry exists in customModels[]
//   - opencode: a "prism" provider block exists
func IsAgentActive(agentID string) bool {
	p := agentConfigPath(agentID)
	if p == "" {
		return false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	if agentID == "grok-build" || agentID == "kimi-code" {
		return strings.Contains(string(data), codexManagedBegin)
	}
	if agentID == "zed" {
		// Zed's settings.json is JSONC; use the comment-tolerant parser.
		m, _, err := readJSONCConfig(p)
		if err != nil {
			return false
		}
		if lm, ok := m["language_models"].(map[string]interface{}); ok {
			if compat, ok := lm["openai_compatible"].(map[string]interface{}); ok {
				_, set := compat[zedProviderID]
				return set
			}
		}
		return false
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
			if _, set := provs["prism-responses"]; set {
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
	case "zed":
		if lm, ok := m["language_models"].(map[string]interface{}); ok {
			if compat, ok := lm["openai_compatible"].(map[string]interface{}); ok {
				if _, set := compat[zedProviderID]; set {
					return true
				}
			}
		}
		return false
	case "omp":
		if provs, ok := m["providers"].(map[string]interface{}); ok {
			if _, set := provs[ompProviderID]; set {
				return true
			}
			if _, set := provs[ompProviderID+"-responses"]; set {
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

// ProxyPortFromEnv resolves the Prism proxy port from PRISM_PORT, falling back
// to 11434 (matching the Codex Desktop integration and main.go defaults).
func ProxyPortFromEnv() int {
	p := os.Getenv("PRISM_PORT")
	if p == "" {
		p = "11434"
	}
	return ParseIntOr(p, 11434)
}

// ── Per-agent sync (filled in by later phases) ──

// syncClaudeCode is implemented in claude_code.go (Phase 2).
// syncFactoryDroid is implemented in factory_droid.go (Phase 3).
// syncOpencode is implemented in opencode_agent.go (Phase 4).
// syncZcode is implemented in zcode_agent.go.

// AgentAutoSyncEnabled reports whether automatic synchronization is enabled
// for an agent. Explicit Setup is allowed to enable an agent; this gate is
// only for startup and model-change synchronization.
func AgentAutoSyncEnabled(agentID string) bool {
	if !isKnownAgentID(agentID) {
		return false
	}
	cfg := config.Load()
	if cfg.AgentIntegrations == nil || !cfg.AgentIntegrations.AutoSyncMigrated {
		return false
	}
	return cfg.AgentIntegrations.AutoSync[agentID]
}

// SetAgentAutoSync persists the user's choice for an agent and keeps the
// running admin process's in-memory config in sync.
func SetAgentAutoSync(agentID string, enabled bool) error {
	if !isKnownAgentID(agentID) {
		return fmt.Errorf("unknown agent: %s", agentID)
	}
	cfg := config.Load()
	integrations := cfg.EnsureAgentIntegrations()
	integrations.AutoSync[agentID] = enabled
	integrations.AutoSyncMigrated = true
	if err := config.Save(cfg); err != nil {
		return err
	}
	config.UpdateCurrent(func(current *config.Config) {
		if current == nil {
			return
		}
		integrations := current.EnsureAgentIntegrations()
		integrations.AutoSync[agentID] = enabled
		integrations.AutoSyncMigrated = true
	})
	return nil
}

// InitializeAutoSync performs the one-time migration from Prism's historical
// always-on agent behavior. Existing active integrations remain enabled;
// agents that were not active, including all agents on a fresh install, start
// disabled until the user clicks Setup.
func InitializeAutoSync() error {
	cfg := config.Load()
	integrations := cfg.EnsureAgentIntegrations()
	if integrations.AutoSyncMigrated {
		return nil
	}

	autoSync := make(map[string]bool, len(allAgentIDs))
	for _, id := range allAgentIDs {
		if id == "codex" {
			autoSync[id] = IsCodexDesktopActive()
		} else {
			autoSync[id] = IsAgentActive(id)
		}
	}
	integrations.AutoSync = autoSync
	integrations.AutoSyncMigrated = true
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to initialize agent auto-sync: %w", err)
	}
	config.UpdateCurrent(func(current *config.Config) {
		if current != nil {
			current.AgentIntegrations = cfg.AgentIntegrations
		}
	})
	return nil
}

func isKnownAgentID(id string) bool {
	for _, known := range allAgentIDs {
		if known == id {
			return true
		}
	}
	return false
}

// AgentInstalled reports whether the agent is installed, using the per-agent
// installed check (OpenCode also accepts the binary being on PATH).
func AgentInstalled(id string) bool {
	switch id {
	case "claude-code":
		return isClaudeCodeInstalled()
	case "factory-droid":
		return isFactoryDroidInstalled()
	case "opencode":
		return isOpencodeInstalled()
	case "zcode":
		return isZcodeInstalled()
	case "zed":
		return isZedInstalled()
	case "omp":
		return isOmpInstalled()
	case "grok-build":
		return isGrokBuildInstalled()
	case "pi":
		return isPiInstalled()
	case "kimi-code":
		return isKimiCodeInstalled()
	}
	return false
}

// SyncAgents syncs all supported agent integrations on startup, mirroring the
// SyncCodexDesktop pattern. Agents that are not installed are silently skipped.
func SyncAgents(port int) {
	for _, id := range supportedAgents {
		if !AgentInstalled(id) {
			log.Printf("[%s] Not installed, skipping sync", AgentDisplayName(id))
			continue
		}
		if !AgentAutoSyncEnabled(id) {
			log.Printf("[%s] Auto-sync disabled, skipping sync", AgentDisplayName(id))
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
		case "zed":
			syncZed(port)
		case "omp":
			syncOmp(port)
		case "grok-build":
			syncGrokBuild(port)
		case "pi":
			syncPi(port)
		case "kimi-code":
			syncKimiCode(port)
		}
	}
}
