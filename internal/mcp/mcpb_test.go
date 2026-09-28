package mcp

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageCommandRegistryTypes(t *testing.T) {
	cases := []struct {
		name    string
		pkg     RegistryPackage
		command string
		args    []string
		wantErr bool
	}{
		{
			name:    "npm pins the version",
			pkg:     RegistryPackage{RegistryType: "npm", Identifier: "@alice/weather", Version: "1.2.3"},
			command: "npx",
			args:    []string{"-y", "@alice/weather@1.2.3"},
		},
		{
			name:    "pypi uses uvx",
			pkg:     RegistryPackage{RegistryType: "pypi", Identifier: "weather-mcp", Version: "1.0.0"},
			command: "uvx",
			args:    []string{"weather-mcp@1.0.0"},
		},
		{
			// NuGet's runner is dnx, and the version is pinned with @.
			name:    "nuget uses dnx",
			pkg:     RegistryPackage{RegistryType: "nuget", Identifier: "Alice.WeatherMcp", Version: "2.0.0"},
			command: "dnx",
			args:    []string{"Alice.WeatherMcp@2.0.0"},
		},
		{
			// cargo has no per-invocation runner: the crate name is the command.
			name:    "cargo invokes the installed binary",
			pkg:     RegistryPackage{RegistryType: "cargo", Identifier: "widget-mcp", Version: "0.3.0"},
			command: "widget-mcp",
			args:    nil,
		},
		{
			name:    "oci runs the image",
			pkg:     RegistryPackage{RegistryType: "oci", Identifier: "docker.io/alice/weather", Version: "1.0.0"},
			command: "docker",
			args:    []string{"run", "-i", "--rm", "docker.io/alice/weather:1.0.0"},
		},
		{
			name: "arguments are flattened after the package",
			pkg: RegistryPackage{
				RegistryType:     "npm",
				Identifier:       "@alice/weather",
				Version:          "1.0.0",
				PackageArguments: []RegistryArgument{{Type: "positional", Value: "extra"}},
			},
			command: "npx",
			args:    []string{"-y", "@alice/weather@1.0.0", "extra"},
		},
		{
			name:    "an unknown type without a hint is rejected",
			pkg:     RegistryPackage{RegistryType: "mystery", Identifier: "thing"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		command, args, err := packageCommand(tc.pkg)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if command != tc.command {
			t.Errorf("%s: command = %q, want %q", tc.name, command, tc.command)
		}
		if strings.Join(args, " ") != strings.Join(tc.args, " ") {
			t.Errorf("%s: args = %v, want %v", tc.name, args, tc.args)
		}
	}
}

func TestPackageCommandCargoHonoursRuntimeHint(t *testing.T) {
	command, args, err := packageCommand(RegistryPackage{
		RegistryType: "cargo",
		Identifier:   "widget-mcp",
		RuntimeHint:  "cargo",
	})
	if err != nil {
		t.Fatalf("packageCommand: %v", err)
	}
	if command != "cargo" || len(args) != 1 || args[0] != "widget-mcp" {
		t.Errorf("cargo with a runtime hint = %q %v", command, args)
	}
}

func TestVerifySHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.mcpb")
	content := []byte("bundle contents")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	if err := VerifySHA256(path, digest); err != nil {
		t.Errorf("a matching digest should verify: %v", err)
	}
	// Digests are written in both cases in the wild.
	if err := VerifySHA256(path, strings.ToUpper(digest)); err != nil {
		t.Errorf("the comparison should be case-insensitive: %v", err)
	}
	if err := VerifySHA256(path, strings.Repeat("0", 64)); err == nil {
		t.Error("a mismatched digest must fail")
	}
	if err := VerifySHA256(path, ""); err == nil {
		t.Error("a missing digest must fail")
	}
	if err := VerifySHA256(filepath.Join(dir, "absent"), digest); err == nil {
		t.Error("a missing file must fail")
	}
}

// makeBundle writes a zip archive with the given entries and returns its path.
func makeBundle(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.mcpb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractZipConfinesEntries(t *testing.T) {
	root := t.TempDir()

	ok := makeBundle(t, map[string]string{
		"manifest.json": `{"manifest_version":"0.3","name":"demo","server":{"mcp_config":{"command":"./run.sh"}}}`,
		"run.sh":        "#!/bin/sh\n",
	})
	if err := extractZip(ok, root); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "run.sh")); err != nil {
		t.Errorf("expected the entry to be extracted: %v", err)
	}

	// A "../" entry must be refused, or an archive could write outside root.
	escape := makeBundle(t, map[string]string{"../evil.txt": "pwned"})
	if err := extractZip(escape, t.TempDir()); err == nil {
		t.Error("a traversal entry must be rejected")
	}

	// The same via a Windows-style separator, which zip does not treat as a
	// separator but the filesystem does.
	escapeWin := makeBundle(t, map[string]string{`..\evil.txt`: "pwned"})
	if err := extractZip(escapeWin, t.TempDir()); err == nil {
		t.Error("a backslash traversal entry must be rejected")
	}

	// An absolute path must be refused too.
	absolute := makeBundle(t, map[string]string{"/etc/evil.txt": "pwned"})
	if err := extractZip(absolute, t.TempDir()); err == nil {
		t.Error("an absolute entry must be rejected")
	}
}

func TestFindMCPBManifestAndLaunch(t *testing.T) {
	// At the archive root.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"),
		[]byte(`{"name":"demo","server":{"mcp_config":{"command":"./run.sh","args":["./x.js"],"env":{"K":"v"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	path, manifest, err := findMCPBManifest(root)
	if err != nil {
		t.Fatalf("findMCPBManifest: %v", err)
	}
	if filepath.Base(path) != "manifest.json" {
		t.Errorf("path = %q", path)
	}
	launch := launchFromManifest(manifest)
	if launch == nil || launch.Command != "./run.sh" {
		t.Fatalf("launch = %+v", launch)
	}
	// Relative paths are rewritten to absolute ones inside the bundle.
	if resolved := resolveBundlePath(launch.Command, root); !filepath.IsAbs(resolved) {
		t.Errorf("command was not resolved: %q", resolved)
	}
	args := resolveBundleArgs(launch.Args, root)
	if len(args) != 1 || !filepath.IsAbs(args[0]) {
		t.Errorf("args were not resolved: %v", args)
	}
	// A bare executable name is left alone so PATH lookup still works.
	if got := resolveBundlePath("node", root); got != "node" {
		t.Errorf("a PATH command should be unchanged, got %q", got)
	}

	// One directory down, as bundles often nest a single top-level folder.
	nested := t.TempDir()
	if err := os.MkdirAll(filepath.Join(nested, "inner"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "inner", "manifest.json"),
		[]byte(`{"server":{"command":"node"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := findMCPBManifest(nested); err != nil {
		t.Errorf("a nested manifest should be found: %v", err)
	}

	// No manifest at all.
	if _, _, err := findMCPBManifest(t.TempDir()); err == nil {
		t.Error("a bundle without a manifest must fail")
	}
}

func TestLaunchFromManifestFallsBackToServerBlock(t *testing.T) {
	m := &MCPBManifest{}
	m.Server.Command = "node"
	m.Server.Args = []string{"index.js"}
	m.Server.Env = map[string]any{"PORT": 8080, "NAME": "x", "EMPTY": nil}

	launch := launchFromManifest(m)
	if launch == nil || launch.Command != "node" {
		t.Fatalf("launch = %+v", launch)
	}
	// Non-string env values are coerced rather than dropped.
	if launch.Env["PORT"] != "8080" || launch.Env["NAME"] != "x" || launch.Env["EMPTY"] != "" {
		t.Errorf("env coercion failed: %+v", launch.Env)
	}
	if launchFromManifest(&MCPBManifest{}) != nil {
		t.Error("a manifest with no launch info should return nil")
	}
	if launchFromManifest(nil) != nil {
		t.Error("nil manifest should return nil")
	}
}

func TestInstallMCPBRequiresHTTPS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cases := []string{
		"http://example.com/x.mcpb",
		"ftp://example.com/x.mcpb",
		"://nope",
	}
	for _, raw := range cases {
		if _, err := InstallMCPB(ctx, raw, strings.Repeat("a", 64), dir); err == nil {
			t.Errorf("%q should be rejected", raw)
		}
	}
}

func TestInstallMCPBRequiresDigest(t *testing.T) {
	// The digest check happens before any network call, so an https URL with no
	// digest must fail without a request being made.
	dir := t.TempDir()
	_, err := InstallMCPB(context.Background(), "https://example.invalid/x.mcpb", "", dir)
	if err == nil {
		t.Fatal("a bundle with no digest must be refused")
	}
	if !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("the error should explain the missing digest: %v", err)
	}
}

func TestInstallMCPBEndToEnd(t *testing.T) {
	bundle := makeBundle(t, map[string]string{
		"manifest.json": `{"manifest_version":"0.3","name":"demo","version":"1.0.0",
			"server":{"mcp_config":{"command":"./run.sh","args":["./server.js"]}}}`,
		"run.sh":    "#!/bin/sh\n",
		"server.js": "console.log('hi')\n",
	})
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	// A TLS server, because the installer refuses plain HTTP. Its own client
	// trusts the test certificate.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	dir := t.TempDir()
	install, err := installMCPBWithClient(context.Background(), srv.Client(), srv.URL+"/demo.mcpb", digest, dir)
	if err != nil {
		t.Fatalf("InstallMCPB: %v", err)
	}
	if !filepath.IsAbs(install.Command) {
		t.Errorf("command should be absolute: %q", install.Command)
	}
	if _, err := os.Stat(install.Command); err != nil {
		t.Errorf("the entry point should exist after install: %v", err)
	}
	if len(install.Args) != 1 || !filepath.IsAbs(install.Args[0]) {
		t.Errorf("args should be resolved inside the bundle: %v", install.Args)
	}
	// The downloaded archive is cleaned up, leaving only the unpacked bundle.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".mcpb") {
				t.Errorf("the downloaded archive was left behind: %s", e.Name())
			}
		}
	}

	// A wrong digest must abort before anything is unpacked.
	badDir := t.TempDir()
	if _, err := installMCPBWithClient(context.Background(), srv.Client(), srv.URL+"/demo.mcpb", strings.Repeat("b", 64), badDir); err == nil {
		t.Error("a mismatched digest must fail the install")
	}
	if _, err := os.Stat(filepath.Join(badDir, "bundle")); err == nil {
		t.Error("nothing should be unpacked when the digest does not match")
	}

	// A bundle with no manifest is rejected after verification.
	emptyBundle := makeBundle(t, map[string]string{"readme.txt": "nothing here"})
	emptyData, err := os.ReadFile(emptyBundle)
	if err != nil {
		t.Fatal(err)
	}
	emptySum := sha256.Sum256(emptyData)
	emptySrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(emptyData)
	}))
	defer emptySrv.Close()
	if _, err := installMCPBWithClient(context.Background(), emptySrv.Client(),
		emptySrv.URL+"/x.mcpb", hex.EncodeToString(emptySum[:]), t.TempDir()); err == nil {
		t.Error("a bundle without a manifest must be rejected")
	}
}

func TestMCPBInstallDirIsUnderConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	got := MCPBInstallDir("demo")
	if !strings.Contains(got, filepath.Join("mcp", "bundles", "demo")) {
		t.Errorf("unexpected install dir: %q", got)
	}
}
