package ingest

import (
	"context"
	"database/sql"
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
	if err := ensureSourceRow(ctx, tx, id, format, loc); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
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
	if v := p.Cost("a", "b", 1, 2, 3, 4); v != 0 {
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

// fakePricer counts invocations and returns a fixed value.
type fakePricer struct {
	calls int
	ret   float64
}

func (p *fakePricer) Cost(provider, model string, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64 {
	p.calls++
	return p.ret
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
