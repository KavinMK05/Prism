// MCPB bundle handling. An .mcpb package is a prebuilt archive hosted on a
// GitHub or GitLab release, not a package manager entry, so installing one
// means downloading and unpacking it rather than asking a runtime to fetch it.
//
// The registry requires a fileSha256 for every mcpb package and says clients
// MUST validate the hash before installation, so nothing is unpacked until the
// download matches the published digest.
package mcp

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"ollama-proxy/internal/platform"
)

// maxMCPBBytes caps a bundle download so a hostile or broken host cannot fill
// the disk. Bundles ship a runtime, so the limit is generous but finite.
const maxMCPBBytes = 512 << 20

// MCPBManifest is the subset of a bundle's manifest.json Prism reads. A bundle
// declares how to launch its server under server.mcp_config.
type MCPBManifest struct {
	ManifestVersion string `json:"manifest_version,omitempty"`
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	Server          struct {
		Type       string         `json:"type,omitempty"`
		EntryPoint string         `json:"entry_point,omitempty"`
		MCPConfig  *MCPBLaunch    `json:"mcp_config,omitempty"`
		Command    string         `json:"command,omitempty"`
		Args       []string       `json:"args,omitempty"`
		Env        map[string]any `json:"env,omitempty"`
	} `json:"server,omitempty"`
}

// MCPBLaunch is the mcp_config block: the command line the bundle wants run.
type MCPBLaunch struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPBInstall is the result of unpacking a bundle.
type MCPBInstall struct {
	Dir     string
	Command string
	Args    []string
	Env     map[string]string
}

// VerifySHA256 checks a file against a published hex digest. The comparison is
// case-insensitive because digests are written both ways in the wild.
func VerifySHA256(path, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return errors.New("no SHA-256 digest was published, so the download cannot be verified")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("the downloaded file does not match the published SHA-256 (expected %s, got %s)", expected, actual)
	}
	return nil
}

// InstallMCPB downloads, verifies, and unpacks an .mcpb bundle, returning the
// command line to run. The bundle must be HTTPS, the published digest must
// match, and every archive entry is confined to the destination directory.
func InstallMCPB(ctx context.Context, rawURL, expectedSHA256, destDir string) (*MCPBInstall, error) {
	return installMCPBWithClient(ctx, oauthClient(), rawURL, expectedSHA256, destDir)
}

// installMCPBWithClient carries the real work, with the HTTP client injectable
// so tests can point it at a TLS test server.
func installMCPBWithClient(ctx context.Context, client *http.Client, rawURL, expectedSHA256, destDir string) (*MCPBInstall, error) {
	if err := validateMCPBURL(rawURL); err != nil {
		return nil, err
	}
	// Refuse before the network call: a bundle with no published digest can
	// never be verified, and running it would mean executing unverified code.
	if strings.TrimSpace(expectedSHA256) == "" {
		return nil, errors.New("this MCPB package publishes no SHA-256 digest, so Prism will not install it")
	}
	if strings.TrimSpace(destDir) == "" {
		return nil, errors.New("a destination directory is required")
	}

	archivePath, err := downloadMCPB(ctx, client, rawURL, destDir)
	if err != nil {
		return nil, err
	}
	// The archive is kept out of the extraction root so the bundle's own files
	// cannot collide with it.
	defer os.Remove(archivePath)

	if err := VerifySHA256(archivePath, expectedSHA256); err != nil {
		return nil, err
	}

	root := filepath.Join(destDir, "bundle")
	if err := os.RemoveAll(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	if err := extractZip(archivePath, root); err != nil {
		return nil, err
	}

	manifestPath, manifest, err := findMCPBManifest(root)
	if err != nil {
		return nil, err
	}
	launch := launchFromManifest(manifest)
	if launch == nil || strings.TrimSpace(launch.Command) == "" {
		return nil, fmt.Errorf("the bundle at %s does not declare a launch command", manifestPath)
	}

	install := &MCPBInstall{
		Dir:     filepath.Dir(manifestPath),
		Command: resolveBundlePath(launch.Command, filepath.Dir(manifestPath)),
		Args:    resolveBundleArgs(launch.Args, filepath.Dir(manifestPath)),
		Env:     launch.Env,
	}
	return install, nil
}

// validateMCPBURL keeps the download to HTTPS, matching the rule the rest of
// the MCP code applies to remote endpoints.
func validateMCPBURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid MCPB artifact URL %q", raw)
	}
	if u.Scheme != "https" {
		return errors.New("an MCPB artifact must be fetched over https")
	}
	return nil
}

// downloadMCPB fetches the artifact into destDir and returns its path.
func downloadMCPB(ctx context.Context, client *http.Client, rawURL, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if client == nil {
		client = oauthClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not download the bundle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the bundle host returned HTTP %d", resp.StatusCode)
	}

	f, err := os.CreateTemp(destDir, "bundle-*.mcpb")
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read one byte past the cap so an oversized body is detected rather than
	// silently truncated.
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxMCPBBytes+1))
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if written > maxMCPBBytes {
		os.Remove(f.Name())
		return "", fmt.Errorf("the bundle exceeds the %d MiB limit", maxMCPBBytes>>20)
	}
	return f.Name(), nil
}

// extractZip unpacks a zip archive into root, rejecting any entry that would
// escape it. Without that check an archive containing "../../x" could write
// anywhere the user can.
func extractZip(archivePath, root string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("the bundle is not a readable archive: %w", err)
	}
	defer r.Close()

	cleanRoot := filepath.Clean(root)
	for _, entry := range r.File {
		target, err := zipEntryPath(cleanRoot, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := writeZipEntry(entry, target); err != nil {
			return err
		}
	}
	return nil
}

// zipEntryPath resolves an archive entry inside root, or fails when it would
// land outside.
func zipEntryPath(root, name string) (string, error) {
	// A backslash is a path separator on Windows but not in the zip format, so
	// normalizing it first prevents a "..\\.." entry from slipping through.
	normalized := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("the bundle contains an absolute path %q", name)
	}
	target := filepath.Join(root, filepath.FromSlash(normalized))
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the bundle contains a path that escapes the install directory: %q", name)
	}
	return target, nil
}

func writeZipEntry(entry *zip.File, target string) error {
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// Archives carry a mode; keep the executable bit but never widen beyond
	// what the archive asked for.
	mode := entry.Mode()
	if mode == 0 {
		mode = 0644
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	// Cap each entry too, so a zip bomb cannot exhaust the disk.
	limit := int64(maxMCPBBytes)
	if entry.UncompressedSize64 > 0 && int64(entry.UncompressedSize64) > limit {
		return fmt.Errorf("the bundle entry %q is implausibly large", entry.Name)
	}
	if _, err := io.Copy(out, io.LimitReader(rc, limit)); err != nil {
		return err
	}
	return nil
}

// findMCPBManifest locates manifest.json inside the unpacked bundle, looking in
// the archive root and one level down, since bundles often nest a single
// top-level directory.
func findMCPBManifest(root string) (string, *MCPBManifest, error) {
	var candidates []string
	direct := filepath.Join(root, "manifest.json")
	if _, err := os.Stat(direct); err == nil {
		candidates = append(candidates, direct)
	}
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			nested := filepath.Join(root, e.Name(), "manifest.json")
			if _, err := os.Stat(nested); err == nil {
				candidates = append(candidates, nested)
			}
		}
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m MCPBManifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		return path, &m, nil
	}
	return "", nil, errors.New("the bundle contains no manifest.json")
}

// launchFromManifest picks the launch description out of a bundle manifest,
// preferring the explicit mcp_config block.
func launchFromManifest(m *MCPBManifest) *MCPBLaunch {
	if m == nil {
		return nil
	}
	if m.Server.MCPConfig != nil && strings.TrimSpace(m.Server.MCPConfig.Command) != "" {
		return m.Server.MCPConfig
	}
	if strings.TrimSpace(m.Server.Command) != "" {
		return &MCPBLaunch{
			Command: m.Server.Command,
			Args:    m.Server.Args,
			Env:     stringifyEnv(m.Server.Env),
		}
	}
	return nil
}

// stringifyEnv coerces a manifest env block, which may hold non-string values,
// into the string map the server config expects.
func stringifyEnv(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprint(t)
		}
	}
	return out
}

// resolveBundlePath rewrites a command that names a file inside the bundle into
// an absolute path, so a relative entry point still launches after install.
func resolveBundlePath(command, dir string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return command
	}
	if strings.HasPrefix(command, "./") || strings.HasPrefix(command, "../") {
		abs := filepath.Join(dir, filepath.FromSlash(command))
		return filepath.Clean(abs)
	}
	return command
}

// resolveBundleArgs applies the same inside-the-bundle rewrite to arguments
// that are clearly file paths.
func resolveBundleArgs(args []string, dir string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "./") || strings.HasPrefix(a, "../") {
			out = append(out, filepath.Clean(filepath.Join(dir, filepath.FromSlash(a))))
			continue
		}
		out = append(out, a)
	}
	return out
}

// MCPBInstallDir is where a bundle is unpacked: a Prism-owned directory that
// the import can safely replace.
func MCPBInstallDir(slug string) string {
	return filepath.Join(platform.ConfigDir(), "mcp", "bundles", slug)
}
