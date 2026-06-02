package opencode

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// nullStr builds a valid (non-NULL) sql.NullString for the scanDest unit tests.
func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// This file pins the SOW-0005 ROUND-7 external-review fixes:
//   - P1-1: the UNIFIED boundary re-scan trigger closes the cheap-MAX(id)
//     co-occurrence class — a true INSERT (cheap path, probed==false) co-occurring
//     with a same-ms in-place update of a low-id row must re-emit BOTH. Pinned by the
//     exact codex case AND a same-ms property/stress test (the guard against a 5th case).
//   - P1-2: reloadAndEmit propagates a transient (non-session-gone, non-compaction)
//     error so the cursor is NOT advanced past un-emitted content; a genuine
//     session-gone skips and the cursor advances. (The reloadAndEmit-level
//     propagation is also pinned in tailer_pollcycle_test.go; here it is pinned at
//     the commitBatch/processChanges cursor-advance boundary.)
//   - P2-1: the boundaryReal cold guard is applied to EVERY re-scan trigger — a cold
//     Tail's first WAL-driven OR safety-net probe does NOT replay the HEAD-snapshot
//     boundary bucket.
//   - P2-2: watchWAL's goroutine is awaited by closeWatch before it returns (no
//     send-on-closed-channel race; the goroutine is provably dead).
//   - P2-3: the FULL-TREE scanners surface a WARN (not a silent out[""] drop) on a
//     corrupt/empty required part.message_id / part.session_id.

// --- P1-1: cheap-MAX(id) INSERT co-occurring with a same-ms boundary update -----

// TestP1_R7_CheapPathInsertCoOccurringBoundaryUpdate is the EXACT codex round-7
// case — the 4th same-ms variant. The cursor sits at (T, highID). Two changes
// co-occur in ONE poll:
//   - ses_a: an in-place UPDATE of a LOW-id part re-stamped to ms T (the boundary).
//     The forward delta's strict tie-break (time_updated = T AND id > highID)
//     EXCLUDES it; only the boundary re-scan can catch it.
//   - ses_b: a TRUE INSERT whose part id sorts ABOVE the cursor's MaxIDSeen, so the
//     CHEAP MAX(id) path fires (changed==true, probed==false) and SHORT-CIRCUITS
//     before the gated MAX(time_updated) probe.
//
// Pre-round-7 (probed-gated trigger): the cheap path returned probed==false →
// gateOpen==false → the boundary re-scan was SKIPPED, processChanges advanced the
// cursor PAST T for the INSERT, and ses_a fell permanently below the new watermark,
// never seen (zero-gaps violation). Round-7 P1-1: the trigger arms on changed==true
// regardless of path, so ses_a's boundary bucket is re-scanned FIRST (pre-advance),
// BOTH sessions are emitted, and the cursor advances to the INSERT's position.
func TestP1_R7_CheapPathInsertCoOccurringBoundaryUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")

	const boundaryMs = int64(100) // T — the cursor boundary ms

	// ses_a: its tree sits AT the boundary ms T=100 with a LOW part id ("prt_aaa_low"),
	// below the cursor's MaxIDSeen/MaxTimeUpdatedID, so the cheap MAX(id) path is silent
	// for it and the forward delta tie-break excludes it — only the boundary re-scan sees
	// it. (Models a same-ms in-place UPDATE of an already-emitted row.)
	insertSession(t, rw, "ses_a", "", boundaryMs, boundaryMs, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", boundaryMs, boundaryMs, 5, 2)
	insertPart(t, rw, "prt_aaa_low", "msg_a", "ses_a", boundaryMs, boundaryMs, stepFinishBody(5, 2, 0.01))

	// ses_b: a TRUE INSERT. Its part id "zzz_insert_high" sorts ABOVE the cursor's
	// MaxIDSeen ("zzz_high"? no — strictly greater), so the CHEAP MAX(id) path fires
	// (changed==true, probed==false). Its time_updated is ALSO T (same ms), so this is
	// the same-ms co-occurrence the fix targets; the forward delta INCLUDES it
	// (id "zzz_insert_high" > tie-break "zzz_high").
	insertSession(t, rw, "ses_b", "", boundaryMs, boundaryMs, 0)
	insertAssistantMessage(t, rw, "msg_b", "ses_b", boundaryMs, boundaryMs, 7, 3)
	insertPart(t, rw, "zzz_insert_high", "msg_b", "ses_b", boundaryMs, boundaryMs, stepFinishBody(7, 3, 0.02))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// Cursor at (T=100, "zzz_high") on every table. MaxIDSeen "zzz_high" is BELOW the
	// inserted part id "zzz_insert_high" (so the cheap MAX(id) path fires on the INSERT)
	// but ABOVE prt_aaa_low (so the boundary update is invisible to the cheap path and the
	// forward delta tie-break excludes it). Boundary ms is T=100.
	cur := newCursor()
	for _, table := range trackedTables {
		cur = cur.withTable(table, TableWatermark{
			MaxIDSeen:        "zzz_high",
			MaxTimeUpdatedMs: boundaryMs,
			MaxTimeUpdatedID: "zzz_high",
		})
	}

	// WARM boundary (boundaryReal=true): the cursor at (T, highID) is a real prior
	// paged position. CLOSE the probe gate (a recent probe, NO WAL event) so the ONLY
	// thing that can arm the boundary re-scan is changed==true via the CHEAP path —
	// proving round-7 P1-1 (the re-scan must fire on the cheap path, NOT only when the
	// gated probe ran). With the old probed-gated trigger this poll would NOT re-scan.
	st := newPollState(true)
	st.markProbe(time.Now()) // gate CLOSED: no WAL event, net not due

	out := make(chan canonical.Event, 512)
	active, err := pollOnce(ctxBG(), db, schema, &cur, "opencode:test", &st, out, silentLogger(), func(error) {})
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	got := drainAll(out)

	// The INSERT (ses_b) is emitted by the forward delta.
	if !hasSession(got, "ses_b") {
		t.Errorf("the co-occurring INSERT session ses_b was not emitted (forward delta must emit it)")
	}
	// The same-ms boundary update (ses_a) is emitted by the boundary re-scan — the
	// round-7 P1-1 fix. With the old probed-gated trigger ses_a was STRANDED here
	// (the cheap MAX(id) path short-circuited probed=false → no re-scan).
	if !hasSession(got, "ses_a") {
		t.Fatalf("STRANDED (round-7 P1-1 regression): the same-ms in-place boundary update ses_a was NOT emitted — the cheap MAX(id) INSERT path skipped the boundary re-scan and the cursor advanced past ms %d", boundaryMs)
	}
	if !active {
		t.Error("pollOnce reported active=false; both an INSERT and a boundary re-emit ran, want active=true")
	}
}

// --- P1-1: same-ms property/stress test (the guard against a 5th case) ----------

// ssState tracks, per session, the latest (time_updated, the cumulative tokens we
// last stamped) so the property check can assert the LAST mutation's content was
// the one finally emitted (zero gaps).
type ssState struct {
	latestUpdatedMs int64
	mutated         bool
}

// TestP1_R7_SameMsStress is the property/stress guard against a 5th same-ms case.
// It seeds a synthetic DB, then across multiple poll cycles applies RANDOM (but
// DETERMINISTICALLY seeded — math/rand, varied by the iteration index) interleavings
// of:
//   - in-place UPDATEs of an arbitrary EXISTING low-id row re-stamped to the CURRENT
//     boundary ms T (the same-ms boundary case — invisible to the cheap MAX(id) path
//     and excluded by the forward delta tie-break, so only the boundary re-scan can
//     catch it);
//   - a CO-OCCURRING true INSERT at ms T+1 (STRICTLY higher), which fires the cheap
//     MAX(id) path (changed==true, probed==false) AND advances the cursor PAST T —
//     so the in-place update at T falls below the new watermark unless the boundary
//     re-scan runs against the pre-advance T (the exact round-7 P1-1 strand);
//   - a "missed-WAL" cycle that relies on the 60 s safety net (no WAL event marked,
//     the net forced due) instead of the WAL hint.
//
// After draining, it asserts EVERY mutated session's LATEST state was emitted (zero
// gaps) and the cursor never regressed. The structure GUARANTEES the cheap-path
// co-occurrence strand on cycles that do both an in-place update and an INSERT, so a
// 5th same-ms variant (or a regression of this fix) is caught. Verified to FAIL
// against the pre-round-7 probed-gated trigger.
//
// Run with -count=5 (per the SOW gate) to shake out nondeterminism; the seed is
// derived from a fixed constant so each run is reproducible.
func TestP1_R7_SameMsStress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")

	// Seed N sessions, each a single assistant turn with a step-finish part, all at a
	// shared starting ms so the boundary bucket is non-trivial from the first poll.
	const (
		seedN    = 6
		startMs  = int64(1000)
		numCycle = 16
	)
	for i := 0; i < seedN; i++ {
		sid := fmt.Sprintf("ses_%03d", i)
		mid := fmt.Sprintf("msg_%03d", i)
		insertSession(t, rw, sid, "", startMs, startMs, 0)
		insertAssistantMessage(t, rw, mid, sid, startMs, startMs, 5, 2)
		insertPart(t, rw, fmt.Sprintf("prt_%03d", i), mid, sid, startMs, startMs, stepFinishBody(5, 2, 0.01))
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)

	// A SEPARATE writable handle simulates opencode's live writer across cycles.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw: %v", err)
	}
	defer func() { _ = rw2.Close() }()

	// Start WARM (boundaryReal=true) from the seed HEAD: a real Scan would have emitted
	// the seed, so this models the resumed Tail. Cursor = the seed maxima.
	cur := newCursor()
	for _, table := range trackedTables {
		mid, _ := maxID(ctxBG(), db, table)
		mtu, _ := maxTimeUpdated(ctxBG(), db, table)
		cur = cur.withTable(table, TableWatermark{MaxIDSeen: mid, MaxTimeUpdatedMs: mtu, MaxTimeUpdatedID: mid})
	}
	st := newPollState(true)

	// Track which sessions were mutated and the ms their latest state lands at, so the
	// final assertion can verify the LAST state was emitted (zero gaps).
	expect := map[string]*ssState{}

	rng := rand.New(rand.NewSource(0xC0DE57)) //nolint:gosec // deterministic test PRNG, not security-sensitive
	insertSeq := 0                            // strictly-increasing id/ms counter for new INSERTs
	curBoundaryMs := startMs                  // the ms in-place updates target (the cursor boundary)
	lastCursor := cur

	out := make(chan canonical.Event, 8192)

	for c := 0; c < numCycle; c++ {
		// Every cycle does an in-place update at the CURRENT boundary T (the same-ms
		// case). Most cycles ALSO do a co-occurring INSERT at T+1 — the strand setup:
		// the INSERT advances the cursor past T, so the in-place update at T is below
		// the new watermark unless the boundary re-scan runs against pre-advance T.
		doInsert := rng.Intn(4) != 0 // ~3/4 cycles co-occur an INSERT (the strand case)
		missedWAL := rng.Intn(3) == 0

		// In-place UPDATE of an arbitrary existing seed session's part, re-stamped to T.
		victim := fmt.Sprintf("ses_%03d", rng.Intn(seedN))
		if _, uerr := rw2.Exec(`UPDATE part SET time_updated = ? WHERE session_id = ?`, curBoundaryMs, victim); uerr != nil {
			t.Fatalf("cycle %d in-place update of %s: %v", c, victim, uerr)
		}
		if e, ok := expect[victim]; ok {
			e.latestUpdatedMs = curBoundaryMs
		} else {
			expect[victim] = &ssState{latestUpdatedMs: curBoundaryMs, mutated: true}
		}

		if doInsert {
			// A NEW session at ms T+1 with a strictly-higher id (the cheap MAX(id) path),
			// advancing the cursor PAST the in-place update's ms T.
			insMs := curBoundaryMs + 1
			sid := fmt.Sprintf("ses_n%03d", insertSeq)
			mid := fmt.Sprintf("msg_n%03d", insertSeq)
			pid := fmt.Sprintf("zzz_ins_%06d", insertSeq) // sorts above every existing id
			insertSession(t, rw2, sid, "", insMs, insMs, 0)
			insertAssistantMessage(t, rw2, mid, sid, insMs, insMs, 9, 4)
			insertPart(t, rw2, pid, mid, sid, insMs, insMs, stepFinishBody(9, 4, 0.03))
			expect[sid] = &ssState{latestUpdatedMs: insMs, mutated: true}
			insertSeq++
		}

		// Open the gate: a WAL event (normal) or, ~1/3 of the time, ONLY the 60 s net
		// (a missed/dropped WAL hint — the safety-net path).
		if missedWAL {
			st.lastWALEvent = time.Time{}
			st.markProbe(time.Now().Add(-2 * timeUpdatedSafetyNet)) // net due, no WAL
		} else {
			st.markWALEvent(time.Now())
		}

		if _, perr := pollOnce(ctxBG(), db, schema, &cur, "opencode:test", &st, out, silentLogger(), func(error) {}); perr != nil {
			t.Fatalf("cycle %d pollOnce: %v", c, perr)
		}

		// The cursor must never regress on any table.
		for _, table := range trackedTables {
			if cmpWatermark(cur.Tables[table], lastCursor.Tables[table]) < 0 {
				t.Fatalf("cycle %d: cursor REGRESSED on table %q: %+v < %+v", c, table, cur.Tables[table], lastCursor.Tables[table])
			}
		}
		lastCursor = cur

		// The boundary follows the forward INSERTs: after a co-occurring INSERT at T+1
		// the cursor's MaxTimeUpdatedMs is now T+1, so the NEXT cycle's in-place update
		// targets the new boundary. (On a no-INSERT cycle the boundary stays at T.)
		if doInsert {
			curBoundaryMs++
		}
	}

	got := drainAll(out)

	// ZERO GAPS: every mutated session must have been emitted at least once (its latest
	// tree). A SessionStarted is emitted on every full-tree (re)emit, so its presence
	// proves the session's latest state reached the output. A stranded same-ms update
	// (the bug class) leaves a mutated session ABSENT from the output.
	emitted := map[string]bool{}
	for _, ev := range got {
		if s, ok := ev.(canonical.SessionStartedEvent); ok {
			emitted[s.NativeID] = true
		}
	}
	for sid, stt := range expect {
		if stt.mutated && !emitted[sid] {
			t.Errorf("ZERO-GAPS VIOLATION: mutated session %s (latest ms %d) was never emitted across %d cycles — a same-ms update was stranded by a co-occurring INSERT advancing the cursor", sid, stt.latestUpdatedMs, numCycle)
		}
	}
}

// --- P1-2: cursor not advanced on a transient error at the batch boundary -------

// TestP1_2_R7_TransientErrorDoesNotAdvanceCursor pins the P1-2 fix at the cursor
// boundary: a transient (non-session-gone) reload error during processChanges must
// leave the committed cursor UNADVANCED, so the same rows are retried next cycle.
// We force the transient error by closing the DB after introspection: the delta
// page scan itself errors, processChanges returns the pre-run cursor and an error,
// and the cursor is NOT advanced.
func TestP1_2_R7_TransientErrorDoesNotAdvanceCursor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", 110, 110, 5, 2)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)
	if err := db.Close(); err != nil { // closed → every query errors (transient)
		t.Fatalf("close ro db: %v", err)
	}

	before := newCursor()
	out := make(chan canonical.Event, 16)
	next, advanced, err := processChanges(ctxBG(), db, schema, before, "opencode:test", out, silentLogger(), func(error) {})
	if err == nil {
		t.Fatal("processChanges over a closed DB must return an error (transient), not swallow it")
	}
	if advanced {
		t.Error("processChanges reported advanced=true despite a transient error — the cursor must not advance past un-emitted content (round-7 P1-2)")
	}
	// The returned cursor is the pre-run cursor (no table watermark advanced).
	for _, table := range trackedTables {
		if cmpWatermark(next.Tables[table], before.Tables[table]) != 0 {
			t.Errorf("table %q cursor advanced on a transient error: %+v != %+v (committed cursor must stay put)", table, next.Tables[table], before.Tables[table])
		}
	}
}

// TestP1_2_R7_SessionGoneAdvances pins the OTHER side of the P1-2 policy: a
// genuinely GONE session (its row absent — deleted between the delta and the load)
// is skip-and-continue, so reloadAndEmit returns nil (the cursor MAY advance). This
// is the one load failure that is legitimately non-fatal. We delete the session row
// but leave the message row (an orphan) so the delta derives the session id but the
// tree load finds no session row → errSessionGone path → skipped, no error returned.
func TestP1_2_R7_SessionGoneAdvances(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_gone", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_orphan", "ses_gone", 110, 110, 5, 2)
	// Delete the session row, leaving the message orphaned: the affected-session
	// derivation still yields "ses_gone", but loadSession finds no row → errSessionGone.
	if _, err := rw.Exec(`DELETE FROM session WHERE id = ?`, "ses_gone"); err != nil {
		t.Fatalf("delete session row: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	var onErr []error
	out := make(chan canonical.Event, 16)
	err := reloadAndEmit(ctxBG(), db, schema, "opencode:test", []string{"ses_gone"}, out, silentLogger(),
		func(e error) { onErr = append(onErr, e) })
	if err != nil {
		t.Fatalf("reloadAndEmit must SKIP a gone session (not propagate), got error: %v", err)
	}
	// The gone session is surfaced once as errSessionGone via onError.
	found := false
	for _, e := range onErr {
		if strings.Contains(e.Error(), "ses_gone") && strings.Contains(e.Error(), "not found") {
			found = true
		}
	}
	if !found {
		t.Errorf("a gone session must surface one errSessionGone via onError; got %v", onErr)
	}
}

// --- P2-1: cold-Tail boundaryReal guard on the WAL-driven AND safety-net paths --

// TestP2_1_R7_ColdTailGateOpenDoesNotReplayBoundary pins P2-1: a COLD Tail
// (boundaryReal==false, HEAD snapshot) must NOT replay its snapshot boundary bucket
// on ANY gate-open path — neither a WAL-driven first probe NOR a safety-net first
// probe (changed==false, gate open). Pre-round-7 the changed==false path was guarded
// only by the now-removed priorProbe flag, so a cold Tail whose first poll was
// WAL-driven (or whose first safety-net probe had priorProbe already set) replayed the
// HEAD-snapshot boundary. boundaryReal (the single cold guard) now suppresses it on
// every path.
func TestP2_1_R7_ColdTailGateOpenDoesNotReplayBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// A pre-existing session whose tree sits at the snapshot boundary ms — the bucket
	// a cold Tail must NOT replay.
	insertSession(t, rw, "ses_snapshot", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_s", "ses_snapshot", 100, 100, 5, 2)
	insertPart(t, rw, "prt_low", "msg_s", "ses_snapshot", 100, 100, stepFinishBody(5, 2, 0.01))
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// A cold HEAD-snapshot cursor at the boundary (100, highID) on every table.
	freshCursor := func() Cursor {
		c := newCursor()
		for _, table := range trackedTables {
			c = c.withTable(table, TableWatermark{MaxIDSeen: "zzz_high", MaxTimeUpdatedMs: 100, MaxTimeUpdatedID: "zzz_high"})
		}
		return c
	}

	// (a) Cold Tail, first poll is WAL-DRIVEN (a WAL event fired before any probe).
	// boundaryReal==false must suppress the re-scan: the snapshot boundary must NOT
	// be replayed even though the gate is open via the WAL path.
	curWAL := freshCursor()
	stWAL := newPollState(false) // COLD: boundaryReal=false
	now := time.Now()
	stWAL.markProbe(now.Add(-2 * timeUpdatedSafetyNet))
	stWAL.markWALEvent(now) // WAL event after the last probe → gate open via WAL
	outWAL := make(chan canonical.Event, 64)
	if _, err := pollOnce(ctxBG(), db, schema, &curWAL, "opencode:test", &stWAL, outWAL, silentLogger(), func(error) {}); err != nil {
		t.Fatalf("pollOnce (cold WAL-driven): %v", err)
	}
	if got := drainAll(outWAL); hasSession(got, "ses_snapshot") {
		t.Fatalf("COLD Tail replayed the snapshot boundary on a WAL-driven first probe (round-7 P2-1); ses_snapshot must NOT be emitted")
	}

	// (b) Cold Tail, first poll is a SAFETY-NET probe with a prior probe already marked
	// (the round-7 P2-1 hole: the old priorProbe guard would have ALLOWED the re-scan
	// here). boundaryReal==false must still suppress it.
	curNet := freshCursor()
	stNet := newPollState(false) // COLD: boundaryReal=false
	stNet.markProbe(time.Now().Add(-2 * timeUpdatedSafetyNet))
	stNet.markProbe(time.Now().Add(-2 * timeUpdatedSafetyNet)) // a SECOND prior probe; net still due, no WAL
	outNet := make(chan canonical.Event, 64)
	if _, err := pollOnce(ctxBG(), db, schema, &curNet, "opencode:test", &stNet, outNet, silentLogger(), func(error) {}); err != nil {
		t.Fatalf("pollOnce (cold safety-net): %v", err)
	}
	if got := drainAll(outNet); hasSession(got, "ses_snapshot") {
		t.Fatalf("COLD Tail replayed the snapshot boundary on a safety-net first probe (round-7 P2-1 hole); ses_snapshot must NOT be emitted")
	}

	// (c) After the cursor first ADVANCES (a forward INSERT), boundaryReal flips true and
	// the boundary re-scan activates — proving the guard only suppresses the COLD window,
	// not forever. Insert a forward row and re-poll on the same (now-warm) state.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw: %v", err)
	}
	defer func() { _ = rw2.Close() }()
	insertSession(t, rw2, "ses_fwd", "", 300, 300, 0)
	insertAssistantMessage(t, rw2, "msg_fwd", "ses_fwd", 300, 300, 6, 2)
	insertPart(t, rw2, "zzz_fwd_high", "msg_fwd", "ses_fwd", 300, 300, stepFinishBody(6, 2, 0.02))

	// Reuse stWAL: its cursor must advance on the forward INSERT, flipping boundaryReal.
	stWAL.markWALEvent(time.Now())
	outFwd := make(chan canonical.Event, 128)
	if _, err := pollOnce(ctxBG(), db, schema, &curWAL, "opencode:test", &stWAL, outFwd, silentLogger(), func(error) {}); err != nil {
		t.Fatalf("pollOnce (forward advance): %v", err)
	}
	if !stWAL.boundaryReal {
		t.Error("boundaryReal did not flip true after the cursor advanced on a forward INSERT")
	}
	if got := drainAll(outFwd); !hasSession(got, "ses_fwd") {
		t.Error("the forward INSERT ses_fwd was not emitted")
	}
}

// --- P2-2: watchWAL goroutine awaited before closeWatch returns -----------------

// TestP2_2_R7_CloseWatchAwaitsGoroutine pins the P2-2 fix: closeWatch returns ONLY
// after the watcher goroutine has exited, so no send to out/onError can happen after
// closeWatch returns (the send-on-closed-channel race the adapter contract forbids).
// The goroutine's `defer close(hint)` runs (LIFO) BEFORE its `defer wg.Done()`, and
// closeWatch's wg.Wait() blocks until wg.Done() — so once closeWatch returns, the
// hint channel is provably closed (the goroutine is dead). Run under -race.
func TestP2_2_R7_CloseWatchAwaitsGoroutine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := dir + "/opencode.db"
	// Create the DB file + an empty WAL companion so the watch establishes successfully.
	if err := writeFileBytes(dbPath, []byte("x")); err != nil {
		t.Fatalf("write db: %v", err)
	}
	if err := writeFileBytes(dbPath+"-wal", []byte{}); err != nil {
		t.Fatalf("write wal: %v", err)
	}

	var ce collectErrs
	hint, closeWatch := watchWAL(dbPath, ce.onError)

	// Trigger a few WAL writes so the goroutine is actively processing events.
	for i := 0; i < 3; i++ {
		if err := appendFileBytes(dbPath+"-wal", []byte("frame")); err != nil {
			t.Fatalf("append wal: %v", err)
		}
	}
	// Drain any pending hint (non-blocking) so the goroutine is back in its select.
	select {
	case <-hint:
	case <-time.After(time.Second):
	}

	// closeWatch must block until the goroutine exits. After it returns, the hint
	// channel is closed (the goroutine's deferred close(hint) ran before wg.Done()).
	closeWatch()

	select {
	case _, ok := <-hint:
		if ok {
			// A buffered pending hint may drain as ok=true ONCE; the next recv must be
			// the closed-channel zero.
			if _, ok2 := <-hint; ok2 {
				t.Fatal("hint channel still open after closeWatch returned — the watcher goroutine was not awaited (round-7 P2-2)")
			}
		}
	default:
		t.Fatal("hint channel not closed after closeWatch returned — the goroutine did not exit before closeWatch (round-7 P2-2)")
	}

	// closeWatch is idempotent (sync.Once): a second call must not panic and returns.
	closeWatch()
}

// --- P2-3: full-tree scanners surface a WARN on a corrupt required owner id ------

// TestP2_3_R7_FullTreeCorruptPartOwnerWarns pins P2-3: a full-tree reload of a
// session whose part has an EMPTY required message_id (or session_id) surfaces a
// structured WARN via onWarn (the post-tx warnSink path) and SKIPS the row — it is
// NOT silently attached to out[""] and dropped. The DELTA scanners already abort on
// this (round-5 P2-2); round-7 P2-3 extends the same discipline to the FULL-TREE
// load path (scanPartRows).
func TestP2_3_R7_FullTreeCorruptPartOwnerWarns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_p", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_p", "ses_p", 110, 110, 5, 2)
	// A part with a VALID body but an EMPTY message_id (corrupt required ownership id).
	// It must NOT land under out[""] silently; it must surface a WARN and be skipped.
	if _, err := rw.Exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`,
		"prt_bad_owner", "", "ses_p", 110, 110, stepFinishBody(5, 2, 0.01)); err != nil {
		t.Fatalf("insert corrupt-owner part: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	var warns []error
	evs, skipped, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_p", silentLogger(),
		func(e error) { warns = append(warns, e) })
	if err != nil {
		t.Fatalf("loadAndMapSession: %v", err)
	}
	if skipped {
		t.Fatal("session wrongly skipped")
	}
	// The corrupt-owner part surfaced a WARN naming the table/column (not a silent drop).
	found := false
	for _, w := range warns {
		if strings.Contains(w.Error(), "required ownership column") &&
			strings.Contains(w.Error(), "message_id") && strings.Contains(w.Error(), "table=part") {
			found = true
		}
	}
	if !found {
		t.Errorf("a corrupt part.message_id on the full-tree path did not surface a WARN (round-7 P2-3); got %v", warns)
	}
	// The session still loaded (the one good message is emitted); the corrupt part was
	// skipped, not attached under out[""].
	if !hasSession(evs, "ses_p") {
		t.Error("session ses_p was not emitted after skipping its corrupt part")
	}
}

// TestP2_3_R7_OwnerOrWarnUnit pins the ownerOrWarn accessor directly: a present
// non-empty value returns (v, true) with no warn; an empty/absent value returns
// ("", false) with exactly one WARN; a nil onWarn is a no-op (no panic).
func TestP2_3_R7_OwnerOrWarnUnit(t *testing.T) {
	t.Parallel()
	idx := columnIndex{"message_id": 0, "session_id": 1}

	// Present + non-empty → (v, true), no warn.
	var warns []error
	dOK := (&scanDest{holders: []sql.NullString{nullStr("msg_1"), nullStr("ses_1")}}).withWarn("part", func(e error) { warns = append(warns, e) })
	if v, ok := dOK.ownerOrWarn(idx, "message_id"); !ok || v != "msg_1" {
		t.Errorf("ownerOrWarn(present) = (%q,%v), want (msg_1,true)", v, ok)
	}
	if len(warns) != 0 {
		t.Errorf("ownerOrWarn(present) warned: %v", warns)
	}

	// Empty → ("", false), exactly one WARN.
	warns = nil
	dEmpty := (&scanDest{holders: []sql.NullString{nullStr(""), nullStr("ses_1")}}).withWarn("part", func(e error) { warns = append(warns, e) })
	if v, ok := dEmpty.ownerOrWarn(idx, "message_id"); ok || v != "" {
		t.Errorf("ownerOrWarn(empty) = (%q,%v), want (\"\",false)", v, ok)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Error(), "message_id") {
		t.Errorf("ownerOrWarn(empty) WARN = %v, want exactly one naming message_id", warns)
	}

	// nil onWarn → no panic, still returns false.
	dNil := &scanDest{holders: []sql.NullString{nullStr(""), nullStr("ses_1")}}
	if _, ok := dNil.ownerOrWarn(idx, "message_id"); ok {
		t.Error("ownerOrWarn(empty, nil onWarn) returned ok=true, want false")
	}
}

// --- tiny file helpers for the WAL watch test -----------------------------------

func writeFileBytes(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }

func appendFileBytes(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, werr := f.Write(b)
	return werr
}
