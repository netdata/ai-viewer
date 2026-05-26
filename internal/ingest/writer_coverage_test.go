package ingest

import (
	"context"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestWriter_ApplySessionStartedEmptyNativeIDErrors(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "NativeID") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriter_TurnStartedRequiresSession(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})
	tx, _ := db.BeginTx(ctx, nil)
	err := w.apply(ctx, tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1},
		SessionNativeID: "missing", Seq: 1,
	})
	if err != nil {
		t.Fatalf("apply turn started with missing session: %v", err)
	}
	_ = tx.Commit()
	// Auto-stub was inserted.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='missing'`); got != 1 {
		t.Errorf("auto-stub session count = %d, want 1", got)
	}
}

func TestWriter_OpStartedAutoStubsTurn(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// Op with no preceding TurnStartedEvent — writer must synthesize.
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 7, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
	})
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM turns WHERE seq=7`); got != 1 {
		t.Errorf("turn auto-stub count = %d, want 1", got)
	}
}

func TestWriter_OpStartedWithParentOpSeq(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "outer",
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2, ParentOpSeq: 1,
		Kind: canonical.OpLLM, Name: "inner",
	})
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE parent_op_id IS NOT NULL`); got != 1 {
		t.Errorf("inner op parent link missing")
	}
}

func TestWriter_LogEntryOpScoped(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// Need an op for op_id FK.
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1050},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
	})
	_ = w.apply(ctx, tx, canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, OpSeq: 1,
		Severity: "INF", Source: "x", Message: "y",
	})
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE op_id IS NOT NULL`); got != 1 {
		t.Errorf("op-scoped log not written")
	}
}

func TestWriter_PayloadRefWithoutHash(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

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
	_ = w.apply(ctx, tx, canonical.PayloadRefEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, OpSeq: 1,
		PayloadKind: "log", Format: "text",
		LocationURI: "file:///tmp/log.txt",
	})
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`); got != 1 {
		t.Errorf("payload count = %d, want 1", got)
	}
	if got := scanString(t, db, `SELECT IFNULL(sha256,'') FROM payload_refs`); got != "" {
		t.Errorf("sha256 = %q, want empty", got)
	}
}

func TestWriter_SessionFinalizedDefaultsStatus(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.SessionFinalizedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 2000},
		NativeID:  "s",
		// Status unset
		EndTs: 2000,
	})
	_ = tx.Commit()
	if got := scanString(t, db, `SELECT status FROM sessions`); got != "completed" {
		t.Errorf("status default = %q, want completed", got)
	}
}

func TestWriter_TurnFinalizedDefaultStatus(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.TurnFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", Seq: 1, EndTs: 1100,
	})
	_ = tx.Commit()
	if got := scanString(t, db, `SELECT status FROM turns WHERE seq=1`); got != "completed" {
		t.Errorf("status default = %q, want completed", got)
	}
}

func TestNonEmpty_FallbackBranch(t *testing.T) {
	t.Parallel()
	if got := nonEmpty("", "fb"); got != "fb" {
		t.Errorf("nonEmpty empty: got %q want fb", got)
	}
	if got := nonEmpty("x", "fb"); got != "x" {
		t.Errorf("nonEmpty 'x': got %q want x", got)
	}
}

func TestNullIfZero(t *testing.T) {
	t.Parallel()
	if v := nullIfZero(0); v != nil {
		t.Errorf("nullIfZero(0) = %v", v)
	}
	if v := nullIfZero(7); v != int64(7) {
		t.Errorf("nullIfZero(7) = %v", v)
	}
}

func TestNullIfEmpty(t *testing.T) {
	t.Parallel()
	if v := nullIfEmpty(""); v != nil {
		t.Errorf("nullIfEmpty('') = %v", v)
	}
	if v := nullIfEmpty("x"); v != "x" {
		t.Errorf("nullIfEmpty('x') = %v", v)
	}
}

func TestCanonicalIDs_Stable(t *testing.T) {
	t.Parallel()
	a := canonicalSessionID("src", "nid")
	b := canonicalSessionID("src", "nid")
	if a != b {
		t.Error("canonicalSessionID not stable")
	}
	c := canonicalSessionID("src", "nid2")
	if a == c {
		t.Error("canonicalSessionID collision on different native id")
	}
	t1 := canonicalTurnID(a, 1)
	t2 := canonicalTurnID(a, 2)
	if t1 == t2 {
		t.Error("canonicalTurnID collision on different seq")
	}
	op := canonicalOpID(t1, 0)
	if op == "" {
		t.Error("canonicalOpID returned empty")
	}
}

func TestMergeExtras_ScalarOverwrite(t *testing.T) {
	t.Parallel()
	a := map[string]any{"k": "old"}
	b := map[string]any{"k": "new"}
	got := mergeExtras(a, b)
	if got["k"] != "new" {
		t.Errorf("scalar overwrite failed: %v", got)
	}
}
