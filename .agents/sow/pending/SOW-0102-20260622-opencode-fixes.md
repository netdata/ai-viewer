# SOW-0102 - opencode mapper fixes (SOW-0096 chunk 0f)

## Status

Status: open (proposed 2026-06-22)
Sub-state: CTO-proposed. Reviewer 6 (kimi) of SOW-0096 is pending; this SOW is the fix-side plan that will be refined after Reviewer 6's fact-finding pass. Depends on SOW-0097 for the `user_input`/`assistant` kind emission.

## Correction - 2026-06-22

SOW-0097 has been reframed from `user_input`/`assistant` kind emission into a deterministic ingestion parity-gate SOW. Any references below to "after SOW-0097 lands, emit new op kinds" are provisional. The opencode fix must follow the SOW-0097 parity spec: first prove which request/message/tool artifacts exist in the opencode SQLite source, then map them to canonical artifacts or document source-unavailable cases.

## Pre-Implementation Gate

### Problem / root-cause model

The CTO's baseline (verified after Reviewer 3's correction) shows three opencode gaps:

1. **0 `llm_request` payload_refs** out of 275,180 LLM ops. Either the opencode SQLite schema is genuinely request-less (request bodies are reconstructed from previous turn responses, not stored separately), or the mapper is not reading the request side of the schema. **TBD via Reviewer 6 fact-finding.**

2. **0 `tool_request` payload_refs** out of 461,940 tool ops, but **455,103 `tool_response` payload_refs** (98.5% coverage on response side). The same source-side or mapper-side question as #1. The 0.48 ratio of `llm_response:llm_op` (133,119 / 275,180) suggests opencode is response-heavy by design. **TBD via Reviewer 6.**

3. **5 session ops lack `child_session_id`** out of 3,189 (99.8% link rate — the 5 missing need to be identified and the cause documented). Probably edge cases (failed parses, schema mismatches) rather than a systemic gap. **Mapper investigation; in scope.**

The reasoning capture rate is the **best** of the 5 adapters (160,976 `llm_reasoning` refs / 161,668 reasoning ops = 99.6% coverage). The pattern in `mapper_parts.go` (parts-based dispatch) is the model the other adapters should follow for request-side coverage.

### Evidence reviewed

- **SOW-0096 review record** at `SOW-0096-review-triage.md`. Open questions for Reviewer 6 are in §"Open SOW Questions" there.
- **Mapper source** at `internal/adapters/opencode/`:
  - `mapper_emitters.go:24-28` — assistant text → `llm_response` PayloadRef (no `OpAssistant` op at all).
  - `mapper_parts.go` — parts-based dispatch; the part-kind enum drives the op emission.
  - `mapper_ops.go`, `mapper_tools.go`, `mapper_turn.go` — the other dispatch paths.
  - `conn.go` — the SQLite connection.
- **Opencode source** at `/opt/baddisk/monitoring/repos/ai/anomalyco__opencode/` — the schema lives here. Reviewer 6 needs to read this to determine if the schema has a request side.
- **Live prod DB** at `/opt/ai-viewer/data/index.db`:
  - 275,180 LLM ops, 133,119 `llm_response` refs, 160,976 `llm_reasoning` refs, 0 `llm_request` refs.
  - 461,940 tool ops, 455,103 `tool_response` refs, 0 `tool_request` refs.
  - 3,189 session ops, 3,184 with `child_session_id` (99.8%).
  - 161,668 reasoning ops, 160,976 `llm_reasoning` refs (99.6%).

### Affected contracts and surfaces

- **TBD by Reviewer 6**: if the schema is request-less, this SOW becomes a documentation SOW (document the opencode model — request bodies reconstructed, not stored) and the SOW-0096 invariant for "LLM requests captured" exempts opencode. If the schema has request data and we're not reading it, the fix is a mapper change.
- **Modified (depends on SOW-0097)**: `internal/adapters/opencode/mapper_emitters.go` — emit `kind='user_input'` for user prompts + `kind='assistant'` for assistant text. The bodies are likely already in the source (the response side is captured at 99.6% rate, so the model is clearly available).
- **Modified**: `internal/adapters/opencode/mapper_ops.go` (or similar) — investigate the 5 missing `child_session_id` cases; fix or document.
- **New tests** (after Reviewer 6's fact-finding):
  1. `TestOpencode_LLMRequestPayloadRef` (if source-side fix) — every LLM op has both a `llm_request` and a `llm_response` PayloadRef.
  2. `TestOpencode_ToolRequestPayloadRef` (if source-side fix) — every tool op has both a `tool_request` and a `tool_response` PayloadRef.
  3. `TestOpencode_MissingChildSessionId` — pin the contract that all session ops have `child_session_id`; the current 5 missing are documented as known exceptions with the cause.

### Spec deltas to land before any test or code

1. `.agents/sow/specs/adapter-opencode.md` — document the request-side gap (source-side or mapper-side) per Reviewer 6. Either:
   - (a) "opencode is request-less by design; the SOW-0096 invariant for llm_request/tool_request exempts opencode" (per the per-adapter matrix), OR
   - (b) "the request side is in the source but the mapper doesn't read it; fix in this SOW".
2. `.agents/sow/specs/canonical-events.md` — update the per-adapter matrix for opencode after Reviewer 6 resolves the request-side question.

### Existing patterns to reuse

- **`mapper_parts.go` part dispatch** — the parts-based dispatch is the model the other adapters should follow. After this SOW, the pattern should be documented as the "if the harness exposes a parts model, emit per-part op kinds + payload_refs" recipe.
- **The 4 other adapters' `withSeed` integration tests** — once Reviewer 6 decides the source-vs-mapper question, the new tests follow the same shape as the codex and claude-code SOWs.

### Risk and blast radius

- **Risk: if the source is request-less, the SOW-0096 invariant for "LLM requests captured" must exempt opencode.** This is a contract decision that the operator may want to review. The exemption is the right answer if the source genuinely doesn't carry requests; documenting it explicitly in the per-adapter matrix is the deliverable.
- **Risk: the 5 missing `child_session_id` cases may be unfixable** if the opencode schema doesn't carry the relevant session_id for those specific 5 sessions. The investigation either finds a fix or documents the exception.
- **Blast radius**: 1-2 mapper file changes + 1-3 new tests (depending on Reviewer 6's findings). Minimal if the source-side hypothesis is correct.

### Sensitive data handling

- If the opencode source has request bodies we're not reading, they're already on disk in the opencode SQLite file; reading them is not a new exposure.
- The 5 missing child_session_id sessions are already in the DB; fixing the link is not a new exposure.

### Implementation plan

**Chunk 1 — Reviewer 6 fact-finding pass (already in the SOW-0096 plan)**:
a. Reviewer 6 reads `anomalyco__opencode/` to determine if the SQLite schema has a request side.
b. Reports the finding to the SOW-0096 triage doc.

**Chunk 2 — Source-side resolution (if Reviewer 6 says "no request side in source")**:
a. Document the opencode model in `adapter-opencode.md`.
b. Exempt opencode from the SOW-0096 `llm_request` / `tool_request` invariants in the per-adapter matrix.
c. No code changes.

**Chunk 3 — Mapper-side resolution (if Reviewer 6 says "request side in source, mapper doesn't read it")**:
a. Extend the parts dispatch in `mapper_parts.go` to emit `llm_request` and `tool_request` PayloadRefs.
b. New test `TestOpencode_LLMRequestPayloadRef` + `TestOpencode_ToolRequestPayloadRef`.

**Chunk 4 — Subagent-link gap investigation**:
a. Identify the 5 sessions missing `child_session_id`.
b. Trace each through the mapper to find the cause.
c. If a fix is possible, write a test + the fix. If not, document the exception.

**Chunk 5 — SOW-0097-dependent kind emission (user_input, assistant)**:
a. After SOW-0097 lands, emit `kind='user_input'` + `kind='assistant'` in `mapper_emitters.go`.

**Chunk 6 — Documentation**:
a. Update `adapter-opencode.md` with the request-side resolution.
b. Update the per-adapter matrix.
c. Update the SOW-0096 triage doc.

### Validation plan

- 1-3 new tests pass. The existing opencode test suite still passes.
- Live-DB SQL after deploy:
  - If mapper fix: 275,180 LLM ops have 275,180 `llm_request` refs (was 0); 461,940 tool ops have 461,940 `tool_request` refs (was 0).
  - If source-side: numbers unchanged, but the per-adapter matrix is updated to document the exemption.
  - Either way: 5 → 0 missing child_session_id sessions, OR documented as known exceptions.

### Artifact impact plan

**New tests** (after Reviewer 6):
- `internal/adapters/opencode/mapper_*.go` — 1-3 new tests

**Modified files** (additive only, depending on Reviewer 6):
- `internal/adapters/opencode/mapper_parts.go` (or `mapper_emitters.go` / `mapper_ops.go`) — request-side emission
- `internal/adapters/opencode/mapper_ops.go` — child_session_id gap investigation
- `.agents/sow/specs/adapter-opencode.md`
- `.agents/sow/specs/canonical-events.md`
- `.agents/sow/current/SOW-0096-review-triage.md`

**Schema impact**: none (unless the investigation finds that a new field is needed, which is unlikely).

### Open decisions

1. **Source-side vs mapper-side** — the central question. Reviewer 6 resolves.
2. **Whether the parts-dispatch pattern is documented as the canonical recipe** — proposal: yes, in the per-adapter matrix section of `canonical-events.md`. Override if the operator prefers to keep it implicit.

### Out of scope (deferred)

- **The 5 missing child_session_id cases** — if the investigation finds they're unfixable, document and move on. If fixable, fix in this SOW.
- **The SOW-0096 framework + SQL reviewers** — paused per operator directive; re-dispatched after SOW-0097..0103 land.
- **The reasoning capture rate (99.6%)** — already best-in-class; nothing to fix. The pattern is the model for other adapters.
