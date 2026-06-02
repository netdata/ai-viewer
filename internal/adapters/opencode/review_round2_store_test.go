package opencode

import (
	"context"
	"errors"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the SOW-0005 ROUND-2 DB-level fixes: P2-B (one part query per
// session, not N+1), P2-C (migrationsTablePresent propagates a real fault rather
// than reporting "absent"), and P2-E (a session with non-NULL time_compacting is
// skipped this cycle and re-emits once the column clears). The mapper-layer
// round-2 fixes live in review_round2_test.go; P1-A in cursor_regression_test.go.

// --- P2-B: parts loaded in ONE query per session ------------------------------

// TestP2B_PartsLoadedInOneQuery pins P2-B: loading a multi-message session's tree
// issues exactly ONE part SELECT (WHERE session_id = ?), NOT one per message. It
// drives loadSessionTree through the counting driver over a 3-message session and
// asserts the parts are correctly grouped per message AND the part query ran once.
//
// NOT t.Parallel(): the counting driver shares one global queryLog.
func TestP2B_PartsLoadedInOneQuery(t *testing.T) {
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	// Three messages, each with two parts, so an N+1 loader would run 3 part queries.
	for m := 1; m <= 3; m++ {
		mid := fmtID("msg", m)
		insertAssistantMessage(t, rw, mid, "ses_a", int64(m*10), int64(m*10), 1, 1)
		insertPart(t, rw, fmtID("prt", m*10+1), mid, "ses_a", int64(m*10+1), int64(m*10+1), stepStartBody())
		insertPart(t, rw, fmtID("prt", m*10+2), mid, "ses_a", int64(m*10+2), int64(m*10+2), textBody("t"))
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	cdb, log := openCounting(t, path)
	cschema, err := introspectAll(ctxBG(), cdb)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}
	log.reset()

	tree, err := loadSessionTree(ctxBG(), cdb, cschema, "ses_a", func(error) {})
	if err != nil {
		t.Fatalf("loadSessionTree: %v", err)
	}
	if len(tree) != 3 {
		t.Fatalf("tree messages = %d, want 3", len(tree))
	}
	// Parts grouped correctly per message, in id order.
	for i := range tree {
		if len(tree[i].Parts) != 2 {
			t.Errorf("message %s has %d parts, want 2", tree[i].Message.ID, len(tree[i].Parts))
		}
		for _, p := range tree[i].Parts {
			if p.MessageID != tree[i].Message.ID {
				t.Errorf("part %s grouped under wrong message %s (want %s)", p.ID, tree[i].Message.ID, p.MessageID)
			}
		}
	}
	// The part SELECT (FROM "part") must have run EXACTLY ONCE, not once per message.
	if n := log.countContaining(`FROM "part"`); n != 1 {
		t.Errorf("part query ran %d times for a 3-message session, want exactly 1 (P2-B: no N+1)", n)
	}
}

// --- P2-C: migrationsTablePresent propagates a real fault ---------------------

// TestP2C_MigrationsTablePresentPropagatesError pins P2-C: a genuine query fault
// (here: a closed DB) makes migrationsTablePresent return an ERROR, not
// (false, nil) — the prior version folded every error into "absent", hiding
// corruption/ctx-cancel/closed-DB. A genuinely absent table still returns
// (false, nil).
func TestP2C_MigrationsTablePresentPropagatesError(t *testing.T) {
	t.Parallel()
	// Genuinely-absent table (a current schema synthetic DB has no __drizzle_migrations).
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db := openRO(t, path)
	present, err := migrationsTablePresent(ctxBG(), db)
	if err != nil {
		t.Fatalf("absent table: err = %v, want nil (soft-absent)", err)
	}
	if present {
		t.Fatal("absent table reported present")
	}

	// A genuine fault: close the DB, then query → error must propagate.
	db2 := openRO(t, path)
	if cerr := db2.Close(); cerr != nil {
		t.Fatalf("close db2: %v", cerr)
	}
	_, err = migrationsTablePresent(ctxBG(), db2)
	if err == nil {
		t.Fatal("query over a CLOSED DB returned nil error; want the fault propagated (P2-C, NOT treated as absent)")
	}
}

// TestP2C_ReadMigrationsPropagatesError pins that readMigrations surfaces the
// closed-DB fault (not the soft errNoMigrationsTable sentinel) so callers see the
// real failure.
func TestP2C_ReadMigrationsPropagatesError(t *testing.T) {
	t.Parallel()
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db := openRO(t, path)
	if cerr := db.Close(); cerr != nil {
		t.Fatalf("close db: %v", cerr)
	}
	_, _, err := readMigrations(ctxBG(), db)
	if err == nil {
		t.Fatal("readMigrations over a closed DB = nil error; want the fault propagated (P2-C)")
	}
	if errors.Is(err, errNoMigrationsTable) {
		t.Fatal("readMigrations over a closed DB returned the soft sentinel; want the real fault (P2-C)")
	}
}

// --- P2-E: time_compacting pauses, then re-emits when it clears ----------------

// TestP2E_CompactingSessionSkippedThenEmits pins P2-E (adapter-opencode.md Edge
// #8): a session whose time_compacting is non-NULL is SKIPPED (no events) this
// cycle; once the column clears (and its time_updated bumps), a later cycle emits
// it. It drives reloadAndEmit directly across the two states.
func TestP2E_CompactingSessionSkippedThenEmits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// A session mid-compaction: time_compacting set.
	insertSession(t, rw, "ses_c", "", 100, 100, 0)
	if _, err := rw.Exec(`UPDATE session SET time_compacting = 150 WHERE id = ?`, "ses_c"); err != nil {
		t.Fatalf("set time_compacting: %v", err)
	}
	insertAssistantMessage(t, rw, "msg_a", "ses_c", 110, 110, 5, 2)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)

	// Cycle 1: compaction in progress → NO events emitted for the session.
	out := make(chan canonical.Event, 64)
	if err := reloadAndEmit(ctxBG(), db, schema, "opencode:test", []string{"ses_c"}, out, silentLogger(), func(error) {}); err != nil {
		t.Fatalf("reloadAndEmit (compacting): %v", err)
	}
	got := drainAll(out)
	if len(got) != 0 {
		t.Fatalf("compacting session emitted %d events, want 0 (P2-E pause)", len(got))
	}

	// time_compacting clears (compaction finished); opencode bumps time_updated.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw: %v", err)
	}
	if _, err := rw2.Exec(`UPDATE session SET time_compacting = NULL, time_updated = 200 WHERE id = ?`, "ses_c"); err != nil {
		_ = rw2.Close()
		t.Fatalf("clear time_compacting: %v", err)
	}
	if err := rw2.Close(); err != nil {
		t.Fatalf("close rw2: %v", err)
	}

	// Cycle 2: compaction cleared → the session's tree now emits.
	out2 := make(chan canonical.Event, 64)
	if err := reloadAndEmit(ctxBG(), db, schema, "opencode:test", []string{"ses_c"}, out2, silentLogger(), func(error) {}); err != nil {
		t.Fatalf("reloadAndEmit (cleared): %v", err)
	}
	got2 := drainAll(out2)
	if n := countKind(got2, canonical.EvSessionStarted); n != 1 {
		t.Fatalf("cleared session emitted %d SessionStarted, want 1 (P2-E re-emit)", n)
	}
}

// TestP2E_LoadSessionReadsCompacting pins that loadSession populates
// TimeCompactingMs from the column so the skip predicate sees it.
func TestP2E_LoadSessionReadsCompacting(t *testing.T) {
	t.Parallel()
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db")
	insertSession(t, rw, "ses_c", "", 100, 100, 0)
	if _, err := rw.Exec(`UPDATE session SET time_compacting = 777 WHERE id = ?`, "ses_c"); err != nil {
		t.Fatalf("set time_compacting: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)
	s, ok, err := loadSession(context.Background(), db, schema, "ses_c", nil)
	if err != nil || !ok {
		t.Fatalf("loadSession: ok=%v err=%v", ok, err)
	}
	if s.TimeCompactingMs != 777 {
		t.Errorf("TimeCompactingMs = %d, want 777", s.TimeCompactingMs)
	}
}

// --- P1-C golden invariant (keyed on canonical FIELDS, not golden text) -------

// TestGoldenInvariant_HFailedTool re-scans the h_failed_tool fixture through the
// public adapter and asserts the P1-C invariant on canonical-event FIELDS, so a
// future -update-golden cannot silently launder the "error"→"failed" regression
// past review: the bash tool's OpFinalized carries the canonical status "failed"
// (never "error"), the opencode detail rides in ErrorClass+ErrorMessage, and the
// TURN stays "completed" (a failed tool op does not fail the turn).
func TestGoldenInvariant_HFailedTool(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "h_failed_tool")

	var toolFin *canonical.OpFinalizedEvent
	for i := range ev {
		f, ok := ev[i].(canonical.OpFinalizedEvent)
		if !ok {
			continue
		}
		if f.Status == "error" {
			t.Fatalf("op_finalized carries non-canonical status %q (P1-C: must be 'failed')", f.Status)
		}
		// The tool op is Seq 2 (Seq 1 is the LLM op); the failed one is the tool.
		if f.Seq == 2 {
			fc := f
			toolFin = &fc
		}
	}
	if toolFin == nil {
		t.Fatal("no tool op_finalized (seq 2) in h_failed_tool")
	}
	if toolFin.Status != "failed" {
		t.Errorf("tool op status = %q, want failed (P1-C)", toolFin.Status)
	}
	if toolFin.ErrorClass != defaultErrorClass {
		t.Errorf("tool op ErrorClass = %q, want %q (P1-C class label)", toolFin.ErrorClass, defaultErrorClass)
	}
	if toolFin.ErrorMessage == "" {
		t.Error("tool op ErrorMessage empty, want the opencode state.error detail (P1-C)")
	}
	// The turn itself is completed (the tool failure is op-scoped, not turn-scoped).
	tf := turnFinalForSeq(t, ev, 1)
	if tf.Status != "completed" {
		t.Errorf("turn status = %q, want completed (a failed tool op does not fail the turn)", tf.Status)
	}
}

// --- P3-C: SourceProgress emitted from ONE layer only -------------------------

// TestP3C_SingleBatchEmitsOneSourceProgress pins P3-C: a productive single-batch
// pollOnce emits EXACTLY ONE SourceProgress checkpoint — the batch processor's,
// after its sessions are emitted — not two (the trailing post-processChanges emit
// that used to double it was removed). A single small session fits in one batch,
// so exactly one checkpoint fires.
func TestP3C_SingleBatchEmitsOneSourceProgress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", 2, 2, 5, 2)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)
	out := make(chan canonical.Event, 256)
	cur := newCursor()
	st := newPollState(false)
	advanced, err := pollOnce(ctxBG(), db, schema, &cur, "opencode:test", &st, out, silentLogger(), func(error) {})
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if !advanced {
		t.Fatal("pollOnce over new rows reported advanced=false")
	}
	got := drainAll(out)
	if n := countKind(got, canonical.EvSourceProgress); n != 1 {
		t.Errorf("single-batch pollOnce emitted %d SourceProgress, want EXACTLY 1 (P3-C: one checkpoint layer)", n)
	}
}

// NOTE (SOW-0005 round-3 P3-1): the former TestP2B_OldSchemaPartFallbackOneQuery
// was removed along with the loadPartsByMessageIDs / selectPartsByMessageIDs
// fallback it exercised. session_id is a REQUIRED part column (requiredColumns),
// so introspectAll makes a part table lacking it FATAL upstream — the fallback was
// unreachable in production, and the test had to bypass introspection to reach it.
// The current P2-B test above (TestP2B_PartsLoadedInOneQuery) covers the live
// single-query path on the real (session_id-present) schema.
