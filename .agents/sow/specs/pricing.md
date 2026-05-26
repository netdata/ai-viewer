# Pricing Data

## TL;DR

Per-model price data is **embedded into the binary at build time** via `go:embed`. Runtime makes zero outbound network calls. Prices are refreshed by a shell script (`scripts/refresh-pricing.sh`) that invokes an external CLI AI tool to look up current public pricing and writes back to the data file; updates land via PR with visible diffs.

This satisfies two non-negotiables: (a) AGENTS.md invariant "No outbound network calls" — ingester and server never reach an external API; (b) operator decision 2026-05-26 that pricing should be maintainable via a shell-script flow using CLI AI tools.

## Storage

The pricing data lives at:

```
internal/pricing/pricing.json
```

Embedded into `ai-viewer-ingest` (where cost is computed from `ops`) via `go:embed`:

```go
package pricing

import _ "embed"

//go:embed pricing.json
var pricingJSONBytes []byte
```

The file is read once at process startup and parsed into an in-memory lookup; it is **never re-read at runtime**. Updating prices requires rebuilding the binary.

## Schema (JSON)

```json
{
  "version": 1,
  "schema_url": "https://github.com/netdata/ai-viewer/blob/master/internal/pricing/pricing.schema.json",
  "generated_at": "2026-05-26T00:00:00Z",
  "generated_by": "manual seed",
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
          "prices": {
            "input_per_million": 15.00,
            "output_per_million": 75.00,
            "cache_read_per_million": 1.50,
            "cache_write_5m_per_million": 18.75,
            "cache_write_1h_per_million": 30.00
          },
          "effective_date": "2026-05-01",
          "citation_url": "https://docs.anthropic.com/en/docs/about-claude/pricing",
          "notes": "Cache pricing differentiates 5-minute and 1-hour TTL writes."
        }
      ]
    }
  ]
}
```

### Field semantics

| Field | Type | Required | Meaning |
|---|---|---|---|
| `version` | int | ✓ | Schema version. Bumped on breaking change; minor additive fields do not bump. |
| `schema_url` | string | ✓ | Stable URL pointing at the JSON Schema for this file (lives at `internal/pricing/pricing.schema.json`). |
| `generated_at` | RFC3339 string | ✓ | When the file was last regenerated. |
| `generated_by` | string | ✓ | `"manual seed"` (initial commit) or the CLI AI tool name from `scripts/refresh-pricing.sh` (e.g. `"codex 0.125"`). |
| `currency` | string | ✓ | ISO 4217 code. Always `USD` in v1. A future SOW adds multi-currency. |
| `providers[]` | array | ✓ | One entry per provider canonical name (`anthropic`, `openai`, `google`, `openrouter`, etc.). |
| `providers[].name` | string | ✓ | Canonical provider name. Matches `ops.provider` exactly. |
| `providers[].aliases[]` | array<string> | optional | Lowercase synonyms. Resolution treats aliases as equivalent to `name`. |
| `providers[].models[]` | array | ✓ | One entry per known model. |
| `providers[].models[].name` | string | ✓ | Canonical model name. Matches `ops.model` exactly. |
| `providers[].models[].aliases[]` | array<string> | optional | Synonyms (versioned suffixes, marketing aliases). |
| `providers[].models[].ctx_max` | int | optional | Model max context window in tokens. Seeds `catalog_models.ctx_max`. |
| `providers[].models[].prices` | object | ✓ | Per-unit prices in USD per million tokens. See below. |
| `providers[].models[].effective_date` | YYYY-MM-DD | ✓ | When this price tier took effect (per the citation URL). |
| `providers[].models[].citation_url` | string | ✓ | Public URL where this price can be verified. |
| `providers[].models[].notes` | string | optional | Free-form per-model note (cache TTL semantics, regional variations, etc.). |

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
| `input_per_million` | ✓ | Price per 1,000,000 input tokens. |
| `output_per_million` | ✓ | Price per 1,000,000 output tokens. |
| `cache_read_per_million` | optional | Price per 1,000,000 cache-hit tokens (replays). |
| `cache_write_per_million` | optional | Default cache-write price when TTL is not differentiated. |
| `cache_write_5m_per_million` | optional | Anthropic 5-minute ephemeral cache write. |
| `cache_write_1h_per_million` | optional | Anthropic 1-hour ephemeral cache write. |
| `reasoning_per_million` | optional | Some providers price reasoning tokens distinctly from output (e.g. OpenAI o-series). Falls back to `output_per_million`. |

Missing optional fields signal "this provider does not differentiate that price tier" and the cost calculator falls back to the next-coarser field.

## Resolution Algorithm (Cost Computation)

For each `op` of `kind='llm'`:

1. Resolve provider: case-insensitive match against `providers[].name` or `providers[].aliases[]`.
2. Resolve model: case-insensitive match against `providers[].models[].name` or `providers[].models[].aliases[]`.
3. If both resolve: compute `cost_usd` from the op's token counts × the provider/model price block.
4. If either resolution fails: emit a `SourceError` event with severity `WRN` (`"unknown pricing: provider=<p>, model=<m>"`) and set `cost_usd = 0.0`. The op still ingests; the operator sees the unknown-pricing warning in the Sources panel.
5. Unknown-pricing warnings are deduplicated by `(provider, model)` so the log isn't spammed across millions of ops.

### Token-to-cost formula

```
cost_usd =
    (tokens_in              × input_per_million        / 1_000_000)
  + (tokens_out             × output_per_million       / 1_000_000)
  + (tokens_cache_read      × cache_read_per_million   / 1_000_000)
  + (tokens_cache_write_5m  × cache_write_5m_per_million / 1_000_000)
  + (tokens_cache_write_1h  × cache_write_1h_per_million / 1_000_000)
  + (reasoning_tokens       × reasoning_per_million    / 1_000_000)
```

Where the operand defaults to `0` when the source did not record it. Cached writes default to `cache_write_per_million` if a TTL-specific field is absent.

### Source-recorded cost takes precedence

When a source already provides `cost_usd` per-op (ai-agent v3, ai-agent v2, opencode), the adapter passes it through; the pricing table is **not** consulted. The pricing table is used only when source cost is missing — primarily for claude-code and codex.

## Refresh Script Contract

`scripts/refresh-pricing.sh` is the maintenance entry point. It is run by an operator (manually, ad-hoc) — not at build, install, or runtime.

### Inputs

```bash
scripts/refresh-pricing.sh [--tool=<claude|codex|gemini|opencode|custom>] [--dry-run]
```

- `--tool` selects which CLI AI tool to invoke. Default: try `claude` then `codex` then `gemini` then `opencode`, use the first available.
- `--dry-run` runs the tool and produces the candidate output but does not write back to `pricing.json`.

### Behavior

1. Read current `internal/pricing/pricing.json`.
2. Construct a prompt asking the CLI tool to look up the most recent public pricing for the providers/models present in the file, plus any provider/model the operator passes via additional arguments. The tool MUST cite a URL for every price.
3. Validate the tool's response: must be JSON, must match the schema, every `prices` block must have at least `input_per_million` and `output_per_million`, every model must have a `citation_url`.
4. Diff against the current file (`git diff --no-index pricing.json proposed-pricing.json`) and print the diff to the terminal.
5. Prompt the operator: `apply changes? (yes/no)`. If yes, write back to `pricing.json`. If no, exit without write.
6. Update `generated_at`, `generated_by`, but **do not auto-commit**. Operator commits via their normal flow with the visible diff.

### Failure modes

- Tool produces invalid JSON → script prints validation errors and exits non-zero without write.
- Tool's response is identical to current file → script exits zero with `no changes` message.
- Tool unavailable (binary not on PATH) → script falls through to the next tool in the default order; if none works, exits non-zero with an installation hint.
- Tool succeeds but lacks `citation_url` for some model → schema validation fails; operator told which model lacks citation.

### What the script does NOT do

- Never makes outbound HTTP requests directly. All network access is via the CLI tool the operator chose to invoke.
- Never auto-commits. Diff visibility is the operator's review gate.
- Never reads or modifies any file outside `internal/pricing/` and a temporary scratch directory under `$TMPDIR`.

## Initial Seed (Phase 1)

The initial `pricing.json` is hand-seeded with the providers/models the operator's existing sessions reference. Discovery query at `pricing-seed-time`:

```sql
SELECT DISTINCT provider, model
FROM ops
WHERE kind='llm' AND provider IS NOT NULL AND model IS NOT NULL;
```

The seed includes at minimum: every (provider, model) pair seen in the operator's 17K v3 sessions and 294K v2 sessions during the Phase 1 backfill. Run the discovery query, then populate `pricing.json` from public pricing pages.

Phase 1 acceptance: zero unknown-pricing warnings on the operator's backfill (every observed `(provider, model)` resolves to a price entry).

## JSON Schema File

A formal JSON Schema lives at `internal/pricing/pricing.schema.json`. The schema is referenced by:

- `pricing.json`'s top-level `schema_url` field (informational).
- `scripts/refresh-pricing.sh`'s validation step (it loads the schema and runs `jq` or `ajv` validation on the candidate output).
- A Go-side test at `internal/pricing/pricing_test.go` that loads `pricing.json` at test time and asserts it validates against the schema.

## Cross-References

- `.agents/sow/specs/canonical-events.md` — `OpFinalizedEvent.CostUSD` field.
- `.agents/sow/specs/data-model.md` — `ops.cost_usd`, `turns.cost_usd`, `sessions.cost_usd`, `catalog_models`.
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` — Chunk 10 ("Pricing data + refresh script") implements this spec.
- `AGENTS.md` — "No outbound network calls" invariant.
