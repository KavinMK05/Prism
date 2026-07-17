package main

import (
	"fmt"
	"log"
	"strings"
)

const opencodeProviderID = "prism"

// opencodeConfigPath returns ~/.config/opencode/opencode.json (cross-platform).
func opencodeConfigPath() string { return agentConfigPath("opencode") }

// isOpencodeInstalled reports whether OpenCode is installed: the config file
// exists OR the `opencode` binary is on PATH. (OpenCode may not create its
// config file until first run, so the binary check avoids a false "not
// installed" when the user clearly has OpenCode.)
func isOpencodeInstalled() bool {
	if isAgentConfigInstalled("opencode") {
		return true
	}
	// OpenCode may not create its config file until first run, so fall back
	// to the binary. lookupBinary searches install dirs GUI apps don't
	// inherit (Homebrew, ~/.bun/bin, mise, …) — see comment there.
	if p, ok := lookupBinary("opencode"); ok && p != "" {
		return true
	}
	return false
}

func isOpencodeActive() bool { return isAgentActive("opencode") }

// buildOpencodeModels splits models into two provider blocks:
// - "prism" with @ai-sdk/openai-compatible for non-Codex models (sends to /v1/chat/completions)
// - "prism-codex" with @ai-sdk/openai for Codex OAuth models (sends to /v1/responses)
// Returns the provider blocks to merge into the config.

// buildOpencodeModelEntries returns model entries for a single provider block.
func buildOpencodeModelEntries(remap *ModelRemapping, cfg *Config, codexOnly bool) map[string]interface{} {
	models := map[string]interface{}{}
	for _, m := range remap.KnownModels {
		isCodex := cfg.isCodexProviderID(m.Provider)
		if codexOnly != isCodex {
			continue
		}
		ctx := m.ContextLength
		if ctx == 0 {
			ctx = 128000
		}
		out := m.MaxOutputTokens
		if out == 0 {
			out = 16384
		}
		models[m.ID] = map[string]interface{}{
			"name": prismManagedTag + " " + humanizeModelID(m.ID),
			"limit": map[string]interface{}{
				"context": ctx,
				"output":  out,
			},
		}
	}
	return models
}

// installOpencodeConfig writes provider blocks into ~/.config/opencode/opencode.json:
// - "prism" with @ai-sdk/openai-compatible for non-Codex models (/v1/chat/completions)
// - "prism-codex" with @ai-sdk/openai for Codex OAuth models (/v1/responses)
// All other providers and top-level keys are preserved. A one-time .prism-backup is kept.
func installOpencodeConfig(port int, remap *ModelRemapping) error {
	p := opencodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine OpenCode config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read OpenCode config: %w", err)
	}
	ensureAgentBackup(p)

	// Build set of Codex OAuth provider IDs
	cfg := loadConfig()

	providers, _ := m["provider"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	baseURL := "http://localhost:" + fmt.Sprintf("%d", port) + "/v1"
	nonCodexModels := buildOpencodeModelEntries(remap, cfg, false)
	codexModels := buildOpencodeModelEntries(remap, cfg, true)

	// Replace our provider blocks wholesale (clean slate)
	if len(nonCodexModels) > 0 {
		providers[opencodeProviderID] = map[string]interface{}{
			"npm":  "@ai-sdk/openai-compatible",
			"name": "Prism",
			"options": map[string]interface{}{
				"baseURL": baseURL,
				"apiKey":  "prism",
			},
			"models": nonCodexModels,
		}
	} else {
		delete(providers, opencodeProviderID)
	}

	if len(codexModels) > 0 {
		providers[opencodeProviderID+"-codex"] = map[string]interface{}{
			"npm":  "@ai-sdk/openai",
			"name": "Prism Codex",
			"options": map[string]interface{}{
				"baseURL": baseURL,
				"apiKey":  "prism",
			},
			"models": codexModels,
		}
	} else {
		delete(providers, opencodeProviderID+"-codex")
	}

	m["provider"] = providers

	// Default model = prism/<first non-Codex model> or prism-codex/<first Codex model>
	if len(remap.KnownModels) > 0 {
		first := remap.KnownModels[0]
		if cfg.isCodexProviderID(first.Provider) {
			m["model"] = opencodeProviderID + "-codex/" + first.ID
		} else {
			m["model"] = opencodeProviderID + "/" + first.ID
		}
	}

	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write OpenCode config: %w", err)
	}
	return nil
}

// restoreOpencodeConfig removes the "prism" and "prism-codex" provider blocks
// and clears the top-level "model"/"small_model" keys if they pointed at a
// prism/ or prism-codex/ model, preserving all other providers and settings.
func restoreOpencodeConfig() error {
	p := opencodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine OpenCode config path")
	}
	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read OpenCode config: %w", err)
	}
	changed := false
	if providers, ok := m["provider"].(map[string]interface{}); ok {
		for _, provID := range []string{opencodeProviderID, opencodeProviderID + "-codex"} {
			if _, exists := providers[provID]; exists {
				delete(providers, provID)
				changed = true
			}
		}
		m["provider"] = providers
	}
	// Clear default model keys only if they reference a prism provider.
	for _, key := range []string{"model", "small_model"} {
		if v, ok := m[key].(string); ok && (strings.HasPrefix(v, opencodeProviderID+"/") || strings.HasPrefix(v, opencodeProviderID+"-codex/")) {
			delete(m, key)
			changed = true
		}
	}
	if changed {
		if err := writeJSONConfig(p, m); err != nil {
			return fmt.Errorf("failed to write OpenCode config: %w", err)
		}
	}
	return nil
}

// syncOpencode is called on proxy startup to sync the OpenCode config when
// OpenCode is installed. Silently skips when not installed or no models.
func syncOpencode(port int) {
	if !isOpencodeInstalled() {
		return
	}
	remap := loadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[OpenCode] No models configured, skipping sync")
		return
	}
	if err := installOpencodeConfig(port, remap); err != nil {
		log.Printf("[OpenCode] Failed to sync config: %v", err)
		return
	}
	log.Printf("[OpenCode] Synced %d models to ~/.config/opencode/opencode.json", len(remap.KnownModels))
}
