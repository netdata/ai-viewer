# SOW-0061 — ingested-canonical per-source counters + periodic opencode re-probe

## Status

Status: open

Sub-state: follow-up filed during SOW-0024 (Hard Rule #9: tech debt is paid or tracked). SOW-0024 delivered the source-native `sources.meta_json` blob (opencode startup probe counts) but deliberately deferred two related signals to keep its scope surgical. This SOW tracks both.

## Requirements

### Purpose

Give the operator two adapter-agnostic, LIVE per-source health signals that the source-native blob from SOW-0024 cannot provide:

1. **Ingested-canonical per-source counts** — "how many sessions/turns/ops has this source contributed to the canonical DB". This is the signal that answers "is each source actually ingesting data?" for EVERY adapter (not just opencode). Today only `/api/stats by_source` offers a filtered, live session count; there is no unfiltered, O(1) per-source counter surfaced in `/api/health`/`/api/sources`.
2. **Periodic opencode re-probe** — SOW-0024's `meta_json` reflects source state at last ingester startup (the probe runs once). On a long-running ingester the opencode-DB native counts drift stale. A periodic re-probe (bounded, gated like the tailer's 60 s safety net) would refresh `meta_json` so the source-native counts stay current.

### User Request

Implied by SOW-0024's gate ("Ingested-canonical per-source counters are a SEPARATE, adapter-agnostic signal ... real ingester complexity outside this SOW's ACs. A follow-up SOW is filed"). Not requested directly by the operator; filed by the CTO as tracked deferral.

### Assistant Understanding

Facts:

- SOW-0024 added `sources.meta_json` (source-native, opencode-only, last-startup freshness) and bumped the schema to v8.
- `/api/health` and `/api/sources` join `sources` + `source_progress`; neither carries an ingested-canonical count.
- There is NO index on `sessions(source_id)` (`data-model.md` §sessions indexes), so a live `SELECT source_id, COUNT(*) FROM sessions GROUP BY source_id` on every `/api/health` poll is a full scan — unacceptable for a frequently-polled health endpoint.
- The ingester maintains catalog rollups but they are keyed by `source_format` (coarser than `source_id`) and time-bucketed, not per-`source_id` cumulative.
- The opencode tailer already has a gated `MAX(time_updated)` probe pattern (`adapter-opencode.md` §Watch Strategy) that could be reused to bound a periodic re-probe.

Inferences:

- Ingested-canonical counters need a maintained per-source counter on the writer side (increment on session/turn/op insert) OR a per-source indexed COUNT. A maintained counter on `source_progress` (or a new `source_stats` table) incremented in the same batch tx is the O(1)-read, no-extra-index choice. The counter must be additive-correct under idempotent re-ingest (re-emitted rows must not double-count: increment only on INSERT, not on ON CONFLICT DO UPDATE — needs an idempotency-aware increment path, likely `changes()`/`total_changes()` or a returned row-affected check).
- Periodic re-probe can reuse the opencode tailer's gate (WAL event OR 60 s safety net) to avoid probing on every idle poll; the refreshed blob writes `sources.meta_json` and emits a `source_status_changed` notify so the UI updates live (SOW-0024 deliberately did NOT wire notify-on-meta-change because meta was static per run; this SOW makes it dynamic).

Unknowns:

- Whether the maintained counter lives on `source_progress` (existing per-source row) or a new `source_stats` table (separation of progress vs stats). Decide in the gate.
- Whether the counter covers sessions only or also turns/ops (ops is the high-churn table; an op counter is the strongest "is it ingesting" signal but the most write-amplification). Decide in the gate.
- Whether re-probe freshness (60 s net) is acceptable or the operator wants tighter. Product call.

### Acceptance Criteria

1. `/api/health` and `/api/sources` carry a per-source ingested-canonical count (at minimum `sessions`; turns/ops per the gate decision) that is LIVE (advances as the ingester commits) and adapter-agnostic (every adapter contributes). **Verification**: presenter test asserts the count advances after an ingester flush; a re-flush of already-ingested rows does NOT advance it (idempotency).
2. The opencode `meta_json` blob refreshes periodically (not just at startup) while the ingester runs, bounded by a gate so an idle source is not probed on every poll. A `source_status_changed` notify fires on refresh so the UI updates without a manual refetch. **Verification**: integration test asserts the blob advances after the opencode DB grows and the gate opens.
3. Specs reconciled: `data-model.md`, `observability.md`, `rest-api.md`, `adapter-opencode.md`, `ingester.md` updated in the same commit. **Verification**: spec-drift sweep clean.

## Analysis

Sources checked: `.agents/sow/specs/{data-model.md, observability.md, rest-api.md, adapter-opencode.md, ingester.md}`, `internal/ingest/{worker.go,writer.go,ingester.go}`, `internal/presenter/{health.go,sources.go}`, SOW-0024 gate. Filed 2026-06-14.

Risks:

- **R1 — Write-amplification.** A maintained counter incremented per op on a high-churn source adds per-row work to the batch tx. Mitigation: increment once per batch via `INSERT ... ON CONFLICT DO UPDATE SET count = count + <delta>` where `<delta>` is the batch's net-new row count (computed from `sql.Result.RowsAffected()` distinguishing insert vs update, or a pre/post COUNT delta within the tx), NOT per individual row.
- **R2 — Idempotency correctness.** Re-ingesting the same source (cursor reset, tail re-read) must not double-count. Mitigation: increment only on genuine INSERT (RowsAffected == 1 with the idempotent ON CONFLICT DO NOTHING returning 0 for a conflict); document the invariant with a test that re-flushes the same events and asserts the counter is stable.
- **R3 — Schema.** Adding a counter column or a `source_stats` table is another schema bump (v8 → v9). Mitigation: additive; follow the 0007/0008 precedent.
- **R4 — Re-probe cost.** The opencode COUNT(*) probe is a few hundred ms on a multi-GB DB. Mitigation: reuse the tailer's gate (WAL event OR 60 s safety net) so it never runs on an idle poll; the probe is read-only on the source DB.

## Pre-Implementation Gate

(To be filled when this SOW is picked up. Required before moving to `current/`.)

## Implementation

(Empty placeholder.)

## Validation

(Empty placeholder.)

## Reviews

(Empty placeholder.)

## Outcome

Pending.

## Lessons / Follow-Ups

Pending.
