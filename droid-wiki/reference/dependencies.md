# Dependencies

## Go module dependencies

Prism has minimal external dependencies:

| Module | Purpose |
|---|---|
| `github.com/getlantern/systray` v1.2.2 | Native Windows system tray menu |

## Indirect dependencies

These are pulled in by `systray`:

| Module | Purpose |
|---|---|
| `github.com/getlantern/context` | Context propagation |
| `github.com/getlantern/errors` | Error handling |
| `github.com/getlantern/golog` | Logging |
| `github.com/getlantern/hex` | Hex encoding |
| `github.com/getlantern/hidden` | Hidden field handling |
| `github.com/getlantern/ops` | Operations tracking |
| `github.com/go-stack/stack` | Stack traces |
| `github.com/oxtoacart/bpool` | Buffer pool |
| `golang.org/x/sys` v0.43.0 | Windows system calls (registry, process management) |

## Build dependencies

| Tool | Purpose |
|---|---|
| `go-winres` | Windows resource embedding (icon, manifest) |

## Runtime footprint

- Binary size: ~5 MB (compressed)
- Memory usage: ~5-10 MB (idle)
- No runtime dependencies — single binary
