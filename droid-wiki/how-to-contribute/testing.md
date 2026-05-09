# Testing

## Current state

Prism has a streaming test file (`streaming_test.go`) but lacks comprehensive unit tests. The project relies on manual verification using the API endpoints.

## Manual testing

Use the verification commands from the [Getting Started guide](../overview/getting-started.md):

```powershell
# Test Anthropic endpoint
Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/messages" -Method POST `
  -ContentType "application/json" `
  -Headers @{"x-api-key"="prism"} `
  -Body '{"model":"glm-5.1:cloud","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'

# Test OpenAI Chat Completions
Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/chat/completions" -Method POST `
  -ContentType "application/json" `
  -Headers @{"Authorization"="Bearer prism"} `
  -Body '{"model":"glm-5.1:cloud","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}'

# Test OpenAI Responses API
Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/responses" -Method POST `
  -ContentType "application/json" `
  -Headers @{"Authorization"="Bearer prism"} `
  -Body '{"model":"glm-5.1:cloud","input":"hi"}'

# Test streaming
Invoke-RestMethod -Uri "http://127.0.0.1:11434/v1/chat/completions" -Method POST `
  -ContentType "application/json" `
  -Headers @{"Authorization"="Bearer prism"} `
  -Body '{"model":"glm-5.1:cloud","stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'
```

## Areas needing test coverage

- Translation correctness (all protocol paths)
- Streaming state machine edge cases
- OAuth token refresh flows
- Model remapping resolution
- Config migration from old format
