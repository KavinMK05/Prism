package main

import (
	"fmt"
	"log"
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
func claudeCodeConfigPath() string {
	return agentConfigPath("claude-code")
}

func isClaudeCodeInstalled() bool { return isAgentConfigInstalled("claude-code") }
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
// ~/.claude/settings.json, preserving all other top-level settings and env
// entries. A one-time .prism-backup of the original file is kept on first
// install as a safety net.
func installClaudeCodeConfig(port int, tiers map[string]string) error {
	p := claudeCodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Claude Code config path")
	}

	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Claude Code config: %w", err)
	}

	ensureAgentBackup(p)

	env, _ := m["env"].(map[string]interface{})
	if env == nil {
		env = map[string]interface{}{}
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
	m["env"] = env

	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write Claude Code config: %w", err)
	}
	return nil
}

// restoreClaudeCodeConfig removes the Prism-managed env keys from
// ~/.claude/settings.json, preserving all other env entries and settings.
func restoreClaudeCodeConfig() error {
	p := claudeCodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Claude Code config path")
	}
	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Claude Code config: %w", err)
	}
	if env, ok := m["env"].(map[string]interface{}); ok {
		changed := false
		for _, k := range claudeCodeManagedEnvKeys {
			if _, exists := env[k]; exists {
				delete(env, k)
				changed = true
			}
		}
		if changed {
			m["env"] = env
			if err := writeJSONConfig(p, m); err != nil {
				return fmt.Errorf("failed to write Claude Code config: %w", err)
			}
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
	log.Printf("[Claude Code] Synced config to ~/.claude/settings.json")
}
