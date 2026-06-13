package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--serve" {
		runProxyServer()
		return
	}

	cleanup, err := acquireInstanceLock()
	if err != nil {
		log.Println(err)
		return
	}

	cleanupOldBinary()

	iconData, err := loadIconData()
	if err != nil {
		log.Fatalf("Failed to load icon: %v", err)
	}
	runTray(iconData, cleanup)
}

func runProxyServer() {
	// Open log file directly instead of relying on stderr redirection
	logDir := getLogDir()
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "proxy.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(logFile)
	}

	// Init persistent stats DB
	if err := initDB(); err != nil {
		log.Printf("[DB] failed to init: %v", err)
	} else {
		defer closeDB()
	}

	cfg := loadConfig()
	proxyAPIKey := "prism"

	port := os.Getenv("PRISM_PORT")
	if port == "" {
		port = "11434"
	}

	host := os.Getenv("PRISM_HOST")
	if host == "" {
		host = "127.0.0.1"
	}

	modelRemap := loadModelRemapping()

	router := NewProviderRouter(cfg, modelRemap)

	if !strings.HasPrefix(host, "127.0.0.1") && !strings.HasPrefix(host, "localhost") && !strings.HasPrefix(host, "::1") {
		log.Printf("WARNING: Proxy is listening on %s which is accessible from the network. Consider using 127.0.0.1 for local-only access.", host)
	}

	adminPort := os.Getenv("PRISM_ADMIN_PORT")
	if adminPort == "" {
		adminPort = "8765"
	}
	// Start the admin UI server in the tray process
	// (not in the --serve proxy process)
	mux := http.NewServeMux()

	// Internal endpoint to hot-reload model remapping without restarting the process
	mux.HandleFunc("/__reload_model_remap__", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		router.ReloadModelRemapping()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", loggingMiddleware(handleRoot))
	mux.HandleFunc("/v1/messages", loggingMiddleware(authMiddleware(proxyAPIKey, router.HandleMessages)))
	mux.HandleFunc("/v1/messages/count_tokens", loggingMiddleware(authMiddleware(proxyAPIKey, handleCountTokens)))
	mux.HandleFunc("/health", loggingMiddleware(handleHealth))
	mux.HandleFunc("/v1/chat/completions", loggingMiddleware(openaiAuthMiddleware(proxyAPIKey, router.HandleOpenAIChatCompletions)))
	mux.HandleFunc("/v1/responses", loggingMiddleware(openaiAuthMiddleware(proxyAPIKey, router.HandleResponsesAPI)))
	mux.HandleFunc("/v1/models", loggingMiddleware(openaiAuthMiddleware(proxyAPIKey, router.HandleModels)))

	// Model info endpoint - proxies models.dev lookups for the admin UI
	mux.HandleFunc("/api/model-info", loggingMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		modelID := r.URL.Query().Get("id")
		if modelID == "" {
			http.Error(w, "missing id parameter", 400)
			return
		}
		// Fetch models.dev database
		resp, err := http.Get("https://models.dev/api.json")
		if err != nil {
			http.Error(w, "failed to fetch models.dev: "+err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			http.Error(w, "models.dev returned status "+fmt.Sprintf("%d", resp.StatusCode), 502)
			return
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "failed to read models.dev response", 502)
			return
		}
		// Parse as raw map to iterate all providers
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			http.Error(w, "failed to parse models.dev response", 502)
			return
		}
		type modelInfo struct {
			Name            string `json:"name"`
			ID              string `json:"id"`
			Limit           struct {
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
		// Strip provider suffix like :cloud or :free from search
		searchBase := modelID
		if idx := strings.Index(modelID, ":"); idx > 0 {
			searchBase = modelID[:idx]
		}
		searchID := strings.ToLower(searchBase)
		type match struct {
			model    modelInfo
			provider string
			exact    bool
			native   bool
		}
		var matches []match
		for _, provRaw := range raw {
			var prov providerInfo
			if json.Unmarshal(provRaw, &prov) != nil {
				continue
			}
			provIDLower := strings.ToLower(prov.ID)
			for _, m := range prov.Models {
				mID := strings.ToLower(m.ID)
				mName := strings.ToLower(m.Name)
				isExact := mID == searchID
				isNative := strings.HasPrefix(searchID, provIDLower)
				if isExact {
					matches = append(matches, match{model: m, provider: prov.ID, exact: true, native: isNative})
				} else if strings.Contains(mID, searchID) || strings.Contains(searchID, mID) || strings.Contains(mName, searchID) {
					matches = append(matches, match{model: m, provider: prov.ID, exact: false, native: isNative})
				}
			}
		}
		// Prefer: exact+native > exact > native+partial > partial
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].exact != matches[j].exact {
				return matches[i].exact
			}
			if matches[i].native != matches[j].native {
				return matches[i].native
			}
			return false
		})
		var bestMatch *modelInfo
		var bestProvider string
		if len(matches) > 0 {
			best := matches[len(matches)-1]
			bestMatch = &best.model
			bestProvider = best.provider
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if bestMatch == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
			return
		}
		vision := false
		if bestMatch.Modalities != nil {
			for _, mod := range bestMatch.Modalities.Input {
				if mod == "image" {
					vision = true
				}
			}
		}
		result := map[string]interface{}{
			"found":             true,
			"id":                bestMatch.ID,
			"name":              bestMatch.Name,
			"provider_id":       bestProvider,
			"context_length":    bestMatch.Limit.Context,
			"max_output_tokens": bestMatch.Limit.Output,
			"reasoning":         bestMatch.Reasoning,
			"tool_calling":      bestMatch.ToolCall,
			"structured_outputs": bestMatch.StructuredOutput,
			"vision":            vision,
		}
		if bestMatch.Reasoning {
			efforts := []string{"low", "medium", "high"}
			if strings.Contains(searchID, "deepseek-v4") {
				efforts = append(efforts, "max")
			}
			result["reasoning_effort"] = efforts
		}
		json.NewEncoder(w).Encode(result)
	}))

	// Stats endpoint (proxied from admin UI)
	mux.HandleFunc("/v1/stats", loggingMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		data, err := StatsToJSON()
		if err != nil {
			http.Error(w, "failed to serialize stats", 500)
			return
		}
		w.Write(data)
	}))

	// Start background TPS snapshot writer
	go startTPSSnapshotLoop()

	addr := host + ":" + port

	log.Printf("Prism starting on %s (default provider: %s)", addr, cfg.DefaultProvider)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	log.Printf("Prism proxy listening on http://%s", addr)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	server.Close()
}

func startTPSSnapshotLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		snapshot := globalStats.GetSnapshot()
		if snapshot.RequestActive && snapshot.LiveTokensPerSec > 0 {
			if err := dbRecordTPSSnapshot(snapshot.CurrentModel, snapshot.CurrentProvider, snapshot.CurrentClient, snapshot.LiveTokensPerSec); err != nil {
				log.Printf("[DB] TPS snapshot error: %v", err)
			}
		}
	}
}

func handleCountTokens(w http.ResponseWriter, r *http.Request) {
	writeAnthropicError(w, 404, "not_found_error", "Token counting is not supported")
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf(`{"status":"ok","service":"prism","version":"%s"}`, version)))
}

func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ua := r.UserAgent()
		xClient := r.Header.Get("X-Client-Name")
		xTitle := r.Header.Get("X-Title")
		referer := r.Header.Get("Referer")
		origin := r.Header.Get("Origin")
		if ua != "" || xClient != "" || xTitle != "" || referer != "" || origin != "" {
			log.Printf("<- %s %s %s | UA=%q X-Client-Name=%q X-Title=%q Referer=%q Origin=%q",
				r.Method, r.URL.Path, r.RemoteAddr, ua, xClient, xTitle, referer, origin)
		} else {
			log.Printf("<- %s %s %s", r.Method, r.URL.Path, r.RemoteAddr)
		}
		next(w, r)
	}
}

const maxRequestBody = 50 << 20

func authMiddleware(proxyAPIKey string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

		if proxyAPIKey != "" {
			clientKey := r.Header.Get("x-api-key")
			if clientKey == "" {
				clientKey = r.Header.Get("Authorization")
				clientKey = strings.TrimPrefix(clientKey, "Bearer ")
				clientKey = strings.TrimPrefix(clientKey, "bearer ")
			}
			if clientKey != proxyAPIKey {
				writeAnthropicError(w, 401, "authentication_error", "Invalid or missing API key")
				return
			}
		}
		next(w, r)
	}
}

func openaiAuthMiddleware(proxyAPIKey string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

		if proxyAPIKey != "" {
			clientKey := r.Header.Get("x-api-key")
			if clientKey == "" {
				clientKey = r.Header.Get("Authorization")
				clientKey = strings.TrimPrefix(clientKey, "Bearer ")
				clientKey = strings.TrimPrefix(clientKey, "bearer ")
			}
			if clientKey != proxyAPIKey {
				writeOpenAIError(w, 401, "authentication_error", "Invalid or missing API key")
				return
			}
		}
		next(w, r)
	}
}

// detectClient identifies the calling client tool from request headers
func detectClient(r *http.Request) string {
	// Prefer explicit client name header if set by user
	xClient := r.Header.Get("X-Client-Name")
	if xClient != "" {
		return xClient
	}

	ua := strings.ToLower(r.UserAgent())
	switch {
	case strings.Contains(ua, "factory-cli") || strings.Contains(ua, "factory-droid") || strings.Contains(ua, "factory-droid"):
		return "Factory Droid"
	case strings.Contains(ua, "claude-code") || strings.Contains(ua, "claude-code"):
		return "Claude Code"
	case strings.Contains(ua, "opencode") || strings.Contains(ua, "open-code"):
		return "OpenCode"
	case strings.Contains(ua, "cursor"):
		return "Cursor"
	case strings.Contains(ua, "copilot") || strings.Contains(ua, "github-copilot"):
		return "GitHub Copilot"
	case strings.Contains(ua, "aider"):
		return "Aider"
	case strings.Contains(ua, "continue"):
		return "Continue"
	case strings.Contains(ua, "supermaven"):
		return "Supermaven"
	case strings.Contains(ua, "windsurf"):
		return "Windsurf"
	case strings.Contains(ua, "trae"):
		return "Trae"
	case strings.Contains(ua, "claude") && strings.Contains(ua, "anthropic"):
		return "Claude"
	default:
		rawUA := r.UserAgent()
		if rawUA != "" {
			return rawUA
		}
		return "Unknown"
	}
}
