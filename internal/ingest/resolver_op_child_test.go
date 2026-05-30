package ingest

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestResolver_LinksOpChildWhenChildArrives pins P1a (SOW-0003 Reviews Round 1):
// a parent `Agent` op is written BEFORE its child sub-agent session exists, so
// ops.child_session_id starts NULL and the child native id is stashed in
// ops.extras_json.aiViewer.childNativeId. The session-only resolver passes
// (linkParents/linkRoots) never touch ops, so without linkOpChildren the parent
// op would permanently show no child. This test drives the child-after-parent-op
// ordering and asserts the resolver re-links ops.child_session_id once the child
// lands, AND emits a session_changed notify for the PARENT session so an open
// parent-detail view refetches.
func TestResolver_LinksOpChildWhenChildArrives(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	const childNative = "parent:agent:abc123def456078"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "claude-code", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "claude-code", "/tmp", NopPricer{})

	// Phase 1: parent session + turn + parent Agent op referencing a child
	// that has NOT been ingested yet. All in one tx, no emitNotify, so the
	// notify table is empty when the resolver later runs.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "parent",
		RootNativeID: "parent",
		Kind:         canonical.KindRoot,
		AgentName:    "root",
	}); err != nil {
		t.Fatalf("apply parent session: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "parent",
		Seq:             1,
	}); err != nil {
		t.Fatalf("apply turn: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:            canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID:      "parent",
		TurnSeq:              1,
		Seq:                  1,
		ParentOpSeq:          -1,
		Kind:                 canonical.OpSession,
		Name:                 "spawn worker",
		ToolNamespace:        "builtin",
		ChildSessionNativeID: childNative,
	}); err != nil {
		t.Fatalf("apply parent Agent op: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}

	parentID := canonicalSessionID(src, "parent")
	turnID := canonicalTurnID(parentID, 1)
	opID := canonicalOpID(turnID, 1)

	// Before the child lands: child_session_id is NULL and the stash carries
	// the child native id for the resolver to act on.
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != "" {
		t.Fatalf("op child_session_id linked before child landed: %q", got)
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.childNativeId'),'') FROM ops WHERE id=?`, opID); got != childNative {
		t.Fatalf("op childNativeId stash = %q, want %q", got, childNative)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM notify`); n != 0 {
		t.Fatalf("notify not empty before resolver: %d rows", n)
	}

	// Phase 2: the child sub-agent session lands.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	if err := w.apply(ctx, tx2, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1200},
		NativeID:       childNative,
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		AgentName:      "general-purpose",
	}); err != nil {
		t.Fatalf("apply child session: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}
	childID := canonicalSessionID(src, childNative)

	// Run the resolver pass.
	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	// (a) ops.child_session_id is now linked to the child row.
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != childID {
		t.Fatalf("op child_session_id after resolver = %q, want %q", got, childID)
	}

	// (b) the PARENT session got a session_changed notify so an open
	// parent-detail view refetches and renders the now-linked child op.
	if !notifyHasSession(t, db, parentID) {
		t.Fatalf("no session_changed notify for parent %q after op-child linkage", parentID)
	}
}

// TestResolver_OpChildNoOpWhenChildAbsent verifies the op-child pass does not
// fire (and emits no notify) while the referenced child session is still
// missing — the op must stay unlinked, not error or spuriously link.
func TestResolver_OpChildNoOpWhenChildAbsent(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	const childNative = "parent:agent:deadbeef0000111"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "claude-code", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "claude-code", "/tmp", NopPricer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "parent",
		RootNativeID: "parent",
		Kind:         canonical.KindRoot,
	}); err != nil {
		t.Fatalf("apply parent: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "parent",
		Seq:             1,
	}); err != nil {
		t.Fatalf("apply turn: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:            canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID:      "parent",
		TurnSeq:              1,
		Seq:                  1,
		ParentOpSeq:          -1,
		Kind:                 canonical.OpSession,
		ChildSessionNativeID: childNative,
	}); err != nil {
		t.Fatalf("apply op: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "parent"), 1), 1)
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != "" {
		t.Fatalf("op linked despite absent child: %q", got)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM notify`); n != 0 {
		t.Fatalf("resolver emitted notify for a no-op pass: %d rows", n)
	}
}

// notifyHasSession reports whether a session_changed notify row exists for the
// given session id.
func notifyHasSession(t *testing.T, db *sql.DB, sessionID string) bool {
	t.Helper()
	for _, row := range readNotify(t, db) {
		if row.kind == "session_changed" && row.sess == sessionID {
			return true
		}
	}
	return false
}
