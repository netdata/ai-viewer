package ingest

import (
	"context"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestCatalog_AgentRowCreatedOnSessionStart(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:     "s",
		RootNativeID: "s",
		Kind:         canonical.KindRoot,
		AgentName:    "research",
		Cwd:          "/work",
	})
	_ = tx.Commit()

	if got := scanInt(t, db, `SELECT session_count FROM catalog_agents WHERE source_format='aiagent_v3' AND name='research'`); got != 1 {
		t.Errorf("catalog_agents session_count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT session_count FROM catalog_cwds WHERE source_format='aiagent_v3' AND cwd='/work'`); got != 1 {
		t.Errorf("catalog_cwds session_count = %d, want 1", got)
	}
}

func TestCatalog_AgentRowIncrementsOnSecondSession(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	for i := 0; i < 3; i++ {
		tx, _ := db.BeginTx(ctx, nil)
		_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: uint64(i + 1), Ts: int64(1000 + i*10)},
			NativeID:  string(rune('a' + i)), RootNativeID: string(rune('a' + i)),
			Kind: canonical.KindRoot, AgentName: "research",
		})
		_ = tx.Commit()
	}
	if got := scanInt(t, db, `SELECT session_count FROM catalog_agents WHERE name='research'`); got != 3 {
		t.Errorf("session_count = %d, want 3", got)
	}
}

func TestCatalog_ToolDefaultsToBuiltinNamespace(t *testing.T) {
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
		Kind: canonical.OpTool, Name: "read_file", // no namespace
	})
	_ = tx.Commit()

	if got := scanInt(t, db, `SELECT call_count FROM catalog_tools WHERE namespace='builtin' AND name='read_file'`); got != 1 {
		t.Errorf("namespace defaulted incorrectly")
	}
}

func TestCatalog_ProviderAliasPreserved(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "opencode:/tmp", "opencode", "/tmp")
	w := newWriter("opencode:/tmp", "opencode", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "opencode:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "opencode:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "openai", ProviderAlias: "my-alias", Model: "gpt-5",
	})
	_ = tx.Commit()

	if got := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='openai' AND alias='my-alias'`); got != 1 {
		t.Errorf("alias not stored: got call_count = %d", got)
	}
}

// TestCatalog_MetaRewriteNoDoubleCount pins the P1.6 invariant (SOW-0003 Round 6):
// the catalog rollups ACCUMULATE on conflict (session_count+1, call_count+1,
// total_tokens_*/cost/duration += …), so re-emitting SessionStarted / OpStarted /
// OpFinalized would double-count them. The Round-6 design repairs a late
// `.meta.json` WITHOUT any such re-emit — the child AgentName is repaired with a
// catalog-safe SessionUpdatedEvent (applySessionUpdated makes NO catalog call). This
// test proves the property at the writer layer: apply a full
// SessionStarted+OpStarted+OpFinalized once, snapshot every catalog aggregate, then
// apply a SessionUpdated AgentName repair (the kind the late-meta path now emits) and
// assert NO catalog aggregate moved.
func TestCatalog_MetaRewriteNoDoubleCount(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "claude-code", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "claude-code", "/tmp", NopPricer{})

	// Phase 1: a child sub-agent session (with AgentName + Cwd → catalog_agents +
	// catalog_cwds) plus one LLM op finalized with tokens/cost → catalog_providers +
	// catalog_models. This is the full set of catalog-touching events.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply := func(ev canonical.Event) {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %T: %v", ev, err)
		}
	}
	apply(canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "c", RootNativeID: "c", Kind: canonical.KindSubAgent,
		AgentName: "Explore", Cwd: "/work",
	})
	apply(canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "c", Seq: 1,
	})
	apply(canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID: "c", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "claude-opus-4", Provider: "anthropic", Model: "claude-opus-4",
	})
	apply(canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "c", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: 1200,
		TokensIn: 30, TokensOut: 8, CostUSD: 0.5,
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit phase 1: %v", err)
	}

	// Snapshot every catalog aggregate after the first (and only legitimate) count.
	type snap struct{ agents, cwds, providerCalls, modelCalls, modelTokensIn int64 }
	read := func() snap {
		return snap{
			agents:        scanInt(t, db, `SELECT session_count FROM catalog_agents WHERE name='Explore'`),
			cwds:          scanInt(t, db, `SELECT session_count FROM catalog_cwds WHERE cwd='/work'`),
			providerCalls: scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='anthropic'`),
			modelCalls:    scanInt(t, db, `SELECT call_count FROM catalog_models WHERE name='claude-opus-4'`),
			modelTokensIn: scanInt(t, db, `SELECT total_tokens_in FROM catalog_models WHERE name='claude-opus-4'`),
		}
	}
	before := read()
	if before.agents != 1 || before.providerCalls != 1 || before.modelCalls != 1 || before.modelTokensIn != 30 {
		t.Fatalf("phase-1 catalog snapshot unexpected: %+v (test premise broken)", before)
	}

	// Phase 2: the catalog-safe AgentName repair the late-meta path now emits.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx phase 2: %v", err)
	}
	if err := w.apply(ctx, tx2, canonical.SessionUpdatedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1300},
		NativeID:  "c", AgentName: "Explore",
	}); err != nil {
		t.Fatalf("apply SessionUpdated: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit phase 2: %v", err)
	}

	after := read()
	if after != before {
		t.Fatalf("catalog aggregates changed after a SessionUpdated AgentName repair:\n before=%+v\n after =%+v\n(the meta-repair path must be catalog-safe — no re-emit, no double-count)", before, after)
	}
}

func TestCatalog_OnSessionStartedNoOpForEmptyMetadata(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		// no AgentName, no Cwd
	})
	_ = tx.Commit()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM catalog_agents`); got != 0 {
		t.Errorf("catalog_agents = %d, want 0", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM catalog_cwds`); got != 0 {
		t.Errorf("catalog_cwds = %d, want 0", got)
	}
}
