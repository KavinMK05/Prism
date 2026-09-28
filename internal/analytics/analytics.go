// Package analytics implements anonymous usage telemetry for Prism.
//
// It sends a single daily "heartbeat" event to PostHog (EU) so the maintainer
// can count active installs and active users. Telemetry is on by default and
// can be turned off from the tray menu, the admin UI settings, or hard-disabled
// with PRISM_ANALYTICS_DISABLED=1. PRISM_ANALYTICS_DEBUG=1 logs the payload
// without sending it.
//
// The only data ever transmitted is: a random anonymous install ID, the app
// version, the OS, the CPU architecture, whether Prism proxied at least one
// request in the last 24 hours, and a coarse bucket of that request count. No
// prompts, models, requests, tokens, keys, URLs, or identifiers are ever sent.
package analytics

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/db"
	"ollama-proxy/internal/platform"
)

const (
	// posthogEndpoint is the PostHog EU capture endpoint.
	posthogEndpoint = "https://eu.i.posthog.com/capture/"
	// posthogProjectKey is the public project key (phc_...). It is ingest-only
	// by design and safe to ship in an open-source binary: it can write events
	// but cannot read, delete, or access the account.
	posthogProjectKey = "phc_B5Lb2LbtXjYCkKkxPxpDUh95tBVG9grKhQKvJjNWZJPZ"
	// heartbeatInterval is how often the tray re-checks for a daily ping.
	heartbeatInterval = 24 * time.Hour
	// httpTimeout bounds a single heartbeat request. Telemetry is low priority.
	httpTimeout = 10 * time.Second
	// usageWindow is how far back we look for proxied requests when deciding
	// whether this install counts as active today.
	usageWindow = 24 * time.Hour
)

// mu guards the state file read/write and the once-per-day dedup check so that
// concurrent callers (tray loop + admin opt-out handler + tray menu toggle)
// never double-send.
var mu sync.Mutex

// stateFilePath returns the path to the analytics state file. It holds only
// the anonymous ID and the last-ping date, kept separate from config.json so a
// user pasting their config into a bug report cannot leak their tracker ID.
func stateFilePath() string {
	return platform.ConfigDir() + "/analytics_id.txt"
}

// disabled reports whether the hard kill switch is set. When set, telemetry is
// never sent and the first-run notice is never shown.
func disabled() bool {
	return os.Getenv("PRISM_ANALYTICS_DISABLED") == "1"
}

// debug reports whether debug mode is set. When set, the payload is logged but
// not sent, so a skeptical user can verify exactly what would leave the machine.
func debug() bool {
	return os.Getenv("PRISM_ANALYTICS_DEBUG") == "1"
}

// ForcedOff reports whether telemetry is disabled by the environment kill
// switch. It wins over every other setting, including the in-app toggle.
func ForcedOff() bool {
	return disabled()
}

// Enabled reports whether telemetry should be sent. It is on by default and off
// only when the user opted out or the kill switch is set.
func Enabled() bool {
	if disabled() {
		return false
	}
	c := config.Current()
	return c != nil && !c.AnalyticsOptOut
}

func today() string {
	return time.Now().Format("2006-01-02")
}

// readState returns the anonymous ID and last-ping date from the state file.
func readState() (id, lastPing string) {
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return "", ""
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) > 0 {
		id = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		lastPing = strings.TrimSpace(lines[1])
	}
	return id, lastPing
}

func writeState(id, lastPing string) error {
	dir := platform.ConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(stateFilePath(), []byte(id+"\n"+lastPing+"\n"), 0600)
}

// newUUID returns a random UUIDv4 string.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// loadOrCreateID returns the persistent anonymous ID, generating and storing a
// new one on first run.
func loadOrCreateID() (string, error) {
	id, _ := readState()
	if id != "" {
		return id, nil
	}
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	if err := writeState(id, ""); err != nil {
		return "", err
	}
	return id, nil
}

// usageBucket maps a 24h request count onto a coarse bucket so the payload never
// reveals exactly how much a user proxies.
func usageBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n < 10:
		return "1-9"
	case n < 100:
		return "10-99"
	default:
		return "100+"
	}
}

// usageProperties returns the activity properties for a heartbeat, or nil when
// the local stats database cannot be read. In that case the event is sent
// (or not) without them rather than claiming the user was inactive.
func usageProperties(now time.Time) map[string]any {
	count, ok := db.RequestCountSince(now.Add(-usageWindow).Unix())
	if !ok {
		return nil
	}
	return map[string]any{
		"used_today":   count > 0,
		"requests_24h": usageBucket(count),
	}
}

// buildPayload assembles the PostHog capture body. It is pure so the exact wire
// shape can be unit tested.
func buildPayload(id, version string, usage map[string]any) map[string]any {
	properties := map[string]any{
		"distinct_id": id,
		"version":     version,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
	}
	for k, v := range usage {
		properties[k] = v
	}
	return map[string]any{
		"api_key":    posthogProjectKey,
		"event":      "app_heartbeat",
		"properties": properties,
	}
}

// PingIfDue sends a heartbeat if telemetry is enabled and one has not already
// been sent today. It is safe to call concurrently and never blocks the caller
// on the network (the POST runs in its own goroutine).
func PingIfDue(version string) {
	if !Enabled() {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	id, lastPing := readState()
	if lastPing == today() {
		return
	}
	if id == "" {
		var err error
		id, err = loadOrCreateID()
		if err != nil {
			log.Printf("[analytics] failed to create id: %v", err)
			return
		}
	}

	// Record the ping date before sending so a concurrent caller cannot
	// double-send within the same day. A failed send is not retried until the
	// next day, which keeps telemetry quiet and self-healing.
	if err := writeState(id, today()); err != nil {
		log.Printf("[analytics] failed to record ping: %v", err)
	}

	usage := usageProperties(time.Now())
	go sendHeartbeat(id, version, usage)
}

// sendHeartbeat posts a single app_heartbeat event to PostHog. Errors are
// swallowed silently: telemetry must never disturb the user.
func sendHeartbeat(id, version string, usage map[string]any) {
	body, err := json.Marshal(buildPayload(id, version, usage))
	if err != nil {
		return
	}

	if debug() {
		log.Printf("[analytics] debug: would send %s", string(body))
		return
	}

	client := &http.Client{Timeout: httpTimeout}
	req, err := http.NewRequest("POST", posthogEndpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Prism/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	// Any status is fine; we do not retry.
}

// StartLoop runs the periodic heartbeat: one ping shortly after startup, then
// every 24 hours. It runs in the tray process only (the proxy child never
// pings, to avoid double-counting).
func StartLoop(version string) {
	go func() {
		time.Sleep(10 * time.Second)
		PingIfDue(version)
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for range ticker.C {
			PingIfDue(version)
		}
	}()
}
