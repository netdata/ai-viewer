package codex

import (
	"fmt"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// finalizeStale is the EOF-finalize surface the scanner (Chunk C) calls when a
// rollout file has reached EOF AND its mtime is stale (>= 1 h — the scanner owns
// the mtime check; spec rule #23, SOW C#3). nowUs is the synthetic end
// timestamp (the scanner passes the file mtime in micros). When the most-recent
// turn is still open (no task_complete / turn_aborted) the mapper emits a
// synthetic TurnFinalizedEvent(failed, incomplete) for it AND a
// SessionFinalizedEvent(failed, incomplete) for the session — the ONLY
// SessionFinalizedEvent codex ever emits. A cleanly-ended session (most recent
// turn already finalized) returns no events and stays running (SOW C#3:
// no clean-EOF completed finalize). Idempotent: a second call is a no-op.
//
// The scanner MUST NOT call this on a fresh (mtime < 1 h) file — an in-flight
// turn there is legitimately still running and must stay open for the next
// append (spec rule #23 "keep turn open").
func (m *fileMapper) finalizeStale(nowUs int64) []canonical.Event {
	if m.staleFinalized {
		return nil
	}
	m.staleFinalized = true
	ts := m.mostRecentOpenTurn()
	if ts == nil {
		// No open turn: the session ended cleanly (or never opened a turn).
		// Codex has no per-session terminal signal, so it stays running.
		return nil
	}
	endUs := nowUs
	if endUs < ts.startTsUs {
		endUs = ts.startTsUs
	}
	base := func() canonical.EventBase {
		return canonical.EventBase{SourceID: m.sourceID, SourceSeq: 0, Ts: endUs}
	}
	// Finalize the hanging turn's dangling ops as cancelled (the process died
	// mid-turn, so in-flight tool/llm ops never completed), then close the turn
	// failed/incomplete and the session failed/incomplete (the ONLY
	// SessionFinalizedEvent codex emits — spec rule #23, SOW C#3).
	out := m.finalizeDanglingOps(ts.codexTurnID, base, endUs, "cancelled")
	out = append(out, m.finalizeTurn(ts, base(), endUs, "failed", "incomplete"))
	if ev := m.turnExtrasLog(ts, base()); ev != nil {
		out = append(out, ev)
	}
	out = append(out, canonical.SessionFinalizedEvent{
		EventBase:  base(),
		NativeID:   m.nativeID,
		Status:     canonical.StatusFailed,
		ErrorClass: "incomplete",
		EndTs:      endUs,
	})
	return out
}

// mostRecentOpenTurn returns the latest-opened turn that has not been finalized,
// or nil when every turn is closed (or none exist). Used by finalizeStale.
func (m *fileMapper) mostRecentOpenTurn() *turnState {
	for i := len(m.turnOrder) - 1; i >= 0; i-- {
		if ts, ok := m.turns[m.turnOrder[i]]; ok && !ts.finalized {
			return ts
		}
	}
	return nil
}

// packSeq packs (recordIdx, subIdx) into a single uint64 that is monotone per
// file. subIdx is masked to subEventBits. Mirrors claude_code.
func packSeq(recordIdx, subIdx uint64) uint64 {
	return recordIdx<<subEventBits | (subIdx & (maxSubEventsPerRecord - 1))
}

// parseTsToMicros decodes an RFC3339 timestamp into UNIX microseconds. Codex
// writes UTC RFC3339 with millisecond precision (spec adapter-codex.md:56);
// nano precision is accepted too.
func parseTsToMicros(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("ts %q: %w", s, err)
	}
	return t.UnixMicro(), nil
}

// trimPreview returns at most max runes of s with surrounding whitespace
// removed, for a non-sensitive Extras preview (e.g. compaction message,
// last_agent_message). Bodies are never inlined wholesale — full content lives
// behind the PayloadRef (spec edge #7).
func trimPreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
