# Phase-by-Phase Plan: Agent BYOK Integrations (Jun 21, 2026)

Extend Prism's existing Codex Desktop BYOK setup pattern to three more coding agents that support custom OpenAI/Anthropic-compatible endpoints. Each agent gets its own admin card (under a new "Agent Integrations" section) with Status / Setup / Restore, plus auto-sync on Prism startup when the agent is installed. All agents target both Windows and macOS via `os.UserHomeDir`.

## Documentation & References

- **Prism codebase (existing patterns)**
  - `codex_desktop.go` — managed-block TOML writer, catalog generator, sync-on-startup pattern (`SyncCodexDesktop`)
  - `admin.go` — `/admin/codex-desktop/{status,setup,restore}` handlers
  - `admin.html` — "Codex Desktop Integration" card (Status/Setup/Restore), `checkCodexDesktopStatus()` JS
  - `main.go` — `SyncCodexDesktop(parseIntOr(port, 11434))` call on startup; `proxyAPIKey := "prism"` (single fixed key for all endpoints)
  - `config.go` — `Config` struct + `loadConfig()` / `saveConfig()` JSON persistence in `getConfigDir()`
  - Proxy endpoints already supported: `/v1/messages` (Anthropic), `/v1/chat/completions` (OpenAI chat), `/v1/responses` (OpenAI responses), `/v1/models`
- **Claude Code**: [Environment variables](https://code.claude.com/docs/en/env-vars) — `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL`; written to `~/.claude/settings.json` under the `env` key
- **Factory Droid**: [BYOK overview](https://docs.factory.ai/cli/byok/overview) — `~/.factory/settings.json` `customModels[]` array; fields `model`, `displayName`, `baseUrl`, `apiKey`, `provider` (`anthropic` | `openai` | `generic-chat-completion-api`), `maxOutputTokens`, `noImageSupport`
- **OpenCode**: [Config](https://opencode.ai/docs/config/) — `~/.config/opencode/opencode.json`; `provider.<id>.options.{baseURL,apiKey}` + per-model config; supports `{env:VAR}` / `{file:path}` substitution; config schema at `https://opencode.ai/config.json`

## Design Decisions (confirmed with user)

- **Scope**: Claude Code, Factory Droid, OpenCode (3 agents). Each is its own phase.
- **API key written into agent configs**: `"prism"` (matches Codex `experimental_bearer_token = "prism"`; the proxy's single fixed `proxyAPIKey`).
- **Managed-entry strategy for JSON configs**: Tag-based — prefix model display names with `[Prism]` and detect/remove our entries by that tag. Keep a one-time backup of each agent's original config file (e.g. `~/.claude/settings.json.prism-backup`) written on first Setup, used only as a safety net.
- **Admin UI**: One card per agent under a new "Agent Integrations" section, matching the existing Codex card pattern (status line + Setup + Restore buttons).
- **Startup behavior**: Auto-sync each installed agent on Prism startup (like `SyncCodexDesktop`); Setup/Restore buttons only; no enable/disable toggle.
- **Model exposure**:
  - Claude Code: user maps 4 tiers (Opus / Sonnet / Haiku / Subagent) to Prism models in the admin card; Prism writes `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN=prism`, and the four `ANTHROPIC_DEFAULT_*_MODEL` / `CLAUDE_CODE_SUBAGENT_MODEL` env keys. Mappings persist in Prism's `config.json` so auto-sync can re-apply them. Defaults: each tier maps to the first Prism model until the user changes it.
  - Factory Droid: one `customModels[]` entry per Prism model, `displayName="[Prism] <humanized>"`, `provider="generic-chat-completion-api"`, `baseUrl=http://127.0.0.1:<port>/v1`, `apiKey="prism"`. Default model = first.
  - OpenCode: one provider block `"prism"` with `baseURL`/`apiKey` + one model entry per Prism model, default = first.
- **Platform**: Windows + macOS (cross-platform via `os.UserHomeDir`).

## Phase 1: Foundation & "Agent Integrations" Section Scaffold
- [x] Add `AgentIntegrations` config struct to `config.go` (persisted via existing `loadConfig`/`saveConfig`): holds `ClaudeCodeTiers` map (`opus`, `sonnet`, `haiku`, `subagent` -> Prism model id). Initialize defaults in `defaultConfig()` / `loadConfig()` so each tier maps to the first Prism model when unset.
- [x] Create `agents_common.go` with shared helpers: `agentManagedTag = "[Prism]"`, `agentConfigPath(agent string) string` (per-agent config file path via `os.UserHomeDir`), `backupAgentConfig(path)` (one-time `.prism-backup` copy), `isAgentInstalled(path)`, `isAgentActive(path)` (presence of `[Prism]` tag / known keys), JSON read/merge/strip-tagged-entries helpers (`readJSON`, `writeJSON`, `stripPrismEntries`, `hasPrismEntries`).
- [x] Add a new "Agent Integrations" section container to `admin.html` (heading + stacked card placeholders), placed near the existing Codex Desktop card. Add stub cards for Claude Code, Factory Droid, OpenCode (status line + Setup/Restore buttons, wired to `/admin/agent/<id>/{status,setup,restore}`).
- [x] Add generic admin handlers in `admin.go` for `/admin/agent/{id}/status`, `.../setup`, `.../restore` that dispatch by agent id (initially returning "not yet implemented" for setup/restore, real status for installed/active). This wires the UI early; each later phase fills in the agent-specific setup/restore logic.
- [x] Add `SyncAgents(port int)` to `agents_common.go` that calls each agent's sync (Claude/Factory/OpenCode) when installed; call it from `main.go` next to `SyncCodexDesktop`. In Phase 1 the per-agent sync funcs are no-ops/log-only stubs.
- [x] Build the project (`go-winres make; go build ... -o prism.exe`) and confirm it compiles.
- **User Acceptance**: Prism builds and runs; admin UI shows the new "Agent Integrations" section with three cards (Claude Code, Factory Droid, OpenCode), each showing a status line ("installed/not detected") and Setup/Restore buttons. Clicking Setup/Restore shows a "not yet implemented" toast (expected for Phase 1). No existing Codex Desktop card or other functionality is broken.

## Phase 2: Claude Code Integration
- [x] Implement `claude_code.go`: `claudeCodeConfigPath()` -> `~/.claude/settings.json`; `isClaudeCodeInstalled()`, `isClaudeCodeActive()` (detect Prism env keys or `[Prism]` backup marker); `installClaudeCodeConfig(port int, tiers map)` writes `env.ANTHROPIC_BASE_URL=http://127.0.0.1:<port>`, `env.ANTHROPIC_AUTH_TOKEN=prism`, `env.ANTHROPIC_DEFAULT_OPUS_MODEL`, `env.ANTHROPIC_DEFAULT_SONNET_MODEL`, `env.ANTHROPIC_DEFAULT_HAIKU_MODEL`, `env.CLAUDE_CODE_SUBAGENT_MODEL` from persisted tiers (default each to first Prism model if unset); preserves all other existing `env` keys and top-level settings; one-time `.prism-backup`. `restoreClaudeCodeConfig()` removes Prism env keys and restores from backup if present. `syncClaudeCode(port int)` = install if installed.
- [x] Wire `/admin/agent/claude-code/setup` and `.../restore` in `admin.go` to call the new funcs; `.../status` returns installed/active. `setup` reads tiers from `loadConfig().AgentIntegrations.ClaudeCodeTiers`, saves updated tiers if the request includes new tier choices.
- [x] Add tier-mapping UI to the Claude Code card in `admin.html`: four dropdowns (Opus / Sonnet / Haiku / Subagent) populated from `/v1/models` (Prism model list), a Save/Setup button that POSTs the chosen tiers to `/admin/agent/claude-code/setup`, then refreshes status. Restore button removes Prism config.
- [x] Replace the Phase 1 `syncClaudeCode` stub with the real sync call inside `SyncAgents`.
- [x] Build and verify the binary compiles.
- **User Acceptance**: With Claude Code installed (`~/.claude/settings.json` present or created), opening the admin card shows "Active". User can pick a Prism model for each of the 4 tiers and click Setup; `~/.claude/settings.json` then contains `env.ANTHROPIC_BASE_URL` pointing at Prism + the four tier env keys = chosen Prism model ids, and all pre-existing settings are preserved. Running `claude` routes through Prism (verify via a request hitting Prism's `/v1/messages`, e.g. Prism logs). Clicking Restore removes Prism env keys and restores the original file from the `.prism-backup`. Restarting Prism re-applies the config automatically.

## Phase 3: Factory Droid Integration
- [x] Implement `factory_droid.go`: `factoryDroidConfigPath()` -> `~/.factory/settings.json`; `isFactoryDroidInstalled()`, `isFactoryDroidActive()` (detect any `customModels[]` entry whose `displayName` starts with `[Prism]`); `installFactoryDroidConfig(port int, remap)` writes one `customModels[]` entry per Prism model: `model=<id>`, `displayName="[Prism] <humanized>"`, `baseUrl="http://127.0.0.1:<port>/v1"`, `apiKey="prism"`, `provider="generic-chat-completion-api"`, `maxOutputTokens` from model entry (fallback 16384), `noImageSupport` when model lacks vision; strips any prior `[Prism]` entries first and preserves all other `customModels[]` entries and top-level keys; one-time `.prism-backup`. `restoreFactoryDroidConfig()` removes `[Prism]` entries and restores from backup if present. `syncFactoryDroid(port int)` = install if installed.
- [x] Wire `/admin/agent/factory-droid/{status,setup,restore}` in `admin.go` to the new funcs (setup needs no user input — uses full model list).
- [x] Replace the Phase 1 `syncFactoryDroid` stub with the real sync call inside `SyncAgents`.
- [x] Build and verify.
- **User Acceptance**: With Factory Droid installed (`~/.factory/settings.json` present or created), the admin card shows "Active" after Setup. `~/.factory/settings.json` `customModels[]` contains one `[Prism] <model>` entry per Prism model with `baseUrl` pointing at Prism and `apiKey="prism"`; existing non-Prism entries are preserved. Running `droid` and using `/model` shows the `[Prism]` models and routes through Prism (verify via Prism logs hitting `/v1/chat/completions`). Restore removes the `[Prism]` entries and restores the original. Restarting Prism re-syncs automatically.

## Phase 4: OpenCode Integration
- [x] Implement `opencode_agent.go`: `opencodeConfigPath()` -> `~/.config/opencode/opencode.json` (create dir if needed); `isOpencodeInstalled()` (config file OR `opencode` binary present), `isOpencodeActive()` (detect `provider.prism` block); `installOpencodeConfig(port int, remap)` writes a `provider.prism` block with `options.baseURL="http://127.0.0.1:<port>/v1"`, `options.apiKey="prism"`, plus a `models` map with one entry per Prism model (id -> model config with display name `[Prism] <humanized>`); sets `model="prism/<first-model-id>"` as default; preserves all other providers/keys; one-time `.prism-backup`. `restoreOpencodeConfig()` removes the `prism` provider + any `model`/`small_model` we set, restores backup if present. `syncOpencode(port int)` = install if installed.
- [x] Wire `/admin/agent/opencode/{status,setup,restore}` in `admin.go` to the new funcs.
- [x] Replace the Phase 1 `syncOpencode` stub with the real sync call inside `SyncAgents`.
- [x] Build and verify.
- **User Acceptance**: With OpenCode installed (`~/.config/opencode/opencode.json` present or `opencode` binary on PATH), the admin card shows "Active" after Setup. The config contains a `provider.prism` block with the Prism base URL/apiKey and one model entry per Prism model; existing providers are preserved. Running `opencode` and selecting a `prism/` model routes through Prism (verify via Prism logs hitting `/v1/chat/completions`). Restore removes the `prism` provider and restores the original. Restarting Prism re-syncs automatically. (Phase 4 PASSED.)

## Notes
- No unit/integration tests — manual testing only, per the phase-by-phase planner convention.
- The existing Codex Desktop card stays as-is (it remains under its own section or can be moved into "Agent Integrations" later if desired — not in scope for this plan).
