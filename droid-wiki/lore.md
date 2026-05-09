# Lore

## The Prism project (April–May 2026)

Prism was created in April 2026 as a lightweight Windows-native alternative to LiteLLM for translating between different LLM API formats. The project was motivated by the need for a single, zero-dependency binary that Windows users could drop onto any machine and run immediately.

### Era 1: Foundation (Apr 2026)

The initial codebase established the core translation engine: Anthropic to Ollama (`proxy.go`), Anthropic to OpenAI (`openai.go`), and the streaming state machine (`streaming.go`). The first version supported non-streaming and streaming translation for the Anthropic Messages API endpoint.

### Era 2: OpenAI inbound support (Apr 2026)

The OpenAI Chat Completions inbound path was added (`openai_inbound.go`, `openai_inbound_streaming.go`), allowing tools like Cursor and Continue to use Prism as an OpenAI-compatible proxy. This era also added the system tray integration (`tray.go`) and admin web UI (`admin.go`).

### Era 3: Responses API and OAuth (May 2026)

The OpenAI Responses API endpoint was added (`responses_inbound.go`, `responses_streaming.go`, `responses_request.go`, `responses_response.go`), along with full Codex OAuth support (`oauth.go`, `oauth_codex.go`) and usage tracking (`usage.go`).

## Longest-standing features

- **Anthropic to Ollama translation** — the original translation path, present since the first commit
- **Model remapping** — `getEffectiveModel` and alias resolution
- **System tray integration** — `runTray` and menu handling

## Deprecated features

- The old single `custom` provider format was migrated to a `custom_providers` array in `config.go`. The `loadConfig` function includes a migration path that converts the old format to the new one.

## Growth trajectory

- **Apr 24**: ~8 source files (core translation + system tray)
- **May 9**: 19 source files (all translation paths + OAuth + admin UI + stats)
- From a single translation path to 6 routing paths with 2 authentication models
