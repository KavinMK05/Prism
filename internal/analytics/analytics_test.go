package analytics

import (
	"bytes"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/db"
	"ollama-proxy/internal/platform"
)

// useTempConfigDir points the platform config dir at a throwaway directory so
// these tests never read or write the real analytics state.
func useTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir) // Windows
	t.Setenv("HOME", dir)    // macOS: <home>/Library/Application Support/prism
}

// syncBuffer is a goroutine-safe log sink. The heartbeat is delivered from its
// own goroutine, so a plain bytes.Buffer would race with the test reading it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog redirects the standard logger into a buffer for the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

func TestUsageBucket(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-1, "0"},
		{0, "0"},
		{1, "1-9"},
		{9, "1-9"},
		{10, "10-99"},
		{99, "10-99"},
		{100, "100+"},
		{5000, "100+"},
	}
	for _, c := range cases {
		if got := usageBucket(c.n); got != c.want {
			t.Errorf("usageBucket(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestEnabled(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("PRISM_ANALYTICS_DISABLED", "")

	// No live config yet: nothing to send.
	config.Publish(nil)
	if Enabled() {
		t.Error("Enabled() = true with no live config, want false")
	}

	// Telemetry is on by default.
	config.Publish(&config.Config{})
	if !Enabled() {
		t.Error("Enabled() = false by default, want true")
	}

	// An explicit opt-out wins.
	config.Publish(&config.Config{AnalyticsOptOut: true, AnalyticsNoticeSeen: true})
	if Enabled() {
		t.Error("Enabled() = true after opt-out, want false")
	}

	// The environment kill switch wins over everything.
	t.Setenv("PRISM_ANALYTICS_DISABLED", "1")
	config.Publish(&config.Config{})
	if Enabled() {
		t.Error("Enabled() = true with PRISM_ANALYTICS_DISABLED=1, want false")
	}
	if !ForcedOff() {
		t.Error("ForcedOff() = false with PRISM_ANALYTICS_DISABLED=1, want true")
	}
}

func TestBuildPayload(t *testing.T) {
	payload := buildPayload("id-1", "0.5.2", map[string]any{"used_today": true, "requests_24h": "10-99"})

	if payload["event"] != "app_heartbeat" {
		t.Errorf("event = %v, want app_heartbeat", payload["event"])
	}
	if payload["api_key"] != posthogProjectKey {
		t.Errorf("api_key = %v, want the public project key", payload["api_key"])
	}
	props, ok := payload["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T, want map[string]any", payload["properties"])
	}
	for k, want := range map[string]any{
		"distinct_id":  "id-1",
		"version":      "0.5.2",
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"used_today":   true,
		"requests_24h": "10-99",
	} {
		if props[k] != want {
			t.Errorf("properties[%q] = %v, want %v", k, props[k], want)
		}
	}

	// An unreadable stats DB omits the activity fields instead of claiming the
	// user was inactive.
	bare, ok := buildPayload("id-1", "0.5.2", nil)["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing without usage")
	}
	if _, present := bare["used_today"]; present {
		t.Error("used_today should be omitted when the stats DB is unreadable")
	}
	if _, present := bare["requests_24h"]; present {
		t.Error("requests_24h should be omitted when the stats DB is unreadable")
	}
}

func TestUsagePropertiesReadsStatsDB(t *testing.T) {
	useTempConfigDir(t)
	if err := os.MkdirAll(platform.ConfigDir(), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := db.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer db.Close()

	props := usageProperties(time.Now())
	if props["used_today"] != false {
		t.Errorf("used_today = %v with no traffic, want false", props["used_today"])
	}
	if props["requests_24h"] != "0" {
		t.Errorf("requests_24h = %v with no traffic, want 0", props["requests_24h"])
	}

	if err := db.RecordRequest(db.RequestStats{Timestamp: time.Now(), Model: "m", Provider: "p", Client: "c"}); err != nil {
		t.Fatalf("RecordRequest: %v", err)
	}
	props = usageProperties(time.Now())
	if props["used_today"] != true {
		t.Errorf("used_today = %v after a request, want true", props["used_today"])
	}
	if props["requests_24h"] != "1-9" {
		t.Errorf("requests_24h = %v after one request, want 1-9", props["requests_24h"])
	}

	// Traffic older than the 24h window must not keep the install "active".
	if err := db.RecordRequest(db.RequestStats{Timestamp: time.Now().Add(-48 * time.Hour), Model: "m", Provider: "p", Client: "c"}); err != nil {
		t.Fatalf("RecordRequest (stale): %v", err)
	}
	if props := usageProperties(time.Now()); props["requests_24h"] != "1-9" {
		t.Errorf("requests_24h = %v with one stale request, want 1-9", props["requests_24h"])
	}
}

func TestPingIfDueSendsOncePerDay(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("PRISM_ANALYTICS_DISABLED", "")
	t.Setenv("PRISM_ANALYTICS_DEBUG", "1")
	config.Publish(&config.Config{})
	out := captureLog(t)

	PingIfDue("test")
	PingIfDue("test")

	if got := waitForLog(out, "would send"); !strings.Contains(got, `"event":"app_heartbeat"`) {
		t.Fatalf("heartbeat payload was not logged, got %q", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(out.String(), "would send"); n != 1 {
		t.Errorf("sent %d heartbeats in one day, want 1:\n%s", n, out.String())
	}

	// The anonymous ID is persisted for the next day's ping.
	id, lastPing := readState()
	if id == "" {
		t.Error("no anonymous id was stored")
	}
	if lastPing != today() {
		t.Errorf("last ping = %q, want %q", lastPing, today())
	}
}

func TestPingIfDueRespectsOptOut(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("PRISM_ANALYTICS_DISABLED", "")
	t.Setenv("PRISM_ANALYTICS_DEBUG", "1")
	config.Publish(&config.Config{AnalyticsOptOut: true, AnalyticsNoticeSeen: true})
	out := captureLog(t)

	PingIfDue("test")
	time.Sleep(100 * time.Millisecond)

	if strings.Contains(out.String(), "would send") {
		t.Errorf("an opted-out install sent a heartbeat:\n%s", out.String())
	}
	if _, err := os.Stat(stateFilePath()); !os.IsNotExist(err) {
		t.Errorf("an opted-out install wrote %s (err = %v)", stateFilePath(), err)
	}
}

func TestPingIfDueRespectsKillSwitch(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("PRISM_ANALYTICS_DISABLED", "1")
	t.Setenv("PRISM_ANALYTICS_DEBUG", "1")
	config.Publish(&config.Config{})
	out := captureLog(t)

	PingIfDue("test")
	time.Sleep(100 * time.Millisecond)

	if strings.Contains(out.String(), "would send") {
		t.Errorf("telemetry ignored PRISM_ANALYTICS_DISABLED=1:\n%s", out.String())
	}
}

// waitForLog polls until the marker appears (the send runs in a goroutine) or
// the deadline passes, then returns everything captured so far.
func waitForLog(buf *syncBuffer, marker string) string {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), marker) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return buf.String()
}
