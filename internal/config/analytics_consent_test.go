package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ollama-proxy/internal/platform"
)

// writeRawConfigForTest points the config dir at a throwaway directory and
// writes the given config.json contents into it.
func writeRawConfigForTest(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir) // Windows
	t.Setenv("HOME", dir)    // macOS: <home>/Library/Application Support/prism
	if err := os.MkdirAll(platform.ConfigDir(), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(getConfigPath(), []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestAnalyticsOptOutMigration covers the opt-in → opt-out move: an explicit
// "no" from the old prompt is honoured forever, everyone else lands on the new
// default (telemetry on, notice not yet seen).
func TestAnalyticsOptOutMigration(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantOptOut bool
		wantSeen   bool
	}{
		{"never prompted", `{}`, false, false},
		{"opted in", `{"analytics_prompted":true,"analytics_opt_in":true}`, false, true},
		{"declined", `{"analytics_prompted":true,"analytics_opt_in":false}`, true, true},
		{"already migrated", `{"analytics_opt_out":true,"analytics_notice_seen":true}`, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeRawConfigForTest(t, c.body)
			cfg := Load()
			if cfg.AnalyticsOptOut != c.wantOptOut {
				t.Errorf("AnalyticsOptOut = %v, want %v", cfg.AnalyticsOptOut, c.wantOptOut)
			}
			if cfg.AnalyticsNoticeSeen != c.wantSeen {
				t.Errorf("AnalyticsNoticeSeen = %v, want %v", cfg.AnalyticsNoticeSeen, c.wantSeen)
			}
			if cfg.AnalyticsOptIn || cfg.AnalyticsPrompted {
				t.Errorf("legacy consent fields survived migration: opt_in=%v prompted=%v",
					cfg.AnalyticsOptIn, cfg.AnalyticsPrompted)
			}
		})
	}
}

// TestAnalyticsLegacyConsentFieldsDropped checks that the one-time migration is
// written back to disk and is not re-litigated on the next load.
func TestAnalyticsLegacyConsentFieldsDropped(t *testing.T) {
	writeRawConfigForTest(t, `{"analytics_prompted":true,"analytics_opt_in":false}`)
	Load() // migrates and saves

	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "analytics_opt_in") || strings.Contains(string(data), "analytics_prompted") {
		t.Errorf("legacy consent keys are still on disk: %s", data)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if onDisk["analytics_opt_out"] != true {
		t.Errorf("analytics_opt_out on disk = %v, want true", onDisk["analytics_opt_out"])
	}

	cfg := Load()
	if !cfg.AnalyticsOptOut || !cfg.AnalyticsNoticeSeen {
		t.Errorf("second load changed the decision: opt_out=%v notice_seen=%v",
			cfg.AnalyticsOptOut, cfg.AnalyticsNoticeSeen)
	}
}
