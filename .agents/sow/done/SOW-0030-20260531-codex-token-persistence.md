# SOW-0030 - codex token/cost totals do not persist (turn-emit vs op-rollup)

## Status

Status: completed

Sub-state: proposed follow-up (P1, pre-existing). Discovered 2026-05-31 during SOW-0029 round-1 external review (codex, the decisive reviewer; glm + minimax missed it). NOT introduced by SOW-0029 — predates it (codex adapter + the ingester's op-rollup aggregate model). SOW-0029's codex `tokens_in`=fresh fix is correct at the event level but moot until this lands (codex token totals are 0 in the DB regardless).

## Requirements

### Purpose

Make codex session/turn token + cost totals actually persist. Today the codex adapter emits its per-turn token rollup ONLY on `TurnFinalizedEvent` (`internal/adapters/codex/mapper_turn.go:252` `finalizeTurn` sets `TokensIn/TokensOut/TokensCacheRead/...`), and its `OpFinalizedEvent`s carry only `CtxUsed`/`CtxMax`, never `TokensIn`. But the ingester rolls turn/session token totals FROM OP rows (`internal/ingest/aggregates.go:43` `turns.tokens_in = SUM(ops.tokens_in)`; `:67` `sessions.tokens_in = SUM(turns.tokens_in)`). So codex's turn-level token fields are never persisted, and codex sessions show `tokens_in = tokens_out = cost_usd = 0` in the DB. This makes the Sessions list, session Overview, and any analytics show zero tokens/cost for every codex session.

### User Request

Implied by the project mission (accurate per-session tokens/cost across all adapters) and surfaced by SOW-0029's real-data review + codex-reviewer cross-check. The operator flagged the broader tokens/cost-correctness theme ("something is wrong with tokens in").

### Assistant Understanding

Facts:

- `internal/ingest/aggregates.go:43,67` — turn/session token totals are `SUM` over `ops.tokens_in` (op-rollup), NOT taken from the `TurnFinalizedEvent` token fields. The `applyTurnFinalized` `INSERT INTO turns` (`writer.go` ~485) writes no token columns.
- `internal/adapters/codex/mapper_turn.go:252` (`finalizeTurn`) sets `TokensIn/TokensOut/TokensCacheRead/TokensCacheWrite` on the `TurnFinalizedEvent`. Codex `OpFinalizedEvent`s (`ops_event.go:223` `applyLLMCtx`, `ops_enrich.go`) set `CtxUsed/CtxMax` only — no `TokensIn`.
- claude-code/aiagent set tokens on OPS (which persist via the rollup); codex is the one that puts them only on the turn → dropped.
- The pricer prices ops (`writer.go applyOpFinalized` → `priceOp`), so codex op cost is also 0 (ops carry no tokens to price).

Inferences (decide in the gate):

- **Option A — codex emits op-level tokens.** Attribute each codex LLM call's `last_token_usage` to its `OpFinalizedEvent` (`applyLLMCtx`) so token totals roll up like the other adapters. Most consistent with the canonical op-rollup model; localizes the change to the codex adapter.
- **Option B — ingester persists turn-level tokens when present.** Have `applyTurnFinalized` write the `TurnFinalizedEvent` token fields and the session rollup prefer them when ops sum to 0. Broader (touches the ingester's aggregate contract for ALL adapters); higher blast radius.
- Option A is likely correct (adapter-local, matches the model) — confirm against codex's event structure (does each LLM op have its `last_token_usage` available at `applyLLMCtx`?).

Unknowns:

- Whether codex's per-call token data is cleanly attributable to a single op (vs only a turn-cumulative rollup). If only turn-cumulative, Option A needs care (avoid double-count across the turn's ops); Option B may be simpler. Resolve in the gate.

### Acceptance Criteria

1. A codex session with token usage shows non-zero `tokens_in`/`tokens_out`/`cost_usd` in the DB (session + turn rows). **Verification**: an INGESTER test (not just an adapter golden) ingests a codex fixture with token_count events and asserts `sessions.tokens_in > 0` and matches the expected fresh-input total; cost > 0 when priced.
2. Token totals equal the sum of the source's per-call fresh input (no double-count across a turn's ops). **Verification**: the ingester test asserts the exact total.
3. Cache totals (`tokens_cache_read/write`) persist for codex too. **Verification**: the same test asserts cache totals.
4. Specs reconciled: `adapter-codex.md` + `canonical-events.md` (the persistence note added in SOW-0029) updated to describe codex's op-level token emission. **Verification**: spec-drift sweep.

## Analysis

Sources checked: `internal/ingest/aggregates.go`, `internal/ingest/writer.go` (applyTurnFinalized/applyOpFinalized), `internal/adapters/codex/{mapper_turn.go,ops_event.go,ops_enrich.go}`, `canonical-events.md`. Discovered 2026-05-31 (SOW-0029 R1, codex).

Risks:

- **R1 — Double-count.** Attributing turn-cumulative tokens to ops could double-count. Mitigation: attribute per-call `last_token_usage` to the matching op, not the turn cumulative; ingester test pins the total.
- **R2 — Aggregate-contract change (Option B).** Persisting turn tokens touches the ingester for all adapters. Mitigation: prefer Option A (adapter-local) unless the gate finds codex tokens aren't op-attributable.
- **R3 — Backfill.** Existing codex sessions stay at 0 until re-ingested (cost is pricer-derived). Mitigation: re-ingest (disposable DB); document.

## Pre-Implementation Gate

(To be filled on pickup. Must decide Option A vs B against codex's event structure; write an INGESTER-level failing test first.)

## Implementation / Validation / Reviews / Outcome

(Empty placeholders.)

## Lessons / Follow-Ups

Pending. Parent: SOW-0029 (token/cost semantics). Sibling: SOW-0031 (ctx_used alignment). Note: golden/event-level tests passed for codex tokens because they assert the EVENT fields; only an INGESTER-level test catches the persistence gap — add that class of test.
