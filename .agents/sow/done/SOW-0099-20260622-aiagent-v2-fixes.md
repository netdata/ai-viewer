# SOW-0099 - aiagent_v2 mapper fixes (SOW-0096 chunk 0c)

## Status

Status: completed
Sub-state: CTO-proposed. Reviewer 4 (minimax) of SOW-0096 produced the findings this SOW fixes. Depends on SOW-0097 (canonical `user_input` op kind) for the invariant #2 work; the mapper-side fixes in this SOW stand alone.

## Correction - 2026-06-22

SOW-0097 has been reframed from a `user_input`/`assistant` op-kind change into a deterministic ingestion parity-gate SOW. Any references below to "after SOW-0097 lands, emit new op kinds" are provisional. The actual v2 mapper work must follow the SOW-0097 parity spec: prove source-visible user prompts and assistant text against canonical artifacts first, then add op kinds only if the parity contract requires them.

## Implementation Update - 2026-06-25

SOW-0097 has now landed the adapter availability matrix and the independent
aiagent_v2 source extractor. That current contract supersedes the older
`user_input` / `assistant` op-kind plan below:

- `user_prompt` and `user_image` are `not_source_visible` for aiagent_v2 because
  the v2 snapshot has no separate user-message stream; source bytes are covered
  by request payload artifacts when present.
- `assistant_message` is source-visible only through `opTree.finalReport`.
- Missing producer-side request/response/reasoning bytes are represented as
  `source_unavailable`, `partial_source`, or absent according to the adapter
  matrix. The mapper must not invent data the source did not persist.
- The old failed-LLM log fallback is dismissed as a false-positive remediation
  for ai-viewer. It would synthesize `error_class` from nearby logs, which
  violates the current source-truth contract and can misattribute sensitive log
  text. The accepted behavior is the current spec: `error_class` comes from
  `attributes.error`; when the producer did not persist it, canonical preserves
  that absence.
- The remaining direct ai-viewer fix in this SOW is T11-v2-1: system ops use
  `attributes.label` when `attributes.name` is empty. The adapter and the
  independent parity source extractor must apply the same fallback so parity
  identity stays stable.

## Pre-Implementation Gate

### Problem / root-cause model

Reviewer 4 (minimax) of SOW-0096 walked the aiagent_v2 mapper end-to-end and surfaced 5 distinct gaps:

1. **T11-v2-1** — 1,186,802 `kind='system'` ops have empty `name` field. The producer writes `attributes.label` (e.g. "init", "final", "router_handoff"); the mapper reads `attributes.name`. Result: system ops are invisible in any UI list that filters by name. The `label` field lives only in `extras_json.original_kind` — not surfaced in the canonical `OpStartedEvent.Name`. **Mapper fix; in scope.**

2. **T8-v2-1 P1** — 100% of 392,555 failed LLM ops have no `error_class`. The producer's `endOp(..., 'failed')` for LLM ops drops the error attribute. SOW-0097's source-truth contract rejects a mapper-side log heuristic; the durable fix is producer-side evidence recording, not ai-viewer synthesis.

3. **T6-v2-1** — 99.1% of LLM ops have no captured `llm_request` / `llm_response` payload_refs. The producer's pre-evidence-recorder snapshots don't carry the request/response bodies. The mapper is correct; the source is the gap. **Out of scope (operator's producer-side harness).**

4. **T7-v2-1** — 0% tool_request / tool_response refs. Same producer-side root cause as T6-v2-1. **Out of scope.**

5. **T3-v2-1** — 1.45% reasoning capture rate. The producer's `appendReasoningChunk` clears `chunks = []` and increments count, but doesn't accumulate to `r.final`. The mapper correctly guards on `op.Reasoning.Final != ""` at `internal/adapters/aiagent_v2/mapper_ops.go:252-254`. **Out of scope (producer-side).**

Plus two structural items tied to SOW-0097:
6. After SOW-0097, keep the adapter-facing parity contract instead of forcing new `user_input` / `assistant` op kinds in this SOW.
7. Assistant text follows the SOW-0097 adapter matrix; no aiagent_v2 op-kind migration lands here.

The accepted ai-viewer-side item is (1). Items (2), (6), and (7) are superseded by the SOW-0097 source-truth parity contract.

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
- **Rejected**: failed LLM log fallback. Do not copy nearby log messages into `error_class`.
- **Superseded**: `user_input` / `assistant` op-kind migration. Use the SOW-0097 adapter matrix.
- **Updated tests/fixtures**: mapper test, parity source test, and aiagent_v2 golden fixtures for the system-op label fallback.

### Spec deltas to land before any test or code

1. `.agents/sow/specs/adapter-aiagent-v2.md` — document the `system` op name fallback (T11-v2-1 fix). Note that producer-side root causes (T3, T6, T7, T8) are explicitly out of scope.
2. No `canonical-events.md` op-kind change is made by this SOW.

### Existing patterns to reuse

- **`attrString` helper** at `internal/adapters/aiagent_v2/mapper_ops.go` — the new `label` fallback for system ops uses the same helper.
- **Source-truth pattern** from SOW-0097: do not infer missing producer fields from adjacent logs. Preserve absence unless the source contains the artifact.
- **`withSeed` integration tests** at `internal/presenter/sessions_testseed_test.go` — the existing per-adapter seed helpers cover most of what's needed; the new tests extend the seed with a system op carrying `label` and a failed LLM op with a recent error log.

### Risk and blast radius

- **Risk: rejected log fallback leaves `error_class` empty.** This is intentional source truth. Filling it requires the producer to persist the field.
- **Risk: existing fixtures with system ops without a `label` attribute become ambiguous.** Mitigation: the fallback only fires when `attributes.name` is empty AND `op.Kind == 'system'`, so the behavior is strictly additive — ops with both fields get `name`; ops with only `name` get `name`; ops with only `label` get `label`; ops with neither get "" (unchanged).
- **Blast radius**: aiagent_v2 system-op display name, matching parity source identity, and expected fixtures.

### Sensitive data handling

- No log message is copied into `error_class`; the rejected fallback introduces no new sensitive-data path.
- The `name` field is already canonical; no new data exposure.

### Implementation plan

**Chunk 1 — System op name fallback (T11-v2-1)**:
a. Add the `label` fallback in `mapper_ops.go:50`.
b. Extend `TestMap_SystemOpKindMapsToOpSystem` with a system op carrying
   `attributes.label` but no `attributes.name`.
c. Existing system-op tests should still pass (they didn't depend on `name` being empty).

**Chunk 2 — Failed LLM error_class log fallback (T8-v2-1 partial fix) — rejected after SOW-0097 parity contract**:
a. Do not synthesize `error_class` from neighboring log messages. The producer did
   not persist a source-visible `attributes.error`, and the parity contract says
   missing mapper support is never converted into invented source data.
b. Preserve the missing producer field as source truth. A future producer-side fix
   belongs in ai-agent, not ai-viewer.
c. No mapper test is added for the rejected heuristic.

**Chunk 3 — SOW-0097-dependent kind emission (user_input, assistant) — superseded**:
a. Do not force new op kinds from this SOW. SOW-0097's adapter matrix is the active contract.
b. No mapper test is added for the rejected kind migration.

**Chunk 4 — Documentation**:
a. Update `adapter-aiagent-v2.md` with the two new behaviors + the explicit "producer-side root causes out of scope" note.
b. Update the SOW-0096 triage doc to mark T11-v2-1 and T8-v2-1 partial as resolved.

### Validation plan

- The system-op mapper test, parity source test, and aiagent_v2 golden tests pass.
- Live-DB expectation after reingest: system ops with `attributes.label` gain a non-empty canonical `name`.
- No log-fallback query is added, so there is no hot-path cost change.

### Artifact impact plan

**New files**: none.

**Modified files** (additive only):
- `internal/adapters/aiagent_v2/mapper_ops.go` — system op name fallback
- `internal/parity/aiagent_v2_source_helpers.go` and `internal/parity/aiagent_v2_source_structural.go` — parity source fallback
- `.agents/sow/specs/adapter-aiagent-v2.md` — document the fallback
- aiagent_v2 tests and golden fixtures

**Schema impact**: none.

### Open decisions

1. **Log-fallback time window** — closed as rejected. No time window is used.
2. **Whether the log-fallback is enabled by default or opt-in via a flag** — closed as rejected. The producer must persist the missing error attribute.

### Out of scope (deferred; needs the operator's producer-side work)

- **T6-v2-1 (99% LLM payload missing)** — producer-side fix: the `aiagent_v2` producer needs to record request/response bodies in the evidence recorder so the mapper can find them. This is a change to the operator's own harness (`session-tree.ts`, `session-turn-runner.ts` per Reviewer 4), not the ai-viewer mapper.
- **T7-v2-1 (0% tool payload missing)** — same producer-side fix.
- **T3-v2-1 (1.45% reasoning capture)** — producer-side fix: `appendReasoningChunk` needs to also accumulate to `r.final`, not just `r.chunks[]`. Single-line change in `session-tree.ts`.
- **T8-v2-1 root cause** — producer-side fix: `endOp(..., 'failed')` for LLM ops needs to accept and store the error attribute.

These are documented in the SOW for the operator's awareness; the CTO can advise on the producer-side changes but cannot implement them.

## Execution Log

### 2026-06-25

- Implemented the accepted ai-viewer fix for T11-v2-1: aiagent_v2 system ops
  now use `attributes.label` as `OpStartedEvent.Name` when `attributes.name` is
  empty.
- Applied the same fallback in the independent SOW-0097 parity source extractor
  so `op_boundary` and `system_op` identities match canonical extraction.
- Updated aiagent_v2 golden fixtures for the intentional system-op name change.
- Did not implement the old failed-LLM log fallback. Current spec explicitly
  rejects synthetic error fallback; preserving the missing producer-side
  `attributes.error` is source-truth behavior, not ai-viewer technical debt.

## Validation

### 2026-06-25

- `go test -count=1 ./internal/adapters/aiagent_v2` - passed.
- `go test -count=1 ./internal/parity -run 'AIAgentV2|AdapterAvailabilityMatrix|Matrix'` - passed.
- `go test -count=1 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode` - passed while auditing sibling SOWs.

## Reviews

### 2026-06-25

- Included in the broader SOW-0099 to SOW-0102 derivative implementation review
  chunk. Final six-reviewer gate result: `PRODUCTION GRADE`.
  - `glm`: `PRODUCTION GRADE`.
  - `minimax`: `PRODUCTION GRADE`.
  - `kimi`: `PRODUCTION GRADE`.
  - `mimo`: `PRODUCTION GRADE`.
  - `deepseek`: `PRODUCTION GRADE`.
  - `qwen`: `PRODUCTION GRADE`.
- No P0/P1/P2 findings remain.
- P3 dispositions:
  - Historical planned-test name did not match the actual mapper test. Fixed in
    this SOW ledger by naming `TestMap_SystemOpKindMapsToOpSystem`.
  - Reviewer suggestions for direct unit coverage of shared lineage helpers and
    Codex root-index performance are logged under SOW-0101 because they touch
    the Codex/root-resolution slice, not aiagent_v2 behavior.
- Action taken: log-fallback and op-kind migration text was rewritten as
  rejected/superseded, matching the SOW-0097 source-truth contract.
