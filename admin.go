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
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

//go:embed admin.html
//go:embed docs/icon.png
var adminFS embed.FS

var (
	adminMu     sync.Mutex
	adminConfig *Config
)

func startAdminServer(cfg *Config, port string) {
	adminMu.Lock()
	adminConfig = cfg
	adminMu.Unlock()

	// Init persistent stats DB (shared with proxy process via WAL)
	if err := initDB(); err != nil {
		log.Printf("[DB] admin server failed to init: %v", err)
	}

	mux := http.NewServeMux()

	// Serve the single-page admin UI
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
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
		icon, _ := adminFS.ReadFile("docs/icon.png")
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
			if newCfg.ActiveProvider == "" {
				newCfg.ActiveProvider = "ollama_cloud"
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
			// Keep OAuth account Active flags in sync with ActiveProvider
			for _, a := range newCfg.OAuthAccounts {
				a.Active = (a.ID == newCfg.ActiveProvider)
			}
			// Validate custom provider URL if active
			if newCfg.ActiveProvider != "ollama_cloud" && newCfg.ActiveProvider != "opencode_go" {
				for _, p := range newCfg.CustomProviders {
					if p.ID == newCfg.ActiveProvider && p.BaseURL != "" {
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
				remap.KnownModels = []string{}
			}
			if remap.Aliases == nil {
				remap.Aliases = map[string]string{}
			}
			if err := saveModelRemapping(&remap); err != nil {
				http.Error(w, "save failed: "+err.Error(), 500)
				return
			}
			// Reload into running proxy
			reloadProxyModelRemap()
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
				"request_active":       false,
				"live_tokens_received": 0,
				"live_tokens_per_sec":  0,
				"total_requests":       0,
				"total_input_tokens":   0,
				"total_output_tokens":  0,
				"avg_tokens_per_sec":   0,
				"recent_requests":      []interface{}{},
				"by_model":             map[string]interface{}{},
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

		var fromTime, toTime time.Time
		if fromStr != "" {
			fromTime, _ = time.Parse("2006-01-02", fromStr)
		}
		if toStr != "" {
			toTime, _ = time.Parse("2006-01-02", toStr)
		}
		if fromTime.IsZero() {
			fromTime = now.AddDate(0, 0, -7)
		}
		if toTime.IsZero() {
			toTime = now
		}
		fromUnix := fromTime.Unix()
		toUnix := toTime.Add(24 * time.Hour).Unix()

		daily, _ := getDailyTokens(fromUnix, toUnix, provider, model)
		monthly, _ := getMonthlyTokens()
		tpsHist, _ := getTPSHistory(fromUnix, toUnix, provider, model)
		byModel, _ := getModelHistory(fromUnix, toUnix, provider, model)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"daily_tokens":   daily,
			"monthly_tokens": monthly,
			"tps_history":    tpsHist,
			"by_model":       byModel,
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
		providers, _ := getDistinctProviders()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models":    models,
			"providers": providers,
		})
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

func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("Prism")
	return err == nil
}

func setAutoStart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enable {
		exePath, err := os.Executable()
		if err != nil {
			return err
		}
		return k.SetStringValue("Prism", exePath)
	}
	return k.DeleteValue("Prism")
}

func openAdminUI(port string) {
	url := fmt.Sprintf("http://127.0.0.1:%s/admin", port)
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.Start()
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
	updateProviderMenu()
	updateAPIKeyDisplay()

	// Restart the proxy so it picks up config changes (e.g. active provider, API keys)
	if isProxyRunning() {
		stopProxyProcess()
		time.Sleep(500 * time.Millisecond)
		startProxyProcess()
		time.Sleep(500 * time.Millisecond)
		updateMenu(isProxyRunning())
	}
}

// reloadProxyModelRemap reloads the model remapping into the running proxy
func reloadProxyModelRemap() {
	// The proxy process will pick up changes on next restart
	if isProxyRunning() {
		stopProxyProcess()
		time.Sleep(500 * time.Millisecond)
		startProxyProcess()
		time.Sleep(500 * time.Millisecond)
		updateMenu(isProxyRunning())
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
		ID                 string  `json:"id"`
		Provider           string  `json:"provider"`
		Label              string  `json:"label"`
		Email              string  `json:"email"`
		PlanTier           string  `json:"plan_tier"`
		Active             bool    `json:"active"`
		TokenExpiry        string  `json:"token_expiry"`
		TokenValid         bool    `json:"token_valid"`
		CreditsUsed        float64 `json:"credits_used"`
		CreditsTotal       float64 `json:"credits_total"`
		CreditsRemaining   float64 `json:"credits_remaining"`
		PercentUsed        float64 `json:"percent_used"`
		WeeklyResetAt      int64   `json:"weekly_reset_at"`
		LastUsageCheck     int64   `json:"last_usage_check"`
		UsageUnavailable   bool    `json:"usage_unavailable"`
	}

	accounts := make([]safeAccount, 0, len(cfg.OAuthAccounts))
	for _, a := range cfg.OAuthAccounts {
		tokenValid := !a.IsTokenExpired()
		tokenExpiry := ""
		if a.ExpiresAt > 0 {
			tokenExpiry = time.Unix(a.ExpiresAt, 0).Format("2006-01-02 15:04:05")
		}

		// Get cached usage
		usage := getCachedUsage(a.ID)
		creditsUsed := a.CreditsUsed
		creditsTotal := a.CreditsTotal
		creditsRemaining := a.CreditsRemaining
		percentUsed := float64(0)
		usageUnavailable := false
		if creditsTotal > 0 {
			percentUsed = (creditsUsed / creditsTotal) * 100
		}

		if usage != nil && usage.LastUpdated > a.LastUsageCheck {
			creditsUsed = usage.CreditsUsed
			creditsTotal = usage.CreditsTotal
			creditsRemaining = usage.CreditsRemaining
			percentUsed = usage.PercentUsed
			if usage.Error == "usage_unavailable" {
				usageUnavailable = true
			}
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
			CreditsUsed:      creditsUsed,
			CreditsTotal:     creditsTotal,
			CreditsRemaining: creditsRemaining,
			PercentUsed:      percentUsed,
			WeeklyResetAt:    a.WeeklyResetAt,
			LastUsageCheck:   a.LastUsageCheck,
			UsageUnavailable: usageUnavailable,
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