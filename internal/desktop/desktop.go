// Package desktop provides the tray application shell: proxy process
// management, system tray menu, auto-updates, and managed SearXNG instance.
package desktop

import (
	"time"

	"ollama-proxy/internal/config"
)

// version is the build version, injected from main via SetVersion.
var version = "dev"

// SetVersion records the build version for display in the tray menu and the
// updater's User-Agent and comparison logic.
func SetVersion(v string) {
	version = v
}

// adminServerStarter starts the admin UI server (registered from main).
var adminServerStarter func(cfg *config.Config, port string)

// SetAdminServerStarter registers the function used to start the admin UI
// server from the tray process.
func SetAdminServerStarter(fn func(cfg *config.Config, port string)) {
	adminServerStarter = fn
}

// ReloadConfigAndRestartProxy is the config change hook: after the live config
// is swapped, restart the proxy (if running) so it picks up the new settings,
// and refresh the tray menu.
func ReloadConfigAndRestartProxy() {
	// Restart the proxy so it picks up config changes (e.g. active provider, API keys)
	if IsProxyRunning() {
		StopProxyProcess()
		time.Sleep(500 * time.Millisecond)
		StartProxyProcess()
		time.Sleep(500 * time.Millisecond)
		UpdateMenu(IsProxyRunning())
	}
}
