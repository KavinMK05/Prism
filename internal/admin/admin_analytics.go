package admin

import (
	"encoding/json"
	"net/http"

	"ollama-proxy/internal/analytics"
	"ollama-proxy/internal/config"
	"ollama-proxy/internal/desktop"
)

// handleAdminAnalyticsSettings gets/sets the anonymous analytics opt-in. The
// consent flags live in config.json. Flipping opt-in to true triggers an
// immediate heartbeat so the user is counted right away rather than waiting for
// the next startup or daily tick.
func handleAdminAnalyticsSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		optIn, prompted := false, false
		if c := config.Current(); c != nil {
			optIn = c.AnalyticsOptIn
			prompted = c.AnalyticsPrompted
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{
			"opt_in":   optIn,
			"prompted": prompted,
		})
	case http.MethodPut:
		var body struct {
			OptIn    bool `json:"opt_in"`
			Prompted bool `json:"prompted"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, err.Error(), 400)
			return
		}
		c := config.Load()
		c.AnalyticsOptIn = body.OptIn
		c.AnalyticsPrompted = body.Prompted
		if err := config.Save(c); err != nil {
			writeJSONError(w, "failed to save config: "+err.Error(), 500)
			return
		}
		// Keep the in-memory live config in sync without triggering a proxy
		// restart (analytics consent does not affect the proxy).
		config.UpdateCurrent(func(c *config.Config) {
			if c != nil {
				c.AnalyticsOptIn = body.OptIn
				c.AnalyticsPrompted = body.Prompted
			}
		})
		// If the user just opted in, send a heartbeat immediately.
		if body.OptIn {
			analytics.PingIfDue(desktop.Version())
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
