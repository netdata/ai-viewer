# SOW-0027 - op duration_us recompute when start_ts changes after finalize

## Status

Status: deferred — verified 0 real-data impact (2026-06-17)

Sub-state: proposed follow-up, awaiting operator prioritization. Discovered 2026-05-31 during SOW-0026 (op duration_us fix) round-2 external review (codex P2). NOT blocking SOW-0026 — the gap is pre-existing (predates SOW-0026), is not triggered by any current adapter, and the SOW-0026 contract is documented in `ingester.md`.

## Requirements

### Purpose

Guarantee `ops.duration_us` (and the `catalog_*.total_duration_us` rollups) stay consistent with `end_ts - start_ts` even when an op's `start_ts` changes AFTER the op was finalized. Today `duration_us` is computed only at `OpFinalized` from the then-persisted `start_ts`, but the op-insert UPSERT applies `start_ts = MIN(ops.start_ts, excluded.start_ts)` (`internal/ingest/writer.go`), so a later `OpStarted` re-emit carrying an EARLIER start lowers `start_ts` without recomputing the duration — leaving `duration_us` (and the catalog total) stale. Real adapters do not currently re-emit an earlier start after finalize, so this is a latent contract gap, not an observed bug.

### User Request

Implied by the data-model invariant `duration_us = end_ts - start_ts when both known` (`data-model.md §ops`). SOW-0026 round-2 review (codex) surfaced that the writer contract ALLOWS a state where the invariant is violated.

### Assistant Understanding

Facts:

- `internal/ingest/writer.go` op-insert UPSERT: `start_ts = MIN(ops.start_ts, excluded.start_ts)` — start_ts is monotonically non-increasing across re-emits.
- `duration_us` is written only in `applyOpFinalized` (`EndTs - persisted start_ts`), not in `applyOpStarted`.
- Sequence that breaks the invariant: OpStarted(1000) → OpFinalized(end=1300) ⇒ duration=300; later OpStarted re-emit(900) ⇒ start_ts=900 but duration stays 300 (should be 400), and the catalog `total_duration_us` keeps the stale 300.
- SOW-0026 documented the contract in `ingester.md` (adapters must emit the authoritative start at/before finalize); no adapter currently violates it.

Inferences:

- Two candidate fixes: (a) in `applyOpStarted`, when the op already has `end_ts` (finalized) AND the UPSERT changed `start_ts`, recompute `duration_us = end_ts - start_ts` and apply the catalog `(now - prior)` delta (mirroring `onOpFinalized`); or (b) make `duration_us` a SQLite GENERATED column `GENERATED ALWAYS AS (CASE WHEN end_ts>=start_ts THEN end_ts-start_ts END) VIRTUAL`, which auto-tracks both columns and eliminates the write-time computation entirely (requires an `ops` table-rebuild migration + verifying the catalog delta logic composes with a generated column). The gate picks one.
- Option (b) also structurally closes the original SOW-0026 finalize-Ts bug class (duration can never diverge from start/end), at the cost of a heavier migration.

Unknowns:

- Whether a single op legitimately observes an earlier start after finalize in ANY adapter (resume replay, late enrichment). If never, option (a)'s "recompute on post-finalize start change" branch is dead-but-defensive; if it can happen, the semantics question is "which start is truth" — resolve in the gate against adapter re-emit behavior.

### Acceptance Criteria

1. After any sequence of OpStarted/OpFinalized re-emits, `ops.duration_us` equals `end_ts - start_ts` (or NULL when either is unknown / `end_ts < start_ts`), and `catalog_*.total_duration_us` equals the SUM of member ops' final durations. **Verification**: an ingester test driving OpStarted(1000)→OpFinalized(1300)→OpStarted(900) asserts duration=400 and the catalog total moved accordingly.
2. The chosen approach (write-time recompute vs generated column) is documented in `data-model.md` / `ingester.md`; if a generated column, the `ops` rebuild migration + catalog-delta compatibility are covered by a migration test. **Verification**: spec reconciliation + migration test.

## Analysis

Sources checked: `internal/ingest/writer.go` (op-insert MIN + applyOpFinalized), `internal/ingest/catalog.go` (onOpFinalized delta), `.agents/sow/specs/{data-model.md,ingester.md}`. Discovered 2026-05-31 (SOW-0026 round-2, codex P2).

Risks:

- **R1 — Hot-path complexity (option a).** Recompute-on-start-change adds a catalog delta path to `applyOpStarted`. Mitigation: gate it on `end_ts IS NOT NULL AND start_ts changed`, mirror `onOpFinalized`'s delta exactly, test it.
- **R2 — Table-rebuild migration (option b).** A generated column needs an `ops` rebuild (CREATE+INSERT SELECT+drop+rename), preserving indexes/FKs. Mitigation: full migration test; the DB is derived/disposable.
- **R3 — Semantics.** "Which start is truth after finalize" may be adapter-specific. Mitigation: gate decision against adapter re-emit behavior.

## Pre-Implementation Gate

(To be filled by the assistant picking this SOW up. Required before moving to `current/`. Must choose option (a) vs (b) with evidence on adapter re-emit behavior.)

## Implementation

(Empty placeholder.)

## Validation

(Empty placeholder.)

## Reviews

(Empty placeholder.)

## Outcome

Pending.

## Lessons / Follow-Ups

Pending. Parent: SOW-0026 (op duration_us from start_ts). Related canonical-carrier follow-ups: SOW-0021/0023/0024/0025.
