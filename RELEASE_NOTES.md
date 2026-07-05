# Prism v0.3.1

## Bug Fixes

- **Fixed: SearXNG first Start failed with `create venv: fork/exec …/searxng/python/bin/python3: no such file or directory` (macOS and Windows).** python-build-standalone `install_only` tarballs extract to a single nested top-level directory (e.g. `python/`), so the standalone interpreter actually landed at `…/searxng/python/python/bin/python3` while the bootstrap path pointed at `…/searxng/python/bin/python3`. Prism now flattens that wrapper directory after extraction, so the interpreter resolves correctly on both platforms. Also added:
  - **Idempotency** — a previously downloaded + flattened interpreter is reused instead of re-downloading ~30 MB on every retry.
  - **Clean slate on re-install** — the Python directory is wiped before re-extraction, so a stale half-extracted tree from a prior failed run (e.g. the v0.3.0 nested-`python/` state) can't confuse the flatten.
  - **Post-extract sanity check** — clear error if the interpreter still isn't where it's expected after extraction.

## Notes

- If you hit this on v0.3.0, just let Prism auto-update to v0.3.1 and click **Start** again — the stale tree is cleaned up automatically; no manual deletion needed.