package main

import (
	"fmt"
	"log"
)

// zcodeProviderID is the fixed UUID key used for the Prism provider in
// ZCode's config.json, matching the ID ZCode assigns to custom providers.
const zcodeProviderID = "90063b74-3eb7-4871-963d-86ce200d4d97"

// zcodeConfigPath returns ~/.zcode/v2/config.json (cross-platform).
func zcodeConfigPath() string { return agentConfigPath("zcode") }

func isZcodeInstalled() bool { return isAgentConfigInstalled("zcode") }
func isZcodeActive() bool    { return isAgentActive("zcode") }

// buildZcodeModels returns model entries for the ZCode provider block.
// Each model gets context/output limits, modalities, and optional reasoning config.
func buildZcodeModels(remap *ModelRemapping, cfg *Config) map[string]interface{} {
	models := map[string]interface{}{}
	for _, m := range remap.KnownModels {
		ctx := m.ContextLength
		if ctx == 0 {
			ctx = 128000
		}
		out := m.MaxOutputTokens
		if out == 0 {
			out = 16384
		}
		inputModalities := []string{"text"}
		// Advertise vision input when the model reports image support, so ZCode
		// exposes image attachments for multimodal models.
		if m.Capabilities != nil && m.Capabilities.Vision {
			inputModalities = append(inputModalities, "image")
		}
		entry := map[string]interface{}{
			// `name` is the model's API slug (used in requests), matching ZCode's
			// own convention of storing the lowercased model id here.
			"name": m.ID,
			"limit": map[string]interface{}{
				"context": ctx,
				"output":  out,
			},
			"modalities": map[string]interface{}{
				"input":  inputModalities,
				"output": []string{"text"},
			},
		}
		if m.Reasoning {
			variants := m.ReasoningEffort
			if len(variants) == 0 {
				variants = []string{"low", "medium", "high"}
			}
			entry["reasoning"] = map[string]interface{}{
				"enabled":        true,
				"variants":       variants,
				"defaultVariant": "high",
			}
		}
		models[m.ID] = entry
	}
	return models
}

// installZcodeConfig writes the "prism" provider block into ~/.zcode/v2/config.json.
// All other providers and top-level keys are preserved. A one-time .prism-backup is kept.
func installZcodeConfig(port int, remap *ModelRemapping) error {
	p := zcodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine ZCode config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read ZCode config: %w", err)
	}
	ensureAgentBackup(p)

	providers, _ := m["provider"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) // root; ZCode appends /v1/messages for anthropic kind
	models := buildZcodeModels(remap, loadConfig())

	providers[zcodeProviderID] = map[string]interface{}{
		"name": "Prism",
		"kind": "anthropic", // was "openai-compatible" — lets ZCode use Anthropic server tools (web_search/web_fetch) that Prism emulates
		"options": map[string]interface{}{
			"baseURL":        baseURL,
			"apiKey":         "prism",
			"apiKeyRequired": true,
		},
		"source": "custom",
		"models": models,
	}

	m["provider"] = providers

	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write ZCode config: %w", err)
	}
	return nil
}

// restoreZcodeConfig removes the "prism" provider block from ~/.zcode/v2/config.json,
// preserving all other providers and settings.
func restoreZcodeConfig() error {
	p := zcodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine ZCode config path")
	}
	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read ZCode config: %w", err)
	}
	changed := false
	if providers, ok := m["provider"].(map[string]interface{}); ok {
		if _, exists := providers[zcodeProviderID]; exists {
			delete(providers, zcodeProviderID)
			changed = true
		}
		m["provider"] = providers
	}
	if changed {
		if err := writeJSONConfig(p, m); err != nil {
			return fmt.Errorf("failed to write ZCode config: %w", err)
		}
	}
	return nil
}

// syncZcode is called on proxy startup to sync the ZCode config when
// ZCode is installed. Silently skips when not installed or no models.
func syncZcode(port int) {
	if !isZcodeInstalled() {
		return
	}
	remap := loadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[ZCode] No models configured, skipping sync")
		return
	}
	if err := installZcodeConfig(port, remap); err != nil {
		log.Printf("[ZCode] Failed to sync config: %v", err)
		return
	}
	log.Printf("[ZCode] Synced %d models to ~/.zcode/v2/config.json", len(remap.KnownModels))
}
