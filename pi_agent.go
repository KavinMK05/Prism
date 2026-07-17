package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// piConfigPath returns ~/.pi/agent (cross-platform), the directory containing
// both settings.json and models.json.
func piConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// piSettingsPath returns ~/.pi/agent/settings.json.
func piSettingsPath() string {
	dir := piConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "settings.json")
}

// piModelsPath returns ~/.pi/agent/models.json.
func piModelsPath() string {
	dir := piConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "models.json")
}

// isPiInstalled reports whether Pi is installed: the ~/.pi/agent directory
// exists OR the `pi` binary is on PATH.
func isPiInstalled() bool {
	dir := piConfigDir()
	if dir != "" {
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}
	p, ok := lookupBinary("pi")
	return ok && p != ""
}

// isPiActive reports whether Prism's provider config is present in
// ~/.pi/agent/models.json.
func isPiActive() bool {
	p := piModelsPath()
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
	providers, ok := m["providers"].(map[string]interface{})
	if !ok {
		return false
	}
	// Check for both "prism" (non-Codex) and "prism-codex" providers
	if _, ok := providers["prism"]; ok {
		return true
	}
	if _, ok := providers["prism-codex"]; ok {
		return true
	}
	return false
}

// buildPiModelEntries returns model entries for the Pi models.json provider
// block. Each model gets context/output limits, modalities, reasoning config,
// and thinking level map.
func buildPiModelEntries(remap *ModelRemapping, cfg *Config, codexOnly bool) []interface{} {
	models := make([]interface{}, 0, len(remap.KnownModels))
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
		input := []string{"text"}
		if m.Capabilities != nil && m.Capabilities.Vision {
			input = []string{"text", "image"}
		}

		entry := map[string]interface{}{
			"id":            m.ID,
			"name":          prismManagedTag + " " + humanizeModelID(m.ID),
			"input":         input,
			"contextWindow": ctx,
			"maxTokens":     out,
		}

		if m.Reasoning {
			entry["reasoning"] = true
			// Build thinking level map based on known reasoning efforts.
			// Pi requires explicit level maps for custom providers.
			efforts := m.ReasoningEffort
			if len(efforts) == 0 {
				efforts = []string{"low", "medium", "high"}
			}
			tlm := map[string]interface{}{}
			// Always mark off as null (no way to disable thinking on reasoning models)
			tlm["off"] = nil
			tlm["minimal"] = nil
			for _, level := range efforts {
				tlm[level] = level
			}
			// If "max" is not in efforts but the model supports it, add it
			hasMax := false
			for _, e := range efforts {
				if e == "max" {
					hasMax = true
					break
				}
			}
			if !hasMax {
				tlm["max"] = nil
			}
			entry["thinkingLevelMap"] = tlm
		}

		models = append(models, entry)
	}
	return models
}

// installPiConfig writes provider blocks into ~/.pi/agent/models.json:
// - "prism" with api: openai-completions for non-Codex models (/v1/chat/completions)
// - "prism-codex" with api: openai-responses for Codex OAuth models (/v1/responses)
// It also updates ~/.pi/agent/settings.json to set defaultProvider and
// defaultModel to point to Prism. All other providers and settings are
// preserved. A one-time .prism-backup is kept for both files.
func installPiConfig(port int, remap *ModelRemapping) error {
	modelsPath := piModelsPath()
	settingsPath := piSettingsPath()
	if modelsPath == "" || settingsPath == "" {
		return fmt.Errorf("cannot determine Pi config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	cfg := loadConfig()
	baseURL := "http://localhost:" + fmt.Sprintf("%d", port) + "/v1"

	// ── models.json ──
	models, err := readJSONConfig(modelsPath)
	if err != nil {
		return fmt.Errorf("failed to read Pi models config: %w", err)
	}
	ensureAgentBackup(modelsPath)

	providers, _ := models["providers"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	nonCodexModels := buildPiModelEntries(remap, cfg, false)
	codexModels := buildPiModelEntries(remap, cfg, true)

	// Replace our provider blocks wholesale (clean slate)
	if len(nonCodexModels) > 0 {
		providers["prism"] = map[string]interface{}{
			"baseUrl": baseURL,
			"api":     "openai-completions",
			"apiKey":  "prism",
			"compat": map[string]interface{}{
				"supportsDeveloperRole":   false,
				"supportsReasoningEffort": true,
			},
			"models": nonCodexModels,
		}
	} else {
		delete(providers, "prism")
	}

	if len(codexModels) > 0 {
		providers["prism-codex"] = map[string]interface{}{
			"baseUrl": baseURL,
			"api":     "openai-responses",
			"apiKey":  "prism",
			"models":  codexModels,
		}
	} else {
		delete(providers, "prism-codex")
	}

	models["providers"] = providers

	if err := writeJSONConfig(modelsPath, models); err != nil {
		return fmt.Errorf("failed to write Pi models config: %w", err)
	}

	// ── settings.json ──
	settings, err := readJSONConfig(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to read Pi settings config: %w", err)
	}
	ensureAgentBackup(settingsPath)

	// Set defaultProvider to "prism" (or "prism-codex" if only Codex models exist)
	if len(nonCodexModels) > 0 {
		settings["defaultProvider"] = "prism"
	} else if len(codexModels) > 0 {
		settings["defaultProvider"] = "prism-codex"
	}

	// Set defaultModel to the first known model
	if len(remap.KnownModels) > 0 {
		settings["defaultModel"] = remap.KnownModels[0].ID
	}

	if err := writeJSONConfig(settingsPath, settings); err != nil {
		return fmt.Errorf("failed to write Pi settings config: %w", err)
	}

	return nil
}

// restorePiConfig removes the "prism" and "prism-codex" provider blocks from
// ~/.pi/agent/models.json and clears defaultProvider/defaultModel in
// ~/.pi/agent/settings.json if they pointed at prism, preserving all other
// providers and settings.
func restorePiConfig() error {
	modelsPath := piModelsPath()
	settingsPath := piSettingsPath()
	if modelsPath == "" || settingsPath == "" {
		return fmt.Errorf("cannot determine Pi config path")
	}

	// ── models.json ──
	changed := false
	models, err := readJSONConfig(modelsPath)
	if err != nil {
		return fmt.Errorf("failed to read Pi models config: %w", err)
	}
	if providers, ok := models["providers"].(map[string]interface{}); ok {
		for _, provID := range []string{"prism", "prism-codex"} {
			if _, exists := providers[provID]; exists {
				delete(providers, provID)
				changed = true
			}
		}
		models["providers"] = providers
	}
	if changed {
		if err := writeJSONConfig(modelsPath, models); err != nil {
			return fmt.Errorf("failed to write Pi models config: %w", err)
		}
	}

	// ── settings.json ──
	settings, err := readJSONConfig(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to read Pi settings config: %w", err)
	}
	settingsChanged := false
	for _, key := range []string{"defaultProvider", "defaultModel"} {
		if v, ok := settings[key].(string); ok {
			if v == "prism" || v == "prism-codex" || strings.HasPrefix(v, "prism/") || strings.HasPrefix(v, "prism-codex/") {
				delete(settings, key)
				settingsChanged = true
			}
		}
	}
	if settingsChanged {
		if err := writeJSONConfig(settingsPath, settings); err != nil {
			return fmt.Errorf("failed to write Pi settings config: %w", err)
		}
	}

	return nil
}

// syncPi is called on proxy startup to sync the Pi config when Pi is installed.
// Silently skips when not installed or no models.
func syncPi(port int) {
	if !isPiInstalled() {
		return
	}
	remap := loadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Pi] No models configured, skipping sync")
		return
	}
	if err := installPiConfig(port, remap); err != nil {
		log.Printf("[Pi] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Pi] Synced %d models to ~/.pi/agent/", len(remap.KnownModels))
}
