# SOW-0031 - ctx_used cross-adapter alignment

## Status

Status: completed

Sub-state: proposed follow-up (P2, pre-existing). Discovered 2026-05-31 during SOW-0029 round-1 external review (codex + glm + minimax all flagged it). Not blocking SOW-0029 — `ctx_used` does not affect the pricer (which uses `tokens_in`/cache) and is a UI context-percent input only.

## Requirements

### Purpose

Make `ctx_used` mean the same thing across adapters. The canonical contract (`canonical-events.md`, SOW-0029) defines `CtxUsed` as the TOTAL context occupancy = `TokensIn + TokensCacheRead + TokensCacheWrite + TokensOut`. claude-code and codex now compute it fully, but three adapters compute a narrower value:

- `internal/adapters/aiagent_v3/ops.go:119` — `CtxUsed = acc.TokensIn + acc.TokensCacheRead` (omits cache_write + output).
- `internal/adapters/opencode/mapper_ops.go` — `CtxUsed = tokens.input + tokens.cache.read` (omits cache_write + output; documented at `adapter-opencode.md:605`).
- `internal/adapters/aiagent_v2/mapper.go:383` — `CtxUsed = acc.Tokens.InputTokens + ev.TokensCacheRead + acc.Tokens.OutputTokens` (omits cache_write). CONFIRMED outlier (SOW-0029 R3, codex).

So `ctx_used / ctx_max` (the UI's context-window-% metric) is not comparable across sources. This SOW aligns aiagent_v3 + opencode + aiagent_v2 to the canonical 4-term formula, with tests.

### User Request

Implied by the canonical `ctx_used` definition (SOW-0029) + the cross-adapter comparability the UI's ctx-% relies on. Surfaced by all three SOW-0029 reviewers.

### Assistant Understanding

Facts:

- Canonical `CtxUsed` = `TokensIn + TokensCacheRead + TokensCacheWrite + TokensOut` (SOW-0029, `canonical-events.md`).
- aiagent_v3 (`ops.go:119`) and opencode omit `cache_write` + `output`; claude-code/codex compute the full term.
- `ctx_used` is NOT used by the pricer; it feeds the UI's `ctx_used / catalog_models.ctx_max` percentage only. So this is a comparability/correctness-of-a-derived-metric issue, not a cost issue.

Inferences:

- Align aiagent_v3 + opencode to the 4-term formula. Confirm aiagent_v2 too. Each needs a test pinning the new value + a golden regen if `ctx_used` appears in goldens. opencode uses warning-capable saturating addition (`addClampWarn`) for the existing 2-term — extend it to 4 terms with the same overflow guard.

Unknowns:

- Whether changing `ctx_used` shifts any committed golden's `ctx_used` value (expected yes for the affected adapters) — regen + verify only ctx_used moved.

### Acceptance Criteria

1. aiagent_v3, opencode, AND aiagent_v2 compute `ctx_used = tokens_in + cache_read + cache_write + tokens_out`. **Verification**: per-adapter unit/golden test asserting the 4-term value.
2. Overflow-safe where the adapter already clamps (opencode `addClampWarn`). **Verification**: the existing clamp test extended.
3. Specs reconciled: `adapter-aiagent-v3.md`, `adapter-opencode.md:605`, and the `canonical-events.md` ctx_used note updated to drop the "tracked gap" once aligned. **Verification**: spec-drift sweep.

## Analysis

Sources: `internal/adapters/{aiagent_v3/ops.go:119, opencode/mapper_ops.go, aiagent_v2/mapper.go}`, `canonical-events.md`, `adapter-opencode.md:605`. Discovered 2026-05-31 (SOW-0029 R1).

Risks:

- **R1 — golden churn.** Regenerating ctx_used in goldens; verify ONLY ctx_used changes. Mitigation: targeted diff review.
- **R2 — overflow.** Adding more terms; use the existing saturating-add/clamp pattern (opencode) + clamp others. Mitigation: extend the clamp tests.

## Pre-Implementation Gate

(To be filled on pickup. Audit all 5 adapters' ctx_used; align the outliers; tests + golden regen.)

## Implementation / Validation / Reviews / Outcome

(Empty placeholders.)

## Lessons / Follow-Ups

Pending. Parent: SOW-0029 (token/cost semantics). Sibling: SOW-0030 (codex token persistence).
