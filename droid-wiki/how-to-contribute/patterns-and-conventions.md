# Patterns and conventions

## Code organization

All source files live in the root package (package `main`). The project does not use sub-packages — each file represents a functional area:

- **Entry point**: `main.go` — single-instance guard, server setup, middleware
- **Config**: `config.go` — config file management
- **Proxy logic**: `proxy.go`, `openai.go`, `openai_inbound.go`, `responses_inbound.go`
- **Streaming**: `streaming.go`, `openai_streaming.go`, `openai_inbound_streaming.go`, `responses_streaming.go`
- **Data models**: `models.go`, `responses_models.go`
- **Translation**: `openai.go`, `responses_request.go`, `responses_response.go`
- **UI**: `tray.go`, `admin.go`
- **Auth**: `oauth.go`, `oauth_codex.go`
- **Telemetry**: `stats.go`, `usage.go`
- **Embedded assets**: `admin.html`

## Error handling

API errors follow the format of the inbound API. Two helper functions produce errors in the correct format:

- `writeAnthropicError` — produces errors in Anthropic format (`{type: "error", error: {type, message}}`)
- `writeOpenAIError` — produces errors in OpenAI format (`{error: {message, type, code}}`)

Middleware errors return 401 for missing/invalid API keys. Translation errors return 400 or 502 depending on whether the error is client-side or upstream-side.

## Translation pattern

Every translation path follows the same pattern:

1. Read and validate the inbound request
2. Apply model remapping to the model name
3. Start stats tracking
4. Translate the request to upstream format
5. If streaming, handle via the streaming codepath
6. Send the translated request to the upstream provider
7. Read and translate the upstream response
8. Record stats for the completed request
9. Write the translated response

Translation functions follow `translateXToY` naming (e.g., `translateToOpenAI`, `translateOpenAIToOllama`, `translateChatCompletionsToResponsesAPI`).

## Streaming state management

Streaming uses state structs to track open content blocks across multiple SSE events:

- `streamState` — for Anthropic-formatted SSE (tracks thinking, text, and tool_use blocks)
- `ollamaStreamState` — for OpenAI-formatted SSE (tracks thinking, content accumulation, tool call dedup)

Each state struct manages the lifecycle of content blocks (start → delta → stop) based on the target API format.

## Naming conventions

- Files use snake_case: `openai_inbound.go`, `responses_streaming.go`
- Types use PascalCase: `AnthropicRequest`, `OllamaChatResponse`
- Private functions use camelCase: `translateRequest`, `handleStreaming`
- Constants are PascalCase with the format described: `CREATE_NO_WINDOW`
