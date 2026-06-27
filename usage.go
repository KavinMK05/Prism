package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// UsageInfo represents usage data from the ChatGPT backend API
type UsageInfo struct {
	Email            string  `json:"email"`
	PlanTier         string  `json:"plan_tier"`
	CreditsUsed      float64 `json:"credits_used"`
	CreditsTotal     float64 `json:"credits_total"`
	CreditsRemaining float64 `json:"credits_remaining"`
	PercentUsed      float64 `json:"percent_used"`
	WeeklyResetAt    int64   `json:"weekly_reset_at"`
	LastUpdated      int64   `json:"last_updated"`
	Error            string  `json:"error,omitempty"`
	SessionPercent   float64 `json:"session_percent,omitempty"`
	SessionResetAt   int64   `json:"session_reset_at,omitempty"`
	WeeklyPercent    float64 `json:"weekly_percent,omitempty"`
	ReviewPercent    float64 `json:"review_percent,omitempty"`
	CreditsBalance   float64 `json:"credits_balance,omitempty"`
	RateLimitResets  int     `json:"rate_limit_resets,omitempty"`
}

var (
	usageMu         sync.Mutex
	usageCache      = make(map[string]*UsageInfo) // account ID -> usage
	usageRefreshing = make(map[string]bool)       // account ID -> currently refreshing
)

// refreshUsageForAccount fetches usage data for a single account
func refreshUsageForAccount(account *OAuthAccount) (*UsageInfo, error) {
	usageMu.Lock()
	if usageRefreshing[account.ID] {
		usageMu.Unlock()
		return usageCache[account.ID], nil
	}
	usageRefreshing[account.ID] = true
	usageMu.Unlock()

	defer func() {
		usageMu.Lock()
		delete(usageRefreshing, account.ID)
		usageMu.Unlock()
	}()

	token, err := getValidAccessToken(account)
	if err != nil {
		return nil, fmt.Errorf("failed to get valid access token: %w", err)
	}

	// Extract chatgpt-account-id (re-extract from JWT if not stored)
	accountID := account.ChatGPTAccountID
	if accountID == "" && token != "" {
		accountID = parseChatGPTAccountID(token)
	}

	info := &UsageInfo{
		Email:       account.Email,
		LastUpdated: time.Now().Unix(),
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Fetch usage from /backend-api/wham/usage (same endpoint as openusage)
	usage, err := fetchCodexUsage(client, token, accountID)
	if err != nil {
		log.Printf("[Usage] Failed to fetch usage for %s: %v", account.Email, err)
		if isForbidden(err) {
			info.Error = "usage_unavailable"
		} else {
			info.Error = fmt.Sprintf("Failed to fetch usage: %v", err)
		}
	} else {
		info.PlanTier = usage.PlanTier
		info.SessionPercent = usage.SessionPercent
		info.SessionResetAt = usage.SessionResetAt
		info.WeeklyPercent = usage.WeeklyPercent
		info.WeeklyResetAt = usage.WeeklyResetAt
		info.ReviewPercent = usage.ReviewPercent
		info.CreditsBalance = usage.CreditsBalance
		info.RateLimitResets = usage.RateLimitResets
		// Set PercentUsed to session percent for backwards compat with UI
		info.PercentUsed = usage.SessionPercent
		// If no plan tier from usage API, try JWT
		if info.PlanTier == "" {
			info.PlanTier = parseJWTPlanTier(token)
		}
	}

	// Update cache
	usageMu.Lock()
	usageCache[account.ID] = info
	usageMu.Unlock()

	// Update account in config
	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	for _, a := range cfg.OAuthAccounts {
		if a.ID == account.ID {
			a.PlanTier = info.PlanTier
			a.SessionPercent = info.SessionPercent
			a.SessionResetAt = info.SessionResetAt
			a.WeeklyPercent = info.WeeklyPercent
			a.WeeklyResetAt = info.WeeklyResetAt
			a.ReviewPercent = info.ReviewPercent
			a.CreditsBalance = info.CreditsBalance
			a.RateLimitResets = info.RateLimitResets
			a.PercentUsed = info.PercentUsed
			a.LastUsageCheck = info.LastUpdated
		}
	}
	saveConfig(cfg)

	return info, nil
}

// isForbidden checks if an error is a 403 Forbidden response
func isForbidden(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return http.StatusForbidden == 403 && (containsStr(errStr, "403") || containsStr(errStr, "Forbidden"))
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// parseJWTPlanTier extracts plan tier from a JWT access token's claims
func parseJWTPlanTier(accessToken string) string {
	parts := splitJWT(accessToken)
	if len(parts) < 2 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	// Try various claim paths that OpenAI might use
	// Path 1: https://api.openai.com/auth -> chatgpt_plan_type
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if plan, ok := auth["chatgpt_plan_type"].(string); ok && plan != "" {
			return plan
		}
		if plan, ok := auth["subscription_plan"].(string); ok && plan != "" {
			return plan
		}
	}

	// Path 2: https://api.openai.com/profile -> plan_type
	if profile, ok := claims["https://api.openai.com/profile"].(map[string]interface{}); ok {
		if plan, ok := profile["plan_type"].(string); ok && plan != "" {
			return plan
		}
		if plan, ok := profile["subscription_plan"].(string); ok && plan != "" {
			return plan
		}
	}

	// Path 3: top-level plan_type
	if plan, ok := claims["plan_type"].(string); ok && plan != "" {
		return plan
	}

	// Path 4: top-level subscription_plan
	if plan, ok := claims["subscription_plan"].(string); ok && plan != "" {
		return plan
	}

	// Path 5: scope contains plus/team indicators
	if scope, ok := claims["scope"].(string); ok {
		if containsStr(scope, "plus") {
			return "plus"
		}
		if containsStr(scope, "team") {
			return "team"
		}
	}

	return ""
}

// codexUsage holds parsed usage data from /backend-api/wham/usage
type codexUsage struct {
	PlanTier        string  `json:"plan_type"`
	SessionPercent  float64 `json:"session_percent"`
	SessionResetAt  int64   `json:"session_reset_at"`
	WeeklyPercent   float64 `json:"weekly_percent"`
	WeeklyResetAt   int64   `json:"weekly_reset_at"`
	ReviewPercent   float64 `json:"review_percent"`
	CreditsBalance  float64 `json:"credits_balance"`
	RateLimitResets int     `json:"rate_limit_resets"`
}

// fetchCodexUsage fetches usage data from /backend-api/wham/usage
// This is the same endpoint used by the openusage project.
// The User-Agent header is critical for bypassing Cloudflare protection.
func fetchCodexUsage(client *http.Client, accessToken string, accountID string) (*codexUsage, error) {
	req, err := http.NewRequest("GET", codexChatGPTBase+"/backend-api/wham/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpenUsage")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}

	usage := &codexUsage{}

	// Extract plan_type
	if planType, ok := raw["plan_type"].(string); ok {
		usage.PlanTier = planType
	}

	// Extract rate_limit windows
	if rateLimit, ok := raw["rate_limit"].(map[string]interface{}); ok {
		if primary, ok := rateLimit["primary_window"].(map[string]interface{}); ok {
			if pct, ok := primary["used_percent"].(float64); ok {
				usage.SessionPercent = pct
			}
			if resetAt, ok := primary["reset_at"].(float64); ok {
				usage.SessionResetAt = int64(resetAt)
			}
		}
		if secondary, ok := rateLimit["secondary_window"].(map[string]interface{}); ok {
			if pct, ok := secondary["used_percent"].(float64); ok {
				usage.WeeklyPercent = pct
			}
			if resetAt, ok := secondary["reset_at"].(float64); ok {
				usage.WeeklyResetAt = int64(resetAt)
			}
		}
	}

	// Extract code_review_rate_limit
	if reviewRL, ok := raw["code_review_rate_limit"].(map[string]interface{}); ok {
		if primary, ok := reviewRL["primary_window"].(map[string]interface{}); ok {
			if pct, ok := primary["used_percent"].(float64); ok {
				usage.ReviewPercent = pct
			}
		}
	}

	// Extract credits balance
	if credits, ok := raw["credits"].(map[string]interface{}); ok {
		if balance, ok := credits["balance"].(float64); ok {
			usage.CreditsBalance = balance
		}
		if hasCredits, ok := credits["has_credits"].(bool); ok && !hasCredits {
			usage.CreditsBalance = 0
		}
	}

	// Extract rate_limit_reset_credits
	if resetCredits, ok := raw["rate_limit_reset_credits"].(map[string]interface{}); ok {
		if count, ok := resetCredits["available_count"].(float64); ok {
			usage.RateLimitResets = int(count)
		}
	}

	log.Printf("[Usage] Parsed wham/usage: plan=%s session=%.0f%% weekly=%.0f%% review=%.0f%% credits=%.1f resets=%d",
		usage.PlanTier, usage.SessionPercent, usage.WeeklyPercent, usage.ReviewPercent, usage.CreditsBalance, usage.RateLimitResets)
	return usage, nil
}

// truncateForLog truncates a string for logging purposes
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// startUsageRefreshLoop starts a background goroutine that periodically refreshes usage
func startUsageRefreshLoop() {
	go func() {
		// Initial delay
		time.Sleep(10 * time.Second)

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			adminMu.Lock()
			cfg := adminConfig
			adminMu.Unlock()

			for _, account := range cfg.OAuthAccounts {
				if account.Provider == "codex" && account.AccessToken != "" {
					go func(a *OAuthAccount) {
						_, err := refreshUsageForAccount(a)
						if err != nil {
							log.Printf("[Usage] Failed to refresh usage for %s: %v", a.Email, err)
						}
					}(account)
				}
			}
		}
	}()
}

// getCachedUsage returns the cached usage info for an account
func getCachedUsage(accountID string) *UsageInfo {
	usageMu.Lock()
	defer usageMu.Unlock()
	return usageCache[accountID]
}
