package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ollama-proxy/internal/config"
)

// isolateMCPConfig points Prism's config dir at a temp directory so the OAuth
// tests can exercise config.Load/Save (which is where tokens are persisted)
// without touching the user's real config.json.
func isolateMCPConfig(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
}

// fakeAS is a minimal OAuth 2.1 authorization server: metadata, dynamic client
// registration, a token endpoint (authorization_code + refresh_token) and
// revocation.
type fakeAS struct {
	*httptest.Server

	mu             sync.Mutex
	registrations  int
	registerBodies []map[string]interface{}
	tokenForms     []url.Values
	tokenErr       string
	dcrClientID    string
	oidcOnly       bool
}

func newFakeAS(t *testing.T) *fakeAS {
	t.Helper()
	f := &fakeAS{dcrClientID: "dcr-client-1"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeAS) metadata() map[string]interface{} {
	return map[string]interface{}{
		"issuer":                                f.URL,
		"authorization_endpoint":                f.URL + "/authorize",
		"token_endpoint":                        f.URL + "/token",
		"registration_endpoint":                 f.URL + "/register",
		"revocation_endpoint":                   f.URL + "/revoke",
		"scopes_supported":                      []string{"read", "write"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256"},
		"client_id_metadata_document_supported": true,
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
	}
}

func (f *fakeAS) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server":
		if f.oidcOnly {
			http.NotFound(w, r)
			return
		}
		writeJSONResponse(w, f.metadata())
	case "/.well-known/openid-configuration":
		writeJSONResponse(w, f.metadata())
	case "/register":
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registrations++
		f.registerBodies = append(f.registerBodies, body)
		id := f.dcrClientID
		f.mu.Unlock()
		writeJSONResponse(w, map[string]interface{}{
			"client_id":                  id,
			"token_endpoint_auth_method": "none",
		})
	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.tokenForms = append(f.tokenForms, r.PostForm)
		mode := f.tokenErr
		f.mu.Unlock()
		if mode == "invalid_grant" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token expired"}`))
			return
		}
		if r.PostForm.Get("grant_type") == "refresh_token" {
			writeJSONResponse(w, map[string]interface{}{
				"access_token":  "at-refreshed",
				"refresh_token": "rt-refreshed",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"scope":         "read",
			})
			return
		}
		writeJSONResponse(w, map[string]interface{}{
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "read",
		})
	case "/revoke":
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAS) tokenFormsSnapshot() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.tokenForms...)
}

// newFakeResource builds the resource server: an MCP endpoint that answers 401
// with a WWW-Authenticate challenge, plus the RFC 9728 metadata document.
func newFakeResource(t *testing.T, asBase, prmPath string, serveWellKnown bool) (*httptest.Server, string) {
	t.Helper()
	handlers := map[string]http.HandlerFunc{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	base := ts.URL
	prm := func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, map[string]interface{}{
			"resource":                 base,
			"authorization_servers":    []string{asBase},
			"scopes_supported":         []string{"read"},
			"bearer_methods_supported": []string{"header"},
			"resource_name":            "Fake MCP",
		})
	}
	handlers["/mcp"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate",
			`Bearer realm="OAuth", resource_metadata="`+base+prmPath+`", error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
	}
	handlers[prmPath] = prm
	if serveWellKnown {
		handlers[protectedResourcePath] = prm
		handlers[protectedResourcePath+"/mcp"] = prm
	}
	t.Cleanup(ts.Close)
	return ts, base
}

func writeJSONResponse(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func oauthTestServer(urlStr string) *config.MCPServerConfig {
	return &config.MCPServerConfig{
		ID:        "fake",
		Name:      "Fake MCP",
		Transport: config.MCPTransportHTTP,
		URL:       urlStr,
		Enabled:   true,
		AuthMode:  config.MCPAuthOAuth,
	}
}

func TestDiscoverAuthFromProtectionMetadata(t *testing.T) {
	as := newFakeAS(t)
	_, resBase := newFakeResource(t, as.URL, protectedResourcePath+"/mcp", true)
	srv := oauthTestServer(resBase + "/mcp")

	got, err := DiscoverAuth(context.Background(), srv)
	if err != nil {
		t.Fatalf("DiscoverAuth: %v", err)
	}
	if got.Resource != resBase {
		t.Errorf("resource = %q, want %q", got.Resource, resBase)
	}
	if got.AuthorizationServer != as.URL {
		t.Errorf("authorization server = %q, want %q", got.AuthorizationServer, as.URL)
	}
	if got.Issuer != as.URL {
		t.Errorf("issuer = %q, want %q", got.Issuer, as.URL)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "read" {
		t.Errorf("scopes = %v", got.Scopes)
	}
	if got.RegistrationMode != "dcr" {
		t.Errorf("registration mode = %q, want dcr", got.RegistrationMode)
	}
	if !got.CIMDSupported {
		t.Error("CIMD should be reported as supported by this AS")
	}
	if got.ResourceName != "Fake MCP" {
		t.Errorf("resource name = %q", got.ResourceName)
	}
}

// The MCP spec requires clients to try both RFC 8414 and OpenID Connect
// discovery: Notion (and this fake) serves only one of them.
func TestDiscoverAuthFallsBackToOIDCDiscovery(t *testing.T) {
	as := newFakeAS(t)
	as.oidcOnly = true
	_, resBase := newFakeResource(t, as.URL, protectedResourcePath+"/mcp", true)

	got, err := DiscoverAuth(context.Background(), oauthTestServer(resBase+"/mcp"))
	if err != nil {
		t.Fatalf("DiscoverAuth with OIDC-only metadata: %v", err)
	}
	if got.AuthorizationServer != as.URL || got.Issuer != as.URL {
		t.Errorf("discovery result = %+v", got)
	}
}

// When the well-known document is missing entirely, the resource metadata URL
// from the WWW-Authenticate challenge is the one that matters.
func TestDiscoverAuthUsesChallengeMetadataURL(t *testing.T) {
	as := newFakeAS(t)
	_, resBase := newFakeResource(t, as.URL, "/oauth/metadata.json", false)

	got, err := DiscoverAuth(context.Background(), oauthTestServer(resBase+"/mcp"))
	if err != nil {
		t.Fatalf("DiscoverAuth: %v", err)
	}
	if got.Resource != resBase {
		t.Errorf("resource = %q, want %q", got.Resource, resBase)
	}
}

func TestChallengeParam(t *testing.T) {
	challenge := `Bearer realm="OAuth", resource_metadata="https://x.example/.well-known/oauth-protected-resource/mcp", error="invalid_token"`
	if got := challengeParam(challenge, "resource_metadata"); got != "https://x.example/.well-known/oauth-protected-resource/mcp" {
		t.Errorf("quoted param = %q", got)
	}
	if got := challengeParam(challenge, "realm"); got != "OAuth" {
		t.Errorf("realm = %q", got)
	}
	if got := challengeParam(`Bearer error=invalid_token, scope=read`, "scope"); got != "read" {
		t.Errorf("unquoted param = %q", got)
	}
	if got := challengeParam(challenge, "missing"); got != "" {
		t.Errorf("missing param = %q", got)
	}
}

func TestResolveClientRegistrationPriority(t *testing.T) {
	ctx := context.Background()
	as := newFakeAS(t)
	_, resBase := newFakeResource(t, as.URL, protectedResourcePath+"/mcp", true)

	asmd, err := discoverASMetadata(ctx, as.URL)
	if err != nil {
		t.Fatalf("discoverASMetadata: %v", err)
	}

	// 1. A user-supplied client_id wins and no registration happens.
	cfg := (&config.Config{}).Clone()
	cfg.EnsureMCP()
	srv := oauthTestServer(resBase + "/mcp")
	srv.OAuth = &config.MCPOAuthToken{ClientID: "pre-registered-1", ClientSecret: "shh", TokenAuthMethod: "client_secret_post"}
	before := registrationCount(as)
	reg, mode, err := resolveClientRegistration(ctx, cfg, srv, as.URL, asmd, "http://127.0.0.1:9/callback", []string{"read"})
	if err != nil {
		t.Fatalf("preregistered: %v", err)
	}
	if mode != "preregistered" || reg.ClientID != "pre-registered-1" {
		t.Errorf("preregistered result = %+v / %q", reg, mode)
	}
	if reg.TokenAuthMethod != "client_secret_post" {
		t.Errorf("auth method = %q, want the preregistered preference", reg.TokenAuthMethod)
	}
	if registrationCount(as) != before {
		t.Error("dynamic registration ran despite a preregistered client")
	}

	// 2. CIMD wins over DCR when the AS advertises it and the user opted in.
	cfg2 := (&config.Config{}).Clone()
	m2 := cfg2.EnsureMCP()
	m2.ClientIDMetadataURL = "https://client.example.com/prism.json"
	reg, mode, err = resolveClientRegistration(ctx, cfg2, oauthTestServer(resBase+"/mcp"), as.URL, asmd, "http://127.0.0.1:9/callback", nil)
	if err != nil {
		t.Fatalf("cimd: %v", err)
	}
	if mode != "cimd" || reg.ClientID != m2.ClientIDMetadataURL {
		t.Errorf("cimd result = %+v / %q", reg, mode)
	}

	// 3. CIMD is skipped when the AS does not advertise support.
	unsupported := *asmd
	unsupported.ClientIDMetadataDocumentSupported = false
	before = registrationCount(as)
	reg, mode, err = resolveClientRegistration(ctx, cfg2, oauthTestServer(resBase+"/mcp"), as.URL, &unsupported, "http://127.0.0.1:9/callback", nil)
	if err != nil {
		t.Fatalf("cimd unsupported: %v", err)
	}
	if mode != "dcr" {
		t.Errorf("mode = %q, want dcr when CIMD is unsupported", mode)
	}
	if registrationCount(as) != before+1 {
		t.Errorf("expected one dynamic registration, got %d", registrationCount(as)-before)
	}
	if reg.ClientID != as.dcrClientID {
		t.Errorf("client id = %q", reg.ClientID)
	}

	// 4. A cached registration (persisted by a previous login) is reused.
	cfg3 := (&config.Config{}).Clone()
	m3 := cfg3.EnsureMCP()
	m3.Clients[as.URL] = &config.MCPClientRegistration{ClientID: "cached-client-9", RegistrationMode: "dcr"}
	before = registrationCount(as)
	reg, mode, err = resolveClientRegistration(ctx, cfg3, oauthTestServer(resBase+"/mcp"), as.URL, asmd, "http://127.0.0.1:9/callback", nil)
	if err != nil {
		t.Fatalf("cached: %v", err)
	}
	if mode != "dcr" || reg.ClientID != "cached-client-9" {
		t.Errorf("cached result = %+v / %q", reg, mode)
	}
	if registrationCount(as) != before {
		t.Error("a cached client must not trigger a new registration")
	}

	// 5. With no registration endpoint and no client id, the user has to help.
	manual := *asmd
	manual.RegistrationEndpoint = ""
	if _, _, err := resolveClientRegistration(ctx, (&config.Config{}).Clone(), oauthTestServer(resBase+"/mcp"), as.URL, &manual, "http://127.0.0.1:9/callback", nil); err == nil {
		t.Error("expected an error when no registration path exists")
	} else if !strings.Contains(err.Error(), "client_id") {
		t.Errorf("error should mention client_id: %v", err)
	}
}

func registrationCount(f *fakeAS) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registrations
}

func TestOAuthLoginFlowEndToEnd(t *testing.T) {
	isolateMCPConfig(t)
	as := newFakeAS(t)
	_, resBase := newFakeResource(t, as.URL, protectedResourcePath+"/mcp", true)

	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.EnsureMCP()
	srv := oauthTestServer(resBase + "/mcp")
	cfg.MCP.Servers = append(cfg.MCP.Servers, srv)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	authURL, redirectURI, err := StartOAuthLogin(context.Background(), cfg, "fake")
	if err != nil {
		t.Fatalf("StartOAuthLogin: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("authorize URL: %v", err)
	}
	q := parsed.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if q.Get("resource") != resBase {
		t.Errorf("resource = %q, want %q (RFC 8707)", q.Get("resource"), resBase)
	}
	if q.Get("scope") != "read" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
	if q.Get("client_id") != as.dcrClientID {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if !strings.HasPrefix(redirectURI, "http://127.0.0.1:") || !strings.HasSuffix(redirectURI, "/callback") {
		t.Errorf("redirect URI = %q, want a loopback callback", redirectURI)
	}

	// The user comes back from the browser with a code.
	resp, err := http.Get(redirectURI + "?code=test-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Connected") {
		t.Errorf("callback page did not report success: %s", body)
	}

	// The exchange used PKCE and the discovered client.
	forms := as.tokenFormsSnapshot()
	if len(forms) != 1 {
		t.Fatalf("token endpoint saw %d requests, want 1", len(forms))
	}
	form := forms[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "test-code" {
		t.Errorf("token form = %v", form)
	}
	if form.Get("client_id") != as.dcrClientID {
		t.Errorf("token client_id = %q", form.Get("client_id"))
	}
	if form.Get("redirect_uri") != redirectURI {
		t.Errorf("token redirect_uri = %q, want %q", form.Get("redirect_uri"), redirectURI)
	}
	if form.Get("resource") != resBase {
		t.Errorf("token resource = %q", form.Get("resource"))
	}
	verifier := form.Get("code_verifier")
	if verifier == "" {
		t.Fatal("no PKCE code_verifier was sent")
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); q.Get("code_challenge") != want {
		t.Error("code_challenge is not the S256 hash of the code_verifier")
	}

	// The token and the client registration are persisted for later calls.
	loaded := config.Load()
	stored := loaded.FindMCPServer("fake")
	if stored == nil || stored.OAuth == nil {
		t.Fatal("token was not persisted")
	}
	if stored.OAuth.AccessToken != "at-1" || stored.OAuth.RefreshToken != "rt-1" {
		t.Errorf("stored token = %+v", stored.OAuth)
	}
	if stored.OAuth.ASURL != as.URL {
		t.Errorf("stored AS URL = %q", stored.OAuth.ASURL)
	}
	if stored.OAuth.RegistrationMode != "dcr" {
		t.Errorf("registration mode = %q", stored.OAuth.RegistrationMode)
	}
	if reg := loaded.MCP.Clients[as.URL]; reg == nil || reg.ClientID != as.dcrClientID {
		t.Errorf("client registration was not cached: %+v", reg)
	}
}

func TestOAuthCallbackRejectsBadStateAndIssuer(t *testing.T) {
	isolateMCPConfig(t)
	as := newFakeAS(t)
	_, resBase := newFakeResource(t, as.URL, protectedResourcePath+"/mcp", true)

	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.EnsureMCP()
	cfg.MCP.Servers = append(cfg.MCP.Servers, oauthTestServer(resBase+"/mcp"))
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	authURL, redirectURI, err := StartOAuthLogin(context.Background(), cfg, "fake")
	if err != nil {
		t.Fatalf("StartOAuthLogin: %v", err)
	}
	state := mustQueryParam(t, authURL, "state")

	// Wrong state: the link is rejected before any token exchange.
	resp, err := http.Get(redirectURI + "?code=test-code&state=not-the-state")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "expired") {
		t.Errorf("bad state: status %d body %s", resp.StatusCode, body)
	}

	// Correct state but an issuer that does not match the discovered one.
	resp, err = http.Get(redirectURI + "?code=test-code&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape("https://evil.example"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "Issuer mismatch") {
		t.Errorf("iss mismatch: status %d body %s", resp.StatusCode, body)
	}

	// The flow is still alive, so the right response completes it.
	resp, err = http.Get(redirectURI + "?code=test-code&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(as.URL))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("matching issuer status = %d", resp.StatusCode)
	}
	if loaded := config.Load(); loaded.FindMCPServer("fake").OAuth == nil {
		t.Error("token was not stored after a valid callback")
	}
}

func TestOAuthRefreshAndInvalidGrant(t *testing.T) {
	isolateMCPConfig(t)
	as := newFakeAS(t)
	_, resBase := newFakeResource(t, as.URL, protectedResourcePath+"/mcp", true)

	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.EnsureMCP()
	srv := oauthTestServer(resBase + "/mcp")
	srv.OAuth = &config.MCPOAuthToken{
		AccessToken:     "at-expired",
		RefreshToken:    "rt-1",
		ExpiresAt:       time.Now().Add(-time.Hour).Unix(),
		ClientID:        as.dcrClientID,
		TokenAuthMethod: "none",
		ASURL:           as.URL,
		Resource:        resBase,
	}
	cfg.MCP.Servers = append(cfg.MCP.Servers, srv)
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(func() *config.Config { return cfg })
	t.Cleanup(mgr.Stop)
	ctx := context.Background()

	token, err := mgr.bearerToken(ctx, "fake")
	if err != nil {
		t.Fatalf("bearerToken: %v", err)
	}
	if token != "at-refreshed" {
		t.Errorf("token = %q, want the refreshed access token", token)
	}
	forms := as.tokenFormsSnapshot()
	if len(forms) != 1 || forms[0].Get("grant_type") != "refresh_token" || forms[0].Get("refresh_token") != "rt-1" {
		t.Errorf("refresh form = %v", forms)
	}
	if loaded := config.Load(); loaded.FindMCPServer("fake").OAuth.AccessToken != "at-refreshed" {
		t.Error("refreshed token was not persisted")
	}

	// A valid, unexpired token is used as-is.
	if got := cfg.FindMCPServer("fake"); got != nil {
		got.OAuth = &config.MCPOAuthToken{AccessToken: "at-good", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	}
	if token, err = mgr.bearerToken(ctx, "fake"); err != nil || token != "at-good" {
		t.Errorf("fresh token: %q, %v", token, err)
	}

	// A rejected refresh token means the user must sign in again: the stored
	// credentials are cleared so the UI stops claiming the server is connected.
	as.mu.Lock()
	as.tokenErr = "invalid_grant"
	as.mu.Unlock()
	expired := cfg.FindMCPServer("fake")
	expired.OAuth = &config.MCPOAuthToken{
		AccessToken:     "at-stale",
		RefreshToken:    "rt-dead",
		ExpiresAt:       time.Now().Add(-time.Hour).Unix(),
		ClientID:        as.dcrClientID,
		TokenAuthMethod: "none",
		ASURL:           as.URL,
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	_, err = mgr.bearerToken(ctx, "fake")
	var authErr *AuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v, want AuthRequiredError", err)
	}
	if loaded := config.Load(); loaded.FindMCPServer("fake").OAuth != nil {
		t.Error("rejected credentials were not cleared")
	}
}

func mustQueryParam(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	value := u.Query().Get(key)
	if value == "" {
		t.Fatalf("URL %q has no %q parameter", rawURL, key)
	}
	return value
}
