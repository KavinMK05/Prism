package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed admin.html
//go:embed icon.png
//go:embed all:web/dist
var adminFS embed.FS

var (
	adminMu     sync.Mutex
	adminConfig *Config
)

// modelsDevMatchProviders fuzzy-matches a Prism provider ID against models.dev provider keys.
// Returns matching models.dev provider keys, or nil to search all.
func modelsDevMatchProviders(prismProviderID string, allProviderKeys []string) []string {
	if prismProviderID == "" {
		return nil
	}
	// Normalize: lowercase, replace _ with - for comparison
	searchNorm := strings.ToLower(strings.ReplaceAll(prismProviderID, "_", "-"))
	// First pass: exact match after normalization
	for _, pk := range allProviderKeys {
		pkNorm := strings.ToLower(strings.ReplaceAll(pk, "_", "-"))
		if pkNorm == searchNorm {
			return []string{pk}
		}
	}
	// Second pass: provider key contains the search term or vice versa
	var matches []string
	for _, pk := range allProviderKeys {
		pkNorm := strings.ToLower(strings.ReplaceAll(pk, "_", "-"))
		if strings.Contains(pkNorm, searchNorm) || strings.Contains(searchNorm, pkNorm) {
			matches = append(matches, pk)
		}
	}
	if len(matches) > 0 {
		return matches
	}
	// Third pass: strip common suffixes and retry
	base := searchNorm
	base = strings.TrimSuffix(base, "-cloud")
	base = strings.TrimSuffix(base, "-go")
	if base != searchNorm && base != "" {
		for _, pk := range allProviderKeys {
			pkNorm := strings.ToLower(strings.ReplaceAll(pk, "_", "-"))
			if strings.Contains(pkNorm, base) || strings.Contains(base, pkNorm) {
				matches = append(matches, pk)
			}
		}
	}
	if len(matches) > 0 {
		return matches
	}
	// No match found - search all providers
	return nil
}

// modelsDevResult holds a parsed models.dev model entry.
type modelsDevResult struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ContextLength    int      `json:"context_length"`
	MaxOutputTokens  int      `json:"max_output_tokens"`
	Reasoning        bool     `json:"reasoning"`
	ToolCall         bool     `json:"tool_calling"`
	StructuredOutput bool     `json:"structured_output"`
	Vision           bool     `json:"vision"`
	ReasoningEffort  []string `json:"reasoning_effort,omitempty"`
	ProviderID       string   `json:"provider_id"`
}

// modelsDevClient is the HTTP client used for models.dev requests. The
// provider index is ~3 MB, so a timeout guards against a stalled download
// hanging the admin UI's "Fetch" indefinitely.
var modelsDevClient = &http.Client{Timeout: 30 * time.Second}

// fetchModelsDevAPI downloads and parses the models.dev provider index into a
// raw map of provider-key -> provider JSON.
func fetchModelsDevAPI() (map[string]json.RawMessage, error) {
	resp, err := modelsDevClient.Get("https://models.dev/api.json")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models.dev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models.dev returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read models.dev response")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse models.dev response")
	}
	return raw, nil
}

// fetchModelsDevModel fetches a model's metadata from models.dev. When
// prismProvider is set (a Prism provider id like "ollama_cloud"), the result
// is scoped to that provider on models.dev so the returned limits/capabilities
// reflect the provider the model is actually routed through; if that provider
// does not list the model, the search falls back to all providers.
func fetchModelsDevModel(modelID, prismProvider string) (*modelsDevResult, error) {
	raw, err := fetchModelsDevAPI()
	if err != nil {
		return nil, err
	}
	return matchModelsDevModel(raw, modelID, prismProvider), nil
}

// matchModelsDevModel is the pure, I/O-free matcher over a parsed models.dev
// index. Split out so it can be unit-tested with fixtures.
//
// Two bugs existed in the prior single-match implementation:
//  1. Selection: sort.Slice put exact matches first but the code took
//     matches[len-1] — the LAST/worst (partial) match. So searching "glm-5.2"
//     under Ollama Cloud returned "glm-5" (202752) instead of the exact
//     "glm-5.2" (976000), because "glm-5.2" contains the substring "glm-5".
//  2. Greedy reverse-substring: strings.Contains(searchID, mID) let a shorter
//     model id ("glm-5") match a longer search ("glm-5.2").
//
// This picks the best match directly (exact > native > deterministic provider
// id) and only accepts a reverse-substring when the search id is a namespaced
// form ending in "/"+mID (e.g. "z-ai/glm-5.1" -> "glm-5.1").
func matchModelsDevModel(raw map[string]json.RawMessage, modelID, prismProvider string) *modelsDevResult {
	type modelInfo struct {
		Name  string `json:"name"`
		ID    string `json:"id"`
		Limit struct {
			Context int `json:"context"`
			Output  int `json:"output"`
		} `json:"limit"`
		Reasoning        bool `json:"reasoning"`
		ToolCall         bool `json:"tool_call"`
		StructuredOutput bool `json:"structured_output"`
		Modalities       *struct {
			Input  []string `json:"input"`
			Output []string `json:"output"`
		} `json:"modalities"`
	}
	type providerInfo struct {
		ID     string               `json:"id"`
		Name   string               `json:"name"`
		Models map[string]modelInfo `json:"models"`
	}

	allProviderKeys := make([]string, 0, len(raw))
	for k := range raw {
		allProviderKeys = append(allProviderKeys, k)
	}
	scopedKeys := modelsDevMatchProviders(prismProvider, allProviderKeys)

	// Strip a Prism routing suffix like ":cloud" or ":free".
	searchBase := modelID
	if idx := strings.Index(modelID, ":"); idx > 0 {
		searchBase = modelID[:idx]
	}
	searchID := strings.ToLower(searchBase)

	type cand struct {
		m       modelInfo
		provID  string
		provKey string
		exact   bool
		native  bool
	}
	inScope := func(provID, provKey string) bool {
		if len(scopedKeys) == 0 {
			return false
		}
		for _, pk := range scopedKeys {
			if strings.EqualFold(provID, pk) || strings.EqualFold(provKey, pk) {
				return true
			}
		}
		return false
	}

	var scoped, all []cand
	for provKey, provRaw := range raw {
		var prov providerInfo
		if json.Unmarshal(provRaw, &prov) != nil {
			continue
		}
		provIDLower := strings.ToLower(prov.ID)
		provKeyLower := strings.ToLower(provKey)
		isScoped := inScope(prov.ID, provKey)
		for _, m := range prov.Models {
			mID := strings.ToLower(m.ID)
			mName := strings.ToLower(m.Name)
			var exact, partial bool
			switch {
			case mID == searchID:
				exact = true
			case strings.Contains(mID, searchID),
				strings.Contains(mName, searchID),
				strings.HasSuffix(searchID, "/"+mID):
				partial = true
			}
			if !exact && !partial {
				continue
			}
			c := cand{
				m:       m,
				provID:  prov.ID,
				provKey: provKey,
				exact:   exact,
				native:  strings.HasPrefix(searchID, provIDLower) || strings.HasPrefix(searchID, provKeyLower),
			}
			all = append(all, c)
			if isScoped {
				scoped = append(scoped, c)
			}
		}
	}
	// Prefer matches from the selected provider; fall back to all providers
	// only when the selected provider does not list the model.
	pool := scoped
	if len(pool) == 0 {
		pool = all
	}
	if len(pool) == 0 {
		return nil
	}
	// Pick the BEST match (not the worst): exact > native > deterministic
	// (alphabetical provider id) so repeated lookups are stable regardless of
	// Go's randomized map iteration.
	best := pool[0]
	for _, c := range pool[1:] {
		switch {
		case c.exact != best.exact:
			if c.exact {
				best = c
			}
		case c.native != best.native:
			if c.native {
				best = c
			}
		case c.provID < best.provID:
			best = c
		}
	}
	vision := false
	if best.m.Modalities != nil {
		for _, mod := range best.m.Modalities.Input {
			if mod == "image" {
				vision = true
			}
		}
	}
	result := &modelsDevResult{
		ID:               best.m.ID,
		Name:             best.m.Name,
		ProviderID:       best.provID,
		ContextLength:    best.m.Limit.Context,
		MaxOutputTokens:  best.m.Limit.Output,
		Reasoning:        best.m.Reasoning,
		ToolCall:         best.m.ToolCall,
		StructuredOutput: best.m.StructuredOutput,
		Vision:           vision,
	}
	if best.m.Reasoning {
		efforts := []string{"low", "medium", "high"}
		if strings.Contains(searchID, "deepseek-v4") {
			efforts = append(efforts, "max")
		}
		result.ReasoningEffort = efforts
	}
	return result
}

// writeModelsDevResult writes a modelsDevResult (or {"found":false}) as the
// JSON shape the admin UI expects. Shared by the admin and proxy endpoints.
func writeModelsDevResult(w http.ResponseWriter, result *modelsDevResult) {
	w.Header().Set("Content-Type", "application/json")
	if result == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found":              true,
		"id":                 result.ID,
		"name":               result.Name,
		"provider_id":        result.ProviderID,
		"context_length":     result.ContextLength,
		"max_output_tokens":  result.MaxOutputTokens,
		"reasoning":          result.Reasoning,
		"tool_calling":       result.ToolCall,
		"structured_outputs": result.StructuredOutput,
		"vision":             result.Vision,
		"reasoning_effort":   result.ReasoningEffort,
	})
}

// fetchModelsDevSearch returns matching model IDs from a specific models.dev provider.
func fetchModelsDevSearch(query, prismProvider string) ([]map[string]string, error) {
	raw, err := fetchModelsDevAPI()
	if err != nil {
		return nil, err
	}
	type modelInfo struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	type providerInfo struct {
		ID     string               `json:"id"`
		Name   string               `json:"name"`
		Models map[string]modelInfo `json:"models"`
	}
	// Fuzzy-match Prism provider to models.dev provider keys
	allProviderKeys := make([]string, 0, len(raw))
	for k := range raw {
		allProviderKeys = append(allProviderKeys, k)
	}
	providerKeys := modelsDevMatchProviders(prismProvider, allProviderKeys)
	q := strings.ToLower(query)
	var results []map[string]string
	for provKey, provRaw := range raw {
		var prov providerInfo
		if json.Unmarshal(provRaw, &prov) != nil {
			continue
		}
		if len(providerKeys) > 0 {
			found := false
			for _, pk := range providerKeys {
				if strings.EqualFold(prov.ID, pk) || strings.EqualFold(provKey, pk) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		for _, m := range prov.Models {
			mID := strings.ToLower(m.ID)
			mName := strings.ToLower(m.Name)
			if q == "" || strings.Contains(mID, q) || strings.Contains(mName, q) {
				results = append(results, map[string]string{
					"id":   m.ID,
					"name": m.Name,
				})
			}
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i]["id"] < results[j]["id"]
	})
	if len(results) > 50 {
		results = results[:50]
	}
	return results, nil
}

func startAdminServer(cfg *Config, port string) {
	adminMu.Lock()
	adminConfig = cfg
	adminMu.Unlock()

	// Init persistent stats DB (shared with proxy process via WAL)
	if err := initDB(); err != nil {
		log.Printf("[DB] admin server failed to init: %v", err)
	}

	mux := http.NewServeMux()

	// Serve the React admin UI (Vite build output in web/dist)
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html, err := adminFS.ReadFile("web/dist/index.html")
		if err != nil {
			w.Write([]byte("<!DOCTYPE html><html><body>Frontend not built. Run: cd web && npm install && npm run build</body></html>"))
			return
		}
		w.Write(html)
	})
	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		// Serve static assets from Vite build (e.g. /admin/assets/index-abc.js)
		path := strings.TrimPrefix(r.URL.Path, "/admin/")
		if path == "" {
			// /admin/ - serve index.html
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			html, err := adminFS.ReadFile("web/dist/index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Write(html)
			return
		}
		data, err := adminFS.ReadFile("web/dist/" + path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(path, ".js"):
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case strings.HasSuffix(path, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(path, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		case strings.HasSuffix(path, ".png"):
			w.Header().Set("Content-Type", "image/png")
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Write(data)
	})

	// Serve the legacy single-page admin UI (plain HTML, pre-React migration)
	mux.HandleFunc("/admin-legacy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html, _ := adminFS.ReadFile("admin.html")
		w.Write(html)
	})

	// Serve the brand icon
	mux.HandleFunc("/admin/icon.png", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		icon, err := adminFS.ReadFile("icon.png")
		if err != nil {
			http.Error(w, "icon not found", http.StatusNotFound)
			return
		}
		w.Write(icon)
	})

	// API: Get config
	mux.HandleFunc("/admin/config", func(w http.ResponseWriter, r *http.Request) {
		adminMu.Lock()
		cfg := adminConfig
		adminMu.Unlock()

		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cfg)
		case http.MethodPut:
			var newCfg Config
			if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), 400)
				return
			}
			// Validate
			if newCfg.DefaultProvider == "" {
				newCfg.DefaultProvider = "ollama_cloud"
			}
			if newCfg.OllamaCloud == nil {
				newCfg.OllamaCloud = &ProviderConfig{ID: "ollama_cloud", Name: "Ollama Cloud", BaseURL: "https://ollama.com"}
			}
			if newCfg.OpenCodeGo == nil {
				newCfg.OpenCodeGo = &ProviderConfig{ID: "opencode_go", Name: "OpenCode Go", BaseURL: "https://opencode.ai/zen/go"}
			}
			if newCfg.CustomProviders == nil {
				newCfg.CustomProviders = []*ProviderConfig{}
			}
			// Ensure built-in IDs
			newCfg.OllamaCloud.ID = "ollama_cloud"
			newCfg.OpenCodeGo.ID = "opencode_go"
			// Ensure custom providers have IDs
			for _, p := range newCfg.CustomProviders {
				if p.ID == "" {
					p.ID = generateProviderID(p.Name)
				}
			}
			// Keep OAuth account Active flags in sync with DefaultProvider
			for _, a := range newCfg.OAuthAccounts {
				a.Active = (a.ID == newCfg.DefaultProvider)
			}
			// Validate custom provider URL if active
			if newCfg.DefaultProvider != "ollama_cloud" && newCfg.DefaultProvider != "opencode_go" {
				for _, p := range newCfg.CustomProviders {
					if p.ID == newCfg.DefaultProvider && p.BaseURL != "" {
						if err := validateBaseURL(p.BaseURL); err != nil {
							http.Error(w, "invalid custom base URL: "+err.Error(), 400)
							return
						}
					}
				}
			}

			adminMu.Lock()
			// Preserve API keys if not provided (empty string = don't overwrite)
			if newCfg.OllamaCloud.APIKey == "" && adminConfig.OllamaCloud.APIKey != "" {
				newCfg.OllamaCloud.APIKey = adminConfig.OllamaCloud.APIKey
			}
			if newCfg.OpenCodeGo.APIKey == "" && adminConfig.OpenCodeGo.APIKey != "" {
				newCfg.OpenCodeGo.APIKey = adminConfig.OpenCodeGo.APIKey
			}
			// Preserve API keys for custom providers that already exist
			for i, newP := range newCfg.CustomProviders {
				if newP.APIKey == "" {
					for _, oldP := range adminConfig.CustomProviders {
						if oldP != nil && newP.ID == oldP.ID && oldP.APIKey != "" {
							newCfg.CustomProviders[i].APIKey = oldP.APIKey
							break
						}
					}
				}
			}
			adminConfig = &newCfg
			adminMu.Unlock()

			if err := saveConfig(&newCfg); err != nil {
				http.Error(w, "save failed: "+err.Error(), 500)
				return
			}

			// Notify tray to reload
			notifyTrayConfigChanged()

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	// API: Model remapping
	mux.HandleFunc("/admin/model-remap", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			remap := loadModelRemapping()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(remap)
		case http.MethodPut:
			var remap ModelRemapping
			if err := json.NewDecoder(r.Body).Decode(&remap); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), 400)
				return
			}
			if remap.DefaultModel == "" {
				remap.DefaultModel = "glm-5.1:cloud"
			}
			if remap.KnownModels == nil {
				remap.KnownModels = []ModelEntry{}
			}
			if remap.Aliases == nil {
				remap.Aliases = map[string]string{}
			}
			// Ensure all model entries have a provider
			cfg := loadConfig()
			for i := range remap.KnownModels {
				if remap.KnownModels[i].Provider == "" {
					remap.KnownModels[i].Provider = cfg.DefaultProvider
				}
			}
			if err := saveModelRemapping(&remap); err != nil {
				http.Error(w, "save failed: "+err.Error(), 500)
				return
			}
			// Reload into running proxy
			reloadProxyModelRemap()
			// Sync agent configs so newly added models appear in Claude Code,
			// Factory Droid, and OpenCode
			SyncAgents(proxyPortFromEnv())
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	// API: Proxy status
	mux.HandleFunc("/admin/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"running": isProxyRunning(),
		})
	})

	// API: Proxy control
	mux.HandleFunc("/admin/proxy/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		startProxyProcess()
		time.Sleep(500 * time.Millisecond)
		updateMenu(isProxyRunning())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/admin/proxy/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		stopProxyProcess()
		time.Sleep(500 * time.Millisecond)
		updateMenu(isProxyRunning())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/admin/proxy/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		stopProxyProcess()
		time.Sleep(500 * time.Millisecond)
		startProxyProcess()
		time.Sleep(500 * time.Millisecond)
		updateMenu(isProxyRunning())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// API: SearXNG managed instance — status / lifecycle / settings / autostart
	mux.HandleFunc("/admin/searxng/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(searxngStatus())
	})

	mux.HandleFunc("/admin/searxng/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		go func() { _ = startSearxngProcess() }()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/admin/searxng/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		stopSearxngProcess()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/admin/searxng/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		go func() { _ = restartSearxngProcess() }()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/admin/searxng/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			f, err := loadSearxngSettingsForm()
			if err != nil {
				writeJSONError(w, "SearXNG not installed: "+err.Error(), 404)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(f)
		case http.MethodPut:
			var f searxngSettingsForm
			if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
				writeJSONError(w, err.Error(), 400)
				return
			}
			if err := validateSearxngSettingsForm(&f); err != nil {
				writeJSONError(w, err.Error(), 400)
				return
			}
			if err := saveSearxngSettingsForm(&f); err != nil {
				writeJSONError(w, "failed to save settings: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	mux.HandleFunc("/admin/searxng/autostart", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]bool{"enabled": searxngAutostartEnabled()})
		case http.MethodPut:
			var body struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONError(w, err.Error(), 400)
				return
			}
			c := loadConfig()
			c.SearXNGAutoStart = body.Enabled
			if err := saveConfig(c); err != nil {
				writeJSONError(w, "failed to save config: "+err.Error(), 500)
				return
			}
			// Keep the in-memory adminConfig in sync. Other handlers (OAuth
			// add/remove, background usage refresh) snapshot adminConfig and call
			// saveConfig, which would otherwise overwrite config.json with a stale
			// SearXNGAutoStart=false and - because the JSON tag is omitempty - drop
			// the field entirely, silently reverting the user's toggle.
			adminMu.Lock()
			if adminConfig != nil {
				adminConfig.SearXNGAutoStart = body.Enabled
			}
			adminMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	// API: Codex Desktop integration
	mux.HandleFunc("/admin/codex-desktop/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"installed": isCodexDesktopInstalled(),
			"active":    isCodexDesktopActive(),
		})
	})

	mux.HandleFunc("/admin/codex-desktop/setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if !isCodexDesktopInstalled() {
			writeJSONError(w, "Codex Desktop is not installed (~/.codex/config.toml not found)", 404)
			return
		}
		remap := loadModelRemapping()
		if err := writeCodexCatalog(remap); err != nil {
			writeJSONError(w, "failed to write catalog: "+err.Error(), 500)
			return
		}
		proxyPort := os.Getenv("PRISM_PORT")
		if proxyPort == "" {
			proxyPort = "11434"
		}
		if err := installCodexConfig(parseIntOr(proxyPort, 11434)); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/admin/codex-desktop/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if err := restoreCodexConfig(); err != nil {
			writeJSONError(w, "failed to restore config: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// API: Agent integrations (Claude Code, Factory Droid, OpenCode)
	// Generic handlers dispatch by ?id=. Setup/Restore return 501 in Phase 1
	// until each agent's writer lands in its own phase.
	mux.HandleFunc("/admin/agent/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		id := r.URL.Query().Get("id")
		if !isSupportedAgent(id) {
			writeJSONError(w, "unknown agent", 400)
			return
		}
		resp := map[string]interface{}{
			"id":          id,
			"displayName": agentDisplayName(id),
			"installed":   agentInstalled(id),
			"active":      isAgentActive(id),
		}
		// Expose persisted Claude Code tier mappings so the UI can pre-select.
		if id == "claude-code" {
			tiers := map[string]string{}
			cfg := loadConfig()
			if cfg.AgentIntegrations != nil && cfg.AgentIntegrations.ClaudeCodeTiers != nil {
				tiers = cfg.AgentIntegrations.ClaudeCodeTiers
			}
			resp["tiers"] = tiers
			// Provide known model IDs so the UI can populate tier dropdowns.
			remap := loadModelRemapping()
			modelOpts := make([]string, 0, len(remap.KnownModels))
			for _, m := range remap.KnownModels {
				modelOpts = append(modelOpts, m.ID)
			}
			if remap.DefaultModel != "" {
				modelOpts = append(modelOpts, remap.DefaultModel)
			}
			resp["model_options"] = modelOpts
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/admin/agent/setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		id := r.URL.Query().Get("id")
		if !isSupportedAgent(id) {
			writeJSONError(w, "unknown agent", 400)
			return
		}
		if !agentInstalled(id) {
			writeJSONError(w, agentDisplayName(id)+" is not installed", 404)
			return
		}
		switch id {
		case "claude-code":
			// Optional body: { "tiers": { "opus": "...", "sonnet": "...", "haiku": "...", "subagent": "..." } }
			var body struct {
				Tiers map[string]string `json:"tiers"`
			}
			if r.ContentLength != 0 {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					writeJSONError(w, "invalid JSON: "+err.Error(), 400)
					return
				}
			}
			cfg := loadConfig()
			if cfg.AgentIntegrations == nil {
				cfg.AgentIntegrations = &AgentIntegrationsConfig{ClaudeCodeTiers: map[string]string{}}
			}
			if cfg.AgentIntegrations.ClaudeCodeTiers == nil {
				cfg.AgentIntegrations.ClaudeCodeTiers = map[string]string{}
			}
			if len(body.Tiers) > 0 {
				for k, v := range body.Tiers {
					cfg.AgentIntegrations.ClaudeCodeTiers[k] = v
				}
				if err := saveConfig(cfg); err != nil {
					writeJSONError(w, "failed to save config: "+err.Error(), 500)
					return
				}
			}
			// Keep the in-memory adminConfig in sync. Background handlers (OAuth
			// token exchange, background usage refresh) and the Provider panel's
			// /config round-trip snapshot adminConfig and call saveConfig, which
			// would otherwise overwrite config.json with a stale agent_integrations
			// and - because the JSON tag is omitempty - drop the tier mappings
			// entirely, silently reverting the user's setup. See the analogous
			// SearXNGAutoStart fix in /admin/searxng/autostart.
			adminMu.Lock()
			if adminConfig != nil {
				adminConfig.AgentIntegrations = cfg.AgentIntegrations
			}
			adminMu.Unlock()
			if err := installClaudeCodeConfig(proxyPortFromEnv(), cfg.AgentIntegrations.ClaudeCodeTiers); err != nil {
				writeJSONError(w, "failed to install config: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"tiers":  cfg.AgentIntegrations.ClaudeCodeTiers,
			})
		case "factory-droid":
			remap := loadModelRemapping()
			if len(remap.KnownModels) == 0 {
				writeJSONError(w, "no Prism models configured", 400)
				return
			}
			if err := installFactoryDroidConfig(proxyPortFromEnv(), remap); err != nil {
				writeJSONError(w, "failed to install config: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"models": len(remap.KnownModels),
			})
		case "opencode":
			remap := loadModelRemapping()
			if len(remap.KnownModels) == 0 {
				writeJSONError(w, "no Prism models configured", 400)
				return
			}
			if err := installOpencodeConfig(proxyPortFromEnv(), remap); err != nil {
				writeJSONError(w, "failed to install config: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"models": len(remap.KnownModels),
			})
		case "zcode":
			remap := loadModelRemapping()
			if len(remap.KnownModels) == 0 {
				writeJSONError(w, "no Prism models configured", 400)
				return
			}
			if err := installZcodeConfig(proxyPortFromEnv(), remap); err != nil {
				writeJSONError(w, "failed to install config: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"models": len(remap.KnownModels),
			})
		case "omp":
			remap := loadModelRemapping()
			if len(remap.KnownModels) == 0 {
				writeJSONError(w, "no Prism models configured", 400)
				return
			}
			if err := installOmpConfig(proxyPortFromEnv(), remap); err != nil {
				writeJSONError(w, "failed to install config: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"models": len(remap.KnownModels),
			})
		case "grok-build":
			remap := loadModelRemapping()
			if len(remap.KnownModels) == 0 {
				writeJSONError(w, "no Prism models configured", 400)
				return
			}
			if err := installGrokBuildConfig(proxyPortFromEnv(), remap); err != nil {
				writeJSONError(w, "failed to install config: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"models": len(remap.KnownModels),
			})
		case "pi":
			remap := loadModelRemapping()
			if len(remap.KnownModels) == 0 {
				writeJSONError(w, "no Prism models configured", 400)
				return
			}
			if err := installPiConfig(proxyPortFromEnv(), remap); err != nil {
				writeJSONError(w, "failed to install config: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"models": len(remap.KnownModels),
			})
		default:
			// Phase 1 scaffold: per-agent setup for factory-droid/opencode lands in Phases 3-4.
			writeJSONError(w, agentDisplayName(id)+" setup is not yet implemented", 501)
		}
	})

	mux.HandleFunc("/admin/agent/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		id := r.URL.Query().Get("id")
		if !isSupportedAgent(id) {
			writeJSONError(w, "unknown agent", 400)
			return
		}
		switch id {
		case "claude-code":
			if err := restoreClaudeCodeConfig(); err != nil {
				writeJSONError(w, "failed to restore: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "factory-droid":
			if err := restoreFactoryDroidConfig(); err != nil {
				writeJSONError(w, "failed to restore: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "opencode":
			if err := restoreOpencodeConfig(); err != nil {
				writeJSONError(w, "failed to restore: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "zcode":
			if err := restoreZcodeConfig(); err != nil {
				writeJSONError(w, "failed to restore: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "omp":
			if err := restoreOmpConfig(); err != nil {
				writeJSONError(w, "failed to restore: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "grok-build":
			if err := restoreGrokBuildConfig(); err != nil {
				writeJSONError(w, "failed to restore: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "pi":
			if err := restorePiConfig(); err != nil {
				writeJSONError(w, "failed to restore: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			// Phase 1 scaffold: per-agent restore for factory-droid/opencode lands in Phases 3-4.
			writeJSONError(w, agentDisplayName(id)+" restore is not yet implemented", 501)
		}
	})

	// API: Logs
	mux.HandleFunc("/admin/autostart", handleAutoStart)

	// OAuth API endpoints
	mux.HandleFunc("/admin/oauth/login", handleOAuthLogin)
	mux.HandleFunc("/admin/oauth/accounts", handleOAuthAccounts)
	mux.HandleFunc("/admin/oauth/accounts/remove", handleOAuthAccountRemove)
	mux.HandleFunc("/admin/oauth/accounts/activate", handleOAuthAccountActivate)
	mux.HandleFunc("/admin/oauth/usage", handleOAuthUsage)
	mux.HandleFunc("/admin/oauth/usage/refresh", handleOAuthUsageRefresh)

	mux.HandleFunc("/admin/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		logPath := getLogFilePath()
		data, err := os.ReadFile(logPath)
		content := ""
		if err == nil {
			lines := strings.Split(string(data), "\n")
			start := 0
			if len(lines) > 200 {
				start = len(lines) - 200
			}
			content = strings.Join(lines[start:], "\n")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"logs": content})
	})

	// API: Live stats - proxy to the running proxy server's /v1/stats endpoint
	mux.HandleFunc("/admin/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		// Forward to the proxy server which has the actual stats
		proxyPort := os.Getenv("PRISM_PORT")
		if proxyPort == "" {
			proxyPort = "11434"
		}
		resp, err := http.Get("http://127.0.0.1:" + proxyPort + "/v1/stats")
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"current_model":        "",
				"current_provider":     "",
				"current_client":       "",
				"request_active":       false,
				"live_tokens_received": 0,
				"live_tokens_per_sec":  0,
				"total_requests":       0,
				"total_input_tokens":   0,
				"total_output_tokens":  0,
				"avg_tokens_per_sec":   0,
				"recent_requests":      []interface{}{},
				"by_model":             map[string]interface{}{},
				"by_client":            map[string]interface{}{},
			})
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body)
	})

	// API: Historical stats - reads directly from SQLite so it works when proxy is off
	mux.HandleFunc("/admin/stats/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}

		now := time.Now()
		fromStr := r.URL.Query().Get("from")
		toStr := r.URL.Query().Get("to")
		provider := r.URL.Query().Get("provider")
		model := r.URL.Query().Get("model")
		client := r.URL.Query().Get("client")

		var fromTime, toTime time.Time
		if fromStr != "" {
			fromTime, _ = time.ParseInLocation("2006-01-02", fromStr, time.Local)
		}
		if toStr != "" {
			toTime, _ = time.ParseInLocation("2006-01-02", toStr, time.Local)
		}
		if fromTime.IsZero() {
			fromTime = now.AddDate(0, 0, -7)
		}
		if toTime.IsZero() {
			toTime = now
		}
		fromUnix := fromTime.Unix()
		toUnix := toTime.Add(24 * time.Hour).Unix()

		daily, _ := getDailyTokens(fromUnix, toUnix, provider, model, client)
		monthly, _ := getMonthlyTokens(client)
		tpsHist, _ := getTPSHistory(fromUnix, toUnix, provider, model, client)
		byModel, _ := getModelHistory(fromUnix, toUnix, provider, model, client)
		byClient, _ := getClientHistory(fromUnix, toUnix, provider, model, client)

		// Heatmap always shows the last 365 days, filtered by provider/model/client
		heatmapTo := now.Add(24 * time.Hour).Unix()
		heatmapFrom := now.AddDate(0, 0, -365).Unix()
		heatmap, _ := getDailyTokens(heatmapFrom, heatmapTo, provider, model, client)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"daily_tokens":   daily,
			"monthly_tokens": monthly,
			"tps_history":    tpsHist,
			"by_model":       byModel,
			"by_client":      byClient,
			"heatmap_tokens": heatmap,
		})
	})

	// API: Clear all persistent stats
	mux.HandleFunc("/admin/stats/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if err := clearAllStats(); err != nil {
			writeJSONError(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// API: Get distinct models and providers for filters
	mux.HandleFunc("/admin/stats/filters", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		models, _ := getDistinctModels()
		dbProviders, _ := getDistinctProviders()
		clients, _ := getDistinctClients()

		// Merge configured providers with ones from the DB
		cfg := loadConfig()
		providerSet := map[string]bool{}
		for _, p := range dbProviders {
			providerSet[p] = true
		}
		// Add all configured providers
		configProviders := []string{"ollama_cloud", "opencode_go"}
		for _, p := range cfg.CustomProviders {
			configProviders = append(configProviders, p.ID)
		}
		for _, a := range cfg.OAuthAccounts {
			configProviders = append(configProviders, a.ID)
		}
		for _, p := range configProviders {
			if !providerSet[p] {
				dbProviders = append(dbProviders, p)
				providerSet[p] = true
			}
		}

		// Build provider id -> display name map
		providerNames := map[string]string{
			"ollama_cloud": "Ollama Cloud",
			"opencode_go":  "OpenCode Go",
		}
		for _, p := range cfg.CustomProviders {
			if p.Name != "" {
				providerNames[p.ID] = p.Name
			}
		}
		for _, a := range cfg.OAuthAccounts {
			name := a.Label
			if name == "" {
				name = a.Email
			}
			if name == "" {
				name = a.ID
			}
			providerNames[a.ID] = name
		}
		// Convert to sorted list of {id, name} objects
		type providerOption struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		sortedProviders := make([]providerOption, 0, len(dbProviders))
		for _, id := range dbProviders {
			name, ok := providerNames[id]
			if !ok {
				name = id
			}
			sortedProviders = append(sortedProviders, providerOption{ID: id, Name: name})
		}
		sort.Slice(sortedProviders, func(i, j int) bool {
			return sortedProviders[i].Name < sortedProviders[j].Name
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models":    models,
			"providers": sortedProviders,
			"clients":   clients,
		})
	})

	// API: Model info from models.dev (fetched directly to work when proxy is off)
	mux.HandleFunc("/admin/model-info", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		modelID := r.URL.Query().Get("id")
		if modelID == "" {
			writeJSONError(w, "missing id parameter", 400)
			return
		}
		prismProvider := r.URL.Query().Get("provider")
		result, err := fetchModelsDevModel(modelID, prismProvider)
		if err != nil {
			writeJSONError(w, err.Error(), 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if result == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"found":              true,
			"id":                 result.ID,
			"name":               result.Name,
			"provider_id":        result.ProviderID,
			"context_length":     result.ContextLength,
			"max_output_tokens":  result.MaxOutputTokens,
			"reasoning":          result.Reasoning,
			"tool_calling":       result.ToolCall,
			"structured_outputs": result.StructuredOutput,
			"vision":             result.Vision,
			"reasoning_effort":   result.ReasoningEffort,
		})
	})

	// API: Model search from models.dev - returns matching model IDs scoped to a provider
	mux.HandleFunc("/admin/model-search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		query := r.URL.Query().Get("q")
		prismProvider := r.URL.Query().Get("provider")
		results, err := fetchModelsDevSearch(query, prismProvider)
		if err != nil {
			writeJSONError(w, err.Error(), 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if results == nil {
			results = []map[string]string{}
		}
		json.NewEncoder(w).Encode(results)
	})

	addr := "127.0.0.1:" + port
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		log.Printf("Admin UI listening on http://%s/admin", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Admin server error: %v", err)
		}
	}()
}

// openAdminUI opens the admin UI in the default browser
func handleAutoStart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabled := isAutoStartEnabled()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"enabled": enabled})
	case http.MethodPut:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if req.Enabled {
			if err := setAutoStart(true); err != nil {
				http.Error(w, "failed to enable auto-start: "+err.Error(), 500)
				return
			}
		} else {
			if err := setAutoStart(false); err != nil {
				http.Error(w, "failed to disable auto-start: "+err.Error(), 500)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"enabled": req.Enabled})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// notifyTrayConfigChanged signals the tray to reload config and update UI
func notifyTrayConfigChanged() {
	// Reload config from disk into the tray's in-memory copy
	newCfg := loadConfig()
	adminMu.Lock()
	adminConfig = newCfg
	adminMu.Unlock()

	// Update the global tray config
	cfg = newCfg

	// Restart the proxy so it picks up config changes (e.g. active provider, API keys)
	if isProxyRunning() {
		stopProxyProcess()
		time.Sleep(500 * time.Millisecond)
		startProxyProcess()
		time.Sleep(500 * time.Millisecond)
		updateMenu(isProxyRunning())
	}
}

// reloadProxyModelRemap signals the running proxy process to hot-reload the model remapping.
func reloadProxyModelRemap() {
	if !isProxyRunning() {
		return
	}

	port := os.Getenv("PRISM_PORT")
	if port == "" {
		port = "11434"
	}

	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%s/__reload_model_remap__", port), "application/json", nil)
	if err != nil {
		log.Printf("Failed to signal model remap reload: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Model remap reload returned status %d", resp.StatusCode)
	}
}

// writeJSONError writes a JSON error response
func writeJSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func isPortAvailable(port string) bool {
	addr := "127.0.0.1:" + port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", 405)
		return
	}

	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", 400)
		return
	}

	if req.Provider != "codex" {
		writeJSONError(w, "unsupported provider: "+req.Provider, 400)
		return
	}

	state, err := startCodexOAuth()
	if err != nil {
		writeJSONError(w, "failed to start OAuth: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"state":  state,
	})
}

func handleOAuthAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	// Return accounts with masked tokens
	type safeAccount struct {
		ID               string  `json:"id"`
		Provider         string  `json:"provider"`
		Label            string  `json:"label"`
		Email            string  `json:"email"`
		PlanTier         string  `json:"plan_tier"`
		Active           bool    `json:"active"`
		TokenExpiry      string  `json:"token_expiry"`
		TokenValid       bool    `json:"token_valid"`
		CreditsUsed      float64 `json:"credits_used"`
		CreditsTotal     float64 `json:"credits_total"`
		CreditsRemaining float64 `json:"credits_remaining"`
		PercentUsed      float64 `json:"percent_used"`
		WeeklyResetAt    int64   `json:"weekly_reset_at"`
		LastUsageCheck   int64   `json:"last_usage_check"`
		UsageUnavailable bool    `json:"usage_unavailable"`
		SessionPercent   float64 `json:"session_percent"`
		SessionResetAt   int64   `json:"session_reset_at"`
		WeeklyPercent    float64 `json:"weekly_percent"`
		ReviewPercent    float64 `json:"review_percent"`
		CreditsBalance   float64 `json:"credits_balance"`
		RateLimitResets  int     `json:"rate_limit_resets"`
	}

	accounts := make([]safeAccount, 0, len(cfg.OAuthAccounts))
	for _, a := range cfg.OAuthAccounts {
		tokenValid := !a.IsTokenExpired()
		tokenExpiry := ""
		if a.ExpiresAt > 0 {
			tokenExpiry = time.Unix(a.ExpiresAt, 0).Format("2006-01-02 15:04:05")
		}

		// Start with stored values
		sessionPercent := a.SessionPercent
		sessionResetAt := a.SessionResetAt
		weeklyPercent := a.WeeklyPercent
		weeklyResetAt := a.WeeklyResetAt
		reviewPercent := a.ReviewPercent
		creditsBalance := a.CreditsBalance
		rateLimitResets := a.RateLimitResets
		percentUsed := a.SessionPercent
		usageUnavailable := false

		// Override with cached usage if newer
		usage := getCachedUsage(a.ID)
		if usage != nil && usage.LastUpdated > a.LastUsageCheck {
			sessionPercent = usage.SessionPercent
			sessionResetAt = usage.SessionResetAt
			weeklyPercent = usage.WeeklyPercent
			weeklyResetAt = usage.WeeklyResetAt
			reviewPercent = usage.ReviewPercent
			creditsBalance = usage.CreditsBalance
			rateLimitResets = usage.RateLimitResets
			percentUsed = usage.PercentUsed
			if usage.Error == "usage_unavailable" {
				usageUnavailable = true
			}
		} else if usage != nil && usage.Error == "usage_unavailable" {
			usageUnavailable = true
		}

		// Default plan tier to provider name if empty, try JWT fallback
		planTier := a.PlanTier
		if planTier == "" && a.AccessToken != "" {
			planTier = parseJWTPlanTier(a.AccessToken)
		}
		if planTier == "" {
			planTier = a.Provider
		}

		accounts = append(accounts, safeAccount{
			ID:               a.ID,
			Provider:         a.Provider,
			Label:            a.Label,
			Email:            a.Email,
			PlanTier:         planTier,
			Active:           a.Active,
			TokenExpiry:      tokenExpiry,
			TokenValid:       tokenValid,
			PercentUsed:      percentUsed,
			WeeklyResetAt:    weeklyResetAt,
			LastUsageCheck:   a.LastUsageCheck,
			UsageUnavailable: usageUnavailable,
			SessionPercent:   sessionPercent,
			SessionResetAt:   sessionResetAt,
			WeeklyPercent:    weeklyPercent,
			ReviewPercent:    reviewPercent,
			CreditsBalance:   creditsBalance,
			RateLimitResets:  rateLimitResets,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(accounts)
}

func handleOAuthAccountRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", 405)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", 400)
		return
	}

	if err := removeOAuthAccount(req.ID); err != nil {
		writeJSONError(w, err.Error(), 404)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleOAuthAccountActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", 405)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", 400)
		return
	}

	if err := setActiveOAuthAccount(req.ID); err != nil {
		writeJSONError(w, err.Error(), 404)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleOAuthUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "method not allowed", 405)
		return
	}

	accountID := r.URL.Query().Get("account")
	if accountID == "" {
		writeJSONError(w, "missing account parameter", 400)
		return
	}

	usage := getCachedUsage(accountID)
	if usage == nil {
		writeJSONError(w, "no usage data available", 404)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}

func handleOAuthUsageRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", 405)
		return
	}

	var req struct {
		AccountID string `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", 400)
		return
	}

	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	var account *OAuthAccount
	for _, a := range cfg.OAuthAccounts {
		if a.ID == req.AccountID {
			account = a
			break
		}
	}

	if account == nil {
		writeJSONError(w, "account not found", 404)
		return
	}

	usage, err := refreshUsageForAccount(account)
	if err != nil {
		writeJSONError(w, "usage refresh failed: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}
