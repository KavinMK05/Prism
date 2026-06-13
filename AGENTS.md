Build command (Windows): go-winres make; go build -ldflags="-H windowsgui -X main.version=dev" -o prism.exe .
Build command (macOS): CGO_ENABLED=1 go build -ldflags="-X main.version=dev" -o prism .
Version: Injected at build time via `-ldflags "-X main.version=TAG"`. Defaults to "dev" if not set. CI injects `$GITHUB_REF_NAME` (the git tag) automatically.

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