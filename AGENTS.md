Build command (Windows): ./build.ps1
Build command (macOS): ./build.sh
Manual build (Windows): cd web; npm run build; cd ..; go-winres make; go build -ldflags="-H windowsgui -X main.version=dev" -o prism.exe .
Manual build (macOS): cd web; npm run build; cd ..; CGO_ENABLED=1 go build -ldflags="-X main.version=dev" -o prism .
Frontend dev server: cd web; npm run dev (HMR on localhost:5173/admin/, proxies API to Go on localhost:8765)
Version: Injected at build time via `-ldflags "-X main.version=TAG"`. Defaults to "dev" if not set. CI injects `$GITHUB_REF_NAME` (the git tag) automatically. The tray/updater copies it via `desktop.SetVersion(version)`.
Admin UI: React app in `web/`, embedded via `go:embed` in root `assets.go` (served by `internal/admin`). Legacy HTML at `/admin-legacy`.

Repository layout:
- Root (package main): `main.go` (entrypoint, proxy server, HTTP middleware), `assets.go` (go:embed of `admin.html`, `icon.png`, `web/dist`). Everything else lives in `internal/`.
- `internal/config`: config load/save, live config API (`Current`/`SetCurrent`/`SetChangeHook`), model remapping, OAuth account types.
- `internal/db`: SQLite stats persistence (requests, TPS snapshots) + `RequestStats`.
- `internal/stats`: in-memory stats tracker (`stats.Global`), `StatsToJSON`.
- `internal/oauth`: Codex PKCE/device OAuth flows, token refresh, ChatGPT usage tracking.
- `internal/agents`: third-party agent integrations (Claude Code, Codex Desktop, OpenCode, ZCode, OMP, Grok Build, Pi, Kimi Code).
- `internal/proxy`: `proxy.NewRouter` — Anthropic/OpenAI/Responses API translation, streaming, search interception. Owns `detectClient`.
- `internal/desktop`: tray app — process management, updates, SearXNG, UI helpers. Admin server is injected via `desktop.SetAdminServerStarter` (avoids a desktop→admin import); version via `desktop.SetVersion`.
- `internal/admin`: admin UI server (`admin.StartAdminServer(embed.FS, cfg, port)`), split by domain (oauth/search/searxng/agents/stats/modelsdev).
- `internal/search`: search provider registry/runner; `internal/platform`: OS-specific paths, single-instance lock, autostart, icons; `internal/util`: shared helpers.
- Darwin-only files (cgo, e.g. `trayicon_darwin.go`) can only be verified by building on macOS with `./build.sh`; keep `_darwin.go`/`_windows.go` pair function signatures symmetric.

Committing and pushing:
1. Stage and commit changes: `git add -A; git commit -m "message"`
2. Push to remote: `git push origin main`
3. Always verify no secrets/credentials are in the diff before committing (`git diff --cached`)

Creating a release:
1. Create an annotated tag: `git tag -a v0.X.Y -m "Release title\n\nRelease notes here"`
2. Push the tag: `git push origin v0.X.Y`
3. The GitHub Actions workflow (`.github/workflows/release.yml`) will automatically build and create a GitHub Release with `prism.exe`, `Prism-macOS.dmg`, and `Prism-macOS.tar.gz` as assets
4. The tag version is injected into the binary via `-X main.version=$GITHUB_REF_NAME`
5. Release assets must match what the auto-updater expects: `prism.exe` (Windows) and `Prism-macOS.tar.gz` (macOS)