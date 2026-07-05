package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// writeDefaultSearxngSettings generates the initial settings.yml. use_default_settings
// inherits SearXNG's full engine set; JSON output is enabled and the bot limiter is
// off so no Redis/Valkey is required for local use. The file is the user's after
// first generation — the structured form edits it in place via merge.
func writeDefaultSearxngSettings() error {
	content := fmt.Sprintf(`use_default_settings: true
server:
  secret_key: "%s"
  bind_address: "127.0.0.1"
  port: 8888
  limiter: false
  public_instance: false
  image_proxy: false
search:
  formats:
    - html
    - json
`, randStr(64))
	return os.WriteFile(searxngSettingsPath(), []byte(content), 0600)
}

// searxngSettingsForm is the JSON shape exchanged with the admin endpoint. It
// covers the user-tunable subset of server:/search:/ui: in settings.yml. JSON-only
// (no yaml tags); the load/save functions translate between this struct and the
// YAML file's generic map.
type searxngSettingsForm struct {
	// server
	Port           int      `json:"port"`
	BindAddress    string   `json:"bind_address"`
	BaseURL        string   `json:"base_url"`
	SecretKey      string   `json:"secret_key"`
	Limiter        bool     `json:"limiter"`
	PublicInstance bool     `json:"public_instance"`
	ImageProxy     bool     `json:"image_proxy"`
	Method         string   `json:"method"`
	// search
	SafeSearch   int      `json:"safe_search"`
	Autocomplete string   `json:"autocomplete"`
	DefaultLang  string   `json:"default_lang"`
	Formats      []string `json:"formats"`
	// ui
	DefaultLocale          string `json:"default_locale"`
	DefaultTheme           string `json:"default_theme"`
	QueryInTitle           bool   `json:"query_in_title"`
	CenterAlignment        bool   `json:"center_alignment"`
	ResultsOnNewTab        bool   `json:"results_on_new_tab"`
	SearchOnCategorySelect bool   `json:"search_on_category_select"`
	Hotkeys                string `json:"hotkeys"`
	SimpleStyle            string `json:"simple_style"` // maps to ui.theme_args.simple_style
}

// loadSearxngSettingsForm reads settings.yml, parses it into a generic map, and
// projects the managed keys into the form struct with documented defaults for
// missing (inherited) keys. Returns an error if the file is absent.
func loadSearxngSettingsForm() (*searxngSettingsForm, error) {
	data, err := os.ReadFile(searxngSettingsPath())
	if err != nil {
		return nil, err
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse settings.yml: %w", err)
	}

	f := &searxngSettingsForm{
		Port:           8888,
		BindAddress:    "127.0.0.1",
		BaseURL:        "",
		SecretKey:      "",
		Limiter:        false,
		PublicInstance: false,
		ImageProxy:     false,
		Method:         "POST",
		SafeSearch:     0,
		Autocomplete:   "",
		DefaultLang:    "",
		Formats:        []string{"html"},
		DefaultLocale:  "",
		DefaultTheme:   "simple",
		QueryInTitle:   false,
		CenterAlignment: false,
		ResultsOnNewTab: false,
		SearchOnCategorySelect: false,
		Hotkeys:        "default",
		SimpleStyle:    "auto",
	}

	server := asMap(root["server"])
	if server != nil {
		if v, ok := server["port"]; ok {
			f.Port = toInt(v, f.Port)
		}
		f.BindAddress = toStr(server["bind_address"], f.BindAddress)
		f.BaseURL = toStr(server["base_url"], f.BaseURL)
		f.SecretKey = toStr(server["secret_key"], f.SecretKey)
		f.Limiter = toBool(server["limiter"], f.Limiter)
		f.PublicInstance = toBool(server["public_instance"], f.PublicInstance)
		f.ImageProxy = toBool(server["image_proxy"], f.ImageProxy)
		f.Method = toStr(server["method"], f.Method)
	}

	search := asMap(root["search"])
	if search != nil {
		if v, ok := search["safe_search"]; ok {
			f.SafeSearch = toInt(v, f.SafeSearch)
		}
		f.Autocomplete = toStr(search["autocomplete"], f.Autocomplete)
		f.DefaultLang = toStr(search["default_lang"], f.DefaultLang)
		if v, ok := search["formats"]; ok {
			f.Formats = toStringSlice(v, f.Formats)
		}
	}

	ui := asMap(root["ui"])
	if ui != nil {
		f.DefaultLocale = toStr(ui["default_locale"], f.DefaultLocale)
		f.DefaultTheme = toStr(ui["default_theme"], f.DefaultTheme)
		f.QueryInTitle = toBool(ui["query_in_title"], f.QueryInTitle)
		f.CenterAlignment = toBool(ui["center_alignment"], f.CenterAlignment)
		f.ResultsOnNewTab = toBool(ui["results_on_new_tab"], f.ResultsOnNewTab)
		f.SearchOnCategorySelect = toBool(ui["search_on_category_select"], f.SearchOnCategorySelect)
		f.Hotkeys = toStr(ui["hotkeys"], f.Hotkeys)
		if themeArgs := asMap(ui["theme_args"]); themeArgs != nil {
			f.SimpleStyle = toStr(themeArgs["simple_style"], f.SimpleStyle)
		}
	}

	return f, nil
}

// saveSearxngSettingsForm merges the form's managed keys into the existing
// settings.yml map, preserving every unmanaged key (engines, outgoing, redis,
// brand, plugins, categories_as_tabs, etc.). Marshal writes the merged map back.
func saveSearxngSettingsForm(f *searxngSettingsForm) error {
	data, err := os.ReadFile(searxngSettingsPath())
	if err != nil {
		return fmt.Errorf("read settings.yml: %w", err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse settings.yml: %w", err)
	}
	if root == nil {
		root = map[string]interface{}{}
	}

	server := ensureMap(root, "server")
	server["port"] = f.Port
	server["bind_address"] = f.BindAddress
	if f.BaseURL != "" {
		server["base_url"] = f.BaseURL
	} else {
		delete(server, "base_url")
	}
	server["secret_key"] = f.SecretKey
	server["limiter"] = f.Limiter
	server["public_instance"] = f.PublicInstance
	server["image_proxy"] = f.ImageProxy
	server["method"] = f.Method

	search := ensureMap(root, "search")
	search["safe_search"] = f.SafeSearch
	if f.Autocomplete != "" {
		search["autocomplete"] = f.Autocomplete
	} else {
		delete(search, "autocomplete")
	}
	if f.DefaultLang != "" {
		search["default_lang"] = f.DefaultLang
	} else {
		delete(search, "default_lang")
	}
	formats := make([]interface{}, 0, len(f.Formats))
	for _, fm := range f.Formats {
		formats = append(formats, fm)
	}
	search["formats"] = formats

	ui := ensureMap(root, "ui")
	if f.DefaultLocale != "" {
		ui["default_locale"] = f.DefaultLocale
	} else {
		delete(ui, "default_locale")
	}
	ui["default_theme"] = f.DefaultTheme
	ui["query_in_title"] = f.QueryInTitle
	ui["center_alignment"] = f.CenterAlignment
	ui["results_on_new_tab"] = f.ResultsOnNewTab
	ui["search_on_category_select"] = f.SearchOnCategorySelect
	ui["hotkeys"] = f.Hotkeys
	themeArgs := ensureMap(ui, "theme_args")
	themeArgs["simple_style"] = f.SimpleStyle

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal settings.yml: %w", err)
	}
	return os.WriteFile(searxngSettingsPath(), out, 0600)
}

// validateSearxngSettingsForm enforces the documented value constraints before save.
func validateSearxngSettingsForm(f *searxngSettingsForm) error {
	if f.Port < 1 || f.Port > 65535 {
		return fmt.Errorf("invalid settings: port must be 1..65535")
	}
	if f.Method != "POST" && f.Method != "GET" {
		return fmt.Errorf("invalid settings: method must be POST or GET")
	}
	if f.SafeSearch < 0 || f.SafeSearch > 2 {
		return fmt.Errorf("invalid settings: safe_search must be 0, 1, or 2")
	}
	validAutocomplete := map[string]bool{"": true, "google": true, "duckduckgo": true, "bing": true, "brave": true, "dbpedia": true, "wikipedia": true, "yandex": true, "startpage": true, "qwant": true, "mwmbl": true, "seznam": true, "swisscows": true, "privacywall": true, "naver": true, "360search": true, "baidu": true, "quark": true, "sogou": true}
	if !validAutocomplete[f.Autocomplete] {
		return fmt.Errorf("invalid settings: autocomplete backend %q not recognized", f.Autocomplete)
	}
	validFormats := map[string]bool{"html": true, "json": true, "csv": true, "rss": true}
	if len(f.Formats) == 0 {
		return fmt.Errorf("invalid settings: at least one output format required")
	}
	hasHTML := false
	for _, fm := range f.Formats {
		if !validFormats[fm] {
			return fmt.Errorf("invalid settings: format %q not allowed", fm)
		}
		if fm == "html" {
			hasHTML = true
		}
	}
	if !hasHTML {
		return fmt.Errorf("invalid settings: html format is required")
	}
	if f.Hotkeys != "default" && f.Hotkeys != "vim" {
		return fmt.Errorf("invalid settings: hotkeys must be default or vim")
	}
	switch f.SimpleStyle {
	case "auto", "light", "dark", "black":
	default:
		return fmt.Errorf("invalid settings: simple_style must be auto, light, dark, or black")
	}
	if strings.TrimSpace(f.DefaultTheme) == "" {
		return fmt.Errorf("invalid settings: default_theme must not be empty")
	}
	if strings.TrimSpace(f.BindAddress) == "" {
		return fmt.Errorf("invalid settings: bind_address must not be empty")
	}
	if strings.TrimSpace(f.SecretKey) == "" {
		return fmt.Errorf("invalid settings: secret_key must not be empty")
	}
	return nil
}

// searxngPortFromSettings reads server.port from settings.yml, defaulting to 8888.
func searxngPortFromSettings() int {
	data, err := os.ReadFile(searxngSettingsPath())
	if err != nil {
		return 8888
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return 8888
	}
	if server := asMap(root["server"]); server != nil {
		if v, ok := server["port"]; ok {
			return toInt(v, 8888)
		}
	}
	return 8888
}

// --- generic map helpers ---

func asMap(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func ensureMap(parent map[string]interface{}, key string) map[string]interface{} {
	if existing := asMap(parent[key]); existing != nil {
		return existing
	}
	m := map[string]interface{}{}
	parent[key] = m
	return m
}

func toInt(v interface{}, def int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case uint64:
		return int(n)
	case string:
		var x int
		if _, err := fmt.Sscanf(n, "%d", &x); err == nil {
			return x
		}
	}
	return def
}

func toStr(v interface{}, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func toBool(v interface{}, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func toStringSlice(v interface{}, def []string) []string {
	if arr, ok := v.([]interface{}); ok {
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}