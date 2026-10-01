package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"ollama-proxy/internal/config"
)

// writeProfilePatch creates a DSH profile directory with a patch layer and
// returns the patch path.
func writeProfilePatch(t *testing.T, home, profile, content string) string {
	t.Helper()
	dir := filepath.Join(home, "profiles", profile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cordis.patch.yml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// decodeProfilePatch parses a profile patch's top-level YAML sequence.
func decodeProfilePatch(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile patch: %v", err)
	}
	var entries []map[string]interface{}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse profile patch: %v\n%s", err, data)
	}
	return entries
}

// patchRow returns the loader entry with the given id, or nil.
func patchRow(t *testing.T, path, id string) map[string]interface{} {
	t.Helper()
	for _, entry := range decodeProfilePatch(t, path) {
		if got, _ := entry["id"].(string); got == id {
			return entry
		}
	}
	return nil
}

// patchProviders returns the llm-pi-ai row's config.providers map.
func patchProviders(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	row := patchRow(t, path, deepSeekHarnessRowID)
	if row == nil {
		t.Fatalf("no %s row in patch:\n%s", deepSeekHarnessRowID, mustRead(t, path))
	}
	cfg, _ := row["config"].(map[string]interface{})
	if cfg == nil {
		return nil
	}
	providers, _ := cfg["providers"].(map[string]interface{})
	return providers
}

// routeModels returns a route's models keyed by model id.
func routeModels(route map[string]interface{}) map[string]map[string]interface{} {
	out := map[string]map[string]interface{}{}
	list, _ := route["models"].([]interface{})
	for _, raw := range list {
		entry, _ := raw.(map[string]interface{})
		if entry == nil {
			continue
		}
		if id, _ := entry["id"].(string); id != "" {
			out[id] = entry
		}
	}
	return out
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	return string(data)
}

// The real patch DSH produces carries other rows and providers the user added
// in the Web UI; both must survive a Prism sync.
func TestInstallDeepSeekHarnessConfigMergesIntoProfilePatch(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	userPatch := `# Your patch layer for this dsh profile, applied after every bundle layer.
- id: llm-pi-ai
  name: "@deepseek-ai/dsh-llm-pi-ai"
  config:
    providers:
      my-own-gateway:
        displayName: Mine
        api: openai-completions
        baseURL: http://127.0.0.1:9999/v1
        models:
          - id: my-model
- id: agent-default-model
  name: "@deepseek-ai/dsh-agent-default-model"
  config:
    provider: my-own-gateway
    model: my-model
- id: ui-chat
  name: "@deepseek-ai/dsh-client-ui-chat"
  config:
    transcriptView: standard
`
	path := writeProfilePatch(t, home, "desktop", userPatch)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "chat-model", Provider: "ollama_cloud", ContextLength: 131072, MaxOutputTokens: 8192},
		{ID: "plain-model", Provider: "ollama_cloud"},
		{ID: "resp-model", Provider: "codex_abc", API: "responses", ContextLength: 200000, MaxOutputTokens: 4096},
	}}

	if err := InstallDeepSeekHarnessConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	content := mustRead(t, path)

	if _, err := os.Stat(path + ".prism-backup"); err != nil {
		t.Errorf("expected a one-time .prism-backup: %v", err)
	}
	for _, want := range []string{"# Your patch layer", "- id: agent-default-model", "my-own-gateway", "- id: ui-chat", "transcriptView: standard"} {
		if !strings.Contains(content, want) {
			t.Errorf("user content %q lost after install:\n%s", want, content)
		}
	}
	if n := strings.Count(content, "id: llm-pi-ai"); n != 1 {
		t.Errorf("expected exactly one llm-pi-ai row, got %d:\n%s", n, content)
	}

	providers := patchProviders(t, path)
	// The user's provider must survive beside Prism's routes.
	if _, ok := providers["my-own-gateway"]; !ok {
		t.Errorf("user provider lost after install: %v", providers)
	}

	chat, _ := providers[deepSeekHarnessProviderID].(map[string]interface{})
	if chat == nil {
		t.Fatalf("openai-completions route missing: %v", providers)
	}
	if chat["api"] != "openai-completions" {
		t.Errorf("chat api = %v, want openai-completions", chat["api"])
	}
	if chat["baseURL"] != "http://127.0.0.1:11434/v1" {
		t.Errorf("chat baseURL = %v, want http://127.0.0.1:11434/v1", chat["baseURL"])
	}
	headers, _ := chat["headers"].(map[string]interface{})
	if headers["Authorization"] != "Bearer prism" {
		t.Errorf("chat Authorization = %v, want Bearer prism", headers["Authorization"])
	}
	chatCompat, _ := chat["compat"].(map[string]interface{})
	if chatCompat["supportsDeveloperRole"] != false {
		t.Errorf("chat supportsDeveloperRole = %v, want false", chatCompat["supportsDeveloperRole"])
	}
	if chatCompat["maxTokensField"] != "max_tokens" {
		t.Errorf("chat maxTokensField = %v, want max_tokens (Prism reads max_tokens only)", chatCompat["maxTokensField"])
	}

	// maxTokens is the PiAiModelProfile field; maxOutputTokens does not exist.
	chatModels := routeModels(chat)
	first := chatModels["ollama_cloud/chat-model"]
	if first == nil {
		t.Fatalf("chat-model missing from the chat route: %v", chatModels)
	}
	if first["contextWindow"] != 131072 {
		t.Errorf("contextWindow = %v, want 131072", first["contextWindow"])
	}
	if first["maxTokens"] != 8192 {
		t.Errorf("maxTokens = %v, want 8192", first["maxTokens"])
	}
	if _, wrong := first["maxOutputTokens"]; wrong {
		t.Error("maxOutputTokens is not a PiAiModelProfile field; it would be dropped silently")
	}
	if second := chatModels["ollama_cloud/plain-model"]; second == nil || second["contextWindow"] != 128000 {
		t.Errorf("default contextWindow = %v, want 128000", second)
	}

	// The Responses route must NOT carry maxTokensField: a route-level compat
	// switch the route's protocol cannot take makes DSH skip every model on it.
	responses, _ := providers[deepSeekHarnessResponsesProviderID].(map[string]interface{})
	if responses == nil {
		t.Fatalf("openai-responses route missing: %v", providers)
	}
	if responses["api"] != "openai-responses" {
		t.Errorf("responses api = %v, want openai-responses", responses["api"])
	}
	responsesCompat, _ := responses["compat"].(map[string]interface{})
	if _, present := responsesCompat["maxTokensField"]; present {
		t.Error("maxTokensField is openai-completions-only and must not appear on the Responses route")
	}
	if responsesCompat["supportsDeveloperRole"] != false {
		t.Errorf("responses supportsDeveloperRole = %v, want false", responsesCompat["supportsDeveloperRole"])
	}
	if _, ok := routeModels(responses)["codex_abc/resp-model"]; !ok {
		t.Errorf("resp-model missing from the Responses route: %v", routeModels(responses))
	}

	// Prism must never write agent-default-model: that row is the user's model
	// choice, and Prism overriding it would silently change their selection.
	defRow := patchRow(t, path, "agent-default-model")
	defCfg, _ := defRow["config"].(map[string]interface{})
	if defCfg["provider"] != "my-own-gateway" || defCfg["model"] != "my-model" {
		t.Errorf("agent-default-model was altered: %v", defCfg)
	}

	// Re-install is idempotent.
	if err := InstallDeepSeekHarnessConfig(11434, remap); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	if again := mustRead(t, path); again != content {
		t.Errorf("re-install changed the file:\n%s\n---\n%s", content, again)
	}

	// Restore drops Prism's routes and keeps the user's provider and rows.
	if err := RestoreDeepSeekHarnessConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	providers = patchProviders(t, path)
	if _, ok := providers[deepSeekHarnessProviderID]; ok {
		t.Error("prism route still present after restore")
	}
	if _, ok := providers["my-own-gateway"]; !ok {
		t.Errorf("user provider lost after restore: %v", providers)
	}
	if patchRow(t, path, "ui-chat") == nil {
		t.Error("unrelated row lost after restore")
	}
}

// DSH's settings migration writes Prism's routes into the profile patch itself.
// A sync must adopt that copy instead of adding a second prism key, which YAML
// would resolve by letting the last one win.
func TestInstallDeepSeekHarnessConfigAdoptsMigratedProviders(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	// Shaped like the patch DSH produced after migrating settings.yaml.
	migrated := `- id: llm-pi-ai
  name: "@deepseek-ai/dsh-llm-pi-ai"
  config:
    providers:
      prism:
        displayName: Prism
        api: openai-completions
        baseURL: http://127.0.0.1:11434/v1
        models:
          - id: stale-model
            contextWindow: 1000
      prism-responses:
        displayName: Prism (Responses)
        api: openai-responses
        baseURL: http://127.0.0.1:11434/v1
        models:
          - id: stale-responses
            contextWindow: 1000
      kept-provider:
        api: openai-completions
        baseURL: http://example.test/v1
- id: ui-chat
  name: "@deepseek-ai/dsh-client-ui-chat"
  config:
    transcriptView: standard
`
	path := writeProfilePatch(t, home, "desktop", migrated)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "fresh-model", Provider: "ollama_cloud", ContextLength: 4096},
	}}
	if err := InstallDeepSeekHarnessConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	content := mustRead(t, path)

	if n := strings.Count(content, deepSeekHarnessProviderID+":"); n != 1 {
		t.Errorf("expected exactly one prism route key, got %d:\n%s", n, content)
	}
	if strings.Contains(content, "stale-model") {
		t.Errorf("the migrated copy was not adopted:\n%s", content)
	}
	providers := patchProviders(t, path)
	if _, ok := providers["kept-provider"]; !ok {
		t.Errorf("unrelated provider lost: %v", providers)
	}
	chat, _ := providers[deepSeekHarnessProviderID].(map[string]interface{})
	if _, ok := routeModels(chat)["ollama_cloud/fresh-model"]; !ok {
		t.Errorf("fresh model missing: %v", routeModels(chat))
	}
	if _, ok := providers[deepSeekHarnessResponsesProviderID]; ok {
		t.Error("stale Responses route kept without a Responses model to justify it")
	}
}

// A profile whose patch is still the freshly generated `[]` placeholder, or has
// no llm-pi-ai row at all, gets one added - without losing the header comments.
func TestInstallDeepSeekHarnessConfigAddsRow(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	header := "# Your patch layer for this dsh profile, applied after every bundle layer:\n# a top-level YAML array of loader patch entries.\n[]\n"
	path := writeProfilePatch(t, home, "web", header)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "chat-model", Provider: "ollama_cloud"},
	}}
	if err := InstallDeepSeekHarnessConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	content := mustRead(t, path)

	if !strings.Contains(content, "# Your patch layer") {
		t.Errorf("header comments lost:\n%s", content)
	}
	if strings.Contains(strings.TrimSpace(content), "[]") {
		t.Errorf("the empty-list placeholder was left in place:\n%s", content)
	}
	if n := strings.Count(content, "id: llm-pi-ai"); n != 1 {
		t.Errorf("expected one llm-pi-ai row, got %d:\n%s", n, content)
	}
	if _, ok := patchProviders(t, path)[deepSeekHarnessProviderID]; !ok {
		t.Errorf("prism route missing:\n%s", content)
	}

	// A patch with no llm-pi-ai row but other rows keeps them.
	path2 := writeProfilePatch(t, home, "desktop", "- id: ui-chat\n  name: \"@deepseek-ai/dsh-client-ui-chat\"\n  config:\n    transcriptView: standard\n")
	if err := InstallDeepSeekHarnessConfig(11434, remap); err != nil {
		t.Fatalf("install into second profile: %v", err)
	}
	if patchRow(t, path2, "ui-chat") == nil {
		t.Errorf("existing row lost:\n%s", mustRead(t, path2))
	}
	if _, ok := patchProviders(t, path2)[deepSeekHarnessProviderID]; !ok {
		t.Errorf("prism route missing from second profile:\n%s", mustRead(t, path2))
	}
}

func TestInstallDeepSeekHarnessConfigRefusesInlineProviders(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	inline := "- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers: {mine: {api: openai-completions}}\n"
	path := writeProfilePatch(t, home, "web", inline)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{{ID: "m", Provider: "ollama_cloud"}}}
	if err := InstallDeepSeekHarnessConfig(11434, remap); err == nil {
		t.Fatal("expected install to refuse an inline config.providers value")
	}
	if got := mustRead(t, path); got != inline {
		t.Errorf("refused install must leave the file untouched:\n%s", got)
	}
}

// Without an initialized profile there is nowhere to write, and silently doing
// nothing would look like success in the UI.
func TestInstallDeepSeekHarnessConfigRequiresProfiles(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "profiles", "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{{ID: "m", Provider: "ollama_cloud"}}}
	if err := InstallDeepSeekHarnessConfig(11434, remap); err == nil {
		t.Fatal("expected an error when no profile is initialized")
	}
}

// The first version of this integration wrote a settings.yaml, which DSH
// migrates into a profile patch and then retires. A leftover region would be
// re-migrated and could shadow a provider the user added in the UI.
func TestInstallAndRestoreStripLegacySettings(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)
	path := writeProfilePatch(t, home, "web", "[]\n")

	settingsPath := deepSeekHarnessSettingsPath()
	legacy := deepSeekHarnessProviderManagedBegin + "\nllm-pi-ai:\n  providers:\n    prism:\n      api: openai-completions\n" + deepSeekHarnessProviderManagedEnd + "\n"
	if err := os.WriteFile(settingsPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{{ID: "m", Provider: "ollama_cloud"}}}
	if err := InstallDeepSeekHarnessConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("legacy settings.yaml should be removed once its region is gone, stat err = %v", err)
	}
	if _, ok := patchProviders(t, path)[deepSeekHarnessProviderID]; !ok {
		t.Errorf("prism route missing from the profile patch:\n%s", mustRead(t, path))
	}
}

func TestInstallDeepSeekHarnessMCPLifecycle(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)

	patchPath := deepSeekHarnessPatchPath()
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatal(err)
	}
	// A user entry that must survive install and restore.
	userPatch := "- id: my-entry\n  name: '@scope/my-plugin'\n"
	if err := os.WriteFile(patchPath, []byte(userPatch), 0600); err != nil {
		t.Fatal(err)
	}

	url := "http://127.0.0.1:11434/mcp/deepseek-harness"
	if err := installDeepSeekHarnessMCP(url); err != nil {
		t.Fatalf("install MCP: %v", err)
	}

	data, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "- id: my-entry") {
		t.Errorf("user entry lost after MCP install:\n%s", content)
	}

	var entries []map[string]interface{}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("patch is not a valid YAML sequence: %v\n%s", err, content)
	}
	if !hasDeepSeekHarnessMCPServer(data) {
		t.Fatalf("prism-mcp entry not detected:\n%s", content)
	}

	var cfg map[string]interface{}
	for _, entry := range entries {
		inserts, _ := entry["insert"].([]interface{})
		for _, raw := range inserts {
			item, _ := raw.(map[string]interface{})
			if id, _ := item["id"].(string); id == deepSeekHarnessMCPEntryID {
				cfg, _ = item["config"].(map[string]interface{})
			}
		}
	}
	if cfg == nil {
		t.Fatal("prism-mcp insert entry missing its config")
	}
	if cfg["serverName"] != "prism" {
		t.Errorf("serverName = %v, want prism", cfg["serverName"])
	}
	if cfg["transport"] != "streamable-http" {
		t.Errorf("transport = %v, want streamable-http", cfg["transport"])
	}
	if cfg["url"] != url {
		t.Errorf("url = %v, want %v", cfg["url"], url)
	}
	// dsh-mcp-client declares both as non-optional even though the README
	// documents defaults, so Prism writes them explicitly.
	if cfg["toolCallTimeoutMs"] != 60000 {
		t.Errorf("toolCallTimeoutMs = %v, want 60000", cfg["toolCallTimeoutMs"])
	}
	if cfg["failOnStartupError"] != false {
		t.Errorf("failOnStartupError = %v, want false", cfg["failOnStartupError"])
	}
	cfgHeaders, _ := cfg["headers"].(map[string]interface{})
	if cfgHeaders["Authorization"] != "Bearer prism" {
		t.Errorf("MCP Authorization = %v, want Bearer prism", cfgHeaders["Authorization"])
	}

	// Idempotent.
	if err := installDeepSeekHarnessMCP(url); err != nil {
		t.Fatalf("re-install MCP: %v", err)
	}
	if again := mustRead(t, patchPath); again != content {
		t.Errorf("re-install changed the patch:\n%s\n---\n%s", content, again)
	}

	// Restore drops Prism's row and keeps the user's.
	if err := restoreDeepSeekHarnessMCP(); err != nil {
		t.Fatalf("restore MCP: %v", err)
	}
	restored, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if hasDeepSeekHarnessMCPServer(restored) {
		t.Errorf("prism-mcp still present after restore:\n%s", restored)
	}
	if !strings.Contains(string(restored), "- id: my-entry") {
		t.Errorf("user entry lost after restore:\n%s", restored)
	}
}

// An existing empty patch (`[]`) must be replaced, not appended to: `[]`
// followed by list items is not valid YAML.
func TestInstallDeepSeekHarnessMCPReplacesEmptyList(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)

	patchPath := deepSeekHarnessPatchPath()
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patchPath, []byte("[]\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDeepSeekHarnessMCP("http://127.0.0.1:11434/mcp/deepseek-harness"); err != nil {
		t.Fatalf("install MCP: %v", err)
	}
	data, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]interface{}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		t.Fatalf("patch is not a valid YAML sequence: %v\n%s", err, data)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly the prism-mcp entry, got %d:\n%s", len(entries), data)
	}
	if !hasDeepSeekHarnessMCPServer(data) {
		t.Errorf("prism-mcp entry not detected:\n%s", data)
	}
}

func TestHasDeepSeekHarnessProviders(t *testing.T) {
	withRoute := []byte("- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n      " + deepSeekHarnessProviderManagedBegin + "\n      prism:\n        api: openai-completions\n      " + deepSeekHarnessProviderManagedEnd + "\n")
	if !hasDeepSeekHarnessProviders(withRoute) {
		t.Error("expected the prism route to be detected")
	}
	withResponses := []byte("- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n      " + deepSeekHarnessProviderManagedBegin + "\n      prism-responses:\n        api: openai-responses\n      " + deepSeekHarnessProviderManagedEnd + "\n")
	if !hasDeepSeekHarnessProviders(withResponses) {
		t.Error("expected the prism-responses route to be detected")
	}
	// A provider that merely resembles ours, with no marker, is not Prism.
	other := []byte("- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n      prism-like:\n        api: openai-completions\n")
	if hasDeepSeekHarnessProviders(other) {
		t.Error("a provider without Prism's marker must not count")
	}
	if hasDeepSeekHarnessProviders([]byte("[]\n")) {
		t.Error("an empty patch must not count")
	}
}

func TestDeepSeekHarnessPaths(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)

	t.Setenv("DSH_HOME", filepath.Join(tmp, "custom"))
	if got, want := deepSeekHarnessPatchPath(), filepath.Join(tmp, "custom", "cordis.patch.yml"); got != want {
		t.Errorf("patch path with DSH_HOME = %q, want %q", got, want)
	}
	if got, want := deepSeekHarnessProfilesDir(), filepath.Join(tmp, "custom", "profiles"); got != want {
		t.Errorf("profiles dir with DSH_HOME = %q, want %q", got, want)
	}
	if got, want := deepSeekHarnessSettingsPath(), filepath.Join(tmp, "custom", "settings.yaml"); got != want {
		t.Errorf("legacy settings path = %q, want %q", got, want)
	}

	// Without the override DSH resolves its home from the user's home directory.
	t.Setenv("DSH_HOME", "")
	if got, want := deepSeekHarnessPatchPath(), filepath.Join(tmp, ".dsh", "cordis.patch.yml"); got != want {
		t.Errorf("default patch path = %q, want %q", got, want)
	}
	if got, want := deepSeekHarnessHome(), filepath.Join(tmp, ".dsh"); got != want {
		t.Errorf("default home = %q, want %q", got, want)
	}
}

// node_modules is the shared dependency store, not a profile, and a profile is
// only usable once it carries a patch or config layer.
func TestDeepSeekHarnessProfileDiscovery(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)

	profiles := filepath.Join(home, "profiles")
	for _, dir := range []string{"node_modules", "half-made", "web", "desktop"} {
		if err := os.MkdirAll(filepath.Join(profiles, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// node_modules holds packages, not a patch.
	if err := os.WriteFile(filepath.Join(profiles, "node_modules", "cordis.patch.yml"), []byte("[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// half-made has no layers yet.
	if err := os.WriteFile(filepath.Join(profiles, "desktop", "cordis.yml"), []byte("[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	webPatch := writeProfilePatch(t, home, "web", "[]\n")

	got := deepSeekHarnessProfilePatchPaths()
	want := []string{filepath.Join(profiles, "desktop", "cordis.patch.yml"), webPatch}
	if len(got) != len(want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("profiles[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDeepSeekHarnessRegisteredAsAgent(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)

	found := false
	for _, id := range supportedAgents {
		if id == "deepseek-harness" {
			found = true
		}
	}
	if !found {
		t.Error("deepseek-harness missing from supportedAgents")
	}
	if got := AgentDisplayName("deepseek-harness"); got != "DeepSeek Harness" {
		t.Errorf("AgentDisplayName = %q, want %q", got, "DeepSeek Harness")
	}
	if !AgentMCPSupported("deepseek-harness") {
		t.Error("AgentMCPSupported(deepseek-harness) = false, want true")
	}
	// Providers are per profile and MCP is home-level, so the agent's config
	// path is the home directory rather than any single file.
	if got, want := agentConfigPath("deepseek-harness"), deepSeekHarnessHome(); got != want {
		t.Errorf("agentConfigPath = %q, want %q", got, want)
	}
	if got, want := AgentMCPConfigPath("deepseek-harness"), deepSeekHarnessPatchPath(); got != want {
		t.Errorf("AgentMCPConfigPath = %q, want %q", got, want)
	}
}

// IsAgentActive cannot use its single-file read path for DSH, whose providers
// span one patch per profile.
func TestIsAgentActiveDeepSeekHarness(t *testing.T) {
	tmp := setTestHomeAndConfigDir(t)
	home := filepath.Join(tmp, "dsh")
	t.Setenv("DSH_HOME", home)
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)
	writeProfilePatch(t, home, "web", "[]\n")

	if IsAgentActive("deepseek-harness") {
		t.Error("active before install")
	}

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "chat-model", Provider: "ollama_cloud"},
	}}
	if err := InstallDeepSeekHarnessConfig(11434, remap); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !IsAgentActive("deepseek-harness") {
		t.Error("expected deepseek-harness to be active after install")
	}

	if err := RestoreDeepSeekHarnessConfig(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if IsAgentActive("deepseek-harness") {
		t.Error("expected deepseek-harness inactive after restore")
	}
}
