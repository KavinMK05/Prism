# Prism overview

Prism is a lightweight (~5 MB) Windows-native proxy server that translates between Anthropic Messages API, OpenAI Chat Completions API, OpenAI Responses API, and Ollama Native API formats in real time. It runs as a system tray application with a built-in web admin UI, supports multiple upstream providers, model remapping, full SSE streaming across all routing paths, tool calling, thinking/reasoning, and image support.

Key capabilities:

- **Protocol translation** — accept requests in any supported format and forward to any supported upstream, translating on the fly
- **Native Windows integration** — system tray icon with full menu, no console window required
- **Built-in admin UI** — web-based configuration at `http://127.0.0.1:8765/admin`
- **Zero dependencies** — single ~5 MB binary, no Python or runtime deps
- **OAuth support** — sign in with your OpenAI account (no API key needed)
- **Model remapping** — alias model names between client and provider

Prism is designed as a lightweight alternative to LiteLLM for Windows users. It starts in under 100 ms, uses ~5-10 MB of memory, and requires no installation beyond dropping the binary somewhere on disk.

## Quick links

- [Architecture](architecture.md) — how the system is structured
- [Getting started](getting-started.md) — install, configure, connect tools
- [API reference](../api/index.md) — endpoint documentation
- [Features](../features/index.md) — deep dives into each capability
