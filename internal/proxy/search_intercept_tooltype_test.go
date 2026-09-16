package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/search"
)

// Codex CLI always declares the built-in `web_search` tool, so when search
// interception is enabled every Codex /v1/responses request is taken over by
// handleResponsesWebSearchLoop. That loop runs its own upstream turns and
// forwards the tool calls the model asked for — so it MUST honour the declared
// tool types exactly like the normal Responses handlers do. Otherwise the client
// gets `function_call` for a tool it declared as `custom` (apply_patch) and
// aborts every call, which is the "Codex retries and then edits files with
// Python" symptom.
func TestResponsesWebSearchLoopHonoursDeclaredToolTypes(t *testing.T) {
	f := loadCodexReplayFixture(t)
	allTools := f.Tools
	toolTypes := buildToolTypeMap(allTools)
	toolNamespaces := buildToolNamespaceMap(allTools)

	if toolTypes["apply_patch"] != "custom" {
		t.Fatalf("fixture: apply_patch type = %q, want custom", toolTypes["apply_patch"])
	}
	if !responsesHasWebSearchTool(toolTypes) {
		t.Fatal("fixture must declare web_search or the loop never takes the request")
	}

	cases := []struct {
		name      string
		callName  string
		arguments string
		wantType  string
		wantName  string
		wantNS    string
		wantInput string
	}{
		{
			name: "custom tool (apply_patch)", callName: "apply_patch",
			arguments: `{"input":` + jsonQuote(capturedApplyPatch) + `}`,
			wantType:  "custom_tool_call", wantName: "apply_patch", wantInput: capturedApplyPatch,
		},
		{
			name: "namespaced tool", callName: "multi_agent_v1__spawn_agent",
			arguments: `{"task":"count the files"}`,
			wantType:  "function_call", wantName: "spawn_agent", wantNS: "multi_agent_v1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(OpenAIChatResponse{
					ID: "chatcmpl-1", Model: "test",
					Choices: []OpenAIChoice{{
						Index:        0,
						FinishReason: "tool_calls",
						Message: OpenAIChatMessage{
							Role: "assistant",
							ToolCalls: []OpenAIToolCall{{
								ID:       "call_x",
								Type:     "function",
								Function: OpenAIToolCallFunc{Name: tc.callName, Arguments: tc.arguments},
							}},
						},
					}},
					Usage: OpenAIUsage{PromptTokens: 10, CompletionTokens: 5},
				})
			}))
			defer ts.Close()

			// The loop only takes the request when interception is enabled.
			search.RegisterProviderForTest("searxng", func(_ *search.ProviderConfig, c *http.Client) search.SearchProvider {
				return &fakeStaticProvider{results: []search.SearchResult{{Title: "t", URL: "https://example.com"}}}
			})
			search.Global.Reload(&search.Config{
				Active: "searxng", MaxPerTurn: 3, DefaultNumResults: 3,
				Providers: map[string]*search.ProviderConfig{"searxng": {Enabled: true}},
			})

			pr := makeTestRouter(ts.URL)
			rp := makeTestRP(ts.URL, "openai")
			respReq := &ResponsesAPIRequest{
				Model:  "test",
				Stream: true,
				Input:  []interface{}{map[string]interface{}{"type": "message", "role": "user", "content": "go"}},
				Tools:  allTools,
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
			if !pr.handleResponsesWebSearchLoop(rr, req, respReq, rp, toolTypes, toolNamespaces, allTools) {
				t.Fatal("search loop did not take the request")
			}
			assertForwardedToolCall(t, rr.Body.String(), tc.wantType, tc.wantName, tc.wantNS, tc.wantInput, tc.arguments)
		})
	}
}

func isForwardedToolItem(item map[string]interface{}) bool {
	switch item["type"] {
	case "function_call", "custom_tool_call":
		return true
	}
	return false
}

// assertForwardedToolCall checks the emitted lifecycle for the forwarded tool
// call named `call_x`: the added/done items, the type-specific done event, and
// the item inside response.completed must all agree with the declared tool type.
func assertForwardedToolCall(t *testing.T, body, wantType, wantName, wantNS, wantInput, wantArguments string) {
	t.Helper()

	var added, done, completed map[string]interface{}
	sawInputDone, sawArgsDone := false, false
	for _, ev := range parseSSEEvents(body) {
		var p map[string]interface{}
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			continue
		}
		switch p["type"] {
		case "response.output_item.added":
			if item, ok := p["item"].(map[string]interface{}); ok && isForwardedToolItem(item) {
				added = item
			}
		case "response.output_item.done":
			if item, ok := p["item"].(map[string]interface{}); ok && isForwardedToolItem(item) {
				done = item
			}
		case "response.custom_tool_call_input.done":
			sawInputDone = true
		case "response.function_call_arguments.done":
			sawArgsDone = true
		case "response.completed":
			resp, _ := p["response"].(map[string]interface{})
			items, _ := resp["output"].([]interface{})
			for _, it := range items {
				if item, ok := it.(map[string]interface{}); ok && isForwardedToolItem(item) {
					completed = item
				}
			}
		}
	}

	if added == nil || done == nil || completed == nil {
		t.Fatalf("missing tool items (added=%v done=%v completed=%v); body:\n%s", added != nil, done != nil, completed != nil, body)
	}
	for label, item := range map[string]map[string]interface{}{"added": added, "done": done, "completed": completed} {
		if item["type"] != wantType {
			t.Errorf("%s item type = %v, want %v (client aborts calls whose type it did not declare)", label, item["type"], wantType)
		}
		if item["name"] != wantName {
			t.Errorf("%s item name = %v, want %v", label, item["name"], wantName)
		}
		if got, _ := item["namespace"].(string); got != wantNS {
			t.Errorf("%s item namespace = %q, want %q", label, got, wantNS)
		}
	}

	if wantType == "custom_tool_call" {
		if !sawInputDone {
			t.Errorf("missing response.custom_tool_call_input.done for a custom tool; body:\n%s", body)
		}
		if sawArgsDone {
			t.Errorf("custom tool emitted response.function_call_arguments.done instead of the input event")
		}
		for label, item := range map[string]map[string]interface{}{"done": done, "completed": completed} {
			if item["input"] != wantInput {
				t.Errorf("%s item input = %q, want the raw extracted patch", label, item["input"])
			}
			if _, hasArgs := item["arguments"]; hasArgs {
				t.Errorf("%s custom tool item must not carry arguments: %v", label, item)
			}
		}
		return
	}

	if sawInputDone {
		t.Errorf("function_call emitted a custom_tool_call input event")
	}
	if done["arguments"] != wantArguments {
		t.Errorf("function_call arguments = %q, want %q", done["arguments"], wantArguments)
	}
}
