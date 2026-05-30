# SOW-0024 - per-source row counts in /api/health

## Status

Status: open

Sub-state: proposed follow-up, awaiting operator prioritization. Discovered during SOW-0005 (opencode adapter) round-1 external review (codex P2.2). Not blocking SOW-0005 — the opencode probe LOGS its counts at startup and registers the source (visible in `/api/sources`); this SOW generalizes the richer per-source metadata into `/api/health`.

## Requirements

### Purpose

Surface per-source content metadata (e.g. session/message/part counts, latest schema/migration marker) in `/api/health` (and/or `/api/sources`) as a GENERAL, all-adapter feature. Today the opencode auto-discovery probe computes `(session_count, message_count, part_count, latest_migration)` and writes them to the startup log only; SOW-0005 AC#8 originally asked for them in `/api/health`, but a bespoke opencode-only health field does not generalize and would special-case the presenter. This SOW designs a generic source-metadata surface so every adapter can contribute health-relevant counts without per-adapter presenter branches.

### User Request

Implied by SOW-0005 AC#8 ("exposes (session_count, message_count, part_count, latest_migration_name) in /api/health") — amended during SOW-0005 to log-only + this follow-up, because the full surface is cross-cutting and should be general.

### Assistant Understanding

Facts:

- `internal/presenter/health.go` builds `/api/health` from a `sources` query + a parse-error rollup; it has no per-source content-count field.
- `cmd/ai-viewer-ingest/discovery.go` + `internal/adapters/opencode/migrations.go:ProbeStatus` compute opencode counts at startup and LOG them; they are not persisted into ai-viewer's DB for the presenter to read.
- `source_progress` / `sources` tables (`data-model.md`) hold per-source state (cursor, last_seq, last_ts, parse_errors). There is no general per-source "content summary" column.
- File-based adapters (aiagent/claude-code/codex) have no cheap O(1) row count analogous to opencode's `COUNT(*)`; a generalized surface must tolerate "count unknown/not-applicable".

Inferences:

- A general design: a small per-source metadata blob (JSON) the adapter/probe can populate (e.g. `sources.meta_json` or `source_progress.extras`), surfaced verbatim under each source in `/api/health` (or `/api/sources`). Adapters that have cheap counts populate them; others omit. Avoids per-adapter presenter branches.
- Alternatively, a periodic ingester-side rollup of canonical row counts per source (sessions/turns/ops the ingester already wrote) — which is adapter-agnostic and always available — may be more useful than source-native counts. Decide in the gate (source-native probe counts vs ingested-canonical counts).

Unknowns:

- Which counts are actually useful for health triage (source-native vs ingested-canonical), and whether they belong in `/api/health` (triage) or `/api/sources` (inventory). Resolve in the gate with the presenter spec.
- Staleness: probe counts are point-in-time at startup; ingested counts are live. The gate picks the model + documents freshness.

### Acceptance Criteria

1. A general per-source metadata surface exists (schema + writer + presenter) that any adapter can populate without a presenter code branch. **Verification**: presenter test asserts a source's metadata round-trips into `/api/health` (or `/api/sources`).
2. The opencode source surfaces its `(session/message/part counts, latest_migration)`; file-based adapters omit gracefully (no error, no zero-as-real). **Verification**: an integration test with an opencode fixture + a file-based fixture asserts the opencode metadata appears and the file-based one is absent/omitted.
3. Specs reconciled: SOW-0005 AC#8 amendment resolved; `data-model.md` + `observability.md`/`rest-api.md` describe the surface. **Verification**: spec-drift sweep clean.

## Analysis

Sources checked: `internal/presenter/health.go`, `cmd/ai-viewer-ingest/discovery.go`, `internal/adapters/opencode/migrations.go`, `.agents/sow/specs/{data-model.md,observability.md,rest-api.md}`. Discovered 2026-05-30 during SOW-0005 round-1 review.

Risks:

- **R1 — Cross-cutting surface.** Touches schema + ingester + presenter. Mitigation: additive (new optional metadata; no existing field changes); full gate + external review.
- **R2 — Generalization.** Must not special-case opencode in the presenter. Mitigation: the metadata blob/rollup is adapter-agnostic; adapters opt in.
- **R3 — Freshness semantics.** Probe-time vs live counts. Mitigation: the gate decides + documents which, and `/api/health` labels it.

## Pre-Implementation Gate

(To be filled by the assistant picking this SOW up. Required before moving to `current/`.)

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
