package mcp

import (
	"strings"
	"testing"
)

// TestNamespacedToolNameAvoidsDoubleUnderscore pins the fix for the "invalid or
// ambiguous qualified name" rejection: agents that qualify MCP tools as
// server__tool (Grok Build, Codex) drop any tool whose name contains a second
// "__", so a name Prism emits must never contain one - including when the
// upstream tool's own name does.
func TestNamespacedToolNameAvoidsDoubleUnderscore(t *testing.T) {
	cases := []struct{ server, tool, want string }{
		{"com-notion-mcp", "notion-check-mcp-next-steps", "mcp_com-notion-mcp_notion-check-mcp-next-steps"},
		{"notes", "search", "mcp_notes_search"},
		{"notes", "list_notes", "mcp_notes_list_notes"},
		{"notes", "group__action", "mcp_notes_group_action"},
		{"notes", "___", "mcp_notes_"},
		{"my server", "weird.tool", "mcp_my-server_weird-tool"},
		{"trailing_", "_leading", "mcp_trailing_leading"},
	}
	for _, c := range cases {
		got := NamespacedToolName(c.server, c.tool)
		if got != c.want {
			t.Errorf("NamespacedToolName(%q, %q) = %q, want %q", c.server, c.tool, got, c.want)
		}
		if c.tool != "___" && strings.Contains(got, "__") {
			t.Errorf("NamespacedToolName(%q, %q) = %q contains \"__\"", c.server, c.tool, got)
		}
	}
}

// A server id that is a prefix of another one still routes: the longest
// matching id wins rather than the first one in the list.
func TestSplitNamespacedToolNamePrefersLongestServerID(t *testing.T) {
	ids := []string{"github", "github-enterprise", "notes"}

	serverID, tool, ok := SplitNamespacedToolName("mcp_github-enterprise_ci_search", ids)
	if !ok || serverID != "github-enterprise" || tool != "ci_search" {
		t.Errorf("split = (%q, %q, %v), want (github-enterprise, ci_search, true)", serverID, tool, ok)
	}

	serverID, tool, ok = SplitNamespacedToolName("mcp_github_search", ids)
	if !ok || serverID != "github" || tool != "search" {
		t.Errorf("split = (%q, %q, %v), want (github, search, true)", serverID, tool, ok)
	}

	for _, bad := range []string{"mcp_ghost_search", "search", "mcp_", "mcp_notes_"} {
		if _, _, ok := SplitNamespacedToolName(bad, ids); ok {
			t.Errorf("%q split unexpectedly", bad)
		}
	}
}

// The pre-switch mcp__<server>__<tool> shape stays parseable so an agent that
// cached a tool list before an upgrade keeps working.
func TestSplitLegacyNamespacedToolName(t *testing.T) {
	serverID, tool, ok := SplitLegacyNamespacedToolName("mcp__com-notion-mcp__notion-check-mcp-next-steps")
	if !ok || serverID != "com-notion-mcp" || tool != "notion-check-mcp-next-steps" {
		t.Errorf("split = (%q, %q, %v)", serverID, tool, ok)
	}
	for _, bad := range []string{"mcp_notes_search", "mcp__notes", "mcp____x", "notes"} {
		if _, _, ok := SplitLegacyNamespacedToolName(bad); ok {
			t.Errorf("%q split unexpectedly", bad)
		}
	}
}
