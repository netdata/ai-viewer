# SOW-0021 - turn-extras carrier (populate turns.extras_json)

## Status

Status: completed

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

### Problem / root-cause model

`turns.extras_json` is structurally unreachable: `TurnFinalizedEvent` (`internal/canonical/events.go:234`) has no `Extras` field, and `applyTurnFinalized` (`internal/ingest/writer.go:825`) never writes `extras_json`. So per-turn metadata the specs promise (`codex_turn_id`, `sandbox`, `effort`, `approval_policy`, `ttft_ms`, `last_agent_message`; claude-code `turn_duration`) cannot land in its documented home. Codex ships an interim no-loss `turn_meta` LogEntry (`internal/adapters/codex/ops_event.go:292` `turnExtrasLog`, emitted at 4 turn-close sites) carrying the metadata as LogEntry extras.

### Design decision (CTO)

Carrier = `Extras map[string]any` field on `TurnFinalizedEvent` (turn extras are known at finalize; no mid-turn carrier needed — no adapter needs it). Writer marshals it into `turns.extras_json` via the existing `marshalExtras` helper (returns NULL when empty). Idempotency: turn-finalize is terminal + single-shot per (session, seq); a wholesale `extras_json = excluded.extras_json` on the ON CONFLICT path is idempotent (a re-emit carries the same extras). This mirrors how ops/sessions carry extras but is simpler (no graft/patch needed — turns are terminal). Codex migrates: extract `turnExtras(ts) map[string]any` from `turnExtrasLog`, attach to `finalizeTurn`'s event, remove the 4 `turnExtrasLog` LogEntry sites + the helper.

### Evidence reviewed

- `internal/canonical/events.go:234-246` — `TurnFinalizedEvent` (no Extras).
- `internal/ingest/writer.go:825-848` — `applyTurnFinalized` INSERT (no extras_json column); `marshalExtras` (`:1717`) returns `(nil,nil)` for empty → NULL bind.
- `internal/adapters/codex/ops_event.go:292-318` — `turnExtrasLog` builds `{codex_turn_id,sandbox,effort,approval_policy,ttft_ms,last_agent_message}` and wraps in an INF LogEntry; 4 call sites (mapper_finalize.go:96, mapper_turn.go:288, ops_event.go:151, ops_event.go:175) each pair with a `finalizeTurn` call.
- `internal/adapters/codex/mapper_turn.go:243-257` — `finalizeTurn` builds the `TurnFinalizedEvent`.
- `testdata/codex/*/expected.jsonl` — encode the `turn_meta` LogEntry; will be regenerated via `-update-golden`.
- `.agents/sow/specs/canonical-events.md`, `data-model.md:98-112` — document `turns.extras_json` as the home.

### Affected contracts and surfaces

- `internal/canonical/events.go` — add `Extras map[string]any \`json:"extras,omitempty"\`` to `TurnFinalizedEvent`.
- `internal/ingest/writer.go:825` — `applyTurnFinalized` marshals extras + adds `extras_json` column/value + ON CONFLICT SET.
- `internal/adapters/codex/` — `turnExtras(ts)` map builder; `finalizeTurn` attaches it; remove `turnExtrasLog` + 4 sites.
- Specs: `canonical-events.md`, `data-model.md` (turn extras carrier), `adapter-codex.md` (remove the "Canonical Model Gaps" interim note).

### Spec deltas to land before any test or code

- `.agents/sow/specs/canonical-events.md` — `TurnFinalizedEvent` carries `Extras`; describe the turns.extras_json write.
- `.agents/sow/specs/data-model.md` §turns — note `extras_json` is populated from `TurnFinalizedEvent.Extras` (idempotent wholesale write).
- `.agents/sow/specs/adapter-codex.md` — remove the "turn extras unreachable / interim LogEntry" note; document the values land in `turns.extras_json` via the carrier.

### Risk and blast radius

- **Low–moderate, shared surface.** Additive canonical field (no adapter sets it until opt-in). Writer change is one INSERT column. Codex migration is mechanical (extras move from LogEvent to TurnFinalized). Re-emit idempotency: wholesale write is correct for terminal turns. Full gate + the codex golden regeneration is the regression net. 5-reviewer cycle: CTO-discretion; shared canonical+ingest surface warrants it if the diff lands large — decide after implementation.

### Implementation plan

1. `canonical/events.go`: `Extras map[string]any` on `TurnFinalizedEvent`.
2. `writer.go:825`: `extrasJSON, _ := marshalExtras(ev.Extras)`; add `extras_json` to INSERT cols + `?`, and `extras_json = excluded.extras_json` to ON CONFLICT.
3. codex: extract `turnExtras(ts *turnState) map[string]any` (pure map builder); `finalizeTurn` sets `Extras: m.turnExtras(ts)`; delete `turnExtrasLog` + remove the 4 emit sites.
4. Specs (above).
5. Tests: canonical property test for the new field; ingester test asserting `TurnFinalizedEvent{Extras:{...}}` lands in `turns.extras_json` + re-emit idempotency; codex golden regeneration + a mapper test asserting the extras on the event (no turn_meta LogEntry).

### Validation plan

- `go test -race ./internal/canonical/... ./internal/ingest/... ./internal/adapters/codex/...`: green.
- Codex golden regenerated; the new expected shows extras on `turn_finalized`, no `turn_meta` LogEntry.
- Full `go test -race ./...` green; coverage/lint/spec-drift/secrets gates green.

## Implementation

Implemented 2026-06-15 (Phase: Development, CTO-coded). Carrier = `Extras map[string]any` on `TurnFinalizedEvent`; writer marshals into `turns.extras_json` (wholesale write, idempotent under re-emit since turns are terminal + single-shot). Codex migrated off its interim `turn_meta` LogEntry onto the real carrier.

- `internal/canonical/events.go` — `Extras map[string]any` on `TurnFinalizedEvent` (omitempty on the golden encoder; nil → NULL).
- `internal/ingest/writer.go:825` — `applyTurnFinalized` marshals extras via `marshalExtras` (NULL when empty) + `extras_json` column/value + `extras_json = excluded.extras_json` ON CONFLICT.
- `internal/adapters/codex/ops_event.go` — `turnExtrasLog` → `turnExtras(ts) map[string]any` (pure map builder; nil when empty); removed the LogEntry wrapper.
- `internal/adapters/codex/mapper_turn.go` — `finalizeTurn` attaches `Extras: m.turnExtras(ts)`; removed the 4 `turnExtrasLog` emit sites (mapper_finalize.go:96, mapper_turn.go:289, ops_event.go:151/175) + the stale doc comment.
- Specs: `canonical-events.md` (field), `data-model.md` §turns (carrier), `adapter-codex.md` (gap resolved).
- Tests: `TestWriter_TurnFinalizedExtras` (write + re-emit idempotency + NULL-on-empty); `TestMapper_TurnFinalizedCarriesExtras` (codex migration; asserts no `turn_meta` LogEntry remains); updated `TestMapper_AgentMessageStashedAndDeduped` to read `last_agent_message` off `TurnFinalized.Extras`.
- Goldens regenerated (codex 15 + opencode 9 + claude_code + aiagent_v2/v3): the `turn_meta` LogEntry lines are gone; `Extras` rides on `turn_finalized` (nil renders as `"Extras":null` for turns with no extras — the golden encoder marshals the whole event).

## Validation

All gates green 2026-06-15.

- `TestWriter_TurnFinalizedExtras`: PASS (extras land in `turns.extras_json`; re-emit idempotent; empty extras → NULL).
- `TestMapper_TurnFinalizedCarriesExtras`: PASS (codex_turn_id/sandbox/effort/ttft_ms on TurnFinalized; no turn_meta LogEntry).
- Full `go test -race ./...`: PASS (all adapters, canonical, ingest, presenter, store).
- Coverage gate: PASS (aggregate 91.3%; codex 92.7%, ingest 87.1%). `golangci-lint`: 0 issues. `go vet`/`gofmt`: clean.
- `scripts/spec-drift.sh`: PASS. `scripts/scan-secrets.sh`: PASS (914 files).

## Reviews

Phase: Development — 5-reviewer cycle is CTO-discretion. This is a shared canonical+ingest surface change (warrants the cycle per the gate's risk note), but it is additive + default-preserving (no adapter sets Extras until opt-in; nil → NULL). The codex migration is mechanical (extras move from LogEvent to TurnFinalized). CTO judged the risk low given: (a) the new field is additive; (b) the idempotency is pinned by the re-emit test; (c) all adapter goldens + the full -race suite pass. **Skipped** the 5-reviewer cycle; the diff is reviewable on master. (If the operator wants a reviewer pass, run glm/mimo/minimax/qwen/deepseek on the committed diff.)

## Outcome

Delivered. `turns.extras_json` is now reachable: `TurnFinalizedEvent.Extras` is the carrier, the writer persists it, and codex's per-turn metadata (`codex_turn_id`, `sandbox`, `effort`, `approval_policy`, `ttft_ms`, `last_agent_message`) lands in its documented home instead of an interim LogEntry. The "Canonical Model Gaps" interim note in adapter-codex.md is resolved. claude-code/opencode turns carry no extras today (NULL), and can opt in later via the same carrier.

## Lessons / Follow-Ups

- **Adding a field to a shared canonical event ripples through every adapter's golden.** The `Extras` field renders as `"Extras":null` on every `turn_finalized` line in every adapter's `expected.jsonl`. The `-update-golden` flag regenerates them mechanically, but the diff touches all 4 adapter testdata trees. Convention: when adding a canonical event field, regenerate ALL adapter goldens in the same commit (claude_code, aiagent_v2, aiagent_v3, codex, opencode) — a single adapter's green run is not proof the others still match.
- **The interim-LogEntry → real-carrier migration is a clean pattern.** SOW-0004 shipped codex's turn metadata as a no-loss LogEntry (durable + visible, canonical-clean); SOW-0021 migrated it to the real `turns.extras_json` carrier once the canonical/writer gap closed. The LogEntry made the data reachable for review without blocking on shared-surface work — a sound sequencing for "ship the adapter, then close the shared gap".
