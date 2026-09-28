package agents

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"ollama-proxy/internal/config"
)

// Empryo keeps every setting — custom providers, MCP servers, display, agent
// limits — in one JSON file per scope and accepts any OpenAI-compatible
// endpoint as a custom provider. See https://empryo.com/docs/providers/custom
//
// The global config lives at %LOCALAPPDATA%\Empryo\config.json on Windows and
// ~/.empryo/config.json on macOS and Linux; empryoConfigPath resolves both.
//
// Unlike the other JSON integrations, Prism cannot write the API key inline:
// Empryo deliberately keeps credentials out of config files (system keychain on
// macOS, DPAPI on Windows, secret store or ~/.empryo/secrets.json on Linux) and
// only reads them from there. The provider therefore declares envVar and the
// user stores Prism's token once with `empryo --set-key prism prism`; Prism's
// setup surfaces that command instead of trying to write the secret itself.

const (
	// empryoProviderID is the id of Prism's custom provider entry. Empryo
	// builds model ids as "<provider id>/<model id>" and never replaces a
	// built-in provider whose id collides (it would rename Prism's to
	// "prism-custom" instead), so this id is the stable one to look for.
	empryoProviderID = "prism"

	// empryoProviderName is what Empryo's model picker shows for the provider.
	empryoProviderName = "Prism"

	// empryoKeyEnvVar is the variable Empryo reads Prism's token from. Empryo
	// refuses to store a key for a provider that declares no envVar, and sends
	// the stored key as "Authorization: Bearer <key>" — exactly what Prism's
	// OpenAI-compatible endpoints expect.
	empryoKeyEnvVar = "PRISM_API_KEY"

	// Limits used when a model declares none, matching the other JSON
	// integrations. Empryo assumes 128k for an undeclared model too, but it
	// also needs maxOutputTokens to know how much room to keep for the reply.
	defaultEmpryoContextWindow = 128000
	defaultEmpryoMaxOutput     = 16384
)

// empryoConfigPath returns Empryo's global config file.
//
// Windows keeps Empryo's data under %LOCALAPPDATA%\Empryo — its own settings,
// logs and secrets.dat all live there — and Empryo does not read
// ~/.empryo/config.json as its global config on that platform. There the only
// role that path can play is a per-project config (<cwd>/.empryo/config.json),
// which stays inert unless Empryo is started from that exact directory and the
// directory is trusted, so a provider written there never reaches the model
// picker. macOS and Linux use the documented ~/.empryo/config.json.
func empryoConfigPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		dir := os.Getenv("LOCALAPPDATA")
		if dir == "" && home != "" {
			dir = filepath.Join(home, "AppData", "Local")
		}
		if dir != "" {
			return filepath.Join(dir, "Empryo", "config.json")
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".empryo", "config.json")
}

// isEmpryoInstalled reports whether Empryo is installed: it has already written
// ~/.empryo/config.json, its config directory exists (the desktop app creates
// that on first run and never puts the `empryo` binary on PATH), or the CLI
// binary is reachable. Unlike OpenCode, no placeholder config is created here —
// setup writes the real block (and its directory) when the file is missing.
func isEmpryoInstalled() bool {
	p := empryoConfigPath()
	if p == "" {
		return false
	}
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if info, err := os.Stat(filepath.Dir(p)); err == nil && info.IsDir() {
		// A populated config directory (sessions, logs, settings) means Empryo
		// has run at least once. An empty one is leftovers from an uninstall, so
		// it does not count as installed.
		if entries, err := os.ReadDir(filepath.Dir(p)); err == nil && len(entries) > 0 {
			return true
		}
	}
	if bin, ok := lookupBinary("empryo"); ok && bin != "" {
		return true
	}
	return false
}

func isEmpryoActive() bool { return IsAgentActive("empryo") }

// empryoModel is one entry of a custom provider's declared "models" list.
type empryoModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContextWindow   int    `json:"contextWindow"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
}

// buildEmpryoModels lists every Prism model for Empryo. A custom provider
// declares a single OpenAI-format base URL, so all models go in one entry even
// when their upstream protocols differ: Prism translates Chat Completions to
// the Responses API (and to the Codex backend) for the models whose API field
// says so, which is what lets one Empryo provider reach them all.
func buildEmpryoModels(remap *config.ModelRemapping, cfg *config.Config) []empryoModel {
	models := make([]empryoModel, 0, len(remap.KnownModels))
	for _, m := range remap.KnownModels {
		ctx := m.ContextLength
		if ctx == 0 {
			ctx = defaultEmpryoContextWindow
		}
		out := m.MaxOutputTokens
		if out == 0 {
			out = defaultEmpryoMaxOutput
		}
		models = append(models, empryoModel{
			ID:              prismModelRouteKey(m),
			Name:            prismModelDisplayName(cfg, m),
			ContextWindow:   ctx,
			MaxOutputTokens: out,
		})
	}
	return models
}

// empryoProviderEntry builds Prism's custom provider block. The reasoning block
// is provider-wide in Empryo (there is no per-model override), so it uses
// "auto": Empryo looks each model up in models.dev and sends the thinking
// fields that model's vendor expects, instead of Prism guessing field names for
// every upstream.
func empryoProviderEntry(port int, remap *config.ModelRemapping, cfg *config.Config) map[string]interface{} {
	return map[string]interface{}{
		"id":        empryoProviderID,
		"name":      empryoProviderName,
		"baseURL":   "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1",
		"envVar":    empryoKeyEnvVar,
		"models":    buildEmpryoModels(remap, cfg),
		"reasoning": map[string]interface{}{"auto": true},
	}
}

// empryoProviderEntryID returns the "id" of one providers[] entry, or "".
func empryoProviderEntryID(item interface{}) string {
	mp, ok := item.(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := mp["id"].(string)
	return id
}

// setEmpryoProvider returns Empryo's providers list with Prism's entry replaced
// (or appended), preserving every other provider in its original order. A
// "providers" value of an unexpected shape is reported rather than clobbered.
func setEmpryoProvider(existing interface{}, entry map[string]interface{}) ([]interface{}, error) {
	out := []interface{}{}
	if existing != nil {
		arr, ok := existing.([]interface{})
		if !ok {
			return nil, fmt.Errorf("config.json has a non-list value where Empryo's providers belong")
		}
		for _, item := range arr {
			if empryoProviderEntryID(item) == empryoProviderID {
				continue // Prism's own previous entry
			}
			out = append(out, item)
		}
	}
	return append(out, entry), nil
}

// hasEmpryoProvider reports whether Empryo's providers list carries Prism's
// entry.
func hasEmpryoProvider(existing interface{}) bool {
	arr, ok := existing.([]interface{})
	if !ok {
		return false
	}
	for _, item := range arr {
		if empryoProviderEntryID(item) == empryoProviderID {
			return true
		}
	}
	return false
}

// InstallEmpryoConfig writes Prism's custom provider into ~/.empryo/config.json.
// Other providers, MCP servers and every unrelated key are preserved, and a
// one-time .prism-backup is kept.
//
// The key is not written here: Empryo stores credentials in the system keychain
// (or a protected file), never in config.json. Until the user runs
// `empryo --set-key prism prism` once, Empryo cannot authenticate to the proxy.
func InstallEmpryoConfig(port int, remap *config.ModelRemapping) error {
	p := empryoConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Empryo config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Empryo config: %w", err)
	}
	ensureAgentBackup(p)

	providers, err := setEmpryoProvider(m["providers"], empryoProviderEntry(port, remap, config.Load()))
	if err != nil {
		return fmt.Errorf("failed to install Empryo provider: %w", err)
	}
	m["providers"] = providers

	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write Empryo config: %w", err)
	}
	return nil
}

// RestoreEmpryoConfig removes Prism's provider entry from
// ~/.empryo/config.json, leaving every other provider, MCP server and setting
// alone. A providers list Prism created is dropped entirely so the file returns
// to its original shape.
func RestoreEmpryoConfig() error {
	p := empryoConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Empryo config path")
	}
	m, err := readJSONConfig(p)
	if err != nil {
		return fmt.Errorf("failed to read Empryo config: %w", err)
	}
	arr, ok := m["providers"].([]interface{})
	if !ok {
		return nil // nothing of ours in here
	}
	out := make([]interface{}, 0, len(arr))
	for _, item := range arr {
		if empryoProviderEntryID(item) == empryoProviderID {
			continue
		}
		out = append(out, item)
	}
	if len(out) == len(arr) {
		return nil
	}
	if len(out) == 0 {
		delete(m, "providers")
	} else {
		m["providers"] = out
	}
	if err := writeJSONConfig(p, m); err != nil {
		return fmt.Errorf("failed to write Empryo config: %w", err)
	}
	return nil
}

// syncEmpryo is called on proxy startup to sync Empryo's provider block when
// Empryo is installed. Silently skips when not installed or no models.
func syncEmpryo(port int) {
	if !isEmpryoInstalled() {
		return
	}
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Empryo] No models configured, skipping sync")
		return
	}
	if err := InstallEmpryoConfig(port, remap); err != nil {
		log.Printf("[Empryo] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Empryo] Synced %d models to %s", len(remap.KnownModels), empryoConfigPath())
}
