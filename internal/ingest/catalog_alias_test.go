package ingest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type aliasCatalogTotals struct {
	calls, failures       int64
	tokensIn, tokensOut   int64
	cacheRead, cacheWrite int64
	durationUS            int64
	costUSD               float64
}

type toolCatalogTotals struct {
	rows                int64
	calls, failures     int64
	tokensIn, tokensOut int64
	durationUS          int64
	costUSD             float64
}

func readProviderAliasTotals(t *testing.T, db *sql.DB, provider, alias string) aliasCatalogTotals {
	t.Helper()
	var c aliasCatalogTotals
	row := db.QueryRowContext(context.Background(), `
SELECT COALESCE(SUM(call_count),0), COALESCE(SUM(failure_count),0),
       COALESCE(SUM(total_tokens_in),0), COALESCE(SUM(total_tokens_out),0),
       COALESCE(SUM(total_tokens_cache_read),0), COALESCE(SUM(total_tokens_cache_write),0),
       COALESCE(SUM(total_cost_usd),0)
  FROM catalog_providers WHERE name=? AND alias=?`, provider, alias)
	if err := row.Scan(&c.calls, &c.failures, &c.tokensIn, &c.tokensOut, &c.cacheRead, &c.cacheWrite, &c.costUSD); err != nil {
		t.Fatalf("read catalog_providers %s/%s: %v", provider, alias, err)
	}
	return c
}

func readModelAliasTotals(t *testing.T, db *sql.DB, provider, model string) aliasCatalogTotals {
	t.Helper()
	var c aliasCatalogTotals
	row := db.QueryRowContext(context.Background(), `
SELECT COALESCE(SUM(call_count),0), COALESCE(SUM(failure_count),0),
       COALESCE(SUM(total_tokens_in),0), COALESCE(SUM(total_tokens_out),0),
       COALESCE(SUM(total_tokens_cache_read),0), COALESCE(SUM(total_tokens_cache_write),0),
       COALESCE(SUM(total_duration_us),0), COALESCE(SUM(total_cost_usd),0)
  FROM catalog_models WHERE provider=? AND name=?`, provider, model)
	if err := row.Scan(&c.calls, &c.failures, &c.tokensIn, &c.tokensOut, &c.cacheRead, &c.cacheWrite, &c.durationUS, &c.costUSD); err != nil {
		t.Fatalf("read catalog_models %s/%s: %v", provider, model, err)
	}
	return c
}

func readToolNamespaceTotals(t *testing.T, db *sql.DB, namespace, name string) toolCatalogTotals {
	t.Helper()
	var c toolCatalogTotals
	row := db.QueryRowContext(context.Background(), `
SELECT COUNT(*), COALESCE(SUM(call_count),0), COALESCE(SUM(failure_count),0),
       COALESCE(SUM(total_tokens_in),0), COALESCE(SUM(total_tokens_out),0),
       COALESCE(SUM(total_duration_us),0), COALESCE(SUM(total_cost_usd),0)
  FROM catalog_tools WHERE namespace=? AND name=?`, namespace, name)
	if err := row.Scan(&c.rows, &c.calls, &c.failures, &c.tokensIn, &c.tokensOut, &c.durationUS, &c.costUSD); err != nil {
		t.Fatalf("read catalog_tools %s/%s: %v", namespace, name, err)
	}
	return c
}

func assertAliasTotals(t *testing.T, label string, got, want aliasCatalogTotals) {
	t.Helper()
	if got != want {
		t.Fatalf("%s totals = %+v, want %+v", label, got, want)
	}
}

func assertToolTotals(t *testing.T, label string, got, want toolCatalogTotals) {
	t.Helper()
	if got != want {
		t.Fatalf("%s totals = %+v, want %+v", label, got, want)
	}
}

func newAliasCatalogTest(t *testing.T, src string) (*sql.DB, context.Context, *writer) {
	t.Helper()
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "opencode", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	return db, ctx, newWriter(src, "opencode", "/tmp", NopPricer{})
}

func applyAliasCatalogEvents(t *testing.T, ctx context.Context, db *sql.DB, w *writer, src string, events ...canonical.Event) {
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
	for _, ev := range events {
		apply(ev)
	}
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}
}

func TestCatalog_ProviderAliasCorrectionMigratesContribution(t *testing.T) {
	t.Parallel()
	const src = "opencode:/tmp"
	db, ctx, w := newAliasCatalogTest(t, src)
	applyAliasCatalogEvents(t, ctx, db, w, src,
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "message", Provider: "openai",
			ProviderAlias: "workspace-a", Model: "gpt-5",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "model_error", EndTs: 1500, TokensIn: 30, TokensOut: 8,
			TokensCacheRead: 2, TokensCacheWrite: 1, CostUSD: 0.5,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "message", Provider: "openai",
			ProviderAlias: "workspace-b", Model: "gpt-5",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "model_error", EndTs: 1500, TokensIn: 30, TokensOut: 8,
			TokensCacheRead: 2, TokensCacheWrite: 1, CostUSD: 0.5,
		},
	)
	assertAliasTotals(t, "old provider alias", readProviderAliasTotals(t, db, "openai", "workspace-a"), aliasCatalogTotals{})
	if got := scanInt(t, db, `SELECT COUNT(*) FROM catalog_providers WHERE name=? AND alias=?`, "openai", "workspace-a"); got != 1 {
		t.Fatalf("old provider alias row count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name=? AND alias=?`, "openai", "workspace-a"); got != 0 {
		t.Fatalf("old provider alias call_count = %d, want 0", got)
	}
	wantProvider := aliasCatalogTotals{calls: 1, failures: 1, tokensIn: 30, tokensOut: 8, cacheRead: 2, cacheWrite: 1, costUSD: 0.5}
	assertAliasTotals(t, "corrected provider alias", readProviderAliasTotals(t, db, "openai", "workspace-b"), wantProvider)
	wantModel := wantProvider
	wantModel.durationUS = 400
	assertAliasTotals(t, "model row", readModelAliasTotals(t, db, "openai", "gpt-5"), wantModel)
}

func TestCatalog_ToolNamespaceCorrectionMigratesContribution(t *testing.T) {
	t.Parallel()
	const src = "opencode:/tmp"
	db, ctx, w := newAliasCatalogTest(t, src)
	applyAliasCatalogEvents(t, ctx, db, w, src,
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpTool, Name: "read_file",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "tool_error", EndTs: 1500, TokensIn: 30, TokensOut: 8,
			CostUSD: 0.5,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpTool, Name: "read_file", ToolNamespace: "mcp:files",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "tool_error", EndTs: 1500, TokensIn: 30, TokensOut: 8,
			CostUSD: 0.5,
		},
	)
	assertToolTotals(t, "old builtin tool", readToolNamespaceTotals(t, db, "builtin", "read_file"), toolCatalogTotals{rows: 1})
	if got := scanInt(t, db, `SELECT call_count FROM catalog_tools WHERE namespace=? AND name=?`, "builtin", "read_file"); got != 0 {
		t.Fatalf("old builtin tool call_count = %d, want 0", got)
	}
	assertToolTotals(t, "corrected namespace tool", readToolNamespaceTotals(t, db, "mcp:files", "read_file"), toolCatalogTotals{
		rows:       1,
		calls:      1,
		failures:   1,
		tokensIn:   30,
		tokensOut:  8,
		durationUS: 400,
		costUSD:    0.5,
	})
}

func TestCatalog_ReEmitEmptyProviderAliasNoDrain(t *testing.T) {
	t.Parallel()
	const src = "opencode:/tmp"
	db, ctx, w := newAliasCatalogTest(t, src)
	applyAliasCatalogEvents(t, ctx, db, w, src,
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "message", Provider: "openai",
			ProviderAlias: "workspace-a", Model: "gpt-5",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "model_error", EndTs: 1500, TokensIn: 30, TokensOut: 8,
			TokensCacheRead: 2, TokensCacheWrite: 1, CostUSD: 0.5,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "message", Provider: "openai", Model: "gpt-5",
		},
	)
	wantProvider := aliasCatalogTotals{calls: 1, failures: 1, tokensIn: 30, tokensOut: 8, cacheRead: 2, cacheWrite: 1, costUSD: 0.5}
	assertAliasTotals(t, "preserved provider alias", readProviderAliasTotals(t, db, "openai", "workspace-a"), wantProvider)
	assertAliasTotals(t, "empty provider alias", readProviderAliasTotals(t, db, "openai", ""), aliasCatalogTotals{})
	if got := scanString(t, db, `SELECT IFNULL(provider_alias,'') FROM ops WHERE provider='openai' AND model='gpt-5'`); got != "workspace-a" {
		t.Fatalf("ops.provider_alias = %q, want workspace-a", got)
	}
	wantModel := wantProvider
	wantModel.durationUS = 400
	assertAliasTotals(t, "model row", readModelAliasTotals(t, db, "openai", "gpt-5"), wantModel)
}

func TestCatalog_StatusCorrectionLLMDeltaOnce(t *testing.T) {
	t.Parallel()
	const src = "opencode:/tmp"
	db, ctx, w := newAliasCatalogTest(t, src)
	applyAliasCatalogEvents(t, ctx, db, w, src,
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "message", Provider: "openai",
			ProviderAlias: "workspace-a", Model: "gpt-5",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "completed",
			EndTs: 1500, TokensIn: 40, TokensOut: 12,
			TokensCacheRead: 4, TokensCacheWrite: 2, CostUSD: 1.25,
		},
	)
	wantCompletedProvider := aliasCatalogTotals{calls: 1, tokensIn: 40, tokensOut: 12, cacheRead: 4, cacheWrite: 2, costUSD: 1.25}
	assertAliasTotals(t, "completed provider", readProviderAliasTotals(t, db, "openai", "workspace-a"), wantCompletedProvider)
	wantCompletedModel := wantCompletedProvider
	wantCompletedModel.durationUS = 400
	assertAliasTotals(t, "completed model", readModelAliasTotals(t, db, "openai", "gpt-5"), wantCompletedModel)

	applyAliasCatalogEvents(t, ctx, db, w, src,
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "model_error", EndTs: 1500, TokensIn: 40, TokensOut: 12,
			TokensCacheRead: 4, TokensCacheWrite: 2, CostUSD: 1.25,
		},
	)
	wantFailedProvider := wantCompletedProvider
	wantFailedProvider.failures = 1
	assertAliasTotals(t, "failed provider", readProviderAliasTotals(t, db, "openai", "workspace-a"), wantFailedProvider)
	wantFailedModel := wantCompletedModel
	wantFailedModel.failures = 1
	assertAliasTotals(t, "failed model", readModelAliasTotals(t, db, "openai", "gpt-5"), wantFailedModel)
}

func TestCatalog_ProviderWithoutModelBooksProviderOnly(t *testing.T) {
	t.Parallel()
	const src = "opencode:/tmp"
	db, ctx, w := newAliasCatalogTest(t, src)
	applyAliasCatalogEvents(t, ctx, db, w, src,
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "message", Provider: "openai",
			ProviderAlias: "workspace-a",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1500},
			SessionNativeID: "s", TurnSeq: 1, Seq: 1, Status: "failed",
			ErrorClass: "model_error", EndTs: 1500, TokensIn: 50, TokensOut: 10,
			TokensCacheRead: 3, TokensCacheWrite: 2, CostUSD: 0.75,
		},
	)
	wantProvider := aliasCatalogTotals{calls: 1, failures: 1, tokensIn: 50, tokensOut: 10, cacheRead: 3, cacheWrite: 2, costUSD: 0.75}
	assertAliasTotals(t, "provider without model", readProviderAliasTotals(t, db, "openai", "workspace-a"), wantProvider)
	if got := scanInt(t, db, `SELECT COUNT(*) FROM catalog_models WHERE provider=?`, "openai"); got != 0 {
		t.Fatalf("catalog_models rows for provider without model = %d, want 0", got)
	}
}
