package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// Client ID matches pi's ChatGPT/Codex OAuth integration
	codexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAuthURL      = "https://auth.openai.com/oauth/authorize"
	codexTokenURL     = "https://auth.openai.com/oauth/token"
	codexDeviceURL    = "https://auth.openai.com/oauth/device/authorize"
	codexScope        = "openid profile email offline_access"
	codexAPIBase      = "https://api.openai.com"
	codexChatGPTBase  = "https://chatgpt.com"
	codexRedirectURI  = "http://localhost:1455/auth/callback"
	codexRedirectPort = 1455
)

// startCodexOAuth initiates the PKCE OAuth flow for OpenAI Codex
func startCodexOAuth() (string, error) {
	pkce, err := generatePKCE()
	if err != nil {
		return "", fmt.Errorf("failed to generate PKCE: %w", err)
	}

	redirectURI := codexRedirectURI

	oauthFlowsMu.Lock()
	// Clean up any expired flows
	for state, flow := range oauthFlows {
		if time.Since(flow.CreatedAt) > 10*time.Minute {
			delete(oauthFlows, state)
		}
	}
	oauthFlows[pkce.State] = &pendingOAuth{
		Provider:    "codex",
		PKCE:        pkce,
		RedirectURI: redirectURI,
		Port:        codexRedirectPort,
		CreatedAt:   time.Now(),
	}
	oauthFlowsMu.Unlock()

	// Start the callback server on the fixed port
	server, err := startOAuthCallbackServer(codexRedirectPort)
	if err != nil {
		oauthFlowsMu.Lock()
		delete(oauthFlows, pkce.State)
		oauthFlowsMu.Unlock()
		return "", fmt.Errorf("failed to start callback server: %w", err)
	}

	// Build the authorization URL using proper URL encoding
	// Matches pi's implementation parameters
	authURLParams := url.Values{}
	authURLParams.Set("response_type", "code")
	authURLParams.Set("client_id", codexClientID)
	authURLParams.Set("redirect_uri", redirectURI)
	authURLParams.Set("scope", codexScope)
	authURLParams.Set("code_challenge", pkce.Challenge)
	authURLParams.Set("code_challenge_method", "S256")
	authURLParams.Set("state", pkce.State)
	authURLParams.Set("id_token_add_organizations", "true")
	authURLParams.Set("codex_cli_simplified_flow", "true")
	authURLParams.Set("originator", "prism")

	authURL := codexAuthURL + "?" + authURLParams.Encode()
	log.Printf("[OAuth] Opening browser for Codex login, state=%s", pkce.State)
	log.Printf("[OAuth] Auth URL: %s", authURL)

	// Open browser
	if err := openBrowser(authURL); err != nil {
		log.Printf("[OAuth] Failed to open browser: %v", err)
	}

	// Clean up the server after timeout
	go func() {
		time.Sleep(5 * time.Minute)
		log.Printf("[OAuth] Callback server timed out, shutting down")
		server.Close()
		oauthCallbackMu.Lock()
		if oauthCallbackServer == server {
			oauthCallbackServer = nil
		}
		oauthCallbackMu.Unlock()
		oauthFlowsMu.Lock()
		delete(oauthFlows, pkce.State)
		oauthFlowsMu.Unlock()
	}()

	return pkce.State, nil
}

// exchangeCodeForTokens exchanges the authorization code for tokens
func exchangeCodeForTokens(flow *pendingOAuth, code string) (*OAuthAccount, error) {
	log.Printf("[OAuth] Exchanging code for tokens, redirect_uri=%s", flow.RedirectURI)

	tokenReq := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     codexClientID,
		"code":          code,
		"redirect_uri":  flow.RedirectURI,
		"code_verifier": flow.PKCE.Verifier,
	}

	account := &OAuthAccount{
		ID:       generateAccountID("codex"),
		Provider: "codex",
		Label:    "Codex",
		Active:   false,
	}

	if err := exchangeToken(account, tokenReq); err != nil {
		log.Printf("[OAuth] Token exchange error: %v", err)
		return nil, err
	}

	log.Printf("[OAuth] Token exchange succeeded, got access_token (len=%d) refresh_token (len=%d)",
		len(account.AccessToken), len(account.RefreshToken))

	// Extract email and account ID from JWT access token
	extractAccountDetails(account)

	return account, nil
}

// extractAccountDetails extracts email and ChatGPT account ID from the JWT access token
func extractAccountDetails(account *OAuthAccount) {
	// Extract email from ID token
	if account.IDToken != "" {
		account.Email = parseIDTokenEmail(account.IDToken)
		log.Printf("[OAuth] Extracted email from ID token: %s", account.Email)
	}

	// Extract ChatGPT account ID from access token (like pi does)
	// The JWT claim path is "https://api.openai.com/auth" -> "chatgpt_account_id"
	if account.AccessToken != "" {
		accountID := parseChatGPTAccountID(account.AccessToken)
		if accountID != "" {
			log.Printf("[OAuth] Extracted ChatGPT account ID: %s", accountID)
			account.ChatGPTAccountID = accountID
		}
	}
}

// parseChatGPTAccountID extracts the chatgpt_account_id from a JWT access token
func parseChatGPTAccountID(accessToken string) string {
	parts := splitJWT(accessToken)
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
		AuthClaim struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}

	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	return claims.AuthClaim.ChatGPTAccountID
}

// exchangeToken performs a token exchange request using application/x-www-form-urlencoded
func exchangeToken(account *OAuthAccount, params map[string]string) error {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	log.Printf("[OAuth] Sending token request to %s", codexTokenURL)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("token request failed: %w", err)
	}

	body, err := readResponseBody(resp)
	if err != nil {
		return fmt.Errorf("failed to read token response: %w", err)
	}

	log.Printf("[OAuth] Token response status: %d, body length: %d", resp.StatusCode, len(body))

	if resp.StatusCode != http.StatusOK {
		bodyPreview := string(body)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		log.Printf("[OAuth] Token exchange error response: %s", bodyPreview)
		return fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}

	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("failed to parse token response: %w", err)
	}

	account.AccessToken = tokenResp.AccessToken
	account.RefreshToken = tokenResp.RefreshToken
	account.IDToken = tokenResp.IDToken

	if tokenResp.ExpiresIn > 0 {
		account.ExpiresAt = time.Now().Unix() + int64(tokenResp.ExpiresIn)
	} else {
		account.ExpiresAt = time.Now().Unix() + 3600 // Default 1 hour
	}

	return nil
}

// findAvailablePort finds an available port in the given range
func findAvailablePort(start, end int) int {
	for port := start; port <= end; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return port
		}
	}
	return 0
}

// openBrowser opens a URL in the default browser
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		// Use rundll32 instead of cmd /c start to avoid cmd.exe mangling
		// special characters like & and % in the URL (which are common in OAuth URLs)
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// startCodexDeviceFlow initiates a device code flow (headless auth)
func startCodexDeviceFlow() (string, string, error) {
	form := url.Values{}
	form.Set("client_id", codexClientID)
	form.Set("scope", codexScope)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", codexDeviceURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("failed to create device auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("device auth request failed: %w", err)
	}

	body, err := readResponseBody(resp)
	if err != nil {
		return "", "", fmt.Errorf("failed to read device auth response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("device auth failed (status %d): %s", resp.StatusCode, string(body))
	}

	var deviceResp struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}

	if err := json.Unmarshal(body, &deviceResp); err != nil {
		return "", "", fmt.Errorf("failed to parse device auth response: %w", err)
	}

	return deviceResp.DeviceCode, deviceResp.UserCode, nil
}

// pollCodexDeviceToken polls for a device code token
func pollCodexDeviceToken(deviceCode string, interval time.Duration, timeout time.Duration) (*OAuthAccount, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		time.Sleep(interval)

		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("client_id", codexClientID)
		form.Set("device_code", deviceCode)

		client := &http.Client{Timeout: 30 * time.Second}
		req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, err := readResponseBody(resp)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var tokenResp struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				IDToken      string `json:"id_token"`
				TokenType    string `json:"token_type"`
				ExpiresIn    int    `json:"expires_in"`
			}

			if err := json.Unmarshal(body, &tokenResp); err != nil {
				return nil, fmt.Errorf("failed to parse token response: %w", err)
			}

			account := &OAuthAccount{
				ID:           generateAccountID("codex"),
				Provider:     "codex",
				Label:        "Codex",
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				IDToken:      tokenResp.IDToken,
				Active:       false,
			}

			if tokenResp.ExpiresIn > 0 {
				account.ExpiresAt = time.Now().Unix() + int64(tokenResp.ExpiresIn)
			} else {
				account.ExpiresAt = time.Now().Unix() + 3600
			}

			extractAccountDetails(account)

			return account, nil
		}

		// Check for slow_down or pending
		var errResp struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &errResp)

		if errResp.Error == "slow_down" {
			interval += 5 * time.Second
		} else if errResp.Error != "authorization_pending" {
			return nil, fmt.Errorf("device auth failed: %s", errResp.Error)
		}
	}

	return nil, fmt.Errorf("device auth timed out")
}

// parsePort extracts a port number from a string
func parsePort(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}