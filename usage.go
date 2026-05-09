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

	info := &UsageInfo{
		Email:       account.Email,
		LastUpdated: time.Now().Unix(),
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Fetch account info from /backend-api/me
	meInfo, meErr := fetchCodexAccountInfo(client, token)
	if meErr != nil {
		log.Printf("[Usage] Failed to fetch account info from ChatGPT backend for %s: %v", account.Email, meErr)
		// 403 means we don't have access - try api.openai.com/v1/me as fallback
		if isForbidden(meErr) {
			info.Error = "usage_unavailable"
			// Fallback to api.openai.com
			apiMeInfo, apiErr := fetchOpenAIMe(client, token)
			if apiErr != nil {
				log.Printf("[Usage] Failed to fetch from api.openai.com for %s: %v", account.Email, apiErr)
			} else {
				if apiMeInfo.Email != "" {
					info.Email = apiMeInfo.Email
				}
				if apiMeInfo.PlanTier != "" {
					info.PlanTier = apiMeInfo.PlanTier
				}
			}
		} else {
			info.Error = fmt.Sprintf("Failed to fetch account info: %v", meErr)
		}
	} else {
		info.Email = meInfo.Email
		info.PlanTier = meInfo.PlanTier
	}

	// If plan tier is still empty, try to extract from JWT access token
	if info.PlanTier == "" && account.AccessToken != "" {
		info.PlanTier = parseJWTPlanTier(account.AccessToken)
	}

	// Fetch usage limits from /backend-api/accounts/check
	checkInfo, checkErr := fetchCodexAccountCheck(client, token)
	if checkErr != nil {
		log.Printf("[Usage] Failed to fetch usage check for %s: %v", account.Email, checkErr)
		if isForbidden(checkErr) {
			if info.Error == "" {
				info.Error = "usage_unavailable"
			}
		} else if info.Error == "" {
			info.Error = fmt.Sprintf("Failed to fetch usage: %v", checkErr)
		}
	} else {
		info.CreditsUsed = checkInfo.CreditsUsed
		info.CreditsTotal = checkInfo.CreditsTotal
		info.CreditsRemaining = checkInfo.CreditsRemaining
		info.PercentUsed = checkInfo.PercentUsed
		info.WeeklyResetAt = checkInfo.WeeklyResetAt
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
			a.CreditsUsed = info.CreditsUsed
			a.CreditsTotal = info.CreditsTotal
			a.CreditsRemaining = info.CreditsRemaining
			a.WeeklyResetAt = info.WeeklyResetAt
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

// codexAccountInfo holds account data from /backend-api/me
type codexAccountInfo struct {
	Email    string `json:"email"`
	PlanTier string `json:"plan_tier"`
	Name     string `json:"name"`
}

func fetchCodexAccountInfo(client *http.Client, accessToken string) (*codexAccountInfo, error) {
	req, err := http.NewRequest("GET", codexChatGPTBase+"/backend-api/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

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
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}

	info := &codexAccountInfo{}

	// Extract email from multiple possible locations
	if email, ok := raw["email"].(string); ok && email != "" {
		info.Email = email
	}
	if info.Email == "" {
		if user, ok := raw["user"].(map[string]interface{}); ok {
			if email, ok := user["email"].(string); ok {
				info.Email = email
			}
		}
	}

	// Extract plan tier from multiple possible paths
	// Path 1: top-level subscription_plan
	if planTier, ok := raw["subscription_plan"].(string); ok && planTier != "" {
		info.PlanTier = planTier
	}
	// Path 2: top-level plan_type
	if info.PlanTier == "" {
		if planType, ok := raw["plan_type"].(string); ok && planType != "" {
			info.PlanTier = planType
		}
	}
	// Path 3: top-level chatgpt_plan
	if info.PlanTier == "" {
		if chatPlan, ok := raw["chatgpt_plan"].(string); ok && chatPlan != "" {
			info.PlanTier = chatPlan
		}
	}
	// Path 4: accounts -> {account_id} -> account -> subscription_plan
	if info.PlanTier == "" {
		if accounts, ok := raw["accounts"].(map[string]interface{}); ok {
			for _, accVal := range accounts {
				if acc, ok := accVal.(map[string]interface{}); ok {
					if plan, ok := acc["subscription_plan"].(string); ok && plan != "" {
						info.PlanTier = plan
						break
					}
					if isPaid, ok := acc["is_paid_subscription_active"].(bool); ok && isPaid && info.PlanTier == "" {
						info.PlanTier = "plus"
					}
				}
			}
		}
	}
	// Path 5: accounts -> account (older structure)
	if info.PlanTier == "" {
		if accounts, ok := raw["accounts"].(map[string]interface{}); ok {
			if acc, ok := accounts["account"].(map[string]interface{}); ok {
				if plan, ok := acc["subscription_plan"].(string); ok && plan != "" {
					info.PlanTier = plan
				}
				if isPaid, ok := acc["is_paid_subscription_active"].(bool); ok && isPaid && info.PlanTier == "" {
					info.PlanTier = "plus"
				}
			}
		}
	}

	log.Printf("[Usage] Parsed account info: email=%s plan=%s", info.Email, info.PlanTier)
	return info, nil
}

// openAIMeInfo holds account data from api.openai.com/v1/me (fallback)
type openAIMeInfo struct {
	Email    string `json:"email"`
	PlanTier string `json:"plan_tier"`
	Name     string `json:"name"`
}

func fetchOpenAIMe(client *http.Client, accessToken string) (*openAIMeInfo, error) {
	req, err := http.NewRequest("GET", "https://api.openai.com/v1/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

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
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}

	info := &openAIMeInfo{}

	if email, ok := raw["email"].(string); ok && email != "" {
		info.Email = email
	}

	// The /v1/me endpoint doesn't return plan info directly, but we can check orgs
	if orgs, ok := raw["orgs"].(map[string]interface{}); ok {
		if data, ok := orgs["data"].([]interface{}); ok && len(data) > 0 {
			if org, ok := data[0].(map[string]interface{}); ok {
				// No direct plan field, but we can infer from settings if present
				if settings, ok := org["settings"].(map[string]interface{}); ok {
					if _, ok := settings["disable_user_api_keys"]; ok {
						// Just a marker that we have an org
					}
				}
			}
		}
	}

	log.Printf("[Usage] Parsed api.openai.com/v1/me: email=%s", info.Email)
	return info, nil
}

// codexAccountCheck holds usage data from /backend-api/accounts/check
type codexAccountCheck struct {
	CreditsUsed      float64 `json:"credits_used"`
	CreditsTotal     float64 `json:"credits_total"`
	CreditsRemaining float64 `json:"credits_remaining"`
	PercentUsed      float64 `json:"percent_used"`
	WeeklyResetAt    int64   `json:"weekly_reset_at"`
}

func fetchCodexAccountCheck(client *http.Client, accessToken string) (*codexAccountCheck, error) {
	req, err := http.NewRequest("GET", codexChatGPTBase+"/backend-api/accounts/check", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

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
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}

	check := &codexAccountCheck{}

	// Helper to extract rate limits from various nested structures
	extractRateLimits := func(m map[string]interface{}) {
		if limits, ok := m["rate_limits"].(map[string]interface{}); ok {
			if used, ok := limits["used"].(float64); ok {
				check.CreditsUsed = used
			}
			if total, ok := limits["total"].(float64); ok {
				check.CreditsTotal = total
			}
			if remaining, ok := limits["remaining"].(float64); ok {
				check.CreditsRemaining = remaining
			}
			if resetAt, ok := limits["resets_at"].(float64); ok {
				check.WeeklyResetAt = int64(resetAt)
			}
		}
	}

	// Path 1: accounts -> {id} -> features -> [{name: "codex", rate_limits: {...}}]
	if accounts, ok := raw["accounts"].(map[string]interface{}); ok {
		for _, accVal := range accounts {
			accMap, ok := accVal.(map[string]interface{})
			if !ok {
				continue
			}

			if features, ok := accMap["features"].([]interface{}); ok {
				for _, f := range features {
					fMap, ok := f.(map[string]interface{})
					if !ok {
						continue
					}
					if featureName, ok := fMap["name"].(string); ok {
						if featureName == "codex_rate_limit" || featureName == "codex" {
							extractRateLimits(fMap)
						}
					}
				}
			}

			// Path 1b: direct rate_limits on account
			if check.CreditsTotal == 0 {
				extractRateLimits(accMap)
			}
		}
	}

	// Path 2: top-level rate_limits
	if check.CreditsTotal == 0 {
		extractRateLimits(raw)
	}

	// Path 3: account_ordering -> first account id -> look in accounts map for rate_limits
	if check.CreditsTotal == 0 {
		if ordering, ok := raw["account_ordering"].([]interface{}); ok && len(ordering) > 0 {
			if firstID, ok := ordering[0].(string); ok {
				if accounts, ok := raw["accounts"].(map[string]interface{}); ok {
					if accVal, ok := accounts[firstID]; ok {
						if accMap, ok := accVal.(map[string]interface{}); ok {
							extractRateLimits(accMap)
							// Also try plan info
							if plan, ok := accMap["plan"].(map[string]interface{}); ok {
								if used, ok := plan["credits_used"].(float64); ok {
									check.CreditsUsed = used
								}
								if total, ok := plan["credits_total"].(float64); ok {
									check.CreditsTotal = total
								}
								if remaining, ok := plan["credits_remaining"].(float64); ok {
									check.CreditsRemaining = remaining
								}
							}
						}
					}
				}
			}
		}
	}

	// Path 4: plan at top level
	if check.CreditsTotal == 0 {
		if plan, ok := raw["plan"].(map[string]interface{}); ok {
			if used, ok := plan["credits_used"].(float64); ok {
				check.CreditsUsed = used
			}
			if total, ok := plan["credits_total"].(float64); ok {
				check.CreditsTotal = total
			}
			if remaining, ok := plan["credits_remaining"].(float64); ok {
				check.CreditsRemaining = remaining
			}
		}
	}

	// Path 5: usage object at top level
	if check.CreditsTotal == 0 {
		if usage, ok := raw["usage"].(map[string]interface{}); ok {
			if used, ok := usage["used"].(float64); ok {
				check.CreditsUsed = used
			}
			if total, ok := usage["total"].(float64); ok {
				check.CreditsTotal = total
			}
			if remaining, ok := usage["remaining"].(float64); ok {
				check.CreditsRemaining = remaining
			}
		}
	}

	// Calculate percentages
	if check.CreditsTotal > 0 {
		check.PercentUsed = (check.CreditsUsed / check.CreditsTotal) * 100
		if check.CreditsRemaining == 0 && check.CreditsUsed > 0 {
			check.CreditsRemaining = check.CreditsTotal - check.CreditsUsed
		}
	}

	log.Printf("[Usage] Parsed account check: used=%.1f total=%.1f remaining=%.1f pct=%.1f reset=%d",
		check.CreditsUsed, check.CreditsTotal, check.CreditsRemaining, check.PercentUsed, check.WeeklyResetAt)
	return check, nil
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
