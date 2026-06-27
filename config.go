package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ProviderConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

type AgentIntegrationsConfig struct {
	// ClaudeCodeTiers maps Claude Code model tiers ("opus", "sonnet", "haiku",
	// "subagent") to a Prism model id. Empty values resolve to the first Prism
	// model at sync time.
	ClaudeCodeTiers map[string]string `json:"claude_code_tiers,omitempty"`
}

type Config struct {
	DefaultProvider   string                   `json:"default_provider"`
	OllamaCloud       *ProviderConfig          `json:"ollama_cloud"`
	OpenCodeGo        *ProviderConfig          `json:"opencode_go"`
	CustomProviders   []*ProviderConfig        `json:"custom_providers"`
	OAuthAccounts     []*OAuthAccount          `json:"oauth_accounts"`
	AgentIntegrations *AgentIntegrationsConfig `json:"agent_integrations,omitempty"`
}

// clone returns a deep copy of the Config, safe for mutation without affecting the original
func (c *Config) clone() *Config {
	cp := *c
	if c.OllamaCloud != nil {
		oc := *c.OllamaCloud
		cp.OllamaCloud = &oc
	}
	if c.OpenCodeGo != nil {
		og := *c.OpenCodeGo
		cp.OpenCodeGo = &og
	}
	if c.CustomProviders != nil {
		cp.CustomProviders = make([]*ProviderConfig, len(c.CustomProviders))
		for i, p := range c.CustomProviders {
			pc := *p
			cp.CustomProviders[i] = &pc
		}
	}
	if c.OAuthAccounts != nil {
		cp.OAuthAccounts = make([]*OAuthAccount, len(c.OAuthAccounts))
		for i, a := range c.OAuthAccounts {
			oa := *a
			cp.OAuthAccounts[i] = &oa
		}
	}
	return &cp
}

// ModelCapabilities describes what a model can do
type ModelCapabilities struct {
	ToolCalling       bool `json:"tool_calling,omitempty"`
	StructuredOutputs bool `json:"structured_outputs,omitempty"`
	Vision            bool `json:"vision,omitempty"`
}

// ModelEntry represents a known model with its associated provider and capabilities
type ModelEntry struct {
	ID              string             `json:"id"`
	Provider        string             `json:"provider"`
	Reasoning       bool               `json:"reasoning,omitempty"`
	ReasoningEffort []string           `json:"reasoning_effort,omitempty"`
	ContextLength   int                `json:"context_length,omitempty"`
	MaxOutputTokens int                `json:"max_output_tokens,omitempty"`
	Capabilities    *ModelCapabilities `json:"capabilities,omitempty"`
}

type ModelRemapping struct {
	DefaultModel string            `json:"default_model"`
	KnownModels  []ModelEntry      `json:"known_models"`
	Aliases      map[string]string `json:"aliases"`
}

// ProviderInfo holds resolved provider details for routing requests
type ProviderInfo struct {
	BaseURL      string
	APIKey       string
	ProviderType string
	Name         string
}

// ResolvedProvider holds per-request resolved provider details
type ResolvedProvider struct {
	BaseURL           string
	APIKey            string
	ProviderType      string
	ProviderID        string // e.g. "ollama_cloud", "opencode_go", custom ID, or OAuth account ID
	ChatGPTAccountID  string // for Codex OAuth: the chatgpt-account-id header value
}

// chatCompletionsURL returns the full URL for /chat/completions, handling
// base URLs that already include /v1 (e.g. https://api.groq.com/openai/v1)
// and those that don't (e.g. https://api.openai.com).
func (rp *ResolvedProvider) chatCompletionsURL() string {
	if strings.HasSuffix(rp.BaseURL, "/v1") || strings.Contains(rp.BaseURL, "/v1/") {
		return rp.BaseURL + "/chat/completions"
	}
	return rp.BaseURL + "/v1/chat/completions"
}

// apiChatURL returns the full URL for Ollama's /api/chat endpoint.
func (rp *ResolvedProvider) apiChatURL() string {
	return rp.BaseURL + "/api/chat"
}

// responsesURL returns the full URL for the Responses API endpoint.
// For Codex providers, the base URL already points to chatgpt.com/backend-api/codex,
// so we just append /responses. For other OpenAI-compatible providers, we use /v1/responses.
func (rp *ResolvedProvider) responsesURL() string {
	if rp.ProviderType == "codex" {
		return rp.BaseURL + "/responses"
	}
	if strings.HasSuffix(rp.BaseURL, "/v1") || strings.Contains(rp.BaseURL, "/v1/") {
		return rp.BaseURL + "/responses"
	}
	return rp.BaseURL + "/v1/responses"
}

func getConfigPath() string {
	return filepath.Join(getConfigDir(), "config.json")
}

// rawConfig is used for migration from old formats
type rawConfig struct {
	ActiveProvider  string            `json:"active_provider"`
	DefaultProvider string            `json:"default_provider"`
	OllamaCloud     *ProviderConfig   `json:"ollama_cloud"`
	OpenCodeGo      *ProviderConfig   `json:"opencode_go"`
	Custom          *ProviderConfig   `json:"custom"`
	CustomProviders []*ProviderConfig `json:"custom_providers"`
}

func loadConfig() *Config {
	cfg := defaultConfig()
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return cfg
	}

	// First try to unmarshal into the new format
	if err := json.Unmarshal(data, cfg); err != nil {
		return defaultConfig()
	}

	// Migration: if old "active_provider" field exists and "default_provider" is empty, migrate it
	var raw rawConfig
	if json.Unmarshal(data, &raw) == nil {
		needsSave := false

		// Migrate active_provider → default_provider
		if raw.ActiveProvider != "" && cfg.DefaultProvider == "" {
			cfg.DefaultProvider = raw.ActiveProvider
			needsSave = true
		}

		// Migrate old "custom" field to custom_providers
		if raw.Custom != nil && (raw.Custom.BaseURL != "" || raw.Custom.APIKey != "") && len(raw.CustomProviders) == 0 {
			raw.Custom.ID = "custom"
			cfg.CustomProviders = []*ProviderConfig{raw.Custom}
			if cfg.DefaultProvider == "" || cfg.DefaultProvider == "custom" {
				cfg.DefaultProvider = "custom"
			}
			needsSave = true
		} else if raw.Custom != nil && len(raw.CustomProviders) == 0 {
			cfg.CustomProviders = []*ProviderConfig{}
			if cfg.DefaultProvider == "custom" {
				cfg.DefaultProvider = "ollama_cloud"
			}
			needsSave = true
		}

		if needsSave {
			saveConfig(cfg)
		}
	}

	// Ensure IDs on built-in providers
	if cfg.OllamaCloud != nil {
		cfg.OllamaCloud.ID = "ollama_cloud"
	}
	if cfg.OpenCodeGo != nil {
		cfg.OpenCodeGo.ID = "opencode_go"
	}

	if cfg.OllamaCloud == nil {
		cfg.OllamaCloud = &ProviderConfig{ID: "ollama_cloud", Name: "Ollama Cloud", BaseURL: "https://ollama.com"}
	}
	if cfg.OpenCodeGo == nil {
		cfg.OpenCodeGo = &ProviderConfig{ID: "opencode_go", Name: "OpenCode Go", BaseURL: "https://opencode.ai/zen/go"}
	}
	if cfg.CustomProviders == nil {
		cfg.CustomProviders = []*ProviderConfig{}
	}
	if cfg.OAuthAccounts == nil {
		cfg.OAuthAccounts = []*OAuthAccount{}
	}
	if cfg.AgentIntegrations == nil {
		cfg.AgentIntegrations = &AgentIntegrationsConfig{
			ClaudeCodeTiers: map[string]string{},
		}
	}
	if cfg.AgentIntegrations.ClaudeCodeTiers == nil {
		cfg.AgentIntegrations.ClaudeCodeTiers = map[string]string{}
	}
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "ollama_cloud"
	}
	return cfg
}

func saveConfig(cfg *Config) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getConfigPath(), data, 0600)
}

func defaultConfig() *Config {
	return &Config{
		DefaultProvider: "ollama_cloud",
		OllamaCloud: &ProviderConfig{
			ID:      "ollama_cloud",
			Name:    "Ollama Cloud",
			BaseURL: "https://ollama.com",
		},
		OpenCodeGo: &ProviderConfig{
			ID:      "opencode_go",
			Name:    "OpenCode Go",
			BaseURL: "https://opencode.ai/zen/go",
		},
		CustomProviders: []*ProviderConfig{},
		OAuthAccounts:   []*OAuthAccount{},
		AgentIntegrations: &AgentIntegrationsConfig{
			ClaudeCodeTiers: map[string]string{},
		},
	}
}

// getProviderByID returns provider info for any provider (built-in, custom, or OAuth)
func (c *Config) getProviderByID(id string) (*ProviderInfo, error) {
	// Check OAuth accounts first (they may have their own provider ID)
	for _, a := range c.OAuthAccounts {
		if a.ID == id {
			return &ProviderInfo{
				BaseURL:      codexChatGPTBase + "/backend-api/codex",
				APIKey:       a.AccessToken,
				ProviderType: "codex",
				Name:         a.Label + " (" + a.Email + ")",
			}, nil
		}
	}

	switch id {
	case "ollama_cloud":
		apiKey := c.OllamaCloud.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("OLLAMA_API_KEY")
		}
		return &ProviderInfo{
			BaseURL:      c.OllamaCloud.BaseURL,
			APIKey:       apiKey,
			ProviderType: "ollama",
			Name:         c.OllamaCloud.Name,
		}, nil
	case "opencode_go":
		apiKey := c.OpenCodeGo.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("OPENCODE_GO_API_KEY")
		}
		return &ProviderInfo{
			BaseURL:      c.OpenCodeGo.BaseURL,
			APIKey:       apiKey,
			ProviderType: "openai",
			Name:         c.OpenCodeGo.Name,
		}, nil
	}

	// Check custom providers
	for _, p := range c.CustomProviders {
		if p.ID == id {
			return &ProviderInfo{
				BaseURL:      p.BaseURL,
				APIKey:       p.APIKey,
				ProviderType: "openai",
				Name:         p.Name,
			}, nil
		}
	}

	return nil, fmt.Errorf("provider not found: %s", id)
}

// getProviderName returns a human-readable name for a provider ID
func (c *Config) getProviderName(id string) string {
	info, err := c.getProviderByID(id)
	if err != nil {
		return id
	}
	return info.Name
}

// isCodexProviderID returns true if the provider ID corresponds to a Codex OAuth account.
// Checks both exact matches against configured OAuth accounts and the "codex_" prefix
// (in case the account was removed/re-added with a new ID but the model still references the old one).
func (c *Config) isCodexProviderID(providerID string) bool {
	for _, a := range c.OAuthAccounts {
		if a.ID == providerID {
			return true
		}
	}
	return strings.HasPrefix(providerID, "codex_")
}

// resolveModel resolves a requested model name to (resolvedModel, providerID)
func resolveModel(remap *ModelRemapping, requestedModel string) (string, string) {
	// 1. Check aliases
	if target, ok := remap.Aliases[requestedModel]; ok {
		// Look up the target model in known_models to find its provider
		for _, entry := range remap.KnownModels {
			if entry.ID == target {
				logModelRemap(requestedModel, target+" (via alias, provider: "+entry.Provider+")", "alias")
				return target, entry.Provider
			}
		}
		// Target not in known_models, fall back to default provider
		logModelRemap(requestedModel, target+" (via alias, provider: default)", "alias")
		return target, remap.DefaultProvider()
	}

	// 2. Check known_models (exact + prefix match)
	for _, entry := range remap.KnownModels {
		if requestedModel == entry.ID || strings.HasPrefix(requestedModel, entry.ID+":") || strings.HasPrefix(requestedModel, entry.ID+"[") {
			return requestedModel, entry.Provider
		}
	}

	// 3. Fall back to default_model → look up in known_models
	if remap.DefaultModel != "" {
		for _, entry := range remap.KnownModels {
			if entry.ID == remap.DefaultModel {
				logModelRemap(requestedModel, remap.DefaultModel+" (default, provider: "+entry.Provider+")", "default")
				return remap.DefaultModel, entry.Provider
			}
		}
		// Default model not in known_models, use default provider
		logModelRemap(requestedModel, remap.DefaultModel+" (default)", "default")
		return remap.DefaultModel, ""
	}

	// 4. Last resort: return original model with empty provider (will use DefaultProvider)
	return requestedModel, ""
}

// DefaultProvider returns the default provider ID from the config
func (m *ModelRemapping) DefaultProvider() string {
	// Return empty string so the caller falls back to cfg.DefaultProvider
	return ""
}

func generateProviderID(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = regexpReplace(slug, "[^a-z0-9_]+", "")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		slug = "provider"
	}
	return "custom_" + slug + "_" + randStr(6)
}

func randStr(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[r.Intn(len(charset))]
	}
	return string(b)
}

func regexpReplace(s, pattern, repl string) string {
	// Simple replacement without importing regexp
	result := ""
	for _, ch := range s {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			result += string(ch)
		}
	}
	return result
}

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func getModelRemappingPath() string {
	return filepath.Join(getConfigDir(), "model_remapping.json")
}

func defaultModelRemapping() *ModelRemapping {
	return &ModelRemapping{
		DefaultModel: "",
		KnownModels:  []ModelEntry{},
		Aliases:      map[string]string{},
	}
}

func loadModelRemapping() *ModelRemapping {
	remap := defaultModelRemapping()
	data, err := os.ReadFile(getModelRemappingPath())
	if err != nil {
		if os.IsNotExist(err) {
			saveModelRemapping(remap)
		}
		return remap
	}

	// Try to unmarshal into the new format (known_models as []ModelEntry)
	if err := json.Unmarshal(data, remap); err != nil {
		// Try migrating from old format (known_models as []string)
		var raw struct {
			DefaultModel string            `json:"default_model"`
			KnownModels  []string          `json:"known_models"`
			Aliases      map[string]string `json:"aliases"`
		}
		if json.Unmarshal(data, &raw) == nil {
			remap.DefaultModel = raw.DefaultModel
			remap.Aliases = raw.Aliases
			remap.KnownModels = nil
			for _, id := range raw.KnownModels {
				remap.KnownModels = append(remap.KnownModels, ModelEntry{
					ID:       id,
					Provider: "", // will be set to default below
				})
			}
		} else {
			return defaultModelRemapping()
		}
	}

	// Migration: if any ModelEntry has empty Provider, fill with the config's DefaultProvider
	cfg := loadConfig()
	for i := range remap.KnownModels {
		if remap.KnownModels[i].Provider == "" {
			remap.KnownModels[i].Provider = cfg.DefaultProvider
		}
	}

	if remap.KnownModels == nil {
		remap.KnownModels = []ModelEntry{}
	}
	if remap.Aliases == nil {
		remap.Aliases = map[string]string{}
	}

	// Save if we migrated
	if err != nil || needsMigration(data) {
		saveModelRemapping(remap)
	}

	return remap
}

// needsMigration checks if the data contains old-format known_models (strings instead of objects)
func needsMigration(data []byte) bool {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	if km, ok := raw["known_models"]; ok {
		// If the first element is a string, it's the old format
		var first string
		if err := json.Unmarshal(km, &first); err == nil {
			return true
		}
	}
	return false
}

func saveModelRemapping(remap *ModelRemapping) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(remap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getModelRemappingPath(), data, 0600)
}

// getDefaultAPIKey returns the API key for the default provider (used by tray for display)
func (c *Config) getDefaultAPIKey() string {
	info, err := c.getProviderByID(c.DefaultProvider)
	if err != nil {
		return ""
	}
	return info.APIKey
}

// getDefaultProvider returns the ProviderConfig for the default provider (used by tray for display)
func (c *Config) getDefaultProvider() *ProviderInfo {
	info, err := c.getProviderByID(c.DefaultProvider)
	if err != nil {
		return &ProviderInfo{Name: c.DefaultProvider}
	}
	return info
}

// resolveModelProvider resolves a model using the model remapping and config's default provider
func resolveModelProvider(cfg *Config, remap *ModelRemapping, requestedModel string) (string, string) {
	resolvedModel, providerID := resolveModel(remap, requestedModel)
	if providerID == "" {
		providerID = cfg.DefaultProvider
	}
	return resolvedModel, providerID
}

func logModelRemap(from, to, reason string) {
	log.Printf("[map] Model remap (%s): %s -> %s", reason, from, to)
}

func validateBaseURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must use http:// or https:// scheme")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must have a host")
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
			return fmt.Errorf("URL points to a private/local address which may be a security risk")
		}
	}
	return nil
}
