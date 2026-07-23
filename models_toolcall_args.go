package main

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON accepts tool-call arguments as either a JSON object (the
// common case) or a JSON string. Some Ollama-compatible upstreams (e.g. GLM
// via certain routers) deliver the whole `arguments` field as a JSON string
// instead of an object; if so, we decode the inner string content into the
// map so downstream clients (Claude Code) receive a proper object rather than
// a stringified blob that fails their tool-input validation and triggers
// infinite retry loops. See ollama/ollama#15645.
func (a *OllamaToolCallFunction) UnmarshalJSON(data []byte) error {
	type alias OllamaToolCallFunction
	var raw struct {
		alias
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*a = OllamaToolCallFunction(raw.alias)

	trimmed := bytes.TrimSpace(raw.Arguments)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	// Arguments arrived as a JSON string: decode the inner content as JSON.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		if s == "" {
			return nil
		}
		return json.Unmarshal([]byte(s), &a.Arguments)
	}
	// Arguments arrived as a JSON object/array: decode directly.
	return json.Unmarshal(trimmed, &a.Arguments)
}