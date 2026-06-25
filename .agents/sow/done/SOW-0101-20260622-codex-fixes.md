# SOW-0101 - codex mapper fixes (SOW-0096 chunk 0e)

## Status

Status: completed
Sub-state: CTO-proposed. Reviewer 1 (codex) of SOW-0096 produced the findings this SOW fixes. Depends on SOW-0097 (canonical `user_input` op kind) for the 24,149-row reclassification.

## Correction - 2026-06-22

SOW-0097 has been reframed from a `user_input` op-kind migration into a deterministic ingestion parity-gate SOW. Any references below to a SOW-0097 migration 0012 or automatic `user_input` reclassification are provisional and superseded. The codex fix must follow the SOW-0097 parity spec: prove the 24,149 source user-input artifacts and their canonical representation first, then reclassify rows or add op kinds only if the parity contract requires them.

## Implementation Update - 2026-06-25

SOW-0097 now defines the current Codex parity contract:

- Codex `user_prompt` stays represented as `kind=internal,name=user_input`
  with exact `payload_refs.kind=tool_request` selectors. The old
  `kind=user_input` migration is superseded and must not be forced.
- Codex raw provider `llm_request` / `llm_response` envelopes are
  `not_source_visible`; assistant text is represented as `assistant_message`
  through exact `llm_response` text selectors.
- Dynamic tool call and enrichment-end paths are accounted for by the current
  Codex mapper and source extractor. End-event tools emit the structural
  `op_boundary`; when the source has no response body, the missing
  `tool_response` payload remains a documented source-unavailable shape rather
  than an invented canonical payload.
- The remaining real SOW-0101 code gap was `parent_thread_id` fallback:
  upstream Codex `SessionMeta` has a top-level `parent_thread_id`. The adapter
  and parity source extractor now use it for sub-agent sessions when nested
  `source.subagent.thread_spawn.parent_thread_id` is missing.

Resolution ledger:

| Original finding | Resolution |
|---|---|
| T7-codex-1 end-event tool responses | Resolved as source-shape accounting: end-event tools are structural ops; absent response bodies are not synthesized. |
| T10-codex-2 `parent_thread_id` fallback | Fixed in `mapper_turn.go`, `types.go`, and the independent Codex source extractor. |
| T2-codex-1 `internal/user_input` | Superseded by SOW-0097 parity contract; `kind=internal,name=user_input` remains canonical for Codex user prompts. |
| T6-codex-1 `tool_request` overcount | Superseded by SOW-0097 parity contract; user prompts intentionally use `payload_refs.kind=tool_request`. |
| T5-codex-1 dynamic tool calls | Accounted for as current event/log behavior unless a future corpus audit finds orphaned source-visible tool bodies. |

## Pre-Implementation Gate

### Problem / root-cause model

Reviewer 1 (codex) walked the codex adapter end-to-end and surfaced 4 distinct gaps:

1. **T7-codex-1 P0** — tool_response refs are absent for tools finalized by enrichment end-events. SOW-0097 reclassifies this as a source-shape case: the end-event is a structural close signal, not a persisted tool response body. The adapter and source extractor must agree on the structural op boundary and must not invent a payload.

2. **T10-codex-2 P1** — `parent_thread_id` not decoded as fallback subagent link. The codex JSONL has a `session_meta.parent_thread_id` field (per the OpenAI codex schema, also referenced in `internal/adapters/codex/types.go:49,64`) that's parsed but never used as a subagent-link fallback. The current subagent-link path uses `source.subagent.thread_spawn.parent_thread_id` (nested, may be absent). When the nested is absent but the top-level is present, the link is missed. **Mapper fix; in scope.**

3. **T2-codex-1 P0** — 24,149 `kind='internal', name='user_input'` ops are intentional user inputs. The canonical model has no `user_input` op kind (per Reviewer 3), so codex overloads `internal`. SOW-0097 now decides the canonical representation through the parity spec: either those source user-input artifacts remain represented as `internal/user_input` with exact artifact metadata, or the spec adds a first-class op/payload representation and this SOW updates the mapper accordingly. **Mapper fix; in scope (depends on SOW-0097's parity contract).**

4. **T6-codex-1 P1** — `tool_request` overcount = 24,149 misclassified user_inputs. The 670,183 tool_request refs include 24,149 refs attached to the user-input ops. SOW-0097's parity contract decides whether these remain canonical user-prompt artifacts under the existing representation or move to a new artifact/op kind. **Resolved only after SOW-0097 proves the correct artifact classification.**

5. **T5-codex-1 P1** — dynamic tool call event messages are accounted for as current event/log behavior unless a future corpus audit proves source-visible orphan tool bodies. No orphan-body fix is implemented in this SOW.

Plus one structural item tied to SOW-0097:
6. After SOW-0097 lands, update the 3 user-prompt emission sites (`ops_response.go:119`, `:160`, `:171`) to match the parity-approved canonical representation.

### Evidence reviewed

- **SOW-0096 review record** at `SOW-0096-review-triage.md`. Items T7-codex-1, T10-codex-2, T2-codex-1, T6-codex-1, T5-codex-1.
- **Mapper source** at `internal/adapters/codex/`:
  - `ops_response.go:119`, `:160`, `:171` — the 3 `kind='internal', name='user_input'` emission sites (T2).
  - `ops_tools.go:98`, `:107` — tool finalize paths (T7).
  - `ops_enrich.go:217` — enrichment end-event handling (T7).
  - `ops_event.go:84` — dynamic tool call recognition (T5).
  - `types.go` / `mapper_turn.go` — subagent parent resolution from nested and top-level `parent_thread_id`.
  - `types.go:49`, `:64` — `session_meta.parent_thread_id` field (T10).
- **Live prod DB** at `/opt/ai-viewer/data/index.db`:
  - 24,149 ops with `kind='internal', name='user_input'`, each with 1 `tool_request` payload_ref (T2, T6).
  - 646,034 tool ops, 638,562 `tool_response` refs (98.8% coverage; 7,472 tool ops lack a response — T7).
  - 670,183 `tool_request` refs (24,149 of which are on the misclassified user_input ops — T6).
  - 72 session ops, 72 with `child_session_id` (100%; the parent_thread_id fallback is for historical/missing cases — T10).
  - 24,149 `internal` ops (T2).
- **OpenAI codex source** at `/opt/baddisk/monitoring/repos/ai/openai__codex/` — the codex writer side. The `event_msg.*_end` records are confirmed to exist; the `session_meta.parent_thread_id` field is confirmed in the JSONL schema.

### Affected contracts and surfaces

- **Unchanged by this SOW**: enrichment end-events remain structural close signals; no synthetic `tool_response` PayloadRef is emitted.
- **Modified**: `internal/adapters/codex/types.go` and `internal/adapters/codex/mapper_turn.go` — decode top-level `session_meta.parent_thread_id` and use it when `source.subagent.thread_spawn.parent_thread_id` is absent.
- **Modified**: `internal/parity/codex_session_metadata.go` and `internal/parity/codex_source.go` — mirror the same sub-agent kind/parent/root identity in the independent Codex source extractor.
- **Unchanged by this SOW**: Codex user prompts remain `kind=internal,name=user_input` per the SOW-0097 parity contract.
- **New tests** in `internal/adapters/codex/mapper_*.go`:
  1. `TestMapper_SubAgentTopLevelParentThreadFallback` — a session with only top-level `session_meta.parent_thread_id` is linked as a sub-agent.
  2. `TestExtractCodexSourceSessionBoundaryTopLevelParentThreadFallback` — the independent source extractor mirrors canonical session kind/parent/root identity.

### Spec deltas to land before any test or code

1. `.agents/sow/specs/adapter-codex.md` — document the `parent_thread_id` fallback and source-shape decision for end-event tools.
2. No `canonical-events.md` op-kind change is made by this SOW.

### Existing patterns to reuse

- **Source-shape pattern** from SOW-0097 — do not synthesize payloads for source records that are structural close signals only.
- **Parent fallback pattern** from existing Codex source classification — prefer the nested source marker, then use the top-level field only when the nested source marker carries no parent.
- **Existing test patterns** at `internal/adapters/codex/mapper_coverage_test.go` and `mapper_subturn_test.go` — extend with the new test cases.

### Risk and blast radius

- **Risk: end-event tools have no `tool_response` payload.** This is intentional source truth. The UI can still show the structural tool op and completion status.
- **Risk: the parent_thread_id fallback may produce false positives** if the field is set for a non-subagent reason. Mitigation: only fall back when the nested `source.subagent.thread_spawn.parent_thread_id` is absent (the nested path is the authoritative one; the top-level is only a fallback).
- **Blast radius**: Codex session parent classification in the mapper and independent source extractor, plus targeted tests/spec text.

### Sensitive data handling

- No synthetic response is emitted for end-event tools, so this SOW adds no new payload exposure.
- The `parent_thread_id` is a thread identifier, not PII.

### Implementation plan

**Chunk 1 — End-event tool response (T7-codex-1 P0) — superseded**:
a. Do not synthesize a `tool_response` from an end-event close signal.
b. Keep structural op-boundary parity as the source-truth contract.

**Chunk 2 — parent_thread_id fallback (T10-codex-2 P1)**:
a. `types.go` and `mapper_turn.go` — extend parent resolution to try the top-level field after the nested field.
b. `codex_session_metadata.go` and `codex_source.go` — mirror the same resolution in the independent parity source extractor.
c. New tests `TestMapper_SubAgentTopLevelParentThreadFallback` and `TestExtractCodexSourceSessionBoundaryTopLevelParentThreadFallback`.

**Chunk 3 — Dynamic tool call corpus check (T5-codex-1 P1)**:
a. Run a corpus check against the live DB: `SELECT COUNT(*) FROM ops WHERE kind='tool' AND name LIKE 'dynamic_%' AND id NOT IN (SELECT op_id FROM payload_refs WHERE kind='tool_response')`. If the count is significant, add a corpus test to pin the contract.
b. New test `TestCodex_DynamicToolCallCorpusCheck`.

**Chunk 4 — SOW-0097-dependent kind emission (user_input, T2-codex-1) — superseded**:
a. Keep `kind=internal,name=user_input` for Codex user prompts per the SOW-0097 parity contract.
b. No mapper test is added for a rejected `kind=user_input` migration.

**Chunk 5 — Documentation**:
a. Update `adapter-codex.md` with the parent_thread_id fallback and source-shape decisions.
b. Update the SOW-0096 triage doc to mark T7-codex-1, T10-codex-2, T2-codex-1, T6-codex-1 (resolved by SOW-0097) as done.

### Validation plan

- The new Codex mapper and source-extractor parent fallback tests pass. The existing codex test suite still passes.
- Live-DB expectation after deploy: sub-agent sessions with only top-level `parent_thread_id` gain correct parent linkage; end-event tools remain structural close signals without invented response payloads.

### Artifact impact plan

**New/updated tests** in `internal/adapters/codex/` and `internal/parity/` — parent fallback tests as listed.

**Modified files** (additive only):
- `internal/adapters/codex/types.go` — top-level parent_thread_id decoding
- `internal/adapters/codex/mapper_turn.go` — parent_thread_id fallback
- `internal/parity/codex_session_metadata.go` and `internal/parity/codex_source.go` — source extractor mirror
- `.agents/sow/specs/adapter-codex.md`

**Schema impact**: none known in this SOW. SOW-0097's parity spec decides whether any canonical/schema change is required for user-prompt artifacts.

### Open decisions

1. **Synthetic-response `Format` value** — closed as rejected. End-event tools do not invent response payloads.
2. **Corpus check threshold for T5-codex-1** — proposal: 0 dynamic_tool_call ops without a companion response_item. If the corpus shows >0, the test fails and the operator triages. If the corpus is 0, the test is a regression guard.

### Out of scope (deferred)

- **T6-codex-1 resolution** — handled by SOW-0097's reclassification. No code changes in this SOW for T6.
- **The `event_msg.dynamic_tool_call` source data** — if the corpus check shows orphans, the fix may be producer-side (the codex CLI writer) and out of scope.

## Execution Log

### 2026-06-25

- Added top-level `parent_thread_id` decoding to Codex `session_meta`.
- Updated Codex session-start mapping to use nested
  `source.subagent.thread_spawn.parent_thread_id` first and top-level
  `parent_thread_id` second for sub-agent parent linkage.
- Updated the independent Codex source extractor so `session_boundary`
  identities carry the same `Kind=sub_agent`, `ParentNativeSessionID`, and
  `RootNativeSessionID` as canonical extraction.
- Updated `adapter-codex.md` to cite the upstream `SessionMeta.parent_thread_id`
  field and define the fallback order.
- Accepted the implementation-review P2 that Codex did not prove nested
  sub-agent `session_boundary` parity and could leave a grandchild rooted at its
  direct parent. Added end-to-end nested Codex session-boundary parity coverage.
- Updated the generic ingester resolver so linked children are re-rooted to the
  top-level `parent.root_session_id`; this repairs provisional direct-parent
  roots for Codex and any future adapter with the same out-of-order shape.
- Updated the independent Codex source extractor to pre-index visible
  `session_meta` parent links and emit top-level root identities for nested
  session boundaries.
- Accepted the implementation-review P2 that canonical Codex
  `session_boundary` parity ignored stashed native lineage when the parent
  rollout was absent. The target contract is now explicit: canonical parity
  uses unresolved `extras_json.aiViewer.parentNativeId/rootNativeId` as native
  lineage evidence without creating missing session rows.
- Rejected the implementation-review P2 claim that the transitive-root CTE can
  recurse forever on a mutual-parent cycle: the recursive walk is seeded only
  from `parent_session_id IS NULL` roots, so a disconnected cycle is not
  reachable. A regression test still pins that malformed cycle handling returns
  without hanging.

## Validation

### 2026-06-25

- `go test -count=1 ./internal/adapters/codex -run 'SessionMeta|SubAgent|ParentThread|Tool|User|Dynamic|Mapper'` - passed.
- `go test -count=1 ./internal/parity -run 'CodexSource|AdapterAvailabilityMatrix|Matrix'` - passed.
- `go test -count=1 ./internal/ingest -run 'TestResolver_PropagatesNestedDirectParentRoot|TestCodexIngestNestedSubagentSessionBoundariesMatchSourceManifest'` - failed before implementation for the expected direct-parent root defect, then passed after the fix.
- `go test -count=1 ./internal/parity -run 'CodexSource|AdapterAvailabilityMatrix|Matrix'` - passed after nested-root fix.
- `go test -count=1 ./internal/ingest -run 'Resolver|CodexIngest.*SourceManifest|CodexIngestNestedSubagentSessionBoundariesMatchSourceManifest'` - passed.
- `go test -count=1 ./internal/adapters/aiagent_v2 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode ./internal/parity ./internal/paritycheck ./cmd/ai-viewer-ingest -run 'AIAgentV2|Claude|Codex|OpenCode|Parity|Source|Manifest|Diff|Canonical|Matrix|CheckParity|SessionMeta|SubAgent|ParentThread|Resolver|Nested'` - passed.
- `git diff --check` - passed.
- `go test -count=1 ./...` - passed.
- Implementation-review P2 follow-up:
  - `go test -count=1 ./internal/ingest -run 'TestCodexIngestAbsentParentSessionBoundaryUsesStashedNativeLineage'` - failed before canonical extractor fix with `session_boundary` hash/bytes mismatch, then passed after the fix.
  - `go test -count=1 ./internal/ingest -run 'TestResolver_MutualParentCycleDoesNotHangTransitiveRootRepair'` - passed, proving the mutual-parent cycle case returns instead of hanging.
  - `go test -count=1 ./internal/ingest -run 'Resolver|CodexIngest.*SessionBoundary|CodexIngestNestedSubagentSessionBoundariesMatchSourceManifest|CodexIngestAbsentParentSessionBoundaryUsesStashedNativeLineage'` - passed.
  - `go test -count=1 ./internal/parity -run 'CodexSource|ExtractCanonical|AdapterAvailabilityMatrix|Matrix'` - passed.
  - `go test -count=1 ./internal/adapters/codex -run 'SessionMeta|SubAgent|ParentThread|Tool|User|Dynamic|Mapper'` - passed.
  - `go test -count=1 ./...` - passed.
  - `git diff --check` - passed.

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
- Accepted P2 findings fixed before final convergence:
  - Codex nested sub-agent `session_boundary` parity needed an end-to-end test
    and transitive root repair.
  - Canonical Codex `session_boundary` parity needed to use stashed native
    parent/root ids when the parent rollout was absent.
  - The claimed recursive-CTE mutual-parent-cycle hang was rejected as a false
    positive because the CTE is seeded only from `parent_session_id IS NULL`;
    a regression test still pins the malformed-cycle return behavior.
- P3 dispositions:
  - Focused unit coverage for `canonicalSessionNeedsStashedLineage` would improve
    long-term maintainability, but current integration tests cover the behavior.
  - Codex root-index prepass opens each file once before full extraction; this is
    accepted for fixture/full-parity scale and can be optimized later if corpus
    size makes it measurable.
  - A code comment explaining the recursive CTE seed behavior was suggested but
    not required for closure.
- Action taken: `adapter-codex.md` now updates the state-machine parent rule;
  the SOW now records that end-event tools are structural close signals, not
  synthetic responses, and points the fallback implementation at `types.go`,
  `mapper_turn.go`, and the independent source extractor.
