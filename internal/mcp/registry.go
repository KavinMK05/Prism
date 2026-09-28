package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ollama-proxy/internal/config"
)

// RegistryBaseURL is the official MCP registry. The API is public and
// read-only; Prism searches it so users never have to hand-write a server.json.
// Any other registry that implements the same OpenAPI shape is searched by
// RegistryClient against its own base URL.
const RegistryBaseURL = config.MCPRegistryOfficialURL

// registryHTTPTimeout bounds one registry request. The catalog sync makes many
// requests, so it uses a shorter timeout than an interactive search would
// tolerate failing.
const registryHTTPTimeout = 30 * time.Second

// RegistryClient talks to one registry. Every source Prism supports speaks the
// official registry's OpenAPI shape, so a private or org catalog is a base URL
// plus an optional auth header rather than a bespoke client.
type RegistryClient struct {
	BaseURL    string
	AuthHeader string
	HTTP       *http.Client
}

// NewRegistryClient builds a client for one source. An empty base URL falls
// back to the official registry.
func NewRegistryClient(source *config.MCPRegistrySource) *RegistryClient {
	c := &RegistryClient{BaseURL: RegistryBaseURL, HTTP: &http.Client{Timeout: registryHTTPTimeout}}
	if source != nil {
		if strings.TrimSpace(source.BaseURL) != "" {
			c.BaseURL = strings.TrimRight(strings.TrimSpace(source.BaseURL), "/")
		}
		c.AuthHeader = strings.TrimSpace(source.AuthHeader)
	}
	return c
}

// do issues one GET with the source's auth header applied.
func (c *RegistryClient) do(ctx context.Context, endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if name, value, ok := strings.Cut(c.AuthHeader, ":"); ok && strings.TrimSpace(name) != "" && strings.TrimSpace(value) != "" {
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: registryHTTPTimeout}
	}
	return client.Do(req)
}

// RegistryArgument is one entry of runtimeArguments/packageArguments.
type RegistryArgument struct {
	Type       string `json:"type,omitempty"`
	Name       string `json:"name,omitempty"`
	Value      string `json:"value,omitempty"`
	Default    string `json:"default,omitempty"`
	ValueHint  string `json:"valueHint,omitempty"`
	IsRequired bool   `json:"isRequired,omitempty"`
	IsSecret   bool   `json:"isSecret,omitempty"`
	IsRepeated bool   `json:"isRepeated,omitempty"`
}

// RegistryKeyValue is an environment variable or header declaration.
type RegistryKeyValue struct {
	Name       string `json:"name"`
	Value      string `json:"value,omitempty"`
	Default    string `json:"default,omitempty"`
	Format     string `json:"format,omitempty"`
	IsRequired bool   `json:"isRequired,omitempty"`
	IsSecret   bool   `json:"isSecret,omitempty"`
}

// RegistryTransport describes how a remote server is reached.
type RegistryTransport struct {
	Type    string             `json:"type,omitempty"`
	URL     string             `json:"url,omitempty"`
	Headers []RegistryKeyValue `json:"headers,omitempty"`
}

// RegistryPackage is one installable artifact of a server.
type RegistryPackage struct {
	RegistryType         string             `json:"registryType,omitempty"`
	Identifier           string             `json:"identifier,omitempty"`
	Version              string             `json:"version,omitempty"`
	RegistryBaseURL      string             `json:"registryBaseUrl,omitempty"`
	RuntimeHint          string             `json:"runtimeHint,omitempty"`
	RuntimeArguments     []RegistryArgument `json:"runtimeArguments,omitempty"`
	PackageArguments     []RegistryArgument `json:"packageArguments,omitempty"`
	EnvironmentVariables []RegistryKeyValue `json:"environmentVariables,omitempty"`
	Transport            *RegistryTransport `json:"transport,omitempty"`
	FileSha256           string             `json:"fileSha256,omitempty"`
}

// RegistryServer is the server.json document the registry publishes.
type RegistryServer struct {
	Schema      string              `json:"$schema,omitempty"`
	Name        string              `json:"name"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Version     string              `json:"version,omitempty"`
	Status      string              `json:"status,omitempty"`
	Packages    []RegistryPackage   `json:"packages,omitempty"`
	Remotes     []RegistryTransport `json:"remotes,omitempty"`
	Repository  *RegistryRepository `json:"repository,omitempty"`
	WebsiteURL  string              `json:"websiteUrl,omitempty"`
}

// RegistryRepository is the source repository of a server.
type RegistryRepository struct {
	URL    string `json:"url,omitempty"`
	Source string `json:"source,omitempty"`
}

// RegistryEntry is one item of a registry search result.
type RegistryEntry struct {
	Server RegistryServer         `json:"server"`
	Meta   map[string]interface{} `json:"_meta,omitempty"`
}

type registrySearchResponse struct {
	Servers  []RegistryEntry `json:"servers"`
	Metadata struct {
		Count      int    `json:"count"`
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// RegistryServerName extracts the reverse-DNS name from an entry.
func (e RegistryEntry) Name() string { return e.Server.Name }

// RegistrySearchItem is the shape the admin UI consumes: a summary plus a
// ready-to-edit server config.
type RegistrySearchItem struct {
	Name        string                  `json:"name"`
	Title       string                  `json:"title,omitempty"`
	Description string                  `json:"description,omitempty"`
	Version     string                  `json:"version,omitempty"`
	Repository  string                  `json:"repository,omitempty"`
	Transport   string                  `json:"transport"`
	Preview     string                  `json:"preview"`
	RequiredEnv []RegistryKeyValue      `json:"required_env,omitempty"`
	OptionalEnv []RegistryKeyValue      `json:"optional_env,omitempty"`
	Server      *config.MCPServerConfig `json:"server"`

	// Marketplace fields. They describe provenance and trust so the UI can
	// show where a server came from before the user installs it.
	SourceID       string `json:"source_id,omitempty"`
	SourceName     string `json:"source_name,omitempty"`
	Publisher      string `json:"publisher,omitempty"`
	NamespaceMatch bool   `json:"namespace_match,omitempty"`
	Status         string `json:"status,omitempty"`
	Trusted        bool   `json:"trusted,omitempty"`
	Deleted        bool   `json:"deleted,omitempty"`
	PublisherMeta  string `json:"publisher_meta,omitempty"`
}

// ApplySource records which marketplace a search result came from.
func (i *RegistrySearchItem) ApplySource(src *config.MCPRegistrySource) {
	if src == nil {
		return
	}
	i.SourceID = src.ID
	i.SourceName = src.Name
}

// SearchRegistry queries the official registry. An empty query returns the
// most recently updated servers. Kept as the single-source entry point for
// callers that do not care which catalog answered.
func SearchRegistry(ctx context.Context, query string, limit int) ([]RegistrySearchItem, error) {
	return NewRegistryClient(nil).Search(ctx, query, limit)
}

// Search queries this source. An empty query returns the most recently updated
// servers.
func (c *RegistryClient) Search(ctx context.Context, query string, limit int) ([]RegistrySearchItem, error) {
	return c.SearchVersion(ctx, query, limit, "latest")
}

// SearchVersion queries this source, pinning the requested version. Pass
// "latest" (or "") to let the registry pick. A concrete version is only
// meaningful when searching for one server by name, since the registry keeps
// one row per version.
func (c *RegistryClient) SearchVersion(ctx context.Context, query string, limit int, version string) ([]RegistrySearchItem, error) {
	entries, _, err := c.ListServers(ctx, ListParams{Query: query, Limit: limit, Version: version})
	if err != nil {
		return nil, err
	}
	items := make([]RegistrySearchItem, 0, len(entries))
	for _, entry := range entries {
		item, err := RegistryItemFromEntry(entry)
		if err != nil {
			continue // servers Prism cannot run yet (unsupported package types)
		}
		items = append(items, item)
	}
	return items, nil
}

// ListParams are the filters the registry API accepts.
type ListParams struct {
	Query        string
	Limit        int
	Version      string
	UpdatedSince string // RFC 3339; used for incremental catalog sync
	Cursor       string
}

// ListServers performs one page request against this source and returns the
// raw entries plus the cursor for the next page.
func (c *RegistryClient) ListServers(ctx context.Context, p ListParams) ([]RegistryEntry, string, error) {
	limit := p.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	version := strings.TrimSpace(p.Version)
	if version == "" {
		version = "latest"
	}
	q.Set("version", version)
	if strings.TrimSpace(p.Query) != "" {
		q.Set("search", strings.TrimSpace(p.Query))
	}
	if strings.TrimSpace(p.UpdatedSince) != "" {
		q.Set("updated_since", strings.TrimSpace(p.UpdatedSince))
	}
	if strings.TrimSpace(p.Cursor) != "" {
		q.Set("cursor", strings.TrimSpace(p.Cursor))
	}
	endpoint := c.BaseURL + "/v0.1/servers?" + q.Encode()

	resp, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, "", fmt.Errorf("%s is unreachable: %w", c.describe(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, "", fmt.Errorf("%s returned HTTP %d: %s", c.describe(), resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var parsed registrySearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&parsed); err != nil {
		return nil, "", fmt.Errorf("invalid registry response from %s: %w", c.describe(), err)
	}
	return parsed.Servers, parsed.Metadata.NextCursor, nil
}

// ListVersions returns every published version of one server, newest first as
// the registry orders them.
func (c *RegistryClient) ListVersions(ctx context.Context, name string) ([]string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a server name is required")
	}
	// The registry requires the name encoded as one path segment, so the
	// namespace separator must be escaped too: url.PathEscape alone leaves "/"
	// intact and would produce a path the API does not route.
	endpoint := c.BaseURL + "/v0.1/servers/" + escapePathSegment(name) + "/versions"
	resp, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("%s is unreachable: %w", c.describe(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, fmt.Errorf("%s returned HTTP %d: %s", c.describe(), resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var parsed struct {
		Servers []RegistryEntry `json:"servers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("invalid versions response from %s: %w", c.describe(), err)
	}
	versions := make([]string, 0, len(parsed.Servers))
	for _, e := range parsed.Servers {
		if e.Server.Version != "" {
			versions = append(versions, e.Server.Version)
		}
	}
	return versions, nil
}

// escapePathSegment percent-encodes a value so it stays a single URL path
// segment. url.PathEscape deliberately leaves "/" alone, which is wrong for
// registry names because the namespace separator must be encoded.
func escapePathSegment(v string) string {
	return strings.ReplaceAll(url.PathEscape(v), "/", "%2F")
}

// describe names this source in errors so a failed search says which registry
// was at fault.
func (c *RegistryClient) describe() string {
	if c.BaseURL == "" {
		return "the MCP registry"
	}
	return c.BaseURL
}

// RegistryItemFromEntry converts a registry entry into a server config Prism
// can save and run.
func RegistryItemFromEntry(entry RegistryEntry) (RegistrySearchItem, error) {
	server, env, err := RegistryServerConfig(entry)
	if err != nil {
		return RegistrySearchItem{}, err
	}
	item := RegistrySearchItem{
		Name:        entry.Server.Name,
		Title:       entry.Server.Title,
		Description: entry.Server.Description,
		Version:     entry.Server.Version,
		Transport:   server.Transport,
		Preview:     serverPreview(server),
		Server:      server,
	}
	if entry.Server.Repository != nil {
		item.Repository = entry.Server.Repository.URL
	}
	item.Publisher = item.Server.Publisher
	item.NamespaceMatch = namespaceMatchesRepository(entry.Server.Name, item.Repository)
	item.Status = entry.Server.Status
	item.Deleted = entry.Server.Status == "deleted"
	// A server is "trusted" when its reverse-DNS namespace and its source
	// repository agree. The registry enforces the namespace half
	// (io.github.<owner>/... requires proving ownership of that GitHub
	// account); this checks the other half, that the repo it points at is
	// the same owner, so a copied namespace cannot borrow someone's repo.
	item.Trusted = item.NamespaceMatch
	if meta := publisherMeta(entry.Meta); meta != "" {
		item.PublisherMeta = meta
	}
	for _, v := range env {
		if v.IsRequired {
			item.RequiredEnv = append(item.RequiredEnv, v)
		} else {
			item.OptionalEnv = append(item.OptionalEnv, v)
		}
	}
	return item, nil
}

// RegistryServerConfig maps a server.json into an MCPServerConfig. Local
// (stdio) packages win over remotes, because that is what the registry treats
// as the primary distribution.
func RegistryServerConfig(entry RegistryEntry) (*config.MCPServerConfig, []RegistryKeyValue, error) {
	s := entry.Server
	display := s.Title
	if display == "" {
		display = s.Name
	}
	base := &config.MCPServerConfig{
		ID:              config.MCPIDFromName(lastSegment(s.Name)),
		Name:            display,
		Source:          config.MCPSourceRegistry,
		RegistryName:    s.Name,
		RegistryVersion: s.Version,
		Enabled:         true,
		Verified:        namespaceMatchesRepository(s.Name, repositoryURL(s)),
	}
	if s.Repository != nil {
		base.Repository = s.Repository.URL
	}
	if parts := strings.Split(s.Name, "/"); len(parts) > 1 {
		base.Publisher = strings.TrimPrefix(parts[0], "io.github.")
	}

	for _, pkg := range s.Packages {
		command, args, err := packageCommand(pkg)
		if err != nil {
			continue
		}
		base.Transport = config.MCPTransportStdio
		base.Command = command
		base.Args = args
		if len(pkg.EnvironmentVariables) > 0 {
			env := map[string]string{}
			secrets := make([]string, 0, len(pkg.EnvironmentVariables))
			for _, v := range pkg.EnvironmentVariables {
				value := v.Value
				if value == "" {
					value = v.Default
				}
				env[v.Name] = value
				// The registry tells us which values are credentials; keeping
				// that list means the UI masks the right fields later instead
				// of guessing from the variable name.
				if v.IsSecret {
					secrets = append(secrets, v.Name)
				}
			}
			base.Env = env
			sortStrings(secrets)
			base.SecretEnv = secrets
		}
		base.IntegritySHA256 = pkg.FileSha256
		return base, pkg.EnvironmentVariables, nil
	}

	for _, remote := range s.Remotes {
		if remote.URL == "" {
			continue
		}
		transport := config.MCPTransportHTTP
		switch remote.Type {
		case "sse":
			transport = config.MCPTransportSSE
		case "streamable-http", "http", "":
			transport = config.MCPTransportHTTP
		}
		base.Transport = transport
		base.URL = remote.URL
		if len(remote.Headers) > 0 {
			headers := map[string]string{}
			secrets := make([]string, 0, len(remote.Headers))
			for _, h := range remote.Headers {
				value := h.Value
				if value == "" {
					value = h.Default
				}
				headers[h.Name] = value
				if h.IsSecret {
					secrets = append(secrets, h.Name)
				}
			}
			base.Headers = headers
			sortStrings(secrets)
			base.SecretEnv = secrets
			if len(headers) > 0 {
				base.AuthMode = config.MCPAuthStatic
			}
		}
		return base, remote.Headers, nil
	}
	return nil, nil, errors.New("this registry entry has no package or remote Prism can use")
}

// repositoryURL returns the repository URL or an empty string.
func repositoryURL(s RegistryServer) string {
	if s.Repository == nil {
		return ""
	}
	return s.Repository.URL
}

// packageCommand turns a registry package into the command line Prism runs.
//
// Each registry type has its own runtime model, so the mapping is not uniform:
//
//   - npm    -> npx -y pkg@version
//   - pypi   -> uvx pkg@version
//   - nuget  -> dnx pkg@version
//   - cargo  -> the crate is installed once and invoked by binary name; there
//     is no per-invocation runner, so the command is the crate itself
//   - oci    -> docker run -i --rm image:tag
//   - mcpb   -> a downloaded .mcpb bundle, executed as a local command
//
// A package type in the default branch still works when the manifest supplies
// an explicit runtimeHint, which keeps newer types usable without a code change.
func packageCommand(pkg RegistryPackage) (string, []string, error) {
	versionSuffix := ""
	if pkg.Version != "" && pkg.Version != "latest" {
		versionSuffix = "@" + pkg.Version
	}

	var command string
	var args []string
	switch pkg.RegistryType {
	case "npm":
		command = firstNonEmpty(pkg.RuntimeHint, "npx")
		if command == "npx" {
			args = append(args, "-y")
		}
		args = append(args, pkg.Identifier+versionSuffix)
	case "pypi":
		command = firstNonEmpty(pkg.RuntimeHint, "uvx")
		if command == "uvx" {
			args = append(args, pkg.Identifier+versionSuffix)
		} else {
			args = append(args, pkg.Identifier)
		}
	case "nuget":
		// dnx is the .NET 10 equivalent of npx and is the documented runtime
		// for NuGet-distributed MCP servers.
		command = firstNonEmpty(pkg.RuntimeHint, "dnx")
		if command == "dnx" && pkg.Version != "" && pkg.Version != "latest" {
			args = append(args, pkg.Identifier+"@"+pkg.Version)
		} else {
			args = append(args, pkg.Identifier)
		}
	case "cargo":
		// cargo has no npx-style runner: `cargo install` puts the binary on
		// PATH and clients invoke it by name. Prism runs the crate name, which
		// is what the registry and the package-types guide both assume.
		if pkg.RuntimeHint != "" {
			command = pkg.RuntimeHint
			args = append(args, pkg.Identifier)
		} else {
			command = pkg.Identifier
		}
	case "mcpb":
		// A .mcpb is a downloaded bundle, so installing it means running the
		// artifact. The identifier is a URL; the caller is responsible for
		// verifying fileSha256 first (see VerifyMCPBPackage).
		if pkg.Identifier == "" {
			return "", nil, errors.New("an mcpb package needs an artifact URL")
		}
		command = firstNonEmpty(pkg.RuntimeHint, "mcpb")
		args = append(args, pkg.Identifier)
	case "oci":
		command = firstNonEmpty(pkg.RuntimeHint, "docker")
		args = append(args, "run", "-i", "--rm")
		image := pkg.Identifier
		if pkg.Version != "" && pkg.Version != "latest" {
			image += ":" + pkg.Version
		}
		args = append(args, image)
	default:
		if pkg.RuntimeHint == "" || pkg.Identifier == "" {
			return "", nil, fmt.Errorf("unsupported registry type %q", pkg.RegistryType)
		}
		command = pkg.RuntimeHint
		args = append(args, pkg.Identifier)
	}

	args = append(args, argumentValues(pkg.RuntimeArguments)...)
	args = append(args, argumentValues(pkg.PackageArguments)...)
	if command == "" || pkg.Identifier == "" {
		return "", nil, errors.New("incomplete package definition")
	}
	return command, args, nil
}

// argumentValues flattens registry arguments into argv.
func argumentValues(list []RegistryArgument) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		value := a.Value
		if value == "" {
			value = a.Default
		}
		if value == "" {
			continue
		}
		if a.Type == "named" && a.Name != "" {
			out = append(out, "--"+strings.TrimPrefix(a.Name, "--"), value)
			continue
		}
		out = append(out, value)
	}
	return out
}

func serverPreview(s *config.MCPServerConfig) string {
	if s == nil {
		return ""
	}
	if s.Transport == config.MCPTransportStdio {
		return strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
	}
	return s.URL
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// namespaceMatchesRepository reports whether a server's reverse-DNS namespace
// and its repository URL name the same owner. Names look like
// "io.github.alice/weather" (or "com.example/weather" for a DNS-verified
// namespace); repositories look like "https://github.com/alice/weather".
//
// A server with no repository cannot be cross-checked, so it is not counted as
// matching.
func namespaceMatchesRepository(name, repoURL string) bool {
	ns := name
	if i := strings.Index(ns, "/"); i >= 0 {
		ns = ns[:i]
	}
	owner := ""
	switch {
	case strings.HasPrefix(ns, "io.github."):
		owner = strings.TrimPrefix(ns, "io.github.")
	case strings.HasPrefix(ns, "io.gitlab."):
		owner = strings.TrimPrefix(ns, "io.gitlab.")
	}
	if owner == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Host == "" {
		return false
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) < 2 {
		return false
	}
	return strings.EqualFold(segments[0], owner)
}

// publisherMeta extracts the publisher-provided metadata block the registry
// reserves for build and provenance information. It is surfaced read-only, so
// the value is returned as raw JSON for display rather than parsed.
func publisherMeta(meta map[string]interface{}) string {
	if len(meta) == 0 {
		return ""
	}
	v, ok := meta["io.modelcontextprotocol.registry/publisher-provided"]
	if !ok {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" || string(raw) == "{}" {
		return ""
	}
	if len(raw) > 2048 {
		raw = raw[:2048]
	}
	return string(raw)
}
