package ingest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the catalog-idempotency-under-op-re-emission invariant
// (SOW-0004 H1a / superseded SOW-0020). The codex and claude_code adapters
// RE-EMIT OpStarted/OpFinalized for the same (turn,seq): a replay-from-0 on
// resume, plus late enrichment that carries corrected status/extras onto an
// already-finalized op. The catalog rollups (catalog_providers / catalog_models
// / catalog_tools) ACCUMULATE call_count, failure_count, total_tokens_*,
// total_cost_usd and total_duration_us, so a naive re-emit would double-count.
// These tests prove a re-emit leaves every aggregate exactly where a single
// emission left it, and that a status correction moves the failure total by
// exactly one.

// catalogTotals snapshots every accumulating column the op-rollups touch for one
// (provider, model)+(provider) LLM op and one tool op, so a re-emit can be
// asserted against a single-emission baseline.
type catalogTotals struct {
	providerCalls, providerFailures            int64
	providerTokensIn, providerTokensOut        int64
	providerCacheRead, providerCacheWrite      int64
	modelCalls, modelFailures, modelDurationUS int64
	modelTokensIn, modelTokensOut              int64
	toolCalls, toolFailures, toolTokensIn      int64
	toolDurationUS                             int64
}

func readCatalogTotals(t *testing.T, db *sql.DB, provider, model, toolNS, toolName string) catalogTotals {
	t.Helper()
	var c catalogTotals
	c.providerCalls = scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name=?`, provider)
	c.providerFailures = scanInt(t, db, `SELECT failure_count FROM catalog_providers WHERE name=?`, provider)
	c.providerTokensIn = scanInt(t, db, `SELECT total_tokens_in FROM catalog_providers WHERE name=?`, provider)
	c.providerTokensOut = scanInt(t, db, `SELECT total_tokens_out FROM catalog_providers WHERE name=?`, provider)
	c.providerCacheRead = scanInt(t, db, `SELECT total_tokens_cache_read FROM catalog_providers WHERE name=?`, provider)
	c.providerCacheWrite = scanInt(t, db, `SELECT total_tokens_cache_write FROM catalog_providers WHERE name=?`, provider)
	c.modelCalls = scanInt(t, db, `SELECT call_count FROM catalog_models WHERE provider=? AND name=?`, provider, model)
	c.modelFailures = scanInt(t, db, `SELECT failure_count FROM catalog_models WHERE provider=? AND name=?`, provider, model)
	c.modelDurationUS = scanInt(t, db, `SELECT total_duration_us FROM catalog_models WHERE provider=? AND name=?`, provider, model)
	c.modelTokensIn = scanInt(t, db, `SELECT total_tokens_in FROM catalog_models WHERE provider=? AND name=?`, provider, model)
	c.modelTokensOut = scanInt(t, db, `SELECT total_tokens_out FROM catalog_models WHERE provider=? AND name=?`, provider, model)
	c.toolCalls = scanInt(t, db, `SELECT call_count FROM catalog_tools WHERE namespace=? AND name=?`, toolNS, toolName)
	c.toolFailures = scanInt(t, db, `SELECT failure_count FROM catalog_tools WHERE namespace=? AND name=?`, toolNS, toolName)
	c.toolTokensIn = scanInt(t, db, `SELECT total_tokens_in FROM catalog_tools WHERE namespace=? AND name=?`, toolNS, toolName)
	c.toolDurationUS = scanInt(t, db, `SELECT total_duration_us FROM catalog_tools WHERE namespace=? AND name=?`, toolNS, toolName)
	return c
}

// applyOpRoundTrip applies a session + turn + one LLM op (provider/model, tokens,
// cost via NopPricer is zero so tokens drive the assertion) + one tool op, all
// finalized, inside a single committed batch. Called once for the baseline and
// again (same identities, same seqs) for the re-emit assertion.
func applyCatalogOps(t *testing.T, ctx context.Context, db *sql.DB, w *writer, src string, llmStatus, toolStatus string) {
	t.Helper()
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
	apply(canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "s", Seq: 1,
	})
	// LLM op (provider+model) → catalog_providers + catalog_models.
	apply(canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "message", Provider: "openai", Model: "gpt-5.5",
	})
	apply(canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: llmStatus, EndTs: 1300,
		TokensIn: 100, TokensOut: 20, TokensCacheRead: 5, TokensCacheWrite: 3,
	})
	// Tool op → catalog_tools.
	apply(canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1400},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "shell", ToolNamespace: "shell",
	})
	apply(canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 1400},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2, Status: toolStatus, EndTs: 1500,
		TokensIn: 7,
	})
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}
}

// TestCatalog_ReEmittedOpNoDoubleCount pins H1a (1): re-emitting an IDENTICAL
// OpStarted+OpFinalized for the same (turn,seq) — exactly what the codex /
// claude_code replay-from-0 + late-enrichment design does — must leave every
// catalog aggregate where the single emission left it. call_count counts the op
// once (only on the genuine insert); failure/token/cost/duration totals
// contribute the op's terminal values once.
func TestCatalog_ReEmittedOpNoDoubleCount(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "codex", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "codex", "/tmp", NopPricer{})

	// Baseline: a single clean emission.
	applyCatalogOps(t, ctx, db, w, src, "completed", "completed")
	baseline := readCatalogTotals(t, db, "openai", "gpt-5.5", "shell", "shell")
	if baseline.providerCalls != 1 || baseline.modelCalls != 1 || baseline.toolCalls != 1 {
		t.Fatalf("baseline call counts unexpected: %+v (test premise broken)", baseline)
	}
	if baseline.modelTokensIn != 100 || baseline.providerTokensIn != 100 || baseline.toolTokensIn != 7 {
		t.Fatalf("baseline token totals unexpected: %+v (test premise broken)", baseline)
	}

	// Re-emit the SAME events (same seqs/identities). Each op row already exists, so
	// every OpStarted is an UPDATE and every OpFinalized carries unchanged totals.
	applyCatalogOps(t, ctx, db, w, src, "completed", "completed")
	after := readCatalogTotals(t, db, "openai", "gpt-5.5", "shell", "shell")

	if after != baseline {
		t.Fatalf("catalog aggregates moved under an identical op re-emit (must be idempotent, H1a):\n baseline=%+v\n after   =%+v", baseline, after)
	}
}

// TestCatalog_ReFinalizeStatusCorrectionDeltaOnce pins H1a (2): finalizing an op
// completed and then RE-finalizing it failed (the codex output-first exec
// correction: exit≠0 overrides a provisional completed) must move the catalog
// failure_count by EXACTLY ONE — not zero (the correction must register) and not
// two (the original completed contributed zero failures, the correction +1). The
// token totals, which did not change, must not move on the re-finalize.
func TestCatalog_ReFinalizeStatusCorrectionDeltaOnce(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"
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

	// Batch 1: session + turn + a tool op finalized COMPLETED (failure 0).
	tx1, _ := db.BeginTx(ctx, nil)
	apply(tx1, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(tx1, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "shell", ToolNamespace: "shell",
	})
	apply(tx1, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: 1200, TokensIn: 9,
	})
	if err := tx1.Commit(); err != nil {
		t.Fatalf("Commit batch 1: %v", err)
	}
	if got := scanInt(t, db, `SELECT failure_count FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != 0 {
		t.Fatalf("after completed finalize, failure_count = %d, want 0", got)
	}

	// Batch 2: the correcting re-finalize → failed (the output-first exec exit≠0
	// path). Tokens unchanged. The failure total must become exactly 1.
	tx2, _ := db.BeginTx(ctx, nil)
	apply(tx2, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "command_failed", EndTs: 1200, TokensIn: 9,
	})
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit batch 2: %v", err)
	}

	if got := scanInt(t, db, `SELECT failure_count FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != 1 {
		t.Errorf("after completed→failed re-finalize, failure_count = %d, want exactly 1 (delta once, H1a)", got)
	}
	if got := scanInt(t, db, `SELECT call_count FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != 1 {
		t.Errorf("call_count = %d, want 1 (no OpStarted re-emit here; finalize must not bump call_count)", got)
	}
	if got := scanInt(t, db, `SELECT total_tokens_in FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != 9 {
		t.Errorf("total_tokens_in = %d, want 9 (unchanged tokens must not re-add on re-finalize)", got)
	}
}
