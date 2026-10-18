package proxy

import (
	"fmt"
	"testing"
	"time"
)

// loopRequest builds one request of a running tool loop: n rounds of
// call+result, with the user's mid-loop line parked at the tail of the result
// message, exactly as the client sends it.
func loopRequest(n int, line string) AnthropicRequest {
	return AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: "go"},
		coalescedTurn(n),
		coalescedResults(n, line),
	}}
}

// memoFor mirrors the handler: one session memory, fresh per test.
func memoFor(t *testing.T) midLoopTextPlacement {
	t.Helper()
	session := t.Name()
	return func(text string, rounds int) int {
		at := placeMidLoopText(session, text, rounds)
		retainMidLoopTexts(session, []string{text})
		return at
	}
}

// The line that arrived during round 4 must stay with round 4 when the loop has
// grown to 6: it is no longer the newest message, and the round that answered it
// still follows it.
func TestMidLoopTextStaysAtItsArrivalRound(t *testing.T) {
	place := memoFor(t)
	const line = "standing line from the user"

	arrived := loopRequest(4, line)
	if _, _, _ = splitCoalescedTurns(&arrived, place); describe(arrived.Messages) == "" {
		t.Fatal("split produced no transcript")
	}
	if got, want := describe(arrived.Messages),
		"user[go] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result,text]"; got != want {
		t.Fatalf("first sighting moved the line:\n  %s\nwant\n  %s", got, want)
	}

	grown := loopRequest(6, line)
	turns, rounds, _ := splitCoalescedTurns(&grown, place)
	if turns != 1 || rounds != 6 {
		t.Fatalf("splitCoalescedTurns = (%d, %d), want (1, 6)", turns, rounds)
	}
	if got, want := describe(grown.Messages),
		"user[go] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result,text] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result]"; got != want {
		t.Fatalf("line did not stay at round 4:\n  %s\nwant\n  %s", got, want)
	}
	assertToolAdjacency(t, grown.Messages)

	// Without the memory the same request parks the line at the end of the loop,
	// which is what makes the model re-read it as its newest message.
	blind := loopRequest(6, line)
	_, _, _ = splitCoalescedTurns(&blind, nil)
	if got := describe(blind.Messages); got == describe(grown.Messages) {
		t.Fatalf("nil placement produced the remembered shape; want the client's tail placement")
	}
}

// A remembered line must not survive the loop it arrived in: the next turn's
// line (or its absence) forgets it, so an identical line typed later is treated
// as new instead of being dragged back to the old round.
func TestMidLoopTextIsForgottenWithItsLoop(t *testing.T) {
	place := memoFor(t)
	const line = "continue"

	first := loopRequest(4, line)
	if _, _, _ = splitCoalescedTurns(&first, place); describe(first.Messages) == "" {
		t.Fatal("split produced no transcript")
	}

	// The loop ended: the handler only retains what the next request still
	// carries, and that request carries nothing.
	retainMidLoopTexts(t.Name(), nil)

	next := loopRequest(2, line)
	_, _, _ = splitCoalescedTurns(&next, place)
	if got, want := describe(next.Messages),
		"user[go] > "+
			"assistant[thinking,text,tool_use] > user[tool_result] > "+
			"assistant[thinking,text,tool_use] > user[tool_result,text]"; got != want {
		t.Fatalf("a line from a finished loop was placed at its old round:\n  %s\nwant\n  %s", got, want)
	}
}

// The remembered placement keeps the block the client sent, so a marker the
// client put on its last block survives being moved.
func TestMidLoopTextKeepsItsOriginalBlock(t *testing.T) {
	place := memoFor(t)
	const line = "standing line from the user"
	cacheControl := map[string]interface{}{"type": "ephemeral"}

	req := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: "go"},
		coalescedTurn(2),
		{Role: "user", Content: []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_0", "content": "out 0"},
			map[string]interface{}{"type": "text", "text": line, "cache_control": cacheControl},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_1", "content": "out 1"},
		}},
	}}
	// First sighting records the line, then a longer loop replays the position.
	if _, _, _ = splitCoalescedTurns(&req, place); describe(req.Messages) == "" {
		t.Fatal("split produced no transcript")
	}
	grown := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: "go"},
		coalescedTurn(4),
		{Role: "user", Content: []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_0", "content": "out 0"},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_1", "content": "out 1"},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_2", "content": "out 2"},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_3", "content": "out 3"},
			map[string]interface{}{"type": "text", "text": line, "cache_control": cacheControl},
		}},
	}}
	if _, _, _ = splitCoalescedTurns(&grown, place); describe(grown.Messages) == "" {
		t.Fatal("split produced no transcript")
	}

	// The line was recorded at round 2 and must therefore be in round 2's result
	// message, cache_control and all.
	found := false
	for _, msg := range grown.Messages {
		blocks, _ := msg.Content.([]interface{})
		for _, raw := range blocks {
			block, _ := raw.(map[string]interface{})
			if text, _ := block["text"].(string); text != line {
				continue
			}
			found = true
			if block["cache_control"] == nil {
				t.Fatalf("placed line lost its cache_control marker: %v", block)
			}
		}
	}
	if !found {
		t.Fatalf("line missing from the transcript: %s", describe(grown.Messages))
	}
}

func TestPlaceMidLoopTextWithoutSessionIsStateless(t *testing.T) {
	for _, rounds := range []int{1, 3, 7} {
		if got := placeMidLoopText("", "line", rounds); got != rounds {
			t.Fatalf("placeMidLoopText without a session = %d, want %d", got, rounds)
		}
	}
	if got := placeMidLoopText("session", "   ", 3); got != 3 {
		t.Fatalf("placeMidLoopText with a blank line = %d, want 3", got)
	}
}

func TestPlaceMidLoopTextExpiresIdleSessions(t *testing.T) {
	session := t.Name()
	if got := placeMidLoopText(session, "line", 6); got != 6 {
		t.Fatalf("first placement = %d, want 6", got)
	}
	if got := placeMidLoopText(session, "line", 9); got != 6 {
		t.Fatalf("remembered placement = %d, want 6", got)
	}

	// Age the session past the idle TTL: the line is treated as new again.
	midLoopTexts.mu.Lock()
	for key, entry := range midLoopTexts.sessions[session] {
		entry.at = time.Now().Add(-2 * midLoopTextSessionIdleTTL)
		midLoopTexts.sessions[session][key] = entry
	}
	midLoopTexts.mu.Unlock()
	if got := placeMidLoopText(session, "line", 9); got != 9 {
		t.Fatalf("placement after expiry = %d, want 9", got)
	}
	retainMidLoopTexts(session, nil)
	midLoopTexts.mu.Lock()
	_, stillThere := midLoopTexts.sessions[session]
	midLoopTexts.mu.Unlock()
	if stillThere {
		t.Fatal("retainMidLoopTexts(nil) left the session behind")
	}
}

func TestMidLoopTextCapKeepsNewestLines(t *testing.T) {
	session := t.Name()
	for i := 0; i < midLoopTextMaxPerSession+10; i++ {
		placeMidLoopText(session, fmt.Sprintf("line %d", i), 3)
	}
	midLoopTexts.mu.Lock()
	size := len(midLoopTexts.sessions[session])
	midLoopTexts.mu.Unlock()
	if size > midLoopTextMaxPerSession {
		t.Fatalf("session holds %d lines, cap is %d", size, midLoopTextMaxPerSession)
	}
	retainMidLoopTexts(session, nil)
}
