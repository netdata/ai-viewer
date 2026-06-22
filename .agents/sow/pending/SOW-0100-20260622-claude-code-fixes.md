# SOW-0100 - claude-code mapper fixes (SOW-0096 chunk 0d)

## Status

Status: open (proposed 2026-06-22)
Sub-state: CTO-proposed. Reviewer 2 (claude) of SOW-0096 produced the findings this SOW fixes. Depends on SOW-0097 (canonical `user_input`/`assistant` op kinds) for the invariant #2/#4 work; the payload-capture fixes in this SOW stand alone.

## Pre-Implementation Gate

### Problem / root-cause model

Reviewer 2 (claude) walked the claude-code adapter end-to-end and surfaced 9 distinct gaps. They are all in the mapper — claude-code is a third-party harness and the source is a fixed contract. The gaps:

1. **T6-claude-1** — 0 `tool_request` payload_refs. The mapper reads `contentBlock.Input` only to extract `description` for the Agent tool (`ops_assistant.go:176` → `agentDescription`); for every other tool the `input` is discarded. A repo-wide grep confirms the only `PayloadKind` literals the adapter emits are `tool_response` and `log`. Result: 65,278 tool ops with 0 request bodies. **Mapper fix; in scope.**

2. **T7-claude-1** — 0 `tool_response` refs for ~52% of tools. The mapper only emits `tool_response` when the user record carries a top-level `toolUseResult` sibling (`rec.HasToolUseResult`, gated at `ops_user.go:53`). The result also lives inside the `tool_result` content block (`content[].type=="tool_result"`, `.content`), which the adapter reads for status/error but never turns into a payload. Result: 31,171 tool_response refs / 65,278 tool ops (~48% coverage; the 52% miss is the tool_result-block-only case). **Mapper fix; in scope.**

3. **R3-claude-1** — 0 `llm_reasoning` payload_refs. Each `thinking` block produces a `reasoning` op with `BytesOut=len(thinking)` but no `PayloadRef` points at the thinking text, and the cryptographic `signature` (`parser.go:116`) is parsed and dropped. Result: 20,115 reasoning ops, 0 thinking text stored. **Mapper fix; in scope.**

4. **A4-claude-1 P0/P1** — 0 `llm_response` payload_refs. The assistant's generated text (`content[].type=="text"`, `.text`) is the operator's primary artifact ("what did my agent say") but the text blocks are read only by `assistantTextTs` to detect sidechain completion markers. No `llm_response` PayloadRef, no text storage anywhere. The LLM op finalize is hardcoded `Status:"completed"` with token counts and zero body. Result: 122,679 LLM ops, 0 llm_response refs. **Mapper fix; in scope.**

5. **E8-claude-1 P1** — LLM ops can never be `failed`. `buildLLMFinalized` hardcodes `Status:"completed"` (`ops_assistant.go:62`); a `system/api_error` record is mapped to a standalone `LogEntry ERR` that is not attached to any LLM op. Result: invariant #8 (`status='failed'`) is structurally 0 for claude-code even when API errors occur. **Mapper fix; in scope.**

6. **S10-claude-1** — 70% subagent-link rate (corrected from CTO's "0/835" claim). The op→child edge depends entirely on the `.meta.json` sidecar in `subagents/agent-*.meta.json`. When the sidecar is absent (the 30% miss), the inline parent/child signals (`isSidechain`, `agentId`, `parentUuid`, `logicalParentUuid`) are **parsed but never consumed**. Result: 250 of 835 session ops lack `child_session_id`. **Mapper fix; in scope (consume the inline signals as a sidecar-independent fallback).**

7. **Inline signals parsed-but-dead** — `isSidechain`, `agentId`, `parentUuid`, `logicalParentUuid` (`parser.go:80-84`) have **zero consumers**. The adapter throws away the source's own parent/child markers. **Mapper fix; in scope (consume them as the S10-claude-1 fallback).**

8. **requestId parsed and dropped** (`parser.go:95`) — the natural deterministic key for `llm_request`/`llm_response` payload correlation and for attaching `api_error` to its op. **Mapper fix; in scope.**

9. **Coarse payload_refs** — even captured `tool_response` refs use `LocationURI` of the whole transcript with `OriginalBytes:-1` (`payloads.go:114-123`); no byte offset. The UI has to re-scan the whole file to find the specific result. **Out of scope for v1 (this SOW); deferred — the byte-offset indexing is a v2 enhancement.**

Plus two structural items tied to SOW-0097:
10. After SOW-0097 lands, emit `kind='user_input'` for user prompts (the prompt text is in the source at `rec.Message.Content`; the mapper currently discards it).
11. After SOW-0097 lands, emit `kind='assistant'` for assistant text (the source has the text; the mapper currently drops it per A4-claude-1).

### Evidence reviewed

- **SOW-0096 review record** at `SOW-0096-review-triage.md`. All 9 items above are Reviewer 2 findings with file:line evidence.
- **Mapper source** at `internal/adapters/claude_code/`:
  - `ops_assistant.go:146-180` — toolUseStarted ignores `input` for non-Agent tools (T6).
  - `ops_user.go:43-83` — block read, no payload from `blk.Content`; top-level `toolUseResult` gate (T7); tool error handling.
  - `ops_assistant.go:97-131` — reasoning op emission (R3).
  - `ops.go:34-58` — `mapAssistant`, no `OpAssistant` emission (A4).
  - `ops_assistant.go:56-75` — `buildLLMFinalized` hardcodes `Status:"completed"` (E8).
  - `ops.go:65-97` — `mapSystem` handles `api_error` as detached log (E8).
  - `parser.go:80-84` — parsed-but-dead inline signals (S10).
  - `parser.go:95` — `requestId` parsed and dropped.
  - `parser.go:115-116` — `signature` parsed and dropped.
  - `scanner_meta.go:36-88` — sidecar-only subagent discovery.
  - `resolver.go:227-340` — child link resolution (sidecar-dependent).
  - `payloads.go:109-124` — coarse payload refs.
- **Live prod DB** at `/opt/ai-viewer/data/index.db`:
  - 122,679 LLM ops, 0 `llm_response` / 0 `llm_request` / 0 `llm_reasoning` payload_refs (A4, R3).
  - 65,278 tool ops, 31,171 `tool_response` refs, 0 `tool_request` refs (T6, T7).
  - 20,115 reasoning ops, 0 `llm_reasoning` refs (R3).
  - 835 session ops, 585 with `child_session_id` (70.1%; S10-claude-1 corrected baseline).
  - 0 LLM ops with `status='failed'` (E8).

### Affected contracts and surfaces

- **Modified**: `internal/adapters/claude_code/ops_assistant.go:146-180` — emit `tool_request` PayloadRef for each `tool_use` block. Use `LocationURI=file://<transcript>` with byte offsets if available (deferred to v2 for byte offsets; for v1, the whole-transcript URI matches the existing `tool_response` pattern).
- **Modified**: `internal/adapters/claude_code/ops_user.go:43-83` — emit `tool_response` from the matched `tool_result` block whenever a block exists, independent of the top-level `toolUseResult` echo. The top-level echo remains the richer source when present.
- **Modified**: `internal/adapters/claude_code/ops_assistant.go:97-131` — emit `llm_reasoning` PayloadRef for each `thinking` block. Stash the parsed `signature` in the reasoning op's `extras_json`.
- **Modified**: `internal/adapters/claude_code/ops.go:34-58` — emit `llm_response` PayloadRef for the assistant text. The text body is `strings.Join(textBlocks, "\n")`. Use the parsed `requestId` as the natural key in `extras_json`.
- **Modified**: `internal/adapters/claude_code/ops_assistant.go:56-75` — `buildLLMFinalized` accepts a `status` parameter (default "completed"); the api_error correlation path sets `status='failed'`.
- **Modified**: `internal/adapters/claude_code/ops.go:65-97` — `mapSystem` correlates `api_error` records to the most recent LLM op in the same session via `requestId` (now captured). On correlation, set the LLM op's `status='failed'`, `error_class='api_error'`, `error_message=api_error.message`.
- **Modified**: `internal/adapters/claude_code/parser.go:80-84` — the parsed inline signals (`IsSidechain`, `AgentID`, `LogicalParentUUID`, `ParentUUID`) are now consumed by the mapper:
  - `IsSidechain=true` on a user/assistant record → the record belongs to a sub-agent, not the main session. The mapper should emit a `kind='session'` op with `child_session_id` pointing to the sub-agent's session (resolved by directory layout: `<sessionId>/subagents/agent-<agentId>.jsonl`).
  - `ParentUUID` / `LogicalParentUUID` → for child sessions, the parent session's UUID; the child→parent edge is set on the child session's `parent_session_id` field.
- **Modified**: `internal/adapters/claude_code/scanner_meta.go:36-88` — the sidecar remains the primary path; the inline signals are a fallback. The fallback fires when the sidecar is absent.
- **New tests** in `internal/adapters/claude_code/mapper_*.go`:
  1. `TestClaudeCode_ToolRequestPayloadRef` — every `tool_use` block emits a `tool_request` PayloadRef.
  2. `TestClaudeCode_ToolResponseFromBlock` — `tool_result` block (no top-level echo) emits a `tool_response` PayloadRef.
  3. `TestClaudeCode_ReasoningPayloadRef` — `thinking` block emits `llm_reasoning` PayloadRef + `signature` in extras.
  4. `TestClaudeCode_AssistantTextPayloadRef` — assistant text emits `llm_response` PayloadRef.
  5. `TestClaudeCode_ApiErrorCorrelatesToLLMOp` — `system/api_error` correlates via `requestId`, sets LLM op `status='failed'`, `error_class='api_error'`.
  6. `TestClaudeCode_InlineSidechainAsSubagentLink` — `isSidechain=true` user record produces a `kind='session'` op with `child_session_id`, even when the sidecar is absent.

### Spec deltas to land before any test or code

1. `.agents/sow/specs/adapter-claude-code.md` — document the 6 new payload_ref emission paths + the api_error correlation + the inline-sidechain fallback. Note the byte-offset deferred-to-v2.
2. `.agents/sow/specs/canonical-events.md` — per-adapter matrix: claude-code emits `llm_request` / `llm_response` / `llm_reasoning` / `tool_request` / `tool_response` payload_refs after this SOW lands. (Previously emitted only `tool_response` + `log`.)

### Existing patterns to reuse

- **`emitToolResultPayload`** at `internal/adapters/claude_code/payloads.go:109-124` — the new `tool_request` and `llm_response` and `llm_reasoning` emissions follow the same shape: open the transcript, write the relevant slice, emit a `PayloadRef` pointing at the file.
- **Golden test** at `internal/adapters/claude_code/golden_test.go` — the existing golden test fixtures cover most of the parser/mapper surface; the new tests extend with: a `tool_use` block (T6), a `tool_result` block without top-level echo (T7), a `thinking` block (R3), an assistant text block (A4), an `api_error` record (E8), a `isSidechain=true` user record (S10-claude-1).
- **Bench types test** at `internal/adapters/claude_code/bench_types_test.go` — the typed-shape tests pin the contract; extend with the new payload_kinds.

### Risk and blast radius

- **Risk: emitting payload_refs at scale inflates the DB significantly.** 122,679 LLM ops × 1 `llm_response` ref each = ~120k new rows. The `payload_refs` table already has ~2.5M rows. The growth is ~5%, within existing capacity. Mitigation: the emission is gated on the JSON content being present (skip the emission if `text == ""`); a 0-byte emission is a no-op.
- **Risk: the api_error correlation is heuristic and may match the wrong LLM op.** If two LLM ops overlap in time (theoretically impossible in claude-code's strict request/response model, but the SOW is paranoid), the correlation could mismatch. Mitigation: correlate via `requestId` (the natural key), not via time.
- **Risk: the inline-sidechain fallback produces false positives.** If a main-session record has `IsSidechain=true` for some other reason, the mapper would emit a spurious `kind='session'` op. Mitigation: only fire the fallback when the record's session ID has a corresponding `subagents/agent-<agentId>.jsonl` file in the project dir. The file presence is the disambiguator.
- **Blast radius**: 6 mapper file changes + 6 new tests. All additive. The existing tests should still pass (the new emissions are strictly additive; old behavior is preserved when the new fields are absent).

### Sensitive data handling

- The new `llm_response` payload_refs point at the assistant text, which may contain user-prompt echoes, generated code, file paths, etc. The bytes are already on disk in the transcript; the new ref is a pointer, not a copy. The data is not more exposed than it is today.
- The `signature` stashed in reasoning op `extras_json` is a cryptographic signature, not a secret. No redaction needed.
- The `requestId` is an Anthropic-provided request ID, not PII.

### Implementation plan

**Chunk 1 — Payload_ref emissions (T6, T7, R3, A4)**:
a. `ops_assistant.go:146-180` — emit `tool_request` PayloadRef for each `tool_use` block.
b. `ops_user.go:43-83` — emit `tool_response` from the matched `tool_result` block.
c. `ops_assistant.go:97-131` — emit `llm_reasoning` PayloadRef + stash `signature`.
d. `ops.go:34-58` — emit `llm_response` PayloadRef for assistant text.
e. 4 new tests, one per emission.

**Chunk 2 — LLM op status='failed' (E8)**:
a. Capture `requestId` in the parser (it's already parsed and dropped; change the drop to a keep).
b. `mapSystem` correlates `api_error` records to the most recent LLM op via `requestId`; on match, finalize the LLM op with `status='failed'`, `error_class='api_error'`, `error_message=api_error.message`.
c. New test `TestClaudeCode_ApiErrorCorrelatesToLLMOp`.

**Chunk 3 — Inline sidechain fallback (S10-claude-1)**:
a. The inline signals (`IsSidechain`, `AgentID`, `LogicalParentUUID`, `ParentUUID`) flow through to the mapper.
b. On `IsSidechain=true`, look for `subagents/agent-<AgentID>.jsonl` in the project dir; if found, emit a `kind='session'` op with `child_session_id` pointing to the sub-agent's session.
c. New test `TestClaudeCode_InlineSidechainAsSubagentLink`.

**Chunk 4 — SOW-0097-dependent kind emission (user_input, assistant)**:
a. After SOW-0097 lands, emit `kind='user_input'` for user prompts + `kind='assistant'` for assistant text.
b. New test `TestClaudeCode_UserInputAndAssistantKinds`.

**Chunk 5 — Documentation**:
a. Update `adapter-claude-code.md` with the 6 new behaviors + the inline-sidechain fallback.
b. Update the per-adapter matrix in `canonical-events.md`.
c. Update the SOW-0096 triage doc.

### Validation plan

- 6 new tests pass. The existing claude-code `withSeed` integration tests + golden tests still pass.
- Live-DB SQL after deploy: 122,679 LLM ops have 122,679 `llm_response` refs (was 0); 65,278 tool ops have 65,278 `tool_request` refs (was 0); 65,278 tool ops have ~65,000 `tool_response` refs (was 31,171); 20,115 reasoning ops have 20,115 `llm_reasoning` refs (was 0); ~all 835 session ops have `child_session_id` (was 585).
- A real-world regression test: load a fixture session, run through the full mapper, assert that the operator's turn viewer shows the assistant text and the tool request bodies.

### Artifact impact plan

**New tests in `internal/adapters/claude_code/mapper_*.go`** — 6 new tests as listed.

**Modified files** (additive only):
- `internal/adapters/claude_code/ops_assistant.go` — tool_request emission, reasoning payload, llm_response emission, buildLLMFinalized accepts status parameter
- `internal/adapters/claude_code/ops_user.go` — tool_response from block
- `internal/adapters/claude_code/ops.go` — mapAssistant emits llm_response, mapSystem correlates api_error
- `internal/adapters/claude_code/parser.go` — keep `requestId` and `signature` (was dropped)
- `internal/adapters/claude_code/scanner_meta.go` — sidecar + inline fallback co-existence
- `.agents/sow/specs/adapter-claude-code.md`
- `.agents/sow/specs/canonical-events.md`
- `.agents/sow/current/SOW-0096-review-triage.md`

**Schema impact**: none.

### Open decisions

1. **Byte-offset payload_refs** — proposal: defer to v2. The whole-transcript URI is the v1 baseline. Override if the operator wants byte offsets in v1.
2. **Where `llm_request` payload_refs come from** — claude-code's JSONL doesn't carry the request separately (it's reconstructed from the user prompt + tools), so the `llm_request` emission may need to come from the user prompt emission (which lands in SOW-0097). Decision: emit `llm_request` from the `kind='user_input'` op (post-SOW-0097) pointing at the user prompt text.

### Out of scope (deferred)

- **Byte-offset indexing** for payload_refs (v2 enhancement; would let the UI jump to the exact line in the transcript).
- **Consuming `IsSidechain` / `AgentID` for non-claude-code adapters** — claude-code-specific behavior; other adapters have their own sub-agent signals.
- **The 30% subagent-link miss that is genuinely sidecar-absent** — this is a SOW-0103 UX concern (surface "sidecar absent" as a SourceError/drift signal); the operator can decide whether to make the sidecar a hard requirement.
