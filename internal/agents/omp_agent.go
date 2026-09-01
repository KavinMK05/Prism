package agents

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"log"
	"os"
	"path/filepath"
	"strings"

	"ollama-proxy/internal/config"
)

// ompProviderID is the provider key used for the Prism provider in OMP's
// models.yml. Models with API=="responses" (Zen muse-spark/gpt/grok or Codex
// OAuth) go under a separate "-responses" provider block so they use the
// openai-responses transport; chat models stay under the base provider.
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

func isOmpActive() bool { return IsAgentActive("omp") }

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

func usesOmpResponses(m config.ModelEntry, cfg *config.Config) bool {
	if m.API == "responses" {
		return true
	}
	if m.API == "chat_completions" {
		return false
	}
	return cfg.IsCodexProviderID(m.Provider)
}

// buildOmpModelEntries returns model entries filtered by protocol.
func buildOmpModelEntries(remap *config.ModelRemapping, cfg *config.Config, wantResponses bool) []interface{} {
	models := make([]interface{}, 0, len(remap.KnownModels))
	for _, m := range remap.KnownModels {
		if usesOmpResponses(m, cfg) != wantResponses {
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
		routeKey := prismModelRouteKey(m)
		entry := map[string]interface{}{
			"id":            routeKey,
			"name":          prismModelDisplayName(cfg, m),
			"input":         input,
			"contextWindow": ctx,
			"maxTokens":     out,
		}
		if m.Reasoning {
			entry["reasoning"] = true
		}
		models = append(models, entry)
	}
	return models
}

// InstallOmpConfig writes Prism provider blocks into ~/.omp/agent/models.yml:
// - "prism" with api: openai-completions for chat_completions models
// - "prism-responses" with api: openai-responses for responses models (Zen muse-spark/gpt/grok or Codex)
// OMP's api is per-provider, so two providers are required when mixing protocols.
func InstallOmpConfig(port int, remap *config.ModelRemapping) error {
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

	cfg := config.Load()

	providers, _ := m["providers"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"
	chatModels := buildOmpModelEntries(remap, cfg, false)
	responsesModels := buildOmpModelEntries(remap, cfg, true)

	if len(chatModels) > 0 {
		providers[ompProviderID] = map[string]interface{}{
			"baseUrl": baseURL,
			"apiKey":  "prism",
			"api":     "openai-completions",
			"auth":    "apiKey",
			"models":  chatModels,
		}
	} else {
		delete(providers, ompProviderID)
	}
	if len(responsesModels) > 0 {
		providers[ompProviderID+"-responses"] = map[string]interface{}{
			"baseUrl": baseURL,
			"apiKey":  "prism",
			"api":     "openai-responses",
			"auth":    "apiKey",
			"models":  responsesModels,
		}
	} else {
		delete(providers, ompProviderID+"-responses")
	}
	delete(providers, ompProviderID+"-codex")

	m["providers"] = providers

	if err := writeYAMLConfig(p, m); err != nil {
		return fmt.Errorf("failed to write Oh My Pi config: %w", err)
	}
	return nil
}

// RestoreOmpConfig removes the "prism", "prism-responses" and legacy "prism-codex" provider blocks from
// ~/.omp/agent/models.yml, preserving all other providers and settings.
func RestoreOmpConfig() error {
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
		for _, provID := range []string{ompProviderID, ompProviderID + "-responses", ompProviderID + "-codex"} {
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
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Oh My Pi] No models configured, skipping sync")
		return
	}
	if err := InstallOmpConfig(port, remap); err != nil {
		log.Printf("[Oh My Pi] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Oh My Pi] Synced %d models to ~/.omp/agent/models.yml", len(remap.KnownModels))
}
