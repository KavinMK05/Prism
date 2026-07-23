package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// factoryDroidConfigPath returns ~/.factory/settings.json — droid's main
// config file, used for the "is installed" check only. Prism does NOT write
// to this file because droid watches it and re-saves on change, clobbering
// our entries (see factoryDroidLocalConfigPath).
func factoryDroidConfigPath() string { return agentConfigPath("factory-droid") }

// factoryDroidLocalConfigPath returns ~/.factory/settings.local.json — the
// local-override file that droid merges on top of settings.json but never
// overwrites. Prism writes customModels here so droid's file watcher can't
// clobber them. See: https://docs.factory.ai/cli/configuration/settings
func factoryDroidLocalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".factory", "settings.local.json")
}

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
// Prism model into ~/.factory/settings.local.json (the local-override file
// that droid merges on top of settings.json but never overwrites). Non-Prism
// customModels from both settings.json and settings.local.json are preserved.
// A one-time .prism-backup of settings.local.json is kept.
func installFactoryDroidConfig(port int, remap *ModelRemapping) error {
	localPath := factoryDroidLocalConfigPath()
	mainPath := factoryDroidConfigPath()
	if localPath == "" || mainPath == "" {
		return fmt.Errorf("cannot determine Factory Droid config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	// Read droid's main settings.json (droid-managed; may have user's
	// non-Prism customModels) and the local-override file (Prism-managed).
	mainCfg, err := readJSONConfig(mainPath)
	if err != nil {
		return fmt.Errorf("failed to read Factory Droid config: %w", err)
	}
	localCfg, err := readJSONConfig(localPath)
	if err != nil {
		return fmt.Errorf("failed to read Factory Droid local config: %w", err)
	}
	ensureAgentBackup(localPath)

	cfg := loadConfig()
	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"

	// Collect non-Prism customModels from both files so they survive the
	// merge (settings.local.json's array replaces settings.json's on merge).
	mainExisting, _ := mainCfg["customModels"].([]interface{})
	localExisting, _ := localCfg["customModels"].([]interface{})
	mainCleaned, _ := stripPrismModels(mainExisting)
	localCleaned, _ := stripPrismModels(localExisting)
	// Deduplicate non-Prism entries by "model" field. Without this, entries
	// copied from settings.json into settings.local.json on a prior sync are
	// re-read from both files on every sync, accumulating duplicates.
	seen := map[string]bool{}
	deduped := make([]interface{}, 0, len(mainCleaned)+len(localCleaned))
	for _, entry := range append(append([]interface{}{}, mainCleaned...), localCleaned...) {
		key := customModelKey(entry)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, entry)
	}
	deduped = append(deduped, buildFactoryDroidModels(remap, baseURL, cfg)...)
	localCfg["customModels"] = deduped

	if err := writeJSONConfig(localPath, localCfg); err != nil {
		return fmt.Errorf("failed to write Factory Droid local config: %w", err)
	}
	return nil
}

// restoreFactoryDroidConfig removes all [Prism]-tagged customModels entries
// from ~/.factory/settings.local.json, preserving any other entries the user
// added. Also strips any stale Prism entries that older Prism versions may
// have written directly into settings.json.
func restoreFactoryDroidConfig() error {
	localPath := factoryDroidLocalConfigPath()
	mainPath := factoryDroidConfigPath()
	if localPath == "" || mainPath == "" {
		return fmt.Errorf("cannot determine Factory Droid config path")
	}

	// Strip Prism entries from settings.local.json (Prism's write target).
	localCfg, err := readJSONConfig(localPath)
	if err != nil {
		return fmt.Errorf("failed to read Factory Droid local config: %w", err)
	}
	if existing, ok := localCfg["customModels"].([]interface{}); ok {
		cleaned, removed := stripPrismModels(existing)
		if removed > 0 {
			localCfg["customModels"] = cleaned
			if err := writeJSONConfig(localPath, localCfg); err != nil {
				return fmt.Errorf("failed to write Factory Droid local config: %w", err)
			}
		}
	}

	// Also clean stale Prism entries from settings.json (written by older
	// Prism versions that wrote there directly). This is a one-time migration.
	mainCfg, err := readJSONConfig(mainPath)
	if err != nil {
		return nil // can't read main config; nothing to clean
	}
	if existing, ok := mainCfg["customModels"].([]interface{}); ok {
		cleaned, removed := stripPrismModels(existing)
		if removed > 0 {
			mainCfg["customModels"] = cleaned
			_ = writeJSONConfig(mainPath, mainCfg) // best-effort; droid may rewrite
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
	log.Printf("[Factory Droid] Synced %d models to ~/.factory/settings.local.json", len(remap.KnownModels))
}
