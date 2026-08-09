package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/oauth"
)

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

	state, err := oauth.StartCodexOAuth()
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

	cfg := config.Current()

	// Return accounts with masked tokens
	type safeAccount struct {
		ID               string  `json:"id"`
		Provider         string  `json:"provider"`
		Label            string  `json:"label"`
		Email            string  `json:"email"`
		PlanTier         string  `json:"plan_tier"`
		Active           bool    `json:"active"`
		TokenExpiry      string  `json:"token_expiry"`
		TokenValid       bool    `json:"token_valid"`
		CreditsUsed      float64 `json:"credits_used"`
		CreditsTotal     float64 `json:"credits_total"`
		CreditsRemaining float64 `json:"credits_remaining"`
		PercentUsed      float64 `json:"percent_used"`
		WeeklyResetAt    int64   `json:"weekly_reset_at"`
		LastUsageCheck   int64   `json:"last_usage_check"`
		UsageUnavailable bool    `json:"usage_unavailable"`
		SessionPercent   float64 `json:"session_percent"`
		SessionResetAt   int64   `json:"session_reset_at"`
		WeeklyPercent    float64 `json:"weekly_percent"`
		ReviewPercent    float64 `json:"review_percent"`
		CreditsBalance   float64 `json:"credits_balance"`
		RateLimitResets  int     `json:"rate_limit_resets"`
	}

	accounts := make([]safeAccount, 0, len(cfg.OAuthAccounts))
	for _, a := range cfg.OAuthAccounts {
		tokenValid := !a.IsTokenExpired()
		tokenExpiry := ""
		if a.ExpiresAt > 0 {
			tokenExpiry = time.Unix(a.ExpiresAt, 0).Format("2006-01-02 15:04:05")
		}

		// Start with stored values
		sessionPercent := a.SessionPercent
		sessionResetAt := a.SessionResetAt
		weeklyPercent := a.WeeklyPercent
		weeklyResetAt := a.WeeklyResetAt
		reviewPercent := a.ReviewPercent
		creditsBalance := a.CreditsBalance
		rateLimitResets := a.RateLimitResets
		percentUsed := a.SessionPercent
		usageUnavailable := false

		// Override with cached usage if newer
		usage := oauth.GetCachedUsage(a.ID)
		if usage != nil && usage.LastUpdated > a.LastUsageCheck {
			sessionPercent = usage.SessionPercent
			sessionResetAt = usage.SessionResetAt
			weeklyPercent = usage.WeeklyPercent
			weeklyResetAt = usage.WeeklyResetAt
			reviewPercent = usage.ReviewPercent
			creditsBalance = usage.CreditsBalance
			rateLimitResets = usage.RateLimitResets
			percentUsed = usage.PercentUsed
			if usage.Error == "usage_unavailable" {
				usageUnavailable = true
			}
		} else if usage != nil && usage.Error == "usage_unavailable" {
			usageUnavailable = true
		}

		// Default plan tier to provider name if empty, try JWT fallback
		planTier := a.PlanTier
		if planTier == "" && a.AccessToken != "" {
			planTier = oauth.ParseJWTPlanTier(a.AccessToken)
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
			PercentUsed:      percentUsed,
			WeeklyResetAt:    weeklyResetAt,
			LastUsageCheck:   a.LastUsageCheck,
			UsageUnavailable: usageUnavailable,
			SessionPercent:   sessionPercent,
			SessionResetAt:   sessionResetAt,
			WeeklyPercent:    weeklyPercent,
			ReviewPercent:    reviewPercent,
			CreditsBalance:   creditsBalance,
			RateLimitResets:  rateLimitResets,
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

	if err := oauth.RemoveOAuthAccount(req.ID); err != nil {
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

	if err := oauth.SetActiveOAuthAccount(req.ID); err != nil {
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

	usage := oauth.GetCachedUsage(accountID)
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

	cfg := config.Current()

	var account *config.OAuthAccount
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

	usage, err := oauth.RefreshUsageForAccount(account)
	if err != nil {
		writeJSONError(w, "usage refresh failed: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}
