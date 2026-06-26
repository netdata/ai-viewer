# SOW-0025 - canonical attachment PayloadKind (file/user attachments)

## Status

Status: deferred (2026-06-15)

Sub-state: CTO decision — defer until the serving route (`/api/payloads`, Phase 2) exists or the operator wants an attachments gallery. Discovered during SOW-0005 round-4 external review (codex P2-3). Not blocking — opencode ships an interim no-loss INF LogEntry carrying filename/url/mime (`internal/adapters/opencode/mapper_emitters.go:92`), so zero data is lost while this waits.

### Deferral rationale (2026-06-15 CTO call)

- **Schema should follow the serving route, not precede it.** `/api/payloads` is Phase 2 and unbuilt; committing a schema now is designing blind.
- **`payload_refs.op_id` is `NOT NULL REFERENCES ops(id)`** (`internal/store/migrations/0001_initial.sql:222`) — a user attachment has no owning op (it's user-supplied context, not an op artifact), so the naive "add an attachment kind to payload_refs" would either FK-roll-back the batch or require making op_id nullable. This is the crux the interim LogEntry exists to avoid.
- **Attachments are minor** for an app whose primary purpose is "see what your AI agents did"; Milestone A (runnable app) is the priority.
- **Two design options were analyzed** (surfaced to the operator 2026-06-15), trading off UX-flexibility vs schema-cleanliness:
  - **Schema-A** (separate `attachment_refs` table, session/turn-scoped, dedicated `/api/sessions/:id/attachments` endpoint): cleaner contract (keeps `payload_refs` op-scoped, NOT NULL); but a separate table means showing attachments inline with op payloads later needs a union.
  - **Schema-B** (make `payload_refs.op_id` nullable + `attachment` kind, serve via `/api/payloads`): more UX-flexible (a gallery is just a filtered query on `payload_refs`); but weakens the op-scoped `payload_refs` contract.
  - Note (corrected during review): on UX-flexibility B is the superset (B→gallery is cheap; A→inline is awkward); on schema-cleanliness A wins. The tension is real and resolving it needs the serving-route design.
- **Original default when revisited:** Schema-A (clean contracts) served via a dedicated endpoint, unless the then-current `/api/payloads` design makes Schema-B materially simpler. This was superseded by the operator guidance recorded below: default to Schema-B unless current `/api/payloads` design evidence makes Schema-A materially better.

Reopen when: `/api/payloads` lands, OR the operator requests an attachments gallery, OR a cross-adapter consistency need appears (codex/claude-code/ai-agent attachment analogues).

### SOW-0097 lineage audit (2026-06-26)

The `/api/payloads` route now exists, so this SOW's original revisit trigger is
true. The SOW is still not SOW-0097 or SOW-0099 through SOW-0102 derivative
debt:

- It was created by SOW-0005 as a first-class attachment schema/product follow-up,
  before the SOW-0097 parity program.
- SOW-0097 represents source-visible attachment references through the
  `attachment_metadata` parity class and adapter matrices.
- Adapter specs explicitly distinguish attachment metadata from attached payload
  bytes. Attached file/image bytes are not implied unless the adapter matrix
  says a payload artifact exists.

Disposition for the SOW-0097 close-out audit: keep SOW-0025 pending as a
separate attachment design SOW. It should be picked up on its own merits now
that `/api/payloads` exists, but it does not block closing SOW-0097 lineage
unless a future parity matrix change claims attachment payload bytes and leaves
them unmapped.

### When-revisited default (operator guidance 2026-06-15)

The operator's standing principle for unresolved UX questions: **"do the thing that enables better/more future potentials."** Applied here, that resolves the Schema-A vs Schema-B tension toward **Schema-B** (make `payload_refs.op_id` nullable + an `attachment` kind, serve via `/api/payloads`), because B is the UX superset:

- B → dedicated gallery: cheap (a filtered query on `payload_refs WHERE kind='attachment'`).
- B → inline-with-payloads: natural (same table/route).
- A → inline-with-payloads: awkward (needs a union across two tables).

So B keeps more UX doors open, which is the operator's stated tiebreaker (especially since the operator has not yet seen the UI and cannot judge the UX directly). The schema-cleanliness cost of B (weakening the op-scoped `payload_refs` contract with a nullable `op_id`) is the accepted trade-off for future UX flexibility — consistent with "long-term-best always wins" when the long-term shape depends on a UX that isn't designed yet. When this SOW is picked up, implement Schema-B unless the then-current `/api/payloads` design makes Schema-A materially better.

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
