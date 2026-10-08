package proxy

// Thinking replay cache.
//
// A non-Anthropic upstream's chain of thought is carried to the client as a
// thinking block's signature (a Responses API reasoning item's
// encrypted_content) or, for DeepSeek/Kimi-style Chat Completions endpoints, as
// reasoning_content. The client is expected to echo it back on the next turn.
// That echo is not always available:
//
//   - An OpenAI-protocol client has no field to carry a signature in, so a
//     Chat Completions -> Responses hop loses the upstream's reasoning state on
//     every turn.
//   - Claude Code rewrites historical thinking into redacted_thinking when it
//     compacts the conversation, which drops the signature the model needs to
//     keep its own plan.
//
// This cache keeps the most recent assistant turns of a session (their
// identity — non-thinking text plus tool-call IDs — alongside the reasoning
// text and signatures that belonged to them) and restores the reasoning state
// onto a later request whose matching assistant turn lost it.
//
// Deliberately mirrors CLIProxyAPI's claude/kimi thinking replay caches,
// including their bounds and their privacy rule: an assistant turn is only
// cached and restored under an explicit session identity, so one caller's
// hidden reasoning can never leak into another's request. A request without a
// session identity simply does not participate.

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"ollama-proxy/internal/config"
)

const (
	// thinkingReplayTTL limits how long a cached turn stays replayable.
	thinkingReplayTTL = time.Hour
	// thinkingReplayMaxTurnsPerSession bounds the turns kept for one session.
	thinkingReplayMaxTurnsPerSession = 64
	// thinkingReplayMaxBytesPerSession bounds the cached bytes of one session.
	thinkingReplayMaxBytesPerSession = 8 << 20
	// thinkingReplayMaxTotalBytes bounds the cache's process-wide footprint.
	thinkingReplayMaxTotalBytes = 256 << 20
)

// assistantTurnReplay is one cached assistant turn. text and callIDs together
// are its identity — matching on text alone cannot identify a turn that only
// made tool calls, and on IDs alone cannot identify a pure text turn.
type assistantTurnReplay struct {
	text       string
	callIDs    []string
	reasoning  string
	signatures []ReasoningSignature
	at         time.Time
}

// matches reports whether a request-side turn is the counterpart of this
// cached turn. Tool-call turns are identified by their call ids: the upstream
// mints e.g. "call_t78435va", Prism hands the client sanitizeToolUseID's
// "toolu_callt78435va", and the client echoes that rewritten id back, so
// toolCallIDs canonicalizes both sides into the same space. Ids are minted per
// turn and unique, which makes them a stronger identity than the assistant
// text — text reconstruction can differ in whitespace or in how multiple text
// blocks were joined (the translator joins with "\n", a streamed response is a
// plain concatenation). Text-only turns (no calls on either side) still match
// on text, since they have no other identity.
func (t assistantTurnReplay) matches(text string, callIDs []string) bool {
	if len(t.callIDs) > 0 && len(callIDs) > 0 {
		if len(t.callIDs) != len(callIDs) {
			return false
		}
		for i := range callIDs {
			if t.callIDs[i] != callIDs[i] {
				return false
			}
		}
		return true
	}
	return len(t.callIDs) == 0 && len(callIDs) == 0 && t.text == text
}

func (t assistantTurnReplay) bytes() int {
	n := len(t.text) + len(t.reasoning) + 64 + 8*len(t.callIDs)
	for _, sig := range t.signatures {
		n += len(sig.Signature)
	}
	return n
}

var thinkingReplayCache = struct {
	mu       sync.Mutex
	sessions map[string][]assistantTurnReplay
	bytes    int
}{
	sessions: make(map[string][]assistantTurnReplay),
}

// thinkingReplayFamily scopes cached reasoning to one provider and model. A
// signature minted by one upstream is meaningless to another, so replay must
// never cross that boundary.
func thinkingReplayFamily(rp *config.ResolvedProvider, model string) string {
	if rp == nil {
		return ""
	}
	return rp.ProviderID + ":" + model
}

// claudeCodeSessionSuffixPattern matches the older Claude Code user_id shape
// "<account-hash>_session_<uuid>". Newer builds send a JSON object instead
// (see claudeCodeSessionID).
var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([A-Za-z0-9-]+)`)

// claudeCodeSessionID resolves the session identity of an Anthropic-protocol
// request: the X-Claude-Code-Session-Id header when present (sent by Claude
// Code and by other Anthropic-compatible agents), otherwise metadata.user_id,
// which Claude Code sends either as a JSON object with a session_id or as
// "<device-hash>_session_<uuid>". Empty when the request carries no identity.
func claudeCodeSessionID(r *http.Request, metadata interface{}) string {
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get("X-Claude-Code-Session-Id")); v != "" {
			return v
		}
	}
	m, ok := metadata.(map[string]interface{})
	if !ok {
		return ""
	}
	raw, _ := m["user_id"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var parsed struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		if v := strings.TrimSpace(parsed.SessionID); v != "" {
			return v
		}
	}
	if match := claudeCodeSessionSuffixPattern.FindStringSubmatch(raw); len(match) == 2 {
		return match[1]
	}
	return ""
}

// openAISessionID resolves the session identity of an OpenAI-protocol request.
// The OpenAI Chat Completions protocol has no session field; clients that want
// reasoning continuity opt in by sending prompt_cache_key (Codex CLI does) or
// user. Empty when neither is present.
func openAISessionID(r *http.Request, req *OpenAIChatRequest) string {
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get("X-Claude-Code-Session-Id")); v != "" {
			return v
		}
	}
	if req == nil {
		return ""
	}
	if v := strings.TrimSpace(req.PromptCacheKey); v != "" {
		return v
	}
	return strings.TrimSpace(req.User)
}

// toolCallIDs returns the non-empty call IDs of a message, in order, in the
// client-visible id space. An Anthropic-inbound response hands the client
// sanitizeToolUseID's rewritten id (upstream "call_t78435va" becomes
// "toolu_callt78435va"), which the client echoes back on the next request, so
// canonicalizing both sides here is what lets a cached turn be matched at all.
// The rewrite is idempotent for ids that are already toolu_-shaped.
func toolCallIDs(msg OpenAIChatMessage) []string {
	var ids []string
	for _, tc := range msg.ToolCalls {
		id := strings.TrimSpace(tc.ID)
		if id == "" {
			continue
		}
		ids = append(ids, sanitizeToolUseID(id))
	}
	return ids
}

// cacheThinkingReplayFromMessage records a completed assistant turn (as the
// response collector built it) for later replay. No-op when the turn carries
// neither reasoning state nor an identity, or when there is no session key.
func cacheThinkingReplayFromMessage(sessionKey, family string, msg OpenAIChatMessage) {
	if sessionKey == "" || family == "" {
		return
	}
	reasoning := ""
	if msg.ReasoningContent != nil {
		reasoning = strings.TrimSpace(*msg.ReasoningContent)
	} else if msg.Reasoning != nil {
		reasoning = strings.TrimSpace(*msg.Reasoning)
	}
	signatures := msg.ReasoningSignatures
	if len(signatures) == 0 && reasoning == "" {
		return
	}
	if len(signatures) > maxReasoningBlocksPerTurn {
		signatures = signatures[len(signatures)-maxReasoningBlocksPerTurn:]
	}
	turn := assistantTurnReplay{
		text:       strings.TrimSpace(contentToString(msg.Content)),
		callIDs:    toolCallIDs(msg),
		reasoning:  reasoning,
		signatures: signatures,
		at:         time.Now(),
	}
	if turn.text == "" && len(turn.callIDs) == 0 {
		return
	}

	key := sessionKey + "\x00" + family
	thinkingReplayCache.mu.Lock()
	defer thinkingReplayCache.mu.Unlock()
	now := time.Now()
	pruneThinkingReplayLocked(now)

	session := thinkingReplayCache.sessions[key]
	replaced := false
	if n := len(session); n > 0 && session[n-1].matches(turn.text, turn.callIDs) {
		// The same assistant turn seen again (e.g. a non-streaming retry with
		// more state): replace it rather than growing the session.
		thinkingReplayCache.bytes -= session[n-1].bytes()
		session[n-1] = turn
		replaced = true
	} else {
		session = append(session, turn)
	}
	thinkingReplayCache.bytes += turn.bytes()
	thinkingReplayCache.sessions[key] = session

	// Per-session bounds: drop the oldest turns first.
	for len(session) > thinkingReplayMaxTurnsPerSession || sessionBytes(session) > thinkingReplayMaxBytesPerSession {
		thinkingReplayCache.bytes -= session[0].bytes()
		session = session[1:]
	}
	thinkingReplayCache.sessions[key] = session
	if len(session) == 0 {
		delete(thinkingReplayCache.sessions, key)
	}
	evictThinkingReplayOverBudgetLocked()

	// One line per cached turn (never the session key itself): enough to
	// correlate a later restore with the turn that made it possible.
	action := "cached"
	if replaced {
		action = "updated"
	}
	log.Printf("[Proxy] reasoning replay %s assistant turn for %s (%d block(s), %d reasoning bytes)", action, family, len(turn.signatures), len(turn.reasoning))
}

// sessionBytes is the cached size of one session's turns.
func sessionBytes(session []assistantTurnReplay) int {
	n := 0
	for _, turn := range session {
		n += turn.bytes()
	}
	return n
}

// pruneThinkingReplayLocked drops expired turns (and sessions left empty)
// before any read or write, so the TTL holds without a background goroutine.
func pruneThinkingReplayLocked(now time.Time) {
	for key, session := range thinkingReplayCache.sessions {
		kept := session[:0]
		for _, turn := range session {
			if now.Sub(turn.at) > thinkingReplayTTL {
				thinkingReplayCache.bytes -= turn.bytes()
				continue
			}
			kept = append(kept, turn)
		}
		if len(kept) == 0 {
			delete(thinkingReplayCache.sessions, key)
			continue
		}
		thinkingReplayCache.sessions[key] = kept
	}
}

// evictThinkingReplayOverBudgetLocked drops the globally oldest turns until
// the process-wide byte budget is satisfied.
func evictThinkingReplayOverBudgetLocked() {
	for thinkingReplayCache.bytes > thinkingReplayMaxTotalBytes {
		oldestKey := ""
		oldestIdx := -1
		var oldestAt time.Time
		for key, session := range thinkingReplayCache.sessions {
			if len(session) == 0 {
				continue
			}
			if oldestIdx < 0 || session[0].at.Before(oldestAt) {
				oldestKey, oldestIdx, oldestAt = key, 0, session[0].at
			}
		}
		if oldestIdx < 0 {
			return
		}
		session := thinkingReplayCache.sessions[oldestKey]
		thinkingReplayCache.bytes -= session[0].bytes()
		session = session[1:]
		if len(session) == 0 {
			delete(thinkingReplayCache.sessions, oldestKey)
		} else {
			thinkingReplayCache.sessions[oldestKey] = session
		}
	}
}

// lookupThinkingReplayTurn finds the cached turn that matches an assistant
// message's identity, newest first, so the freshest reasoning state wins.
func lookupThinkingReplayTurn(sessionKey, family, text string, callIDs []string) (assistantTurnReplay, bool) {
	if sessionKey == "" || family == "" || (text == "" && len(callIDs) == 0) {
		return assistantTurnReplay{}, false
	}
	key := sessionKey + "\x00" + family
	thinkingReplayCache.mu.Lock()
	defer thinkingReplayCache.mu.Unlock()
	pruneThinkingReplayLocked(time.Now())
	session := thinkingReplayCache.sessions[key]
	for i := len(session) - 1; i >= 0; i-- {
		if session[i].matches(text, callIDs) {
			return session[i], true
		}
	}
	return assistantTurnReplay{}, false
}

// restoreReasoningReplay fills in missing reasoning state on the translated
// messages of a request. wantSignatures restores Responses-API signatures (for
// a Responses upstream), wantReasoning restores reasoning_content (for a
// DeepSeek/Kimi-style Chat Completions upstream). Returns how many turns were
// restored, for the caller's log line.
func restoreReasoningReplay(sessionKey, family string, messages []OpenAIChatMessage, wantSignatures, wantReasoning bool) int {
	if sessionKey == "" || family == "" {
		return 0
	}
	restored := 0
	for i := range messages {
		msg := &messages[i]
		if msg.Role != "assistant" {
			continue
		}
		needSignatures := wantSignatures && len(msg.ReasoningSignatures) == 0
		// A tool-call turn satisfies DeepSeek/Kimi-style validation with a
		// synthesized placeholder, but the real reasoning is what keeps the
		// model's plan; treat a placeholder as missing.
		needReasoning := wantReasoning && ((msg.ReasoningContent == nil && msg.Reasoning == nil && len(msg.ToolCalls) > 0) || msg.ReasoningPlaceholder)
		if !needSignatures && !needReasoning {
			continue
		}
		text := strings.TrimSpace(contentToString(msg.Content))
		callIDs := toolCallIDs(*msg)
		turn, ok := lookupThinkingReplayTurn(sessionKey, family, text, callIDs)
		if !ok {
			continue
		}
		restoredTurn := false
		if needSignatures && len(turn.signatures) > 0 {
			msg.ReasoningSignatures = append([]ReasoningSignature(nil), turn.signatures...)
			restoredTurn = true
		}
		if needReasoning && turn.reasoning != "" {
			reasoning := turn.reasoning
			msg.ReasoningContent = &reasoning
			msg.ReasoningPlaceholder = false
			restoredTurn = true
		}
		if restoredTurn {
			restored++
		}
	}
	return restored
}

// restoreThinkingReplayForAnthropic restores reasoning state for a translated
// Anthropic-inbound request and logs the result.
func restoreThinkingReplayForAnthropic(r *http.Request, anthroReq *AnthropicRequest, rp *config.ResolvedProvider, openAIReq *OpenAIChatRequest, wantSignatures, wantReasoning bool) int {
	family := thinkingReplayFamily(rp, anthroReq.Model)
	sessionKey := claudeCodeSessionID(r, anthroReq.Metadata)
	n := restoreReasoningReplay(sessionKey, family, openAIReq.Messages, wantSignatures, wantReasoning)
	if n > 0 {
		log.Printf("[Proxy] reasoning replay restored %d assistant turn(s) for %s (%s)", n, anthroReq.Model, rp.ProviderID)
	}
	return n
}

// restoreThinkingReplayForOpenAI restores reasoning signatures for an
// OpenAI-inbound request whose translated form is headed to a Responses
// upstream, and logs the result.
func restoreThinkingReplayForOpenAI(r *http.Request, openAIReq *OpenAIChatRequest, rp *config.ResolvedProvider) int {
	family := thinkingReplayFamily(rp, openAIReq.Model)
	sessionKey := openAISessionID(r, openAIReq)
	n := restoreReasoningReplay(sessionKey, family, openAIReq.Messages, true, false)
	if n > 0 {
		log.Printf("[Proxy] reasoning replay restored %d assistant turn(s) for %s (%s)", n, openAIReq.Model, rp.ProviderID)
	}
	return n
}
