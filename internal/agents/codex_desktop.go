package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/platform"
)

const (
	codexManagedBegin = "# >>> prism managed >>>"
	codexManagedEnd   = "# <<< prism managed <<<"
	codexProviderKey  = "prism"

	// codexPreviousTopLevelPrefix introduces the stash of top-level values that
	// were present before Prism wrote its managed block, so they can be put back
	// afterwards. It is a comment, so Codex ignores it.
	codexPreviousTopLevelPrefix = "# prism previous-top-level = "
)

// codexManagedTopLevelKeys are the top-level keys Prism sets in config.toml and
// therefore saves and restores. The user's `model` is deliberately not one of
// them: Prism routes traffic through model_provider and lists its models in
// model_catalog_json, but never picks which model Codex uses.
var codexManagedTopLevelKeys = []string{"model_provider", "model_catalog_json"}

// codexOwnedTopLevelKeys are the top-level keys Prism strips along with its
// Codex block. It covers codexManagedTopLevelKeys plus `model`, which an older
// Prism version wrote inside the block (the user's own `model` went to the
// stash), so a migration cannot leave a stale value behind.
var codexOwnedTopLevelKeys = append(append([]string(nil), codexManagedTopLevelKeys...), "model")

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
	return filepath.Join(platform.ConfigDir(), "codex_catalog.json")
}

// IsCodexDesktopInstalled checks if Codex Desktop is installed by looking for its config file.
func IsCodexDesktopInstalled() bool {
	p := codexDesktopConfigPath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// IsCodexDesktopActive checks if Prism's managed blocks are present in the Codex config.
func IsCodexDesktopActive() bool {
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
func generateCodexCatalog(remap *config.ModelRemapping) []map[string]interface{} {
	if remap == nil || len(remap.KnownModels) == 0 {
		return nil
	}

	entries := make([]map[string]interface{}, 0, len(remap.KnownModels))
	cfg := config.Load()

	for i, m := range remap.KnownModels {
		routeKey := prismModelRouteKey(m)
		entry := map[string]interface{}{
			"slug":         routeKey,
			"display_name": prismModelDisplayName(cfg, m),
			"description":  prismModelDisplayName(cfg, m) + " via Prism.",
			"visibility":   "list",
			"priority":     maxInt(1, 1000-i),
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

		// Reasoning — always expose all 4 levels, default to medium (matches codex-shim)
		entry["default_reasoning_level"] = "medium"
		entry["supported_reasoning_levels"] = reasoningLevels([]string{"low", "medium", "high", "xhigh"})

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
			"When using the apply_patch tool, emit patches in V4A format: wrap with *** Begin Patch and *** End Patch, " +
			"use *** Add File: <path> to create files, *** Update File: <path> to modify them, *** Delete File: <path> to remove them. " +
			"Prefix added lines with +, deleted lines with -, context lines with nothing. " +
			"Always use tools when needed. Be concise and direct."

		entry["model_messages"] = map[string]interface{}{
			"instructions_template": "You are Codex running on {model_name} through Prism, a local proxy. Be a helpful, direct coding collaborator.",
			"instructions_variables": map[string]interface{}{
				"model_name": prismModelDisplayName(cfg, m),
			},
		}

		entries = append(entries, entry)
	}

	return entries
}

// WriteCodexCatalog generates and writes the Codex Desktop catalog JSON.
func WriteCodexCatalog(remap *config.ModelRemapping) error {
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

	dir := platform.ConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	return os.WriteFile(codexCatalogPath(), data, 0644)
}

// InstallCodexConfig writes the managed blocks into ~/.codex/config.toml. The
// managed block sets model_provider and model_catalog_json so Codex can reach
// Prism and list its models; it deliberately does not set the top-level `model`
// key, so the model Codex runs stays the user's choice.
func InstallCodexConfig(port int) error {
	configPath := codexDesktopConfigPath()
	if configPath == "" {
		return fmt.Errorf("cannot determine Codex config path")
	}

	// Read existing config (or start with empty)
	var existing []byte
	if data, err := os.ReadFile(configPath); err == nil {
		existing = data
	}

	// Values an older Prism version stashed before it took over the top-level
	// keys. Kept as a fallback, because a re-sync sees a body that no longer
	// contains them (the old installer removed them from the body).
	stashed := extractPreviousTopLevel(string(existing))

	// Strip any existing Prism managed blocks
	cleaned := stripCodexManagedBlocks(string(existing))

	// Previous top-level values: prefer what the body actually has, falling back
	// to the stash for the keys Prism manages.
	prevTopLevel := extractTopLevelOverrides(cleaned)
	prevManaged := map[string]string{}
	for _, key := range codexManagedTopLevelKeys {
		if v := prevTopLevel[key]; v != "" {
			prevManaged[key] = v
		} else if v := stashed[key]; v != "" {
			prevManaged[key] = v
		}
	}
	prevManagedJSON, _ := json.Marshal(prevManaged)

	// Remove the top-level keys Prism manages; the user's `model` is left alone.
	cleaned = removeTopLevelKeys(cleaned)

	// Prism no longer owns `model`. Configs written by an older version had it
	// removed from the body and parked in the stash, so hand it back to the user
	// in the body, where they can see and change it.
	if _, present := prevTopLevel["model"]; !present {
		if model := stashed["model"]; model != "" {
			cleaned = prependTopLevelKey(cleaned, "model", model)
		}
	}

	// Build the top-level managed block. Prism points Codex at its catalog and
	// provider; which model is selected stays the user's choice, so no `model`
	// line is written.
	var topBlock strings.Builder
	topBlock.WriteString("\n")
	topBlock.WriteString(codexManagedBegin + "\n")
	topBlock.WriteString(codexPreviousTopLevelPrefix + string(prevManagedJSON) + "\n")
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

// RestoreCodexConfig removes Prism's managed blocks from ~/.codex/config.toml
// and restores the top-level values that were present before Prism wrote them.
func RestoreCodexConfig() error {
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

	// What the user had before Prism wrote its block. Only the stash records the
	// managed keys, and older Prism versions also stashed `model`, which they had
	// removed from the body.
	stashed := extractPreviousTopLevel(content)

	// Strip managed blocks
	cleaned := stripCodexManagedBlocks(content)

	// Remove the top-level keys Prism set
	cleaned = removeTopLevelKeys(cleaned)

	// Put back anything Prism moved aside that isn't already in the body. A sync
	// by the current version leaves `model` in place, so it is only restored for
	// configs written by an older version.
	topLevel := extractTopLevelOverrides(cleaned)
	for _, key := range append([]string{"model"}, codexManagedTopLevelKeys...) {
		if _, present := topLevel[key]; present {
			continue
		}
		if v := stashed[key]; v != "" {
			cleaned = prependTopLevelKey(cleaned, key, v)
		}
	}

	return os.WriteFile(configPath, []byte(cleaned), 0644)
}

// SyncCodexDesktop is called on proxy startup to sync the catalog and config.
// It silently skips if Codex Desktop is not installed.
func SyncCodexDesktop(port int) {
	if !IsCodexDesktopInstalled() {
		log.Printf("[Codex Desktop] Not installed, skipping sync")
		return
	}
	if !AgentAutoSyncEnabled("codex") {
		log.Printf("[Codex Desktop] Auto-sync disabled, skipping sync")
		return
	}

	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Codex Desktop] No models configured, skipping sync")
		return
	}

	if err := WriteCodexCatalog(remap); err != nil {
		log.Printf("[Codex Desktop] Failed to write catalog: %v", err)
		return
	}

	if err := InstallCodexConfig(port); err != nil {
		log.Printf("[Codex Desktop] Failed to install config: %v", err)
		return
	}

	log.Printf("[Codex Desktop] Synced %d models to ~/.codex/config.toml", len(remap.KnownModels))
}

// ── TOML helpers ──

// stripManagedBlocks removes every line between the >>> prism managed >>> and
// <<< prism managed <<< markers. It is for agent configs whose managed block
// Prism owns in full (Grok Build, Kimi Code); Codex uses stripCodexManagedBlocks,
// which keeps tables Codex wrote for itself inside those markers.
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

// stripCodexManagedBlocks removes Prism's Codex markers along with the top-level
// keys, stash comment, and [model_providers.prism] table that Prism owns,
// keeping everything else that ended up between the markers.
//
// Codex appends the tables it persists — project trust under [projects.*], the
// Windows sandbox mode under [windows], trusted hook hashes under [hooks.state]
// — to the end of ~/.codex/config.toml, which is where Prism's blocks sit, so
// those tables land inside the markers. Deleting the whole region threw the
// user's answers away on every restart, and Codex asked for them again.
func stripCodexManagedBlocks(content string) string {
	return stripManagedRegion(content, codexManagedBegin, codexManagedEnd, isPrismProviderTableHeader, isPrismManagedTopLevelLine)
}

// stripManagedRegion removes one Prism marker pair and the TOML inside it that
// Prism owns, while preserving content Prism did not write.
//
// ownedTable reports whether a (trimmed) table header names a table Prism
// wrote, which also ends the previous section; key/value lines under such a
// header are dropped with it. ownedLine reports whether a non-table line inside
// the region was written by Prism. Table headers claiming to be Prism's are
// honoured wherever they appear, so a section left outside the markers by an
// older version is removed instead of being duplicated on the next write.
func stripManagedRegion(content, begin, end string, ownedTable func(string) bool, ownedLine func(string) bool) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inRegion := false
	inOwnedTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == begin {
			inRegion = true
			inOwnedTable = false
			continue
		}
		if trimmed == end {
			inRegion = false
			inOwnedTable = false
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			// Any table header ends the section that came before it.
			inOwnedTable = ownedTable(trimmed)
			if inOwnedTable {
				continue
			}
			out = append(out, line)
			continue
		}
		if inOwnedTable {
			continue
		}
		if inRegion && ownedLine != nil && ownedLine(trimmed) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// isPrismProviderTableHeader reports whether a table header names Prism's model
// provider table or one of its sub-tables.
func isPrismProviderTableHeader(trimmed string) bool {
	return isManagedTableHeader(trimmed, "model_providers."+codexProviderKey)
}

// isManagedTableHeader reports whether a table header names the dotted key path
// or a table nested under it, such as [model_providers.prism] and
// [model_providers.prism.http_headers]. The last segment is also accepted
// quoted, since Codex quotes a segment that is not a bare key.
//
// A longer key must not match: "[model_providers.prismatic]" is not Prism's.
func isManagedTableHeader(trimmed, path string) bool {
	segments := strings.Split(path, ".")
	quoted := append(append([]string(nil), segments[:len(segments)-1]...), `"`+segments[len(segments)-1]+`"`)
	for _, candidate := range []string{
		"[" + path + "]",
		"[" + strings.Join(quoted, ".") + "]",
	} {
		base := strings.TrimSuffix(candidate, "]")
		if trimmed == candidate {
			return true
		}
		if strings.HasPrefix(trimmed, base+".") {
			return true
		}
		// A trailing comment or padding after the header still names the table.
		if rest, ok := strings.CutPrefix(trimmed, candidate); ok && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '#') {
			return true
		}
	}
	return false
}

// isPrismManagedTopLevelLine reports whether a non-table line inside Prism's
// Codex block was written by Prism: one of the keys it manages, a key an older
// version managed, or the comment stashing the values it replaced.
func isPrismManagedTopLevelLine(trimmed string) bool {
	if strings.HasPrefix(trimmed, codexPreviousTopLevelPrefix) {
		return true
	}
	key, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	for _, managed := range codexOwnedTopLevelKeys {
		if key == managed {
			return true
		}
	}
	return false
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

// extractPreviousTopLevel reads the stash of pre-Prism top-level values that
// Prism records inside its managed block. Returns nil when the stash is absent
// (fresh install) or unreadable.
func extractPreviousTopLevel(content string) map[string]string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, codexPreviousTopLevelPrefix) {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(trimmed, codexPreviousTopLevelPrefix))
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil
		}
		return m
	}
	return nil
}

// removeTopLevelKeys removes specific top-level keys (before any [section]) from a TOML config.
func removeTopLevelKeys(content string) string {
	removeKeys := map[string]bool{}
	for _, k := range codexManagedTopLevelKeys {
		removeKeys[k] = true
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
		"low":    "Faster, lighter reasoning",
		"medium": "Balanced speed and reasoning",
		"high":   "Deeper reasoning",
		"xhigh":  "Maximum reasoning where supported",
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

// HumanizeModelID converts a model ID like "gpt-4o" to "GPT 4o" for display.
func HumanizeModelID(id string) string {
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

// ParseIntOr parses a string to int, returning fallback on failure.
func ParseIntOr(s string, fallback int) int {
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
