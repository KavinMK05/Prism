# Glossary

| Term | Definition |
|---|---|
| **Anthropic Messages API** | The Claude API format at `/v1/messages`. Prism accepts this format and translates it to the upstream provider's format. |
| **Codex** | OpenAI's internal code generation service. Prism supports connecting via OAuth to use Codex API tokens as an upstream provider. |
| **Content block** | A typed element in an API message (text, thinking, tool_use, tool_result, image). Prism translates between different block formats. |
| **Custom provider** | Any OpenAI-compatible API endpoint configured by the user (OpenRouter, Groq, Together AI, etc.). |
| **Done reason** | An Ollama response field indicating why generation stopped (`stop`, `length`, `tool_call`). Prism maps these to the appropriate field in the target format. |
| **Finish reason** | An OpenAI response field (`stop`, `length`, `tool_calls`). |
| **Model remapping** | The system for rewriting model names between client and provider using aliases, a known model whitelist, and a default fallback model. |
| **OAuth account** | An OpenAI account connected via OAuth PKCE flow. Prism uses the account's access token instead of an API key. |
| **Ollama** | An open-source LLM server. Prism translates to/from Ollama's native `/api/chat` format. |
| **OpenAI Chat Completions** | The standard OpenAI API format at `/v1/chat/completions`. |
| **OpenAI Responses API** | The newer OpenAI API format at `/v1/responses` with structured output items (messages, function calls, reasoning). |
| **PKCE** | Proof Key for Code Exchange — an OAuth 2.0 flow Prism uses for secure Codex authentication. |
| **Provider type** | The upstream API format: `ollama` (Ollama native), `openai` (OpenAI Chat Completions), or `codex` (OpenAI Responses via Codex). |
| **Proxy process** | The child OS process (`prism.exe --serve`) that handles API translation. |
| **SSE** | Server-Sent Events — the streaming protocol used for real-time API responses. |
| **Stop reason** | An Anthropic response field (`end_turn`, `max_tokens`, `tool_use`). |
| **Tray process** | The main OS process that manages the system tray icon, admin UI, and proxy process lifecycle. |
