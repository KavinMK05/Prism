# Deployment

## Single binary deployment

Prism is distributed as a single executable (`prism.exe`, ~5 MB). No installation, Python, or runtime dependencies are required.

## Auto-start on Windows

Prism can start automatically at user login via the Windows Registry:

```
HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Prism
```

Toggle this from the admin UI (**Proxy** tab → **Start at Login**) or via the `/admin/autostart` API. No admin rights are required since it modifies the current user's registry hive.

## Port configuration

| Variable | Default | Notes |
|---|---|---|
| `PRISM_PORT` | `11434` | Proxy server port |
| `PRISM_HOST` | `127.0.0.1` | Bind address. Use `0.0.0.0` for network access (with a warning logged) |
| `PRISM_ADMIN_PORT` | `8765` | Admin UI port. Admin is always on `127.0.0.1` |

## Logging

Logs are written to `%APPDATA%\prism\proxy.log`. View them from:
- System tray → **Show Logs** (opens a PowerShell console with a live tail)
- Admin UI → **Logs** tab (last 200 lines)
- Direct file access at `%APPDATA%\prism\proxy.log`
