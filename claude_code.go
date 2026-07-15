package main

import (
	"fmt"
	"log"
	"os"
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

// claudeCodeLocalConfigPath returns the user settings file that Claude Code
// actually reads. Claude Code has no user-level settings.local.json — that
// file is project-scoped (.claude/settings.local.json). The real user-level
// config is ~/.claude/settings.json, which is where Prism must write the
// ANTHROPIC_* env keys so Claude Code picks them up. The proxy re-syncs these
// keys on every startup (syncClaudeCode), so any rewrite by Claude Code's file
// watcher is recovered automatically.
func claudeCodeLocalConfigPath() string {
	return agentConfigPath("claude-code")
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

// installClaudeCodeConfig writes the Prism-managed env keys into the user
// settings file ~/.claude/settings.json so Claude Code actually reads them.
// Any non-Prism env entries are preserved, and prior Prism-managed keys are
// removed before re-adding. A one-time .prism-backup of the original file is
// kept on first install as a safety net. The proxy re-syncs these keys on
// every startup (syncClaudeCode), so any rewrite by Claude Code's file watcher
// is recovered automatically.
func installClaudeCodeConfig(port int, tiers map[string]string) error {
	path := claudeCodeConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Claude Code config path")
	}

	cfg, err := readJSONConfig(path)
	if err != nil {
		return fmt.Errorf("failed to read Claude Code config: %w", err)
	}
	ensureAgentBackup(path)

	// Preserve any existing non-Prism env entries; remove stale Prism keys.
	env, _ := cfg["env"].(map[string]interface{})
	if env == nil {
		env = map[string]interface{}{}
	}
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
	cfg["env"] = env

	if err := writeJSONConfig(path, cfg); err != nil {
		return fmt.Errorf("failed to write Claude Code config: %w", err)
	}
	return nil
}

// restoreClaudeCodeConfig removes the Prism-managed env keys from
// ~/.claude/settings.json, preserving all other env entries and settings.
func restoreClaudeCodeConfig() error {
	path := claudeCodeConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Claude Code config path")
	}
	cfg, err := readJSONConfig(path)
	if err != nil {
		return fmt.Errorf("failed to read Claude Code config: %w", err)
	}
	env, ok := cfg["env"].(map[string]interface{})
	if !ok {
		return nil // no env block; nothing to clean
	}
	changed := false
	for _, k := range claudeCodeManagedEnvKeys {
		if _, exists := env[k]; exists {
			delete(env, k)
			changed = true
		}
	}
	if changed {
		cfg["env"] = env
		if err := writeJSONConfig(path, cfg); err != nil {
			return fmt.Errorf("failed to write Claude Code config: %w", err)
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
