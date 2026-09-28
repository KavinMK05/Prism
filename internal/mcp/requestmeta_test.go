package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// The example table from the stateless revision's Value Encoding section: a
// value is mirrored verbatim when it is header-safe, and base64-wrapped with
// the sentinel when it is not.
func TestMirrorHeaderValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"us-west1", "us-west1"},
		{"Notion", "Notion"},
		{"Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?="},
		{" padded ", "=?base64?IHBhZGRlZCA=?="},
		{"line1\nline2", "=?base64?bGluZTEKbGluZTI=?="},
		{"=?base64?literal?=", "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?="},
		{"", "=?base64??="},
		{"tab\there", "tab\there"},
		{"cr\rhere", "=?base64?Y3INaGVyZQ==?="},
	}
	for _, tc := range cases {
		if got := mirrorHeaderValue(tc.in); got != tc.want {
			t.Errorf("mirrorHeaderValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMCPRoutingName(t *testing.T) {
	cases := []struct {
		method string
		params string
		want   string
		ok     bool
	}{
		{"tools/call", `{"name":"search","arguments":{"q":"x"}}`, "search", true},
		{"prompts/get", `{"name":"review"}`, "review", true},
		{"resources/read", `{"uri":"file:///tmp/a.txt"}`, "file:///tmp/a.txt", true},
		{"tools/call", `{"name":""}`, "", false},
		{"tools/call", `{"name":null}`, "", false},
		{"tools/call", `{}`, "", false},
		{"tools/list", `{}`, "", false},
		{"initialize", `{"protocolVersion":"2025-06-18"}`, "", false},
	}
	for _, tc := range cases {
		got, ok := mcpRoutingName(tc.method, json.RawMessage(tc.params))
		if ok != tc.ok || got != tc.want {
			t.Errorf("mcpRoutingName(%s, %s) = (%q, %v), want (%q, %v)", tc.method, tc.params, got, ok, tc.want, tc.ok)
		}
	}
}

func TestXMPCHeaderParams(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"region": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
			"count":  map[string]interface{}{"type": "integer", "x-mcp-header": "Count"},
			"config": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tenant": map[string]interface{}{"type": "string", "x-mcp-header": "Tenant"},
				},
			},
			"items": map[string]interface{}{
				// Array keywords are not statically reachable: never mirrored.
				"type":  "array",
				"items": map[string]interface{}{"x-mcp-header": "Ignored"},
			},
		},
	}
	mapping := xMCPHeaderParams(schema)
	want := map[string]string{"region": "Region", "count": "Count", "config.tenant": "Tenant"}
	if len(mapping) != len(want) {
		t.Fatalf("mapping = %v, want %v", mapping, want)
	}
	for k, v := range want {
		if mapping[k] != v {
			t.Errorf("mapping[%q] = %q, want %q", k, mapping[k], v)
		}
	}

	headers := mcpParamHeaders(mapping, json.RawMessage(`{"region":"us-west1","count":42,"config":{"tenant":"acme"},"absent":1}`))
	if headers["Mcp-Param-Region"] != "us-west1" {
		t.Errorf("Region header = %q", headers["Mcp-Param-Region"])
	}
	if headers["Mcp-Param-Count"] != "42" {
		t.Errorf("Count header = %q, want 42", headers["Mcp-Param-Count"])
	}
	if headers["Mcp-Param-Tenant"] != "acme" {
		t.Errorf("Tenant header = %q", headers["Mcp-Param-Tenant"])
	}
	if len(headers) != 3 {
		t.Errorf("headers = %v, want exactly the three declared parameters", headers)
	}

	// A null or absent parameter must not produce a header at all.
	if got := mcpParamHeaders(mapping, json.RawMessage(`{"region":null}`)); len(got) != 0 {
		t.Errorf("null parameter produced %v", got)
	}
}

// metadataUpstream records the request-metadata headers of every call and
// answers like a server that speaks both protocol eras.
type metadataUpstream struct {
	mu   sync.Mutex
	seen []map[string]string
}

func (f *metadataUpstream) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req rpcRequest
	_ = json.Unmarshal(body, &req)

	record := map[string]string{
		"method":  req.Method,
		"proto":   r.Header.Get("MCP-Protocol-Version"),
		"mcp":     r.Header.Get("Mcp-Method"),
		"name":    r.Header.Get("Mcp-Name"),
		"region":  r.Header.Get("Mcp-Param-Region"),
		"session": r.Header.Get("Mcp-Session-Id"),
	}
	f.mu.Lock()
	f.seen = append(f.seen, record)
	f.mu.Unlock()

	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result interface{}
	switch req.Method {
	case "server/discover":
		result = map[string]interface{}{
			"supportedVersions": []string{ProtocolVersion},
			"capabilities":      map[string]interface{}{},
		}
	case "initialize":
		result = map[string]interface{}{
			"protocolVersion": LegacyProtocolVersion,
			"capabilities":    map[string]interface{}{},
			"serverInfo":      map[string]string{"name": "legacy", "version": "1.0.0"},
		}
	case "tools/list":
		result = map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name": "search",
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"region": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
						},
					},
				},
			},
		}
	case "tools/call":
		result = map[string]interface{}{"content": []map[string]interface{}{{"type": "text", "text": "ok"}}}
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   rpcErrorf(CodeMethodNotFound, "method %q not supported", req.Method),
		})
		return
	}
	raw, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: raw})
}

// The transport must mirror the body into the headers the stateless revision
// requires, and keep the protocol version header consistent with the version it
// advertises in the body.
func TestHTTPTransportRequestMetadata(t *testing.T) {
	f := &metadataUpstream{}
	ts := httptest.NewServer(http.HandlerFunc(f.handler))
	defer ts.Close()

	schema := map[string]string{"region": "Region"}
	tr := newHTTPTransport(ts.URL, nil, nil, func(tool string) map[string]string {
		if tool == "search" {
			return schema
		}
		return nil
	}, false)
	defer tr.Close()

	ctx := context.Background()
	if err := handshake(ctx, tr); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := tr.RoundTrip(ctx, "tools/list", map[string]interface{}{}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if _, err := tr.RoundTrip(ctx, "tools/call", map[string]interface{}{
		"name":      "search",
		"arguments": map[string]interface{}{"region": "us-west1"},
	}); err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 3 {
		t.Fatalf("upstream saw %d requests, want 3: %v", len(f.seen), f.seen)
	}
	if got := f.seen[0]; got["method"] != "server/discover" || got["mcp"] != "server/discover" {
		t.Errorf("discover headers = %v", got)
	}
	for i, want := range []string{"server/discover", "tools/list", "tools/call"} {
		if f.seen[i]["mcp"] != want {
			t.Errorf("request %d Mcp-Method = %q, want %q", i, f.seen[i]["mcp"], want)
		}
		if f.seen[i]["proto"] != ProtocolVersion {
			t.Errorf("request %d MCP-Protocol-Version = %q, want %q", i, f.seen[i]["proto"], ProtocolVersion)
		}
	}
	if f.seen[1]["name"] != "" {
		t.Errorf("tools/list carried Mcp-Name %q", f.seen[1]["name"])
	}
	if f.seen[2]["name"] != "search" {
		t.Errorf("tools/call Mcp-Name = %q, want search", f.seen[2]["name"])
	}
	if f.seen[2]["region"] != "us-west1" {
		t.Errorf("tools/call Mcp-Param-Region = %q, want us-west1", f.seen[2]["region"])
	}
}

// stubTransport records the methods the handshake tries and answers from a
// script, so era detection can be tested without a server.
type stubTransport struct {
	mu     sync.Mutex
	calls  []string
	reply  map[string]error
	result map[string]json.RawMessage
}

func (s *stubTransport) RoundTrip(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
	s.mu.Lock()
	s.calls = append(s.calls, method)
	err := s.reply[method]
	result := s.result[method]
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *stubTransport) Notify(_ context.Context, method string, _ interface{}) error {
	s.mu.Lock()
	s.calls = append(s.calls, "notify:"+method)
	s.mu.Unlock()
	return nil
}

func (s *stubTransport) Close() error { return nil }

func (s *stubTransport) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func TestHandshakePrefersStatelessRevision(t *testing.T) {
	modern := &stubTransport{result: map[string]json.RawMessage{
		"server/discover": json.RawMessage(`{"supportedVersions":["` + ProtocolVersion + `"]}`),
	}}
	if err := handshake(context.Background(), modern); err != nil {
		t.Fatalf("modern handshake: %v", err)
	}
	if calls := modern.methods(); len(calls) != 1 || calls[0] != "server/discover" {
		t.Fatalf("modern handshake made calls %v, want only server/discover", calls)
	}
}

func TestHandshakeFallsBackToInitialize(t *testing.T) {
	legacy := &stubTransport{
		reply: map[string]error{
			"server/discover": rpcErrorf(CodeMethodNotFound, "method not found"),
		},
		result: map[string]json.RawMessage{
			"initialize": json.RawMessage(`{"protocolVersion":"` + LegacyProtocolVersion + `"}`),
		},
	}
	if err := handshake(context.Background(), legacy); err != nil {
		t.Fatalf("legacy handshake: %v", err)
	}
	calls := legacy.methods()
	want := []string{"server/discover", "initialize", "notify:notifications/initialized"}
	if len(calls) != len(want) {
		t.Fatalf("legacy handshake made calls %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("legacy handshake made calls %v, want %v", calls, want)
		}
	}
}

// A modern server rejects initialize, so the discovery error is the useful one.
func TestHandshakeReportsDiscoveryErrorWhenBothFail(t *testing.T) {
	tr := &stubTransport{reply: map[string]error{
		"server/discover": rpcErrorf(-32022, "unsupported protocol version"),
		"initialize":      rpcErrorf(CodeMethodNotFound, "method not found"),
	}}
	err := handshake(context.Background(), tr)
	if err == nil {
		t.Fatal("handshake succeeded, want the discovery failure")
	}
	var rpcErr *rpcError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32022 {
		t.Fatalf("handshake error = %v, want the discovery error", err)
	}
}

// An HTTP status whose body is a JSON-RPC error must keep the code, because
// that is how a modern server declines an unsupported method.
func TestHTTPErrorKeepsJSONRPCCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
	}))
	defer ts.Close()

	tr := newHTTPTransport(ts.URL, nil, nil, nil, false)
	defer tr.Close()
	_, err := tr.RoundTrip(context.Background(), "server/discover", map[string]interface{}{})
	if !isMethodNotFound(err) {
		t.Fatalf("error = %v, want a method-not-found error", err)
	}
}
