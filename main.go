package main

import (
	"embed"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

//go:embed icon.ico
var iconFS embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--serve" {
		runProxyServer()
		return
	}

	iconData, err := embed.FS.ReadFile(iconFS, "icon.ico")
	if err != nil {
		log.Fatalf("Failed to load icon: %v", err)
	}
	runTray(iconData)
}

func runProxyServer() {
	cfg := loadConfig()
	apiKey := cfg.getActiveAPIKey()
	if apiKey == "" {
		log.Fatal("API key is required. Set it via the tray menu or environment variable.")
	}

	port := os.Getenv("OLLAMA_PROXY_PORT")
	if port == "" {
		port = "11434"
	}

	host := os.Getenv("OLLAMA_PROXY_HOST")
	if host == "" {
		host = "127.0.0.1"
	}

	upstreamURL := cfg.getActiveBaseURL()
	providerType := cfg.getProviderType()
	modelRemap := loadModelRemapping()

	proxy := NewProxy(upstreamURL, apiKey, providerType, modelRemap)

	if !strings.HasPrefix(host, "127.0.0.1") && !strings.HasPrefix(host, "localhost") && !strings.HasPrefix(host, "::1") {
		log.Printf("WARNING: Proxy is listening on %s which is accessible from the network. Consider using 127.0.0.1 for local-only access.", host)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", loggingMiddleware(handleRoot))
	mux.HandleFunc("/v1/messages", loggingMiddleware(authMiddleware(apiKey, proxy.HandleMessages)))
	mux.HandleFunc("/v1/messages/count_tokens", loggingMiddleware(authMiddleware(apiKey, handleCountTokens)))
	mux.HandleFunc("/health", loggingMiddleware(handleHealth))

	addr := host + ":" + port

	log.Printf("ollama-proxy starting on %s → %s (provider: %s)", addr, upstreamURL, cfg.ActiveProvider)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	log.Printf("ollama-proxy listening on http://%s", addr)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	server.Close()
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
	w.Write([]byte(`{"status":"ok","service":"ollama-proxy","version":"1.0.0"}`))
}

func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("← %s %s %s", r.Method, r.URL.Path, r.RemoteAddr)
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