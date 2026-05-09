# Getting started

## Prerequisites

- Windows 10 or later
- An account with at least one upstream provider (Ollama Cloud, OpenCode Go, or an OpenAI-compatible API)
- No Python or runtime dependencies required

## Download

Download the latest `prism.exe` from the [releases page](https://github.com/KavinMK05/Prism/releases). The binary is ~5 MB and self-contained.

Alternatively, build from source:

```powershell
go-winres make; go build -ldflags="-H windowsgui" -o prism.exe .
```

## Run

Double-click `prism.exe` or run from a terminal:

```powershell
.\prism.exe
```

Prism starts:
- Proxy server on `http://127.0.0.1:11434`
- Admin web UI on `http://127.0.0.1:8765/admin`
- System tray icon appears in the notification area

## Configure your provider

1. Right-click the system tray icon and select **Open Settings** (or open `http://127.0.0.1:8765/admin`)
2. Go to the **Provider** tab
3. Select your upstream provider:
   - **Ollama Cloud** — default, uses `https://ollama.com`
   - **OpenCode Go** — uses `https://opencode.ai/zen/go`
   - **Custom provider** — any OpenAI-compatible API (OpenRouter, Groq, Together AI, etc.)
   - **Codex OAuth** — sign in with your OpenAI account (no API key)
4. Enter the API key for the selected provider (not needed for OAuth)
5. Changes are saved automatically and the proxy restarts

## Connect your tools

### Claude Desktop

Edit your Claude Desktop config to use Prism as a gateway:

```json
{
  "inferenceProvider": "gateway",
  "inferenceGatewayBaseUrl": "http://127.0.0.1:11434",
  "inferenceGatewayApiKey": "prism",
  "inferenceModels": [
    { "name": "glm-5.1:cloud" },
    { "name": "deepseek-v4-pro:cloud", "supports1m": true }
  ]
}
```

### Claude Code

Set environment variables or edit `~/.claude/settings.json`:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:11434",
    "ANTHROPIC_AUTH_TOKEN": "prism",
    "ANTHROPIC_API_KEY": ""
  }
}
```

### Cursor / Continue / other OpenAI clients

Point your client to `http://127.0.0.1:11434/v1` with any API key (e.g., `prism`).

### OpenAI SDK (Responses API)

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:11434/v1",
    api_key="prism"
)

response = client.responses.create(
    model="glm-5.1:cloud",
    input="Hello!",
    stream=True
)
```

## Verify

```powershell
# Anthropic endpoint
Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/messages" -Method POST `
  -ContentType "application/json" `
  -Headers @{"x-api-key"="prism"} `
  -Body '{"model":"glm-5.1:cloud","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'

# OpenAI Chat Completions
Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/chat/completions" -Method POST `
  -ContentType "application/json" `
  -Headers @{"Authorization"="Bearer prism"} `
  -Body '{"model":"glm-5.1:cloud","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'

# Health check
Invoke-RestMethod -Uri "http://127.0.0.1:11434/health"
```

## Logs

Logs are written to `%APPDATA%\prism\proxy.log`. View them from the system tray (**Show Logs**) or the admin UI (**Logs** tab).
