# Usage tracking

Active contributors: KavinMK05

## Purpose

Prism monitors credit usage for Codex OAuth accounts by polling the ChatGPT backend APIs. Usage data is displayed in the system tray and admin UI, giving users visibility into their remaining credits.

## Data sources

Usage data is fetched from two ChatGPT backend endpoints:

1. **`/backend-api/me`** — account info (email, plan tier)
2. **`/backend-api/accounts/check`** — credit limits with rate_limits

A fallback path uses `api.openai.com/v1/me` when the ChatGPT backend returns 403 Forbidden.

## Rate limit parsing

The `fetchCodexAccountCheck` function in `usage.go` parses credit limits from the ChatGPT backend's complex response structure. It tries five extraction paths in order:

1. `accounts.{id}.features[].rate_limits` — features array with named rate limit entries
2. Direct `rate_limits` on account objects
3. Top-level `rate_limits`
4. `account_ordering` → first account ID → account → plan credits
5. Top-level `plan` or `usage` objects

## Caching and refresh cycle

Usage data is cached in a `usageCache` map and refreshed:

- On initial connection (triggered by `refreshUsageForAccount` in `oauth.go`)
- Every 5 minutes via a background goroutine started by `startUsageRefreshLoop`
- On demand from the admin UI or system tray

Refresh operations are deduplicated per account to prevent concurrent refreshes.

## JWT plan tier extraction

When API endpoints don't return plan information, Prism falls back to parsing the JWT access token's claims. The `parseJWTPlanTier` function in `usage.go` tries multiple claim paths:

- `https://api.openai.com/auth.chatgpt_plan_type`
- `https://api.openai.com/auth.subscription_plan`
- `https://api.openai.com/profile.plan_type`
- Top-level `plan_type` or `subscription_plan`
- Scope-based inference (plus, team)

## Key source files

| File | Purpose |
|---|---|
| `usage.go` | Usage data fetching, parsing, caching, refresh loop |
| `oauth.go` | Account struct with usage fields |
