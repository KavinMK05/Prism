package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"ollama-proxy/internal/agents"
	"ollama-proxy/internal/config"
	"ollama-proxy/internal/mcp"
)

// The MCP admin API manages Prism's MCP gateway: the upstream servers, their
// credentials, which agents may reach them, and the agent config entries that
// point at Prism. Live connection state is owned by the proxy process and read
// back over the authenticated /mcp/control endpoint.

const mcpProxyToken = "prism"

func mcpProxyBase() string {
	port := os.Getenv("PRISM_PORT")
	if port == "" {
		port = "11434"
	}
	return "http://127.0.0.1:" + port
}

// callMCPProxy talks to the running proxy's MCP control endpoints. Returns
// (nil, nil) when the proxy is not running.
func callMCPProxy(ctx context.Context, method, path string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, mcpProxyBase()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+mcpProxyToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, nil // proxy not running or unreachable
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return data, fmt.Errorf("proxy returned HTTP %d", resp.StatusCode)
	}
	return data, nil
}

func readJSONBody(r *http.Request, v interface{}) error {
	if r.Body == nil {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(v)
}

// handleMCPConfig serves the whole MCP state the panel needs in one request.
func handleMCPConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	cfg := config.Load()
	if cfg.EnsureMCP(); cfg.MCP == nil {
		writeJSONError(w, "MCP configuration unavailable", 500)
		return
	}
	redacted := cfg.RedactMCPSecrets()

	statuses := map[string]mcp.ServerStatus{}
	proxyRunning := false
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if data, err := callMCPProxy(ctx, http.MethodGet, "/mcp/status", nil); err == nil && len(data) > 0 {
		var payload struct {
			Statuses []mcp.ServerStatus `json:"statuses"`
		}
		if json.Unmarshal(data, &payload) == nil {
			for _, s := range payload.Statuses {
				statuses[s.ID] = s
			}
			proxyRunning = true
		}
	}

	servers := make([]map[string]interface{}, 0, len(redacted.MCP.Servers))
	for _, s := range redacted.MCP.Servers {
		if s == nil {
			continue
		}
		allowed := []string{}
		for agent, ids := range cfg.MCP.AgentServers {
			for _, id := range ids {
				if id == s.ID {
					allowed = append(allowed, agent)
				}
			}
		}
		sort.Strings(allowed)
		entry := map[string]interface{}{
			"server": s,
			"agents": allowed,
		}
		if st, ok := statuses[s.ID]; ok {
			entry["status"] = st
		} else {
			entry["status"] = nil
		}
		servers = append(servers, entry)
	}

	agentList := make([]map[string]interface{}, 0, len(agents.AllAgentIDs()))
	for _, id := range agents.AllAgentIDs() {
		ids := cfg.MCP.AgentServers[id]
		if ids == nil {
			ids = []string{}
		}
		agentList = append(agentList, map[string]interface{}{
			"id":            id,
			"name":          agents.AgentDisplayName(id),
			"installed":     agentInstalledForMCP(id),
			"mcp_supported": agents.AgentMCPSupported(id),
			"mcp_active":    agents.AgentMCPActive(id),
			"servers":       ids,
			"endpoint":      agents.AgentMCPEndpointPath(id),
		})
	}

	settings := map[string]interface{}{
		"idle_timeout_sec":       cfg.MCP.IdleTimeoutSec,
		"client_id_metadata_url": cfg.MCP.ClientIDMetadataURL,
		"proxy_running":          proxyRunning,
		"default_idle_timeout":   config.MCPDefaultIdleTimeoutSec,
		"registry_url":           mcp.RegistryBaseURL,
	}

	encodeJSON(w, map[string]interface{}{
		"servers":  servers,
		"agents":   agentList,
		"settings": settings,
	})
}

func agentInstalledForMCP(id string) bool {
	if id == "codex" {
		return agents.IsCodexDesktopInstalled()
	}
	return agents.AgentInstalled(id)
}

// mcpServerRequest is the add/update payload. Empty strings mean "unchanged"
// on update, matching how the rest of the admin API treats secrets.
type mcpServerRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Enabled   *bool  `json:"enabled"`
	Transport string `json:"transport"`

	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`

	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`

	AuthMode      string   `json:"auth_mode"`
	ToolAllowlist []string `json:"tool_allowlist"`

	RegistryName string `json:"registry_name"`
	Publisher    string `json:"publisher"`
	Repository   string `json:"repository"`

	OAuthClientID     string `json:"oauth_client_id"`
	OAuthClientSecret string `json:"oauth_client_secret"`
}

func handleMCPServerAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req mcpServerRequest
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	cfg := config.Load()
	mcpCfg := cfg.EnsureMCP()

	srv := &config.MCPServerConfig{
		Name:          strings.TrimSpace(req.Name),
		Source:        firstNonEmptyString(req.Source, config.MCPSourceManual),
		Transport:     req.Transport,
		Enabled:       true,
		Command:       strings.TrimSpace(req.Command),
		Args:          req.Args,
		Env:           req.Env,
		Cwd:           strings.TrimSpace(req.Cwd),
		URL:           strings.TrimSpace(req.URL),
		Headers:       req.Headers,
		AuthMode:      req.AuthMode,
		ToolAllowlist: req.ToolAllowlist,
		RegistryName:  req.RegistryName,
		Publisher:     req.Publisher,
		Repository:    req.Repository,
		AddedAt:       time.Now().Unix(),
	}
	if req.Enabled != nil {
		srv.Enabled = *req.Enabled
	}
	if srv.Name == "" {
		srv.Name = firstNonEmptyString(srv.RegistryName, srv.URL, srv.Command)
	}
	if err := config.ValidateMCPServer(srv); err != nil {
		writeJSONError(w, err.Error(), 400)
		return
	}
	srv.ID = cfg.UniqueMCPID(config.MCPIDFromName(srv.Name))
	if srv.AuthMode == "" {
		if len(srv.Headers) > 0 {
			srv.AuthMode = config.MCPAuthStatic
		} else {
			srv.AuthMode = config.MCPAuthNone
		}
	}
	mcpCfg.Servers = append(mcpCfg.Servers, srv)

	if err := config.Save(cfg); err != nil {
		writeJSONError(w, "save failed: "+err.Error(), 500)
		return
	}
	// Swap the live config in this process; the proxy picks the server up on
	// its next restart, and the change hook performs that restart.
	config.SetCurrent(cfg)
	encodeJSON(w, map[string]interface{}{"status": "ok", "id": srv.ID})
}

func handleMCPServerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req mcpServerRequest
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	cfg := config.Load()
	srv := cfg.FindMCPServer(req.ID)
	if srv == nil {
		writeJSONError(w, "unknown MCP server", 404)
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		srv.Name = name
	}
	if req.Transport != "" {
		srv.Transport = req.Transport
	}
	if req.Command != "" {
		srv.Command = strings.TrimSpace(req.Command)
	}
	if req.Args != nil {
		srv.Args = req.Args
	}
	if req.Env != nil {
		srv.Env = mergeSecretMaps(srv.Env, req.Env)
	}
	if req.Cwd != "" {
		srv.Cwd = strings.TrimSpace(req.Cwd)
	}
	if req.URL != "" {
		srv.URL = strings.TrimSpace(req.URL)
	}
	if req.Headers != nil {
		srv.Headers = mergeSecretMaps(srv.Headers, req.Headers)
	}
	if req.AuthMode != "" {
		srv.AuthMode = req.AuthMode
	}
	if req.ToolAllowlist != nil {
		srv.ToolAllowlist = req.ToolAllowlist
	}
	if req.Enabled != nil {
		srv.Enabled = *req.Enabled
	}
	if req.OAuthClientID != "" {
		if srv.OAuth == nil {
			srv.OAuth = &config.MCPOAuthToken{}
		}
		srv.OAuth.ClientID = req.OAuthClientID
		srv.OAuth.RegistrationMode = "preregistered"
	}
	if req.OAuthClientSecret != "" && !looksMaskedSecret(req.OAuthClientSecret) {
		if srv.OAuth == nil {
			srv.OAuth = &config.MCPOAuthToken{}
		}
		srv.OAuth.ClientSecret = req.OAuthClientSecret
	}
	if err := config.ValidateMCPServer(srv); err != nil {
		writeJSONError(w, err.Error(), 400)
		return
	}
	if err := config.Save(cfg); err != nil {
		writeJSONError(w, "save failed: "+err.Error(), 500)
		return
	}
	config.SetCurrent(cfg)
	encodeJSON(w, map[string]interface{}{"status": "ok"})
}

func handleMCPServerRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	cfg := config.Load()
	if !cfg.RemoveMCPServer(req.ID) {
		writeJSONError(w, "unknown MCP server", 404)
		return
	}
	if err := config.Save(cfg); err != nil {
		writeJSONError(w, "save failed: "+err.Error(), 500)
		return
	}
	config.SetCurrent(cfg)
	encodeJSON(w, map[string]interface{}{"status": "ok"})
}

func handleMCPServerEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	cfg := config.Load()
	srv := cfg.FindMCPServer(req.ID)
	if srv == nil {
		writeJSONError(w, "unknown MCP server", 404)
		return
	}
	srv.Enabled = req.Enabled
	if err := config.Save(cfg); err != nil {
		writeJSONError(w, "save failed: "+err.Error(), 500)
		return
	}
	config.SetCurrent(cfg)
	encodeJSON(w, map[string]interface{}{"status": "ok"})
}

// handleMCPServerControl forwards restart/probe to the proxy process, which
// owns the live connections.
func handleMCPServerControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Action string `json:"action"`
		ID     string `json:"id"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	if req.Action == "" {
		writeJSONError(w, "missing action", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	data, err := callMCPProxy(ctx, http.MethodPost, "/mcp/control", map[string]string{"action": req.Action, "server": req.ID})
	if err != nil {
		writeJSONError(w, err.Error(), 502)
		return
	}
	if data == nil {
		writeJSONError(w, "the Prism proxy is not running; start it and try again", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handleMCPRegistrySearch searches the official MCP registry.
func handleMCPRegistrySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	query := r.URL.Query().Get("q")
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parsePositiveInt(v); err == nil {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	items, err := mcp.SearchRegistry(ctx, query, limit)
	if err != nil {
		writeJSONError(w, err.Error(), 502)
		return
	}
	if items == nil {
		items = []mcp.RegistrySearchItem{}
	}
	encodeJSON(w, map[string]interface{}{"results": items})
}

// handleMCPGitImport clones a repository and returns the servers its manifest
// declares. Nothing is saved: the caller chooses what to add.
func handleMCPGitImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	plugin, err := mcp.ImportGitRepository(ctx, req.URL)
	if err != nil {
		writeJSONError(w, err.Error(), 400)
		return
	}
	encodeJSON(w, map[string]interface{}{"status": "ok", "plugin": plugin})
}

// handleMCPAuthDiscover reports the OAuth setup Prism discovered for a server.
func handleMCPAuthDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	cfg := config.Load()
	srv := cfg.FindMCPServer(r.URL.Query().Get("id"))
	if srv == nil {
		writeJSONError(w, "unknown MCP server", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	discovery, err := mcp.DiscoverAuth(ctx, srv)
	if err != nil {
		writeJSONError(w, err.Error(), 502)
		return
	}
	encodeJSON(w, discovery)
}

// handleMCPAuthLogin starts the OAuth flow and returns the URL to open.
func handleMCPAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	authURL, redirectURI, err := mcp.StartOAuthLogin(ctx, cfg, req.ID)
	if err != nil {
		writeJSONError(w, err.Error(), 400)
		return
	}
	// Remember that this server is OAuth-protected even before the flow
	// finishes, so the panel shows the right controls.
	if srv := cfg.FindMCPServer(req.ID); srv != nil && srv.AuthMode != config.MCPAuthOAuth {
		srv.AuthMode = config.MCPAuthOAuth
		if err := config.Save(cfg); err == nil {
			config.SetCurrent(cfg)
		}
	}
	encodeJSON(w, map[string]interface{}{
		"status":       "ok",
		"authorizeUrl": authURL,
		"redirectUri":  redirectURI,
	})
}

// handleMCPAuthLogout revokes and clears a server's stored token.
func handleMCPAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := mcp.Logout(ctx, req.ID); err != nil {
		writeJSONError(w, err.Error(), 400)
		return
	}
	config.SetCurrent(config.Load())
	encodeJSON(w, map[string]interface{}{"status": "ok"})
}

// handleMCPAgentServers replaces one agent's server allowlist.
func handleMCPAgentServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Agent   string   `json:"agent"`
		Servers []string `json:"servers"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	if !agents.IsSupportedAgent(req.Agent) && req.Agent != "codex" {
		writeJSONError(w, "unknown agent", 400)
		return
	}
	cfg := config.Load()
	cfg.SetAgentMCPServers(req.Agent, req.Servers)
	if err := config.Save(cfg); err != nil {
		writeJSONError(w, "save failed: "+err.Error(), 500)
		return
	}
	config.SetCurrent(cfg)
	encodeJSON(w, map[string]interface{}{"status": "ok", "servers": cfg.MCP.AgentServers[req.Agent]})
}

// handleMCPAgentSetup installs (or removes) Prism's MCP entry in an agent's
// config file.
func handleMCPAgentSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Agent  string `json:"agent"`
		Remove bool   `json:"remove"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	if !agents.AgentMCPSupported(req.Agent) {
		writeJSONError(w, agents.AgentDisplayName(req.Agent)+" does not expose an MCP configuration Prism can write", 400)
		return
	}
	if !agentInstalledForMCP(req.Agent) {
		writeJSONError(w, agents.AgentDisplayName(req.Agent)+" is not installed", 404)
		return
	}
	var err error
	if req.Remove {
		err = agents.RestoreAgentMCPConfig(req.Agent)
	} else {
		err = agents.InstallAgentMCPConfig(req.Agent, agents.ProxyPortFromEnv())
	}
	if err != nil {
		writeJSONError(w, err.Error(), 500)
		return
	}
	encodeJSON(w, map[string]interface{}{
		"status": "ok",
		"active": agents.AgentMCPActive(req.Agent),
		"path":   agents.AgentMCPConfigPath(req.Agent),
	})
}

// handleMCPSettings stores gateway-wide settings.
func handleMCPSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		IdleTimeoutSec      *int   `json:"idle_timeout_sec"`
		ClientIDMetadataURL string `json:"client_id_metadata_url"`
	}
	if err := readJSONBody(r, &req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	cfg := config.Load()
	mcpCfg := cfg.EnsureMCP()
	if req.IdleTimeoutSec != nil {
		seconds := *req.IdleTimeoutSec
		if seconds < 30 {
			seconds = 30
		}
		if seconds > 86400 {
			seconds = 86400
		}
		mcpCfg.IdleTimeoutSec = seconds
	}
	if req.ClientIDMetadataURL != "" {
		if err := config.ValidateBaseURL(req.ClientIDMetadataURL); err != nil {
			writeJSONError(w, "invalid client metadata URL: "+err.Error(), 400)
			return
		}
	}
	mcpCfg.ClientIDMetadataURL = req.ClientIDMetadataURL
	if err := config.Save(cfg); err != nil {
		writeJSONError(w, "save failed: "+err.Error(), 500)
		return
	}
	config.SetCurrent(cfg)
	encodeJSON(w, map[string]interface{}{"status": "ok"})
}

// -- helpers --

// preserveMCPSecrets keeps stored credentials when a full-config PUT arrives
// with MCP fields omitted or redacted. The admin UI reads masked values, so a
// naive round-trip would otherwise erase every token.
func preserveMCPSecrets(cur, next *config.Config) {
	if cur == nil || cur.MCP == nil || next == nil || next.MCP == nil {
		return
	}
	if next.MCP.Clients == nil {
		next.MCP.Clients = cur.MCP.Clients
	} else {
		for asURL, reg := range cur.MCP.Clients {
			if existing, ok := next.MCP.Clients[asURL]; !ok || existing == nil {
				next.MCP.Clients[asURL] = reg
			} else if existing.ClientSecret == "" && reg != nil {
				existing.ClientSecret = reg.ClientSecret
			}
		}
	}
	if next.MCP.ClientIDMetadataURL == "" {
		next.MCP.ClientIDMetadataURL = cur.MCP.ClientIDMetadataURL
	}
	if next.MCP.IdleTimeoutSec <= 0 {
		next.MCP.IdleTimeoutSec = cur.MCP.IdleTimeoutSec
	}
	if next.MCP.AgentServers == nil {
		next.MCP.AgentServers = cur.MCP.AgentServers
	}

	for _, srv := range next.MCP.Servers {
		if srv == nil {
			continue
		}
		old := cur.FindMCPServer(srv.ID)
		if old == nil {
			continue
		}
		if srv.OAuth == nil {
			srv.OAuth = old.OAuth
		} else {
			carry := func(newVal, oldVal string) string {
				if newVal == "" || looksMaskedSecret(newVal) {
					return oldVal
				}
				return newVal
			}
			var oldOAuth config.MCPOAuthToken
			if old.OAuth != nil {
				oldOAuth = *old.OAuth
			}
			srv.OAuth.AccessToken = carry(srv.OAuth.AccessToken, oldOAuth.AccessToken)
			srv.OAuth.RefreshToken = carry(srv.OAuth.RefreshToken, oldOAuth.RefreshToken)
			srv.OAuth.ClientID = carry(srv.OAuth.ClientID, oldOAuth.ClientID)
			srv.OAuth.ClientSecret = carry(srv.OAuth.ClientSecret, oldOAuth.ClientSecret)
			if srv.OAuth.TokenAuthMethod == "" {
				srv.OAuth.TokenAuthMethod = oldOAuth.TokenAuthMethod
			}
			if srv.OAuth.ASURL == "" {
				srv.OAuth.ASURL = oldOAuth.ASURL
			}
			if srv.OAuth.Issuer == "" {
				srv.OAuth.Issuer = oldOAuth.Issuer
			}
			if srv.OAuth.Resource == "" {
				srv.OAuth.Resource = oldOAuth.Resource
			}
			if srv.OAuth.RegistrationMode == "" {
				srv.OAuth.RegistrationMode = oldOAuth.RegistrationMode
			}
			if srv.OAuth.ExpiresAt == 0 {
				srv.OAuth.ExpiresAt = oldOAuth.ExpiresAt
			}
			if len(srv.OAuth.Scopes) == 0 {
				srv.OAuth.Scopes = oldOAuth.Scopes
			}
			if srv.OAuth.AccessToken == "" && srv.OAuth.RefreshToken == "" &&
				srv.OAuth.ClientID == "" && srv.OAuth.ClientSecret == "" {
				srv.OAuth = nil
			}
		}
		carryMap := func(nextMap, oldMap map[string]string) map[string]string {
			if len(nextMap) == 0 {
				return oldMap
			}
			for k, v := range nextMap {
				if looksMaskedSecret(v) {
					if oldVal, ok := oldMap[k]; ok {
						nextMap[k] = oldVal
					}
				}
			}
			return nextMap
		}
		srv.Headers = carryMap(srv.Headers, old.Headers)
		srv.Env = carryMap(srv.Env, old.Env)
		for _, key := range []string{"transport", "name", "source"} {
			switch key {
			case "transport":
				if srv.Transport == "" {
					srv.Transport = old.Transport
				}
			case "name":
				if srv.Name == "" {
					srv.Name = old.Name
				}
			case "source":
				if srv.Source == "" {
					srv.Source = old.Source
				}
			}
		}
	}
}

// mergeSecretMaps applies an update while keeping stored secrets the client did
// not resend (the UI echoes masked values back).
func mergeSecretMaps(existing, incoming map[string]string) map[string]string {
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if looksMaskedSecret(v) {
			if old, ok := existing[k]; ok {
				out[k] = old
				continue
			}
		}
		out[k] = v
	}
	return out
}

func looksMaskedSecret(v string) bool {
	if v == "****" || v == "(not set)" {
		return true
	}
	return len(v) <= 14 && strings.Contains(v, "...")
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parsePositiveInt(v string) (int, error) {
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
