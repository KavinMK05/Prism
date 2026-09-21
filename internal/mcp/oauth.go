package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ollama-proxy/internal/config"
)

// MCP authorization follows the spec's OAuth 2.1 profile:
//
//	401 + WWW-Authenticate  ->  resource metadata (RFC 9728)
//	resource metadata       ->  authorization server metadata (RFC 8414,
//	                            falling back to OpenID Connect discovery)
//	AS metadata             ->  client registration (pre-registered, CIMD, or
//	                            dynamic client registration, RFC 7591)
//	                        ->  authorization code + PKCE on a loopback redirect
//	                        ->  token exchange, with RFC 8707 `resource`
//
// Everything is brokered by Prism: the resulting tokens live in config.json and
// never reach the agents.

const (
	protectedResourcePath    = "/.well-known/oauth-protected-resource"
	authorizationServerPath  = "/.well-known/oauth-authorization-server"
	openIDConfigurationPath  = "/.well-known/openid-configuration"
	oauthHTTPTimeout         = 20 * time.Second
	oauthFlowTTL             = 10 * time.Minute
	asMetadataCacheTTL       = time.Hour
	resourceMetadataCacheTTL = time.Hour
)

// ProtectedResourceMetadata is the RFC 9728 document a resource server
// publishes to point clients at its authorization server.
type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name"`
}

// AuthorizationServerMetadata is the RFC 8414 document (the OpenID Connect
// discovery document is parsed into the same shape).
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
}

// AuthDiscovery summarizes everything the admin UI shows about a server's
// authorization setup before the user starts a login.
type AuthDiscovery struct {
	Resource             string   `json:"resource"`
	ResourceName         string   `json:"resource_name,omitempty"`
	AuthorizationServer  string   `json:"authorization_server"`
	Issuer               string   `json:"issuer,omitempty"`
	RegistrationEndpoint string   `json:"registration_endpoint,omitempty"`
	Scopes               []string `json:"scopes"`
	RegistrationMode     string   `json:"registration_mode"`
	CIMDSupported        bool     `json:"cimd_supported"`
}

// oauthFlow is one in-progress authorization-code exchange.
type oauthFlow struct {
	serverID     string
	state        string
	verifier     string
	redirectURI  string
	asURL        string
	issuer       string
	tokenURL     string
	revokeURL    string
	resource     string
	scopes       []string
	clientID     string
	clientSecret string
	authMethod   string
	regMode      string
	createdAt    time.Time
	server       *http.Server
}

var (
	flowMu       sync.Mutex
	flowsByState = map[string]*oauthFlow{}

	prmCache  sync.Map // resource URL -> cachedResourceMetadata
	asmdCache sync.Map // AS URL -> cachedASMetadata
)

type cachedResourceMetadata struct {
	md        *ProtectedResourceMetadata
	fetchedAt time.Time
}

type cachedASMetadata struct {
	md        *AuthorizationServerMetadata
	fetchedAt time.Time
}

func oauthClient() *http.Client {
	return &http.Client{Timeout: oauthHTTPTimeout}
}

// -- discovery --

// DiscoverAuth resolves the resource metadata and authorization server
// metadata for a server, without starting a login.
func DiscoverAuth(ctx context.Context, s *config.MCPServerConfig) (*AuthDiscovery, error) {
	if s == nil {
		return nil, errors.New("missing server")
	}
	if s.Transport == config.MCPTransportStdio {
		return nil, errors.New("stdio servers authenticate with environment variables, not OAuth")
	}
	prm, err := discoverProtectedResource(ctx, s)
	if err != nil {
		return nil, err
	}
	asURL := firstAuthorizationServer(prm, s.URL)
	asmd, err := discoverASMetadata(ctx, asURL)
	if err != nil {
		return nil, err
	}
	scopes := prm.ScopesSupported
	if len(scopes) == 0 {
		scopes = asmd.ScopesSupported
	}
	return &AuthDiscovery{
		Resource:             prm.Resource,
		ResourceName:         prm.ResourceName,
		AuthorizationServer:  asURL,
		Issuer:               asmd.Issuer,
		RegistrationEndpoint: asmd.RegistrationEndpoint,
		Scopes:               scopes,
		RegistrationMode:     registrationModeFor(s, asmd),
		CIMDSupported:        asmd.ClientIDMetadataDocumentSupported,
	}, nil
}

// discoverProtectedResource finds the RFC 9728 document: first from the
// server's own WWW-Authenticate challenge, then from the well-known location,
// and finally by assuming the resource origin is its own authorization server.
func discoverProtectedResource(ctx context.Context, s *config.MCPServerConfig) (*ProtectedResourceMetadata, error) {
	if cached, ok := prmCache.Load(s.URL); ok {
		if c, ok := cached.(cachedResourceMetadata); ok && time.Since(c.fetchedAt) < resourceMetadataCacheTTL {
			return c.md, nil
		}
	}

	var lastErr error
	if challenge, err := authChallenge(ctx, s); err == nil && challenge != "" {
		if raw := challengeParam(challenge, "resource_metadata"); raw != "" {
			md, err := fetchProtectedResourceMetadata(ctx, raw)
			if err == nil {
				prmCache.Store(s.URL, cachedResourceMetadata{md: md, fetchedAt: time.Now()})
				return md, nil
			}
			lastErr = err
		}
	}
	for _, candidate := range wellKnownProtectedResourceURLs(s.URL) {
		md, err := fetchProtectedResourceMetadata(ctx, candidate)
		if err == nil {
			prmCache.Store(s.URL, cachedResourceMetadata{md: md, fetchedAt: time.Now()})
			return md, nil
		}
		lastErr = err
	}
	origin, err := originOf(s.URL)
	if err != nil {
		return nil, err
	}
	if lastErr == nil {
		lastErr = errors.New("no protected resource metadata found")
	}
	log.Printf("[MCP] %s: using resource origin as authorization server (%v)", s.ID, lastErr)
	md := &ProtectedResourceMetadata{Resource: origin, AuthorizationServers: []string{origin}}
	prmCache.Store(s.URL, cachedResourceMetadata{md: md, fetchedAt: time.Now()})
	return md, nil
}

// authChallenge performs an unauthenticated request to the MCP endpoint and
// returns the WWW-Authenticate header, which is where a compliant server
// advertises its resource metadata URL and scope.
func authChallenge(ctx context.Context, s *config.MCPServerConfig) (string, error) {
	body, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "server/discover",
		Params:  injectMeta(nil),
	})
	cctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, s.URL, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	// Static headers (API keys) are intentionally not sent: the challenge is
	// what tells Prism whether OAuth is needed at all.
	resp, err := oauthClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return "", fmt.Errorf("no authorization challenge (HTTP %d)", resp.StatusCode)
	}
	return resp.Header.Get("WWW-Authenticate"), nil
}

func fetchProtectedResourceMetadata(ctx context.Context, rawURL string) (*ProtectedResourceMetadata, error) {
	cctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resource metadata %s returned HTTP %d", rawURL, resp.StatusCode)
	}
	var md ProtectedResourceMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&md); err != nil {
		return nil, fmt.Errorf("invalid resource metadata from %s: %w", rawURL, err)
	}
	return &md, nil
}

// discoverASMetadata tries RFC 8414 first and the OpenID Connect discovery
// document second, which the MCP spec requires clients to support: Notion, for
// example, serves the former and 404s the latter.
func discoverASMetadata(ctx context.Context, asURL string) (*AuthorizationServerMetadata, error) {
	if cached, ok := asmdCache.Load(asURL); ok {
		if c, ok := cached.(cachedASMetadata); ok && time.Since(c.fetchedAt) < asMetadataCacheTTL {
			return c.md, nil
		}
	}
	var lastErr error
	for _, candidate := range wellKnownASMetadataURLs(asURL) {
		md, err := fetchASMetadata(ctx, candidate)
		if err == nil {
			if md.Issuer == "" {
				md.Issuer = asURL
			}
			if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" {
				lastErr = fmt.Errorf("authorization server metadata from %s is missing endpoints", candidate)
				continue
			}
			asmdCache.Store(asURL, cachedASMetadata{md: md, fetchedAt: time.Now()})
			return md, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no authorization server metadata found")
	}
	return nil, lastErr
}

func fetchASMetadata(ctx context.Context, rawURL string) (*AuthorizationServerMetadata, error) {
	cctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authorization server metadata %s returned HTTP %d", rawURL, resp.StatusCode)
	}
	var md AuthorizationServerMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&md); err != nil {
		return nil, fmt.Errorf("invalid authorization server metadata from %s: %w", rawURL, err)
	}
	return &md, nil
}

func wellKnownProtectedResourceURLs(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	base := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.Path, "/")
	out := []string{base + protectedResourcePath + path}
	if path != "" {
		out = append(out, base+protectedResourcePath)
	}
	return out
}

// wellKnownASMetadataURLs implements the RFC 8414 path-insertion rule plus the
// OpenID Connect fallback the MCP spec requires.
func wellKnownASMetadataURLs(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	base := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.Path, "/")
	return []string{
		base + authorizationServerPath + path,
		base + openIDConfigurationPath + path,
		base + authorizationServerPath,
		base + openIDConfigurationPath,
	}
}

func originOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid url %q", rawURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

func firstAuthorizationServer(prm *ProtectedResourceMetadata, fallbackURL string) string {
	if prm != nil {
		for _, as := range prm.AuthorizationServers {
			if strings.TrimSpace(as) != "" {
				return strings.TrimSpace(as)
			}
		}
		if prm.Resource != "" {
			return prm.Resource
		}
	}
	origin, err := originOf(fallbackURL)
	if err != nil {
		return fallbackURL
	}
	return origin
}

// challengeParam extracts one parameter from a WWW-Authenticate header value,
// tolerating the unquoted forms some servers emit.
func challengeParam(challenge, name string) string {
	lower := strings.ToLower(challenge)
	idx := strings.Index(lower, strings.ToLower(name)+"=")
	if idx < 0 {
		return ""
	}
	rest := challenge[idx+len(name)+1:]
	if strings.HasPrefix(rest, "\"") {
		rest = rest[1:]
		if end := strings.Index(rest, "\""); end >= 0 {
			return rest[:end]
		}
		return rest
	}
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// -- registration --

// registrationModeFor reports how Prism would register itself with this server.
func registrationModeFor(s *config.MCPServerConfig, asmd *AuthorizationServerMetadata) string {
	if s.OAuth != nil && s.OAuth.ClientID != "" && s.OAuth.RegistrationMode == "preregistered" {
		return "preregistered"
	}
	if cfg := config.Current(); cfg != nil && cfg.MCP != nil && cfg.MCP.ClientIDMetadataURL != "" && asmd.ClientIDMetadataDocumentSupported {
		return "cimd"
	}
	if asmd.RegistrationEndpoint != "" {
		return "dcr"
	}
	return "manual"
}

// resolveClientRegistration picks the OAuth client Prism uses: a pre-registered
// client from the server config, a CIMD client id, a cached dynamic
// registration, or a fresh dynamic registration.
func resolveClientRegistration(ctx context.Context, cfg *config.Config, s *config.MCPServerConfig, asURL string, asmd *AuthorizationServerMetadata, redirectURI string, scopes []string) (*config.MCPClientRegistration, string, error) {
	if s.OAuth != nil && s.OAuth.ClientID != "" {
		return &config.MCPClientRegistration{
			ClientID:         s.OAuth.ClientID,
			ClientSecret:     s.OAuth.ClientSecret,
			TokenAuthMethod:  defaultAuthMethod(s.OAuth.TokenAuthMethod, asmd),
			RedirectURI:      redirectURI,
			Issuer:           asmd.Issuer,
			RegistrationMode: "preregistered",
		}, "preregistered", nil
	}
	if cfg.MCP != nil && cfg.MCP.ClientIDMetadataURL != "" && asmd.ClientIDMetadataDocumentSupported {
		return &config.MCPClientRegistration{
			ClientID:         cfg.MCP.ClientIDMetadataURL,
			TokenAuthMethod:  "none",
			RedirectURI:      redirectURI,
			Issuer:           asmd.Issuer,
			RegistrationMode: "cimd",
		}, "cimd", nil
	}
	if cfg.MCP != nil {
		if reg, ok := cfg.MCP.Clients[asURL]; ok && reg != nil && reg.ClientID != "" {
			return &config.MCPClientRegistration{
				ClientID:         reg.ClientID,
				ClientSecret:     reg.ClientSecret,
				TokenAuthMethod:  defaultAuthMethod(reg.TokenAuthMethod, asmd),
				RedirectURI:      redirectURI,
				Issuer:           asmd.Issuer,
				RegistrationMode: "dcr",
			}, "dcr", nil
		}
	}
	if asmd.RegistrationEndpoint != "" {
		reg, err := dynamicClientRegister(ctx, asmd, redirectURI, scopes)
		if err != nil {
			return nil, "", err
		}
		return reg, "dcr", nil
	}
	return nil, "", errors.New("this authorization server supports neither dynamic registration nor a client_id supplied by the user; add a client_id in the server settings")
}

func defaultAuthMethod(preferred string, asmd *AuthorizationServerMetadata) string {
	supported := asmd.TokenEndpointAuthMethodsSupported
	if len(supported) == 0 {
		if preferred != "" {
			return preferred
		}
		return "none"
	}
	if preferred != "" {
		for _, m := range supported {
			if m == preferred {
				return preferred
			}
		}
	}
	for _, want := range []string{"none", "client_secret_post", "client_secret_basic"} {
		for _, m := range supported {
			if m == want {
				return want
			}
		}
	}
	return supported[0]
}

func dynamicClientRegister(ctx context.Context, asmd *AuthorizationServerMetadata, redirectURI string, scopes []string) (*config.MCPClientRegistration, error) {
	authMethod := defaultAuthMethod("", asmd)
	body := map[string]interface{}{
		"client_name":                "Prism",
		"application_type":           "native",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": authMethod,
		"software_id":                "prism",
	}
	if len(scopes) > 0 {
		body["scope"] = strings.Join(scopes, " ")
	}
	data, _ := json.Marshal(body)

	cctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, asmd.RegistrationEndpoint, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("dynamic client registration failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("invalid registration response: %w", err)
	}
	if out.ClientID == "" {
		return nil, errors.New("registration response did not include a client_id")
	}
	method := out.TokenEndpointAuthMethod
	if method == "" {
		method = authMethod
	}
	return &config.MCPClientRegistration{
		ClientID:         out.ClientID,
		ClientSecret:     out.ClientSecret,
		TokenAuthMethod:  method,
		RedirectURI:      redirectURI,
		Issuer:           asmd.Issuer,
		RegistrationMode: "dcr",
		RegisteredAt:     time.Now().Unix(),
	}, nil
}

// -- login flow --

// StartOAuthLogin begins the authorization-code flow for a server: it discovers
// the endpoints, resolves a client, opens a loopback callback listener, and
// returns the URL the user must open.
func StartOAuthLogin(ctx context.Context, cfg *config.Config, serverID string) (string, string, error) {
	s := cfg.FindMCPServer(serverID)
	if s == nil {
		return "", "", fmt.Errorf("unknown MCP server %q", serverID)
	}
	if s.Transport == config.MCPTransportStdio {
		return "", "", errors.New("stdio servers authenticate with environment variables, not OAuth")
	}

	prm, err := discoverProtectedResource(ctx, s)
	if err != nil {
		return "", "", err
	}
	asURL := firstAuthorizationServer(prm, s.URL)
	asmd, err := discoverASMetadata(ctx, asURL)
	if err != nil {
		return "", "", err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("failed to start the OAuth callback listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	scopes := challengeScopes(ctx, s, prm, asmd)
	reg, mode, err := resolveClientRegistration(ctx, cfg, s, asURL, asmd, redirectURI, scopes)
	if err != nil {
		ln.Close()
		return "", "", err
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		ln.Close()
		return "", "", err
	}
	state, err := randomTokenString()
	if err != nil {
		ln.Close()
		return "", "", err
	}

	flow := &oauthFlow{
		serverID:     serverID,
		state:        state,
		verifier:     verifier,
		redirectURI:  redirectURI,
		asURL:        asURL,
		issuer:       asmd.Issuer,
		tokenURL:     asmd.TokenEndpoint,
		revokeURL:    asmd.RevocationEndpoint,
		resource:     resourceFor(prm, s.URL),
		scopes:       scopes,
		clientID:     reg.ClientID,
		clientSecret: reg.ClientSecret,
		authMethod:   defaultAuthMethod(reg.TokenAuthMethod, asmd),
		regMode:      mode,
		createdAt:    time.Now(),
	}

	mux := http.NewServeMux()
	flow.server = &http.Server{Handler: mux}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		handleOAuthCallback(w, r, flow)
	})

	flowMu.Lock()
	flowsByState[state] = flow
	flowMu.Unlock()

	go func() {
		if err := flow.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[MCP] OAuth callback server error: %v", err)
		}
	}()
	go func() {
		time.Sleep(oauthFlowTTL)
		flowMu.Lock()
		_, pending := flowsByState[state]
		delete(flowsByState, state)
		flowMu.Unlock()
		if pending {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			flow.server.Shutdown(ctx)
		}
	}()

	authURL, err := buildAuthorizeURL(asmd.AuthorizationEndpoint, flow, challenge)
	if err != nil {
		return "", "", err
	}
	return authURL, redirectURI, nil
}

// challengeScopes returns the scopes Prism should request: the challenge's
// scope when the server names one (authoritative per the spec), otherwise
// everything the resource or authorization server advertises.
func challengeScopes(ctx context.Context, s *config.MCPServerConfig, prm *ProtectedResourceMetadata, asmd *AuthorizationServerMetadata) []string {
	if challenge, err := authChallenge(ctx, s); err == nil && challenge != "" {
		if raw := challengeParam(challenge, "scope"); raw != "" {
			return strings.Fields(raw)
		}
	}
	if len(prm.ScopesSupported) > 0 {
		return prm.ScopesSupported
	}
	return asmd.ScopesSupported
}

func resourceFor(prm *ProtectedResourceMetadata, fallbackURL string) string {
	if prm != nil && prm.Resource != "" {
		return prm.Resource
	}
	if origin, err := originOf(fallbackURL); err == nil {
		return origin
	}
	return fallbackURL
}

func buildAuthorizeURL(endpoint string, flow *oauthFlow, challenge string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", flow.clientID)
	q.Set("redirect_uri", flow.redirectURI)
	q.Set("state", flow.state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if flow.resource != "" {
		q.Set("resource", flow.resource)
	}
	if len(flow.scopes) > 0 {
		q.Set("scope", strings.Join(flow.scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// handleOAuthCallback completes the flow: validate, exchange, persist.
func handleOAuthCallback(w http.ResponseWriter, r *http.Request, flow *oauthFlow) {
	query := r.URL.Query()
	if errParam := query.Get("error"); errParam != "" {
		desc := query.Get("error_description")
		renderOAuthResult(w, http.StatusBadRequest, "Authentication failed", errParam+": "+desc, false)
		return
	}
	if state := query.Get("state"); state != flow.state {
		renderOAuthResult(w, http.StatusBadRequest, "Invalid or expired link", "This sign-in link has expired. Start the connection again from Prism.", false)
		return
	}
	// RFC 9207: when the server returns `iss`, it must match the issuer Prism
	// recorded, compared literally (no normalization or case folding).
	if iss := query.Get("iss"); iss != "" && flow.issuer != "" && iss != flow.issuer {
		renderOAuthResult(w, http.StatusBadRequest, "Issuer mismatch", "The authorization response came from an unexpected issuer ("+iss+").", false)
		return
	}
	code := query.Get("code")
	if code == "" {
		renderOAuthResult(w, http.StatusBadRequest, "Missing authorization code", "No authorization code was received.", false)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
	defer cancel()
	token, err := exchangeAuthorizationCode(ctx, flow, code)
	if err != nil {
		log.Printf("[MCP] OAuth token exchange failed for %s: %v", flow.serverID, err)
		renderOAuthResult(w, http.StatusInternalServerError, "Token exchange failed", err.Error(), false)
		return
	}
	if err := persistToken(flow, token); err != nil {
		log.Printf("[MCP] failed to save token for %s: %v", flow.serverID, err)
		renderOAuthResult(w, http.StatusInternalServerError, "Could not save the token", err.Error(), false)
		return
	}

	// The tray process owns the live config; swapping it also restarts the
	// proxy process so the MCP gateway picks the token up.
	config.SetCurrent(config.Load())

	flowMu.Lock()
	delete(flowsByState, flow.state)
	flowMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		flow.server.Shutdown(ctx)
	}()

	renderOAuthResult(w, http.StatusOK, "Connected", "Prism can now reach this MCP server. You can close this window.", true)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func exchangeAuthorizationCode(ctx context.Context, flow *oauthFlow, code string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", flow.redirectURI)
	form.Set("code_verifier", flow.verifier)
	if flow.resource != "" {
		form.Set("resource", flow.resource)
	}
	return postTokenRequest(ctx, flow.tokenURL, form, flow.clientID, flow.clientSecret, flow.authMethod)
}

// postTokenRequest performs a form-encoded token request with the client
// authentication method the authorization server advertised.
func postTokenRequest(ctx context.Context, endpoint string, form url.Values, clientID, clientSecret, method string) (*tokenResponse, error) {
	if endpoint == "" {
		return nil, errors.New("no token endpoint is known for this server")
	}
	if method == "client_secret_basic" {
		// credentials go in the Authorization header below
	} else {
		form.Set("client_id", clientID)
		if method == "client_secret_post" && clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if method == "client_secret_basic" {
		req.SetBasicAuth(clientID, clientSecret)
	}
	resp, err := oauthClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, tokenEndpointError(resp.StatusCode, raw)
	}
	var out tokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("invalid token response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, errors.New("token response did not include an access_token")
	}
	return &out, nil
}

// tokenEndpointError recognizes the one RFC 6749 error code Prism treats
// specially: invalid_grant means the user has to sign in again.
func tokenEndpointError(status int, raw []byte) error {
	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &body)
	if body.Error == "invalid_grant" {
		return errInvalidGrant
	}
	msg := strings.TrimSpace(string(raw))
	if body.Error != "" {
		msg = body.Error
		if body.Description != "" {
			msg += ": " + body.Description
		}
	}
	return fmt.Errorf("token endpoint returned HTTP %d: %s", status, msg)
}

var errInvalidGrant = errors.New("authorization is no longer valid (invalid_grant); sign in again")

// persistToken writes the token and its client registration into config.json.
func persistToken(flow *oauthFlow, token *tokenResponse) error {
	cfg := config.Load()
	s := cfg.FindMCPServer(flow.serverID)
	if s == nil {
		return fmt.Errorf("server %q disappeared from the config", flow.serverID)
	}
	scopes := flow.scopes
	if token.Scope != "" {
		scopes = strings.Fields(token.Scope)
	}
	expiresAt := int64(0)
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
	}
	s.AuthMode = config.MCPAuthOAuth
	s.OAuth = &config.MCPOAuthToken{
		AccessToken:      token.AccessToken,
		RefreshToken:     token.RefreshToken,
		ExpiresAt:        expiresAt,
		ClientID:         flow.clientID,
		ClientSecret:     flow.clientSecret,
		TokenAuthMethod:  flow.authMethod,
		Scopes:           scopes,
		Resource:         flow.resource,
		ASURL:            flow.asURL,
		Issuer:           flow.issuer,
		RegistrationMode: flow.regMode,
		AuthorizedAt:     time.Now().Unix(),
	}
	if flow.regMode == "dcr" {
		cfg.EnsureMCP().Clients[flow.asURL] = &config.MCPClientRegistration{
			ClientID:         flow.clientID,
			ClientSecret:     flow.clientSecret,
			TokenAuthMethod:  flow.authMethod,
			RedirectURI:      flow.redirectURI,
			Issuer:           flow.issuer,
			RegistrationMode: "dcr",
			RegisteredAt:     time.Now().Unix(),
		}
	}
	asmdCache.Delete(flow.asURL)
	return config.Save(cfg)
}

// Logout revokes the token when the authorization server supports revocation
// and always clears it locally.
func Logout(ctx context.Context, serverID string) error {
	cfg := config.Load()
	s := cfg.FindMCPServer(serverID)
	if s == nil {
		return fmt.Errorf("unknown MCP server %q", serverID)
	}
	if s.OAuth == nil || s.OAuth.AccessToken == "" {
		return nil
	}
	token := *s.OAuth
	if token.ASURL != "" {
		if asmd, err := discoverASMetadata(ctx, token.ASURL); err == nil && asmd.RevocationEndpoint != "" {
			form := url.Values{}
			form.Set("token", token.AccessToken)
			if token.RefreshToken != "" {
				form.Set("token_type_hint", "refresh_token")
			}
			if err := postRevocation(ctx, asmd.RevocationEndpoint, form, token); err != nil {
				log.Printf("[MCP] %s: revocation failed: %v", serverID, err)
			}
		}
	}
	cfg = config.Load()
	if target := cfg.FindMCPServer(serverID); target != nil {
		target.OAuth = nil
	}
	return config.Save(cfg)
}

func postRevocation(ctx context.Context, endpoint string, form url.Values, token config.MCPOAuthToken) error {
	method := token.TokenAuthMethod
	if method != "client_secret_basic" {
		form.Set("client_id", token.ClientID)
		if method == "client_secret_post" && token.ClientSecret != "" {
			form.Set("client_secret", token.ClientSecret)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if method == "client_secret_basic" {
		req.SetBasicAuth(token.ClientID, token.ClientSecret)
	}
	resp, err := oauthClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

// -- runtime token access --

// bearerToken returns a valid access token for a server, refreshing it when the
// stored one has expired. It is the only piece the MCP gateway needs from the
// OAuth layer.
func (m *Manager) bearerToken(ctx context.Context, serverID string) (string, error) {
	cfg := m.cfg()
	if cfg == nil {
		return "", &AuthRequiredError{ServerID: serverID, Message: "no configuration loaded"}
	}
	s := cfg.FindMCPServer(serverID)
	if s == nil {
		return "", &AuthRequiredError{ServerID: serverID, Message: "unknown MCP server"}
	}
	if s.OAuth == nil || s.OAuth.AccessToken == "" {
		return "", &AuthRequiredError{ServerID: serverID, Message: "this server has not been authorized yet"}
	}
	if !s.OAuth.IsExpired() {
		return s.OAuth.AccessToken, nil
	}
	return m.refreshOAuthToken(ctx, s)
}

// refreshOAuthToken exchanges the refresh token for a new access token and
// persists it. A rejected refresh token (invalid_grant) clears the stored
// credentials so the UI shows the server as needing authorization again.
func (m *Manager) refreshOAuthToken(ctx context.Context, s *config.MCPServerConfig) (string, error) {
	if s.OAuth == nil || s.OAuth.RefreshToken == "" {
		return "", &AuthRequiredError{ServerID: s.ID, Message: "the access token expired and no refresh token is stored"}
	}
	asmd, err := discoverASMetadata(ctx, s.OAuth.ASURL)
	if err != nil {
		return "", fmt.Errorf("token refresh failed: %w", err)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", s.OAuth.RefreshToken)
	if s.OAuth.Resource != "" {
		form.Set("resource", s.OAuth.Resource)
	}
	token, err := postTokenRequest(ctx, asmd.TokenEndpoint, form, s.OAuth.ClientID, s.OAuth.ClientSecret, s.OAuth.TokenAuthMethod)
	if err != nil {
		if errors.Is(err, errInvalidGrant) {
			if cerr := clearServerToken(s.ID); cerr != nil {
				log.Printf("[MCP] %s: failed to clear rejected token: %v", s.ID, cerr)
			}
			return "", &AuthRequiredError{ServerID: s.ID, Message: "authorization expired; sign in again"}
		}
		return "", err
	}
	expiresAt := int64(0)
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
	}
	refresh := token.RefreshToken
	if refresh == "" {
		refresh = s.OAuth.RefreshToken
	}
	updated := &config.MCPOAuthToken{
		AccessToken:      token.AccessToken,
		RefreshToken:     refresh,
		ExpiresAt:        expiresAt,
		ClientID:         s.OAuth.ClientID,
		ClientSecret:     s.OAuth.ClientSecret,
		TokenAuthMethod:  s.OAuth.TokenAuthMethod,
		Scopes:           s.OAuth.Scopes,
		Resource:         s.OAuth.Resource,
		ASURL:            s.OAuth.ASURL,
		Issuer:           s.OAuth.Issuer,
		RegistrationMode: s.OAuth.RegistrationMode,
		AuthorizedAt:     s.OAuth.AuthorizedAt,
	}
	if err := saveServerToken(s.ID, updated); err != nil {
		log.Printf("[MCP] %s: refreshed token could not be saved: %v", s.ID, err)
	}
	return updated.AccessToken, nil
}

// saveServerToken persists a refreshed token and keeps the calling process's
// in-memory config in sync.
func saveServerToken(serverID string, token *config.MCPOAuthToken) error {
	cfg := config.Load()
	s := cfg.FindMCPServer(serverID)
	if s == nil {
		return fmt.Errorf("unknown MCP server %q", serverID)
	}
	s.OAuth = token
	if err := config.Save(cfg); err != nil {
		return err
	}
	config.UpdateCurrent(func(cur *config.Config) {
		if cur == nil {
			return
		}
		if cs := cur.FindMCPServer(serverID); cs != nil {
			cp := *token
			cp.Scopes = append([]string(nil), token.Scopes...)
			cs.OAuth = &cp
		}
	})
	return nil
}

func clearServerToken(serverID string) error {
	cfg := config.Load()
	if s := cfg.FindMCPServer(serverID); s != nil {
		s.OAuth = nil
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	config.UpdateCurrent(func(cur *config.Config) {
		if cur == nil {
			return
		}
		if cs := cur.FindMCPServer(serverID); cs != nil {
			cs.OAuth = nil
		}
	})
	return nil
}

// -- helpers --

func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("failed to generate a PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomTokenString() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// renderOAuthResult writes the small page the loopback callback shows in the
// browser. It mirrors the Codex login page's look without depending on the
// oauth package's unexported helpers.
func renderOAuthResult(w http.ResponseWriter, status int, title, message string, success bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	accent := "#ef4444"
	icon := "&#10005;"
	autoClose := ""
	if success {
		accent = "#22c55e"
		icon = "&#10003;"
		autoClose = "<script>setTimeout(function(){window.close();},2500);</script>"
	}
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s &middot; Prism</title>
<style>
	:root { --bg:#ffffff; --surface:#ffffff; --border:#e5e5e5; --text:#171717; --muted:#737373; }
	@media (prefers-color-scheme: dark) { :root { --bg:#0a0a0a; --surface:#171717; --border:#262626; --text:#fafafa; --muted:#a3a3a3; } }
	* { margin:0; padding:0; box-sizing:border-box; }
	body { font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif; background:var(--bg); color:var(--text); min-height:100vh; display:flex; align-items:center; justify-content:center; padding:24px; }
	.card { background:var(--surface); border:1px solid var(--border); border-radius:12px; padding:40px 32px; max-width:420px; width:100%%; text-align:center; }
	.badge { width:56px; height:56px; border-radius:50%%; background:%s; color:#fff; display:flex; align-items:center; justify-content:center; font-size:26px; margin:0 auto 20px; }
	h1 { font-size:18px; font-weight:700; margin-bottom:10px; }
	p { font-size:14px; color:var(--muted); line-height:1.6; }
	.hint { font-size:12px; color:var(--muted); margin-top:20px; opacity:.7; }
</style></head>
<body><div class="card">
	<div class="badge">%s</div>
	<h1>%s</h1>
	<p>%s</p>
	<div class="hint">You can close this window.</div>
</div>%s</body></html>`, title, accent, icon, title, message, autoClose)
}
