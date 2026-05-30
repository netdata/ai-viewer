# SOW-0023 - session provider carrier (populate sessions.provider / provider_alias)

## Status

Status: open

Sub-state: proposed follow-up, awaiting operator prioritization. Discovered during SOW-0005 (opencode adapter) round-1 external review (codex P2.3). Not blocking SOW-0005 — the op-scoped provider/alias is complete and authoritative; this is a denormalized session-level convenience.

## Requirements

### Purpose

Make `sessions.provider` and `sessions.provider_alias` reachable. `data-model.md` §sessions defines both columns (`provider TEXT`, `provider_alias TEXT -- user-defined provider alias (opencode); NULL otherwise`), but **no canonical session event carries provider fields**: `SessionStartedEvent` (`internal/canonical/events.go`) carries `Model` only — no `Provider`/`ProviderAlias` — and the ingest writer's `sessions` upsert (`internal/ingest/writer.go`) never writes the `provider`/`provider_alias` columns (they are written only for `ops`). So the session-level provider columns the data-model promises are structurally unreachable from any adapter. This SOW adds a session provider carrier to the canonical event model + ingest writer so adapters can populate the session-level provider columns, and wires the opencode adapter (which already knows the per-message provider) to set them.

### User Request

Implied by `data-model.md` §sessions (the columns exist) and the opencode adapter's multi-provider awareness. SOW-0005 round-1 review surfaced that the home is unwired.

### Assistant Understanding

Facts:

- `internal/canonical/events.go`: `SessionStartedEvent` = {EventBase, SessionNativeID, ParentNativeID, RootNativeID, Kind, AgentName, Model, Cwd, CallPath, Extras, ...} — no `Provider`/`ProviderAlias`. `OpStartedEvent` carries `Provider`/`ProviderAlias`.
- `internal/ingest/writer.go`: the `sessions` UPSERT maps `kind, agent_name, model, cwd, call_path, status, ...`; it does NOT reference `provider`/`provider_alias` (those are in the `ops` UPSERT only).
- `data-model.md` §sessions: `provider`, `provider_alias` columns are defined ("primary/last-known"; alias is opencode-specific).
- The opencode mapper has the per-message `providerID` (alias) and a best-effort canonical mapping; it currently surfaces them per-op + in SessionStarted `Extras.providerID` only.

Inferences:

- Cleanest carrier: add `Provider` + `ProviderAlias` (+ optionally `Model` already present) to `SessionStartedEvent` (and/or a `SessionUpdatedEvent` for last-known refresh), mirroring how ops carry them; the writer marshals them into the `sessions` columns with the same `COALESCE(NULLIF(excluded.x,''), sessions.x)` idempotency discipline used elsewhere.
- A single session-level alias is inherently lossy for **multi-provider** sessions (opencode sessions can span providers). Decide in the gate: "primary = last-known" vs "first" vs leave NULL when >1 distinct provider. The op-scoped data remains the authoritative complete record; this column is a UI convenience.
- Shared infrastructure: claude-code/codex/opencode could all populate it. Deliberately out of SOW-0005's adapter-additive blast radius.

Unknowns:

- Whether the session row should carry "primary" provider (last-known) or be NULL for genuinely multi-provider sessions. Resolve in the gate against the presenter's intended use.
- Re-emit/idempotency: SessionStarted may re-emit (tailer full-tree re-feed); the writer must not corrupt the column. Confirm against the idempotent-write model.

### Acceptance Criteria

1. `SessionStartedEvent` (and/or `SessionUpdatedEvent`) carries `Provider` + `ProviderAlias`; `internal/canonical` tests cover it. **Verification**: `go test` for canonical.
2. The ingest writer marshals session `Provider`/`ProviderAlias` into `sessions.provider`/`sessions.provider_alias`, idempotently under re-emit. **Verification**: an ingester test asserts a `SessionStartedEvent{Provider:...,ProviderAlias:...}` lands in the columns and a re-emit does not corrupt them.
3. The opencode adapter populates the session provider columns from its per-message provider (per the gate's primary-provider decision). **Verification**: an opencode golden/invariant test asserts the session event's provider fields; the `c_multi_provider` fixture exercises the multi-provider decision.
4. Specs reconciled: SOW-0005 AC#7 note resolved; `data-model.md` + `canonical-events.md` describe the session provider carrier. **Verification**: spec-drift sweep clean.

## Analysis

Sources checked: `internal/canonical/events.go`, `internal/ingest/writer.go`, `.agents/sow/specs/{data-model.md,canonical-events.md,adapter-opencode.md}`, `internal/adapters/opencode/mapper*.go`. Discovered 2026-05-30 during SOW-0005 round-1 review.

Risks:

- **R1 — Shared-surface change.** Touches `internal/canonical` + `internal/ingest` (every adapter). Mitigation: additive field (no existing adapter sets it → no behavior change until opt-in); full gate + external review.
- **R2 — Multi-provider lossiness.** A single session alias cannot represent a multi-provider session. Mitigation: the gate's primary-vs-NULL decision; the op-scoped data stays authoritative.
- **R3 — Idempotency.** Re-emitted SessionStarted must not corrupt the column. Mitigation: COALESCE/NULLIF write + a re-emit test.

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

Pending. Related: [[SOW-0021-20260530-turn-extras-carrier]] (the same class of canonical-carrier gap, for turns).
