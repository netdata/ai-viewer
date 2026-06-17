# SOW-0022 - codex duplicate-rollout-id disambiguation

## Status

Status: completed

Sub-state: proposed follow-up, awaiting operator prioritization. Discovered during SOW-0004 (codex adapter) round-6 review (codex P3). Not blocking — unobserved edge, accepted for v1.

## Requirements

### Purpose

Honor `adapter-codex.md` edge case #14: when two codex rollout files carry the same `session_meta.payload.id`, they must become SEPARATE canonical sessions keyed on `(source_id, native_id + ":" + file_basename)` with a `LogEntry` warning — instead of collapsing into one session as they do in SOW-0004's v1.

### User Request

Implied by `adapter-codex.md` edge #14 (the spec documents the disambiguation behavior). Surfaced by codex's round-6 review of SOW-0004 as the one remaining (P3, non-blocking) gap.

### Assistant Understanding

Facts:

- SOW-0004's codex adapter sets `SessionStartedEvent.NativeID = session_meta.payload.id` (authoritative; `internal/adapters/codex/mapper_turn.go`), and the ingester upserts sessions on `(source_id, native_id)` (`internal/ingest/writer.go`). So two rollout files with the same `payload.id` upsert into ONE `sessions` row — they collapse.
- The spec (`adapter-codex.md` edge #14) intends them kept separate via `native_id + ":" + file_basename` + a warning.
- This is **unobserved**: 0 of the 2,566 modern files on the reference workstation have duplicate ids. It would require codex to resume into a forked thread writing the same id to two files.
- The per-file adapter cannot detect the collision alone — it has no cross-file view. Detection needs either the ingester (which sees the `(source_id, native_id)` conflict) to disambiguate, or the adapter to track seen ids across files within a Scan/Tail run.

Inferences:

- The cleanest home is likely the ingester: on a sessions upsert where the incoming `start_ts`/file differs from the existing row in a way that indicates a DIFFERENT physical file with the same native_id, disambiguate by appending the basename. But that needs the file basename threaded into the SessionStarted extras (the adapter has it).
- Alternatively the adapter carries the basename in extras and the ingester composes the disambiguated native_id when it detects a collision. Design decision for the gate.

Unknowns:

- Whether to disambiguate in the adapter (cross-file id set per run) or the ingester (collision-on-upsert). Resolve in the Pre-Implementation Gate.
- Whether the disambiguated `native_id` breaks parent/child linkage (parent_thread_id/forked_from_id reference the bare id) — the resolver may need to match the bare id prefix.

### Acceptance Criteria

1. Two codex rollout files with the same `session_meta.payload.id` produce TWO distinct canonical sessions, each keyed on a basename-disambiguated native id, with a `LogEntry` warning per the spec. **Verification**: a golden/integration test with two same-id fixtures asserts two sessions.
2. Parent/child + fork linkage still resolves when a parent/child id collides (the resolver matches the bare id). **Verification**: a linkage test across the disambiguated ids.
3. The single-file common case (unique ids — 100% of observed data) is byte-for-byte unchanged. **Verification**: existing codex goldens unchanged.

## Analysis

Sources checked: `adapter-codex.md` edge #14 (:504), `internal/adapters/codex/mapper_turn.go` (NativeID assignment), `internal/ingest/writer.go` (sessions upsert + resolver), SOW-0004 round-6 review (codex P3).

Risks:

- **R1 — cross-file state.** Disambiguation needs a view the per-file adapter lacks; the ingester is the natural place but it must not regress the common single-file path. Mitigation: gate the disambiguation strictly on a detected `(source_id, native_id)` collision from a DIFFERENT file.
- **R2 — linkage.** Disambiguated native ids must not break parent_thread_id/forked_from_id resolution. Mitigation: the resolver matches the bare id.

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
