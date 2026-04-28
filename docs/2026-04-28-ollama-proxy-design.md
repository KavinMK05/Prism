# ollama-proxy: Lightweight Anthropic-to-Ollama API Proxy

**Date:** 2026-04-28

## Problem

Claude Desktop requires a localhost base URL for its API endpoint (`http://127.0.0.1:PORT`). It cannot connect directly to `https://ollama.com`. Additionally, Claude Desktop communicates using the Anthropic Messages API format (`/v1/messages`), while Ollama's cloud API at `https://ollama.com/api/chat` uses the Ollama native API format.

Currently, a heavy Python-based LiteLLM proxy is required to bridge this gap. The goal is to replace it with a lightweight Go binary.

## Solution

A Go binary (`ollama-proxy`) that:

1. Listens on `127.0.0.1:11434`
2. Accepts Anthropic Messages API requests (`POST /v1/messages`)
3. Translates them to Ollama `/api/chat` format
4. Forwards to `https://ollama.com/api/chat` with Bearer authentication
5. Translates responses back to Anthropic Messages API format
6. Handles SSE streaming for real-time token-by-token output

```
Claude Desktop
    ↓ Anthropic Messages API (/v1/messages)
ollama-proxy @ 127.0.0.1:11434
    ↓ Ollama Native API (/api/chat) + Bearer auth
https://ollama.com/api/chat
```

## Architecture

### Single Go Binary

- ~5MB on disk, ~5-10MB RAM
- No runtime dependencies
- Fast startup (< 100ms)

### Request Flow

1. Claude Desktop sends `POST /v1/messages` to `127.0.0.1:11434`
2. Proxy parses Anthropic-format request body
3. Proxy translates to Ollama `/api/chat` request format
4. Proxy forwards to `https://ollama.com/api/chat` with `Authorization: Bearer <OLLAMA_API_KEY>`
5. Proxy receives Ollama response (streaming or non-streaming)
6. Proxy translates response back to Anthropic format
7. Proxy returns Anthropic-format response to Claude Desktop

### Translation Details

#### Anthropic → Ollama Request Mapping

| Anthropic Field | Ollama Field | Notes |
|---|---|---|
| `model` | `model` | Pass through (e.g., `glm-5.1:cloud`) |
| `messages` | `messages` | Translate content blocks to string or array format |
| `system` | `messages[].role=system` | Inject as first system message |
| `max_tokens` | `options.num_predict` | Map to Ollama options |
| `temperature` | `options.temperature` | Map to Ollama options |
| `top_p` | `options.top_p` | Map to Ollama options |
| `top_k` | `options.top_k` | Map to Ollama options |
| `stream` | `stream` | Pass through |
| `stop_sequences` | `options.stop` | Map to Ollama options |
| `tools` | `tools` | Translate Anthropic tool schema to Ollama format |
| `thinking` | `think` | Map thinking config |

#### Message Content Block Translation

- Anthropic `text` content block → Ollama string content
- Anthropic `image` content block (base64) → Ollama `images[]` field
- Anthropic `tool_use` content block → Ollama `tool_calls` in response
- Anthropic `tool_result` content block → Ollama `tool` message format

#### Ollama → Anthropic Response Mapping

| Ollama Field | Anthropic Field | Notes |
|---|---|---|
| `message.content` | `content[0].text` | Wrap in content block array |
| `message.tool_calls` | `content[].tool_use` | Translate to Anthropic tool_use blocks |
| `done_reason` | `stop_reason` | Map: `stop` → `end_turn`, `length` → `max_tokens`, tool call → `tool_use` |
| `model` | `model` | Pass through |
| `prompt_eval_count` | `usage.input_tokens` | Approximate |
| `eval_count` | `usage.output_tokens` | Count tokens |

#### Streaming SSE Translation

Ollama streams newline-delimited JSON. The proxy translates each chunk into Anthropic SSE events:

```
Ollama stream:               Anthropic SSE events:
─────────────                ──────────────────────
first chunk           →      event: message_start
                              event: content_block_start
text delta            →      event: content_block_delta (text_delta)
...                          ...
last chunk (done)     →      event: content_block_stop
                              event: message_delta (stop_reason)
                              event: message_stop
```

For tool calls during streaming:
```
Ollama stream:               Anthropic SSE events:
─────────────                ──────────────────────
tool_call delta       →      event: content_block_start (tool_use)
                              event: content_block_delta (input_json_delta)
                              event: content_block_stop
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `OLLAMA_API_KEY` | (required) | API key for ollama.com |
| `OLLAMA_PROXY_PORT` | `11434` | Port to listen on |
| `OLLAMA_PROXY_HOST` | `127.0.0.1` | Host to bind to |
| `OLLAMA_UPSTREAM_URL` | `https://ollama.com` | Ollama cloud URL |

### Model Selection

Claude Desktop has a built-in model picker UI configured via:

**File:** `C:\Users\Kavin\AppData\Local\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude-3p\configLibrary\a8494411-6831-45fb-b975-04160105234a.json`

```json
{
  "inferenceProvider": "gateway",
  "inferenceGatewayBaseUrl": "http://127.0.0.1:11434",
  "inferenceGatewayApiKey": "ollama",
  "inferenceModels": [
    { "name": "glm-5.1:cloud" },
    { "name": "deepseek-v4-pro:cloud", "supports1m": true }
  ]
}
```

To add a new model, add an entry to `inferenceModels` and it appears in Claude Desktop's model dropdown. The proxy passes whatever model name Claude Desktop sends straight through to Ollama — no hardcoded models.

### Claude Code Settings

**`C:\Users\Kavin\.claude\settings.json`:**

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:11434",
    "ANTHROPIC_AUTH_TOKEN": "ollama",
    "ANTHROPIC_API_KEY": ""
  }
}
```

This file also contains `permissions.deny` and `disallowedTools` to block the built-in `WebSearch`/`WebFetch` tools (configured separately).

### Auto-Start on Login

The proxy must be running before Claude Desktop starts. Auto-start via Windows Startup folder:

**File:** `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\ollama-proxy.bat`

```bat
@echo off
start /B "" "C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\ollama-proxy.exe" >> "%APPDATA%\ollama-proxy\proxy.log" 2>&1
```

This runs the proxy in the background on login, logging to `%APPDATA%\ollama-proxy\proxy.log`. No admin rights required.

### Running Manually

```bash
$env:OLLAMA_API_KEY = "your-key"
.\ollama-proxy.exe
```

## File Structure

```
C:\Users\Kavin\Documents\Personal Projects\Ollama proxy\
├── main.go              # Entry point, config, server setup
├── proxy.go             # Core request/response translation
├── streaming.go         # SSE stream translation
├── models.go            # Request/response type definitions
├── go.mod
├── go.sum
├── ollama-proxy.exe     # Compiled binary (after build)
└── docs/
    └── 2026-04-28-ollama-proxy-design.md
```

## What It Does NOT Support (YAGNI)

These Anthropic API features are not supported by Ollama and will be handled gracefully:

- `/v1/messages/count_tokens` → Returns 404
- `tool_choice` → Ignored (Ollama doesn't support forcing specific tools)
- `metadata` → Ignored
- Prompt caching → Not applicable
- Batches API → Not applicable
- PDF support → Not supported
- Server-sent errors during streaming → Errors return as HTTP status codes
- URL images → Only base64 images supported (Ollama limitation)

## Error Handling

- **Upstream errors**: Proxy returns Anthropic-format error responses with appropriate HTTP status codes
- **Network failures**: Returns 502 Bad Gateway with descriptive error message
- **Invalid requests**: Returns 400 Bad Request with problem description
- **Missing API key**: Returns 401 Unauthorized
- **Streaming interruption**: Sends `event: error` SSE event then closes

## Verification

After deployment:
1. Start proxy: `$env:OLLAMA_API_KEY = "your-key"; .\ollama-proxy.exe`
2. Test non-streaming: `Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/messages" -Method POST -ContentType "application/json" -Body '{"model":"glm-5.1:cloud","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'`
3. Test streaming: Same request with `"stream": true`
4. Update Claude Desktop configLibrary to point to `http://127.0.0.1:11434`
5. Open Claude Desktop and verify it works