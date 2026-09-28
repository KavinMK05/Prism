package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ollama-proxy/internal/config"
)

// triggerRecorder is a stubbed autoAuthorize: it records the calls and, when
// release is non-nil, blocks until it is closed.
type triggerRecorder struct {
	mu      sync.Mutex
	calls   []string
	waits   []bool
	entered chan struct{}
	release chan struct{}
}

func (r *triggerRecorder) fn(ctx context.Context, serverID string, wait bool) error {
	r.mu.Lock()
	r.calls = append(r.calls, serverID)
	r.waits = append(r.waits, wait)
	r.mu.Unlock()
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.release != nil {
		<-r.release
	}
	return nil
}

func (r *triggerRecorder) snapshot() ([]string, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...), append([]bool(nil), r.waits...)
}

// startAuthFakeUpstream is fakeUpstream behind a bearer check, so a call
// without the token fails with an upstream 401 and succeeds with it.
func startAuthFakeUpstream(t *testing.T, token string) (*fakeUpstream, string) {
	t.Helper()
	f := &fakeUpstream{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="OAuth", error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.handler(w, r)
	}))
	t.Cleanup(ts.Close)
	return f, ts.URL
}

// TestToolsListTriggersAutoConnectWithoutWaiting pins the tools/list half of
// the contract: the trigger fires, but the listing itself never blocks on a
// human approving.
func TestToolsListTriggersAutoConnectWithoutWaiting(t *testing.T) {
	isolateMCPConfig(t)
	as := newFakeAS(t)
	_, resBase := newFakeResource(t, as.URL, protectedResourcePath+"/mcp", true)

	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.EnsureMCP()
	cfg.MCP.Servers = append(cfg.MCP.Servers, oauthTestServer(resBase+"/mcp"))
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	config.Publish(cfg)
	t.Cleanup(func() { config.Publish(nil) })

	mgr := NewManager(config.Current)
	t.Cleanup(mgr.Stop)
	rec := &triggerRecorder{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(rec.release)
	mgr.SetAutoAuthorize(rec.fn)

	g := NewGateway(mgr, config.Current)
	resp := postRPC(g, "/mcp", "application/json", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	result := resultObject(t, decodeRPC(t, resp))
	if tools, _ := result["tools"].([]interface{}); len(tools) != 0 {
		t.Errorf("unauthorized server exposed %d tools", len(tools))
	}

	// The trigger has to have fired before the listing returned.
	select {
	case <-rec.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tools/list never triggered the sign-in")
	}
	calls, waits := rec.snapshot()
	if len(calls) != 1 || calls[0] != "fake" {
		t.Errorf("trigger calls = %v", calls)
	}
	if len(waits) != 1 || waits[0] {
		t.Errorf("tools/list must not wait for the approval, waits = %v", waits)
	}
	if status, ok := mgr.Status("fake"); !ok || status.State != config.MCPStateNeedsAuth {
		t.Errorf("status = %+v, want needs_auth", status)
	}
}

// TestToolsCallWaitsAndRetries covers the tools/call half: the call blocks for
// the approval and is retried once with the stored token.
func TestToolsCallWaitsAndRetries(t *testing.T) {
	isolateMCPConfig(t)
	_, upstreamURL := startAuthFakeUpstream(t, "at-1")

	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	cfg.EnsureMCP()
	cfg.MCP.Servers = append(cfg.MCP.Servers, &config.MCPServerConfig{
		ID:        "fake",
		Name:      "Fake",
		Transport: config.MCPTransportHTTP,
		URL:       upstreamURL,
		Enabled:   true,
		AuthMode:  config.MCPAuthOAuth,
	})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	config.Publish(cfg)
	t.Cleanup(func() { config.Publish(nil) })

	mgr := NewManager(config.Current)
	t.Cleanup(mgr.Stop)
	rec := &triggerRecorder{}
	mgr.SetAutoAuthorize(func(ctx context.Context, serverID string, wait bool) error {
		if err := rec.fn(ctx, serverID, wait); err != nil {
			return err
		}
		// Stand in for the finished sign-in: the callback persists the token.
		return saveServerToken(serverID, &config.MCPOAuthToken{AccessToken: "at-1", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	})

	g := NewGateway(mgr, config.Current)
	resp := postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mcp__fake__search","arguments":{"q":"hello"}}}`)
	result := resultObject(t, decodeRPC(t, resp))
	content, _ := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("retried call returned %v", result)
	}
	if text := content[0].(map[string]interface{})["text"]; text != "ok:search" {
		t.Errorf("retried content = %v", text)
	}
	calls, waits := rec.snapshot()
	if len(calls) != 1 || calls[0] != "fake" {
		t.Errorf("trigger calls = %v", calls)
	}
	if len(waits) != 1 || !waits[0] {
		t.Errorf("tools/call must wait for the approval, waits = %v", waits)
	}
}
