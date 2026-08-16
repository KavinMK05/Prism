// Package admin implements the Prism admin UI and API server.
package admin

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"ollama-proxy/internal/agents"
	"ollama-proxy/internal/config"
	"ollama-proxy/internal/db"
	"ollama-proxy/internal/desktop"
	"ollama-proxy/internal/platform"
)

// assets is the embedded admin UI filesystem (admin.html, icon.png,
// web/dist), owned by the main package's go:embed and wired in by
// StartAdminServer.
var assets embed.FS

func StartAdminServer(adminAssets embed.FS, cfg *config.Config, port string) {
	assets = adminAssets
	config.SetCurrent(cfg)
	config.SetChangeHook(func(_ *config.Config) { desktop.ReloadConfigAndRestartProxy() })

	// Init persistent stats DB (shared with proxy process via WAL)
	if err := db.Init(); err != nil {
		log.Printf("[DB] admin server failed to init: %v", err)
	}

	mux := http.NewServeMux()

	// Serve the React admin UI (Vite build output in web/dist)
	mux.HandleFunc("/admin", handleAdminIndex)
	mux.HandleFunc("/admin/", handleAdminStatic)

	// Serve the legacy single-page admin UI (plain HTML, pre-React migration)
	mux.HandleFunc("/admin-legacy", handleAdminLegacy)

	// Serve the brand icon
	mux.HandleFunc("/admin/icon.png", handleAdminIcon)

	// API: Get config
	mux.HandleFunc("/admin/config", handleAdminConfig)

	// API: Model remapping
	mux.HandleFunc("/admin/model-remap", handleAdminModelRemap)

	// API: Debug logs toggle
	mux.HandleFunc("/admin/debug-logs", handleAdminDebugLogs)

	// API: Anonymous analytics opt-in
	mux.HandleFunc("/admin/analytics/settings", handleAdminAnalyticsSettings)

	// API: Proxy status
	mux.HandleFunc("/admin/status", handleAdminStatus)

	// API: Proxy control
	mux.HandleFunc("/admin/proxy/start", handleProxyStart)

	mux.HandleFunc("/admin/proxy/stop", handleProxyStop)

	mux.HandleFunc("/admin/proxy/restart", handleProxyRestart)

	// API: SearXNG managed instance — status / lifecycle / settings / autostart
	mux.HandleFunc("/admin/searxng/status", handleSearxngStatus)

	mux.HandleFunc("/admin/searxng/start", handleSearxngStart)

	mux.HandleFunc("/admin/searxng/stop", handleSearxngStop)

	mux.HandleFunc("/admin/searxng/restart", handleSearxngRestart)

	mux.HandleFunc("/admin/searxng/settings", handleSearxngSettings)

	mux.HandleFunc("/admin/searxng/autostart", handleSearxngAutostart)

	// API: Search providers (pluggable web-search backends for agent interception)
	mux.HandleFunc("/admin/search/providers", handleSearchProviders)

	mux.HandleFunc("/admin/search/config", handleSearchConfig)

	mux.HandleFunc("/admin/search/test", handleSearchTest)

	// API: Codex Desktop integration
	mux.HandleFunc("/admin/codex-desktop/status", handleCodexDesktopStatus)

	mux.HandleFunc("/admin/codex-desktop/setup", handleCodexDesktopSetup)

	mux.HandleFunc("/admin/codex-desktop/restore", handleCodexDesktopRestore)

	// API: Agent integrations (Claude Code, Factory Droid, OpenCode)
	// Generic handlers dispatch by ?id=. Setup/Restore return 501 in Phase 1
	// until each agent's writer lands in its own phase.
	mux.HandleFunc("/admin/agent/status", handleAgentStatus)

	mux.HandleFunc("/admin/agent/setup", handleAgentSetup)

	mux.HandleFunc("/admin/agent/restore", handleAgentRestore)

	// API: Logs
	mux.HandleFunc("/admin/autostart", handleAutoStart)

	// OAuth API endpoints
	mux.HandleFunc("/admin/oauth/login", handleOAuthLogin)
	mux.HandleFunc("/admin/oauth/accounts", handleOAuthAccounts)
	mux.HandleFunc("/admin/oauth/accounts/remove", handleOAuthAccountRemove)
	mux.HandleFunc("/admin/oauth/accounts/activate", handleOAuthAccountActivate)
	mux.HandleFunc("/admin/oauth/usage", handleOAuthUsage)
	mux.HandleFunc("/admin/oauth/usage/refresh", handleOAuthUsageRefresh)

	mux.HandleFunc("/admin/logs", handleAdminLogs)

	// API: Live stats - proxy to the running proxy server's /v1/stats endpoint
	mux.HandleFunc("/admin/stats", handleStats)

	// API: Historical stats - reads directly from SQLite so it works when proxy is off
	mux.HandleFunc("/admin/stats/history", handleStatsHistory)

	// API: Clear all persistent stats
	mux.HandleFunc("/admin/stats/clear", handleStatsClear)

	// API: Get distinct models and providers for filters
	mux.HandleFunc("/admin/stats/filters", handleStatsFilters)

	// API: Model info from models.dev (fetched directly to work when proxy is off)
	mux.HandleFunc("/admin/model-info", handleModelInfo)

	// API: Model search from models.dev - returns matching model IDs scoped to a provider
	mux.HandleFunc("/admin/model-search", handleModelSearch)

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
		enabled := platform.IsAutoStartEnabled()
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
			if err := platform.SetAutoStart(true); err != nil {
				http.Error(w, "failed to enable auto-start: "+err.Error(), 500)
				return
			}
		} else {
			if err := platform.SetAutoStart(false); err != nil {
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

// reloadProxyModelRemap signals the running proxy process to hot-reload the model remapping.
func reloadProxyModelRemap() {
	if !desktop.IsProxyRunning() {
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

// encodeJSON is a tiny helper for admin JSON responses.
func encodeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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

func handleAdminIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html, err := assets.ReadFile("web/dist/index.html")
	if err != nil {
		w.Write([]byte("<!DOCTYPE html><html><body>Frontend not built. Run: cd web && npm install && npm run build</body></html>"))
		return
	}
	w.Write(html)
}

func handleAdminStatic(w http.ResponseWriter, r *http.Request) {
	// Serve static assets from Vite build (e.g. /admin/assets/index-abc.js)
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	if path == "" {
		// /admin/ - serve index.html
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html, err := assets.ReadFile("web/dist/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(html)
		return
	}
	data, err := assets.ReadFile("web/dist/" + path)
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
}

func handleAdminLegacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html, _ := assets.ReadFile("admin.html")
	w.Write(html)
}

func handleAdminIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	icon, err := assets.ReadFile("icon.png")
	if err != nil {
		http.Error(w, "icon not found", http.StatusNotFound)
		return
	}
	w.Write(icon)
}

func handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	cfg := config.Current()

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	case http.MethodPut:
		var newCfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), 400)
			return
		}
		// Validate
		if newCfg.DefaultProvider == "" {
			newCfg.DefaultProvider = "ollama_cloud"
		}
		if newCfg.OllamaCloud == nil {
			newCfg.OllamaCloud = &config.ProviderConfig{ID: "ollama_cloud", Name: "Ollama Cloud", BaseURL: "https://ollama.com"}
		}
		if newCfg.OpenCodeGo == nil {
			newCfg.OpenCodeGo = &config.ProviderConfig{ID: "opencode_go", Name: "OpenCode Go", BaseURL: "https://opencode.ai/zen/go"}
		}
		if newCfg.CustomProviders == nil {
			newCfg.CustomProviders = []*config.ProviderConfig{}
		}
		// Ensure built-in IDs
		newCfg.OllamaCloud.ID = "ollama_cloud"
		newCfg.OpenCodeGo.ID = "opencode_go"
		// Ensure custom providers have IDs
		for _, p := range newCfg.CustomProviders {
			if p.ID == "" {
				p.ID = config.GenerateProviderID(p.Name)
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
					if err := config.ValidateBaseURL(p.BaseURL); err != nil {
						http.Error(w, "invalid custom base URL: "+err.Error(), 400)
						return
					}
				}
			}
		}

		// Preserve API keys if not provided (empty string = don't overwrite)
		cur := config.Current()
		if newCfg.OllamaCloud.APIKey == "" && cur.OllamaCloud.APIKey != "" {
			newCfg.OllamaCloud.APIKey = cur.OllamaCloud.APIKey
		}
		if newCfg.OpenCodeGo.APIKey == "" && cur.OpenCodeGo.APIKey != "" {
			newCfg.OpenCodeGo.APIKey = cur.OpenCodeGo.APIKey
		}
		// Preserve API keys for custom providers that already exist
		for i, newP := range newCfg.CustomProviders {
			if newP.APIKey == "" {
				for _, oldP := range cur.CustomProviders {
					if oldP != nil && newP.ID == oldP.ID && oldP.APIKey != "" {
						newCfg.CustomProviders[i].APIKey = oldP.APIKey
						break
					}
				}
			}
		}

		if err := config.Save(&newCfg); err != nil {
			http.Error(w, "save failed: "+err.Error(), 500)
			return
		}
		// Swap the live config; the change hook restarts the proxy (if running).
		config.SetCurrent(&newCfg)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func handleAdminModelRemap(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		remap := config.LoadModelRemapping()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(remap)
	case http.MethodPut:
		var remap config.ModelRemapping
		if err := json.NewDecoder(r.Body).Decode(&remap); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), 400)
			return
		}
		if remap.DefaultModel == "" {
			remap.DefaultModel = "glm-5.1:cloud"
		}
		if remap.KnownModels == nil {
			remap.KnownModels = []config.ModelEntry{}
		}
		if remap.Aliases == nil {
			remap.Aliases = map[string]string{}
		}
		// Ensure all model entries have a provider
		cfg := config.Load()
		for i := range remap.KnownModels {
			if remap.KnownModels[i].Provider == "" {
				remap.KnownModels[i].Provider = cfg.DefaultProvider
			}
		}
		if err := config.SaveModelRemapping(&remap); err != nil {
			http.Error(w, "save failed: "+err.Error(), 500)
			return
		}
		// Reload into running proxy
		reloadProxyModelRemap()
		// Sync agent configs so newly added models appear in Claude Code,
		// Factory Droid, and OpenCode
		agents.SyncAgents(agents.ProxyPortFromEnv())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"running": desktop.IsProxyRunning(),
		"version": desktop.Version(),
	})
}

func handleProxyStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	desktop.StartProxyProcess()
	time.Sleep(500 * time.Millisecond)
	desktop.UpdateMenu(desktop.IsProxyRunning())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleProxyStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	desktop.StopProxyProcess()
	time.Sleep(500 * time.Millisecond)
	desktop.UpdateMenu(desktop.IsProxyRunning())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleProxyRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	desktop.StopProxyProcess()
	time.Sleep(500 * time.Millisecond)
	desktop.StartProxyProcess()
	time.Sleep(500 * time.Millisecond)
	desktop.UpdateMenu(desktop.IsProxyRunning())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	logPath := desktop.GetLogFilePath()
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
}
