package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"ollama-proxy/internal/config"
)

// primeAgentConfigDir returns the Prime Agent config directory.
//
// This mirrors prime-agent's own getAgentDir() resolution:
//  1. $PRIME_AGENT_CODING_AGENT_DIR if set (current name)
//  2. $PI_CODING_AGENT_DIR if set (legacy name from when the package was
//     @earendil-works/pi-coding-agent)
//  3. ~/.prime/agent (CONFIG_DIR_NAME = ".prime/agent")
//
// If you ever moved Prime Agent's data dir with an env var, Prism follows the
// same override instead of blindly writing to ~/.prime/agent. Unset (the
// normal case) just means ~/.prime/agent.
//
// On Windows, when the native %USERPROFILE%\.prime\agent directory does not
// exist, Prism falls back to the default WSL distro's ~/.prime/agent via the
// \\wsl$ share: Prime Agent is frequently installed inside WSL rather than
// natively on Windows, and the 127.0.0.1 proxy URL works from WSL through
// WSL2's localhost forwarding. That fallback is only consulted while the distro
// is already running: Prism never starts WSL itself.
func primeAgentConfigDir() string {
	if dir := os.Getenv("PRIME_AGENT_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		native := filepath.Join(home, ".prime", "agent")
		if _, err := os.Stat(native); err == nil {
			return native
		}
		if runtime.GOOS == "windows" {
			if dir := wslPrimeAgentDir(); dir != "" {
				return dir
			}
		}
		return native
	}
	if runtime.GOOS == "windows" {
		if dir := wslPrimeAgentDir(); dir != "" {
			return dir
		}
	}
	return ""
}

// wslRunningCheckTTL bounds how often the distro-running check runs while the
// distro is stopped: each check spawns wsl.exe, and one status poll asks for
// the config dir more than once.
const wslRunningCheckTTL = 15 * time.Second

var (
	wslPrimeAgentDirOnce sync.Mutex
	wslPrimeAgentDirDone bool
	wslPrimeAgentDirVal  string
	wslRunningCheckedAt  time.Time
	wslRunningVal        bool
)

// wslPrimeAgentDir returns the Prime Agent config dir inside the default WSL
// distro as a UNC path (\\wsl$\<distro>\...), or "" when unavailable.
//
// Windows only; a resolved directory is cached for the process lifetime so
// status polls and startup sync pay the wsl.exe spawn cost at most once.
//
// Prism never starts WSL. Every way into a distro boots it when it is not
// already running - the probe below reads env vars inside it, and even a bare
// stat of the \\wsl$ share takes it Stopped -> Running. On a cold start that is
// ~5s of extra startup and ~1.8GB of VM memory, so the lookup is gated on the
// distro already running, using a metadata query that boots nothing.
func wslPrimeAgentDir() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	wslPrimeAgentDirOnce.Lock()
	defer wslPrimeAgentDirOnce.Unlock()
	if wslPrimeAgentDirDone {
		return wslPrimeAgentDirVal
	}
	if !wslDefaultDistroRunningLocked() {
		// Deliberately not cached: the user may start WSL later in this session,
		// and the dir only has to resolve when Prime Agent is actually usable.
		// That is what lets a running distro be picked up without a restart.
		return ""
	}
	wslPrimeAgentDirDone = true
	wslPrimeAgentDirVal = resolveWSLPrimeAgentDir()
	return wslPrimeAgentDirVal
}

// wslDefaultDistroRunningLocked reports whether the default WSL distro is
// already running. Caller must hold wslPrimeAgentDirOnce. Answers are reused
// for wslRunningCheckTTL because every check spawns wsl.exe.
func wslDefaultDistroRunningLocked() bool {
	if time.Since(wslRunningCheckedAt) < wslRunningCheckTTL {
		return wslRunningVal
	}
	wslRunningVal = wslDefaultDistroRunning()
	wslRunningCheckedAt = time.Now()
	return wslRunningVal
}

// wslExe returns the wsl.exe path, or "" when WSL is not available.
func wslExe() string {
	if wsl, ok := lookupBinary("wsl"); ok && wsl != "" {
		return wsl
	}
	if _, err := os.Stat(`C:\Windows\System32\wsl.exe`); err == nil {
		return `C:\Windows\System32\wsl.exe`
	}
	return ""
}

// decodeUTF16LE decodes wsl.exe's own output, which is UTF-16LE on a pipe
// (every ASCII byte arrives followed by a NUL) - unlike commands run *inside*
// the distro, whose stdout reaches us as UTF-8.
func decodeUTF16LE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

// parseWSLDistroList extracts distro names from `wsl --list --quiet` /
// `wsl --list --running --quiet` output. Empty output yields no names.
func parseWSLDistroList(out []byte) []string {
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(decodeUTF16LE(out), "\r\n", "\n"), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// wslDistroInList reports whether name appears among distro names from wsl.exe.
// Distro names are compared case-insensitively, as on the command line.
func wslDistroInList(names []string, name string) bool {
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// resolveWSLPrimeAgentDir queries the default WSL distro for its Prime Agent
// config dir (honoring the in-WSL env overrides) and verifies it is reachable
// through the \\wsl$ share. This boots the distro when it is not already
// running, so callers must confirm that first (see wslPrimeAgentDir).
func resolveWSLPrimeAgentDir() string {
	wsl := wslExe()
	if wsl == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// One spawn prints: distro name, home, and both legacy env overrides.
	// Args pass through exec directly (no shell quoting issues).
	cmd := exec.CommandContext(ctx, wsl, "-e", "sh", "-c",
		`echo "$WSL_DISTRO_NAME"; echo "$HOME"; echo "$PRIME_AGENT_CODING_AGENT_DIR"; echo "$PI_CODING_AGENT_DIR"`)
	// wsl.exe is a console-subsystem binary; without CREATE_NO_WINDOW the GUI
	// tray app gets a new console window flashing on screen at every startup.
	hideConsoleWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	distro := ""
	if len(lines) > 0 {
		distro = strings.TrimSpace(lines[0])
	}
	home, override, legacy := "", "", ""
	if len(lines) > 1 {
		home = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		override = strings.TrimSpace(lines[2])
	}
	if len(lines) > 3 {
		legacy = strings.TrimSpace(lines[3])
	}
	if distro == "" {
		return ""
	}
	dir := override
	if dir == "" {
		dir = legacy
	}
	if dir == "" {
		if home == "" || !strings.HasPrefix(home, "/") {
			return ""
		}
		dir = home + "/.prime/agent"
	}
	unc := wslUncPath(distro, dir)
	if _, err := os.Stat(unc); err != nil {
		return ""
	}
	return unc
}

// wslUncPath maps an absolute Linux path to its \\wsl$\<distro> UNC equivalent,
// e.g. ("Ubuntu", "/home/kavin/.prime/agent") ->
// "\\wsl$\Ubuntu\home\kavin\.prime\agent".
func wslUncPath(distro, linuxPath string) string {
	rel := strings.TrimPrefix(linuxPath, "/")
	rel = strings.ReplaceAll(rel, "/", `\`)
	return `\\wsl$\` + distro + `\` + rel
}

// primeAgentModelsPath returns <config-dir>/models.json, the file Prime Agent's
// ModelRegistry loads custom providers from (join(agentDir, "models.json")).
func primeAgentModelsPath() string {
	dir := primeAgentConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "models.json")
}

// primeAgentSettingsPath returns <config-dir>/settings.json. Prism does not
// write it (per user choice: never touch defaultProvider/defaultModel); it is
// only used for the installed check.
func primeAgentSettingsPath() string {
	dir := primeAgentConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "settings.json")
}

// isPrimeAgentInstalled reports whether Prime Agent is installed: the config
// dir exists (models.json or settings.json present) OR the `prime-agent`
// binary is on PATH.
func isPrimeAgentInstalled() bool {
	if p := primeAgentModelsPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	if p := primeAgentSettingsPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	if dir := primeAgentConfigDir(); dir != "" {
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}
	if p, ok := lookupBinary("prime-agent"); ok && p != "" {
		return true
	}
	return false
}

// isPrimeAgentActive reports whether Prism's provider config is present in
// Prime Agent's models.json.
func isPrimeAgentActive() bool {
	p := primeAgentModelsPath()
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
	if _, ok := providers["prism"]; ok {
		return true
	}
	if _, ok := providers["prism-responses"]; ok {
		return true
	}
	if _, ok := providers["prism-codex"]; ok {
		return true
	}
	return false
}

func usesPrimeAgentResponses(m config.ModelEntry, cfg *config.Config) bool {
	if m.API == "responses" {
		return true
	}
	if m.API == "chat_completions" {
		return false
	}
	return cfg.IsCodexProviderID(m.Provider)
}

// buildPrimeAgentModelEntries returns model entries filtered by protocol.
// When wantResponses is true, only responses models are included.
func buildPrimeAgentModelEntries(remap *config.ModelRemapping, cfg *config.Config, wantResponses bool) []interface{} {
	models := make([]interface{}, 0, len(remap.KnownModels))
	for _, m := range remap.KnownModels {
		if usesPrimeAgentResponses(m, cfg) != wantResponses {
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
			efforts := m.ReasoningEffort
			if len(efforts) == 0 {
				efforts = []string{"low", "medium", "high"}
			}
			tlm := map[string]interface{}{}
			tlm["off"] = nil
			tlm["minimal"] = nil
			for _, level := range efforts {
				tlm[level] = level
			}
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

// InstallPrimeAgentConfig writes Prism provider blocks into Prime Agent's
// models.json:
// - "prism" with api: openai-completions for chat_completions models
// - "prism-responses" with api: openai-responses for responses models
// (Zen muse-spark/gpt/grok or Codex OAuth).
// Prime Agent's provider api is per-model (with provider-level default), but
// Prism keeps the Pi-style two-provider split so mixed-protocol setups work
// and Pi/OMP/Prime Agent configs stay consistent. All other providers and
// settings are preserved. settings.json (defaultProvider/defaultModel) is
// intentionally left untouched.
func InstallPrimeAgentConfig(port int, remap *config.ModelRemapping) error {
	modelsPath := primeAgentModelsPath()
	if modelsPath == "" {
		return fmt.Errorf("cannot determine Prime Agent config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	cfg := config.Load()
	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"

	models, err := readJSONConfig(modelsPath)
	if err != nil {
		return fmt.Errorf("failed to read Prime Agent models config: %w", err)
	}
	ensureAgentBackup(modelsPath)

	providers, _ := models["providers"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	chatModels := buildPrimeAgentModelEntries(remap, cfg, false)
	responsesModels := buildPrimeAgentModelEntries(remap, cfg, true)

	if len(chatModels) > 0 {
		providers["prism"] = map[string]interface{}{
			"baseUrl": baseURL,
			"apiKey":  "prism",
			"api":     "openai-completions",
			"models":  chatModels,
		}
	} else {
		delete(providers, "prism")
	}
	if len(responsesModels) > 0 {
		providers["prism-responses"] = map[string]interface{}{
			"baseUrl": baseURL,
			"apiKey":  "prism",
			"api":     "openai-responses",
			"models":  responsesModels,
		}
	} else {
		delete(providers, "prism-responses")
	}
	// Clean up legacy "prism-codex" (pre-API-split)
	delete(providers, "prism-codex")

	models["providers"] = providers

	if err := writeJSONConfig(modelsPath, models); err != nil {
		return fmt.Errorf("failed to write Prime Agent models config: %w", err)
	}

	return nil
}

// RestorePrimeAgentConfig removes the "prism", "prism-responses" and legacy
// "prism-codex" provider blocks from Prime Agent's models.json, preserving all
// other providers and settings. settings.json is left untouched.
func RestorePrimeAgentConfig() error {
	modelsPath := primeAgentModelsPath()
	if modelsPath == "" {
		return fmt.Errorf("cannot determine Prime Agent config path")
	}

	models, err := readJSONConfig(modelsPath)
	if err != nil {
		return fmt.Errorf("failed to read Prime Agent models config: %w", err)
	}
	changed := false
	if providers, ok := models["providers"].(map[string]interface{}); ok {
		for _, provID := range []string{"prism", "prism-responses", "prism-codex"} {
			if _, exists := providers[provID]; exists {
				delete(providers, provID)
				changed = true
			}
		}
		models["providers"] = providers
	}
	if changed {
		if err := writeJSONConfig(modelsPath, models); err != nil {
			return fmt.Errorf("failed to write Prime Agent models config: %w", err)
		}
	}

	return nil
}

// syncPrimeAgent is called on proxy startup to sync the Prime Agent config
// when Prime Agent is installed. Silently skips when not installed or no models.
func syncPrimeAgent(port int) {
	if !isPrimeAgentInstalled() {
		return
	}
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Prime Agent] No models configured, skipping sync")
		return
	}
	if err := InstallPrimeAgentConfig(port, remap); err != nil {
		log.Printf("[Prime Agent] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Prime Agent] Synced %d models to %s", len(remap.KnownModels), primeAgentModelsPath())
}
