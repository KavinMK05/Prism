package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	codexManagedBegin = "# >>> prism managed >>>"
	codexManagedEnd   = "# <<< prism managed <<<"
	codexProviderKey  = "prism"
)

// codexDesktopConfigPath returns the path to Codex Desktop's config.toml.
// Works on both macOS (~/.codex/) and Windows (%USERPROFILE%/.codex/).
func codexDesktopConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// codexCatalogPath returns the path where we write the custom model catalog JSON.
func codexCatalogPath() string {
	return filepath.Join(getConfigDir(), "codex_catalog.json")
}

// isCodexDesktopInstalled checks if Codex Desktop is installed by looking for its config file.
func isCodexDesktopInstalled() bool {
	p := codexDesktopConfigPath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// isCodexDesktopActive checks if Prism's managed blocks are present in the Codex config.
func isCodexDesktopActive() bool {
	p := codexDesktopConfigPath()
	if p == "" {
		return false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), codexManagedBegin)
}

// generateCodexCatalog builds a Codex Desktop catalog from the model remapping.
func generateCodexCatalog(remap *ModelRemapping) []map[string]interface{} {
	if remap == nil || len(remap.KnownModels) == 0 {
		return nil
	}

	entries := make([]map[string]interface{}, 0, len(remap.KnownModels))

	for i, m := range remap.KnownModels {
		entry := map[string]interface{}{
			"slug":        m.ID,
			"display_name": humanizeModelID(m.ID),
			"description": humanizeModelID(m.ID) + " via Prism.",
			"visibility":  "list",
			"priority":    maxInt(1, 1000-i),
		}

		// Context window
		ctx := m.ContextLength
		if ctx == 0 {
			ctx = 128000
		}
		entry["context_window"] = ctx
		entry["max_context_window"] = ctx

		// Auto compact limit
		autoCompact := int(float64(ctx) * 0.8)
		if autoCompact < 8000 {
			autoCompact = 8000
		}
		entry["auto_compact_token_limit"] = autoCompact

		// Truncation policy
		truncLimit := int(float64(ctx) * 0.32)
		if truncLimit > 64000 {
			truncLimit = 64000
		}
		if truncLimit < 8000 {
			truncLimit = 8000
		}
		entry["truncation_policy"] = map[string]interface{}{
			"mode":  "tokens",
			"limit": truncLimit,
		}

		// Reasoning
		if m.Reasoning {
			efforts := m.ReasoningEffort
			if len(efforts) == 0 {
				efforts = []string{"low", "medium", "high", "xhigh"}
			}
			entry["default_reasoning_level"] = efforts[0]
			entry["supported_reasoning_levels"] = reasoningLevels(efforts)
		} else {
			entry["default_reasoning_level"] = "low"
			entry["supported_reasoning_levels"] = reasoningLevels([]string{"low", "medium", "high", "xhigh"})
		}

		entry["default_reasoning_summary"] = "none"
		entry["reasoning_summary_format"] = "none"
		entry["supports_reasoning_summaries"] = false

		// Vision / modalities
		noImage := true
		if m.Capabilities != nil && m.Capabilities.Vision {
			noImage = false
		}
		if noImage {
			entry["input_modalities"] = []string{"text"}
			entry["supports_image_detail_original"] = false
		} else {
			entry["input_modalities"] = []string{"text", "image"}
			entry["supports_image_detail_original"] = true
		}

		// Tool support
		entry["supports_parallel_tool_calls"] = true
		entry["experimental_supported_tools"] = []interface{}{}
		entry["apply_patch_tool_type"] = "freeform"
		entry["web_search_tool_type"] = "text_and_image"
		entry["supports_search_tool"] = false

		// Misc fixed fields
		entry["shell_type"] = "shell_command"
		entry["minimal_client_version"] = "0.0.1"
		entry["supported_in_api"] = true
		entry["availability_nux"] = nil
		entry["upgrade"] = nil
		entry["prefer_websockets"] = false
		entry["default_verbosity"] = "low"
		entry["support_verbosity"] = false
		entry["available_in_plans"] = []string{"free", "plus", "pro", "team", "business", "enterprise"}

		// Base instructions
		entry["base_instructions"] = "You are a coding agent running through Prism, a local proxy. " +
			"You have access to the user's codebase and can run commands. " +
			"Always use tools when needed. Be concise and direct."

		entry["model_messages"] = map[string]interface{}{
			"instructions_template": "",
			"instructions_variables": map[string]interface{}{},
		}

		entries = append(entries, entry)
	}

	return entries
}

// writeCodexCatalog generates and writes the Codex Desktop catalog JSON.
func writeCodexCatalog(remap *ModelRemapping) error {
	entries := generateCodexCatalog(remap)
	if entries == nil {
		return fmt.Errorf("no models in remapping")
	}

	catalog := map[string]interface{}{
		"models": entries,
	}

	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal catalog: %w", err)
	}

	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	return os.WriteFile(codexCatalogPath(), data, 0644)
}

// installCodexConfig writes the managed blocks into ~/.codex/config.toml.
func installCodexConfig(port int) error {
	configPath := codexDesktopConfigPath()
	if configPath == "" {
		return fmt.Errorf("cannot determine Codex config path")
	}

	// Read existing config (or start with empty)
	var existing []byte
	if data, err := os.ReadFile(configPath); err == nil {
		existing = data
	}

	// Strip any existing Prism managed blocks
	cleaned := stripManagedBlocks(string(existing))

	// Extract previous top-level values from the existing config
	prevTopLevel := extractTopLevelOverrides(cleaned)
	prevTopLevelJSON, _ := json.Marshal(prevTopLevel)

	// Get the first model slug as the default
	remap := loadModelRemapping()
	defaultSlug := ""
	if len(remap.KnownModels) > 0 {
		defaultSlug = remap.KnownModels[0].ID
	}
	if defaultSlug == "" && remap.DefaultModel != "" {
		defaultSlug = remap.DefaultModel
	}

	// Remove old top-level keys that we'll set
	cleaned = removeTopLevelKeys(cleaned)

	// Build the top-level managed block
	var topBlock strings.Builder
	topBlock.WriteString("\n")
	topBlock.WriteString(codexManagedBegin + "\n")
	topBlock.WriteString("# prism previous-top-level = " + string(prevTopLevelJSON) + "\n")
	if defaultSlug != "" {
		topBlock.WriteString("model = \"" + defaultSlug + "\"\n")
	}
	topBlock.WriteString("model_provider = \"" + codexProviderKey + "\"\n")
	topBlock.WriteString("model_catalog_json = \"" + tomlEscapePath(codexCatalogPath()) + "\"\n")
	topBlock.WriteString(codexManagedEnd + "\n")

	// Build the provider section managed block
	var provBlock strings.Builder
	provBlock.WriteString("\n")
	provBlock.WriteString(codexManagedBegin + "\n")
	provBlock.WriteString("[model_providers." + codexProviderKey + "]\n")
	provBlock.WriteString("name = \"Prism\"\n")
	provBlock.WriteString("base_url = \"http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1\"\n")
	provBlock.WriteString("wire_api = \"responses\"\n")
	provBlock.WriteString("experimental_bearer_token = \"prism\"\n")
	provBlock.WriteString("request_max_retries = 3\n")
	provBlock.WriteString("stream_max_retries = 3\n")
	provBlock.WriteString("stream_idle_timeout_ms = 600000\n")
	provBlock.WriteString(codexManagedEnd + "\n")

	// Assemble: top block first, then cleaned content, then provider block last
	result := topBlock.String() + "\n" + strings.TrimLeft(cleaned, "\r\n") + "\n" + provBlock.String() + "\n"

	// Ensure the directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create .codex directory: %w", err)
	}

	return os.WriteFile(configPath, []byte(result), 0644)
}

// restoreCodexConfig removes Prism's managed blocks from ~/.codex/config.toml
// and restores any previous top-level values.
func restoreCodexConfig() error {
	configPath := codexDesktopConfigPath()
	if configPath == "" {
		return fmt.Errorf("cannot determine Codex config path")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to restore
		}
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)

	// Extract previous top-level values before stripping
	prevTopLevel := extractTopLevelOverrides(content)

	// Strip managed blocks
	cleaned := stripManagedBlocks(content)

	// Remove any top-level keys we set
	cleaned = removeTopLevelKeys(cleaned)

	// Restore previous top-level values
	if model, ok := prevTopLevel["model"]; ok && model != "" {
		cleaned = prependTopLevelKey(cleaned, "model", model)
	}
	if provider, ok := prevTopLevel["model_provider"]; ok && provider != "" {
		cleaned = prependTopLevelKey(cleaned, "model_provider", provider)
	}
	if catalog, ok := prevTopLevel["model_catalog_json"]; ok && catalog != "" {
		cleaned = prependTopLevelKey(cleaned, "model_catalog_json", catalog)
	}

	return os.WriteFile(configPath, []byte(cleaned), 0644)
}

// SyncCodexDesktop is called on proxy startup to sync the catalog and config.
// It silently skips if Codex Desktop is not installed.
func SyncCodexDesktop(port int) {
	if !isCodexDesktopInstalled() {
		log.Printf("[Codex Desktop] Not installed, skipping sync")
		return
	}

	remap := loadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Codex Desktop] No models configured, skipping sync")
		return
	}

	if err := writeCodexCatalog(remap); err != nil {
		log.Printf("[Codex Desktop] Failed to write catalog: %v", err)
		return
	}

	if err := installCodexConfig(port); err != nil {
		log.Printf("[Codex Desktop] Failed to install config: %v", err)
		return
	}

	log.Printf("[Codex Desktop] Synced %d models to ~/.codex/config.toml", len(remap.KnownModels))
}

// ── TOML helpers ──

// stripManagedBlocks removes all sections between >>> prism managed >>> and <<< prism managed <<< markers.
func stripManagedBlocks(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == codexManagedBegin {
			inBlock = true
			continue
		}
		if trimmed == codexManagedEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// extractTopLevelOverrides extracts only true top-level key-value pairs (before any [section]).
func extractTopLevelOverrides(content string) map[string]string {
	result := map[string]string{}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Stop at the first section header — everything after is not top-level
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")
		result[key] = val
	}
	return result
}

// removeTopLevelKeys removes specific top-level keys (before any [section]) from a TOML config.
func removeTopLevelKeys(content string) string {
	removeKeys := map[string]bool{
		"model":              true,
		"model_provider":     true,
		"model_catalog_json": true,
	}
	lines := strings.Split(content, "\n")
	var result []string
	inTopLevel := true // only process lines before the first [section]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inTopLevel && strings.HasPrefix(trimmed, "[") {
			inTopLevel = false
		}
		if inTopLevel {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				if removeKeys[key] {
					continue
				}
			}
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// tomlEscapePath converts a Windows path to a TOML-safe string using forward slashes.
func tomlEscapePath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// reasoningLevels builds the supported_reasoning_levels array in the format
// Codex Desktop expects: list of {effort, description} dicts.
func reasoningLevels(efforts []string) []map[string]string {
	descriptions := map[string]string{
		"low":   "Faster, lighter reasoning",
		"medium": "Balanced speed and reasoning",
		"high":  "Deeper reasoning",
		"xhigh": "Maximum reasoning where supported",
	}
	result := make([]map[string]string, 0, len(efforts))
	for _, e := range efforts {
		desc := descriptions[e]
		if desc == "" {
			desc = "Reasoning at " + e + " level"
		}
		result = append(result, map[string]string{
			"effort":      e,
			"description": desc,
		})
	}
	return result
}

// prependTopLevelKey adds a key = "value" line at the top of the TOML content.
func prependTopLevelKey(content, key, value string) string {
	line := key + " = \"" + tomlEscapePath(value) + "\"\n"
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return line
	}
	return line + "\n" + trimmed
}

// humanizeModelID converts a model ID like "gpt-4o" to "GPT 4o" for display.
func humanizeModelID(id string) string {
	// Simple heuristic: split on hyphens, capitalize words
	parts := strings.Split(id, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// parseIntOr parses a string to int, returning fallback on failure.
func parseIntOr(s string, fallback int) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			return fallback
		}
	}
	if n == 0 {
		return fallback
	}
	return n
}
