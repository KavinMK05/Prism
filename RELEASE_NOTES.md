# Prism v0.3.13

## Features

- **Pluggable web-search interception.** Prism now intercepts web-search tool calls from Claude Code, ZCode, Codex Desktop, and Grok Build and routes them through configurable search backends (SearXNG, Exa, Tavily, Brave, Serper). When a model emits a `web_search` or `x_search` tool call, Prism runs the query locally and returns results — no hosted search API required. Supports both the Anthropic Messages API (server-tool emulation + Claude Code secondary-conversation detection) and the OpenAI Responses API (web_search_call loop with live SSE streaming).

- **Search providers admin UI.** New "Search" tab in the admin panel for configuring the active provider, fallback chain, per-provider enable/API-key/base-URL settings, and a test button that runs a live query against any provider. Keys are write-only (never echoed back); existing env vars like `EXA_API_KEY` are auto-detected.

- **Admin API endpoints for search.** `GET/PUT /admin/search/config` (provider settings, keys, fallback chain), `GET /admin/search/providers` (catalog metadata), and `POST /admin/search/test` (live test query).

- **SearXNG and Exa provider implementations.** SearXNG is the default managed provider (bundled, no API key needed). Exa is included as a paid alternative. Tavily, Brave, and Serper are cataloged but not yet implemented — they appear in the admin UI as "coming soon" placeholders.

- **Grok Build: `supports_backend_search = true`.** All Prism model sections in Grok Build's `models.toml` now advertise `supports_backend_search = true` and use `api_backend = "responses"`, so Grok Build emits typed `x_search` tools that Prism intercepts.

- **ZCode: `kind: "anthropic"` provider config.** ZCode's Prism provider entry now uses `kind: "anthropic"` instead of `openai-compatible`, so ZCode sends Anthropic-format requests with server tools (`web_search_20250305`, `web_fetch_20250924`) that Prism emulates via the search runner.

- **Responses API: `x_search` tool type support.** The Responses API path now recognizes `x_search` (xAI/Grok Build) as a built-in web-search tool type alongside `web_search`, with proper argument extraction (`query` → `input` fallback), domain filtering (`excluded_domains` / `blocked_domains`), and `max_uses` limits.

- **Responses API streaming: `item_id` fields added.** All streaming events that were missing `item_id` (reasoning summary part added/done, content part added/done, output text delta/done) now include it. Grok Build's strict Rust deserializer requires `item_id` on every event where it's expected.

- **Responses API: `input_tokens_details` / `output_tokens_details` in usage.** All streaming and non-streaming Responses API usage objects now include `input_tokens_details` (with `cached_tokens`) and `output_tokens_details` (with `reasoning_tokens`). Grok Build's client requires these fields on every usage object.

- **Responses API: `model`, `background`, `error`, `output`, `usage` in `response.in_progress`.** The `response.in_progress` event now carries the full response object (model, background flag, error, output array, usage) matching the completed event shape, so clients that inspect it don't see null/missing fields.

## Bug Fixes

- **Fixed: Translation debug directory pruning was order-dependent.** `pruneTranslationDebugDirs` sorted by directory name (zero-padded sequence), which broke after process restarts — fresh low-numbered dirs were pruned before stale high-numbered ones. Now sorts by modification time so the most recent directories always survive.

## Notes

- Search interception is enabled by default with the managed SearXNG instance. Disable it by setting the active provider to empty or toggling SearXNG off in the admin UI; requests then pass through to upstream unchanged.