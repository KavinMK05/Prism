# Prism v0.3.0

## What's New

- **Managed SearXNG instance** — Prism can now run a local [SearXNG](https://github.com/searxng/searxng) metasearch engine alongside the proxy, managed entirely from the tray and admin UI. A new **SearXNG** tab provides start/stop/restart, a live status indicator, an auto-start toggle, and a structured settings editor for the user-tunable subset of `settings.yml` (server, search, and UI keys — engines/outgoing/redis stay editable in the file directly).
  - **Zero-Python-friction install.** First **Start** bootstraps an isolated venv and `pip install`s SearXNG (~80 MB, a minute or two). If no system Python ≥3.10 is on PATH, Prism downloads a matching [python-build-standalone](https://github.com/astral-sh/python-build-standalone) interpreter first — so SearXNG runs on Windows machines with no Python installed at all.
  - **Windows portability patch.** SearXNG imports the Unix-only `pwd` module unconditionally in `searx/valkeydb.py`, which crashes the webapp on Windows before the (off-by-default) limiter is ever consulted. Prism applies an idempotent conditional-import patch on install so the webapp launches cleanly on Windows.
  - **Sensible local defaults.** The generated `settings.yml` inherits SearXNG's full engine set via `use_default_settings`, enables JSON output, and turns the bot limiter **off** — so no Valkey/Redis is required for local single-user use.
  - **Tray integration.** New **SearXNG** menu items (status line, Start / Stop / Restart) mirror the admin UI, and SearXNG is stopped cleanly on Quit.
  - **Auto-start.** Opt in from the SearXNG tab; if SearXNG is already installed, Prism launches it on startup. The first-time download is never triggered automatically — only an explicit **Start** installs.
- **Cross-platform archive extraction** — The macOS self-update `extractTarGz` / `isPathSafe` helpers (and a new Windows-illegal-filename guard) have been promoted to a shared `archive.go`, now reused by both the macOS auto-updater and the SearXNG python-build-standalone install on Windows and macOS.

## Improvements

- `isProxyRunning` is now built on shared `pidAlive` / `stopProcessByPID` helpers on both Windows and macOS, eliminating per-platform duplication of the PID liveness check and process termination.

## Bug Fixes

- No user-facing bug fixes this release; the SearXNG work surfaced and fixed the platform-duplication noted above.

## Notes

- The managed SearXNG instance lives entirely under Prism's config dir (`%APPDATA%\prism\searxng\` on Windows, `~/Library/Application Support/prism/searxng/` on macOS) — venv, source tree, and `settings.yml`. Uninstalling Prism or deleting that directory removes it completely.
- SearXNG defaults to `http://127.0.0.1:8888/` (port configurable in the SearXNG tab). It runs as a separate process from the proxy; the proxy port (`11434`) is unchanged.