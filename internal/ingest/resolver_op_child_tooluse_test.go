package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestResolver_LinksOpChildByToolUseId pins P1.6 (SOW-0003 Round 6): a parent
// `Agent` op tailed BEFORE its sidecar `.meta.json` is written carries NO
// ChildSessionNativeID (the child native id is unknown at op-write time), so the
// childNativeId stash + linkOpChildren pass cannot link it. Instead the parent op
// stashes the `toolUseId` it has from its own assistant.tool_use block, and the
// child sub-agent session — when it lands — carries the SAME `toolUseId` in its
// extras. The additive linkOpChildrenByToolUse resolver pass links
// ops.child_session_id by matching the two stashed `toolUseId`s, with NO transcript
// re-read (so the catalog rollups are never re-counted), and notifies the PARENT
// session so an open UI refetches.
//
// This is the re-emit-free replacement for the Round-3/4/5 late-meta from-0
// re-emit, which double-counted catalog_* aggregates (P1.6).
func TestResolver_LinksOpChildByToolUseId(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	const childNative = "parent:agent:abc111def222ccc"
	const toolUseID = "toolu_agent_xyz"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "claude-code", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "claude-code", "/tmp", NopPricer{})

	// Phase 1: parent session + turn + parent Agent op. The op carries the
	// toolUseId stash but NO ChildSessionNativeID (the meta was not read yet, so
	// the child native id is unknown) — exactly the parent-before-meta ordering.
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
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "parent",
		TurnSeq:         1,
		Seq:             1,
		ParentOpSeq:     -1,
		Kind:            canonical.OpSession,
		Name:            "explore",
		// No ChildSessionNativeID — the meta is not known. Just the toolUseId
		// stash the adapter always emits.
		Extras: map[string]any{"aiViewer": map[string]any{"toolUseId": toolUseID}},
	}); err != nil {
		t.Fatalf("apply parent Agent op: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}

	parentID := canonicalSessionID(src, "parent")
	opID := canonicalOpID(canonicalTurnID(parentID, 1), 1)

	// Before the child lands: child_session_id is NULL; the op carries the
	// toolUseId stash for the resolver to match on.
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != "" {
		t.Fatalf("op child_session_id linked before child landed: %q", got)
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM ops WHERE id=?`, opID); got != toolUseID {
		t.Fatalf("op toolUseId stash = %q, want %q", got, toolUseID)
	}

	// Phase 2: the child sub-agent session lands carrying the SAME toolUseId.
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
		AgentName:      "Explore",
		Extras:         map[string]any{"aiViewer": map[string]any{"toolUseId": toolUseID}},
	}); err != nil {
		t.Fatalf("apply child session: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}
	childID := canonicalSessionID(src, childNative)

	// Confirm the child row carries the toolUseId stash through the writer's
	// mergeExtras (it must survive alongside parentNativeId/rootNativeId).
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM sessions WHERE id=?`, childID); got != toolUseID {
		t.Fatalf("child session toolUseId stash = %q, want %q (mergeExtras dropped it?)", got, toolUseID)
	}

	// Run the resolver pass.
	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	// (a) ops.child_session_id is now linked to the child row by the toolUseId match.
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != childID {
		t.Fatalf("op child_session_id after resolver = %q, want %q (toolUseId pass)", got, childID)
	}

	// (b) the PARENT session got a session_changed notify so an open
	// parent-detail view refetches and renders the now-linked child op.
	if !notifyHasSession(t, db, parentID) {
		t.Fatalf("no session_changed notify for parent %q after toolUseId op-child linkage", parentID)
	}
}

// TestResolver_ToolUseIdPassIgnoresAiagentOps pins the additive guarantee: an op
// that carries NO `aiViewer.toolUseId` stash (every aiagent v2/v3 op, and any
// non-Agent claude-code op) is never touched by linkOpChildrenByToolUse, even when
// a session in the same source happens to be a sub-agent. Without this guarantee
// the new pass would risk mis-linking unrelated ops or regressing aiagent.
func TestResolver_ToolUseIdPassIgnoresAiagentOps(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// A parent session, a sub-agent child session (no toolUseId stash anywhere —
	// aiagent links via parentNativeId, not toolUseId), and a parent op with NO
	// aiViewer stash at all.
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "p", RootNativeID: "p", Kind: canonical.KindRoot,
	}); err != nil {
		t.Fatalf("apply parent: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1050},
		NativeID:  "c", RootNativeID: "p", ParentNativeID: "p", Kind: canonical.KindSubAgent,
	}); err != nil {
		t.Fatalf("apply child: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1000},
		SessionNativeID: "p", Seq: 1,
	}); err != nil {
		t.Fatalf("apply turn: %v", err)
	}
	// A plain LLM op — no aiViewer stash, child_session_id NULL.
	if err := w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "p", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call", Provider: "anthropic", Model: "claude-opus-4",
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

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "p"), 1), 1)
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != "" {
		t.Fatalf("toolUseId pass spuriously linked an aiagent op with no stash: child_session_id=%q", got)
	}
	// The toolUseId pass must NOT emit any notify for an aiagent op (no-op pass).
	if n := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='session_changed'`); n != 0 {
		t.Fatalf("toolUseId pass emitted %d session_changed notify for aiagent ops, want 0", n)
	}
}
