package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"ollama-proxy/internal/config"
)

// piPrismProviderIDs are the provider blocks Prism writes into
// ~/.pi/agent/models.json. "prism-codex" is the legacy pre-API-split id.
var piPrismProviderIDs = []string{"prism", "prism-responses", "prism-codex"}

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
	for _, provID := range piPrismProviderIDs {
		if _, ok := providers[provID]; ok {
			return true
		}
	}
	return false
}

// piDefaultsMigrationMarker is touched once Prism has cleared the
// defaultProvider/defaultModel values it used to write into settings.json.
// The cleanup must happen exactly once: after that, any defaultProvider or
// defaultModel on disk is the user's own choice (Pi persists /model there) and
// Prism must never touch it again.
func piDefaultsMigrationMarker() string {
	dir := piConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ".prism-defaults-migrated")
}

// piDefaultsMigrationDone reports whether the one-time defaults cleanup has
// already run. An undeterminable path counts as done so a bad HOME never
// causes repeated edits to Pi's settings.
func piDefaultsMigrationDone() bool {
	p := piDefaultsMigrationMarker()
	if p == "" {
		return true
	}
	_, err := os.Stat(p)
	return err == nil
}

func markPiDefaultsMigrationDone() {
	p := piDefaultsMigrationMarker()
	if p == "" {
		return
	}
	_ = os.WriteFile(p, []byte("Prism cleared the defaultProvider/defaultModel it previously wrote.\n"), 0600)
}

// piSettingIsPrismOwned reports whether a defaultProvider/defaultModel value is
// one Prism could have written: a Prism provider id, a "prism/"-qualified
// route, or the route key / bare id of a model Prism registers in models.json.
// Values the user chose that have nothing to do with Prism return false.
func piSettingIsPrismOwned(value string, remap *config.ModelRemapping) bool {
	if value == "" {
		return false
	}
	for _, provID := range piPrismProviderIDs {
		if value == provID || strings.HasPrefix(value, provID+"/") {
			return true
		}
	}
	if remap != nil {
		for _, m := range remap.KnownModels {
			if value == prismModelRouteKey(m) || value == m.ID {
				return true
			}
		}
	}
	return false
}

// piClearPrismDefaults removes Prism-written defaultProvider/defaultModel
// entries from Pi's settings, leaving the user's own choices alone. It reports
// whether settings.json needs to be rewritten.
func piClearPrismDefaults(settings map[string]interface{}, remap *config.ModelRemapping) bool {
	changed := false
	for _, key := range []string{"defaultProvider", "defaultModel"} {
		if v, ok := settings[key].(string); ok && piSettingIsPrismOwned(v, remap) {
			delete(settings, key)
			changed = true
		}
	}
	return changed
}

func usesPiResponses(m config.ModelEntry, cfg *config.Config) bool {
	if m.API == "responses" {
		return true
	}
	if m.API == "chat_completions" {
		return false
	}
	return cfg.IsCodexProviderID(m.Provider)
}

// buildPiModelEntries returns model entries filtered by protocol.
// When wantResponses is true, only responses models are included.
func buildPiModelEntries(remap *config.ModelRemapping, cfg *config.Config, wantResponses bool) []interface{} {
	models := make([]interface{}, 0, len(remap.KnownModels))
	for _, m := range remap.KnownModels {
		if usesPiResponses(m, cfg) != wantResponses {
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

// InstallPiConfig writes Prism provider blocks into ~/.pi/agent/models.json:
// - "prism" with api: openai-completions for chat_completions models
// - "prism-responses" with api: openai-responses for responses models (Zen muse-spark/gpt/grok or Codex)
// Pi's provider api is per-provider (no per-model override), so two providers are required
// when the user mixes protocols. All other providers and settings are preserved.
// defaultProvider/defaultModel in settings.json are never set — the user picks
// their own model, and Prism registers models only.
func InstallPiConfig(port int, remap *config.ModelRemapping) error {
	modelsPath := piModelsPath()
	settingsPath := piSettingsPath()
	if modelsPath == "" || settingsPath == "" {
		return fmt.Errorf("cannot determine Pi config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	cfg := config.Load()
	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"

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

	chatModels := buildPiModelEntries(remap, cfg, false)
	responsesModels := buildPiModelEntries(remap, cfg, true)

	if len(chatModels) > 0 {
		providers["prism"] = map[string]interface{}{
			"baseUrl": baseURL,
			"api":     "openai-completions",
			"apiKey":  "prism",
			"compat": map[string]interface{}{
				"supportsDeveloperRole":   false,
				"supportsReasoningEffort": true,
			},
			"models": chatModels,
		}
	} else {
		delete(providers, "prism")
	}
	if len(responsesModels) > 0 {
		providers["prism-responses"] = map[string]interface{}{
			"baseUrl": baseURL,
			"api":     "openai-responses",
			"apiKey":  "prism",
			"models":  responsesModels,
		}
	} else {
		delete(providers, "prism-responses")
	}
	// Clean up legacy "prism-codex" (pre-API-split)
	delete(providers, "prism-codex")

	models["providers"] = providers

	if err := writeJSONConfig(modelsPath, models); err != nil {
		return fmt.Errorf("failed to write Pi models config: %w", err)
	}

	// ── settings.json ──
	// Prism does not choose a default provider or model for Pi: registering the
	// providers in models.json is the whole job, and Pi keeps its own default.
	// Older Prism versions did write defaultProvider/defaultModel, so clear them
	// once — after that the values belong to the user (Pi persists /model there)
	// and are never touched again.
	if !piDefaultsMigrationDone() {
		settings, err := readJSONConfig(settingsPath)
		if err != nil {
			return fmt.Errorf("failed to read Pi settings config: %w", err)
		}
		if piClearPrismDefaults(settings, remap) {
			ensureAgentBackup(settingsPath)
			if err := writeJSONConfig(settingsPath, settings); err != nil {
				return fmt.Errorf("failed to write Pi settings config: %w", err)
			}
			log.Printf("[Pi] Cleared the Prism-written defaultProvider/defaultModel from settings.json")
		}
		markPiDefaultsMigrationDone()
	}

	return nil
}

// RestorePiConfig removes the "prism", "prism-responses" and legacy "prism-codex"
// provider blocks from ~/.pi/agent/models.json and clears defaultProvider/defaultModel in
// ~/.pi/agent/settings.json when they point at Prism or one of its models,
// preserving all other providers and settings.
func RestorePiConfig() error {
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
		for _, provID := range piPrismProviderIDs {
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
	// The prism providers are gone from models.json, so any default pointing at
	// one of their models is dangling — including a route key like
	// "zen/muse-spark" that carries no "prism" prefix.
	if piClearPrismDefaults(settings, config.LoadModelRemapping()) {
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
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Pi] No models configured, skipping sync")
		return
	}
	if err := InstallPiConfig(port, remap); err != nil {
		log.Printf("[Pi] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Pi] Synced %d models to ~/.pi/agent/", len(remap.KnownModels))
}
