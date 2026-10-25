package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ollama-proxy/internal/config"
)

// TestCapReasoningHistory verifies the replay caps keep the newest thinking
// blocks and reasoning bytes, and leave anything within bounds untouched.
func TestCapReasoningHistory(t *testing.T) {
	signatures := make([]ReasoningSignature, 0, maxReasoningBlocksPerTurn+88)
	for i := 0; i < maxReasoningBlocksPerTurn+88; i++ {
		signatures = append(signatures, ReasoningSignature{Signature: "sig-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + itoa(i)})
	}
	reasoning := strings.Repeat("think ", 60000) // 360 KB, over the 256 KiB cap
	msgs := []OpenAIChatMessage{{
		Role:                "assistant",
		Content:             "hi",
		ReasoningSignatures: signatures,
		ReasoningContent:    &reasoning,
	}}

	out := capReasoningHistory(msgs, "test-model")

	if got := len(out[0].ReasoningSignatures); got != maxReasoningBlocksPerTurn {
		t.Fatalf("signature count = %d, want %d", got, maxReasoningBlocksPerTurn)
	}
	// Newest kept: the first surviving entry is the one at index 88.
	if got, want := out[0].ReasoningSignatures[0].Signature, signatures[88].Signature; got != want {
		t.Errorf("oldest kept signature = %q, want %q", got, want)
	}
	if got := len(*out[0].ReasoningContent); got > maxReasoningContentBytes {
		t.Errorf("reasoning bytes = %d, want <= %d", got, maxReasoningContentBytes)
	}
	if !strings.HasSuffix(*out[0].ReasoningContent, "think ") {
		t.Errorf("reasoning content lost its tail (newest text)")
	}

	// Within bounds: nothing changes.
	small := "small reasoning"
	untouched := capReasoningHistory([]OpenAIChatMessage{{
		Role:                "assistant",
		ReasoningSignatures: []ReasoningSignature{{Signature: "s"}},
		ReasoningContent:    &small,
	}}, "test-model")
	if len(untouched[0].ReasoningSignatures) != 1 || *untouched[0].ReasoningContent != small {
		t.Errorf("in-bounds reasoning was modified")
	}
}

// TestTrimReasoningTail preserves valid UTF-8 at the cut.
func TestTrimReasoningTail(t *testing.T) {
	s := strings.Repeat("é", 2048) // 2 bytes per rune
	trimmed := trimReasoningTail(s, 1001)
	if len(trimmed) > 1001 {
		t.Errorf("trimmed length = %d, want <= 1001", len(trimmed))
	}
	if strings.ContainsRune(trimmed, '\uFFFD') {
		t.Errorf("trim produced invalid UTF-8")
	}
	if !strings.HasSuffix(trimmed, "é") {
		t.Errorf("trim cut off the tail")
	}
}

// TestPreserveReasoningContentOverride covers the per-model
// reasoning_preservation setting and its fallback to vendor-name inference.
func TestPreserveReasoningContentOverride(t *testing.T) {
	cfg := &config.Config{}
	remap := &config.ModelRemapping{
		KnownModels: []config.ModelEntry{
			{ID: "plain-model", Provider: "p1", ReasoningPreservation: "always"},
			{ID: "deepseek-custom", Provider: "p1", ReasoningPreservation: "never"},
		},
	}
	pr := NewRouter(cfg, remap)

	if !pr.preserveReasoningContent("plain-model", "p1") {
		t.Errorf("reasoning_preservation=always must preserve even without a vendor hint")
	}
	if pr.preserveReasoningContent("deepseek-custom", "p1") {
		t.Errorf("reasoning_preservation=never must drop even for a deepseek name")
	}
	// No setting: vendor-name inference still applies.
	if !pr.preserveReasoningContent("deepseek-v4.1-flash_cloud", "p1") {
		t.Errorf("deepseek model without an override must keep reasoning")
	}
	if pr.preserveReasoningContent("gpt-5.6-luna", "p1") {
		t.Errorf("non-reasoning vendor without an override must not preserve reasoning")
	}
	// The override is provider-scoped like every other ModelEntry field.
	if !pr.preserveReasoningContent("deepseek-custom", "p2") {
		t.Errorf("override for p1 must not apply to p2")
	}
}

// TestIsReasoningRejection covers the markers added for stale/foreign
// signature rejections on top of the original include/encrypted_content ones.
func TestIsReasoningRejection(t *testing.T) {
	rejections := []string{
		`{"error":{"message":"Unsupported parameter: include"}}`,
		`{"error":{"code":"invalid_encrypted_content"}}`,
		`{"error":{"message":"thinking_signature_invalid"}}`,
		`{"error":{"message":"invalid signature in thinking block"}}`,
	}
	for _, body := range rejections {
		if !isReasoningRejection([]byte(body)) {
			t.Errorf("isReasoningRejection(%q) = false, want true", body)
		}
	}
	unrelated := []string{
		`{"error":{"message":"model not found"}}`,
		`{"error":{"message":"context length exceeded"}}`,
	}
	for _, body := range unrelated {
		if isReasoningRejection([]byte(body)) {
			t.Errorf("isReasoningRejection(%q) = true, want false", body)
		}
	}
}

// TestPostChatCompletionsBody_RetriesWithoutReasoning verifies a 400 blaming a
// replayed signature is retried once with every reasoning field stripped, so a
// stale signature cannot turn a working provider into a hard failure.
func TestPostChatCompletionsBody_RetriesWithoutReasoning(t *testing.T) {
	var calls int32
	var sawReasoningOnRetry bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req OpenAIChatRequest
		_ = json.Unmarshal(body, &req)
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"thinking_signature_invalid"}}`)
			return
		}
		for _, m := range req.Messages {
			if m.ReasoningContent != nil || m.Reasoning != nil || len(m.ReasoningSignatures) > 0 {
				sawReasoningOnRetry = true
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	pr := NewRouter(&config.Config{}, nil)
	reasoning := "stale plan"
	req := &OpenAIChatRequest{
		Model: "m",
		Messages: []OpenAIChatMessage{{
			Role:             "assistant",
			Content:          "hello",
			ReasoningContent: &reasoning,
		}},
	}

	resp, err := pr.postChatCompletionsBody(context.Background(), srv.URL, req, &config.ResolvedProvider{APIKey: "k"}, "")
	if err != nil {
		t.Fatalf("postChatCompletionsBody: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (retry should have succeeded)", resp.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	if sawReasoningOnRetry {
		t.Errorf("retry still carried replayed reasoning")
	}
	// The caller's request must be untouched (the debug capture reflects it).
	if req.Messages[0].ReasoningContent == nil {
		t.Errorf("original request was mutated by the strip")
	}
}

// TestPostChatCompletionsBody_NoRetryForUnrelated400 verifies an unrelated 400
// is returned as-is: retrying it would only repeat the same error.
func TestPostChatCompletionsBody_NoRetryForUnrelated400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv.Close()

	pr := NewRouter(&config.Config{}, nil)
	reasoning := "plan"
	req := &OpenAIChatRequest{
		Model: "m",
		Messages: []OpenAIChatMessage{{
			Role:             "assistant",
			ReasoningContent: &reasoning,
		}},
	}
	resp, err := pr.postChatCompletionsBody(context.Background(), srv.URL, req, &config.ResolvedProvider{APIKey: "k"}, "")
	if err != nil {
		t.Fatalf("postChatCompletionsBody: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "model not found") {
		t.Fatalf("unrelated 400 was altered: status=%d body=%s", resp.StatusCode, string(body))
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}

// TestClaudeCodeSessionID covers the session-identity extraction order.
func TestClaudeCodeSessionID(t *testing.T) {
	headerReq, _ := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	headerReq.Header.Set("X-Claude-Code-Session-Id", "header-session")
	if got := claudeCodeSessionID(headerReq, nil); got != "header-session" {
		t.Errorf("header session = %q, want header-session", got)
	}

	jsonMeta := map[string]interface{}{
		"user_id": `{"device_id":"abc","account_uuid":"","session_id":"6f02eb1e-ba85-4441-87fe-a322a6594135"}`,
	}
	if got := claudeCodeSessionID(nil, jsonMeta); got != "6f02eb1e-ba85-4441-87fe-a322a6594135" {
		t.Errorf("json metadata session = %q", got)
	}

	legacyMeta := map[string]interface{}{"user_id": "hash123_session_deadbeef-0000"}
	if got := claudeCodeSessionID(nil, legacyMeta); got != "deadbeef-0000" {
		t.Errorf("legacy metadata session = %q", got)
	}

	if got := claudeCodeSessionID(nil, map[string]interface{}{"user_id": "no-session-here"}); got != "" {
		t.Errorf("unidentified metadata session = %q, want empty", got)
	}
	if got := claudeCodeSessionID(nil, nil); got != "" {
		t.Errorf("nil metadata session = %q, want empty", got)
	}
}

// TestOpenAISessionID covers the OpenAI-protocol session identity.
func TestOpenAISessionID(t *testing.T) {
	if got := openAISessionID(nil, &OpenAIChatRequest{PromptCacheKey: "pc-1"}); got != "pc-1" {
		t.Errorf("prompt_cache_key session = %q", got)
	}
	if got := openAISessionID(nil, &OpenAIChatRequest{User: "user-1"}); got != "user-1" {
		t.Errorf("user session = %q", got)
	}
	if got := openAISessionID(nil, &OpenAIChatRequest{}); got != "" {
		t.Errorf("no identity = %q, want empty (replay must stay disabled)", got)
	}
}

// TestThinkingReplay_CacheAndRestore covers the cache's store/lookup round trip
// and the privacy rule: no session key means no replay.
func TestThinkingReplay_CacheAndRestore(t *testing.T) {
	resetThinkingReplayCache()
	reasoning := "the plan"
	sig := ReasoningSignature{Signature: "encrypted-blob", CallIndex: 0}
	cacheThinkingReplayFromMessage("session-a", "p1:m", OpenAIChatMessage{
		Role:                "assistant",
		Content:             "let me check",
		ToolCalls:           []OpenAIToolCall{{ID: "toolu_1", Type: "function"}},
		ReasoningContent:    &reasoning,
		ReasoningSignatures: []ReasoningSignature{sig},
	})

	turn, ok := lookupThinkingReplayTurn("session-a", "p1:m", "let me check", []string{"toolu_1"})
	if !ok {
		t.Fatalf("cached turn not found")
	}
	if turn.reasoning != reasoning || len(turn.signatures) != 1 || turn.signatures[0].Signature != sig.Signature {
		t.Errorf("cached turn mismatch: %+v", turn)
	}
	// A different identity must not match. Wrong call IDs block the match even
	// with identical text...
	if _, ok := lookupThinkingReplayTurn("session-a", "p1:m", "let me check", []string{"toolu_9"}); ok {
		t.Errorf("turn matched on wrong call IDs")
	}
	// ...while a tool-call turn is matched by its call IDs, so a text difference
	// alone (whitespace, or how multiple text blocks were joined) does not hide
	// the turn's reasoning.
	if _, ok := lookupThinkingReplayTurn("session-a", "p1:m", "other text", []string{"toolu_1"}); !ok {
		t.Errorf("tool-call turn was not matched by its call IDs")
	}
	// Different session or model family must not match (no cross-caller leak).
	if _, ok := lookupThinkingReplayTurn("session-b", "p1:m", "let me check", []string{"toolu_1"}); ok {
		t.Errorf("turn leaked across sessions")
	}
	if _, ok := lookupThinkingReplayTurn("session-a", "p2:m", "let me check", []string{"toolu_1"}); ok {
		t.Errorf("turn leaked across model families")
	}
	// No session identity: nothing is cached.
	cacheThinkingReplayFromMessage("", "p1:m", OpenAIChatMessage{Role: "assistant", Content: "x", ReasoningContent: &reasoning})
	if _, ok := lookupThinkingReplayTurn("", "p1:m", "x", nil); ok {
		t.Errorf("cache answered a request without a session identity")
	}
}

// TestThinkingReplay_Caps verifies the per-session turn bound.
func TestThinkingReplay_Caps(t *testing.T) {
	resetThinkingReplayCache()
	reasoning := "plan"
	for i := 0; i < thinkingReplayMaxTurnsPerSession+10; i++ {
		cacheThinkingReplayFromMessage("session-caps", "p1:m", OpenAIChatMessage{
			Role:             "assistant",
			Content:          "turn " + itoa(i),
			ReasoningContent: &reasoning,
		})
	}
	thinkingReplayCache.mu.Lock()
	session := thinkingReplayCache.sessions["session-caps\x00p1:m"]
	got := len(session)
	thinkingReplayCache.mu.Unlock()
	if got > thinkingReplayMaxTurnsPerSession {
		t.Fatalf("session turns = %d, want <= %d", got, thinkingReplayMaxTurnsPerSession)
	}
	// Oldest dropped, newest kept.
	if _, ok := lookupThinkingReplayTurn("session-caps", "p1:m", "turn 0", nil); ok {
		t.Errorf("oldest turn was not evicted")
	}
	if _, ok := lookupThinkingReplayTurn("session-caps", "p1:m", "turn "+itoa(thinkingReplayMaxTurnsPerSession+9), nil); !ok {
		t.Errorf("newest turn missing")
	}
}

// TestThinkingReplay_TTL verifies expired turns are pruned on access.
func TestThinkingReplay_TTL(t *testing.T) {
	resetThinkingReplayCache()
	reasoning := "plan"
	cacheThinkingReplayFromMessage("session-ttl", "p1:m", OpenAIChatMessage{
		Role:             "assistant",
		Content:          "old turn",
		ReasoningContent: &reasoning,
	})
	thinkingReplayCache.mu.Lock()
	for i := range thinkingReplayCache.sessions["session-ttl\x00p1:m"] {
		thinkingReplayCache.sessions["session-ttl\x00p1:m"][i].at = time.Now().Add(-2 * time.Hour)
	}
	thinkingReplayCache.mu.Unlock()

	if _, ok := lookupThinkingReplayTurn("session-ttl", "p1:m", "old turn", nil); ok {
		t.Fatalf("expired turn was still replayable")
	}
	thinkingReplayCache.mu.Lock()
	_, present := thinkingReplayCache.sessions["session-ttl\x00p1:m"]
	thinkingReplayCache.mu.Unlock()
	if present {
		t.Errorf("expired session was not pruned")
	}
}

// TestRestoreReasoningReplay verifies restored state lands only on messages
// that need it, and only when the caller asked for that field.
func TestRestoreReasoningReplay(t *testing.T) {
	resetThinkingReplayCache()
	reasoning := "the plan"
	cacheThinkingReplayFromMessage("session-r", "p1:m", OpenAIChatMessage{
		Role:                "assistant",
		Content:             "check the file",
		ToolCalls:           []OpenAIToolCall{{ID: "toolu_7", Type: "function"}},
		ReasoningContent:    &reasoning,
		ReasoningSignatures: []ReasoningSignature{{Signature: "blob"}},
	})

	// Responses direction: signature restored, reasoning_content left alone.
	msgs := []OpenAIChatMessage{{
		Role:      "assistant",
		Content:   "check the file",
		ToolCalls: []OpenAIToolCall{{ID: "toolu_7", Type: "function"}},
	}}
	if n := restoreReasoningReplay("session-r", "p1:m", msgs, true, false); n != 1 {
		t.Fatalf("restored = %d, want 1", n)
	}
	if len(msgs[0].ReasoningSignatures) != 1 || msgs[0].ReasoningSignatures[0].Signature != "blob" {
		t.Errorf("signature not restored: %+v", msgs[0].ReasoningSignatures)
	}
	if msgs[0].ReasoningContent != nil {
		t.Errorf("reasoning_content restored when only signatures were wanted")
	}

	// Chat direction: reasoning_content restored onto a tool-call turn.
	msgs = []OpenAIChatMessage{{
		Role:      "assistant",
		Content:   "check the file",
		ToolCalls: []OpenAIToolCall{{ID: "toolu_7", Type: "function"}},
	}}
	if n := restoreReasoningReplay("session-r", "p1:m", msgs, false, true); n != 1 {
		t.Fatalf("restored = %d, want 1", n)
	}
	if msgs[0].ReasoningContent == nil || *msgs[0].ReasoningContent != reasoning {
		t.Errorf("reasoning_content not restored: %v", msgs[0].ReasoningContent)
	}

	// An assistant turn that already carries state is never overwritten.
	existing := "fresh plan"
	msgs = []OpenAIChatMessage{{
		Role:                "assistant",
		Content:             "check the file",
		ToolCalls:           []OpenAIToolCall{{ID: "toolu_7", Type: "function"}},
		ReasoningContent:    &existing,
		ReasoningSignatures: []ReasoningSignature{{Signature: "fresh-blob"}},
	}}
	if n := restoreReasoningReplay("session-r", "p1:m", msgs, true, true); n != 0 {
		t.Fatalf("restored = %d, want 0 (message already had state)", n)
	}
	if *msgs[0].ReasoningContent != existing || msgs[0].ReasoningSignatures[0].Signature != "fresh-blob" {
		t.Errorf("existing state was overwritten")
	}
}

// TestTranslateToOpenAIWithPreservation_AlwaysNever verifies the per-model
// setting reaches the translation: "always" replays thinking history for a
// model the name inference would ignore, "never" drops it for a vendor the
// inference would keep.
func TestTranslateToOpenAIWithPreservation_AlwaysNever(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"type": "thinking", "thinking": "my plan", "signature": ""},
		map[string]interface{}{"type": "tool_use", "id": "t1", "name": "Write", "input": map[string]interface{}{}},
	}
	req := &AnthropicRequest{
		Model: "plain-model",
		Messages: []AnthropicMessage{
			{Role: "assistant", Content: blocks},
			{Role: "user", Content: []interface{}{map[string]interface{}{"type": "thinking", "thinking": "user plan"}}},
		},
	}
	msgs := translateToOpenAIWithPreservation(req, false, toolImagesRelayed, true).Messages
	thinkingOnToolHistory := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.ReasoningContent != nil && *m.ReasoningContent == "my plan" {
			thinkingOnToolHistory = true
		}
	}
	if !thinkingOnToolHistory {
		t.Errorf("preserveReasoningContent=true did not replay thinking history: %#v", msgs)
	}
	// The user-role thinking block must stay ignored regardless.
	for _, m := range msgs {
		if m.Role == "user" && m.ReasoningContent != nil {
			t.Errorf("user-role thinking was mapped to reasoning_content")
		}
	}

	msgs = translateToOpenAIWithPreservation(req, false, toolImagesRelayed, false).Messages
	for _, m := range msgs {
		if m.ReasoningContent != nil {
			t.Errorf("preserveReasoningContent=false kept reasoning_content")
		}
	}
}

// TestOpenAIInbound_ReplayReachesResponsesBody verifies the Chat Completions ->
// Responses path, which uses includeReasoning=replayed>0, actually emits the
// restored signature as a reasoning item plus the include it needs, and stays
// untouched when there is nothing to replay.
func TestOpenAIInbound_ReplayReachesResponsesBody(t *testing.T) {
	resetThinkingReplayCache()
	sig := "encrypted-blob"
	cacheThinkingReplayFromMessage("pc-1", "p1:m", OpenAIChatMessage{
		Role:                "assistant",
		Content:             "check the file",
		ToolCalls:           []OpenAIToolCall{{ID: "call_1", Type: "function", Function: OpenAIToolCallFunc{Name: "shell"}}},
		ReasoningSignatures: []ReasoningSignature{{Signature: sig}},
	})

	rp := &config.ResolvedProvider{ProviderID: "p1"}
	req := &OpenAIChatRequest{
		Model:          "m",
		PromptCacheKey: "pc-1",
		Messages: []OpenAIChatMessage{
			{Role: "user", Content: "go"},
			{Role: "assistant", Content: "check the file", ToolCalls: []OpenAIToolCall{{ID: "call_1", Type: "function", Function: OpenAIToolCallFunc{Name: "shell"}}}},
			{Role: "tool", ToolID: "call_1", Content: "ok"},
		},
	}
	replayed := restoreThinkingReplayForOpenAI(nil, req, rp)
	if replayed != 1 {
		t.Fatalf("restored = %d, want 1", replayed)
	}
	body := translateChatCompletionsToCodexResponses(req, replayed > 0)
	if _, ok := body["include"]; !ok {
		t.Errorf("replayed signature did not request include=reasoning.encrypted_content")
	}
	include, _ := body["include"].([]string)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", body["include"])
	}
	found := false
	items, _ := body["input"].([]interface{})
	for _, raw := range items {
		item, _ := raw.(map[string]interface{})
		if item != nil && item["type"] == "reasoning" && item["encrypted_content"] == sig {
			found = true
		}
	}
	if !found {
		t.Errorf("restored signature not present in the Responses input: %#v", body["input"])
	}

	// Without a session identity nothing is restored, so the body keeps the
	// plain shape (no include, no reasoning items) callers relied on before.
	plain := &OpenAIChatRequest{
		Model: "m",
		Messages: []OpenAIChatMessage{
			{Role: "assistant", Content: "check the file", ToolCalls: []OpenAIToolCall{{ID: "call_1", Type: "function", Function: OpenAIToolCallFunc{Name: "shell"}}}},
		},
	}
	if n := restoreThinkingReplayForOpenAI(nil, plain, rp); n != 0 {
		t.Fatalf("restored = %d without a session identity, want 0", n)
	}
	plainBody := translateChatCompletionsToCodexResponses(plain, false)
	if _, ok := plainBody["include"]; ok {
		t.Errorf("plain request gained an include")
	}
	for _, raw := range plainBody["input"].([]interface{}) {
		item, _ := raw.(map[string]interface{})
		if item != nil && item["type"] == "reasoning" {
			t.Errorf("plain request gained a reasoning item")
		}
	}
}

// TestRestoreReasoningReplay_ReplacesPlaceholder covers the compaction case:
// Claude Code rewrote the turn's thinking to redacted_thinking, so the
// translation could only synthesize the "tool call" placeholder; the session's
// cached reasoning must take its place.
func TestRestoreReasoningReplay_ReplacesPlaceholder(t *testing.T) {
	resetThinkingReplayCache()
	reasoning := "the real plan"
	cacheThinkingReplayFromMessage("session-p", "p1:m", OpenAIChatMessage{
		Role:             "assistant",
		Content:          "run the test",
		ToolCalls:        []OpenAIToolCall{{ID: "toolu_3", Type: "function"}},
		ReasoningContent: &reasoning,
	})

	// The translated form of a compacted turn: tool call kept, thinking gone, so
	// the translator filled in the placeholder.
	translated := translateToOpenAIWithPreservation(&AnthropicRequest{
		Model: "deepseek-v4.1-flash_cloud",
		Messages: []AnthropicMessage{{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "redacted_thinking", "data": "opaque"},
				map[string]interface{}{"type": "text", "text": "run the test"},
				map[string]interface{}{"type": "tool_use", "id": "toolu_3", "name": "Bash", "input": map[string]interface{}{"cmd": "go test"}},
			},
		}},
	}, false, toolImagesRelayed, true).Messages
	if len(translated) != 1 || translated[0].ReasoningContent == nil || *translated[0].ReasoningContent != "[redacted thinking]" {
		t.Fatalf("expected the redacted-thinking placeholder, got %#v", translated)
	}
	if !translated[0].ReasoningPlaceholder {
		t.Fatalf("synthesized reasoning was not marked as a placeholder")
	}

	if n := restoreReasoningReplay("session-p", "p1:m", translated, false, true); n != 1 {
		t.Fatalf("restored = %d, want 1", n)
	}
	if *translated[0].ReasoningContent != reasoning || translated[0].ReasoningPlaceholder {
		t.Errorf("placeholder not replaced: %q placeholder=%v", *translated[0].ReasoningContent, translated[0].ReasoningPlaceholder)
	}

	// Real (non-placeholder) client reasoning is still never overwritten.
	real := "the client's own reasoning"
	translated[0].ReasoningContent = &real
	translated[0].ReasoningPlaceholder = false
	if n := restoreReasoningReplay("session-p", "p1:m", translated, false, true); n != 0 {
		t.Errorf("restored = %d over real reasoning, want 0", n)
	}
	if *translated[0].ReasoningContent != real {
		t.Errorf("real reasoning was overwritten: %q", *translated[0].ReasoningContent)
	}
}

// TestThinkingReplay_UpstreamToolIDsAreCanonicalized pins the bug a live probe
// exposed: the upstream mints "call_t78435va", Prism answers the client with
// sanitizeToolUseID's "toolu_callt78435va", and the client echoes that back. A
// cache that stored the raw upstream id could never match the request side.
func TestThinkingReplay_UpstreamToolIDsAreCanonicalized(t *testing.T) {
	resetThinkingReplayCache()
	reasoning := "the real plan"
	// Response side: raw upstream id, as it arrives from the provider.
	cacheThinkingReplayFromMessage("session-ids", "ollama_cloud:m", OpenAIChatMessage{
		Role:             "assistant",
		ToolCalls:        []OpenAIToolCall{{ID: "call_t78435va", Type: "function"}},
		ReasoningContent: &reasoning,
	})
	// Request side: the id the client got from Prism and echoed back.
	placeholder := "[redacted thinking]"
	msgs := []OpenAIChatMessage{{
		Role:                 "assistant",
		ToolCalls:            []OpenAIToolCall{{ID: "toolu_callt78435va", Type: "function"}},
		ReasoningContent:     &placeholder,
		ReasoningPlaceholder: true,
	}}
	if got := sanitizeToolUseID("call_t78435va"); got != "toolu_callt78435va" {
		t.Fatalf("premise changed: sanitizeToolUseID(call_t78435va) = %q", got)
	}
	if n := restoreReasoningReplay("session-ids", "ollama_cloud:m", msgs, false, true); n != 1 {
		t.Fatalf("restored = %d, want 1 (upstream and client id spaces must match)", n)
	}
	if *msgs[0].ReasoningContent != reasoning {
		t.Errorf("reasoning not restored: %q", *msgs[0].ReasoningContent)
	}

	// The other direction: an upstream id that already looks like a chatcmpl
	// tool id is rewritten the same way on both sides.
	resetThinkingReplayCache()
	cacheThinkingReplayFromMessage("session-ids2", "p:m", OpenAIChatMessage{
		Role:             "assistant",
		ToolCalls:        []OpenAIToolCall{{ID: "chatcmpl-tool-abc", Type: "function"}},
		ReasoningContent: &reasoning,
	})
	msgs = []OpenAIChatMessage{{
		Role:                 "assistant",
		ToolCalls:            []OpenAIToolCall{{ID: "toolu_abc", Type: "function"}},
		ReasoningContent:     &placeholder,
		ReasoningPlaceholder: true,
	}}
	if n := restoreReasoningReplay("session-ids2", "p:m", msgs, false, true); n != 1 {
		t.Fatalf("restored = %d, want 1 for a chatcmpl-tool id", n)
	}
}

func resetThinkingReplayCache() {
	thinkingReplayCache.mu.Lock()
	thinkingReplayCache.sessions = make(map[string][]assistantTurnReplay)
	thinkingReplayCache.bytes = 0
	thinkingReplayCache.mu.Unlock()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [24]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
