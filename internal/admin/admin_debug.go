package admin

import (
	"encoding/json"
	"net/http"

	"ollama-proxy/internal/config"
)

// handleAdminDebugLogs gets/sets the debug-logs toggle. When enabled, the proxy
// writes per-request translation dump files under <logdir>/debug. The running
// proxy hot-reloads config after a PUT, so no restart is required.
func handleAdminDebugLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabled := false
		if c := config.Current(); c != nil {
			enabled = c.DebugLogs
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"enabled": enabled})
	case http.MethodPut:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, err.Error(), 400)
			return
		}
		c := config.Load()
		c.DebugLogs = body.Enabled
		if err := config.Save(c); err != nil {
			writeJSONError(w, "failed to save config: "+err.Error(), 500)
			return
		}
		// Keep the in-memory adminConfig in sync. Other handlers snapshot
		// adminConfig and call saveConfig, which would otherwise overwrite
		// config.json with a stale DebugLogs=false and - because the JSON tag is
		// omitempty - drop the field entirely, silently reverting the toggle.
		config.UpdateCurrent(func(c *config.Config) {
			if c != nil {
				c.DebugLogs = body.Enabled
			}
		})
		// Hot-reload the running proxy so the change takes effect immediately.
		reloadProxyModelRemap()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
