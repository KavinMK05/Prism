package mcp

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// The stateless MCP revision (2026-07-28) mirrors selected JSON-RPC body fields
// into HTTP headers so servers, load balancers, and gateways can route and
// validate a request without parsing its body. Prism is a client of that
// revision, so it sends them; servers speaking older revisions ignore them.

// mcpParamHeaderPrefix namespaces the headers built from tool parameters the
// tool's input schema annotates with x-mcp-header.
const mcpParamHeaderPrefix = "Mcp-Param-"

// base64Sentinel wraps a header value that cannot be sent as plain ASCII.
const (
	base64SentinelPrefix = "=?base64?"
	base64SentinelSuffix = "?="
)

// mcpRoutingName returns the value the Mcp-Name header must carry for a method
// that addresses a single named item, and false for methods that do not.
func mcpRoutingName(method string, params json.RawMessage) (string, bool) {
	if len(params) == 0 {
		return "", false
	}
	switch method {
	case "tools/call", "prompts/get":
		var p struct {
			Name *string `json:"name"`
		}
		if json.Unmarshal(params, &p) != nil || p.Name == nil || *p.Name == "" {
			return "", false
		}
		return *p.Name, true
	case "resources/read":
		var p struct {
			URI *string `json:"uri"`
		}
		if json.Unmarshal(params, &p) != nil || p.URI == nil || *p.URI == "" {
			return "", false
		}
		return *p.URI, true
	}
	return "", false
}

// mirrorHeaderValue renders a value for an HTTP header. Values outside the
// header-safe ASCII set (and values that would look like an already-encoded
// one) are base64-wrapped with the sentinel the specification defines.
func mirrorHeaderValue(value string) string {
	if headerSafeASCII(value) {
		return value
	}
	return base64SentinelPrefix + base64.StdEncoding.EncodeToString([]byte(value)) + base64SentinelSuffix
}

// headerSafeASCII reports whether a value can be mirrored verbatim: visible
// ASCII, spaces, and tabs, without surrounding whitespace, and without
// colliding with the base64 sentinel.
func headerSafeASCII(value string) bool {
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, base64SentinelPrefix) && strings.HasSuffix(value, base64SentinelSuffix) {
		return false
	}
	if strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r > 0x7E {
			return false
		}
	}
	return true
}

// xMCPHeaderParams walks a tool's input schema and returns the parameter paths
// the server wants mirrored into headers, mapped to the header name portion of
// Mcp-Param-{name}. Only statically reachable `properties` chains are followed,
// matching the extension's constraints.
func xMCPHeaderParams(schema map[string]interface{}) map[string]string {
	var out map[string]string
	var walk func(prefix string, node map[string]interface{})
	walk = func(prefix string, node map[string]interface{}) {
		props, _ := node["properties"].(map[string]interface{})
		for name, raw := range props {
			child, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if header, ok := child["x-mcp-header"].(string); ok && header != "" {
				if out == nil {
					out = map[string]string{}
				}
				out[path] = header
			}
			walk(path, child)
		}
	}
	walk("", schema)
	return out
}

// mcpParamHeaders builds the Mcp-Param-* headers for one tools/call. Arguments
// that are absent or null are omitted, as the specification requires.
func mcpParamHeaders(mapping map[string]string, arguments json.RawMessage) map[string]string {
	if len(mapping) == 0 || len(arguments) == 0 {
		return nil
	}
	var args map[string]interface{}
	if json.Unmarshal(arguments, &args) != nil || len(args) == 0 {
		return nil
	}
	out := make(map[string]string, len(mapping))
	for path, header := range mapping {
		value, ok := lookupArgPath(args, path)
		if !ok || value == nil {
			continue
		}
		text, ok := headerValueString(value)
		if !ok {
			continue
		}
		out[mcpParamHeaderPrefix+header] = mirrorHeaderValue(text)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// lookupArgPath resolves a dotted parameter path ("region" or "config.region")
// inside a decoded arguments object.
func lookupArgPath(args map[string]interface{}, path string) (interface{}, bool) {
	var current interface{} = args
	for _, part := range strings.Split(path, ".") {
		obj, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// headerValueString applies the extension's type conversion: strings as-is,
// integers in decimal, booleans lowercase. Numbers with a fractional part are
// not permitted by the extension.
func headerValueString(value interface{}) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case json.Number:
		if strings.ContainsAny(v.String(), ".eE") {
			return "", false
		}
		return v.String(), true
	case float64:
		if math.Trunc(v) != v {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	}
	return "", false
}
