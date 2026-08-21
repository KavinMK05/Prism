package admin

import (
	"encoding/json"
	"net/http"

	"ollama-proxy/internal/agents"
	"ollama-proxy/internal/config"
)

func handleCodexDesktopStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"installed": agents.IsCodexDesktopInstalled(),
		"active":    agents.IsCodexDesktopActive(),
	})
}

func handleCodexDesktopSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !agents.IsCodexDesktopInstalled() {
		writeJSONError(w, "Codex Desktop is not installed (~/.codex/config.toml not found)", 404)
		return
	}
	remap := config.LoadModelRemapping()
	if err := agents.WriteCodexCatalog(remap); err != nil {
		writeJSONError(w, "failed to write catalog: "+err.Error(), 500)
		return
	}
	if err := agents.InstallCodexConfig(agents.ProxyPortFromEnv()); err != nil {
		writeJSONError(w, "failed to install config: "+err.Error(), 500)
		return
	}
	if err := agents.SetAgentAutoSync("codex", true); err != nil {
		writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleCodexDesktopRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := agents.RestoreCodexConfig(); err != nil {
		writeJSONError(w, "failed to restore config: "+err.Error(), 500)
		return
	}
	if err := agents.SetAgentAutoSync("codex", false); err != nil {
		writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := r.URL.Query().Get("id")
	if !agents.IsSupportedAgent(id) {
		writeJSONError(w, "unknown agent", 400)
		return
	}
	resp := map[string]interface{}{
		"id":          id,
		"displayName": agents.AgentDisplayName(id),
		"installed":   agents.AgentInstalled(id),
		"active":      agents.IsAgentActive(id),
	}
	// Expose persisted Claude Code tier mappings so the UI can pre-select.
	if id == "claude-code" {
		tiers := map[string]string{}
		cfg := config.Load()
		if cfg.AgentIntegrations != nil && cfg.AgentIntegrations.ClaudeCodeTiers != nil {
			tiers = cfg.AgentIntegrations.ClaudeCodeTiers
		}
		resp["tiers"] = tiers
		// Provide known model IDs so the UI can populate tier dropdowns.
		remap := config.LoadModelRemapping()
		modelOpts := make([]string, 0, len(remap.KnownModels))
		for _, m := range remap.KnownModels {
			modelOpts = append(modelOpts, m.ID)
		}
		if remap.DefaultModel != "" {
			modelOpts = append(modelOpts, remap.DefaultModel)
		}
		resp["model_options"] = modelOpts
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleAgentSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := r.URL.Query().Get("id")
	if !agents.IsSupportedAgent(id) {
		writeJSONError(w, "unknown agent", 400)
		return
	}
	if !agents.AgentInstalled(id) {
		writeJSONError(w, agents.AgentDisplayName(id)+" is not installed", 404)
		return
	}
	switch id {
	case "claude-code":
		// Optional body: { "tiers": { "opus": "...", "sonnet": "...", "haiku": "...", "subagent": "..." } }
		var body struct {
			Tiers map[string]string `json:"tiers"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONError(w, "invalid JSON: "+err.Error(), 400)
				return
			}
		}
		cfg := config.Load()
		if cfg.AgentIntegrations == nil {
			cfg.AgentIntegrations = &config.AgentIntegrationsConfig{ClaudeCodeTiers: map[string]string{}}
		}
		if cfg.AgentIntegrations.ClaudeCodeTiers == nil {
			cfg.AgentIntegrations.ClaudeCodeTiers = map[string]string{}
		}
		if len(body.Tiers) > 0 {
			for k, v := range body.Tiers {
				cfg.AgentIntegrations.ClaudeCodeTiers[k] = v
			}
			if err := config.Save(cfg); err != nil {
				writeJSONError(w, "failed to save config: "+err.Error(), 500)
				return
			}
		}
		// Keep the in-memory adminConfig in sync. Background handlers (OAuth
		// token exchange, background usage refresh) and the Provider panel's
		// /config round-trip snapshot adminConfig and call saveConfig, which
		// would otherwise overwrite config.json with a stale agent_integrations
		// and - because the JSON tag is omitempty - drop the tier mappings
		// entirely, silently reverting the user's setup. See the analogous
		// SearXNGAutoStart fix in /admin/searxng/autostart.
		config.UpdateCurrent(func(c *config.Config) {
			if c != nil {
				c.AgentIntegrations = cfg.AgentIntegrations
			}
		})
		if err := agents.InstallClaudeCodeConfig(agents.ProxyPortFromEnv(), cfg.AgentIntegrations.ClaudeCodeTiers); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"tiers":  cfg.AgentIntegrations.ClaudeCodeTiers,
		})
	case "factory-droid":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallFactoryDroidConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	case "opencode":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallOpencodeConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	case "zcode":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallZcodeConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	case "omp":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallOmpConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	case "grok-build":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallGrokBuildConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	case "pi":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallPiConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	case "zed":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallZedConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	case "kimi-code":
		remap := config.LoadModelRemapping()
		if len(remap.KnownModels) == 0 {
			writeJSONError(w, "no Prism models configured", 400)
			return
		}
		if err := agents.InstallKimiCodeConfig(agents.ProxyPortFromEnv(), remap); err != nil {
			writeJSONError(w, "failed to install config: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, true); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"models": len(remap.KnownModels),
		})
	default:
		writeJSONError(w, agents.AgentDisplayName(id)+" setup is not yet implemented", 501)
	}
}

func handleAgentRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := r.URL.Query().Get("id")
	if !agents.IsSupportedAgent(id) {
		writeJSONError(w, "unknown agent", 400)
		return
	}
	switch id {
	case "claude-code":
		if err := agents.RestoreClaudeCodeConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "factory-droid":
		if err := agents.RestoreFactoryDroidConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "opencode":
		if err := agents.RestoreOpencodeConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "zcode":
		if err := agents.RestoreZcodeConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "omp":
		if err := agents.RestoreOmpConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "grok-build":
		if err := agents.RestoreGrokBuildConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "pi":
		if err := agents.RestorePiConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "zed":
		if err := agents.RestoreZedConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "kimi-code":
		if err := agents.RestoreKimiCodeConfig(); err != nil {
			writeJSONError(w, "failed to restore: "+err.Error(), 500)
			return
		}
		if err := agents.SetAgentAutoSync(id, false); err != nil {
			writeJSONError(w, "failed to save agent preference: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		writeJSONError(w, agents.AgentDisplayName(id)+" restore is not yet implemented", 501)
	}
}
