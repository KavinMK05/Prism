package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// grokBuildConfigPath returns ~/.grok/config.toml (cross-platform).
func grokBuildConfigPath() string { return agentConfigPath("grok-build") }

// isGrokBuildInstalled reports whether Grok Build is installed: the
// ~/.grok/config.toml config file exists OR the `grok` binary is on PATH.
// (Grok Build may not create its config file until first run, so the binary
// check avoids a false "not installed".)
func isGrokBuildInstalled() bool {
	if isAgentConfigInstalled("grok-build") {
		return true
	}
	if p, ok := lookupBinary("grok"); ok && p != "" {
		return true
	}
	return false
}

func isGrokBuildActive() bool { return isAgentActive("grok-build") }

// sanitizeGrokModelKey lowercases the id and replaces every rune not in
// [a-z0-9] with '-', trimming leading/trailing '-'. TOML bare keys allow
// [A-Za-z0-9_-]; restricting to [a-z0-9-] avoids ':' in model ids like
// "deepseek-v4-flash:cloud".
func sanitizeGrokModelKey(id string) string {
	s := strings.ToLower(id)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// tomlQuote returns a double-quoted TOML string literal with backslash and
// double-quote escaped.
func tomlQuote(s string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s) + "\""
}

// hasModelsSection reports whether the content contains a top-level [models]
// or [models.*] section header. Prevents emitting a second [models] section
// (a TOML duplicate-section parse error) when the user already has one.
func hasModelsSection(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "[models]" || strings.HasPrefix(t, "[models.") {
			return true
		}
	}
	return false
}

// stripPrismModelSections removes all [model.prism-*] section blocks from the
// content. Each section starts with a [model.prism-...] header and runs until
// the next section header (or EOF). This catches sections that were written
// outside the managed block markers (e.g. by an older version of the code)
// and would otherwise be duplicated on every re-sync.
func stripPrismModelSections(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	skipping := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[model.prism-") {
			skipping = true
			continue
		}
		if skipping {
			// End the skip when we hit any new section header or a managed marker
			if strings.HasPrefix(t, "[") || t == codexManagedBegin || t == codexManagedEnd {
				skipping = false
				result = append(result, line)
			}
			// Otherwise skip the line (it's a key=value inside the prism section)
		} else {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// buildGrokBuildModelSections writes one [model.prism-<key>] section per known
// Prism model. All Prism models use api_backend = "responses" so Grok Build
// routes through /v1/responses, where Prism intercepts the hosted web_search
// tool and runs it locally via the SearchRunner. supports_backend_search = true
// tells Grok Build the backend can execute web search, so it emits the typed
// web_search tool Prism emulates (instead of falling back to a client-side tool
// the model then claims it doesn't have).
func buildGrokBuildModelSections(remap *ModelRemapping, cfg *Config, baseURL string) string {
	var b strings.Builder
	for _, m := range remap.KnownModels {
		key := "prism-" + sanitizeGrokModelKey(m.ID)
		apiBackend := "responses"
		ctx := m.ContextLength
		if ctx == 0 {
			ctx = 128000
		}
		b.WriteString("[model." + key + "]\n")
		b.WriteString("model = " + tomlQuote(m.ID) + "\n")
		b.WriteString("base_url = " + tomlQuote(baseURL) + "\n")
		b.WriteString("name = " + tomlQuote(prismManagedTag+" "+humanizeModelID(m.ID)) + "\n")
		b.WriteString("api_key = \"prism\"\n")
		b.WriteString("api_backend = " + tomlQuote(apiBackend) + "\n")
		b.WriteString(fmt.Sprintf("context_window = %d\n", ctx))
		b.WriteString("supports_backend_search = true\n")
		if m.Reasoning {
			b.WriteString("supports_reasoning_effort = true\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// installGrokBuildConfig writes a Prism managed block of [model.prism-*]
// sections into ~/.grok/config.toml. The block is appended at the end of the
// file (a new [section] header correctly closes any prior section). Any prior
// Prism managed block is stripped first, making re-sync idempotent. A one-time
// .prism-backup is kept of the original user content.
func installGrokBuildConfig(port int, remap *ModelRemapping) error {
	p := grokBuildConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Grok Build config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	existing, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read Grok Build config: %w", err)
	}
	cleaned := stripManagedBlocks(string(existing))
	cleaned = stripPrismModelSections(cleaned)
	ensureAgentBackup(p)

	cfg := loadConfig()
	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"

	var block strings.Builder
	block.WriteString("\n" + codexManagedBegin + "\n")
	if !hasModelsSection(cleaned) {
		block.WriteString("[models]\n")
		block.WriteString("default = " + tomlQuote("prism-"+sanitizeGrokModelKey(remap.KnownModels[0].ID)) + "\n")
		block.WriteString("\n")
	}
	block.WriteString(buildGrokBuildModelSections(remap, cfg, baseURL))
	block.WriteString(codexManagedEnd + "\n")

	result := strings.TrimRight(cleaned, "\r\n") + "\n" + block.String()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("failed to create Grok Build config dir: %w", err)
	}
	if err := os.WriteFile(p, []byte(result), 0644); err != nil {
		return fmt.Errorf("failed to write Grok Build config: %w", err)
	}
	return nil
}

// restoreGrokBuildConfig removes the Prism managed block from
// ~/.grok/config.toml, preserving all of the user's own content. We only ever
// added sections inside the managed markers, so stripping fully reverses us —
// no top-level restoration is needed (unlike Codex).
func restoreGrokBuildConfig() error {
	p := grokBuildConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Grok Build config path")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read Grok Build config: %w", err)
	}
	cleaned := stripManagedBlocks(string(data))
	if cleaned != string(data) {
		if err := os.WriteFile(p, []byte(cleaned), 0644); err != nil {
			return fmt.Errorf("failed to write Grok Build config: %w", err)
		}
	}
	return nil
}

// syncGrokBuild is called on proxy startup to sync the Grok Build config when
// Grok Build is installed. Silently skips when not installed or no models.
func syncGrokBuild(port int) {
	if !isGrokBuildInstalled() {
		return
	}
	remap := loadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Grok Build] No models configured, skipping sync")
		return
	}
	if err := installGrokBuildConfig(port, remap); err != nil {
		log.Printf("[Grok Build] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Grok Build] Synced %d models to ~/.grok/config.toml", len(remap.KnownModels))
}