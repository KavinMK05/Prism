package main

import (
	"embed"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

//go:embed logo_icon.ico
var iconFS embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--serve" {
		runProxyServer()
		return
	}

	// Single-instance guard for the tray process
	mutexName, _ := windows.UTF16PtrFromString("PrismSingleInstance")
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil {
		log.Fatalf("Failed to create mutex: %v", err)
	}
	defer windows.CloseHandle(mutex)

	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		log.Println("Prism is already running")
		return
	}

	iconData, err := embed.FS.ReadFile(iconFS, "logo_icon.ico")
	if err != nil {
		log.Fatalf("Failed to load icon: %v", err)
	}
	runTray(iconData)
}

func runProxyServer() {
	// Open log file directly instead of relying on stderr redirection
	logDir := filepath.Join(os.Getenv("APPDATA"), "prism")
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
	upstreamAPIKey := cfg.getActiveAPIKey()

	port := os.Getenv("PRISM_PORT")
	if port == "" {
		port = "11434"
	}

	host := os.Getenv("PRISM_HOST")
	if host == "" {
		host = "127.0.0.1"
	}

	upstreamURL := cfg.getActiveBaseURL()
	providerType := cfg.getProviderType()
	modelRemap := loadModelRemapping()

	var proxy *Proxy

	// Check if active provider is an OAuth account
	activeOAuth := getActiveOAuthAccountForConfig(cfg)
	if activeOAuth != nil {
		proxy = NewProxyWithOAuth(upstreamURL, providerType, modelRemap, activeOAuth)
	} else {
		proxy = NewProxy(upstreamURL, upstreamAPIKey, providerType, modelRemap)
	}

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
	mux.HandleFunc("/", loggingMiddleware(handleRoot))
	mux.HandleFunc("/v1/messages", loggingMiddleware(authMiddleware(proxyAPIKey, proxy.HandleMessages)))
	mux.HandleFunc("/v1/messages/count_tokens", loggingMiddleware(authMiddleware(proxyAPIKey, handleCountTokens)))
	mux.HandleFunc("/health", loggingMiddleware(handleHealth))
	mux.HandleFunc("/v1/chat/completions", loggingMiddleware(openaiAuthMiddleware(proxyAPIKey, proxy.HandleOpenAIChatCompletions)))
	mux.HandleFunc("/v1/responses", loggingMiddleware(openaiAuthMiddleware(proxyAPIKey, proxy.HandleResponsesAPI)))
	mux.HandleFunc("/v1/models", loggingMiddleware(openaiAuthMiddleware(proxyAPIKey, proxy.HandleModels)))

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

	log.Printf("Prism starting on %s -> %s (provider: %s)", addr, upstreamURL, cfg.ActiveProvider)

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
			if err := dbRecordTPSSnapshot(snapshot.CurrentModel, snapshot.CurrentProvider, snapshot.LiveTokensPerSec); err != nil {
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
	w.Write([]byte(`{"status":"ok","service":"prism","version":"1.0.0"}`))
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
