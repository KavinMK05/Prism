package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/config"
)

// A registry that answers the official OpenAPI shape. The client should
// produce identical results from the official registry and from a private one,
// which is the whole point of the source abstraction.
func newFakeRegistry(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestRegistryClientUsesSourceBaseURL(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[{"server":{"name":"io.github.alice/weather",
			"description":"Weather","version":"1.0.0",
			"repository":{"url":"https://github.com/alice/weather","source":"github"},
			"packages":[{"registryType":"npm","identifier":"@alice/weather","version":"1.0.0"}]}}],"metadata":{"count":1}}`))
	})

	client := NewRegistryClient(&config.MCPRegistrySource{
		BaseURL:    srv.URL,
		AuthHeader: "Authorization: Bearer private-token",
	})
	items, err := client.Search(context.Background(), "weather", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotPath != "/v0.1/servers" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "search=weather") || !strings.Contains(gotQuery, "version=latest") {
		t.Errorf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer private-token" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items[0]
	if item.Transport != config.MCPTransportStdio {
		t.Errorf("transport = %q", item.Transport)
	}
	if item.Server.Command != "npx" {
		t.Errorf("command = %q", item.Server.Command)
	}
	if !item.NamespaceMatch || !item.Trusted {
		t.Errorf("namespace/repository should match: %+v", item)
	}
}

func TestRegistryClientErrorsNameTheSource(t *testing.T) {
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	})
	client := NewRegistryClient(&config.MCPRegistrySource{BaseURL: srv.URL})
	_, err := client.Search(context.Background(), "", 5)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error should name the source: %v", err)
	}
}

func TestRegistryClientListVersions(t *testing.T) {
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		// The registry API documents the name as a single escaped segment:
		// io.modelcontextprotocol%2Feverything. A raw "/" would be routed as a
		// different path.
		if r.URL.EscapedPath() != "/v0.1/servers/io.github.alice%2Fweather/versions" {
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"servers":[{"server":{"name":"io.github.alice/weather","version":"2.0.0"}},
			{"server":{"name":"io.github.alice/weather","version":"1.0.0"}}]}`))
	})
	versions, err := NewRegistryClient(&config.MCPRegistrySource{BaseURL: srv.URL}).
		ListVersions(context.Background(), "io.github.alice/weather")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 || versions[0] != "2.0.0" || versions[1] != "1.0.0" {
		t.Errorf("versions = %v", versions)
	}
}

func TestRegistryClientListVersionsRequiresName(t *testing.T) {
	client := NewRegistryClient(nil)
	if _, err := client.ListVersions(context.Background(), "  "); err == nil {
		t.Error("an empty name should be rejected")
	}
}

// The trust heuristic: a namespace that names one owner while the repository
// names another must not be reported as trusted.
func TestNamespaceMatchesRepository(t *testing.T) {
	cases := []struct {
		name string
		ns   string
		repo string
		want bool
	}{
		{"matching github owner", "io.github.alice/weather", "https://github.com/alice/weather", true},
		{"matching case-insensitively", "io.github.Alice/weather", "https://github.com/alice/weather", true},
		{"different owner", "io.github.alice/weather", "https://github.com/bob/weather", false},
		{"repo has .git suffix", "io.github.alice/weather", "https://github.com/alice/weather.git", true},
		{"no repository", "io.github.alice/weather", "", false},
		{"dns namespace cannot be cross-checked", "com.example/weather", "https://github.com/example/weather", false},
		{"github enterprise host", "io.github.alice/weather", "https://github.example.com/alice/weather", true},
		{"repo without owner", "io.github.alice/weather", "https://github.com", false},
		{"malformed repo", "io.github.alice/weather", "://nope", false},
	}
	for _, tc := range cases {
		if got := namespaceMatchesRepository(tc.ns, tc.repo); got != tc.want {
			t.Errorf("%s: namespaceMatchesRepository(%q, %q) = %v, want %v", tc.name, tc.ns, tc.repo, got, tc.want)
		}
	}
}

func TestRegistryItemCarriesStatusAndPublisherMeta(t *testing.T) {
	var entry RegistryEntry
	raw := `{"server":{"name":"io.github.alice/weather","description":"d","version":"1.0.0",
		"status":"deprecated","repository":{"url":"https://github.com/alice/weather","source":"github"},
		"remotes":[{"type":"streamable-http","url":"https://weather.example.com/mcp"}]},
		"_meta":{"io.modelcontextprotocol.registry/publisher-provided":{"tool":"publisher-cli"}}}`
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	item, err := RegistryItemFromEntry(entry)
	if err != nil {
		t.Fatalf("RegistryItemFromEntry: %v", err)
	}
	if item.Status != "deprecated" {
		t.Errorf("status = %q", item.Status)
	}
	if item.Deleted {
		t.Error("deprecated is not deleted")
	}
	if item.Publisher != "alice" {
		t.Errorf("publisher = %q", item.Publisher)
	}
	if !strings.Contains(item.PublisherMeta, "publisher-cli") {
		t.Errorf("publisher meta = %q", item.PublisherMeta)
	}
	if item.Transport != config.MCPTransportHTTP {
		t.Errorf("transport = %q", item.Transport)
	}
}

func TestRegistryItemFlagsDeleted(t *testing.T) {
	var entry RegistryEntry
	raw := `{"server":{"name":"io.github.alice/spam","description":"d","version":"1.0.0","status":"deleted",
		"packages":[{"registryType":"npm","identifier":"@alice/spam","version":"1.0.0"}]}}`
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	item, err := RegistryItemFromEntry(entry)
	if err != nil {
		t.Fatalf("RegistryItemFromEntry: %v", err)
	}
	if !item.Deleted {
		t.Error("a deleted server must be flagged so the UI can hide it")
	}
}

func TestApplySource(t *testing.T) {
	item := RegistrySearchItem{}
	item.ApplySource(&config.MCPRegistrySource{ID: "internal", Name: "Internal"})
	if item.SourceID != "internal" || item.SourceName != "Internal" {
		t.Errorf("source not applied: %+v", item)
	}
	item.ApplySource(nil)
	if item.SourceID != "internal" {
		t.Error("a nil source must not clear the existing one")
	}
}

// The registry declares which values are secrets. That must reach the saved
// server config so the UI masks the right fields later rather than guessing
// from the variable name.
func TestRegistryServerConfigCarriesSecretEnv(t *testing.T) {
	var entry RegistryEntry
	raw := `{"server":{"name":"io.github.alice/weather","title":"Weather","description":"d","version":"1.2.3",
		"repository":{"url":"https://github.com/alice/weather","source":"github"},
		"packages":[{"registryType":"npm","identifier":"@alice/weather","version":"1.2.3",
			"fileSha256":"fe333e598595000ae021bd27117db32ec69af6987f507ba7a63c90638ff633ce",
			"environmentVariables":[
				{"name":"WEATHER_API_KEY","isRequired":true,"isSecret":true},
				{"name":"WEATHER_UNITS","default":"metric"}
			]}]}}`
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	srv, env, err := RegistryServerConfig(entry)
	if err != nil {
		t.Fatalf("RegistryServerConfig: %v", err)
	}
	if len(srv.SecretEnv) != 1 || srv.SecretEnv[0] != "WEATHER_API_KEY" {
		t.Errorf("secret env = %v, want [WEATHER_API_KEY]", srv.SecretEnv)
	}
	if srv.Env["WEATHER_UNITS"] != "metric" {
		t.Errorf("default should fill the env value, got %q", srv.Env["WEATHER_UNITS"])
	}
	if srv.IntegritySHA256 != "fe333e598595000ae021bd27117db32ec69af6987f507ba7a63c90638ff633ce" {
		t.Errorf("sha256 not carried: %q", srv.IntegritySHA256)
	}
	if srv.RegistryVersion != "1.2.3" {
		t.Errorf("registry version not carried: %q", srv.RegistryVersion)
	}
	if !srv.Verified {
		t.Error("namespace and repository agree, so the server should be verified")
	}
	if len(env) != 2 {
		t.Errorf("expected both env declarations returned, got %d", len(env))
	}
}

func TestRegistryServerConfigRemoteSecretHeaders(t *testing.T) {
	var entry RegistryEntry
	raw := `{"server":{"name":"io.github.alice/weather","description":"d","version":"1.0.0",
		"repository":{"url":"https://github.com/mallory/weather","source":"github"},
		"remotes":[{"type":"streamable-http","url":"https://weather.example.com/mcp",
			"headers":[{"name":"Authorization","isSecret":true,"isRequired":true},{"name":"X-Tenant"}]}]}}`
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	srv, _, err := RegistryServerConfig(entry)
	if err != nil {
		t.Fatalf("RegistryServerConfig: %v", err)
	}
	if srv.AuthMode != config.MCPAuthStatic {
		t.Errorf("auth mode = %q", srv.AuthMode)
	}
	if len(srv.SecretEnv) != 1 || srv.SecretEnv[0] != "Authorization" {
		t.Errorf("secret headers = %v, want [Authorization]", srv.SecretEnv)
	}
	if srv.Verified {
		t.Error("a mismatched repository must not be verified")
	}
}
