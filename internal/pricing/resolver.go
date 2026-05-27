package pricing

import (
	"strings"
	"time"
)

// tsUSToDate converts a UNIX-microseconds timestamp into the UTC date
// portion as time.Time at 00:00:00 UTC. The resolveTier comparison is
// then a strict same-second compare against effectiveAt (also at
// 00:00:00 UTC). tsUS values <= 0 yield a zero time.Time so callers
// can short-circuit to "latest tier" without parsing.
func tsUSToDate(tsUS int64) time.Time {
	if tsUS <= 0 {
		return time.Time{}
	}
	secs := tsUS / 1_000_000
	nsec := (tsUS % 1_000_000) * 1_000
	t := time.Unix(secs, nsec).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// resolveModel returns the modelEntry for a (provider, model) pair, or
// nil if neither name+aliases nor model+aliases match. Lookup is
// case-insensitive.
func (p *Pricer) resolveModel(provider, model string) *modelEntry {
	if provider == "" || model == "" {
		return nil
	}
	key := modelKey{
		provider: strings.ToLower(provider),
		model:    strings.ToLower(model),
	}
	entry, ok := p.lookup[key]
	if !ok {
		return nil
	}
	return entry
}

// resolveTier picks the most-recent tier whose effective_date is <=
// the op's UTC date. The tiers slice is sorted DESC by effectiveAt at
// load time (see buildLookup), so the first hit wins.
//
// Edge case: tsUS <= 0 means the source did not record an event
// timestamp. The pricer defaults to tiers[0] (the most-recent) on the
// rationale that "unknown when" is best treated as "now" — better
// than silently returning 0. Callers that want strict tier matching
// can pass a real timestamp.
//
// Edge case: tsUS predates every tier's effectiveAt. Returns nil so
// the caller can bump the "unknown tier" miss counter.
func (p *Pricer) resolveTier(entry *modelEntry, tsUS int64) *tier {
	t, _ := p.resolveTierDetail(entry, tsUS)
	return t
}

// resolveTierDetail mirrors resolveTier but also signals whether the
// "tsUS<=0 defaulted to latest tier" branch fired. The caller
// (CostWithDetail) bumps the dedicated stat counter when defaulted is
// true so the Sources panel can distinguish real temporal matches
// from "unknown when" fallbacks.
func (p *Pricer) resolveTierDetail(entry *modelEntry, tsUS int64) (t *tier, defaulted bool) {
	if entry == nil || len(entry.tiers) == 0 {
		return nil, false
	}
	if tsUS <= 0 {
		return &entry.tiers[0], true
	}
	want := tsUSToDate(tsUS)
	for i := range entry.tiers {
		if !entry.tiers[i].effectiveAt.After(want) {
			return &entry.tiers[i], false
		}
	}
	return nil, false
}

// computeCost applies the per-million prices to the token counts.
// All operands default to 0 when the source did not record them, and
// optional price fields default to 0 when the provider does not
// differentiate that token class (anthropic vs openai shapes differ).
//
// The current canonical OpFinalizedEvent carries a single
// tokensCacheWrite int64 (the Chunk 7 seam) and no separate
// reasoning_tokens. We apply cache_write_per_million and ignore the
// finer TTL split + reasoning prices; the schema retains those fields
// for forward-compatibility once the canonical event grows the
// corresponding inputs. See pricing.md §"Token-to-cost formula" for
// the full formula the schema is designed to support.
func computeCost(t *tier, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64 {
	pr := t.prices
	return float64(tokensIn)*pr.InputPerMillion/1_000_000 +
		float64(tokensOut)*pr.OutputPerMillion/1_000_000 +
		float64(tokensCacheRead)*pr.CacheReadPerMillion/1_000_000 +
		float64(tokensCacheWrite)*pr.CacheWritePerMillion/1_000_000
}
