package ingest

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// SOW-0055 characterization shared helpers — setup, event builders, utilities.
// Used by the per-area characterization test files.

// ----- shared setup helpers -----

func sow55SetupWriter(t *testing.T, src, format, loc string) (*writer, *sql.DB, context.Context) {
	t.Helper()
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, format, loc); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, format, loc, NopPricer{})
	return w, db, ctx
}

func sow55Begin(t *testing.T, ctx context.Context, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	return tx
}

func sow55ApplyEvent(t *testing.T, ctx context.Context, w *writer, tx *sql.Tx, ev canonical.Event) {
	t.Helper()
	if err := w.apply(ctx, tx, ev); err != nil {
		t.Fatalf("apply %T: %v", ev, err)
	}
}

func sow55ApplyBatch(t *testing.T, ctx context.Context, w *writer, tx *sql.Tx, evs []canonical.Event) {
	t.Helper()
	for _, ev := range evs {
		sow55ApplyEvent(t, ctx, w, tx, ev)
	}
}

func sow55CommitTx(t *testing.T, tx *sql.Tx) {
	t.Helper()
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// ----- event builders -----

func sow55ToolCascadeEvents(src string) []canonical.Event {
	return []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
			SessionNativeID: "s", Seq: 1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpTool, Name: "search", ToolNamespace: "custom",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "boom",
			EndTs: 1500, TokensIn: 7, TokensOut: 11, CostUSD: 0.5,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpTool, Name: "files.read", ToolNamespace: "mcp:files",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "boom",
			EndTs: 1500, TokensIn: 7, TokensOut: 11, CostUSD: 0.5,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 7, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpTool, Name: "files.read_v2", ToolNamespace: "mcp:files-v2",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 8, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "boom",
			EndTs: 1500, TokensIn: 7, TokensOut: 11, CostUSD: 0.5,
		},
	}
}

func sow55LLMCascadeEvents(src string) []canonical.Event {
	return []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
			SessionNativeID: "s", Seq: 1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Provider: "anthropic", Model: "claude-opus-4.7",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 2000},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed",
			EndTs: 2000, TokensIn: 12, TokensOut: 34, CostUSD: 1.25,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Provider: "openai", Model: "gpt-5.5",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 2000},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed",
			EndTs: 2000, TokensIn: 12, TokensOut: 34, CostUSD: 1.25,
		},
	}
}

func sow55NilExtrasPhase1Events(src string) []canonical.Event {
	return []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:  "parent", RootNativeID: "parent", Kind: canonical.KindRoot,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
			SessionNativeID: "parent", Seq: 1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpSession, Name: "explore",
			Extras: map[string]any{
				"attr.x":   "should-be-dropped",
				"aiViewer": map[string]any{"toolUseId": "toolu_abc", "childNativeId": "parent:agent:xyz"},
			},
		},
	}
}

func sow55ReEmitRollupEvents(src string) []canonical.Event {
	return []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
			SessionNativeID: "s", Seq: 1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 3_600_000_005},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Provider: "openai", Model: "gpt-5.5",
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 3_600_000_005},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Provider: "openai", Model: "gpt-5.5",
		},
	}
}

func sow55LogBatch(src string, seqBase uint64, ts int64, message string) []canonical.Event {
	return []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: seqBase, Ts: 500},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.LogEntryEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: seqBase + 1, Ts: ts},
			SessionNativeID: "s",
			Severity:        "INF", Source: "agent", Message: message,
		},
	}
}

// ----- utility functions -----

// scanFloat returns a single float64 from a one-row SELECT.
func scanFloat(t *testing.T, db *sql.DB, query string, args ...any) float64 {
	t.Helper()
	var v float64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0
		}
		t.Fatalf("query %s: %v", query, err)
	}
	return v
}

// floatNear reports whether a and b agree to 6 decimal places.
func floatNear(a, b float64) bool {
	const eps = 1e-6
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
