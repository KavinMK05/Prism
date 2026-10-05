package admin

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"ollama-proxy/internal/agents"
	"ollama-proxy/internal/desktop"
)

// The Connect panel's data source. It is assembled entirely from local state:
// the same process status the rest of the admin UI reads (/admin/status,
// /admin/searxng/status) plus the agent integration registry. Nothing here
// probes an endpoint, so the panel reports "running" exactly when the tray
// process believes the service is running.

// connectEndpointInfo is one documented route on the proxy.
type connectEndpointInfo struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Protocol    string `json:"protocol"`
	Auth        bool   `json:"auth"`
	Description string `json:"description"`
}

// connectServiceInfo is one running (or stopped) surface Prism exposes.
type connectServiceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Running   bool   `json:"running"`
	Installed *bool  `json:"installed,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// connectMCPAgent is one agent's scoped MCP endpoint.
type connectMCPAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	Active    bool   `json:"active"`
	Installed bool   `json:"installed"`
}

type connectMCPInfo struct {
	AggregateURL    string            `json:"aggregate_url"`
	PerAgentPattern string            `json:"per_agent_pattern"`
	Agents          []connectMCPAgent `json:"agents"`
}

type connectInfo struct {
	Version   string                `json:"version"`
	Token     string                `json:"token"`
	Services  []connectServiceInfo  `json:"services"`
	Endpoints []connectEndpointInfo `json:"endpoints"`
	MCP       connectMCPInfo        `json:"mcp"`
}

// connectProxyBase is the proxy's externally usable base URL. It mirrors the
// bind logic in main.go: PRISM_HOST defaults to loopback, PRISM_PORT to 11434.
func connectProxyBase() string {
	host := strings.TrimSpace(os.Getenv("PRISM_HOST"))
	if host == "" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(os.Getenv("PRISM_PORT"))
	if port == "" {
		port = "11434"
	}
	// JoinHostPort brackets IPv6 literals, which a bare host:port cannot.
	return "http://" + net.JoinHostPort(host, port)
}

// connectAdminURL is the admin UI address. The admin server always binds
// loopback and is started by the tray process on PRISM_ADMIN_PORT (default 8765).
func connectAdminURL() string {
	port := strings.TrimSpace(os.Getenv("PRISM_ADMIN_PORT"))
	if port == "" {
		port = "8765"
	}
	return "http://127.0.0.1:" + port + "/admin"
}

// handleAdminConnect serves everything the Connect panel needs in one request.
func handleAdminConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	encodeJSON(w, buildConnectInfo())
}

// buildConnectInfo is split out so tests can call it directly under a
// controlled environment.
func buildConnectInfo() connectInfo {
	proxyBase := connectProxyBase()
	proxyRunning := desktop.IsProxyRunning()

	searxng := desktop.SearxngStatus()
	searxngRunning, _ := searxng["running"].(bool)
	searxngInstalled, _ := searxng["installed"].(bool)
	searxngPort, _ := searxng["port"].(int)
	if searxngPort <= 0 {
		searxngPort = 8888
	}
	searxngURL := "http://127.0.0.1:" + strconv.Itoa(searxngPort)

	services := []connectServiceInfo{
		{
			ID:      "proxy",
			Name:    "Prism proxy",
			URL:     proxyBase,
			Running: proxyRunning,
			Detail:  "Anthropic Messages, OpenAI Chat Completions, OpenAI Responses and the MCP gateway",
		},
		{
			ID:      "admin",
			Name:    "Admin UI",
			URL:     connectAdminURL(),
			Running: true,
			Detail:  "This control panel",
		},
		{
			ID:        "searxng",
			Name:      "SearXNG search",
			URL:       searxngURL,
			Running:   searxngRunning,
			Installed: &searxngInstalled,
			Detail:    "Free unlimited web search for agents",
		},
	}

	return connectInfo{
		Version:   desktop.Version(),
		Token:     mcpProxyToken,
		Services:  services,
		Endpoints: connectEndpoints(),
		MCP: connectMCPInfo{
			AggregateURL:    proxyBase + "/mcp",
			PerAgentPattern: proxyBase + "/mcp/<agent>",
			Agents:          connectMCPAgents(agents.ProxyPortFromEnv()),
		},
	}
}

// connectEndpoints is the user-facing route inventory. It is hand-maintained
// beside the mux registrations in main.go on purpose: admin_connect_test.go
// checks every path here appears as a HandleFunc target in main.go, so a route
// rename or removal fails the build instead of silently going stale.
//
// /v1/messages/count_tokens is deliberately absent: it exists only to return an
// Anthropic-shaped 404. A downstream Ollama-native /api/chat is also absent
// because Prism does not register one; /api/chat is only ever an upstream URL.
func connectEndpoints() []connectEndpointInfo {
	return []connectEndpointInfo{
		{
			Method:      http.MethodPost,
			Path:        "/v1/messages",
			Protocol:    "Anthropic Messages",
			Auth:        true,
			Description: "Anthropic-compatible chat endpoint used by Claude Code and other Anthropic clients.",
		},
		{
			Method:      http.MethodPost,
			Path:        "/v1/chat/completions",
			Protocol:    "OpenAI Chat Completions",
			Auth:        true,
			Description: "OpenAI-compatible chat endpoint for Continue, the OpenAI SDK and similar clients.",
		},
		{
			Method:      http.MethodPost,
			Path:        "/v1/responses",
			Protocol:    "OpenAI Responses",
			Auth:        true,
			Description: "OpenAI Responses API used by Codex Desktop, Codex CLI and Responses-native clients.",
		},
		{
			Method:      http.MethodPost,
			Path:        "/mcp",
			Protocol:    "MCP",
			Auth:        true,
			Description: "MCP gateway. /mcp reaches every enabled server; /mcp/<agent> is scoped to one agent.",
		},
		{
			Method:      http.MethodGet,
			Path:        "/v1/models",
			Protocol:    "OpenAI",
			Auth:        false,
			Description: "Lists the models Prism exposes. Open like a local Ollama server, so no key is needed.",
		},
		{
			Method:      http.MethodGet,
			Path:        "/health",
			Protocol:    "JSON",
			Auth:        false,
			Description: "Liveness probe.",
		},
		{
			Method:      http.MethodGet,
			Path:        "/v1/stats",
			Protocol:    "JSON",
			Auth:        false,
			Description: "Live request and token statistics as JSON.",
		},
		{
			Method:      http.MethodGet,
			Path:        "/api/model-info",
			Protocol:    "JSON",
			Auth:        false,
			Description: "Model metadata lookup from models.dev (pass ?id= and optional ?provider=).",
		},
	}
}

// connectMCPAgents lists the agents whose MCP config Prism knows how to write,
// with the scoped endpoint each one uses.
func connectMCPAgents(port int) []connectMCPAgent {
	ids := agents.AllAgentIDs()
	out := make([]connectMCPAgent, 0, len(ids))
	for _, id := range ids {
		if !agents.AgentMCPSupported(id) {
			continue
		}
		out = append(out, connectMCPAgent{
			ID:        id,
			Name:      agents.AgentDisplayName(id),
			Path:      agents.AgentMCPEndpointPath(id),
			URL:       agents.AgentMCPURL(id, port),
			Active:    agents.AgentMCPActive(id),
			Installed: agents.AgentInstalled(id),
		})
	}
	return out
}
