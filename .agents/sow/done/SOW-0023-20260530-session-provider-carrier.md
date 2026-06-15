# SOW-0023 - session provider carrier (populate sessions.provider / provider_alias)

## Status

Status: completed

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

### Problem / root-cause model

`sessions.provider` and `sessions.provider_alias` are structurally unreachable. `SessionStartedEvent` (`internal/canonical/events.go:149`) carries `Model` but no provider fields; `applySessionStarted` (`internal/ingest/writer.go:584`) writes provider columns only on `ops`, never `sessions`. Opencode knows the per-message provider alias (`mr.ProviderID`) and the canonical mapping (`canonicalProvider`), but surfaces them per-op + in `SessionStarted.Extras.providerID` only — not in the session columns the data-model promises.

### Design decision (CTO)

Carrier = `Provider` + `ProviderAlias` fields on `SessionStartedEvent` (the session's provider is known at start from the session.model JSON; no mid-session carrier needed). Also add to `SessionUpdatedEvent` for the last-known refresh case (a session that discovers its provider via the first op when session.model is absent). Writer marshals both into `sessions.provider`/`sessions.provider_alias` with the same `COALESCE(NULLIF(excluded.x,''), sessions.x)` idempotency as `agent_name`/`model`/`cwd` (re-emit safety under tail re-feed). Opencode populates them from `mr.ProviderID` (alias) + `canonicalProvider(alias)` (canonical).

**Multi-provider decision (the gate's open question): "primary = last-known".** A single session column cannot represent a multi-provider session; the op-scoped data stays authoritative. "Last-known" is chosen because (a) it's the same semantics as the existing `sessions.model` (last-known, updated via SessionUpdated); (b) opencode's session.model is itself the session-level declaration, so it IS the primary signal; (c) a NULL-when-multi-provider rule would make the column empty for the most common multi-provider case, defeating its UI purpose. Documented in the spec.

### Evidence reviewed

- `internal/canonical/events.go:149-179` — `SessionStartedEvent` (Model, no provider); `:188-199` — `SessionUpdatedEvent` (Model, no provider).
- `internal/ingest/writer.go:584-606` — `applySessionStarted` sessions UPSERT (no provider/provider_alias columns).
- `internal/ingest/writer.go:650-669` — `applySessionUpdated` UPDATE (no provider columns).
- `internal/adapters/opencode/mapper.go:244-256` — SessionStarted build (has `mr` = modelRef with ProviderID).
- `internal/adapters/opencode/mapper_turn.go:293` — `canonicalProvider(alias)` mapping.
- `data-model.md` §sessions — `provider`, `provider_alias` columns defined.
- `internal/adapters/aiagent_v3`, `claude_code`, `codex` — each builds SessionStarted; none sets provider today (will leave empty → COALESCE keeps existing → no behavior change until opt-in).

### Affected contracts and surfaces

- `internal/canonical/events.go` — `Provider` + `ProviderAlias` on `SessionStartedEvent` AND `SessionUpdatedEvent` (string fields; empty = no change on update).
- `internal/ingest/writer.go` — both `applySessionStarted` (add `provider`, `provider_alias` cols + `COALESCE(NULLIF(...))` ON CONFLICT) and `applySessionUpdated` (add the two cols to the UPDATE).
- `internal/adapters/opencode/mapper.go` — SessionStarted sets `Provider`/`ProviderAlias` from `mr`.
- Specs: `canonical-events.md`, `data-model.md` (provider carrier + last-known semantics), `adapter-opencode.md`.

### Spec deltas

- `canonical-events.md` — add `Provider`/`ProviderAlias` to both SessionStarted and SessionUpdated structs; document last-known semantics.
- `data-model.md` §sessions — note `provider`/`provider_alias` are populated from the SessionStarted/Updated carrier (last-known; op-scoped data authoritative for multi-provider).
- `adapter-opencode.md` — note opencode populates the session provider from session.model.

### Risk and blast radius

Low–moderate, shared surface. Additive fields (no adapter sets them until opt-in → COALESCE keeps existing values → no behavior change). Writer change is two columns per UPSERT/UPDATE. Opencode opt-in is one event build. Re-emit idempotent via COALESCE(NULLIF). Same canonical-field-add ripple as SOW-0021 (all adapter goldens render the new fields as `""` → verify/regenerate). Full gate; CTO-discretion on the 5-reviewer cycle (shared canonical+ingest surface — likely run it given it's the 2nd such change this session and the cumulative diff is growing).

### Implementation plan

1. `canonical/events.go`: `Provider`, `ProviderAlias string` on SessionStarted + SessionUpdated.
2. `writer.go`: add the two columns to both UPSERTs (SessionStarted INSERT + ON CONFLICT; SessionUpdated UPDATE) with COALESCE(NULLIF) idempotency.
3. `opencode/mapper.go`: SessionStarted sets `Provider: canonicalProvider(mr.ProviderID)`, `ProviderAlias: mr.ProviderID`.
4. Specs.
5. Tests: canonical round-trip (already parametric — verify); ingester test asserting SessionStarted{Provider,ProviderAlias} lands in the columns + re-emit COALESCE idempotency; opencode golden/mapper test asserting the session provider fields.
6. Regenerate adapter goldens (the new empty fields render).

### Validation plan

- `go test -race ./internal/canonical/... ./internal/ingest/... ./internal/adapters/...`: green.
- Full `go test -race ./...` green; coverage/lint/spec-drift/secrets gates green.

### Open decisions

- 5-reviewer cycle: run it (2nd shared-surface change this session, cumulative diff growing). Decide after implementation.

## Implementation

Implemented 2026-06-15 (Phase: Development, CTO-coded). Carrier = `Provider` + `ProviderAlias` string fields on `SessionStartedEvent` AND `SessionUpdatedEvent`; writer marshals into `sessions.provider`/`sessions.provider_alias` with `COALESCE(NULLIF(excluded.x,''), sessions.x)` idempotency (re-emit keeps existing). Multi-provider decision: "primary = last-known" (op-scoped stays authoritative; same semantics as `sessions.model`). Opencode populates both from `session.model`.

- `internal/canonical/events.go` — `Provider`/`ProviderAlias` on SessionStarted + SessionUpdated.
- `internal/ingest/writer.go` — `applySessionStarted` adds the two columns + COALESCE ON CONFLICT; `applySessionUpdated` adds them to the UPDATE.
- `internal/adapters/opencode/mapper.go` — SessionStarted sets `Provider: canonicalProvider(mr.ProviderID)`, `ProviderAlias: mr.ProviderID`.
- Specs: `canonical-events.md` (fields on both events), `data-model.md` §sessions (carrier + last-known semantics), `adapter-opencode.md` (opencode populates from session.model).
- Tests: `TestWriter_SessionProviderCarrier` (SessionStarted write + empty-re-emit COALESCE-keeps + SessionUpdated updates + empty-provider-update untouched); opencode `TestMapSession_RootSessionStarted` extended to assert Provider/ProviderAlias.
- Goldens regenerated across all adapters (the new empty fields render on session_started/session_updated lines).

## Validation

All gates green 2026-06-15.

- `TestWriter_SessionProviderCarrier`: PASS (write; empty-re-emit keeps; SessionUpdated updates; empty-update keeps).
- opencode `TestMapSession_RootSessionStarted`: PASS (Provider/ProviderAlias asserted).
- Full `go test -race ./...`: PASS. Coverage gate: PASS (aggregate 91.3%). `golangci-lint` (full `./...`): 0 issues. `go vet`/`gofmt`: clean.
- `scripts/spec-drift.sh`: PASS. `scripts/scan-secrets.sh`: PASS (914 files).

## Reviews

Phase: Development — 5-reviewer cycle is CTO-discretion. This is the 2nd shared canonical+ingest carrier this session (after SOW-0021), additive + default-preserving (COALESCE keeps existing until opt-in). The idempotency is pinned by the re-emit test. CTO judged the cumulative risk still low and **skipped** the 5-reviewer cycle; the diff is reviewable on master. The two carrier SOWs (0021 + 0023) together close the canonical-carrier-gap class; a reviewer pass over the pair before Milestone A handoff is worthwhile if the operator wants one.

## Outcome

Delivered. `sessions.provider`/`provider_alias` are now reachable: SessionStarted/Updated carry the fields, the writer persists them idempotently, and opencode populates them from `session.model`. Other adapters (ai-agent v2/v3, claude-code, codex) leave them empty today (COALESCE keeps NULL) and can opt in via the same carrier. The "primary = last-known" multi-provider decision is documented in the spec; op-scoped `ops.provider` remains authoritative for multi-provider sessions.

## Lessons / Follow-Ups

- **The canonical-carrier-gap class is now closed (SOW-0021 + SOW-0023).** Both unreachable-column classes (turns.extras_json, sessions.provider/provider_alias) have real carriers. The pattern — additive canonical field + COALESCE(NULLIF) writer + adapter opt-in + golden regen — is established for any future such gap.
- **`canonicalProvider` pass-through for unknown aliases is the right default.** An unknown alias returns unchanged rather than empty, so the session still carries attribution (better a verbatim alias than nothing); the catalog gates on `Provider != ""` so a pass-through alias still seeds a row. Documented in the spec.
