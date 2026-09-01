package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"ollama-proxy/internal/config"
)

// zedProviderID is the key Prism uses under language_models.openai_compatible
// in Zed's settings.json. Zed generates the PRISM_API_KEY environment variable
// name from this ID, but a local proxy needs no API key.
const zedProviderID = "prism"

// zedConfigPath returns Zed's settings.json path for the current platform:
//   - Windows: %APPDATA%\Zed\settings.json
//   - macOS:   ~/Library/Application Support/Zed/settings.json
//   - Linux:   $XDG_CONFIG_HOME/zed/settings.json (default ~/.config/zed/settings.json)
func zedConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
			return filepath.Join(cfgDir, "Zed", "settings.json")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "Library", "Application Support", "Zed", "settings.json")
		}
	default: // linux and other unix-like systems
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "zed", "settings.json")
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".config", "zed", "settings.json")
		}
	}
	return ""
}

func isZedInstalled() bool {
	if isAgentConfigInstalled("zed") {
		return true
	}
	// Zed's CLI (`zed`) is only on PATH when the user runs "zed: install cli",
	// so also look inside the macOS app bundle.
	if p, ok := lookupBinary("zed"); ok && p != "" {
		return true
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/Applications/Zed.app"); err == nil {
			return true
		}
	}
	return false
}

func isZedActive() bool { return IsAgentActive("zed") }

// readJSONCConfig reads Zed's settings file, which is JSONC: users commonly
// have // or /* */ comments and trailing commas in it. Strict JSON parses as
// usual; otherwise comments and trailing commas are stripped before parsing
// (stripped is reported via the second return value so callers can warn that
// comments will not survive a rewrite).
func readJSONCConfig(path string) (map[string]interface{}, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{}, false, nil
		}
		return nil, false, err
	}
	text := string(data)
	if len(strings.TrimSpace(text)) == 0 {
		return map[string]interface{}{}, false, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(text), &m); err == nil {
		return m, false, nil
	}
	stripped := stripJSONC(text)
	if err := json.Unmarshal([]byte(stripped), &m); err != nil {
		return nil, true, fmt.Errorf("invalid JSONC in %s: %w", path, err)
	}
	return m, true, nil
}

// stripJSONC removes // line comments, /* block comments */ and trailing
// commas from JSON text, preserving string literals (a "//" inside a string
// must not be treated as a comment).
func stripJSONC(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inString := false
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			b.WriteByte(c)
		case '/':
			if i+1 < len(text) && text[i+1] == '/' {
				for i < len(text) && text[i] != '\n' {
					i++
				}
				if i < len(text) {
					b.WriteByte('\n')
				}
			} else if i+1 < len(text) && text[i+1] == '*' {
				i += 2
				for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
					i++
				}
				i++ // skip trailing '/'
			} else {
				b.WriteByte(c)
			}
		case ',':
			// Look ahead past whitespace; drop the comma if } or ] follows.
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\r' || text[j] == '\n') {
				j++
			}
			if j < len(text) && (text[j] == '}' || text[j] == ']') {
				// trailing comma: skip it
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// buildZedModels returns the available_models array for the Prism provider
// block. Models with API=="responses" (e.g. Zen muse-spark/gpt/grok or Codex OAuth)
// set capabilities.chat_completions=false so Zed talks to Prism's /v1/responses
// endpoint for them, while all other models use /v1/chat/completions — both
// within the single provider block. Per-model protocol is stored in m.API.
func buildZedModels(remap *config.ModelRemapping, cfg *config.Config) []interface{} {
	models := make([]interface{}, 0, len(remap.KnownModels))
	for _, m := range remap.KnownModels {
		ctx := m.ContextLength
		if ctx == 0 {
			ctx = 128000
		}
		out := m.MaxOutputTokens
		if out == 0 {
			out = 16384
		}
		useResponsesBackend := m.API == "responses" || cfg.IsCodexProviderID(m.Provider)
		// Zed requires every capabilities field to be present once the object
		// is provided (no serde defaults), so write the full set.
		entry := map[string]interface{}{
			"name":         prismModelRouteKey(m),
			"display_name": prismModelDisplayName(cfg, m),
			"max_tokens":   ctx,
			"capabilities": map[string]interface{}{
				"tools":                 true,
				"images":                m.Capabilities != nil && m.Capabilities.Vision,
				"parallel_tool_calls":   false,
				"prompt_cache_key":      false,
				"chat_completions":      !useResponsesBackend,
				"interleaved_reasoning": false,
				"max_tokens_parameter":  false,
			},
		}
		if out > 0 {
			entry["max_output_tokens"] = out
		}
		if m.Reasoning {
			efforts := m.ReasoningEffort
			if len(efforts) == 0 {
				efforts = []string{"low", "medium", "high"}
			}
			// Zed takes a single reasoning_effort per model; prefer "high",
			// falling back to the last configured effort level.
			effort := efforts[len(efforts)-1]
			for _, e := range efforts {
				if e == "high" {
					effort = e
					break
				}
			}
			entry["reasoning_effort"] = effort
		}
		models = append(models, entry)
	}
	return models
}

// InstallZedConfig writes the "prism" provider block into Zed's settings.json
// under language_models.openai_compatible. All other providers and top-level
// keys are preserved. A one-time .prism-backup is kept. Note: if the user's
// settings contain JSONC comments they are lost on rewrite (the backup keeps
// the original).
func InstallZedConfig(port int, remap *config.ModelRemapping) error {
	p := zedConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Zed config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	m, hadComments, err := readJSONCConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Zed config: %w", err)
	}
	ensureAgentBackup(p)

	cfg := config.Load()
	lm, _ := m["language_models"].(map[string]interface{})
	if lm == nil {
		lm = map[string]interface{}{}
	}
	compat, _ := lm["openai_compatible"].(map[string]interface{})
	if compat == nil {
		compat = map[string]interface{}{}
	}

	compat[zedProviderID] = map[string]interface{}{
		"api_url": "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1",
		"custom_headers": map[string]interface{}{
			// Lets Prism's detectClient identify Zed even when the
			// User-Agent is not informative.
			"X-Client-Name": "Zed",
		},
		"available_models": buildZedModels(remap, cfg),
	}
	lm["openai_compatible"] = compat
	m["language_models"] = lm

	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write Zed config: %w", err)
	}
	if hadComments {
		log.Printf("[Zed] Note: JSONC comments in %s were removed by the rewrite (original kept at %s)", p, agentBackupPath(p))
	}
	return nil
}

// RestoreZedConfig removes the "prism" provider block from Zed's settings.json,
// preserving all other providers and settings. Empty openai_compatible /
// language_models containers left behind are cleaned up.
func RestoreZedConfig() error {
	p := zedConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Zed config path")
	}
	m, _, err := readJSONCConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Zed config: %w", err)
	}
	changed := false
	if lm, ok := m["language_models"].(map[string]interface{}); ok {
		if compat, ok := lm["openai_compatible"].(map[string]interface{}); ok {
			if _, exists := compat[zedProviderID]; exists {
				delete(compat, zedProviderID)
				changed = true
			}
			if len(compat) == 0 {
				delete(lm, "openai_compatible")
			}
		}
		if len(lm) == 0 {
			delete(m, "language_models")
		} else {
			m["language_models"] = lm
		}
	}
	if changed {
		if err := writeJSONConfig(p, m); err != nil {
			return fmt.Errorf("failed to write Zed config: %w", err)
		}
	}
	return nil
}

// syncZed is called on proxy startup to sync the Zed config when Zed is
// installed. Silently skips when not installed or no models.
func syncZed(port int) {
	if !isZedInstalled() {
		return
	}
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Zed] No models configured, skipping sync")
		return
	}
	if err := InstallZedConfig(port, remap); err != nil {
		log.Printf("[Zed] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Zed] Synced %d models to %s", len(remap.KnownModels), zedConfigPath())
}
