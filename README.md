<h1 align="center">Prism</h1>

<p align="center">
  <strong>Featherweight LLM Proxy for Windows</strong><br/>
  9 MB binary. 6 MB RAM. Native system tray. Zero config.<br/>
  Proxy any Anthropic API request to Ollama or OpenAI-compatible backends.
</p>

<p align="center">
  <img src="https://img.shields.io/badge/RAM-6_MB-22c55e?style=flat-square" alt="RAM" />
  <img src="https://img.shields.io/badge/Binary-9_MB-22c55e?style=flat-square" alt="Binary" />
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go" alt="Go" />
  <img src="https://img.shields.io/badge/Platform-Windows-0078D4?style=flat-square&logo=windows" alt="Platform" />
  <img src="https://img.shields.io/badge/License-MIT-green?style=flat-square" alt="License" />
</p>

---

## Why Prism?

You want to use **Claude Desktop**, **Claude Code**, or any Anthropic-compatible client with your own LLM backend — Ollama, OpenCode, or any OpenAI-compatible API. You need a **simple proxy** that translates the Anthropic Messages API into formats your backend understands, with a **GUI to manage it** without touching config files or terminals.

Prism is a **single 9 MB executable** that runs in your system tray, uses barely 6 MB of RAM, and just works. No Python, no Docker, no Node.js, no 500 MB of dependencies.

### Features

- **Featherweight** — 9 MB binary, 6 MB RAM. Compiles to a single static executable with no runtime dependencies
- **System Tray GUI** — Native Windows tray icon to start/stop the proxy, switch providers, set API keys, and edit model config
- **Multi-Provider** — Switch between Ollama Cloud, OpenCode Go, or any custom OpenAI-compatible endpoint in one click
- **Run on Startup** — Lives in your system tray, starts with Windows, always ready
- **Full Anthropic API** — Translates the Anthropic Messages API (including streaming, tool use, thinking blocks, and images) to Ollama `/api/chat` or OpenAI `/v1/chat/completions`
- **SSE Streaming** — Real-time Server-Sent Events streaming for both Ollama and OpenAI backends
- **Model Remapping** — Automatically redirect hardcoded model names (like `claude-3-5-haiku`) to your preferred model via a simple JSON config
- **Zero Config** — Sensible defaults out of the box. Configure only what you need

---

## How It Compares

| | **Prism** | **LiteLLM** | **Bifrost** | **Portkey** | **OpenRouter** |
|---|---|---|---|---|---|
| **Binary size** | 9 MB | ~200 MB+ (Python) | ~30 MB | Cloud only | Cloud only |
| **RAM usage** | ~6 MB | ~300 MB+ | ~50 MB | N/A | N/A |
| **Language** | Go | Python | Go | Node.js | — |
| **Desktop GUI** | Native system tray | CLI only | CLI only | Web dashboard | Web dashboard |
| **Self-hosted** | Yes | Yes | Yes | Optional | No |
| **Windows native** | Yes (.exe, tray) | Yes (Python) | Yes | No | No |
| **Run on startup** | Tray icon, zero config | Manual setup | Manual setup | N/A | N/A |
| **Provider switching** | One-click in tray | Edit config file | Edit config file | Web UI | Web UI |
| **Model remapping** | JSON config + tray editor | Model aliases | Config file | Guardrails | Auto-routing |
| **Anthropic → Ollama** | Built-in | Via config | Via config | No | No |
| **Anthropic → OpenAI** | Built-in | Via config | Via config | Via config | No |
| **Setup time** | Double-click | pip install + config | Build + config | Sign up + config | Sign up |
| **Use case** | Personal desktop proxy | Team/production gateway | Production gateway | Production observability | Quick multi-model access |

**Prism is not an enterprise gateway.** It's a personal desktop tool for developers who want their LLM proxy to be as invisible as possible — sitting in the tray, sipping RAM, and staying out of the way.

If you need production features like rate limiting, observability, fallback routing, or multi-user access, reach for [LiteLLM](https://github.com/BerriAI/litellm), [Bifrost](https://github.com/bifrost-io/bifrost), or [Portkey](https://portkey.ai/). If you just want to point Claude Desktop at your Ollama server and forget about it — that's Prism.

---

## Quick Start

### Build

```bash
go build -ldflags="-H windowsgui" -o prism.exe .
```

### Run

Double-click `prism.exe` — it appears in your system tray and starts the proxy automatically.

Or run as a console server:

```bash
prism.exe --serve
```

The proxy listens on `http://127.0.0.1:11434` by default.

---

## System Tray

<p align="center">
  <img src="https://img.shields.io/badge/Menu_Item-Action-blue?style=flat-square" alt="Menu" />
</p>

| Menu Item | What It Does |
|-----------|-------------|
| **Start / Stop / Restart** | Control the proxy server |
| **Provider** | Switch between Ollama Cloud, OpenCode Go, or Custom |
| **Edit Model Config** | Open `model_remapping.json` in Notepad — proxy restarts on save |
| **Open Folder** | Open the config directory in Explorer |
| **Show Logs** | Live tail of proxy logs in a terminal |
| **Set API Key** | Set the API key for the active provider |
| **Quit** | Stop proxy and exit |

---

## Setting Up with Claude Desktop

1. Start Prism and pick your provider from the tray menu
2. Open your Claude Desktop config (`developer_settings.json` or the config editor) and set:

```json
{
  "inferenceProvider": "gateway",
  "inferenceGatewayBaseUrl": "http://127.0.0.1:11434",
  "inferenceGatewayApiKey": "prism",
  "inferenceModels": [
    { "name": "glm-5.1:cloud" },
    { "name": "deepseek-v4-flash:cloud", "supports1m": true }
  ]
}
```

3. Done. Prism translates Anthropic Messages API calls to your backend format automatically.

---

## Model Remapping

AI clients like Claude Code hardcode model names for subagents (e.g. `claude-3-5-haiku`). When you're proxying through a non-Anthropic backend, those requests fail. Prism solves this with a simple JSON config.

Click **Edit Model Config** in the tray to open `%APPDATA%\prism\model_remapping.json`:

```json
{
  "default_model": "glm-5.1:cloud",
  "known_models": [
    "glm-5.1:cloud",
    "deepseek-v4-flash:cloud",
    "deepseek-v4-flash"
  ],
  "aliases": {
    "claude-3-5-haiku": "deepseek-v4-flash:cloud",
    "claude-3-5-haiku-20241022": "deepseek-v4-flash:cloud"
  }
}
```

| Priority | Rule | Example |
|----------|------|---------|
| 1 | **Exact alias** | `claude-3-5-haiku` → `deepseek-v4-flash:cloud` |
| 2 | **Known model** | `glm-5.1:cloud` passes through unchanged |
| 3 | **Default fallback** | Any unknown model → `glm-5.1:cloud` |

After saving the file, the proxy restarts automatically to apply changes.

---

## Configuration

### Provider Config — `%APPDATA%\prism\config.json`

```json
{
  "active_provider": "ollama_cloud",
  "ollama_cloud": {
    "name": "Ollama Cloud",
    "base_url": "https://ollama.com",
    "api_key": ""
  },
  "opencode_go": {
    "name": "OpenCode Go",
    "base_url": "https://opencode.ai/zen/go",
    "api_key": ""
  },
  "custom": {
    "name": "Custom",
    "base_url": "",
    "api_key": ""
  }
}
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `OLLAMA_PROXY_PORT` | `11434` | Port to listen on |
| `OLLAMA_PROXY_HOST` | `127.0.0.1` | Host to bind to |
| `OLLAMA_API_KEY` | — | Fallback API key for Ollama Cloud |
| `OPENCODE_GO_API_KEY` | — | Fallback API key for OpenCode Go |

---

## Architecture

```
┌─────────────────┐                    ┌─────────┐                   ┌──────────────┐
│  Claude Desktop │  Anthropic API     │  Prism   │  Ollama / OpenAI  │  Your LLM    │
│  Claude Code    │  ───────────────►  │  Proxy   │  ──────────────► │  Backend     │
│  Any client     │  /v1/messages       │  (9 MB)  │                  │              │
└─────────────────┘                    └─────────┘                  └──────────────┘
```

Prism translates the Anthropic Messages API format into:
- **Ollama** — `POST /api/chat` with Ollama's chat format
- **OpenAI-compatible** — `POST /v1/chat/completions` with OpenAI's chat format

Both streaming and non-streaming are supported. Tool use, thinking blocks, and images are translated accordingly.

---

## Project Structure

```
├── main.go               Entry point, CLI flags, HTTP server
├── proxy.go              Core proxy logic, Anthropic → Ollama translation
├── openai.go             Anthropic → OpenAI translation (non-streaming)
├── streaming.go           SSE streaming for Ollama backend
├── openai_streaming.go    SSE streaming for OpenAI backend
├── config.go             Config loading, model remapping, provider management
├── models.go             Request/response types for all API formats
├── tray.go               Windows system tray UI and process management
├── icon.ico              Application icon
└── go.mod                Go module definition
```

---

## License

MIT