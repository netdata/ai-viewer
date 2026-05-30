package opencode

import (
	"database/sql"
	"testing"
	"time"
)

// This file is the load-bearing P1-A regression proof (SOW-0005 round-2): the
// cursor's MaxIDSeen (monotonic insert-detect high-water) is kept SEPARATE from
// the (time_updated, id) paging position, so an in-place UPDATE of an OLD row —
// whose time_updated jumps above the newest row but whose id stays small — does
// NOT regress the cheap-detect watermark and does NOT permanently re-arm the
// expensive unindexed MAX(time_updated) full scan on every idle poll (which is
// exactly what the pre-P1-A single-MaxID code did, defeating AC#6's gate).

// pageSession pages the session table forward from `from` via scanTableDelta and
// returns the advanced watermark. It mirrors scanMessagesFrom (store_query_test)
// for the session table, which this regression seeds.
func pageSession(t *testing.T, db *sql.DB, schema schemaSet, from TableWatermark) TableWatermark {
	t.Helper()
	s := schema["session"]
	idx := newColumnIndex(s)
	scan, _ := scanSessionRow(idx, len(s.Present), nil)
	delta, err := scanTableDelta(ctxBG(), db, s, from, func(rows *sql.Rows) (rowKey, error) {
		return scan(rows)
	})
	if err != nil {
		t.Fatalf("scanTableDelta(session): %v", err)
	}
	return delta.watermark
}

// TestP1A_OldRowUpdateDoesNotReArmIdleScan is the decisive regression. It seeds
// monotonic sessions, pages them to establish the cursor, UPDATEs the OLDEST
// (lowest-id) row in place so its time_updated sorts LAST, re-pages that update,
// and then asserts an IDLE detectChange (gate closed):
//
//   - returns changed=false (the in-place update of an already-seen id is not a
//     new insert), and
//   - executes ZERO MAX(time_updated) queries (the cheap MAX(id) path is
//     satisfied by MaxIDSeen, so the expensive unindexed scan never runs).
//
// Pre-P1-A this failed both ways: the paging position's id regressed to the
// small updated id, so MAX(id) stayed permanently greater than the watermark,
// flipping changed=true and forcing the expensive scan on every idle poll.
//
// NOT t.Parallel(): the counting driver shares one global queryLog (see
// store_testhelpers_test.go), so counting tests run serially.
func TestP1A_OldRowUpdateDoesNotReArmIdleScan(t *testing.T) {
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// Five monotonic sessions: id and time_updated both increase together.
	for i := 1; i <= 5; i++ {
		insertSession(t, rw, fmtID("ses", i), "", int64(i*10), int64(i*10), 0)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)

	// Page from zero to establish the cursor. The monotonic fixture's max id and
	// max time_updated both belong to ses_5.
	wm := pageSession(t, db, schema, TableWatermark{})
	if wm.MaxIDSeen != fmtID("ses", 5) {
		t.Fatalf("after initial paging MaxIDSeen = %q, want %q", wm.MaxIDSeen, fmtID("ses", 5))
	}
	if wm.MaxTimeUpdatedID != fmtID("ses", 5) || wm.MaxTimeUpdatedMs != 50 {
		t.Fatalf("after initial paging paging-position = {%d,%q}, want {50,%q}", wm.MaxTimeUpdatedMs, wm.MaxTimeUpdatedID, fmtID("ses", 5))
	}

	// In-place UPDATE of the OLDEST row (ses_000...001): its time_updated jumps
	// ABOVE the newest row (50 → 999) while its id stays the lowest. This is the
	// Drizzle .$onUpdate pattern that re-stamps time_updated on an existing row.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw: %v", err)
	}
	if _, err := rw2.Exec(`UPDATE session SET time_updated = 999 WHERE id = ?`, fmtID("ses", 1)); err != nil {
		_ = rw2.Close()
		t.Fatalf("in-place update of old row: %v", err)
	}
	if err := rw2.Close(); err != nil {
		t.Fatalf("close rw2: %v", err)
	}

	// Re-page the in-place update from the established watermark. The paging
	// position advances to the updated old row (999, ses_1), but MaxIDSeen MUST
	// NOT regress — it stays at ses_5 (the true high-water id).
	wm = pageSession(t, db, schema, wm)
	if wm.MaxTimeUpdatedMs != 999 || wm.MaxTimeUpdatedID != fmtID("ses", 1) {
		t.Fatalf("after re-paging the update paging-position = {%d,%q}, want {999,%q}", wm.MaxTimeUpdatedMs, wm.MaxTimeUpdatedID, fmtID("ses", 1))
	}
	if wm.MaxIDSeen != fmtID("ses", 5) {
		t.Fatalf("MaxIDSeen REGRESSED to %q after an old-row update; want it pinned at %q (P1-A)", wm.MaxIDSeen, fmtID("ses", 5))
	}

	// Build the post-update cursor (only the session table is seeded here; the
	// other tracked tables stay at zero watermark, which is correct — they are
	// empty, so their MAX(id) is "" and the cheap check is also satisfied).
	cur := newCursor().withTable("session", wm)

	// Now drive an IDLE detectChange through the COUNTING driver and assert no
	// MAX(time_updated) is issued and no change is reported.
	cdb, log := openCounting(t, path)
	cschema, err := introspectAll(ctxBG(), cdb)
	if err != nil {
		t.Fatalf("introspectAll(counting): %v", err)
	}
	log.reset()

	now := time.Unix(1_700_000_000, 0)
	st := newPollState()
	st.markProbe(now)                       // lastProbe = now → 60 s net not yet due
	st.lastWALEvent = now.Add(-time.Second) // a WAL event BEFORE the probe → gate stays CLOSED

	changed, probed, derr := detectChange(ctxBG(), cdb, cschema, cur, &st, now.Add(activePollInterval))
	if derr != nil {
		t.Fatalf("detectChange: %v", derr)
	}
	if changed {
		t.Error("idle detectChange reported changed=true after an in-place update of an OLD, already-seen row (P1-A regression: MaxIDSeen must absorb it)")
	}
	if probed {
		t.Error("idle detectChange ran the gated probe with the gate CLOSED")
	}
	if n := log.countContaining("MAX(time_updated)"); n != 0 {
		t.Errorf("idle detectChange executed MAX(time_updated) %d times after an old-row update; want 0 (P1-A: the expensive scan must not re-arm)", n)
	}
	// Sanity: the cheap MAX(id) DID run (one per tracked table), proving the cheap
	// path is what closed the cycle.
	if n := log.countContaining("MAX(id)"); n < len(trackedTables) {
		t.Errorf("cheap MAX(id) ran %d times, want >= %d (one per tracked table)", n, len(trackedTables))
	}
}
