package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ompProviderID is the provider key used for the Prism provider in OMP's
// models.yml. Codex OAuth models go under a separate "-codex" provider block
// so they use the openai-responses transport.
const ompProviderID = "prism"

// ompConfigPath returns ~/.omp/agent/models.yml (cross-platform).
func ompConfigPath() string { return agentConfigPath("omp") }

// isOmpInstalled reports whether Oh My Pi is installed: the models.yml config
// file exists OR the `omp` binary is on PATH. (OMP may not create its config
// file until first run, so the binary check avoids a false "not installed".)
func isOmpInstalled() bool {
	if isAgentConfigInstalled("omp") {
		return true
	}
	// OMP may not create ~/.omp/agent/models.yml until first run, so fall
	// back to the binary. lookupBinary also searches the install dirs GUI
	// apps don't inherit (Homebrew, ~/.bun/bin, mise, …) — see comment there.
	if p, ok := lookupBinary("omp"); ok && p != "" {
		return true
	}
	return false
}

func isOmpActive() bool { return isAgentActive("omp") }

// readYAMLConfig reads and parses a YAML config file into a generic map.
// Returns an empty (non-nil) map if the file does not exist or is empty.
func readYAMLConfig(path string) (map[string]interface{}, error) {
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
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// writeYAMLConfig writes a generic map to a YAML file with 2-space indent,
// creating parent directories as needed.
func writeYAMLConfig(path string, m map[string]interface{}) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// buildOmpModelEntries returns model entries for a single OMP provider block.
// When codexOnly is true, only Codex OAuth models are included (routed via
// openai-responses); otherwise only non-Codex models (openai-completions).
func buildOmpModelEntries(remap *ModelRemapping, cfg *Config, codexOnly bool) []interface{} {
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
			"input":          input,
			"contextWindow":  ctx,
			"maxTokens":      out,
		}
		if m.Reasoning {
			entry["reasoning"] = true
		}
		models = append(models, entry)
	}
	return models
}

// installOmpConfig writes provider blocks into ~/.omp/agent/models.yml:
// - "prism" with api: openai-completions for non-Codex models (/v1/chat/completions)
// - "prism-codex" with api: openai-responses for Codex OAuth models (/v1/responses)
// All other providers and top-level keys are preserved. A one-time .prism-backup is kept.
func installOmpConfig(port int, remap *ModelRemapping) error {
	p := ompConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Oh My Pi config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	m, err := readYAMLConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Oh My Pi config: %w", err)
	}
	ensureAgentBackup(p)

	cfg := loadConfig()

	providers, _ := m["providers"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	baseURL := "http://localhost:" + fmt.Sprintf("%d", port) + "/v1"
	nonCodexModels := buildOmpModelEntries(remap, cfg, false)
	codexModels := buildOmpModelEntries(remap, cfg, true)

	// Replace our provider blocks wholesale (clean slate)
	if len(nonCodexModels) > 0 {
		providers[ompProviderID] = map[string]interface{}{
			"baseUrl": baseURL,
			"apiKey":  "prism",
			"api":     "openai-completions",
			"auth":    "apiKey",
			"models":  nonCodexModels,
		}
	} else {
		delete(providers, ompProviderID)
	}

	if len(codexModels) > 0 {
		providers[ompProviderID+"-codex"] = map[string]interface{}{
			"baseUrl": baseURL,
			"apiKey":  "prism",
			"api":     "openai-responses",
			"auth":    "apiKey",
			"models":  codexModels,
		}
	} else {
		delete(providers, ompProviderID+"-codex")
	}

	m["providers"] = providers

	if err := writeYAMLConfig(p, m); err != nil {
		return fmt.Errorf("failed to write Oh My Pi config: %w", err)
	}
	return nil
}

// restoreOmpConfig removes the "prism" and "prism-codex" provider blocks from
// ~/.omp/agent/models.yml, preserving all other providers and settings.
func restoreOmpConfig() error {
	p := ompConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Oh My Pi config path")
	}
	m, err := readYAMLConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Oh My Pi config: %w", err)
	}
	changed := false
	if providers, ok := m["providers"].(map[string]interface{}); ok {
		for _, provID := range []string{ompProviderID, ompProviderID + "-codex"} {
			if _, exists := providers[provID]; exists {
				delete(providers, provID)
				changed = true
			}
		}
		m["providers"] = providers
	}
	if changed {
		if err := writeYAMLConfig(p, m); err != nil {
			return fmt.Errorf("failed to write Oh My Pi config: %w", err)
		}
	}
	return nil
}

// syncOmp is called on proxy startup to sync the Oh My Pi config when
// OMP is installed. Silently skips when not installed or no models.
func syncOmp(port int) {
	if !isOmpInstalled() {
		return
	}
	remap := loadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Oh My Pi] No models configured, skipping sync")
		return
	}
	if err := installOmpConfig(port, remap); err != nil {
		log.Printf("[Oh My Pi] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Oh My Pi] Synced %d models to ~/.omp/agent/models.yml", len(remap.KnownModels))
}
