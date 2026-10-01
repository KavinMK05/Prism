package agents

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"ollama-proxy/internal/config"
)

// Hermes Agent (NousResearch) keeps everything in a single config.yaml under
// its home directory: model providers under the top-level `providers:` key and
// MCP servers under `mcp_servers:`. The file is heavily commented and
// hand-edited, and YAML silently overrides duplicate top-level keys — so Prism
// writes marker-delimited regions inserted under the existing top-level keys
// instead of rewriting the file or appending a second key.

const (
	// hermesProviderID is the chat_completions provider key Hermes routes
	// non-Responses models through.
	hermesProviderID = "prism"
	// hermesResponsesProviderID is the codex_responses provider key, written
	// only when at least one model uses the Responses API.
	hermesResponsesProviderID = "prism-responses"

	hermesProviderManagedBegin = "# >>> prism providers managed >>>"
	hermesProviderManagedEnd   = "# <<< prism providers managed <<<"
	hermesMCPManagedBegin      = "# >>> prism mcp managed >>>"
	hermesMCPManagedEnd        = "# <<< prism mcp managed <<<"
)

// hermesConfigPath returns the path to Hermes's config.yaml, honoring
// $HERMES_HOME. The platform default mirrors hermes_constants.py:
// %LOCALAPPDATA%\hermes on Windows, ~/.hermes elsewhere.
func hermesConfigPath() string {
	if root := os.Getenv("HERMES_HOME"); root != "" {
		return filepath.Join(root, "config.yaml")
	}
	if runtime.GOOS == "windows" {
		dir := os.Getenv("LOCALAPPDATA")
		if dir == "" {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				dir = filepath.Join(home, "AppData", "Local")
			}
		}
		if dir != "" {
			return filepath.Join(dir, "hermes", "config.yaml")
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".hermes", "config.yaml")
}

// isHermesInstalled reports whether Hermes is installed: config.yaml exists,
// its config directory is non-empty (Hermes has run at least once), or the
// `hermes` binary is on PATH.
func isHermesInstalled() bool {
	p := hermesConfigPath()
	if p == "" {
		return false
	}
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if info, err := os.Stat(filepath.Dir(p)); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(filepath.Dir(p)); err == nil && len(entries) > 0 {
			return true
		}
	}
	if bin, ok := lookupBinary("hermes"); ok && bin != "" {
		return true
	}
	return false
}

func isHermesActive() bool { return IsAgentActive("hermes") }

func usesHermesResponses(m config.ModelEntry, cfg *config.Config) bool {
	if m.API == "responses" {
		return true
	}
	if m.API == "chat_completions" {
		return false
	}
	return cfg.IsCodexProviderID(m.Provider)
}

// yamlQuote double-quotes a YAML scalar, escaping backslashes and double
// quotes. Model route keys contain `/` and `:`, so they are always quoted.
func yamlQuote(s string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s) + "\""
}

// writeHermesModelEntry emits one models.<key> entry with its context window
// (defaulting to 128000) and output cap when set.
func writeHermesModelEntry(b *strings.Builder, m config.ModelEntry) {
	ctx := m.ContextLength
	if ctx == 0 {
		ctx = 128000
	}
	b.WriteString("      " + yamlQuote(prismModelRouteKey(m)) + ":\n")
	b.WriteString("        context_length: " + fmt.Sprintf("%d", ctx) + "\n")
	if m.MaxOutputTokens > 0 {
		b.WriteString("        max_output_tokens: " + fmt.Sprintf("%d", m.MaxOutputTokens) + "\n")
	}
}

// buildHermesProviderBlock emits the managed YAML region inserted under the
// top-level `providers:` key. Transport is per-provider in Hermes, so chat and
// Responses models get two entries (prism / prism-responses), each listing
// only the models that route through it. An entry with no models is omitted.
func buildHermesProviderBlock(port int, remap *config.ModelRemapping, cfg *config.Config) string {
	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"
	var chatModels, responsesModels []config.ModelEntry
	for _, m := range remap.KnownModels {
		if usesHermesResponses(m, cfg) {
			responsesModels = append(responsesModels, m)
		} else {
			chatModels = append(chatModels, m)
		}
	}
	var b strings.Builder
	b.WriteString("  " + hermesProviderManagedBegin + "\n")
	if len(chatModels) > 0 {
		b.WriteString("  " + hermesProviderID + ":\n")
		b.WriteString("    api: " + baseURL + "\n")
		b.WriteString("    api_key: prism\n")
		b.WriteString("    transport: chat_completions\n")
		b.WriteString("    default_model: " + yamlQuote(prismModelRouteKey(chatModels[0])) + "\n")
		b.WriteString("    models:\n")
		for _, m := range chatModels {
			writeHermesModelEntry(&b, m)
		}
	}
	if len(responsesModels) > 0 {
		b.WriteString("  " + hermesResponsesProviderID + ":\n")
		b.WriteString("    api: " + baseURL + "\n")
		b.WriteString("    api_key: prism\n")
		b.WriteString("    transport: codex_responses\n")
		b.WriteString("    default_model: " + yamlQuote(prismModelRouteKey(responsesModels[0])) + "\n")
		b.WriteString("    models:\n")
		for _, m := range responsesModels {
			writeHermesModelEntry(&b, m)
		}
	}
	b.WriteString("  " + hermesProviderManagedEnd + "\n")
	return b.String()
}

// stripMarkerRegion removes every line from begin through end inclusive,
// wherever the region appears in the file. Parameterized on the marker pair
// because Hermes keeps two independent Prism regions (providers, mcp_servers)
// in one file, and DeepSeek Harness keeps one per file across two files;
// stripManagedBlocks is hardcoded to the Codex TOML pair.
func stripMarkerRegion(content, begin, end string) string {
	lines := strings.Split(content, "\n")
	var result []string
	skipping := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if !skipping && t == begin {
			skipping = true
			continue
		}
		if skipping {
			if t == end {
				skipping = false
			}
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// insertHermesRegion inserts a marker-delimited block under a top-level YAML
// key without producing a duplicate key. When the key exists with an empty
// value the block is inserted right after it; `key: {}` is rewritten to a bare
// `key:` first. Any other inline value is refused rather than clobbered. When
// the key is absent it is appended at EOF.
func insertHermesRegion(content, key, block string) (string, error) {
	re := regexp.MustCompile(`^` + key + `:(.*)$`)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		value := strings.TrimSpace(m[1])
		if value != "" && value != "{}" {
			return "", fmt.Errorf("Hermes config has an inline value for %s that Prism would clobber; put it on its own lines and retry", key)
		}
		if value == "{}" {
			lines[i] = key + ":"
		}
		out := make([]string, 0, len(lines)+strings.Count(block, "\n")+1)
		out = append(out, lines[:i+1]...)
		out = append(out, strings.Split(strings.TrimRight(block, "\n"), "\n")...)
		out = append(out, lines[i+1:]...)
		return strings.Join(out, "\n"), nil
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n" + key + ":\n" + block, nil
}

// InstallHermesConfig writes Prism's managed providers region into Hermes's
// config.yaml. The region is stripped and re-inserted under the existing
// `providers:` key, so re-syncs are idempotent and every user key, provider
// and comment outside the region survives byte-for-byte. A one-time
// .prism-backup is kept of the original content.
func InstallHermesConfig(port int, remap *config.ModelRemapping) error {
	p := hermesConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Hermes config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}
	existing, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read Hermes config: %w", err)
	}
	cleaned := stripMarkerRegion(string(existing), hermesProviderManagedBegin, hermesProviderManagedEnd)
	ensureAgentBackup(p)

	cfg := config.Load()
	result, err := insertHermesRegion(cleaned, "providers", buildHermesProviderBlock(port, remap, cfg))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("failed to create Hermes config dir: %w", err)
	}
	if err := os.WriteFile(p, []byte(result), 0644); err != nil {
		return fmt.Errorf("failed to write Hermes config: %w", err)
	}
	return nil
}

// RestoreHermesConfig removes Prism's managed providers region from Hermes's
// config.yaml, leaving the MCP region and every user key alone.
func RestoreHermesConfig() error {
	p := hermesConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Hermes config path")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read Hermes config: %w", err)
	}
	cleaned := stripMarkerRegion(string(data), hermesProviderManagedBegin, hermesProviderManagedEnd)
	if cleaned != string(data) {
		if err := os.WriteFile(p, []byte(cleaned), 0644); err != nil {
			return fmt.Errorf("failed to write Hermes config: %w", err)
		}
	}
	return nil
}

// syncHermes is called on proxy startup to sync the Hermes config when Hermes
// is installed. Silently skips when not installed or no models.
func syncHermes(port int) {
	if !isHermesInstalled() {
		return
	}
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Hermes] No models configured, skipping sync")
		return
	}
	if err := InstallHermesConfig(port, remap); err != nil {
		log.Printf("[Hermes] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Hermes] Synced %d models to %s", len(remap.KnownModels), hermesConfigPath())
}
