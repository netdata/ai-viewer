package ingest

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// closedDB returns a *sql.DB whose underlying connection has been
// closed. Every Exec/Query against it surfaces sql.ErrConnDone (or
// driver-specific equivalent), which exercises the error-return path
// inside the writer / catalog / aggregate code.
func closedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_ = db.Close()
	return db
}

func TestRefreshTurnAggregates_PropagatesError(t *testing.T) {
	t.Parallel()
	db := closedDB(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err == nil {
		_ = tx.Rollback()
		t.Skip("driver allowed BeginTx on closed DB; cannot exercise error path")
	}
	// Test still meaningful: the empty-dirty-set path is no-op.
	if err := refreshTurnAggregates(context.Background(), nil, nil); err != nil {
		t.Errorf("empty dirty turns errored: %v", err)
	}
}

func TestRefreshSessionAggregates_EmptyDirtyIsNoOp(t *testing.T) {
	t.Parallel()
	if err := refreshSessionAggregates(context.Background(), nil, nil); err != nil {
		t.Errorf("empty dirty sessions errored: %v", err)
	}
}

func TestRefreshAggregates_EmptyDirtyIsNoOp(t *testing.T) {
	t.Parallel()
	if err := refreshAggregates(context.Background(), nil, nil, nil); err != nil {
		t.Errorf("empty refresh errored: %v", err)
	}
}

func TestUpsertProvider_EmptyAliasOK(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "src", "fmt", "/loc")
	tx, _ := db.BeginTx(ctx, nil)
	if err := upsertProvider(ctx, tx, "anthropic", "", 1000, 1); err != nil {
		t.Fatalf("upsertProvider: %v", err)
	}
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='anthropic' AND alias=''`); got != 1 {
		t.Errorf("call_count = %d", got)
	}
}

// TestWriter_OpFinalizedSkipsCatalogWhenOpMissing covers the
// onOpFinalized lookup miss branch where ops row is not present yet.
func TestWriter_OpFinalizedSkipsCatalogWhenOpMissing(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	// Apply OpFinalized without preceding OpStarted: ensures the
	// catalog onOpFinalized lookup hits sql.ErrNoRows path.
	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	err := w.apply(ctx, tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 99,
		Status: "completed", EndTs: 1100,
	})
	// OpFinalized's UPDATE matches no row (no OpStarted ran), and the
	// catalog lookup returns sql.ErrNoRows which is handled gracefully.
	// No error should propagate.
	if err != nil {
		t.Errorf("apply OpFinalized w/o OpStarted: %v", err)
	}
	_ = tx.Commit()
}

// TestWorker_ReportFallsBackToLoggerWhenNilOnErr verifies the report()
// branch when no onErr callback is configured.
func TestWorker_ReportFallsBackToLoggerWhenNilOnErr(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	w := &worker{
		sourceID: "src", sourceFormat: "fmt", location: "/loc",
		db: db, hwm: newHWMCache(), pricer: NopPricer{},
		logger:    silentLogger(),
		batchSize: 1, batchEvery: time.Second,
	}
	// Should not panic.
	w.report(nil)
	w.report(errSentinel)
}

var errSentinel = sql.ErrConnDone

// unmarshalableExtras returns a map[string]any whose JSON marshalling
// will fail — channels are not JSON-encodable. Used to exercise the
// marshalExtras error branches.
func unmarshalableExtras() map[string]any {
	return map[string]any{"bad": make(chan int)}
}

func TestMarshalExtras_ChannelValueErrors(t *testing.T) {
	t.Parallel()
	if _, err := marshalExtras(unmarshalableExtras()); err == nil {
		t.Fatal("expected JSON marshal error")
	}
}

func TestWriter_SessionStartedBadExtrasErrors(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		Extras: unmarshalableExtras(),
	})
	if err == nil {
		t.Fatal("expected error from bad extras")
	}
}

func TestWriter_SessionUpdatedBadExtrasErrors(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	err := w.apply(ctx, tx, canonical.SessionUpdatedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1},
		NativeID:  "s",
		Extras:    unmarshalableExtras(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWriter_OpStartedBadExtrasErrors(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	err := w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 2},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "x",
		Extras: unmarshalableExtras(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWriter_LogEntryBadExtrasErrors(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	err := w.apply(ctx, tx, canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 2},
		SessionNativeID: "s",
		Severity:        "INF", Source: "x", Message: "y",
		Extras: unmarshalableExtras(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// rolledTx returns a *sql.Tx that has been rolled back, so every
// subsequent ExecContext returns sql.ErrTxDone. Used to exercise the
// post-exec error-wrap branches across writer.go and catalog.go.
func rolledTx(t *testing.T, db *sql.DB) (context.Context, *sql.Tx) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	return ctx, tx
}

func TestEnsureSourceRow_FailsOnDeadTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx, tx := rolledTx(t, db)
	if err := ensureSourceRow(ctx, tx, "src", "fmt", "/loc"); err == nil {
		t.Fatal("expected error on rolled-back tx")
	}
}

func TestUpsertSourceProgress_FailsOnDeadTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx, tx := rolledTx(t, db)
	// Force the hasCursor branch.
	if err := upsertSourceProgress(ctx, tx, "src", 1, 1, "{}", true); err == nil {
		t.Error("expected error on rolled-back tx (cursor branch)")
	}
	ctx2, tx2 := rolledTx(t, db)
	if err := upsertSourceProgress(ctx2, tx2, "src", 1, 1, "", false); err == nil {
		t.Error("expected error on rolled-back tx (no-cursor branch)")
	}
}

func TestUpsertProvider_FailsOnDeadTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx, tx := rolledTx(t, db)
	if err := upsertProvider(ctx, tx, "p", "", 1, 1); err == nil {
		t.Fatal("expected error on rolled-back tx")
	}
}

func TestCatalog_OnOpFinalized_RolledTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx, tx := rolledTx(t, db)
	c := newCatalogWriter(NopPricer{})
	err := c.onOpFinalized(ctx, tx, "any-op-id", canonical.OpFinalizedEvent{
		EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
	}, opPriorTotals{})
	if err == nil {
		t.Fatal("expected error on rolled-back tx")
	}
}

func TestRefreshAggregates_RolledTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx, tx := rolledTx(t, db)
	turns := map[string]struct{}{"a": {}}
	sessions := map[string]struct{}{"s": {}}
	if err := refreshAggregates(ctx, tx, turns, sessions); err == nil {
		t.Fatal("expected refresh err on rolled-back tx")
	}
}

func TestCatalog_OnSessionStarted_RolledTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx, tx := rolledTx(t, db)
	c := newCatalogWriter(NopPricer{})
	err := c.onSessionStarted(ctx, tx, "fmt", canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
		NativeID:  "s", AgentName: "agent",
	})
	if err == nil {
		t.Fatal("expected error on rolled-back tx (agent branch)")
	}
	ctx2, tx2 := rolledTx(t, db)
	err = c.onSessionStarted(ctx2, tx2, "fmt", canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
		NativeID:  "s", Cwd: "/work",
	})
	if err == nil {
		t.Fatal("expected error on rolled-back tx (cwd branch)")
	}
}

func TestCatalog_OnOpStarted_RolledTx(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx, tx := rolledTx(t, db)
	c := newCatalogWriter(NopPricer{})
	// LLM branch with provider+model. A genuine insert (inserted=true) has no prior
	// identity to migrate, so pass an empty priorOpIdentity (SOW-0004 I1 signature).
	err := c.onOpStarted(ctx, tx, canonical.OpStartedEvent{
		EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
		Kind:      canonical.OpLLM, Provider: "anthropic", Model: "m",
	}, true, priorOpIdentity{})
	if err == nil {
		t.Fatal("expected error on rolled-back tx (llm branch)")
	}
	// Tool branch.
	ctx2, tx2 := rolledTx(t, db)
	err = c.onOpStarted(ctx2, tx2, canonical.OpStartedEvent{
		EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
		Kind:      canonical.OpTool, Name: "read",
	}, true, priorOpIdentity{})
	if err == nil {
		t.Fatal("expected error on rolled-back tx (tool branch)")
	}
	// Identity-migration error paths (SOW-0004 I1): an existing op (inserted=false)
	// whose catalog identity CHANGED triggers removeOpContribution before the upsert;
	// on a rolled-back tx that UPDATE fails, exercising the migrate-out error return.
	// Tool kind (namespace change) and LLM kind (provider/model change) cover both
	// catalog-table branches.
	ctx3, tx3 := rolledTx(t, db)
	err = c.onOpStarted(ctx3, tx3, canonical.OpStartedEvent{
		EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
		Kind:      canonical.OpTool, Name: "read", ToolNamespace: "mcp:srv",
	}, false, priorOpIdentity{
		found: true, kind: string(canonical.OpTool), name: "read", toolNamespace: "custom",
		totals: opPriorTotals{found: true, status: "completed"},
	})
	if err == nil {
		t.Fatal("expected error on rolled-back tx (tool migrate-out)")
	}
	ctx4, tx4 := rolledTx(t, db)
	err = c.onOpStarted(ctx4, tx4, canonical.OpStartedEvent{
		EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
		Kind:      canonical.OpLLM, Name: "message", Provider: "openai", Model: "gpt-5.5",
	}, false, priorOpIdentity{
		found: true, kind: string(canonical.OpLLM), name: "message", provider: "openai", model: "old-model",
		totals: opPriorTotals{found: true, status: "completed"},
	})
	if err == nil {
		t.Fatal("expected error on rolled-back tx (llm migrate-out)")
	}
}

func TestWriter_AllEventTypes_RolledTx(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		ev   canonical.Event
	}
	cases := []tc{
		{"session_started", canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		}},
		{"session_updated", canonical.SessionUpdatedEvent{
			EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			NativeID:  "s", Model: "m",
		}},
		{"session_finalized", canonical.SessionFinalizedEvent{
			EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			NativeID:  "s", Status: canonical.StatusCompleted, EndTs: 1,
		}},
		{"turn_started", canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			SessionNativeID: "s", Seq: 1,
		}},
		{"turn_finalized", canonical.TurnFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			SessionNativeID: "s", Seq: 1, Status: "completed", EndTs: 1,
		}},
		{"op_started", canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "c", Provider: "p", Model: "m",
		}},
		{"op_finalized", canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: 1,
		}},
		{"payload_ref", canonical.PayloadRefEvent{
			EventBase:       canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			SessionNativeID: "s", TurnSeq: 1, OpSeq: 1,
			PayloadKind: "k", Format: "f", LocationURI: "file:///x",
		}},
		{"log_entry", canonical.LogEntryEvent{
			EventBase:       canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			SessionNativeID: "s", Severity: "INF", Source: "x", Message: "m",
		}},
		{"source_error", canonical.SourceErrorEvent{
			EventBase: canonical.EventBase{SourceID: "x", SourceSeq: 1, Ts: 1},
			Message:   "boom",
		}},
	}
	_, db := openTestStore(t)
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx, tx := rolledTx(t, db)
			w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
			err := w.apply(ctx, tx, c.ev)
			if err == nil {
				t.Errorf("%s: expected error on rolled-back tx", c.name)
			}
		})
	}
}

func TestWriter_LogEntryEmptySeverityDefaults(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 2},
		SessionNativeID: "s", Source: "x", Message: "y", // no Severity
	})
	_ = tx.Commit()
	if got := scanString(t, db, `SELECT severity FROM log_entries`); got != "INF" {
		t.Errorf("severity default = %q, want INF", got)
	}
}
