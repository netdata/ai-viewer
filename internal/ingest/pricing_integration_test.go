package ingest

import (
	"context"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/pricing"
)

// pricer-package import lives in the test file so the production
// internal/pricing package does not import internal/ingest (cycle
// avoidance).

// Compile-time assertions that *pricing.Pricer satisfies Pricer,
// DetailedPricer, AND MetadataPricer. The blank identifier lines
// below fail to compile if the signatures drift. Asserting the
// optional interfaces on the production pricer (and not just the
// fake in writer_test.go) prevents accidental loss of the
// CostWithDetail or CtxMax method from causing the writer / catalog
// to silently fall back to non-detailed paths.
var (
	_ Pricer         = (*pricing.Pricer)(nil)
	_ DetailedPricer = (*pricing.Pricer)(nil)
	_ MetadataPricer = (*pricing.Pricer)(nil)
)

// TestNopPricerSatisfiesPricerNotDetailed pins NopPricer's contract:
// it intentionally implements only Pricer (returning 0 unconditionally)
// and must NOT satisfy DetailedPricer. Adding CostWithDetail to
// NopPricer would change writer behaviour for every adapter still
// wired to it (every test that omits a real pricer) by routing them
// into the pricing-miss path. This test fails loudly if that drift
// ever lands.
func TestNopPricerSatisfiesPricerNotDetailed(t *testing.T) {
	t.Parallel()
	var p Pricer = NopPricer{}
	if got := p.Cost("any", "model", 0, 1, 1, 0, 0); got != 0 {
		t.Errorf("NopPricer.Cost = %v, want 0 (Nop must always return zero)", got)
	}
	if _, ok := p.(DetailedPricer); ok {
		t.Errorf("NopPricer must not satisfy DetailedPricer; adding CostWithDetail would silently route writer into pricing-miss path")
	}
}

// TestPricingPackageSatisfiesIngestInterface is the runtime mirror of
// the compile-time assertion: it instantiates the real pricer, casts
// it to the ingest.Pricer interface, and confirms a Cost call returns
// a non-negative number for a known seed model. Catches the unlikely
// case where the embedded data parses but the interface assignment
// silently uses a method set we did not intend.
func TestPricingPackageSatisfiesIngestInterface(t *testing.T) {
	t.Parallel()
	p, err := pricing.New()
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	var iface Pricer = p
	// claude-3-5-sonnet is in the seed; any ts after 2024-06-20 hits
	// the tier. 2025-01-01 00:00 UTC = 1_735_689_600_000_000 us.
	got := iface.Cost("anthropic", "claude-3-5-sonnet", 1_735_689_600_000_000, 1_000_000, 0, 0, 0)
	if got <= 0 {
		t.Errorf("Cost = %v, want > 0 (priced seed model)", got)
	}
}

// TestCatalogModelsCtxMaxSeededFromPricer wires the production pricer
// into the writer and verifies that catalog_models.ctx_max receives
// the embedded pricing seed when the adapter records no CtxMax on
// the OpStartedEvent. Per pricing.md §"Field semantics" the table's
// `ctx_max` field is the catalog seed; without this end-to-end test
// a future change that drops MetadataPricer or the COALESCE clause
// in catalog.go would silently regress the seeding contract.
func TestCatalogModelsCtxMaxSeededFromPricer(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")

	pr, err := pricing.New()
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", pr)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	// LLM op with provider=anthropic, model=claude-3-5-sonnet (a known
	// seeded model in the embedded pricing.json). The adapter records
	// NO CtxMax, so the only path to catalog_models.ctx_max is the
	// pricer seed.
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "anthropic", Model: "claude-3-5-sonnet",
	})
	_ = tx.Commit()

	got := scanInt(t, db, `SELECT COALESCE(ctx_max, 0) FROM catalog_models WHERE provider='anthropic' AND name='claude-3-5-sonnet'`)
	if got <= 0 {
		t.Errorf("catalog_models.ctx_max = %d, want > 0 (seeded from pricing table)", got)
	}
}

// TestCatalogModelsCtxMaxAbsentWhenPricerUnknown proves the seeding
// is opt-in on pricer knowledge: a (provider, model) the embedded
// pricing.json does not carry leaves catalog_models.ctx_max NULL.
// Prevents a future "always non-zero" regression.
func TestCatalogModelsCtxMaxAbsentWhenPricerUnknown(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")

	pr, err := pricing.New()
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", pr)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "fake-vendor-99", Model: "fake-model-impossible",
	})
	_ = tx.Commit()

	// scanInt's COALESCE collapses NULL to 0 — the assertion is the
	// inverse of the prior test: an unknown (provider, model) must
	// NOT receive a fabricated seed.
	got := scanInt(t, db, `SELECT COALESCE(ctx_max, 0) FROM catalog_models WHERE provider='fake-vendor-99' AND name='fake-model-impossible'`)
	if got != 0 {
		t.Errorf("catalog_models.ctx_max = %d for unknown model, want 0", got)
	}
}

// TestCatalogModelsCtxMaxObservedExceedsSeed pins the data-model.md
// §"catalog_models" semantics: the catalog ctx_max is the MAX of all
// observed values, with the pricing seed acting as a FLOOR (not a
// ceiling). Without the MAX/CASE update in onOpFinalized, the seed
// pins the column forever even when an adapter observes a larger
// context window — exactly the regression this test guards against.
// claude-3-5-sonnet seeds at 200000; the test ingests an op
// observing 300000 and asserts the catalog row carries the larger
// value.
func TestCatalogModelsCtxMaxObservedExceedsSeed(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")

	pr, err := pricing.New()
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", pr)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "anthropic", Model: "claude-3-5-sonnet",
	})
	// Seed was applied on OpStartedEvent. Observe a larger CtxMax on
	// OpFinalized — the catalog row must climb to that value.
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		// Finalized.Ts == EndTs (the op END), spec-conformant: a finalize sorts
		// after its OpStarted (Ts 1100), so its own Ts is the end, not the start.
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		EndTs: 1200, Status: "completed",
		CtxMax: 300000,
	})
	_ = tx.Commit()

	got := scanInt(t, db, `SELECT COALESCE(ctx_max, 0) FROM catalog_models WHERE provider='anthropic' AND name='claude-3-5-sonnet'`)
	if got != 300000 {
		t.Errorf("catalog_models.ctx_max = %d after observing 300000 (seed 200000), want 300000", got)
	}
}

// TestCatalogModelsCtxMaxObservedBelowSeedKeepsSeed is the reverse
// of the above: when the adapter observes a SMALLER ctx_max than the
// pricing seed, the catalog row must keep the seed value (MAX wins,
// never decrease). This guards against a regression where the
// observed value silently overwrites the seed.
func TestCatalogModelsCtxMaxObservedBelowSeedKeepsSeed(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "anthropic:/tmp", "aiagent_v3", "/tmp")

	pr, err := pricing.New()
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	// claude-opus-4-7 seeds at 1_000_000 — larger than the observed
	// 200_000 below.
	w := newWriter("anthropic:/tmp", "aiagent_v3", "/tmp", pr)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "anthropic:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "anthropic:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "anthropic", Model: "claude-opus-4-7",
	})
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		// Finalized.Ts == EndTs (the op END), spec-conformant: a finalize sorts
		// after its OpStarted (Ts 1100), so its own Ts is the end, not the start.
		EventBase:       canonical.EventBase{SourceID: "anthropic:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		EndTs: 1200, Status: "completed",
		CtxMax: 200000,
	})
	_ = tx.Commit()

	got := scanInt(t, db, `SELECT COALESCE(ctx_max, 0) FROM catalog_models WHERE provider='anthropic' AND name='claude-opus-4-7'`)
	if got != 1000000 {
		t.Errorf("catalog_models.ctx_max = %d after observing 200000 (seed 1000000), want 1000000 (MAX wins)", got)
	}
}

// TestCatalogModelsCtxMaxObservedZeroKeepsSeed proves an OpFinalized
// with CtxMax == 0 (the "not recorded" sentinel — see writer.go:472
// NULLIF(?, 0)) does NOT overwrite the seeded value. The CASE WHEN
// gate on `ev.CtxMax > 0` is what enforces this; without the gate, a
// finalize event with zero would zero the catalog column.
func TestCatalogModelsCtxMaxObservedZeroKeepsSeed(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")

	pr, err := pricing.New()
	if err != nil {
		t.Fatalf("pricing.New: %v", err)
	}
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", pr)

	tx, _ := db.BeginTx(ctx, nil)
	_ = w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	_ = w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "anthropic", Model: "claude-3-5-sonnet",
	})
	// CtxMax: 0 == "adapter did not observe a ctx_max for this op"
	// per writer.go:472 NULLIF semantics.
	_ = w.apply(ctx, tx, canonical.OpFinalizedEvent{
		// Finalized.Ts == EndTs (the op END), spec-conformant: a finalize sorts
		// after its OpStarted (Ts 1100), so its own Ts is the end, not the start.
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		EndTs: 1200, Status: "completed",
		CtxMax: 0,
	})
	_ = tx.Commit()

	got := scanInt(t, db, `SELECT COALESCE(ctx_max, 0) FROM catalog_models WHERE provider='anthropic' AND name='claude-3-5-sonnet'`)
	if got != 200000 {
		t.Errorf("catalog_models.ctx_max = %d after observing 0 (seed 200000), want 200000 (zero must not overwrite)", got)
	}
}

// TestCatalogModelsCtxMaxSkippedWithNopPricer pins the optional-
// interface contract: NopPricer does NOT implement MetadataPricer
// (it only implements the base Pricer), so the catalog seeding path
// is a no-op. This guards against an accidental NopPricer satisfying
// MetadataPricer in the future, which would route every test
// through a no-op seed and mask real misses.
func TestCatalogModelsCtxMaxSkippedWithNopPricer(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")

	if _, ok := Pricer(NopPricer{}).(MetadataPricer); ok {
		t.Fatalf("NopPricer must not satisfy MetadataPricer; adding CtxMax would silently route every test through a no-op seed and mask real seeding bugs")
	}
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
		Provider: "anthropic", Model: "claude-3-5-sonnet",
	})
	_ = tx.Commit()

	got := scanInt(t, db, `SELECT COALESCE(ctx_max, 0) FROM catalog_models WHERE provider='anthropic' AND name='claude-3-5-sonnet'`)
	if got != 0 {
		t.Errorf("catalog_models.ctx_max = %d with NopPricer, want 0 (NopPricer cannot satisfy MetadataPricer)", got)
	}
}
