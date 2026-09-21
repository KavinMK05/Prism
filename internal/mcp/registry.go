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

	"ollama-proxy/internal/config"
)

// RegistryBaseURL is the official MCP registry. The API is public and
// read-only; Prism searches it so users never have to hand-write a server.json.
const RegistryBaseURL = "https://registry.modelcontextprotocol.io"

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
	Packages    []RegistryPackage   `json:"packages,omitempty"`
	Remotes     []RegistryTransport `json:"remotes,omitempty"`
	Repository  *RegistryRepository `json:"repository,omitempty"`
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
}

// SearchRegistry queries the official registry. An empty query returns the
// most recently updated servers.
func SearchRegistry(ctx context.Context, query string, limit int) ([]RegistrySearchItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("version", "latest")
	if strings.TrimSpace(query) != "" {
		q.Set("search", strings.TrimSpace(query))
	}
	endpoint := RegistryBaseURL + "/v0.1/servers?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("the MCP registry is unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, fmt.Errorf("the MCP registry returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var parsed registrySearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("invalid registry response: %w", err)
	}
	items := make([]RegistrySearchItem, 0, len(parsed.Servers))
	for _, entry := range parsed.Servers {
		item, err := RegistryItemFromEntry(entry)
		if err != nil {
			continue // servers Prism cannot run yet (unsupported package types)
		}
		items = append(items, item)
	}
	return items, nil
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
		ID:           config.MCPIDFromName(lastSegment(s.Name)),
		Name:         display,
		Source:       config.MCPSourceRegistry,
		RegistryName: s.Name,
		Enabled:      true,
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
			for _, v := range pkg.EnvironmentVariables {
				value := v.Value
				if value == "" {
					value = v.Default
				}
				env[v.Name] = value
			}
			base.Env = env
		}
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
			for _, h := range remote.Headers {
				value := h.Value
				if value == "" {
					value = h.Default
				}
				headers[h.Name] = value
			}
			base.Headers = headers
			if len(headers) > 0 {
				base.AuthMode = config.MCPAuthStatic
			}
		}
		return base, remote.Headers, nil
	}
	return nil, nil, errors.New("this registry entry has no package or remote Prism can use")
}

// packageCommand turns a registry package into the command line Prism runs.
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
