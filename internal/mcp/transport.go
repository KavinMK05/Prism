package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ollama-proxy/internal/config"
)

// transport carries JSON-RPC messages to one upstream MCP server.
type transport interface {
	RoundTrip(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
	Notify(ctx context.Context, method string, params interface{}) error
	Close() error
}

// dynamicHeaders supplies per-request headers (bearer tokens, API keys) so an
// expired OAuth token is refreshed without rebuilding the transport.
type dynamicHeaders func(ctx context.Context) (map[string]string, error)

// toolParamsFunc supplies the x-mcp-header mapping (parameter path -> header
// name) of one tool, learned from its tools/list schema, so a tools/call can
// mirror those arguments into Mcp-Param-* headers.
type toolParamsFunc func(tool string) map[string]string

const mcpClientVersion = "1.0.0"

// newTransport builds the transport for a server config.
func newTransport(s *config.MCPServerConfig, headers dynamicHeaders, params toolParamsFunc) (transport, error) {
	switch s.Transport {
	case config.MCPTransportStdio, "":
		return newStdioTransport(s)
	case config.MCPTransportHTTP:
		return newHTTPTransport(s.URL, s.Headers, headers, params, false), nil
	case config.MCPTransportSSE:
		return newHTTPTransport(s.URL, s.Headers, headers, params, true), nil
	default:
		return nil, rpcErrorf(CodeInvalidParams, "unsupported transport %q", s.Transport)
	}
}

// resolveCommand finds an executable, preferring the process PATH and then the
// usual install locations a GUI process does not inherit.
func resolveCommand(command string) (string, bool) {
	if command == "" {
		return "", false
	}
	if filepath.IsAbs(command) || strings.HasPrefix(command, "./") || strings.HasPrefix(command, ".\\") {
		if info, err := os.Stat(command); err == nil && !info.IsDir() {
			return command, true
		}
		return "", false
	}
	if p, err := exec.LookPath(command); err == nil && p != "" {
		return p, true
	}
	home, _ := os.UserHomeDir()
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".local", "share", "mise", "shims"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".opencode", "bin"),
		)
	}
	if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
		dirs = append(dirs, filepath.Join(cfgDir, "npm"))
	}
	for _, dir := range dirs {
		candidates := []string{filepath.Join(dir, command)}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				filepath.Join(dir, command+".exe"),
				filepath.Join(dir, command+".cmd"),
				filepath.Join(dir, command+".bat"),
			)
		}
		for _, p := range candidates {
			info, err := os.Stat(p)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue
			}
			return p, true
		}
	}
	return "", false
}

// commandRunner adapts a resolved command for the platform. Windows cannot
// exec a .cmd/.bat shim (npx, uvx) directly, so those go through cmd.exe.
func commandRunner(resolved string, args []string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(resolved))
		if ext == ".cmd" || ext == ".bat" {
			comspec := os.Getenv("ComSpec")
			if comspec == "" {
				comspec = "cmd.exe"
			}
			all := append([]string{"/c", resolved}, args...)
			return exec.Command(comspec, all...)
		}
	}
	return exec.Command(resolved, args...)
}
