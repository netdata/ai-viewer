package ingest

// Pricer computes a USD cost for an LLM op when the source did not
// record one. Implementations are stateless and safe for concurrent
// use. See .agents/sow/specs/pricing.md for the data shape behind the
// real implementation (Chunk 10).
//
// Cost is invoked only when the adapter-supplied CostUSD is zero;
// see writer.go applyOpFinalized.
type Pricer interface {
	// Cost returns the computed cost in USD for the given provider/model
	// and token counts. The implementation must return 0 when pricing
	// data for the (provider, model) tuple is unknown so the caller can
	// surface "unknown pricing" warnings via the standard channel.
	Cost(provider, model string, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64
}

// NopPricer is the default Pricer used by New until Chunk 10 wires the
// real pricing-table implementation. It returns 0 unconditionally, so
// adapter-supplied costs (ai-agent v3 ops with cost recorded, opencode
// messages with data.cost) flow through unchanged and ops without
// recorded cost stay at zero — visible in the UI as "cost unknown".
type NopPricer struct{}

// Cost implements Pricer for NopPricer.
func (NopPricer) Cost(provider, model string, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64 {
	_ = provider
	_ = model
	_ = tokensIn
	_ = tokensOut
	_ = tokensCacheRead
	_ = tokensCacheWrite
	return 0
}
