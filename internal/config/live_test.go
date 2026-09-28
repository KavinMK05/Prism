package config

import "testing"

// The proxy process publishes the config it loaded at startup; the tray process
// swaps it and expects the change hook (which restarts the proxy) to fire. If
// the proxy ever publishes through SetCurrent instead of Publish, it would
// restart itself; if it forgets to publish at all, the MCP gateway in that
// process sees zero servers.
func TestPublishSetsCurrentWithoutFiringHook(t *testing.T) {
	t.Cleanup(func() {
		SetChangeHook(nil)
		Publish(nil)
	})

	fired := 0
	SetChangeHook(func(*Config) { fired++ })

	published := &Config{DefaultProvider: "published"}
	Publish(published)
	if Current() != published {
		t.Fatal("Publish did not install the config as Current")
	}
	if fired != 0 {
		t.Fatalf("Publish fired the change hook %d time(s)", fired)
	}

	swapped := &Config{DefaultProvider: "swapped"}
	SetCurrent(swapped)
	if Current() != swapped {
		t.Fatal("SetCurrent did not install the config as Current")
	}
	if fired != 1 {
		t.Fatalf("SetCurrent fired the change hook %d time(s), want 1", fired)
	}
}
