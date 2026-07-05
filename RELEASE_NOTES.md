# Prism v0.3.2

## Bug Fixes

- **Fixed: SearXNG crashed repeatedly with `ModuleNotFoundError: No module named 'tomllib'`.** SearXNG's `searx/botdetection/config.py` does `import tomllib`, a stdlib module added in **Python 3.11**. Prism's `python-build-standalone` selector matched the **first** `install_only` asset for the platform, which is published as Python **3.10** in every release — so the venv was created from a 3.10 interpreter and the webapp crashed on import, looping 5× and stopping. The selector now:
  - **Picks the lowest Python ≥3.11** build available for the platform (verified against the real `astral-sh/python-build-standalone` release asset names). 3.11 is preferred over 3.12/3.13/3.14 to maximize wheel availability for SearXNG's dependencies (lxml, markupsafe, etc.).
  - **Raises the system-Python floor to ≥3.11** (was ≥3.10), so a 3.10 system interpreter is no longer accepted.
  - **Detects and rebuilds a stale 3.10 venv.** On Start, Prism checks the existing venv's interpreter version; if it's <3.11 (e.g. a venv left by v0.3.0/v0.3.1), both the venv and the cached standalone interpreter are wiped and a compatible ≥3.11 build is downloaded and the venv recreated — automatically, no manual cleanup.
  - **Idempotency now version-aware** — a cached standalone interpreter is reused only if it's ≥3.11; a cached 3.10 build is discarded and re-downloaded.
  - Covered by hermetic tests for the asset-name parser, the ≥3.11 selector (incl. rejection when only 3.10 exists), and the version floor.

## Notes

- If you hit the `tomllib` crash on v0.3.0/v0.3.1, let Prism auto-update to v0.3.2 and click **Start** again — the 3.10 venv is rebuilt as 3.11 automatically. Re-download of the Python interpreter (~30 MB) happens once.
- Also includes the v0.3.1 fix for the `python-build-standalone` nested-`python/` directory flatten (the venv bootstrap `fork/exec ... no such file or directory` error).