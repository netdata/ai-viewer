# SOW-0029 - canonical tokens_in semantics + claude-code cache-fold fix + cache UI

## Status

Status: in-progress

Sub-state: gate ready; on branch `sow-0029-token-cost-semantics` (off master). Spec deltas landed (canonical-events.md + data-model.md); delegating the audit/fix/UI build. Operator-approved 2026-05-31 ("Fix data+cost AND cache UI now"), recorded before code. Discovered during the SOW-0006 real-data review: the operator's own claude-code session showed `tokens_in = 3.84B` and `cost_usd = $9,377`, both inflated. Root-caused to the claude-code adapter folding cached tokens into `tokens_in`, which (a) inflates the token total and (b) double-counts cache in the pricer. To be done on its own branch + PR (independent, fast merge — a correctness bug, not bundled into the long SOW-0006 frontend).

## Requirements

### Purpose

Make `tokens_in` mean the same thing across every adapter — **fresh/uncached input tokens** — with cache as separate first-class counters, so token totals and costs are correct and consistent, and surface the cache breakdown (read/write + cache-hit-rate) in the UI. The cache-hit ratio is a key agent-efficiency/cost metric and must be visible.

### User Request

Operator, viewing real claude-code data in the UI: "something is wrong with tokens in. This session shows 3.8 billion. Also I don't see cached tokens anywhere." Decision (AskUserQuestion, 2026-05-31): **"Fix data+cost AND cache UI now"** — fix the token/cost mapping + canonical definition AND add the cache display to the UI in this pass.

### Assistant Understanding

Facts (verified against code + live real data):

- `internal/adapters/claude_code/ops.go:253` sets `ev.TokensIn = InputTokens + CacheCreationInputTokens + CacheReadInputTokens` (TOTAL input incl. cache), and `ev.CtxUsed = ev.TokensIn + ev.TokensOut`.
- The other four adapters set `tokens_in` = the source's **fresh** input, cache separate: `aiagent_v3/ops.go:111` (`acc.TokensIn`), `aiagent_v2/mapper.go:378` (`acc.Tokens.InputTokens`), `opencode/mapper_*.go` (`delta.Input`), `codex/mapper_turn.go:252` (`ts.tokensIn`). ai-agent v3 (the primary source) records `tokensIn` and `tokensCacheRead` as separate fields → `tokens_in` is fresh there.
- A real claude-code session (the operator's largest, ~61 MB / ~11,900 ops): `tokens_in=3,839,269,251`, `cache_read=3,787,214,325` (98.6% of `tokens_in`), `cache_write=50,973,960`; per op `in=999,668 ≈ cache_read=997,539` (fresh input ≈ 2 tokens/turn).
- `internal/pricing/resolver.go:91` `computeCost` = `tokensIn×InputPerMillion + tokensOut×OutputPerMillion + tokensCacheRead×CacheReadPerMillion + tokensCacheWrite×CacheWritePerMillion`. With claude-code's `tokens_in` containing `cache_read`, cache is charged at BOTH the full input rate (via `tokens_in`) and the cache rate → over-charge. Active: session `cost_usd=$9,377` (12,392 of 26,025 llm ops priced).
- The canonical meaning of `tokens_in` is NOT documented (`canonical-events.md:127` `TokensIn int64` has no fresh-vs-total comment; `data-model.md:67` `tokens_in INTEGER` no comment) — which let the claude-code outlier go unnoticed.
- Cache IS captured (`tokens_cache_read`/`tokens_cache_write` columns; claude-code also stashes `uncachedInput` in op extras). The frontend has ZERO `cache` references — the UI never displays it.

Inferences:

- Canonical definition (principled + Anthropic-billing-aligned + already true for 4/5 adapters): **`tokens_in` = fresh/uncached input tokens**; `tokens_cache_read` = cached input read; `tokens_cache_write` = cache creation; `tokens_out` = output; total input processed = `tokens_in + cache_read + cache_write`; `ctx_used` = total context occupancy (= total input + output), NOT `tokens_in + tokens_out`.
- claude-code fix: `ev.TokensIn = u.InputTokens`; `ev.CtxUsed = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.OutputTokens` (preserve true context occupancy). `TokensCacheRead`/`TokensCacheWrite` unchanged. This makes `tokens_in` fresh + removes the pricing double-count.
- Backfill: the index.db is disposable/re-ingestable; re-ingesting claude-code sources after the fix corrects historical rows (cost is pricer-derived, so a SQL backfill cannot recompute cost — re-ingest is the correct repair). Document this.

Unknowns:

- Whether any adapter OTHER than claude-code also mis-maps (audit all 5 on pickup; ai-agent v3 appears correct but confirm `acc.TokensIn` is the source's fresh input, not total).

### Acceptance Criteria

1. Canonical `tokens_in` semantics are documented (fresh input; cache separate; total = sum; `ctx_used` = total context). **Verification**: `canonical-events.md` + `data-model.md` updated; spec-drift sweep clean.
2. The claude-code adapter sets `tokens_in` = fresh input and `ctx_used` = total context. **Verification**: a claude-code golden/unit test with non-zero `cache_read`/`cache_write` asserts `tokens_in == InputTokens`, `cache_read`/`cache_write` populated, `ctx_used == input+cacheCreate+cacheRead+output`.
3. No cost double-count: a pricing test with cache asserts `cost = fresh×inputRate + cache_read×cacheReadRate + cache_write×cacheWriteRate + out×outputRate` (cache NOT charged at the input rate). **Verification**: pricing test.
4. Cross-adapter audit: all 5 adapters conform to `tokens_in` = fresh; any other outlier fixed or documented. **Verification**: audit note in the SOW + tests.
5. UI/API: the session **Overview tab** displays the cache breakdown (`cache_read`, `cache_write`) and a cache-hit-rate (`cache_read / (tokens_in + cache_read + cache_write)`) with `tokens_in` labeled as fresh input; the **`/api/stats` totals response** (backend) exposes `tokens_cache_read/write`. The cross-session Stats *page* (UI) does not exist yet — it is Phase-2 (SOW-0007) and is out of scope here. **Verification**: frontend tests assert the Overview cache fields/ratio render; a presenter/REST test asserts `/api/stats` totals include the cache fields; manual check on re-ingested real data.
6. All quality gates green; external review converged.

## Analysis

Sources checked: all 5 adapters' token mapping; `internal/pricing/resolver.go`; live real-data DB; `canonical-events.md`/`data-model.md`; frontend Overview/Stats (no cache display). Risks:

- **R1 — Cross-cutting.** Touches canonical semantics (spec), 1+ adapters, pricing interaction, catalog rollups, and UI. Mitigation: the change is "fix the outlier to the documented definition" (4/5 already conform); full gate + external review.
- **R2 — Backfill.** Existing DBs have wrong claude-code `tokens_in`/`cost` until re-ingested; cost is pricer-derived so a SQL migration cannot recompute it. Mitigation: re-ingest (disposable DB); document; no migration that fabricates cost.
- **R3 — ctx_used regression.** Changing `tokens_in` must NOT shrink `ctx_used` (context occupancy). Mitigation: compute `ctx_used` from the total explicitly; test it.
- **R4 — UI scope.** Cache UI is additive and limited to the Overview tab; the cross-session Stats *page* does not exist yet (SOW-0007), so `/api/stats` only gains the cache fields in its totals *response* (no Stats-page rendering). Mitigation: additive display; tests.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model: claude-code is the lone adapter folding cache into `tokens_in` (`ops.go:253`); the canonical definition was undocumented; the pricer charges `tokens_in` (incl. cache) at the input rate AND `cache_read` at the cache rate → inflated tokens + double-counted cost ($9,377 observed). Fix: define `tokens_in` = fresh input canonically; conform claude-code (`tokens_in=InputTokens`, `ctx_used`=total); surface cache in the UI.

Evidence reviewed: the 5-adapter mapping grep; `resolver.go:91-96`; live DB token/cost sums; `canonical-events.md:127-130`; frontend cache-absence grep. (All above.)

Affected contracts and surfaces: `internal/adapters/claude_code/ops.go`; `canonical-events.md` + `data-model.md` (tokens_in/ctx_used definition); pricing (interaction only — no rate change); frontend Overview tab + `/api/stats` totals response (+ `ui-pages.md` Overview contract; no Stats page exists yet — SOW-0007); claude-code testdata (a cache-bearing fixture); no REST shape change (cache fields already exist in the schema/response — confirm the session-detail + stats responses expose them).

Existing patterns to reuse: the 4 conforming adapters' mapping; `pricing/*_test.go` for the cost test; `claude_code` golden fixtures for the adapter test; the Overview/Stats components + their tests for the UI.

Risk and blast radius: see R1–R4. No canonical event-shape change (fields exist); the only behavior change is claude-code `tokens_in`/`ctx_used` values + the UI gaining cache display.

Sensitive data handling plan: the cache-bearing claude-code fixture must be synthesized/sanitized (no real transcript content); synthetic token numbers only.

Implementation plan:
1. Spec (master): document canonical `tokens_in`/`cache`/`ctx_used` semantics in `canonical-events.md` + `data-model.md`.
2. Adapter audit + fix (subagent): confirm all 5; fix claude-code `tokens_in`/`ctx_used`; add a cache-bearing claude-code golden/unit test.
3. Pricing test (subagent): assert no double-count with cache.
4. UI (subagent): Overview-tab cache breakdown + cache-hit-rate; label `tokens_in` as fresh; expose cache in the `/api/stats` totals response; tests; update `ui-pages.md` Overview contract.
5. Gates + external review + merge (master); document the re-ingest backfill.

Validation plan: adapter test (cache-bearing), pricing test (no double-count), frontend tests (cache renders), full gates; manual re-ingest of the real claude-code source → confirm `tokens_in` ≈ fresh, cost sane, cache visible.

Artifact impact plan: AGENTS.md none; specs `canonical-events.md`+`data-model.md`+`ui-pages.md`; project skills none; docs none beyond corrected numbers; SOW lifecycle: own branch/PR, `completed`→`done/` in the implementing close.

Open decisions: **D1 (resolved, operator):** fix data+cost AND cache UI now (AskUserQuestion 2026-05-31). **D2 (resolved, CTO):** canonical `tokens_in` = fresh input (matches 4/5 adapters + Anthropic billing); claude-code is the outlier to conform.

## Implications And Decisions

1. Operator (2026-05-31): "Fix data+cost AND cache UI now" — the token/cost mapping fix + the cache UI ship together in this SOW.
2. CTO: canonical `tokens_in` = fresh/uncached input; cache separate; `ctx_used` = total context. claude-code conforms (it was the only outlier). No pricing-rate change — the double-count disappears once `tokens_in` excludes cache.

## Plan

1. Spec deltas (canonical-events.md + data-model.md) — master.
2. Adapter audit + claude-code fix + cache-bearing test — subagent.
3. Pricing no-double-count test — subagent.
4. UI cache breakdown + hit-rate (Overview tab) + `/api/stats` totals + ui-pages.md — subagent.
5. Gates + external review + merge + re-ingest backfill note — master.

## Execution Log

### 2026-05-31

- Created on branch `sow-0029-token-cost-semantics` (off master). Root-cause verified live; operator approved scope. Gate ready.

## Validation

Pending.

## Reviews

### Round 1 — 2026-05-31 (codex + glm + minimax) on commit `249ce2a`

No P1 in the core change; claude-code fix confirmed correct + EFFECTIVE; pricer no longer double-counts; cache UI + hit-rate correct; goldens intended-only. Findings, each adjudicated against code:

- **P1 (codex, decisive — glm/minimax missed it): codex token/cost totals don't persist.** codex sets tokens only on `TurnFinalizedEvent` (`mapper_turn.go:252`); the ingester rolls turn/session tokens from OP rows (`aggregates.go:43`), and codex ops carry no tokens → codex sessions show 0 tokens/cost. Verified by code. **PRE-EXISTING** (predates SOW-0029); my codex `tokens_in`=fresh fix is correct-but-moot until persistence lands (it broke nothing — codex was already 0 — and future-proofs the semantics). It is an ingester/adapter persistence-architecture decision, NOT token-semantics → **out of SOW-0029 scope; filed SOW-0030.** SOW-0029's codex claim is hereby narrowed: codex's *mapping* is now fresh-correct; codex *persistence* is tracked separately.
- **P2 (codex clamp order):** clamp `cached` ≥0 BEFORE subtracting (match upstream `non_cached_input()`); accumulate the clamped cached. **Fixed** (`mapper_turn.go` + test `TestMapper_TokenRollupCacheClamp`, RED→GREEN).
- **P2 (ctx_used over-claim — all three):** my new canonical text claimed all adapters compute the 4-term `ctx_used`; aiagent_v3/opencode omit cache_write+output (pre-existing). **Narrowed** the `canonical-events.md` contract (ctx_used = intended total, tracked gap); **filed SOW-0031** for alignment.
- **P2 (stale specs):** `adapter-claude-code.md:574/576` still stated the old `tokens_in`/`ctx_used`; `rest-api.md` stats example lacked cache fields. **Fixed** both.
- **P3 (SessionsList label):** "Tokens in" → "Tokens in (fresh)". **Fixed.**

Post-fix (orchestrator, ground truth): codex pkg `go test -race` green; golangci-lint 0; frontend lint/tsc/vitest green.

### Round 2 — 2026-05-31 (codex + glm + minimax) on commits `249ce2a` + `ea11fca`

Same scope as round 1, plus notes of each round-1 fix. codex remained the decisive reviewer (glm = "ship it"; minimax = clean). All findings adjudicated against ground truth:

- **P1 (codex — decisive): operator PII in durable artifacts.** SOW-0028 + SOW-0029 carried the operator's home path (`~/.claude/projects/-home-<user>-…`) and raw session IDs (`1d8b99cd…`, `085b690f…`) — committed to a PUBLIC repo, violating the never-write-PII rule. **Fixed:** sanitized both SOWs (home path → `~/.claude/projects/<this repo>`; session IDs → generic descriptors); repo-wide grep for `costa`/home-path/session-IDs now CLEAN.
- **P2 (codex): `data-model.md` v2 cache marked `n/a`.** Verified WRONG against ground truth — `aiagent_v2/mapper.go:380-381` emits `TokensCacheRead`/`TokensCacheWrite` (turn `:158-159`, op `:220-221`; tests assert non-zero at `mapper_test.go:350-354`). **Fixed:** v2 column `n/a` → `~` for both `turns/ops.tokens_cache_read/write`.
- **P2 (codex): SOW "Overview + Stats" over-claim + stale `ui-pages.md`.** Verified against ground truth: there is NO Stats *page* component (only `frontend/src/api/stats.ts`); cache renders ONLY in OverviewTab (`:56-62`). The `/api/stats` change is the backend totals *response* exposing the fields; the Stats page is Phase-2 (SOW-0007). **Fixed:** narrowed SOW AC#5/R4/affected-surfaces/plan to "Overview tab UI + `/api/stats` totals response (no Stats page yet — SOW-0007)"; updated `ui-pages.md` Overview tab contract to list the actual StatCards (Tokens in (fresh), Tokens out, Cache read, Cache write, Cache hit rate, Cost, Turns, Ops, Failures).
- **P3 (codex): codex `cache_creation_input_tokens` (and `output_tokens`) accumulated UNCLAMPED** while `fresh`/`cached` are floored ≥0 — a malformed negative would corrupt rollup totals and (via the pricer) produce a negative cost component (silent-failure class). **Fixed (closed the whole class, not just the flagged instance):** added `nonNeg` helper; all four per-call components in `addTokenUsage` now floored ≥0; new test `TestMapper_TokenClamp_OutputAndCacheWrite` (RED→GREEN) pins negative output/cache_write → 0 with `tokens_in` unaffected.

Post-fix (orchestrator, ground truth, whole repo): `gofmt -l` clean; `go vet ./...` 0; `go build ./...` 0; `go test -race ./...` all packages ok; `golangci-lint run` 0 issues. Round-3 re-review pending (same scope + these fix notes).

## Outcome

Pending.

## Lessons Extracted

Pending. (Provisional: an undocumented canonical field definition let one adapter diverge for the entire project lifetime; canonical semantics — especially anything pricing touches — must be documented + cross-adapter-tested. Found only by serving REAL data.)

## Followup

None yet.

## Regression Log

None yet.
