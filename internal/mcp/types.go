// Package mcp implements Prism's MCP gateway: it connects upstream to MCP
// servers (local stdio processes and remote Streamable HTTP endpoints) and
// re-exposes their tools to agents through a single namespaced MCP endpoint.
package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Protocol versions Prism speaks downstream. The modern value is the stateless
// 2026-07-28 revision; the legacy value is used when an older client opens with
// an initialize handshake.
const (
	ProtocolVersion       = "2026-07-28"
	LegacyProtocolVersion = "2025-06-18"
)

// ServerName is the identity Prism reports to downstream clients and to
// upstream servers.
const ServerName = "prism"

// ToolNamePrefix is the namespace Prism puts in front of every upstream tool
// name: mcp_<server>_<tool>. The separators are single underscores on purpose:
// agents that qualify MCP tools as server__tool (Grok Build, Codex) reject a
// tool name that itself contains "__" as ambiguous and drop the tool entirely,
// so a downstream name Prism emits must never contain one.
const ToolNamePrefix = "mcp_"

// toolNameSeparator joins the namespaced server id and the tool name.
const toolNameSeparator = "_"

// legacyToolNamePrefix is the mcp__<server>__<tool> shape Prism exposed before
// the switch. Still accepted on tools/call, because an agent that cached a tool
// list before an upgrade keeps calling the names it was given.
const legacyToolNamePrefix = "mcp__"

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Tool is the subset of the MCP tool shape Prism relays. Unknown fields are
// preserved through Extra so upstream-specific annotations survive the hop.
type Tool struct {
	Name        string                 `json:"name"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
	Extra       map[string]interface{} `json:"-"`
}

// Content is one content block in a tool result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallResult is the result of tools/call.
type CallResult struct {
	Content           []Content              `json:"content"`
	IsError           bool                   `json:"isError,omitempty"`
	StructuredContent map[string]interface{} `json:"structuredContent,omitempty"`
}

// ToolListResult is the result of tools/list, including the optional caching
// hints the modern revision defines.
type ToolListResult struct {
	Tools      []Tool `json:"tools"`
	TTLMs      int    `json:"ttlMs,omitempty"`
	CacheScope string `json:"cacheScope,omitempty"`
}

// rpcRequest is a JSON-RPC 2.0 request or notification.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message)
}

func rpcErrorf(code int, format string, args ...interface{}) *rpcError {
	return &rpcError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AuthRequiredError reports that an upstream server rejected the request with
// 401/403, so the caller can start or refresh the OAuth flow.
type AuthRequiredError struct {
	ServerID  string
	Status    int
	Challenge string // raw WWW-Authenticate value, if any
	Message   string
}

func (e *AuthRequiredError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("server %s requires authorization (HTTP %d)", e.ServerID, e.Status)
}

// RuntimeMissingError reports that a stdio command could not be found.
type RuntimeMissingError struct {
	Command string
}

func (e *RuntimeMissingError) Error() string {
	return fmt.Sprintf("runtime %q was not found on PATH; install it and retry", e.Command)
}

// NamespacedToolName builds the downstream tool name for an upstream tool.
// Both segments are narrowed to the [A-Za-z0-9_-] provider charset, so the
// result is a valid function name for every client.
func NamespacedToolName(serverID, tool string) string {
	return ToolNamePrefix + toolNameSegment(serverID) + toolNameSeparator + toolNameSegment(tool)
}

// toolNameSegment narrows one name for concatenation: characters outside
// [A-Za-z0-9_-] become "-", and runs of "_" collapse to one. Together with the
// edge trim that keeps the joined name free of "__", which downstream clients
// reserve as their own server/tool delimiter.
func toolNameSegment(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r == '_':
			if !lastUnderscore {
				b.WriteRune('_')
			}
			lastUnderscore = true
			continue
		case r == '-', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		lastUnderscore = false
	}
	return strings.Trim(b.String(), "_")
}

// SplitNamespacedToolName reverses NamespacedToolName against the server ids an
// endpoint can reach; the longest matching id wins, so a server id that is a
// prefix of another one still routes. Tool names may contain "_", so only the
// separator after the server id splits.
func SplitNamespacedToolName(name string, serverIDs []string) (serverID, tool string, ok bool) {
	if !strings.HasPrefix(name, ToolNamePrefix) {
		return "", "", false
	}
	matched := 0
	for _, id := range serverIDs {
		segment := toolNameSegment(id)
		if segment == "" {
			continue
		}
		prefix := ToolNamePrefix + segment + toolNameSeparator
		if len(prefix) > matched && strings.HasPrefix(name, prefix) {
			matched = len(prefix)
			serverID = id
		}
	}
	if matched == 0 {
		return "", "", false
	}
	tool = name[matched:]
	if tool == "" {
		return "", "", false
	}
	return serverID, tool, true
}

// SplitLegacyNamespacedToolName reverses the pre-switch mcp__<server>__<tool>
// shape. Tool names may contain "__" themselves, so only the first separator
// after the server id splits.
func SplitLegacyNamespacedToolName(name string) (serverID, tool string, ok bool) {
	if !strings.HasPrefix(name, legacyToolNamePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, legacyToolNamePrefix)
	idx := strings.Index(rest, "__")
	if idx <= 0 {
		return "", "", false
	}
	serverID = rest[:idx]
	tool = rest[idx+2:]
	if serverID == "" || tool == "" {
		return "", "", false
	}
	return serverID, tool, true
}
