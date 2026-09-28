package mcp

import (
	"encoding/json"
	"errors"
)

// marshalParams normalizes the params argument of a JSON-RPC request. A nil
// params means "omit the member" (not `null`), which some servers validate.
func marshalParams(params interface{}) (json.RawMessage, error) {
	switch v := params.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		if len(v) == 0 {
			return nil, nil
		}
		return v, nil
	case []byte:
		if len(v) == 0 {
			return nil, nil
		}
		return json.RawMessage(v), nil
	default:
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
}

// injectMeta adds the reserved `_meta` object the stateless MCP revision
// defines (protocol version, client identity, client capabilities) to a params
// object. Non-object params are returned untouched.
func injectMeta(params json.RawMessage) json.RawMessage {
	return injectMetaVersion(params, ProtocolVersion)
}

// injectMetaVersion is injectMeta with an explicit protocol version, so the
// `_meta` block always advertises the same version as the request's
// MCP-Protocol-Version header.
func injectMetaVersion(params json.RawMessage, version string) json.RawMessage {
	if len(params) == 0 {
		return params
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(params, &obj); err != nil {
		return params
	}
	if obj == nil {
		return params
	}
	meta, _ := obj["_meta"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
	}
	if version == "" {
		version = ProtocolVersion
	}
	if _, ok := meta[metaProtocolVersionKey]; !ok {
		meta[metaProtocolVersionKey] = version
	}
	if _, ok := meta[metaClientInfoKey]; !ok {
		meta[metaClientInfoKey] = map[string]string{"name": ServerName, "version": mcpClientVersion}
	}
	if _, ok := meta[metaClientCapabilitiesKey]; !ok {
		meta[metaClientCapabilitiesKey] = map[string]interface{}{}
	}
	obj["_meta"] = meta
	out, err := json.Marshal(obj)
	if err != nil {
		return params
	}
	return out
}

const (
	metaProtocolVersionKey    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfoKey         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilitiesKey = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfoKey         = "io.modelcontextprotocol/serverInfo"
)

// isMethodNotFound reports whether an error is a JSON-RPC -32601, which is how
// a server that only speaks the stateless revision answers `initialize`.
func isMethodNotFound(err error) bool {
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == CodeMethodNotFound
	}
	return false
}

// isAuthError reports whether an error asks for (re)authorization upstream.
func isAuthError(err error) bool {
	var authErr *AuthRequiredError
	return errors.As(err, &authErr)
}

// isRuntimeMissing reports whether an error is a missing stdio runtime.
func isRuntimeMissing(err error) bool {
	var missing *RuntimeMissingError
	return errors.As(err, &missing)
}

// isRPCError reports whether the upstream answered with a JSON-RPC error
// (a protocol-level failure, not a broken connection).
func isRPCError(err error) bool {
	var rpcErr *rpcError
	return errors.As(err, &rpcErr)
}
