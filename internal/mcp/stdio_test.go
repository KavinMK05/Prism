package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ollama-proxy/internal/config"
)

// TestMCPStdioHelperServer is not a real test: it is the tiny MCP server the
// lifecycle test spawns. It only runs when the env var marks it as a child, so
// a normal `go test` skips it.
func TestMCPStdioHelperServer(t *testing.T) {
	if os.Getenv("PRISM_MCP_HELPER") != "1" {
		t.Skip("helper process; only used when spawned as a stdio MCP server")
	}

	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // notification: no reply
		}

		toolName := ""
		var result interface{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": LegacyProtocolVersion,
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]interface{}{"name": "helper", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "echo", "description": "Echo the tool name back"},
				},
			}
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			toolName = p.Name
			result = map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": "echo:" + p.Name}},
			}
		default:
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErrorf(CodeMethodNotFound, "unsupported")})
			continue
		}

		raw, _ := json.Marshal(result)
		_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: raw})

		if toolName == "crash" {
			// Answer, then die: the transport must notice and recover.
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func helperServerConfig(t *testing.T) *config.MCPServerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return &config.MCPServerConfig{
		ID:        "helper",
		Name:      "Helper",
		Transport: config.MCPTransportStdio,
		Command:   exe,
		Args:      []string{"-test.run=TestMCPStdioHelperServer"},
		Env:       map[string]string{"PRISM_MCP_HELPER": "1"},
		Enabled:   true,
	}
}

func TestStdioLifecycle(t *testing.T) {
	srv := helperServerConfig(t)
	cfg := newTestConfig(srv)
	cfg.MCP.IdleTimeoutSec = 1

	mgr := NewManager(func() *config.Config { return cfg })
	t.Cleanup(mgr.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Cold start: nothing is spawned until the first call.
	if rt := mgr.runtimeFor("helper"); rt.tr != nil {
		t.Fatal("the stdio server was spawned before its first use")
	}

	tools, statuses := mgr.ToolsForServers(ctx, []*config.MCPServerConfig{srv})
	if len(tools) != 1 || tools[0].Name != "mcp__helper__echo" {
		t.Fatalf("tools = %+v", tools)
	}
	if len(statuses) != 1 || statuses[0].State != config.MCPStateReady {
		t.Fatalf("status = %+v", statuses)
	}
	if rt := mgr.runtimeFor("helper"); rt.tr == nil {
		t.Fatal("no live transport after the first tools/list")
	}

	raw, err := mgr.CallTool(ctx, srv, "echo", json.RawMessage(`{"value":1}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(string(raw), "echo:echo") {
		t.Errorf("tool result = %s", raw)
	}

	// Cached tool lists mean the second listing does not need the process to
	// still be alive; a call does.
	if _, err := mgr.CallTool(ctx, srv, "crash", nil); err != nil {
		t.Logf("crash call surfaced an error (racing EOF is acceptable): %v", err)
	}
	if _, err := mgr.CallTool(ctx, srv, "echo", nil); err != nil {
		t.Fatalf("CallTool after the helper crashed: %v", err)
	}

	// Idle reaping shuts the child process down and resets the cached tools.
	rt := mgr.runtimeFor("helper")
	rt.mu.Lock()
	rt.lastUsed = time.Now().Add(-time.Hour)
	rt.mu.Unlock()
	mgr.reapIdle()

	rt.mu.Lock()
	tr, state := rt.tr, rt.state
	rt.mu.Unlock()
	if tr != nil {
		t.Error("idle stdio process was not reaped")
	}
	if state != config.MCPStateIdle {
		t.Errorf("state after reaping = %q, want idle", state)
	}

	// ...and it lazily starts again.
	if _, err := mgr.CallTool(ctx, srv, "echo", nil); err != nil {
		t.Fatalf("CallTool after reaping: %v", err)
	}
}

func TestStdioRuntimeMissing(t *testing.T) {
	srv := &config.MCPServerConfig{
		ID:        "missing",
		Name:      "Missing",
		Transport: config.MCPTransportStdio,
		Command:   "prism-definitely-not-installed-binary",
		Enabled:   true,
	}
	cfg := newTestConfig(srv)
	mgr := NewManager(func() *config.Config { return cfg })
	t.Cleanup(mgr.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, statuses := mgr.ToolsForServers(ctx, []*config.MCPServerConfig{srv})
	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].State != config.MCPStateRuntimeMissing {
		t.Errorf("state = %q, want runtime_missing (err %q)", statuses[0].State, statuses[0].LastError)
	}

	_, err := mgr.CallTool(ctx, srv, "anything", nil)
	var missing *RuntimeMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("CallTool error = %v, want RuntimeMissingError", err)
	}
	if missing.Command != srv.Command {
		t.Errorf("reported command = %q, want %q", missing.Command, srv.Command)
	}
}
