# Admin web UI

Active contributors: KavinMK05

## Purpose

Prism includes a built-in web-based admin interface for managing configuration without editing JSON files by hand. It runs on `http://127.0.0.1:8765/admin` (configurable via `PRISM_ADMIN_PORT`).

## Architecture

The admin UI is served by an HTTP server embedded in the tray process (`admin.go`). The HTML UI is embedded using Go's `embed` directive:

```go
//go:embed admin.html
//go:embed docs/icon.png
var adminFS embed.FS
```

The admin server starts in `startAdminServer` when the tray process initializes. It runs on a separate goroutine and shares the config via `adminMu`-protected global variables.

## API endpoints

| Endpoint | Methods | Purpose |
|---|---|---|
| `/admin` | GET | Serve the admin HTML page |
| `/admin/icon.png` | GET | Serve the brand icon |
| `/admin/config` | GET, PUT | Read/write provider configuration |
| `/admin/model-remap` | GET, PUT | Read/write model remapping |
| `/admin/status` | GET | Check if proxy is running |
| `/admin/proxy/start` | POST | Start the proxy process |
| `/admin/proxy/stop` | POST | Stop the proxy process |
| `/admin/proxy/restart` | POST | Restart the proxy process |
| `/admin/autostart` | GET, PUT | Toggle Windows auto-start |
| `/admin/logs` | GET | Last 200 lines of proxy log |
| `/admin/stats` | GET | Forwarded from proxy's `/v1/stats` |
| `/admin/oauth/login` | POST | Start Codex OAuth flow |
| `/admin/oauth/accounts` | GET | List OAuth accounts (with masked tokens) |
| `/admin/oauth/accounts/remove` | POST | Remove an OAuth account |
| `/admin/oauth/accounts/activate` | POST | Activate an OAuth account |
| `/admin/oauth/usage` | GET | Get cached usage for an account |
| `/admin/oauth/usage/refresh` | POST | Refresh usage data for an account |

## Config preservation

The PUT handler for `/admin/config` preserves API keys that are sent as empty strings — it only overwrites keys when a non-empty value is provided. This prevents the admin UI from inadvertently clearing stored API keys when only other settings are changed.

## Key source files

| File | Purpose |
|---|---|
| `admin.go` | All admin server setup, API handlers, and config management |
| `admin.html` | Embedded admin UI HTML (embedded at build time) |
