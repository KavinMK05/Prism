# Prism — Startup Design Doc

**Office Hours Session: 2026-04-28**
**Stage: Pre-product**  
**Mode: Startup — YC Product Diagnostic**

---

## What Prism Is

A 9 MB Windows binary. 6 MB RAM. Native system tray. Proxies Anthropic Messages API (`/v1/messages`) to Ollama `/api/chat` or OpenAI `/v1/chat/completions`. Built in Go. Single executable. Zero config.

The use case: you want Claude Desktop, Claude Code, or any Anthropic-compatible client to talk to your local Ollama server or OpenAI-compatible backend instead of the Anthropic API.

---

## Demand Evidence

| Signal | Strength | Notes |
|---|---|---|
| **Self-use (founder = user #1)** | Strong | Daily dependency. Built it because the workarounds were unbearable. |
| **Coworkers / fellow devs** | Moderate | Real people sharing the same pain — Anthropic tooling with local LLMs. Not just interest. |
| **Pattern observation** | Moderate | Claude Code adoption + local LLM quality both rising. Gap widening. |

**Verdict:** Real demand for one person (the founder). Anecdotally extends to a small cohort. Market sizing needs actual distribution to validate — are there 50, 500, or 50,000 devs in this intersection?

---

## Status Quo — What People Do Now

| Workaround | Cost |
|---|---|
| **Manual config per tool** | ~1-2 hours of setup for each new Anthropic-compatible tool. Edit endpoints, write custom scripts, debug. |
| **Run LiteLLM locally** | ~300MB+ RAM, Python dependency hell, overkill for single-user desktop proxy. |
| **Give up** | Some devs just abandon Anthropic clients and use Ollama's native interfaces. Lose access to Claude Code's power. |

**The pain is real and measurable.** People are spending time and compute on work that should be invisible.

---

## Target User

**The AI-native developer** — a daily Claude Code user who wants to run against local models for cost, privacy, or offline access. The founder IS this user.

---

## Competitive Landscape

| Competitor | Prism's Edge | Prism's Weakness |
|---|---|---|
| **LiteLLM** | 30x smaller, 50x less RAM, GUI, Windows-native | No production features (rate limiting, observability) |
| **Bifrost** | Featherweight + GUI vs CLI-only | Fewer API format adapters |
| **Manual config** | One-click setup vs hours of endpoint tweaking | User must download and trust a binary |
| **Ollama `/v1/messages`** (future) | Multi-provider switching, tray, model remap | Proxy layer thins if Ollama goes native |

---

## Key Threat

**Ollama adding a native Anthropic-compatible `/v1/messages` endpoint.** Ollama already ships OpenAI-compatible endpoints. This is plausible.

Defense: multi-provider switching, system tray GUI, Windows-native polish, model remapping — layers Ollama won't build.

---

## The Assignment

**Get Prism into the hands of 5 people who aren't you.**

Five real developers who:
1. Download the binary
2. Double-click `prism.exe`
3. Point Claude Code or Claude Desktop at `http://127.0.0.1:11434`
4. Use it for their actual work
5. Tell you what broke, what was confusing, and whether they'd keep using it

Everything else — pricing, features, market sizing — is premature until you see someone else's machine run it and hear what happened.

---

## Open Questions

1. **Distribution:** How do people find Prism? GitHub? Reddit? Word of mouth?
2. **Monetization:** Paid product, free utility, or open source with premium?
3. **Cross-platform:** macOS/Linux have the same pain. Stay Windows-only?
4. **Scale:** Is this 500 devs or 50,000?
