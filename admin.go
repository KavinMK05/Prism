package main

import (
	"embed"
	"encoding/json"
	"fmt"
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
				newCfg.OllamaCloud = &ProviderConfig{Name: "Ollama Cloud", BaseURL: "https://ollama.com"}
			}
			if newCfg.OpenCodeGo == nil {
				newCfg.OpenCodeGo = &ProviderConfig{Name: "OpenCode Go", BaseURL: "https://opencode.ai/zen/go"}
			}
			if newCfg.Custom == nil {
				newCfg.Custom = &ProviderConfig{Name: "Custom", BaseURL: ""}
			}
			// Validate custom URL if switching to custom
			if newCfg.ActiveProvider == "custom" && newCfg.Custom.BaseURL != "" {
				if err := validateBaseURL(newCfg.Custom.BaseURL); err != nil {
					http.Error(w, "invalid custom base URL: "+err.Error(), 400)
					return
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
			if newCfg.Custom.APIKey == "" && adminConfig.Custom.APIKey != "" {
				newCfg.Custom.APIKey = adminConfig.Custom.APIKey
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
	cmd := exec.Command("cmd", "/c", "start", url)
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

func isPortAvailable(port string) bool {
	addr := "127.0.0.1:" + port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}