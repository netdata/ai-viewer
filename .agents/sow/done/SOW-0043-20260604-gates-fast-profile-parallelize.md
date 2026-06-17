# SOW-0043 - gates.sh `--fast` profile + parallelization (hit the < 5 min target)

## Status

Status: deferred — internal quality; no user-visible impact (2026-06-17)

Sub-state: filed by SOW-0013 as the tracked follow-up for AC#3 / R1 — the full `scripts/gates.sh` exceeds the 5-minute local target and the SOW-0013 mandate forbids dropping/weakening any gate to chase it. This SOW owns getting under the target the right way.

## Requirements

### Purpose

Restore the `quality-gates.md` §Performance Target (full local `gates.sh` < 5 min on the operator's workstation) **without removing or weakening any gate**, so the pre-commit feedback loop stays fast.

### User Request

Implicit in `quality-gates.md` §Performance Target ("if it exceeds, profile and parallelize before adding more gates") and SOW-0013 R1 ("if total exceeds 5 min, parallelization within `gates.sh` is added before any gate is dropped or weakened").

### Assistant Understanding

Facts (measured in SOW-0013, operator workstation, cold run):

- `scripts/test.sh` alone (Go `-race` over `./...` + frontend Vitest) = **6m38s** — already over the 5-min target on its own.
- Long pole inside the `-race` suite: `internal/adapters/aiagent_v2` ≈ 123 s and `internal/canonical` ≈ 62 s.
- Full `scripts/gates.sh` ≈ 7–8 min (lint.sh + scans + spec-drift + build.sh stack on top of the 6m38s test.sh).
- `scripts/gates.sh` today runs sections strictly serially, fail-fast, slow gates last.

Inferences (to be validated during implementation, not assumed):

- A `gates.sh --fast` profile that runs the static + cross-cutting gates (lint.sh, scan-secrets, scan-ai-attribution, spec-drift, build.sh) and SKIPS the `-race` suite would give a sub-minute-to-~2-min pre-commit pass while iterating, with the full run reserved for pre-push.
- Internal parallelization of independent sections (e.g. lint.sh ∥ scan-secrets ∥ spec-drift) via background jobs + `wait` could shave the static phase, but the dominant cost is the serial `-race` suite, so the bigger lever is the `internal/ingest`/`aiagent_v2`/`canonical` test runtime itself.
- The `internal/ingest` rollup test slowness is already tracked (SOW-0009 Followup, referenced in `quality-gates.md` §Go — Race Stress); speeding it up helps both this SOW and the per-push CI `-count` decision.

Unknowns:

- Whether the `-race` long pole is reducible by test refactor (smaller fixtures, `t.Parallel()`, shared setup) without losing coverage — needs profiling per package.
- Whether `--fast` should skip only the `-race` suite or also `build.sh` (the bundle-size gate needs a build).

### Acceptance Criteria

1. `scripts/gates.sh --fast` exists, documented in the script header + `quality-gates.md` + the `project-quality-gates` skill, running the fast gates only and completing well under 1–2 min; the default (no-arg) `gates.sh` stays the full, complete run.
2. Either the full `gates.sh` is brought under 5 min by genuine speedups (test-runtime reduction and/or section parallelization) **with every gate still present and unweakened**, OR — if the `-race` suite floor genuinely cannot reach < 5 min on the workstation — the target in `quality-gates.md` is revised to the measured floor with the `--fast` profile as the fast-iteration path, and the revision is operator-visible (this is the only path that may change the documented number, and only with evidence).
3. No gate dropped, skipped (outside `--fast`), or threshold-lowered.
4. Specs + skill updated in the same commit; external review per `project-second-opinions`.

## Analysis

(To be filled when the SOW is picked up. Start from the SOW-0013 timing evidence above and profile each `-race` package.)

## Pre-Implementation Gate

Status: not started (pending operator sign-off).

## Plan

(To be filled.)

## Execution Log

### 2026-06-04 — Filed

Filed by SOW-0013 as the tracked follow-up for the gates.sh runtime overage (AC#3 / R1). SOW-0013 kept the gate complete and documented the measured ~7–8 min total + the 6m38s `test.sh` long pole rather than weakening any gate; this SOW owns the speedup.

## Validation

Pending.

## Reviews

Pending.

## Outcome

Pending.
