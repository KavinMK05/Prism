# Configuration

## Config file

Configuration is stored as JSON in `%APPDATA%\prism\config.json`:

```json
{
  "active_provider": "ollama_cloud",
  "ollama_cloud": {
    "id": "ollama_cloud",
    "name": "Ollama Cloud",
    "base_url": "https://ollama.com",
    "api_key": ""
  },
  "opencode_go": {
    "id": "opencode_go",
    "name": "OpenCode Go",
    "base_url": "https://opencode.ai/zen/go",
    "api_key": ""
  },
  "custom_providers": [
    {
      "id": "custom_openrouter_abc123",
      "name": "OpenRouter",
      "base_url": "https://openrouter.ai/api/v1",
      "api_key": ""
    }
  ],
  "oauth_accounts": [
    {
      "id": "codex_user_abc123",
      "provider": "codex",
      "label": "Codex",
      "email": "user@example.com",
      "access_token": "...",
      "refresh_token": "...",
      "expires_at": 1234567890,
      "plan_tier": "plus",
      "active": true
    }
  ]
}
```

## Model remapping file

Stored in `%APPDATA%\prism\model_remapping.json`:

```json
{
  "default_model": "glm-5.1:cloud",
  "known_models": [
    "glm-5.1:cloud",
    "deepseek-v4-flash:cloud"
  ],
  "aliases": {
    "claude-3-5-haiku": "deepseek-v4-flash:cloud"
  }
}
```

## Provider type inference

The provider type is determined by the `active_provider` field:

| Active provider | Provider type | Upstream format |
|---|---|---|
| `ollama_cloud` | `ollama` | Ollama `/api/chat` |
| `opencode_go` | `openai` | OpenAI `/v1/chat/completions` |
| Any custom provider | `openai` | OpenAI `/v1/chat/completions` |
| Any OAuth account | `codex` | OpenAI `/v1/chat/completions` (via Codex tokens) |

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `PRISM_PORT` | `11434` | Proxy server port |
| `PRISM_HOST` | `127.0.0.1` | Proxy bind address |
| `PRISM_ADMIN_PORT` | `8765` | Admin UI port |
| `OLLAMA_API_KEY` | — | Fallback API key for Ollama Cloud |
| `OPENCODE_GO_API_KEY` | — | Fallback API key for OpenCode Go |
