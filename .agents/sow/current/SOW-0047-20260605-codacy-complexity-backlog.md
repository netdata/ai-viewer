# SOW-0047 - Codacy Complexity Backlog Reduction

## Status

Status: open

Sub-state: active 2026-06-05. First production slice
`frontend/src/state/filters.ts` is merged. Second production slice selected:
`internal/adapters/aiagent_v2/mapper.go`; implementation may proceed only for
this slice under the Pre-Implementation Gate below.

## Requirements

### Purpose

Keep ai-viewer maintainable as code scanning becomes a durable defense layer:
reduce or explicitly justify Codacy/Lizard production-code complexity without
weakening project-native tests, coverage, or quality gates.

### User Request

The operator asked for code-scanning defenses after the active SOW, including
visibility into low complexity, maintainability, and security.

### Assistant Understanding

Facts:

- SOW-0046 final local Codacy analysis on 2026-06-05 found 608 remaining Lizard
  complexity findings after critical/high security triage.
- The refreshed grouped backlog is 195 production-code findings in 84 files, 406
  test-file findings in 143 files, 7 frontend tooling-script findings in 2
  files, and 0 docs/spec findings.
- The largest production buckets are `internal/adapters/aiagent_v2/mapper.go`
  with 12 findings, `internal/adapters/claude_code/scanner.go` with 11,
  `internal/adapters/claude_code/tailer.go` with 9, and
  `internal/ingest/writer.go` with 8.

Inferences:

- Most production findings are pre-existing complexity hotspots rather than
  regressions introduced by SOW-0046.
- Production adapters and ingest paths need careful refactoring because they
  parse untrusted snapshots and sit on performance-sensitive code paths.

Unknowns:

- Which production hotspots are true maintainability defects versus intentional
  parser/state-machine density must be determined file by file with tests and
  benchmarks.

### Acceptance Criteria

- Production complexity findings are ranked by risk and change cost with file
  evidence.
- High-value production hotspots are refactored only where tests and benchmarks
  can prove unchanged behavior and acceptable performance.
- Parser, adapter, ingest, and presenter behavior touched by refactors has
  focused regression coverage before implementation changes.
- Any remaining production complexity above threshold is explicitly justified in
  specs or tracked by narrower follow-up SOWs.
- Full local gates pass and external review converges before completion.

## Analysis

Sources checked:

- `.agents/sow/current/SOW-0046-20260605-codacy-critical-high-triage.md`
- `/tmp/ai-viewer-sow0046-codacy-final-r6.json` final local Codacy result
- Refreshed grouping computed from
  `/tmp/ai-viewer-sow0046-codacy-final-r6.json`

Current state:

- Codacy/Lizard reports 195 production complexity findings across 84 files.
- Test complexity is larger by count, but tests are lower priority unless they
  block maintainability or hide production regressions.

Risks:

- Mechanical complexity reduction can make parsers less clear or slower.
- Refactoring adapter and ingest code without broad fixtures, fuzz targets, and
  benchmarks risks data-loss or ingest regressions.

## Pre-Implementation Gate

Status: ready for the first implementation slice.

Selected first slice:

- `frontend/src/state/filters.ts` URL/SSE filter helper refactor.
- Goal: remove Codacy/Lizard complexity findings from this production helper
  without changing URL filter behavior, SSE subscription mapping, or user-visible
  filter flows.
- Explicit non-goal: do not refactor backend adapter/ingest hotspots in the
  same change. `internal/adapters/aiagent_v2/mapper.go` is the likely next
  production slice after this one.

Problem / root-cause model:

- Production complexity exists in adapter parsers, tailers, ingest loops, and a
  few frontend visualization/state helpers. The root cause is accumulated
  state-machine logic and scanner-driven false-positive cleanup, not one single
  architectural defect.
- In the selected frontend helper, the main root cause is compact multi-key
  URL/SSE mapping. Lizard also appears to parse intervening TypeScript helper
  declarations as part of the preceding exported function, so helper placement
  contributes to noisy NLOC/parameter-count findings.

Evidence reviewed:

- SOW-0046 final Codacy result summary:
  - production: 195 findings, 84 files
  - tests: 406 findings, 143 files
  - scripts/tooling: 7 findings, 2 files
  - docs/specs: 0 findings
- Local Lizard evidence for the selected slice:
  - `applyPatch` at `frontend/src/state/filters.ts:115`: 69 NLOC, CCN 15,
    parameter count 10 in Lizard's TypeScript parse.
  - `filtersToSubscription` at `frontend/src/state/filters.ts:197`: 26 NLOC,
    CCN 10.
- Read-only frontend triage recommended `frontend/src/state/filters.ts` first:
  4 findings, pure URL/SSE filter logic, no rendering path, and focused Vitest
  coverage.
- Read-only backend triage recommended `internal/adapters/aiagent_v2/mapper.go`
  as the first backend slice, but it remains higher risk than the frontend
  helper because it maps source snapshots into canonical events.
- Official docs checked on 2026-06-05:
  - Lizard upstream documents warning thresholds for `nloc`,
    `cyclomatic_complexity`, `token_count`, and `parameter_count` and reports a
    function when a configured field exceeds its limit:
    `https://github.com/terryyin/lizard`.
  - Codacy docs document local/client-side analysis as the mechanism for running
    tools locally and surfacing results in Codacy dashboards:
    `https://docs.codacy.com/repositories-configure/local-analysis/client-side-tools/`.
  - React Router documents `useSearchParams()` as returning current
    `URLSearchParams` plus a setter that updates search params by navigation:
    `https://reactrouter.com/api/hooks/useSearchParams`.

Affected contracts and surfaces:

- First slice: URL-synced global filters, FilterBar URL round-trip behavior, and
  SSE subscription filter mapping.
- Later slices: adapter parsing/tailing behavior, ingest ordering,
  deduplication, SQLite write behavior, and frontend visualization helpers.
- Quality-gate and Codacy maintainability reporting.

Spec deltas landed before tests/code:

- `.agents/sow/specs/frontend-architecture.md` gained `URL Filter Contract`,
  covering:
  - array filter dimensions and comma-joined serialization,
  - strict integer parsing for `from`/`to`,
  - `applyPatch` mutation/delete/preserve rules,
  - `clearFilters` delete behavior,
  - `filtersToSubscription` omission rules and deliberate `q` drop.
- No REST, SSE protocol, schema, or backend spec change is required for this
  behavior-preserving frontend refactor.

Existing patterns to reuse:

- Spec -> test -> code workflow.
- `ARRAY_FILTER_KEYS` as the single ordered list of array filter dimensions.
- Small helper functions with exhaustive `switch` statements rather than unsafe
  dynamic object writes.
- Focused frontend unit tests plus Playwright coverage for UI-visible behavior.

Risk and blast radius:

- Low for the selected frontend helper simplification with existing focused
  tests.
- No schema, API, storage, adapter, ingest, or benchmark-sensitive code changes
  are expected in the first slice.
- Medium to high for later adapter and ingest hotspots; those require fixtures,
  fuzzing, and benchmarks before any production refactor.
- No schema or external API change is expected.

Sensitive data handling plan:

- Codacy exports stay in `/tmp`. Durable SOW/spec artifacts record aggregate
  counts, sanitized file paths, and rationale only.

Implementation plan:

1. Update `frontend-architecture.md` with the URL filter contract.
2. Delegate frontend tests that pin all array dimensions, scalar deletion,
   unrelated-param preservation, and SSE subscription omission/mapping rules.
3. Delegate the `filters.ts` refactor to keep behavior unchanged while reducing
   Lizard-reported helper complexity.
4. Verify with focused Vitest, FilterBar tests, direct Lizard, local Codacy
   analysis, full gates, and external reviewers.
5. Record remaining complexity and choose the next production slice only after
   this slice is fully merged.

Validation plan:

- `scripts/test/codacy-config-test.sh`
- `cd frontend && npm test -- --run src/state/filters.test.tsx src/components/FilterBar/FilterBar.test.tsx`
- `~/.codacy/runtimes/lizard-1/venv/bin/lizard frontend/src/state/filters.ts -l javascript`
- Local Codacy Lizard analysis for `frontend/src/state/filters.ts` or full local
  Codacy analysis if file-scoped CLI filtering is unavailable.
- Full `./scripts/gates.sh`.
- External second-opinion review with at least three reviewers.

Test-order note:

- This first slice is a behavior-preserving refactor. The added/strengthened
  frontend tests are characterization tests and may pass before implementation.
  The pre-existing failing signal is the Codacy/Lizard complexity report itself,
  especially the `applyPatch` and `filtersToSubscription` findings above.

Benchmarks:

- No benchmark is required for the first slice because it touches only small
  frontend URL/SSE serialization helpers and no parser, ingest, database, SSE
  hub, REST query, or rendering hot path.

Artifact impact plan:

- AGENTS.md: likely unaffected unless a new permanent workflow rule emerges.
- Runtime project skills: update `project-quality-gates` if complexity policy
  changes.
- Specs: `frontend-architecture.md` updated for the first slice; adapter,
  ingest, and quality-gate specs will be updated only when later slices touch
  those areas.
- End-user/operator docs: likely unaffected unless setup/quality reporting
  changes.
- End-user/operator skills: likely unaffected.
- SOW lifecycle: active in `.agents/sow/current/`.

Open-source reference evidence:

- First slice does not change a protocol parser or external source-format
  adapter, so no cloned open-source parser reference is required. Official
  Lizard, Codacy, and React Router documentation was checked for the tools and
  library behavior used in this slice.

Open decisions:

- None for the operator. Technical sequencing belongs to the assistant.

### Second Slice Gate - aiagent_v2 mapper

Status: ready for implementation after the spec drift cleanup above.

Selected second slice:

- `internal/adapters/aiagent_v2/mapper.go` mapper decomposition.
- Goal: reduce Codacy/Lizard maintainability findings in the v2 mapper while
  keeping the v2 snapshot-to-canonical projection byte-for-byte equivalent for
  existing golden fixtures and behaviorally equivalent for focused unit tests.
- Explicit non-goal: do not change v2 source-format semantics, payload storage
  policy, canonical schema, ingest idempotency, cursor behavior, scanner/tailer
  behavior, or frontend presentation in this slice.

Problem / root-cause model:

- The mapper owns too many responsibilities in one 633-NLOC file: session
  subtree walking, turn/step rollups, op lifecycle events, payload refs,
  reasoning spans, extras construction, status/kind normalization, timestamp
  fallback, and model discovery.
- The highest-risk function is `mapOp`: it interleaves event construction,
  payload handling, reasoning emission, logs, failure surfacing, child-session
  recursion, and rollup return values.
- The complexity is real maintainability risk, not a parser-behavior defect.
  The correct fix is narrow decomposition around existing event contracts, not
  semantic rewrites.

Evidence reviewed:

- Codacy export `/tmp/ai-viewer-sow0046-codacy-final-r6.json` reported 12
  mapper findings:
  - file NLOC 633 > 500 threshold.
  - `mapSession`: 9 parameters > 8.
  - `mapSteps`: 54 NLOC > 50.
  - `mapOp`: CCN 14 > 8, NLOC 72 > 50, 9 parameters > 8.
  - `opStartedExtras`: CCN 14 > 8.
  - `buildSessionStarted`: 9 parameters > 8.
  - `extractPayloadRef`: CCN 11 > 8.
  - `emitPayloadRefs`: 9 parameters > 8.
  - `appendPayloadRef`: 10 parameters > 8.
  - `emitReasoningOp`: 9 parameters > 8.
- Direct Lizard baseline on 2026-06-05 confirmed the same hotspot shape even
  though default Lizard thresholds do not warn:
  - `mapSession` 30 NLOC / CCN 8 / 9 params.
  - `mapSteps` 54 NLOC / CCN 6 / 6 params.
  - `mapOp` 72 NLOC / CCN 14 / 9 params.
  - `opStartedExtras` 28 NLOC / CCN 14.
  - `buildSessionStarted` 39 NLOC / CCN 8 / 9 params.
  - `extractPayloadRef` 25 NLOC / CCN 11.
  - `appendPayloadRef` 24 NLOC / CCN 3 / 10 params.
- Existing focused mapper tests already cover session start/finalize, embedded
  child sessions, turn 0, failed op error logs, step mapping, payload refs,
  path traversal rejection, inline payload skip behavior, reasoning spans,
  model pre-pass, token/cache accounting, and status/kind helpers.
- Golden scenarios under `testdata/aiagent_v2/` cover happy v1/v2, embedded
  sub-agents, multi-descendant same-file mapping, final report, tool character
  accounting, system op kind, init turn zero, temporary-file ignoring, and
  zero-byte files.
- Focused baseline before this slice:
  `go test ./internal/adapters/aiagent_v2 -run 'Test(Map|Golden|Scan_ProducesSessionStartedForFixture|Streamer_AgreesWithNonStreaming)' -count=1`
  passed.

Affected contracts and surfaces:

- v2 adapter canonical event projection only:
  `SessionStartedEvent`, `SessionFinalizedEvent`, `TurnStartedEvent`,
  `TurnFinalizedEvent`, `OpStartedEvent`, `OpFinalizedEvent`,
  `PayloadRefEvent`, `LogEntryEvent`, and `SourceSeq` generation.
- No SQLite migration, REST, SSE, frontend, source discovery, cursor, or tailer
  contract changes are expected.

Spec deltas landed before tests/code:

- `.agents/sow/specs/adapter-aiagent-v2.md` was corrected to match current
  mapper behavior before test/code edits:
  - `SourceSeq` is FNV-64a over `originId + NUL + path`, masked to 63 bits,
    not xxhash.
  - v2 session model discovery populates `SessionStartedEvent.Model` directly;
    no later model `SessionUpdatedEvent` is emitted for this mapper path.
  - LLM op `Name` comes from `attributes.name`; `Model` comes from
    `attributes.model`.
  - tool character accounting maps to `CharsIn`/`CharsOut`; request/response
    size still maps to `BytesIn`/`BytesOut`.
  - `system` maps to canonical `OpSystem`, with original source kind retained
    in extras.
  - totals, final report, and plugin metadata are kept in session extras.
  - `reasoning.final` is carried on the nested reasoning op extras; no extra
    reasoning log row is emitted.
  - legacy inline payloads are deliberately skipped; producer-shaped
    `payload.ref` and `payload.sdk.ref` descriptors produce `PayloadRefEvent`
    rows, with constrained compatibility for legacy flat ref descriptors.
  - reasoning spans get their own deterministic synthetic op sequence values so
    they cannot overwrite the parent LLM op in storage.
  - a request/response side may carry both regular and SDK payload refs; both
    are emitted independently, and a bad ref cannot suppress a valid sibling ref.
  - child-session model discovery uses the same depth cap as event emission, so
    over-cap child subtrees cannot influence parent session model metadata.

Existing patterns to reuse:

- Pure mapper helpers with no I/O and deterministic event ordering.
- Existing `mapContext` for per-file invariants, extended with narrow per-node
  structs only where they reduce parameter lists.
- Existing golden tests as the broad behavior-preservation oracle.
- Existing `BenchmarkScan_SyntheticCorpus` and `BenchmarkTail_SyntheticAppend`
  as the performance oracle for parser+mapper changes.

Risk and blast radius:

- Medium: the mapper feeds historical v2 sessions and sub-agent lineage. A
  subtle ordering, SourceSeq, parent linkage, or rollup regression could change
  stored rows even when tests still compile.
- Low schema/API risk: the intended change is internal decomposition only.
- Performance risk exists because mapper helpers run for every v2 op across
  hundreds of thousands of snapshots; full benchmark gate is mandatory.

Sensitive data handling plan:

- No real snapshot contents are written to durable artifacts. Tests must use
  existing sanitized fixtures or synthetic snapshots. Any temporary Codacy or
  benchmark output stays in `/tmp`.

Implementation plan:

1. Add focused mapper characterization tests, if gaps remain after reading the
   current suite, before production changes. At minimum, pin exact event-order
   and rollup behavior around a mixed turn/step/session/reasoning/payload case
   if existing tests do not already prove it in one assertion.
2. Delegate mapper decomposition:
   - introduce narrow value structs for session emit parameters, op emit
     context, rollup totals, payload ref sides, and reasoning emit context;
   - split high-branch helpers (`mapOp`, `opStartedExtras`,
     `extractPayloadRef`) into small, named helpers;
   - keep event values, event order, SourceSeq path strings, and exported
     behavior unchanged.
3. Run focused mapper/golden tests and direct Lizard/Codacy file analysis.
4. Run full `./scripts/gates.sh`, including the benchmark regression gate.
5. Run external second-opinion review and iterate until convergence.

Validation plan:

- `go test ./internal/adapters/aiagent_v2 -run 'Test(Map|Golden|Scan_ProducesSessionStartedForFixture|Streamer_AgreesWithNonStreaming)' -count=1`
- `go test -race ./internal/adapters/aiagent_v2`
- `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/adapters/aiagent_v2/mapper.go -l go`
- Local Codacy file analysis for `internal/adapters/aiagent_v2/mapper.go`.
- `scripts/scan-secrets.sh && scripts/spec-drift.sh`
- Full `./scripts/gates.sh`.
- PR checks must all pass before merge; `codacy-coverage` may skip on PRs
  before secrets by design, but every other row must be green.

Test-order note:

- This second slice is a behavior-preserving refactor. Existing tests and any
  added characterization tests may pass before implementation. The failing
  signal is the stricter Codacy/Lizard maintainability report listed above.

Benchmarks:

- Required. The v2 mapper is on both adapter `Scan` and `Tail` hot paths.
  `./scripts/gates.sh` must include `scripts/check-bench.sh`; any significant
  >20% sec/op regression blocks the slice.

Artifact impact plan:

- Specs: `adapter-aiagent-v2.md` updated before tests/code.
- Runtime project skills: no expected change unless a new mapper-decomposition
  convention emerges.
- End-user docs: no expected change.
- SOW lifecycle: remains active until enough production complexity backlog is
  reduced or explicitly split into narrower follow-up SOWs.

Open-source reference evidence:

- This is an internal behavior-preserving mapper decomposition against the
  already-specified v2 format. No new source-format claim is introduced, so no
  additional upstream clone/mirror research is required for this slice.

Open decisions:

- None for the operator. Technical sequencing belongs to the assistant.

## Plan

1. Triage production hotspots and choose the first low-risk/high-value slice.
2. Strengthen tests and benchmarks for that slice.
3. Refactor only where behavior and performance can be proved stable.
4. Record justified remaining complexity and create narrower follow-up SOWs if
   needed.

## Execution Log

### 2026-06-05

- Created from SOW-0046 local Codacy complexity grouping.
- Refreshed from the final SOW-0046 local Codacy export after post-Cloud-fix
  cleanup: 608 total complexity findings, including 195 production findings in
  84 files.
- Activated in `.agents/sow/current/` for the first implementation slice.
- Selected `frontend/src/state/filters.ts` as the first production slice after
  frontend and backend read-only hotspot triage.
- Updated `.agents/sow/specs/frontend-architecture.md` with the URL filter
  contract before tests and implementation.
- Added focused characterization coverage for all array filter dimensions,
  scalar deletion, unrelated query preservation, and SSE subscription mapping.
- Refactored `filters.ts` into smaller URL patching and subscription mapping
  helpers while keeping exported names and public behavior stable.
- Addressed first-review findings by pinning `readFilters` coverage for
  `tools` and `sources`, `clearFilters` preservation of unrelated params,
  empty and whitespace-padded `q` handling, and narrower helper types.

## Validation

First slice local validation:

- `cd frontend && npm test -- --run src/state/filters.test.tsx src/components/FilterBar/FilterBar.test.tsx`
  passed after review fixes: 2 files, 32 tests.
- `cd frontend && npm run lint` passed.
- `cd frontend && npm run typecheck` passed.
- `~/.codacy/runtimes/lizard-1/venv/bin/lizard frontend/src/state/filters.ts -l javascript`
  reported zero threshold warnings. Key metrics after refactor:
  `applyPatch` 10 NLOC / CCN 1 / 2 params; `filtersToSubscription` 7 NLOC /
  CCN 1 / 1 param; file NLOC 271, below the 500 file-NLOC threshold observed
  in the SOW-0046 Codacy export.
- `npx -y @codacy/analysis-cli analyze --files frontend/src/state/filters.ts --output-format json --output /tmp/ai-viewer-sow0047-filters-codacy-r2.json`
  reported 0 issues; Lizard reported 0 issues. The CLI emitted one
  warning-level tool-config message that Codacy-bundled ESLint skipped
  type-aware plugin rules because parser services were unavailable; project
  native ESLint and TypeScript checks passed.
- `./scripts/gates.sh` passed after Round 2 review recording in 530s:
  lint/static/security/vulnerability checks, secrets, attribution, spec drift,
  Codacy config/coverage self-tests, systemd, build + bundle-size, benchmark
  regression gate, Go race+coverage, frontend Vitest coverage, Go coverage
  threshold gate, adapter fuzz seed corpus, and Playwright/axe. The run reported
  Go total coverage 85.0%, gated `internal/*` aggregate coverage 90.5%, and
  frontend Vitest coverage with 631 passing tests.
- PR-level Codacy Cloud analysis for PR #50 reported one new issue after the
  first push: `@typescript-eslint/consistent-type-definitions` on the local
  `BuiltSubscriptionTimeRange` object shape in `frontend/src/state/filters.ts`.
  The follow-up fix changed that local object shape from `type` to `interface`
  with no behavior or exported API change.
- Post-follow-up targeted validation passed: `cd frontend && npm run lint`,
  `cd frontend && npm run typecheck`, focused Vitest for
  `src/state/filters.test.tsx` and `src/components/FilterBar/FilterBar.test.tsx`
  with 32 passing tests, direct Lizard on `frontend/src/state/filters.ts` with
  zero threshold warnings, local Codacy file analysis with 0 issues, and
  `scripts/scan-secrets.sh && scripts/spec-drift.sh`.
- `./scripts/gates.sh` passed after the PR check follow-up in 515s:
  lint/static/security/vulnerability checks, secrets, attribution, spec drift,
  Codacy config/coverage self-tests, systemd, build + bundle-size, benchmark
  regression gate, Go race+coverage, frontend Vitest coverage, Go coverage
  threshold gate, adapter fuzz seed corpus, and Playwright/axe. The run reported
  Go total coverage 85.0%, gated `internal/*` aggregate coverage 90.5%, and
  frontend Vitest coverage with 631 passing tests.

Second slice focused validation:

- Added `internal/adapters/aiagent_v2/mapper_characterization_test.go`, a
  synthetic mixed-snapshot characterization test that pins mapper traversal
  order, SourceSeq path hashing, replay determinism, embedded child-session
  in-place emission, reasoning nesting, payload refs, step projection, and
  turn/step rollups before production mapper changes.
- Refactored the v2 mapper into smaller production files:
  `mapper.go`, `mapper_payload.go`, `mapper_model.go`, `mapper_session.go`,
  `mapper_walk.go`, and `mapper_ops.go`. The split separates shared mapper
  invariants, payload/ref handling, model discovery, session emission,
  turn/step walking and rollups, op lifecycle construction,
  reasoning/log/failure helpers, and child-session recursion.
- Added explicit characterization assertions that the root
  `SessionStartedEvent.Extras.version` carries the snapshot version while
  embedded child sessions carry `version = 0`.
- Added producer-shaped payload-ref coverage in
  `internal/adapters/aiagent_v2/mapper_payload_test.go` for `payload.ref`,
  `payload.sdk.ref`, `compressedBytes -> StoredBytes`, pathless uncaptured refs,
  and traversal rejection that skips only the malformed ref.
- Focused mapper/golden test passed:
  `go test ./internal/adapters/aiagent_v2 -run 'Test(Map|Golden|Scan_ProducesSessionStartedForFixture|Streamer_AgreesWithNonStreaming)' -count=1`.
- Focused payload-ref regression test passed:
  `go test ./internal/adapters/aiagent_v2 -run 'TestMap_(PayloadRefEmittedForRequestAndResponse|PayloadRefForToolOpUsesToolKinds|PayloadRefTraversalGuardRejects|PayloadInlineSkipsRefEmission|MixedSnapshotPinsTraversalSourceSeqAndRollups)|TestExtractPayloadRef_VariousShapes|TestResolvePayloadPath_RootHandling' -count=1`.
- Uncached adapter race test passed:
  `go test -race -count=1 ./internal/adapters/aiagent_v2` in 80.533s after the
  producer-shaped payload-ref fix.
- Direct Lizard on the six production mapper files and three focused test files
  reported zero warnings. Physical line counts: `mapper.go` 270,
  `mapper_payload.go` 328, `mapper_model.go` 42, `mapper_session.go` 157,
  `mapper_walk.go` 153, `mapper_ops.go` 283, `mapper_characterization_test.go`
  402, `mapper_fixes_test.go` 246, `mapper_payload_test.go` 330. Lizard NLOC by
  file: `mapper.go` 182, `mapper_payload.go` 262, `mapper_model.go` 33,
  `mapper_session.go` 130, `mapper_walk.go` 133, `mapper_ops.go` 245,
  `mapper_characterization_test.go` 371, `mapper_fixes_test.go` 208,
  `mapper_payload_test.go` 281. Highest function metrics stayed within the
  selected thresholds: max NLOC 24, max CCN 8, max parameter count 7.
- Local Codacy/Lizard analysis for the production mapper split reported 0
  issues, down from the 12 baseline issues for `mapper.go`.
- Codacy Analysis CLI staged Lizard run reported 0 issues and 0 errors across
  the final staged aiagent_v2 mapper files and tests after the Round 7
  payload-ref fix:
  `/tmp/ai-viewer-sow0047-aiagent-v2-mapper-codacy-final-r3.json`.
- Quick mapper benchmark smoke passed with no obvious regression:
  `BenchmarkScan_SyntheticCorpus` ranged 129.849-139.122 ms/op and
  `BenchmarkTail_SyntheticAppend` ranged 91.046-97.193 us/op across 3 runs.
- `git diff --check`, `scripts/scan-secrets.sh`, and `scripts/spec-drift.sh`
  passed for the current slice state. `scripts/scan-ai-attribution.sh` also
  passed; the secret scan covered 836 tracked files.
- Full `./scripts/gates.sh` passed in 495s for the pre-Round 7 mapper
  decomposition state:
  lint/static/security/vulnerability checks, secrets, attribution, spec drift,
  Codacy config/coverage self-tests, systemd, build + bundle-size, benchmark
  regression gate, Go race+coverage, frontend Vitest coverage, Go coverage
  threshold gate, adapter fuzz seed corpus, and Playwright/axe. The run
  reported Go total coverage 85.2%, gated `internal/*` aggregate coverage
  90.6%, `internal/adapters/aiagent_v2` coverage 92.7%, and frontend Vitest
  coverage with 631 passing tests. Final full-gate evidence is rerun after the
  Round 7 payload-ref fix and review convergence.
- Round 8 blocker fixes added:
  - deterministic synthetic reasoning op seqs starting after the source op index
    range in each turn/step;
  - multi-ref payload-side emission for regular plus SDK refs, with SDK event
    paths using `::payload:<side>:sdk`;
  - depth-capped model discovery using the same child-session cap as mapper
    event emission;
  - an ingest regression proving a reasoning child op does not overwrite the
    parent LLM op row or its token/byte/cost fields.
- Focused Round 8 adapter regressions passed:
  `go test ./internal/adapters/aiagent_v2 -run 'TestMap_(ReasoningOpEmittedNestedUnderLLM|PayloadSideEmitsRegularAndSDKRefs|PayloadSideBadRegularRefDoesNotSuppressSDKRef|FirstLLMModelSkipsOverDepthCapChild|MixedSnapshotPinsTraversalSourceSeqAndRollups|PayloadRefEmittedForRequestAndResponse|PayloadRefForToolOpUsesToolKinds|PayloadRefTraversalGuardRejects|PayloadInlineSkipsRefEmission)|TestExtractPayloadRef_VariousShapes|TestExtractPayloadRefs_MultipleAndMixedShapes|TestResolvePayloadPath_RootHandling' -count=1`.
- Focused ingest regression passed:
  `go test ./internal/ingest -run 'TestWriter_ReasoningOpDoesNotOverwriteParentLLM' -count=1`.
- Package-level tests passed:
  `go test ./internal/adapters/aiagent_v2 -count=1` in 5.099s and
  `go test ./internal/ingest -count=1` in 8.257s.
- Package-level race tests passed:
  `go test -race -count=1 ./internal/adapters/aiagent_v2` in 81.471s and
  `go test -race -count=1 ./internal/ingest` in 328.043s.
- `gofmt -l`, `goimports -l`, and `git diff --check` were clean for the touched
  mapper and ingest files.
- Direct Lizard on the six production mapper files, four mapper test files, and
  the ingest writer coverage test reported zero warnings. Physical line counts:
  `mapper.go` 271, `mapper_payload.go` 358, `mapper_model.go` 50,
  `mapper_session.go` 159, `mapper_walk.go` 163, `mapper_ops.go` 283,
  `mapper_characterization_test.go` 402, `mapper_fixes_test.go` 298,
  `mapper_payload_test.go` 330, `mapper_payload_multi_test.go` 106,
  `writer_coverage_test.go` 334. Lizard NLOC by file: `mapper.go` 183,
  `mapper_payload.go` 286, `mapper_model.go` 40, `mapper_session.go` 132,
  `mapper_walk.go` 142, `mapper_ops.go` 245, `mapper_characterization_test.go`
  371, `mapper_fixes_test.go` 256, `mapper_payload_test.go` 281,
  `mapper_payload_multi_test.go` 95, `writer_coverage_test.go` 303. Highest
  function metrics stayed within the selected thresholds: max NLOC 35,
  max CCN 8, max parameter count 7.
- Codacy Analysis CLI staged Lizard run reported 0 issues and 0 errors across
  11 staged Go files after the Round 8 fixes:
  `/tmp/ai-viewer-sow0047-aiagent-v2-mapper-codacy-final-r4.json`.
- `git diff --cached --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh` passed for the
  staged Round 8 state. The secret scan covered 837 tracked files.
- Final `./scripts/gates.sh` passed after Round 9 review convergence in 497s:
  lint/static/security/vulnerability checks, secrets, attribution, spec drift,
  Codacy config/coverage self-tests, systemd, build + bundle-size, benchmark
  regression gate, Go race+coverage, frontend Vitest coverage, Go coverage
  threshold gate, adapter fuzz seed corpus, and Playwright/axe. The run
  reported Go total coverage 85.2%, gated `internal/*` aggregate coverage
  90.6%, `internal/adapters/aiagent_v2` coverage 92.7%, frontend Vitest
  coverage with 631 passing tests, frontend E2E/axe with 51 passing tests, and
  no benchmark `sec/op` regression over the 20% gate.
- PR review follow-up focused validation passed after Round 10 fixes:
  `go test ./internal/adapters/aiagent_v2 -run 'TestExtractPayloadRef_VariousShapes|TestExtractPayloadRefs_MultipleAndMixedShapes|TestResolvePayloadPath_RootHandling' -count=1`,
  and `go test ./internal/adapters/aiagent_v2 -count=1`. These tests pin the
  exact resolved payload URI, skip bare path-only inline objects, preserve
  evidence-shaped legacy flat refs, and keep regular-plus-SDK payload refs
  independently emitted.
- Fresh final `./scripts/gates.sh` passed after the Round 10 follow-up and
  reviewer rerun in 505s: lint/static/security/vulnerability checks, secrets,
  attribution, spec drift, Codacy config/coverage self-tests, systemd, build +
  bundle-size, benchmark regression gate, Go race+coverage, frontend Vitest
  coverage, Go coverage threshold gate, adapter fuzz seed corpus, and
  Playwright/axe. The run reported Go total coverage 85.2%, gated `internal/*`
  aggregate coverage 90.6%, `internal/adapters/aiagent_v2` coverage 92.6%,
  frontend Vitest coverage with 631 passing tests, frontend E2E/axe with 51
  passing tests, and no benchmark `sec/op` regression over the 20% gate.

## Reviews

### Round 1 - 2026-06-05

Scope: SOW file and current uncommitted diff for the first SOW-0047
implementation slice.

Reviewers:

- `codex`: no production correctness or security issue. Findings: add
  `clearFilters` preservation coverage, broaden `readFilters` coverage for
  `tools` and `sources`, and remove live branch name from durable SOW text.
- `glm`: no correctness bug, security issue, or unwanted side effect. Findings:
  narrow the subscription time-range helper type, optionally document internal
  helper style, and add empty-string `q` coverage.
- `qwen`: no correctness bug, security issue, or unwanted side effect. Findings:
  narrow `setOrDeleteNumber` key type and add whitespace-padded non-empty `q`
  coverage.
- `gemini`: invocation exited with no review output and was not counted.

Resolution:

- Added focused tests for `tools` and `sources` decoding, `clearFilters`
  unrelated-param preservation and all filter-key removal, empty-string `q`
  deletion, and whitespace-padded non-empty `q` preservation.
- Narrowed `setOrDeleteNumber` to `from | to`.
- Replaced the subscription time-range helper alias with a local builder type
  that emits only numeric optional bounds.
- Removed the live branch name from the durable SOW text.

### Round 2 - 2026-06-05

Scope: same broad SOW file and current uncommitted diff review, with the
Round 1 fixes included in the reviewed state.

Reviewers:

- `codex`: no blocking correctness, security, or unwanted-side-effect finding.
  Verified the `q` omission against the subscription API type and REST spec,
  and verified the added tests for all Round 1 fixes.
- `glm`: no blocking correctness, security, or unwanted-side-effect finding.
  Noted the repeated exhaustive `ArrayFilterKey` switches as an explicit
  maintainability tradeoff, noted very-low-risk test asymmetries for
  multi-value `sources` and independent `to` deletion, and noted a cosmetic
  `q` whitespace wording precision point.
- `qwen`: no actionable findings. Verified exported contracts, helper data
  flow, security posture, consumer impact, spec accuracy, and SOW accuracy.

Resolution:

- Accepted the repeated exhaustive-switch structure as intentional for this
  slice: each switch has a distinct read/write responsibility, avoids unsafe
  dynamic writes, and remains compile-time guarded by the `never` exhaustiveness
  checks.
- Accepted the test asymmetries as non-actionable: `parseList` behavior is
  shared across all array keys, multi-value parsing is already covered through
  other dimensions, `sources` path selection is directly covered, and `to`
  set/delete behavior is covered by the all-dimension and all-key-clear tests.
- No code or behavior change was required after Round 2.

### PR Check Follow-up - 2026-06-05

Finding:

- Codacy Cloud PR analysis reported one new style issue in
  `frontend/src/state/filters.ts`: use `interface` instead of a `type` alias for
  the local `BuiltSubscriptionTimeRange` object shape.

Resolution:

- Delegated a one-file style-only production-source change that converted the
  local builder shape to `interface BuiltSubscriptionTimeRange`.
- No runtime behavior, exported API, tests, specs, or query/subscription mapping
  changed.

### Round 3 - 2026-06-05

Scope: same broad SOW file and current uncommitted diff review, with the
PR-level Codacy Cloud follow-up and project-frontend guidance update included in
the reviewed state.

Reviewers:

- `codex`: no correctness, security, race, performance, or behavior finding.
  Finding: post-follow-up validation had not yet been recorded in the SOW.
- `glm`: no actionable findings. Verified the type-to-interface follow-up,
  consumer impact, Lizard state, and SOW/skill accuracy.
- `kimi`: no actionable findings. Verified the type-to-interface follow-up is
  local-only and behaviorally erased, confirmed remaining type aliases are
  justified, and checked the SOW and skill updates.
- `qwen`: invocation stalled without a final review after the fallback reviewer
  completed and was stopped; not counted.

Resolution:

- Re-ran the full local gate suite after the PR check follow-up and recorded the
  post-follow-up validation above.
- No code or behavior change was required after Round 3.

### Round 4 - 2026-06-05

Scope: SOW file, `adapter-aiagent-v2.md`, and the staged aiagent_v2 mapper
decomposition for the second SOW-0047 implementation slice.

Reviewers:

- `codex`: no mapper correctness, security, ordering, rollup, payload,
  recursion, or reasoning-span issue. Findings: fix the `SourceSeq` path grammar
  in `adapter-aiagent-v2.md` and replace the stale `extras_json.step_kind`
  wording with the actual `step.<index>.kind` session-extra key.
- `glm`: no blocking finding. Verified behavior preservation, payload traversal
  guard, rollups, race/focused tests, Lizard metrics, and benchmark shape. Noted
  only low/cosmetic observations.
- `mimo`: no blocking finding. Verified event ordering, SourceSeq/path hashing,
  rollups, child-session linkage, payload security, reasoning nesting, test
  coverage, and spec corrections.
- `kimi`: no blocking finding. Ran focused tests, race test, Lizard, and
  benchmark smoke; verified the mapper split is behavior-preserving.
- `gemini`: invocation exited with no review output and was not counted.
- `qwen`: invocation exited without a final review report and was not counted.

Resolution:

- Corrected `adapter-aiagent-v2.md` to document the actual emitted event-path
  grammar used for `SourceSeq`.
- Corrected step-kind documentation to the actual `step.<index>.kind` session
  extras key.
- Corrected the nearby source comment for `baseEvent` so it names the real
  `originId + NUL + path` hash input.
- No runtime code or behavior change was required after Round 4.

### Round 5 - 2026-06-05

Scope: same broad staged SOW file, `adapter-aiagent-v2.md`, and aiagent_v2
mapper decomposition review, with the Round 4 spec/comment fixes included in
the reviewed state.

Reviewers:

- `codex`: no mapper runtime bug. Findings: two remaining spec-only drifts in
  `adapter-aiagent-v2.md`: the mapping intro and cursor replay prose still
  described `SourceSeq` as per-node/`opTreePath`, and the interrupted-op edge
  case still said unfinished ops emit only `OpStartedEvent`.
- `glm`: no actionable finding. Verified behavior preservation, focused tests,
  and SOW/spec alignment; noted only low-risk observations.
- `mimo`: no blocking finding. Verified the mapper split and characterization
  coverage; noted only low-risk theoretical/cosmetic observations.
- `deepseek`: no runtime correctness finding. Finding: document the existing
  child-session `extras.version = 0` behavior so the spec explicitly matches the
  mapper and tests.

Resolution:

- Corrected the mapping intro to describe deterministic canonical events per
  emitted event, not one event per opTree node.
- Corrected cursor replay prose to name `(originId, eventPath)` as the
  deterministic `SourceSeq` input.
- Corrected the duplicate-child theoretical case to use
  `eventPath-in-that-file` terminology consistently.
- Corrected the interrupted-op edge case to state that unfinished ops emit an
  `OpFinalizedEvent` with status `running` and start-time fallback `EndTs`.
- Documented that embedded child sessions carry `extras.version = 0` because
  the snapshot version belongs to the root file envelope.
- No runtime code or behavior change was required after Round 5.

### Round 6 - 2026-06-05

Scope: same broad staged SOW file, `adapter-aiagent-v2.md`, and aiagent_v2
mapper decomposition review, with the Round 5 fixes included in the reviewed
state.

Reviewers:

- `codex`: no runtime correctness, race, security, or payload traversal
  blocker. Findings: fix remaining spec-only drift for child-session
  `SourceSeq` path roots, session error mapping, and failed-op error fallback;
  address the residual `mapper.go` physical line-count smell.
- `glm`: no blocking correctness, security, race, performance, or behavior
  finding. Findings: add explicit coverage for embedded child
  `SessionStartedEvent.Extras.version = 0`; noted the remaining file-size
  smell as low severity.
- `mimo`: no blocking finding. Verified the mapper split, event ordering,
  SourceSeq, rollups, child version behavior, payload traversal guard, SOW
  ledger, and focused tests; noted explicit child-version coverage as a
  low-severity test-hardening opportunity.

Resolution:

- Corrected `adapter-aiagent-v2.md` so child-session `SourceSeq` prose names
  child event paths rooted at `childSession.traceId`.
- Corrected `adapter-aiagent-v2.md` so session errors map to
  `SessionFinalizedEvent.ErrorMessage`, matching the mapper.
- Corrected `adapter-aiagent-v2.md` so failed op errors map directly from
  `attributes.error` without a synthetic `"unknown"` fallback.
- Split payload/ref helpers into `mapper_payload.go` and model discovery into
  `mapper_model.go`, reducing `mapper.go` from 507 to 270 physical lines.
- Added focused characterization assertions for root snapshot version and
  embedded child `version = 0`.
- Refactored the characterization test helpers so the staged Codacy/Lizard run
  reports 0 issues for both production and test files.

### Round 7 - 2026-06-05

Scope: same broad staged SOW file, `adapter-aiagent-v2.md`, and aiagent_v2
mapper decomposition review, with the Round 6 fixes and Codacy/Lizard test
cleanup included in the reviewed state.

Reviewers:

- `codex`: found a blocking producer-shape coverage/correctness gap: real v2
  payload refs are nested as `payload.ref` and SDK refs as `payload.sdk.ref`,
  while the staged mapper only accepted the synthetic flat ref shape used by
  the tests. Also noted spec drift around ref-only `childSessionSummary`
  handling.
- `glm`: no blocking correctness, security, race, performance, or spec-drift
  finding in the mapper decomposition.
- `mimo`: no blocking finding. Noted the pre-existing duplicate `callPath`
  storage in `SessionStartedEvent.CallPath` and `Extras.callPath`; this slice
  preserves that behavior and it is not part of the complexity backlog.

Resolution:

- Corrected `adapter-aiagent-v2.md` before code changes so the payload-ref
  contract names producer-shaped `payload.ref` and `payload.sdk.ref`, maps
  `StoredBytes` from `compressedBytes`, and keeps inline payloads skipped.
- Corrected `adapter-aiagent-v2.md` so ref-only `childSessionSummary` is stored
  in started-event extras while `OpFinalizedEvent` remains derived from the
  session op's own timing/status.
- Delegated runtime/test fixes for producer-shaped `payload.ref` and
  `payload.sdk.ref`, including `compressedBytes -> StoredBytes`, pathless
  uncaptured ref metadata emission, and traversal rejection that skips only the
  malformed ref.
- Split the expanded payload tests into `mapper_payload_test.go` so
  `mapper_fixes_test.go` and the new payload test file both remain below the
  project file-size guideline.
- Review remains open until the same broad reviewer scope is rerun on the
  integrated Round 7 fixes.

### Round 8 - 2026-06-05

Scope: same broad staged SOW file, `adapter-aiagent-v2.md`, and aiagent_v2
mapper decomposition review, with the Round 7 producer-shaped payload-ref fixes
included in the reviewed state.

Reviewers:

- `codex`: found three actionable blockers: nested reasoning ops reused the
  parent LLM op `Seq`; producer deep-merge can leave both `payload.ref` and
  `payload.sdk.ref` on the same side while the mapper emitted only one; model
  discovery recursively followed child sessions without the mapper's depth cap.
- `glm`: no blocking finding. Verified payload-ref extraction, path traversal
  behavior, and v2/v3 captured-ref handling; noted no required changes.
- `mimo`: no blocking finding. Noted the same low-risk mixed-shape
  `legacyPayloadRef` precedence class and an SDK string-ref test gap; both are
  covered by the broader multi-ref payload fix.

Resolution:

- Corrected `adapter-aiagent-v2.md` before code changes so reasoning ops require
  unique synthetic seqs, payload sides emit regular and SDK refs independently,
  and model discovery shares the child-session depth cap with event emission.
- Delegated runtime/test fixes for the three blockers plus the SDK string-ref
  and mixed-shape payload tests.
- Review remains open until the same broad reviewer scope is rerun on the
  integrated Round 8 fixes.

### Round 9 - 2026-06-05

Scope: same broad staged SOW file, `adapter-aiagent-v2.md`, aiagent_v2 mapper
decomposition, and the added ingest regression, with the Round 8 fixes and the
post-review spec-reference correction included in the reviewed state.

Reviewers:

- `codex`: no actionable finding. Verified synthetic reasoning seq allocation,
  independent regular plus SDK payload-ref emission, depth-capped model
  discovery, and corrected spec contracts. Read-only `git diff --cached --check`
  was clean.
- `glm`: no blocking correctness, security, race, idempotency, spec drift, or
  sensitive-data finding. Noted a non-blocking test-hardening observation that
  the mixed characterization fixture uses the legacy string-ref payload shape
  while producer-shaped refs are covered by focused payload tests.
- `mimo`: no blocking correctness, security, or behavioral regression. Verified
  the Round 8 fixes, focused tests, golden tests, path traversal behavior,
  Codacy/Lizard complexity reduction, and unaffected code paths. Noted only
  low/cosmetic observations: pre-existing `callPath` duplication, intentional
  `original_kind` source-fidelity extras, and a fragile-but-readable
  characterization lock format.

Resolution:

- Corrected `adapter-aiagent-v2.md` stale post-split references from
  `mapper.go` line numbers to `mapper_ops.go`, and corrected lineage wording
  from nonexistent `opTree.children[]` to embedded `operationNode.childSession`.
- Accepted the characterization payload-shape note as non-actionable for this
  slice: focused tests now cover producer-shaped `payload.ref`,
  `payload.sdk.ref`, multi-ref siblings, string SDK refs, `compressedBytes`, and
  bad-ref isolation; the characterization fixture still provides useful legacy
  string-ref compatibility coverage.
- Accepted pre-existing duplicate `callPath` storage and source-fidelity
  `original_kind` extras as preserved behavior, not regressions.
- External review converged with no actionable findings remaining.

### Round 10 - 2026-06-05

Scope: PR-level review of the pushed `ca34e96` state for the same broad SOW
slice before merge.

Findings:

- `mapper_payload_test.go` had a weak URI assertion in
  `assertPayloadPathResolvesUnderRoot`: the prefix check could allow an
  unexpected suffix such as `file:///tmp/payloads/x/y.bin/extra`.
- `adapter-aiagent-v2.md` documented root `SessionFinalizedEvent` status as a
  binary completed/failed mapping, while the mapper and existing tests map
  `endedAt` with absent `success` to `interrupted`.
- `adapter-aiagent-v2.md` described only producer-shaped `payload.ref` and
  `payload.sdk.ref` descriptors, while the mapper intentionally still accepts
  constrained legacy flat ref descriptors for older helper-shaped snapshots and
  tests.

Resolution:

- Hardened `TestResolvePayloadPath_RootHandling` to require the exact expected
  `file:///tmp/payloads/x/y.bin` URI.
- Corrected the session-finalized spec row to state completed, failed, and
  interrupted terminal mappings explicitly.
- Corrected the payload-gap prose to distinguish skipped inline payloads,
  producer-shaped ref descriptors, and retained constrained legacy flat
  descriptor compatibility.
- Follow-up review found two additional issues before merge: bare top-level
  `path` compatibility could misclassify inline payloads as refs, and the spec
  gap section still carried stale "decision needed"/"proposed" language.
  The code now skips bare path-only inline payload objects, keeps explicit
  legacy flat ref descriptors, and the spec now records settled behavior and
  exact extras keys. The same broad review scope was rerun in Round 11 before
  merge.

### Round 11 - 2026-06-05

Scope: same broad SOW file, `adapter-aiagent-v2.md`, aiagent_v2 mapper
decomposition, and staged PR follow-up diff, with the Round 10 fixes and the
bare path-only discriminator fix included in the reviewed state.

Reviewers:

- `codex`: no blocking correctness, race, security, or spec-drift findings.
  Noted a non-blocking evidence gap that the SOW should record the broader
  payload-shape tests, because the staged fix changes legacy payload-ref
  classification as well as URI assertion strength.
- `glm`: no blocking correctness, security, race, idempotency, spec drift, or
  sensitive-data finding. Noted only low-risk observations: a tiny repeated
  scan of `env.Ref` in the discriminator and a pre-existing emission-table
  summary that does not list every session extra key.
- `mimo`: no blocking correctness, security, separation-of-concerns, coverage,
  performance, race, or unwanted-side-effect finding. Noted the same
  non-blocking repeated `env.Ref` probe and that the SOW is long because it
  preserves the review audit trail.

Resolution:

- Recorded the broader focused payload tests and fresh full-gate result in the
  validation section above.
- Accepted the repeated `env.Ref` probe as non-blocking: payload-ref raw
  messages are tiny, the mapper remains pure, and the extra scan is not visible
  in the benchmark gate.
- Accepted the emission-table extras summary and long SOW audit trail as
  non-blocking documentation style; neither changes runtime behavior or merge
  readiness.
- External review converged with no actionable findings remaining.

## Outcome

Pending.

## Lessons Extracted

- Frontend TypeScript object shapes should use `interface` unless a `type` alias
  is needed for a union, intersection, mapped/conditional type, or utility-type
  expression. Codacy Cloud enforces this convention even when project-native
  ESLint does not surface the same rule locally.

## Followup

None yet.

## Regression Log

None yet.
