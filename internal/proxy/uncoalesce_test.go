package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// coalescedTurn builds the shape Claude Code sends: every tool round of one
// user prompt packed into a single assistant message.
func coalescedTurn(rounds int) AnthropicMessage {
	blocks := make([]interface{}, 0, rounds*3)
	for i := 0; i < rounds; i++ {
		blocks = append(blocks,
			map[string]interface{}{
				"type":      "thinking",
				"thinking":  fmt.Sprintf("round %d plan", i),
				"signature": fmt.Sprintf("sig-%d", i),
			},
			map[string]interface{}{
				"type": "text",
				"text": fmt.Sprintf("round %d closing line", i),
			},
			map[string]interface{}{
				"type":  "tool_use",
				"id":    fmt.Sprintf("toolu_%d", i),
				"name":  "Bash",
				"input": map[string]interface{}{"command": "echo hi"},
			},
		)
	}
	return AnthropicMessage{Role: "assistant", Content: blocks}
}

// coalescedResults builds the single user message that follows it: one result
// per call, with any mid-loop user text pinned at the tail.
func coalescedResults(rounds int, pinned string) AnthropicMessage {
	blocks := make([]interface{}, 0, rounds+1)
	for i := 0; i < rounds; i++ {
		blocks = append(blocks, map[string]interface{}{
			"type":        "tool_result",
			"tool_use_id": fmt.Sprintf("toolu_%d", i),
			"content":     fmt.Sprintf("output %d", i),
		})
	}
	if pinned != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": pinned})
	}
	return AnthropicMessage{Role: "user", Content: blocks}
}

// describe renders a transcript as role[block types] so a failure shows the
// message boundaries the split produced, not a wall of JSON.
func describe(msgs []AnthropicMessage) string {
	var parts []string
	for _, msg := range msgs {
		blocks, ok := msg.Content.([]interface{})
		if !ok {
			parts = append(parts, fmt.Sprintf("%s[%v]", msg.Role, msg.Content))
			continue
		}
		var types []string
		for _, raw := range blocks {
			block, _ := raw.(map[string]interface{})
			blockType, _ := block["type"].(string)
			types = append(types, blockType)
		}
		parts = append(parts, fmt.Sprintf("%s[%s]", msg.Role, strings.Join(types, ",")))
	}
	return strings.Join(parts, " > ")
}

// blockCount counts content blocks across a transcript, to prove the rewrite
// loses nothing.
func blockCount(msgs []AnthropicMessage) int {
	total := 0
	for _, msg := range msgs {
		if blocks, ok := msg.Content.([]interface{}); ok {
			total += len(blocks)
		}
	}
	return total
}

// assertToolAdjacency checks the property the upstream needs: every assistant
// message that calls tools is answered, in order, by the message that follows
// it. Stray results for calls that were never in this turn (an orphaned result
// the client carried over) are tolerated — they are a pre-existing artifact of
// the client's history, and moving them does not create their missing call.
func assertToolAdjacency(t *testing.T, msgs []AnthropicMessage) {
	t.Helper()
	for i, msg := range msgs {
		blocks, _ := msg.Content.([]interface{})
		var calls []string
		for _, raw := range blocks {
			block, _ := raw.(map[string]interface{})
			if blockType, _ := block["type"].(string); blockType == "tool_use" {
				id, _ := block["id"].(string)
				calls = append(calls, id)
			}
		}
		if len(calls) == 0 {
			continue
		}
		if i+1 >= len(msgs) || msgs[i+1].Role != "user" {
			t.Fatalf("message %d (%s) leaves %d call(s) unanswered: %s", i, describe(msgs[i:i+1]), len(calls), describe(msgs))
		}
		next, _ := msgs[i+1].Content.([]interface{})
		var answered []string
		for _, raw := range next {
			block, _ := raw.(map[string]interface{})
			if blockType, _ := block["type"].(string); blockType == "tool_result" {
				id, _ := block["tool_use_id"].(string)
				answered = append(answered, id)
			}
		}
		for _, call := range calls {
			found := false
			for _, id := range answered {
				if id == call {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("message %d calls %v but is answered by %v: %s", i, calls, answered, describe(msgs))
			}
		}
	}
}

func TestSplitCoalescedTurnsPairsEveryRoundWithItsResults(t *testing.T) {
	req := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: "do the thing"},
		coalescedTurn(3),
		coalescedResults(3, "standing line from the user"),
		{Role: "system", Content: "env"},
	}}

	turns, rounds := splitCoalescedTurns(&req)
	if turns != 1 || rounds != 3 {
		t.Fatalf("splitCoalescedTurns = (%d, %d), want (1, 3)", turns, rounds)
	}

	want := "user[do the thing] > " +
		"assistant[thinking,text,tool_use] > user[tool_result] > " +
		"assistant[thinking,text,tool_use] > user[tool_result] > " +
		"assistant[thinking,text,tool_use] > user[tool_result,text] > " +
		"system[env]"
	if got := describe(req.Messages); got != want {
		t.Fatalf("transcript =\n  %s\nwant\n  %s", got, want)
	}
	assertToolAdjacency(t, req.Messages)

	// The mid-loop user text stays where the client had it: last, after the
	// final round's result.
	tail, _ := req.Messages[len(req.Messages)-2].Content.([]interface{})
	last, _ := tail[len(tail)-1].(map[string]interface{})
	if text, _ := last["text"].(string); text != "standing line from the user" {
		t.Fatalf("pinned user text = %q, want %q", text, "standing line from the user")
	}
}

func TestSplitCoalescedTurnsLeavesOneRoundPerMessageAlone(t *testing.T) {
	req := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: "go"},
		coalescedTurn(1),
		coalescedResults(1, ""),
		{Role: "user", Content: "next"},
	}}
	before, _ := json.Marshal(req.Messages)

	if turns, rounds := splitCoalescedTurns(&req); turns != 0 || rounds != 0 {
		t.Fatalf("splitCoalescedTurns = (%d, %d), want no-op", turns, rounds)
	}
	after, _ := json.Marshal(req.Messages)
	if string(before) != string(after) {
		t.Fatalf("transcript changed:\n  before %s\n  after  %s", before, after)
	}
}

func TestSplitCoalescedTurnsIsIdempotent(t *testing.T) {
	req := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: "go"},
		coalescedTurn(4),
		coalescedResults(4, "still there?"),
	}}
	if turns, rounds := splitCoalescedTurns(&req); turns != 1 || rounds != 4 {
		t.Fatalf("first pass = (%d, %d), want (1, 4)", turns, rounds)
	}
	once, _ := json.Marshal(req.Messages)
	if turns, rounds := splitCoalescedTurns(&req); turns != 0 || rounds != 0 {
		t.Fatalf("second pass = (%d, %d), want no-op: %s", turns, rounds, describe(req.Messages))
	}
	twice, _ := json.Marshal(req.Messages)
	if string(once) != string(twice) {
		t.Fatalf("second pass changed the transcript:\n  once  %s\n  twice %s", once, twice)
	}
	assertToolAdjacency(t, req.Messages)
}

func TestSplitCoalescedTurnsKeepsParallelCallsTogether(t *testing.T) {
	// One round that calls two tools at once, then a second round: the boundary
	// is the content after the second call, not the second call itself.
	req := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "assistant", Content: []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "plan one"},
			map[string]interface{}{"type": "text", "text": "checking two things"},
			map[string]interface{}{"type": "tool_use", "id": "tu_a", "name": "Read"},
			map[string]interface{}{"type": "tool_use", "id": "tu_b", "name": "Read"},
			map[string]interface{}{"type": "text", "text": "both failed"},
			map[string]interface{}{"type": "tool_use", "id": "tu_c", "name": "Read"},
		}},
		{Role: "user", Content: []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_a", "content": "a"},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_c", "content": "c"},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_b", "content": "b"},
		}},
	}}

	if turns, rounds := splitCoalescedTurns(&req); turns != 1 || rounds != 2 {
		t.Fatalf("splitCoalescedTurns = (%d, %d), want (1, 2): %s", turns, rounds, describe(req.Messages))
	}
	if got, want := describe(req.Messages),
		"assistant[thinking,text,tool_use,tool_use] > user[tool_result,tool_result] > assistant[text,tool_use] > user[tool_result]"; got != want {
		t.Fatalf("transcript =\n  %s\nwant\n  %s", got, want)
	}
	assertToolAdjacency(t, req.Messages)
}

func TestSplitCoalescedTurnsKeepsUnmatchedBlocks(t *testing.T) {
	// A result for a call this turn does not contain, and an unknown block type:
	// both survive, after the rounds that could claim their results.
	req := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "assistant", Content: []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "plan one"},
			map[string]interface{}{"type": "tool_use", "id": "tu_a", "name": "Read"},
			map[string]interface{}{"type": "text", "text": "round one done"},
			map[string]interface{}{"type": "tool_use", "id": "tu_b", "name": "Read"},
		}},
		{Role: "user", Content: []interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_from_elsewhere", "content": "stale"},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_a", "content": "a"},
			map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_b", "content": "b"},
			map[string]interface{}{"type": "unknown_block", "opaque": true},
		}},
	}}

	before := blockCount(req.Messages)
	if turns, _ := splitCoalescedTurns(&req); turns != 1 {
		t.Fatalf("splitCoalescedTurns = %d turns, want 1: %s", turns, describe(req.Messages))
	}
	if after := blockCount(req.Messages); after != before {
		t.Fatalf("block count changed: %d -> %d: %s", before, after, describe(req.Messages))
	}
	if got, want := describe(req.Messages),
		"assistant[thinking,tool_use] > user[tool_result] > assistant[text,tool_use] > user[tool_result,tool_result,unknown_block]"; got != want {
		t.Fatalf("transcript =\n  %s\nwant\n  %s", got, want)
	}
	assertToolAdjacency(t, req.Messages)
}

func TestSplitCoalescedTurnsLeavesTurnWithoutResultsAlone(t *testing.T) {
	req := AnthropicRequest{Messages: []AnthropicMessage{
		coalescedTurn(2),
		{Role: "user", Content: "unrelated prompt"},
	}}
	before, _ := json.Marshal(req.Messages)

	if turns, rounds := splitCoalescedTurns(&req); turns != 0 || rounds != 0 {
		t.Fatalf("splitCoalescedTurns = (%d, %d), want no-op", turns, rounds)
	}
	after, _ := json.Marshal(req.Messages)
	if string(before) != string(after) {
		t.Fatalf("transcript changed:\n  before %s\n  after  %s", before, after)
	}
}

func TestSplitCoalescedTurnsPoolsConsecutiveResultMessages(t *testing.T) {
	// Some clients coalesce the assistant turn but keep one user message per
	// round; the pairing must not depend on the two agreeing.
	req := AnthropicRequest{Messages: []AnthropicMessage{
		coalescedTurn(3),
		{Role: "user", Content: []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_0"}}},
		{Role: "user", Content: []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_1"}}},
		{Role: "user", Content: []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": "toolu_2"}}},
		{Role: "user", Content: "then what?"},
	}}

	if turns, rounds := splitCoalescedTurns(&req); turns != 1 || rounds != 3 {
		t.Fatalf("splitCoalescedTurns = (%d, %d), want (1, 3): %s", turns, rounds, describe(req.Messages))
	}
	if got, want := describe(req.Messages),
		"assistant[thinking,text,tool_use] > user[tool_result] > assistant[thinking,text,tool_use] > user[tool_result] > assistant[thinking,text,tool_use] > user[tool_result] > user[then what?]"; got != want {
		t.Fatalf("transcript =\n  %s\nwant\n  %s", got, want)
	}
	assertToolAdjacency(t, req.Messages)
}

// The point of the rewrite: the OpenAI chat request the upstream receives gets
// one assistant message per round, each carrying its own text and its own
// reasoning, instead of one message that concatenates every closing line the
// model ever wrote for that prompt.
func TestSplitCoalescedTurnsTranslationStopsConcatenatingRounds(t *testing.T) {
	countRounds := func(msgs []OpenAIChatMessage) (assistants int, reasoning []string) {
		for _, msg := range msgs {
			if msg.Role != "assistant" {
				continue
			}
			assistants++
			if msg.ReasoningContent != nil {
				reasoning = append(reasoning, *msg.ReasoningContent)
			}
		}
		return assistants, reasoning
	}

	coalesced := AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: "go"},
		coalescedTurn(3),
		coalescedResults(3, ""),
	}}
	split := AnthropicRequest{Messages: append([]AnthropicMessage(nil), coalesced.Messages...)}
	if turns, _ := splitCoalescedTurns(&split); turns != 1 {
		t.Fatalf("splitCoalescedTurns = %d turns, want 1", turns)
	}

	before := translateToOpenAIWithPreservation(&coalesced, true, toolImagesInline, true)
	after := translateToOpenAIWithPreservation(&split, true, toolImagesInline, true)

	coalescedAssistants, coalescedReasoning := countRounds(before.Messages)
	if coalescedAssistants != 1 {
		t.Fatalf("coalesced translation produced %d assistant messages, want 1", coalescedAssistants)
	}
	if len(coalescedReasoning) != 1 || !strings.Contains(coalescedReasoning[0], "round 2 plan") || !strings.Contains(coalescedReasoning[0], "round 0 plan") {
		t.Fatalf("coalesced reasoning = %v, want all three rounds concatenated", coalescedReasoning)
	}

	splitAssistants, splitReasoning := countRounds(after.Messages)
	if splitAssistants != 3 {
		t.Fatalf("split translation produced %d assistant messages, want 3", splitAssistants)
	}
	for i, reasoning := range splitReasoning {
		if want := fmt.Sprintf("round %d plan", i); reasoning != want {
			t.Fatalf("assistant %d reasoning = %q, want %q", i, reasoning, want)
		}
	}
	for _, msg := range after.Messages {
		content, _ := msg.Content.(string)
		if msg.Role == "assistant" && strings.Contains(content, "round 0 closing line") && strings.Contains(content, "round 1 closing line") {
			t.Fatalf("assistant message still concatenates rounds: %q", content)
		}
	}
}
