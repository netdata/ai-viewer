package ingest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the op-duration invariant (SOW-0026): ops.duration_us is
// derived from the op's PERSISTED start_ts (the matching OpStarted's Ts), NOT
// from the OpFinalized event's own Ts. Per the canonical contract (events.go,
// EventBase.Ts: "the ingester orders events by Ts within a session"), a
// finalize event sorts AFTER its OpStarted, so OpFinalizedEvent.Ts ≈ the op
// END — identical to OpFinalizedEvent.EndTs. Computing duration as EndTs-Ts
// therefore yields ≈0 for every spec-conformant adapter. The correct formula
// is EndTs - start_ts, which data-model.md §ops.duration_us already mandates.

// scanNullInt64 returns the (value, valid) of a nullable INTEGER column from a
// one-row SELECT, so a test can distinguish 0 from SQL NULL.
func scanNullInt64(t *testing.T, db *sql.DB, query string, args ...any) sql.NullInt64 {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	return v
}

// TestApplyOpFinalized_DurationFromPersistedStart pins the core fix: an op
// started at S and finalized with a spec-conformant Finalized.Ts == EndTs == E
// (the END, S<E) must record ops.duration_us == E-S, and the catalog model
// rollup must accumulate the same. On the BUGGY code duration_us is EndTs-Ts
// == E-E == 0, so this test FAILS until the writer derives duration from the
// persisted start_ts.
func TestApplyOpFinalized_DurationFromPersistedStart(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"
	const startTs = int64(1000)
	const endTs = int64(1300)
	const wantDur = endTs - startTs // 300

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
	apply := func(ev canonical.Event) {
		if aErr := w.apply(ctx, tx, ev); aErr != nil {
			t.Fatalf("apply %T: %v", ev, aErr)
		}
	}
	apply(canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: startTs},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: startTs},
		SessionNativeID: "s", Seq: 1,
	})
	// OpStarted carries the op START as its Ts (persisted to ops.start_ts).
	apply(canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: startTs},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "message", Provider: "openai", Model: "gpt-5.5",
	})
	// Spec-conformant finalize: its OWN Ts is the END (== EndTs), as every
	// real adapter emits. The buggy formula EndTs-Ts collapses to 0 here.
	apply(canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: endTs},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: endTs,
		TokensIn: 100, TokensOut: 20,
	})
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "s"), 1), 1)
	gotDur := scanNullInt64(t, db, `SELECT duration_us FROM ops WHERE id = ?`, opID)
	if !gotDur.Valid {
		t.Fatalf("ops.duration_us is NULL, want %d (must derive from persisted start_ts)", wantDur)
	}
	if gotDur.Int64 != wantDur {
		t.Fatalf("ops.duration_us = %d, want %d (EndTs-start_ts, NOT EndTs-finalize.Ts)", gotDur.Int64, wantDur)
	}
	if got := scanInt(t, db, `SELECT total_duration_us FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != wantDur {
		t.Fatalf("catalog_models.total_duration_us = %d, want %d", got, wantDur)
	}
}

// TestApplyOpFinalized_OrphanFinalizeNoDuration pins the orphan case: an
// OpFinalized whose matching OpStarted never landed (no ops row, hence no
// recorded start_ts) must NOT fabricate a duration. The writer's
// UPDATE ... WHERE id=? matches zero rows (no op exists), so nothing is
// written and no phantom duration appears. We assert the op row is absent —
// the finalize is a no-op for the ops table (catalog.go's ErrNoRows skip).
func TestApplyOpFinalized_OrphanFinalizeNoDuration(t *testing.T) {
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
	apply := func(ev canonical.Event) {
		if aErr := w.apply(ctx, tx, ev); aErr != nil {
			t.Fatalf("apply %T: %v", ev, aErr)
		}
	}
	apply(canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// No OpStarted for (turn 1, seq 1). The finalize arrives orphaned.
	apply(canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1300},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: 1300,
	})
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "s"), 1), 1)
	// No OpStarted ⇒ no ops row ⇒ no fabricated duration anywhere.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE id = ?`, opID); got != 0 {
		t.Fatalf("orphan finalize created an ops row (count=%d); a finalize without start_ts must not fabricate one", got)
	}
}

// TestApplyOpFinalized_ReFinalizeNullEndPreservesDuration pins the COALESCE
// contract: once a real duration is recorded, a corrective re-finalize that
// carries EndTs=0 (NULL end) must NOT zero the duration — it preserves the
// prior value. This guards the duration_us = COALESCE(?, duration_us) UPDATE
// and the catalog delta-zero invariant under the new start_ts-based formula.
func TestApplyOpFinalized_ReFinalizeNullEndPreservesDuration(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"
	const startTs = int64(2000)
	const endTs = int64(2500)
	const wantDur = endTs - startTs // 500

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "codex", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "codex", "/tmp", NopPricer{})
	apply := func(tx *sql.Tx, ev canonical.Event) {
		if aErr := w.apply(ctx, tx, ev); aErr != nil {
			t.Fatalf("apply %T: %v", ev, aErr)
		}
	}

	tx1, _ := db.BeginTx(ctx, nil)
	apply(tx1, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: startTs},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(tx1, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: startTs},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "shell", ToolNamespace: "shell",
	})
	apply(tx1, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: endTs},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: endTs, TokensIn: 5,
	})
	if err := tx1.Commit(); err != nil {
		t.Fatalf("Commit batch 1: %v", err)
	}

	opID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "s"), 1), 1)
	if got := scanNullInt64(t, db, `SELECT duration_us FROM ops WHERE id = ?`, opID); !got.Valid || got.Int64 != wantDur {
		t.Fatalf("after first finalize duration_us = %+v, want valid %d", got, wantDur)
	}

	// Re-finalize with EndTs=0 (NULL end). duration_us = COALESCE(NULL, prior)
	// must keep wantDur; catalog total_duration_us must not move.
	tx2, _ := db.BeginTx(ctx, nil)
	apply(tx2, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: endTs},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: 0, TokensIn: 5,
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit batch 2: %v", err)
	}
	if got := scanNullInt64(t, db, `SELECT duration_us FROM ops WHERE id = ?`, opID); !got.Valid || got.Int64 != wantDur {
		t.Fatalf("after NULL-end re-finalize duration_us = %+v, want preserved %d", got, wantDur)
	}
	if got := scanInt(t, db, `SELECT total_duration_us FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != wantDur {
		t.Fatalf("catalog_tools.total_duration_us = %d, want preserved %d (NULL-end re-finalize must not move it)", got, wantDur)
	}
}
