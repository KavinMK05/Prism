package db

import (
	"os"
	"testing"
	"time"

	"ollama-proxy/internal/platform"
)

func TestRequestCountSince(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir) // Windows
	t.Setenv("HOME", dir)    // macOS
	if err := os.MkdirAll(platform.ConfigDir(), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer Close()

	now := time.Now()
	record := func(ts time.Time) {
		t.Helper()
		if err := RecordRequest(RequestStats{Timestamp: ts, Model: "m", Provider: "p", Client: "c"}); err != nil {
			t.Fatalf("RecordRequest: %v", err)
		}
	}
	record(now.Add(-48 * time.Hour)) // outside the window
	record(now)
	record(now)

	count, ok := RequestCountSince(now.Add(-24 * time.Hour).Unix())
	if !ok {
		t.Fatal("ok = false with an open database")
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (the stale request must not count)", count)
	}
}

func TestRequestCountSinceWithoutDB(t *testing.T) {
	Close() // make sure no earlier test left a handle open
	if count, ok := RequestCountSince(time.Now().Unix()); ok || count != 0 {
		t.Errorf("RequestCountSince() = (%d, %v) with no database, want (0, false)", count, ok)
	}
}
