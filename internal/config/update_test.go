package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ollama-proxy/internal/platform"
)

// Concurrent read-modify-write updates must not lose each other's fields. The
// proxy and the tray are separate processes, but the same in-process mutex
// also covers several goroutines in one process (usage polling, MCP refresh).
func TestUpdatePreservesConcurrentWriters(t *testing.T) {
	isolateConfigDir(t)
	if err := Save(defaultConfig()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Update(func(cfg *Config) error {
				cfg.CustomProviders = append(cfg.CustomProviders, &ProviderConfig{
					ID:      fmt.Sprintf("provider-%d", i),
					Name:    fmt.Sprintf("Provider %d", i),
					BaseURL: "https://example.test",
				})
				return nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	loaded := Load()
	if len(loaded.CustomProviders) != writers {
		t.Fatalf("custom providers = %d, want %d (a writer was lost)", len(loaded.CustomProviders), writers)
	}
	seen := map[string]bool{}
	for _, p := range loaded.CustomProviders {
		seen[p.ID] = true
	}
	for i := 0; i < writers; i++ {
		if !seen[fmt.Sprintf("provider-%d", i)] {
			t.Errorf("provider-%d is missing", i)
		}
	}
}

// Updating an unrelated setting must not roll an MCP token back to an older
// snapshot. Every admin handler now uses Update for this reason.
func TestUpdateKeepsNewerMCPToken(t *testing.T) {
	isolateConfigDir(t)
	cfg := defaultConfig()
	cfg.EnsureMCP().Servers = append(cfg.MCP.Servers, &MCPServerConfig{
		ID:        "notion",
		Name:      "Notion",
		Transport: MCPTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Enabled:   true,
		AuthMode:  MCPAuthOAuth,
		OAuth:     &MCPOAuthToken{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Hour).Unix()},
	})
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The proxy refreshes the token after this caller's snapshot was read.
	if _, err := Update(func(fresh *Config) error {
		srv := fresh.FindMCPServer("notion")
		srv.OAuth = &MCPOAuthToken{AccessToken: "at-2", RefreshToken: "rt-2", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		return nil
	}); err != nil {
		t.Fatalf("token Update: %v", err)
	}

	// A later setting change starts from the fresh file, not the stale cfg.
	if _, err := Update(func(fresh *Config) error {
		fresh.DebugLogs = true
		return nil
	}); err != nil {
		t.Fatalf("setting Update: %v", err)
	}

	loaded := Load()
	srv := loaded.FindMCPServer("notion")
	if srv == nil || srv.OAuth == nil {
		t.Fatal("MCP server or token disappeared")
	}
	if srv.OAuth.RefreshToken != "rt-2" {
		t.Errorf("refresh token = %q, want rt-2", srv.OAuth.RefreshToken)
	}
	if !loaded.DebugLogs {
		t.Error("the setting change was not saved")
	}
}

// A malformed config.json is a hard error, not an invitation to overwrite it
// with defaults. That protects provider keys and OAuth tokens after a bad
// hand-edit or a truncated write.
func TestUpdateRefusesMalformedConfig(t *testing.T) {
	isolateConfigDir(t)
	dir := platform.ConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	const malformed = `{"default_provider": "ollama_cloud",`
	if err := os.WriteFile(path, []byte(malformed), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := Update(func(cfg *Config) error {
		cfg.DefaultProvider = "custom"
		return nil
	}); err == nil {
		t.Fatal("Update accepted a malformed config")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != malformed {
		t.Errorf("config.json was rewritten:\n%s", got)
	}
}
