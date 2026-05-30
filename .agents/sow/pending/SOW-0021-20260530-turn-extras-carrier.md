# SOW-0021 - turn-extras carrier (populate turns.extras_json)

## Status

Status: open

Sub-state: proposed follow-up, awaiting operator prioritization. Discovered during SOW-0004 (codex adapter) Chunk B. Not blocking SOW-0004 — codex ships an interim no-loss workaround.

## Requirements

### Purpose

Make `turns.extras_json` reachable. The `turns` table defines an `extras_json TEXT` column (data-model.md:112, with `codex_turn_id` cited as the canonical example value), but **no canonical turn event carries an `Extras` field** (`TurnStartedEvent`/`TurnFinalizedEvent` in `internal/canonical/events.go:220,233` have none) and the ingest writer never populates `turns.extras_json`. So every per-turn extra the specs promise is structurally unreachable from any adapter. This SOW adds a turn-extras carrier to the canonical event model + ingest writer so adapters can populate `turns.extras_json`, and migrates the codex adapter's interim surface onto it.

### User Request

Implied by the data-model + adapter specs, which document `turns.extras_json.{codex_turn_id,sandbox,ttft_ms}` (adapter-codex.md "Canonical Model Gaps" #2/#3/#8) and `claude-code system.subtype='turn_duration'` (data-model.md:112) as the durable home for per-turn metadata. SOW-0004 surfaced that this home is unwired.

### Assistant Understanding

Facts:

- `internal/canonical/events.go`: `TurnStartedEvent` = {EventBase, SessionNativeID, Seq}; `TurnFinalizedEvent` = {EventBase, SessionNativeID, Seq, Status, ErrorClass, EndTs, Tokens*, CostUSD}. Neither carries `Extras`.
- `internal/ingest/writer.go`: the `turns` UPSERT paths never write `extras_json`; the `graftAiViewerExtras` extras handling is ops/sessions-only.
- `data-model.md:98-112`: the `turns` table has `extras_json TEXT`.
- SOW-0004 codex adapter computes per-turn `codex_turn_id`, `sandbox`, and `ttft_ms` on its `turnState` and currently surfaces them via a single informational `turn_meta` LogEntry at turn finalize (no silent loss) — `internal/adapters/codex/ops_event.go` (`turnExtrasLog`).

Inferences:

- The cleanest carrier is an `Extras map[string]any` field on `TurnFinalizedEvent` (turn extras are known by the time the turn finalizes), mirroring how ops/sessions carry `Extras`; the writer then marshals it into `turns.extras_json` on the turn UPSERT, mirroring the ops/sessions extras write. A `TurnUpdatedEvent` is an alternative if mid-turn extras are ever needed, but no current adapter needs that.
- This is shared infrastructure: claude-code (`turn_duration`), codex, and future adapters all benefit. It is deliberately out of SOW-0004's codex-only-additive blast radius.

Unknowns:

- Whether `turns.extras_json` needs the same per-key graft protection the ops/sessions paths use (re-emit safety). Turn finalize is terminal and single-shot per (session,seq), so a wholesale write is likely safe — confirm against the idempotent-write model (SOW-0015) during the gate.

### Acceptance Criteria

1. `TurnFinalizedEvent` carries an `Extras` field (or an equivalent carrier); `internal/canonical` tests cover it. **Verification**: `go build`/`go test` for canonical.
2. The ingest writer marshals turn `Extras` into `turns.extras_json` on the turn UPSERT, idempotently. **Verification**: an ingester test asserts a `TurnFinalizedEvent{Extras:{...}}` lands in `turns.extras_json`, and a re-emit does not corrupt it.
3. The codex adapter populates `turns.extras_json.{codex_turn_id,sandbox,ttft_ms}` via the carrier and **removes** the interim `turn_meta` LogEntry. **Verification**: codex golden/mapper tests assert the turn extras on the event; the `turn_meta` LogEntry is gone.
4. Specs reconciled: adapter-codex.md "Canonical Model Gaps" v1-reachability note removed/updated; data-model.md + canonical-events.md describe the turn-extras carrier. **Verification**: spec-drift sweep clean.

## Analysis

Sources checked: `internal/canonical/events.go`, `internal/ingest/writer.go`, `.agents/sow/specs/{data-model.md,canonical-events.md,adapter-codex.md}`, `internal/adapters/codex/ops_event.go`.

Current state: discovered 2026-05-30 during SOW-0004 Chunk B. Codex ships an interim no-loss `turn_meta` LogEntry; this SOW migrates to the real column.

Risks:

- **R1 — Shared-surface change.** Touches `internal/canonical` + `internal/ingest`, used by every adapter. Mitigation: additive field (no existing adapter sets it → no behavior change for v2/v3/claude-code until they opt in); full gate + external review.
- **R2 — Idempotency.** Re-emitted turn finalize must not corrupt `turns.extras_json`. Mitigation: confirm against SOW-0015 idempotent-write model in the gate; test the re-emit path.

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
