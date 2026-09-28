package admin

import (
	"encoding/json"
	"net/http"

	"ollama-proxy/internal/analytics"
	"ollama-proxy/internal/config"
	"ollama-proxy/internal/desktop"
)

// handleAdminAnalyticsSettings gets/sets the anonymous analytics opt-out. The
// flags live in config.json. Telemetry is on by default; turning it off records
// an explicit opt-out, and turning it back on fires an immediate heartbeat so
// the install is counted right away rather than on the next daily tick.
func handleAdminAnalyticsSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		optOut, noticeSeen := false, false
		if c := config.Current(); c != nil {
			optOut = c.AnalyticsOptOut
			noticeSeen = c.AnalyticsNoticeSeen
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{
			"enabled":     analytics.Enabled(),
			"opt_out":     optOut,
			"notice_seen": noticeSeen,
			"forced_off":  analytics.ForcedOff(),
		})
	case http.MethodPut:
		var body struct {
			OptOut     bool `json:"opt_out"`
			NoticeSeen bool `json:"notice_seen"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, err.Error(), 400)
			return
		}
		c := config.Load()
		c.AnalyticsOptOut = body.OptOut
		c.AnalyticsNoticeSeen = body.NoticeSeen
		if err := config.Save(c); err != nil {
			writeJSONError(w, "failed to save config: "+err.Error(), 500)
			return
		}
		// Keep the in-memory live config in sync without triggering a proxy
		// restart (analytics consent does not affect the proxy).
		config.UpdateCurrent(func(c *config.Config) {
			if c != nil {
				c.AnalyticsOptOut = body.OptOut
				c.AnalyticsNoticeSeen = body.NoticeSeen
			}
		})
		// Count the user right away if they just turned telemetry back on.
		if !body.OptOut {
			analytics.PingIfDue(desktop.Version())
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
