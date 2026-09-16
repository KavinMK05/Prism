package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode/utf16"

	"ollama-proxy/internal/config"
)

func TestWslUncPath(t *testing.T) {
	cases := map[string]string{
		"/home/kavin/.prime/agent": `\\wsl$\Ubuntu\home\kavin\.prime\agent`,
		"/root/.prime/agent":       `\\wsl$\Debian\root\.prime\agent`,
		"/home/a b/.prime/agent":   `\\wsl$\Ubuntu\home\a b\.prime\agent`,
	}
	for in, want := range cases {
		distro := "Ubuntu"
		if len(in) > 6 && in[1:5] == "root" {
			distro = "Debian"
		}
		if got := wslUncPath(distro, in); got != want {
			t.Errorf("wslUncPath(%q, %q) = %q, want %q", distro, in, got, want)
		}
	}
}

func TestPrimeAgentEnvOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", tmp)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if got := primeAgentConfigDir(); got != tmp {
		t.Fatalf("primeAgentConfigDir() = %q, want %q", got, tmp)
	}
	if got := primeAgentModelsPath(); got != filepath.Join(tmp, "models.json") {
		t.Fatalf("primeAgentModelsPath() = %q", got)
	}

	// Legacy var is honored when the current one is unset.
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", tmp)
	if got := primeAgentConfigDir(); got != tmp {
		t.Fatalf("legacy override: primeAgentConfigDir() = %q, want %q", got, tmp)
	}
}

func TestPrimeAgentInstallRestore(t *testing.T) {
	setTestHomeAndConfigDir(t)
	tmp := t.TempDir()
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", tmp)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	writePrismConfig(t, `{"default_provider":"ollama_cloud"}`)

	remap := &config.ModelRemapping{KnownModels: []config.ModelEntry{
		{ID: "chat-model", Provider: "ollama_cloud"},
		{ID: "codex-model", Provider: "codex_test1"},
	}}

	// Pre-existing user content must survive install/restore.
	modelsPath := filepath.Join(tmp, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"other":{"baseUrl":"https://x","apiKey":"k","api":"openai-completions","models":[{"id":"m"}]}}}`), 0600); err != nil {
		t.Fatalf("seed models.json: %v", err)
	}

	if err := InstallPrimeAgentConfig(11434, remap); err != nil {
		t.Fatalf("InstallPrimeAgentConfig: %v", err)
	}
	if !isPrimeAgentActive() {
		t.Fatal("expected Prime Agent to be active after install")
	}
	data, _ := os.ReadFile(modelsPath)
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse models.json: %v", err)
	}
	provs := m["providers"].(map[string]interface{})
	if _, ok := provs["prism"]; !ok {
		t.Error("prism provider missing after install")
	}
	if _, ok := provs["prism-responses"]; !ok {
		t.Error("prism-responses provider missing after install (codex model)")
	}
	if _, ok := provs["other"]; !ok {
		t.Error("pre-existing provider lost after install")
	}

	// Default model file must be left untouched.
	if _, err := os.Stat(filepath.Join(tmp, "settings.json")); !os.IsNotExist(err) {
		t.Error("settings.json must not be created by install")
	}

	if err := RestorePrimeAgentConfig(); err != nil {
		t.Fatalf("RestorePrimeAgentConfig: %v", err)
	}
	if isPrimeAgentActive() {
		t.Fatal("expected Prime Agent to be inactive after restore")
	}
	data, _ = os.ReadFile(modelsPath)
	m = nil
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse models.json after restore: %v", err)
	}
	if _, ok := m["providers"].(map[string]interface{})["other"]; !ok {
		t.Error("pre-existing provider lost after restore")
	}
}

// utf16le encodes s the way wsl.exe writes its own output to a pipe.
func utf16le(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// TestParseWSLDistroList covers decoding wsl.exe's UTF-16LE metadata output.
// A wrong decode here is not cosmetic: losing the distro name stops Prime Agent
// syncing silently, and inventing a name that is not running would let Prism
// boot WSL, which it must never do.
func TestParseWSLDistroList(t *testing.T) {
	cases := []struct {
		name string
		out  []byte
		want []string
	}{
		{"nothing running", nil, nil},
		{"only newlines", utf16le("\r\n"), nil},
		{"one distro", utf16le("Ubuntu\r\n"), []string{"Ubuntu"}},
		{"two distros", utf16le("Ubuntu\r\nDebian\r\n"), []string{"Ubuntu", "Debian"}},
		{"no trailing newline", utf16le("Ubuntu"), []string{"Ubuntu"}},
		{"unix newlines", utf16le("Ubuntu\nDebian\n"), []string{"Ubuntu", "Debian"}},
	}
	for _, c := range cases {
		if got := parseWSLDistroList(c.out); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: parseWSLDistroList = %#v, want %#v", c.name, got, c.want)
		}
	}
}

func TestDecodeUTF16LEKeepsNonASCII(t *testing.T) {
	const want = "Ubuntu-日本語"
	if got := decodeUTF16LE(utf16le(want)); got != want {
		t.Errorf("decodeUTF16LE = %q, want %q", got, want)
	}
}

func TestWSLDistroInList(t *testing.T) {
	names := []string{"Ubuntu", "Debian"}
	if !wslDistroInList(names, "ubuntu") {
		t.Error("distro names must match case-insensitively; wsl.exe does not guarantee casing")
	}
	if wslDistroInList(names, "Arch") {
		t.Error("wslDistroInList matched a distro that is not in the list")
	}
	if wslDistroInList(nil, "Ubuntu") {
		t.Error("wslDistroInList matched against an empty list")
	}
}
