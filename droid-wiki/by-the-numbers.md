# By the numbers

Data collected on 2026-05-09.

## Size

### Lines of code by language

```mermaid
xychart-beta
    x-title "Language"
    y-title "Lines of code"
    x-axis ["Go", "HTML", "PowerShell", "Batch"]
    y-axis "Lines" 0 --> 5000
    bar [4800, 400, 200, 100]
```

- **Total source files**: 27 (19 Go, 1 HTML, 3 Batch, 3 PowerShell, 1 Markdown)
- **Go source files**: ~4,800 lines across 19 `.go` files
- **Test files**: 1 (`streaming_test.go`)
- **Config/files**: `.gitignore`, `.go.mod`, `.go.sum`

## Activity

- **Project started**: April 2026
- **Active development**: April–May 2026 (1 month)
- **Most active files** (by commit count): `main.go`, `config.go`, `tray.go`, `proxy.go`, `admin.go`

## Complexity

| Directory | Avg file size | Files |
|---|---|---|
| Root (Go) | ~250 lines | 19 |
| `cmd/` | — | 0 |
| `docs/` | — | 7 |

Largest files:
- `responses_streaming.go`: ~550 lines (Responses API SSE streaming)
- `admin.go`: ~500 lines (Admin UI API handlers)
- `tray.go`: ~500 lines (System tray menu and proxy lifecycle)
