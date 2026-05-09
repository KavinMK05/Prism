# Security

## Network exposure

By default, Prism binds to `127.0.0.1`, making it accessible only from the local machine. If `PRISM_HOST` is set to `0.0.0.0`, a warning is logged:

```
WARNING: Proxy is listening on 0.0.0.0 which is accessible from the network.
```

## API key authentication

All LLM API endpoints (except `/health`) require authentication via:

- **`x-api-key` header** — for `/v1/messages` (Anthropic format)
- **`Authorization: Bearer` header** — for `/v1/chat/completions`, `/v1/responses`, `/v1/models`

The proxy API key defaults to `prism`. Changing it is done by editing the `proxyAPIKey` variable in `main.go` and rebuilding.

## Admin UI

The admin UI server binds exclusively to `127.0.0.1` and has no authentication. This is acceptable because admin access is limited to localhost.

## OAuth token security

Access tokens are stored in `%APPDATA%\prism\config.json` with file permissions set to `0600` (owner read/write only). The `showInputDialog` function in `config.go` validates dialog input against shell injection characters (`(){}<>|&;`$`).

## Process isolation

The proxy process is spawned with filtered environment variables to prevent API key leakage. Only the active provider's API key is set via `OLLAMA_API_KEY` (or `OPENCODE_GO_API_KEY`) in the child process environment.

## Custom provider URL validation

Custom provider base URLs are validated against security constraints in `validateBaseURL`:

- Only `http://` and `https://` schemes are accepted
- URLs pointing to private/local IP addresses are rejected with a security warning
- The URL must have a host component

## Missing

- No TLS support — Prism is designed for localhost use only
- No admin UI authentication
- No rate limiting on API endpoints
