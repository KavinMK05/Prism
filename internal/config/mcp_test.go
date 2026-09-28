package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ollama-proxy/internal/platform"
)

// isolateConfigDir points the platform config dir at a temp directory so tests
// can Load/Save without touching the user's real config.json.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	return tmp
}

func TestMCPIDFromName(t *testing.T) {
	cases := map[string]string{
		"Notion MCP":       "notion-mcp",
		"  Hello!!World  ": "hello-world",
		"":                 "server",
		"UPPER":            "upper",
		"a/b/c":            "a-b-c",
	}
	for in, want := range cases {
		if got := MCPIDFromName(in); got != want {
			t.Errorf("MCPIDFromName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := MCPIDFromName("x"); got != "x" {
		t.Errorf("MCPIDFromName single char = %q", got)
	}
}

func TestUniqueMCPIDReservesPrism(t *testing.T) {
	cfg := &Config{}
	m := cfg.EnsureMCP()
	m.Servers = append(m.Servers, &MCPServerConfig{ID: "notion", Name: "Notion"})

	if got := cfg.UniqueMCPID("slack"); got != "slack" {
		t.Errorf("unused id should be returned unchanged, got %q", got)
	}
	if got := cfg.UniqueMCPID("notion"); got != "notion-2" {
		t.Errorf("collision should suffix -2, got %q", got)
	}
	// "prism" is reserved for the gateway's own downstream entry.
	if got := cfg.UniqueMCPID("prism"); got != "prism-2" {
		t.Errorf("reserved id should be avoided, got %q", got)
	}
}

func TestValidateMCPServer(t *testing.T) {
	cases := []struct {
		name    string
		server  MCPServerConfig
		wantErr bool
	}{
		{"stdio ok", MCPServerConfig{Name: "fs", Transport: MCPTransportStdio, Command: "npx"}, false},
		{"stdio flags in command", MCPServerConfig{Name: "fs", Transport: MCPTransportStdio, Command: "npx -y pkg"}, true},
		{"stdio missing command", MCPServerConfig{Name: "fs", Transport: MCPTransportStdio}, true},
		{"missing name", MCPServerConfig{Transport: MCPTransportHTTP, URL: "https://example.com/mcp"}, true},
		{"missing transport", MCPServerConfig{Name: "x"}, true},
		{"https ok", MCPServerConfig{Name: "x", Transport: MCPTransportHTTP, URL: "https://example.com/mcp"}, false},
		{"loopback http ok", MCPServerConfig{Name: "x", Transport: MCPTransportHTTP, URL: "http://127.0.0.1:8080/mcp"}, false},
		{"remote http rejected", MCPServerConfig{Name: "x", Transport: MCPTransportHTTP, URL: "http://example.com/mcp"}, true},
		{"fragment rejected", MCPServerConfig{Name: "x", Transport: MCPTransportHTTP, URL: "https://example.com/mcp#frag"}, true},
		{"bad scheme", MCPServerConfig{Name: "x", Transport: MCPTransportHTTP, URL: "ftp://example.com/mcp"}, true},
		{"bad auth mode", MCPServerConfig{Name: "x", Transport: MCPTransportStdio, Command: "npx", AuthMode: "oauth2"}, true},
	}
	for _, tc := range cases {
		err := ValidateMCPServer(&tc.server)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
	if ValidateMCPServer(nil) == nil {
		t.Error("nil server should be rejected")
	}
}

func TestRedactMCPSecrets(t *testing.T) {
	cfg := &Config{}
	m := cfg.EnsureMCP()
	m.Servers = append(m.Servers, &MCPServerConfig{
		ID:        "notion",
		Name:      "Notion",
		Transport: MCPTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		AuthMode:  MCPAuthOAuth,
		OAuth: &MCPOAuthToken{
			AccessToken:  "access-token-value-1234",
			RefreshToken: "refresh-token-value-5678",
			ClientSecret: "client-secret-value-90",
		},
		Headers: map[string]string{
			"Authorization": "Bearer static-secret-0001",
			"X-Api-Key":     "api-key-secret-0002",
			"X-Custom":      "not-a-secret",
		},
	})
	m.Clients = map[string]*MCPClientRegistration{
		"https://auth.example.com": {ClientID: "abc", ClientSecret: "dcr-secret-value-01"},
	}

	redacted := cfg.RedactMCPSecrets()
	if redacted == cfg {
		t.Fatal("RedactMCPSecrets returned the original config")
	}
	srv := redacted.FindMCPServer("notion")
	if srv.OAuth.AccessToken == "access-token-value-1234" || srv.OAuth.AccessToken == "" {
		t.Errorf("access token not masked: %q", srv.OAuth.AccessToken)
	}
	if srv.OAuth.RefreshToken == "refresh-token-value-5678" {
		t.Error("refresh token not masked")
	}
	if srv.OAuth.ClientSecret == "client-secret-value-90" {
		t.Error("client secret not masked")
	}
	if srv.Headers["Authorization"] == "Bearer static-secret-0001" {
		t.Error("Authorization header not masked")
	}
	if srv.Headers["X-Api-Key"] == "api-key-secret-0002" {
		t.Error("X-Api-Key header not masked")
	}
	if srv.Headers["X-Custom"] != "not-a-secret" {
		t.Error("non-secret header should be untouched")
	}
	if reg := redacted.MCP.Clients["https://auth.example.com"]; reg.ClientSecret == "dcr-secret-value-01" {
		t.Error("client registration secret not masked")
	}

	// The source config must not be mutated: redaction returns a copy.
	if original := cfg.FindMCPServer("notion"); original.OAuth.AccessToken != "access-token-value-1234" {
		t.Error("source config was mutated by RedactMCPSecrets")
	}
	if cfg.MCP.Clients["https://auth.example.com"].ClientSecret != "dcr-secret-value-01" {
		t.Error("source client cache was mutated by RedactMCPSecrets")
	}
}

func TestMCPServersForAgentScoping(t *testing.T) {
	cfg := &Config{}
	m := cfg.EnsureMCP()
	m.Servers = []*MCPServerConfig{
		{ID: "notes", Name: "Notes", Enabled: true},
		{ID: "github", Name: "GitHub", Enabled: true},
		{ID: "disabled-one", Name: "Disabled", Enabled: false},
	}

	// Empty allowlist: the agent reaches nothing (opt-in model).
	if got := cfg.MCPServersForAgent("claude-code"); len(got) != 0 {
		t.Errorf("empty allowlist should expose nothing, got %d servers", len(got))
	}

	// Duplicates are collapsed and disabled servers are excluded.
	cfg.SetAgentMCPServers("claude-code", []string{"notes", "notes", "github", "disabled-one", ""})
	got := cfg.MCPServersForAgent("claude-code")
	if len(got) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(got))
	}
	if got[0].ID != "notes" || got[1].ID != "github" {
		t.Errorf("unexpected servers: %s, %s", got[0].ID, got[1].ID)
	}
	if ids := cfg.MCP.AgentServers["claude-code"]; len(ids) != 3 {
		t.Errorf("dedupe failed in stored allowlist: %v", ids)
	}

	// Removing a server purges every per-agent reference to it.
	if !cfg.RemoveMCPServer("notes") {
		t.Fatal("RemoveMCPServer returned false")
	}
	for agent, ids := range cfg.MCP.AgentServers {
		for _, id := range ids {
			if id == "notes" {
				t.Errorf("agent %s still references removed server", agent)
			}
		}
	}
	if cfg.RemoveMCPServer("notes") {
		t.Error("removing an unknown server should return false")
	}
}

func TestMCPConfigRoundTrip(t *testing.T) {
	isolateConfigDir(t)

	cfg := Load()
	cfg.EnsureMCP()
	cfg.MCP.Servers = append(cfg.MCP.Servers, &MCPServerConfig{
		ID:        "notion",
		Name:      "Notion MCP",
		Source:    MCPSourceManual,
		Transport: MCPTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Enabled:   true,
		AuthMode:  MCPAuthOAuth,
		OAuth: &MCPOAuthToken{
			AccessToken:      "at-1234567890",
			RefreshToken:     "rt-1234567890",
			ExpiresAt:        2000000000,
			ASURL:            "https://mcp.notion.com",
			RegistrationMode: "dcr",
		},
	})
	cfg.MCP.IdleTimeoutSec = 120
	cfg.SetAgentMCPServers("claude-code", []string{"notion"})
	cfg.MCP.Clients = map[string]*MCPClientRegistration{
		"https://mcp.notion.com": {ClientID: "client-abc", TokenAuthMethod: "none", RegistrationMode: "dcr"},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The file must exist under the platform config dir and be owner-only on
	// platforms that can express that.
	path := filepath.Join(platform.ConfigDir(), "config.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.json not written: %v", err)
	}

	loaded := Load()
	srv := loaded.FindMCPServer("notion")
	if srv == nil {
		t.Fatal("server did not survive the round trip")
	}
	if srv.OAuth == nil || srv.OAuth.AccessToken != "at-1234567890" || srv.OAuth.RefreshToken != "rt-1234567890" {
		t.Errorf("token did not survive: %+v", srv.OAuth)
	}
	if loaded.MCP.IdleTimeoutSec != 120 {
		t.Errorf("idle timeout = %d, want 120", loaded.MCP.IdleTimeoutSec)
	}
	if ids := loaded.MCP.AgentServers["claude-code"]; len(ids) != 1 || ids[0] != "notion" {
		t.Errorf("agent allowlist did not survive: %v", ids)
	}
	if reg := loaded.MCP.Clients["https://mcp.notion.com"]; reg == nil || reg.ClientID != "client-abc" {
		t.Errorf("client registration did not survive: %+v", reg)
	}

	// A config file without an MCP section still gets a usable one.
	if err := os.WriteFile(path, []byte(`{"default_provider":"ollama_cloud"}`), 0600); err != nil {
		t.Fatal(err)
	}
	fresh := Load()
	if fresh.MCP == nil || fresh.MCP.Servers == nil || fresh.MCP.AgentServers == nil || fresh.MCP.Clients == nil {
		t.Error("EnsureMCP did not initialize the MCP section on Load")
	}
	if fresh.MCP.IdleTimeoutSec != MCPDefaultIdleTimeoutSec {
		t.Errorf("default idle timeout = %d, want %d", fresh.MCP.IdleTimeoutSec, MCPDefaultIdleTimeoutSec)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mustReadFile(t, path), &raw); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// The marketplace fields must survive Clone, or a redacted config served to
// the UI would silently drop the provenance it is meant to display.
func TestCloneMCPServerCarriesMarketplaceFields(t *testing.T) {
	original := &MCPServerConfig{
		ID:               "weather",
		Name:             "Weather",
		SecretEnv:        []string{"API_KEY"},
		RegistrySourceID: "internal",
		RegistryVersion:  "1.2.3",
		Verified:         true,
		IntegritySHA256:  "abc",
	}
	clone := cloneMCPServer(original)
	if clone == original {
		t.Fatal("clone returned the same pointer")
	}
	// The slice must be copied, not shared.
	clone.SecretEnv[0] = "CHANGED"
	if original.SecretEnv[0] != "API_KEY" {
		t.Error("SecretEnv was shared between the clone and the original")
	}
	if clone.RegistrySourceID != "internal" || clone.RegistryVersion != "1.2.3" || !clone.Verified || clone.IntegritySHA256 != "abc" {
		t.Errorf("marketplace fields lost in the clone: %+v", clone)
	}
}

func TestEnsureOfficialRegistry(t *testing.T) {
	// A fresh install gets the official registry.
	got := EnsureOfficialRegistry(nil)
	if len(got) != 1 || got[0].ID != MCPRegistryOfficialID || !got[0].Builtin || !got[0].Enabled {
		t.Fatalf("official registry not seeded: %+v", got)
	}

	// A config saved before the marketplace existed is upgraded without losing
	// its own sources, and the seeded one is not duplicated.
	existing := []*MCPRegistrySource{{ID: "internal", Name: "Internal", BaseURL: "https://registry.example.com"}}
	got = EnsureOfficialRegistry(existing)
	if len(got) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(got))
	}
	if got[0].ID != MCPRegistryOfficialID || got[1].ID != "internal" {
		t.Errorf("official registry should come first: %v, %v", got[0].ID, got[1].ID)
	}

	// Running it twice is idempotent.
	if again := EnsureOfficialRegistry(got); len(again) != 2 {
		t.Errorf("not idempotent: %d sources", len(again))
	}

	// Entries with no base URL or a duplicate id are dropped.
	messy := []*MCPRegistrySource{
		{ID: "official", BaseURL: "https://registry.modelcontextprotocol.io"},
		{ID: "official", BaseURL: "https://dupe.example.com"},
		{ID: "blank", BaseURL: "  "},
	}
	got = EnsureOfficialRegistry(messy)
	if len(got) != 1 || got[0].BaseURL != MCPRegistryOfficialURL {
		t.Errorf("dedupe/filter failed: %+v", got)
	}
}

func TestRemoveMCPRegistryProtectsBuiltin(t *testing.T) {
	cfg := &Config{}
	cfg.EnsureMCP()
	cfg.MCP.Registries = EnsureOfficialRegistry([]*MCPRegistrySource{
		{ID: "internal", Name: "Internal", BaseURL: "https://registry.example.com"},
	})

	if cfg.RemoveMCPRegistry(MCPRegistryOfficialID) {
		t.Error("the official registry must not be removable")
	}
	if cfg.FindMCPRegistry(MCPRegistryOfficialID) == nil {
		t.Error("the official registry disappeared")
	}
	if !cfg.RemoveMCPRegistry("internal") {
		t.Error("a user-added source should be removable")
	}
	if cfg.FindMCPRegistry("internal") != nil {
		t.Error("the removed source is still present")
	}
	if cfg.RemoveMCPRegistry("internal") {
		t.Error("removing an unknown source should return false")
	}
}

func TestUniqueMCPRegistryID(t *testing.T) {
	cfg := &Config{}
	cfg.EnsureMCP()
	cfg.MCP.Registries = EnsureOfficialRegistry(nil)

	if got := cfg.UniqueMCPRegistryID("internal"); got != "internal" {
		t.Errorf("free id changed: %q", got)
	}
	// The seeded official id is taken.
	if got := cfg.UniqueMCPRegistryID(MCPRegistryOfficialID); got == MCPRegistryOfficialID {
		t.Errorf("a taken id must be suffixed, got %q", got)
	}
}

func TestValidateMCPRegistrySource(t *testing.T) {
	cases := []struct {
		name    string
		source  MCPRegistrySource
		wantErr bool
	}{
		{"https ok", MCPRegistrySource{Name: "x", BaseURL: "https://registry.example.com"}, false},
		{"loopback http ok", MCPRegistrySource{Name: "x", BaseURL: "http://127.0.0.1:9000"}, false},
		{"remote http rejected", MCPRegistrySource{Name: "x", BaseURL: "http://registry.example.com"}, true},
		{"missing name", MCPRegistrySource{BaseURL: "https://registry.example.com"}, true},
		{"missing host", MCPRegistrySource{Name: "x", BaseURL: "https://"}, true},
		{"bad scheme", MCPRegistrySource{Name: "x", BaseURL: "ftp://registry.example.com"}, true},
		{"fragment rejected", MCPRegistrySource{Name: "x", BaseURL: "https://registry.example.com#frag"}, true},
	}
	for _, tc := range cases {
		err := ValidateMCPRegistrySource(&tc.source)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
	if ValidateMCPRegistrySource(nil) == nil {
		t.Error("nil source should be rejected")
	}
}

func TestRedactMCPRegistryAuthHeader(t *testing.T) {
	cfg := &Config{}
	m := cfg.EnsureMCP()
	m.Registries = EnsureOfficialRegistry([]*MCPRegistrySource{{
		ID:         "internal",
		Name:       "Internal",
		BaseURL:    "https://registry.example.com",
		AuthHeader: "Authorization: Bearer super-secret-token",
	}})

	redacted := cfg.RedactMCPSecrets()
	src := redacted.FindMCPRegistry("internal")
	if src == nil {
		t.Fatal("source lost")
	}
	if src.AuthHeader == "Authorization: Bearer super-secret-token" {
		t.Error("registry auth header was not masked")
	}
	if !strings.HasPrefix(src.AuthHeader, "Authorization:") {
		t.Errorf("the header name should survive masking, got %q", src.AuthHeader)
	}
	if strings.Contains(src.AuthHeader, "super-secret-token") {
		t.Errorf("the credential leaked into the redacted form: %q", src.AuthHeader)
	}
	// The source config must not be mutated by redaction.
	if original := cfg.FindMCPRegistry("internal"); original.AuthHeader != "Authorization: Bearer super-secret-token" {
		t.Error("redaction mutated the source config")
	}

	// A header with no colon is masked whole.
	cfg2 := &Config{}
	m2 := cfg2.EnsureMCP()
	m2.Registries = EnsureOfficialRegistry([]*MCPRegistrySource{{
		ID: "bare", Name: "Bare", BaseURL: "https://registry.example.com", AuthHeader: "opaque-credential-value",
	}})
	if got := cfg2.RedactMCPSecrets().FindMCPRegistry("bare").AuthHeader; got == "opaque-credential-value" {
		t.Error("a colon-less auth header was not masked")
	}
}

func TestMCPRegistryRoundTrip(t *testing.T) {
	isolateConfigDir(t)

	cfg := Load()
	cfg.EnsureMCP()
	cfg.MCP.Registries = append(cfg.MCP.Registries, &MCPRegistrySource{
		ID:         "internal",
		Name:       "Internal Registry",
		BaseURL:    "https://registry.example.com",
		Enabled:    false,
		AuthHeader: "X-Api-Key: secret-key",
	})
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded := Load()
	src := loaded.FindMCPRegistry("internal")
	if src == nil {
		t.Fatal("registry source did not survive the round trip")
	}
	if src.BaseURL != "https://registry.example.com" || src.Enabled || src.AuthHeader != "X-Api-Key: secret-key" {
		t.Errorf("source fields lost: %+v", src)
	}
	if loaded.FindMCPRegistry(MCPRegistryOfficialID) == nil {
		t.Error("the official registry was not seeded on load")
	}
	// Disabled sources are excluded from the search set.
	enabled := loaded.EnabledMCPRegistries()
	for _, s := range enabled {
		if s.ID == "internal" {
			t.Error("a disabled source should not be searched")
		}
	}
}

func TestAutoConnectEnabledDefault(t *testing.T) {
	var nilCfg *MCPConfig
	if !nilCfg.AutoConnectEnabled() {
		t.Error("a missing MCP section must leave auto-connect on")
	}
	if !(&MCPConfig{}).AutoConnectEnabled() {
		t.Error("a config written before this setting existed must leave auto-connect on")
	}
	off := false
	if (&MCPConfig{AutoConnect: &off}).AutoConnectEnabled() {
		t.Error("an explicit false must turn auto-connect off")
	}
	on := true
	if !(&MCPConfig{AutoConnect: &on}).AutoConnectEnabled() {
		t.Error("an explicit true must keep auto-connect on")
	}
}

func TestCloneMCPCopiesAutoConnect(t *testing.T) {
	off := false
	original := &MCPConfig{AutoConnect: &off}
	clone := cloneMCP(original)
	if clone == original {
		t.Fatal("clone returned the same pointer")
	}
	if clone.AutoConnect == original.AutoConnect {
		t.Fatal("AutoConnect pointer was shared with the clone")
	}
	*clone.AutoConnect = true
	if *original.AutoConnect {
		t.Error("mutating the clone's AutoConnect changed the original")
	}
	if cloneMCP(&MCPConfig{}).AutoConnect != nil {
		t.Error("a nil AutoConnect must stay nil through the clone")
	}
}
