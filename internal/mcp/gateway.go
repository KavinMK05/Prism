package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ollama-proxy/internal/agents"
	"ollama-proxy/internal/config"
)

// maxGatewayBody caps an inbound JSON-RPC message from an agent.
const maxGatewayBody = 8 << 20

// Gateway is Prism's downstream MCP endpoint. Agents connect to /mcp (every
// enabled server) or /mcp/<agent> (that agent's allowlist) with Prism's API
// token; Prism hides every upstream credential from them.
type Gateway struct {
	mgr *Manager
	cfg func() *config.Config
}

// NewGateway wires a gateway to a manager.
func NewGateway(mgr *Manager, cfgFn func() *config.Config) *Gateway {
	if cfgFn == nil {
		cfgFn = config.Current
	}
	return &Gateway{mgr: mgr, cfg: cfgFn}
}

var jsonRPCNullID = json.RawMessage("null")

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSuffix(r.URL.Path, "/") == "/mcp/status" {
		g.ServeStatus(w, r)
		return
	}
	if strings.TrimSuffix(r.URL.Path, "/") == "/mcp/control" {
		g.ServeControl(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeRPCStatusError(w, http.StatusMethodNotAllowed, "MCP endpoints accept POST (streamable HTTP)")
		return
	}
	agent := agentFromPath(r.URL.Path)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayBody))
	if err != nil {
		writeRPCStatusError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		writeRPCStatusError(w, http.StatusBadRequest, "empty request body")
		return
	}

	ctx := r.Context()
	var responses []rpcResponse
	if strings.HasPrefix(trimmed, "[") {
		var batch []rpcRequest
		if err := json.Unmarshal(body, &batch); err != nil {
			responses = append(responses, errorResponse(jsonRPCNullID, rpcErrorf(CodeParseError, "invalid JSON: %v", err)))
		} else {
			for _, req := range batch {
				if resp, ok := g.handleRequest(ctx, agent, req); ok {
					responses = append(responses, resp)
				}
			}
		}
	} else {
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			responses = append(responses, errorResponse(jsonRPCNullID, rpcErrorf(CodeParseError, "invalid JSON: %v", err)))
		} else if resp, ok := g.handleRequest(ctx, agent, req); ok {
			responses = append(responses, resp)
		}
	}

	if len(responses) == 0 {
		// Notifications only: acknowledge with an empty 202.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	g.writeResponses(w, r, responses)
}

// writeResponses answers either with a JSON body or with a request-scoped SSE
// stream, depending on what the client accepts. JSON is preferred because it
// is simpler to consume; SSE is used only when the client asks for it alone.
func (g *Gateway) writeResponses(w http.ResponseWriter, r *http.Request, responses []rpcResponse) {
	accept := r.Header.Get("Accept")
	wantJSON := accept == "" || strings.Contains(accept, "application/json") || strings.Contains(accept, "*/*")
	if wantJSON {
		w.Header().Set("Content-Type", "application/json")
		var payload interface{}
		if len(responses) == 1 {
			payload = responses[0]
		} else {
			payload = responses
		}
		json.NewEncoder(w).Encode(payload)
		return
	}
	if !strings.Contains(accept, "text/event-stream") {
		w.Header().Set("Content-Type", "application/json")
		var payload interface{} = responses[0]
		if len(responses) > 1 {
			payload = responses
		}
		json.NewEncoder(w).Encode(payload)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	for _, resp := range responses {
		data, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// agentFromPath maps /mcp to the aggregate endpoint and /mcp/<agent> to a
// per-agent view.
func agentFromPath(path string) string {
	p := strings.TrimSuffix(path, "/")
	if p == "" || p == "/mcp" {
		return ""
	}
	if strings.HasPrefix(p, "/mcp/") {
		return strings.TrimPrefix(p, "/mcp/")
	}
	return ""
}

// handleRequest dispatches one JSON-RPC message. The bool result is false for
// notifications, which get no reply.
func (g *Gateway) handleRequest(ctx context.Context, agent string, req rpcRequest) (rpcResponse, bool) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return errorResponse(reqID(req), rpcErrorf(CodeInvalidRequest, "unsupported JSON-RPC version %q", req.JSONRPC)), true
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		return rpcResponse{}, false
	}
	if len(req.ID) == 0 {
		// A request without an id is a notification by definition.
		return rpcResponse{}, false
	}
	id := reqID(req)

	switch req.Method {
	case "initialize":
		return resultResponse(id, g.initializeResult(req.Params)), true
	case "server/discover":
		return resultResponse(id, g.discoverResult()), true
	case "ping":
		return resultResponse(id, map[string]interface{}{}), true
	case "tools/list":
		return g.handleToolsList(ctx, id, agent), true
	case "tools/call":
		return g.handleToolsCall(ctx, id, agent, req.Params), true
	case "resources/list":
		return resultResponse(id, map[string]interface{}{"resources": []interface{}{}}), true
	case "prompts/list":
		return resultResponse(id, map[string]interface{}{"prompts": []interface{}{}}), true
	default:
		return errorResponse(id, rpcErrorf(CodeMethodNotFound, "method %q is not supported by the Prism gateway", req.Method)), true
	}
}

// initializeResult implements the legacy handshake so older clients (Claude
// Code, Codex, Zed) can connect, while advertising tool support.
func (g *Gateway) initializeResult(params json.RawMessage) map[string]interface{} {
	version := LegacyProtocolVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		version = p.ProtocolVersion
	}
	return map[string]interface{}{
		"protocolVersion": version,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{"listChanged": true},
		},
		"serverInfo":   map[string]interface{}{"name": ServerName, "version": mcpClientVersion},
		"instructions": gatewayInstructions(),
	}
}

// discoverResult implements the stateless revision's discovery method.
func (g *Gateway) discoverResult() map[string]interface{} {
	return map[string]interface{}{
		"protocolVersion": ProtocolVersion,
		"serverInfo":      map[string]interface{}{"name": ServerName, "version": mcpClientVersion},
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{"listChanged": true},
		},
		"instructions": gatewayInstructions(),
		"ttlMs":        int(toolCacheTTL / time.Millisecond),
		"cacheScope":   "private",
	}
}

func gatewayInstructions() string {
	return "Prism aggregates MCP servers for you. Tool names are namespaced as " +
		"mcp__<server>__<tool>; call them exactly as listed, and Prism routes each " +
		"call to the upstream server with its own credentials."
}

func (g *Gateway) handleToolsList(ctx context.Context, id json.RawMessage, agent string) rpcResponse {
	servers := g.serversForAgent(agent)
	tools, _ := g.mgr.ToolsForServers(ctx, servers)
	out := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		out = append(out, ToolMap(t))
	}
	return resultResponse(id, map[string]interface{}{
		"tools":      out,
		"ttlMs":      int(toolCacheTTL / time.Millisecond),
		"cacheScope": "private",
	})
}

func (g *Gateway) handleToolsCall(ctx context.Context, id json.RawMessage, agent string, params json.RawMessage) rpcResponse {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return errorResponse(id, rpcErrorf(CodeInvalidParams, "invalid tools/call params: %v", err))
	}
	if p.Name == "" {
		return errorResponse(id, rpcErrorf(CodeInvalidParams, "tools/call requires a tool name"))
	}
	serverID, tool, ok := SplitNamespacedToolName(p.Name)
	if !ok {
		return errorResponse(id, rpcErrorf(CodeInvalidParams,
			"tool %q is not namespaced; use the mcp__<server>__<tool> names from tools/list", p.Name))
	}
	server := g.findServerForAgent(agent, serverID)
	if server == nil {
		return errorResponse(id, rpcErrorf(CodeInvalidParams,
			"server %q is not enabled for this endpoint", serverID))
	}
	raw, err := g.mgr.CallTool(ctx, server, tool, p.Arguments)
	if err != nil {
		return errorResponse(id, errorFromUpstream(err, serverID))
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{"content":[]}`)
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: raw}
}

// serversForAgent returns the servers an endpoint may reach.
func (g *Gateway) serversForAgent(agent string) []*config.MCPServerConfig {
	cfg := g.cfg()
	if cfg == nil {
		return nil
	}
	if agent == "" {
		return cfg.EnabledMCPServers()
	}
	return cfg.MCPServersForAgent(agent)
}

// findServerForAgent returns the server when (and only when) it is reachable
// from this endpoint.
func (g *Gateway) findServerForAgent(agent, serverID string) *config.MCPServerConfig {
	for _, s := range g.serversForAgent(agent) {
		if s.ID == serverID {
			return s
		}
	}
	return nil
}

// ServeStatus reports the health of every configured server. The admin UI
// reads this through the proxy's authenticated /mcp/status endpoint.
func (g *Gateway) ServeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeRPCStatusError(w, http.StatusMethodNotAllowed, "status is a GET endpoint")
		return
	}
	statuses := g.mgr.Statuses()
	if statuses == nil {
		statuses = []ServerStatus{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"statuses": statuses,
		"agents":   g.agentBindings(),
	})
}

// ServeControl is the admin-facing control channel (restart, probe). It lives
// on the proxy because the proxy process owns the upstream connections.
func (g *Gateway) ServeControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeRPCStatusError(w, http.StatusMethodNotAllowed, "control is a POST endpoint")
		return
	}
	var req struct {
		Action string `json:"action"`
		Server string `json:"server"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeRPCStatusError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	switch req.Action {
	case "status":
		encodeGatewayJSON(w, map[string]interface{}{"statuses": orEmptyStatuses(g.mgr.Statuses())})
	case "restart":
		if req.Server == "" {
			g.mgr.RestartAll()
		} else {
			g.mgr.Restart(req.Server)
		}
		encodeGatewayJSON(w, map[string]interface{}{"status": "ok"})
	case "probe":
		if req.Server == "" {
			writeRPCStatusError(w, http.StatusBadRequest, "probe requires a server id")
			return
		}
		status, err := g.mgr.Probe(ctx, req.Server)
		payload := map[string]interface{}{"status": status}
		if err != nil {
			payload["error"] = err.Error()
		}
		encodeGatewayJSON(w, payload)
	default:
		writeRPCStatusError(w, http.StatusBadRequest, "unknown action "+req.Action)
	}
}

func orEmptyStatuses(s []ServerStatus) []ServerStatus {
	if s == nil {
		return []ServerStatus{}
	}
	return s
}

func encodeGatewayJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// agentBindings reports, for each agent, which servers it may reach and whether
// Prism's MCP entry has been installed in that agent's config.
func (g *Gateway) agentBindings() []map[string]interface{} {
	cfg := g.cfg()
	if cfg == nil || cfg.MCP == nil {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(cfg.MCP.AgentServers))
	for _, agent := range agents.AllAgentIDs() {
		ids := cfg.MCP.AgentServers[agent]
		if ids == nil {
			ids = []string{}
		}
		out = append(out, map[string]interface{}{
			"agent":   agent,
			"servers": ids,
		})
	}
	return out
}

// errorFromUpstream converts an upstream failure into a JSON-RPC error the
// agent can act on (needs_auth is the interesting one: the user has to connect
// the server in Prism).
func errorFromUpstream(err error, serverID string) *rpcError {
	var authErr *AuthRequiredError
	if errors.As(err, &authErr) {
		return &rpcError{
			Code:    CodeInvalidParams,
			Message: fmt.Sprintf("MCP server %q needs authorization in Prism before its tools can be used", serverID),
			Data:    map[string]interface{}{"reason": "needs_auth", "server": serverID},
		}
	}
	var missing *RuntimeMissingError
	if errors.As(err, &missing) {
		return &rpcError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("MCP server %q cannot start: runtime %q was not found on PATH", serverID, missing.Command),
			Data:    map[string]interface{}{"reason": "runtime_missing", "server": serverID},
		}
	}
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &rpcError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("MCP server %q did not respond in time", serverID),
			Data:    map[string]interface{}{"reason": "timeout", "server": serverID},
		}
	}
	return &rpcError{
		Code:    CodeInternalError,
		Message: fmt.Sprintf("MCP server %q failed: %v", serverID, err),
		Data:    map[string]interface{}{"reason": "server_error", "server": serverID},
	}
}

func reqID(req rpcRequest) json.RawMessage {
	if len(req.ID) == 0 {
		return jsonRPCNullID
	}
	return req.ID
}

func resultResponse(id json.RawMessage, result interface{}) rpcResponse {
	raw, err := json.Marshal(result)
	if err != nil {
		return errorResponse(id, rpcErrorf(CodeInternalError, "failed to encode result: %v", err))
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: raw}
}

func errorResponse(id json.RawMessage, e *rpcError) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: e}
}

func writeRPCStatusError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"error":   map[string]interface{}{"code": CodeInvalidRequest, "message": message},
	})
}
