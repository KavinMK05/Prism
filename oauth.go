package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// OAuthAccount stores a connected OAuth provider account
type OAuthAccount struct {
	ID                  string  `json:"id"`
	Provider            string  `json:"provider"` // "codex"
	Label               string  `json:"label"`
	Email               string  `json:"email"`
	ChatGPTAccountID    string  `json:"chatgpt_account_id,omitempty"`
	AccessToken         string  `json:"access_token"`
	RefreshToken        string  `json:"refresh_token"`
	IDToken             string  `json:"id_token,omitempty"`
	ExpiresAt           int64   `json:"expires_at"`
	PlanTier            string  `json:"plan_tier"`
	Active              bool    `json:"active"`
	CreditsUsed         float64 `json:"credits_used"`
	CreditsTotal        float64 `json:"credits_total"`
	CreditsRemaining    float64 `json:"credits_remaining"`
	WeeklyResetAt       int64   `json:"weekly_reset_at"`
	LastUsageCheck      int64   `json:"last_usage_check"`
}

// IsTokenExpired returns true if the access token has expired
func (a *OAuthAccount) IsTokenExpired() bool {
	if a.ExpiresAt == 0 {
		return true
	}
	return time.Now().Unix() > a.ExpiresAt-60 // 60s buffer
}

// PKCE codes for OAuth flow
type pkceCodes struct {
	Verifier  string
	Challenge string
	State     string
}

// generatePKCE creates a PKCE code verifier and challenge pair
func generatePKCE() (*pkceCodes, error) {
	verifier := make([]byte, 32)
	if _, err := rand.Read(verifier); err != nil {
		return nil, fmt.Errorf("failed to generate verifier: %w", err)
	}
	verifierStr := base64.RawURLEncoding.EncodeToString(verifier)

	h := sha256.New()
	h.Write([]byte(verifierStr))
	challenge := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	state := make([]byte, 16)
	if _, err := rand.Read(state); err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}
	stateStr := base64.RawURLEncoding.EncodeToString(state)

	return &pkceCodes{
		Verifier:  verifierStr,
		Challenge: challenge,
		State:     stateStr,
	}, nil
}

// pendingOAuth tracks an in-progress OAuth flow
type pendingOAuth struct {
	Provider    string
	PKCE        *pkceCodes
	RedirectURI string
	Port        int
	CreatedAt   time.Time
}

var (
	oauthFlows   = make(map[string]*pendingOAuth) // state -> flow
	oauthFlowsMu sync.Mutex

	// Track the active OAuth callback server so we can shut it down before starting a new one
	oauthCallbackServer *http.Server
	oauthCallbackMu      sync.Mutex
)

func renderOAuthPage(w http.ResponseWriter, title, message, status string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	statusColor := "var(--success)"
	statusIcon := "✓"
	if status == "error" {
		statusColor = "var(--danger)"
		statusIcon = "✕"
	}

	// Auto-close script for success
	autoCloseScript := ""
	if status == "success" {
		autoCloseScript = `<script>
			setTimeout(function() { window.close(); }, 2500);
		</script>`
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s · Prism</title>
<style>
	:root {
		--bg: #ffffff;
		--surface: #ffffff;
		--border: #e5e5e5;
		--text: #171717;
		--text-secondary: #737373;
		--accent: #171717;
		--accent-text: #ffffff;
		--success: #22c55e;
		--danger: #ef4444;
		--radius-lg: 12px;
		--radius-md: 8px;
		--shadow: 0 1px 2px rgba(0,0,0,0.04);
	}
	@media (prefers-color-scheme: dark) {
		:root {
			--bg: #0a0a0a;
			--surface: #171717;
			--border: #262626;
			--text: #fafafa;
			--text-secondary: #a3a3a3;
			--accent: #fafafa;
			--accent-text: #0a0a0a;
			--success: #4ade80;
			--danger: #f87171;
			--shadow: 0 1px 2px rgba(0,0,0,0.2);
		}
	}
	* { margin: 0; padding: 0; box-sizing: border-box; }
	body {
		font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
		background: var(--bg);
		color: var(--text);
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 24px;
		-webkit-font-smoothing: antialiased;
	}
	.card {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-lg);
		padding: 40px 32px;
		max-width: 420px;
		width: 100%%;
		text-align: center;
		box-shadow: var(--shadow);
	}
	.brand {
		display: inline-flex;
		align-items: center;
		gap: 10px;
		margin-bottom: 28px;
		font-size: 15px;
		font-weight: 600;
		color: var(--text);
	}
	.brand-icon {
		width: 32px;
		height: 32px;
		border-radius: var(--radius-md);
		background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 16px;
		color: #fff;
		font-weight: 700;
	}
	.status-icon {
		width: 56px;
		height: 56px;
		border-radius: 50%%;
		background: %s;
		display: flex;
		align-items: center;
		justify-content: center;
		margin: 0 auto 20px;
		font-size: 26px;
		color: #fff;
		font-weight: 600;
		box-shadow: 0 0 0 8px rgba(128,128,128,0.08);
	}
	.card h1 {
		font-size: 18px;
		font-weight: 700;
		letter-spacing: -0.2px;
		margin-bottom: 10px;
	}
	.card p {
		font-size: 14px;
		color: var(--text-secondary);
		line-height: 1.6;
		margin-bottom: 4px;
	}
	.close-hint {
		font-size: 12px;
		color: var(--text-secondary);
		margin-top: 20px;
		opacity: 0.7;
	}
</style>
</head>
<body>
	<div class="card">
		<div class="brand"><div class="brand-icon">P</div> Prism</div>
		<div class="status-icon">%s</div>
		<h1>%s</h1>
		<p>%s</p>
		<div class="close-hint">You can close this window.</div>
	</div>
	%s
</body>
</html>`, title, statusColor, statusIcon, title, message, autoCloseScript)

	fmt.Fprint(w, html)
}

// startOAuthCallbackServer starts a temporary local HTTP server to receive the OAuth callback
func startOAuthCallbackServer(port int) (*http.Server, error) {
	// Shut down any existing callback server first (e.g. from a previous failed attempt)
	oauthCallbackMu.Lock()
	if oauthCallbackServer != nil {
		old := oauthCallbackServer
		oauthCallbackServer = nil
		oauthCallbackMu.Unlock()
		// Use a short context deadline for graceful shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		old.Shutdown(ctx)
		cancel()
	} else {
		oauthCallbackMu.Unlock()
	}

	mux := http.NewServeMux()
	// Match the Codex CLI redirect URI path: /auth/callback
	mux.HandleFunc("/auth/callback", handleOAuthCallback)
	// Also handle /callback for backwards compatibility
	mux.HandleFunc("/callback", handleOAuthCallback)

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}

	// Track the server
	oauthCallbackMu.Lock()
	oauthCallbackServer = server
	oauthCallbackMu.Unlock()

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[OAuth] Callback server error: %v", err)
		}
	}()

	return server, nil
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	log.Printf("[OAuth] Received callback request: %s %s", r.Method, r.URL.String())

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	errorParam := r.URL.Query().Get("error")

	if errorParam != "" {
		errorDesc := r.URL.Query().Get("error_description")
		log.Printf("[OAuth] Callback received error: %s - %s", errorParam, errorDesc)
		w.WriteHeader(400)
		renderOAuthPage(w, "Authentication Failed", errorParam+": "+errorDesc, "error")
		return
	}

	if code == "" {
		log.Printf("[OAuth] Callback missing authorization code")
		w.WriteHeader(400)
		renderOAuthPage(w, "Missing Authorization Code", "No authorization code was received. Please try the authentication flow again.", "error")
		return
	}

	log.Printf("[OAuth] Callback received code, state=%s", state)

	oauthFlowsMu.Lock()
	flow, ok := oauthFlows[state]
	if !ok {
		log.Printf("[OAuth] No matching OAuth flow for state=%s (available flows: %d)", state, len(oauthFlows))
		oauthFlowsMu.Unlock()
		w.WriteHeader(400)
		renderOAuthPage(w, "Invalid or Expired Link", "This sign-in link has expired or is invalid. Please return to Prism Settings and try again.", "error")
		return
	}
	delete(oauthFlows, state)
	oauthFlowsMu.Unlock()

	log.Printf("[OAuth] Exchanging authorization code for tokens...")

	// Exchange the code for tokens
	account, err := exchangeCodeForTokens(flow, code)
	if err != nil {
		log.Printf("[OAuth] Token exchange failed: %v", err)
		w.WriteHeader(500)
		renderOAuthPage(w, "Token Exchange Failed", err.Error(), "error")
		return
	}

	log.Printf("[OAuth] Token exchange successful, account=%s email=%s", account.ID, account.Email)

	// Save to config
	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	cfg.OAuthAccounts = append(cfg.OAuthAccounts, account)
	if err := saveConfig(cfg); err != nil {
		log.Printf("[OAuth] Failed to save config: %v", err)
		w.WriteHeader(500)
		renderOAuthPage(w, "Failed to Save Account", "Could not save the connected account: "+err.Error(), "error")
		return
	}

	log.Printf("[OAuth] Account saved successfully")

	// Trigger usage check in background
	go refreshUsageForAccount(account)

	// Notify tray
	notifyTrayConfigChanged()

	w.WriteHeader(200)
	renderOAuthPage(w, "Account Connected", "Your Codex account has been successfully added to Prism.", "success")
}

// removeOAuthAccount removes an OAuth account by ID
func removeOAuthAccount(id string) error {
	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	found := false
	newAccounts := []*OAuthAccount{}
	for _, a := range cfg.OAuthAccounts {
		if a.ID == id {
			found = true
			continue
		}
		newAccounts = append(newAccounts, a)
	}

	if !found {
		return fmt.Errorf("account not found: %s", id)
	}

	cfg.OAuthAccounts = newAccounts
	if err := saveConfig(cfg); err != nil {
		return err
	}

	notifyTrayConfigChanged()
	return nil
}

// setActiveOAuthAccount sets an OAuth account as the active provider
func setActiveOAuthAccount(id string) error {
	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	found := false
	for _, a := range cfg.OAuthAccounts {
		if a.ID == id {
			a.Active = true
			found = true
		} else {
			a.Active = false
		}
	}

	if !found {
		return fmt.Errorf("account not found: %s", id)
	}

	// Set active provider to the codex_ prefix
	cfg.ActiveProvider = id

	if err := saveConfig(cfg); err != nil {
		return err
	}

	notifyTrayConfigChanged()
	return nil
}

// getActiveOAuthAccount returns the currently active OAuth account, or nil
func getActiveOAuthAccount() *OAuthAccount {
	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	return getActiveOAuthAccountForConfig(cfg)
}

// getActiveOAuthAccountForConfig returns the active OAuth account from a given config
// based on the ActiveProvider field, which is the canonical source of truth.
func getActiveOAuthAccountForConfig(cfg *Config) *OAuthAccount {
	for _, a := range cfg.OAuthAccounts {
		if a.ID == cfg.ActiveProvider {
			return a
		}
	}
	return nil
}

// refreshAccessToken attempts to refresh the access token for an account
func refreshAccessToken(account *OAuthAccount) error {
	if account.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	tokenReq := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": account.RefreshToken,
		"client_id":     codexClientID,
	}

	return exchangeToken(account, tokenReq)
}

// getValidAccessToken returns a valid access token, refreshing if needed
func getValidAccessToken(account *OAuthAccount) (string, error) {
	if !account.IsTokenExpired() {
		return account.AccessToken, nil
	}

	if err := refreshAccessToken(account); err != nil {
		return "", fmt.Errorf("token refresh failed: %w", err)
	}

	// Save updated token
	adminMu.Lock()
	cfg := adminConfig
	adminMu.Unlock()

	for _, a := range cfg.OAuthAccounts {
		if a.ID == account.ID {
			a.AccessToken = account.AccessToken
			a.RefreshToken = account.RefreshToken
			a.ExpiresAt = account.ExpiresAt
		}
	}
	saveConfig(cfg)

	return account.AccessToken, nil
}

// parseIDToken extracts email from a JWT ID token (without full verification)
func parseIDTokenEmail(idToken string) string {
	parts := splitJWT(idToken)
	if len(parts) < 2 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Try standard base64
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}

	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	return claims.Email
}

func splitJWT(token string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	return parts
}

// generateAccountID creates a unique ID for an OAuth account
func generateAccountID(provider string) string {
	b := make([]byte, 4)
	rand.Read(b)
	short := base64.RawURLEncoding.EncodeToString(b)
	return provider + "_" + short
}

// readResponseBody reads and returns the response body
func readResponseBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
