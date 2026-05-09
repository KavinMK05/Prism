# API

Prism exposes several HTTP endpoints for LLM API translation. All endpoints accept JSON request bodies and return JSON responses (or SSE for streaming).

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/v1/messages` | `x-api-key` header | Anthropic Messages API translation |
| `POST` | `/v1/chat/completions` | `Authorization: Bearer` | OpenAI Chat Completions API translation |
| `POST` | `/v1/responses` | `Authorization: Bearer` | OpenAI Responses API translation |
| `GET` | `/v1/models` | `Authorization: Bearer` | List available models |
| `GET` | `/health` | None | Health check |
| `GET` | `/v1/stats` | `x-api-key` header | Live request statistics |
| `POST` | `/v1/messages/count_tokens` | `x-api-key` header | Returns 404 (not supported) |
| `GET` | `/` | None | Service info |

## Authentication

- **Anthropic endpoint** (`/v1/messages`): validates the `x-api-key` header against the proxy API key (default: `prism`)
- **OpenAI endpoints** (`/v1/chat/completions`, `/v1/responses`, `/v1/models`): validates `Authorization: Bearer <key>` header
- **Health endpoint** (`/health`): no authentication required
- **Admin endpoints** (`/admin/*`): served on a separate port (8765), no authentication

Missing or invalid API keys return 401 errors in the appropriate format.

## Anthropic Messages API

**Endpoint**: `POST /v1/messages`
**Auth header**: `x-api-key`

Accepts standard Anthropic Messages format requests. Supported fields:

- `model` (string) — model name (remapped via model remapping)
- `max_tokens` (int) — maximum output tokens
- `messages` (array) — conversation messages with content blocks
- `system` (string or array) — system prompt
- `stream` (bool) — enable SSE streaming
- `temperature` / `top_p` / `top_k` — generation parameters
- `stop_sequences` (array) — custom stop sequences
- `tools` (array) — tool definitions
- `thinking` (object) — thinking/reasoning configuration

## OpenAI Chat Completions API

**Endpoint**: `POST /v1/chat/completions`
**Auth header**: `Authorization: Bearer`

Accepts standard OpenAI Chat Completions format. Supported fields:

- `model` (string)
- `messages` (array) — with role, content, tool_calls
- `stream` (bool)
- `max_tokens`, `temperature`, `top_p` — generation parameters
- `tools` (array) — tool definitions
- `response_format` — structured output
- `reasoning_effort` — thinking mode enablement

## OpenAI Responses API

**Endpoint**: `POST /v1/responses`
**Auth header**: `Authorization: Bearer`

Accepts the newer OpenAI Responses API format. Supported fields:

- `model` (string)
- `input` (string or array) — input with items (message, function_call, function_call_output, reasoning)
- `instructions` (string or array) — system prompt
- `stream` (bool)
- `tools` (array) — function tools only (built-in tools filtered for Ollama upstreams)
- `reasoning` (string or object) — reasoning configuration
- `text.format` — structured output configuration
- `temperature`, `top_p`, `max_output_tokens`
