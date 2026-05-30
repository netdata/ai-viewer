package opencode

import (
	"testing"
	"time"
)

// This file is the AC#6 secondary proof via the query-counting driver: across
// several IDLE poll cycles (no WAL event, within the safety-net window) the
// literal MAX(time_updated) SQL is NEVER executed, while the cheap MAX(id) check
// runs every cycle. The pure-gate test (tailer_gate_test.go) is the primary
// property proof; this pins it against real executed SQL.

// TestDetectChange_NoIdleMaxTimeUpdated drives detectChange across several idle
// polls through the counting driver and asserts zero MAX(time_updated) queries
// were executed, while MAX(id) ran on every poll.
//
// NOT t.Parallel(): the counting driver shares one global queryLog (sql.Register
// is once-only), so the two counting tests run serially to keep their reset+
// measure windows exclusive.
func TestDetectChange_NoIdleMaxTimeUpdated(t *testing.T) {
	dir := t.TempDir()
	path := seedBackfillDB(t, dir, 2)

	db, log := openCounting(t, path)
	schema, err := introspectAll(ctxBG(), db)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}

	// Cursor already at the DB maxima → no change is detected (steady state). We
	// compute it from the live maxima so MAX(id) reports "no new rows".
	cur := newCursor()
	for _, table := range trackedTables {
		mid, _ := maxID(ctxBG(), db, table)
		mtu, _ := maxTimeUpdated(ctxBG(), db, table)
		cur = cur.withTable(table, TableWatermark{MaxID: mid, MaxTimeUpdatedMs: mtu})
	}

	// Reset the recorded SQL AFTER the watermark-priming queries above so we only
	// count the idle-poll queries.
	log.reset()

	// Idle poll state: a recent probe and NO WAL event since, so the gate is
	// CLOSED for the whole run (the window is far from the 60 s net).
	now := time.Unix(1_700_000_000, 0)
	st := newPollState()
	st.markProbe(now) // lastProbe = now → net not yet due
	// lastWALEvent stays zero (before lastProbe) → no WAL-driven probe.

	const idlePolls = 5
	for i := 0; i < idlePolls; i++ {
		pollNow := now.Add(time.Duration(i) * activePollInterval) // all within the net window
		changed, probed, derr := detectChange(ctxBG(), db, schema, cur, &st, pollNow)
		if derr != nil {
			t.Fatalf("detectChange poll %d: %v", i, derr)
		}
		if changed {
			t.Fatalf("poll %d reported a change on a steady-state DB", i)
		}
		if probed {
			t.Fatalf("poll %d ran the gated probe during idle (gate should be closed)", i)
		}
	}

	// The literal MAX(time_updated) must NOT appear across the idle polls.
	if n := log.countContaining("MAX(time_updated)"); n != 0 {
		t.Errorf("idle polls executed MAX(time_updated) %d times, want 0 (AC#6)", n)
	}
	// The cheap MAX(id) must have run (one per table per poll).
	if n := log.countContaining("MAX(id)"); n < idlePolls {
		t.Errorf("idle polls executed MAX(id) %d times, want >= %d (cheap path runs every poll)", n, idlePolls)
	}
}

// TestDetectChange_GateOpenRunsProbe asserts that when the gate IS open (a WAL
// event since the last probe), detectChange DOES execute MAX(time_updated).
//
// NOT t.Parallel(): see TestDetectChange_NoIdleMaxTimeUpdated (shared queryLog).
func TestDetectChange_GateOpenRunsProbe(t *testing.T) {
	dir := t.TempDir()
	path := seedBackfillDB(t, dir, 1)

	db, log := openCounting(t, path)
	schema, err := introspectAll(ctxBG(), db)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}
	cur := newCursor()
	for _, table := range trackedTables {
		mid, _ := maxID(ctxBG(), db, table)
		mtu, _ := maxTimeUpdated(ctxBG(), db, table)
		cur = cur.withTable(table, TableWatermark{MaxID: mid, MaxTimeUpdatedMs: mtu})
	}
	log.reset()

	now := time.Unix(1_700_000_000, 0)
	st := newPollState()
	st.markProbe(now)
	st.markWALEvent(now.Add(time.Second)) // WAL event AFTER the last probe → gate open

	_, probed, derr := detectChange(ctxBG(), db, schema, cur, &st, now.Add(2*time.Second))
	if derr != nil {
		t.Fatalf("detectChange: %v", derr)
	}
	if !probed {
		t.Fatal("gate open (WAL event) but probe did not run")
	}
	if n := log.countContaining("MAX(time_updated)"); n == 0 {
		t.Error("gate open but MAX(time_updated) never executed")
	}
}
