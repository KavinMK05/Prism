package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"ollama-proxy/internal/config"
)

func TestParseGitURL(t *testing.T) {
	cases := []struct {
		in     string
		remote string
		ref    string
		subdir string
	}{
		{
			in:     "https://github.com/owner/repo",
			remote: "https://github.com/owner/repo.git",
		},
		{
			in:     "https://github.com/owner/repo/tree/main/plugins/notes",
			remote: "https://github.com/owner/repo.git",
			ref:    "main",
			subdir: "plugins/notes",
		},
		{
			in:     "git@github.com:owner/repo.git",
			remote: "git@github.com:owner/repo.git",
		},
		{
			in:     "https://github.com/owner/repo/some/subdir",
			remote: "https://github.com/owner/repo.git",
			subdir: "some/subdir",
		},
	}
	for _, tc := range cases {
		repo, err := parseGitURL(tc.in)
		if err != nil {
			t.Errorf("parseGitURL(%q): %v", tc.in, err)
			continue
		}
		if repo.Remote != tc.remote || repo.Ref != tc.ref || repo.Subdir != tc.subdir {
			t.Errorf("parseGitURL(%q) = %+v", tc.in, repo)
		}
		if repo.Slug == "" {
			t.Errorf("parseGitURL(%q) produced no slug", tc.in)
		}
	}
	if _, err := parseGitURL(""); err == nil {
		t.Error("empty URL should fail")
	}
	if _, err := parseGitURL("https://github.com"); err == nil {
		t.Error("URL without owner/repo should fail")
	}
}

func TestServersFromManifest(t *testing.T) {
	manifest := []byte(`{
	  "mcpServers": {
	    "notes": {
	      "type": "stdio",
	      "command": "node",
	      "args": ["${PLUGIN_ROOT}/server.js"],
	      "env": {"DATA_DIR": "${PLUGIN_DATA}"},
	      "cwd": "${PLUGIN_ROOT}"
	    },
	    "remote": {
	      "type": "streamable-http",
	      "url": "https://api.example.com/mcp",
	      "headers": {"Authorization": "Bearer ${PLUGIN_DATA}"}
	    },
	    "legacy": {"type": "sse", "url": "https://sse.example.com/mcp"},
	    "off": {"command": "node", "args": ["x.js"], "enabled": false}
	  }
	}`)

	servers, err := serversFromManifest(manifest, "/plugins/notes", "/data/notes")
	if err != nil {
		t.Fatalf("serversFromManifest: %v", err)
	}
	if len(servers) != 4 {
		t.Fatalf("got %d servers, want 4", len(servers))
	}
	byID := map[string]*config.MCPServerConfig{}
	for _, s := range servers {
		byID[s.ID] = s
	}

	notes := byID["notes"]
	if notes == nil {
		t.Fatal("notes server missing")
	}
	if notes.Transport != config.MCPTransportStdio || notes.Command != "node" {
		t.Errorf("notes = %+v", notes)
	}
	if len(notes.Args) != 1 || notes.Args[0] != "/plugins/notes/server.js" {
		t.Errorf("PLUGIN_ROOT was not expanded: %v", notes.Args)
	}
	if notes.Env["DATA_DIR"] != "/data/notes" {
		t.Errorf("PLUGIN_DATA was not expanded: %v", notes.Env)
	}
	if notes.Cwd != "/plugins/notes" {
		t.Errorf("cwd = %q", notes.Cwd)
	}
	if notes.Source != config.MCPSourceGit {
		t.Errorf("source = %q", notes.Source)
	}

	remote := byID["remote"]
	if remote.Transport != config.MCPTransportHTTP || remote.URL != "https://api.example.com/mcp" {
		t.Errorf("remote = %+v", remote)
	}
	if remote.AuthMode != config.MCPAuthStatic {
		t.Errorf("remote auth mode = %q", remote.AuthMode)
	}
	if remote.Headers["Authorization"] != "Bearer /data/notes" {
		t.Errorf("headers = %v", remote.Headers)
	}

	legacy := byID["legacy"]
	if legacy.Transport != config.MCPTransportSSE {
		t.Errorf("legacy transport = %q", legacy.Transport)
	}
	// A server without headers carries no auth mode; the runtime treats the
	// empty value as "none".
	if legacy.AuthMode != "" && legacy.AuthMode != config.MCPAuthNone {
		t.Errorf("legacy auth mode = %q", legacy.AuthMode)
	}

	if off := byID["off"]; off == nil || off.Enabled {
		t.Errorf("explicitly disabled server was not honoured: %+v", off)
	}
}

func TestFindManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	// A plugin.json without mcpServers must be skipped in favour of mcp.json.
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"demo","version":"1.2.3"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "mcp.json"), []byte(`{"mcpServers":{"a":{"command":"node"}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	path, data, err := findManifest(dir, "nested")
	if err != nil {
		t.Fatalf("findManifest: %v", err)
	}
	if filepath.Base(path) != "mcp.json" || !manifestDeclaresServers(data) {
		t.Errorf("found %q with %s", path, data)
	}
	if _, _, err := findManifest(dir, ""); err == nil {
		t.Error("the top level has no mcp.json, so discovery should fail")
	}
	meta := parsePluginMeta(dir)
	if meta.Name != "demo" || meta.Version != "1.2.3" {
		t.Errorf("plugin meta = %+v", meta)
	}
}

func TestRegistryServerConfig(t *testing.T) {
	npmEntry := RegistryEntry{Server: RegistryServer{
		Name: "io.github.owner/notes-mcp",
		Packages: []RegistryPackage{{
			RegistryType: "npm",
			Identifier:   "@owner/notes-mcp",
			Version:      "1.2.0",
			EnvironmentVariables: []RegistryKeyValue{
				{Name: "NOTES_TOKEN", IsRequired: true, IsSecret: true},
			},
		}},
	}}
	item, err := RegistryItemFromEntry(npmEntry)
	if err != nil {
		t.Fatalf("npm entry: %v", err)
	}
	if item.Server.Transport != config.MCPTransportStdio || item.Server.Command != "npx" {
		t.Errorf("npm server = %+v", item.Server)
	}
	if got := item.Server.Args; len(got) != 2 || got[0] != "-y" || got[1] != "@owner/notes-mcp@1.2.0" {
		t.Errorf("npm args = %v", got)
	}
	if item.Server.ID != "notes-mcp" {
		t.Errorf("id = %q", item.Server.ID)
	}
	if item.Server.RegistryName != npmEntry.Server.Name || item.Server.Publisher != "owner" {
		t.Errorf("registry metadata = %+v", item.Server)
	}
	if len(item.RequiredEnv) != 1 || item.RequiredEnv[0].Name != "NOTES_TOKEN" {
		t.Errorf("required env = %+v", item.RequiredEnv)
	}

	pypiEntry := RegistryEntry{Server: RegistryServer{
		Name:     "io.github.owner/py-mcp",
		Packages: []RegistryPackage{{RegistryType: "pypi", Identifier: "py-mcp"}},
	}}
	item, err = RegistryItemFromEntry(pypiEntry)
	if err != nil {
		t.Fatalf("pypi entry: %v", err)
	}
	if item.Server.Command != "uvx" || len(item.Server.Args) != 1 || item.Server.Args[0] != "py-mcp" {
		t.Errorf("pypi command = %s %v", item.Server.Command, item.Server.Args)
	}

	ociEntry := RegistryEntry{Server: RegistryServer{
		Name:     "io.github.owner/oci-mcp",
		Packages: []RegistryPackage{{RegistryType: "oci", Identifier: "ghcr.io/owner/mcp", Version: "2.0"}},
	}}
	item, err = RegistryItemFromEntry(ociEntry)
	if err != nil {
		t.Fatalf("oci entry: %v", err)
	}
	want := []string{"run", "-i", "--rm", "ghcr.io/owner/mcp:2.0"}
	if item.Server.Command != "docker" || len(item.Server.Args) != len(want) {
		t.Errorf("oci command = %s %v", item.Server.Command, item.Server.Args)
	}

	// A local package wins over a remote, matching the registry's own priority.
	mixed := RegistryEntry{Server: RegistryServer{
		Name:     "io.github.owner/mixed",
		Packages: []RegistryPackage{{RegistryType: "npm", Identifier: "mixed-mcp"}},
		Remotes:  []RegistryTransport{{Type: "streamable-http", URL: "https://mixed.example/mcp"}},
	}}
	item, err = RegistryItemFromEntry(mixed)
	if err != nil {
		t.Fatalf("mixed entry: %v", err)
	}
	if item.Server.Transport != config.MCPTransportStdio {
		t.Errorf("mixed transport = %q, want stdio", item.Server.Transport)
	}

	remoteOnly := RegistryEntry{Server: RegistryServer{
		Name:    "io.github.owner/remote",
		Remotes: []RegistryTransport{{Type: "sse", URL: "https://remote.example/sse"}},
	}}
	item, err = RegistryItemFromEntry(remoteOnly)
	if err != nil {
		t.Fatalf("remote entry: %v", err)
	}
	if item.Server.Transport != config.MCPTransportSSE || item.Server.URL != "https://remote.example/sse" {
		t.Errorf("remote server = %+v", item.Server)
	}

	if _, err := RegistryItemFromEntry(RegistryEntry{Server: RegistryServer{Name: "io.github.owner/empty"}}); err == nil {
		t.Error("an entry with no packages or remotes should fail")
	}
}
