package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestConnectEndpointsMatchMainRoutes reads main.go and asserts every path the
// Connect panel advertises is actually registered there. The endpoint list is
// hand-maintained next to that mux, so this is what stops it going stale.
func TestConnectEndpointsMatchMainRoutes(t *testing.T) {
	source, err := os.ReadFile("../../main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	main := string(source)

	for _, ep := range connectEndpoints() {
		target := `mux.HandleFunc("` + ep.Path + `"`
		if !strings.Contains(main, target) {
			t.Errorf("endpoint %s %s is documented but not registered in main.go (looked for %s)", ep.Method, ep.Path, target)
		}
	}
}

// TestConnectEndpointsOmitUnregisteredRoutes pins the two routes that are easy
// to add by mistake: the Anthropic token counter exists only to 404, and
// Prism has no downstream Ollama-native /api/chat (/api/chat is upstream-only).
func TestConnectEndpointsOmitUnregisteredRoutes(t *testing.T) {
	for _, ep := range connectEndpoints() {
		if ep.Path == "/v1/messages/count_tokens" {
			t.Errorf("count_tokens is a 404 stub and must not be advertised")
		}
		if ep.Path == "/api/chat" {
			t.Errorf("/api/chat is not a registered Prism route (it is an upstream provider URL)")
		}
	}
}

// TestBuildConnectInfoUsesLivePorts verifies the URL assembly honours the
// proxy/admin env overrides and that the shared token is present.
func TestBuildConnectInfoUsesLivePorts(t *testing.T) {
	t.Setenv("PRISM_HOST", "127.0.0.1")
	t.Setenv("PRISM_PORT", "12345")
	t.Setenv("PRISM_ADMIN_PORT", "9999")

	info := buildConnectInfo()

	if info.Token != "prism" {
		t.Errorf("token = %q, want %q", info.Token, "prism")
	}

	services := map[string]connectServiceInfo{}
	for _, s := range info.Services {
		services[s.ID] = s
	}

	if got := services["proxy"].URL; got != "http://127.0.0.1:12345" {
		t.Errorf("proxy URL = %q, want http://127.0.0.1:12345", got)
	}
	if got := services["admin"].URL; got != "http://127.0.0.1:9999/admin" {
		t.Errorf("admin URL = %q, want http://127.0.0.1:9999/admin", got)
	}
	searxng, ok := services["searxng"]
	if !ok {
		t.Fatalf("searxng service missing")
	}
	if !strings.HasPrefix(searxng.URL, "http://127.0.0.1:") {
		t.Errorf("searxng URL = %q, want a 127.0.0.1 URL", searxng.URL)
	}
	if searxng.Installed == nil {
		t.Errorf("searxng installed flag should always be present")
	}

	if got := info.MCP.AggregateURL; got != "http://127.0.0.1:12345/mcp" {
		t.Errorf("MCP aggregate URL = %q, want http://127.0.0.1:12345/mcp", got)
	}
	if got := info.MCP.PerAgentPattern; got != "http://127.0.0.1:12345/mcp/<agent>" {
		t.Errorf("MCP per-agent pattern = %q, want http://127.0.0.1:12345/mcp/<agent>", got)
	}
	if len(info.MCP.Agents) == 0 {
		t.Fatalf("expected MCP-capable agents, got none")
	}
	for _, a := range info.MCP.Agents {
		if want := "http://127.0.0.1:12345" + a.Path; a.URL != want {
			t.Errorf("agent %s URL = %q, want %q", a.ID, a.URL, want)
		}
	}
}

// TestConnectAgentEntriesAreWellFormed checks the agent list has no duplicates
// and each entry carries the fields the UI renders.
func TestConnectAgentEntriesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range connectMCPAgents(12345) {
		if a.ID == "" || a.Name == "" || a.Path == "" {
			t.Errorf("agent entry incomplete: %+v", a)
		}
		if seen[a.ID] {
			t.Errorf("agent %s listed twice", a.ID)
		}
		seen[a.ID] = true
		if want := "/mcp/" + a.ID; a.Path != want {
			t.Errorf("agent %s path = %q, want %q", a.ID, a.Path, want)
		}
	}
	if len(seen) == 0 {
		t.Fatalf("expected MCP-capable agents, got none")
	}
}

// TestHandleAdminConnectMethod pins GET-only behaviour.
func TestHandleAdminConnectMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAdminConnect(rec, httptest.NewRequest(http.MethodPost, "/admin/connect", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	rec = httptest.NewRecorder()
	handleAdminConnect(rec, httptest.NewRequest(http.MethodGet, "/admin/connect", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("GET Content-Type = %q, want JSON", ct)
	}
}
