# Prism v0.3.4

## New Features

- **Added: Grok Build agent integration.** Prism now registers `[model.prism-*]` entries in `~/.grok/config.toml` so Grok Build can route through your local Prism models. One-click setup is available on the **Agents** tab in the admin UI (Setup/Restore), with status detection that falls back to checking for the `grok` binary on PATH since Grok Build may not create its config file until first run. README documents the integration alongside the existing agents.

## Bug Fixes

- **Fixed: parallel tool calls to the same tool collapsed into one when translating OpenAI → Ollama.** The streaming dedup keyed tool calls on `id` then function name, so two concurrent calls to the same tool with no `id` merged. Ollama identifies tool calls within an assistant message by `function.index`, which it always emits and which is distinct even for parallel calls to the same tool. Prism now mirrors the array position as `index` on the inbound translation and uses `index` (falling back to `id`, then name) as the dedup key, so parallel same-name calls survive the round-trip back to Ollama.

- **Fixed: buffered tool calls flushed mid-stream, re-emitting the same call multiple times.** `closeToolCalls` was called whenever content arrived after tool calls, which — combined with Ollama re-emitting the full cumulative arguments on every chunk — produced duplicate/garbled tool calls. Flushing now happens only at stream finalization (the `done` chunk, or post-loop on a dropped stream), matching Ollama's single-emission behaviour.