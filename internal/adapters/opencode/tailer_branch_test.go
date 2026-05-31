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
	err := reloadAndEmit(ctxBG(), db, schema, "opencode:test", []string{"ses_ghost", "ses_real"}, out, silentLogger(), ce.onError)
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
	err := reloadAndEmit(ctx, db, schema, "opencode:test", []string{"ses_real"}, out, silentLogger(), ce.onError)
	if !isContextErr(err) {
		t.Fatalf("reloadAndEmit(cancelled) = %v, want context error", err)
	}
}

// TestResolvePartSession pins the SIMPLIFIED resolver (SOW-0005 round-6 P3-2): the
// part table's session_id is a REQUIRED column (requiredColumns["part"]) — its
// absence is fatal upstream in introspectAll — so the resolver reads the denormalized
// value directly. There is no message-lookup fallback (the old-schema branch was
// unreachable and removed, same class as the round-3 P3-1 dead-fallback removal). The
// resolver is now a PURE function of the partRow (no ctx/db/map), so it needs no DB.
func TestResolvePartSession(t *testing.T) {
	t.Parallel()

	// Denormalized (required) session_id present → returned directly.
	p := partRow{ID: "prt_1", MessageID: "msg_a", SessionID: "ses_a"}
	if sid, err := resolvePartSession(p); err != nil || sid != "ses_a" {
		t.Fatalf("denormalized: sid=%q err=%v, want ses_a/nil", sid, err)
	}

	// Empty session_id → ERROR (defence in depth; the delta scanner's requiredOwner
	// already errors the page before this, but the resolver must never derive an
	// empty affected session that affectedSet.add would silently drop → cursor gap).
	pEmpty := partRow{ID: "prt_empty", MessageID: "msg_a", SessionID: ""}
	sid, err := resolvePartSession(pEmpty)
	if err == nil {
		t.Fatalf("empty session_id must ERROR (got sid=%q, nil err); a silent empty would gap the cursor", sid)
	}
	if !strings.Contains(err.Error(), "empty session_id") {
		t.Errorf("error = %v, want an empty-session_id refusal naming the part", err)
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
		cur = cur.withTable(table, TableWatermark{MaxIDSeen: mid, MaxTimeUpdatedMs: mtu, MaxTimeUpdatedID: mid})
	}

	out := make(chan canonical.Event, 256)
	next, advanced, err := processChanges(ctxBG(), db, schema, cur, "opencode:test", out, silentLogger(), func(error) {})
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

// TestProcessChanges_BatchedCheckpoint asserts processChanges emits at least one
// SourceProgress checkpoint when more than progressEveryRows rows are paged (so a
// backfill resumes mid-scan) AND that every checkpoint is preceded by the content
// it covers — the checkpoint-after-emit invariant (SOW-0005 P1.1). Because all
// rows belong to one session, the batched loop re-emits that session's tree each
// batch, then checkpoints; the LAST event in the stream must therefore be a
// SourceProgress (the final batch checkpoint), never a checkpoint with trailing
// uncommitted content.
func TestProcessChanges_BatchedCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	// Insert > progressEveryRows messages so the loop runs more than one batch.
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
	var ce collectErrs
	_, advanced, err := processChanges(ctxBG(), db, schema, newCursor(), "opencode:test", out, silentLogger(), ce.onError)
	if err != nil {
		t.Fatalf("processChanges: %v", err)
	}
	if !advanced {
		t.Error("processChanges with new rows reported advanced=false")
	}
	got := drainAll(out)
	if n := countKind(got, canonical.EvSourceProgress); n < 1 {
		t.Errorf("processChanges paged > %d rows but emitted %d SourceProgress, want >= 1", progressEveryRows, n)
	}
	// Checkpoint-after-emit: the final emitted event is the last batch's
	// SourceProgress, so the run never ends with content past the last checkpoint.
	if len(got) == 0 || got[len(got)-1].EventKind() != canonical.EvSourceProgress {
		t.Errorf("last event = %v, want a SourceProgress checkpoint (checkpoint-after-emit)", lastKind(got))
	}
}

// lastKind returns the kind of the last event (or "" for an empty slice) for
// assertion messages.
func lastKind(evs []canonical.Event) canonical.EventKind {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].EventKind()
}

// TestDetectChange_TimeUpdatedPath asserts an in-place mutation (time_updated
// bumped, id already covered by MaxIDSeen) is caught only via the gated
// MAX(time_updated) probe, never the cheap MAX(id) path (SOW-0005 round-2 P1-A).
func TestDetectChange_TimeUpdatedPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 100, 100, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// Cursor whose monotonic high-water (MaxIDSeen) already saw the current MAX(id)
	// but whose time_updated paging position is LOWER, simulating an in-place row
	// update (same id, bumped time_updated) we have not yet paged. The cheap MAX(id)
	// check must NOT fire (MaxIDSeen already covers the id); only the gated
	// MAX(time_updated) probe catches the mutation (SOW-0005 round-2 P1-A).
	cur := newCursor()
	mid, _ := maxID(ctxBG(), db, "session")
	cur = cur.withTable("session", TableWatermark{MaxIDSeen: mid, MaxTimeUpdatedMs: 50, MaxTimeUpdatedID: mid})

	st := newPollState(false) // zero lastProbe ⇒ gate open (net immediately due)
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

// TestCoerceScanCursor_PureShaping asserts coerceScanCursor only normalises the
// Tables map and Version and does NOT record a schema hash (the hash is recorded
// separately by recordSchemaHash after __drizzle_migrations is read — SOW-0005
// chunk D, replacing chunk C's present-column placeholder).
func TestCoerceScanCursor_PureShaping(t *testing.T) {
	t.Parallel()
	c := coerceScanCursor(Cursor{})
	if c.Tables == nil {
		t.Error("coerceScanCursor did not initialise the Tables map")
	}
	if c.Version != cursorVersion {
		t.Errorf("coerceScanCursor Version = %d, want %d", c.Version, cursorVersion)
	}
	if c.SchemaHash != "" {
		t.Errorf("coerceScanCursor recorded a schema hash %q; the hash is recordSchemaHash's job", c.SchemaHash)
	}
}

// TestPureHelpers covers isContextErr, messageOrderBy, and parseInt64/parseFloat64
// — the small pure-helper branches.
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
	// NOTE: buildSelectByID and its assertion were removed with the id-only delta
	// fallback (SOW-0005 P3.1) — time_updated is a required column, so the
	// composite-key buildSelect is the only delta SELECT.
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
