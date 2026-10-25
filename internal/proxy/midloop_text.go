package proxy

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

// A user line typed while a tool loop is running is delivered by the client at
// the tail of the tool-result message -- and stays there for every following
// round, because the client merges the whole loop into one user turn. Nothing
// in the request marks where the line arrived (verified across 87 captured
// requests carrying a mid-loop line: every one parks it at the tail). So the
// model reads that line as its newest message in round after round: both its
// reasoning and its reply re-process it each time ("The user says X ..." five
// rounds running), and because the line ends up below the answers to it, the
// finished transcript reads as if the model answered a message that had not
// been sent yet -- which is how a model concludes it invented the user's words.
//
// midLoopTextStore remembers, per session, how many tool rounds existed when a
// given line first arrived, and replays that index on later requests. The line
// then stays with the round it interrupted, the round that answered it follows
// it, and it stops drifting to the end of the loop. The first request that
// carries the line is the only signal available, and it is accurate in practice
// because the client picks a typed line up on its next loop iteration.
//
// Entries are scoped to one loop: a line that is no longer present in a
// request's tool-result message is forgotten (see retainMidLoopTexts), so a
// later "continue" in a new turn is not confused with an earlier one. An idle
// TTL and a session cap bound the memory if a client disappears mid-loop.

const (
	midLoopTextSessionIdleTTL = 2 * time.Hour
	midLoopTextMaxSessions    = 4096
	midLoopTextMaxPerSession  = 256
)

// midLoopTextEntry is where one mid-loop user line first arrived: the number of
// tool rounds the loop already had, and when we last saw it.
type midLoopTextEntry struct {
	rounds int
	at     time.Time
}

// midLoopTextPlacement maps a mid-loop user line to the 1-based index of the
// tool round after which it belongs, given how many rounds this request holds.
type midLoopTextPlacement func(text string, rounds int) int

var midLoopTexts = struct {
	mu       sync.Mutex
	sessions map[string]map[string]midLoopTextEntry
}{sessions: make(map[string]map[string]midLoopTextEntry)}

// midLoopTextKey identifies a line by content: sessions replay the same text,
// and the client rewrites nothing about it between rounds.
func midLoopTextKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return string(sum[:16])
}

// placeMidLoopText returns the round a mid-loop user line belongs to. The first
// time a line is seen it is recorded against the current round count and left
// where the client put it (`rounds`); every later request returns the recorded
// round instead. A session-less or empty line, or one whose round count no
// longer fits (a compacted transcript can hold fewer rounds), falls back to the
// client's own position.
func placeMidLoopText(sessionKey, text string, rounds int) int {
	text = strings.TrimSpace(text)
	if sessionKey == "" || text == "" || rounds <= 0 {
		return rounds
	}
	key := midLoopTextKey(text)
	now := time.Now()

	midLoopTexts.mu.Lock()
	defer midLoopTexts.mu.Unlock()
	pruneMidLoopTextsLocked(now)

	session := midLoopTexts.sessions[sessionKey]
	if session == nil {
		session = make(map[string]midLoopTextEntry)
		midLoopTexts.sessions[sessionKey] = session
	}
	if entry, ok := session[key]; ok {
		entry.at = now
		at := entry.rounds
		if at > rounds {
			at = rounds
		}
		session[key] = entry
		return at
	}

	// First sighting: this is where the line arrived, and where it stays.
	session[key] = midLoopTextEntry{rounds: rounds, at: now}
	if len(session) > midLoopTextMaxPerSession {
		evictOldestMidLoopTextsLocked(session, len(session)-midLoopTextMaxPerSession)
	}
	evictMidLoopTextSessionsLocked()
	return rounds
}

// retainMidLoopTexts forgets this session's lines that are not part of the
// request that just arrived. The client drops a mid-loop line when the loop
// ends (it becomes part of the turn's history that is no longer re-sent), so
// what is absent now belongs to an earlier loop.
func retainMidLoopTexts(sessionKey string, present []string) {
	if sessionKey == "" {
		return
	}
	keep := make(map[string]bool, len(present))
	for _, text := range present {
		keep[midLoopTextKey(strings.TrimSpace(text))] = true
	}

	midLoopTexts.mu.Lock()
	defer midLoopTexts.mu.Unlock()
	session := midLoopTexts.sessions[sessionKey]
	if session == nil {
		return
	}
	for key := range session {
		if !keep[key] {
			delete(session, key)
		}
	}
	if len(session) == 0 {
		delete(midLoopTexts.sessions, sessionKey)
	}
}

// pruneMidLoopTextsLocked drops sessions idle past the TTL. Callers hold mu.
func pruneMidLoopTextsLocked(now time.Time) {
	for sessionKey, session := range midLoopTexts.sessions {
		newest := time.Time{}
		for _, entry := range session {
			if entry.at.After(newest) {
				newest = entry.at
			}
		}
		if now.Sub(newest) > midLoopTextSessionIdleTTL {
			delete(midLoopTexts.sessions, sessionKey)
		}
	}
}

// evictMidLoopTextSessionsLocked bounds the number of remembered sessions,
// dropping the least recently used first. Callers hold mu.
func evictMidLoopTextSessionsLocked() {
	for len(midLoopTexts.sessions) > midLoopTextMaxSessions {
		oldestKey, oldest := "", time.Time{}
		for sessionKey, session := range midLoopTexts.sessions {
			newest := time.Time{}
			for _, entry := range session {
				if entry.at.After(newest) {
					newest = entry.at
				}
			}
			if oldestKey == "" || newest.Before(oldest) {
				oldestKey, oldest = sessionKey, newest
			}
		}
		delete(midLoopTexts.sessions, oldestKey)
	}
}

// evictOldestMidLoopTextsLocked drops the oldest entries of one session down to
// the per-session cap. Callers hold mu.
func evictOldestMidLoopTextsLocked(session map[string]midLoopTextEntry, drop int) {
	for i := 0; i < drop; i++ {
		oldestKey, oldest := "", time.Time{}
		for key, entry := range session {
			if oldestKey == "" || entry.at.Before(oldest) {
				oldestKey, oldest = key, entry.at
			}
		}
		if oldestKey == "" {
			return
		}
		delete(session, oldestKey)
	}
}
