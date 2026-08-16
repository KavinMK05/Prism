# Prism Telemetry

Prism collects **anonymous, opt-in** usage data so the maintainer can count
active installs and understand which platforms Prism is used on. This page is
the exact, honest list of what is collected, where it goes, and how to disable
it.

## TL;DR

- **Opt-in only.** Telemetry is **off by default**. You are never asked to
  participate more than once, and you can turn it off at any time.
- **One tiny event per day.** Prism sends a single `app_heartbeat` event at
  most once per calendar day.
- **Nothing sensitive.** No prompts, models, requests, tokens, API keys, URLs,
  file paths, or personal identifiers are ever sent.

## What is collected

Each `app_heartbeat` event contains exactly four fields:

| Field        | Example        | Description                                  |
| ------------ | -------------- | -------------------------------------------- |
| `distinct_id`| `3f2a…` (UUID) | A random anonymous ID generated on first run |
| `version`    | `0.5.2`        | The Prism build version                       |
| `os`         | `windows`      | The operating system (`windows` / `darwin`)   |
| `arch`       | `amd64`        | The CPU architecture                          |

The `distinct_id` is a random UUID stored in a local file
(`analytics_id.txt`). It is **not** derived from your machine, username, or any
identifying hardware. It is kept separate from `config.json` so that sharing
your config for support never leaks your tracker ID.

## What is NOT collected

Prism never sends:

- Prompts, messages, or model names
- Request or response bodies
- API keys, OAuth tokens, or account identifiers
- URLs, file paths, or environment variables
- IP-derived location or any personal data

## Where it goes

Events are sent to **PostHog (EU)** — `https://eu.i.posthog.com/capture/` —
using PostHog's public project key. The public key is ingest-only: it can write
events but cannot read, delete, or access the account. The data is used only in
aggregate to gauge adoption and platform distribution.

## How often

At most **once per calendar day** per install. Prism checks on startup and then
every 24 hours, but a per-day guard ensures no more than one event per day even
if Prism is restarted repeatedly.

## How to disable

1. **In the app:** open the admin UI → **Proxy** → **Anonymous Analytics** and
   turn the toggle off.
2. **Environment variable (hard override):** set `PRISM_ANALYTICS_DISABLED=1`.
   This disables telemetry entirely — no pings, and the consent prompt is never
   shown — regardless of the in-app setting.

## How to verify what is sent

Set `PRISM_ANALYTICS_DEBUG=1`. Prism will build the heartbeat payload and log
it to the log file **without sending it**, so you can inspect the exact bytes.

## Notes

- The user count is **approximate and directional**. Because the project key is
  public (by design), a bad actor could inject fake events; the numbers should
  be read as "roughly how many active installs," not an exact figure.
- Telemetry is low priority: requests time out after 10 seconds, run in a
  background goroutine, and failures are silently ignored. It can never slow
  down or break Prism.
