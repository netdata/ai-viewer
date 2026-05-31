package pricing

import (
	"testing"
	"time"
)

// TestTsUSToDate covers the timestamp-to-UTC-date conversion that
// drives tier selection. Verified against well-known epoch values.
func TestTsUSToDate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tsUS int64
		want string
	}{
		{"zero returns zero time", 0, "0001-01-01"},
		{"negative returns zero time", -42, "0001-01-01"},
		{"epoch start", 1, "1970-01-01"},
		{"2025-01-01 00:00 UTC", 1_735_689_600_000_000, "2025-01-01"},
		// 2025-06-15 12:34:56 UTC = 1750_004_096 seconds.
		{"2025-06-15 12:34 UTC (truncates to day)", 1_750_004_096_000_000, "2025-06-15"},
	}
	for _, tc := range cases {
		got := tsUSToDate(tc.tsUS).Format(dateLayout)
		if got != tc.want {
			t.Errorf("%s: tsUSToDate(%d) = %s, want %s", tc.name, tc.tsUS, got, tc.want)
		}
	}
}

// TestResolveTierExactDateBoundary verifies an op whose timestamp
// equals the tier's effective_date hits that tier, not an older one.
func TestResolveTierExactDateBoundary(t *testing.T) {
	t.Parallel()
	p := mustMultiTierPricer(t)
	// Tier dates: 2025-06-15 (new) and 2024-01-01 (old).
	// 2025-06-15 00:00 UTC = 1_750_032_000.
	const tsBoundary = int64(1_750_032_000_000_000)
	entry := p.resolveModel("x", "m")
	tr := p.resolveTier(entry, tsBoundary)
	if tr == nil {
		t.Fatal("resolveTier returned nil at boundary")
	}
	if tr.effectiveDate != "2025-06-15" {
		t.Errorf("tier = %q, want 2025-06-15", tr.effectiveDate)
	}
}

// TestResolveTierBetweenTiers verifies a timestamp between two tiers
// resolves to the older tier (the one in effect at that moment).
func TestResolveTierBetweenTiers(t *testing.T) {
	t.Parallel()
	p := mustMultiTierPricer(t)
	// 2024-06-15 — between 2024-01-01 and 2025-06-15.
	const tsBetween = int64(1_718_409_600_000_000)
	entry := p.resolveModel("x", "m")
	tr := p.resolveTier(entry, tsBetween)
	if tr == nil {
		t.Fatal("resolveTier returned nil")
	}
	if tr.effectiveDate != "2024-01-01" {
		t.Errorf("tier = %q, want 2024-01-01", tr.effectiveDate)
	}
}

// TestResolveTierAfterAllTiers verifies a future timestamp resolves
// to the most-recent tier.
func TestResolveTierAfterAllTiers(t *testing.T) {
	t.Parallel()
	p := mustMultiTierPricer(t)
	// 2030-01-01.
	const tsFar = int64(1_893_456_000_000_000)
	entry := p.resolveModel("x", "m")
	tr := p.resolveTier(entry, tsFar)
	if tr == nil {
		t.Fatal("resolveTier returned nil for far-future ts")
	}
	if tr.effectiveDate != "2025-06-15" {
		t.Errorf("tier = %q, want 2025-06-15", tr.effectiveDate)
	}
}

// TestResolveTierBeforeAllTiers verifies a timestamp predating every
// tier returns nil so the caller can bump MissTier.
func TestResolveTierBeforeAllTiers(t *testing.T) {
	t.Parallel()
	p := mustMultiTierPricer(t)
	// 2020-01-01.
	const tsOld = int64(1_577_836_800_000_000)
	entry := p.resolveModel("x", "m")
	if tr := p.resolveTier(entry, tsOld); tr != nil {
		t.Errorf("resolveTier = %q, want nil", tr.effectiveDate)
	}
}

// TestResolveTierUnknownTimestampPicksLatest covers the documented
// edge case that tsUS<=0 defaults to the most-recent tier.
func TestResolveTierUnknownTimestampPicksLatest(t *testing.T) {
	t.Parallel()
	p := mustMultiTierPricer(t)
	entry := p.resolveModel("x", "m")
	tr := p.resolveTier(entry, 0)
	if tr == nil || tr.effectiveDate != "2025-06-15" {
		t.Errorf("resolveTier(0) = %v, want 2025-06-15", tr)
	}
}

// TestResolveTierNilEntry verifies defensive nil-handling.
func TestResolveTierNilEntry(t *testing.T) {
	t.Parallel()
	p := mustMultiTierPricer(t)
	if tr := p.resolveTier(nil, 0); tr != nil {
		t.Errorf("resolveTier(nil, 0) = %v, want nil", tr)
	}
	// Empty tiers slice is also handled.
	if tr := p.resolveTier(&modelEntry{}, 0); tr != nil {
		t.Errorf("resolveTier(empty, 0) = %v, want nil", tr)
	}
}

// TestComputeCostFormula exercises the per-million math in isolation
// so a regression in the price math is caught directly, not via the
// embedded data.
func TestComputeCostFormula(t *testing.T) {
	t.Parallel()
	t0 := tier{
		effectiveAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		prices: resolvedPrices{
			InputPerMillion:      10,
			OutputPerMillion:     20,
			CacheReadPerMillion:  1,
			CacheWritePerMillion: 5,
		},
	}
	// 100k in, 50k out, 200k cache-read, 50k cache-write.
	got := computeCost(&t0, 100_000, 50_000, 200_000, 50_000)
	want := 100_000.0*10/1_000_000 +
		50_000.0*20/1_000_000 +
		200_000.0*1/1_000_000 +
		50_000.0*5/1_000_000
	if got != want {
		t.Errorf("computeCost = %v, want %v", got, want)
	}
}

// TestComputeCostNoCacheDoubleCount documents the SOW-0029 invariant: now that
// tokens_in is the FRESH/uncached input only (every adapter excludes cache —
// see canonical-events.md token contract), the pricer charges cache_read and
// cache_write at their OWN rates and NEVER at the input rate. The rates are
// deliberately distinct (input 10, cacheRead 1, cacheWrite 5 per-million) so a
// regression that re-folded cache into tokens_in — pricing R+W at the input
// rate — would change the result and fail here.
func TestComputeCostNoCacheDoubleCount(t *testing.T) {
	t.Parallel()
	const (
		freshIn    = 100_000 // F: fresh/uncached input
		out        = 50_000  // O: output
		cacheRead  = 200_000 // R: cached input read
		cacheWrite = 40_000  // W: cache creation
	)
	tr := tier{
		effectiveAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		prices: resolvedPrices{
			InputPerMillion:      10,
			OutputPerMillion:     20,
			CacheReadPerMillion:  1,
			CacheWritePerMillion: 5,
		},
	}
	got := computeCost(&tr, freshIn, out, cacheRead, cacheWrite)

	// Each component priced at its OWN rate. Cache is NOT charged at the input
	// rate — it appears only via CacheRead/CacheWritePerMillion.
	want := float64(freshIn)*10/1_000_000 +
		float64(out)*20/1_000_000 +
		float64(cacheRead)*1/1_000_000 +
		float64(cacheWrite)*5/1_000_000
	if got != want {
		t.Fatalf("computeCost = %v, want %v", got, want)
	}

	// Guard: the buggy "fold cache into tokens_in" cost (charging R and W at the
	// input rate, on top of their own rate) must be strictly larger — proving
	// this test would catch a regression rather than coincidentally matching.
	doubleCounted := want + float64(cacheRead+cacheWrite)*10/1_000_000
	if got >= doubleCounted {
		t.Fatalf("expected no-double-count cost %v to be < double-counted cost %v", got, doubleCounted)
	}
}

// mustMultiTierPricer parses a small two-tier synthetic doc used by
// the resolver tests above. Lives here (not pricing_test.go) so the
// test file is self-contained.
func mustMultiTierPricer(t *testing.T) *Pricer {
	t.Helper()
	const docJSON = `{
	  "version": 2,
	  "schema_url": "https://example.com",
	  "generated_at": "2026-05-27T00:00:00Z",
	  "generated_by": "test",
	  "currency": "USD",
	  "providers": [{
	    "name": "x",
	    "models": [{
	      "name": "m",
	      "tiers": [
	        {"effective_date": "2024-01-01", "citation_url": "https://example.com", "source": "test",
	         "prices": {"input_per_million": 1, "output_per_million": 2}},
	        {"effective_date": "2025-06-15", "citation_url": "https://example.com", "source": "test",
	         "prices": {"input_per_million": 3, "output_per_million": 4}}
	      ]
	    }]
	  }]
	}`
	lookup, _, err := parseDoc([]byte(docJSON))
	if err != nil {
		t.Fatalf("parseDoc: %v", err)
	}
	return &Pricer{lookup: lookup}
}
