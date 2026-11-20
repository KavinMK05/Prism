package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ollama-proxy/internal/config"
)

// rotatingAS is a small OAuth authorization server that behaves like Notion:
// every refresh retires the refresh token that was sent, and replaying an old
// one is invalid_grant.
type rotatingAS struct {
	*httptest.Server

	mu             sync.Mutex
	currentRefresh string
	attempts       int
	refreshes      int
	blockFirst     bool
	entered        chan struct{}
	release        chan struct{}
}

func newRotatingAS(t *testing.T, initialRefresh string) *rotatingAS {
	t.Helper()
	as := &rotatingAS{
		currentRefresh: initialRefresh,
		entered:        make(chan struct{}, 1),
		release:        make(chan struct{}),
	}
	as.Server = httptest.NewServer(http.HandlerFunc(as.handle))
	t.Cleanup(as.Server.Close)
	return as
}

func (a *rotatingAS) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case authorizationServerPath, openIDConfigurationPath:
		writeJSONResponse(w, map[string]interface{}{
			"issuer":                                a.URL,
			"authorization_endpoint":                a.URL + "/authorize",
			"token_endpoint":                        a.URL + "/token",
			"grant_types_supported":                 []string{"refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	case "/token":
		if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "refresh_token" {
			http.Error(w, "bad token request", http.StatusBadRequest)
			return
		}
		sent := r.PostForm.Get("refresh_token")
		a.mu.Lock()
		a.attempts++
		if sent != a.currentRefresh {
			a.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token was already rotated"}`))
			return
		}
		a.refreshes++
		n := a.refreshes
		a.currentRefresh = fmt.Sprintf("rt-%d", n)
		block := a.blockFirst && n == 1
		a.mu.Unlock()

		if block {
			select {
			case a.entered <- struct{}{}:
			default:
			}
			<-a.release
		}

		writeJSONResponse(w, map[string]interface{}{
			"access_token":  fmt.Sprintf("at-%d", n),
			"refresh_token": fmt.Sprintf("rt-%d", n),
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	default:
		http.NotFound(w, r)
	}
}

func (a *rotatingAS) counts() (attempts, refreshes int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.attempts, a.refreshes
}

// publishRefreshFixture saves and publishes one server whose access token has
// expired, so the next bearerToken call must refresh it.
func publishRefreshFixture(t *testing.T, asURL, accessToken, refreshToken string) {
	t.Helper()
	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.EnsureMCP()
	srv := oauthTestServer("https://mcp.example.test/mcp")
	srv.OAuth = &config.MCPOAuthToken{
		AccessToken:     accessToken,
		RefreshToken:    refreshToken,
		ExpiresAt:       time.Now().Add(-time.Hour).Unix(),
		ClientID:        "client-1",
		TokenAuthMethod: "none",
		ASURL:           asURL,
		Resource:        "https://mcp.example.test",
	}
	cfg.MCP.Servers = append(cfg.MCP.Servers, srv)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	config.Publish(cfg)
	t.Cleanup(func() { config.Publish(nil) })
}

// Parallel calls on one expired token must produce exactly one refresh. The
// other callers wait on the per-server lock and reuse the winner's token.
func TestOAuthRefreshIsSingleFlight(t *testing.T) {
	isolateMCPConfig(t)
	as := newRotatingAS(t, "rt-0")
	as.blockFirst = true
	publishRefreshFixture(t, as.URL, "at-0", "rt-0")

	mgr := NewManager(config.Current)
	t.Cleanup(mgr.Stop)

	const callers = 8
	start := make(chan struct{})
	type result struct {
		token string
		err   error
	}
	results := make(chan result, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			token, err := mgr.bearerToken(context.Background(), "fake")
			results <- result{token: token, err: err}
		}()
	}
	close(start)

	select {
	case <-as.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first refresh never reached the token endpoint")
	}
	// Let the other callers reach the per-server lock before the winner saves.
	time.Sleep(200 * time.Millisecond)
	close(as.release)

	for i := 0; i < callers; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("caller %d: %v", i, r.err)
		}
		if r.token != "at-1" {
			t.Errorf("caller %d token = %q, want at-1", i, r.token)
		}
	}
	attempts, refreshes := as.counts()
	if attempts != 1 || refreshes != 1 {
		t.Errorf("token endpoint attempts/refreshes = %d/%d, want 1/1", attempts, refreshes)
	}
	stored := config.Load().FindMCPServer("fake")
	if stored == nil || stored.OAuth == nil {
		t.Fatal("the refreshed token was not persisted")
	}
	if stored.OAuth.AccessToken != "at-1" || stored.OAuth.RefreshToken != "rt-1" {
		t.Errorf("stored token = %+v, want at-1/rt-1", stored.OAuth)
	}
}

// A refresh that loses a race must not clear the newer token another writer
// just saved. This is the invalid_grant path with a stale server snapshot.
func TestStaleRefreshCannotClearNewerToken(t *testing.T) {
	isolateMCPConfig(t)
	as := newRotatingAS(t, "rt-1")
	publishRefreshFixture(t, as.URL, "at-1", "rt-1")

	stale := oauthTestServer("https://mcp.example.test/mcp")
	stale.OAuth = &config.MCPOAuthToken{
		AccessToken:     "at-0",
		RefreshToken:    "rt-0",
		ExpiresAt:       time.Now().Add(-time.Hour).Unix(),
		ClientID:        "client-1",
		TokenAuthMethod: "none",
		ASURL:           as.URL,
		Resource:        "https://mcp.example.test",
	}

	mgr := NewManager(config.Current)
	t.Cleanup(mgr.Stop)
	token, err := mgr.refreshOAuthTokenLocked(context.Background(), stale)
	if err != nil {
		t.Fatalf("refreshOAuthTokenLocked: %v", err)
	}
	if token != "at-1" {
		t.Errorf("returned token = %q, want the live token at-1", token)
	}
	stored := config.Load().FindMCPServer("fake")
	if stored == nil || stored.OAuth == nil {
		t.Fatal("the newer credentials were cleared by a stale refresh")
	}
	if stored.OAuth.RefreshToken != "rt-1" {
		t.Errorf("refresh token = %q, want rt-1", stored.OAuth.RefreshToken)
	}
	if attempts, _ := as.counts(); attempts != 1 {
		t.Errorf("token endpoint attempts = %d, want 1", attempts)
	}
}

// A proactive refresh that gets invalid_grant is terminal even when the old
// access token has not quite expired: the grant is gone, so masking the error
// and replaying the old token would only loop.
func TestProactiveInvalidGrantIsTerminal(t *testing.T) {
	isolateMCPConfig(t)
	as := newRotatingAS(t, "rt-winner")

	now := time.Now().Unix()
	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.EnsureMCP()
	srv := oauthTestServer("https://mcp.example.test/mcp")
	srv.OAuth = &config.MCPOAuthToken{
		AccessToken:     "at-old",
		RefreshToken:    "rt-retired",
		ExpiresAt:       now + 240,
		IssuedAt:        now - 3360,
		ClientID:        "client-1",
		TokenAuthMethod: "none",
		ASURL:           as.URL,
		Resource:        "https://mcp.example.test",
	}
	cfg.MCP.Servers = append(cfg.MCP.Servers, srv)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	config.Publish(cfg)
	t.Cleanup(func() { config.Publish(nil) })

	mgr := NewManager(config.Current)
	t.Cleanup(mgr.Stop)
	_, err := mgr.bearerToken(context.Background(), "fake")
	var authErr *AuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v, want AuthRequiredError", err)
	}
	stored := config.Load().FindMCPServer("fake")
	if stored == nil || stored.OAuth != nil {
		t.Error("the rejected credentials were not cleared")
	}
}

// A 401 from the upstream must force one refresh and replay the request once.
func TestHTTPTransportRefreshesOn401AndRetriesOnce(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()
		if auth != "Bearer at-new" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="OAuth", error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	t.Cleanup(srv.Close)

	var tokenMu sync.Mutex
	token := "at-old"
	dyn := func(context.Context) (map[string]string, error) {
		tokenMu.Lock()
		defer tokenMu.Unlock()
		return map[string]string{"Authorization": "Bearer " + token}, nil
	}
	tr := newHTTPTransport(srv.URL, nil, dyn, nil, false)
	var refreshes int
	tr.setAuthRetry(func(_ context.Context, rejected string) (string, error) {
		if rejected != "at-old" {
			t.Errorf("rejected token = %q, want at-old", rejected)
		}
		refreshes++
		tokenMu.Lock()
		token = "at-new"
		tokenMu.Unlock()
		return "at-new", nil
	})

	raw, err := tr.RoundTrip(context.Background(), "tools/list", nil)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("response: %v", err)
	}
	if got["ok"] != true {
		t.Errorf("response = %s", raw)
	}
	if refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "Bearer at-old" || seen[1] != "Bearer at-new" {
		t.Errorf("authorization headers = %v, want old then new", seen)
	}
}
