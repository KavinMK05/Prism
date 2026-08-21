package admin

import (
	"encoding/json"
	"log"
	"net/http"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/desktop"
)

func handleSearxngStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(desktop.SearxngStatus())
}

func handleSearxngStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	go func() { _ = desktop.StartSearxngProcess() }()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleSearxngStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	desktop.StopSearxngProcess()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleSearxngRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	go func() { _ = desktop.RestartSearxngProcess() }()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleSearxngUpdate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st, err := desktop.SearxngCheckUpdate()
		if err != nil {
			writeJSONError(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st)
	case http.MethodPost:
		if desktop.SearxngUpdateInProgress() {
			writeJSONError(w, "SearXNG update already in progress", 409)
			return
		}
		go func() {
			if err := desktop.UpdateSearxng(); err != nil {
				log.Printf("[admin] SearXNG update failed: %v", err)
			}
		}()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func handleSearxngSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f, err := desktop.LoadSearxngSettingsForm()
		if err != nil {
			writeJSONError(w, "SearXNG not installed: "+err.Error(), 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(f)
	case http.MethodPut:
		var f desktop.SearxngSettingsForm
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeJSONError(w, err.Error(), 400)
			return
		}
		if err := desktop.ValidateSearxngSettingsForm(&f); err != nil {
			writeJSONError(w, err.Error(), 400)
			return
		}
		if err := desktop.SaveSearxngSettingsForm(&f); err != nil {
			writeJSONError(w, "failed to save settings: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func handleSearxngAutostart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"enabled": desktop.SearxngAutostartEnabled()})
	case http.MethodPut:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, err.Error(), 400)
			return
		}
		c := config.Load()
		c.SearXNGAutoStart = body.Enabled
		if err := config.Save(c); err != nil {
			writeJSONError(w, "failed to save config: "+err.Error(), 500)
			return
		}
		// Keep the in-memory adminConfig in sync. Other handlers (OAuth
		// add/remove, background usage refresh) snapshot adminConfig and call
		// saveConfig, which would otherwise overwrite config.json with a stale
		// SearXNGAutoStart=false and - because the JSON tag is omitempty - drop
		// the field entirely, silently reverting the user's toggle.
		config.UpdateCurrent(func(c *config.Config) {
			if c != nil {
				c.SearXNGAutoStart = body.Enabled
			}
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
