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

const (
	mcpManagedBegin = "# >>> prism mcp managed >>>"
	mcpManagedEnd   = "# <<< prism mcp managed <<<"
)

// mcpAgentSpec describes how to write Prism's MCP entry into one agent's config.
type mcpAgentSpec struct {
	path  string
	key   string
	jsonc bool
	entry func(url string) map[string]interface{}
}

// AgentMCPSupported reports whether Prism knows the MCP config shape of an
// agent. Agents without a documented MCP config are reported as unsupported in
// the admin UI instead of being written to speculatively.
func AgentMCPSupported(agentID string) bool {
	switch agentID {
	case "claude-code", "codex", "opencode", "zed":
		return true
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
	if spec, ok := mcpAgentSpecFor(agentID); ok {
		return spec.path
	}
	if agentID == "codex" {
		return codexDesktopConfigPath()
	}
	return ""
}

func mcpAgentSpecFor(agentID string) (mcpAgentSpec, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return mcpAgentSpec{}, false
	}
	headers := func() map[string]interface{} {
		return map[string]interface{}{"Authorization": "Bearer prism"}
	}
	switch agentID {
	case "claude-code":
		return mcpAgentSpec{
			// Claude Code keeps user-scope MCP servers in ~/.claude.json.
			path: filepath.Join(home, ".claude.json"),
			key:  "mcpServers",
			entry: func(url string) map[string]interface{} {
				return map[string]interface{}{
					"type":    "http",
					"url":     url,
					"headers": headers(),
				}
			},
		}, true
	case "opencode":
		return mcpAgentSpec{
			path: opencodeConfigPath(),
			key:  "mcp",
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
			key:   "context_servers",
			entry: func(url string) map[string]interface{} {
				return map[string]interface{}{
					"url":     url,
					"headers": headers(),
				}
			},
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
	if agentID == "codex" {
		return installCodexMCP(url)
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

	servers, _ := m[spec.key].(map[string]interface{})
	if servers == nil {
		servers = map[string]interface{}{}
	}
	servers[prismMCPEntryName] = spec.entry(url)
	m[spec.key] = servers

	if err := writeJSONConfig(spec.path, m); err != nil {
		return fmt.Errorf("failed to write %s config: %w", AgentDisplayName(agentID), err)
	}
	return nil
}

// RestoreAgentMCPConfig removes Prism's MCP entry, leaving every other server
// alone.
func RestoreAgentMCPConfig(agentID string) error {
	if agentID == "codex" {
		return restoreCodexMCP()
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
	servers, ok := m[spec.key].(map[string]interface{})
	if !ok {
		return nil
	}
	if _, exists := servers[prismMCPEntryName]; !exists {
		return nil
	}
	delete(servers, prismMCPEntryName)
	if len(servers) == 0 {
		delete(m, spec.key)
	} else {
		m[spec.key] = servers
	}
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
	var m map[string]interface{}
	if agentID == "zed" {
		m, _, err = readJSONCConfig(path)
		if err != nil {
			return false
		}
	} else {
		if err := json.Unmarshal(data, &m); err != nil {
			return false
		}
	}
	key := "mcpServers"
	switch agentID {
	case "opencode":
		key = "mcp"
	case "zed":
		key = "context_servers"
	}
	servers, ok := m[key].(map[string]interface{})
	if !ok {
		return false
	}
	_, exists := servers[prismMCPEntryName]
	return exists
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

// stripMCPManagedBlock removes the MCP marker block, leaving Prism's other
// managed blocks (model provider, catalog) untouched.
func stripMCPManagedBlock(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == mcpManagedBegin {
			inBlock = true
			continue
		}
		if trimmed == mcpManagedEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
