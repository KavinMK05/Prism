# Prism v0.2.2

## What's New

- **Oh My Pi (omp) Agent Integration** — Prism now registers `prism` and `prism-codex` providers in `~/.omp/agent/models.yml` so Oh My Pi can route your local models through Prism. Non-Codex models use the `openai-completions` transport (`/v1/chat/completions`); Codex OAuth models use `openai-responses` (`/v1/responses`). A new admin UI card provides Setup/Restore actions and live status checks. Existing providers and top-level keys in `models.yml` are preserved, with a one-time `.prism-backup`.
- **macOS Auto-start** — LaunchAgent plist (`~/Library/LaunchAgents/com.prism.plist`) launches Prism at login (toggle in Proxy tab → Start at Login).
- **Node.js 20 deprecation notice** — Release workflow actions still target Node.js 20 (GitHub is forcing Node.js 24). Builds pass today; action versions will be bumped before GitHub hard-blocks Node.js 20.

## Improvements

- Rebranded README from a translation proxy to a universal LLM proxy with agent integrations documentation.
- Tighter inbound streaming: reworked OpenAI-compatible stream handling for more reliable passthrough.
- Model catalog generation now produces `reasoning_levels` in the correct dict format and matches codex-shim's file ordering.

## Bug Fixes

- **Fixed: models.dev lookup returned the wrong model and wrong context window.** Searching a model (e.g. GLM-5.2 under Ollama Cloud) returned a *different* model's metadata — GLM-5.2 came back as `glm-5` with context 202,752 instead of the exact `glm-5.2` at 976,000 — because (1) the matcher sorted exact matches to the front but selected the *last* (worst/partial) match, and (2) greedy reverse-substring matching let a shorter id (`glm-5`) match a longer search (`glm-5.2`). The matcher now picks the **best** match directly (exact > in-scope-provider > native > deterministic provider id), so lookups are stable regardless of Go's randomized map iteration, prefer the selected provider's value, and fall back to all providers when that provider doesn't list the model. Also added a 30s HTTP timeout so a stalled ~3 MB models.dev download can no longer hang the UI's "Fetch". The duplicate parser in the public `/api/model-info` endpoint now shares the same implementation. Covered by hermetic + live tests.
- **Fixed: OMP / OpenCode reported as "not detected" on macOS.** macOS `.app` bundles launched from Finder/Dock inherit a minimal PATH (`/usr/bin:/bin:/usr/sbin:/sbin`), so `exec.LookPath` missed binaries installed by Homebrew (`/opt/homebrew/bin`), bun (`~/.bun/bin`), mise (`~/.local/share/mise/shims`), and npm-global. When the agent config file didn't yet exist, detection fell through to the broken binary check. Added a shared `lookupBinary` helper that augments the search with the common install directories for OMP's documented macOS install methods (Homebrew, bun, curl, mise, npm-global); applied to both OMP and OpenCode, which shared the identical defect. Covered by a hermetic test simulating the minimal-PATH GUI scenario.
- Fixed TOML path escaping and section-aware key extraction when writing managed blocks to `~/.codex/config.toml`.
- Preserved Codex Desktop built-in tool types in the Responses API.