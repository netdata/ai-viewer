package pricing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedDataLoads asserts the binary-shipped pricing.json parses
// cleanly. If this ever fails CI catches it before the bad data is
// embedded into a release.
func TestEmbeddedDataLoads(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(p.lookup) == 0 {
		t.Fatal("New returned empty lookup map")
	}
}

// TestEmbeddedDataCoversFixtureModels asserts every (provider, model)
// pair appearing in our test fixtures resolves against the embedded
// table. The list is built dynamically by scanning every
// `testdata/aiagent_v[23]/*/expected.jsonl` file at test time, so any
// new fixture that references an unseeded model fails CI before the
// adapter pipeline can silently produce zero-cost ops for it. The
// repo root is two levels up from this test file
// (`internal/pricing/`); the lookup is resilient to test invocation
// directory because go test runs with cwd == the package dir.
func TestEmbeddedDataCoversFixtureModels(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pairs := fixtureProviderModelPairs(t)
	if len(pairs) == 0 {
		t.Fatal("no (provider, model) pairs found in testdata/aiagent_v[23]; the test file globbing is broken")
	}
	for _, w := range pairs {
		if p.resolveModel(w.provider, w.model) == nil {
			t.Errorf("seed pricing.json missing (provider=%q model=%q) referenced in testdata fixtures", w.provider, w.model)
		}
	}
}

// fixtureProviderModelPairs scans every `expected.jsonl` under the
// repo's testdata/aiagent_v[23] tree and returns the unique
// (provider, model) pairs extracted from canonical event payloads.
func fixtureProviderModelPairs(t *testing.T) []struct{ provider, model string } {
	t.Helper()
	// Tests run with cwd == package dir; the repo root is two parents
	// up. We allow the lookup to fail silently and return an empty
	// slice if testdata is not present (the calling test treats that
	// as a hard failure).
	repoRoot := filepath.Join("..", "..")
	roots := []string{
		filepath.Join(repoRoot, "testdata", "aiagent_v2"),
		filepath.Join(repoRoot, "testdata", "aiagent_v3"),
	}
	seen := make(map[[2]string]struct{})
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Logf("skipping %s: %v", root, err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name(), "expected.jsonl")
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var ev struct {
					Payload struct {
						Provider string `json:"Provider"`
						Model    string `json:"Model"`
					} `json:"payload"`
				}
				if err := json.Unmarshal([]byte(line), &ev); err != nil {
					continue
				}
				if ev.Payload.Provider == "" || ev.Payload.Model == "" {
					continue
				}
				seen[[2]string{
					strings.ToLower(ev.Payload.Provider),
					strings.ToLower(ev.Payload.Model),
				}] = struct{}{}
			}
		}
	}
	out := make([]struct{ provider, model string }, 0, len(seen))
	for pp := range seen {
		out = append(out, struct{ provider, model string }{pp[0], pp[1]})
	}
	return out
}

// TestCostHappyPath exercises the full resolution path for a known
// (provider, model, ts) triple. Numbers were derived from the
// embedded table so the test stays in sync if the seed is refreshed.
func TestCostHappyPath(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// claude-3-5-sonnet 2024-06-20 tier: input=$3/M, output=$15/M,
	// cache_read=$0.30/M, cache_write=$3.75/M.
	// 1_000_000 us → 1970-01-01 UTC, before the tier; use a 2025 ts.
	// 2025-01-01 00:00 UTC = 1735689600 seconds = 1_735_689_600_000_000 us.
	const ts2025 = int64(1_735_689_600_000_000)
	got := p.Cost("anthropic", "claude-3-5-sonnet", ts2025, 1_000_000, 500_000, 0, 0)
	want := 3.0 + 7.5
	if got != want {
		t.Errorf("Cost = %v, want %v", got, want)
	}
	stats := p.Stats()
	if stats.Hits != 1 {
		t.Errorf("Stats.Hits = %d, want 1", stats.Hits)
	}
	if stats.MissProviderModel != 0 || stats.MissTier != 0 {
		t.Errorf("Stats had unexpected misses: %+v", stats)
	}
}

// TestCostCaseInsensitive verifies upper/mixed-case provider+model
// strings still resolve. Adapters sometimes pass through whatever
// casing the source recorded.
func TestCostCaseInsensitive(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const ts2025 = int64(1_735_689_600_000_000)
	got := p.Cost("Anthropic", "Claude-3-5-Sonnet", ts2025, 1_000_000, 0, 0, 0)
	if got != 3.0 {
		t.Errorf("case-insensitive Cost = %v, want 3.0", got)
	}
}

// TestCostAliasExpansion verifies a provider alias (claude →
// anthropic) and a model alias (gpt4o → gpt-4o) both resolve.
func TestCostAliasExpansion(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const ts2025 = int64(1_735_689_600_000_000)
	// Provider alias.
	if c := p.Cost("claude", "claude-3-5-sonnet", ts2025, 1_000_000, 0, 0, 0); c != 3.0 {
		t.Errorf("provider-alias Cost = %v, want 3.0", c)
	}
	// Model alias.
	if c := p.Cost("openai", "gpt4o", ts2025, 1_000_000, 0, 0, 0); c != 2.50 {
		t.Errorf("model-alias Cost = %v, want 2.50", c)
	}
}

// TestCostMissProviderModel verifies the (provider, model) miss path
// returns 0 and bumps the right counter.
func TestCostMissProviderModel(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c := p.Cost("madeup", "doesnotexist", 1_735_689_600_000_000, 1, 1, 0, 0); c != 0 {
		t.Errorf("miss Cost = %v, want 0", c)
	}
	if s := p.Stats(); s.MissProviderModel != 1 {
		t.Errorf("Stats.MissProviderModel = %d, want 1 (%+v)", s.MissProviderModel, s)
	}
}

// TestCostEmptyInputs verifies empty provider or model resolves as a
// (provider, model) miss without crashing. This matches the writer's
// observation that an op may have neither field set if the source
// snapshot did not populate them.
func TestCostEmptyInputs(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c := p.Cost("", "", 1, 100, 100, 0, 0); c != 0 {
		t.Errorf("empty inputs Cost = %v, want 0", c)
	}
	if c := p.Cost("anthropic", "", 1, 100, 100, 0, 0); c != 0 {
		t.Errorf("empty model Cost = %v, want 0", c)
	}
}

// TestCostMissTier verifies a tsUS that predates every tier returns 0
// and bumps the tier miss counter.
func TestCostMissTier(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 2020-01-01 — before every tier in the seed.
	const ts2020 = int64(1_577_836_800_000_000)
	if c := p.Cost("anthropic", "claude-3-5-sonnet", ts2020, 1_000_000, 0, 0, 0); c != 0 {
		t.Errorf("pre-tier Cost = %v, want 0", c)
	}
	if s := p.Stats(); s.MissTier != 1 {
		t.Errorf("Stats.MissTier = %d, want 1 (%+v)", s.MissTier, s)
	}
}

// TestCostUnknownTimestampDefaultsToLatest verifies tsUS <= 0 picks
// the most-recent tier instead of failing.
func TestCostUnknownTimestampDefaultsToLatest(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// claude-3-5-sonnet has a single tier in the seed; tsUS=0 should
	// hit it.
	if c := p.Cost("anthropic", "claude-3-5-sonnet", 0, 1_000_000, 0, 0, 0); c != 3.0 {
		t.Errorf("tsUS=0 Cost = %v, want 3.0", c)
	}
	if c := p.Cost("anthropic", "claude-3-5-sonnet", -1, 1_000_000, 0, 0, 0); c != 3.0 {
		t.Errorf("tsUS=-1 Cost = %v, want 3.0", c)
	}
	if s := p.Stats(); s.DefaultedLatestTier != 2 {
		t.Errorf("Stats.DefaultedLatestTier = %d, want 2", s.DefaultedLatestTier)
	}
}

// TestCostWithDetailReportsMissKinds verifies the detail API surfaces
// the correct miss-kind string for each branch so the writer can dedup
// per (provider, model, missKind).
func TestCostWithDetailReportsMissKinds(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Unknown provider/model.
	if c, hit, miss := p.CostWithDetail("madeup", "doesnotexist", 1_735_689_600_000_000, 1, 1, 0, 0); c != 0 || hit || miss != MissUnknownProviderModel {
		t.Errorf("unknown provider/model: cost=%v hit=%v miss=%q, want 0/false/%q", c, hit, miss, MissUnknownProviderModel)
	}
	// Known model but ts predates every tier.
	const ts2020 = int64(1_577_836_800_000_000)
	if c, hit, miss := p.CostWithDetail("anthropic", "claude-3-5-sonnet", ts2020, 1, 1, 0, 0); c != 0 || hit || miss != MissUnknownTier {
		t.Errorf("pre-tier: cost=%v hit=%v miss=%q, want 0/false/%q", c, hit, miss, MissUnknownTier)
	}
	// Hit.
	const ts2025 = int64(1_735_689_600_000_000)
	if c, hit, miss := p.CostWithDetail("anthropic", "claude-3-5-sonnet", ts2025, 1_000_000, 0, 0, 0); c != 3.0 || !hit || miss != MissNone {
		t.Errorf("hit: cost=%v hit=%v miss=%q, want 3.0/true/empty", c, hit, miss)
	}
}

// TestCostCacheTokensCounted verifies cache-read and cache-write
// tokens contribute to the total via the per-million fields.
func TestCostCacheTokensCounted(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const ts2025 = int64(1_735_689_600_000_000)
	// claude-3-5-sonnet: cache_read=$0.30/M, cache_write=$3.75/M.
	got := p.Cost("anthropic", "claude-3-5-sonnet", ts2025, 0, 0, 1_000_000, 1_000_000)
	want := 0.30 + 3.75
	if got != want {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

// TestCostMissingOptionalPriceFieldYieldsZero verifies that an
// unrecorded optional field (e.g. cache_read for gpt-4-turbo) yields
// zero for that token class without misattributing it elsewhere.
func TestCostMissingOptionalPriceFieldYieldsZero(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const ts2025 = int64(1_735_689_600_000_000)
	// gpt-4-turbo in the seed has only input + output prices.
	got := p.Cost("openai", "gpt-4-turbo", ts2025, 0, 0, 1_000_000, 1_000_000)
	if got != 0 {
		t.Errorf("Cost = %v, want 0 (no cache prices)", got)
	}
}

// TestTiersSortedDescending verifies buildLookup leaves tiers in
// DESC-by-effective-date order so resolveTier can stop on first hit.
// We construct a synthetic doc with two tiers and assert ordering.
func TestTiersSortedDescending(t *testing.T) {
	t.Parallel()
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
	entry := lookup[modelKey{provider: "x", model: "m"}]
	if entry == nil {
		t.Fatal("missing entry")
	}
	if len(entry.tiers) != 2 {
		t.Fatalf("len(tiers) = %d, want 2", len(entry.tiers))
	}
	if entry.tiers[0].effectiveDate != "2025-06-15" {
		t.Errorf("tiers[0] = %q, want 2025-06-15", entry.tiers[0].effectiveDate)
	}
	if entry.tiers[1].effectiveDate != "2024-01-01" {
		t.Errorf("tiers[1] = %q, want 2024-01-01", entry.tiers[1].effectiveDate)
	}
}

// TestStatsAccumulatesAcrossCalls verifies counters accumulate, not
// reset, across multiple Cost calls.
func TestStatsAccumulatesAcrossCalls(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const ts2025 = int64(1_735_689_600_000_000)
	p.Cost("anthropic", "claude-3-5-sonnet", ts2025, 1, 1, 0, 0)
	p.Cost("nope", "nope", ts2025, 1, 1, 0, 0)
	p.Cost("anthropic", "claude-3-5-sonnet", 1_577_836_800_000_000, 1, 1, 0, 0)
	s := p.Stats()
	if s.Hits != 1 || s.MissProviderModel != 1 || s.MissTier != 1 {
		t.Errorf("Stats = %+v, want hits=1 missProvider=1 missTier=1", s)
	}
}

// TestCtxMaxReturnsSeedForKnownModel exercises iter-8 fix iter8-4:
// the Pricer exposes the embedded ctx_max as a seed for
// catalog_models.ctx_max. The catalog writer reads this via the
// MetadataPricer optional interface (internal/ingest/pricing.go).
//
// claude-3-5-sonnet is in the seeded pricing.json with ctx_max=200000.
func TestCtxMaxReturnsSeedForKnownModel(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, hit := p.CtxMax("anthropic", "claude-3-5-sonnet")
	if !hit {
		t.Fatalf("CtxMax hit = false, want true (seeded model)")
	}
	if got <= 0 {
		t.Errorf("CtxMax = %d, want > 0", got)
	}
}

// TestCtxMaxAliasResolution proves alias lookup works through CtxMax,
// matching the Cost() alias semantics. The pricing seed declares
// `claude` as an alias for `anthropic`; both forms must yield the
// same ctx_max so a session that records the alias is not silently
// stripped of its ctx_max seed.
func TestCtxMaxAliasResolution(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	canonical, _ := p.CtxMax("anthropic", "claude-3-5-sonnet")
	aliased, hit := p.CtxMax("claude", "claude-3-5-sonnet")
	if !hit {
		t.Fatalf("CtxMax(alias) hit = false, want true")
	}
	if canonical != aliased {
		t.Errorf("alias mismatch: canonical=%d alias=%d", canonical, aliased)
	}
}

// TestCtxMaxMissReturnsFalse pins the miss contract: unknown
// (provider, model) returns 0 + false so the catalog writer can
// distinguish "seed me" from "no seed available" without testing for
// a sentinel value.
func TestCtxMaxMissReturnsFalse(t *testing.T) {
	t.Parallel()
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, hit := p.CtxMax("fake-vendor-99", "fake-model-impossible"); hit || got != 0 {
		t.Errorf("CtxMax(unknown) = (%d, %v), want (0, false)", got, hit)
	}
	if got, hit := p.CtxMax("", ""); hit || got != 0 {
		t.Errorf("CtxMax(empty) = (%d, %v), want (0, false)", got, hit)
	}
}

// TestSchemaURLPointsAtRepo is a smoke check that schema_url stays in
// sync with the file we ship.
func TestSchemaURLPointsAtRepo(t *testing.T) {
	t.Parallel()
	_, doc, err := parseDoc(pricingJSONBytes)
	if err != nil {
		t.Fatalf("parseDoc: %v", err)
	}
	if !strings.Contains(doc.SchemaURL, "pricing.schema.json") {
		t.Errorf("schema_url = %q, want it to reference pricing.schema.json", doc.SchemaURL)
	}
}
