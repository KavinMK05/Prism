package proxy

// Claude Code packs every tool round of one user prompt into a single assistant
// message — N x (thinking, text, tool_use) — followed by one user message that
// carries all N tool_results, with any text the user typed mid-loop pinned at
// its tail. That is the CLI's session shape, not the transcript the model
// produced, and a plain chat upstream reads it as one unfinished turn that keeps
// narrating itself: the model's own most recent words are the closing line of
// the previous round, so it writes that line again, the client appends the
// restatement to the same blob, and the next request carries both. A live Claude
// Code session showed five near-identical closing lines restating the same
// resolved question inside one coalesced turn, with 24 thinking blocks flattened
// into a single 103 KB reasoning_content.
//
// splitCoalescedTurns rewrites that shape back into one assistant message per
// round, each followed by the tool results that answer its calls — the shape the
// rounds were produced in, and the shape the reasoning replay cache is keyed by.
// A round ends exactly where the model ended it (a tool_use followed by new
// content), so parallel calls stay together. The pass is bounded and idempotent:
// a transcript that already has one round per message is returned untouched.

// toolRound is one model round inside a coalesced assistant turn: the content it
// emitted plus the ids of the tool calls it made, in order.
type toolRound struct {
	blocks []interface{}
	calls  []string
}

// assistantRounds splits an assistant message's blocks into tool rounds. It
// reports false when the message holds fewer than two rounds that call a tool,
// which is the only shape worth rewriting — everything else is already one
// round per message, or content the client interleaved for its own purposes.
func assistantRounds(content interface{}) ([]toolRound, bool) {
	blocks, ok := content.([]interface{})
	if !ok || len(blocks) == 0 {
		return nil, false
	}
	var rounds []toolRound
	cur := toolRound{}
	for _, raw := range blocks {
		block, ok := raw.(map[string]interface{})
		if !ok {
			cur.blocks = append(cur.blocks, raw)
			continue
		}
		if blockType, _ := block["type"].(string); blockType == "tool_use" {
			cur.blocks = append(cur.blocks, raw)
			if id, _ := block["id"].(string); id != "" {
				cur.calls = append(cur.calls, id)
			}
			continue
		}
		if len(cur.calls) > 0 {
			// Content after a call starts the next round: the model emitted it
			// either as this round's closing line or as the next round's
			// preamble, and either way the rounds are two.
			rounds = append(rounds, cur)
			cur = toolRound{}
		}
		cur.blocks = append(cur.blocks, raw)
	}
	if len(cur.blocks) > 0 || len(cur.calls) > 0 {
		rounds = append(rounds, cur)
	}
	callRounds := 0
	for _, round := range rounds {
		if len(round.calls) > 0 {
			callRounds++
		}
	}
	if len(rounds) < 2 || callRounds < 2 {
		return nil, false
	}
	return rounds, true
}

// collectResultBlocks gathers the content blocks of the consecutive user
// messages that follow an assistant turn when they carry tool results, and
// reports how many messages were consumed. Consecutive messages are pooled so
// the pairing does not depend on whether the client coalesced its user turns
// the same way it coalesced the assistant turn.
func collectResultBlocks(messages []AnthropicMessage, start int) ([]interface{}, int) {
	var blocks []interface{}
	consumed := 0
	for i := start; i < len(messages); i++ {
		if messages[i].Role != "user" {
			break
		}
		msgBlocks, ok := messages[i].Content.([]interface{})
		if !ok || !hasToolResult(msgBlocks) {
			break
		}
		blocks = append(blocks, msgBlocks...)
		consumed++
	}
	return blocks, consumed
}

func hasToolResult(blocks []interface{}) bool {
	for _, raw := range blocks {
		block, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if blockType, _ := block["type"].(string); blockType == "tool_result" {
			return true
		}
	}
	return false
}

// splitCoalescedTurns rewrites every client-coalesced assistant turn in req. It
// returns the number of assistant turns expanded and the number of tool rounds
// recovered (0, 0 when the transcript already has one round per message).
func splitCoalescedTurns(req *AnthropicRequest) (int, int) {
	if req == nil || len(req.Messages) < 2 {
		return 0, 0
	}
	out := make([]AnthropicMessage, 0, len(req.Messages)+8)
	expanded, recovered := 0, 0
	for i := 0; i < len(req.Messages); i++ {
		rounds, ok := assistantRounds(req.Messages[i].Content)
		if !ok || req.Messages[i].Role != "assistant" {
			out = append(out, req.Messages[i])
			continue
		}
		results, consumed := collectResultBlocks(req.Messages, i+1)
		if consumed == 0 {
			// No results to pair the rounds with: leave the turn alone rather
			// than invent boundaries the client did not send.
			out = append(out, req.Messages[i])
			continue
		}

		// Claim the results that answer this turn's calls; everything else the
		// user turn carried (a result for an unknown call, a duplicate, the
		// mid-loop user text Claude Code pins at the tail) is kept in source
		// order and re-attached after the last round's results.
		known := map[string]bool{}
		for _, round := range rounds {
			for _, id := range round.calls {
				known[id] = true
			}
		}
		byID := map[string]interface{}{}
		var extra []interface{}
		for _, raw := range results {
			block, _ := raw.(map[string]interface{})
			id, _ := block["tool_use_id"].(string)
			if id != "" && known[id] {
				if _, dup := byID[id]; !dup {
					byID[id] = raw
					continue
				}
			}
			extra = append(extra, raw)
		}

		lastRoundPaired := false
		for ri, round := range rounds {
			out = append(out, AnthropicMessage{Role: "assistant", Content: round.blocks})
			claimed := make([]interface{}, 0, len(round.calls))
			for _, id := range round.calls {
				if block, found := byID[id]; found {
					claimed = append(claimed, block)
					delete(byID, id)
				}
			}
			lastRoundPaired = ri == len(rounds)-1 && len(claimed) > 0
			if len(claimed) > 0 {
				out = append(out, AnthropicMessage{Role: "user", Content: claimed})
			}
		}
		// Leftover results keep their source order too.
		for _, raw := range results {
			block, _ := raw.(map[string]interface{})
			id, _ := block["tool_use_id"].(string)
			if id != "" && byID[id] != nil {
				extra = append(extra, byID[id])
				delete(byID, id)
			}
		}
		if len(extra) > 0 {
			if n := len(out); lastRoundPaired && n > 0 && out[n-1].Role == "user" {
				blocks, _ := out[n-1].Content.([]interface{})
				out[n-1].Content = append(blocks, extra...)
			} else {
				out = append(out, AnthropicMessage{Role: "user", Content: extra})
			}
		}

		i += consumed
		expanded++
		recovered += len(rounds)
	}
	req.Messages = out
	return expanded, recovered
}
