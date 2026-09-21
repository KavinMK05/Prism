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
// name: mcp__<server>__<tool>. It matches the convention the proxy already
// normalizes in Responses API tool names.
const ToolNamePrefix = "mcp__"

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
func NamespacedToolName(serverID, tool string) string {
	return ToolNamePrefix + serverID + "__" + tool
}

// SplitNamespacedToolName reverses NamespacedToolName. Tool names may contain
// "__" themselves, so only the first separator after the server id splits.
func SplitNamespacedToolName(name string) (serverID, tool string, ok bool) {
	if !strings.HasPrefix(name, ToolNamePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, ToolNamePrefix)
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
