package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ollama-proxy/internal/config"
)

// fakeUpstream is a minimal MCP Streamable HTTP server: it answers initialize,
// tools/list and tools/call with JSON bodies and records the last call.
type fakeUpstream struct {
	mu       sync.Mutex
	lastTool string
	lastArgs json.RawMessage
	requests int
}

func (f *fakeUpstream) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req rpcRequest
	_ = json.Unmarshal(body, &req)

	f.mu.Lock()
	f.requests++
	f.mu.Unlock()

	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	writeResult := func(result interface{}) {
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: raw})
	}

	switch req.Method {
	case "initialize":
		writeResult(map[string]interface{}{
			"protocolVersion": LegacyProtocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "fake", "version": "1.0.0"},
		})
	case "tools/list":
		writeResult(map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "search",
					"description": "Search notes",
					"inputSchema": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{"q": map[string]interface{}{"type": "string"}},
					},
				},
				{"name": "list_notes"},
			},
		})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		f.mu.Lock()
		f.lastTool = p.Name
		f.lastArgs = p.Arguments
		f.mu.Unlock()
		writeResult(map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": "ok:" + p.Name}},
		})
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   rpcErrorf(CodeMethodNotFound, "method %q not supported", req.Method),
		})
	}
}

func startFakeUpstream(t *testing.T) (*fakeUpstream, string) {
	t.Helper()
	f := &fakeUpstream{}
	ts := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(ts.Close)
	return f, ts.URL
}

func newTestConfig(servers ...*config.MCPServerConfig) *config.Config {
	cfg := &config.Config{DefaultProvider: "ollama_cloud"}
	m := cfg.EnsureMCP()
	m.Servers = append(m.Servers, servers...)
	return cfg
}

func testServer(id, url string) *config.MCPServerConfig {
	return &config.MCPServerConfig{
		ID:        id,
		Name:      id,
		Transport: config.MCPTransportHTTP,
		URL:       url,
		Enabled:   true,
		AuthMode:  config.MCPAuthNone,
	}
}

func newTestGateway(cfg *config.Config) *Gateway {
	mgr := NewManager(func() *config.Config { return cfg })
	return NewGateway(mgr, func() *config.Config { return cfg })
}

// postRPC drives one JSON-RPC message through the gateway.
func postRPC(g *Gateway, path, accept, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func decodeRPC(t *testing.T, rec *httptest.ResponseRecorder) rpcResponse {
	t.Helper()
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not a JSON-RPC message: %v\n%s", err, rec.Body.String())
	}
	return resp
}

func resultObject(t *testing.T, resp rpcResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("result is not an object: %v", err)
	}
	return out
}

func TestGatewayDiscoverAndInitialize(t *testing.T) {
	g := newTestGateway(newTestConfig())

	rec := postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	discover := resultObject(t, decodeRPC(t, rec))
	if discover["protocolVersion"] != ProtocolVersion {
		t.Errorf("discover protocolVersion = %v, want %s", discover["protocolVersion"], ProtocolVersion)
	}
	info := discover["serverInfo"].(map[string]interface{})
	if info["name"] != ServerName {
		t.Errorf("serverInfo.name = %v", info["name"])
	}

	// The legacy handshake echoes the client's version and still advertises tools.
	rec = postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"1"}}}`)
	init := resultObject(t, decodeRPC(t, rec))
	if init["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize protocolVersion = %v", init["protocolVersion"])
	}
	caps, _ := init["capabilities"].(map[string]interface{})
	if _, ok := caps["tools"]; !ok {
		t.Errorf("initialize capabilities missing tools: %v", caps)
	}

	// ping, resources/list and prompts/list answer with empty results.
	for _, method := range []string{"ping", "tools/list", "resources/list", "prompts/list"} {
		rec = postRPC(g, "/mcp", "application/json", `{"jsonrpc":"2.0","id":3,"method":"`+method+`"}`)
		if resp := decodeRPC(t, rec); resp.Error != nil {
			t.Errorf("%s returned an error: %v", method, resp.Error)
		}
	}

	// Unknown methods are a protocol error, not an HTTP failure.
	rec = postRPC(g, "/mcp", "application/json", `{"jsonrpc":"2.0","id":4,"method":"sampling/createMessage"}`)
	if resp := decodeRPC(t, rec); resp.Error == nil || resp.Error.Code != CodeMethodNotFound {
		t.Errorf("unknown method error = %+v", resp.Error)
	}
}

func TestGatewayToolsListNamespacing(t *testing.T) {
	_, url := startFakeUpstream(t)
	cfg := newTestConfig(testServer("notes", url))
	g := newTestGateway(cfg)

	rec := postRPC(g, "/mcp", "application/json", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result := resultObject(t, decodeRPC(t, rec))
	tools, _ := result["tools"].([]interface{})
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %v", len(tools), tools)
	}
	names := map[string]map[string]interface{}{}
	for _, raw := range tools {
		tool := raw.(map[string]interface{})
		names[tool["name"].(string)] = tool
	}
	search, ok := names["mcp__notes__search"]
	if !ok {
		t.Fatalf("namespaced tool missing, got %v", names)
	}
	if search["description"] != "Search notes" {
		t.Errorf("description not relayed: %v", search["description"])
	}
	if _, ok := search["inputSchema"]; !ok {
		t.Error("inputSchema not relayed")
	}
	if _, ok := names["mcp__notes__list_notes"]; !ok {
		t.Errorf("second tool missing: %v", names)
	}
}

func TestGatewayToolsCallRouting(t *testing.T) {
	f, url := startFakeUpstream(t)
	g := newTestGateway(newTestConfig(testServer("notes", url)))

	rec := postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mcp__notes__search","arguments":{"q":"hello"}}}`)
	resp := decodeRPC(t, rec)
	result := resultObject(t, resp)
	content, _ := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("unexpected content: %v", result["content"])
	}
	if text := content[0].(map[string]interface{})["text"]; text != "ok:search" {
		t.Errorf("content text = %v", text)
	}

	// The upstream must receive the *un-namespaced* tool name and the arguments.
	f.mu.Lock()
	gotTool, gotArgs := f.lastTool, string(f.lastArgs)
	f.mu.Unlock()
	if gotTool != "search" {
		t.Errorf("upstream saw tool %q, want %q", gotTool, "search")
	}
	if !strings.Contains(gotArgs, `"q":"hello"`) {
		t.Errorf("upstream arguments = %s", gotArgs)
	}

	// A bare tool name is rejected with guidance.
	rec = postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search"}}`)
	if resp := decodeRPC(t, rec); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Errorf("un-namespaced call error = %+v", resp.Error)
	}

	// An unknown server id is rejected.
	rec = postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mcp__ghost__search"}}`)
	if resp := decodeRPC(t, rec); resp.Error == nil {
		t.Error("unknown server should be rejected")
	}
}

func TestGatewayPerAgentScoping(t *testing.T) {
	_, url := startFakeUpstream(t)
	cfg := newTestConfig(testServer("notes", url))
	cfg.SetAgentMCPServers("claude-code", []string{"notes"})
	g := newTestGateway(cfg)

	countTools := func(path string) int {
		rec := postRPC(g, path, "application/json", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		result := resultObject(t, decodeRPC(t, rec))
		tools, _ := result["tools"].([]interface{})
		return len(tools)
	}

	if n := countTools("/mcp"); n != 2 {
		t.Errorf("aggregate endpoint exposed %d tools, want 2", n)
	}
	if n := countTools("/mcp/claude-code"); n != 2 {
		t.Errorf("allowlisted agent exposed %d tools, want 2", n)
	}
	if n := countTools("/mcp/zed"); n != 0 {
		t.Errorf("agent with an empty allowlist exposed %d tools, want 0", n)
	}

	// Calls from an endpoint that cannot reach the server fail with a clear
	// message rather than leaking the tool.
	rec := postRPC(g, "/mcp/zed", "application/json",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcp__notes__search"}}`)
	resp := decodeRPC(t, rec)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "not enabled") {
		t.Errorf("scoped call error = %+v", resp.Error)
	}
}

func TestGatewaySSEAndNotifications(t *testing.T) {
	_, url := startFakeUpstream(t)
	g := newTestGateway(newTestConfig(testServer("notes", url)))

	// A client that only accepts SSE gets an event stream.
	rec := postRPC(g, "/mcp", "text/event-stream", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: message") || !strings.Contains(body, "mcp__notes__search") {
		t.Errorf("SSE body missing the reply: %s", body)
	}

	// Notifications get no reply body.
	rec = postRPC(g, "/mcp", "application/json", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Errorf("notification status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification returned a body: %s", rec.Body.String())
	}

	// Batches are answered with an array.
	rec = postRPC(g, "/mcp", "application/json",
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"server/discover"}]`)
	var batch []rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil {
		t.Fatalf("batch response is not an array: %v\n%s", err, rec.Body.String())
	}
	if len(batch) != 2 {
		t.Errorf("batch returned %d responses, want 2", len(batch))
	}

	// GET is not allowed on the message endpoint.
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec2 := httptest.NewRecorder()
	g.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp status = %d, want 405", rec2.Code)
	}
}

func TestGatewayErrorMapping(t *testing.T) {
	// needs_auth: an OAuth server that has never been authorized.
	oauthServer := &config.MCPServerConfig{
		ID:        "notion",
		Name:      "Notion",
		Transport: config.MCPTransportHTTP,
		URL:       "https://mcp.notion.com/mcp",
		Enabled:   true,
		AuthMode:  config.MCPAuthOAuth,
	}
	g := newTestGateway(newTestConfig(oauthServer))
	rec := postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mcp__notion__search"}}`)
	errObj := decodeRPC(t, rec).Error
	if errObj == nil {
		t.Fatal("expected an error for an unauthorized server")
	}
	data, _ := errObj.Data.(map[string]interface{})
	if data["reason"] != "needs_auth" {
		t.Errorf("reason = %v, want needs_auth (error %v)", data["reason"], errObj)
	}

	// runtime_missing: a stdio server whose command is not installed.
	missing := &config.MCPServerConfig{
		ID:        "broken",
		Name:      "Broken",
		Transport: config.MCPTransportStdio,
		Command:   "prism-definitely-not-installed-binary",
		Enabled:   true,
	}
	g = newTestGateway(newTestConfig(missing))
	rec = postRPC(g, "/mcp", "application/json",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcp__broken__anything"}}`)
	errObj = decodeRPC(t, rec).Error
	if errObj == nil {
		t.Fatal("expected an error for a missing runtime")
	}
	data, _ = errObj.Data.(map[string]interface{})
	if data["reason"] != "runtime_missing" {
		t.Errorf("reason = %v, want runtime_missing (error %v)", data["reason"], errObj)
	}

	// tools/list reports failures per server instead of failing outright.
	rec = postRPC(g, "/mcp", "application/json", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	result := resultObject(t, decodeRPC(t, rec))
	if tools, _ := result["tools"].([]interface{}); len(tools) != 0 {
		t.Errorf("broken server exposed %d tools", len(tools))
	}
}

func TestGatewayStatusAndControl(t *testing.T) {
	_, url := startFakeUpstream(t)
	cfg := newTestConfig(testServer("notes", url))
	cfg.SetAgentMCPServers("claude-code", []string{"notes"})
	g := newTestGateway(cfg)

	req := httptest.NewRequest(http.MethodGet, "/mcp/status", nil)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var payload struct {
		Statuses []ServerStatus `json:"statuses"`
		Agents   []struct {
			Agent   string   `json:"agent"`
			Servers []string `json:"servers"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("status payload: %v", err)
	}
	if len(payload.Statuses) != 1 || payload.Statuses[0].ID != "notes" {
		t.Fatalf("statuses = %+v", payload.Statuses)
	}
	if payload.Statuses[0].State != config.MCPStateIdle {
		t.Errorf("cold server state = %q, want idle", payload.Statuses[0].State)
	}
	found := false
	for _, binding := range payload.Agents {
		if binding.Agent == "claude-code" && len(binding.Servers) == 1 && binding.Servers[0] == "notes" {
			found = true
		}
	}
	if !found {
		t.Errorf("agent bindings missing the allowlist: %+v", payload.Agents)
	}

	// probe forces a connection and refreshes the cached tool list.
	rec = postRPC(g, "/mcp/control", "application/json", `{"action":"probe","server":"notes"}`)
	var probe struct {
		Status ServerStatus `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatalf("probe payload: %v", err)
	}
	if probe.Status.State != config.MCPStateReady {
		t.Errorf("probed state = %q, want ready (err %s)", probe.Status.State, probe.Status.LastError)
	}
	if probe.Status.ToolCount != 2 {
		t.Errorf("probed tool count = %d, want 2", probe.Status.ToolCount)
	}

	// restart drops the connection so the next call reconnects.
	rec = postRPC(g, "/mcp/control", "application/json", `{"action":"restart","server":"notes"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restart status = %d", rec.Code)
	}
	if status, ok := g.mgr.Status("notes"); !ok || status.State != config.MCPStateIdle {
		t.Errorf("state after restart = %+v", status)
	}
}
