package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"ollama-proxy/internal/config"
)

// autoConnectFixture publishes a config holding one OAuth-marked server that
// has no token, and returns the manager plus the live config.
func autoConnectFixture(t *testing.T) (*Manager, *config.Config) {
	t.Helper()
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
	mgr.SetAutoAuthorize(mgr.AutoAuthorize)
	t.Cleanup(mgr.Stop)
	return mgr, cfg
}

// completingOpener stands in for the browser: it reads the state and the
// loopback callback out of the authorize URL and completes the flow with the
// given query fragment (e.g. "code=test-code" or "error=access_denied").
func completingOpener(t *testing.T, calls *int32, extraQuery string) func(string) error {
	t.Helper()
	return func(rawURL string) error {
		atomic.AddInt32(calls, 1)
		u, err := url.Parse(rawURL)
		if err != nil {
			return err
		}
		q := u.Query()
		target := q.Get("redirect_uri") + "?" + extraQuery
		if extraQuery == "code=test-code" {
			target += "&state=" + url.QueryEscape(q.Get("state"))
		}
		resp, err := http.Get(target)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
}

func TestAutoAuthorizeSignsInAndStoresToken(t *testing.T) {
	old := openAuthURL
	t.Cleanup(func() { openAuthURL = old })

	mgr, _ := autoConnectFixture(t)
	var calls int32
	openAuthURL = completingOpener(t, &calls, "code=test-code")

	if err := mgr.AutoAuthorize(context.Background(), "fake", true); err != nil {
		t.Fatalf("AutoAuthorize: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("opener ran %d times, want 1", got)
	}
	if got := config.Load().FindMCPServer("fake"); got == nil || got.OAuth == nil || got.OAuth.AccessToken != "at-1" {
		t.Errorf("token was not persisted: %+v", got.OAuth)
	}
}

// An unknown 401 must never open a browser: discovery follows URLs the 401'ing
// server controls, so only a server the user vouched for may be auto-opened.
func TestAutoAuthorizeRequiresOAuthMode(t *testing.T) {
	old := openAuthURL
	t.Cleanup(func() { openAuthURL = old })

	mgr, cfg := autoConnectFixture(t)
	cfg.FindMCPServer("fake").AuthMode = config.MCPAuthNone
	var calls int32
	openAuthURL = completingOpener(t, &calls, "code=test-code")

	if err := mgr.AutoAuthorize(context.Background(), "fake", true); !errors.Is(err, errAutoConnectPending) {
		t.Errorf("error = %v, want errAutoConnectPending", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("opener ran %d times for a non-OAuth server, want 0", got)
	}
}

func TestAutoAuthorizeDisabledBySetting(t *testing.T) {
	old := openAuthURL
	t.Cleanup(func() { openAuthURL = old })

	mgr, cfg := autoConnectFixture(t)
	off := false
	cfg.MCP.AutoConnect = &off
	var calls int32
	openAuthURL = completingOpener(t, &calls, "code=test-code")

	if err := mgr.AutoAuthorize(context.Background(), "fake", true); !errors.Is(err, errAutoConnectPending) {
		t.Errorf("error = %v, want errAutoConnectPending", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("opener ran %d times with auto-connect off, want 0", got)
	}
}

// A refresh token that a transient failure kept Prism from using must not pop a
// browser: the next call retries the refresh instead.
func TestAutoAuthorizeSkipsWithRefreshToken(t *testing.T) {
	old := openAuthURL
	t.Cleanup(func() { openAuthURL = old })

	mgr, cfg := autoConnectFixture(t)
	cfg.FindMCPServer("fake").OAuth = &config.MCPOAuthToken{RefreshToken: "rt-1"}
	var calls int32
	openAuthURL = completingOpener(t, &calls, "code=test-code")

	if err := mgr.AutoAuthorize(context.Background(), "fake", true); !errors.Is(err, errAutoConnectPending) {
		t.Errorf("error = %v, want errAutoConnectPending", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("opener ran %d times for a server with a refresh token, want 0", got)
	}
}

// A second trigger while the window is open joins the pending flow instead of
// opening a second browser.
func TestAutoAuthorizeJoinsPendingFlow(t *testing.T) {
	old := openAuthURL
	t.Cleanup(func() { openAuthURL = old })

	mgr, _ := autoConnectFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	openAuthURL = func(rawURL string) error {
		atomic.AddInt32(&calls, 1)
		close(entered)
		<-release
		u, _ := url.Parse(rawURL)
		q := u.Query()
		resp, err := http.Get(q.Get("redirect_uri") + "?code=test-code&state=" + url.QueryEscape(q.Get("state")))
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	first := make(chan error, 1)
	go func() { first <- mgr.AutoAuthorize(context.Background(), "fake", true) }()
	<-entered

	second := make(chan error, 1)
	go func() { second <- mgr.AutoAuthorize(context.Background(), "fake", true) }()
	time.Sleep(200 * time.Millisecond) // let the second call reach the pending flow
	close(release)

	if err := <-first; err != nil {
		t.Errorf("first call: %v", err)
	}
	if err := <-second; err != nil {
		t.Errorf("second call: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("opener ran %d times, want 1 (the second call must join the flow)", got)
	}
}

func TestAutoAuthorizeCooldownAfterFailure(t *testing.T) {
	old := openAuthURL
	t.Cleanup(func() { openAuthURL = old })

	mgr, _ := autoConnectFixture(t)
	var calls int32
	openAuthURL = completingOpener(t, &calls, "error=access_denied")

	if err := mgr.AutoAuthorize(context.Background(), "fake", true); err == nil {
		t.Fatal("expected the denied sign-in to report an error")
	}
	if err := mgr.AutoAuthorize(context.Background(), "fake", true); !errors.Is(err, errAutoConnectPending) {
		t.Errorf("second call error = %v, want errAutoConnectPending", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("opener ran %d times, want 1 (the second attempt is inside the cooldown)", got)
	}
}
