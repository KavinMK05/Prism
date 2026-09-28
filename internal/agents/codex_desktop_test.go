package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexTestConfigPath returns ~/.codex/config.toml under the test home.
func codexTestConfigPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir codex config dir: %v", err)
	}
	return path
}

func writeCodexTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readCodexTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

const codexUserConfig = `model = "gpt-5.1-codex"
model_provider = "openai"

[history]
persistence = "save-all"
`

// TestInstallCodexConfigDoesNotSetModel pins the guarantee for Codex: the
// managed block points Codex at Prism (model_provider + catalog) but never
// chooses a model, so the user's own `model` line survives verbatim.
func TestInstallCodexConfigDoesNotSetModel(t *testing.T) {
	setTestHomeAndConfigDir(t)
	path := codexTestConfigPath(t)
	writeCodexTestFile(t, path, codexUserConfig)

	if err := InstallCodexConfig(11434); err != nil {
		t.Fatalf("install: %v", err)
	}
	s := readCodexTestFile(t, path)

	if got := strings.Count(s, `model = "`); got != 1 {
		t.Errorf("expected exactly the user's one model = line, got %d:\n%s", got, s)
	}
	top := extractTopLevelOverrides(s)
	if top["model"] != "gpt-5.1-codex" {
		t.Errorf("model = %q, want the user's gpt-5.1-codex", top["model"])
	}
	if top["model_provider"] != codexProviderKey {
		t.Errorf("model_provider = %q, want %q", top["model_provider"], codexProviderKey)
	}
	if top["model_catalog_json"] == "" {
		t.Error("model_catalog_json not set")
	}
	if !strings.Contains(s, "[history]") || !strings.Contains(s, `persistence = "save-all"`) {
		t.Errorf("user sections lost:\n%s", s)
	}

	// The stash must record what Prism replaced, so restore can undo it. `model`
	// is not Prism's to manage, so it must not appear in the stash.
	stash := extractPreviousTopLevel(s)
	if stash["model_provider"] != "openai" {
		t.Errorf("stashed model_provider = %q, want openai", stash["model_provider"])
	}
	if _, ok := stash["model"]; ok {
		t.Errorf("stash must not capture the user's model: %v", stash["model"])
	}
}

// TestRestoreCodexConfigRestoresUserValues re-syncs before restoring: an
// earlier Prism version rewrote the stash on every startup, wiping the user's
// original values, so a restore right after a re-sync is the case that matters.
func TestRestoreCodexConfigRestoresUserValues(t *testing.T) {
	setTestHomeAndConfigDir(t)
	path := codexTestConfigPath(t)
	writeCodexTestFile(t, path, codexUserConfig)

	for i := 0; i < 2; i++ {
		if err := InstallCodexConfig(11434); err != nil {
			t.Fatalf("install %d: %v", i+1, err)
		}
	}
	if err := RestoreCodexConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	s := readCodexTestFile(t, path)

	if strings.Contains(s, codexManagedBegin) || strings.Contains(s, "prism") {
		t.Errorf("prism residue after restore:\n%s", s)
	}
	top := extractTopLevelOverrides(s)
	if top["model"] != "gpt-5.1-codex" {
		t.Errorf("model = %q, want the user's gpt-5.1-codex", top["model"])
	}
	if top["model_provider"] != "openai" {
		t.Errorf("model_provider = %q, want openai", top["model_provider"])
	}
	if _, ok := top["model_catalog_json"]; ok {
		t.Errorf("prism catalog left behind: %v", top["model_catalog_json"])
	}
	if !strings.Contains(s, "[history]") || !strings.Contains(s, `persistence = "save-all"`) {
		t.Errorf("user sections lost:\n%s", s)
	}
}

// A config written by an older Prism version had the user's `model` removed from
// the body and parked in the stash. The next sync must hand it back to the user.
func TestInstallCodexConfigMigratesLegacyManagedModel(t *testing.T) {
	setTestHomeAndConfigDir(t)
	path := codexTestConfigPath(t)
	legacy := codexManagedBegin + "\n" +
		`# prism previous-top-level = {"model":"gpt-5.1-codex","model_provider":"openai"}` + "\n" +
		`model = "ollama_cloud/glm-5.2:cloud"` + "\n" +
		`model_provider = "prism"` + "\n" +
		`model_catalog_json = "/tmp/prism/codex_catalog.json"` + "\n" +
		codexManagedEnd + "\n\n" +
		"[history]\npersistence = \"save-all\"\n\n" +
		codexManagedBegin + "\n" +
		"[model_providers.prism]\nname = \"Prism\"\n" +
		codexManagedEnd + "\n"
	writeCodexTestFile(t, path, legacy)

	if err := InstallCodexConfig(11434); err != nil {
		t.Fatalf("install: %v", err)
	}
	s := readCodexTestFile(t, path)

	if got := strings.Count(s, `model = "`); got != 1 {
		t.Errorf("expected one model = line (the user's restored one), got %d:\n%s", got, s)
	}
	top := extractTopLevelOverrides(s)
	if top["model"] != "gpt-5.1-codex" {
		t.Errorf("model = %q, want the stashed gpt-5.1-codex restored", top["model"])
	}
	if top["model_provider"] != codexProviderKey {
		t.Errorf("model_provider = %q, want %q", top["model_provider"], codexProviderKey)
	}
	stash := extractPreviousTopLevel(s)
	if stash["model_provider"] != "openai" {
		t.Errorf("stashed model_provider = %q, want openai", stash["model_provider"])
	}
	if _, ok := stash["model"]; ok {
		t.Errorf("stash still holds model after migration: %v", stash["model"])
	}

	// And a restore must not resurrect a duplicate `model` line.
	if err := RestoreCodexConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	s = readCodexTestFile(t, path)
	if got := strings.Count(s, `model = "`); got != 1 {
		t.Errorf("expected exactly one model = line after restore, got %d:\n%s", got, s)
	}
	top = extractTopLevelOverrides(s)
	if top["model"] != "gpt-5.1-codex" || top["model_provider"] != "openai" {
		t.Errorf("restore gave model=%q provider=%q, want gpt-5.1-codex/openai", top["model"], top["model_provider"])
	}
}

// TestExtractPreviousTopLevel covers the stash parser directly, including the
// absent/unreadable cases that a fresh install hits.
func TestExtractPreviousTopLevel(t *testing.T) {
	if got := extractPreviousTopLevel("model = \"x\"\n"); got != nil {
		t.Errorf("no stash: got %v, want nil", got)
	}
	if got := extractPreviousTopLevel(codexPreviousTopLevelPrefix + "not json\n"); got != nil {
		t.Errorf("malformed stash: got %v, want nil", got)
	}
	content := codexManagedBegin + "\n" +
		codexPreviousTopLevelPrefix + `{"model_provider":"openai"}` + "\n" +
		codexManagedEnd + "\n"
	got := extractPreviousTopLevel(content)
	if got["model_provider"] != "openai" {
		t.Errorf("stash = %v, want model_provider=openai", got)
	}
}

// Codex appends the tables it persists to the end of config.toml, which lands
// them inside Prism's markers because that block is written last. Syncing and
// restoring Prism's provider block must keep them: they hold the user's answers
// (a trusted hook here), and Codex re-asks for anything it cannot find.
func TestInstallCodexConfigKeepsTablesInsideManagedBlock(t *testing.T) {
	setTestHomeAndConfigDir(t)
	path := codexTestConfigPath(t)

	codexWritten := "[hooks.state.\"C:\\\\Users\\\\Kavin\\\\.codex\\\\hooks.json:pre_tool_use:0:0\"]\n" +
		"enabled = true\n" +
		"trusted_hash = \"sha256:abc\"\n"
	writeCodexTestFile(t, path, "model = \"gpt-5.1-codex\"\n\n"+
		codexManagedBegin+"\n"+
		"[model_providers."+codexProviderKey+"]\nname = \"Prism\"\n"+
		"\n"+codexWritten+
		codexManagedEnd+"\n")

	if err := InstallCodexConfig(11434); err != nil {
		t.Fatalf("install: %v", err)
	}
	s := readCodexTestFile(t, path)
	if !strings.Contains(s, "[hooks.state.") || !strings.Contains(s, `trusted_hash = "sha256:abc"`) {
		t.Errorf("install dropped the hook trust Codex wrote:\n%s", s)
	}
	if n := strings.Count(s, "[model_providers."+codexProviderKey+"]"); n != 1 {
		t.Errorf("expected one Prism provider table, got %d:\n%s", n, s)
	}

	if err := RestoreCodexConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	s = readCodexTestFile(t, path)
	if strings.Contains(s, "model_providers."+codexProviderKey) {
		t.Errorf("Prism's provider table survived restore:\n%s", s)
	}
	if !strings.Contains(s, `trusted_hash = "sha256:abc"`) {
		t.Errorf("restore dropped the hook trust Codex wrote:\n%s", s)
	}
}

// TestIsManagedTableHeader pins the prefix handling: a table Prism owns is
// matched exactly (or as a parent of a sub-table), never as a prefix of a
// longer, different key.
func TestIsManagedTableHeader(t *testing.T) {
	cases := []struct {
		path string
		line string
		want bool
	}{
		{"mcp_servers.prism", "[mcp_servers.prism]", true},
		{"mcp_servers.prism", "[mcp_servers.prism.http_headers]", true},
		{"mcp_servers.prism", `[mcp_servers."prism"]`, true},
		{"mcp_servers.prism", `[mcp_servers."prism".http_headers]`, true},
		{"mcp_servers.prism", "[mcp_servers.prism] # mine", true},
		{"mcp_servers.prism", "[mcp_servers.prismatic]", false},
		{"mcp_servers.prism", "[mcp_servers.prism_extra]", false},
		{"mcp_servers.prism", "[mcp_servers]", false},
		{"model_providers.prism", "[model_providers.prism]", true},
		{"model_providers.prism", "[model_providers.prism.retries]", true},
		{"model_providers.prism", "[model_providers.prismatic]", false},
	}
	for _, tc := range cases {
		if got := isManagedTableHeader(tc.line, tc.path); got != tc.want {
			t.Errorf("isManagedTableHeader(%q, %q) = %v, want %v", tc.line, tc.path, got, tc.want)
		}
	}
}

// TestCodexOwnedTopLevelKeysCoverManagedKeys keeps the two lists in step: a key
// Prism stashes and restores must also be stripped from inside the block.
func TestCodexOwnedTopLevelKeysCoverManagedKeys(t *testing.T) {
	owned := map[string]bool{}
	for _, k := range codexOwnedTopLevelKeys {
		owned[k] = true
	}
	for _, k := range codexManagedTopLevelKeys {
		if !owned[k] {
			t.Errorf("%q is managed but not stripped from Prism's managed block", k)
		}
	}
}
