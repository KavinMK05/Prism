package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/config"
)

func mcpFixture() *config.Config {
	cfg := &config.Config{}
	m := cfg.EnsureMCP()
	m.Servers = append(m.Servers, &config.MCPServerConfig{
		ID:        "notion",
		Name:      "Notion",
		Transport: config.MCPTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Enabled:   true,
		AuthMode:  config.MCPAuthOAuth,
		OAuth: &config.MCPOAuthToken{
			AccessToken:      "at-real-token",
			RefreshToken:     "rt-real-token",
			ClientSecret:     "cs-real-secret",
			ClientID:         "client-1",
			ASURL:            "https://mcp.notion.com",
			Issuer:           "https://mcp.notion.com",
			RegistrationMode: "dcr",
			ExpiresAt:        2000000000,
			Resource:         "https://mcp.notion.com",
		},
		Headers: map[string]string{"Authorization": "Bearer static-secret"},
		Env:     map[string]string{"API_TOKEN": "env-secret"},
	})
	m.AgentServers["claude-code"] = []string{"notion"}
	m.Clients["https://mcp.notion.com"] = &config.MCPClientRegistration{
		ClientID:     "client-1",
		ClientSecret: "cs-cache-secret",
	}
	m.IdleTimeoutSec = 300
	m.ClientIDMetadataURL = "https://client.example.com/prism.json"
	return cfg
}

// The admin UI reads a redacted config and PUTs it back, so the secret-bearing
// fields it echoed as masked values must not overwrite what is stored.
func TestPreserveMCPSecrets(t *testing.T) {
	cur := mcpFixture()

	next := cur.RedactMCPSecrets()
	// A fuller round trip may even drop the per-agent wiring entirely.
	next.MCP.AgentServers = nil
	next.MCP.Clients = nil

	preserveMCPSecrets(cur, next)

	srv := next.FindMCPServer("notion")
	if srv == nil || srv.OAuth == nil {
		t.Fatal("server or token lost")
	}
	if srv.OAuth.AccessToken != "at-real-token" || srv.OAuth.RefreshToken != "rt-real-token" {
		t.Errorf("tokens were replaced by their masked form: %+v", srv.OAuth)
	}
	if srv.OAuth.ClientSecret != "cs-real-secret" {
		t.Errorf("client secret = %q", srv.OAuth.ClientSecret)
	}
	if srv.OAuth.ClientID != "client-1" || srv.OAuth.ASURL == "" || srv.OAuth.RegistrationMode != "dcr" {
		t.Errorf("registration metadata lost: %+v", srv.OAuth)
	}
	if srv.Headers["Authorization"] != "Bearer static-secret" {
		t.Errorf("header secret = %q", srv.Headers["Authorization"])
	}
	if srv.Env["API_TOKEN"] != "env-secret" {
		t.Errorf("env secret = %q", srv.Env["API_TOKEN"])
	}
	if reg := next.MCP.Clients["https://mcp.notion.com"]; reg == nil || reg.ClientID != "client-1" {
		t.Errorf("client cache lost: %+v", reg)
	}
	if ids := next.MCP.AgentServers["claude-code"]; len(ids) != 1 || ids[0] != "notion" {
		t.Errorf("agent allowlist lost: %v", ids)
	}
	if next.MCP.ClientIDMetadataURL == "" || next.MCP.IdleTimeoutSec != 300 {
		t.Errorf("settings lost: %+v", next.MCP)
	}
}

func TestPreserveMCPSecretsAcceptsNewValues(t *testing.T) {
	cur := mcpFixture()
	next := cur.RedactMCPSecrets()
	next.FindMCPServer("notion").OAuth.AccessToken = "at-brand-new"
	next.FindMCPServer("notion").Headers["Authorization"] = "Bearer new-secret"

	preserveMCPSecrets(cur, next)

	srv := next.FindMCPServer("notion")
	if srv.OAuth.AccessToken != "at-brand-new" {
		t.Errorf("a real new token must win, got %q", srv.OAuth.AccessToken)
	}
	if srv.Headers["Authorization"] != "Bearer new-secret" {
		t.Errorf("a real new header must win, got %q", srv.Headers["Authorization"])
	}
}

func TestMergeSecretMaps(t *testing.T) {
	existing := map[string]string{"Authorization": "Bearer real", "X-Old": "keep"}

	merged := mergeSecretMaps(existing, map[string]string{
		"Authorization": "Bear...real", // masked echo from the UI
		"X-New":         "fresh",
	})
	if merged["Authorization"] != "Bearer real" {
		t.Errorf("masked value overwrote the stored secret: %q", merged["Authorization"])
	}
	if merged["X-New"] != "fresh" {
		t.Errorf("a new header was dropped: %v", merged)
	}
	if _, ok := merged["X-Old"]; ok {
		t.Error("the merge should follow the incoming map, not the existing one")
	}

	replaced := mergeSecretMaps(existing, map[string]string{"Authorization": "Bearer rotated"})
	if replaced["Authorization"] != "Bearer rotated" {
		t.Errorf("a real value should replace the stored secret: %q", replaced["Authorization"])
	}
}

func TestLooksMaskedSecret(t *testing.T) {
	masked := []string{"****", "(not set)", "sk-a...b123", "abc...xyz", "Bear...2345"}
	for _, v := range masked {
		if !looksMaskedSecret(v) {
			t.Errorf("looksMaskedSecret(%q) = false, want true", v)
		}
	}
	real := []string{"", "Bearer abcdefghijklmnop", "**********************************", "a...b-but-way-longer-than-fourteen"}
	for _, v := range real {
		if looksMaskedSecret(v) {
			t.Errorf("looksMaskedSecret(%q) = true, want false", v)
		}
	}
}

// A registry auth header carries its name, which makes the whole string longer
// than the mask shape allows, so only the value after the colon is inspected.
func TestLooksMaskedAuthHeader(t *testing.T) {
	masked := []string{"Authorization: ****", "Authorization: Bear...oken", "X-Api-Key: sk-a...b123"}
	for _, v := range masked {
		if !looksMaskedAuthHeader(v) {
			t.Errorf("looksMaskedAuthHeader(%q) = false, want true", v)
		}
	}
	real := []string{
		"",
		"Authorization: Bearer a-real-long-token-value",
		"X-Key: value",
		"Authorization:",
	}
	for _, v := range real {
		if looksMaskedAuthHeader(v) {
			t.Errorf("looksMaskedAuthHeader(%q) = true, want false", v)
		}
	}
}

// The admin UI reads a redacted config and PUTs it back, so a masked registry
// auth header must not overwrite the stored credential.
func TestPreserveMCPRegistryAuthHeader(t *testing.T) {
	cur := &config.Config{}
	m := cur.EnsureMCP()
	m.Registries = config.EnsureOfficialRegistry([]*config.MCPRegistrySource{{
		ID:         "internal",
		Name:       "Internal",
		BaseURL:    "https://registry.example.com",
		Enabled:    true,
		AuthHeader: "Authorization: Bearer real-registry-token",
	}})

	next := cur.RedactMCPSecrets()
	preserveMCPSecrets(cur, next)

	src := next.FindMCPRegistry("internal")
	if src == nil {
		t.Fatal("registry source lost")
	}
	if src.AuthHeader != "Authorization: Bearer real-registry-token" {
		t.Errorf("the masked echo overwrote the stored credential: %q", src.AuthHeader)
	}
	if !src.Enabled || src.BaseURL != "https://registry.example.com" {
		t.Errorf("other source fields lost: %+v", src)
	}
	if !src.Builtin && src.ID == config.MCPRegistryOfficialID {
		t.Error("the official registry lost its builtin mark")
	}
	// The official registry seeded by EnsureOfficialRegistry must survive too.
	if next.FindMCPRegistry(config.MCPRegistryOfficialID) == nil {
		t.Error("the official registry disappeared")
	}
}

func TestPreserveMCPRegistryAcceptsNewAuthHeader(t *testing.T) {
	cur := &config.Config{}
	m := cur.EnsureMCP()
	m.Registries = config.EnsureOfficialRegistry([]*config.MCPRegistrySource{{
		ID: "internal", Name: "Internal", BaseURL: "https://registry.example.com",
		AuthHeader: "Authorization: Bearer old-token",
	}})

	next := cur.RedactMCPSecrets()
	next.FindMCPRegistry("internal").AuthHeader = "Authorization: Bearer new-token-value"
	preserveMCPSecrets(cur, next)

	if got := next.FindMCPRegistry("internal").AuthHeader; got != "Authorization: Bearer new-token-value" {
		t.Errorf("a real new header must win, got %q", got)
	}
}

// The panel toggle round-trips through handleMCPSettings, and a later
// full-config PUT that omits the field must not silently re-enable it.
func TestAutoConnectSettingRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Cleanup(func() { config.SetCurrent(nil) })

	on := true
	cur := mcpFixture()
	cur.MCP.AutoConnect = &on
	if err := config.Save(cur); err != nil {
		t.Fatalf("Save: %v", err)
	}
	config.SetCurrent(cur)

	body := strings.NewReader(`{"auto_connect":false}`)
	req := httptest.NewRequest(http.MethodPut, "/admin/mcp/settings", body)
	rec := httptest.NewRecorder()
	handleMCPSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	if stored := config.Load().MCP; stored.AutoConnectEnabled() {
		t.Error("auto_connect=false was not persisted")
	}

	// A redacted round-trip from the panel omits the field; it must survive.
	next := config.Load().RedactMCPSecrets()
	next.MCP.AutoConnect = nil
	preserveMCPSecrets(config.Load(), next)
	if next.MCP.AutoConnectEnabled() {
		t.Error("preserveMCPSecrets dropped the stored auto_connect=false")
	}
}
