# Pricing Data

## TL;DR

Per-model price data is **embedded into the binary at build time** via `go:embed`. Runtime makes zero outbound network calls. Each model carries one or more **time-banded price tiers** (`effective_date` + `prices`) so historical sessions are priced with the tier that was in effect when they ran. Prices are refreshed by an operator-runnable shell script that pulls from LiteLLM's community-maintained JSON (primary) + OpenRouter's API (cross-check) + AI CLI tools (fallback for brand-new models). Updates land via PR with visible diffs.

## Why this design (not runtime fetch)

1. **Temporal correctness.** ai-viewer shows historical sessions. A session that ran when Opus cost $15/M tokens must show $15/M, even if the price is $12/M today. Runtime fetch returns "now" and silently misreports historical cost. The static table with per-tier `effective_date` is the only design that preserves this.
2. **Air-gapped operation.** Workstation tool; operator may be offline (plane, captive portal, SCIF). Runtime fetch hangs or fails on startup; static binary always works.
3. **SOC2 / reproducibility.** Same binary + same data → same cost numbers, always. Auditable.
4. **Supply chain.** A compromise of LiteLLM/OpenRouter shouldn't silently change costs in deployed binaries. PR-reviewed commits are auditable.
5. **Test determinism.** No network = no flaky tests, no mocks.

## Storage

The pricing data lives at:

```
internal/pricing/pricing.json
```

Embedded into both `ai-viewer-ingest` (where cost is computed from `ops`) and any future cost-recompute tool via `go:embed`:

```go
package pricing

import _ "embed"

//go:embed pricing.json
var pricingJSONBytes []byte
```

The file is read once at process startup and parsed into an in-memory lookup; it is **never re-read at runtime**. Updating prices requires rebuilding the binary.

JSON Schema lives at `internal/pricing/pricing.schema.json` and is referenced both by `pricing.json`'s top-level `schema_url` field (informational) and by the refresh script's validation step + a Go-side test in `internal/pricing/pricing_test.go`.

## Schema (JSON)

The example below shows the live first-tier prices for `claude-opus-4-7` exactly as carried in `internal/pricing/pricing.json` at the time of writing (2026-01-01 tier per Anthropic's published pricing). The second tier (`2025-08-01`, `$20/$90`) is **illustrative only** — it demonstrates how an older tier is preserved alongside a newer one when prices change, and the numbers are not a real historical Anthropic price.

```json
{
  "version": 2,
  "schema_url": "https://github.com/netdata/ai-viewer/blob/master/internal/pricing/pricing.schema.json",
  "generated_at": "2026-05-27T00:00:00Z",
  "generated_by": "scripts/refresh-pricing.sh --source=litellm",
  "currency": "USD",
  "providers": [
    {
      "name": "anthropic",
      "aliases": ["claude"],
      "models": [
        {
          "name": "claude-opus-4-7",
          "aliases": ["claude-opus-4.7", "claude-opus-4-7-20260101"],
          "ctx_max": 1000000,
          "tiers": [
            {
              "effective_date": "2026-01-01",
              "citation_url": "https://docs.anthropic.com/en/docs/about-claude/pricing",
              "source": "litellm",
              "prices": {
                "input_per_million":          5.00,
                "output_per_million":         25.00,
                "cache_read_per_million":      0.50,
                "cache_write_5m_per_million":  6.25,
                "cache_write_1h_per_million": 10.00,
                "reasoning_per_million":      25.00
              }
            },
            {
              "effective_date": "2025-08-01",
              "citation_url":   "https://web.archive.org/web/2025...",
              "source":         "manual_archive",
              "prices": {
                "input_per_million":  20.00,
                "output_per_million": 90.00
              }
            }
          ]
        }
      ]
    }
  ]
}
```

### Field semantics

| Field | Type | Required | Meaning |
|---|---|---|---|
| `version` | int | ✓ | Schema version. v2 introduces tiers. v1 (deprecated; never embedded since the chunk shipped v2 directly) is **rejected** by the loader (`internal/pricing/loader.go:140`) with a descriptive error; no auto-migration path exists or is planned. If the loader ever encounters v1, the operator runs `scripts/refresh-pricing.sh` to regenerate from current vendor data. |
| `schema_url` | string | ✓ | Informational pointer to the JSON Schema. |
| `generated_at` | RFC3339 | ✓ | Wall-clock when the file was regenerated. |
| `generated_by` | string | ✓ | The exact command + arg that produced this file. |
| `currency` | string | ✓ | ISO 4217. Always `USD` in v2. Future SOW adds multi-currency. |
| `providers[]` | array | ✓ | One entry per canonical provider name. |
| `providers[].name` | string | ✓ | Canonical provider name. Must match `ops.provider` exactly after case-fold. |
| `providers[].aliases[]` | optional | Lowercase synonyms (e.g. `claude` for `anthropic`). |
| `providers[].models[].name` | string | ✓ | Canonical model name. Must match `ops.model` exactly after case-fold. |
| `providers[].models[].aliases[]` | optional | Versioned suffixes, marketing names. |
| `providers[].models[].ctx_max` | int | optional | Max context window. Seeds `catalog_models.ctx_max`. |
| `providers[].models[].tiers[]` | array | ✓ | Time-banded price tiers. **Must contain at least one tier.** Sorted by `effective_date` descending at load time. |
| `providers[].models[].tiers[].effective_date` | YYYY-MM-DD | ✓ | The date this price tier took effect. |
| `providers[].models[].tiers[].citation_url` | string | ✓ | Public URL verifying this tier. Web archive URLs acceptable for older tiers. |
| `providers[].models[].tiers[].source` | string | ✓ | Refresh source: `litellm` / `openrouter` / `manual_archive` / `cli:<tool>` (e.g., `cli:codex`). |
| `providers[].models[].tiers[].prices` | object | ✓ | Per-unit prices in USD per million tokens. |

### Prices block

```json
"prices": {
  "input_per_million":          15.00,
  "output_per_million":         75.00,
  "cache_read_per_million":      1.50,
  "cache_write_per_million":    18.75,
  "cache_write_5m_per_million": 18.75,
  "cache_write_1h_per_million": 30.00,
  "reasoning_per_million":      75.00
}
```

| Field | Required | Meaning |
|---|---|---|
| `input_per_million` | ✓ | USD per 1,000,000 input tokens. |
| `output_per_million` | ✓ | USD per 1,000,000 output tokens. |
| `cache_read_per_million` | optional | USD per 1,000,000 cache-hit tokens (replays). |
| `cache_write_per_million` | optional | Default cache-write price when TTL is not differentiated. |
| `cache_write_5m_per_million` | optional | Anthropic 5-minute ephemeral cache write. |
| `cache_write_1h_per_million` | optional | Anthropic 1-hour ephemeral cache write. |
| `reasoning_per_million` | optional | Some providers price reasoning tokens distinctly (OpenAI o-series). Falls back to `output_per_million`. |

Missing optional fields mean "this provider does not differentiate that price tier"; the calculator falls back to the next-coarser field.

## Temporal resolution algorithm

The Pricer's `Cost` method takes the op's start timestamp (the value
persisted in `ops.start_ts`, in UNIX-microseconds UTC). For each LLM
op:

1. Resolve provider: case-insensitive match against `providers[].name` or `providers[].aliases[]`.
2. Resolve model: case-insensitive match against `providers[].models[].name` or `providers[].models[].aliases[]`.
3. Pick the **first** tier in `tiers[]` (sorted descending at load) where `effective_date <= ops.start_ts` (date comparison, UTC). Using start_ts (not the finalize timestamp) prices an op against the tier that was in effect when it STARTED — matching every vendor's billing model and giving the correct answer for ops that straddle a price-change date.
4. If both resolution + tier selection succeed: compute `cost_usd` from the op's token counts × the tier's prices.
5. If either step fails: emit a `SourceError` event severity `WRN`, deduped per `(provider, model, missKind)`, bump `sources.parse_errors` once per deduped tuple, set `cost_usd = 0`. The op still ingests; the operator sees the unknown-pricing warning in the Sources panel through the same counter the adapter `SourceError` path uses.

### Token-to-cost formula

```
cost_usd =
    (tokens_in              × input_per_million          / 1_000_000)
  + (tokens_out             × output_per_million         / 1_000_000)
  + (tokens_cache_read      × cache_read_per_million     / 1_000_000)
  + (tokens_cache_write     × cache_write_per_million    / 1_000_000)

# Deferred — schema-ready; not yet applied by computeCost. These
# terms land when the canonical OpFinalizedEvent grows the matching
# token fields (currently TokensCacheWrite is one undifferentiated
# int64 and there is no ReasoningTokens field). The schema retains
# the per-million fields so the refresh script can carry them
# forward; Cost() ignores them today.
  + (tokens_cache_write_5m  × cache_write_5m_per_million / 1_000_000)
  + (tokens_cache_write_1h  × cache_write_1h_per_million / 1_000_000)
  + (reasoning_tokens       × reasoning_per_million      / 1_000_000)
```

Operands default to `0` when not recorded. Cache writes use the single
`cache_write_per_million` field today; the TTL-differentiated 5m/1h
fields are stored in the schema for forward-compatibility but not
applied by `computeCost` until the canonical event grows
`TokensCacheWrite5m` / `TokensCacheWrite1h`. Likewise
`reasoning_per_million` is schema-ready but not yet applied because
the canonical event has no `ReasoningTokens` field.

### Source-recorded cost takes precedence

When a source provides `cost_usd` per-op (ai-agent v3, v2, opencode), the adapter passes it through; the pricing table is not consulted. The table is used only when source cost is missing — primarily claude-code and codex.

## Pricer Go types

The Chunk 10 implementation lives in `internal/pricing`. `Pricer` is a
struct (not an interface) carrying the embedded pricing tables + the
lookup map + atomic counters; the corresponding INTERFACE seam lives in
`internal/ingest` (see `ingester.md` §"Cost Computation") and the
production `*pricing.Pricer` satisfies it. Keeping the concrete type out
of `internal/ingest` avoids an import cycle and lets the ingester
default to `NopPricer{}` when constructed from tests.

```go
package pricing

// Pricer carries the embedded pricing tables and lookup map. New() returns
// a fully-initialised *Pricer; the value is safe for concurrent use.
//
// *Pricer satisfies internal/ingest.Pricer AND internal/ingest.DetailedPricer
// (compile-time assertion in internal/ingest/pricing_integration_test.go).
type Pricer struct { /* lookup + atomic counters */ }

// New constructs a Pricer from the embedded pricing.json. Returns an
// error if the embedded data fails schema validation.
func New() (*Pricer, error)

// Cost is the plain temporal-tier signature. tsUS<=0 means "timestamp
// unknown" and the pricer defaults to the most-recent tier (bumping
// the DefaultedLatestTier counter so the Sources panel distinguishes
// "real temporal match" from "defaulted to now").
func (p *Pricer) Cost(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64

// CostWithDetail returns the cost plus a hit flag and a stable
// miss-kind string the writer uses to emit a deduped SourceError WRN.
// Miss kinds are MissNone ("" on success), MissUnknownProviderModel,
// and MissUnknownTier — see internal/pricing/pricing.go.
func (p *Pricer) CostWithDetail(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) (cost float64, hit bool, missKind string)

// CtxMax returns the seeded context-window size for a (provider,
// model) pair. The catalog writer queries this in onOpStarted so a
// new catalog_models row gets a non-zero ctx_max even when the
// adapter does not record one on the canonical event. Hit=false
// means the pair is unknown or the table has no ctx_max declared
// (most testdata fakes). Iter-8 fix iter8-4.
//
// Per data-model.md §"catalog_models" the pricing seed is a FLOOR,
// not a ceiling: on OpFinalized the catalog writer applies
// MAX(existing, ev.CtxMax) when ev.CtxMax > 0, so an adapter that
// observes a larger context window still updates the catalog
// (iter-9 fix iter9-1; ev.CtxMax == 0 means "not recorded" per
// writer.go's NULLIF(?, 0) convention and never overwrites).
func (p *Pricer) CtxMax(provider, model string) (int64, bool)

// Stats reports running lookup counters consumed by the Sources panel.
// Reads are lock-free; the returned struct is a snapshot.
func (p *Pricer) Stats() Stats

type Stats struct {
    Hits                int64 // tier resolved + prices applied
    MissProviderModel   int64 // (provider, model) not in the table
    MissTier            int64 // pair resolved but tsUS predates every tier
    DefaultedLatestTier int64 // tsUS<=0 so the most-recent tier was used
}
```

The `internal/ingest.Pricer` interface (defined in
`internal/ingest/pricing.go`) carries the same `Cost` signature so
`*pricing.Pricer` can be assigned to it directly. The richer
`internal/ingest.DetailedPricer` (also satisfied by `*pricing.Pricer`)
adds `CostWithDetail` so the writer can route pricing-miss WRN events
through `emitPricingMiss` (writer.go) deduped per
`(sourceID, provider, model, missKind)`. The
`internal/ingest.MetadataPricer` (also satisfied by
`*pricing.Pricer`, iter-8 fix iter8-4) adds `CtxMax(provider, model)`
so `catalogWriter.onOpStarted` can seed `catalog_models.ctx_max`
from the embedded pricing table. `NopPricer` in `internal/ingest`
deliberately does NOT implement `DetailedPricer` OR
`MetadataPricer` — that keeps test fixtures that wire a do-nothing
pricer from accidentally emitting miss events or fabricating ctx_max
seeds.

Chunk 11 wires `pricing.New()` into the production binary by passing
the returned `*Pricer` to `ingest.New(... ingest.Pricer ...)`. The
ingester needs no further changes between Chunk 7 (NopPricer wired) and
Chunk 11 (real Pricer wired) — the constructor argument is the only
boundary that moves.

## Refresh script: `scripts/refresh-pricing.sh`

Operator-runnable; never invoked at build, install, or runtime.

### CLI

```bash
scripts/refresh-pricing.sh \
  [--source=litellm|openrouter|cli:<tool>|all]  # default: all
  [--db=<path>]                                  # SQLite to drive seed list; default ~/.local/share/ai-viewer/ingest.db
  [--out=<path>]                                 # default: internal/pricing/pricing.json
  [--dry-run]                                    # produce proposed JSON; don't write
  [--allow-partial]                              # opt in to writing pricing.json that silently omits seeds with no pricing data
  [--add-provider=<name>] [--add-model=<name>]   # extend the seed list beyond what's in the DB
```

### Sources

Layered in this order; later sources fill gaps the earlier ones missed:

1. **LiteLLM (primary).** `https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json` — community-maintained, 2,700+ entries with provider tagging, cache pricing, context windows. No auth, no rate limit. Snapshot via plain `curl -fsSL` (`scripts/refresh-pricing.sh:250`); no caching layer is used. Re-fetching is cheap enough on every run, and a stale cache would mask real price changes the operator is running the script to detect. The script always fetches fresh data.

2. **OpenRouter (cross-check).** `https://openrouter.ai/api/v1/models` — 350+ models, reflects aggregator pricing (provider price + OR's margin). Use to sanity-check LiteLLM numbers; flag drift > 20% per metric.

3. **AI CLI fallback** (`--source=cli:<tool>`). Reserved for brand-new models neither LiteLLM nor OpenRouter has yet. **Currently a clean-fail stub** at `scripts/refresh-pricing.sh:288`: when the operator passes `--source=cli:codex` (or any other `cli:*`), the script exits with a clear "not yet implemented" error rather than producing a half-built result. Rationale: LiteLLM + OpenRouter together cover well over 99% of realistic models in operator sessions, so CLI fallback is tail coverage and not blocking for Phase 1. A follow-up SOW will implement this if the gap becomes material.

The script does NOT make outbound network requests directly except for the LiteLLM curl + the OpenRouter API call. No telemetry, no analytics, no auth tokens.

### Behaviour

1. Build the "models we care about" list from the operator's local database via:
   ```sql
   SELECT DISTINCT provider, model FROM ops
   WHERE kind='llm' AND provider <> '' AND model <> '';
   ```
   Plus any `--add-provider` / `--add-model` extensions.

2. For each (provider, model):
   - Look up in the LiteLLM JSON.
   - Look up in OpenRouter.
   - If both: compare and warn on drift > 20%; record the LiteLLM tier (provider-direct) as authoritative.
   - If only one: use it, record its source.
   - If neither: collect into "unknown" set. The `--source=cli:<tool>` fallback is currently a clean-fail stub (see §"Sources" above and `scripts/refresh-pricing.sh:288`); unknowns are reported and the script exits non-zero rather than producing a half-built record.

3. Build the proposed `pricing.json`. For each model, **preserve existing older tiers** (don't overwrite history) and prepend a new tier with today's `effective_date` if the prices differ from the most-recent existing tier.

4. Validate the proposed JSON. Two checkers run in series: (a) `scripts/lib/pricing-validate.jq` invoked via `jq -e -f` from the refresh script's `validate_proposed` step (`scripts/refresh-pricing.sh:322-325`) — a schema-equivalent filter that enforces every structural invariant the JSON Schema declares (type checks, `additionalProperties:false`, calendar-valid `effective_date` including leap-year handling, case-fold uniqueness for providers/models); (b) the Go loader's `validateDoc` at load time (`internal/pricing/loader.go`), which re-checks the same invariants after `json.Unmarshal` and acts as the runtime safety net for the embedded `pricing.json`. There is **no** separate Go validator binary; the loader's check + the jq filter together cover every assertion the schema makes.

5. Print `git diff --no-index pricing.json proposed-pricing.json` to the terminal.

6. Prompt the operator: `apply changes? (yes/no)`. If yes, write back. If no, exit without write.

7. Update `generated_at` + `generated_by`; **do not auto-commit**. Operator commits via the normal PR flow with the visible diff.

### Failure modes (script exits non-zero, no write)

- LiteLLM JSON returns non-200 or parse fails.
- OpenRouter API returns non-200 or parse fails (unless `--source=litellm` is set explicitly, in which case OR failure is ignored).
- **One or more requested (provider, model) seeds had no pricing data in any source.** The script exits BEFORE the validate/diff/prompt step and lists every missing pair. Operator opts in to writing a partial file with `--allow-partial`; without the flag, "I would have approved that if I had noticed it" is impossible. (`scripts/refresh-pricing.sh` ALLOW_PARTIAL gate.)
- Schema validation fails.
- The discovery query against the DB returns 0 rows AND no `--add-*` flags supplied.

### What the script does NOT do

- Never makes outbound HTTP requests for unrelated endpoints (no telemetry).
- Never auto-commits. The diff is the review gate.
- Never reads or modifies any file outside `internal/pricing/`, a `$TMPDIR` scratch, and the read-only ingest DB.

## Initial seed

The first `pricing.json` is hand-seeded for the providers/models the operator's existing sessions reference. The discovery query at Chunk 10 commit time:

```sql
-- Run after Chunk 11+ wires the full pipeline and a backfill has occurred.
SELECT DISTINCT provider, model
FROM ops
WHERE kind='llm' AND provider <> '' AND model <> ''
ORDER BY provider, model;
```

For Chunk 10 itself (before the full pipeline is wired), the seed list is built from:
- The synthetic events the v2/v3 adapter test fixtures emit (providers + models referenced in `testdata/aiagent_v[23]/**/expected.jsonl`).
- A hand-list of common providers/models the operator's workstation is known to use (anthropic claude-*, openai gpt-*, google gemini-*, etc.).
- The `--add-provider` / `--add-model` mechanism for anything else needed.

**Chunk 10 acceptance**: zero unknown-pricing warnings on the v2 backfill (Chunk 9 ran 294,316 sessions in 2:11; rerun against a small subset and assert no `SourceError` events with the `unknown pricing` message).

## Cross-references

- `.agents/sow/specs/canonical-events.md` — `OpFinalizedEvent.CostUSD` field.
- `.agents/sow/specs/data-model.md` — `ops.cost_usd`, `turns.cost_usd`, `sessions.cost_usd`, `catalog_models.ctx_max`.
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` — Chunk 10 implements this spec.
- `internal/ingest/pricing.go` — interface seam landed in Chunk 7 (will be updated in Chunk 10 to add the timestamp argument).
- `AGENTS.md` — "No outbound network calls" invariant (preserved at runtime; refresh script is operator-runnable and outside the invariant).
