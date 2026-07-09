package main

import (
	"fmt"
	"log"
)

// factoryDroidConfigPath returns ~/.factory/settings.json (cross-platform).
func factoryDroidConfigPath() string { return agentConfigPath("factory-droid") }

// isFactoryDroidInstalled reports whether Factory Droid is installed: the
// ~/.factory/settings.json config file exists OR the `droid` binary is on
// PATH. (Factory Droid may not create its settings.json until first run, so
// the binary check avoids a false "not installed".)
func isFactoryDroidInstalled() bool {
	if isAgentConfigInstalled("factory-droid") {
		return true
	}
	if p, ok := lookupBinary("droid"); ok && p != "" {
		return true
	}
	return false
}
func isFactoryDroidActive() bool { return isAgentActive("factory-droid") }

// buildFactoryDroidModels returns [Prism]-tagged customModels entries, one
// per Prism known model. Codex OAuth models use the "openai" provider type
// (sends to /v1/responses); all others use "generic-chat-completion-api"
// (sends to /v1/chat/completions).
func buildFactoryDroidModels(remap *ModelRemapping, baseURL string, cfg *Config) []interface{} {
	entries := make([]interface{}, 0, len(remap.KnownModels))
	for _, m := range remap.KnownModels {
		noImage := true
		if m.Capabilities != nil && m.Capabilities.Vision {
			noImage = false
		}
		maxOut := m.MaxOutputTokens
		if maxOut == 0 {
			maxOut = 16384
		}
		providerType := "generic-chat-completion-api"
		if cfg.isCodexProviderID(m.Provider) {
			providerType = "openai"
		}
		entry := map[string]interface{}{
			"model":           m.ID,
			"displayName":     prismManagedTag + " " + humanizeModelID(m.ID),
			"baseUrl":         baseURL,
			"apiKey":          "prism",
			"provider":        providerType,
			"maxOutputTokens": maxOut,
			"noImageSupport":  noImage,
		}
		entries = append(entries, entry)
	}
	return entries
}

// installFactoryDroidConfig writes one [Prism]-tagged customModels entry per
// Prism model into ~/.factory/settings.json, preserving all other top-level
// keys and non-Prism customModels entries. A one-time .prism-backup is kept.
func installFactoryDroidConfig(port int, remap *ModelRemapping) error {
	p := factoryDroidConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Factory Droid config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Factory Droid config: %w", err)
	}
	ensureAgentBackup(p)

	// Build set of Codex OAuth provider IDs for per-model provider type selection
	cfg := loadConfig()

	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"
	existing, _ := m["customModels"].([]interface{})
	cleaned, _ := stripPrismModels(existing) // drop prior Prism entries
	cleaned = append(cleaned, buildFactoryDroidModels(remap, baseURL, cfg)...)
	m["customModels"] = cleaned

	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write Factory Droid config: %w", err)
	}
	return nil
}

// restoreFactoryDroidConfig removes all [Prism]-tagged customModels entries,
// preserving any other entries the user added.
func restoreFactoryDroidConfig() error {
	p := factoryDroidConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Factory Droid config path")
	}
	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Factory Droid config: %w", err)
	}
	if existing, ok := m["customModels"].([]interface{}); ok {
		cleaned, removed := stripPrismModels(existing)
		if removed > 0 {
			m["customModels"] = cleaned
			if err := writeJSONConfig(p, m); err != nil {
				return fmt.Errorf("failed to write Factory Droid config: %w", err)
			}
		}
	}
	return nil
}

// syncFactoryDroid is called on proxy startup to sync the Factory Droid config
// when Factory Droid is installed. Silently skips when not installed or no models.
func syncFactoryDroid(port int) {
	if !isFactoryDroidInstalled() {
		return
	}
	remap := loadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Factory Droid] No models configured, skipping sync")
		return
	}
	if err := installFactoryDroidConfig(port, remap); err != nil {
		log.Printf("[Factory Droid] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Factory Droid] Synced %d models to ~/.factory/settings.json", len(remap.KnownModels))
}
