package ingest

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// seedOrphanChildThenParent writes a child session whose parent has not yet
// been ingested (so parent_session_id stays NULL and root_session_id falls
// back to the child's own id), then writes the parent — exactly the
// child-first ingestion order the background resolver exists to repair. Both
// rows are written through the real writer in one tx WITHOUT calling
// emitNotify, so the notify table is empty when the resolver pass runs and
// every notify row asserted afterwards is attributable to the resolver.
func seedOrphanChildThenParent(t *testing.T, src string) (db *sql.DB, childID, parentID string) {
	t.Helper()
	_, db = openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// Child first: parent absent, so parent_session_id=NULL and
	// root_session_id=self; parentNativeId/rootNativeId stashed in extras.
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:       "child",
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		AgentName:      "sub",
	}); err != nil {
		t.Fatalf("apply child: %v", err)
	}
	// Parent lands later in the same tx.
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 2000},
		NativeID:     "parent",
		RootNativeID: "parent",
		Kind:         canonical.KindRoot,
		AgentName:    "root",
	}); err != nil {
		t.Fatalf("apply parent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	childID = canonicalSessionID(src, "child")
	parentID = canonicalSessionID(src, "parent")
	// Sanity: child must be an orphan before the resolver runs, and the
	// notify table must be empty (no emitNotify above).
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != "" {
		t.Fatalf("child already linked before resolver: %q", got)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != childID {
		t.Fatalf("child root before resolver = %q, want self %q", got, childID)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM notify`); n != 0 {
		t.Fatalf("notify not empty before resolver: %d rows", n)
	}
	return db, childID, parentID
}

// TestResolver_EmitsNotifyOnLinkage is the live-update correctness test for
// the resolver: after a child-first ingestion is repaired by one linkOrphans
// pass, an already-open UI must be told to refetch. It asserts (a) the
// child's parent_session_id/root_session_id are now linked, and (b) notify
// session_changed rows exist for the affected child + its newly-linked parent
// + its root, plus exactly one stats_invalidated (child-count/topology
// aggregates changed). Without these rows the SSE contract
// (sse-protocol.md §session_changed) is silently broken.
func TestResolver_EmitsNotifyOnLinkage(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	db, childID, parentID := seedOrphanChildThenParent(t, src)
	ctx := context.Background()

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	// (a) linkage happened.
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != parentID {
		t.Fatalf("child parent_session_id = %q, want %q", got, parentID)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != parentID {
		t.Fatalf("child root_session_id = %q, want %q (re-resolved to parent)", got, parentID)
	}

	// (b) notify rows tell an open poller to refetch.
	rows := readNotify(t, db)
	sessChanged := map[string]bool{}
	stats := 0
	for _, row := range rows {
		switch row.kind {
		case "session_changed":
			sessChanged[row.sess] = true
		case "stats_invalidated":
			stats++
		default:
			t.Errorf("unexpected notify kind %q from resolver", row.kind)
		}
	}
	// The child changed (both columns) and its newly-linked parent/root must
	// also be signalled so an open parent-detail view refetches its children.
	// Here root == parent, so the distinct affected set is {child, parent}.
	for _, want := range []string{childID, parentID} {
		if !sessChanged[want] {
			t.Errorf("missing session_changed for %q; got rows %+v", want, rows)
		}
	}
	if stats != 1 {
		t.Errorf("stats_invalidated rows = %d, want exactly 1 (linkage changed aggregates)", stats)
	}
	// Every session_changed row must carry the affected session's current
	// root (parentID for both child and parent in this topology).
	for _, row := range rows {
		if row.kind == "session_changed" && row.root != parentID {
			t.Errorf("session_changed %q root_session_id = %q, want %q", row.sess, row.root, parentID)
		}
	}
}

// seedSeparateRootOrphanChild builds a three-level tree where the ROOT is a
// distinct session from the child's PARENT, ingested in the order that
// exercises the parent-link-only path: root R, then child C, then parent P.
//
// C arrives while P is still absent but R is already present, so the writer
// records C with root_session_id=R (root resolvable) and parent_session_id=NULL
// (parent not yet ingested) — exactly the state the root re-resolution UPDATE
// will NOT touch (C's root is already R, not self). P is then ingested fully
// linked under R. A single resolver pass must link C to P AND emit
// session_changed for C, P, and R: a detail-page subscriber filters by EXACT
// session_id, so an open R-detail view only refetches if R itself is signalled
// (sse-protocol.md §session_changed; ingester.md §resolver pass).
func seedSeparateRootOrphanChild(t *testing.T, src string) (db *sql.DB, rootID, childID, parentID string) {
	t.Helper()
	_, db = openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// Root R first: self-rooted top of the tree.
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "root",
		RootNativeID: "root",
		Kind:         canonical.KindRoot,
		AgentName:    "root",
	}); err != nil {
		t.Fatalf("apply root: %v", err)
	}
	// Child C next: root R present (root_session_id=R) but parent P absent
	// (parent_session_id=NULL). parentNativeId/rootNativeId stashed in extras.
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 2000},
		NativeID:       "child",
		RootNativeID:   "root",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		AgentName:      "sub",
	}); err != nil {
		t.Fatalf("apply child: %v", err)
	}
	// Parent P last: fully linked under R (parent and root both R).
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 3000},
		NativeID:       "parent",
		RootNativeID:   "root",
		ParentNativeID: "root",
		Kind:           canonical.KindSubAgent,
		AgentName:      "mid",
	}); err != nil {
		t.Fatalf("apply parent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	rootID = canonicalSessionID(src, "root")
	childID = canonicalSessionID(src, "child")
	parentID = canonicalSessionID(src, "parent")
	// Sanity: C is an orphan whose root is already R (NOT self), so only the
	// parent-link path can fire for it; the notify table is empty.
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != "" {
		t.Fatalf("child already linked before resolver: %q", got)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != rootID {
		t.Fatalf("child root before resolver = %q, want root %q", got, rootID)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM notify`); n != 0 {
		t.Fatalf("notify not empty before resolver: %d rows", n)
	}
	return db, rootID, childID, parentID
}

// TestResolver_EmitsNotifyForRootOnParentLink pins the separate-root case: when
// a parent-only link fires (child already correctly rooted, parent lands
// later), the resolver must still emit session_changed for the ROOT, not just
// the child and its parent. Without the root row an open root-detail view —
// which filters by EXACT session_id — never learns its subtree changed. This is
// the regression the P2 reviewer finding caught (the 3-level tree was
// previously untested).
func TestResolver_EmitsNotifyForRootOnParentLink(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	db, rootID, childID, parentID := seedSeparateRootOrphanChild(t, src)
	ctx := context.Background()

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	// (a) the parent-only link happened; the child's root stays R (the root
	// re-resolution UPDATE must NOT have re-pointed it — it was already R).
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != parentID {
		t.Fatalf("child parent_session_id = %q, want %q", got, parentID)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != rootID {
		t.Fatalf("child root_session_id = %q, want root %q (unchanged by parent link)", got, rootID)
	}

	// (b) session_changed must cover child, parent, AND root; plus exactly one
	// stats_invalidated.
	rows := readNotify(t, db)
	sessChanged := map[string]bool{}
	stats := 0
	for _, row := range rows {
		switch row.kind {
		case "session_changed":
			sessChanged[row.sess] = true
		case "stats_invalidated":
			stats++
		default:
			t.Errorf("unexpected notify kind %q from resolver", row.kind)
		}
	}
	for _, want := range []string{childID, parentID, rootID} {
		if !sessChanged[want] {
			t.Errorf("missing session_changed for %q; got rows %+v", want, rows)
		}
	}
	if stats != 1 {
		t.Errorf("stats_invalidated rows = %d, want exactly 1 (linkage changed aggregates)", stats)
	}
	// Each session_changed carries that session's own current root: child and
	// parent are rooted at R, and R is its own root.
	wantRoot := map[string]string{childID: rootID, parentID: rootID, rootID: rootID}
	for _, row := range rows {
		if row.kind != "session_changed" {
			continue
		}
		if want := wantRoot[row.sess]; row.root != want {
			t.Errorf("session_changed %q root_session_id = %q, want %q", row.sess, row.root, want)
		}
	}
}

func TestResolver_PropagatesNestedDirectParentRoot(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "codex", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "codex", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for _, ev := range []canonical.SessionStartedEvent{
		{
			EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:     "root",
			RootNativeID: "root",
			Kind:         canonical.KindRoot,
			AgentName:    "root",
		},
		{
			EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 2000},
			NativeID:       "parent",
			RootNativeID:   "root",
			ParentNativeID: "root",
			Kind:           canonical.KindSubAgent,
			AgentName:      "parent",
		},
		{
			EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 3000},
			NativeID:       "child",
			RootNativeID:   "parent",
			ParentNativeID: "parent",
			Kind:           canonical.KindSubAgent,
			AgentName:      "child",
		},
	} {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %s: %v", ev.NativeID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	rootID := canonicalSessionID(src, "root")
	parentID := canonicalSessionID(src, "parent")
	childID := canonicalSessionID(src, "child")
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != parentID {
		t.Fatalf("child root before resolver = %q, want provisional direct parent %q", got, parentID)
	}

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != rootID {
		t.Fatalf("child root_session_id = %q, want transitive root %q", got, rootID)
	}

	rows := readNotify(t, db)
	sessChanged := map[string]bool{}
	for _, row := range rows {
		if row.kind == "session_changed" {
			sessChanged[row.sess] = true
			if row.root != rootID {
				t.Errorf("session_changed %q root_session_id = %q, want %q", row.sess, row.root, rootID)
			}
		}
	}
	for _, want := range []string{childID, parentID, rootID} {
		if !sessChanged[want] {
			t.Errorf("missing session_changed for %q; got rows %+v", want, rows)
		}
	}
}

func TestResolver_MutualParentCycleDoesNotHangTransitiveRootRepair(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "codex", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "codex", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for _, ev := range []canonical.SessionStartedEvent{
		{
			EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:       "a",
			RootNativeID:   "b",
			ParentNativeID: "b",
			Kind:           canonical.KindSubAgent,
			AgentName:      "a",
		},
		{
			EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 2000},
			NativeID:       "b",
			RootNativeID:   "a",
			ParentNativeID: "a",
			Kind:           canonical.KindSubAgent,
			AgentName:      "b",
		},
	} {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %s: %v", ev.NativeID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- newResolver(db, silentLogger(), time.Minute).linkOrphans(context.Background())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("linkOrphans: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("linkOrphans hung on mutual parent cycle")
	}

	aID := canonicalSessionID(src, "a")
	bID := canonicalSessionID(src, "b")
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, aID); got != bID {
		t.Fatalf("a parent_session_id = %q, want %q", got, bID)
	}
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, bID); got != aID {
		t.Fatalf("b parent_session_id = %q, want %q", got, aID)
	}
}

// TestResolver_NoNotifyWhenNothingLinked asserts the resolver writes ZERO
// notify rows when a pass links nothing (no orphans). An open poller must not
// be spammed with refetch signals for a no-op pass.
func TestResolver_NoNotifyWhenNothingLinked(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})
	// A single self-rooted root session: no orphan, nothing to link.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "solo",
		RootNativeID: "solo",
		Kind:         canonical.KindRoot,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans (no orphans): %v", err)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM notify`); n != 0 {
		t.Fatalf("resolver wrote %d notify rows on a no-op pass, want 0", n)
	}
}
