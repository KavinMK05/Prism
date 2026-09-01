package agents

import (
	"fmt"
	"log"
	"os"

	"ollama-proxy/internal/config"
)

const opencodeProviderID = "prism"

// opencodeConfigPath returns ~/.config/opencode/opencode.json (cross-platform).
func opencodeConfigPath() string { return agentConfigPath("opencode") }

// ensureOpencodeConfigFile creates an empty ~/.config/opencode/opencode.json
// when the opencode binary is installed but the config file doesn't exist yet
// (OpenCode only creates it on first run, which blocked Prism's setup gate for
// new users). No provider data is written here — InstallOpencodeConfig fills
// in the prism blocks when the user clicks Setup.
func ensureOpencodeConfigFile() {
	p := opencodeConfigPath()
	if p == "" {
		return
	}
	if _, err := os.Stat(p); err == nil {
		return // already exists; never touch user content here
	}
	if bin, ok := lookupBinary("opencode"); !ok || bin == "" {
		return // OpenCode not installed; don't litter the disk
	}
	if err := writeJSONConfig(p, map[string]interface{}{}); err != nil {
		log.Printf("[OpenCode] Failed to create empty config: %v", err)
		return
	}
	log.Printf("[OpenCode] Created empty config at %s (setup pending)", p)
}

// isOpencodeInstalled reports whether OpenCode is installed: the config file
// exists OR the `opencode` binary is on PATH. When only the binary is found,
// an empty opencode.json is created so Prism's setup gate passes; provider
// data is written later by InstallOpencodeConfig on explicit setup.
func isOpencodeInstalled() bool {
	if isAgentConfigInstalled("opencode") {
		return true
	}
	// OpenCode may not create its config file until first run, so fall back
	// to the binary. lookupBinary searches install dirs GUI apps don't
	// inherit (Homebrew, ~/.bun/bin, mise, …) — see comment there.
	if p, ok := lookupBinary("opencode"); ok && p != "" {
		ensureOpencodeConfigFile()
		return true
	}
	return false
}

func isOpencodeActive() bool { return IsAgentActive("opencode") }

// usesOpencodeResponses reports whether a model should use the Responses API (Zen muse-spark/gpt/grok
// or Codex OAuth). Centralizes the m.API == "responses" check with the legacy IsCodex fallback.
func usesOpencodeResponses(m config.ModelEntry, cfg *config.Config) bool {
	if m.API == "responses" {
		return true
	}
	if m.API == "chat_completions" {
		return false
	}
	// Empty/missing API (pre-migration) -> infer from provider
	return cfg.IsCodexProviderID(m.Provider)
}

// buildOpencodeModelEntries returns model entries filtered by desired protocol.
// When wantResponses is true, only models with API=="responses" are included; otherwise only chat.
func buildOpencodeModelEntries(remap *config.ModelRemapping, cfg *config.Config, wantResponses bool) map[string]interface{} {
	models := map[string]interface{}{}
	for _, m := range remap.KnownModels {
		if usesOpencodeResponses(m, cfg) != wantResponses {
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
		input := []string{"text"}
		if m.Capabilities != nil && m.Capabilities.Vision {
			input = append(input, "image")
		}
		routeKey := prismModelRouteKey(m)
		entry := map[string]interface{}{
			"name": prismModelDisplayName(cfg, m),
			"limit": map[string]interface{}{
				"context": ctx,
				"output":  out,
			},
			"modalities": map[string]interface{}{
				"input":  input,
				"output": []string{"text"},
			},
		}
		if m.Reasoning {
			efforts := m.ReasoningEffort
			if len(efforts) == 0 {
				efforts = []string{"low", "medium", "high"}
			}
			variants := map[string]interface{}{}
			for _, level := range efforts {
				variants[level] = map[string]interface{}{"reasoningEffort": level}
			}
			entry["variants"] = variants
		}
		models[routeKey] = entry
	}
	return models
}

// InstallOpencodeConfig writes Prism provider blocks into ~/.config/opencode/opencode.json:
// - "prism" with @ai-sdk/openai-compatible for chat_completions models (/v1/chat/completions)
// - "prism-responses" with @ai-sdk/openai for responses models (/v1/responses, e.g. Zen muse-spark/gpt/grok or Codex OAuth)
// OpenCode's provider model is per-provider (no per-model `api` override), so two providers are
// required when the user mixes protocols. When all models share one protocol only that provider is written.
// All other providers and top-level keys are preserved. A one-time .prism-backup is kept.
func InstallOpencodeConfig(port int, remap *config.ModelRemapping) error {
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

	cfg := config.Load()

	providers, _ := m["provider"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"
	options := map[string]interface{}{
		"baseURL": baseURL,
		"apiKey":  "prism",
		"headers": map[string]interface{}{
			"X-Client-Name": "OpenCode",
		},
	}
	chatModels := buildOpencodeModelEntries(remap, cfg, false)
	responsesModels := buildOpencodeModelEntries(remap, cfg, true)

	if len(chatModels) > 0 {
		providers[opencodeProviderID] = map[string]interface{}{
			"npm":     "@ai-sdk/openai-compatible",
			"name":    "Prism",
			"options": options,
			"models":  chatModels,
		}
	} else {
		delete(providers, opencodeProviderID)
	}
	if len(responsesModels) > 0 {
		providers[opencodeProviderID+"-responses"] = map[string]interface{}{
			"npm":     "@ai-sdk/openai",
			"name":    "Prism Responses",
			"options": options,
			"models":  responsesModels,
		}
	} else {
		delete(providers, opencodeProviderID+"-responses")
	}
	// Clean up legacy "prism-codex" (pre-API-split) if present
	delete(providers, opencodeProviderID+"-codex")

	m["provider"] = providers

	// The top-level "model" key is left untouched: the user's default model
	// choice is theirs to make, Prism never writes or clears it.

	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write OpenCode config: %w", err)
	}
	return nil
}

// RestoreOpencodeConfig removes the "prism", "prism-responses" and legacy "prism-codex"
// provider blocks, preserving all other providers and settings.
func RestoreOpencodeConfig() error {
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
		for _, provID := range []string{opencodeProviderID, opencodeProviderID + "-responses", opencodeProviderID + "-codex"} {
			if _, exists := providers[provID]; exists {
				delete(providers, provID)
				changed = true
			}
		}
		m["provider"] = providers
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
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[OpenCode] No models configured, skipping sync")
		return
	}
	if err := InstallOpencodeConfig(port, remap); err != nil {
		log.Printf("[OpenCode] Failed to sync config: %v", err)
		return
	}
	log.Printf("[OpenCode] Synced %d models to ~/.config/opencode/opencode.json", len(remap.KnownModels))
}
