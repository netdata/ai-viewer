package opencode

import (
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the SOW-0005 ROUND-6 external-review P1 fix: the boundary-ms
// re-scan must run BEFORE the forward delta (processChanges) on EVERY gated probe,
// against the PRE-ADVANCE cursor, so a co-occurring forward change can never strand
// a same-ms in-place update of a low-id row. (The round-3/4 P1 tests live in
// review_round3_test.go; this is the deeper "co-occurring forward change" case the
// round-3/4 code missed because it ran the re-scan only on the changed==false path.)
//
// The other round-6 fixes are pinned in their natural homes: P2-1 (tool_response
// PayloadRef only when state.output non-empty) in mapper_test.go, P3-1 (retry log
// error.name) in mapper_test.go, P3-2 (resolvePartSession simplification) in
// tailer_branch_test.go, P3-3 (j_file_attachment golden) in testdata + the golden
// suite + golden_invariants_test.go.

// TestP1_R6_CoOccurringForwardChangeDoesNotStrandBoundaryUpdate is the EXACT codex
// round-6 case. Two sessions sit at the SAME table's boundary ms T:
//   - ses_a: an in-place UPDATE re-stamped to ms T with a LOW part id (id ≤ the
//     cursor's MaxTimeUpdatedID). The forward delta's strict `> :tuid` tie-break
//     (time_updated = T AND id > highID) EXCLUDES it; only the boundary re-scan sees
//     it.
//   - ses_b: an in-place UPDATE re-stamped to ms T2 > T (a NORMAL forward change
//     that advances MAX(time_updated)). The gated MAX(time_updated) probe catches it
//     → detectChange returns changed=true, probed=true.
//
// Old (round-3/4) behaviour: changed==true → the boundary re-scan was SKIPPED, and
// processChanges advanced the cursor to (T2, …) — leaving ses_a's row permanently
// below the new watermark, never seen (a zero-gaps violation). Round-6: the boundary
// re-scan runs FIRST against the pre-advance cursor (boundary T), re-emits ses_a,
// THEN processChanges emits ses_b and advances the cursor to T2. BOTH are emitted and
// ses_a is not stranded.
func TestP1_R6_CoOccurringForwardChangeDoesNotStrandBoundaryUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")

	const (
		boundaryMs = int64(100) // T — the cursor's boundary ms
		forwardMs  = int64(200) // T2 > T — the forward change's new ms
	)

	// ses_a: whole tree at ms T=100, part id is LOW ("prt_aaa_low") so the forward
	// delta tie-break (id > highID) excludes it — only the boundary re-scan catches
	// this same-ms in-place update.
	insertSession(t, rw, "ses_a", "", boundaryMs, boundaryMs, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", boundaryMs, boundaryMs, 5, 2)
	insertPart(t, rw, "prt_aaa_low", "msg_a", "ses_a", boundaryMs, boundaryMs, stepFinishBody(5, 2, 0.01))

	// ses_b: a NORMAL forward change — an in-place UPDATE re-stamped to ms T2=200,
	// which advances MAX(time_updated) past the cursor boundary T. Its part id is
	// existing (NOT greater than MaxIDSeen), so the cheap MAX(id) path stays silent
	// and the GATED MAX(time_updated) probe is what fires (probed=true) — exactly the
	// state in which the boundary re-scan gate is open.
	insertSession(t, rw, "ses_b", "", forwardMs, forwardMs, 0)
	insertAssistantMessage(t, rw, "msg_b", "ses_b", forwardMs, forwardMs, 7, 3)
	insertPart(t, rw, "prt_bbb_fwd", "msg_b", "ses_b", forwardMs, forwardMs, stepFinishBody(7, 3, 0.02))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// Cursor at (T=100, "zzz_high") on every table: MaxIDSeen "zzz_high" is greater
	// than BOTH planted part ids, so the cheap MAX(id) path is silent for both (no
	// INSERT); the boundary ms is T=100; the tie-break id "zzz_high" is greater than
	// prt_aaa_low, so the forward delta excludes ses_a but includes ses_b (T2 > T).
	cur := newCursor()
	for _, table := range trackedTables {
		cur = cur.withTable(table, TableWatermark{
			MaxIDSeen:        "zzz_high",
			MaxTimeUpdatedMs: boundaryMs,
			MaxTimeUpdatedID: "zzz_high",
		})
	}

	// WARM start: this cursor at (T, highID) came from REAL prior paging (a Scan
	// cursor / a Tail that has paged), so the boundary bucket was already emitted and
	// boundaryReal starts true — the codex stranding case is a warm cursor. (A cold
	// HEAD-snapshot Tail is the separate TestP1_R6_ColdFirstProbe… case below.)
	// Gate open via the SAFETY NET (no WAL event needed): the 60 s net is due. This is
	// the harder trigger (no WAL hint); the unified trigger must fire the boundary
	// re-scan here too (boundaryReal=true is the single cold guard — round-7 P2-1).
	st := newPollState(true)
	st.markProbe(time.Now().Add(-2 * timeUpdatedSafetyNet)) // net due; no WAL event

	out := make(chan canonical.Event, 512)
	active, err := pollOnce(ctxBG(), testPollRequest(db, schema, &cur, "opencode:test", &st, out, func(error) {}))
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	got := drainAll(out)

	// BOTH sessions must be emitted: ses_b via the forward delta, ses_a via the
	// boundary re-scan. The round-3/4 code emitted ONLY ses_b (ses_a stranded).
	if !hasSession(got, "ses_b") {
		t.Errorf("forward-change session ses_b was not emitted (forward delta must emit it)")
	}
	if !hasSession(got, "ses_a") {
		t.Fatalf("STRANDED: same-ms in-place-updated session ses_a was not emitted — a co-occurring forward change advanced the cursor past boundary ms %d before the boundary re-scan ran (round-6 P1 regression)", boundaryMs)
	}

	// The cursor advanced to the forward change's ms T2 (the forward delta's job); the
	// boundary re-scan itself never advances the cursor.
	if cur.Tables["part"].MaxTimeUpdatedMs != forwardMs {
		t.Errorf("cursor part MaxTimeUpdatedMs = %d, want %d (forward delta advances to T2)", cur.Tables["part"].MaxTimeUpdatedMs, forwardMs)
	}
	if !active {
		t.Error("pollOnce reported active=false; a forward change + boundary re-emit both ran, want active=true")
	}
}

// TestP1_R6_ColdFirstProbeStillGuardsBoundaryReplay re-pins the cold-Tail replay
// guard under the unified trigger: the boundary re-scan runs before the forward
// delta, but a fresh COLD Tail (boundaryReal==false, no preceding Scan) must STILL
// NOT replay the boundary bucket (it is a HEAD-snapshot reconciliation, not a real
// in-place update). boundaryReal is the single cold guard (round-7 P2-1): it gates
// the re-scan on EVERY path, so even with the gate open the cold snapshot boundary
// is not replayed until the cursor first advances.
func TestP1_R6_ColdFirstProbeStillGuardsBoundaryReplay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// A session whose tree sits at the boundary ms with a LOW part id (the boundary
	// bucket the guard must NOT replay on the cold first probe).
	insertSession(t, rw, "ses_boundary", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_b", "ses_boundary", 100, 100, 5, 2)
	insertPart(t, rw, "prt_aaa_low", "msg_b", "ses_boundary", 100, 100, stepFinishBody(5, 2, 0.01))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	cur := newCursor()
	for _, table := range trackedTables {
		cur = cur.withTable(table, TableWatermark{
			MaxIDSeen:        "zzz_high",
			MaxTimeUpdatedMs: 100,
			MaxTimeUpdatedID: "zzz_high",
		})
	}

	// Cold Tail: fresh pollState (boundaryReal==false), no WAL event. The net is
	// immediately due so the gate is open (and the cheap MAX(id) path is silent here),
	// but the boundary re-scan must not run — boundaryReal==false suppresses it on the
	// gate-open path (the HEAD-snapshot replay guard; round-7 P2-1 single cold guard).
	st := newPollState(false)
	out := make(chan canonical.Event, 256)
	if _, err := pollOnce(ctxBG(), testPollRequest(db, schema, &cur, "opencode:test", &st, out, func(error) {})); err != nil {
		t.Fatalf("pollOnce (cold first probe): %v", err)
	}
	if got := drainAll(out); hasSession(got, "ses_boundary") {
		t.Fatalf("cold first probe replayed the boundary bucket (round-6 must keep the round-4 cold guard); ses_boundary must NOT be emitted")
	}
}
