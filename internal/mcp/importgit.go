package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/platform"
)

// Importing a plugin manifest is Prism's thin slice of Agent Plugins support:
// point Prism at a git repository that contains an mcp.json (optionally next to
// a plugin.json) and every MCP server it declares becomes a Prism server.
//
// The manifest shape follows the Agent Plugins / MCP convention:
//
//	{ "$schema": "...", "mcpServers": { "name": { "type": "stdio"|"streamable-http"|"sse",
//	    "command": "npx", "args": ["-y", "pkg"], "env": {...},
//	    "url": "https://...", "headers": {...} } } }
//
// ${PLUGIN_ROOT} and ${PLUGIN_DATA} placeholders are resolved to the cloned
// plugin directory and a per-plugin data directory.

// manifestFileNames are checked in order inside the repository.
var manifestFileNames = []string{"mcp.json", ".mcp.json", "plugin.json", ".plugin/plugin.json"}

// gitRepository is a parsed git URL: the clone target plus an optional
// subdirectory and ref.
type gitRepository struct {
	Remote string
	Ref    string
	Subdir string
	Slug   string
}

// ImportedPlugin is the result of an import: the servers to add plus where the
// plugin was cloned, for display.
type ImportedPlugin struct {
	Slug    string                    `json:"slug"`
	Dir     string                    `json:"dir"`
	Name    string                    `json:"name,omitempty"`
	Version string                    `json:"version,omitempty"`
	Servers []*config.MCPServerConfig `json:"servers"`
}

// ImportGitRepository clones a git repository and converts its manifest into
// server configs. The clone is shallow and lives under Prism's config dir.
func ImportGitRepository(ctx context.Context, rawURL string) (*ImportedPlugin, error) {
	repo, err := parseGitURL(rawURL)
	if err != nil {
		return nil, err
	}
	git, ok := resolveCommand("git")
	if !ok {
		return nil, &RuntimeMissingError{Command: "git"}
	}

	dir := filepath.Join(platform.ConfigDir(), "mcp", "plugins", repo.Slug)

	// A fresh clone each import keeps the manifest and the code in step; the
	// directory is Prism-owned, so replacing it is safe.
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("failed to clear the previous checkout: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return nil, err
	}
	args := []string{"clone", "--depth", "1"}
	if repo.Ref != "" {
		args = append(args, "--branch", repo.Ref)
	}
	args = append(args, repo.Remote, dir)
	cmd := exec.CommandContext(ctx, git, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	manifestPath, manifest, err := findManifest(dir, repo.Subdir)
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(platform.ConfigDir(), "mcp", "data", repo.Slug)
	_ = os.MkdirAll(dataDir, 0755)

	pluginRoot := manifestPath
	servers, err := serversFromManifest(manifest, pluginRoot, dataDir)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, errors.New("the manifest does not declare any MCP servers")
	}

	meta := parsePluginMeta(filepath.Dir(manifestPath))
	return &ImportedPlugin{
		Slug:    repo.Slug,
		Dir:     filepath.Dir(manifestPath),
		Name:    meta.Name,
		Version: meta.Version,
		Servers: servers,
	}, nil
}

// pluginMeta is the subset of plugin.json Prism reads.
type pluginMeta struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

func parsePluginMeta(dir string) pluginMeta {
	for _, name := range []string{"plugin.json", ".plugin.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var meta pluginMeta
		if json.Unmarshal(data, &meta) == nil {
			return meta
		}
	}
	return pluginMeta{}
}

// findManifest locates the plugin manifest inside the checkout.
func findManifest(root, subdir string) (string, []byte, error) {
	base := root
	if subdir != "" {
		base = filepath.Join(root, filepath.FromSlash(subdir))
	}
	for _, name := range manifestFileNames {
		path := filepath.Join(base, filepath.FromSlash(name))
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if manifestDeclaresServers(data) {
			return path, data, nil
		}
	}
	return "", nil, fmt.Errorf("no mcp.json with mcpServers found in %s", base)
}

func manifestDeclaresServers(data []byte) bool {
	var probe struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return len(probe.MCPServers) > 0
}

// manifestEntry is one server in an mcp.json file. The union of the stdio and
// remote shapes is deliberate: manifests mix them in one map.
type manifestEntry struct {
	Type      string            `json:"type,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Enabled   *bool             `json:"enabled,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
}

// serversFromManifest converts an mcp.json into server configs.
func serversFromManifest(data []byte, pluginRoot, dataDir string) ([]*config.MCPServerConfig, error) {
	var wrapper struct {
		MCPServers map[string]manifestEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("invalid mcp.json: %w", err)
	}
	entries := wrapper.MCPServers
	if len(entries) == 0 {
		// Also accept a bare map of servers, which some repositories use.
		var bare map[string]manifestEntry
		if err := json.Unmarshal(data, &bare); err == nil {
			entries = bare
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("mcp.json does not declare any servers")
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sortStrings(names)

	servers := make([]*config.MCPServerConfig, 0, len(names))
	for _, name := range names {
		entry := entries[name]
		srv, err := serverFromManifestEntry(name, entry, pluginRoot, dataDir)
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		servers = append(servers, srv)
	}
	return servers, nil
}

func serverFromManifestEntry(name string, entry manifestEntry, pluginRoot, dataDir string) (*config.MCPServerConfig, error) {
	expand := func(s string) string {
		s = strings.ReplaceAll(s, "${PLUGIN_ROOT}", pluginRoot)
		s = strings.ReplaceAll(s, "${PLUGIN_DATA}", dataDir)
		s = strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginRoot)
		return s
	}

	srv := &config.MCPServerConfig{
		ID:      config.MCPIDFromName(name),
		Name:    name,
		Source:  config.MCPSourceGit,
		Enabled: true,
	}
	if entry.Enabled != nil {
		srv.Enabled = *entry.Enabled
	}

	kind := entry.Type
	if kind == "" {
		kind = entry.Transport
	}
	switch strings.ToLower(kind) {
	case "stdio", "local", "":
		if entry.URL != "" {
			return nil, errors.New("manifest entry has both a command and a url")
		}
		command := expand(strings.TrimSpace(entry.Command))
		if command == "" {
			return nil, errors.New("stdio server has no command")
		}
		// The Agent Plugins schema wants a single executable token, but
		// hand-written manifests often inline flags; split defensively.
		parts := strings.Fields(command)
		srv.Transport = config.MCPTransportStdio
		srv.Command = parts[0]
		srv.Args = append(append([]string(nil), parts[1:]...), expandArgs(entry.Args, expand)...)
		if entry.Cwd != "" {
			srv.Cwd = expand(entry.Cwd)
		}
		if len(entry.Env) > 0 {
			env := map[string]string{}
			for k, v := range entry.Env {
				env[k] = expand(v)
			}
			srv.Env = env
		}
	case "streamable-http", "http", "remote":
		if entry.URL == "" {
			return nil, errors.New("remote server has no url")
		}
		srv.Transport = config.MCPTransportHTTP
		srv.URL = entry.URL
		srv.Headers = expandHeaders(entry.Headers, expand)
		if len(srv.Headers) > 0 {
			srv.AuthMode = config.MCPAuthStatic
		}
	case "sse":
		if entry.URL == "" {
			return nil, errors.New("remote server has no url")
		}
		srv.Transport = config.MCPTransportSSE
		srv.URL = entry.URL
		srv.Headers = expandHeaders(entry.Headers, expand)
		if len(srv.Headers) > 0 {
			srv.AuthMode = config.MCPAuthStatic
		}
	default:
		return nil, fmt.Errorf("unsupported transport %q", kind)
	}
	return srv, nil
}

func expandArgs(args []string, expand func(string) string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, expand(a))
	}
	return out
}

func expandHeaders(headers map[string]string, expand func(string) string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = expand(v)
	}
	return out
}

var gitSSHPattern = regexp.MustCompile(`^git@([^:]+):(.+)$`)

// parseGitURL accepts https, ssh (git@host:owner/repo), and the browser URL
// forms with a /tree/<ref>/<subdir> suffix.
func parseGitURL(raw string) (*gitRepository, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("enter a git repository URL")
	}
	if m := gitSSHPattern.FindStringSubmatch(raw); m != nil {
		path := strings.TrimSuffix(m[2], ".git")
		return &gitRepository{
			Remote: "git@" + m[1] + ":" + path + ".git",
			Slug:   slugForGitPath(m[1], path),
		}, nil
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid git URL %q", raw)
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) < 2 {
		return nil, fmt.Errorf("git URL must point at a repository (owner/repo)")
	}
	remote := u.Scheme + "://" + u.Host + "/" + segments[0] + "/" + strings.TrimSuffix(segments[1], ".git") + ".git"
	repo := &gitRepository{Remote: remote, Slug: slugForGitPath(u.Host, segments[0]+"/"+segments[1])}
	if len(segments) > 2 {
		// github.com/owner/repo/tree/<ref>/<subdir>
		if segments[2] == "tree" || segments[2] == "blob" {
			if len(segments) > 3 {
				repo.Ref = segments[3]
			}
			if len(segments) > 4 {
				repo.Subdir = strings.Join(segments[4:], "/")
			}
		} else {
			repo.Subdir = strings.Join(segments[2:], "/")
		}
	}
	return repo, nil
}

func slugForGitPath(host, path string) string {
	slug := host + "/" + path
	slug = strings.TrimSuffix(slug, ".git")
	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 80 {
		out = out[:80]
	}
	if out == "" {
		out = "plugin"
	}
	return out
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
