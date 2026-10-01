package agents

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"ollama-proxy/internal/config"
)

// DeepSeek Harness (DSH, github.com/deepseek-ai/deepseek-harness) is a Cordis
// plugin tree where "everything is a plugin". Unlike every other agent Prism
// integrates with, its model providers and its MCP servers live in different
// files under one home directory:
//
//   - Provider routes go into the `llm-pi-ai` row of each profile's
//     cordis.patch.yml — the same row the DSH Web UI's Models page writes.
//     DSH's settings service migrates an `llm-pi-ai:` section out of
//     settings.yaml into this row and then renames settings.yaml to
//     settings.yaml.imported, so writing settings.yaml only works until the
//     next migration retires it. Verified: on a real install DSH moved Prism's
//     routes from settings.yaml into profiles/desktop/cordis.patch.yml and
//     left the web profile without them. Prism therefore writes where DSH
//     keeps them, merging its routes in beside any provider the user added.
//   - MCP goes into the home-level cordis.patch.yml as one `- insert:` row, so
//     a single write covers every profile and the file is never rewritten by
//     the UI.
//
// Two routes are written because DSH binds one wire protocol per route:
// `prism` for openai-completions and `prism-responses` for openai-responses.
// Prism cannot instead mount a second dsh-llm-pi-ai instance: that plugin
// declares the entire installed provider catalog into a shared service when it
// mounts, so a second instance fails the whole plugin tree with
// DUPLICATE_DIRECTORY and DSH refuses to start. Expressing Prism as routes
// inside the existing single instance is the only shape that boots.
const (
	// deepSeekHarnessProviderID is the openai-completions route.
	deepSeekHarnessProviderID = "prism"
	// deepSeekHarnessResponsesProviderID is the openai-responses route, written
	// only when at least one model uses the Responses API.
	deepSeekHarnessResponsesProviderID = "prism-responses"

	// deepSeekHarnessRowID is the loader row Prism merges its routes into.
	deepSeekHarnessRowID = "llm-pi-ai"

	deepSeekHarnessProviderManagedBegin = "# >>> prism providers managed >>>"
	deepSeekHarnessProviderManagedEnd   = "# <<< prism providers managed <<<"
	deepSeekHarnessMCPManagedBegin      = "# >>> prism mcp managed >>>"
	deepSeekHarnessMCPManagedEnd        = "# <<< prism mcp managed <<<"
)

// deepSeekHarnessAuthorization is the bearer token both the provider routes and
// the MCP row send. It is the same token Prism's MCP endpoint accepts.
const deepSeekHarnessAuthorization = "Bearer " + prismMCPToken

// deepSeekHarnessHome returns $DSH_HOME, else ~/.dsh — the same resolution the
// harness itself uses.
func deepSeekHarnessHome() string {
	if root := os.Getenv("DSH_HOME"); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".dsh")
}

// deepSeekHarnessProfilesDir is the directory holding one subdirectory per
// initialized profile.
func deepSeekHarnessProfilesDir() string {
	home := deepSeekHarnessHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "profiles")
}

// deepSeekHarnessSettingsPath is the legacy location the first version of this
// integration wrote to. DSH retires the file on its next settings migration, so
// Prism only ever strips its own region from here.
func deepSeekHarnessSettingsPath() string {
	home := deepSeekHarnessHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "settings.yaml")
}

// deepSeekHarnessPatchPath is the home-level loader patch holding the MCP row.
// It applies to every profile at once and is left alone by the Web UI.
func deepSeekHarnessPatchPath() string {
	home := deepSeekHarnessHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "cordis.patch.yml")
}

// deepSeekHarnessProfilePatchPaths lists each profile's patch file, sorted so
// writes are deterministic. A profile directory counts once it carries either
// patch layer; the shared node_modules store is not a profile.
func deepSeekHarnessProfilePatchPaths() []string {
	root := deepSeekHarnessProfilesDir()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "node_modules" {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		patch := filepath.Join(dir, "cordis.patch.yml")
		if _, err := os.Stat(patch); err == nil {
			paths = append(paths, patch)
			continue
		}
		// DSH creates cordis.yml with the profile; the patch layer is optional
		// and Prism may create it.
		if _, err := os.Stat(filepath.Join(dir, "cordis.yml")); err == nil {
			paths = append(paths, patch)
		}
	}
	sort.Strings(paths)
	return paths
}

// isDeepSeekHarnessInstalled reports whether DSH is present. The dominant
// install is `npx @deepseek-ai/dsh`, which never puts a binary on PATH, so the
// harness home existing with any content is the primary signal.
func isDeepSeekHarnessInstalled() bool {
	if home := deepSeekHarnessHome(); home != "" {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			if entries, err := os.ReadDir(home); err == nil && len(entries) > 0 {
				return true
			}
		}
	}
	if bin, ok := lookupBinary("dsh"); ok && bin != "" {
		return true
	}
	return false
}

// deepSeekHarnessProvidersPresent reports whether any profile carries Prism's
// provider region. Providers live in more than one file, so IsAgentActive
// cannot use its single-file read path for DSH.
func deepSeekHarnessProvidersPresent() bool {
	for _, path := range deepSeekHarnessProfilePatchPaths() {
		data, err := os.ReadFile(path)
		if err == nil && hasDeepSeekHarnessProviders(data) {
			return true
		}
	}
	return false
}

// hasDeepSeekHarnessProviders reports whether a patch carries Prism's provider
// region with at least one route.
func hasDeepSeekHarnessProviders(data []byte) bool {
	if !strings.Contains(string(data), deepSeekHarnessProviderManagedBegin) {
		return false
	}
	var entries []map[string]interface{}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		// A patch may carry `!!js` expressions on other rows that yaml.v3
		// cannot represent; the marker alone is enough to call Prism present.
		return true
	}
	for _, entry := range entries {
		if id, _ := entry["id"].(string); id != deepSeekHarnessRowID {
			continue
		}
		cfg, _ := entry["config"].(map[string]interface{})
		if cfg == nil {
			return false
		}
		providers, _ := cfg["providers"].(map[string]interface{})
		if providers == nil {
			return false
		}
		if _, set := providers[deepSeekHarnessProviderID]; set {
			return true
		}
		_, set := providers[deepSeekHarnessResponsesProviderID]
		return set
	}
	return false
}

// usesDeepSeekHarnessResponses reports whether a model must ride the
// openai-responses route. Unset API values defer to the provider, matching how
// the proxy itself decides.
func usesDeepSeekHarnessResponses(m config.ModelEntry, cfg *config.Config) bool {
	if m.API == "responses" {
		return true
	}
	if m.API == "chat_completions" {
		return false
	}
	return cfg.IsCodexProviderID(m.Provider)
}

// writeDeepSeekHarnessModel emits one models list item. The output cap is
// `maxTokens`: dsh-llm-pi-ai's PiAiModelProfile declares no maxOutputTokens
// field, so writing that name would silently drop every model's cap.
func writeDeepSeekHarnessModel(b *strings.Builder, m config.ModelEntry) {
	ctx := m.ContextLength
	if ctx == 0 {
		ctx = 128000
	}
	b.WriteString("        - id: " + yamlQuote(prismModelRouteKey(m)) + "\n")
	b.WriteString("          contextWindow: " + fmt.Sprintf("%d", ctx) + "\n")
	if m.MaxOutputTokens > 0 {
		b.WriteString("          maxTokens: " + fmt.Sprintf("%d", m.MaxOutputTokens) + "\n")
	}
}

// writeDeepSeekHarnessRoute emits one provider route under config.providers.
//
// supportsDeveloperRole is declared for both protocols (both take it) and set
// false because Prism's proxy rewrites system prompts itself and pi-ai only
// sends the `developer` role to reasoning models. maxTokensField is
// openai-completions-only: a route-level compat switch the route's protocol
// cannot take makes DSH skip every model on that route, so it is written for
// the chat route alone.
func writeDeepSeekHarnessRoute(b *strings.Builder, id, displayName, api, baseURL string, models []config.ModelEntry) {
	b.WriteString("      " + id + ":\n")
	b.WriteString("        displayName: " + displayName + "\n")
	b.WriteString("        api: " + api + "\n")
	b.WriteString("        baseURL: " + baseURL + "\n")
	b.WriteString("        headers:\n")
	b.WriteString("          Authorization: " + deepSeekHarnessAuthorization + "\n")
	b.WriteString("        compat:\n")
	b.WriteString("          supportsDeveloperRole: false\n")
	if api == "openai-completions" {
		b.WriteString("          maxTokensField: max_tokens\n")
	}
	b.WriteString("        models:\n")
	for _, m := range models {
		writeDeepSeekHarnessModel(b, m)
	}
}

// buildDeepSeekHarnessProviderBlock emits the managed region that lives inside
// the llm-pi-ai row's config.providers map, indented one level below
// `providers:`. Models are split by wire protocol because each route binds one.
func buildDeepSeekHarnessProviderBlock(port int, remap *config.ModelRemapping, cfg *config.Config) string {
	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"
	var chatModels, responsesModels []config.ModelEntry
	for _, m := range remap.KnownModels {
		if usesDeepSeekHarnessResponses(m, cfg) {
			responsesModels = append(responsesModels, m)
		} else {
			chatModels = append(chatModels, m)
		}
	}
	var b strings.Builder
	b.WriteString("      " + deepSeekHarnessProviderManagedBegin + "\n")
	if len(chatModels) > 0 {
		writeDeepSeekHarnessRoute(&b, deepSeekHarnessProviderID, "Prism", "openai-completions", baseURL, chatModels)
	}
	if len(responsesModels) > 0 {
		writeDeepSeekHarnessRoute(&b, deepSeekHarnessResponsesProviderID, "Prism (Responses)", "openai-responses", baseURL, responsesModels)
	}
	b.WriteString("      " + deepSeekHarnessProviderManagedEnd + "\n")
	return b.String()
}

// indentOf returns the number of leading spaces on a line.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// insertLinesAfter returns lines with add spliced in directly after index idx.
func insertLinesAfter(lines []string, idx int, add []string) []string {
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:idx+1]...)
	out = append(out, add...)
	out = append(out, lines[idx+1:]...)
	return out
}

// stripDeepSeekHarnessProviderKeys removes `prism:` / `prism-responses:` blocks
// that sit under config.providers without Prism's markers. DSH's own settings
// migration writes those keys into this row, so a patch can already carry them
// before Prism ever touches it; leaving them would duplicate the key and let
// the copy YAML reads last win.
func stripDeepSeekHarnessProviderKeys(lines []string, start, end int) []string {
	var out []string
	for i := 0; i < len(lines); i++ {
		if i > start && i < end && indentOf(lines[i]) == 6 {
			key := strings.TrimSuffix(strings.TrimSpace(lines[i]), ":")
			if key == deepSeekHarnessProviderID || key == deepSeekHarnessResponsesProviderID {
				// Drop the key and every deeper line that belongs to it.
				for i+1 < len(lines) && i+1 < end {
					next := lines[i+1]
					if strings.TrimSpace(next) != "" && indentOf(next) <= 6 {
						break
					}
					i++
				}
				continue
			}
		}
		out = append(out, lines[i])
	}
	return out
}

// insertDeepSeekHarnessProviders merges the managed provider block into a
// profile patch's llm-pi-ai row, creating the row, its config and its providers
// map when any are missing. Every other row, key and comment survives.
func insertDeepSeekHarnessProviders(content, block string) (string, error) {
	lines := strings.Split(content, "\n")
	blockLines := strings.Split(strings.TrimRight(block, "\n"), "\n")

	start := -1
	for i, line := range lines {
		if indentOf(line) != 0 {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if strings.HasPrefix(rest, "id:") {
			id := strings.TrimSpace(strings.TrimPrefix(rest, "id:"))
			id = strings.Trim(id, `"'`)
			if id == deepSeekHarnessRowID {
				start = i
				break
			}
		}
	}

	if start < 0 {
		// This profile has no llm-pi-ai row yet, so add a whole one. An existing
		// `[]` placeholder is replaced in place to keep header comments.
		row := append([]string{
			"- id: " + deepSeekHarnessRowID,
			"  name: '@deepseek-ai/dsh-llm-pi-ai'",
			"  config:",
			"    providers:",
		}, blockLines...)
		return withDeepSeekHarnessPatchRow(lines, row), nil
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "-") {
			end = i
			break
		}
	}

	lines = stripDeepSeekHarnessProviderKeys(lines, start, end)
	// The strip only removes lines, so the entry boundaries still hold.

	configIdx, providersIdx := -1, -1
	for i := start; i < end; i++ {
		if indentOf(lines[i]) == 2 && strings.HasPrefix(strings.TrimSpace(lines[i]), "config:") {
			configIdx = i
			continue
		}
		if indentOf(lines[i]) == 4 && strings.HasPrefix(strings.TrimSpace(lines[i]), "providers:") {
			providersIdx = i
			break
		}
	}

	switch {
	case providersIdx >= 0:
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[providersIdx]), "providers:"))
		switch value {
		case "", "{}":
			// A bare key, or an empty flow mapping rewritten to one.
			lines[providersIdx] = "    providers:"
		default:
			return "", fmt.Errorf("DeepSeek Harness profile patch has an inline value for config.providers that Prism would clobber; put it on its own lines and retry")
		}
		lines = insertLinesAfter(lines, providersIdx, blockLines)

	case configIdx >= 0:
		scaffold := append([]string{"    providers:"}, blockLines...)
		lines = insertLinesAfter(lines, configIdx, scaffold)

	default:
		// No config at all: add it after the row's name line.
		anchor := start
		for i := start + 1; i < end; i++ {
			if indentOf(lines[i]) == 2 && strings.HasPrefix(strings.TrimSpace(lines[i]), "name:") {
				anchor = i
				break
			}
		}
		scaffold := append([]string{"  config:", "    providers:"}, blockLines...)
		lines = insertLinesAfter(lines, anchor, scaffold)
	}

	return strings.Join(lines, "\n"), nil
}

// withDeepSeekHarnessPatchRow appends a row to a top-level YAML sequence,
// replacing an empty `[]` document in place so the profile patch's header
// comments survive.
func withDeepSeekHarnessPatchRow(lines []string, row []string) string {
	for i, line := range lines {
		if strings.TrimSpace(line) == "[]" {
			out := make([]string, 0, len(lines)+len(row))
			out = append(out, lines[:i]...)
			out = append(out, row...)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n")
		}
	}
	trimmed := strings.TrimSpace(strings.Join(lines, "\n"))
	if trimmed == "" || trimmed == "{}" {
		return strings.Join(row, "\n") + "\n"
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n" + strings.Join(row, "\n") + "\n"
}

// stripLegacyDeepSeekHarnessSettings removes Prism's region from the legacy
// settings.yaml the first version of this integration wrote. DSH migrates that
// section into a profile patch and retires the file, so a leftover here would
// be re-migrated and could shadow a provider the user added in the UI.
func stripLegacyDeepSeekHarnessSettings() error {
	path := deepSeekHarnessSettingsPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read DeepSeek Harness settings: %w", err)
	}
	cleaned := stripMarkerRegion(string(data), deepSeekHarnessProviderManagedBegin, deepSeekHarnessProviderManagedEnd)
	if cleaned == string(data) {
		return nil
	}
	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove DeepSeek Harness settings: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, []byte(cleaned), 0644); err != nil {
		return fmt.Errorf("failed to write DeepSeek Harness settings: %w", err)
	}
	return nil
}

// InstallDeepSeekHarnessConfig writes Prism's provider routes into every DSH
// profile's patch, merging them into the llm-pi-ai row so any provider the user
// configured in the Web UI survives. Re-syncs are idempotent: Prism's own
// region is stripped and re-inserted, and a row DSH's settings migration wrote
// earlier is adopted. A one-time .prism-backup is kept per file.
//
// It deliberately does not write `agent-default-model` into any layer. That row
// is the user's model choice, made in the DSH Web UI, and Prism changing it
// would silently override them.
func InstallDeepSeekHarnessConfig(port int, remap *config.ModelRemapping) error {
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}
	paths := deepSeekHarnessProfilePatchPaths()
	if len(paths) == 0 {
		return fmt.Errorf("no DeepSeek Harness profiles found under %s; run the harness once to create one", deepSeekHarnessProfilesDir())
	}
	block := buildDeepSeekHarnessProviderBlock(port, remap, config.Load())

	for _, path := range paths {
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to read DeepSeek Harness profile patch: %w", err)
		}
		cleaned := stripMarkerRegion(string(existing), deepSeekHarnessProviderManagedBegin, deepSeekHarnessProviderManagedEnd)
		result, err := insertDeepSeekHarnessProviders(cleaned, block)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		ensureAgentBackup(path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("failed to create DeepSeek Harness profile dir: %w", err)
		}
		if err := os.WriteFile(path, []byte(result), 0644); err != nil {
			return fmt.Errorf("failed to write DeepSeek Harness profile patch: %w", err)
		}
	}
	return stripLegacyDeepSeekHarnessSettings()
}

// RestoreDeepSeekHarnessConfig removes Prism's managed provider region from
// every profile patch, leaving the MCP patch, the llm-pi-ai row's other
// providers and every user key alone.
func RestoreDeepSeekHarnessConfig() error {
	for _, path := range deepSeekHarnessProfilePatchPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("failed to read DeepSeek Harness profile patch: %w", err)
		}
		cleaned := stripMarkerRegion(string(data), deepSeekHarnessProviderManagedBegin, deepSeekHarnessProviderManagedEnd)
		if cleaned == string(data) {
			continue
		}
		if err := os.WriteFile(path, []byte(cleaned), 0644); err != nil {
			return fmt.Errorf("failed to write DeepSeek Harness profile patch: %w", err)
		}
	}
	return stripLegacyDeepSeekHarnessSettings()
}

// syncDeepSeekHarness runs on proxy startup to keep DSH's routes in step with
// the configured models. Silently skips an uninstalled harness.
func syncDeepSeekHarness(port int) {
	if !isDeepSeekHarnessInstalled() {
		return
	}
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[DeepSeek Harness] No models configured, skipping sync")
		return
	}
	paths := deepSeekHarnessProfilePatchPaths()
	if len(paths) == 0 {
		log.Printf("[DeepSeek Harness] No profiles initialized yet, skipping sync")
		return
	}
	if err := InstallDeepSeekHarnessConfig(port, remap); err != nil {
		log.Printf("[DeepSeek Harness] Failed to sync config: %v", err)
		return
	}
	log.Printf("[DeepSeek Harness] Synced %d models to %d profile(s)", len(remap.KnownModels), len(paths))
}
