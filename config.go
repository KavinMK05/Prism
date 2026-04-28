package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type ProviderConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

type Config struct {
	ActiveProvider string          `json:"active_provider"`
	OllamaCloud    *ProviderConfig `json:"ollama_cloud"`
	OpenCodeGo     *ProviderConfig `json:"opencode_go"`
	Custom         *ProviderConfig `json:"custom"`
}

type ModelRemapping struct {
	DefaultModel string            `json:"default_model"`
	KnownModels  []string          `json:"known_models"`
	Aliases      map[string]string `json:"aliases"`
}

func getConfigDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "ollama-proxy")
}

func getConfigPath() string {
	return filepath.Join(getConfigDir(), "config.json")
}

func loadConfig() *Config {
	cfg := defaultConfig()
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return defaultConfig()
	}
	if cfg.OllamaCloud == nil {
		cfg.OllamaCloud = &ProviderConfig{Name: "Ollama Cloud", BaseURL: "https://ollama.com"}
	}
	if cfg.OpenCodeGo == nil {
		cfg.OpenCodeGo = &ProviderConfig{Name: "OpenCode Go", BaseURL: "https://opencode.ai/zen/go"}
	}
	if cfg.Custom == nil {
		cfg.Custom = &ProviderConfig{Name: "Custom", BaseURL: ""}
	}
	if cfg.ActiveProvider == "" {
		cfg.ActiveProvider = "ollama_cloud"
	}
	return cfg
}

func saveConfig(cfg *Config) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getConfigPath(), data, 0600)
}

func defaultConfig() *Config {
	return &Config{
		ActiveProvider: "ollama_cloud",
		OllamaCloud: &ProviderConfig{
			Name:    "Ollama Cloud",
			BaseURL: "https://ollama.com",
		},
		OpenCodeGo: &ProviderConfig{
			Name:    "OpenCode Go",
			BaseURL: "https://opencode.ai/zen/go",
		},
		Custom: &ProviderConfig{
			Name:    "Custom",
			BaseURL: "",
		},
	}
}

func (c *Config) getActiveProvider() *ProviderConfig {
	switch c.ActiveProvider {
	case "ollama_cloud":
		return c.OllamaCloud
	case "opencode_go":
		return c.OpenCodeGo
	case "custom":
		return c.Custom
	default:
		return c.OllamaCloud
	}
}

func (c *Config) getActiveAPIKey() string {
	p := c.getActiveProvider()
	if p.APIKey != "" {
		return p.APIKey
	}
	switch c.ActiveProvider {
	case "ollama_cloud":
		if key := os.Getenv("OLLAMA_API_KEY"); key != "" {
			return key
		}
	case "opencode_go":
		if key := os.Getenv("OPENCODE_GO_API_KEY"); key != "" {
			return key
		}
	}
	return ""
}

func (c *Config) getActiveBaseURL() string {
	p := c.getActiveProvider()
	return p.BaseURL
}

func (c *Config) getProviderType() string {
	switch c.ActiveProvider {
	case "ollama_cloud":
		return "ollama"
	case "opencode_go", "custom":
		return "openai"
	default:
		return "ollama"
	}
}

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func showInputDialog(title, prompt, defaultValue string) (string, error) {
	if !isSafeInput(title) || !isSafeInput(prompt) {
		return "", fmt.Errorf("invalid characters in dialog title or prompt")
	}

	safeDefault := defaultValue
	if !isSafeInput(safeDefault) {
		safeDefault = ""
	}

	vbs := fmt.Sprintf(`Dim result
result = InputBox("%s", "%s", "%s")
If result <> "" Then
    WScript.Echo result
End If`,
		escapeVBS(prompt),
		escapeVBS(title),
		escapeVBS(safeDefault),
	)

	tmpVBS := filepath.Join(os.TempDir(), "ollama-proxy-input.vbs")
	if err := os.WriteFile(tmpVBS, []byte(vbs), 0600); err != nil {
		return "", err
	}
	defer os.Remove(tmpVBS)

	cmd := exec.Command("cscript", "//Nologo", "//E:vbscript", tmpVBS)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000,
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return "", nil
	}
	return result, nil
}

func escapeVBS(s string) string {
	s = strings.ReplaceAll(s, `"`, `""`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func isSafeInput(s string) bool {
	for _, r := range s {
		if r < 32 && r != '\t' {
			return false
		}
	}
	return !strings.ContainsAny(s, "(){}<>|&;`$")
}

func getModelRemappingPath() string {
	return filepath.Join(getConfigDir(), "model_remapping.json")
}

func defaultModelRemapping() *ModelRemapping {
	return &ModelRemapping{
		DefaultModel: "glm-5.1:cloud",
		KnownModels: []string{
			"glm-5.1:cloud",
			"deepseek-v4-flash:cloud",
			"opencode/deepseek-v4-flash",
			"deepseek-v4-flash",
			"deepseek-v4-pro:cloud",
		},
		Aliases: map[string]string{
			"claude-3-5-haiku":            "deepseek-v4-flash:cloud",
			"claude-3-5-haiku-20241022":   "deepseek-v4-flash:cloud",
			"claude-3-haiku-20240307":     "deepseek-v4-flash:cloud",
			"claude-haiku-3-5-20241022":   "deepseek-v4-flash:cloud",
		},
	}
}

func loadModelRemapping() *ModelRemapping {
	remap := defaultModelRemapping()
	data, err := os.ReadFile(getModelRemappingPath())
	if err != nil {
		if os.IsNotExist(err) {
			saveModelRemapping(remap)
		}
		return remap
	}
	if err := json.Unmarshal(data, remap); err != nil {
		return defaultModelRemapping()
	}
	if remap.DefaultModel == "" {
		remap.DefaultModel = "glm-5.1:cloud"
	}
	if remap.KnownModels == nil {
		remap.KnownModels = []string{}
	}
	if remap.Aliases == nil {
		remap.Aliases = map[string]string{}
	}
	return remap
}

func saveModelRemapping(remap *ModelRemapping) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(remap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getModelRemappingPath(), data, 0600)
}

func getEffectiveModel(remap *ModelRemapping, requestedModel string) string {
	if target, ok := remap.Aliases[requestedModel]; ok {
		logModelRemap(requestedModel, target, "alias")
		return target
	}

	for _, known := range remap.KnownModels {
		if requestedModel == known || strings.HasPrefix(requestedModel, known+":") || strings.HasPrefix(requestedModel, known+"[") {
			return requestedModel
		}
	}

	if remap.DefaultModel != "" {
		logModelRemap(requestedModel, remap.DefaultModel, "default")
		return remap.DefaultModel
	}

	return requestedModel
}

func logModelRemap(from, to, reason string) {
	log.Printf("⊞ Model remap (%s): %s → %s", reason, from, to)
}

func validateBaseURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must use http:// or https:// scheme")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must have a host")
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
			return fmt.Errorf("URL points to a private/local address which may be a security risk")
		}
	}
	return nil
}