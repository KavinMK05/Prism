Build command (Windows): ./build.ps1
Build command (macOS): ./build.sh
Manual build (Windows): cd web; npm run build; cd ..; go-winres make; go build -ldflags="-H windowsgui -X main.version=dev" -o prism.exe .
Manual build (macOS): cd web; npm run build; cd ..; CGO_ENABLED=1 go build -ldflags="-X main.version=dev" -o prism .
MSI build (Windows, after the exe exists): ./installer/windows/build-msi.ps1 [-Version 0.9.1] [-Scope perUser|perMachine]
Frontend dev server: cd web; npm run dev (HMR on localhost:5173/admin/, proxies API to Go on localhost:8765)
Version: Injected at build time via `-ldflags "-X main.version=TAG"`. Defaults to "dev" if not set. CI injects `$GITHUB_REF_NAME` (the git tag) automatically. The tray/updater copies it via `desktop.SetVersion(version)`.
Admin UI: React app in `web/`, embedded via `go:embed` in root `assets.go` (served by `internal/admin`). Legacy HTML at `/admin-legacy`.

Repository layout:
- Root (package main): `main.go` (entrypoint, proxy server, HTTP middleware), `assets.go` (go:embed of `admin.html`, `icon.png`, `web/dist`). Everything else lives in `internal/`.
- `internal/config`: config load/save, live config API (`Current`/`SetCurrent`/`SetChangeHook`), model remapping, OAuth account types.
- `internal/db`: SQLite persistence (requests, TPS snapshots, MCP marketplace catalog) + `RequestStats`.
- `internal/stats`: in-memory stats tracker (`stats.Global`), `StatsToJSON`.
- `internal/oauth`: Codex PKCE/device OAuth flows, token refresh, ChatGPT usage tracking.
- `internal/agents`: third-party agent integrations (Claude Code, Codex Desktop, OpenCode, ZCode, OMP, Grok Build, Pi, Kimi Code, Empryo, Hermes).
- `internal/mcp`: the MCP gateway (`gateway.go`), connection lifecycle (`runtime.go`), brokered OAuth (`oauth.go`), and the automatic first-use sign-in (`autoconnect.go`, gated by the `auto_connect` MCP setting).
- `internal/proxy`: `proxy.NewRouter` — Anthropic/OpenAI/Responses API translation, streaming, search interception. Owns `detectClient`.
- `internal/desktop`: tray app — process management, updates, SearXNG, UI helpers. Admin server is injected via `desktop.SetAdminServerStarter` (avoids a desktop→admin import); version via `desktop.SetVersion`.
- `internal/admin`: admin UI server (`admin.StartAdminServer(embed.FS, cfg, port)`), split by domain (oauth/search/searxng/agents/stats/modelsdev).
- `internal/search`: search provider registry/runner; `internal/platform`: OS-specific paths, single-instance lock, autostart, icons; `internal/util`: shared helpers.
- `installer/windows`: the WiX MSI (`Prism.wxs`) and its build script (`build-msi.ps1`), used by both developers and CI. Installs per-user to `%LOCALAPPDATA%\Programs\Prism` by default, records install state under `HKCU\Software\Prism`, and ships as the release asset `Prism-Windows-x64.msi`.
- Darwin-only files (cgo, e.g. `trayicon_darwin.go`) can only be verified by building on macOS with `./build.sh`; keep `_darwin.go`/`_windows.go` pair function signatures symmetric.

Windows MSI — rules that keep installs updatable:
- `HKCU\Software\Prism` (`InstallType`, `InstallDir`, `InstalledVersion`) is the contract between the package and the app. `platform.MSIInstallDir()` reads it and `desktop.InstalledViaMSI()` matches it against the running exe; renaming any of those values silently turns MSI installs back into "portable".
- Never let the portable update flow (`performPortableUpdate` in `internal/desktop/update_windows.go`) write over an MSI-managed `prism.exe`: Windows Installer owns that file and reverts it on the next repair or upgrade. MSI installs download the `.msi` asset and hand it to `msiexec` (`performMSIUpdate`), which is also why `getUpdateAssetName`/`updateAssetCandidates` are per-OS.
- The MSI deliberately does not touch `HKCU\...\Run\Prism`. A `RemoveRegistryValue` row also fires while a major upgrade uninstalls the previous product, which would silently disable "Start at Login" on every update. Uninstall cleanup needs a custom action conditioned on `NOT UPGRADINGPRODUCTCODE`.
- A per-machine MSI needs an elevated `msiexec`; the updater does not elevate, so `-Scope perMachine` packages are manual-install only until `performMSIUpdate` learns to elevate.
- **Author `Feature`, `CustomAction`, and `util:` actions inside `<Package>`, not in their own `<Fragment>`.** WiX v6 silently discards anything that is not reachable from `<Package>`: the build exits 0, prints no warning, and emits an MSI with no `File` rows, no `Component` table and no payload. This shipped once as a 28 KB `Prism-Windows-x64.msi` (see the comment in `Prism.wxs`). `build-msi.ps1` now reads the built database back with `Assert-MsiFilePayload` and fails if `prism.exe` is missing or the wrong size, so a green build can no longer mean an empty package.

Committing, pushing, and releasing — explicit permission required:
Do NOT commit, push, or create any release tag unless the user explicitly asks for it in the
current request (e.g. "commit this", "push", "cut a release"). Making file edits, staging, and
showing diffs is fine; writing to git history or the remote is not. When in doubt, stop and ask.
This applies even after a task is finished — finishing the work is not permission to commit it.

Committing and pushing:
1. Stage and commit changes: `git add -A; git commit -m "message"`
2. Push to remote: `git push origin main`
3. Always verify no secrets/credentials are in the diff before committing (`git diff --cached`)

Creating a release:
1. Create an annotated tag: `git tag -a v0.X.Y -m "Release title\n\nRelease notes here"`
2. Push the tag: `git push origin v0.X.Y`
3. The GitHub Actions workflow (`.github/workflows/release.yml`) will automatically build and create a GitHub Release with `prism.exe`, `Prism-Windows-x64.msi`, `Prism-macOS.dmg`, `Prism-macOS.tar.gz`, `Prism-Linux-x86_64.AppImage` and `Prism-Linux.tar.gz` as assets
4. The tag version is injected into the binary via `-X main.version=$GITHUB_REF_NAME`. Tags must be `v<major>.<minor>.<build>`: the MSI and the PE version resource need numeric versions, and the Windows job fails early on anything else.
5. Release assets must match what the auto-updater expects: `prism.exe` and `Prism-Windows-x64.msi` (Windows), `Prism-macOS.tar.gz` (macOS), `Prism-Linux.tar.gz` plus the AppImage (Linux)