package proxy

import (
	"net/http"
	"strings"
)

// detectClient identifies the calling client tool from request headers
func detectClient(r *http.Request) string {
	// Prefer explicit client name header if set by user
	xClient := r.Header.Get("X-Client-Name")
	if xClient != "" {
		return xClient
	}

	ua := strings.ToLower(r.UserAgent())
	switch {
	case strings.Contains(ua, "factory-cli") || strings.Contains(ua, "factory-droid") || strings.Contains(ua, "factory-droid"):
		return "Factory Droid"
	case strings.Contains(ua, "claude-code") || strings.Contains(ua, "claude-code"):
		return "Claude Code"
	case strings.Contains(ua, "opencode") || strings.Contains(ua, "open-code"):
		return "OpenCode"
	case strings.Contains(ua, "cursor"):
		return "Cursor"
	case strings.Contains(ua, "copilot") || strings.Contains(ua, "github-copilot"):
		return "GitHub Copilot"
	case strings.Contains(ua, "aider"):
		return "Aider"
	case strings.Contains(ua, "continue"):
		return "Continue"
	case strings.Contains(ua, "supermaven"):
		return "Supermaven"
	case strings.Contains(ua, "windsurf"):
		return "Windsurf"
	case strings.Contains(ua, "trae"):
		return "Trae"
	case strings.Contains(ua, "claude") && strings.Contains(ua, "anthropic"):
		return "Claude"
	default:
		rawUA := r.UserAgent()
		if rawUA != "" {
			return rawUA
		}
		return "Unknown"
	}
}
