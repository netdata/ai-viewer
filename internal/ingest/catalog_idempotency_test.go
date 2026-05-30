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

// TestCatalog_IdentityChangeMigratesContribution pins SOW-0004 I1: the codex MCP
// enrichment re-emits OpStarted for the SAME (turn,seq) with a CORRECTED catalog
// identity (the heuristic tool_namespace="custom" placeholder is re-stamped to
// "mcp:<server>", and the tool name is corrected). The op physically counted once
// under the placeholder key (and any finalize totals booked there); a naive
// re-emit would INSERT a fresh catalog_tools row at call_count=1 under the
// corrected key — counting the one op TWICE — and strand the finalize totals on
// the old key. The fix MOVES the whole contribution (call_count + failure/tokens/
// cost/duration totals) onto the new key. This test drives the exact event
// sequence the codex adapter emits (function_call → output → mcp_tool_call_end
// re-emit) and asserts the total call_count across BOTH keys is exactly 1 and the
// failure/token totals live ONLY under the final key.
func TestCatalog_IdentityChangeMigratesContribution(t *testing.T) {
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

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "s", Seq: 1,
	})
	// 1) function_call → heuristic placeholder identity (namespace "custom").
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "search", ToolNamespace: "custom",
	})
	// 2) function_call_output → finalizes the placeholder op (with a token cost so
	//    a stranded total would be visible on the old key).
	apply(tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "tool_error",
		EndTs: 1200, TokensIn: 11, TokensOut: 4,
	})
	// 3) mcp_tool_call_end → re-emit OpStarted with the CORRECTED identity (the I1
	//    case): namespace "mcp:files", name "files.read". Same (turn,seq).
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "files.read", ToolNamespace: "mcp:files",
	})
	// 4) the enrichment's correcting OpFinalized (same terminal status here).
	apply(tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "tool_error",
		EndTs: 1200, TokensIn: 11, TokensOut: 4,
	})
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	// Total call_count across BOTH the placeholder key and the corrected key must be
	// exactly 1 — the one physical op, counted once.
	oldCalls := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_tools WHERE namespace='custom' AND name='search'`)
	newCalls := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_tools WHERE namespace='mcp:files' AND name='files.read'`)
	if oldCalls+newCalls != 1 {
		t.Fatalf("total call_count across both keys = %d (old=%d new=%d), want 1 (one op counted once, I1)", oldCalls+newCalls, oldCalls, newCalls)
	}
	// The op's final contribution lives ENTIRELY under the corrected key.
	if newCalls != 1 {
		t.Errorf("corrected key call_count = %d, want 1 (contribution migrated to final identity)", newCalls)
	}
	if got := scanInt(t, db, `SELECT failure_count FROM catalog_tools WHERE namespace='mcp:files' AND name='files.read'`); got != 1 {
		t.Errorf("corrected key failure_count = %d, want 1 (failed op's failure migrated to final key)", got)
	}
	if got := scanInt(t, db, `SELECT total_tokens_in FROM catalog_tools WHERE namespace='mcp:files' AND name='files.read'`); got != 11 {
		t.Errorf("corrected key total_tokens_in = %d, want 11 (tokens migrated to final key)", got)
	}
	if got := scanInt(t, db, `SELECT total_tokens_out FROM catalog_tools WHERE namespace='mcp:files' AND name='files.read'`); got != 4 {
		t.Errorf("corrected key total_tokens_out = %d, want 4 (tokens migrated to final key)", got)
	}
	// The old placeholder key must be fully drained: no stranded count or totals.
	if got := scanInt(t, db, `SELECT COALESCE(failure_count,0) FROM catalog_tools WHERE namespace='custom' AND name='search'`); got != 0 {
		t.Errorf("placeholder key failure_count = %d, want 0 (must be migrated off the old key, I1)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(total_tokens_in,0) FROM catalog_tools WHERE namespace='custom' AND name='search'`); got != 0 {
		t.Errorf("placeholder key total_tokens_in = %d, want 0 (no stranded tokens on the old key, I1)", got)
	}
}

// TestCatalog_LLMIdentityChangeMigratesContribution pins SOW-0004 I1 for the LLM
// catalog rows (catalog_providers + catalog_models): an OpStarted re-emit that
// corrects an LLM op's (provider, model) on the same (turn,seq) must MOVE its
// whole contribution — call_count + failure/tokens/cost/duration totals — off the
// old provider/model rows and onto the corrected ones, so each physical op counts
// once. This exercises the provider/model migrate-out + migrate-in branches the
// tool case above does not. (LLM identity correction is rarer than the codex MCP
// tool case, but the catalog migration must be symmetric across kinds.)
func TestCatalog_LLMIdentityChangeMigratesContribution(t *testing.T) {
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "s", Seq: 1,
	})
	// 1) LLM op under (openai, gpt-5.5) + a failed finalize with tokens/cost-bearing
	//    duration so a stranded total would be visible on the old key.
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "message", Provider: "openai", Model: "gpt-5.5",
	})
	apply(tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "model_error",
		EndTs: 1500, TokensIn: 30, TokensOut: 8, TokensCacheRead: 2, TokensCacheWrite: 1,
	})
	// 2) Re-emit OpStarted with a CORRECTED model (same provider): gpt-5.5 → gpt-5.5-codex.
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "message", Provider: "openai", Model: "gpt-5.5-codex",
	})
	apply(tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed", ErrorClass: "model_error",
		EndTs: 1500, TokensIn: 30, TokensOut: 8, TokensCacheRead: 2, TokensCacheWrite: 1,
	})
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	// catalog_models: the old model row drained to 0; the corrected model row holds
	// the single op's full contribution.
	oldModelCalls := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`)
	newModelCalls := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_models WHERE provider='openai' AND name='gpt-5.5-codex'`)
	if oldModelCalls != 0 {
		t.Errorf("old model call_count = %d, want 0 (migrated off, I1)", oldModelCalls)
	}
	if newModelCalls != 1 {
		t.Errorf("corrected model call_count = %d, want 1 (migrated on, I1)", newModelCalls)
	}
	if got := scanInt(t, db, `SELECT failure_count FROM catalog_models WHERE provider='openai' AND name='gpt-5.5-codex'`); got != 1 {
		t.Errorf("corrected model failure_count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT total_tokens_in FROM catalog_models WHERE provider='openai' AND name='gpt-5.5-codex'`); got != 30 {
		t.Errorf("corrected model total_tokens_in = %d, want 30", got)
	}
	if got := scanInt(t, db, `SELECT total_duration_us FROM catalog_models WHERE provider='openai' AND name='gpt-5.5-codex'`); got != 400 {
		t.Errorf("corrected model total_duration_us = %d, want 400 (1500-1100)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(total_tokens_in,0) FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != 0 {
		t.Errorf("old model total_tokens_in = %d, want 0 (no stranded tokens, I1)", got)
	}
	// catalog_providers: provider unchanged across the re-emit, so its call_count
	// stays exactly 1 (the migrate-out −1 and migrate-in +1 net to zero on the SAME
	// provider row) — a provider that did not change must not double-count NOR drop.
	if got := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='openai'`); got != 1 {
		t.Errorf("provider call_count = %d, want 1 (unchanged provider nets to 1 across an identity-changed re-emit)", got)
	}
	if got := scanInt(t, db, `SELECT failure_count FROM catalog_providers WHERE name='openai'`); got != 1 {
		t.Errorf("provider failure_count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT total_tokens_in FROM catalog_providers WHERE name='openai'`); got != 30 {
		t.Errorf("provider total_tokens_in = %d, want 30", got)
	}
}

// TestCatalog_IdentityChangeBeforeFinalize pins the SOW-0004 I1 migration when the
// op was OpStarted under one identity but its identity is corrected by a re-emitted
// OpStarted BEFORE any OpFinalized (no totals booked yet). Only call_count moves:
// the old key drains to 0, the new key counts 1, and no failure/token total is
// stranded or duplicated. This covers the migrate path with an unfinalized prior
// (addMigratedTotals's no-booked-totals short-circuit).
func TestCatalog_IdentityChangeBeforeFinalize(t *testing.T) {
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
	tx, _ := db.BeginTx(ctx, nil)
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "search", ToolNamespace: "custom",
	})
	// Identity correction BEFORE any finalize: only the call_count exists to move.
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "files.read", ToolNamespace: "mcp:files",
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_tools WHERE namespace='custom' AND name='search'`); got != 0 {
		t.Errorf("old key call_count = %d, want 0 (migrated before finalize)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_tools WHERE namespace='mcp:files' AND name='files.read'`); got != 1 {
		t.Errorf("new key call_count = %d, want 1 (migrated before finalize)", got)
	}
}

// TestCatalog_KindChangeMigratesAcrossTables pins the SOW-0004 I1 migration when a
// re-emitted OpStarted changes the op KIND (tool → llm) on the same (turn,seq) — a
// defensive edge: the op's contribution must move OFF the tool catalog row and ONTO
// the LLM provider/model rows, never double-counted across tables. Covers
// catalogIdentityChanged's kind-changed branch.
func TestCatalog_KindChangeMigratesAcrossTables(t *testing.T) {
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
	tx, _ := db.BeginTx(ctx, nil)
	apply(tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "shell", ToolNamespace: "shell",
	})
	apply(tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: 1200, TokensIn: 5,
	})
	// Re-emit as an LLM op (kind change) on the same (turn,seq).
	apply(tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "message", Provider: "openai", Model: "gpt-5.5",
	})
	apply(tx, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: 1200, TokensIn: 5,
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// The tool row drained; the LLM model+provider rows hold the single contribution.
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != 0 {
		t.Errorf("tool key call_count = %d, want 0 (migrated to llm tables, I1 kind change)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != 1 {
		t.Errorf("model call_count = %d, want 1 (migrated from tool, I1 kind change)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(call_count,0) FROM catalog_providers WHERE name='openai'`); got != 1 {
		t.Errorf("provider call_count = %d, want 1 (migrated from tool, I1 kind change)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(total_tokens_in,0) FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != 0 {
		t.Errorf("tool total_tokens_in = %d, want 0 (no stranded tokens after kind change)", got)
	}
	if got := scanInt(t, db, `SELECT COALESCE(total_tokens_in,0) FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != 5 {
		t.Errorf("model total_tokens_in = %d, want 5 (migrated from tool)", got)
	}
}
