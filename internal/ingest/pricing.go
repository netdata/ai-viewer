package ingest

// Pricer computes a USD cost for an LLM op when the source did not
// record one. Implementations are stateless and safe for concurrent
// use. See .agents/sow/specs/pricing.md for the data shape behind the
// real implementation (internal/pricing landed in Chunk 10).
//
// Cost is invoked only when the adapter-supplied CostUSD is zero;
// see writer.go applyOpFinalized.
type Pricer interface {
	// Cost returns the computed cost in USD for the given provider/model
	// and token counts. tsUS is the op's start timestamp in
	// UNIX-microseconds (UTC); pricers that carry temporal tiers use it
	// to pick the price tier that was in effect when the session ran, so
	// historical sessions get historical prices. Pricers that lack
	// pricing data for (provider, model, tsUS) must return 0 so the
	// caller can surface "unknown pricing" warnings via the standard
	// channel.
	Cost(provider, model string, tsUS int64, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64
}

// DetailedPricer is an optional extension of Pricer that returns the
// same cost plus a hit flag and a miss-kind string the writer uses to
// emit a deduped SourceError WRN when pricing data is unavailable
// (pricing.md §"Temporal resolution algorithm"). Pricers that do not
// implement this interface fall back to silently returning 0 on miss,
// and the writer skips the warning — preserving the pre-iteration-2
// behaviour for synthetic fakes that don't care about miss kinds.
type DetailedPricer interface {
	Pricer
	// CostWithDetail returns the cost plus (hit=true on success, false
	// on miss) and a stable miss-kind string when hit=false. Miss-kind
	// strings are defined by the concrete pricer; the writer treats
	// them as opaque dedup keys.
	CostWithDetail(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) (cost float64, hit bool, missKind string)
}

// MetadataPricer is an optional extension of Pricer that exposes
// per-(provider, model) metadata seeds — currently the
// context-window size declared in the embedded pricing table. The
// catalog writer queries this in onOpStarted so a new
// catalog_models row gets a non-zero ctx_max even when the adapter
// does not record one on the canonical event. The op's own CtxMax
// (recorded on OpFinalized) still takes precedence on subsequent
// updates.
//
// pricing.md §"Field semantics" declares this seeding behaviour.
// Pricers that do not implement MetadataPricer (e.g. NopPricer)
// cause the catalog seed to skip silently — matching the pre-iter-8
// behaviour and keeping the test surface stable.
//
// Iter-8 fix iter8-4 (codex iter-7 P2#4).
type MetadataPricer interface {
	Pricer
	// CtxMax returns the seeded context-window size for a (provider,
	// model) pair. Second return is true on a hit, false when the
	// pair is unknown or the table has no ctx_max for it.
	CtxMax(provider, model string) (int64, bool)
}

// NopPricer is the default Pricer wired into the ingester until Chunk 11
// swaps it for *pricing.Pricer in the production binary. It returns 0
// unconditionally, so adapter-supplied costs (ai-agent v3 ops with cost
// recorded, opencode messages with data.cost) flow through unchanged and
// ops without recorded cost stay at zero — visible in the UI as
// "cost unknown".
type NopPricer struct{}

// Cost implements Pricer for NopPricer.
func (NopPricer) Cost(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64 {
	_ = provider
	_ = model
	_ = tsUS
	_ = tokensIn
	_ = tokensOut
	_ = tokensCacheRead
	_ = tokensCacheWrite
	return 0
}
