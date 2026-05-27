package pricing

import (
	_ "embed"
	"sync/atomic"
)

// pricingJSONBytes is the embedded pricing data parsed at New() time.
// Updating prices requires rebuilding the binary; see pricing.md and
// scripts/refresh-pricing.sh for the operator workflow.
//
//go:embed pricing.json
var pricingJSONBytes []byte

// Pricer carries the embedded pricing tables and lookup map. New()
// returns a fully-initialised *Pricer; the value is safe for
// concurrent use because the lookup map is read-only after
// construction and Stats() counters use atomic adds.
//
// Pricer satisfies the ingest.Pricer interface (verified via a
// compile-time assertion in pricing_integration_test.go under the
// ingest package so this package does not import ingest, avoiding the
// import cycle).
type Pricer struct {
	lookup map[modelKey]*modelEntry

	// Counters are bumped lock-free on every Cost call. They feed the
	// Sources panel via Stats() so the operator sees which (provider,
	// model, tsUS) tuples were not priced.
	hits                atomic.Int64
	missProviderModel   atomic.Int64
	missTier            atomic.Int64
	defaultedLatestTier atomic.Int64
}

// Stats reports the running lookup counters. Reads are lock-free and
// the returned struct is a snapshot, not a live view.
type Stats struct {
	// Hits is the number of Cost calls that resolved to a tier and
	// applied prices.
	Hits int64
	// MissProviderModel is the number of Cost calls that could not
	// resolve the (provider, model) pair against the embedded table.
	MissProviderModel int64
	// MissTier is the number of Cost calls that resolved the (provider,
	// model) pair but had no tier whose effective_date was <= the op's
	// timestamp. This is the "session predates the earliest tier we
	// know about" case.
	MissTier int64
	// DefaultedLatestTier is the number of Cost calls where the caller
	// passed tsUS<=0 (timestamp unknown) so the pricer defaulted to
	// the most-recent tier. Distinguishes "real temporal match" from
	// "defaulted to now" in the Sources panel.
	DefaultedLatestTier int64
}

// Miss kinds reported by CostWithDetail. These string constants are
// stable so the writer's SourceError dedup map can key on them and the
// log_entries rows have a parseable category.
const (
	// MissNone signals a successful lookup; cost was computed.
	MissNone = ""
	// MissUnknownProviderModel signals the (provider, model) pair did
	// not resolve against the embedded table.
	MissUnknownProviderModel = "unknown_provider_model"
	// MissUnknownTier signals the (provider, model) pair resolved but
	// the op's timestamp predates every known tier.
	MissUnknownTier = "unknown_tier"
)

// New constructs a Pricer from the embedded pricing.json. It returns
// an error if the data fails schema validation; in production this
// should never happen because pricing_test.go runs the same
// validation in CI before the binary ever ships.
func New() (*Pricer, error) {
	lookup, _, err := parseDoc(pricingJSONBytes)
	if err != nil {
		return nil, err
	}
	return &Pricer{lookup: lookup}, nil
}

// Cost implements the temporal-tier Pricer. The signature matches
// ingest.Pricer.Cost exactly so *Pricer can be assigned to that
// interface.
//
// Resolution order: provider+model (case-insensitive, alias-aware) →
// tier (most-recent whose effective_date <= ts). On miss returns 0
// and bumps the appropriate counter so Stats() reports actionable
// numbers.
//
// Callers that need to surface unknown-pricing warnings should use
// CostWithDetail instead — it returns the same cost plus a hit flag
// and a miss kind string the caller can use to dedup SourceError
// emission.
func (p *Pricer) Cost(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64 {
	cost, _, _ := p.CostWithDetail(provider, model, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite)
	return cost
}

// CostWithDetail computes the cost and reports lookup detail so the
// caller (writer) can emit a deduped SourceError WRN per
// pricing.md §"Temporal resolution algorithm". A nil miss kind ==
// MissNone means the lookup hit. A non-empty miss kind names the
// failure mode so the writer's dedup map can key on (sourceID,
// provider, model, missKind).
func (p *Pricer) CostWithDetail(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) (cost float64, hit bool, missKind string) {
	entry := p.resolveModel(provider, model)
	if entry == nil {
		p.missProviderModel.Add(1)
		return 0, false, MissUnknownProviderModel
	}
	t, defaulted := p.resolveTierDetail(entry, tsUS)
	if t == nil {
		p.missTier.Add(1)
		return 0, false, MissUnknownTier
	}
	if defaulted {
		p.defaultedLatestTier.Add(1)
	}
	p.hits.Add(1)
	return computeCost(t, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite), true, MissNone
}

// Stats returns a snapshot of the running counters.
func (p *Pricer) Stats() Stats {
	return Stats{
		Hits:                p.hits.Load(),
		MissProviderModel:   p.missProviderModel.Load(),
		MissTier:            p.missTier.Load(),
		DefaultedLatestTier: p.defaultedLatestTier.Load(),
	}
}

// CtxMax returns the seeded context-window size for a (provider,
// model) pair when the pricing table carries one. The second return
// is true on a hit, false when the pair is unknown or has no
// ctx_max declared.
//
// Per pricing.md §"Field semantics" the schema's `ctx_max` field
// seeds `catalog_models.ctx_max`. The ingester calls this from
// catalogWriter.onOpStarted so a new (provider, model) row gets a
// non-zero context-window value even when the adapter does not
// record CtxMax on the OpStartedEvent. The op's own CtxMax (recorded
// on OpFinalized) still takes precedence on subsequent updates.
// Iter-8 fix iter8-4 (codex iter-7 P2#4).
func (p *Pricer) CtxMax(provider, model string) (int64, bool) {
	entry := p.resolveModel(provider, model)
	if entry == nil {
		return 0, false
	}
	if entry.ctxMax <= 0 {
		return 0, false
	}
	return entry.ctxMax, true
}
