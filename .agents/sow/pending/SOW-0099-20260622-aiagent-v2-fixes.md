# SOW-0099 - aiagent_v2 mapper fixes (SOW-0096 chunk 0c)

## Status

Status: open (proposed 2026-06-22)
Sub-state: CTO-proposed. Reviewer 4 (minimax) of SOW-0096 produced the findings this SOW fixes. Depends on SOW-0097 (canonical `user_input` op kind) for the invariant #2 work; the mapper-side fixes in this SOW stand alone.

## Correction - 2026-06-22

SOW-0097 has been reframed from a `user_input`/`assistant` op-kind change into a deterministic ingestion parity-gate SOW. Any references below to "after SOW-0097 lands, emit new op kinds" are provisional. The actual v2 mapper work must follow the SOW-0097 parity spec: prove source-visible user prompts and assistant text against canonical artifacts first, then add op kinds only if the parity contract requires them.

## Pre-Implementation Gate

### Problem / root-cause model

Reviewer 4 (minimax) of SOW-0096 walked the aiagent_v2 mapper end-to-end and surfaced 5 distinct gaps:

1. **T11-v2-1** — 1,186,802 `kind='system'` ops have empty `name` field. The producer writes `attributes.label` (e.g. "init", "final", "router_handoff"); the mapper reads `attributes.name`. Result: system ops are invisible in any UI list that filters by name. The `label` field lives only in `extras_json.original_kind` — not surfaced in the canonical `OpStartedEvent.Name`. **Mapper fix; in scope.**

2. **T8-v2-1 P1** — 100% of 392,555 failed LLM ops have no `error_class`. The producer's `endOp(..., 'failed')` for LLM ops drops the error attribute. The mapper has no fallback. **Mapper fix in scope (log-fallback); producer-side root cause is out of scope (operator's own harness).**

3. **T6-v2-1** — 99.1% of LLM ops have no captured `llm_request` / `llm_response` payload_refs. The producer's pre-evidence-recorder snapshots don't carry the request/response bodies. The mapper is correct; the source is the gap. **Out of scope (operator's producer-side harness).**

4. **T7-v2-1** — 0% tool_request / tool_response refs. Same producer-side root cause as T6-v2-1. **Out of scope.**

5. **T3-v2-1** — 1.45% reasoning capture rate. The producer's `appendReasoningChunk` clears `chunks = []` and increments count, but doesn't accumulate to `r.final`. The mapper correctly guards on `op.Reasoning.Final != ""` at `internal/adapters/aiagent_v2/mapper_ops.go:252-254`. **Out of scope (producer-side).**

Plus two structural items tied to SOW-0097:
6. After SOW-0097 lands, emit `kind='user_input'` for v2 user prompts (mapper change; depends on whether the producer carries the prompt text in the source — Reviewer 4 indicates the source has it but the mapper doesn't extract it).
7. After SOW-0097 lands, emit `kind='assistant'` for v2 assistant text (same dependency).

The in-scope items (1, 2, plus 6+7 once SOW-0097 is in) are all mapper-side fixes. They are independent of the producer-side gaps and can land in their own PRs.

### Evidence reviewed

- **SOW-0096 review record** at `SOW-0096-review-triage.md` (file in `current/`). Items T11-v2-1, T8-v2-1, T3-v2-1, T6-v2-1, T7-v2-1, T9-v2-1 (positive).
- **Mapper source** at `internal/adapters/aiagent_v2/mapper_ops.go:50` (the `Name: attrString(v.op.Attributes, "name")` line that drops `label` for system ops), `mapper_ops.go:252-254` (the reasoning guard), `mapper_payload.go` (no fallback for failed LLM ops' error_class).
- **Live prod DB** at `/opt/ai-viewer/data/index.db`:
  - 1,186,802 `kind='system'` ops, all with empty `name` and `extras_json = {"original_kind":"system"}` (per Reviewer 4)
  - 392,555 failed LLM ops (`status='failed'`), 0 with `error_class` populated
  - 1,343,664 LLM ops total, 6,169+6,054=12,223 payload_refs (0.9% coverage — the producer-side gap)
- **Producer's tool path** is well-designed: 4,601 "agent__final_report requires non-empty report_content field", 4,378 "MCP error -32603: Simulated MCP tool failure", 3,869 "Tool execution timed out" — the tool `error_class` IS captured correctly. This is the positive finding (T9-v2-1) that bounds the failure: the LLM path is the only broken one.

### Affected contracts and surfaces

- **Modified**: `internal/adapters/aiagent_v2/mapper_ops.go:50` — add a fallback for system ops: `if op.Kind == "system" && name == "" { name = attrString(op.Attributes, "label") }`. Pin via a new test in `coverage_test.go`.
- **Modified**: `internal/adapters/aiagent_v2/mapper_ops.go` — for failed LLM ops (`status='failed' && error_class == ''`), attempt a log-fallback by searching `log_entries` for the most recent ERR/CRITICAL log on the same session within ±1s. If found, copy its message into `error_class`. (This is a partial fix; the producer-side change in SOW-0099-future is the proper fix.) Pin via a new test.
- **Modified (depends on SOW-0097)**: `internal/adapters/aiagent_v2/mapper_*.go` — emit `kind='user_input'` for user prompts + `kind='assistant'` for assistant text. Pin via the SOW-0097 per-adapter test suite.
- **New tests**: `internal/adapters/aiagent_v2/mapper_*.go` — 3 new tests:
  1. `TestMapperV2_SystemOpNameFallback` — system ops with `attributes.label` populated get `name=label`.
  2. `TestMapperV2_FailedLLMErrorClassLogFallback` — failed LLM ops with no producer error_class get the most recent log message.
  3. `TestMapperV2_UserInputAndAssistantKinds` (depends on SOW-0097) — user prompts and assistant text emit the new op kinds.

### Spec deltas to land before any test or code

1. `.agents/sow/specs/adapter-aiagent-v2.md` — document the `system` op name fallback (T11-v2-1 fix) and the failed-LLM log fallback (T8-v2-1 partial fix). Note that the producer-side root causes (T3, T6, T7) are explicitly out of scope.
2. `.agents/sow/specs/canonical-events.md` — update the per-adapter matrix: after SOW-0097 + SOW-0099, aiagent_v2 emits `user_input` + `assistant` op kinds (the kinds, not necessarily the bodies).

### Existing patterns to reuse

- **`attrString` helper** at `internal/adapters/aiagent_v2/mapper_ops.go` — the new `label` fallback for system ops uses the same helper.
- **Log-fallback pattern** in other adapters: `internal/adapters/codex/ops_enrich.go` has a similar pattern (per Reviewer 1's T7-codex-1 finding) — the claude-code adapter may also have one. The v2 log-fallback follows the same shape.
- **`withSeed` integration tests** at `internal/presenter/sessions_testseed_test.go` — the existing per-adapter seed helpers cover most of what's needed; the new tests extend the seed with a system op carrying `label` and a failed LLM op with a recent error log.

### Risk and blast radius

- **Risk: the log-fallback is heuristic and may match the wrong log entry.** If a session has multiple LLM failures close in time, the fallback may attach the wrong message. Mitigation: limit the time window to ±1s, and prefer logs on the same op (by `op_id` association in log_entries) over session-wide searches.
- **Risk: existing fixtures with system ops without a `label` attribute become ambiguous.** Mitigation: the fallback only fires when `attributes.name` is empty AND `op.Kind == 'system'`, so the behavior is strictly additive — ops with both fields get `name`; ops with only `name` get `name`; ops with only `label` get `label`; ops with neither get "" (unchanged).
- **Blast radius**: 2 mapper file changes + 3 new tests. The SOW-0097-dependent kind-emission changes are scoped to the SOW-0097 per-adapter update.

### Sensitive data handling

- The log-fallback may copy a log message into `error_class`. Log messages can contain sensitive content (API responses with PII, file paths, etc.). The current mapper ALREADY reads log messages for other purposes; this is no new exposure. The `error_class` column is not redacted; if a future SOW redacts error messages, both the existing log-derived fields and the new fallback field are subject to the same policy.
- The `name` field is already canonical; no new data exposure.

### Implementation plan

**Chunk 1 — System op name fallback (T11-v2-1)**:
a. Add the `label` fallback in `mapper_ops.go:50`.
b. New test `TestMapperV2_SystemOpNameFallback` (uses a system op with `attributes.label="init"`).
c. Existing system-op tests should still pass (they didn't depend on `name` being empty).

**Chunk 2 — Failed LLM error_class log fallback (T8-v2-1 partial fix)**:
a. Add a helper `attachErrorClassFromLog(op, sessionLogs)` that searches `log_entries` for the closest ERR/CRITICAL log within ±1s.
b. Call it from the LLM op finalize path when `status == 'failed' && error_class == ''`.
c. New test `TestMapperV2_FailedLLMErrorClassLogFallback`.

**Chunk 3 — SOW-0097-dependent kind emission (user_input, assistant)**:
a. After SOW-0097 lands, emit `kind='user_input'` for user prompts + `kind='assistant'` for assistant text.
b. New test `TestMapperV2_UserInputAndAssistantKinds`.
c. Update the per-adapter matrix in `canonical-events.md`.

**Chunk 4 — Documentation**:
a. Update `adapter-aiagent-v2.md` with the two new behaviors + the explicit "producer-side root causes out of scope" note.
b. Update the SOW-0096 triage doc to mark T11-v2-1 and T8-v2-1 partial as resolved.

### Validation plan

- The 3 new tests pass. The existing `withSeed` integration tests still pass.
- Live-DB SQL after deploy: 1,186,802 system ops have non-empty `name` (was 0 before the fix); 392,555 failed LLM ops have non-empty `error_class` (was 0). The numbers may not be exact (the fallback is heuristic), but the order of magnitude should shift.
- Per-adapter perf check: the log-fallback adds one query per failed LLM op finalize. With 392,555 failed LLM ops in the historical data, a one-time backfill adds 1-2 seconds. The hot path cost is the per-op query (1ms) for any future failed ops; well within the existing per-op cost budget.

### Artifact impact plan

**New files**:
- `internal/adapters/aiagent_v2/mapper_v2_fixes_test.go` (3 new tests)

**Modified files** (additive only):
- `internal/adapters/aiagent_v2/mapper_ops.go` — system op name fallback + log-fallback helper
- `.agents/sow/specs/adapter-aiagent-v2.md` — document the new behaviors
- `.agents/sow/specs/canonical-events.md` — per-adapter matrix update (post-SOW-0097)
- `.agents/sow/current/SOW-0096-review-triage.md` — mark T11-v2-1, T8-v2-1 partial as resolved

**Schema impact**: none.

### Open decisions

1. **Log-fallback time window** — proposal is ±1s. The session-tree.ts turnaround for an LLM error is sub-second, so ±1s is conservative. If real failures take longer to log (e.g. retries), widen to ±5s. Default: ±1s, override if live testing shows misses.
2. **Whether the log-fallback is enabled by default or opt-in via a flag** — the heuristic could misfire on concurrent sessions. Default: enabled (the cost of a wrong error_class is low — it's a UX signal, not a correctness signal), opt-out via a hidden flag if live testing shows problems.

### Out of scope (deferred; needs the operator's producer-side work)

- **T6-v2-1 (99% LLM payload missing)** — producer-side fix: the `aiagent_v2` producer needs to record request/response bodies in the evidence recorder so the mapper can find them. This is a change to the operator's own harness (`session-tree.ts`, `session-turn-runner.ts` per Reviewer 4), not the ai-viewer mapper.
- **T7-v2-1 (0% tool payload missing)** — same producer-side fix.
- **T3-v2-1 (1.45% reasoning capture)** — producer-side fix: `appendReasoningChunk` needs to also accumulate to `r.final`, not just `r.chunks[]`. Single-line change in `session-tree.ts`.
- **T8-v2-1 root cause** — producer-side fix: `endOp(..., 'failed')` for LLM ops needs to accept and store the error attribute.

These are documented in the SOW for the operator's awareness; the CTO can advise on the producer-side changes but cannot implement them.
