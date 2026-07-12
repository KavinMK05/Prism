# Prism v0.3.11

## Bug Fixes

- **Fixed: Claude Code integration writes to settings.local.json to prevent config clobbering.** Claude Code watches and rewrites `settings.json`, which previously caused Prism's environment variables to be overwritten on file changes. Prism now writes to `settings.local.json` (the local-override file that Claude Code merges but never overwrites), preserving Prism's configuration across Claude Code restarts. Existing entries from `settings.json` are copied into the local file so they survive the merge.

- **Fixed: Factory Droid customModels entries duplicated on every sync.** When Prism synced models, entries copied from `settings.json` into `settings.local.json` on a prior sync were re-read from both files on every subsequent sync, accumulating duplicates. Added deduplication by model name so entries are preserved without duplication.

- **Fixed: Grok Build model sections duplicated on re-sync.** Older Prism versions wrote `[model.prism-*]` sections outside managed block markers. On re-sync, these would be duplicated because only the managed blocks were stripped. Added `stripPrismModelSections()` to clean all Prism model sections before re-writing.

- **Fixed: Strict streaming clients (Grok Build) failed to parse error responses.** Some strict serde clients attempt to deserialize non-200 responses as chat completion chunks, failing with "missing field `id`". Streaming errors are now sent in SSE format with proper `id`/`object`/`created` fields so clients can parse and detect the error via the `error` field.

- **Fixed: Strict streaming clients failed on incomplete tool call chunks.** Some OpenAI-compatible providers omit `id`, `type`, and `name` fields in subsequent tool call argument-delta chunks (only the first chunk carries metadata). Prism now tracks and injects these fields into every tool call chunk, ensuring strict clients can deserialize the stream.

## Improvements

- **Admin UI: Models panel shows provider display names instead of IDs.** The `/api/stats/models` endpoint now returns sorted `{id, name}` objects instead of raw provider ID strings, so the Models panel dropdown shows human-readable names (e.g. "Ollama Cloud" instead of "ollama_cloud").

- **Admin UI: Tab icons added.** Each tab in the admin UI now has an SVG icon for visual clarity.

- **Grok Build: Deduplication key for customModels.** Added `customModelKey()` helper that extracts the model name from custom model entries for deduplication, preventing accumulation of duplicate non-Prism entries.

- **README updated** with admin UI screenshot.

# Prism v0.3.9

## Bug Fixes

- **Fixed: Unicode escape sequences rendering as literal text in the React admin UI.** JSX text content in AgentsPanel, ModelsPanel, ProviderPanel, SearXNGPanel, and StatsPanel was using JavaScript escape sequences (`\u2014`, `\u2192`, `\u00d7`, `\u00b7`) which JSX renders verbatim instead of interpreting as Unicode characters. Replaced with actual Unicode characters (—, →, ×, ·) so em-dashes, arrows, multiplication signs, and middle dots render correctly throughout the admin interface.

- **Fixed: Claude Code tier dropdown had no model options to choose from.** The `/api/agents/claude-code` status endpoint only returned persisted tier mappings but no list of available models, leaving the tier dropdowns empty. The endpoint now also returns `model_options` — a deduplicated list of known model IDs (from model remapping + default model) so the UI can populate the dropdown menus.

## Improvements

- **Heatmap grid layout corrected.** The stats heatmap was filling cells row-by-row instead of column-by-column, producing an incorrect calendar layout. Added `grid-auto-flow: column` so cells fill top-to-bottom then left-to-right, matching the standard GitHub-style contribution calendar.

- **SearXNG panel styles extracted to CSS classes.** Inline styles for `.searx-status`, `.searx-install-msg`, and `.searx-form-section` (including `h4` headings) moved to `styles.css` for consistency with the rest of the admin UI.

- **Removed obsolete `web/dist/.gitkeep`** — the placeholder is no longer needed since the frontend build generates the dist directory at build time.

# Prism v0.3.8

## Features

- **Admin UI migrated from plain HTML to React + Vite + TypeScript.** The entire admin settings page (all 7 panels: Provider, OAuth, Models, Stats, Agents, Proxy, SearXNG) has been rewritten as a React application built with Vite. The production build is embedded into the Go binary via `go:embed`, preserving the single-binary distribution model. No Node.js is required at runtime.

- **Chart.js integrated via npm with tree-shaking.** Stats panel charts (bar, line, sparkline) now use `chart.js` and `react-chartjs-2` imported from npm, with only the Bar and Line controllers registered for minimal bundle size. No more CDN dependency for Chart.js.

- **Dev workflow with HMR.** Running `cd web && npm run dev` starts a Vite dev server with hot module replacement on `localhost:5173/admin/`, proxying API calls to the Go server on port 8765.

## Infrastructure

- **Build scripts (`build.ps1` / `build.sh`)** that build the frontend then the Go binary in one command.

- **CI/CD updated** with Node.js 22 setup and `npm ci && npm run build` before `go build` in both Windows and macOS jobs. PR trigger paths now include `web/**`.

- **TypeScript strict mode** with `any` escape hatch for untyped API responses.

- **Legacy admin page preserved** at `/admin-legacy` for reference during migration.

- **AGENTS.md updated** with new build commands and frontend dev instructions.

## Notes

- Local builds now require Node.js to be installed. Run `./build.ps1` (Windows) or `./build.sh` (macOS) instead of `go build` directly.
- The frontend dev dependencies are in `web/package.json`. Run `cd web && npm install` before first build.

# Prism v0.3.5

## Bug Fixes

- **Fixed: SearXNG failed to start with "Address already in use" (port 8888) after an unclean Prism exit.** When Prism was force-quit or crashed without reaping its child SearXNG process, the orphaned webapp survived and kept holding port 8888. On the next launch Prism saw no tracked SearXNG PID (`isSearxngRunning()` false), spawned a fresh webapp, which couldn't bind 8888 and crashed — and the crash-restart loop gave up after 5 attempts in 60s, leaving SearXNG permanently down. The existing `killOrphanOnPort()` only reclaimed the proxy port (11434); SearXNG's port was never reclaimed. Prism now reclaims the configured SearXNG port before spawning the webapp, killing any orphan holding it while sparing Prism's own tracked PID. The recursive restart path also flows through this, so crash-restarts benefit too.

- **Refactor: shared `killOrphansOnPort(port, knownPID)` helper (macOS + Windows).** Extracted from the proxy-only `killOrphanOnPort()` so both the proxy and SearXNG reclaim ports the same way. Added `port_orphan_test.go` verifying both branches: a known PID is spared (returns 0, process alive) and a foreign orphan is killed (returns ≥1, process exits).

## Notes

- The SearXNG log lines `fatal: not a git repository`, `missing config file ... limiter.toml`, and `ahmia`/`torch: can't register engine` are harmless SearXNG-internal noise (running from an extracted tarball; limiter is off; optional engines missing optional deps) and do not affect search.