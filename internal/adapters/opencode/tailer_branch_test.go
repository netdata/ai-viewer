package opencode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file covers the error/edge branches of the chunk-C delta + tailer layer
// that the happy-path tests don't reach: errSessionGone skip, the part→session
// message-id fallback + indexed lookup, ctx-cancel in emit/reload, the
// >progressEveryRows checkpoint, the time_updated change path, and the small
// pure helpers (isContextErr, coerceScanCursor schema-hash, messageOrderBy).

// TestReloadAndEmit_SessionGoneSkipped feeds an affected id with no session row
// and asserts reloadAndEmit skips it with one errSessionGone error and keeps
// going for the others.
func TestReloadAndEmit_SessionGoneSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_real", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	out := make(chan canonical.Event, 256)
	var ce collectErrs
	// "ses_ghost" has no session row → errSessionGone; "ses_real" emits normally.
	err := reloadAndEmit(ctxBG(), db, schema, "opencode:test", []string{"ses_ghost", "ses_real"}, out, ce.onError)
	if err != nil {
		t.Fatalf("reloadAndEmit: %v", err)
	}
	got := drainAll(out)
	if n := countKind(got, canonical.EvSessionStarted); n != 1 {
		t.Errorf("SessionStarted count = %d, want 1 (only ses_real)", n)
	}
	if ce.count() != 1 {
		t.Fatalf("onError count = %d, want 1 (errSessionGone for ses_ghost)", ce.count())
	}
	if !errors.Is(ce.errs[0], errSessionGone) {
		t.Errorf("error = %v, want errSessionGone", ce.errs[0])
	}
}

// TestReloadAndEmit_CtxCancel asserts reloadAndEmit returns ctx.Err() promptly
// when the context is already cancelled, without emitting.
func TestReloadAndEmit_CtxCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_real", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event) // unbuffered; a non-ctx-aware emit would hang
	var ce collectErrs
	err := reloadAndEmit(ctx, db, schema, "opencode:test", []string{"ses_real"}, out, ce.onError)
	if !isContextErr(err) {
		t.Fatalf("reloadAndEmit(cancelled) = %v, want context error", err)
	}
}

// TestResolvePartSession_Fallbacks covers all three resolvePartSession branches:
// denormalized session_id present, message-map fallback, and indexed lookup.
func TestResolvePartSession_Fallbacks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", 2, 2, 1, 1)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db := openRO(t, path)

	// 1) Denormalized session_id present → returned directly (no query).
	p := partRow{ID: "prt_1", MessageID: "msg_a", SessionID: "ses_a"}
	if sid, err := resolvePartSession(ctxBG(), db, true, p, map[string]string{}); err != nil || sid != "ses_a" {
		t.Fatalf("denormalized: sid=%q err=%v, want ses_a/nil", sid, err)
	}

	// 2) No session_id (hasSessionID=false) but message map has it → no query.
	p2 := partRow{ID: "prt_2", MessageID: "msg_a"}
	if sid, err := resolvePartSession(ctxBG(), db, false, p2, map[string]string{"msg_a": "ses_map"}); err != nil || sid != "ses_map" {
		t.Fatalf("map fallback: sid=%q err=%v, want ses_map/nil", sid, err)
	}

	// 3) No session_id, not in map → indexed lookup on message PK.
	p3 := partRow{ID: "prt_3", MessageID: "msg_a"}
	if sid, err := resolvePartSession(ctxBG(), db, false, p3, map[string]string{}); err != nil || sid != "ses_a" {
		t.Fatalf("indexed lookup: sid=%q err=%v, want ses_a/nil", sid, err)
	}

	// 4) Empty message id → empty (no query, no error).
	p4 := partRow{ID: "prt_4"}
	if sid, err := resolvePartSession(ctxBG(), db, false, p4, map[string]string{}); err != nil || sid != "" {
		t.Fatalf("empty message id: sid=%q err=%v, want \"\"/nil", sid, err)
	}

	// 5) Unknown message id → ErrNoRows handled as empty.
	p5 := partRow{ID: "prt_5", MessageID: "msg_nope"}
	if sid, err := resolvePartSession(ctxBG(), db, false, p5, map[string]string{}); err != nil || sid != "" {
		t.Fatalf("unknown message id: sid=%q err=%v, want \"\"/nil", sid, err)
	}
}

// TestProcessChanges_NoChange asserts processChanges over a cursor already at the
// DB maxima returns advanced=false and emits nothing.
func TestProcessChanges_NoChange(t *testing.T) {
	t.Parallel()
	path := seedBackfillDB(t, t.TempDir(), 2)
	db, schema := introspect(t, path)

	// Prime the cursor to the current maxima.
	cur := newCursor()
	for _, table := range trackedTables {
		mid, _ := maxID(ctxBG(), db, table)
		mtu, _ := maxTimeUpdated(ctxBG(), db, table)
		cur = cur.withTable(table, TableWatermark{MaxID: mid, MaxTimeUpdatedMs: mtu})
	}

	out := make(chan canonical.Event, 256)
	next, advanced, err := processChanges(ctxBG(), db, schema, cur, "opencode:test", out, func(error) {})
	if err != nil {
		t.Fatalf("processChanges: %v", err)
	}
	if advanced {
		t.Error("processChanges over up-to-date cursor reported advanced=true")
	}
	if got := drainAll(out); len(got) != 0 {
		t.Errorf("no-change processChanges emitted %d events, want 0", len(got))
	}
	_ = next
}

// TestCollectDeltas_MidScanCheckpoint asserts collectDeltas emits at least one
// SourceProgress checkpoint when more than progressEveryRows rows are paged (so
// a backfill resumes mid-scan).
func TestCollectDeltas_MidScanCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	// Insert > progressEveryRows messages so the checkpoint fires mid-paging.
	tx, _ := rw.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`)
	for i := 1; i <= progressEveryRows+50; i++ {
		if _, err := stmt.Exec(fmtID("msg", i), "ses_a", int64(i), int64(i), `{"role":"user"}`); err != nil {
			t.Fatalf("bulk insert: %v", err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)
	out := make(chan canonical.Event, 8192)
	_, _, advanced, err := collectDeltas(ctxBG(), db, schema, newCursor(), "opencode:test", out)
	if err != nil {
		t.Fatalf("collectDeltas: %v", err)
	}
	if !advanced {
		t.Error("collectDeltas with new rows reported advanced=false")
	}
	if n := countKind(drainAll(out), canonical.EvSourceProgress); n < 1 {
		t.Errorf("collectDeltas paged > %d rows but emitted %d SourceProgress, want >= 1", progressEveryRows, n)
	}
}

// TestDetectChange_TimeUpdatedPath asserts an in-place mutation (time_updated
// bumped, MaxID unchanged) is caught only via the gated MAX(time_updated) probe.
func TestDetectChange_TimeUpdatedPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 100, 100, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// Cursor at the current MaxID but a LOWER time_updated watermark, simulating
	// an in-place row update (same id, bumped time_updated) we have not seen.
	cur := newCursor()
	mid, _ := maxID(ctxBG(), db, "session")
	cur = cur.withTable("session", TableWatermark{MaxID: mid, MaxTimeUpdatedMs: 50})

	st := newPollState() // zero lastProbe ⇒ gate open (net immediately due)
	changed, probed, err := detectChange(ctxBG(), db, schema, cur, &st, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("detectChange: %v", err)
	}
	if !probed {
		t.Error("expected the gated probe to run (gate open)")
	}
	if !changed {
		t.Error("in-place mutation (time_updated > watermark) not detected via MAX(time_updated)")
	}
}

// TestCoerceScanCursor_RecordsSchemaHash asserts coerceScanCursor records a
// non-empty schema fingerprint and that different schemas yield different hashes.
func TestCoerceScanCursor_RecordsSchemaHash(t *testing.T) {
	t.Parallel()
	pathCur := seedBackfillDB(t, t.TempDir(), 1)
	_, cur := introspect(t, pathCur)

	c := coerceScanCursor(newCursor(), cur)
	if c.SchemaHash == "" {
		t.Fatal("coerceScanCursor did not record a schema hash")
	}

	pathOld := seedOldSchemaDB(t, t.TempDir())
	dbOld := openRO(t, pathOld)
	oldSet, err := introspectAll(ctxBG(), dbOld)
	if err != nil {
		t.Fatalf("introspect old: %v", err)
	}
	cOld := coerceScanCursor(newCursor(), oldSet)
	if cOld.SchemaHash == c.SchemaHash {
		t.Error("current and old schemas produced the same fingerprint (shape differs)")
	}
	// Nil schema leaves the hash untouched (no panic).
	if got := coerceScanCursor(newCursor(), nil); got.SchemaHash != "" {
		t.Errorf("nil-schema coerce set a hash: %q", got.SchemaHash)
	}
}

// TestPureHelpers covers isContextErr, messageOrderBy, parseInt64/parseFloat64,
// and buildSelectByID-without-time_created — the small branches.
func TestPureHelpers(t *testing.T) {
	t.Parallel()
	if !isContextErr(context.Canceled) || !isContextErr(context.DeadlineExceeded) {
		t.Error("isContextErr should be true for canceled/deadline")
	}
	if isContextErr(errors.New("other")) || isContextErr(nil) {
		t.Error("isContextErr should be false for non-context / nil")
	}

	// messageOrderBy: with time_created → composite; without → id only.
	withTC := tableSchema{Table: "message", Present: []string{"id", "time_created"}, live: map[string]struct{}{"time_created": {}}}
	if ob := messageOrderBy(withTC); !strings.Contains(ob, "time_created") {
		t.Errorf("messageOrderBy(with time_created) = %q, want composite", ob)
	}
	noTC := tableSchema{Table: "message", Present: []string{"id"}, live: map[string]struct{}{}}
	if ob := messageOrderBy(noTC); strings.Contains(ob, "time_created") {
		t.Errorf("messageOrderBy(no time_created) = %q, want id only", ob)
	}

	if parseInt64("123") != 123 || parseInt64("nope") != 0 {
		t.Error("parseInt64 wrong")
	}
	if parseFloat64("1.5") != 1.5 || parseFloat64("nope") != 0 {
		t.Error("parseFloat64 wrong")
	}

	// buildSelectByID for a schema without time_created still orders by id.
	sel := noTC.buildSelectByID()
	if !strings.Contains(sel, "WHERE id > ? ORDER BY id LIMIT 1000") {
		t.Errorf("buildSelectByID = %q, want id-only paging", sel)
	}
}

// TestEmitHelpers_CtxCancel covers the ctx-cancel branches of emitProgress and
// emitEvents.
func TestEmitHelpers_CtxCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event) // unbuffered
	if err := emitProgress(ctx, "s", newCursor(), out); !isContextErr(err) {
		t.Errorf("emitProgress(cancelled) = %v, want context error", err)
	}
	ev := []canonical.Event{canonical.SourceProgressEvent{}}
	if err := emitEvents(ctx, ev, out); !isContextErr(err) {
		t.Errorf("emitEvents(cancelled) = %v, want context error", err)
	}
	// emitEvents over an empty slice is a no-op nil.
	if err := emitEvents(context.Background(), nil, out); err != nil {
		t.Errorf("emitEvents(nil) = %v, want nil", err)
	}
}

// TestOrNoop covers the nil-onError substitution.
func TestOrNoop(t *testing.T) {
	t.Parallel()
	if orNoop(nil) == nil {
		t.Fatal("orNoop(nil) returned nil")
	}
	orNoop(nil)(errors.New("must not panic"))
	called := false
	orNoop(func(error) { called = true })(errors.New("x"))
	if !called {
		t.Error("orNoop did not pass through the provided func")
	}
}
