package config

import "sync"

// The live (in-memory) config. The tray process is the single owner: it loads
// config.json at startup, serves it to the admin UI, and swaps in new versions
// when handlers mutate and save the config. The change hook lets the tray
// react to swaps (e.g. restart the proxy so it picks up new provider settings).

var (
	currentMu sync.Mutex
	current   *Config
	hook      func(*Config)
)

// Current returns the live in-memory config. It may be nil before the tray
// has loaded it; callers that hold onto the pointer must treat it as shared
// mutable state, exactly like the old adminConfig global.
func Current() *Config {
	currentMu.Lock()
	defer currentMu.Unlock()
	return current
}

// SetCurrent replaces the live config and fires the change hook, if any,
// with the new config. The hook is invoked outside the internal lock.
func SetCurrent(c *Config) {
	currentMu.Lock()
	current = c
	h := hook
	currentMu.Unlock()
	if h != nil && c != nil {
		h(c)
	}
}

// UpdateCurrent mutates the live config in place without firing the change
// hook. Use it for small in-memory syncs (e.g. keeping SearXNGAutoStart in
// sync) where a full proxy restart is not wanted.
func UpdateCurrent(fn func(*Config)) {
	currentMu.Lock()
	defer currentMu.Unlock()
	if fn != nil {
		fn(current)
	}
}

// SetChangeHook registers the callback invoked after SetCurrent swaps the
// live config. Pass nil to unregister.
func SetChangeHook(fn func(*Config)) {
	currentMu.Lock()
	hook = fn
	currentMu.Unlock()
}
