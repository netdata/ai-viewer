package ingest

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestApplySessionStarted_StashSurvivesReEmitWithoutStash pins P1.7b (SOW-0003
// Round 7): the SESSION upsert grafts the resolver's `aiViewer` stash on conflict,
// so the child session's `toolUseId` join key survives a later stash-free session
// re-emit. Before the fix the session upsert replaced extras_json WHOLESALE
// (`extras_json = excluded.extras_json`), so a stash-free re-emit erased the
// child's toolUseId and the resolver could never link the op→child edge. (The
// Round-6 P2.6d fix covered OP extras only; this is the missed SESSION sibling.)
func TestApplySessionStarted_StashSurvivesReEmitWithoutStash(t *testing.T) {
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
	apply := func(tx *sql.Tx, ev canonical.Event) {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}

	// Phase 1: the child sub-agent session lands carrying the toolUseId stash (and
	// the parentNativeId stash the writer always adds).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:       childNative,
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		AgentName:      "Explore",
		Extras:         map[string]any{"aiViewer": map[string]any{"toolUseId": toolUseID}},
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}
	childID := canonicalSessionID(src, childNative)
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM sessions WHERE id=?`, childID); got != toolUseID {
		t.Fatalf("child toolUseId stash = %q after first emit, want %q (premise)", got, toolUseID)
	}

	// Phase 2: re-emit the SAME child session WITHOUT the aiViewer stash — exactly
	// what a stash-free parent-map re-read would emit (no Extras at all).
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	apply(tx2, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		NativeID:       childNative,
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		// No toolUseId in Extras — the writer still stamps parentNativeId/rootNativeId.
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}

	// The toolUseId stash must survive the stash-free re-emit (grafted back).
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM sessions WHERE id=?`, childID); got != toolUseID {
		t.Errorf("session toolUseId stash erased by stash-free re-emit: got %q, want %q (P1.7b — session extras must graft the aiViewer stash, not wholesale-replace)", got, toolUseID)
	}
	// The parentNativeId the second emit DID carry must still be present too.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.parentNativeId'),'') FROM sessions WHERE id=?`, childID); got != "parent" {
		t.Errorf("session parentNativeId = %q after re-emit, want %q", got, "parent")
	}
}

// TestApplyOpStarted_NullAttributeNotDeleted pins P2.7c (SOW-0003 Round 7): the op
// extras graft uses json_set, NOT json_patch, so a replay whose extras carry an
// explicit JSON `null` value does NOT delete a key. aiagent_v3 copies arbitrary
// source attributes into op extras (`extras["attr."+k] = v`), so a replay with
// `{"attr.x":null}` under json_patch (RFC 7386) would DELETE key `attr.x` — a
// shared-ingester data-loss regression. This test confirms no key is lost.
func TestApplyOpStarted_NullAttributeNotDeleted(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})
	apply := func(tx *sql.Tx, ev canonical.Event) {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.TurnStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000}, SessionNativeID: "s", Seq: 1,
	})
	// Phase 1: op with two attributes, one of which will be re-sent as null.
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "call",
		Extras: map[string]any{"attr.x": "v", "attr.y": "keep"},
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}
	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "s"), 1), 1)

	// Phase 2: replay the SAME op with attr.x explicitly null (a value the source can
	// legitimately produce). Under json_patch this would DELETE attr.x.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	apply(tx2, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "call",
		Extras: map[string]any{"attr.x": nil, "attr.y": "keep"},
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}

	// attr.x must still EXIST as a key (its value may be the replayed null, but the
	// key must not be deleted). json_type returns NULL (the SQL NULL, scanned as "")
	// only when the path is absent; for a present null value it returns 'null'.
	if typ := scanString(t, db,
		`SELECT IFNULL(json_type(extras_json,'$."attr.x"'),'') FROM ops WHERE id=?`, opID); typ == "" {
		t.Errorf("key attr.x was DELETED by a null-valued replay (P2.7c — json_patch null-as-delete; the graft must use json_set)")
	}
	// attr.y must be intact.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$."attr.y"'),'') FROM ops WHERE id=?`, opID); got != "keep" {
		t.Errorf("attr.y = %q after null-valued replay, want %q", got, "keep")
	}
}

// TestApplySessionStarted_NullAttributeNotDeleted is the SESSION sibling of the op
// null-delete pin (P2.7c): the session extras graft must also be json_set, never
// json_patch, so a null-valued session-extras replay never deletes a key.
func TestApplySessionStarted_NullAttributeNotDeleted(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})
	apply := func(tx *sql.Tx, ev canonical.Event) {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		Extras: map[string]any{"k": "v", "keep": "yes"},
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}
	sid := canonicalSessionID(src, "s")

	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	apply(tx2, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		Extras: map[string]any{"k": nil, "keep": "yes"},
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}

	if typ := scanString(t, db,
		`SELECT IFNULL(json_type(extras_json,'$.k'),'') FROM sessions WHERE id=?`, sid); typ == "" {
		t.Errorf("session key k was DELETED by a null-valued replay (P2.7c — session graft must use json_set, not json_patch)")
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.keep'),'') FROM sessions WHERE id=?`, sid); got != "yes" {
		t.Errorf("session key keep = %q after null replay, want %q", got, "yes")
	}
}

// TestResolver_ToolUseIdMatchConstrainedByParent pins P2.7e (SOW-0003 Round 7):
// when two children in ONE source carry the SAME toolUseId under DIFFERENT parents,
// linkOpChildrenByToolUse must link each parent op to ITS OWN structural child, not
// an arbitrary same-source child. The structural constraint
// (child.parent_session_id = parent.id OR child's stashed parentNativeId = parent)
// is what disambiguates. Without it the scalar subquery could pick either child.
func TestResolver_ToolUseIdMatchConstrainedByParent(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	const sharedTool = "toolu_shared"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "claude-code", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "claude-code", "/tmp", NopPricer{})
	apply := func(tx *sql.Tx, ev canonical.Event) {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// Two parents, each with an Agent op stashing the SAME toolUseId, and each with
	// its OWN child carrying that toolUseId. The children land first so their
	// parent_session_id resolves immediately (the structural FK is set), giving the
	// resolver a clean structural signal to disambiguate.
	for _, p := range []string{"pA", "pB"} {
		apply(tx, canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:  p, RootNativeID: p, Kind: canonical.KindRoot,
		})
		child := p + ":agent:ag"
		apply(tx, canonical.SessionStartedEvent{
			EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1050},
			NativeID:       child,
			RootNativeID:   p,
			ParentNativeID: p,
			Kind:           canonical.KindSubAgent,
			Extras:         map[string]any{"aiViewer": map[string]any{"toolUseId": sharedTool}},
		})
		apply(tx, canonical.TurnStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1000}, SessionNativeID: p, Seq: 1,
		})
		apply(tx, canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
			SessionNativeID: p, TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpSession, Name: "explore",
			Extras: map[string]any{"aiViewer": map[string]any{"toolUseId": sharedTool}},
		})
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	// Each parent op must link to ITS OWN child, not the other's.
	for _, p := range []string{"pA", "pB"} {
		opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, p), 1), 1)
		wantChild := canonicalSessionID(src, p+":agent:ag")
		if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != wantChild {
			t.Errorf("parent %q op linked to %q, want its own child %q (P2.7e — toolUseId match must be parent-constrained)", p, got, wantChild)
		}
	}
}

// TestResolver_ToolUseIdMatchConstrainedByParent_StashOnly is the
// pre-FK-resolution variant of P2.7e: a child whose parent_session_id has not yet
// been resolved (parent landed after the child) still disambiguates via its stashed
// `aiViewer.parentNativeId`, so the structural constraint's second arm is exercised.
func TestResolver_ToolUseIdMatchConstrainedByParent_StashOnly(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	const sharedTool = "toolu_shared2"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "claude-code", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "claude-code", "/tmp", NopPricer{})
	apply := func(tx *sql.Tx, ev canonical.Event) {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}

	// Children land BEFORE their parents → parent_session_id stays NULL, but each
	// child stashes its parentNativeId. The op→child match must still pick the right
	// child via the parentNativeId stash arm. We deliberately do NOT run the parent
	// linkage between, so the FK arm cannot help — only the stash arm can.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for _, p := range []string{"qA", "qB"} {
		child := p + ":agent:ag"
		// Child first, parent not yet present → parent_session_id NULL, parentNativeId stashed.
		apply(tx, canonical.SessionStartedEvent{
			EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1050},
			NativeID:       child,
			RootNativeID:   p,
			ParentNativeID: p,
			Kind:           canonical.KindSubAgent,
			Extras:         map[string]any{"aiViewer": map[string]any{"toolUseId": sharedTool}},
		})
	}
	// Now the parents + their ops land.
	for _, p := range []string{"qA", "qB"} {
		apply(tx, canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
			NativeID:  p, RootNativeID: p, Kind: canonical.KindRoot,
		})
		apply(tx, canonical.TurnStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1000}, SessionNativeID: p, Seq: 1,
		})
		apply(tx, canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
			SessionNativeID: p, TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpSession, Name: "explore",
			Extras: map[string]any{"aiViewer": map[string]any{"toolUseId": sharedTool}},
		})
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Confirm the premise: the children's parent_session_id is still NULL (parent
	// linkage has not been run yet, so only the stash arm can disambiguate).
	for _, p := range []string{"qA", "qB"} {
		cid := canonicalSessionID(src, p+":agent:ag")
		if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, cid); got != "" {
			t.Fatalf("child of %q already has parent_session_id %q; premise (FK arm unavailable) broken", p, got)
		}
	}

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	for _, p := range []string{"qA", "qB"} {
		opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, p), 1), 1)
		wantChild := canonicalSessionID(src, p+":agent:ag")
		if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != wantChild {
			t.Errorf("parent %q op linked to %q, want its own child %q (P2.7e — parentNativeId-stash arm of the structural constraint)", p, got, wantChild)
		}
	}
}

// TestResolver_LinksOpChildAfterLateMetaToolUseId pins the END-TO-END P1.7a gap:
// parent Agent op read BEFORE its meta (has toolUseId, no child link) + child
// transcript read BEFORE its own meta (lands with NO toolUseId stash) → the
// resolver CANNOT link yet (the child carries no toolUseId to match). When the
// late `.meta.json` finally arrives, the adapter emits a SessionUpdated carrying
// the child's toolUseId (the P1.7a fix); applySessionUpdated merges it into the
// child row, and the resolver then links the op→child edge. This is the exact
// child-before-meta scenario the prior round left orphaned.
func TestResolver_LinksOpChildAfterLateMetaToolUseId(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	const childNative = "parent:agent:abc111def222ccc"
	const toolUseID = "toolu_agent_late"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "claude-code", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "claude-code", "/tmp", NopPricer{})
	apply := func(tx *sql.Tx, ev canonical.Event) {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}

	// Phase 1: parent session + turn + Agent op (toolUseId stash, NO child link), AND
	// the child session — but the child landed BEFORE its meta, so it carries NO
	// toolUseId stash (only parentNativeId/rootNativeId from the structural path).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "parent", RootNativeID: "parent", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.TurnStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000}, SessionNativeID: "parent", Seq: 1,
	})
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpSession, Name: "explore",
		Extras: map[string]any{"aiViewer": map[string]any{"toolUseId": toolUseID}},
	})
	apply(tx, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1200},
		NativeID:       childNative,
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		// NO toolUseId — the child transcript was read before its .meta.json.
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}

	parentID := canonicalSessionID(src, "parent")
	opID := canonicalOpID(canonicalTurnID(parentID, 1), 1)
	childID := canonicalSessionID(src, childNative)

	// Run the resolver: it must NOT link yet — the child carries no toolUseId.
	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans (pre-meta): %v", err)
	}
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != "" {
		t.Fatalf("op linked to %q before the child's toolUseId arrived; the child-before-meta gap was not reproduced", got)
	}

	// Phase 2: the late .meta.json arrives → the adapter's repair emits a
	// SessionUpdated carrying the child's toolUseId (P1.7a). applySessionUpdated
	// merges it into the child row alongside parentNativeId/rootNativeId.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	apply(tx2, canonical.SessionUpdatedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1300},
		NativeID:  childNative,
		AgentName: "Explore",
		Extras:    map[string]any{"aiViewer": map[string]any{"toolUseId": toolUseID}},
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}
	// The merge must preserve parentNativeId AND add toolUseId.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM sessions WHERE id=?`, childID); got != toolUseID {
		t.Fatalf("child toolUseId after SessionUpdated repair = %q, want %q (applySessionUpdated must merge the stash)", got, toolUseID)
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.parentNativeId'),'') FROM sessions WHERE id=?`, childID); got != "parent" {
		t.Fatalf("child parentNativeId clobbered by the toolUseId repair: got %q, want %q", got, "parent")
	}

	// Phase 3: the resolver now links the op→child edge via the toolUseId match.
	if err := r.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans (post-meta): %v", err)
	}
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != childID {
		t.Errorf("op child_session_id after late-meta toolUseId repair = %q, want %q (P1.7a end-to-end — child-before-meta must eventually link)", got, childID)
	}
}
