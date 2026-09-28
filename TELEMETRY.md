# Prism Telemetry

Prism collects **anonymous** usage data so the maintainer can count active
installs and active users — how many people run Prism, and how many of them
actually sent traffic through it. This page is the exact, honest list of what
is collected, where it goes, and how to turn it off.

## TL;DR

- **On by default, off in one click.** Telemetry is enabled on first run and
  can be turned off at any time, with no restart and no questions asked.
- **One tiny event per day.** Prism sends a single `app_heartbeat` event at
  most once per calendar day.
- **Nothing sensitive.** No prompts, models, requests, tokens, API keys, URLs,
  file paths, or personal identifiers are ever sent.

## What is collected

Each `app_heartbeat` event contains exactly six fields:

| Field          | Example        | Description                                              |
| -------------- | -------------- | -------------------------------------------------------- |
| `distinct_id`  | `3f2a…` (UUID) | A random anonymous ID generated on first run             |
| `version`      | `0.5.2`        | The Prism build version                                   |
| `os`           | `windows`      | The operating system (`windows` / `darwin`)               |
| `arch`         | `amd64`        | The CPU architecture                                      |
| `used_today`   | `true`         | Whether Prism proxied at least one request in the last 24 hours |
| `requests_24h` | `10-99`        | A coarse bucket of that 24-hour request count (`0`, `1-9`, `10-99`, `100+`) |

The last two fields are what separate "someone has Prism running" from "someone
actually used Prism today". They are counts only: Prism never sends what was
asked, which model answered, or which client asked. If Prism cannot read its
local stats database, those two fields are simply omitted — the event never
claims you were inactive.

The `distinct_id` is a random UUID stored in a local file
(`analytics_id.txt`). It is **not** derived from your machine, username, or any
identifying hardware. It is kept separate from `config.json` so that sharing
your config for support never leaks your tracker ID. Because it is stable
across days, PostHog stores a "person" record for it — that record contains
nothing beyond the six fields above.

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
aggregate to gauge adoption, active usage, and platform distribution. See
[POSTHOG.md](POSTHOG.md) for what is actually built from it.

## How often

At most **once per calendar day** per install. Prism checks on startup and then
every 24 hours, but a per-day guard ensures no more than one event per day even
if Prism is restarted repeatedly.

## How to disable

1. **In the app:** open the admin UI → **Proxy** → **Anonymous Analytics** and
   turn the toggle off. Turning it back on sends a heartbeat immediately.
2. **Environment variable (hard override):** set `PRISM_ANALYTICS_DISABLED=1`.
   This disables telemetry entirely — no pings, no first-run notice, and the
   in-app toggle is ignored — regardless of any other setting.

## How to verify what is sent

Set `PRISM_ANALYTICS_DEBUG=1`. Prism will build the heartbeat payload and log
it to the log file **without sending it**, so you can inspect the exact bytes.

## How this changed

Telemetry was introduced as opt-in. It is now **opt-out**, because an opt-in
ping only ever measured the small share of users who clicked "yes" and could
not answer "how many people use Prism". The move is deliberately conservative
about past decisions:

- If you had already been asked and declined, you **stay off**. That choice is
  recorded as an opt-out and is never overridden or re-asked.
- If you had opted in, or had never been asked, telemetry is on and you see a
  one-time notice in the admin UI with a **Turn off analytics** button.

Prism is open source, and this file is part of the source: if the shipped
payload and this description ever disagree, that is a bug worth reporting.

## Notes

- The user count is **approximate and directional**. Because the project key is
  public (by design), a bad actor could inject fake events; the numbers should
  be read as "roughly how many active installs," not an exact figure.
- Prism only pings from the tray process. A proxy started on its own
  (`prism --serve`, e.g. as a service or in a container) is never counted.
- Telemetry is low priority: requests time out after 10 seconds, run in a
  background goroutine, and failures are silently ignored. It can never slow
  down or break Prism.
- Opting out does not delete the local `analytics_id.txt`, so turning telemetry
  back on keeps the same anonymous identity instead of creating a new one.
