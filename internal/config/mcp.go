package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// MCP transport identifiers. "http" is the MCP Streamable HTTP transport;
// "sse" is the deprecated HTTP+SSE transport, kept because some servers still
// only expose it.
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
	MCPTransportSSE   = "sse"
)

// MCP authentication modes for an upstream server.
const (
	MCPAuthNone   = "none"
	MCPAuthStatic = "static" // user-supplied headers / bearer token
	MCPAuthOAuth  = "oauth"  // discovered OAuth 2.1 flow
)

// MCP server provenance.
const (
	MCPSourceRegistry = "registry"
	MCPSourceManual   = "manual"
	MCPSourceGit      = "git"
)

// MCPDefaultIdleTimeoutSec is how long an idle stdio server process is kept
// warm after its last tool call before Prism shuts it down.
const MCPDefaultIdleTimeoutSec = 300

// Default registry source ids. The official registry is always seeded so a
// fresh install can search without any setup.
const (
	MCPRegistryOfficialID  = "official"
	MCPRegistryOfficialURL = "https://registry.modelcontextprotocol.io"
)

// MCPRegistrySource is one registry Prism can search for servers. Every source
// speaks the official registry's OpenAPI shape (GET /v0.1/servers), which is
// what makes a private or org registry a drop-in addition rather than a new
// client per vendor.
type MCPRegistrySource struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Enabled bool   `json:"enabled"`
	// Builtin marks the seeded official registry, which cannot be removed.
	Builtin bool `json:"builtin,omitempty"`
	// AuthHeader is an optional "Name: value" pair sent on every request, for
	// private catalogs that require a token. Masked on the way to the UI.
	AuthHeader string `json:"auth_header,omitempty"`
	AddedAt    int64  `json:"added_at,omitempty"`
}

// DefaultMCPRegistrySources returns the sources a fresh config starts with.
func DefaultMCPRegistrySources() []*MCPRegistrySource {
	return []*MCPRegistrySource{{
		ID:      MCPRegistryOfficialID,
		Name:    "Official MCP Registry",
		BaseURL: MCPRegistryOfficialURL,
		Enabled: true,
		Builtin: true,
	}}
}

// MCPServerState values reported by the running gateway.
const (
	MCPStateDisabled       = "disabled"
	MCPStateIdle           = "idle"
	MCPStateReady          = "ready"
	MCPStateNeedsAuth      = "needs_auth"
	MCPStateRuntimeMissing = "runtime_missing"
	MCPStateError          = "error"
)

// MCPOAuthToken holds everything needed to keep calling one upstream MCP
// server, plus the client registration that produced the token. Tokens live in
// config.json (0600) alongside provider API keys and are masked on the way out
// to the admin UI.
type MCPOAuthToken struct {
	AccessToken      string   `json:"access_token,omitempty"`
	RefreshToken     string   `json:"refresh_token,omitempty"`
	ExpiresAt        int64    `json:"expires_at,omitempty"`
	ClientID         string   `json:"client_id,omitempty"`
	ClientSecret     string   `json:"client_secret,omitempty"`
	TokenAuthMethod  string   `json:"token_auth_method,omitempty"` // none | client_secret_basic | client_secret_post
	Scopes           []string `json:"scopes,omitempty"`
	Resource         string   `json:"resource,omitempty"`
	ASURL            string   `json:"as_url,omitempty"`
	Issuer           string   `json:"issuer,omitempty"`
	RegistrationMode string   `json:"registration_mode,omitempty"` // preregistered | cimd | dcr
	AuthorizedAt     int64    `json:"authorized_at,omitempty"`
}

// IsExpired reports whether the access token is expired, with a 60s buffer.
func (t *MCPOAuthToken) IsExpired() bool {
	if t == nil || t.AccessToken == "" {
		return true
	}
	if t.ExpiresAt == 0 {
		return false // no expiry advertised; assume it is valid until a 401 says otherwise
	}
	return time.Now().Unix() > t.ExpiresAt-60
}

// MCPServerConfig describes one upstream MCP server Prism connects to.
type MCPServerConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"source,omitempty"`
	Transport string `json:"transport"` // stdio | http | sse
	Enabled   bool   `json:"enabled"`

	// stdio transport
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`

	// http / sse transport
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	AuthMode      string         `json:"auth_mode,omitempty"`
	OAuth         *MCPOAuthToken `json:"oauth,omitempty"`
	ToolAllowlist []string       `json:"tool_allowlist,omitempty"`

	// Discovery metadata, carried through from the registry or git import.
	RegistryName string `json:"registry_name,omitempty"`
	Publisher    string `json:"publisher,omitempty"`
	Repository   string `json:"repository,omitempty"`

	// Marketplace provenance. RegistrySourceID is which catalog the server was
	// installed from, so a later re-resolve can go back to the same place.
	RegistrySourceID string `json:"registry_source_id,omitempty"`
	RegistryVersion  string `json:"registry_version,omitempty"`
	Verified         bool   `json:"verified,omitempty"`

	// SecretEnv names the env keys the registry declared as secrets, so the UI
	// keeps masking them across edits instead of re-guessing from the name.
	SecretEnv []string `json:"secret_env,omitempty"`

	// IntegritySHA256 is the package hash the registry published, when it did.
	// It is verified before a direct-download package is run.
	IntegritySHA256 string `json:"integrity_sha256,omitempty"`

	AddedAt int64 `json:"added_at,omitempty"`
}

// MCPClientRegistration caches a dynamically registered OAuth client for one
// authorization server, so a second MCP server on the same AS reuses it
// instead of registering again.
type MCPClientRegistration struct {
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret,omitempty"`
	TokenAuthMethod  string `json:"token_auth_method,omitempty"`
	RedirectURI      string `json:"redirect_uri,omitempty"`
	Issuer           string `json:"issuer,omitempty"`
	RegistrationMode string `json:"registration_mode,omitempty"`
	RegisteredAt     int64  `json:"registered_at,omitempty"`
}

// MCPConfig is Prism's MCP gateway state: the upstream servers, which agents
// may reach which server, and the shared OAuth client cache.
type MCPConfig struct {
	Servers        []*MCPServerConfig                `json:"servers,omitempty"`
	AgentServers   map[string][]string               `json:"agent_servers,omitempty"`
	IdleTimeoutSec int                               `json:"idle_timeout_sec,omitempty"`
	Clients        map[string]*MCPClientRegistration `json:"clients,omitempty"`

	// Registries are the catalogs the marketplace searches. The official
	// registry is seeded and cannot be removed.
	Registries []*MCPRegistrySource `json:"registries,omitempty"`

	// ClientIDMetadataURL opts into CIMD registration: the client_id is this
	// HTTPS URL, which must serve a client metadata document. Off by default
	// because a local-first app has nowhere to host a stable HTTPS document;
	// users who run their own endpoint can point at it here.
	ClientIDMetadataURL string `json:"client_id_metadata_url,omitempty"`

	// AutoConnect lets Prism start a sign-in by itself the first time an agent
	// uses a server the user marked OAuth that has no token yet: the browser
	// opens, and the call that hit needs_auth is retried once the user
	// approves. A pointer so an explicit false survives a save; nil is on.
	AutoConnect *bool `json:"auto_connect,omitempty"`
}

// AutoConnectEnabled reports whether Prism may start an OAuth sign-in on its
// own when an agent first uses a server that is not authorized yet. Defaults to
// on: a config written before this setting existed has no opinion.
func (m *MCPConfig) AutoConnectEnabled() bool {
	if m == nil || m.AutoConnect == nil {
		return true
	}
	return *m.AutoConnect
}

func cloneMCP(m *MCPConfig) *MCPConfig {
	if m == nil {
		return nil
	}
	cp := *m
	cp.Servers = make([]*MCPServerConfig, len(m.Servers))
	for i, s := range m.Servers {
		cp.Servers[i] = cloneMCPServer(s)
	}
	if m.AgentServers != nil {
		cp.AgentServers = make(map[string][]string, len(m.AgentServers))
		for k, v := range m.AgentServers {
			cp.AgentServers[k] = append([]string(nil), v...)
		}
	}
	if m.Clients != nil {
		cp.Clients = make(map[string]*MCPClientRegistration, len(m.Clients))
		for k, v := range m.Clients {
			if v == nil {
				continue
			}
			rc := *v
			cp.Clients[k] = &rc
		}
	}
	if m.AutoConnect != nil {
		v := *m.AutoConnect
		cp.AutoConnect = &v
	}
	cp.Registries = make([]*MCPRegistrySource, len(m.Registries))
	for i, r := range m.Registries {
		if r == nil {
			continue
		}
		rc := *r
		cp.Registries[i] = &rc
	}
	return &cp
}

func cloneMCPServer(s *MCPServerConfig) *MCPServerConfig {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Args = append([]string(nil), s.Args...)
	if s.Env != nil {
		cp.Env = make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			cp.Env[k] = v
		}
	}
	if s.Headers != nil {
		cp.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			cp.Headers[k] = v
		}
	}
	cp.ToolAllowlist = append([]string(nil), s.ToolAllowlist...)
	cp.SecretEnv = append([]string(nil), s.SecretEnv...)
	if s.OAuth != nil {
		ot := *s.OAuth
		ot.Scopes = append([]string(nil), s.OAuth.Scopes...)
		cp.OAuth = &ot
	}
	return &cp
}

// EnsureMCP initializes the MCP section and its maps, returning the usable
// section for callers that need to mutate it.
func (c *Config) EnsureMCP() *MCPConfig {
	if c.MCP == nil {
		c.MCP = &MCPConfig{}
	}
	if c.MCP.Servers == nil {
		c.MCP.Servers = []*MCPServerConfig{}
	}
	if c.MCP.AgentServers == nil {
		c.MCP.AgentServers = map[string][]string{}
	}
	if c.MCP.Clients == nil {
		c.MCP.Clients = map[string]*MCPClientRegistration{}
	}
	if c.MCP.IdleTimeoutSec <= 0 {
		c.MCP.IdleTimeoutSec = MCPDefaultIdleTimeoutSec
	}
	c.MCP.Registries = EnsureOfficialRegistry(c.MCP.Registries)
	return c.MCP
}

// EnsureOfficialRegistry guarantees the official registry is present and that
// every source has an id and a base URL. A config saved before the marketplace
// existed gets the seeded source on the next load, so users who never add a
// registry keep exactly the behavior they had.
func EnsureOfficialRegistry(sources []*MCPRegistrySource) []*MCPRegistrySource {
	out := make([]*MCPRegistrySource, 0, len(sources)+1)
	seen := map[string]bool{}
	hasOfficial := false
	for _, s := range sources {
		if s == nil || strings.TrimSpace(s.BaseURL) == "" {
			continue
		}
		if s.ID == "" {
			s.ID = MCPIDFromName(s.Name)
		}
		if s.ID == MCPRegistryOfficialID {
			s.Builtin = true
			hasOfficial = true
			if s.Name == "" {
				s.Name = "Official MCP Registry"
			}
		}
		if seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	if !hasOfficial {
		out = append(DefaultMCPRegistrySources(), out...)
	}
	return out
}

// FindMCPRegistry returns the registry source with the given id, or nil.
func (c *Config) FindMCPRegistry(id string) *MCPRegistrySource {
	if c == nil || c.MCP == nil {
		return nil
	}
	for _, s := range c.MCP.Registries {
		if s != nil && s.ID == id {
			return s
		}
	}
	return nil
}

// EnabledMCPRegistries returns the enabled registry sources.
func (c *Config) EnabledMCPRegistries() []*MCPRegistrySource {
	if c == nil || c.MCP == nil {
		return nil
	}
	out := make([]*MCPRegistrySource, 0, len(c.MCP.Registries))
	for _, s := range c.MCP.Registries {
		if s != nil && s.Enabled {
			out = append(out, s)
		}
	}
	return out
}

// UniqueMCPRegistryID returns base, or base-2/base-3/... if it is taken.
func (c *Config) UniqueMCPRegistryID(base string) string {
	if base == "" {
		base = "registry"
	}
	taken := func(id string) bool { return c.FindMCPRegistry(id) != nil }
	if !taken(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken(candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}

// RemoveMCPRegistry deletes a registry source. The builtin official registry
// cannot be removed. Returns true when something was removed.
func (c *Config) RemoveMCPRegistry(id string) bool {
	if c == nil || c.MCP == nil {
		return false
	}
	kept := make([]*MCPRegistrySource, 0, len(c.MCP.Registries))
	removed := false
	for _, s := range c.MCP.Registries {
		if s != nil && s.ID == id {
			if s.Builtin {
				kept = append(kept, s)
				continue
			}
			removed = true
			continue
		}
		kept = append(kept, s)
	}
	if !removed {
		return false
	}
	c.MCP.Registries = kept
	return true
}

// ValidateMCPRegistrySource checks the fields required to query a source.
func ValidateMCPRegistrySource(s *MCPRegistrySource) error {
	if s == nil {
		return errors.New("missing registry source")
	}
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("name is required")
	}
	raw := strings.TrimSpace(s.BaseURL)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid base URL %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("base URL must use http or https")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return errors.New("non-loopback registry URLs must use https")
	}
	if u.Fragment != "" {
		return errors.New("base URL must not contain a fragment")
	}
	return nil
}

// FindMCPServer returns the server with the given id, or nil.
func (c *Config) FindMCPServer(id string) *MCPServerConfig {
	if c == nil || c.MCP == nil {
		return nil
	}
	for _, s := range c.MCP.Servers {
		if s != nil && s.ID == id {
			return s
		}
	}
	return nil
}

// RemoveMCPServer deletes a server and every per-agent reference to it.
// Returns true when something was removed.
func (c *Config) RemoveMCPServer(id string) bool {
	if c == nil || c.MCP == nil {
		return false
	}
	kept := make([]*MCPServerConfig, 0, len(c.MCP.Servers))
	removed := false
	for _, s := range c.MCP.Servers {
		if s != nil && s.ID == id {
			removed = true
			continue
		}
		kept = append(kept, s)
	}
	if !removed {
		return false
	}
	c.MCP.Servers = kept
	for agent, ids := range c.MCP.AgentServers {
		c.MCP.AgentServers[agent] = removeString(ids, id)
	}
	return true
}

// MCPServersForAgent returns the enabled servers an agent is allowed to reach.
// An empty allowlist means the agent reaches nothing, so the gateway exposes
// no tools until the user opts in.
func (c *Config) MCPServersForAgent(agent string) []*MCPServerConfig {
	if c == nil || c.MCP == nil {
		return nil
	}
	allowed := c.MCP.AgentServers[agent]
	if len(allowed) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		set[id] = struct{}{}
	}
	out := make([]*MCPServerConfig, 0, len(allowed))
	for _, s := range c.MCP.Servers {
		if s == nil || !s.Enabled {
			continue
		}
		if _, ok := set[s.ID]; ok {
			out = append(out, s)
		}
	}
	return out
}

// EnabledMCPServers returns every enabled server regardless of agent.
func (c *Config) EnabledMCPServers() []*MCPServerConfig {
	if c == nil || c.MCP == nil {
		return nil
	}
	out := make([]*MCPServerConfig, 0, len(c.MCP.Servers))
	for _, s := range c.MCP.Servers {
		if s != nil && s.Enabled {
			out = append(out, s)
		}
	}
	return out
}

// SetAgentMCPServers replaces one agent's server allowlist.
func (c *Config) SetAgentMCPServers(agent string, ids []string) {
	m := c.EnsureMCP()
	clean := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	m.AgentServers[agent] = clean
}

// RedactMCPSecrets returns a copy of the config with MCP credentials stripped,
// for handlers that serve config to the admin UI.
func (c *Config) RedactMCPSecrets() *Config {
	if c == nil || c.MCP == nil {
		return c
	}
	cp := c.Clone()
	for _, s := range cp.MCP.Servers {
		if s == nil {
			continue
		}
		if s.OAuth != nil {
			if s.OAuth.AccessToken != "" {
				s.OAuth.AccessToken = maskKey(s.OAuth.AccessToken)
			}
			if s.OAuth.RefreshToken != "" {
				s.OAuth.RefreshToken = maskKey(s.OAuth.RefreshToken)
			}
			if s.OAuth.ClientSecret != "" {
				s.OAuth.ClientSecret = maskKey(s.OAuth.ClientSecret)
			}
		}
		for k, v := range s.Headers {
			if looksSecretHeader(k) && v != "" {
				s.Headers[k] = maskKey(v)
			}
		}
		// A registry install declares which env keys are credentials, so those
		// are masked even when the variable name does not give it away (e.g.
		// WEATHER_API_KEY vs. a key called simply TOKEN_VALUE).
		for _, k := range s.SecretEnv {
			if v, ok := s.Env[k]; ok && v != "" {
				s.Env[k] = maskKey(v)
			}
		}
	}
	for _, reg := range cp.MCP.Clients {
		if reg != nil && reg.ClientSecret != "" {
			reg.ClientSecret = maskKey(reg.ClientSecret)
		}
	}
	for _, src := range cp.MCP.Registries {
		if src == nil || src.AuthHeader == "" {
			continue
		}
		// Keep the header name so the field reads as configured, but mask the
		// credential the same way other secrets are masked.
		if name, value, ok := strings.Cut(src.AuthHeader, ":"); ok {
			src.AuthHeader = strings.TrimSpace(name) + ": " + maskKey(strings.TrimSpace(value))
		} else {
			src.AuthHeader = maskKey(src.AuthHeader)
		}
	}
	return cp
}

func looksSecretHeader(name string) bool {
	l := strings.ToLower(name)
	return l == "authorization" || strings.Contains(l, "api-key") || strings.Contains(l, "apikey") ||
		strings.Contains(l, "token") || strings.Contains(l, "secret")
}

// IsSecretHeader reports whether an HTTP header name carries a credential, so
// callers outside this package can apply the same redaction rule.
func IsSecretHeader(name string) bool { return looksSecretHeader(name) }

// ValidateMCPServer checks the fields required to actually connect.
func ValidateMCPServer(s *MCPServerConfig) error {
	if s == nil {
		return errors.New("missing server")
	}
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("name is required")
	}
	switch s.Transport {
	case MCPTransportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return errors.New("command is required for stdio servers")
		}
		if strings.ContainsAny(s.Command, " \t") {
			return errors.New("command must be a single executable token; put flags in args")
		}
	case MCPTransportHTTP, MCPTransportSSE:
		if err := validateMCPURL(s.URL); err != nil {
			return err
		}
	case "":
		return errors.New("transport is required (stdio or http)")
	default:
		return fmt.Errorf("unsupported transport %q", s.Transport)
	}
	switch s.AuthMode {
	case "", MCPAuthNone, MCPAuthStatic, MCPAuthOAuth:
	default:
		return fmt.Errorf("unsupported auth mode %q", s.AuthMode)
	}
	return nil
}

// validateMCPURL enforces the Agent Plugins rule: HTTPS for anything that is
// not loopback.
func validateMCPURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("url is required for remote servers")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must use http or https")
	}
	if u.Host == "" {
		return errors.New("url must include a host")
	}
	if u.Fragment != "" {
		return errors.New("url must not contain a fragment")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return errors.New("non-loopback MCP endpoints must use https")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	return strings.HasPrefix(host, "127.")
}

var mcpIDPattern = regexp.MustCompile(`[^a-z0-9]+`)

// MCPIDFromName produces a stable, URL-safe server id from a display name.
func MCPIDFromName(name string) string {
	l := strings.ToLower(strings.TrimSpace(name))
	l = mcpIDPattern.ReplaceAllString(l, "-")
	l = strings.Trim(l, "-")
	if l == "" {
		l = "server"
	}
	if len(l) > 48 {
		l = l[:48]
	}
	return l
}

// UniqueMCPID returns base, or base-2/base-3/... if the id is already taken.
// It also avoids the reserved "prism" name used by the downstream entry.
func (c *Config) UniqueMCPID(base string) string {
	if base == "" {
		base = "server"
	}
	if c == nil || c.MCP == nil {
		return base
	}
	taken := func(id string) bool {
		if id == "prism" {
			return true // reserved for the Prism gateway entry itself
		}
		return c.FindMCPServer(id) != nil
	}
	if !taken(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken(candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}

func removeString(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}
