package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"ollama-proxy/internal/config"
)

const (
	// toolListTimeout bounds one upstream tools/list, so a hung server cannot
	// stall the aggregated listing for every other server.
	toolListTimeout = 20 * time.Second
	// toolCallTimeout bounds a tools/call to the gateway. Long-running tools
	// (builds, searches) need room, but nothing should hang forever.
	toolCallTimeout = 10 * time.Minute
	// handshakeTimeout bounds process startup and the initialize handshake.
	handshakeTimeout = 60 * time.Second
	// toolCacheTTL is how long a tools/list result is reused. The stateless
	// revision lets servers advertise their own TTL; this is the floor.
	toolCacheTTL = 30 * time.Second
	// idleSweepInterval is how often the reaper checks for idle processes.
	idleSweepInterval = 15 * time.Second
)

// Manager owns the upstream MCP connections: lazily started stdio processes,
// HTTP transports, their cached tool lists, and the idle reaper that shuts
// idle processes down again.
type Manager struct {
	cfg func() *config.Config

	// autoAuthorize is the callback the Gateway uses to sign in when an agent
	// asks for a server that is not authorized yet. Nil unless the proxy
	// process wired it, so nothing else opens a browser.
	autoAuthorize func(ctx context.Context, serverID string, wait bool) error

	mu      sync.Mutex
	servers map[string]*serverRuntime

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// SetAutoAuthorize installs the callback the Gateway uses to sign in when an
// agent asks for a server that is not authorized yet. Nil (the default) leaves
// today's behavior: the error reaches the agent and the user connects the
// server in Prism by hand. Only the proxy process wires this, because only it
// may open a browser.
func (m *Manager) SetAutoAuthorize(fn func(ctx context.Context, serverID string, wait bool) error) {
	m.autoAuthorize = fn
}

// serverRuntime is the per-server connection state.
type serverRuntime struct {
	id string

	mu        sync.Mutex
	tr        transport
	state     string
	lastErr   string
	lastUsed  time.Time
	startedAt time.Time
	restarts  int
	tools     []Tool
	toolsAt   time.Time
}

// NewManager builds a manager reading live config through cfgFn.
func NewManager(cfgFn func() *config.Config) *Manager {
	if cfgFn == nil {
		cfgFn = config.Current
	}
	return &Manager{
		cfg:     cfgFn,
		servers: map[string]*serverRuntime{},
		stopCh:  make(chan struct{}),
	}
}

// Start launches the idle reaper. Safe to call once.
func (m *Manager) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(idleSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.reapIdle()
			}
		}
	}()
}

// Stop closes every upstream connection.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.mu.Lock()
	runtimes := make([]*serverRuntime, 0, len(m.servers))
	for _, rt := range m.servers {
		runtimes = append(runtimes, rt)
	}
	m.mu.Unlock()
	for _, rt := range runtimes {
		rt.mu.Lock()
		if rt.tr != nil {
			_ = rt.tr.Close()
			rt.tr = nil
		}
		rt.state = config.MCPStateIdle
		rt.mu.Unlock()
	}
	m.wg.Wait()
}

func (m *Manager) idleTimeout() time.Duration {
	cfg := m.cfg()
	if cfg == nil || cfg.MCP == nil || cfg.MCP.IdleTimeoutSec <= 0 {
		return config.MCPDefaultIdleTimeoutSec * time.Second
	}
	return time.Duration(cfg.MCP.IdleTimeoutSec) * time.Second
}

// reapIdle shuts down stdio servers that have not been used recently. Remote
// transports are cheap to keep; only child processes are reaped.
func (m *Manager) reapIdle() {
	timeout := m.idleTimeout()
	now := time.Now()
	m.mu.Lock()
	runtimes := make([]*serverRuntime, 0, len(m.servers))
	for _, rt := range m.servers {
		runtimes = append(runtimes, rt)
	}
	m.mu.Unlock()
	for _, rt := range runtimes {
		rt.mu.Lock()
		if rt.tr == nil || rt.lastUsed.IsZero() || now.Sub(rt.lastUsed) < timeout {
			rt.mu.Unlock()
			continue
		}
		tr := rt.tr
		rt.tr = nil
		rt.tools = nil
		rt.toolsAt = time.Time{}
		rt.state = config.MCPStateIdle
		rt.mu.Unlock()
		if tr != nil {
			_ = tr.Close()
		}
		log.Printf("[MCP] %s idle for %s, stopped", rt.id, timeout)
	}
}

// runtimeFor returns (creating if needed) the runtime record for a server id.
func (m *Manager) runtimeFor(id string) *serverRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.servers[id]
	if !ok {
		rt = &serverRuntime{id: id, state: config.MCPStateIdle}
		m.servers[id] = rt
	}
	return rt
}

// Restart drops any live connection for a server so the next call reconnects.
func (m *Manager) Restart(id string) {
	rt := m.runtimeFor(id)
	rt.mu.Lock()
	tr := rt.tr
	rt.tr = nil
	rt.tools = nil
	rt.toolsAt = time.Time{}
	rt.lastErr = ""
	rt.state = config.MCPStateIdle
	rt.mu.Unlock()
	if tr != nil {
		_ = tr.Close()
	}
}

// RestartAll drops every live connection (used when MCP config changes).
func (m *Manager) RestartAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.servers))
	for id := range m.servers {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Restart(id)
	}
}

// ensureConn returns a live transport for a server, starting one when the
// server is cold. Errors are recorded on the runtime for the status API.
func (m *Manager) ensureConn(ctx context.Context, s *config.MCPServerConfig) (transport, error) {
	if s == nil {
		return nil, rpcErrorf(CodeInvalidParams, "unknown MCP server")
	}
	if !s.Enabled {
		return nil, fmt.Errorf("server %s is disabled", s.ID)
	}
	rt := m.runtimeFor(s.ID)

	rt.mu.Lock()
	if rt.tr != nil {
		rt.lastUsed = time.Now()
		tr := rt.tr
		rt.mu.Unlock()
		return tr, nil
	}
	rt.state = config.MCPStateIdle
	rt.mu.Unlock()

	tr, err := m.connect(ctx, s)
	if err != nil {
		m.recordError(rt, s.ID, err)
		return nil, err
	}

	rt.mu.Lock()
	if rt.tr != nil {
		// Another goroutine won the race; keep its transport.
		rt.mu.Unlock()
		_ = tr.Close()
		return m.currentTransport(rt), nil
	}
	rt.tr = tr
	rt.state = config.MCPStateReady
	rt.lastErr = ""
	rt.lastUsed = time.Now()
	rt.startedAt = time.Now()
	rt.tools = nil
	rt.toolsAt = time.Time{}
	rt.mu.Unlock()
	return tr, nil
}

func (m *Manager) currentTransport(rt *serverRuntime) transport {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.tr
}

// connect builds a transport and runs the MCP handshake against it.
func (m *Manager) connect(ctx context.Context, s *config.MCPServerConfig) (transport, error) {
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	tr, err := newTransport(s, m.dynamicHeadersFor(s), m.toolParamsFor(s.ID))
	if err != nil {
		return nil, err
	}
	if err := handshake(hctx, tr); err != nil {
		_ = tr.Close()
		return nil, err
	}
	return tr, nil
}

// handshake negotiates the protocol era with a server. The stateless revision
// (2026-07-28) removed the initialize exchange in favour of server/discover, so
// Prism probes for that first: a server speaking the stateless revision answers
// it, while a server that only knows the legacy handshake answers method-not-
// found and is retried with initialize.
func handshake(ctx context.Context, tr transport) error {
	_, discoverErr := tr.RoundTrip(ctx, "server/discover", serverDiscoverParams())
	if discoverErr == nil {
		return nil
	}
	// An authorization or startup failure is not an era problem: retrying with
	// initialize would only produce a second, less useful error.
	if isAuthError(discoverErr) || isRuntimeMissing(discoverErr) {
		return discoverErr
	}

	params := map[string]interface{}{
		"protocolVersion": LegacyProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": ServerName, "version": mcpClientVersion},
	}
	if _, err := tr.RoundTrip(ctx, "initialize", params); err != nil {
		// A stateless server rejects initialize outright: report what the
		// discovery probe said, because that describes the real problem.
		if isMethodNotFound(err) {
			return discoverErr
		}
		return err
	}
	_ = tr.Notify(ctx, "notifications/initialized", nil)
	return nil
}

// toolParamsFor returns the callback a transport uses to mirror x-mcp-header
// parameters, reading the schemas cached by the last tools/list.
func (m *Manager) toolParamsFor(id string) toolParamsFunc {
	return func(tool string) map[string]string {
		rt := m.runtimeFor(id)
		rt.mu.Lock()
		defer rt.mu.Unlock()
		for _, cached := range rt.tools {
			if cached.Name == tool {
				return xMCPHeaderParams(cached.InputSchema)
			}
		}
		return nil
	}
}

// serverDiscoverParams is the params object the stateless revision defines for
// server/discover.
func serverDiscoverParams() map[string]interface{} {
	return map[string]interface{}{
		"_meta": map[string]interface{}{
			metaProtocolVersionKey:    ProtocolVersion,
			metaClientInfoKey:         map[string]string{"name": ServerName, "version": mcpClientVersion},
			metaClientCapabilitiesKey: map[string]interface{}{},
		},
	}
}

// dynamicHeadersFor returns the per-request header supplier for a server.
// Static API-key/bearer headers are handed to the transport directly; only
// OAuth needs a callback (so a refreshed token is picked up without
// reconnecting).
func (m *Manager) dynamicHeadersFor(s *config.MCPServerConfig) dynamicHeaders {
	if s.AuthMode != config.MCPAuthOAuth {
		return nil
	}
	return func(ctx context.Context) (map[string]string, error) {
		token, err := m.bearerToken(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"Authorization": "Bearer " + token}, nil
	}
}

// recordError stores the failure on the runtime and maps it to a state.
func (m *Manager) recordError(rt *serverRuntime, id string, err error) {
	state := config.MCPStateError
	switch {
	case isAuthError(err):
		state = config.MCPStateNeedsAuth
	case isRuntimeMissing(err):
		state = config.MCPStateRuntimeMissing
	}
	rt.mu.Lock()
	rt.state = state
	rt.lastErr = err.Error()
	rt.mu.Unlock()
	if state == config.MCPStateError {
		log.Printf("[MCP] %s failed: %v", id, err)
	}
}

// ToolsForServers lists the tools an agent may see: every allowed, enabled
// server, with namespaced tool names. Servers that fail are reported in the
// statuses slice instead of failing the whole listing.
func (m *Manager) ToolsForServers(ctx context.Context, servers []*config.MCPServerConfig) ([]Tool, []ServerStatus) {
	type result struct {
		s      *config.MCPServerConfig
		tools  []Tool
		status ServerStatus
	}
	results := make([]result, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func(i int, s *config.MCPServerConfig) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, toolListTimeout)
			defer cancel()
			tools, err := m.listTools(sctx, s)
			results[i] = result{s: s, tools: tools, status: m.StatusFor(s, err)}
		}(i, s)
	}
	wg.Wait()

	out := make([]Tool, 0, 16)
	statuses := make([]ServerStatus, 0, len(servers))
	for _, r := range results {
		for _, t := range r.tools {
			namespaced := t
			namespaced.Name = NamespacedToolName(r.s.ID, t.Name)
			out = append(out, namespaced)
		}
		statuses = append(statuses, r.status)
	}
	return out, statuses
}

// listTools returns a server's tool list, cached briefly.
func (m *Manager) listTools(ctx context.Context, s *config.MCPServerConfig) ([]Tool, error) {
	rt := m.runtimeFor(s.ID)
	rt.mu.Lock()
	if rt.tools != nil && time.Since(rt.toolsAt) < toolCacheTTL {
		tools := rt.tools
		rt.mu.Unlock()
		return tools, nil
	}
	rt.mu.Unlock()

	tr, err := m.ensureConn(ctx, s)
	if err != nil {
		return nil, err
	}
	raw, err := tr.RoundTrip(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		if !isAuthError(err) && !isMethodNotFound(err) {
			// A dead process should be replaced once before reporting failure.
			if refreshed, rerr := m.retryAfterCrash(ctx, s, err); rerr == nil && refreshed != nil {
				raw, err = refreshed.RoundTrip(ctx, "tools/list", map[string]interface{}{})
			}
		}
		if err != nil {
			return nil, err
		}
	}
	tools, err := parseToolList(raw)
	if err != nil {
		return nil, err
	}

	// Apply the per-server tool allowlist (empty = every tool the server has).
	if len(s.ToolAllowlist) > 0 {
		allowed := map[string]struct{}{}
		for _, name := range s.ToolAllowlist {
			allowed[name] = struct{}{}
		}
		filtered := make([]Tool, 0, len(tools))
		for _, t := range tools {
			if _, ok := allowed[t.Name]; ok {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}

	rt.mu.Lock()
	rt.tools = tools
	rt.toolsAt = time.Now()
	rt.lastUsed = time.Now()
	rt.mu.Unlock()
	return tools, nil
}

// retryAfterCrash restarts a server once after a transport-level failure and
// returns the fresh transport.
func (m *Manager) retryAfterCrash(ctx context.Context, s *config.MCPServerConfig, cause error) (transport, error) {
	rt := m.runtimeFor(s.ID)
	rt.mu.Lock()
	rt.restarts++
	restarts := rt.restarts
	rt.mu.Unlock()
	log.Printf("[MCP] %s transport error (%v), restarting (attempt %d)", s.ID, cause, restarts)
	m.Restart(s.ID)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(250 * time.Millisecond):
	}
	return m.ensureConn(ctx, s)
}

// CallTool routes a namespaced tool call to its upstream server.
func (m *Manager) CallTool(ctx context.Context, s *config.MCPServerConfig, tool string, args json.RawMessage) (json.RawMessage, error) {
	if s == nil {
		return nil, rpcErrorf(CodeInvalidParams, "unknown MCP server")
	}
	cctx, cancel := context.WithTimeout(ctx, toolCallTimeout)
	defer cancel()

	params := map[string]interface{}{"name": tool}
	if len(args) > 0 {
		if trimmed := bytes.TrimSpace(args); len(trimmed) > 0 && string(trimmed) != "null" {
			params["arguments"] = json.RawMessage(trimmed)
		}
	}

	// Warm the schema cache once so the transport can mirror x-mcp-header
	// parameters into Mcp-Param-* headers. Failures are ignored: the call
	// itself reports the real problem.
	if m.toolSchemasMissing(s.ID) {
		_, _ = m.listTools(cctx, s)
	}

	tr, err := m.ensureConn(cctx, s)
	if err != nil {
		return nil, err
	}
	raw, err := tr.RoundTrip(cctx, "tools/call", params)
	if err != nil && !isAuthError(err) && !isRPCError(err) {
		if fresh, rerr := m.retryAfterCrash(cctx, s, err); rerr == nil && fresh != nil {
			raw, err = fresh.RoundTrip(cctx, "tools/call", params)
		}
	}
	if err != nil {
		if isAuthError(err) || isRuntimeMissing(err) {
			m.recordError(m.runtimeFor(s.ID), s.ID, err)
		}
		return nil, err
	}
	rt := m.runtimeFor(s.ID)
	rt.mu.Lock()
	rt.lastUsed = time.Now()
	rt.state = config.MCPStateReady
	rt.lastErr = ""
	rt.mu.Unlock()
	return raw, nil
}

// toolSchemasMissing reports whether a server has never listed its tools in
// this process, which is what x-mcp-header mirroring reads.
func (m *Manager) toolSchemasMissing(id string) bool {
	rt := m.runtimeFor(id)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.tools == nil
}

// ServerStatus is the health snapshot the admin UI renders.
type ServerStatus struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Transport  string   `json:"transport"`
	AuthMode   string   `json:"auth_mode"`
	Source     string   `json:"source,omitempty"`
	Enabled    bool     `json:"enabled"`
	State      string   `json:"state"`
	LastError  string   `json:"last_error,omitempty"`
	ToolCount  int      `json:"tool_count"`
	Tools      []string `json:"tools,omitempty"`
	LastUsed   int64    `json:"last_used,omitempty"`
	Authorized bool     `json:"authorized"`
}

// StatusFor builds a snapshot for one server, folding in the runtime state.
// err (when non-nil) is the most recent failure observed by the caller.
func (m *Manager) StatusFor(s *config.MCPServerConfig, err error) ServerStatus {
	rt := m.runtimeFor(s.ID)
	rt.mu.Lock()
	state := rt.state
	lastErr := rt.lastErr
	toolCount := len(rt.tools)
	lastUsed := rt.lastUsed
	tools := make([]string, 0, toolCount)
	for _, t := range rt.tools {
		tools = append(tools, t.Name)
	}
	rt.mu.Unlock()

	if state == "" {
		state = config.MCPStateIdle
	}
	if err != nil {
		lastErr = err.Error()
		switch {
		case isAuthError(err):
			state = config.MCPStateNeedsAuth
		case isRuntimeMissing(err):
			state = config.MCPStateRuntimeMissing
		default:
			state = config.MCPStateError
		}
	}
	if !s.Enabled {
		state = config.MCPStateDisabled
	}
	return ServerStatus{
		ID:         s.ID,
		Name:       s.Name,
		Transport:  s.Transport,
		AuthMode:   authModeOf(s),
		Source:     s.Source,
		Enabled:    s.Enabled,
		State:      state,
		LastError:  lastErr,
		ToolCount:  toolCount,
		Tools:      tools,
		LastUsed:   lastUsed.Unix(),
		Authorized: hasCredentials(s),
	}
}

// Status reports one server by id.
func (m *Manager) Status(id string) (ServerStatus, bool) {
	cfg := m.cfg()
	if cfg == nil {
		return ServerStatus{}, false
	}
	s := cfg.FindMCPServer(id)
	if s == nil {
		return ServerStatus{}, false
	}
	return m.StatusFor(s, nil), true
}

// Statuses reports every configured server.
func (m *Manager) Statuses() []ServerStatus {
	cfg := m.cfg()
	if cfg == nil || cfg.MCP == nil {
		return nil
	}
	out := make([]ServerStatus, 0, len(cfg.MCP.Servers))
	for _, s := range cfg.MCP.Servers {
		if s == nil {
			continue
		}
		out = append(out, m.StatusFor(s, nil))
	}
	return out
}

// Probe connects a server on demand and returns its status, refreshing the
// cached tool list. Used by the admin UI's "test"/"refresh" actions.
func (m *Manager) Probe(ctx context.Context, id string) (ServerStatus, error) {
	cfg := m.cfg()
	if cfg == nil {
		return ServerStatus{}, fmt.Errorf("no config loaded")
	}
	s := cfg.FindMCPServer(id)
	if s == nil {
		return ServerStatus{}, fmt.Errorf("unknown MCP server %q", id)
	}
	rt := m.runtimeFor(id)
	rt.mu.Lock()
	rt.tools = nil
	rt.toolsAt = time.Time{}
	rt.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, toolListTimeout)
	defer cancel()
	_, err := m.listTools(ctx, s)
	return m.StatusFor(s, err), err
}

// ProbeAll warms every enabled server concurrently and returns their statuses.
// The admin UI calls it when the MCP panel opens, so tool names are already
// listed instead of the user refreshing each server by hand. Unlike Probe it
// keeps any cached tool list, which makes a repeat call within the cache window
// nearly free. A failing server is reported through its status rather than
// failing the whole call.
func (m *Manager) ProbeAll(ctx context.Context) []ServerStatus {
	cfg := m.cfg()
	if cfg == nil || cfg.MCP == nil {
		return nil
	}
	servers := cfg.EnabledMCPServers()
	if len(servers) == 0 {
		return []ServerStatus{}
	}
	_, statuses := m.ToolsForServers(ctx, servers)
	return statuses
}

// authModeOf normalizes the stored auth mode for display.
func authModeOf(s *config.MCPServerConfig) string {
	if s.AuthMode != "" {
		return s.AuthMode
	}
	if hasCredentials(s) {
		return config.MCPAuthStatic
	}
	return config.MCPAuthNone
}

// hasCredentials reports whether the server already has something to
// authenticate with.
func hasCredentials(s *config.MCPServerConfig) bool {
	// A refresh-only token (access token expired but refresh token present) is
	// still authorized: Prism will mint a new access token on the next call.
	if s.OAuth != nil && (s.OAuth.AccessToken != "" || s.OAuth.RefreshToken != "") {
		return true
	}
	for k, v := range s.Headers {
		if v != "" && looksSecretHeaderName(k) {
			return true
		}
	}
	return false
}

func looksSecretHeaderName(name string) bool {
	return config.IsSecretHeader(name)
}

// parseToolList decodes a tools/list result, preserving unknown per-tool fields
// in Tool.Extra so upstream annotations survive the hop.
func parseToolList(raw json.RawMessage) ([]Tool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var outer struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("invalid tools/list result: %w", err)
	}
	tools := make([]Tool, 0, len(outer.Tools))
	for _, rawTool := range outer.Tools {
		var fields map[string]interface{}
		if err := json.Unmarshal(rawTool, &fields); err != nil {
			continue
		}
		t := Tool{}
		if v, ok := fields["name"].(string); ok {
			t.Name = v
		}
		if t.Name == "" {
			continue
		}
		if v, ok := fields["title"].(string); ok {
			t.Title = v
		}
		if v, ok := fields["description"].(string); ok {
			t.Description = v
		}
		if v, ok := fields["inputSchema"].(map[string]interface{}); ok {
			t.InputSchema = v
		}
		delete(fields, "name")
		delete(fields, "title")
		delete(fields, "description")
		delete(fields, "inputSchema")
		if len(fields) > 0 {
			t.Extra = fields
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// ToolMap renders a tool for the downstream tools/list response.
func ToolMap(t Tool) map[string]interface{} {
	out := map[string]interface{}{"name": t.Name}
	if t.Title != "" {
		out["title"] = t.Title
	}
	if t.Description != "" {
		out["description"] = t.Description
	}
	if t.InputSchema != nil {
		out["inputSchema"] = t.InputSchema
	} else {
		out["inputSchema"] = map[string]interface{}{"type": "object"}
	}
	for k, v := range t.Extra {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}
