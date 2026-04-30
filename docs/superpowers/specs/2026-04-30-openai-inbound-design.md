# OpenAI Inbound API Support Design

## Overview

Add OpenAI API compatibility to Prism so that any client using the OpenAI Python/JS SDK (Cursor, Continue, Aider, etc.) can connect to the proxy and reach the configured backend (Ollama or OpenAI-compatible).

**Approach B: Direct Backend Routing** — Four independent pipelines with direct translation per (inbound, backend) combination. No intermediate canonical format.

## Architecture

```
                        ┌─ Anthropic→Ollama ─── /api/chat ─── OllamaNDJSON→AnthropicSSE
                        │                        (existing)
Client ──/v1/messages──→│
  (Anthropic)          ├─ Anthropic→OpenAI ─── /v1/chat/completions ─── OpenAISSE→AnthropicSSE
                        │                        (existing)
                        │
Client ──/v1/chat/─────→├─ OpenAI→Ollama ────── /api/chat ─── OllamaNDJSON→OpenAISSE
  completions           │                        (NEW)
  (OpenAI)              │
                        └─ OpenAI→OpenAI ────── /v1/chat/completions ─── OpenAI SSE pass-through
                                                 (NEW - near pass-through)
```

## Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/v1/chat/completions` | POST | OpenAI Chat Completions (non-streaming + streaming) |
| `/v1/models` | GET | List available models |
| `/v1/messages` | POST | Existing Anthropic endpoint (unchanged) |
| `/v1/messages/count_tokens` | POST | Returns 404 (unchanged) |
| `/health` | GET | Health check (unchanged) |
| `/` | GET | Root status (unchanged) |

## Request Translation

### OpenAI Inbound → Ollama Backend

**Request (`OpenAIChatRequest` → `OllamaChatRequest`):**
- `model` → remapped via `getEffectiveModel()`, then passed through
- `messages` → role-by-role translation:
  - `system` → `OllamaMessage{Role: "system"}`
  - `user` → `OllamaMessage{Role: "user"}` — content as string; if multitype array (text + image_url), concatenate text parts, extract base64 data from `image_url` entries
  - `assistant` → `OllamaMessage{Role: "assistant"}` — text content + `tool_calls` mapped to `OllamaToolCall[]`
  - `tool` → `OllamaMessage{Role: "tool"}` — content stringified
- `tools` → `OllamaTool[]` with `OllamaToolFunc`
- `temperature`, `top_p`, `max_tokens` → `OllamaOptions`
- `stream` → passed through

**Response (`OllamaChatResponse` → `OpenAIChatResponse`):**
- Generate: `id` (e.g. `chatcmpl-<model>`), `object: "chat.completion"`, `model`, `choices[0]`
- `ollama.Message.Content` → `choice.message.content`
- `ollama.Message.Thinking` → `choice.message.reasoning_content`
- `ollama.Message.ToolCalls` → `choice.message.tool_calls[]` with string-serialized arguments
- `ollama.DoneReason` → `finish_reason`: `"stop"`, `"length"`, `"tool_calls"`
- `ollama.PromptEvalCount/EvalCount` → `usage`

### OpenAI Inbound → OpenAI Backend (pass-through)

**Request:** Near pass-through — model remapping applied, then forwarded as-is to upstream `/v1/chat/completions`.

**Response:** Near pass-through — response forwarded back as-is.

### Streaming

| Path | Transformation |
|------|---------------|
| OpenAI→Ollama streaming | Read Ollama NDJSON chunks → emit OpenAI SSE (`data: {...}\n\n` with `object: "chat.completion.chunk"`) |
| OpenAI→OpenAI streaming | Pass-through — read upstream SSE, forward to client byte-by-byte |

## `/v1/models` Endpoint

Returns known models from `ModelRemapping.KnownModels` plus alias targets:

```json
{
  "object": "list",
  "data": [
    {"id": "glm-5.1:cloud", "object": "model", "created": 0, "owned_by": "ollama-proxy"},
    {"id": "deepseek-v4-flash:cloud", "object": "model", "created": 0, "owned_by": "ollama-proxy"}
  ]
}
```

## Error Handling

OpenAI inbound endpoint returns **OpenAI error format**:

```json
{
  "error": {
    "message": "Invalid or missing API key",
    "type": "authentication_error",
    "code": 401
  }
}
```

Existing Anthropic endpoint continues returning Anthropic error format.

| Scenario | OpenAI Error Type | HTTP Code |
|----------|-------------------|-----------|
| Missing/invalid API key | `authentication_error` | 401 |
| Wrong HTTP method | `invalid_request_error` | 405 |
| Malformed request body | `invalid_request_error` | 400 |
| Upstream unavailable | `server_error` | 502 |
| Upstream returned error | `server_error` | upstream code |

## Authentication

Same API key check as existing endpoint. OpenAI SDK clients typically send `Authorization: Bearer <key>` — this already works with existing `authMiddleware`. The `x-api-key` header is also accepted.

## Unsupported OpenAI Parameters

Silently ignored (not passed upstream):
- `n` (multiple completions) — always generate 1
- `logprobs`
- `response_format`
- `seed`
- `frequency_penalty` / `presence_penalty`

## File Organization

**New files:**
- `openai_inbound.go` — OpenAI inbound handler, non-streaming translation functions for both backend paths
- `openai_inbound_streaming.go` — OpenAI inbound streaming handlers (Ollama NDJSON → OpenAI SSE, and OpenAI SSE pass-through)

**Modified files:**
- `main.go` — Add route registrations for `/v1/chat/completions` and `/v1/models`, add `writeOpenAIError()` helper
- `models.go` — Add OpenAI error types (`OpenAIErrorResponse`, `OpenAIErrorDetail`), add `Stop` field to `OllamaOptions` for stop sequences

**Untouched files:**
- `proxy.go`, `streaming.go`, `openai.go`, `openai_streaming.go`, `config.go`, `tray.go`

## New Functions

### `openai_inbound.go`

- `HandleOpenAIChatCompletions()` — main handler, dispatches to Ollama or OpenAI backend
- `handleOpenAIInboundToOllama()` — non-streaming OpenAI→Ollama path
- `handleOpenAIInboundToOpenAI()` — non-streaming OpenAI→OpenAI pass-through
- `translateOpenAIToOllama()` — OpenAIChatRequest → OllamaChatRequest
- `translateOllamaToOpenAI()` — OllamaChatResponse → OpenAIChatResponse
- `translateOpenAIMessagesToOllama()` — message-level translation
- `translateOpenAIToolsToOllama()` — tool definition translation
- `HandleModels()` — GET /v1/models handler
- `writeOpenAIError()` — error response in OpenAI format

### `openai_inbound_streaming.go`

- `handleOpenAIInboundOllamaStreaming()` — Ollama NDJSON → OpenAI SSE chunks
- `handleOpenAIInboundOpenAIStreaming()` — OpenAI SSE pass-through

## Backward Compatibility

Zero changes to existing Anthropic pipeline. The existing `/v1/messages` endpoint, error format, streaming, and behavior are completely untouched. New endpoints are purely additive.