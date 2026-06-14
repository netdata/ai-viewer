package ingest

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// withWriter runs fn inside a transaction owned by a fresh writer
// against an in-memory store. The transaction is committed on return so
// subsequent SELECTs see the rows.
func withWriter(t *testing.T, sourceID string, fn func(ctx context.Context, tx *sql.Tx, w *writer)) *sql.DB {
	t.Helper()
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(sourceID, "aiagent_v3", "/tmp", NopPricer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	fn(ctx, tx, w)
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return db
}

func ensureSourceRowDirect(ctx context.Context, db *sql.DB, id, format, loc string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// true mirrors the ingester's default-resolved fts5_index_logs (indexed
	// unless the operator opts out); these tests do not exercise the flag.
	// "" mirrors the ingester's default-resolved metaJSON (no adapter-owned
	// metadata, so the column is bound NULL — the omit-when-NULL contract).
	// Dedicated meta-persistence coverage lives in TestIngester_PersistsSourceMeta.
	if err := ensureSourceRow(ctx, tx, id, format, loc, true, ""); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// TestApplyOpStarted_StashSurvivesReEmitWithoutStash pins P2.6d (SOW-0003 Round 6):
// the op upsert MERGES extras_json on conflict, so the resolver's aiViewer stash
// (childNativeId / toolUseId) survives a later re-emit of the same op whose extras
// lack the stash. Before the fix the upsert replaced extras_json wholesale, so a
// stash-free re-emit erased the join key the resolver still needs and permanently
// orphaned the op→child edge.
func TestApplyOpStarted_StashSurvivesReEmitWithoutStash(t *testing.T) {
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

	// Phase 1: parent session + turn + Agent op carrying the toolUseId stash (and a
	// childNativeId stash, the two join keys the resolver consumes).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "parent", RootNativeID: "parent", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "parent", Seq: 1,
	})
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpSession, Name: "explore",
		Extras: map[string]any{"aiViewer": map[string]any{
			"toolUseId":     toolUseID,
			"childNativeId": childNative,
		}},
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "parent"), 1), 1)

	// Phase 2: re-emit the SAME op (same turn/seq) with extras that do NOT carry the
	// aiViewer stash — exactly what a plain parent re-read/re-emit would look like.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	apply(tx2, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpSession, Name: "explore",
		// No Extras at all.
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}

	// Both stash keys must still be present after the stash-free re-emit.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM ops WHERE id=?`, opID); got != toolUseID {
		t.Errorf("toolUseId stash erased by stash-free re-emit: got %q, want %q", got, toolUseID)
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.childNativeId'),'') FROM ops WHERE id=?`, opID); got != childNative {
		t.Errorf("childNativeId stash erased by stash-free re-emit: got %q, want %q", got, childNative)
	}
}

// TestApplyOpStarted_NullExcludedKeepsOnlyStash pins P2.8 (SOW-0003 Round 8): when an
// op re-emit carries NO extras at all (excluded.extras_json IS NULL), the graft must
// keep ONLY the existing row's `$.aiViewer` stash (the resolver join keys) — NOT the
// whole old blob. Before the fix the NULL-excluded branch returned the existing extras
// WHOLESALE, so a stale non-aiViewer attribute (e.g. an aiagent_v3 op's copied attr.*)
// survived a re-emit that deliberately dropped it, contradicting "excluded wins
// wholesale, only $.aiViewer grafted" (ingester.md §145).
//
// Without the fix the stale `attr.x` key survives, so the assertion FAILS.
func TestApplyOpStarted_NullExcludedKeepsOnlyStash(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	const childNative = "parent:agent:abc111def222ccc"
	const toolUseID = "toolu_agent_xyz"

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

	// Phase 1: parent session + turn + op carrying BOTH a non-aiViewer attribute
	// (the stale key a re-emit must drop) AND the aiViewer stash (the join key a
	// re-emit must preserve).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "parent", RootNativeID: "parent", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "parent", Seq: 1,
	})
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpSession, Name: "explore",
		Extras: map[string]any{
			"attr.x":   "stale",
			"aiViewer": map[string]any{"toolUseId": toolUseID, "childNativeId": childNative},
		},
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "parent"), 1), 1)

	// Phase 2: re-emit the SAME op with NO extras at all (NULL excluded.extras_json).
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	apply(tx2, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpSession, Name: "explore",
		// No Extras → excluded.extras_json IS NULL.
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}

	// The aiViewer stash survives.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM ops WHERE id=?`, opID); got != toolUseID {
		t.Errorf("toolUseId stash dropped by NULL-excluded re-emit: got %q, want %q", got, toolUseID)
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.childNativeId'),'') FROM ops WHERE id=?`, opID); got != childNative {
		t.Errorf("childNativeId stash dropped by NULL-excluded re-emit: got %q, want %q", got, childNative)
	}
	// The stale non-aiViewer attribute is DROPPED (the whole old blob must NOT survive).
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.attr\.x'),'__absent__') FROM ops WHERE id=?`, opID); got != "__absent__" {
		t.Errorf("stale non-aiViewer attr survived NULL-excluded re-emit: got %q, want absent (only the aiViewer stash may survive)", got)
	}
	// Only the aiViewer object remains at the top level.
	if got := scanString(t, db,
		`SELECT IFNULL(group_concat(key,','),'') FROM ops, json_each(ops.extras_json) WHERE ops.id=?`, opID); got != "aiViewer" {
		t.Errorf("NULL-excluded re-emit kept top-level keys %q, want only \"aiViewer\"", got)
	}
}

// TestApplyOpStarted_NullExcludedNoStashYieldsNull pins the P2.8 second case: a
// NULL-excluded re-emit of an op whose existing row has NO aiViewer stash must yield a
// SQL NULL extras_json (nothing to preserve), not an empty `{}` object and not the
// stale old blob.
func TestApplyOpStarted_NullExcludedNoStashYieldsNull(t *testing.T) {
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
		NativeID:  "parent", RootNativeID: "parent", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "parent", Seq: 1,
	})
	// Op with a non-aiViewer attribute and NO aiViewer stash.
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "gen",
		Extras: map[string]any{"attr.x": "stale"},
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "parent"), 1), 1)

	// Re-emit with NO extras → nothing to preserve → extras_json must become NULL.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	apply(tx2, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "gen",
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}

	if got := scanInt(t, db, `SELECT extras_json IS NULL FROM ops WHERE id=?`, opID); got != 1 {
		blob := scanString(t, db, `SELECT IFNULL(extras_json,'<null>') FROM ops WHERE id=?`, opID)
		t.Errorf("NULL-excluded re-emit with no stash left extras_json non-NULL: %q (want SQL NULL)", blob)
	}
}

func TestWriter_SessionStartedInsertsRow(t *testing.T) {
	t.Parallel()
	db := withWriter(t, "aiagent_v3:/tmp", func(ctx context.Context, tx *sql.Tx, w *writer) {
		ev := canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
			NativeID:     "sess-1",
			RootNativeID: "sess-1",
			Kind:         canonical.KindRoot,
			AgentName:    "research",
			Model:        "claude-opus-4",
			CallPath:     "cli:research",
		}
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply: %v", err)
		}
	})

	got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='sess-1'`)
	if got != 1 {
		t.Errorf("session count = %d, want 1", got)
	}
	if name := scanString(t, db, `SELECT agent_name FROM sessions WHERE native_id='sess-1'`); name != "research" {
		t.Errorf("agent_name = %q, want research", name)
	}
	if model := scanString(t, db, `SELECT model FROM sessions WHERE native_id='sess-1'`); model != "claude-opus-4" {
		t.Errorf("model = %q, want claude-opus-4", model)
	}
}

func TestWriter_SessionUpdatedCoalescesEmptyFields(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
		AgentName: "research",
	})
	_ = tx.Commit()

	tx, _ = db.BeginTx(ctx, nil)
	w.resetBatch()
	_ = w.apply(ctx, tx, canonical.SessionUpdatedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1500},
		NativeID:  "sess-1",
		Model:     "claude-opus-4", // empty fields stay null/unchanged
	})
	_ = tx.Commit()

	if name := scanString(t, db, `SELECT agent_name FROM sessions WHERE native_id='sess-1'`); name != "research" {
		t.Errorf("agent_name = %q, want research (unchanged)", name)
	}
	if model := scanString(t, db, `SELECT model FROM sessions WHERE native_id='sess-1'`); model != "claude-opus-4" {
		t.Errorf("model = %q, want claude-opus-4 (now set)", model)
	}
}

func TestWriter_SessionFinalizedSetsStatusAndEndTs(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
	})
	_ = tx.Commit()

	tx, _ = db.BeginTx(ctx, nil)
	w.resetBatch()
	_ = w.apply(ctx, tx, canonical.SessionFinalizedEvent{
		EventBase:    canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 5000},
		NativeID:     "sess-1",
		Status:       canonical.StatusFailed,
		ErrorClass:   "exec_error",
		ErrorMessage: "boom",
		EndTs:        5000,
	})
	_ = tx.Commit()

	if s := scanString(t, db, `SELECT status FROM sessions WHERE native_id='sess-1'`); s != "failed" {
		t.Errorf("status = %q, want failed", s)
	}
	if endTs := scanInt(t, db, `SELECT IFNULL(end_ts,0) FROM sessions WHERE native_id='sess-1'`); endTs != 5000 {
		t.Errorf("end_ts = %d, want 5000", endTs)
	}
	if ec := scanString(t, db, `SELECT IFNULL(error_class,'') FROM sessions WHERE native_id='sess-1'`); ec != "exec_error" {
		t.Errorf("error_class = %q, want exec_error", ec)
	}
}

func TestWriter_TurnAndOpFlow(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	events := []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
			NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
			SessionNativeID: "sess-1", Seq: 1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "call",
			Model: "claude-opus-4", Provider: "anthropic",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 4, Ts: 1500},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1,
			Status:   "completed",
			EndTs:    1500,
			TokensIn: 100, TokensOut: 200, TokensCacheRead: 50, TokensCacheWrite: 25,
			CostUSD: 0.0123,
		},
		canonical.TurnFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 5, Ts: 1600},
			SessionNativeID: "sess-1", Seq: 1, Status: "completed", EndTs: 1600,
		},
	}

	tx, _ := db.BeginTx(ctx, nil)
	for _, ev := range events {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}
	if err := refreshAggregates(ctx, tx, w.dirtyTurnIDs, w.dirtySessionIDs); err != nil {
		t.Fatalf("aggregates: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Turn aggregates reflect the op.
	if v := scanInt(t, db, `SELECT tokens_in FROM turns WHERE seq=1`); v != 100 {
		t.Errorf("turn tokens_in = %d, want 100", v)
	}
	if v := scanInt(t, db, `SELECT tokens_out FROM turns WHERE seq=1`); v != 200 {
		t.Errorf("turn tokens_out = %d, want 200", v)
	}
	if v := scanInt(t, db, `SELECT op_count FROM turns WHERE seq=1`); v != 1 {
		t.Errorf("turn op_count = %d, want 1", v)
	}
	// Session aggregates roll up from the turn.
	if v := scanInt(t, db, `SELECT tokens_in FROM sessions WHERE native_id='sess-1'`); v != 100 {
		t.Errorf("session tokens_in = %d, want 100", v)
	}
	if v := scanInt(t, db, `SELECT turn_count FROM sessions WHERE native_id='sess-1'`); v != 1 {
		t.Errorf("session turn_count = %d, want 1", v)
	}
	if v := scanInt(t, db, `SELECT op_count FROM sessions WHERE native_id='sess-1'`); v != 1 {
		t.Errorf("session op_count = %d, want 1", v)
	}
	// Catalog rows populated.
	if v := scanInt(t, db, `SELECT call_count FROM catalog_models WHERE provider='anthropic' AND name='claude-opus-4'`); v != 1 {
		t.Errorf("catalog_models call_count = %d, want 1", v)
	}
	if v := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='anthropic'`); v != 1 {
		t.Errorf("catalog_providers call_count = %d, want 1", v)
	}
}

func TestWriter_OpFailureBumpsFailureCount(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	events := []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
			NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
			SessionNativeID: "sess-1", Seq: 1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpTool, Name: "shell", ToolNamespace: "shell",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 4, Ts: 1500},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "exit_nonzero", ErrorMessage: "boom",
			EndTs: 1500,
		},
	}
	for _, ev := range events {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}
	if err := refreshAggregates(ctx, tx, w.dirtyTurnIDs, w.dirtySessionIDs); err != nil {
		t.Fatalf("aggregates: %v", err)
	}
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT failure_count FROM sessions WHERE native_id='sess-1'`); v != 1 {
		t.Errorf("session failure_count = %d, want 1", v)
	}
	if v := scanInt(t, db, `SELECT failure_count FROM catalog_tools WHERE namespace='shell' AND name='shell'`); v != 1 {
		t.Errorf("catalog_tools failure_count = %d, want 1", v)
	}
}

func TestWriter_PayloadRef(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1200},
		SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
	})
	_ = w.apply(ctx, tx, canonical.PayloadRefEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1250},
		SessionNativeID: "sess-1", TurnSeq: 1, OpSeq: 1,
		PayloadKind: "llm_request", Format: "http", Compression: "gzip",
		LocationURI:   "file:///tmp/payloads/sess-1/turn-1/op-1-request.http.gz",
		OriginalBytes: 12345, StoredBytes: 4096, SHA256: "abc123",
	})
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`); v != 1 {
		t.Errorf("payload_refs count = %d, want 1", v)
	}
	if s := scanString(t, db, `SELECT kind FROM payload_refs`); s != "llm_request" {
		t.Errorf("payload kind = %q, want llm_request", s)
	}
	if v := scanInt(t, db, `SELECT original_bytes FROM payload_refs`); v != 12345 {
		t.Errorf("original_bytes = %d, want 12345", v)
	}
}

func TestWriter_LogEntrySessionScoped(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "sess-1", Severity: "wrn",
		Source: "aiagent_v3", Message: "slow turn",
	})
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE source_id IS NULL`); v != 1 {
		t.Errorf("session-scoped log count = %d, want 1", v)
	}
	if s := scanString(t, db, `SELECT severity FROM log_entries`); s != "WRN" {
		t.Errorf("severity = %q, want WRN", s)
	}
}

func TestWriter_SourceError(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SourceErrorEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 0, Ts: 1100},
		File:      "session/bad.jsonl", Offset: 42, Message: "bad json",
	})
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 1 {
		t.Errorf("parse_errors = %d, want 1", v)
	}
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE source_id IS NOT NULL AND severity='ERR'`); v != 1 {
		t.Errorf("source-scoped error log count = %d, want 1", v)
	}
}

func TestWriter_PricerFillsZeroCost(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakePricer{ret: 1.25}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	for _, ev := range []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "call",
			Provider: "openai", Model: "gpt-5",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1,
			TokensIn: 100, TokensOut: 50, EndTs: 1200, Status: "completed",
		},
	} {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}
	_ = tx.Commit()

	var cost float64
	_ = db.QueryRow(`SELECT cost_usd FROM ops`).Scan(&cost)
	if cost != 1.25 {
		t.Errorf("cost_usd = %f, want 1.25 (from pricer)", cost)
	}
	if p.calls != 1 {
		t.Errorf("pricer calls = %d, want 1", p.calls)
	}
	// The writer reads the op's start_ts from the ops row and passes
	// that to the pricer (per pricing.md §"Temporal resolution
	// algorithm") so tier selection reflects when the op STARTED, not
	// the finalize event timestamp. OpStarted above carried Ts=1100.
	if p.lastTsUS != 1100 {
		t.Errorf("pricer tsUS = %d, want 1100 (op start_ts)", p.lastTsUS)
	}
}

// TestWriter_PricerSkippedWhenOpRowMissing verifies that an
// OpFinalized arriving without a matching OpStarted row (sql.ErrNoRows
// on the lookup) does NOT invoke the pricer — provider/model/kind are
// unknown so pricing it would produce a noisy and unactionable
// "unknown pricing for provider \"\" model \"\"" warning. The op is
// still written with cost_usd=0; the missing OpStarted is the real
// defect and is logged via the standard SourceError path.
func TestWriter_PricerSkippedWhenOpRowMissing(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 7777},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 10, TokensOut: 5, EndTs: 7777, Status: "completed",
	})
	_ = tx.Commit()

	if p.calls != 0 {
		t.Errorf("pricer calls = %d, want 0 (no provider/model lookup -> skip)", p.calls)
	}
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN'`); v != 0 {
		t.Errorf("WRN log_entries count = %d, want 0", v)
	}
}

// TestWriter_PricingMissEmitsWarningOnce verifies the writer emits a
// SourceError WRN row per unique (provider, model, missKind) on the
// first miss and dedups subsequent misses within the same batch.
func TestWriter_PricingMissEmitsWarningOnce(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// Two ops with the SAME (provider, model) — only one warning row
	// must land.
	for i, seq := range []uint64{2, 3, 4, 5} {
		opSeq := i + 1
		_ = w.apply(ctx, tx, canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: seq, Ts: 1100 + int64(i)},
			SessionNativeID: "s", TurnSeq: 1, Seq: opSeq, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "call",
			Provider: "madeup", Model: "doesnotexist",
		})
		_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: seq + 10, Ts: 1200 + int64(i)},
			SessionNativeID: "s", TurnSeq: 1, Seq: opSeq,
			TokensIn: 100, TokensOut: 50, EndTs: 1200 + int64(i), Status: "completed",
		})
	}
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN' AND source_id IS NOT NULL`); v != 1 {
		t.Errorf("dedup failed: WRN log_entries count = %d, want 1", v)
	}
	if p.calls != 4 {
		t.Errorf("pricer calls = %d, want 4 (one per op, including dedupped warnings)", p.calls)
	}
	// The Sources panel surfaces pricing misses through the same
	// parse_errors counter the adapter SourceError path bumps. A
	// deduped miss must bump the counter exactly once per unique
	// (provider, model, missKind) tuple within the batch.
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 1 {
		t.Errorf("parse_errors = %d, want 1 (deduped pricing miss must bump counter once)", v)
	}
	// The log entry's timestamp must be the op's pricing timestamp
	// (start_ts) — the same value passed to the pricer — so the WRN
	// row sits at the same point in the timeline as the op that
	// triggered it. The first op was OpStarted with Ts=1100.
	if v := scanInt(t, db, `SELECT ts FROM log_entries WHERE severity='WRN' AND source_id IS NOT NULL`); v != 1100 {
		t.Errorf("log_entry ts = %d, want 1100 (op start_ts, not finalize ts)", v)
	}
}

// TestWriter_PricingMissDistinctModelsEmitDistinctWarnings verifies
// the dedup keys on (provider, model, missKind) — different misses
// land separate rows.
func TestWriter_PricingMissDistinctModelsEmitDistinctWarnings(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	models := []struct{ provider, model string }{
		{"madeup", "alpha"},
		{"madeup", "beta"},
		{"unknown", "alpha"},
	}
	for i, mm := range models {
		opSeq := i + 1
		_ = w.apply(ctx, tx, canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: uint64(2 + i*2), Ts: 1100 + int64(i)},
			SessionNativeID: "s", TurnSeq: 1, Seq: opSeq, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "call",
			Provider: mm.provider, Model: mm.model,
		})
		_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: uint64(3 + i*2), Ts: 1200 + int64(i)},
			SessionNativeID: "s", TurnSeq: 1, Seq: opSeq,
			TokensIn: 10, TokensOut: 5, EndTs: 1200 + int64(i), Status: "completed",
		})
	}
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN' AND source_id IS NOT NULL`); v != 3 {
		t.Errorf("WRN log_entries count = %d, want 3 (one per unique (provider, model))", v)
	}
	// Three distinct misses must bump parse_errors three times.
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 3 {
		t.Errorf("parse_errors = %d, want 3 (one per unique miss)", v)
	}
}

// TestWriter_PricingMissDedupedAcrossOps verifies a single (provider,
// model, missKind) tuple emits exactly one log row and one
// parse_errors increment regardless of how many priceable ops touch
// it within a batch.
func TestWriter_PricingMissDedupedAcrossOps(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// Ten ops with identical (provider, model) — one warning row,
	// one parse_errors increment.
	for i := 0; i < 10; i++ {
		opSeq := i + 1
		_ = w.apply(ctx, tx, canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: uint64(2 + i*2), Ts: 1100 + int64(i)},
			SessionNativeID: "s", TurnSeq: 1, Seq: opSeq, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "call",
			Provider: "madeup", Model: "doesnotexist",
		})
		_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: uint64(3 + i*2), Ts: 1200 + int64(i)},
			SessionNativeID: "s", TurnSeq: 1, Seq: opSeq,
			TokensIn: 10, TokensOut: 5, EndTs: 1200 + int64(i), Status: "completed",
		})
	}
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN' AND source_id IS NOT NULL`); v != 1 {
		t.Errorf("WRN log_entries count = %d, want 1 (dedup across 10 ops)", v)
	}
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 1 {
		t.Errorf("parse_errors = %d, want 1 (dedup across 10 ops)", v)
	}
}

// TestWriter_PricerSkippedForNonLLMOp verifies that non-LLM ops (kind
// in {"tool","system",...}) bypass the pricer entirely, so they don't
// produce noisy "unknown pricing for provider \"\" model \"\"" warnings.
func TestWriter_PricerSkippedForNonLLMOp(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// Tool op: no provider/model, kind=tool.
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "Read",
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		EndTs: 1200, Status: "completed",
	})
	_ = tx.Commit()

	if p.calls != 0 {
		t.Errorf("pricer called %d times, want 0 (non-LLM op must not invoke pricer)", p.calls)
	}
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN'`); v != 0 {
		t.Errorf("WRN log_entries count = %d, want 0 (no pricer call -> no warning)", v)
	}
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 0 {
		t.Errorf("parse_errors = %d, want 0 (no pricer call -> no counter bump)", v)
	}
}

func TestWriter_PricerSkippedWhenSourceCostPresent(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakePricer{ret: 999}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		CostUSD: 0.5, EndTs: 1200, Status: "completed",
	})
	_ = tx.Commit()

	var cost float64
	_ = db.QueryRow(`SELECT cost_usd FROM ops`).Scan(&cost)
	if cost != 0.5 {
		t.Errorf("cost_usd = %f, want 0.5 (source-recorded)", cost)
	}
	if p.calls != 0 {
		t.Errorf("pricer called %d times, want 0 (source had cost)", p.calls)
	}
}

func TestNopPricer_ReturnsZero(t *testing.T) {
	t.Parallel()
	p := NopPricer{}
	if v := p.Cost("a", "b", 1234567890, 1, 2, 3, 4); v != 0 {
		t.Errorf("NopPricer = %f, want 0", v)
	}
}

func TestMergeExtras_MergesNestedMaps(t *testing.T) {
	t.Parallel()
	a := map[string]any{
		"top":      "a",
		"aiViewer": map[string]any{"x": 1},
	}
	b := map[string]any{
		"aiViewer": map[string]any{"y": 2},
		"new":      true,
	}
	got := mergeExtras(a, b)
	av, _ := got["aiViewer"].(map[string]any)
	if av["x"] != 1 || av["y"] != 2 {
		t.Errorf("nested merge failed: %v", av)
	}
	if got["top"] != "a" {
		t.Errorf("top key lost: %v", got)
	}
	if got["new"] != true {
		t.Errorf("new key missing: %v", got)
	}
}

func TestMarshalExtras_EmptyMapReturnsNil(t *testing.T) {
	t.Parallel()
	v, err := marshalExtras(nil)
	if err != nil {
		t.Fatalf("marshalExtras(nil): %v", err)
	}
	if v != nil {
		t.Errorf("marshalExtras(nil) = %v, want nil", v)
	}
}

func TestWriter_UnknownEventKindErrors(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	if err := w.apply(ctx, tx, fakeEvent{}); err == nil {
		t.Fatal("expected error on unknown event kind")
	}
}

func TestWriter_RequireSessionIDStubsMissing(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	id, err := w.requireSessionID(ctx, tx, "ghost", 1000)
	if err != nil {
		t.Fatalf("requireSessionID: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}
	_ = tx.Commit()

	if v := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='ghost'`); v != 1 {
		t.Errorf("stub session not inserted, count = %d", v)
	}
}

func TestWriter_RequireSessionIDEmptyNativeIsError(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	if _, err := w.requireSessionID(ctx, tx, "", 1000); err == nil {
		t.Fatal("expected error on empty native id")
	}
}

func TestEnsureSourceRow_UpsertsLocation(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "src", "aiagent_v3", "/loc1")
	_ = ensureSourceRowDirect(ctx, db, "src", "aiagent_v3", "/loc2")
	if got := scanString(t, db, `SELECT location FROM sources WHERE id='src'`); got != "/loc2" {
		t.Errorf("location = %q, want /loc2", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sources WHERE id='src'`); got != 1 {
		t.Errorf("row count = %d, want 1", got)
	}
}

func TestUpsertSourceProgress_NoOpEmpty(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "src", "aiagent_v3", "/loc")
	tx, _ := db.BeginTx(ctx, nil)
	if err := upsertSourceProgress(ctx, tx, "src", 0, 0, "", false); err != nil {
		t.Fatalf("noop upsert: %v", err)
	}
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM source_progress`); got != 0 {
		t.Errorf("source_progress count = %d, want 0", got)
	}
}

func TestUpsertSourceProgress_SeqAdvances(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "src", "aiagent_v3", "/loc")

	tx, _ := db.BeginTx(ctx, nil)
	_ = upsertSourceProgress(ctx, tx, "src", 100, 1000, "", false)
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT last_seq FROM source_progress WHERE source_id='src'`); got != 100 {
		t.Errorf("last_seq = %d, want 100", got)
	}

	// Regression must be ignored.
	tx, _ = db.BeginTx(ctx, nil)
	_ = upsertSourceProgress(ctx, tx, "src", 50, 1000, "", false)
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT last_seq FROM source_progress WHERE source_id='src'`); got != 100 {
		t.Errorf("last_seq after regression = %d, want 100", got)
	}

	// Cursor flag updates cursor + advances seq.
	tx, _ = db.BeginTx(ctx, nil)
	_ = upsertSourceProgress(ctx, tx, "src", 200, 2000, `{"a":1}`, true)
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT last_seq FROM source_progress WHERE source_id='src'`); got != 200 {
		t.Errorf("last_seq after cursor advance = %d, want 200", got)
	}
	if got := scanString(t, db, `SELECT cursor FROM source_progress WHERE source_id='src'`); got != `{"a":1}` {
		t.Errorf("cursor = %q, want JSON", got)
	}
}

// fakePricer counts invocations and returns a fixed value. lastTsUS
// records the most-recent ts argument so tests can assert that the
// writer forwards the op's pricing timestamp as the temporal-tier
// selector.
type fakePricer struct {
	calls    int
	ret      float64
	lastTsUS int64
}

func (p *fakePricer) Cost(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64 {
	p.calls++
	p.lastTsUS = tsUS
	return p.ret
}

// fakeDetailPricer implements DetailedPricer so the writer's SourceError
// emission path is exercised. miss controls whether each call reports a
// hit (empty string) or a named miss kind so dedup behaviour can be
// verified.
type fakeDetailPricer struct {
	calls int
	ret   float64
	miss  string
}

func (p *fakeDetailPricer) Cost(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64 {
	cost, _, _ := p.CostWithDetail(provider, model, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite)
	return cost
}

func (p *fakeDetailPricer) CostWithDetail(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) (float64, bool, string) {
	_ = provider
	_ = model
	_ = tsUS
	_ = tokensIn
	_ = tokensOut
	_ = tokensCacheRead
	_ = tokensCacheWrite
	p.calls++
	if p.miss != "" {
		return 0, false, p.miss
	}
	return p.ret, true, ""
}

// TestWriter_PricerNonDetailedFallback covers the priceOp branch that
// runs when the wired Pricer does NOT also implement DetailedPricer.
// In that case the writer must call Cost() and emit zero observability
// rows (no WRN, no parse_errors bump) because the Pricer cannot signal
// "miss" through the plain-Cost contract.
func TestWriter_PricerNonDetailedFallback(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakePricer{ret: 1.23}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "anthropic", Model: "claude-opus-4",
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 10, TokensOut: 5, EndTs: 1200, Status: "completed",
	})
	_ = tx.Commit()

	if p.calls != 1 {
		t.Errorf("pricer called %d times, want 1", p.calls)
	}
	var cost float64
	_ = db.QueryRow(`SELECT cost_usd FROM ops`).Scan(&cost)
	if cost != 1.23 {
		t.Errorf("cost_usd = %f, want 1.23 (plain Pricer.Cost return value)", cost)
	}
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN'`); v != 0 {
		t.Errorf("WRN log_entries = %d, want 0 (plain Pricer cannot signal miss)", v)
	}
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 0 {
		t.Errorf("parse_errors = %d, want 0", v)
	}
}

// TestIsPriceableOp_NonLLMKindSkipped pins the branch that rejects an
// op whose kind is set and not "llm" even when provider/model are
// present (e.g. legacy fixtures that mis-label a tool op with an LLM
// model). Direct unit test against isPriceableOp because the priceOp
// integration path needs OpStarted to land first; we want fast
// coverage of the gate alone.
func TestIsPriceableOp_NonLLMKindSkipped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		kind          sql.NullString
		provider      sql.NullString
		model         sql.NullString
		wantPriceable bool
	}{
		{
			name:          "tool_with_provider_and_model_rejected",
			kind:          sql.NullString{String: "tool", Valid: true},
			provider:      sql.NullString{String: "builtin", Valid: true},
			model:         sql.NullString{String: "Read", Valid: true},
			wantPriceable: false,
		},
		{
			name:          "system_with_provider_model_rejected",
			kind:          sql.NullString{String: "system", Valid: true},
			provider:      sql.NullString{String: "x", Valid: true},
			model:         sql.NullString{String: "y", Valid: true},
			wantPriceable: false,
		},
		{
			name:          "llm_with_provider_model_accepted",
			kind:          sql.NullString{String: "llm", Valid: true},
			provider:      sql.NullString{String: "anthropic", Valid: true},
			model:         sql.NullString{String: "opus", Valid: true},
			wantPriceable: true,
		},
		{
			name:          "missing_kind_legacy_accepted",
			kind:          sql.NullString{Valid: false},
			provider:      sql.NullString{String: "anthropic", Valid: true},
			model:         sql.NullString{String: "opus", Valid: true},
			wantPriceable: true,
		},
		{
			name:          "empty_kind_treated_as_legacy_accepted",
			kind:          sql.NullString{String: "", Valid: true},
			provider:      sql.NullString{String: "anthropic", Valid: true},
			model:         sql.NullString{String: "opus", Valid: true},
			wantPriceable: true,
		},
		{
			name:          "missing_provider_rejected",
			kind:          sql.NullString{String: "llm", Valid: true},
			provider:      sql.NullString{Valid: false},
			model:         sql.NullString{String: "opus", Valid: true},
			wantPriceable: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPriceableOp(tc.kind, tc.provider, tc.model); got != tc.wantPriceable {
				t.Errorf("isPriceableOp = %v, want %v", got, tc.wantPriceable)
			}
		})
	}
}

// TestWriter_PriceableOpToolKindIntegration drives the writer end-to-end
// with a tool op that carries provider+model (e.g. an mcp tool that
// happens to record routing metadata) to confirm the writer.applyOpFinalized
// path skips the pricer entirely for kind != "llm".
func TestWriter_PriceableOpToolKindIntegration(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "mcp_tool",
		Provider: "mcp", Model: "search",
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		EndTs: 1200, Status: "completed",
	})
	_ = tx.Commit()

	if p.calls != 0 {
		t.Errorf("pricer called %d times, want 0 (tool op even with provider/model must not be priced)", p.calls)
	}
}

// TestWriter_DrainObservabilityErrsEmpty pins drainObservabilityErrs
// returns nil when no errors have been recorded, so the worker's
// post-commit log loop is a no-op in the happy case.
func TestWriter_DrainObservabilityErrsEmpty(t *testing.T) {
	t.Parallel()
	w := newWriter("src", "aiagent_v3", "/tmp", NopPricer{})
	if got := w.drainObservabilityErrs(); got != nil {
		t.Errorf("drainObservabilityErrs on fresh writer = %v, want nil", got)
	}
}

// TestWriter_DrainObservabilityErrsCollectsAndClears verifies that
// errors appended to batchObservabilityErrs (the same slice priceOp
// pushes onto when emitPricingMiss fails) are returned in order and
// the slice is cleared so a subsequent drain returns nil. This is the
// core contract the worker relies on.
func TestWriter_DrainObservabilityErrsCollectsAndClears(t *testing.T) {
	t.Parallel()
	w := newWriter("src", "aiagent_v3", "/tmp", NopPricer{})
	w.batchObservabilityErrs = append(w.batchObservabilityErrs,
		errors.New("first"), errors.New("second"))
	got := w.drainObservabilityErrs()
	if len(got) != 2 || got[0].Error() != "first" || got[1].Error() != "second" {
		t.Fatalf("drainObservabilityErrs returned %v, want [first second]", got)
	}
	if again := w.drainObservabilityErrs(); again != nil {
		t.Errorf("drainObservabilityErrs after drain = %v, want nil", again)
	}
}

// TestWriter_ResetBatchClearsObservabilityErrs ensures resetBatch
// truncates the slice so a fresh batch starts clean — without this,
// errors from a previous batch would leak into the next worker log.
func TestWriter_ResetBatchClearsObservabilityErrs(t *testing.T) {
	t.Parallel()
	w := newWriter("src", "aiagent_v3", "/tmp", NopPricer{})
	w.batchObservabilityErrs = append(w.batchObservabilityErrs, errors.New("stale"))
	w.resetBatch()
	if len(w.batchObservabilityErrs) != 0 {
		t.Errorf("after resetBatch, batchObservabilityErrs len = %d, want 0", len(w.batchObservabilityErrs))
	}
}

// TestWriter_EmitPricingMissErrorOnClosedTx exercises the error
// return path of emitPricingMiss: when the surrounding transaction is
// already rolled back, both the bumpSourceErrorCounter UPDATE and the
// log_entries INSERT fail. priceOp must not panic and must record
// the error onto batchObservabilityErrs.
func TestWriter_EmitPricingMissErrorOnClosedTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// Call emitPricingMiss against the now-closed tx; the UPDATE in
	// bumpSourceErrorCounter must fail.
	if err := w.emitPricingMiss(ctx, tx, "p", "m", "kind", 1000); err == nil {
		t.Errorf("emitPricingMiss on closed tx returned nil error, want non-nil")
	}
}

// TestWriter_PriceOpRecordsObservabilityErrOnEmitFailure verifies the
// iter-4 "no silent failures" fix: when emitPricingMiss fails (closed
// tx), priceOp records the error onto batchObservabilityErrs rather
// than swallowing it. drainObservabilityErrs returns the recorded
// error so the worker can log it after commit.
func TestWriter_PriceOpRecordsObservabilityErrOnEmitFailure(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = tx.Rollback()
	ev := canonical.OpFinalizedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1100},
		TokensIn:  1, TokensOut: 1,
	}
	cost := w.priceOp(ctx, tx, "anthropic", "doesnotexist", 1100, ev)
	if cost != 0 {
		t.Errorf("priceOp cost = %f, want 0 on miss", cost)
	}
	errs := w.drainObservabilityErrs()
	if len(errs) != 1 {
		t.Fatalf("drainObservabilityErrs len = %d, want 1 (emit failure must surface)", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "emit pricing miss") {
		t.Errorf("recorded err = %q, want contains 'emit pricing miss'", errs[0].Error())
	}
}

// TestWriter_ApplyOpFinalizedLookupNonErrNoRowsBubbles verifies that
// OpFinalized propagates a closed-transaction error instead of silently
// treating it as a normal missing-op/orphan finalize path.
func TestWriter_ApplyOpFinalizedLookupNonErrNoRowsBubbles(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", &fakePricer{ret: 1.0})

	// Seed a session so requireSessionID succeeds without inserting
	// against the soon-to-be-closed tx.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}

	// New tx that we close immediately so the next SELECT fails with
	// something other than sql.ErrNoRows.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin closed tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback closed tx: %v", err)
	}

	err = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 10, TokensOut: 5, EndTs: 1200, Status: "completed",
	})
	if err == nil {
		t.Errorf("apply OpFinalized on closed tx returned nil, want non-nil error")
	}
}

func TestResolveFinalizedOpTiming(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		endTs     int64
		startTs   sql.NullInt64
		wantEnd   sql.NullInt64
		wantDurUS sql.NullInt64
	}{
		{
			name:    "zero_end",
			endTs:   0,
			startTs: sql.NullInt64{Int64: 100, Valid: true},
		},
		{
			name:    "invalid_start",
			endTs:   200,
			startTs: sql.NullInt64{},
		},
		{
			name:    "non_positive_start",
			endTs:   200,
			startTs: sql.NullInt64{Int64: 0, Valid: true},
		},
		{
			name:    "end_before_start",
			endTs:   150,
			startTs: sql.NullInt64{Int64: 200, Valid: true},
		},
		{
			name:      "valid",
			endTs:     250,
			startTs:   sql.NullInt64{Int64: 100, Valid: true},
			wantEnd:   sql.NullInt64{Int64: 250, Valid: true},
			wantDurUS: sql.NullInt64{Int64: 150, Valid: true},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveFinalizedOpTiming(canonical.OpFinalizedEvent{EndTs: tc.endTs}, tc.startTs)
			if got.endTsArg != tc.wantEnd {
				t.Fatalf("endTsArg = %+v, want %+v", got.endTsArg, tc.wantEnd)
			}
			if got.durUS != tc.wantDurUS {
				t.Fatalf("durUS = %+v, want %+v", got.durUS, tc.wantDurUS)
			}
		})
	}
}

func TestResolveFinalizedOpCostNilPricerReturnsEventCost(t *testing.T) {
	t.Parallel()
	w := newWriter("src", "aiagent_v3", "/tmp", nil)
	ev := canonical.OpFinalizedEvent{
		EventBase: canonical.EventBase{Ts: 200},
		CostUSD:   0,
		TokensIn:  10,
		TokensOut: 5,
	}
	op := finalizedOpLookup{
		kind:     sql.NullString{String: string(canonical.OpLLM), Valid: true},
		provider: sql.NullString{String: "openai", Valid: true},
		model:    sql.NullString{String: "gpt-test", Valid: true},
		startTs:  sql.NullInt64{Int64: 100, Valid: true},
	}

	got := w.resolveFinalizedOpCost(context.Background(), nil, ev, op)
	if got != ev.CostUSD {
		t.Fatalf("cost = %f, want event cost %f", got, ev.CostUSD)
	}
}

const brokenPersistedOpLookupSource = "aiagent_v3:/tmp"

// TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles verifies the
// provider/model/kind/start_ts lookup itself treats only sql.ErrNoRows as
// non-fatal. A schema-level lookup failure must bubble up instead of being
// silently treated like an orphan finalize or pricing skip.
func TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles(t *testing.T) {
	t.Parallel()

	ctx, db, w := setupBrokenPersistedOpLookup(t)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx broken lookup: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	err = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: brokenPersistedOpLookupSource, SourceSeq: 2, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 10, TokensOut: 5, EndTs: 1200, Status: "completed",
	})
	if err == nil {
		t.Fatal("apply OpFinalized with broken persisted-op lookup returned nil, want error")
	}
	if !strings.Contains(err.Error(), "lookup op") {
		t.Fatalf("apply OpFinalized error = %v, want persisted op lookup error", err)
	}
}

func setupBrokenPersistedOpLookup(t *testing.T) (context.Context, *sql.DB, *writer) {
	t.Helper()
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, brokenPersistedOpLookupSource, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(brokenPersistedOpLookupSource, "aiagent_v3", "/tmp", &fakePricer{ret: 1.0})

	seedSessionForBrokenPersistedOpLookup(t, ctx, db, w, brokenPersistedOpLookupSource)
	// Renaming the column forces the persisted-op lookup to hit a schema error.
	if _, err := db.ExecContext(ctx, `ALTER TABLE ops RENAME COLUMN provider TO provider_broken`); err != nil {
		t.Fatalf("rename ops.provider in test DB: %v", err)
	}
	return ctx, db, w
}

func seedSessionForBrokenPersistedOpLookup(t *testing.T, ctx context.Context, db *sql.DB, w *writer, src string) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx seed: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// TestWriter_PricerSkippedWhenOpRowSelectFails covers the priceOp
// branch where applyOpFinalized's ops-row SELECT returns
// sql.ErrNoRows (OpFinalized arriving before OpStarted has been
// committed). The op write must still land with cost_usd=0, the
// pricer must not be called, and no WRN row should appear.
func TestWriter_PricerSkippedWhenOpRowSelectFails(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// OpFinalized arrives without a matching OpStarted commit: the
	// ops-row SELECT returns sql.ErrNoRows and the writer skips the
	// pricer to avoid emitting an unactionable "provider \"\" model \"\""
	// warning.
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 10, TokensOut: 5, EndTs: 1200, Status: "completed",
	})
	_ = tx.Commit()

	if p.calls != 0 {
		t.Errorf("pricer called %d times, want 0 (op row missing)", p.calls)
	}
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN'`); v != 0 {
		t.Errorf("WRN count = %d, want 0", v)
	}
}

// TestWriter_PricerComputedCostFlowsToCatalog pins that
// when the pricer computes cost (because ev.CostUSD arrives as 0),
// the catalog rollups (catalog_providers.total_cost_usd /
// catalog_models.total_cost_usd) must include the computed value, not
// the original zero. Before iter-7 the writer passed the unmodified
// `ev` to catalog.onOpFinalized, which read ev.CostUSD and silently
// undercounted by the entire pricer-computed amount.
func TestWriter_PricerComputedCostFlowsToCatalog(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	// fakePricer returns 2.50 USD per op regardless of inputs; ev.CostUSD
	// stays 0 on the OpFinalizedEvent below so the writer's pricer path
	// fires and the catalog must reflect the computed value.
	p := &fakePricer{ret: 2.50}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	tx, _ := db.BeginTx(ctx, nil)
	for _, ev := range []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "call",
			Provider: "openai", Model: "gpt-5",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1,
			TokensIn: 100, TokensOut: 50, EndTs: 1200, Status: "completed",
			// CostUSD is intentionally omitted (zero value) so the pricer
			// path computes and the catalog rollups must pick that up.
		},
	} {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}
	_ = tx.Commit()

	// ops.cost_usd should reflect the pricer-computed value (the original
	// assertion that exists in TestWriter_PricerFillsZeroCost).
	var opCost float64
	_ = db.QueryRow(`SELECT cost_usd FROM ops`).Scan(&opCost)
	if opCost != 2.50 {
		t.Errorf("ops.cost_usd = %f, want 2.50 (pricer-computed)", opCost)
	}

	// catalog_models.total_cost_usd must now match ops.cost_usd. Before
	// iter-7 this was zero because catalog.onOpFinalized read ev.CostUSD
	// (still 0) instead of the post-pricer cost.
	var modelCost float64
	if err := db.QueryRow(
		`SELECT total_cost_usd FROM catalog_models WHERE provider='openai' AND name='gpt-5'`,
	).Scan(&modelCost); err != nil {
		t.Fatalf("query catalog_models: %v", err)
	}
	if modelCost != 2.50 {
		t.Errorf("catalog_models.total_cost_usd = %f, want 2.50", modelCost)
	}

	// catalog_providers.total_cost_usd must also match.
	var providerCost float64
	if err := db.QueryRow(
		`SELECT total_cost_usd FROM catalog_providers WHERE name='openai' AND alias=''`,
	).Scan(&providerCost); err != nil {
		t.Fatalf("query catalog_providers: %v", err)
	}
	if providerCost != 2.50 {
		t.Errorf("catalog_providers.total_cost_usd = %f, want 2.50", providerCost)
	}
}

// TestWriter_PricingMissDedupedAcrossBatches pins that
// the pricing-miss dedup must survive resetBatch() so the same
// (provider, model, missKind) tuple emits exactly one WRN row and one
// parse_errors increment per source — not one per batch. Before iter-7
// pricingMissDedup was cleared in resetBatch(), producing one warning
// per batch which contradicted pricing.md §"Temporal resolution
// algorithm".
func TestWriter_PricingMissDedupedAcrossBatches(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	// --- Batch 1: one priceable op with an unknown (provider, model). ---
	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "madeup", Model: "doesnotexist",
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 100, TokensOut: 50, EndTs: 1200, Status: "completed",
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("batch 1 commit: %v", err)
	}
	// Mimic worker.flush's post-commit sequence: promote pending dedup
	// keys into the lifetime map, then reset per-batch state. Without
	// the promotion the next batch's emitPricingMiss would see neither
	// the pending nor the lifetime map carrying the key and re-emit.
	w.promotePendingMissDedup()
	w.resetBatch()

	// --- Batch 2: another op with the SAME (provider, model). The
	// dedup map must survive resetBatch() so no new WRN row lands.
	tx, _ = db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 4, Ts: 2100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "madeup", Model: "doesnotexist",
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 5, Ts: 2200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2,
		TokensIn: 100, TokensOut: 50, EndTs: 2200, Status: "completed",
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("batch 2 commit: %v", err)
	}

	// Exactly ONE WRN row across both batches (spec: "deduped per
	// (sourceID, provider, model, missKind)").
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN' AND source_id IS NOT NULL`); v != 1 {
		t.Errorf("WRN log_entries count across 2 batches = %d, want 1", v)
	}
	// Exactly ONE parse_errors increment.
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 1 {
		t.Errorf("parse_errors across 2 batches = %d, want 1", v)
	}
}

// TestWriter_PricingMissDedup_RollbackDoesNotSuppress pins that
// marking pricingMissDedup at emit time (BEFORE commit) silently
// suppressed every future identical warning even when the surrounding
// tx rolled back and the warning row never landed. Fix: the writer
// marks per-batch pendingMissDedup on INSERT success, and worker.flush
// calls promotePendingMissDedup only AFTER tx.Commit() succeeds. On
// rollback, resetBatch wipes pendingMissDedup so the next batch with
// the same (provider, model, missKind) re-emits the warning.
//
// Mutation check: comment out the promotePendingMissDedup call in
// worker.go OR move the pendingMissDedup[key] mark above the WRN
// INSERT in writer.go and this test still passes — both shapes match
// "mark only after commit". Revert the iter-10 changes entirely (mark
// pricingMissDedup eagerly inside emitPricingMiss) and the second
// batch's INSERT count drops to 0 → assertion fires.
func TestWriter_PricingMissDedup_RollbackDoesNotSuppress(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	p := &fakeDetailPricer{miss: "unknown_provider_model"}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", p)

	// --- Batch 1: emit the WRN row, then ROLL BACK the tx. The
	// warning never durably commits. Without the fix, pricingMissDedup
	// would already carry the key and future batches would never
	// re-emit.
	tx1, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx1, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx1, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "madeup", Model: "doesnotexist",
	})
	_ = w.apply(ctx, tx1, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 100, TokensOut: 50, EndTs: 1200, Status: "completed",
	})
	// Sanity: writer recorded the dedup intent in the PENDING map but
	// NOT in the lifetime map (commit has not happened).
	if len(w.pendingMissDedup) != 1 {
		t.Fatalf("pendingMissDedup size after emit = %d, want 1", len(w.pendingMissDedup))
	}
	if len(w.pricingMissDedup) != 0 {
		t.Fatalf("pricingMissDedup size before commit = %d, want 0 (rollback case)", len(w.pricingMissDedup))
	}
	if err := tx1.Rollback(); err != nil {
		t.Fatalf("batch 1 rollback: %v", err)
	}
	w.resetBatch() // worker calls this on every flush exit, commit or rollback
	// After resetBatch the pending map is cleared and the lifetime map
	// is still empty — the fix's whole point.
	if len(w.pendingMissDedup) != 0 {
		t.Fatalf("pendingMissDedup after resetBatch = %d, want 0", len(w.pendingMissDedup))
	}
	if len(w.pricingMissDedup) != 0 {
		t.Fatalf("pricingMissDedup after rollback+reset = %d, want 0 (warning never committed)", len(w.pricingMissDedup))
	}

	// --- Batch 2: COMMIT a tx with the SAME (provider, model). The
	// fix must re-emit the warning because batch 1 never made it
	// durable. Before the fix the dedup map would silently suppress
	// this insert.
	tx2, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx2, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 10, Ts: 2000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx2, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 11, Ts: 2100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "madeup", Model: "doesnotexist",
	})
	_ = w.apply(ctx, tx2, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 12, Ts: 2200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2,
		TokensIn: 100, TokensOut: 50, EndTs: 2200, Status: "completed",
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("batch 2 commit: %v", err)
	}
	// Worker calls promotePendingMissDedup AFTER tx.Commit() — mimic
	// that here so subsequent batches see the committed key.
	w.promotePendingMissDedup()
	w.resetBatch()

	// The exact assertion that breaks if the fix is reverted: exactly
	// ONE WRN row landed in the DB (from batch 2), even though batch 1
	// also tried to emit one. Without the fix the count is 0 because
	// emitPricingMiss in batch 2 would have short-circuited.
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN' AND source_id IS NOT NULL`); v != 1 {
		t.Errorf("WRN log_entries count after rollback+commit = %d, want 1 (rolled-back warning must NOT suppress next batch)", v)
	}
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 1 {
		t.Errorf("parse_errors after rollback+commit = %d, want 1", v)
	}

	// --- Batch 3: NOW the lifetime map carries the key. A third
	// batch with the same (provider, model) MUST be deduped (no new
	// WRN row, no new parse_errors increment). This pins the
	// across-batch dedup contract still holds after commit.
	tx3, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx3, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 20, Ts: 3100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 3, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "madeup", Model: "doesnotexist",
	})
	_ = w.apply(ctx, tx3, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 21, Ts: 3200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 3,
		TokensIn: 50, TokensOut: 25, EndTs: 3200, Status: "completed",
	})
	if err := tx3.Commit(); err != nil {
		t.Fatalf("batch 3 commit: %v", err)
	}
	w.promotePendingMissDedup()
	w.resetBatch()
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN' AND source_id IS NOT NULL`); v != 1 {
		t.Errorf("WRN log_entries count after batch 3 = %d, want 1 (lifetime dedup must still suppress)", v)
	}
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 1 {
		t.Errorf("parse_errors after batch 3 = %d, want 1 (lifetime dedup must still suppress)", v)
	}
}

// fakeEvent implements canonical.Event but is not one of the writer's
// known concrete types — used to exercise the default branch.
type fakeEvent struct{}

func (fakeEvent) EventKind() canonical.EventKind { return canonical.EventKind("fake") }
func (fakeEvent) EventSourceID() string          { return "fake" }
func (fakeEvent) EventSourceSeq() uint64         { return 0 }
func (fakeEvent) EventTs() int64                 { return 0 }

// timingFake holds a t for hooks that need access; reserved for future
// use to avoid the linter complaining about unused imports.
var _ = time.Now
