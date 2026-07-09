package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setTestHomeAndConfigDir redirects both the agent-config home dir (via
// os.UserHomeDir's env var) and Prism's own config dir (APPDATA on Windows,
// HOME on darwin) to a temp dir, so install/restore tests never touch the
// real ~/.grok or real Prism config.
func setTestHomeAndConfigDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("APPDATA", tmp)
	return tmp
}

// writePrismConfig writes a config.json into Prism's config dir for every
// platform-specific path (only the platform-relevant one is read at runtime;
// writing the others is harmless), so loadConfig() returns predictable state.
func writePrismConfig(t *testing.T, cfgJSON string) {
	t.Helper()
	appdata := os.Getenv("APPDATA")
	home := os.Getenv("HOME")
	dirs := []string{
		filepath.Join(appdata, "prism"),                                  // Windows
		filepath.Join(home, "Library", "Application Support", "prism"),   // darwin
		filepath.Join(home, ".config", "prism"),                          // linux fallback
	}
	for _, dir := range dirs {
		if dir == filepath.Join("", "prism") || dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir config dir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0600); err != nil {
			t.Fatalf("write config.json %s: %v", dir, err)
		}
	}
}


func TestTomlQuote(t *testing.T) {
	cases := map[string]string{
		`simple`:      `"simple"`,
		`has"quote`:   `"has\"quote"`,
		`back\slash`:  `"back\\slash"`,
		`http://x/v1`: `"http://x/v1"`,
	}
	for in, want := range cases {
		if got := tomlQuote(in); got != want {
			t.Errorf("tomlQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasModelsSection(t *testing.T) {
	if hasModelsSection("[models]\ndefault = \"x\"") != true {
		t.Error("[models] not detected")
	}
	if hasModelsSection("[models.foo]\ndefault = \"x\"") != true {
		t.Error("[models.foo] not detected")
	}
	if hasModelsSection("  [models]\ndefault=\"x\"") != true {
		t.Error("indented [models] not detected")
	}
	if hasModelsSection("[model.prism-x]\nmodel = \"x\"") != false {
		t.Error("[model.*] falsely matched as [models]")
	}
	if hasModelsSection("# [models] comment") != false {
		t.Error("commented [models] falsely matched")
	}
}

func TestBuildGrokBuildModelSections(t *testing.T) {
	cfg := &Config{DefaultProvider: "ollama_cloud"}
	remap := &ModelRemapping{KnownModels: []ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud", ContextLength: 200000, Reasoning: true},
		{ID: "gpt-5-codex", Provider: "codex-acct-1", ContextLength: 0}, // 0 -> 128000, codex -> responses
	}}
	cfg.OAuthAccounts = []*OAuthAccount{{ID: "codex-acct-1", Provider: "codex"}}

	out := buildGrokBuildModelSections(remap, cfg, "http://127.0.0.1:11434/v1")

	// First model: chat_completions, reasoning flag, ctx preserved.
	if !strings.Contains(out, "[model.prism-glm-5-2-cloud]") {
		t.Error("missing first model section header")
	}
	if !strings.Contains(out, `model = "glm-5.2:cloud"`) {
		t.Error("model id not quoted/preserved")
	}
	if !strings.Contains(out, `base_url = "http://127.0.0.1:11434/v1"`) {
		t.Error("base_url wrong")
	}
	if !strings.Contains(out, "api_backend = \"chat_completions\"") {
		t.Error("non-codex model should use chat_completions")
	}
	if !strings.Contains(out, "context_window = 200000") {
		t.Error("context_window not preserved")
	}
	if !strings.Contains(out, "supports_reasoning_effort = true") {
		t.Error("reasoning model missing supports_reasoning_effort")
	}
	if !strings.Contains(out, `name = "[Prism] Glm 5.2:cloud"`) {
	}

	// Second model: codex -> responses, ctx defaulted to 128000, no reasoning flag line.
	if !strings.Contains(out, "[model.prism-gpt-5-codex]") {
		t.Error("missing second model section header")
	}
	if !strings.Contains(out, "api_backend = \"responses\"") {
		t.Error("codex model should use responses")
	}
	if !strings.Contains(out, "context_window = 128000") {
		t.Error("zero context_window should default to 128000")
	}
	// reasoning line must appear exactly once (only for the reasoning model).
	if strings.Count(out, "supports_reasoning_effort = true") != 1 {
		t.Errorf("expected exactly 1 reasoning flag, got %d", strings.Count(out, "supports_reasoning_effort = true"))
	}
}

func TestInstallGrokBuildConfigLifecycle(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud","oauth_accounts":[{"id":"codex-1","provider":"codex"}]}`)

	remap := &ModelRemapping{KnownModels: []ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud", ContextLength: 200000},
		{ID: "gpt-5", Provider: "codex-1", ContextLength: 0},
	}}

	cfgPath := filepath.Join(tmp, ".grok", "config.toml")

	// Pre-existing user content with an existing [models] section.
	userContent := `# my grok config
[models]
default = "user-default"

[model.user-thing]
model = "user-model"
`
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(userContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Install.
	if err := installGrokBuildConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	data, _ := os.ReadFile(cfgPath)
	s := string(data)
	if strings.Count(s, codexManagedBegin) != 1 || strings.Count(s, codexManagedEnd) != 1 {
		t.Fatalf("expected exactly one managed block, got:\n%s", s)
	}
	// User content preserved.
	if !strings.Contains(s, "model = \"user-model\"") {
		t.Error("user model entry lost after install")
	}
	// Pre-existing [models] section means we must NOT add a second [models].
	if strings.Count(s, "[models]") != 1 {
		t.Errorf("expected exactly one [models] section (user's), got %d:\n%s", strings.Count(s, "[models]"), s)
	}
	// Prism model sections present.
	if !strings.Contains(s, "[model.prism-glm-5-2-cloud]") || !strings.Contains(s, "[model.prism-gpt-5]") {
		t.Errorf("prism model sections missing:\n%s", s)
	}

	// Re-install (idempotency): still exactly one managed block.
	if err := installGrokBuildConfig(11434, remap); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	data, _ = os.ReadFile(cfgPath)
	s = string(data)
	if strings.Count(s, codexManagedBegin) != 1 || strings.Count(s, codexManagedEnd) != 1 {
		t.Fatalf("idempotency: expected one managed block, got %d:\n%s", strings.Count(s, codexManagedBegin), s)
	}
	if strings.Count(s, "[model.prism-glm-5-2-cloud]") != 1 || strings.Count(s, "[model.prism-gpt-5]") != 1 {
		t.Errorf("idempotency: model sections duplicated:\n%s", s)
	}
	if strings.Count(s, "[models]") != 1 {
		t.Errorf("idempotency: [models] duplicated:\n%s", s)
	}

	// Restore: managed block gone, user content intact.
	if err := restoreGrokBuildConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	data, _ = os.ReadFile(cfgPath)
	s = string(data)
	if strings.Contains(s, codexManagedBegin) || strings.Contains(s, codexManagedEnd) {
		t.Errorf("restore: managed block still present:\n%s", s)
	}
	if strings.Contains(s, "prism") {
		t.Errorf("restore: prism residue remains:\n%s", s)
	}
	if !strings.Contains(s, "model = \"user-model\"") || !strings.Contains(s, "default = \"user-default\"") {
		t.Errorf("restore: user content lost:\n%s", s)
	}
}

func TestInstallGrokBuildConfigNoModelsSectionAddsDefault(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	remap := &ModelRemapping{KnownModels: []ModelEntry{
		{ID: "glm-5.2:cloud", Provider: "ollama_cloud", ContextLength: 200000},
	}}
	cfgPath := filepath.Join(tmp, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	// No pre-existing file at all.
	if err := installGrokBuildConfig(11434, remap); err != nil {
		t.Fatalf("install into empty file: %v", err)
	}
	data, _ := os.ReadFile(cfgPath)
	s := string(data)
	if strings.Count(s, "[models]") != 1 {
		t.Errorf("expected one [models] section added when none existed, got %d", strings.Count(s, "[models]"))
	}
	if !strings.Contains(s, `default = "prism-glm-5-2-cloud"`) {
		t.Errorf("expected default pointing at first prism model:\n%s", s)
	}
}

func TestInstallGrokBuildConfigNoModelsError(t *testing.T) {
	setTestHomeAndConfigDir(t)
	err := installGrokBuildConfig(11434, &ModelRemapping{KnownModels: []ModelEntry{}})
	if err == nil {
		t.Fatal("expected error when no models configured")
	}
}