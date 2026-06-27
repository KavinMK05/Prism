# Phase-by-Phase Plan: Fix Codex OAuth Integration (Jun 22, 2026)

## Documentation & References

- **Codex Auth**: [Authentication Overview](https://developers.openai.com/codex/auth), [Config Reference](https://developers.openai.com/codex/config-reference)
- **openai-oauth proxy**: [GitHub repo](https://github.com/EvanZhouDev/openai-oauth) - reference implementation that proxies ChatGPT OAuth tokens through `chatgpt.com/backend-api/codex/responses`
- **Codex CLI source**: [openai/codex](https://github.com/openai/codex) - `codex-rs/backend-client/src/client.rs` shows header construction
- **Simon Willison debug capture**: Live request capture to `chatgpt.com/backend-api/codex/responses`
- **Key endpoint**: `POST https://chatgpt.com/backend-api/codex/responses` (Responses API, works with Bearer token)
- **Blocked endpoint**: `POST https://chatgpt.com/backend-api/codex/chat/completions` (Cloudflare 403 with bearer-only, must NOT use)

### Required Headers for backend-api/codex/responses
- `Authorization: Bearer <access_token>`
- `chatgpt-account-id: <account_id>` (from JWT claim `https://api.openai.com/auth.chatgpt_account_id`)
- `OpenAI-Beta: responses=experimental`
- `Content-Type: application/json`

### Request Body Normalization
- `instructions` must be present (empty string OK, but key required)
- `store` defaults to `false`
- `max_output_tokens` should be stripped before forwarding
- Streaming uses SSE

## Problem Summary

1. **"Usage is over" error**: Proxy sends Codex OAuth requests to `https://api.openai.com/v1/chat/completions` which rejects ChatGPT OAuth tokens. Must use `https://chatgpt.com/backend-api/codex/responses` instead.
2. **Usage bar shows wrong/missing data**: Usage endpoints (`/backend-api/me`, `/backend-api/accounts/check`) return 403 without `chatgpt-account-id` header. Need to add this header and try `/wham/accounts/check`.
3. **UI looks funny**: Usage bar shows "?" values because data isn't fetched. Need graceful fallback.

## Phase 1: Forward Responses API to backend-api/codex/responses
- [x] Change Codex provider base URL from `https://api.openai.com` to `https://chatgpt.com/backend-api/codex`
- [x] Add a `responsesURL()` method to `ResolvedProvider` that returns the correct Responses API endpoint for Codex providers
- [x] Add `chatgpt-account-id` header extraction (re-extract from JWT on every request as fallback)
- [x] Add `OpenAI-Beta: responses=experimental` header for Codex provider requests
- [x] Modify `handleResponsesAPI` in `responses_inbound.go` to forward Responses API requests directly to `chatgpt.com/backend-api/codex/responses` for Codex providers (skip the current translation to Chat Completions)
- [x] Handle Responses API streaming (SSE passthrough) for Codex providers
- [x] Handle Responses API non-streaming for Codex providers (force stream upstream, reassemble response)
- **User Acceptance**: Codex Desktop can send prompts through Prism and get responses. No more "usage is over" error.

## Phase 2: Update Agent Sync to Use Responses API for Codex Models Only
- [x] Update Factory Droid sync to use `openai` provider type for Codex models, `generic-chat-completion-api` for others
- [x] Update OpenCode sync to use two provider blocks: `prism` with `@ai-sdk/openai-compatible` for non-Codex, `prism-codex` with `@ai-sdk/openai` for Codex models
- [x] Add `isCodexProviderID` helper to config.go (checks codex_ prefix)
- [x] Update OpenCode restore function to remove both `prism` and `prism-codex` provider blocks
- [x] Update OpenCode active detection to check for both provider blocks
- [x] Strip provider prefix from model name before sending to Codex backend (openai/gpt-5.4 -> gpt-5.4)
- [x] Verify Claude Code continues to use Anthropic format (has its own proxy conversion, no change needed)
- [x] Ensure the existing Responses API handlers in `responses_inbound.go` correctly handle requests from Factory Droid and OpenCode for both Codex and non-Codex providers
- **User Acceptance**: Factory Droid and OpenCode can use Codex models through Prism using the Responses API natively. Non-Codex models use Chat Completions as before.

## Phase 3: Chat Completions to Responses API Translation for Codex (Fallback)
- [x] Add `translateChatCompletionsToCodexResponses` function (convert Chat Completions request to Responses API format)
- [x] Add `translateCodexResponsesToChatCompletions` function to convert Responses API response back to Chat Completions format (non-streaming)
- [x] Add streaming translation: convert Responses SSE events to Chat Completions SSE chunks
- [x] Modify `openai_inbound.go` to route Chat Completions requests from Codex providers through the Responses API translation path
- [x] Add config reload mechanism (ReloadConfig) so proxy picks up OAuth account changes without restart
- [x] Add Codex OAuth fallback: if model's provider ID doesn't match any account, use first available Codex account
- [x] Tool calls, reasoning, and content properly translated
- [x] Tested: both streaming and non-streaming Chat Completions work with Codex (verified with curl)
- **User Acceptance**: Clients using Chat Completions API (e.g., curl, other tools) can send requests through Prism to Codex OAuth accounts and get valid Chat Completions responses.

## Phase 4: Fix Usage Data Fetching
- [x] Add `chatgpt-account-id` header to usage API requests (`/backend-api/me`, `/backend-api/accounts/check`)
- [x] Try `/wham/accounts/check` endpoint as an alternative for rate limit data
- [x] Add `OpenAI-Beta` header to usage requests
- [x] Improve error handling: if all usage endpoints fail, set `usage_unavailable` flag
- [x] Update the usage refresh loop to include the new headers (handled via refreshUsageForAccount)
- [x] Confirmed: usage endpoints return 403 with bearer-only auth (require session cookies). Graceful fallback is the correct approach.
- **User Acceptance**: Usage bar shows real data when available, or gracefully shows "Usage unavailable" when not.

## Phase 5: UI Improvements
- [x] When usage data is unavailable, show clear explanation message instead of "?" values
- [x] Fix encoding issues: replace mojibake characters (┬╖) with proper HTML entities (&middot;)
- [x] Fix plan tier display: show extracted plan tier (Go/Plus/Pro/Team) instead of always falling back to "Codex"
- [x] Fix credits display: show 0 instead of "?" when values are numeric but zero (check null instead of falsy)
- [x] Improve "usage unavailable" message to explain why (session cookies required)
- **User Acceptance**: OAuth tab looks clean and professional whether or not usage data is available. No "?" values or broken layout.
