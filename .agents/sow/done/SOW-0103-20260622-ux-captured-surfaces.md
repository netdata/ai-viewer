# SOW-0103 - UX: surface captured-but-unsurfaced fields (SOW-0096 chunk 0g)

## Status

Status: completed
Sub-state: Completed as SUPERSEDED on 2026-06-25 by SOW-0105, which absorbs the remaining captured-but-unsurfaced UI contract work into a complete DB/API/TypeScript/UI matrix. SOW-0103's provisional `user_input` / `assistant` op-kind assumption is stale after SOW-0097 was reframed as deterministic ingestion parity gates.

## Correction - 2026-06-22

SOW-0097 has been reframed from new op kinds into deterministic ingestion parity gates. Any references below to new `user_input`/`assistant` op kinds are provisional. This UX SOW should surface whatever canonical artifact representation the parity spec approves, whether that is new op kinds, existing `kind + name` combinations, or payload-derived artifact classes.

## Superseded - 2026-06-25

SOW-0105 owns this work now. The useful SOW-0103 chunks are absorbed as follows:

- Type extensions for session/turn/op fields: absorbed into SOW-0105's field-intent matrix.
- Turn header cache/error surfacing: absorbed.
- Span detail rows for reasoning kind, byte/char counters, provider alias, and call path: absorbed and expanded.
- Stats/provider/call-path display decisions: absorbed as matrix decisions, not automatic UI additions.
- Backend verification including `session_trace.go`: absorbed and expanded to all REST/SSE surfaces.
- Documentation/spec updates: absorbed.

The obsolete chunks are not carried forward:

- New `user_input` / `assistant` op-kind rendering is rejected as stale. SOW-0097 did not approve those op kinds; SOW-0105 uses canonical payload kinds and the existing op model unless a later spec explicitly changes the taxonomy.

This file is moved to `done/` as a closed superseded record. SOW-0105 is the active contract for the remaining UI-vs-DB gap work.

## Pre-Implementation Gate

### Problem / root-cause model

Reviewer 3 (T11-canonical-1 through T11-canonical-4) walked the frontend codebase and found that 6+ canonical fields are captured, persisted, and have zero UI surface:

1. **`reasoning_kind`** (`ops.reasoning_kind` in `events.go:295`, persisted per `schema_contract_test.go:370`) — values `'summary'|'raw'`. Distinguishes a raw chain-of-thought from a distilled summary. Neither snake_case nor camelCase appears anywhere in `frontend/src/`. The operator cannot tell the two apart in the UI, even though the data is captured.

2. **`bytes_in`, `bytes_out`** (`ops.bytes_in`/`ops.bytes_out` in `events.go:322-323`, persisted per `schema_contract_test.go:382-383`) — the size of tool I/O. For tool-heavy sessions these are the only signal of "which tool call pulled in a 50MB file." Zero frontend hits.

3. **`chars_in`, `chars_out`** (`ops.chars_in`/`ops.chars_out` in `events.go:325-327`, persisted per `schema_contract_test.go:384-385`) — character count for LLM ops. Zero frontend hits.

4. **Turn-level `tokens_cache_read`, `tokens_cache_write`** (in `events.go:257-258`, persisted per `schema_contract_test.go:342-343`) — the per-turn cache-hit ratio is a first-class cost-analysis signal. `TurnDetail` in `frontend/src/api/types.ts:278-289` surfaces `tokens_in`, `tokens_out`, `cost_usd` but **omits** the two cache token fields.

5. **Turn-level `error_class`** (in `events.go:253`, persisted per `schema_contract_test.go:339`) — surfaced 3 times in the frontend (in `Search`/related contexts), but not in `TurnDetail`. Inconsistent.

6. **`provider_alias`** (`ops.provider_alias` in `events.go:293`, persisted per `schema_contract_test.go:369`) — matters for opencode cost attribution. Zero frontend hits.

7. **`call_path`** (`sessions.call_path` in `events.go:174`, persisted per `schema_contract_test.go:289`) — matters for nested-agent topology debugging. Zero frontend hits.

8. **`sha256`** (`payload_refs.sha256` in `events.go:358`, persisted per `schema_contract_test.go:421`) — internal-use; not user-facing. Defer.

Plus the new op kinds from SOW-0097:
9. **`kind='user_input'`** — needs a UI surface. The turn viewer's TurnStep dispatches on `kind`; the new kind needs a renderer. Proposal: a `UserInputStep` similar to `AssistantStep`, with the user prompt text as the body.
10. **`kind='assistant'`** — same. The assistant's text body is in the `llm_response` payload_ref (per SOW-0100 for claude-code; per SOW-0097 + per-adapter SOWs for the others); the `AssistantStep` reads the payload_ref.

### Evidence reviewed

- **SOW-0096 review record** at `SOW-0096-review-triage.md`. Items T11-canonical-1 through T11-canonical-4.
- **Frontend source** at `frontend/src/`:
  - `api/types.ts:278-289` — `TurnDetail` interface (omits cache tokens + error_class)
  - `pages/SessionDetail/UnifiedView/UnifiedView.tsx` — the 3-zone resizable layout
  - `components/TurnView/TurnStep.tsx` — the per-op step dispatch
  - `components/SpanDetailDrawer/SpanDetailDrawer.tsx` — the side panel for op detail
  - `components/Stats/Stats.tsx` and `pages/Stats/Stats.tsx` — stats pages that could surface `provider_alias` and `call_path`
- **Backend** (the data the UI gets):
  - `internal/canonical/events.go` — the canonical types
  - `internal/presenter/session_detail.go` — the session-detail endpoint
  - `internal/presenter/session_trace.go` — the trace endpoint
- **Live prod DB** at `/opt/ai-viewer/data/index.db`: the fields are populated; a sample query confirms.

### Affected contracts and surfaces

This SOW touches every UI surface that displays ops/turns. The bulk of the work is in the React components; the backend changes are minimal (the canonical types are already exposed; the presenter just needs to include the new fields in the JSON it returns, and most do already).

- **Modified**: `frontend/src/api/types.ts`:
  - Extend `SessionListItem` with `provider_alias` and (for tool ops) `bytes_in`/`bytes_out`/`chars_in`/`chars_out`/`reasoning_kind`.
  - Extend `TurnDetail` with `tokens_cache_read`, `tokens_cache_write`, `error_class`.
  - Extend `SessionDetail` with `call_path`.
  - Add new `UserInputStep` and `AssistantStep` shapes (post-SOW-0097).
- **Modified**: `frontend/src/components/TurnView/TurnStep.tsx` — dispatch table: add `user_input` and `assistant` cases. The `assistant` case reads the `llm_response` payload_ref.
- **Modified**: `frontend/src/components/TurnView/TurnView.tsx` — the per-turn header surfaces the cache-hit ratio (turn-level `tokens_cache_read / (tokens_in + tokens_cache_read)`) and `error_class` if set.
- **Modified**: `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx` — add rows for the 7 fields. Each field is a small label + value; the existing pattern (key: value rows) extends directly.
- **Modified**: `frontend/src/pages/Stats/Stats.tsx` — surface `provider_alias` in the provider breakdown and `call_path` in the call_path filter (if it makes sense as a filter).
- **Modified**: `frontend/src/components/SessionRow/SessionRow.tsx` — add a small "raw/summary" badge for reasoning ops (uses `reasoning_kind`).
- **Modified (backend, if needed)**: `internal/presenter/session_detail.go` and `session_trace.go` — verify the new fields are included in the JSON. Most likely no change; the canonical types already expose them and the JSON encoder includes all struct fields by default. Pin via the per-field assertions in the SOW-0096 framework's invariant #11 check.
- **New tests**:
  1. `frontend/src/components/TurnView/TurnView.test.tsx` — extend with cases for `user_input` and `assistant` ops.
  2. `frontend/src/components/TurnView/TurnStep.test.tsx` — same.
  3. `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.test.tsx` — same, per field.
  4. `frontend/src/pages/Stats/Stats.test.tsx` — provider_alias surfaces in the breakdown.

### Spec deltas to land before any test or code

1. `.agents/sow/specs/ui-pages.md` — update the Session Detail / Turn View / Stats sections to document the surfaced fields.
2. `.agents/sow/specs/ui-turn-view.md` — document the new `user_input` and `assistant` step shapes.
3. `.agents/sow/specs/frontend-architecture.md` — document the "every captured canonical field must have a UI surface" invariant (SOW-0096 invariant #11) as a contract.

### Existing patterns to reuse

- **SpanDetailDrawer's key-value row pattern** — the existing rows for `status`, `error_class`, `tokens_in/out`, `cost_usd` are the pattern to follow for the 7 new fields. Each is a small label + value component.
- **TurnStep's kind dispatch table** — the existing cases for `llm`, `tool`, `reasoning`, `session`, `compaction`, `internal` are the pattern for the new `user_input` and `assistant` cases.
- **Stats's provider breakdown** — the existing breakdown by `provider` is the pattern for a `provider_alias` breakdown.
- **SessionRow's kind badges** — the existing badges for `root`/`sub_agent` are the pattern for the new `reasoning_kind` badge (raw vs summary).

### Risk and blast radius

- **Risk: the new `UserInputStep` and `AssistantStep` may have different rendering needs than the existing steps.** The user prompt may be long; the assistant text may include code blocks; both need markdown rendering. Mitigation: the existing `Markdown` component handles both; the new step components use it.
- **Risk: the cache-hit ratio display may confuse the operator** if shown as a percentage. Mitigation: show as `cache hit: 80%` (text), not a number.
- **Risk: changing the `TurnDetail` type may break consumers.** Mitigation: extend the type, don't change existing fields. The new fields are all optional in the JSON.
- **Blast radius**: every UI component that displays an op or turn. The backend changes are minimal (likely no change; just verify the presenter returns all fields).

### Sensitive data handling

- All the new fields are captured-and-persisted. The new UI surfaces don't introduce new data exposure.
- The user prompt (`UserInputStep`) may contain PII or sensitive user content. The existing `TurnView` already renders user content; this is no new exposure.

### Implementation plan

**Chunk 1 — `TurnDetail` and `SessionListItem` extension**:
a. Extend the types in `frontend/src/api/types.ts`.
b. Pin via new test: `frontend/src/api/types.test.ts` (or extend the existing `types.test.ts` if any).

**Chunk 2 — `TurnStep` dispatch**:
a. Add `user_input` and `assistant` cases in `TurnStep.tsx`.
b. The `user_input` case renders the user prompt as a quoted block.
c. The `assistant` case reads the `llm_response` payload_ref and renders the assistant text.
d. New test cases.

**Chunk 3 — `TurnView` header**:
a. Surface the cache-hit ratio + turn-level `error_class`.
b. New test.

**Chunk 4 — `SpanDetailDrawer` rows**:
a. Add 7 rows (reasoning_kind, bytes_in/out, chars_in/out, provider_alias, call_path).
b. New test.

**Chunk 5 — `SessionRow` reasoning-kind badge**:
a. Add a small "raw" / "summary" badge for reasoning ops.
b. New test.

**Chunk 6 — `Stats` page**:
a. Surface `provider_alias` in the provider breakdown.
b. Optional: `call_path` filter.
c. New test.

**Chunk 7 — Backend verification**:
a. Verify `internal/presenter/session_detail.go` and `session_trace.go` return all the new fields. Pin via integration test.
b. The SOW-0096 framework's invariant #11 check is the durable guard.

**Chunk 8 — Documentation**:
a. Update `ui-pages.md` and `ui-turn-view.md` and `frontend-architecture.md`.

### Validation plan

- 4+ new tests pass. The existing UI test suite still passes.
- Manual smoke: open a session, walk through the UI, assert that:
  - A reasoning op shows the raw/summary badge.
  - A tool op shows bytes_in/out in the side panel.
  - A turn header shows the cache-hit ratio.
  - A user_input op renders the user prompt.
  - An assistant op renders the assistant text.
- The SOW-0096 framework's invariant #11 check passes for at least one fixture per adapter.

### Artifact impact plan

**New tests** in `frontend/src/components/TurnView/`, `SpanDetailDrawer/`, `pages/Stats/`, `api/` — 4+ new tests.

**Modified files** (additive only):
- `frontend/src/api/types.ts` — extend types
- `frontend/src/components/TurnView/TurnStep.tsx` — dispatch for `user_input`, `assistant`
- `frontend/src/components/TurnView/TurnView.tsx` — turn header
- `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx` — 7 new rows
- `frontend/src/components/SessionRow/SessionRow.tsx` — reasoning-kind badge
- `frontend/src/pages/Stats/Stats.tsx` — provider_alias
- `frontend/src/api/sessions.ts` — possibly (the API client; depends on whether the fields are auto-included or need explicit handling)
- `internal/presenter/session_detail.go` — verify (likely no change)
- `internal/presenter/session_trace.go` — verify (likely no change)
- `.agents/sow/specs/ui-pages.md`
- `.agents/sow/specs/ui-turn-view.md`
- `.agents/sow/specs/frontend-architecture.md`

**Schema impact**: none.

### Open decisions

1. **Cache-hit ratio display** — proposal: text `cache hit: 80%` (when > 0). Override if the operator prefers a numeric badge.
2. **`provider_alias` vs `provider`** — proposal: surface `provider_alias` when set, fall back to `provider`. Override if the operator prefers to always show both.
3. **`call_path` filter** — proposal: optional filter; default off. Override if the operator wants it always on.
4. **The new `user_input` / `assistant` step shapes** — proposal: `UserInputStep` and `AssistantStep` mirror the existing step shapes. Override if the operator wants a different naming convention.

### Out of scope (deferred)

- **`sha256`** (`payload_refs.sha256`) — internal-use; not user-facing. The SOW-0096 framework's invariant #11 explicitly exempts it. Documented.
- **The new `OpStatus` enum (from SOW-0097) display in the UI** — depends on the SOW-0097 per-adapter SOWs. The `StatusBadge` component in `frontend/src/components/StatusViews.tsx` will need to be updated to handle the new literals. This is part of SOW-0097's chunks 2/3 (per-adapter adapter updates), not this SOW. If the SOW-0097 work extends the StatusBadge, this SOW doesn't need to touch it.
- **The SOW-0096 framework + SQL reviewers** — paused per operator directive; re-dispatched after SOW-0097..0103 land.
