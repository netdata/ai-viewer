// Package pricing computes per-op USD cost for LLM calls when the source
// snapshot did not record one. Provider/model pricing is shipped inside
// the binary at build time via go:embed (no outbound network calls at
// runtime). Each (provider, model) carries one or more time-banded
// price tiers — a tier's effective_date is the date the tier took
// effect, and the pricer picks the most-recent tier whose
// effective_date is <= the op's timestamp so historical sessions are
// priced with the tier that was in effect when they ran.
//
// Data shape, schema, refresh script, and operator policy live in
// .agents/sow/specs/pricing.md. The schema definition lives at
// internal/pricing/pricing.schema.json and is exercised by
// pricing_test.go on every test run; the embedded data is at
// internal/pricing/pricing.json.
//
// # Resolution order
//
//  1. Provider lookup: case-insensitive match against
//     providers[].name or providers[].aliases[].
//  2. Model lookup: same, within the resolved provider's models[].
//  3. Tier selection: linear scan of the DESC-sorted tiers[] slice,
//     returning the first tier whose effective_date <= tsUS (UTC date).
//
// # Edge cases
//
//   - Unknown provider or model: returns 0 cost and bumps an
//     "unknown provider/model" miss counter exposed via Stats(). The
//     ingester surfaces the miss via the Sources panel.
//   - tsUS <= 0 (the source didn't record a timestamp): returns the
//     most-recent tier (tiers[0] after DESC sort). Rationale: "unknown
//     when" defaults to "now" — better than zero cost.
//   - tsUS predates every tier: returns 0 cost and bumps an
//     "unknown tier (before earliest)" miss counter.
//
// # Concurrency
//
// Pricer is safe for concurrent use. New() does all parsing under the
// constructor; the lookup tables are read-only after construction.
// Stats() counters use atomic adds; reading them is lock-free.
//
// # Forward compatibility
//
// The pricing.json schema (v2) carries cache_write_5m_per_million,
// cache_write_1h_per_million, and reasoning_per_million fields. The
// current Cost() signature takes a single tokensCacheWrite int64 (the
// Chunk 7 seam) and no separate reasoning_tokens parameter, so the
// pricer applies cache_write_per_million only and ignores the TTL
// split + reasoning prices for now. The schema is forward-compatible:
// adding richer parameters to Cost() later does not require a schema
// change. See pricing.md §"Token-to-cost formula".
package pricing
