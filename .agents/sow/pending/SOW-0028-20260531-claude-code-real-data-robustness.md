# SOW-0028 - claude-code adapter real-data robustness (retryInMs float + root start_ts=0)

## Status

Status: open

Sub-state: proposed follow-up, awaiting operator prioritization. Discovered 2026-05-31 by ingesting a real claude-code project (a sanitized read-only copy; one ~61 MB transcript + 139 sub-agents) into the corrected backend during the SOW-0006 Trace-tab real-data review. Not blocking SOW-0006 (frontend); these are claude-code ADAPTER (SOW-0003) data-correctness bugs that only real transcripts expose — the synthetic golden fixtures use clean integer/complete data and never hit them.

## Requirements

### Purpose

Make the claude-code adapter robust against the shapes real Claude Code transcripts actually emit. Two concrete defects were observed on real data:

1. **`retryInMs` is a float in real transcripts but the adapter decodes it as `int64`.** Real `system` records carry e.g. `"retryInMs": 38317.38269012852` (API backoff). The adapter's `systemBody.retryInMs int64` fails to unmarshal → repeated `adapter parse error` (correctly surfaced to `/api/health` → `degraded`, not silent). Affected `system` records are dropped.
2. **Root session `start_ts = 0`.** The root session (the top-level transcript) ingested with `start_ts=0` while its 139 sub-agents have correct `start_ts`. Likely linked to (1) (parse failures on the root transcript's early records, including whatever sets the session start) or a root-start derivation gap. Result: the session header and any session-start-relative axis are wrong for the root.

### User Request

Implied by the project mission ("read source-system snapshots … production-quality, no silent failures") and the operator's standing principle: test against real production artifacts; our code must handle what real systems emit. Surfaced live during the SOW-0006 real-data review.

### Assistant Understanding

Facts:

- `internal/adapters/claude_code/*` decodes a `system` record body with `retryInMs int64` (exact field/file to confirm on pickup — error: `decode system: json: cannot unmarshal number 38317.38269012852 into Go struct field systemBody.retryInMs of type int64`).
- A real ~61 MB claude-code transcript produced several such errors at distinct offsets; health went `degraded` (parse-error path works — no silent failure).
- The DB after ingest: 1 root (`start_ts=0`, 11,928 ops, 347 turns) + 139 sub_agents (real `start_ts`); the 140 transcripts are correctly ONE session tree (root + its sub-agents), NOT a collapse bug — verified `count(DISTINCT root_session_id)=1` with native_ids `<root-uuid>:age<N>`.

Inferences:

- (1) Fix: type `retryInMs` as `float64` (or `json.Number`, then round) — Claude Code emits fractional backoff ms. Round/truncate to int on store if the canonical field is integer ms.
- (2) Fix: determine why the root's `start_ts` is 0 — either a consequence of (1) dropping the record that carries the session start, or the adapter not deriving the root start from the first event. Confirm on pickup with a real (sanitized) transcript fixture.
- These are regressions against SOW-0003 (claude-code adapter, `done/`). Per the regression rule, picking this up should reopen SOW-0003 with a dated `## Regression` section (failing test pinning each defect BEFORE the fix), or be delivered as this standalone bug SOW — decide on pickup.

Unknowns:

- Exact struct/field/file for `retryInMs` (confirm by grep on pickup).
- Whether root `start_ts=0` is caused by (1) or independent (confirm by fixing (1) first, then re-ingesting, then checking).

### Acceptance Criteria

1. A real `system` record with a fractional `retryInMs` parses without error. **Verification**: a sanitized golden transcript fixture containing `"retryInMs": <float>` ingests cleanly (no parse error; health not degraded by it); a unit/golden test pins it.
2. A claude-code root session derives a correct non-zero `start_ts` from its transcript. **Verification**: a golden/integration test asserts the root session `start_ts > 0` (and equals the first event's ts).
3. Specs reconciled: `adapter-claude-code.md` documents `retryInMs` is a float and the root-start derivation. **Verification**: spec note + tests.
4. Sanitized real-shaped fixtures committed under `testdata/claude_code/` capturing both shapes (float retryInMs; root start). All ids/paths/content sanitized per the sensitive-data policy. **Verification**: secret/PII scan clean.

## Analysis

Sources checked: live ingest of a real claude-code project (sanitized read-only copy into a temp tree); `/api/health` (degraded + parse errors); the seeded DB (`sessions`/`ops` row inspection). `internal/adapters/claude_code/*` to be read on pickup.

Risks:

- **R1 — Real-data fixtures may carry sensitive content.** Mitigation: synthesize minimal sanitized fixtures (keep only the field shapes: a `system` record with float `retryInMs`; a clean session start) — never commit real transcript content.
- **R2 — Float→int rounding.** If the canonical/store field is integer ms, decide round vs truncate; document it. Mitigation: spec note + test.
- **R3 — Regression bookkeeping.** SOW-0003 is `done/`; follow the regression-reopen rule (failing test first) on pickup.

## Pre-Implementation Gate

(To be filled by the assistant picking this up. Required before moving to `current/`. Must: grep the exact `retryInMs` struct/field; reproduce both defects with a sanitized fixture; decide reopen-SOW-0003-regression vs standalone; write failing tests before the fix.)

## Implementation

(Empty placeholder.)

## Validation

(Empty placeholder.)

## Reviews

(Empty placeholder.)

## Outcome

Pending.

## Lessons / Follow-Ups

Pending. Found via the SOW-0006 real-data review (the value of testing on real production artifacts: the synthetic golden fixtures never exercised a fractional `retryInMs` or a 61 MB multi-day transcript). Sibling follow-ups: SOW-0027 (op duration recompute on post-finalize start_ts change).
