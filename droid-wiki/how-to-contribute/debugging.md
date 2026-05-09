# Debugging

## Common issues

### Proxy won't start

Check if the port is already in use:

```powershell
netstat -ano | Select-String ":11434"
```

If another process is listening, either stop it or change `PRISM_PORT`.

### Model remapping not working

Check the proxy logs for `[map]` messages:

```
[map] Model remap (default): unknown-model -> glm-5.1:cloud
[map] Model remap (alias): claude-3-5-haiku -> deepseek-v4-flash:cloud
```

If you don't see a remap log line, the model name might be in the known models list.

### Authentication errors

Check which API endpoint you're hitting and use the correct middleware:

- `/v1/messages` → `x-api-key` header
- `/v1/chat/completions` → `Authorization: Bearer` header

The proxy API key is `prism` by default.

### Log viewer

- From system tray: **Show Logs** opens a live-tail PowerShell console
- From admin UI: **Logs** tab shows the last 200 lines
- Directly: `%APPDATA%\prism\proxy.log`

## Live stats

The proxy tracks request statistics that can be viewed via the admin UI or directly:

```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/stats" -Headers @{"x-api-key"="prism"}
```
