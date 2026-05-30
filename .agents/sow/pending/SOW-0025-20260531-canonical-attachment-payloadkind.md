# SOW-0025 - canonical attachment PayloadKind (file/user attachments)

## Status

Status: open

Sub-state: proposed follow-up, awaiting operator prioritization. Discovered during SOW-0005 (opencode adapter) round-4 external review (codex P2-3). Not blocking SOW-0005 — opencode ships an interim no-loss representation (an INF LogEntry carrying the attachment metadata).

## Requirements

### Purpose

Give user/file attachments a first-class canonical representation. The canonical `PayloadRefEvent.PayloadKind` set is `llm_request | llm_response | llm_sdk_request | llm_sdk_response | llm_reasoning | tool_request | tool_response | log` (`internal/canonical/events.go`). None represents a USER file attachment (an image/file a user attached to a turn). opencode's `file` part carries exactly that (filename/url/mime). SOW-0005 round-4 surfaced that the adapter was emitting a non-canonical `PayloadKind: "user_attachment"` — a contract violation — and fixed it by emitting an INF `LogEntryEvent` with the attachment metadata in extras instead (no data loss, canonical-clean, but not a first-class payload servable via the future `/api/payloads`). This SOW adds a canonical attachment payload kind so file attachments across adapters (opencode today; codex/claude-code likely have analogues) are a first-class, servable payload.

### User Request

Implied by the data model (payloads are first-class) + opencode's file parts. SOW-0005 round-4 review flagged the canonical-contract gap.

### Assistant Understanding

Facts:

- `internal/canonical/events.go`: `PayloadRefEvent.PayloadKind` documents 8 kinds; none is a user/file attachment.
- `internal/adapters/opencode/mapper_emitters.go`: a `file` part now emits an INF `LogEntryEvent` ("file attachment", extras `{filename,url,mime}`) — the SOW-0005 round-4 interim (was a non-canonical `user_attachment` PayloadRef).
- The `/api/payloads` serving route is itself Phase 2 (unregistered today), so the interim LogEntry loses no currently-served capability.

Inferences:

- Cleanest carrier: add a canonical `user_attachment` (or `attachment`) value to the `PayloadRefEvent.PayloadKind` set, document it in `canonical-events.md` + `data-model.md`, ensure the ingest writer accepts it (payload_refs.kind is TEXT — likely no enum constraint, confirm), and the presenter/UI renders it. Then opencode's `file` part emits a PayloadRef of that kind (LocationURI = the file url / `opencode-sqlite://` ref) instead of (or in addition to) the LogEntry.
- This is a shared-surface change (canonical + ingest + presenter + every adapter that has attachments) — deliberately out of SOW-0005's adapter-additive scope.

Unknowns:

- Whether `/api/payloads` (Phase 2) should land first so the attachment payload is actually servable, or whether the kind can be added ahead of the serving route. Sequence in the gate.
- Whether codex/claude-code/ai-agent have attachment analogues to map at the same time (cross-adapter consistency).

### Acceptance Criteria

1. The canonical `PayloadRefEvent.PayloadKind` set includes an attachment kind; `internal/canonical` + `data-model.md` + `canonical-events.md` document it. **Verification**: `go test` for canonical; spec-drift sweep clean.
2. The ingest writer persists the new kind into `payload_refs.kind`; a presenter/UI surface renders it (or it is explicitly deferred with the kind reserved). **Verification**: ingester + presenter tests.
3. The opencode `file` part emits a PayloadRef of the attachment kind (replacing or complementing the interim LogEntry); golden updated. **Verification**: an opencode golden with a file part asserts the canonical attachment PayloadRef.
4. Specs reconciled: adapter-opencode.md file-part mapping updated; SOW-0005 round-4 interim note resolved. **Verification**: spec-drift sweep clean.

## Analysis

Sources checked: `internal/canonical/events.go`, `internal/ingest/writer.go` (payload_refs upsert), `internal/adapters/opencode/mapper_emitters.go`, `.agents/sow/specs/{canonical-events.md,data-model.md,adapter-opencode.md}`. Discovered 2026-05-31 during SOW-0005 round-4 review.

Risks:

- **R1 — Shared-surface change.** Touches canonical + ingest + presenter + adapters. Mitigation: additive enum value (no existing kind changes); full gate + external review.
- **R2 — Serving route ordering.** `/api/payloads` is Phase 2; the kind may land before it is servable. Mitigation: the gate sequences this; the kind is reserved + rendered even if served later.

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

Pending. Related canonical-carrier-gap follow-ups: SOW-0021 (turn extras), SOW-0023 (session provider), SOW-0024 (per-source health counts).
