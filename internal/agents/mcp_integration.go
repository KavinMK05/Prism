package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Prism exposes one MCP endpoint per agent (/mcp/<agent>). Each supported
// agent gets a single "prism" entry pointing at it, authenticated with Prism's
// own token; the upstream credentials stay inside Prism.
const prismMCPEntryName = "prism"

// prismMCPToken is the bearer token every agent uses to reach Prism's MCP
// gateway. It is the same key the proxy accepts on its other endpoints.
const prismMCPToken = "prism"

// prismMCPAuthorization is the header value agents send to the gateway.
const prismMCPAuthorization = "Bearer " + prismMCPToken

const (
	mcpManagedBegin = "# >>> prism mcp managed >>>"
	mcpManagedEnd   = "# <<< prism mcp managed <<<"
)

// mcpAgentSpec describes how to write Prism's MCP entry into one agent's config.
type mcpAgentSpec struct {
	path string
	// keys is the path from the file's root to the object holding the MCP
	// servers. Most agents use a single top-level "mcpServers" object; ZCode
	// nests its servers under mcp.servers.
	keys  []string
	jsonc bool
	entry func(url string) map[string]interface{}
}

// mcpSupportedAgents lists the agents whose MCP configuration Prism knows how
// to write. Codex and Grok Build keep their entries in TOML files and have
// their own writers; every other id here is handled by mcpAgentSpecFor.
var mcpSupportedAgents = []string{
	"codex",
	"grok-build",
	"claude-code",
	"factory-droid",
	"opencode",
	"zcode",
	"zed",
	"omp",
	"kimi-code",
	"pi",
	"prime-agent",
	"empryo",
}

// AgentMCPSupported reports whether Prism knows the MCP config shape of an
// agent. Agents without a documented MCP config are reported as unsupported in
// the admin UI instead of being written to speculatively.
func AgentMCPSupported(agentID string) bool {
	for _, id := range mcpSupportedAgents {
		if id == agentID {
			return true
		}
	}
	return false
}

// AgentMCPEndpointPath is the downstream path Prism serves for an agent.
func AgentMCPEndpointPath(agentID string) string {
	return "/mcp/" + agentID
}

// AgentMCPURL is the URL agents are configured with.
func AgentMCPURL(agentID string, port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, AgentMCPEndpointPath(agentID))
}

// AgentMCPConfigPath returns the config file Prism writes the MCP entry into.
func AgentMCPConfigPath(agentID string) string {
	switch agentID {
	case "codex":
		return codexDesktopConfigPath()
	case "grok-build":
		return grokBuildConfigPath()
	case "empryo":
		// Empryo keeps MCP servers beside its providers, in the same file.
		return empryoConfigPath()
	}
	if spec, ok := mcpAgentSpecFor(agentID); ok {
		return spec.path
	}
	return ""
}

func mcpAgentSpecFor(agentID string) (mcpAgentSpec, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return mcpAgentSpec{}, false
	}
	headers := func() map[string]interface{} {
		return map[string]interface{}{"Authorization": prismMCPAuthorization}
	}
	// The streamable-HTTP shape shared by Claude Code, Factory Droid, OMP and
	// Prime Agent.
	httpEntry := func(url string) map[string]interface{} {
		return map[string]interface{}{
			"type":    "http",
			"url":     url,
			"headers": headers(),
		}
	}
	switch agentID {
	case "claude-code":
		return mcpAgentSpec{
			// Claude Code keeps user-scope MCP servers in ~/.claude.json.
			path:  filepath.Join(home, ".claude.json"),
			keys:  []string{"mcpServers"},
			entry: httpEntry,
		}, true
	case "factory-droid":
		return mcpAgentSpec{
			// Droid keeps user-scope MCP servers in ~/.factory/mcp.json.
			// oauth:false keeps Droid from starting an OAuth flow for an entry
			// that authenticates with a plain header.
			path: filepath.Join(home, ".factory", "mcp.json"),
			keys: []string{"mcpServers"},
			entry: func(url string) map[string]interface{} {
				entry := httpEntry(url)
				entry["oauth"] = false
				return entry
			},
		}, true
	case "opencode":
		return mcpAgentSpec{
			path: opencodeConfigPath(),
			keys: []string{"mcp"},
			entry: func(url string) map[string]interface{} {
				return map[string]interface{}{
					"type":    "remote",
					"url":     url,
					"headers": headers(),
					"enabled": true,
				}
			},
		}, true
	case "zed":
		return mcpAgentSpec{
			path:  zedConfigPath(),
			jsonc: true,
			keys:  []string{"context_servers"},
			entry: func(url string) map[string]interface{} {
				return map[string]interface{}{
					"url":     url,
					"headers": headers(),
				}
			},
		}, true
	case "zcode":
		return mcpAgentSpec{
			// ZCode's native user-scope config is ~/.zcode/cli/config.json with
			// the servers under mcp.servers. (~/.zcode/v2/config.json is the
			// provider config Prism already writes, not the MCP one.)
			path: filepath.Join(home, ".zcode", "cli", "config.json"),
			keys: []string{"mcp", "servers"},
			entry: func(url string) map[string]interface{} {
				// No "enable" field: absent means enabled.
				return map[string]interface{}{
					"type":    "http",
					"url":     url,
					"headers": headers(),
				}
			},
		}, true
	case "omp":
		return mcpAgentSpec{
			// OMP keeps MCP servers in ~/.omp/agent/mcp.json, beside the
			// models.yml Prism writes as its provider config.
			path:  filepath.Join(home, ".omp", "agent", "mcp.json"),
			keys:  []string{"mcpServers"},
			entry: httpEntry,
		}, true
	case "kimi-code":
		return mcpAgentSpec{
			// Kimi Code reads MCP servers from mcp.json, separate from the
			// config.toml that holds providers and model aliases. A url with no
			// transport field means HTTP.
			path: kimiCodeMCPConfigPath(),
			keys: []string{"mcpServers"},
			entry: func(url string) map[string]interface{} {
				return map[string]interface{}{
					"url":     url,
					"headers": headers(),
				}
			},
		}, true
	case "pi":
		return mcpAgentSpec{
			// Pi has no built-in MCP support: this file is read by the
			// npm:pi-mcp-adapter extension, which is where Pi users get MCP.
			path: piMCPConfigPath(),
			keys: []string{"mcpServers"},
			entry: func(url string) map[string]interface{} {
				return map[string]interface{}{
					"url":     url,
					"headers": headers(),
				}
			},
		}, true
	case "prime-agent":
		return mcpAgentSpec{
			// Prime Agent only executes MCP servers declared in the user-scope
			// settings file; project-level entries are ignored.
			path:  primeAgentSettingsPath(),
			keys:  []string{"mcpServers"},
			entry: httpEntry,
		}, true
	}
	return mcpAgentSpec{}, false
}

// InstallAgentMCPConfig writes Prism's MCP entry into an agent's config.
// Existing user entries and unrelated settings are preserved, and a one-time
// .prism-backup is kept on the first write.
func InstallAgentMCPConfig(agentID string, port int) error {
	if !AgentMCPSupported(agentID) {
		return fmt.Errorf("%s does not have a supported MCP configuration", AgentDisplayName(agentID))
	}
	url := AgentMCPURL(agentID, port)
	switch agentID {
	case "codex":
		return installCodexMCP(url)
	case "grok-build":
		return installGrokMCP(url)
	case "empryo":
		return installEmpryoMCP(url)
	}
	spec, ok := mcpAgentSpecFor(agentID)
	if !ok || spec.path == "" {
		return fmt.Errorf("cannot determine the %s MCP config path", AgentDisplayName(agentID))
	}
	var m map[string]interface{}
	var err error
	if spec.jsonc {
		m, _, err = readJSONCConfig(spec.path)
	} else {
		m, err = readJSONConfig(spec.path)
	}
	if err != nil {
		return fmt.Errorf("failed to read %s config: %w", AgentDisplayName(agentID), err)
	}
	ensureAgentBackup(spec.path)

	servers := mcpMapAt(m, spec.keys, true)
	if servers == nil {
		return fmt.Errorf("%s config has a non-object value where the MCP servers belong", AgentDisplayName(agentID))
	}
	servers[prismMCPEntryName] = spec.entry(url)

	if err := writeJSONConfig(spec.path, m); err != nil {
		return fmt.Errorf("failed to write %s config: %w", AgentDisplayName(agentID), err)
	}
	return nil
}

// RestoreAgentMCPConfig removes Prism's MCP entry, leaving every other server
// alone.
func RestoreAgentMCPConfig(agentID string) error {
	switch agentID {
	case "codex":
		return restoreCodexMCP()
	case "grok-build":
		return restoreGrokMCP()
	case "empryo":
		return restoreEmpryoMCP()
	}
	spec, ok := mcpAgentSpecFor(agentID)
	if !ok || spec.path == "" {
		if !AgentMCPSupported(agentID) {
			return fmt.Errorf("%s does not have a supported MCP configuration", AgentDisplayName(agentID))
		}
		return fmt.Errorf("cannot determine the %s MCP config path", AgentDisplayName(agentID))
	}
	var m map[string]interface{}
	var err error
	if spec.jsonc {
		m, _, err = readJSONCConfig(spec.path)
	} else {
		m, err = readJSONConfig(spec.path)
	}
	if err != nil {
		return fmt.Errorf("failed to read %s config: %w", AgentDisplayName(agentID), err)
	}
	servers := mcpMapAt(m, spec.keys, false)
	if servers == nil {
		return nil
	}
	if _, exists := servers[prismMCPEntryName]; !exists {
		return nil
	}
	delete(servers, prismMCPEntryName)
	pruneEmptyMCPPath(m, spec.keys)
	if err := writeJSONConfig(spec.path, m); err != nil {
		return fmt.Errorf("failed to write %s config: %w", AgentDisplayName(agentID), err)
	}
	return nil
}

// AgentMCPActive reports whether Prism's MCP entry is present in the agent's
// config.
func AgentMCPActive(agentID string) bool {
	if !AgentMCPSupported(agentID) {
		return false
	}
	path := AgentMCPConfigPath(agentID)
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if agentID == "codex" {
		return strings.Contains(string(data), mcpManagedBegin)
	}
	if agentID == "grok-build" {
		return hasGrokMCPTable(string(data))
	}
	if agentID == "empryo" {
		return hasEmpryoMCPServer(data)
	}
	spec, ok := mcpAgentSpecFor(agentID)
	if !ok || spec.path == "" {
		return false
	}
	var m map[string]interface{}
	if spec.jsonc {
		m, _, err = readJSONCConfig(path)
		if err != nil {
			return false
		}
	} else if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	servers := mcpMapAt(m, spec.keys, false)
	if servers == nil {
		return false
	}
	_, exists := servers[prismMCPEntryName]
	return exists
}

// mcpMapAt walks a nested key path and returns the object it names. With create
// set, missing intermediate objects are created; without it, a missing path
// returns nil. A path that runs into a non-object value - something Prism did
// not write, such as a string or array - returns nil either way rather than
// clobbering it.
func mcpMapAt(m map[string]interface{}, keys []string, create bool) map[string]interface{} {
	cur := m
	for _, key := range keys {
		child, ok := cur[key].(map[string]interface{})
		if !ok {
			if _, exists := cur[key]; exists || !create {
				return nil
			}
			child = map[string]interface{}{}
			cur[key] = child
		}
		cur = child
	}
	return cur
}

// pruneEmptyMCPPath removes the objects Prism's entry left empty, so a config
// file created only for that entry (ZCode's ~/.zcode/cli/config.json, with just
// mcp.servers) returns to its original shape on restore.
func pruneEmptyMCPPath(m map[string]interface{}, keys []string) {
	for i := len(keys) - 1; i >= 0; i-- {
		parent := mcpMapAt(m, keys[:i], false)
		if parent == nil {
			return
		}
		child, ok := parent[keys[i]].(map[string]interface{})
		if !ok || len(child) > 0 {
			return
		}
		delete(parent, keys[i])
	}
}

// syncAgentMCPServers refreshes Prism's MCP entry for one agent on startup.
func syncAgentMCPServers(agentID string, port int) {
	if !AgentMCPSupported(agentID) {
		return
	}
	if err := InstallAgentMCPConfig(agentID, port); err != nil {
		log.Printf("[%s] Failed to sync MCP config: %v", AgentDisplayName(agentID), err)
		return
	}
	log.Printf("[%s] Synced MCP entry -> %s", AgentDisplayName(agentID), AgentMCPURL(agentID, port))
}

// SyncAllAgentMCP installs Prism's MCP entry into every installed agent whose
// integration is enabled. It is separate from SyncAgents because a user may
// want MCP without Prism-as-model-provider, or vice versa.
func SyncAllAgentMCP(port int) {
	for _, id := range allAgentIDs {
		if !AgentMCPSupported(id) {
			continue
		}
		if id == "codex" {
			if !IsCodexDesktopInstalled() {
				continue
			}
		} else if !AgentInstalled(id) {
			continue
		}
		// Only refresh entries the user already installed: Setup is an explicit
		// action, and a startup sync must never resurrect one they removed.
		if !AgentMCPActive(id) {
			continue
		}
		syncAgentMCPServers(id, port)
	}
}

// -- Empryo (array or object mcpServers) --

// Empryo accepts mcpServers either as the array of named servers its own docs
// use or as an object keyed by server name, so Prism writes whichever shape the
// file already has and creates the array form on a fresh entry. Unlike the
// other JSON agents this one file also holds the provider block, so both
// writers read-modify-write it and preserve each other's keys.

// empryoMCPServerEntry is Prism's server in Empryo's array shape. The "name" is
// correct for the object shape too, where it matches the key Empryo reads.
func empryoMCPServerEntry(url string) map[string]interface{} {
	return map[string]interface{}{
		"name":      prismMCPEntryName,
		"transport": "http",
		"url":       url,
		"headers":   map[string]interface{}{"Authorization": prismMCPAuthorization},
	}
}

// installEmpryoMCP writes Prism's MCP entry into ~/.empryo/config.json.
func installEmpryoMCP(url string) error {
	path := empryoConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Empryo config path")
	}
	m, err := readJSONConfig(path)
	if err != nil {
		return fmt.Errorf("failed to read Empryo config: %w", err)
	}
	ensureAgentBackup(path)

	switch servers := m["mcpServers"].(type) {
	case nil:
		m["mcpServers"] = []interface{}{empryoMCPServerEntry(url)}
	case []interface{}:
		m["mcpServers"] = upsertEmpryoMCPServerArray(servers, empryoMCPServerEntry(url))
	case map[string]interface{}:
		servers[prismMCPEntryName] = empryoMCPServerEntry(url)
	default:
		return fmt.Errorf("Empryo config has a non-list value where the MCP servers belong")
	}

	if err := writeJSONConfig(path, m); err != nil {
		return fmt.Errorf("failed to write Empryo config: %w", err)
	}
	return nil
}

// upsertEmpryoMCPServerArray replaces Prism's entry in Empryo's array-shaped
// mcpServers list, preserving every other server and its position.
func upsertEmpryoMCPServerArray(servers []interface{}, entry map[string]interface{}) []interface{} {
	out := make([]interface{}, 0, len(servers)+1)
	for _, item := range servers {
		if mp, ok := item.(map[string]interface{}); ok {
			if name, _ := mp["name"].(string); name == prismMCPEntryName {
				continue
			}
		}
		out = append(out, item)
	}
	return append(out, entry)
}

// restoreEmpryoMCP removes Prism's MCP entry from ~/.empryo/config.json,
// leaving Empryo's own servers alone and dropping the list only when Prism's
// entry leaves it empty.
func restoreEmpryoMCP() error {
	path := empryoConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Empryo config path")
	}
	m, err := readJSONConfig(path)
	if err != nil {
		return fmt.Errorf("failed to read Empryo config: %w", err)
	}

	switch servers := m["mcpServers"].(type) {
	case []interface{}:
		out := make([]interface{}, 0, len(servers))
		for _, item := range servers {
			if mp, ok := item.(map[string]interface{}); ok {
				if name, _ := mp["name"].(string); name == prismMCPEntryName {
					continue
				}
			}
			out = append(out, item)
		}
		if len(out) == len(servers) {
			return nil // Prism has no entry here
		}
		if len(out) == 0 {
			delete(m, "mcpServers")
		} else {
			m["mcpServers"] = out
		}
	case map[string]interface{}:
		if _, exists := servers[prismMCPEntryName]; !exists {
			return nil
		}
		delete(servers, prismMCPEntryName)
		if len(servers) == 0 {
			delete(m, "mcpServers")
		} else {
			m["mcpServers"] = servers
		}
	default:
		return nil // no list, so no Prism entry to remove
	}

	if err := writeJSONConfig(path, m); err != nil {
		return fmt.Errorf("failed to write Empryo config: %w", err)
	}
	return nil
}

// hasEmpryoMCPServer reports whether Empryo's config carries Prism's MCP entry,
// in either of the two shapes Empryo accepts.
func hasEmpryoMCPServer(data []byte) bool {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	switch servers := m["mcpServers"].(type) {
	case []interface{}:
		for _, item := range servers {
			if mp, ok := item.(map[string]interface{}); ok {
				if name, _ := mp["name"].(string); name == prismMCPEntryName {
					return true
				}
			}
		}
	case map[string]interface{}:
		_, exists := servers[prismMCPEntryName]
		return exists
	}
	return false
}

// -- Codex (TOML managed block) --

// installCodexMCP writes the [mcp_servers.prism] table inside a dedicated
// managed block. It uses its own markers so re-running the model-provider
// installer (which strips the other marker pair) never removes it.
func installCodexMCP(url string) error {
	path := codexDesktopConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Codex config path")
	}
	var existing string
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	}
	cleaned := stripMCPManagedBlock(existing)

	var block strings.Builder
	block.WriteString("\n")
	block.WriteString(mcpManagedBegin + "\n")
	block.WriteString("[mcp_servers." + prismMCPEntryName + "]\n")
	block.WriteString("url = \"" + url + "\"\n")
	// Codex's streamable_http transport sends no credentials unless told to.
	// Its literal `bearer_token` key is rejected for HTTP transports, so the
	// token goes in http_headers, matching what the other agents get.
	block.WriteString("http_headers = { Authorization = \"" + prismMCPAuthorization + "\" }\n")
	block.WriteString(mcpManagedEnd + "\n")

	result := strings.TrimRight(cleaned, "\r\n") + "\n" + block.String()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(result), 0644)
}

// restoreCodexMCP removes the MCP managed block.
func restoreCodexMCP() error {
	path := codexDesktopConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Codex config path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cleaned := stripMCPManagedBlock(string(data))
	if cleaned == string(data) {
		return nil
	}
	return os.WriteFile(path, []byte(cleaned), 0644)
}

// stripMCPManagedBlock removes the MCP markers and the [mcp_servers.prism]
// table Prism owns, leaving Prism's other managed blocks (model provider,
// catalog) and everything else untouched.
//
// It must not delete the rest of what sits between the markers. Codex persists
// settings of its own — project trust, the Windows sandbox mode, hook approvals
// — by appending tables to ~/.codex/config.toml, and Prism's blocks live at the
// end of that file, so those tables land inside the markers. A range-based
// strip would drop them on every startup, which is why Codex kept asking the
// user to trust the folder and set up the sandbox again.
func stripMCPManagedBlock(content string) string {
	return stripManagedRegion(content, mcpManagedBegin, mcpManagedEnd, isPrismMCPTableHeader, nil)
}

// isPrismMCPTableHeader reports whether a table header names Prism's MCP server
// entry or one of its sub-tables (e.g. a written-out [mcp_servers.prism.http_headers]).
func isPrismMCPTableHeader(trimmed string) bool {
	return isManagedTableHeader(trimmed, "mcp_servers."+prismMCPEntryName)
}

// -- Grok Build (TOML table) --

// installGrokMCP writes Prism's [mcp_servers.prism] table into
// ~/.grok/config.toml.
//
// Grok Build rewrites its whole config file whenever `grok mcp add/remove/
// enable` runs, and that rewrite drops every comment - including managed-block
// markers. Prism therefore identifies its entry by table name instead of by
// markers: an existing [mcp_servers.prism] table (and any of its sub-tables) is
// stripped before the fresh one is appended, which keeps re-syncing idempotent
// even after Grok has rewritten the file. Grok's own [mcp_servers.<name>]
// entries are left alone.
func installGrokMCP(url string) error {
	path := grokBuildConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Grok Build config path")
	}
	var existing string
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read Grok Build config: %w", err)
	}
	ensureAgentBackup(path)
	cleaned := stripGrokMCPTable(existing)

	var block strings.Builder
	block.WriteString("\n")
	block.WriteString("[mcp_servers." + prismMCPEntryName + "]\n")
	block.WriteString("url = " + tomlQuote(url) + "\n")
	block.WriteString("enabled = true\n")
	// The header is sent on every request; Grok's CLI writes the same
	// headers = {...} shape for an authenticated remote server.
	block.WriteString("headers = { Authorization = " + tomlQuote(prismMCPAuthorization) + " }\n")

	result := strings.TrimRight(cleaned, "\r\n") + "\n" + block.String()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create Grok Build config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(result), 0644); err != nil {
		return fmt.Errorf("failed to write Grok Build config: %w", err)
	}
	return nil
}

// restoreGrokMCP removes Prism's MCP table, leaving Grok's own servers alone.
func restoreGrokMCP() error {
	path := grokBuildConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine Grok Build config path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cleaned := stripGrokMCPTable(string(data))
	if cleaned == string(data) {
		return nil
	}
	return os.WriteFile(path, []byte(cleaned), 0644)
}

// stripGrokMCPTable removes the [mcp_servers.prism] table and its sub-tables,
// keeping every other table and key in the file. The managed-region markers are
// passed so a block written by an older Prism version is cleaned up too; Grok
// itself has usually erased them by rewriting the file.
func stripGrokMCPTable(content string) string {
	return stripManagedRegion(content, mcpManagedBegin, mcpManagedEnd, isPrismMCPTableHeader, nil)
}

// hasGrokMCPTable reports whether the config already carries Prism's MCP table.
func hasGrokMCPTable(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if isPrismMCPTableHeader(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}
