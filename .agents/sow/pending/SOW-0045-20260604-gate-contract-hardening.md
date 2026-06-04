# SOW-0045 - Gate contract hardening follow-ups

## Status

Status: open

Sub-state: filed by SOW-0013 review round 2. Not blocking SOW-0013 because the
current gates fail closed and are validated, but these hardening items reduce
future maintenance drift.

## Requirements

### Purpose

Make the gate infrastructure easier to evolve safely: test the CI presence-check
logic itself, remove duplicated fuzz-target lists, and trim avoidable CI checkout
cost. Fit-for-purpose: keep the quality layer maintainable as more gates are
added, without weakening any current gate.

### User Request

Implicit in SOW-0013's quality-gate mandate. External review round 2 identified
non-blocking maintenance risks in the gate infrastructure that should be tracked
rather than left as loose notes.

### Assistant Understanding

Facts:

- `.github/workflows/ci.yml` now fail-closes when required gate scripts
  (`scan-secrets`, `spec-drift`, `scan-ai-attribution`, and `gates.sh`) are
  missing and syntax-checks `scripts/gates.sh`, but that workflow presence-check
  logic has no hermetic self-test.
- The deterministic adapter fuzz target set is duplicated in `scripts/gates.sh`
  and `.github/workflows/ci.yml`, so adding/removing a fuzz target requires two
  edits.
- Most CI jobs use `actions/checkout` with `fetch-depth: 0`, even though only
  CodeQL and future history-aware gates are likely to need full history.
- `scripts/scan-ai-attribution.sh` intentionally scans for attribution phrases
  involving reviewer names. A future legitimate domain comment such as "one
  event per codex message" could false-positive because `codex` is both a source
  format and a reviewer name.
- The same scanner walks `cmd/` recursively today, so ignored generated embed
  output under `cmd/ai-viewer-serve/frontend_dist/` could also create local
  false positives or unnecessary scan work after a build.

Inferences:

- A small script or checked manifest can make the required-script and fuzz-target
  contract self-testable without invoking GitHub Actions locally.
- A single source for the fuzz target matrix reduces the chance that local gates
  and CI diverge.
- Checkout-depth reduction is low risk but should be measured once the first CI
  run establishes a baseline.

### Acceptance Criteria

1. The CI required-script/presence contract has a hermetic self-test that proves
   missing required scripts fail closed and optional helpers remain optional.
2. The adapter fuzz seed target set has one committed source of truth consumed by
   both `scripts/gates.sh` and `.github/workflows/ci.yml` or by a generator that
   verifies the workflow stays in sync.
3. CI checkout depths are reduced to shallow clones where full history is not
   needed, with CodeQL/full-history consumers kept explicit.
4. AI-attribution scanner false-positive risk is reduced without weakening the
   ban on reviewer-name attribution comments, including ignored generated embed
   output and legitimate source-format wording.
5. Specs, skills, and workflow-check documentation are updated in the same commit.
6. Full local gates and external review converge before closing.

## Analysis

Sources checked:

- SOW-0013 round-2 external review findings.
- `.github/workflows/ci.yml`
- `scripts/gates.sh`
- `.agents/sow/specs/quality-gates.md`

Risks:

- Over-abstracting CI may make the workflow harder to read. Keep the source of
  truth simple and inspectable.
- A shallow checkout could break a future history-aware gate. Any full-history
  consumer must be named in the workflow comment.

## Pre-Implementation Gate

Status: not started (pending prioritization).

## Plan

To be filled when activated.

## Validation

Pending.

## Reviews

Pending.

## Outcome

Pending.
