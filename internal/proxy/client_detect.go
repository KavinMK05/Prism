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
	case strings.Contains(ua, "empryo"):
		// Empryo's custom-provider config has no header field, so its User-Agent
		// is the only client signal Prism gets.
		return "Empryo"
	case strings.Contains(ua, "hermes"):
		// Hermes's provider config has no header field either; the User-Agent is
		// the only client signal Prism gets.
		return "Hermes"
	case strings.Contains(ua, "deepseek-harness"):
		// DSH sends deepseek-harness/<version> (+<repo url>) on its LLM requests.
		// Its MCP client sends a bare "node" User-Agent instead, which carries no
		// usable signal — but that only reaches /mcp, not the LLM endpoints.
		return "DeepSeek Harness"
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
