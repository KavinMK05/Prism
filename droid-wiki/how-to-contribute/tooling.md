# Tooling

## Build system

Building Prism requires:

1. `go-winres` — generates Windows resource files (icon, manifest, version info)
2. `go build` — compiles the Go binary

The build command:

```powershell
go-winres make; go build -ldflags="-H windowsgui" -o prism.exe .
```

The `-H windowsgui` flag is critical — it tells the Go linker to produce a Windows GUI application (no console window). Without it, the system tray app would show a console window.

## Run modes

| Command | Mode | Description |
|---|---|---|
| `.\prism.exe` | Tray mode | System tray icon + admin UI + proxy process management |
| `.\prism.exe --serve` | Proxy mode | Proxy server only (no tray) |
| `.\prism.exe --serve` (no `-H windowsgui`) | Debug mode | Proxy server with visible console |

## Batch scripts

The project includes helper scripts:

- `start-proxy.bat` — launches the proxy
- `stop-proxy.bat` — stops the proxy
- `show-logs.bat` — opens the log viewer
