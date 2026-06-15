package codex

import (
	"fmt"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// finalizeAtEOF is the EOF-finalize surface the scanner (Chunk C) calls
// UNCONDITIONALLY when a rollout file has been fully read to EOF (F1). The
// behavior splits on the most-recent open turn's format and on staleness:
//
//   - OLD-format open turn (turn_context-only, no task_started — cli < ~0.93):
//     finalize the turn COMPLETED, REGARDLESS of staleness (spec edge #3 "close
//     at EOF"). Real corpus: 1,006 files (38%) are pure old-format ending cleanly
//     with no completion marker — without this they would be mislabeled crashes.
//     NO SessionFinalizedEvent (codex has no per-session terminal signal — SOW
//     C#3); the session stays running and the UI uses last_activity_ts.
//   - NEW-format open turn (saw a task_started but no task_complete/turn_aborted):
//     finalize the turn FAILED/incomplete AND emit SessionFinalizedEvent(failed,
//     incomplete) ONLY when `stale` (mtime ≥ 1 h — the scanner owns the check;
//     spec rule #23, SOW C#3). On a FRESH new-format file the turn is left open
//     (it is legitimately still running; the next append continues it).
//   - No open turn (clean end, or never opened a turn): nothing — stays running.
//
// nowUs is the synthetic end timestamp (the scanner passes the file mtime in
// micros). Idempotent: a second call is a no-op. The scanner now calls this at
// EVERY full-read EOF (not only when stale) and passes the stale bool, so the
// OLD-format completed-close fires on fresh files too (F1).
func (m *fileMapper) finalizeAtEOF(stale bool, nowUs int64) []canonical.Event {
	if m.eofFinalized {
		return nil
	}
	ts := m.mostRecentOpenTurn()
	if ts == nil {
		// No open turn: the session ended cleanly (or never opened a turn).
		// Codex has no per-session terminal signal, so it stays running. Mark
		// finalized so a later call is a no-op.
		m.eofFinalized = true
		return nil
	}
	if !ts.sawTaskStarted {
		// OLD-format: close COMPLETED at EOF regardless of staleness (spec edge #3).
		// EndTs MUST be the turn's LAST-CONTENT-ACTIVITY timestamp (m.lastContentTsUs,
		// the max ts over content records — which, for the most-recent open turn, IS
		// that turn's last activity), NOT the file mtime / wall-clock (G6) and NOT
		// m.lastTsUs (which a metadata-only session_meta append would advance,
		// re-dating the close — I2). A clean old-format turn ended when its last real
		// record was written. Fall back to nowUs only when no content record carried a
		// timestamp (lastContentTsUs == 0).
		endUs := m.lastContentTsUs
		if endUs == 0 {
			endUs = nowUs
		}
		m.eofFinalized = true
		return m.closeOpenTurnAtEOF(ts, endUs, "completed", "", false)
	}
	// NEW-format: only a stale file's hanging turn is a crash (rule #23). A fresh
	// file's turn is still running — leave it open (do NOT set eofFinalized, so a
	// later stale sweep can still close it).
	if !stale {
		return nil
	}
	m.eofFinalized = true
	return m.closeOpenTurnAtEOF(ts, nowUs, "failed", "incomplete", true)
}

// closeOpenTurnAtEOF finalizes a hanging turn at EOF: its dangling ops are closed
// (cancelled for a crashed new-format turn, completed for a cleanly-ended
// old-format turn), the turn is finalized with the supplied status/errClass, its
// turn-extras log is emitted, and — only when withSessionFinalize is set (the
// stale new-format crash path) — a SessionFinalizedEvent(failed, incomplete) is
// appended (the ONLY SessionFinalizedEvent codex emits; SOW C#3). closeUs is the
// close timestamp: the turn's last-activity ts for the OLD-format completed close
// (deterministic, G6) or the file mtime for the stale new-format crash close. It
// is floored at the turn's start so the synthetic close never predates the open.
func (m *fileMapper) closeOpenTurnAtEOF(ts *turnState, closeUs int64, status, errClass string, withSessionFinalize bool) []canonical.Event {
	endUs := closeUs
	if endUs < ts.startTsUs {
		endUs = ts.startTsUs
	}
	base := func() canonical.EventBase {
		return canonical.EventBase{SourceID: m.sourceID, SourceSeq: 0, Ts: endUs}
	}
	danglingStatus := "cancelled"
	if status == "completed" {
		// An old-format turn that ended cleanly: its in-flight ops (e.g. a final
		// assistant message with no explicit output) are treated as completed, not
		// cancelled — the session did not crash.
		danglingStatus = "completed"
	}
	out := m.finalizeDanglingOps(ts.codexTurnID, base, endUs, danglingStatus)
	out = append(out, m.finalizeTurn(ts, base(), endUs, status, errClass))
	if withSessionFinalize {
		out = append(out, canonical.SessionFinalizedEvent{
			EventBase:  base(),
			NativeID:   m.nativeID,
			Status:     canonical.StatusFailed,
			ErrorClass: "incomplete",
			EndTs:      endUs,
		})
	}
	return out
}

// mostRecentOpenTurn returns the latest-opened turn that has not been finalized,
// or nil when every turn is closed (or none exist). Used by finalizeAtEOF and
// the replaced/superseded-turn helpers.
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

// trimPreview returns at most maxRunes runes of s with surrounding whitespace
// removed, for a non-sensitive Extras preview (e.g. compaction message,
// last_agent_message). Bodies are never inlined wholesale — full content lives
// behind the PayloadRef (spec edge #7).
func trimPreview(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
