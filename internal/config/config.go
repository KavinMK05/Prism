// Package config owns the Prism configuration model: the config.json schema
// (Config, providers, OAuth accounts, search) and the model_remapping.json
// schema, plus the in-memory "current" config the tray/admin UI share.
package config

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

	"ollama-proxy/internal/platform"
	"ollama-proxy/internal/search"
)

// ChatGPTBase is the base URL for ChatGPT (Codex) endpoints.
const ChatGPTBase = "https://chatgpt.com"

// OAuthAccount stores a connected OAuth provider account
type OAuthAccount struct {
	ID               string  `json:"id"`
	Provider         string  `json:"provider"` // "codex"
	Label            string  `json:"label"`
	Email            string  `json:"email"`
	ChatGPTAccountID string  `json:"chatgpt_account_id,omitempty"`
	AccessToken      string  `json:"access_token"`
	RefreshToken     string  `json:"refresh_token"`
	IDToken          string  `json:"id_token,omitempty"`
	ExpiresAt        int64   `json:"expires_at"`
	PlanTier         string  `json:"plan_tier"`
	Active           bool    `json:"active"`
	CreditsUsed      float64 `json:"credits_used"`
	CreditsTotal     float64 `json:"credits_total"`
	CreditsRemaining float64 `json:"credits_remaining"`
	WeeklyResetAt    int64   `json:"weekly_reset_at"`
	LastUsageCheck   int64   `json:"last_usage_check"`
	SessionPercent   float64 `json:"session_percent,omitempty"`
	SessionResetAt   int64   `json:"session_reset_at,omitempty"`
	WeeklyPercent    float64 `json:"weekly_percent,omitempty"`
	ReviewPercent    float64 `json:"review_percent,omitempty"`
	CreditsBalance   float64 `json:"credits_balance,omitempty"`
	RateLimitResets  int     `json:"rate_limit_resets,omitempty"`
	PercentUsed      float64 `json:"percent_used,omitempty"`
}

// IsTokenExpired returns true if the access token has expired
func (a *OAuthAccount) IsTokenExpired() bool {
	if a.ExpiresAt == 0 {
		return true
	}
	return time.Now().Unix() > a.ExpiresAt-60 // 60s buffer
}

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
	// AutoSync stores the user's choice for each agent integration. Setup sets
	// an agent to true, while Disable/Restore sets it to false.
	AutoSync map[string]bool `json:"auto_sync,omitempty"`
	// AutoSyncMigrated marks that the one-time migration from the historical
	// always-on behavior has completed.
	AutoSyncMigrated bool `json:"auto_sync_migrated,omitempty"`
}

type Config struct {
	DefaultProvider   string                   `json:"default_provider"`
	OllamaCloud       *ProviderConfig          `json:"ollama_cloud"`
	OpenCodeGo        *ProviderConfig          `json:"opencode_go"`
	CustomProviders   []*ProviderConfig        `json:"custom_providers"`
	OAuthAccounts     []*OAuthAccount          `json:"oauth_accounts"`
	AgentIntegrations *AgentIntegrationsConfig `json:"agent_integrations,omitempty"`
	SearXNGAutoStart  bool                     `json:"searxng_autostart,omitempty"`
	DebugLogs         bool                     `json:"debug_logs,omitempty"`
	Search            *search.Config           `json:"search,omitempty"`

	// AnalyticsOptIn records whether the user has opted in to anonymous usage
	// telemetry (a single daily heartbeat to PostHog EU). Defaults to false.
	AnalyticsOptIn bool `json:"analytics_opt_in,omitempty"`
	// AnalyticsPrompted records whether the one-time first-run consent prompt
	// has been shown. Defaults to false; once true it is never shown again.
	AnalyticsPrompted bool `json:"analytics_prompted,omitempty"`
}

// EnsureAgentIntegrations initializes the agent integration section and its
// maps, returning the usable section for callers that need to mutate it.
func (c *Config) EnsureAgentIntegrations() *AgentIntegrationsConfig {
	if c.AgentIntegrations == nil {
		c.AgentIntegrations = &AgentIntegrationsConfig{}
	}
	if c.AgentIntegrations.ClaudeCodeTiers == nil {
		c.AgentIntegrations.ClaudeCodeTiers = map[string]string{}
	}
	if c.AgentIntegrations.AutoSync == nil {
		c.AgentIntegrations.AutoSync = map[string]bool{}
	}
	return c.AgentIntegrations
}

// Clone returns a deep copy of the Config, safe for mutation without affecting the original
func (c *Config) Clone() *Config {
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
	if c.Search != nil && c.Search.CustomProviders != nil {
		cp.Search = c.Search.Clone()
	}
	if c.AgentIntegrations != nil {
		ai := *c.AgentIntegrations
		if c.AgentIntegrations.ClaudeCodeTiers != nil {
			ai.ClaudeCodeTiers = make(map[string]string, len(c.AgentIntegrations.ClaudeCodeTiers))
			for k, v := range c.AgentIntegrations.ClaudeCodeTiers {
				ai.ClaudeCodeTiers[k] = v
			}
		}
		if c.AgentIntegrations.AutoSync != nil {
			ai.AutoSync = make(map[string]bool, len(c.AgentIntegrations.AutoSync))
			for k, v := range c.AgentIntegrations.AutoSync {
				ai.AutoSync[k] = v
			}
		}
		cp.AgentIntegrations = &ai
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

// ModelRouteKey returns the provider-qualified model identifier used by
// integrations that need to distinguish identical upstream model IDs.
// ModelEntry.ID remains the raw model ID sent to the upstream provider.
func ModelRouteKey(entry ModelEntry) string {
	if entry.Provider == "" {
		return entry.ID
	}
	return entry.Provider + "/" + entry.ID
}

type ModelRemapping struct {
	DefaultModel string            `json:"default_model"`
	KnownModels  []ModelEntry      `json:"known_models"`
	Aliases      map[string]string `json:"aliases"`
}

// RemoveModelsForProviders removes known models assigned to any of the given
// providers and aliases that point at those models. It also clears the
// default model when it points at a removed model.
func RemoveModelsForProviders(remap *ModelRemapping, providerIDs map[string]struct{}) bool {
	if remap == nil || len(providerIDs) == 0 {
		return false
	}

	removedModels := make(map[string]struct{})
	removedRoutes := make(map[string]struct{})
	keptModels := make([]ModelEntry, 0, len(remap.KnownModels))
	changed := false
	for _, model := range remap.KnownModels {
		if _, removed := providerIDs[model.Provider]; removed {
			removedModels[model.ID] = struct{}{}
			removedRoutes[ModelRouteKey(model)] = struct{}{}
			changed = true
			continue
		}
		keptModels = append(keptModels, model)
	}
	if !changed {
		return false
	}
	remap.KnownModels = keptModels

	availableModels := make(map[string]struct{}, len(keptModels))
	for _, model := range keptModels {
		availableModels[model.ID] = struct{}{}
	}
	if modelTargetMatchesRemoved(remap.DefaultModel, removedModels, removedRoutes, availableModels) {
		remap.DefaultModel = ""
	}
	for alias, target := range remap.Aliases {
		if modelTargetMatchesRemoved(target, removedModels, removedRoutes, availableModels) {
			delete(remap.Aliases, alias)
		}
	}
	return true
}

func modelTargetMatchesRemoved(target string, removedModels, removedRoutes, availableModels map[string]struct{}) bool {
	if _, removed := removedRoutes[target]; removed {
		return true
	}
	for route := range removedRoutes {
		if strings.HasPrefix(target, route+":") || strings.HasPrefix(target, route+"[") {
			return true
		}
	}
	if _, removed := removedModels[target]; removed {
		_, stillAvailable := availableModels[target]
		return !stillAvailable
	}
	for modelID := range removedModels {
		if _, stillAvailable := availableModels[modelID]; stillAvailable {
			continue
		}
		if strings.HasPrefix(target, modelID+":") || strings.HasPrefix(target, modelID+"[") {
			return true
		}
	}
	return false
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
	BaseURL          string
	APIKey           string
	ProviderType     string
	ProviderID       string // e.g. "ollama_cloud", "opencode_go", custom ID, or OAuth account ID
	ChatGPTAccountID string // for Codex OAuth: the chatgpt-account-id header value
}

// ChatCompletionsURL returns the full URL for /chat/completions, handling
// base URLs that already include /v1 (e.g. https://api.groq.com/openai/v1)
// and those that don't (e.g. https://api.openai.com).
func (rp *ResolvedProvider) ChatCompletionsURL() string {
	if strings.HasSuffix(rp.BaseURL, "/v1") || strings.Contains(rp.BaseURL, "/v1/") {
		return rp.BaseURL + "/chat/completions"
	}
	return rp.BaseURL + "/v1/chat/completions"
}

// ApiChatURL returns the full URL for Ollama's /api/chat endpoint.
func (rp *ResolvedProvider) ApiChatURL() string {
	return rp.BaseURL + "/api/chat"
}

// ResponsesURL returns the full URL for the Responses API endpoint.
// For Codex providers, the base URL already points to chatgpt.com/backend-api/codex,
// so we just append /responses. For other OpenAI-compatible providers, we use /v1/responses.
func (rp *ResolvedProvider) ResponsesURL() string {
	if rp.ProviderType == "codex" {
		return rp.BaseURL + "/responses"
	}
	if strings.HasSuffix(rp.BaseURL, "/v1") || strings.Contains(rp.BaseURL, "/v1/") {
		return rp.BaseURL + "/responses"
	}
	return rp.BaseURL + "/v1/responses"
}

func getConfigPath() string {
	return filepath.Join(platform.ConfigDir(), "config.json")
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

// Load reads config.json from the config dir, migrating legacy fields.
func Load() *Config {
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
			Save(cfg)
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
	cfg.EnsureAgentIntegrations()
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "ollama_cloud"
	}
	if cfg.Search == nil {
		cfg.Search = search.DefaultConfig()
	} else {
		cfg.Search = search.MergeConfig(cfg.Search)
	}
	return cfg
}

// Save writes config.json to the config dir.
func Save(cfg *Config) error {
	dir := platform.ConfigDir()
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
		Search: search.DefaultConfig(),
	}
}

// GetProviderByID returns provider info for any provider (built-in, custom, or OAuth)
func (c *Config) GetProviderByID(id string) (*ProviderInfo, error) {
	// Check OAuth accounts first (they may have their own provider ID)
	for _, a := range c.OAuthAccounts {
		if a.ID == id {
			return &ProviderInfo{
				BaseURL:      ChatGPTBase + "/backend-api/codex",
				APIKey:       a.AccessToken,
				ProviderType: "codex",
				Name:         a.Label + " (" + a.Email + ")",
			}, nil
		}
	}

	switch id {
	case "ollama_cloud":
		apiKey := ""
		baseURL := ""
		name := ""
		if c.OllamaCloud != nil {
			apiKey = c.OllamaCloud.APIKey
			baseURL = c.OllamaCloud.BaseURL
			name = c.OllamaCloud.Name
		}
		if apiKey == "" {
			apiKey = os.Getenv("OLLAMA_API_KEY")
		}
		return &ProviderInfo{
			BaseURL:      baseURL,
			APIKey:       apiKey,
			ProviderType: "ollama",
			Name:         name,
		}, nil
	case "opencode_go":
		apiKey := ""
		baseURL := ""
		name := ""
		if c.OpenCodeGo != nil {
			apiKey = c.OpenCodeGo.APIKey
			baseURL = c.OpenCodeGo.BaseURL
			name = c.OpenCodeGo.Name
		}
		if apiKey == "" {
			apiKey = os.Getenv("OPENCODE_GO_API_KEY")
		}
		return &ProviderInfo{
			BaseURL:      baseURL,
			APIKey:       apiKey,
			ProviderType: "openai",
			Name:         name,
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

// GetProviderName returns a human-readable name for a provider ID
func (c *Config) GetProviderName(id string) string {
	info, err := c.GetProviderByID(id)
	if err != nil {
		return id
	}
	return info.Name
}

// IsCodexProviderID returns true if the provider ID corresponds to a Codex OAuth account.
// Checks both exact matches against configured OAuth accounts and the "codex_" prefix
// (in case the account was removed/re-added with a new ID but the model still references the old one).
func (c *Config) IsCodexProviderID(providerID string) bool {
	for _, a := range c.OAuthAccounts {
		if a.ID == providerID {
			return true
		}
	}
	return strings.HasPrefix(providerID, "codex_")
}

// ResolveModel resolves a requested model name to (resolvedModel, providerID)
func ResolveModel(remap *ModelRemapping, requestedModel string) (string, string) {
	// 1. Check aliases
	if target, ok := remap.Aliases[requestedModel]; ok {
		// Prefer a provider-qualified target, then preserve the legacy
		// first-match behavior for bare targets.
		for _, entry := range remap.KnownModels {
			if entryMatchesTarget(entry, target) {
				logModelRemap(requestedModel, target+" (via alias, provider: "+entry.Provider+")", "alias")
				return routeResolvedModel(entry, target), entry.Provider
			}
		}
		// Target not in known_models, fall back to default provider
		logModelRemap(requestedModel, target+" (via alias, provider: default)", "alias")
		return target, remap.DefaultProvider()
	}

	// 2. Check provider-qualified known model keys first. This allows agents
	// to select a specific provider while keeping the upstream model ID raw.
	for _, entry := range remap.KnownModels {
		if entryMatchesRoute(entry, requestedModel) {
			return routeResolvedModel(entry, requestedModel), entry.Provider
		}
	}

	// 3. Check bare known_models (exact + prefix match). This intentionally
	// keeps the historical first-entry-wins behavior for direct clients.
	for _, entry := range remap.KnownModels {
		if requestedModel == entry.ID || strings.HasPrefix(requestedModel, entry.ID+":") || strings.HasPrefix(requestedModel, entry.ID+"[") {
			return requestedModel, entry.Provider
		}
	}

	// 4. Fall back to default_model → look up in known_models
	if remap.DefaultModel != "" {
		for _, entry := range remap.KnownModels {
			if entryMatchesTarget(entry, remap.DefaultModel) {
				logModelRemap(requestedModel, remap.DefaultModel+" (default, provider: "+entry.Provider+")", "default")
				return routeResolvedModel(entry, remap.DefaultModel), entry.Provider
			}
		}
		// Default model not in known_models, use default provider
		logModelRemap(requestedModel, remap.DefaultModel+" (default)", "default")
		return remap.DefaultModel, ""
	}

	// 5. Last resort: return original model with empty provider (will use DefaultProvider)
	return requestedModel, ""
}

// entryMatchesRoute matches an exact provider/model key or a qualified model
// variant such as provider/model:cloud or provider/model[variant].
func entryMatchesRoute(entry ModelEntry, requested string) bool {
	key := ModelRouteKey(entry)
	return requested == key ||
		strings.HasPrefix(requested, key+":") ||
		strings.HasPrefix(requested, key+"[")
}

func entryMatchesTarget(entry ModelEntry, requested string) bool {
	return entryMatchesRoute(entry, requested) || requested == entry.ID
}

// routeResolvedModel removes the provider prefix from a qualified request,
// preserving any model suffix supplied by the caller.
func routeResolvedModel(entry ModelEntry, requested string) string {
	key := ModelRouteKey(entry)
	if requested == key || requested == entry.ID {
		return entry.ID
	}
	return strings.TrimPrefix(requested, entry.Provider+"/")
}

// DefaultProvider returns the default provider ID from the config
func (m *ModelRemapping) DefaultProvider() string {
	// Return empty string so the caller falls back to cfg.DefaultProvider
	return ""
}

// GenerateProviderID creates a stable-ish unique ID for a custom provider.
func GenerateProviderID(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "_")
	slug = regexpReplace(slug, "[^a-z0-9_]+", "")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		slug = "provider"
	}
	return "custom_" + slug + "_" + RandStr(6)
}

// RandStr returns a random lowercase alphanumeric string of length n.
func RandStr(n int) string {
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

func ModelRemappingPath() string {
	return filepath.Join(platform.ConfigDir(), "model_remapping.json")
}

func DefaultModelRemapping() *ModelRemapping {
	return &ModelRemapping{
		DefaultModel: "",
		KnownModels:  []ModelEntry{},
		Aliases:      map[string]string{},
	}
}

// LoadModelRemapping reads model_remapping.json, migrating legacy formats.
func LoadModelRemapping() *ModelRemapping {
	remap := DefaultModelRemapping()
	data, err := os.ReadFile(ModelRemappingPath())
	if err != nil {
		if os.IsNotExist(err) {
			SaveModelRemapping(remap)
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
			return DefaultModelRemapping()
		}
	}

	// Migration: if any ModelEntry has empty Provider, fill with the config's DefaultProvider
	cfg := Load()
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
		SaveModelRemapping(remap)
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

// SaveModelRemapping writes model_remapping.json to the config dir.
func SaveModelRemapping(remap *ModelRemapping) error {
	dir := platform.ConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(remap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ModelRemappingPath(), data, 0600)
}

// GetDefaultAPIKey returns the API key for the default provider (used by tray for display)
func (c *Config) GetDefaultAPIKey() string {
	info, err := c.GetProviderByID(c.DefaultProvider)
	if err != nil {
		return ""
	}
	return info.APIKey
}

// GetDefaultProvider returns the ProviderConfig for the default provider (used by tray for display)
func (c *Config) GetDefaultProvider() *ProviderInfo {
	info, err := c.GetProviderByID(c.DefaultProvider)
	if err != nil {
		return &ProviderInfo{Name: c.DefaultProvider}
	}
	return info
}

// ResolveModelProvider resolves a model using the model remapping and config's default provider
func ResolveModelProvider(cfg *Config, remap *ModelRemapping, requestedModel string) (string, string) {
	resolvedModel, providerID := ResolveModel(remap, requestedModel)
	if providerID == "" {
		providerID = cfg.DefaultProvider
	}
	return resolvedModel, providerID
}

func logModelRemap(from, to, reason string) {
	log.Printf("[map] Model remap (%s): %s -> %s", reason, from, to)
}

// ValidateBaseURL checks that a provider base URL is http(s) and not pointing
// at a private/local address (which may be a security risk).
func ValidateBaseURL(rawURL string) error {
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
