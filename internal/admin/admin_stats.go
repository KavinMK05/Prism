package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/db"
)

func handleStats(w http.ResponseWriter, r *http.Request) {
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
			"current_client":       "",
			"request_active":       false,
			"live_tokens_received": 0,
			"live_tokens_per_sec":  0,
			"total_requests":       0,
			"total_input_tokens":   0,
			"total_output_tokens":  0,
			"avg_tokens_per_sec":   0,
			"recent_requests":      []interface{}{},
			"by_model":             map[string]interface{}{},
			"by_client":            map[string]interface{}{},
		})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

func handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	now := time.Now()
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	provider := r.URL.Query().Get("provider")
	model := r.URL.Query().Get("model")
	client := r.URL.Query().Get("client")

	// Parse from/to as either full datetime or date-only. When date-only,
	// "to" is inclusive end-of-day (+24h). When datetime, "to" is exact.
	const dateLayout = "2006-01-02"
	const dtLayout = "2006-01-02T15:04"
	var fromTime, toTime time.Time
	var toIsDatetime bool
	if fromStr != "" {
		if t, err := time.ParseInLocation(dtLayout, fromStr, time.Local); err == nil {
			fromTime = t
		} else if t, err := time.ParseInLocation(dateLayout, fromStr, time.Local); err == nil {
			fromTime = t
		}
	}
	if toStr != "" {
		if t, err := time.ParseInLocation(dtLayout, toStr, time.Local); err == nil {
			toTime = t
			toIsDatetime = true
		} else if t, err := time.ParseInLocation(dateLayout, toStr, time.Local); err == nil {
			toTime = t
		}
	}
	if fromTime.IsZero() {
		fromTime = now.AddDate(0, 0, -7)
	}
	if toTime.IsZero() {
		toTime = now
	}
	fromUnix := fromTime.Unix()
	toUnix := toTime.Unix()
	if !toIsDatetime {
		toUnix = toTime.Add(24 * time.Hour).Unix()
	}

	daily, _ := db.GetDailyTokens(fromUnix, toUnix, provider, model, client)
	monthly, _ := db.GetMonthlyTokens(client)
	tpsHist, _ := db.GetTPSHistory(fromUnix, toUnix, provider, model, client)
	byModel, _ := db.GetModelHistory(fromUnix, toUnix, provider, model, client)
	byClient, _ := db.GetClientHistory(fromUnix, toUnix, provider, model, client)

	// For ranges <= 24h, return hourly-bucketed tokens (adaptive bucket size).
	spanMin := int((toUnix - fromUnix) / 60)
	var hourly []db.HourlyTokens
	hasHourly := false
	if spanMin > 0 && spanMin <= 24*60 {
		bucket := 60
		switch {
		case spanMin <= 90:
			bucket = 5
		case spanMin <= 6*60:
			bucket = 30
		}
		hourly, _ = db.GetHourlyTokens(fromUnix, toUnix, bucket, provider, model, client)
		hasHourly = true
	}

	// Heatmap always shows the last 365 days, filtered by provider/model/client
	heatmapTo := now.Add(24 * time.Hour).Unix()
	heatmapFrom := now.AddDate(0, 0, -365).Unix()
	heatmap, _ := db.GetDailyTokens(heatmapFrom, heatmapTo, provider, model, client)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"daily_tokens":   daily,
		"monthly_tokens": monthly,
		"tps_history":    tpsHist,
		"by_model":       byModel,
		"by_client":      byClient,
		"heatmap_tokens": heatmap,
		"hourly_tokens":  hourly,
		"has_hourly":     hasHourly,
	})
}

func handleStatsClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := db.ClearAllStats(); err != nil {
		writeJSONError(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleStatsFilters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	models, _ := db.GetDistinctModels()
	dbProviders, _ := db.GetDistinctProviders()
	clients, _ := db.GetDistinctClients()

	// Merge configured providers with ones from the DB
	cfg := config.Load()
	providerSet := map[string]bool{}
	for _, p := range dbProviders {
		providerSet[p] = true
	}
	// Add all configured providers
	configProviders := []string{"ollama_cloud", "opencode_go"}
	for _, p := range cfg.CustomProviders {
		configProviders = append(configProviders, p.ID)
	}
	for _, a := range cfg.OAuthAccounts {
		configProviders = append(configProviders, a.ID)
	}
	for _, p := range configProviders {
		if !providerSet[p] {
			dbProviders = append(dbProviders, p)
			providerSet[p] = true
		}
	}

	// Build provider id -> display name map
	providerNames := map[string]string{
		"ollama_cloud": "Ollama Cloud",
		"opencode_go":  "OpenCode Go",
	}
	for _, p := range cfg.CustomProviders {
		if p.Name != "" {
			providerNames[p.ID] = p.Name
		}
	}
	for _, a := range cfg.OAuthAccounts {
		name := a.Label
		if name == "" {
			name = a.Email
		}
		if name == "" {
			name = a.ID
		}
		providerNames[a.ID] = name
	}
	// Convert to sorted list of {id, name} objects
	type providerOption struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	sortedProviders := make([]providerOption, 0, len(dbProviders))
	for _, id := range dbProviders {
		name, ok := providerNames[id]
		if !ok {
			name = id
		}
		sortedProviders = append(sortedProviders, providerOption{ID: id, Name: name})
	}
	sort.Slice(sortedProviders, func(i, j int) bool {
		return sortedProviders[i].Name < sortedProviders[j].Name
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models":    models,
		"providers": sortedProviders,
		"clients":   clients,
	})
}
