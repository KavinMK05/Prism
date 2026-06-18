# Prism v0.1.4

## What's New

- **Codex Desktop Integration** — Prism now auto-syncs your `model_remapping.json` into Codex Desktop's config on every proxy startup, keeping your model catalog and config.toml managed blocks in sync
- **Admin UI Setup/Restore Buttons** — New manual controls in the Proxy tab let you push or restore the Codex Desktop integration without restarting the proxy

## Improvements

- Tighter inbound streaming: reworked OpenAI-compatible stream handling for more reliable passthrough
- Model catalog generation now produces `reasoning_levels` in the correct dict format and matches codex-shim's file ordering

## Bug Fixes

- Fixed TOML path escaping and section-aware key extraction when writing managed blocks to `~/.codex/config.toml`
