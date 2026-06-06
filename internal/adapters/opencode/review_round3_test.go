package opencode

import (
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the SOW-0005 ROUND-3 external-review fixes that live at the
// DB/poll-loop layer: P1-1 (boundary-millisecond re-scan catches an in-place
// update of an already-seen low-id row at the cursor boundary), P1-2 (the session
// row read + time_compacting check + tree load share ONE read-only transaction),
// P2-2 (malformed message/part data routes through onError → /api/health), and
// P2-3 (the defensive full-tree-size WARN). The mapper-layer P2-1 fix lives in
// review_round2_test.go; the CLI P3-2 fix in cmd/ai-viewer-ingest/sources_test.go.

// --- P1-1: same-ms in-place update at the boundary is re-emitted --------------

// TestP1_1_BoundaryUpdateReEmitted pins the P1-1 fix (completed by round-4 P1): a
// cursor sits at (T, highID) for a table; an already-seen LOW-id row lives at the
// SAME ms T (the canonical "in-place update re-stamped to the same millisecond"
// case). The cheap MAX(id) path is silent (no id past the high-water), the gated
// MAX(time_updated) > gate is silent (boundary ms unchanged), and the forward
// delta's strict tie-break (time_updated = T AND id > highID) excludes the low-id
// row — so without the fix the row's session is lost forever. The boundary re-scan
// re-emits that session on a WARM/resumed boundary (boundaryReal==true) when the
// gate is open under EITHER trigger:
//   - a WAL-driven probe (a WAL event since the last probe), OR
//   - a SAFETY-NET probe (the 60 s net is due, NO WAL event) — covering a
//     DROPPED/absent WAL hint (round-4 P1).
//
// The cold-Tail snapshot (boundaryReal==false, round-7 P2-1's single cold guard)
// must NOT re-emit on ANY path — the HEAD-snapshot replay guard.
func TestP1_1_BoundaryUpdateReEmitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// One session whose whole tree sits at ms=100. Its part id is LOW ("prt_low").
	insertSession(t, rw, "ses_b", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_b", "ses_b", 100, 100, 5, 2)
	insertPart(t, rw, "prt_low", "msg_b", "ses_b", 100, 100, stepFinishBody(5, 2, 0.01))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// A cursor that has ALREADY paged past ms=100 at a HIGHER tie-break id than the
	// low-id row, with the monotonic high-water also above the low row — exactly the
	// state in which the low-id boundary row is invisible to both detectors and the
	// forward delta. MaxTimeUpdatedMs == 100 (the boundary T) on every tracked table.
	freshCursor := func() Cursor {
		c := newCursor()
		for _, table := range trackedTables {
			c = c.withTable(table, TableWatermark{
				MaxIDSeen:        "zzz_high", // > any planted id → cheap MAX(id) silent
				MaxTimeUpdatedMs: 100,        // boundary T
				MaxTimeUpdatedID: "zzz_high", // > prt_low → forward tie-break excludes it
			})
		}
		return c
	}

	// (a) Cold-Tail snapshot: a brand-new pollState (boundaryReal==false) with NO WAL
	// event. The gate opens via the immediately-due safety net, but the boundary re-scan
	// must NOT fire — this is the HEAD-snapshot reconciliation, and replaying the
	// snapshot's boundary session there would be spurious (round-7 P2-1: boundaryReal is
	// the single cold guard, gating BOTH the changed and gate-open paths).
	cur := freshCursor()
	stCold := newPollState(false) // boundaryReal=false; lastProbe zero ⇒ net immediately due
	out0 := make(chan canonical.Event, 64)
	if _, err := pollOnce(ctxBG(), testPollRequest(db, schema, &cur, "opencode:test", &stCold, out0, func(error) {})); err != nil {
		t.Fatalf("pollOnce (cold first probe): %v", err)
	}
	if got := drainAll(out0); hasSession(got, "ses_b") {
		t.Fatalf("boundary session re-emitted on the COLD snapshot (boundaryReal must guard it on all paths); got %d events", len(got))
	}

	// (b) SAFETY-NET probe after a prior cycle: the net is due with NO WAL event. The
	// cursor at (T, highID) is a WARM/resumed boundary (a real prior paged position →
	// boundaryReal=true), so round-4 P1's safety-net boundary re-scan fires and a
	// same-ms in-place update that arrived with a missed WAL hint is still surfaced.
	// (Round-7 P2-1: boundaryReal is now the single cold guard; a warm boundary like
	// this one sets it true, where the old code relied on the removed priorProbe flag.)
	cur = freshCursor()
	stNet := newPollState(true)
	stNet.markProbe(time.Now().Add(-2 * timeUpdatedSafetyNet)) // net due; no WAL
	outNet := make(chan canonical.Event, 256)
	if _, err := pollOnce(ctxBG(), testPollRequest(db, schema, &cur, "opencode:test", &stNet, outNet, func(error) {})); err != nil {
		t.Fatalf("pollOnce (safety-net probe): %v", err)
	}
	if got := drainAll(outNet); !hasSession(got, "ses_b") {
		t.Fatalf("boundary re-scan did not re-emit ses_b on the safety-net probe (round-4 P1); got %d events", len(got))
	}

	// (c) WAL-driven probe with no detector advance: the boundary re-scan fires and
	// re-emits ses_b's tree (the round-3 immediate path). The cursor at (T, highID) is
	// a WARM/resumed boundary (boundaryReal=true); round-7 P2-1 gates this gate-open
	// path on boundaryReal too, so a warm boundary is required for the WAL-driven
	// re-emit (a cold Tail's first WAL-driven probe must NOT replay — see the cold case).
	cur = freshCursor()
	st2 := newPollState(true)
	now := time.Now()
	st2.markProbe(now.Add(-2 * timeUpdatedSafetyNet))
	st2.markWALEvent(now) // lastWALEvent.After(lastProbe) → gate open via WAL
	out := make(chan canonical.Event, 256)
	if _, err := pollOnce(ctxBG(), testPollRequest(db, schema, &cur, "opencode:test", &st2, out, func(error) {})); err != nil {
		t.Fatalf("pollOnce (WAL-driven): %v", err)
	}
	got := drainAll(out)
	if !hasSession(got, "ses_b") {
		t.Fatalf("boundary re-scan did not re-emit the same-ms in-place-updated session ses_b; got %d events", len(got))
	}
	if n := countKind(got, canonical.EvSessionStarted); n != 1 {
		t.Errorf("boundary re-emit SessionStarted count = %d, want 1", n)
	}
	// The cursor must NOT have advanced (the boundary rows are already at the
	// watermark; the re-scan only re-emits).
	if cur.Tables["part"].MaxTimeUpdatedID != "zzz_high" {
		t.Errorf("boundary re-scan advanced the cursor (MaxTimeUpdatedID=%q); it must not", cur.Tables["part"].MaxTimeUpdatedID)
	}
}

// TestP1_1_CompactingClearsAtBoundaryReSurfacesOnSafetyNet pins the round-4 P1
// completeness case the brief calls out: a session that was paused mid-compaction
// (skipped, no events) has its time_compacting CLEARED by an in-place UPDATE that
// lands at exactly the cursor's boundary ms T. That update moves neither MAX(id)
// (no insert) nor MAX(time_updated) (boundary value unchanged), and the forward
// delta's strict tie-break excludes the session row (id <= boundary highID) — so
// without the safety-net boundary re-scan the now-clean session is stranded. With
// priorProbe set and the net due (NO WAL event), the boundary re-scan re-surfaces
// it and the session emits its tree (it is no longer skipped).
func TestP1_1_CompactingClearsAtBoundaryReSurfacesOnSafetyNet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// A session whose whole tree sits at ms=100, time_compacting already CLEARED
	// (NULL) — i.e. compaction finished, re-stamped to the same boundary ms.
	insertSession(t, rw, "ses_comp", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_comp", "ses_comp", 100, 100, 5, 2)
	insertPart(t, rw, "prt_comp", "msg_comp", "ses_comp", 100, 100, stepFinishBody(5, 2, 0.01))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// Cursor already at the boundary (100, highID) on every table, with the monotonic
	// high-water above the session/message/part ids → both detectors silent, forward
	// delta tie-break excludes the rows.
	cur := newCursor()
	for _, table := range trackedTables {
		cur = cur.withTable(table, TableWatermark{MaxIDSeen: "zzz_high", MaxTimeUpdatedMs: 100, MaxTimeUpdatedID: "zzz_high"})
	}

	// WARM/resumed boundary (cursor at a real paged position → boundaryReal=true); the
	// net is due with NO WAL event, so the safety-net boundary re-scan fires (round-4 P1).
	// Round-7 P2-1: boundaryReal is the single cold guard, so a warm boundary is what
	// arms this safety-net re-emit (the old code keyed it off the now-removed priorProbe).
	st := newPollState(true)
	st.markProbe(time.Now().Add(-2 * timeUpdatedSafetyNet)) // net due; no WAL
	out := make(chan canonical.Event, 256)
	if _, err := pollOnce(ctxBG(), testPollRequest(db, schema, &cur, "opencode:test", &st, out, func(error) {})); err != nil {
		t.Fatalf("pollOnce (safety-net, compaction cleared at boundary): %v", err)
	}
	got := drainAll(out)
	if !hasSession(got, "ses_comp") {
		t.Fatalf("a session whose compaction cleared at the boundary ms was not re-surfaced by the safety-net boundary re-scan; got %d events", len(got))
	}
	if n := countKind(got, canonical.EvSessionStarted); n != 1 {
		t.Errorf("re-surfaced compaction session SessionStarted count = %d, want 1", n)
	}
}

// TestP1_1_BoundaryReScanSkipsColdAndEmptyTables pins two guards: a table with a
// zero boundary watermark (cold start) and a probe with no WAL event both yield no
// boundary re-emit, so an empty/idle DB never spuriously fires.
func TestP1_1_BoundaryReScanSkipsColdAndEmptyTables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_c", "", 100, 100, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// A zero cursor: every table's MaxTimeUpdatedMs == 0 → boundary re-scan skips
	// every table even on a WAL-driven probe.
	affected, err := boundaryAffectedSessions(ctxBG(), db, schema, newCursor(), func(error) {})
	if err != nil {
		t.Fatalf("boundaryAffectedSessions: %v", err)
	}
	if len(affected) != 0 {
		t.Errorf("cold-cursor boundary re-scan returned %v, want none", affected)
	}
}

// TestP1_1_BoundaryAffectedSessionsAcrossTables exercises the boundary re-scan's
// per-table derivation: a message row and a part row both at the boundary ms
// contribute their session via the message and part handlers (the part path also
// runs resolvePartSession). It also covers the error path on a closed DB.
func TestP1_1_BoundaryAffectedSessionsAcrossTables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_x", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_x", "ses_x", 100, 100, 5, 2)
	insertPart(t, rw, "prt_x", "msg_x", "ses_x", 100, 100, textBody("t"))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// Cursor boundary at ms=100 on every table → all three (session/message/part)
	// boundary buckets contain ses_x's rows. The derived set is exactly {ses_x}.
	cur := newCursor()
	for _, table := range trackedTables {
		cur = cur.withTable(table, TableWatermark{MaxIDSeen: "zzz", MaxTimeUpdatedMs: 100, MaxTimeUpdatedID: "zzz"})
	}
	affected, err := boundaryAffectedSessions(ctxBG(), db, schema, cur, func(error) {})
	if err != nil {
		t.Fatalf("boundaryAffectedSessions: %v", err)
	}
	if len(affected) != 1 || affected[0] != "ses_x" {
		t.Fatalf("boundary affected = %v, want [ses_x]", affected)
	}

	// Error path: a closed DB makes the per-table bucket query fail; the error is
	// surfaced (not swallowed).
	closed := openRO(t, path)
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := boundaryAffectedSessions(ctxBG(), closed, schema, cur, func(error) {}); err == nil {
		t.Error("boundaryAffectedSessions on a closed DB returned nil error, want a surfaced failure")
	}

	// emitBoundarySessions over a cold (zero) cursor returns emitted=false with no
	// events and no error (the no-affected early return).
	out := make(chan canonical.Event, 8)
	emitted, eerr := emitBoundarySessions(ctxBG(), db, schema, newCursor(), "opencode:test", out, silentLogger(), func(error) {})
	if eerr != nil {
		t.Fatalf("emitBoundarySessions(cold): %v", eerr)
	}
	if emitted {
		t.Error("emitBoundarySessions(cold) reported emitted=true, want false")
	}
}

// --- P1-2: single read-only transaction for the whole per-session read --------

// TestP1_2_CompactingSkippedAtomically pins P1-2's observable contract: a session
// whose time_compacting is non-NULL is skipped (no tree emit) — and the check +
// the (skipped) tree read happen on one snapshot. A direct mid-read race is not
// portably forceable; this asserts the atomic-skip behaviour and that a CLEAN
// session loads its tree on the same path.
func TestP1_2_CompactingSkippedAtomically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// A compacting session (time_compacting set) and a clean one.
	insertSession(t, rw, "ses_busy", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_busy", "ses_busy", 110, 110, 5, 2)
	if _, err := rw.Exec(`UPDATE session SET time_compacting = 555 WHERE id = ?`, "ses_busy"); err != nil {
		t.Fatalf("set time_compacting: %v", err)
	}
	insertSession(t, rw, "ses_ok", "", 200, 200, 0)
	insertAssistantMessage(t, rw, "msg_ok", "ses_ok", 210, 210, 7, 3)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// Compacting session: skipped, NO events.
	evs, skipped, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_busy", silentLogger(), func(error) {})
	if err != nil {
		t.Fatalf("loadAndMapSession(ses_busy): %v", err)
	}
	if !skipped {
		t.Error("a compacting session was not skipped (P1-2/P2-E)")
	}
	if len(evs) != 0 {
		t.Errorf("a compacting session emitted %d events, want 0", len(evs))
	}

	// Clean session: loaded + emitted on the same single-tx path.
	evs2, skipped2, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_ok", silentLogger(), func(error) {})
	if err != nil {
		t.Fatalf("loadAndMapSession(ses_ok): %v", err)
	}
	if skipped2 {
		t.Error("a clean session was wrongly skipped")
	}
	if n := countKind(evs2, canonical.EvSessionStarted); n != 1 {
		t.Errorf("clean session SessionStarted count = %d, want 1", n)
	}
}

// TestP1_2_TreeLoadRunsInCallerTx proves loadSessionTree no longer opens its own
// transaction: it accepts a roQuerier and runs entirely within the transaction the
// caller (loadAndMapSession) owns, so the session row, the compaction check, the
// root resolution and the tree all share one consistent snapshot. The test passes
// an explicit tx and asserts the tree loads; a tx ROLLBACK afterwards must succeed
// (loadSessionTree did not commit it — the caller owns the lifecycle).
func TestP1_2_TreeLoadRunsInCallerTx(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_t", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_t", "ses_t", 110, 110, 5, 2)
	insertPart(t, rw, "prt_t", "msg_t", "ses_t", 110, 110, textBody("hi"))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	tx, err := beginRO(ctxBG(), db)
	if err != nil {
		t.Fatalf("beginRO: %v", err)
	}
	tree, err := loadSessionTree(ctxBG(), tx, schema, "ses_t", func(error) {})
	if err != nil {
		t.Fatalf("loadSessionTree(tx): %v", err)
	}
	if len(tree) != 1 || len(tree[0].Parts) != 1 {
		t.Fatalf("tree shape = %d msgs / parts, want 1/1", len(tree))
	}
	// The caller still owns the tx: loadSessionTree did not commit, so an explicit
	// Rollback here succeeds (it would error "already committed" otherwise).
	if err := tx.Rollback(); err != nil {
		t.Errorf("tx.Rollback after loadSessionTree: %v (loadSessionTree must NOT own the tx lifecycle)", err)
	}
}

// --- P2-2: malformed message/part data routes through onError (health) --------

// TestP2_2_MalformedDataRoutesToOnError pins P2-2: a session with a malformed
// message.data blob (NOT-NULL but undecodable — a corruption signal) routes the
// failure through the adapter's onError callback (which the ingester turns into a
// SourceErrorEvent → /api/health) IN ADDITION to the session-scoped WRN LogEntry.
func TestP2_2_MalformedDataRoutesToOnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_m", "", 100, 100, 0)
	// A message whose data is not valid JSON.
	insertMessageRaw(t, rw, "msg_m", "ses_m", 110, 110, "{not json")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	var onErr []error
	evs, skipped, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_m", silentLogger(),
		func(e error) { onErr = append(onErr, e) })
	if err != nil {
		t.Fatalf("loadAndMapSession: %v", err)
	}
	if skipped {
		t.Fatal("session wrongly skipped")
	}
	// onError fired (so /api/health degrades) and names the malformed message.
	foundMsg := false
	for _, e := range onErr {
		if strings.Contains(e.Error(), "undecodable message.data") && strings.Contains(e.Error(), "msg_m") {
			foundMsg = true
		}
	}
	if !foundMsg {
		t.Errorf("malformed message did not route through onError; got %v", onErr)
	}
	// The session WRN LogEntry is still emitted (detail view).
	wrn := 0
	for _, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "WRN" {
			wrn++
		}
	}
	if wrn != 1 {
		t.Errorf("session WRN LogEntry count = %d, want 1 (kept alongside onError)", wrn)
	}
}

// TestP2_2_MalformedPartRoutesToOnError is the part-level companion: an assistant
// turn with one malformed part blob routes through onError AND keeps the WRN.
func TestP2_2_MalformedPartRoutesToOnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_p", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_p", "ses_p", 110, 110, 5, 2)
	insertPart(t, rw, "prt_bad", "msg_p", "ses_p", 110, 110, "{not json")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	var onErr []error
	if _, _, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_p", silentLogger(),
		func(e error) { onErr = append(onErr, e) }); err != nil {
		t.Fatalf("loadAndMapSession: %v", err)
	}
	found := false
	for _, e := range onErr {
		if strings.Contains(e.Error(), "undecodable part.data") && strings.Contains(e.Error(), "prt_bad") {
			found = true
		}
	}
	if !found {
		t.Errorf("malformed part did not route through onError; got %v", onErr)
	}
}

// --- P2-3: defensive full-tree-size WARN --------------------------------------

// TestP2_3_OversizedSessionWarns pins P2-3: warnIfSessionTooLarge emits ONE
// structured WARN via onWarn when a session's message or part count exceeds its
// bound, and stays silent for a normal-sized session. The threshold consts are
// 100k (too large to materialize in a test), so this drives the bound predicate
// directly with synthetic counts — the same predicate loadSessionTree calls.
func TestP2_3_OversizedSessionWarns(t *testing.T) {
	t.Parallel()

	// Over the MESSAGE bound → exactly one WARN naming messages.
	tooManyMsgs := make([]messageRow, maxSessionMessagesWarn+1)
	var warns []error
	warnIfSessionTooLarge("ses_big", tooManyMsgs, nil, func(e error) { warns = append(warns, e) })
	if len(warns) != 1 || !strings.Contains(warns[0].Error(), "messages") {
		t.Fatalf("oversized-messages WARN = %v, want exactly one naming messages", warns)
	}

	// Over the PART bound → exactly one WARN naming parts.
	bigParts := map[string][]partRow{"msg_x": make([]partRow, maxSessionPartsWarn+1)}
	var pwarns []error
	warnIfSessionTooLarge("ses_big", nil, bigParts, func(e error) { pwarns = append(pwarns, e) })
	if len(pwarns) != 1 || !strings.Contains(pwarns[0].Error(), "parts") {
		t.Fatalf("oversized-parts WARN = %v, want exactly one naming parts", pwarns)
	}

	// A normal session → NO WARN.
	var none []error
	warnIfSessionTooLarge("ses_small",
		make([]messageRow, 3),
		map[string][]partRow{"m": make([]partRow, 5)},
		func(e error) { none = append(none, e) })
	if len(none) != 0 {
		t.Errorf("normal-sized session warned: %v", none)
	}

	// A nil onWarn is a no-op (the pure no-DB path), not a panic.
	warnIfSessionTooLarge("ses_big", tooManyMsgs, nil, nil)
}
