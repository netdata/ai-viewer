# SOW-0101 - codex mapper fixes (SOW-0096 chunk 0e)

## Status

Status: open (proposed 2026-06-22)
Sub-state: CTO-proposed. Reviewer 1 (codex) of SOW-0096 produced the findings this SOW fixes. Depends on SOW-0097 (canonical `user_input` op kind) for the 24,149-row reclassification.

## Pre-Implementation Gate

### Problem / root-cause model

Reviewer 1 (codex) walked the codex adapter end-to-end and surfaced 4 distinct gaps:

1. **T7-codex-1 P0** — tool_response refs incomplete for tools finalized by enrichment end-events. The codex flow has tools that don't get a paired `tool_request` / `tool_response` because the tool is finalized by a `event_msg.*_end` record (e.g. `event_msg.token_count`, `event_msg.compaction`) rather than the `response_item.function_call` → `function_call_output` pair. Result: 7,472 tool ops have no `tool_response` ref (638,562 tool_response refs / 646,034 tool ops = 98.8% coverage; the 1.2% miss is the end-event case). **Mapper fix; in scope.**

2. **T10-codex-2 P1** — `parent_thread_id` not decoded as fallback subagent link. The codex JSONL has a `session_meta.parent_thread_id` field (per the OpenAI codex schema, also referenced in `internal/adapters/codex/types.go:49,64`) that's parsed but never used as a subagent-link fallback. The current subagent-link path uses `source.subagent.thread_spawn.parent_thread_id` (nested, may be absent). When the nested is absent but the top-level is present, the link is missed. **Mapper fix; in scope.**

3. **T2-codex-1 P0** — 24,149 `kind='internal', name='user_input'` ops are intentional user inputs. The canonical model has no `user_input` op kind (per Reviewer 3), so codex overloads `internal`. The fix is not to reclassify manually but to (a) add `user_input` to the canonical enum (SOW-0097) and (b) re-emit those ops with the new kind. The 24,149 rows are migrated by SOW-0097's migration 0012. **Mapper fix; in scope (depends on SOW-0097).**

4. **T6-codex-1 P1** — `tool_request` overcount = 24,149 misclassified user_inputs. The 670,183 tool_request refs include 24,149 refs attached to the misclassified user_input ops. After SOW-0097's reclassification, those 24,149 refs become `user_input` payload_refs (not `tool_request`); the overcount resolves automatically. **Resolved by SOW-0097.**

5. **T5-codex-1 P1** — dynamic tool call event messages (`event_msg.dynamic_tool_call`) may only become debug logs. The mapper recognizes them at `ops_event.go:84` but the corpus may show they persist without a companion `response_item.function_call`, in which case they're orphans. Needs a corpus check. **Open question for SOW-0101 chunk 2.**

Plus one structural item tied to SOW-0097:
6. After SOW-0097 lands, emit `kind='user_input'` for user prompts at the 3 emission sites (`ops_response.go:119`, `:160`, `:171`) so future sessions don't re-create the misclassification.

### Evidence reviewed

- **SOW-0096 review record** at `SOW-0096-review-triage.md`. Items T7-codex-1, T10-codex-2, T2-codex-1, T6-codex-1, T5-codex-1.
- **Mapper source** at `internal/adapters/codex/`:
  - `ops_response.go:119`, `:160`, `:171` — the 3 `kind='internal', name='user_input'` emission sites (T2).
  - `ops_tools.go:98`, `:107` — tool finalize paths (T7).
  - `ops_enrich.go:217` — enrichment end-event handling (T7).
  - `ops_event.go:84` — dynamic tool call recognition (T5).
  - `ops_collab.go:24`, `:45` — subagent/thread_spawn handling.
  - `types.go:49`, `:64` — `session_meta.parent_thread_id` field (T10).
- **Live prod DB** at `/opt/ai-viewer/data/index.db`:
  - 24,149 ops with `kind='internal', name='user_input'`, each with 1 `tool_request` payload_ref (T2, T6).
  - 646,034 tool ops, 638,562 `tool_response` refs (98.8% coverage; 7,472 tool ops lack a response — T7).
  - 670,183 `tool_request` refs (24,149 of which are on the misclassified user_input ops — T6).
  - 72 session ops, 72 with `child_session_id` (100%; the parent_thread_id fallback is for historical/missing cases — T10).
  - 24,149 `internal` ops (T2).
- **OpenAI codex source** at `/opt/baddisk/monitoring/repos/ai/openai__codex/` — the codex writer side. The `event_msg.*_end` records are confirmed to exist; the `session_meta.parent_thread_id` field is confirmed in the JSONL schema.

### Affected contracts and surfaces

- **Modified**: `internal/adapters/codex/ops_tools.go:98,107` — when a tool is finalized by an enrichment end-event (e.g. `event_msg.token_count` end), emit a synthetic `tool_response` PayloadRef pointing at the end-event body (the tool's "response" is the end-event summary in this case).
- **Modified**: `internal/adapters/codex/ops_enrich.go:217` — when an enrichment end-event fires and the tool has no paired response, emit the synthetic response.
- **Modified**: `internal/adapters/codex/ops_collab.go:24,45` — when `source.subagent.thread_spawn.parent_thread_id` is absent, fall back to `session_meta.parent_thread_id` (top-level).
- **Modified**: `internal/adapters/codex/ops_response.go:119,160,171` — change `kind='internal', name='user_input'` to `kind='user_input'` (after SOW-0097 lands).
- **New tests** in `internal/adapters/codex/mapper_*.go`:
  1. `TestCodex_EnrichmentEndEventEmitsToolResponse` — a tool finalized by `event_msg.token_count` end gets a `tool_response` PayloadRef.
  2. `TestCodex_ParentThreadIdFallback` — a session with only `session_meta.parent_thread_id` (no nested `source.subagent.thread_spawn.parent_thread_id`) gets the child link.
  3. `TestCodex_UserInputOpKind` (depends on SOW-0097) — new sessions emit `kind='user_input'`, not `kind='internal', name='user_input'`.
  4. `TestCodex_DynamicToolCallCorpusCheck` — verify the `event_msg.dynamic_tool_call` events have a companion `response_item.function_call` (if not, log a warning for SOW-0101 follow-up).

### Spec deltas to land before any test or code

1. `.agents/sow/specs/adapter-codex.md` — document the end-event synthetic response + the parent_thread_id fallback + (post-SOW-0097) the new `user_input` op kind emission.
2. `.agents/sow/specs/canonical-events.md` — per-adapter matrix: codex emits `user_input` op kind after SOW-0097 lands.

### Existing patterns to reuse

- **Synthetic-response pattern** in claude-code's `ops_user.go:43-83` (the SOW-0100 work) — when a tool result comes from a block (no top-level echo), the mapper synthesizes a response. The codex end-event case is the same shape: synthesize a response from the end-event body.
- **Subagent-link fallback pattern** in claude-code's `ops_collab.go` (the SOW-0100 inline-sidechain work) — the parent_thread_id fallback follows the same shape: try the nested path first, fall back to the top-level field.
- **Existing test patterns** at `internal/adapters/codex/mapper_coverage_test.go` and `mapper_subturn_test.go` — extend with the new test cases.

### Risk and blast radius

- **Risk: synthetic responses for end-event tools may be misleading.** A tool that ends via `event_msg.token_count` doesn't really have a "response" the way a `function_call_output` does; it's a summary. The synthetic response is a UX convenience (so the tool op has a payload_ref) but the UI should label it as "summary" not "response". Mitigation: the synthetic response uses `Format='summary'` or a new `PayloadKind` constant `tool_summary`; the UI renders accordingly.
- **Risk: the parent_thread_id fallback may produce false positives** if the field is set for a non-subagent reason. Mitigation: only fall back when the nested `source.subagent.thread_spawn.parent_thread_id` is absent (the nested path is the authoritative one; the top-level is only a fallback).
- **Blast radius**: 4 mapper file changes + 4 new tests. The reclassification is in SOW-0097 (separate PR).

### Sensitive data handling

- The synthetic response for end-event tools points at the end-event body, which is a summary (token counts, compaction summaries). No new sensitive data exposure.
- The `parent_thread_id` is a thread identifier, not PII.

### Implementation plan

**Chunk 1 — End-event synthetic response (T7-codex-1 P0)**:
a. `ops_tools.go:98,107` + `ops_enrich.go:217` — when a tool finalize path is taken via an end-event and no `tool_response` exists, emit a synthetic response.
b. The synthetic response uses the end-event body as the payload.
c. New test `TestCodex_EnrichmentEndEventEmitsToolResponse`.

**Chunk 2 — parent_thread_id fallback (T10-codex-2 P1)**:
a. `ops_collab.go:24,45` — extend the parent_thread_id resolution to try the top-level field after the nested field.
b. New test `TestCodex_ParentThreadIdFallback`.

**Chunk 3 — Dynamic tool call corpus check (T5-codex-1 P1)**:
a. Run a corpus check against the live DB: `SELECT COUNT(*) FROM ops WHERE kind='tool' AND name LIKE 'dynamic_%' AND id NOT IN (SELECT op_id FROM payload_refs WHERE kind='tool_response')`. If the count is significant, add a corpus test to pin the contract.
b. New test `TestCodex_DynamicToolCallCorpusCheck`.

**Chunk 4 — SOW-0097-dependent kind emission (user_input, T2-codex-1)**:
a. After SOW-0097 lands, change the 3 emission sites in `ops_response.go:119,160,171` to use `kind='user_input'`.
b. New test `TestCodex_UserInputOpKind`.

**Chunk 5 — Documentation**:
a. Update `adapter-codex.md` with the synthetic response, the parent_thread_id fallback, and the new user_input kind.
b. Update the SOW-0096 triage doc to mark T7-codex-1, T10-codex-2, T2-codex-1, T6-codex-1 (resolved by SOW-0097) as done.

### Validation plan

- 4 new tests pass. The existing codex test suite (`mapper_coverage_test.go`, `mapper_subturn_test.go`) still passes.
- Live-DB SQL after deploy: 7,472 → 0 tool ops lack a `tool_response` (T7); 24,149 ops have `kind='user_input'` (T2); `tool_request` overcount resolved (T6).
- A real-world regression test: load a fixture session with an `event_msg.token_count` end-event, run through the mapper, assert the tool op has both a request and a synthetic response.

### Artifact impact plan

**New tests** in `internal/adapters/codex/mapper_*.go` — 4 new tests as listed.

**Modified files** (additive only):
- `internal/adapters/codex/ops_tools.go` — synthetic response emission
- `internal/adapters/codex/ops_enrich.go` — synthetic response emission
- `internal/adapters/codex/ops_collab.go` — parent_thread_id fallback
- `internal/adapters/codex/ops_response.go` (post-SOW-0097) — kind='user_input'
- `.agents/sow/specs/adapter-codex.md`
- `.agents/sow/specs/canonical-events.md`
- `.agents/sow/current/SOW-0096-review-triage.md`

**Schema impact**: none (SOW-0097's migration 0012 handles the data reclassification).

### Open decisions

1. **Synthetic-response `Format` value** — proposal: `Format='summary'` (new constant). Override if the operator prefers to use the existing `Format` enum values.
2. **Corpus check threshold for T5-codex-1** — proposal: 0 dynamic_tool_call ops without a companion response_item. If the corpus shows >0, the test fails and the operator triages. If the corpus is 0, the test is a regression guard.

### Out of scope (deferred)

- **T6-codex-1 resolution** — handled by SOW-0097's reclassification. No code changes in this SOW for T6.
- **The `event_msg.dynamic_tool_call` source data** — if the corpus check shows orphans, the fix may be producer-side (the codex CLI writer) and out of scope.
