# Development workflow

## Branch, code, test, PR

1. Create a feature branch from `main`
2. Make changes to the relevant files
3. Build and test locally
4. Submit a pull request against `main`

## Proxy process debugging

For debugging, run in console mode:

```powershell
go build -o prism.exe .
.\prism.exe --serve
```

This runs the proxy server directly without the system tray, showing all log output in the console. Combine with the admin UI at `http://127.0.0.1:8765/admin` for configuration.

## Streaming debugging

Set log level to verbose by checking `proxy.log` at `%APPDATA%\prism\proxy.log` during streaming. All streaming events are logged with `[STREAM]` prefix.
