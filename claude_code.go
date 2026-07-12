package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// claudeCodeManagedEnvKeys are the env keys Prism writes into
// ~/.claude/settings.json to route Claude Code through the Prism proxy.
// Restore removes exactly these keys, preserving any other env entries.
var claudeCodeManagedEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeCodeConfigPath returns ~/.claude/settings.json (cross-platform).
// This is droid's main config file, used for the "is installed" check and
// for reading existing settings. Prism does NOT write env keys here because
// Claude Code watches and rewrites this file, clobbering our entries (see
// claudeCodeLocalConfigPath).
func claudeCodeConfigPath() string {
	return agentConfigPath("claude-code")
}

// claudeCodeLocalConfigPath returns ~/.claude/settings.local.json — the
// local-override file that Claude Code merges on top of settings.json but
// never overwrites. Prism writes env keys here so Claude Code's file watcher
// can't clobber them.
func claudeCodeLocalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.local.json")
}

func isClaudeCodeInstalled() bool {
	// Installed if either settings.json or settings.local.json exists.
	if isAgentConfigInstalled("claude-code") {
		return true
	}
	if p := claudeCodeLocalConfigPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
func isClaudeCodeActive() bool    { return isAgentActive("claude-code") }

// claudeCodeTierModel returns the Prism model id for a tier, falling back to
// the first available Prism model when the tier mapping is unset/empty.
func claudeCodeTierModel(tiers map[string]string, key, fallback string) string {
	if v, ok := tiers[key]; ok && v != "" {
		return v
	}
	return fallback
}

// firstPrismModelID returns the first known model id (or the default model),
// or "" if no models are configured.
func firstPrismModelID() string {
	remap := loadModelRemapping()
	if len(remap.KnownModels) > 0 {
		return remap.KnownModels[0].ID
	}
	return remap.DefaultModel
}

// installClaudeCodeConfig writes the Prism-managed env keys into
// ~/.claude/settings.local.json (the local-override file that Claude Code
// merges on top of settings.json but never overwrites). Env entries from
// settings.json are copied into the local file so they survive the merge,
// then Prism-managed keys are set on top. A one-time .prism-backup of the
// original local file is kept on first install as a safety net.
func installClaudeCodeConfig(port int, tiers map[string]string) error {
	mainPath := claudeCodeConfigPath()
	localPath := claudeCodeLocalConfigPath()
	if localPath == "" || mainPath == "" {
		return fmt.Errorf("cannot determine Claude Code config path")
	}

	// Read existing settings from both files. Env entries from settings.json
	// are merged into the local file so they survive (local's env replaces
	// main's on merge, same as customModels for Factory Droid).
	mainCfg, err := readJSONConfig(mainPath)
	if err != nil {
		return fmt.Errorf("failed to read Claude Code config: %w", err)
	}
	localCfg, err := readJSONConfig(localPath)
	if err != nil {
		return fmt.Errorf("failed to read Claude Code local config: %w", err)
	}
	ensureAgentBackup(localPath)

	// Start with env from local (Prism's write target), then merge in any
	// non-Prism keys from main that aren't already in local.
	env, _ := localCfg["env"].(map[string]interface{})
	if env == nil {
		env = map[string]interface{}{}
	}
	if mainEnv, ok := mainCfg["env"].(map[string]interface{}); ok {
		for k, v := range mainEnv {
			if _, exists := env[k]; !exists {
				env[k] = v
			}
		}
	}
	// Clean slate: remove any prior Prism-managed keys before re-adding.
	for _, k := range claudeCodeManagedEnvKeys {
		delete(env, k)
	}

	first := firstPrismModelID()
	if first == "" {
		return fmt.Errorf("no Prism models configured")
	}

	env["ANTHROPIC_BASE_URL"] = "http://127.0.0.1:" + fmt.Sprintf("%d", port)
	env["ANTHROPIC_AUTH_TOKEN"] = "prism"
	env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = claudeCodeTierModel(tiers, "opus", first)
	env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = claudeCodeTierModel(tiers, "sonnet", first)
	env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = claudeCodeTierModel(tiers, "haiku", first)
	env["CLAUDE_CODE_SUBAGENT_MODEL"] = claudeCodeTierModel(tiers, "subagent", first)
	localCfg["env"] = env

	if err := writeJSONConfig(localPath, localCfg); err != nil {
		return fmt.Errorf("failed to write Claude Code local config: %w", err)
	}

	// Also strip stale Prism env keys from settings.json (written by older
	// Prism versions that wrote there directly). This is a one-time migration.
	if mainEnv, ok := mainCfg["env"].(map[string]interface{}); ok {
		changed := false
		for _, k := range claudeCodeManagedEnvKeys {
			if _, exists := mainEnv[k]; exists {
				delete(mainEnv, k)
				changed = true
			}
		}
		if changed {
			mainCfg["env"] = mainEnv
			_ = writeJSONConfig(mainPath, mainCfg) // best-effort; Claude Code may rewrite
		}
	}
	return nil
}

// restoreClaudeCodeConfig removes the Prism-managed env keys from both
// ~/.claude/settings.local.json (Prism's write target) and
// ~/.claude/settings.json (stale entries from older Prism versions),
// preserving all other env entries and settings.
func restoreClaudeCodeConfig() error {
	localPath := claudeCodeLocalConfigPath()
	mainPath := claudeCodeConfigPath()
	if localPath == "" || mainPath == "" {
		return fmt.Errorf("cannot determine Claude Code config path")
	}

	// Strip Prism entries from settings.local.json (Prism's write target).
	localCfg, err := readJSONConfig(localPath)
	if err != nil {
		return fmt.Errorf("failed to read Claude Code local config: %w", err)
	}
	if env, ok := localCfg["env"].(map[string]interface{}); ok {
		changed := false
		for _, k := range claudeCodeManagedEnvKeys {
			if _, exists := env[k]; exists {
				delete(env, k)
				changed = true
			}
		}
		if changed {
			localCfg["env"] = env
			if err := writeJSONConfig(localPath, localCfg); err != nil {
				return fmt.Errorf("failed to write Claude Code local config: %w", err)
			}
		}
	}

	// Also clean stale Prism entries from settings.json (written by older
	// Prism versions that wrote there directly). Best-effort; Claude Code
	// may rewrite this file.
	mainCfg, err := readJSONConfig(mainPath)
	if err != nil {
		return nil // can't read main config; nothing to clean
	}
	if env, ok := mainCfg["env"].(map[string]interface{}); ok {
		changed := false
		for _, k := range claudeCodeManagedEnvKeys {
			if _, exists := env[k]; exists {
				delete(env, k)
				changed = true
			}
		}
		if changed {
			mainCfg["env"] = env
			_ = writeJSONConfig(mainPath, mainCfg)
		}
	}
	return nil
}

// syncClaudeCode is called on proxy startup to sync the Claude Code config
// when Claude Code is installed. Silently skips when not installed or when no
// Prism models are configured.
func syncClaudeCode(port int) {
	if !isClaudeCodeInstalled() {
		return
	}
	if firstPrismModelID() == "" {
		log.Printf("[Claude Code] No models configured, skipping sync")
		return
	}
	cfg := loadConfig()
	tiers := map[string]string{}
	if cfg.AgentIntegrations != nil && cfg.AgentIntegrations.ClaudeCodeTiers != nil {
		tiers = cfg.AgentIntegrations.ClaudeCodeTiers
	}
	if err := installClaudeCodeConfig(port, tiers); err != nil {
		log.Printf("[Claude Code] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Claude Code] Synced config to ~/.claude/settings.local.json")
}
