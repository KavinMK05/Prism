// Package util holds small helpers shared across multiple packages.
package util

import (
	"os/exec"
	"runtime"
)

// JoinStrings joins parts with newlines. Unlike strings.Join with "\n", an
// empty parts slice yields "" rather than a single empty element artifact,
// and the result is the concatenation of non-empty runs (empty strings stay
// empty lines, matching the original behavior callers relied on).
func JoinStrings(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}

// OpenBrowser opens rawURL in the user's default browser.
func OpenBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "windows":
		// rundll32, not `cmd /c start`: cmd mangles `&` and `%`, both common in
		// OAuth authorization URLs.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	case "darwin":
		return exec.Command("open", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}
