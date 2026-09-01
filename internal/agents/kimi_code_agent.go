package agents

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"ollama-proxy/internal/config"
)

// kimiCodeProviderID is the provider name used for the Prism provider in Kimi
// Code CLI's config.toml. Model aliases reference it via `provider`.
const kimiCodeProviderID = "prism"

// kimiCodeConfigPath returns the path to Kimi Code CLI's config.toml,
// honoring the KIMI_CODE_HOME override and defaulting to ~/.kimi-code.
func kimiCodeConfigPath() string { return agentConfigPath("kimi-code") }

// isKimiCodeInstalled reports whether Kimi Code CLI is installed: the
// config.toml file exists OR the `kimi` binary is on PATH. (Kimi Code may not
// create its config file until first run, so the binary check avoids a false
// "not installed".)
func isKimiCodeInstalled() bool {
	if isAgentConfigInstalled("kimi-code") {
		return true
	}
	if p, ok := lookupBinary("kimi"); ok && p != "" {
		return true
	}
	return false
}

func isKimiCodeActive() bool { return IsAgentActive("kimi-code") }

// sanitizeKimiModelKey lowercases the id and replaces every rune not in
// [a-z0-9] with '-', trimming leading/trailing '-'. Used to build a safe TOML
// quoted key for the [models."prism-<key>"] alias.
func sanitizeKimiModelKey(id string) string {
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

// stripKimiPrismSections removes the managed [providers.prism] /
// [providers.prism-responses] and [models."prism-*"] section blocks that may
// exist outside the managed markers (e.g. written by an older version), so a
// re-sync never leaves duplicates. Each section starts with a matching header
// and runs until the next section header (or EOF).
func stripKimiPrismSections(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	skipping := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "[providers."+kimiCodeProviderID+"]" ||
			t == "[providers."+kimiCodeProviderID+"-responses]" ||
			strings.HasPrefix(t, "[models.\"prism-") ||
			strings.HasPrefix(t, "[models.prism-") {
			skipping = true
			continue
		}
		if skipping {
			if strings.HasPrefix(t, "[") || t == codexManagedBegin || t == codexManagedEnd {
				skipping = false
				result = append(result, line)
			}
		} else {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

func usesKimiResponses(m config.ModelEntry, cfg *config.Config) bool {
	if m.API == "responses" {
		return true
	}
	if m.API == "chat_completions" {
		return false
	}
	return cfg.IsCodexProviderID(m.Provider)
}

// buildKimiCodeProviderSection writes the [providers.prism] block that points
// Kimi Code CLI at the Prism proxy. When any responses models exist, a second
// [providers.prism-responses] block is also written so Kimi can route those
// models via /v1/responses. api_key "prism" is the token the proxy accepts.
func buildKimiCodeProviderSection(port int, hasResponses bool) string {
	baseURL := "http://127.0.0.1:" + fmt.Sprintf("%d", port) + "/v1"
	s := "[providers." + kimiCodeProviderID + "]\n" +
		"type = \"openai\"\n" +
		"base_url = " + tomlQuote(baseURL) + "\n" +
		"api_key = \"prism\"\n" +
		"\n"
	if hasResponses {
		s += "[providers." + kimiCodeProviderID + "-responses]\n" +
			"type = \"openai\"\n" +
			"base_url = " + tomlQuote(baseURL) + "\n" +
			"api_key = \"prism\"\n" +
			"\n"
	}
	return s
}

// buildKimiCodeModelSections writes one [models."prism-<key>"] section per
// known Prism model, each referencing the appropriate Prism provider based on
// its selected protocol (m.API). Responses models point to prism-responses,
// chat models to prism.
func buildKimiCodeModelSections(remap *config.ModelRemapping, cfg *config.Config) string {
	var b strings.Builder
	for _, m := range remap.KnownModels {
		routeKey := prismModelRouteKey(m)
		key := "prism-" + sanitizeKimiModelKey(routeKey)
		providerID := kimiCodeProviderID
		if usesKimiResponses(m, cfg) {
			providerID = kimiCodeProviderID + "-responses"
		}
		ctx := m.ContextLength
		if ctx == 0 {
			ctx = 128000
		}
		b.WriteString("[models.\"" + key + "\"]\n")
		b.WriteString("provider = " + tomlQuote(providerID) + "\n")
		b.WriteString("model = " + tomlQuote(routeKey) + "\n")
		b.WriteString(fmt.Sprintf("max_context_size = %d\n", ctx))
		b.WriteString("display_name = " + tomlQuote(prismModelDisplayName(cfg, m)) + "\n")
		if m.MaxOutputTokens > 0 {
			b.WriteString(fmt.Sprintf("max_output_size = %d\n", m.MaxOutputTokens))
		}
		capabilities := []string{"tool_use"}
		if m.Capabilities != nil && m.Capabilities.Vision {
			capabilities = append(capabilities, "image_in")
		}
		if m.Reasoning {
			capabilities = append(capabilities, "thinking")
			efforts := m.ReasoningEffort
			if len(efforts) == 0 {
				efforts = []string{"low", "medium", "high"}
			}
			b.WriteString("support_efforts = [" + joinQuoted(efforts) + "]\n")
			b.WriteString("default_effort = " + tomlQuote(pickDefaultEffort(efforts)) + "\n")
		}
		b.WriteString("capabilities = [" + joinQuoted(capabilities) + "]\n")
		b.WriteString("\n")
	}
	return b.String()
}

// joinQuoted joins strings into a TOML array literal of double-quoted values.
func joinQuoted(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, tomlQuote(it))
	}
	return strings.Join(quoted, ", ")
}

// pickDefaultEffort prefers "high", then "max", then the last (highest) entry.
func pickDefaultEffort(efforts []string) string {
	for _, pref := range []string{"high", "max"} {
		for _, e := range efforts {
			if e == pref {
				return e
			}
		}
	}
	return efforts[len(efforts)-1]
}

// InstallKimiCodeConfig writes a Prism managed block containing the provider
// and model sections into ~/.kimi-code/config.toml (or $KIMI_CODE_HOME). Any
// prior managed block and stray Prism sections are stripped first, making
// re-sync idempotent. A one-time .prism-backup is kept of the original content.
func InstallKimiCodeConfig(port int, remap *config.ModelRemapping) error {
	p := kimiCodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Kimi Code config path")
	}
	if remap == nil || len(remap.KnownModels) == 0 {
		return fmt.Errorf("no Prism models configured")
	}

	existing, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read Kimi Code config: %w", err)
	}
	cleaned := stripManagedBlocks(string(existing))
	cleaned = stripKimiPrismSections(cleaned)
	ensureAgentBackup(p)

	cfg := config.Load()
	hasResponses := false
	for _, m := range remap.KnownModels {
		if usesKimiResponses(m, cfg) {
			hasResponses = true
			break
		}
	}
	var block strings.Builder
	block.WriteString("\n" + codexManagedBegin + "\n")
	block.WriteString(buildKimiCodeProviderSection(port, hasResponses))
	block.WriteString(buildKimiCodeModelSections(remap, cfg))
	block.WriteString(codexManagedEnd + "\n")

	result := strings.TrimRight(cleaned, "\r\n") + "\n" + block.String()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return fmt.Errorf("failed to create Kimi Code config dir: %w", err)
	}
	if err := os.WriteFile(p, []byte(result), 0644); err != nil {
		return fmt.Errorf("failed to write Kimi Code config: %w", err)
	}
	return nil
}

// RestoreKimiCodeConfig removes the Prism managed block from the Kimi Code
// config.toml, preserving all of the user's own content.
func RestoreKimiCodeConfig() error {
	p := kimiCodeConfigPath()
	if p == "" {
		return fmt.Errorf("cannot determine Kimi Code config path")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read Kimi Code config: %w", err)
	}
	cleaned := stripManagedBlocks(string(data))
	if cleaned != string(data) {
		if err := os.WriteFile(p, []byte(cleaned), 0644); err != nil {
			return fmt.Errorf("failed to write Kimi Code config: %w", err)
		}
	}
	return nil
}

// syncKimiCode is called on proxy startup to sync the Kimi Code config when
// Kimi Code CLI is installed. Silently skips when not installed or no models.
func syncKimiCode(port int) {
	if !isKimiCodeInstalled() {
		return
	}
	remap := config.LoadModelRemapping()
	if len(remap.KnownModels) == 0 {
		log.Printf("[Kimi Code] No models configured, skipping sync")
		return
	}
	if err := InstallKimiCodeConfig(port, remap); err != nil {
		log.Printf("[Kimi Code] Failed to sync config: %v", err)
		return
	}
	log.Printf("[Kimi Code] Synced %d models to %s", len(remap.KnownModels), kimiCodeConfigPath())
}
