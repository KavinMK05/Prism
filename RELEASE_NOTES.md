# Prism v0.3.15

## Bug Fixes

- **Fixed: Agent configs now use `127.0.0.1` instead of `localhost`.** All agent base URLs (Claude Code, Codex Desktop, Factory Droid, Grok Build, OMP, OpenCode, Pi, ZCode) default to `127.0.0.1` to avoid IPv6 resolution issues on systems where `localhost` resolves to `::1` first, causing connection failures.

- **Fixed: Stale thinking blocks replayed to Ollama models.** Historical/unsigned thinking blocks from previous assistant turns are now dropped on the Ollama `/api/chat` path, matching CLIProxyAPI's signature-gated behaviour and our own OpenAI path. Previously, every thinking block Claude Code echoed back was replayed, causing models like GLM-5.1 to re-see their own "Let me confirm" reasoning every turn and loop endlessly. A `preserveHistoryThinkingOnOllamaPath` flag is available to restore the old keep-last behaviour for models that require it.

- **Fixed: Tool-call IDs lost on Ollama path.** Anthropic `tool_use` block IDs are now preserved as `id` on the outbound `tool_call` and as `tool_call_id` on the corresponding `tool_result` message. Without this correlation, OpenAI-compatible cloud backends (GLM via Ollama Cloud, etc.) reject multi-turn tool conversations. Mirrors Ollama's own `/v1/messages` converter.

- **Fixed: Tool-call arguments delivered as JSON strings.** Some Ollama-compatible upstreams (e.g. GLM via certain routers) deliver the `arguments` field as a JSON string instead of an object. A custom `UnmarshalJSON` on `OllamaToolCallFunction` now detects and unwraps string-encoded arguments so downstream clients (Claude Code) receive proper objects instead of stringified blobs that fail validation and trigger infinite retry loops.

- **Fixed: Empty assistant turns injected into conversation history.** An assistant turn that becomes empty after stripping stale/unsigned thinking is now dropped entirely, preventing content-less messages from polluting the conversation context.

- **Fixed: Streaming text→thinking block transition (ollama/ollama#17101).** When a model emits text tokens before a thinking block, the open text block is now properly closed and the content-block index bumped before the thinking block starts. Previously this produced an invalid `text → thinking` sequence that confused clients' block accumulators. A `thinkingDone` guard also prevents thinking from re-opening after text/tool content has followed thinking.

- **Fixed: `input_tokens` reported as 0 in streaming `message_start`.** The `message_start` usage now includes an `input_tokens` estimate (request body size / 4) so Claude Code can track context-window usage and trigger auto-compaction. Previously, reporting 0 hid a growing context, causing turns to balloon past 100k tokens and the model to drown into a tool-call loop.

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