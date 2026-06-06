# SOW-0047 - Codacy Complexity Backlog Reduction

## Status

Status: open

Sub-state: active 2026-06-05. First production slice
`frontend/src/state/filters.ts` is merged. Second production slice
`internal/adapters/aiagent_v2/mapper.go` is merged. Third production slice
`internal/ingest/writer.go` `applyOpFinalized` is merged. Fourth production
slice `internal/ingest/catalog.go` `onOpStarted` and `onOpFinalized` is merged.
Fifth slice implemented: baseline Claude-code scan/tail benchmarks before
scanner/tailer decomposition. Next slice resumes production complexity
reduction. Sixth slice selected: Claude-code scanner decomposition with Tail
behavior preserved by focused regression tests and the new benchmark guard.
Sixth slice is merged. Seventh slice selected: Claude-code tailer/parser
decomposition, using the existing Scan/Tail benchmark guard and Tail restart
regression suite.

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

### Third Slice Gate - ingest writer applyOpFinalized

Status: ready for implementation after this SOW update.

Selected third slice:

- `internal/ingest/writer.go` `applyOpFinalized` decomposition.
- Goal: reduce Codacy/Lizard maintainability findings in the ingest writer's
  op-finalization path while preserving all persisted-row, catalog, rollup,
  FTS, pricing, duration, and dirty-set behavior exactly.
- Explicit non-goal: do not change SQLite schema or migrations, cursor/high-water
  behavior, rollup algorithms, `applyOpStarted`, extras grafting,
  payload/log idempotency, adapter scanner/tailer behavior, REST/SSE contracts,
  or frontend presentation in this slice.

Problem / root-cause model:

- `applyOpFinalized` currently performs several distinct jobs in one function:
  session/turn/op identity resolution, prior terminal-total capture, persisted
  op lookup, priceable-op gating, computed-cost resolution, end/duration
  validity gating, ops-row update, dirty-set marking, FTS invalidation, and
  catalog delta forwarding.
- The complexity is a maintainability defect in a hot write path, not a behavior
  defect. The correct fix is narrow extraction into named helper functions and
  small local value structs while keeping SQL statements, bind ordering, and
  side-effect order stable.

Evidence reviewed:

- Direct Lizard on current `master` reported the selected hotspot:
  `applyOpFinalized` at `internal/ingest/writer.go:917-1046`, 74 NLOC, CCN 17,
  130 physical lines.
- The same triage also found higher-blast-radius Claude adapter hotspots:
  `internal/adapters/claude_code/scanner.go` `streamLines` / `scanAll` and
  `internal/adapters/claude_code/tailer.go` `tailLoop` / `flushDirty`.
  Those touch scanner/tailer state machines, filesystem streaming, and restart
  behavior, so they are deferred until this narrower ingest-writer slice is
  completed.
- Existing focused tests already cover the sensitive op-finalize contracts:
  persisted-start duration computation, orphan finalize no-op behavior,
  null/skewed end preservation, pricer skip/call/miss behavior, computed-cost
  catalog forwarding, catalog re-finalize idempotency, FTS refresh, and
  start-bucket rollup refresh.

Affected contracts and surfaces:

- SQLite `ops` row updates for `OpFinalizedEvent`.
- Catalog provider/model/tool totals and idempotent `(now - prior)` delta
  behavior.
- Time-bucket dirty marking for the op's persisted start bucket.
- FTS dirty-op invalidation for finalized op error text.
- Pricing-miss observability and computed-cost forwarding into catalog rollups.
- No public API, schema, source-format adapter, frontend, or operator-facing
  behavior change is expected.

Spec deltas before tests/code:

- No spec file delta is required for this slice because it is a pure
  behavior-preserving refactor. `.agents/sow/specs/ingester.md` already
  documents the target behavior:
  - `OpFinalizedEvent` applies a `(now - prior)` delta to catalog totals.
  - `duration_us` is computed from persisted `ops.start_ts`, never from
    `OpFinalizedEvent.Ts`, and orphan/invalid-end finalizes do not fabricate or
    clobber duration.
  - zero-cost priceable LLM ops are priced with the op start timestamp and the
    resolved cost is the value persisted and accumulated.
- If implementation reveals a real spec/code drift, stop the slice, update the
  relevant spec first, then resume tests and production refactor.

Existing patterns to reuse:

- Small unexported helpers on `writer` for one database or side-effect job.
- Local value structs for grouped SQL lookup/update state rather than passing
  long primitive parameter lists.
- Existing `opPriorTotals`, `priceOp`, `isPriceableOp`, dirty-set helpers, and
  catalog writer contracts.
- Focused ingest tests plus the full package race suite and benchmark gate.

Risk and blast radius:

- Medium: this is a hot ingest write path and a regression could silently affect
  persisted costs, durations, catalog totals, FTS, or rollups.
- Low schema/API risk: the intended change is internal decomposition only.
- Performance risk exists because the path runs for every finalized op; the
  benchmark regression gate is mandatory.

Sensitive data handling plan:

- No real session data or snapshot content is written to durable artifacts.
  Tests must use synthetic events and existing sanitized fixtures only. Any
  temporary Codacy, Lizard, or benchmark output stays under `/tmp`.

Implementation plan:

1. Delegate a test audit and add characterization coverage only for real gaps
   around `applyOpFinalized` helper-boundary invariants.
2. Delegate the production refactor of `applyOpFinalized` into narrow helpers:
   persisted op lookup, price resolution, end/duration resolution, ops finalize
   update, dirty-set marking, and catalog forwarding.
3. Keep SQL text/bind semantics and side-effect order stable unless a focused
   test proves the current order is irrelevant.
4. Run focused ingest tests, package race tests, direct Lizard, local Codacy
   analysis, benchmark gate, full gates, and external second-opinion review.
5. Merge only after reviewers converge and PR checks are green.

Validation plan:

- `go test ./internal/ingest -run 'TestApplyOpFinalized|TestWriter_Pricer|TestWriter_Priceable|TestWriter_PricingMiss|TestWriter_ApplyOpFinalized|TestWriter_PricerComputedCostFlowsToCatalog|TestCatalog_Re|TestRefreshRollups_OpFinalized|TestWriter_ReasoningOpDoesNotOverwriteParentLLM' -count=1`
- `go test -race -count=1 ./internal/ingest`
- `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/ingest/writer.go -l go`
- Local Codacy file analysis for `internal/ingest/writer.go`.
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh`.
- Full `./scripts/gates.sh`.
- PR checks must all pass before merge; `codacy-coverage` may skip on PRs by
  design, but every other row must be green.

Test-order note:

- This third slice is a behavior-preserving refactor. Existing tests and any
  added characterization tests may pass before implementation. The failing
  signal is the stricter Codacy/Lizard maintainability report listed above.

Benchmarks:

- Required. `applyOpFinalized` is part of the SQLite batch-insert hot path.
  `./scripts/gates.sh` must include `scripts/check-bench.sh`; any significant
  >20% `sec/op` regression blocks the slice.

Artifact impact plan:

- Specs: no expected spec file update for this behavior-preserving slice unless
  implementation reveals real drift.
- Runtime project skills: no expected change unless a new ingest-writer
  decomposition convention emerges.
- End-user docs: no expected change.
- SOW lifecycle: remains active until the selected slice is merged and enough
  production complexity backlog is reduced or explicitly split into narrower
  follow-up SOWs.

Open-source reference evidence:

- This is an internal behavior-preserving ingest-writer decomposition against
  already-specified canonical/store contracts. No new source-format or protocol
  claim is introduced, so no additional upstream clone/mirror research is
  required for this slice.

Open decisions:

- None for the operator. Technical sequencing belongs to the assistant.

### Fourth Slice Gate - ingest catalog writer

Status: ready for test-first implementation after this SOW update.

Selected fourth slice:

- `internal/ingest/catalog.go` `onOpStarted` and `onOpFinalized`
  decomposition.
- Goal: reduce Codacy/Lizard maintainability findings in the catalog writer's
  op-start and op-finalization paths while preserving catalog row identity,
  call-count idempotency, migration, delta totals, ctx_max layering, and
  provider alias semantics exactly.
- Explicit non-goal: do not change SQLite schema or migrations, rollup refresh,
  writer op persistence, pricing lookup policy, adapter behavior, REST/SSE
  contracts, frontend presentation, or catalog aggregation semantics in this
  slice.

Problem / root-cause model:

- `onOpStarted` currently combines effective identity resolution, identity
  migration, provider/model/tool booking, pricing metadata ctx_max seeding,
  call-count idempotency, and migrated-total rebooking in one function.
- `onOpFinalized` currently combines persisted op lookup, delta computation,
  provider/model/tool totals updates, ctx_max observation raising, duration
  propagation, and failure/token/cost accounting in one function.
- The complexity is real maintainability risk in a hot write path, not a
  behavior defect. The correct fix is narrow decomposition around existing SQL
  side effects and existing catalog invariants, not a semantic rewrite.

Evidence reviewed:

- Direct Lizard on current `master` reported two selected production hotspots:
  - `internal/ingest/catalog.go:87` `onOpStarted`: 60 NLOC, CCN 22,
    98 physical lines.
  - `internal/ingest/catalog.go:228` `onOpFinalized`: 96 NLOC, CCN 17,
    117 physical lines.
- A read-only ingest-catalog assessment recommended this slice now because the
  hotspots are isolated, specified, and strongly covered by existing
  idempotency tests. It also found a focused characterization gap around
  `provider_alias` correction and empty-alias re-emission.
- Refreshed current production Lizard triage showed higher-count alternatives
  in adapter scanner/tailer state machines and frontend trace visualizations.
  The scanner/tailer paths are deferred because they carry restart,
  filesystem-streaming, and cursor/tailing risks that require broader adapter
  fixture work.
- A parallel read-only Claude-code scanner/tailer assessment found that slice
  viable but correctly identified a prerequisite gap: Claude-specific scan/tail
  benchmarks should be added before production scanner/tailer decomposition.
  That makes it a strong next candidate after this narrower catalog slice.
- PR #52 was merged for the third slice. A post-merge full-gate attempt on
  `master` failed only in the local workstation benchmark gate while the system
  was under high CPU load. A controlled read-only benchmark comparison of the
  pre-v2-mapper commit against current `master` under the same load showed no
  significant `aiagent_v2` `Scan_SyntheticCorpus` or `Tail_SyntheticAppend`
  regression, so the failure is treated as a workstation baseline/load issue.
  Full gates remain unclaimed for this next slice until `./scripts/gates.sh`
  passes on the final branch state.

Affected contracts and surfaces:

- `catalog_providers` rows keyed by `(name, alias)`.
- `catalog_models` rows keyed by `(provider, name)`.
- `catalog_tools` rows keyed by `(namespace, name)`.
- `catalog_agents` and `catalog_cwds` are not intended to change, but remain in
  the same file and must keep existing session-start behavior.
- Op re-emission idempotency: same-identity re-emits add no extra `call_count`
  and no duplicate terminal totals.
- Identity migration: changed op identity moves call count and terminal totals
  off the old key and onto the final key exactly once.
- Op finalization deltas: failure, tokens, cache tokens, cost, and duration
  update by `(now - prior)`.
- Context-window max: pricing metadata seeds `ctx_max`; adapter observations
  raise it by max and never lower it.

Spec deltas before tests/code:

- No spec file delta is required for this slice because it is a pure
  behavior-preserving refactor. `.agents/sow/specs/ingester.md` and
  `.agents/sow/specs/data-model.md` already document the target catalog
  behavior:
  - `OpStartedEvent{Kind=llm}` populates `catalog_providers` and
    `catalog_models`; `OpStartedEvent{Kind=tool}` populates `catalog_tools`.
  - `call_count` increments only on genuine new ops or is migrated on identity
    change.
  - `OpFinalizedEvent` applies `(now - prior)` terminal-total deltas.
  - `catalog_models.ctx_max` is seeded from pricing metadata and raised by
    adapter observations.
- If implementation reveals real spec/code drift, stop the slice, update the
  relevant spec first, then resume tests and production refactor.

Existing patterns to reuse:

- Small unexported helpers on `catalogWriter` for one catalog side-effect job.
- Local value structs for grouped persisted-row and delta state instead of long
  primitive parameter lists.
- Existing `effectiveOpIdentity`, `priorOpIdentity`,
  `catalogIdentityChanged`, `removeOpContribution`, `addMigratedTotals`, and
  `normalizeToolNamespace` contracts.
- Focused ingest tests plus the full package race suite and benchmark gate.

Risk and blast radius:

- Medium: catalog rows are denormalized running totals. A regression can
  silently double-count, drain, or strand provider/model/tool aggregates while
  stored op rows still look correct.
- Provider alias handling is the main identified gap. `catalog_providers` is
  keyed by `(name, alias)`, while most existing tests assert only provider name
  or model/tool behavior.
- Performance risk exists because the catalog writer runs inside every ingest
  batch transaction; the benchmark regression gate is mandatory.
- Low schema/API risk: the intended change is internal decomposition only.

Sensitive data handling plan:

- No real session data or snapshot content is written to durable artifacts.
  Tests must use synthetic events and existing sanitized fixtures only. Any
  temporary Codacy, Lizard, or benchmark output stays under `/tmp`.

Implementation plan:

1. Delegate focused characterization tests before production changes for:
   provider-alias correction migration, empty provider-alias re-emit
   preservation, and alias-path totals including cache/cost/duration.
2. Delegate production refactor of `catalog.go`:
   - split `onOpStarted` into identity/call increment resolution, LLM booking,
     tool booking, ctx_max seed resolution, and migrated-total rebooking;
   - split `onOpFinalized` into persisted catalog-op lookup, delta construction,
     provider totals update, model totals update, and tool totals update;
   - keep SQL text/bind semantics and side-effect order stable unless a focused
     test proves the current order is irrelevant.
3. Run focused catalog tests, package tests, package race tests, direct Lizard,
   local Codacy file analysis, benchmark gate, full gates, and external
   second-opinion review.
4. Merge only after reviewers converge and PR checks are green.

Validation plan:

- `go test ./internal/ingest -run 'TestCatalog|TestWriter_PricerComputedCostFlowsToCatalog|TestWriter_OpFailureBumpsFailureCount|TestWriter_Pricer|TestWriter_Priceable|TestWriter_PricingMiss|TestOpDuration|TestPricing' -count=1`
- `go test ./internal/ingest -count=1`
- `go test -race -count=1 ./internal/ingest`
- `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/ingest/catalog.go internal/ingest/*catalog*_test.go -l go -C 8 -L 50 -a 8`
- Local Codacy file analysis for `internal/ingest/catalog.go` and any touched
  catalog test files.
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh`.
- `scripts/check-bench.sh` and full `./scripts/gates.sh`.
- PR checks must all pass before merge; `codacy-coverage` may skip on PRs by
  design, but every other row must be green.

Test-order note:

- This fourth slice is a behavior-preserving refactor. Existing and added
  characterization tests may pass before implementation. The failing signal is
  the stricter Codacy/Lizard maintainability report listed above.

Benchmarks:

- Required. Catalog writes run inside the SQLite batch-insert hot path.
  `./scripts/gates.sh` must include `scripts/check-bench.sh`; any significant
  >20% `sec/op` regression blocks the slice.

Artifact impact plan:

- Specs: no expected spec file update for this behavior-preserving slice unless
  implementation reveals real drift.
- Runtime project skills: no expected change unless a new catalog-writer
  decomposition convention emerges.
- End-user docs: no expected change.
- SOW lifecycle: remains active until the selected slice is merged and enough
  production complexity backlog is reduced or explicitly split into narrower
  follow-up SOWs.

Open-source reference evidence:

- This is an internal behavior-preserving catalog-writer decomposition against
  already-specified canonical/store contracts. No new source-format or protocol
  claim is introduced, so no additional upstream clone/mirror research is
  required for this slice.

Open decisions:

- None for the operator. Technical sequencing belongs to the assistant.

### Fifth Slice Gate - claude-code benchmark baseline

Status: ready for test-first implementation after this SOW/spec update.

Selected fifth slice:

- Add deterministic Claude-code adapter `Scan` and `Tail` benchmarks and include
  them in the local workstation benchmark regression gate.
- Goal: create a real performance guard before reducing complexity in the
  Claude-code scanner/tailer state machines.
- Explicit non-goal: do not refactor `internal/adapters/claude_code/scanner.go`,
  `tailer.go`, or `parser.go` in this slice; do not change adapter semantics,
  cursor format, source discovery, ingester behavior, SQLite schema, REST/SSE,
  frontend presentation, or security posture.

Problem / root-cause model:

- Refreshed strict Lizard on current `master` shows Claude-code scanner/tailer
  remain high-risk complexity hotspots:
  - `internal/adapters/claude_code/scanner.go` has 9 warnings; largest selected
    functions include `scanAll` at 74 NLOC / CCN 19 / 128 physical lines and
    `streamLines` at 68 NLOC / CCN 18 / 108 physical lines.
  - `internal/adapters/claude_code/tailer.go` has 6 warnings; largest selected
    functions include `tailLoop` at 74 NLOC / CCN 21 / 112 physical lines and
    `flushDirty` at 56 NLOC / CCN 16 / 75 physical lines.
  - `internal/adapters/claude_code/parser.go` has 1 warning:
    `parseLine` at 54 NLOC / CCN 16 / 61 physical lines.
- The scanner/tailer code is behaviorally delicate: file watching, cursor
  offsets, symlink containment, partial-line parking, late `.meta.json` repair,
  and Agent-op finalization all interact. Refactoring it without a scan/tail
  performance guard would leave a blind spot in the next complexity slice.

Evidence reviewed:

- Direct strict Lizard command on current `master`:
  `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/adapters/claude_code/scanner.go internal/adapters/claude_code/tailer.go internal/adapters/claude_code/parser.go -l go -C 8 -L 50 -a 8`
  reported 16 warnings across the three files.
- Existing benchmark gate before this slice covers 5 baselined benchmarks in 4
  packages: ai-agent v2 adapter scan/tail, ingest batch insert, presenter
  sessions list, and notify hub fanout.
- Existing CI benchmark smoke already executes `go test -run=^$ -bench=.`
  across `./...`, but the local workstation regression gate only runs packages
  listed in `scripts/check-bench.sh` and only hard-gates names present in
  `bench/baseline.txt`.
- The earlier read-only scanner/tailer assessment identified Claude-specific
  scan/tail benchmarks as a prerequisite before production decomposition. This
  fifth slice satisfies that prerequisite and makes this SOW the explicit
  baseline-refresh authorization required by the benchmark policy.

Affected contracts and surfaces:

- `bench/baseline.txt` benchmark inventory and workstation baseline.
- `scripts/check-bench.sh` benchmark package list.
- `.github/workflows/ci.yml` required benchmark-count guard.
- `.agents/sow/specs/quality-gates.md` and
  `.agents/sow/specs/testing-strategy.md` benchmark inventory.
- `.agents/sow/specs/adapter-claude-code.md` throughput section.
- `internal/adapters/claude_code/bench_test.go` only; no production adapter
  behavior changes in this slice.

Spec deltas before tests/code:

- `.agents/sow/specs/quality-gates.md`: update benchmark inventory from 5
  paths / 4 packages to 7 paths / 5 packages, adding Claude-code Scan and Tail
  to the local workstation `scripts/check-bench.sh` gate.
- `.agents/sow/specs/testing-strategy.md`: update performance-regression test
  inventory to list the new Claude-code benchmark file.
- `.agents/sow/specs/adapter-claude-code.md`: add a throughput note naming the
  two deterministic benchmarks and stating they are included in the local
  workstation baseline.

Existing patterns to reuse:

- `internal/adapters/aiagent_v2/bench_test.go`: deterministic synthetic corpus,
  fixed append variants, producer-write fencing with `b.StopTimer`, event
  counting, `b.ReportMetric`, and `peak_heap_mb` sampling.
- `scripts/check-bench.sh`: baseline-first benchmark inventory, `benchstat`
  comparison, and >20% significant `sec/op` regression threshold.
- `.github/workflows/ci.yml` benchmark presence guard, updated from exactly 5
  baseline names to exactly 7.

Risk and blast radius:

- Medium gate-risk: adding benchmarks to the hard local baseline can make the
  full workstation gate slower or noisier. The benchmark bodies must be
  deterministic, small enough for repeated `-count=6`, and scoped to code CPU
  rather than OS fsnotify latency.
- Low runtime risk: benchmark files do not ship in the binary and the slice
  intentionally avoids production adapter refactors.
- CI risk: benchmark-count guard and bench smoke must agree with the new
  baseline inventory. A mismatch fails CI by design.

Sensitive data handling plan:

- Benchmarks use only synthetic transcripts under `b.TempDir()`.
- No real Claude-code transcript content, cwd, prompt, tool output, or model
  payload is written to durable artifacts.

Implementation plan:

1. Delegate benchmark/test implementation for `internal/adapters/claude_code`:
   synthetic projects tree builder, scan benchmark, tail-flush benchmark, and
   focused tests if helper correctness needs pinning.
2. Delegate benchmark-gate wiring: add the package to `scripts/check-bench.sh`,
   update CI's required benchmark count to 7, and refresh `bench/baseline.txt`
   with six samples for the seven benchmark names on this workstation.
3. Run focused Claude-code benchmark smoke and `scripts/check-bench.sh`.
4. Run full local gates and external review before commit/PR.

Validation plan:

- `go test ./internal/adapters/claude_code -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`
- `go test ./internal/adapters/claude_code -count=1`
- `go test -race -count=1 ./internal/adapters/claude_code`
- `scripts/check-bench.sh`
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh`.
- Full `./scripts/gates.sh`.
- PR checks must all pass before merge; `codacy-coverage` may skip on PRs by
  design, but every other row must be green.

Test-order note:

- This fifth slice adds benchmark coverage and gate wiring before production
  refactoring. The benchmark "failing" signal before implementation is absence
  from the Go benchmark inventory and mismatch with the just-updated specs.

Benchmarks:

- Required. The implementation must generate a deterministic six-sample
  `bench/baseline.txt` refresh including the five existing benchmarks and the
  two new Claude-code benchmarks. Baseline refresh is explicitly authorized by
  this SOW slice.

Artifact impact plan:

- Specs: quality gates, testing strategy, and Claude-code adapter throughput
  spec updated first.
- Runtime project skills: no expected change unless benchmark-gate conventions
  change during implementation.
- End-user docs: no expected change.
- SOW lifecycle: remains active after this benchmark prerequisite slice; the
  following slice will reduce Claude-code scanner/tailer complexity using the
  new benchmark guard.

Open-source reference evidence:

- No new Claude-code source-format claim is introduced in this slice. The
  existing adapter spec remains grounded in the previously cited local mirror
  evidence (`jarmuine/claude-code @ 4b9d30f79532`) and sanitized operator
  transcript observations. Benchmarks synthesize records matching the already
  specified JSONL contract rather than adding a new protocol assertion.

Open decisions:

- None for the operator. Technical sequencing belongs to the assistant.

### Sixth Slice Gate - claude-code scanner decomposition

Status: ready for test-first implementation after this SOW update.

Selected sixth slice:

- `internal/adapters/claude_code/scanner.go` scanner decomposition.
- Goal: reduce Codacy/Lizard maintainability findings in the Claude-code scanner
  while preserving transcript discovery, meta sidecar reading, cursor offsets,
  line streaming, partial-line parking, oversized-line recovery, orphan-root
  synthesis, Agent-op deferral collection, and Scan-side late-meta repair.
- Explicit non-goal: do not refactor `internal/adapters/claude_code/tailer.go`
  event-loop state machine or `internal/adapters/claude_code/parser.go`
  discriminator parsing in this slice except for narrowly shared helper moves
  required by scanner extraction. Do not change source-format semantics,
  cursor JSON shape, ingester behavior, SQLite schema, REST/SSE contracts,
  frontend presentation, or security posture.

Problem / root-cause model:

- The scanner file combines several jobs in one production file: filesystem
  discovery, symlink containment, meta sidecar collection and bounded reads,
  transcript opening, line streaming, parse-error policy, oversized-line
  recovery, full Scan orchestration, orphan-root synthesis, and SourceProgress
  checkpointing.
- The largest selected complexity hotspots are state-machine/orchestration
  functions, not known behavior bugs. The correct fix is decomposition into
  narrow helpers and small local structs while preserving event order and cursor
  state.
- Tailer remains a separate high-risk slice: it owns fsnotify event-loop
  behavior, dirty-set flushing, debounce/tick policy, and restart deferral
  state. Mixing tailer changes into this scanner slice would widen the blast
  radius beyond the new benchmark prerequisite's first intended use.

Evidence reviewed:

- Direct strict Lizard on current `master` after PR #54:
  `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/adapters/claude_code/scanner.go internal/adapters/claude_code/tailer.go internal/adapters/claude_code/parser.go -l go -C 8 -L 50 -a 8`
  reported 16 warnings across three production files.
- Scanner warnings selected for this slice:
  - `discoverProject`: 40 NLOC, 51 physical lines.
  - anonymous `discoverSessionSubagents` walk callback: CCN 10.
  - `readSessionMetas`: CCN 9.
  - `readTranscript`: CCN 9, 8 parameters, 87 physical lines.
  - `streamLines`: 68 NLOC, CCN 18, 108 physical lines.
  - `readOneLine`: CCN 9.
  - `scanAll`: 74 NLOC, CCN 19, 128 physical lines.
  - `emitOrphanRoots`: CCN 12.
  - `earliestTs`: CCN 9.
- Tailer/parser residual warnings remain real but deferred:
  `tailLoop`, `handleEvent`, `markExistingDirty` callback, `addWatchTree`
  callback, `flushDirty`, `repairChangedMetas`, and `parseLine`.
- Existing Claude-code adapter tests passed before this slice:
  `go test ./internal/adapters/claude_code -count=1`.
- Existing scanner/tailer tests already cover unknown-type dedup, symlink
  containment, meta containment and size caps, discovery fail-soft behavior,
  partial-line parking, oversized-line continuation, late-meta Scan repair,
  Scan→Tail window catch-up, parked/finalized deferral snapshots, and golden
  fixtures.

Affected contracts and surfaces:

- Claude-code adapter `Scan` behavior and cursor persistence.
- Shared scanner helpers also used by Tail (`readTranscript`,
  `readSessionMetas`, `metaHashes`, `repairChangedMetas` callers), so Tail
  focused tests and benchmark coverage are mandatory even if `tailer.go` is not
  the primary refactor target.
- No public API, SQLite schema, source discovery CLI, frontend, or operator
  behavior change is expected.

Spec deltas before tests/code:

- No spec file delta is required for this slice because it is a
  behavior-preserving scanner decomposition. `.agents/sow/specs/adapter-claude-code.md`
  already documents the target scanner behavior for discovery, symlink
  containment, meta error surfacing, meta size caps, Scan/Tail cursor keys,
  partial-line hold-back, oversized-line continuation, orphan-root synthesis,
  Agent-op finalization, Scan-side late-meta repair, and the Claude-code Scan
  and Tail benchmarks.
- If implementation reveals real spec/code drift, stop the slice, update the
  relevant spec first, then resume tests and production refactor.
- Implementation revealed one spec precision gap before slice acceptance:
  `.agents/sow/specs/adapter-claude-code.md` now states that synthetic
  orphan-root `Ts` uses the minimum first-parseable timestamp across child
  transcripts. Each child probe skips malformed lines, oversized lines, skipped
  known-no-op records, missing timestamps, and unparsable timestamps until the
  first parseable timestamp in that append-only file; `Ts=0` only when no child
  has a parseable timestamp.

Existing patterns to reuse:

- Small unexported helpers that each own one file-system, parse, or emit job.
- Existing `tailDeferral` and `repairChangedMetas` contracts as shared
  Scan/Tail invariants.
- Existing deterministic event ordering: sorted transcripts, sorted meta paths,
  sorted orphan roots, and deterministic child-finalization pairing.
- Existing benchmark style from the fifth slice: synthetic projects tree,
  fixed event counts, and `scripts/check-bench.sh` hard gate.

Risk and blast radius:

- Medium to high: scanner code controls historical backfill and the same
  `readTranscript`/line-streaming path is reused by Tail. Regressions can cause
  duplicate rows, missing rows, missing parent Agent-op finalization, suppressed
  meta repair, or silent source errors.
- Security risk is specific and testable: all reads must still open
  containment-checked symlink-resolved paths and must still refuse transcript or
  meta symlink escapes.
- Performance risk exists because every historical transcript passes through
  this path. The newly-added Claude-code Scan/Tail benchmark gate is mandatory.

Sensitive data handling plan:

- No real Claude-code transcript content is written to durable artifacts.
  Tests must use committed sanitized fixtures or synthetic transcript lines.
  Any temporary Codacy, Lizard, or benchmark output stays under `/tmp`.

Implementation plan:

1. Delegate a focused test audit and add characterization coverage only for
   real gaps around scanner helper boundaries before production changes.
   Candidate gaps: direct `readTranscript` resume/emit-gate assertions and
   scanner-orphan/meta behavior that currently relies only on broader Scan tests.
2. Delegate scanner decomposition:
   - split transcript discovery callbacks into named entry handlers;
   - extract meta sidecar loading and transcript-open setup into small helpers;
   - split `streamLines` into line outcome handlers that keep the physical
     last-record completion flag correct;
   - split `scanAll` into scan setup, per-transcript processing, progress
     checkpointing, finalization pairing, meta refresh/repair, and final cursor
     persistence helpers;
   - keep event values, event order, cursor JSON, error messages, and
     SourceProgress behavior stable unless a focused test proves the current
     text is irrelevant.
3. Run focused scanner/tailer tests, package tests, package race tests, strict
   Lizard, local Codacy file analysis, benchmark gate, full gates, and external
   second-opinion review.
4. Merge only after reviewers converge and PR checks are green.

Validation plan:

- `go test ./internal/adapters/claude_code -run 'TestScan|TestReadTranscript|TestReadOneLine|TestRestart|TestScanThenTail|TestFlushDirty|TestMeta|TestCollectMeta|TestTail' -count=1`
- `go test ./internal/adapters/claude_code -count=1`
- `go test -race -count=1 ./internal/adapters/claude_code`
- `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/adapters/claude_code/scanner.go internal/adapters/claude_code/scanner_discovery.go internal/adapters/claude_code/scanner_meta.go internal/adapters/claude_code/scanner_transcript.go internal/adapters/claude_code/scanner_run.go internal/adapters/claude_code/scanner_orphans.go internal/adapters/claude_code/scanner_characterization_test.go -l go -C 8 -L 50 -a 8`
- Local Codacy file analysis for changed Claude-code adapter files.
- `go test ./internal/adapters/claude_code -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`
- `scripts/check-bench.sh`
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh`.
- Full `./scripts/gates.sh`.
- PR checks must all pass before merge; `codacy-coverage` may skip on PRs by
  design, but every other row must be green.

Test-order note:

- This sixth slice is a behavior-preserving refactor. Existing and added
  characterization tests may pass before implementation. The failing signal is
  the stricter Codacy/Lizard maintainability report listed above.

Benchmarks:

- Required. Claude-code scanner changes are covered by both
  `BenchmarkClaudeScan_SyntheticCorpus` and `BenchmarkClaudeTail_SyntheticAppend`.
  Any statistically significant >20% `sec/op` regression blocks the slice.

Artifact impact plan:

- Specs: no expected spec file update unless real drift is found.
- Runtime project skills: no expected change unless a new scanner-decomposition
  convention emerges.
- End-user docs: no expected change.
- SOW lifecycle: remains active until this selected slice is merged and enough
  production complexity backlog is reduced or explicitly split into narrower
  follow-up SOWs.

Open-source reference evidence:

- This is a behavior-preserving decomposition against the already-specified
  Claude-code transcript contract. No new source-format claim is introduced, so
  no additional upstream clone/mirror research is required for this slice.

Open decisions:

- None for the operator. Technical sequencing belongs to the assistant.

### Seventh Slice Gate - claude-code tailer/parser decomposition

Status: ready for test-first implementation after this SOW update.

Selected seventh slice:

- `internal/adapters/claude_code/tailer.go` Tail event-loop and flush-path
  decomposition, with a narrow `internal/adapters/claude_code/parser.go`
  discriminator split if it removes the remaining parser hotspot without
  changing source-format semantics.
- Goal: reduce Codacy/Lizard maintainability findings in the Claude-code Tail
  path while preserving fsnotify watch behavior, debounce/flush semantics,
  cursor offsets, Scan-to-Tail catch-up, meta repair, Agent-op deferral
  durability, symlink containment, parse error policy, and parser output.
- Explicit non-goal: do not change scanner behavior, source-format semantics,
  cursor JSON shape, ingester behavior, SQLite schema, REST/SSE contracts,
  frontend presentation, benchmark thresholds, or runtime operator behavior.

Problem / root-cause model:

- After the sixth slice, `scanner.go` has no strict Lizard warnings, but the
  Tail path still combines watcher setup, event classification, dirty-set
  management, debounce/tick policy, transcript replay, meta repair, and
  Agent-op finalization in a few dense functions.
- `parseLine` still mixes envelope validation, type dispatch, typed payload
  decoding, known-no-op handling, unknown-type reporting, and tool-use-result
  probing. That is maintainability risk in untrusted JSONL parsing, not a
  requested semantic change.
- The right fix is narrow helper extraction and small local structs around
  existing state machines. The event order, cursor persistence, emitted
  canonical events, and surfaced errors must remain stable.

Evidence reviewed:

- Post-merge `master` local gates passed in 587s after PR #55: lint/static
  analysis, Go security/vulnerability checks, secrets over 847 tracked files,
  attribution scan, spec drift, Codacy coverage/config self-tests, systemd unit
  lint, build + bundle-size, seven-benchmark regression gate, Go race+coverage,
  frontend Vitest coverage, Go coverage threshold gate, adapter fuzz seed
  corpus, and Playwright/axe all passed. The run reported Go total coverage
  85.4%, gated `internal/*` aggregate coverage 90.7%,
  `internal/adapters/claude_code` coverage 85.9%, frontend Vitest coverage with
  631 passing tests, frontend E2E/axe with 51 passing tests, and main bundle
  size 132.2 KB gzipped.
- Direct strict Lizard on post-merge `master`:
  `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/adapters/claude_code/scanner.go internal/adapters/claude_code/tailer.go internal/adapters/claude_code/parser.go -l go -C 8 -L 50 -a 8`
  reported 7 remaining Claude-code production warnings:
  - `tailLoop`: 74 NLOC, CCN 21, 112 physical lines.
  - `handleEvent`: 27 NLOC, CCN 10.
  - `markExistingDirty` walk callback: 25 NLOC, CCN 9.
  - `addWatchTree` walk callback: 26 NLOC, CCN 9.
  - `flushDirty`: 56 NLOC, CCN 16, 9 parameters, 75 physical lines.
  - `repairChangedMetas`: 44 NLOC, CCN 12, 8 parameters, 54 physical lines.
  - `parseLine`: 54 NLOC, CCN 16, 61 physical lines.
- Existing Tail tests cover append pickup, new project directory watches,
  symlinked root watching, symlink transcript escape refusal, missing-root
  clean return, partial-line hold-back, transcript-relative path reconstruction,
  meta hash no-op suppression, late-meta AgentName repair, unreadable meta
  surfacing, parked/finalized deferral snapshots, restart no-gap/no-dup
  behavior, Scan-to-Tail catch-up, late-meta parent linkage, durable Agent-op
  finalization, no premature tool-use finalization, parked completion restore,
  no double finalize on replay, and late-meta rewrite no-double-finalize.
- Existing parser tests cover blank/whitespace skip, malformed JSON,
  missing type, unknown-type error classification, known no-op skip, user string
  content, user array content, assistant usage/content blocks, system compact
  boundary records, and fuzz no-panic behavior.

Affected contracts and surfaces:

- Claude-code `Tail` event stream and cursor persistence.
- fsnotify watcher lifecycle, create-directory race-window catch-up, periodic
  re-walk, debounce-triggered flush, and SourceProgress checkpoint emission.
- Shared meta-repair contract used by both Scan and Tail.
- Agent-op finalization deferral state: pending, completed/parked, finalized,
  and restart persistence.
- Parser record classification and typed payload decoding for records consumed
  by the mapper.
- No public API, schema, frontend, source configuration, or operator-facing
  behavior change is expected.

Spec deltas before tests/code:

- No spec file delta is required for this slice because it is a pure
  behavior-preserving refactor. `.agents/sow/specs/adapter-claude-code.md`
  already documents the target Tail behavior for watch setup, symlink
  containment, catch-up, partial-line parking, oversized-line recovery,
  meta repair, SourceProgress, Agent-op finalization, and the Claude-code
  Scan/Tail benchmarks.
- The parser behavior is already covered by the same adapter spec sections
  describing skipped known no-op records, surfaced unknown record types, and
  parsed record kinds.
- If implementation reveals real spec/code drift, stop the slice, update the
  relevant spec first, then resume tests and production refactor.

Existing patterns to reuse:

- The sixth slice's scanner split: keep orchestration files thin and move
  discovery/meta/transcript/finalization helpers into focused files.
- Small unexported helpers that own one state transition or one filesystem
  operation.
- Existing `tailDeferral`, `readTranscript`, `flushChangedMetas`, and
  `repairChangedMetas` contracts.
- Existing deterministic ordering: sorted dirty transcript names, sorted dirty
  meta names, sorted repair rels, and sorted child finalizations.
- Existing Claude-code Scan/Tail benchmarks and full benchmark regression gate.

Risk and blast radius:

- High within the adapter: Tail is the real-time path and a subtle regression
  can lose appended records, replay historical records, double-finalize Agent
  ops, suppress meta repairs, or hide source errors.
- Security-sensitive paths remain in scope: symlink containment for watched
  directories, transcripts, and meta sidecars must stay fail-closed.
- Performance risk exists because `flushDirty` runs for every real-time change;
  `BenchmarkClaudeTail_SyntheticAppend` and the full benchmark gate are
  mandatory.
- Parser risk is bounded but important: parsing untrusted JSONL must continue
  to return wrapped errors, skip only known no-op records silently, and never
  panic under fuzz.

Sensitive data handling plan:

- No real Claude-code transcript content is written to durable artifacts.
  Tests must use committed sanitized fixtures or synthetic transcript/meta
  lines. Any temporary Codacy, Lizard, or benchmark output stays under `/tmp`.

Implementation plan:

1. Delegate a focused Tail/parser test audit and add characterization coverage
   only for real helper-boundary gaps before production changes. Candidate
   gaps: direct `handleEvent` create-file/remove classification, direct
   `flushDirty` no-dirty/meta-only progress behavior, changed-meta repair order,
   and parser type-dispatch equivalence.
2. Delegate Tail decomposition:
   - introduce a small Tail runtime state struct for watcher/cursor/dirty sets,
     deferral, output, and error callback;
   - split watcher setup and startup catch-up from the `select` loop;
   - split event classification from dirty-set mutation;
   - extract walk callback bodies for existing-file dirty marking and watch-tree
     addition;
   - split `flushDirty` into changed-meta processing, dirty transcript
     processing, deferral pairing/checkpointing, and progress emission helpers;
   - keep event values, ordering, cursor JSON, error text, and side-effect order
     stable unless a focused test proves the current text is irrelevant.
3. If the Tail split leaves the parser warning as the next cheapest
   same-package win, delegate a narrow parser discriminator split:
   envelope decode, per-type body decode, tool-use-result probe, known-no-op
   handling, and unknown-type error construction.
4. Run focused Tail/parser tests, package tests, package race tests, strict
   Lizard, local Codacy file analysis, Claude-code benchmark smoke,
   `scripts/check-bench.sh`, full gates, and external second-opinion review.
5. Merge only after reviewers converge and PR checks are green.

Validation plan:

- `go test ./internal/adapters/claude_code -run 'TestTail|TestFlushDirty|TestScanThenTail|TestRestart|TestParseLine|TestReadTranscript|TestReadOneLine|TestMeta|TestCollectMeta' -count=1`
- `go test ./internal/adapters/claude_code -count=1`
- `go test -race -count=1 ./internal/adapters/claude_code`
- `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/adapters/claude_code/tailer.go internal/adapters/claude_code/tailer_*.go internal/adapters/claude_code/parser.go internal/adapters/claude_code/parser*.go internal/adapters/claude_code/*tail*_test.go internal/adapters/claude_code/parser*_test.go -l go -C 8 -L 50 -a 8`
- Local Codacy file analysis for changed Claude-code Tail/parser files.
- `go test ./internal/adapters/claude_code -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`
- `scripts/check-bench.sh`
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh`.
- Full `./scripts/gates.sh`.
- PR checks must all pass before merge; `codacy-coverage` may skip on PRs by
  design, but every other row must be green.

Test-order note:

- This seventh slice is a behavior-preserving refactor. Existing and added
  characterization tests may pass before implementation. The failing signal is
  the strict Codacy/Lizard maintainability report listed above.

Benchmarks:

- Required. Tail changes are covered directly by
  `BenchmarkClaudeTail_SyntheticAppend`; parser changes are covered indirectly
  by both Claude-code Scan and Tail benchmarks. Any statistically significant
  >20% `sec/op` regression blocks the slice.

Artifact impact plan:

- Specs: no expected spec file update unless real drift is found.
- Runtime project skills: no expected change unless a new tailer-decomposition
  convention emerges.
- End-user docs: no expected change.
- SOW lifecycle: remains active until this selected slice is merged and enough
  production complexity backlog is reduced or explicitly split into narrower
  follow-up SOWs.

Open-source reference evidence:

- This is a behavior-preserving decomposition against the already-specified
  Claude-code transcript contract. No new source-format claim is introduced, so
  no additional upstream clone/mirror research is required for this slice.

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
- Merged PR #52 for the third slice. Post-merge full gates were not claimed
  because the workstation-specific benchmark gate failed under high CPU load on
  unrelated `aiagent_v2` benchmarks. Controlled same-load comparison against
  the pre-v2-mapper commit showed no significant current-branch regression, and
  the next slice keeps full gates mandatory before merge.
- Refreshed current production Lizard triage on `master` after the first three
  merged slices and selected `internal/ingest/catalog.go` as the fourth
  production slice.
- Added focused provider-alias catalog characterization coverage in
  `internal/ingest/catalog_alias_test.go` before production refactoring.
- Refactored `internal/ingest/catalog.go` `onOpStarted` and `onOpFinalized`
  into smaller catalog identity, booking, persisted-row, delta, and totals
  update helpers while preserving SQL text/bind order and side-effect order.
- Addressed first fourth-slice review findings by splitting finalized helpers
  into `internal/ingest/catalog_finalize.go`, keeping `catalog.go` below the
  400-line production-file guideline, scalarizing the finalized-tool helper
  dependency to `endTs`, restoring a concise `ctx_max` contract comment, and
  adding direct provider-alias row-existence plus tool-namespace migration
  characterization coverage.
- Addressed later fourth-slice review findings by hardening catalog alias/status
  correction tests, updating stale `data-model.md` `ctx_max` code references,
  and correcting the `onOpStarted` `call_count` comment for identity-change
  migration.
- Merged PR #53 for the fourth slice. All PR checks passed before merge:
  Codacy static analysis, CodeQL action and language analyses, lint, test,
  frontend, gates, embed smoke, reviewer check, WIP guard, and CLA.
- Selected the fifth slice as a prerequisite benchmark-baseline slice for the
  Claude-code scanner/tailer. The slice adds deterministic Claude-code `Scan`
  and `Tail` benchmarks before production scanner/tailer decomposition.
- Updated the benchmark inventory specs first, then added the benchmark files,
  wired `scripts/check-bench.sh` to the Claude-code package, refreshed the
  seven-benchmark workstation baseline, and updated CI's benchmark-count guard
  from 5 to 7 required names.
- Selected the seventh slice after the merged scanner decomposition:
  behavior-preserving Claude-code tailer/parser decomposition with focused
  event, flush, repair, deferral, and parser tests.
- Split the Claude-code tail loop and parser dispatcher into focused files,
  keeping exported adapter contracts, cursor semantics, REST/SSE surfaces,
  frontend code, and specs unchanged because this slice intentionally changes
  internal maintainability only.
- Addressed the local Codacy file-length finding by moving tailer deferral
  tests into a focused test file, keeping `tailer_test.go` below the 500-NLOC
  Codacy medium-file threshold.

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
- PR #51 was merged, local `master` was fast-forwarded, and post-merge
  `./scripts/gates.sh` passed in 503s on the merged state: lint/static/security/
  vulnerability checks, secrets, attribution, spec drift, Codacy config/coverage
  self-tests, systemd, build + bundle-size, benchmark regression gate, Go
  race+coverage, frontend Vitest coverage, Go coverage threshold gate, adapter
  fuzz seed corpus, and Playwright/axe. The run reported Go total coverage
  85.2%, gated `internal/*` aggregate coverage 90.5%,
  `internal/adapters/aiagent_v2` coverage 92.5%, frontend Vitest coverage with
  631 passing tests, frontend E2E/axe with 51 passing tests, and no benchmark
  `sec/op` regression over the 20% gate.

Third slice focused validation:

- Added `TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles`, a
  characterization test proving the persisted op lookup treats only
  `sql.ErrNoRows` as a non-fatal orphan finalize and bubbles schema/transaction
  lookup errors instead of silently skipping pricing or duration work.
- Refactored `internal/ingest/writer.go` `applyOpFinalized` into helpers for
  persisted op lookup, cost resolution, timing resolution, ops-row update,
  dirty-set marking, and catalog forwarding. The side-effect order remains
  `requireSessionID` -> `opPriorTotals` -> lookup -> cost -> timing -> update
  -> dirty marks -> catalog.
- Focused ingest validation passed:
  `go test ./internal/ingest -run 'TestApplyOpFinalized|TestWriter_Pricer|TestWriter_Priceable|TestWriter_PricingMiss|TestWriter_ApplyOpFinalized|TestWriter_PricerComputedCostFlowsToCatalog|TestCatalog_Re|TestRefreshRollups_OpFinalized|TestWriter_ReasoningOpDoesNotOverwriteParentLLM|TestFTS' -count=1`
  in 0.280s.
- Package validation passed: `go test ./internal/ingest -count=1` in 8.750s.
- Package race validation passed:
  `go test -race -count=1 ./internal/ingest` in 332.579s.
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh` passed. The
  secret scan covered 837 tracked files.
- Direct Lizard on `internal/ingest/writer.go` and
  `internal/ingest/writer_test.go` reported zero default-threshold warnings.
  The selected hotspot changed from `applyOpFinalized` 74 NLOC / CCN 17 /
  130 physical lines to 26 NLOC / CCN 6 / 27 physical lines.
- Local Codacy file analysis for `internal/ingest/writer.go` still reported six
  residual Lizard findings, all outside the selected `applyOpFinalized` hotspot:
  `apply` at line 420 CCN 13, the anonymous handler at line 639 CCN 10, the
  anonymous handler at line 754 NLOC 76 and CCN 11, the handler at line 1290
  CCN 12, and file-level NLOC 925. These remain backlog for later slices.
- Quick benchmark smoke passed:
  `go test ./internal/ingest -run '^BenchmarkBatchInsert$' -bench '^BenchmarkBatchInsert$' -benchmem -count=3`
  with runs at 123.951 ms/op, 119.172 ms/op, and 121.325 ms/op.
- Full `./scripts/gates.sh` passed after Round 12 reviewer fixes in 594s:
  lint/static/security/vulnerability checks, secrets, attribution, spec drift,
  Codacy config/coverage self-tests, systemd, build + bundle-size, benchmark
  regression gate, Go race+coverage, frontend Vitest coverage, Go coverage
  threshold gate, adapter fuzz seed corpus, and Playwright/axe. The benchmark
  gate reported no `sec/op` regression over the 20% threshold; `BatchInsert`
  was neutral at 115.5 ms/op vs 123.5 ms/op baseline. The run reported Go total
  coverage 85.2%, gated `internal/*` aggregate coverage 90.6%,
  `internal/ingest` coverage 85.9%, frontend Vitest coverage with 631 passing
  tests, and frontend E2E/axe with 51 passing tests.
- Round 13 test-hardening follow-up passed:
  `go test ./internal/ingest -run 'TestWriter_ApplyOpFinalizedLookupNonErrNoRowsBubbles|TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles' -count=1`
  in 0.016s, and the broader focused apply-finalize suite listed above in
  0.335s.
- Package validation after the Round 13 test-hardening follow-up passed:
  `go test ./internal/ingest -count=1` in 9.396s.
- Direct Lizard after the Round 13 test-hardening follow-up still reported zero
  default-threshold warnings across `internal/ingest/writer.go` and
  `internal/ingest/writer_test.go`. `applyOpFinalized` remained 26 NLOC /
  CCN 6 / 27 physical lines.
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh` passed after
  the Round 13 test-hardening follow-up. The secret scan covered 837 tracked
  files.
- Full `./scripts/gates.sh` passed after the Round 13 test-hardening follow-up
  in 504s: lint/static/security/vulnerability checks, secrets, attribution,
  spec drift, Codacy config/coverage self-tests, systemd, build + bundle-size,
  benchmark regression gate, Go race+coverage, frontend Vitest coverage, Go
  coverage threshold gate, adapter fuzz seed corpus, and Playwright/axe. The
  benchmark gate reported no `sec/op` regression over the 20% threshold;
  `BatchInsert` improved to 113.3 ms/op vs 123.5 ms/op baseline. The run
  reported Go total coverage 85.2%, gated `internal/*` aggregate coverage
  90.6%, `internal/ingest` coverage 85.9%, frontend Vitest coverage with 631
  passing tests, and frontend E2E/axe with 51 passing tests.
- Round 14 test/comment follow-up passed:
  `go test ./internal/ingest -run 'TestResolveFinalizedOpTiming|TestResolveFinalizedOpCost|TestWriter_ApplyOpFinalizedLookupNonErrNoRowsBubbles|TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles' -count=1`
  in 0.018s.
- After the first Round 14 full-gate attempt, `golangci-lint` correctly failed
  on staticcheck `SA1012` because the new nil-pricer helper test passed a nil
  `context.Context`. The test-only follow-up changed that argument to
  `context.Background()` while preserving the nil transaction/pricer guard
  coverage. The focused nil-pricer test passed in 0.017s, and
  `golangci-lint run --timeout=5m ./internal/ingest` passed with zero issues.
- Direct Lizard after the Round 14 test/comment follow-up still reported zero
  default-threshold warnings across `internal/ingest/writer.go` and
  `internal/ingest/writer_test.go`. `applyOpFinalized` remained 26 NLOC /
  CCN 6 / 27 physical lines.
- `git diff --check` passed after the Round 14 test/comment follow-up.
- One full-gate attempt later failed in the global `go test -race ./...` stage
  on `internal/adapters/aiagent_v3`
  `TestTail_HoldsBackPartialLineThenCompletes` with no touched adapter code.
  Targeted follow-up evidence showed the failure was transient under full
  workstation load: the exact test passed 20 non-race iterations and 10 race
  iterations, and the full `internal/adapters/aiagent_v3` package then passed
  `go test -race ./internal/adapters/aiagent_v3 -count=5`.
- Final full `./scripts/gates.sh` rerun passed in 520s: lint/static/security/
  vulnerability checks, secrets, attribution, spec drift, Codacy config/coverage
  self-tests, systemd, build + bundle-size, benchmark regression gate, Go
  race+coverage, frontend Vitest coverage, Go coverage threshold gate, adapter
  fuzz seed corpus, and Playwright/axe. The benchmark gate reported no `sec/op`
  regression over the 20% threshold; `BatchInsert` improved to 118.7 ms/op vs
  123.5 ms/op baseline. The run reported Go total coverage 85.2%, gated
  `internal/*` aggregate coverage 90.6%, `internal/ingest` coverage 85.9%,
  frontend Vitest coverage with 631 passing tests, and frontend E2E/axe with 51
  passing tests.
- PR #52 initial CI then passed every GitHub-native row except Codacy Static
  Code Analysis; `codacy-coverage` skipped by design. Codacy Cloud reported one
  new issue introduced by this slice:
  `internal/ingest/writer_test.go` `TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles`
  had CCN 9 against the enforced limit 8.
- The test-only Codacy follow-up extracted the source/session/schema-break setup
  into helpers while keeping the lookup-error assertion in the test body. Focused
  validation passed:
  `go test ./internal/ingest -run 'TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles|TestWriter_ApplyOpFinalizedLookupNonErrNoRowsBubbles|TestResolveFinalizedOpTiming|TestResolveFinalizedOpCostNilPricerReturnsEventCost' -count=1`
  in 0.014s, `golangci-lint run --timeout=5m ./internal/ingest` passed with
  zero issues, and Lizard with Codacy's CCN threshold reported the fixed test at
  CCN 4.
- Full `./scripts/gates.sh` passed after the test-only Codacy follow-up in
  517s: lint/static/security/vulnerability checks, secrets, attribution, spec
  drift, Codacy config/coverage self-tests, systemd, build + bundle-size,
  benchmark regression gate, Go race+coverage, frontend Vitest coverage, Go
  coverage threshold gate, adapter fuzz seed corpus, and Playwright/axe. The
  benchmark gate reported no `sec/op` regression over the 20% threshold;
  `BatchInsert` improved to 116.0 ms/op vs 123.5 ms/op baseline. The run
  reported Go total coverage 85.2%, gated `internal/*` aggregate coverage
  90.6%, `internal/ingest` coverage 85.9%, frontend Vitest coverage with 631
  passing tests, and frontend E2E/axe with 51 passing tests.

Fourth slice focused validation:

- Added provider-alias catalog characterization coverage in
  `internal/ingest/catalog_alias_test.go` before production refactoring:
  provider-alias correction migration, empty provider-alias re-emission
  preservation, alias-path cache/cost totals, and model duration totals.
- Focused pre-refactor validation passed:
  `go test ./internal/ingest -run 'TestCatalog_.*Alias|TestCatalog_ReEmittedOpNoDoubleCount|TestCatalog_LLMIdentityChangeMigratesContribution|TestCatalog_LLMReEmitEmptyProviderModelNoDrain' -count=1`
  in 0.033s. The new tests pass on current production code, so they pin
  existing behavior rather than exposing a bug.
- Direct strict Lizard on the final alias test file passed with zero warnings:
  `~/.codacy/runtimes/lizard-1/venv/bin/lizard internal/ingest/catalog_alias_test.go -l go -C 8 -L 50 -a 8`.
  File metrics: 153 NLOC, max function 35 NLOC, max CCN 5, max parameter
  count 4.
- Refactored `internal/ingest/catalog.go` `onOpStarted` from 60 NLOC /
  CCN 22 / 98 physical lines to 10 NLOC / CCN 3, and `onOpFinalized` from
  96 NLOC / CCN 17 / 117 physical lines to 17 NLOC / CCN 5.
- Direct strict Lizard on `internal/ingest/catalog.go` passed with zero
  warnings at `-C 8 -L 50 -a 8`; the production file is 313 NLOC and the
  largest remaining function is 33 NLOC / CCN 5.
- Focused post-refactor ingest validation passed:
  `go test ./internal/ingest -run 'TestCatalog|TestWriter_PricerComputedCostFlowsToCatalog|TestWriter_OpFailureBumpsFailureCount|TestWriter_Pricer|TestWriter_Priceable|TestWriter_PricingMiss|TestOpDuration|TestPricing' -count=1`
  in 0.192s.
- Package validation passed: `go test ./internal/ingest -count=1` in 9.759s.
- Package race validation passed:
  `go test -race -count=1 ./internal/ingest` in 334.872s.
- `golangci-lint run --timeout=5m ./internal/ingest` passed with zero issues.
- Local Codacy Analysis CLI focused on `internal/ingest/catalog.go` and
  `internal/ingest/catalog_alias_test.go` reported 0 issues across
  Trivy/Semgrep/Lizard:
  `/tmp/ai-viewer-sow0047-catalog-codacy-final-focused.json`.
- `git diff --check`, `scripts/scan-secrets.sh`,
  `scripts/scan-ai-attribution.sh`, and `scripts/spec-drift.sh` passed after
  the code/test split; the secret scan covered 837 tracked files.
- `scripts/check-bench.sh` passed. The relevant ingest benchmark,
  `BatchInsert-16`, measured 117.7 ms/op vs the 123.5 ms/op baseline in one
  focused run and 115.6 ms/op vs baseline inside the full gate; neither run had
  a gated `sec/op` regression.
- Full `./scripts/gates.sh` passed in 610s: lint/static/security/vulnerability
  checks, secrets, attribution, spec drift, Codacy config/coverage self-tests,
  systemd, build + bundle-size, benchmark regression gate, Go race+coverage,
  frontend Vitest coverage, Go coverage threshold gate, adapter fuzz seed
  corpus, and Playwright/axe. The run reported Go total coverage 85.3%, gated
  `internal/*` aggregate coverage 90.7%, `internal/ingest` coverage 86.2%,
  frontend Vitest coverage with 631 passing tests, frontend E2E/axe with
  51 passing tests, and no benchmark `sec/op` regression over the 20% gate.
- Round 17 reviewer follow-up validation passed after the catalog-finalize split
  and test hardening:
  `go test ./internal/ingest -run 'TestCatalog_.*Alias|TestCatalog_ToolNamespaceCorrectionMigratesContribution|TestCatalog_ReEmittedOpNoDoubleCount|TestCatalog_LLMIdentityChangeMigratesContribution|TestCatalog_LLMReEmitEmptyProviderModelNoDrain|TestCatalog_ToolReEmitEmptyNamespaceNoMigrate|TestCatalog_KindChangeMigratesAcrossTables' -count=1`
  in 0.053s, `go test ./internal/ingest -count=1` in 9.275s,
  `go test -race -count=1 ./internal/ingest` in 361.124s, and
  `golangci-lint run --timeout=5m ./internal/ingest` with 0 issues.
- Direct strict Lizard after the split reported zero warnings across
  `internal/ingest/catalog.go`, `internal/ingest/catalog_finalize.go`, and
  `internal/ingest/catalog_alias_test.go` at `-C 8 -L 50 -a 8`. Physical line
  counts are `catalog.go` 244, `catalog_finalize.go` 179, and
  `catalog_alias_test.go` 238; file NLOC are 172, 148, and 224 respectively.
- Local Codacy Analysis CLI after the review fixes reported 0 issues across
  Trivy/Semgrep/Lizard for `internal/ingest/catalog.go`,
  `internal/ingest/catalog_finalize.go`, and
  `internal/ingest/catalog_alias_test.go`:
  `/tmp/ai-viewer-sow0047-catalog-codacy-after-review-fixes.json`.
- Full `./scripts/gates.sh` passed after the Round 17 review fixes in 519s:
  lint/static/security/vulnerability checks, secrets, attribution, spec drift,
  Codacy config/coverage self-tests, systemd, build + bundle-size, benchmark
  regression gate, Go race+coverage, frontend Vitest coverage, Go coverage
  threshold gate, adapter fuzz seed corpus, and Playwright/axe. The benchmark
  gate reported no `sec/op` regression over the 20% threshold; `BatchInsert`
  was 134.6 ms/op vs 123.5 ms/op baseline. The run reported Go total coverage
  85.3%, gated `internal/*` aggregate coverage 90.7%, `internal/ingest`
  coverage 86.2%, frontend Vitest coverage with 631 passing tests, frontend
  E2E/axe with 51 passing tests, and no benchmark `sec/op` regression over the
  20% gate.
- Round 18 test-hardening validation passed after accepting reviewer
  non-blocking coverage suggestions:
  `go test ./internal/ingest -run 'TestCatalog_.*Alias|TestCatalog_ToolNamespaceCorrectionMigratesContribution|TestCatalog_StatusCorrection|TestCatalog_ProviderWithoutModel|TestCatalog_ReEmittedOpNoDoubleCount|TestCatalog_LLMIdentityChangeMigratesContribution|TestCatalog_LLMReEmitEmptyProviderModelNoDrain|TestCatalog_ToolReEmitEmptyNamespaceNoMigrate|TestCatalog_KindChangeMigratesAcrossTables' -count=1`
  and direct strict Lizard on `internal/ingest/catalog_alias_test.go` passed
  with zero threshold warnings. The hardened test file is 306 physical lines,
  289 NLOC, and its largest test function is 43 NLOC / CCN 3.
- Post-hardening package validation passed:
  `go test ./internal/ingest -count=1` in 10.071s,
  `golangci-lint run --timeout=5m ./internal/ingest` with 0 issues, and local
  Codacy Analysis CLI reported 0 issues across Trivy/Semgrep/Lizard for
  `internal/ingest/catalog.go`, `internal/ingest/catalog_finalize.go`, and
  `internal/ingest/catalog_alias_test.go`:
  `/tmp/ai-viewer-sow0047-catalog-codacy-after-round18-tests.json`.
- `git diff --check`, `scripts/scan-ai-attribution.sh`,
  `scripts/spec-drift.sh`, and `scripts/scan-secrets.sh` passed after the
  Round 18 SOW/test hardening; the secret scan covered 837 tracked files.
- After exact staging of the two new Go files, `git diff --cached --check`,
  `scripts/scan-ai-attribution.sh`, `scripts/spec-drift.sh`, and
  `scripts/scan-secrets.sh` passed; the staged secret scan covered 839 tracked
  files.
- Final fourth-slice pre-merge fast checks passed after Round 20 fixes:
  `git diff --cached --check`, `scripts/spec-drift.sh`,
  `scripts/scan-secrets.sh`, `scripts/scan-ai-attribution.sh`, and
  `go test ./internal/ingest -count=1` in 9.027s. The final staged secret scan
  covered 839 tracked files.
- Final fourth-slice full `./scripts/gates.sh` passed in 520s: lint/static
  analysis, Go security/vulnerability checks, secrets, attribution, spec drift,
  Codacy coverage/config self-tests, systemd unit lint, build + bundle-size,
  benchmark regression gate, Go race+coverage, frontend Vitest coverage, Go
  coverage threshold gate, adapter fuzz seed corpus, and Playwright/axe all
  passed. The benchmark gate reported no `sec/op` regression over the 20%
  threshold; `BatchInsert` improved to 116.9 ms/op vs the 123.5 ms/op baseline.
  The run reported Go total coverage 85.3%, gated `internal/*` aggregate
  coverage 90.6%, `internal/ingest` coverage 86.2%, frontend Vitest coverage
  with 631 passing tests, frontend E2E/axe with 51 passing tests, and main
  frontend bundle size 132.2 KB gzipped against the 500 KB budget.

Fifth slice focused validation:

- `go test ./internal/adapters/claude_code -count=1` passed in 6.667s.
- `go test -race -count=1 ./internal/adapters/claude_code` passed in 7.694s.
- `go test ./internal/adapters/claude_code -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`
  passed. Latest smoke results: `BenchmarkClaudeScan_SyntheticCorpus` at
  12.39 ms/op, 35.16 MB/s, 166089 events/sec, 6456 transcripts/sec,
  10818097 B/op, and 45954 allocs/op; `BenchmarkClaudeTail_SyntheticAppend` at
  51.337 us/op, 24.49 MB/s, 116874 events/sec, 82633 B/op, and 148 allocs/op.
- Direct strict Lizard on
  `internal/adapters/claude_code/bench_test.go` and
  `internal/adapters/claude_code/bench_types_test.go` passed with zero
  threshold warnings at `-C 8 -L 50 -a 8`. Physical line counts are
  `bench_test.go` 402 and `bench_types_test.go` 68 after the Round 24 review
  fixes.
- Local Codacy Analysis CLI initially found two real ShellCheck SC2086 issues in
  `scripts/check-bench.sh` diagnostic printing. The fix changed the missing and
  new-benchmark diagnostic output to quoted `read -r` loops without weakening
  benchmark comparison behavior. Rerun result:
  `/tmp/ai-viewer-sow0047-claude-bench-codacy-rerun.json` reported 0 issues
  across Trivy, Semgrep/Opengrep, ShellCheck, Lizard, and ESLint8 for the
  changed benchmark/script/workflow files.
- `shellcheck scripts/check-bench.sh`, `scripts/test/check-bench-test.sh`,
  `git diff --check`, and `gofmt -l` on the new Claude-code benchmark Go files
  passed; the benchmark self-test reported 8/8 assertions pass.
- `scripts/check-bench.sh` passed across all seven baselined benchmarks. No
  statistically-significant `sec/op` regression exceeded the 20% threshold.
  The new Claude-code comparisons were `ClaudeScan_SyntheticCorpus` at
  12.69 ms/op vs 13.34 ms/op baseline (-4.87%, p=0.009) and
  `ClaudeTail_SyntheticAppend` at 53.42 us/op vs 52.15 us/op baseline
  (noise band, p=0.937).
- Full staged-candidate `./scripts/gates.sh` passed in 557s: lint/static
  analysis, Go security/vulnerability checks, secrets over 841 tracked files,
  attribution, spec drift, Codacy coverage/config self-tests, systemd unit
  lint, build + bundle-size, seven-benchmark regression gate, Go race+coverage,
  frontend Vitest coverage, Go coverage threshold gate, adapter fuzz seed
  corpus, and Playwright/axe all passed. The run reported Go total coverage
  85.3%, gated `internal/*` aggregate coverage 90.7%,
  `internal/adapters/claude_code` coverage 84.3%, frontend Vitest coverage with
  631 passing tests, frontend E2E/axe with 51 passing tests, and main frontend
  bundle size 132.2 KB gzipped against the 500 KB budget.
- Round 21 review-fix focused validation passed:
  `go test ./internal/adapters/claude_code -count=1`,
  `go test -race -count=1 ./internal/adapters/claude_code`,
  `go test ./internal/adapters/claude_code -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`,
  direct strict Lizard on the two Claude-code benchmark files,
  local Codacy Analysis CLI at
  `/tmp/ai-viewer-sow0047-claude-bench-codacy-reviewfix.json`,
  baseline inventory check (7 unique benchmark names, 6 samples each),
  stale-count grep for old 5-benchmark/4-package wording, and
  `scripts/check-bench.sh`. The refreshed benchmark gate passed with no
  statistically-significant `sec/op` regression over the 20% threshold; the
  Claude-code comparisons were `ClaudeScan_SyntheticCorpus` at 12.96 ms/op vs
  14.95 ms/op baseline (-13.27%, p=0.015) and
  `ClaudeTail_SyntheticAppend` at 54.19 us/op vs 61.73 us/op baseline
  (-12.22%, p=0.009).
- Round 22 benchmark-assertion fix validation passed after adding hard failure
  on adapter-reported benchmark errors and exact event-count assertions:
  `go test ./internal/adapters/claude_code -count=1`,
  `go test -race -count=1 ./internal/adapters/claude_code`,
  `go test ./internal/adapters/claude_code -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`,
  `gofmt -l internal/adapters/claude_code/bench_test.go internal/adapters/claude_code/bench_types_test.go`,
  and a baseline inventory check for 7 unique benchmark names with 6 samples
  each. The benchmark smoke reported `BenchmarkClaudeScan_SyntheticCorpus` at
  12.858 ms/op, 160053 events/sec, and 47044 allocs/op, and
  `BenchmarkClaudeTail_SyntheticAppend` at 55.133 us/op, 108827 events/sec,
  and 152 allocs/op.
- Refreshed `bench/baseline.txt` again after the Round 22 benchmark timed-body
  change with the established seven-benchmark command:
  `go test -run=^$ -bench=. -benchmem -count=6 ./internal/adapters/aiagent_v2/ ./internal/adapters/claude_code/ ./internal/ingest/ ./internal/presenter/ ./internal/notify/`.
  The refresh produced 42 benchmark samples: 7 benchmark names with 6 samples
  each. An immediate full benchmark-gate rerun after back-to-back benchmark
  work failed on `ClaudeScan_SyntheticCorpus` at +31.24% `sec/op`; focused
  `-count=10` Claude Scan/Tail runs then showed stable samples, and the
  subsequent uncontended `scripts/check-bench.sh` passed with no `sec/op`
  regression over 20%. The passing comparison reported
  `ClaudeScan_SyntheticCorpus` at 12.90 ms/op vs 12.73 ms/op baseline
  (noise band, p=0.093) and `ClaudeTail_SyntheticAppend` at 57.07 us/op vs
  53.89 us/op baseline (+5.91%, p=0.002).
- Round 23 review-fix validation passed after correcting benchmark byte
  accounting and durable benchmark inventory drift: `go test
  ./internal/adapters/claude_code -count=1`, `go test -race -count=1
  ./internal/adapters/claude_code`, `go test ./internal/adapters/claude_code
  -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`, `gofmt -l
  internal/adapters/claude_code/bench_test.go
  internal/adapters/claude_code/bench_types_test.go`, baseline inventory check
  for 7 unique benchmark names with 6 samples each, and
  `scripts/check-bench.sh`. The regenerated baseline still has 42 benchmark
  samples. The Claude-code benchmark smoke passed after `BenchmarkClaudeTail`
  switched to full parsed file bytes and `BenchmarkClaudeScan` counted
  `.meta.json` sidecar bytes.
- Final Round 25 full `./scripts/gates.sh` passed in 554s after external
  review convergence: lint/static analysis, Go security/vulnerability checks,
  secrets over 841 tracked files, attribution, spec drift, Codacy
  coverage/config self-tests, systemd unit lint, build + bundle-size,
  seven-benchmark regression gate, Go race+coverage, frontend Vitest coverage,
  Go coverage threshold gate, adapter fuzz seed corpus, and Playwright/axe all
  passed. The benchmark gate reported no `sec/op` regression over the 20%
  threshold. The run reported Go total coverage 85.3%, gated `internal/*`
  aggregate coverage 90.6%, `internal/adapters/claude_code` coverage 84.3%,
  frontend Vitest coverage with 631 passing tests, frontend E2E/axe with 51
  passing tests, and main frontend bundle size 132.2 KB gzipped against the
  500 KB budget.

Sixth slice focused validation:

- Added `internal/adapters/claude_code/scanner_characterization_test.go` to pin
  scanner helper boundaries before the production split: EOF replay rebuilds
  parent Agent-op refs without emitting replay events; EOF replay does not mark
  a child complete below the emit gate; oversized complete lines skip with
  `errLineTooLong`; oversized partial EOF lines park with `io.EOF` and
  consumed `0`; underlying reader errors propagate; orphan-root timestamp
  synthesis skips malformed, known-no-op, oversized, timestampless,
  unparsable, and non-positive child records.
- Split the Claude-code scanner into focused production files:
  `scanner.go` (constants/types), `scanner_discovery.go`, `scanner_meta.go`,
  `scanner_transcript.go`, `scanner_run.go`, and `scanner_orphans.go`.
  `tailer.go`, `parser.go`, `mapper.go`, cursor shape, REST/SSE contracts, and
  frontend code were intentionally untouched.
- Focused regression suite passed:
  `go test ./internal/adapters/claude_code -run 'TestReadOneLine|TestScan_OrphanRoot|TestReadTranscript_Replay' -count=1`.
- Claude-code package tests passed:
  `go test ./internal/adapters/claude_code -count=1`.
- Claude-code package race test passed:
  `go test -race -count=1 ./internal/adapters/claude_code`.
- Direct strict Lizard on the split scanner production files and new
  characterization test passed with zero threshold warnings at `-C 8 -L 50 -a
  8`. The analyzed files were `scanner.go`, `scanner_discovery.go`,
  `scanner_meta.go`, `scanner_transcript.go`, `scanner_run.go`,
  `scanner_orphans.go`, and `scanner_characterization_test.go`; total warning
  count was 0.
- Local Codacy Analysis CLI file-scoped run passed with 0 issues:
  `/tmp/ai-viewer-sow0047-claude-scanner-codacy-final.json`. Trivy,
  Semgrep/Opengrep, and Lizard analyzed the seven scanner files; Lizard
  reported 0 issues.
- Final sixth-slice full `./scripts/gates.sh` passed in 551s after external
  review convergence: lint/static analysis, Go security/vulnerability checks,
  secrets over 847 tracked files, attribution scan, spec drift,
  Codacy coverage/config self-tests, systemd unit lint, build + bundle-size,
  seven-benchmark regression gate, Go race+coverage, frontend Vitest coverage,
  Go coverage threshold gate, adapter fuzz seed corpus, and Playwright/axe all
  passed. The benchmark gate reported no `sec/op` regression over the 20%
  threshold. The run reported Go total coverage 85.5%, gated `internal/*`
  aggregate coverage 90.8%, `internal/adapters/claude_code` coverage 85.8%,
  frontend Vitest coverage with 631 passing tests, frontend E2E/axe with 51
  passing tests, and main frontend bundle size 132.2 KB gzipped against the
  500 KB budget.

Seventh slice focused validation:

- Added/kept focused Claude-code tailer/parser tests before production
  refactoring: watcher event marking and error surfacing, empty and meta-only
  flush behavior, meta repair ordering, parser `tool_use_result` probing,
  parser decode-error wrapping, and tail deferral restore/snapshot behavior.
- Split the Claude-code tailer into focused production files:
  `tailer.go`, `tailer_loop.go`, `tailer_events.go`, `tailer_watch.go`,
  `tailer_flush.go`, `tailer_meta.go`, `tailer_deferral.go`, and
  `tailer_transcript.go`. Split the parser dispatcher into `parser.go` and
  `parser_decode.go`.
- Focused regression suite passed:
  `go test ./internal/adapters/claude_code -run 'TestTail|TestFlushDirty|TestScanThenTail|TestRestart|TestParseLine|TestReadTranscript|TestReadOneLine|TestMeta|TestCollectMeta|TestHandleEvent|TestRepairChangedMetas' -count=1`.
- Claude-code package race test passed:
  `go test -race -count=1 ./internal/adapters/claude_code`.
- Benchmark smoke passed:
  `go test ./internal/adapters/claude_code -run '^$' -bench 'BenchmarkClaude(Scan|Tail)' -benchmem -count=1`.
- Direct strict Lizard on the split tailer/parser production and focused test
  files passed with zero threshold warnings at `-C 8 -L 50 -a 8`. The selected
  production hotspots are now below threshold: `tailLoop` 8 NLOC / CCN 3,
  `flushDirty` 15 NLOC / CCN 5, `repairChangedMetas` 12 NLOC / CCN 1, and
  `parseLine` 11 NLOC / CCN 3. `tailer_test.go` is now 466 NLOC, below the
  500-NLOC Codacy medium-file threshold.
- Local Codacy Analysis CLI file-scoped run passed with 0 issues:
  `/tmp/ai-viewer-sow0047-claude-tailer-parser-codacy-r2.json`. Trivy,
  Semgrep/Opengrep, and Lizard analyzed the 16 changed Claude-code files.
- Cross-cutting checks passed before full gates: `git diff --check`,
  `scripts/scan-secrets.sh`, `scripts/scan-ai-attribution.sh`, and
  `scripts/spec-drift.sh`.
- After staging the new split files explicitly, staged safety checks passed:
  `git diff --cached --check`, staged Go `gofmt -l`, staged Go `goimports -l`,
  `scripts/scan-secrets.sh`, and `scripts/scan-ai-attribution.sh`. The staged
  secret scan covered 857 tracked files.
- `scripts/check-bench.sh` passed before full gates and again inside full
  gates with no `sec/op` regression over the 20% threshold. The Claude-code
  comparisons remained within noise: `ClaudeScan_SyntheticCorpus` around
  12.83-13.13 ms/op vs baseline, and `ClaudeTail_SyntheticAppend` around
  54.64-59.22 us/op vs baseline.
- Full seventh-slice candidate `./scripts/gates.sh` passed in 550s:
  lint/static analysis, Go security/vulnerability checks, secrets over 847
  tracked files, attribution scan, spec drift, Codacy coverage/config
  self-tests, systemd unit lint, build + bundle-size, seven-benchmark
  regression gate, Go race+coverage, frontend Vitest coverage, Go coverage
  threshold gate, adapter fuzz seed corpus, and Playwright/axe all passed. The
  run reported Go total coverage 85.6%, gated `internal/*` aggregate coverage
  90.9%, `internal/adapters/claude_code` coverage 86.6%, frontend Vitest
  coverage with 631 passing tests, frontend E2E/axe with 51 passing tests, and
  main frontend bundle size 132.2 KB gzipped against the 500 KB budget.
- Round 29 review-fix focused validation passed after making
  `transcriptSessionDir` build a fresh slice instead of appending into a
  caller-provided subslice:
  `go test ./internal/adapters/claude_code -run 'TestTranscriptForRel|TestTail|TestScanThenTail' -count=1`,
  direct strict Lizard on `tailer_transcript.go`, and a
  `TODO|FIXME|nolint|#nosec` scan on the touched file.
- Round 30 review-fix focused validation passed after making the mutating
  dirty-set/flush receiver contracts explicit and adding direct coverage for
  the `transcriptSessionDir` aliasing fix:
  `go test ./internal/adapters/claude_code -run 'TestTranscriptForRel|TestTranscriptSessionDir|TestTail|TestFlushDirty|TestHandleEvent|TestScanThenTail' -count=1`,
  `go test -race -count=1 ./internal/adapters/claude_code`, and direct strict
  Lizard on `tailer_loop.go`, `tailer_flush.go`, `tailer_events.go`, and
  `tailer_helpers_test.go` all passed with zero threshold warnings.
- Local Codacy Analysis CLI after the Round 30 follow-up reported 0 issues
  across the 16 changed Claude-code files:
  `/tmp/ai-viewer-sow0047-claude-tailer-parser-codacy-r4.json`.
- Final full local gates passed after Round 31 review convergence:
  `./scripts/gates.sh` completed in 552s with lint/static/security/
  vulnerability checks clean, secrets over 857 tracked files clean, attribution
  scan clean, spec drift clean, build + bundle-size clean, the local benchmark
  regression gate clean, Go race+coverage clean, frontend Vitest coverage
  clean, Go coverage threshold clean, adapter fuzz seed corpus clean, and
  Playwright/axe clean. The run reported Go total coverage 85.6%, gated
  `internal/*` aggregate coverage 90.9%, `internal/adapters/claude_code`
  coverage 86.7%, frontend Vitest with 631 passing tests, frontend E2E/axe
  with 51 passing tests, and main frontend bundle size 132.2 KB gzipped against
  the 500 KB budget.

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

### Round 12 - 2026-06-05

Scope: SOW file and current uncommitted diff for the third SOW-0047 production
slice, `internal/ingest/writer.go` `applyOpFinalized` decomposition and
`internal/ingest/writer_test.go` coverage.

Reviewers:

- `codex`: no blocking correctness, race, security, behavior, or performance
  finding. Findings: harden the new lookup-error test by checking setup errors,
  and record third-slice validation/review evidence in this SOW.
- `mimo`: no blocking correctness, security, race, behavior, or performance
  finding. Findings: preserve concise helper-boundary comments for the pricing,
  timing, dirty-set, and catalog invariants that were previously inline in the
  monolithic function.
- `kimi`: no blocking correctness, security, race, behavior, or performance
  finding. Findings: record the third-slice execution log, add concise helper
  contract comments, and optionally note why the column-rename test forces the
  schema-error path.

Resolution:

- Delegated reviewer fixes for the production/test files: added concise
  helper-boundary comments around orphan-finalize lookup behavior, temporal
  pricing, timing preservation via COALESCE, start-bucket/FTS dirty semantics,
  and computed-cost catalog forwarding.
- Hardened `TestWriter_ApplyOpFinalizedPersistedOpLookupErrorBubbles` so
  `ensureSourceRowDirect`, `db.BeginTx`, and seed `w.apply` setup failures now
  fail the test explicitly.
- Added a one-line test comment documenting the column rename as a deliberate
  schema-error trigger for the persisted-op lookup.
- Recorded third-slice validation evidence above.
- Review remains open until the same broad scope is rerun on the integrated
  Round 12 fixes.

### Round 13 - 2026-06-05

Scope: same broad SOW file and current uncommitted diff review for the third
SOW-0047 production slice, with the Round 12 helper comments, test setup
hardening, and validation ledger included.

Reviewers:

- `codex`: no blocking correctness, race, security, separation-of-concerns,
  unwanted-side-effect, performance, or SOW/spec-drift finding. Verified
  side-effect order, lookup error handling, pricing and timing gates, the new
  setup-error-hardened test, and SOW validation evidence.
- `glm`: no blocking correctness, race, security, behavior, or performance
  finding. Findings: a pre-existing sibling lookup-error test still ignored
  setup errors, and Round 12's "review remains open" note needed to be closed by
  a follow-up review entry before merge.
- `mimo`: no blocking finding. Verified SQL/bind ordering, side-effect order,
  schema-error test behavior, focused tests, and catalog/dirty semantics. Noted
  only non-blocking comment-density tradeoff observations.
- `qwen`: no blocking finding. Verified functional equivalence, security,
  performance, test coverage, SOW/spec hygiene, and unaffected code paths. Noted
  only non-blocking comment-density and package-local-type observations.

Resolution:

- Accepted the helper comment-density observations as non-blocking: Round 12
  restored the important helper-boundary invariants, while detailed behavior
  remains in `.agents/sow/specs/ingester.md` and focused tests.
- Delegated a test-only follow-up for the sibling
  `TestWriter_ApplyOpFinalizedLookupNonErrNoRowsBubbles` setup path. The test
  now fails explicitly on `ensureSourceRowDirect`, seed `BeginTx`, seed
  `w.apply`, seed `Commit`, second `BeginTx`, and rollback failures while
  preserving the intended closed-transaction assertion.
- Recorded the post-follow-up validation evidence above.
- Review remains open until the same broad scope is rerun on the integrated
  Round 13 fixes.

### Round 14 - 2026-06-05

Scope: same broad SOW file and current uncommitted diff review for the third
SOW-0047 production slice, with the Round 13 sibling-test hardening included.

Reviewers:

- `codex`: no blocking production correctness, security, race, performance, or
  spec-drift finding. Finding: the comment above
  `TestWriter_ApplyOpFinalizedLookupNonErrNoRowsBubbles` still described an
  outdated persisted-op lookup path, while the test actually proves
  closed-transaction error propagation.
- `glm`: no actionable finding. Verified behavior-preserving decomposition,
  side-effect order, SQL/bind equivalence, security, race safety, performance,
  and SOW hygiene.
- `mimo`: no actionable finding. Verified side-effect order, branch
  equivalence, security, race safety, performance, tests, and SOW/spec hygiene.
  Noted only non-blocking comment-density and SOW citation-style observations.
- `qwen`: no blocking correctness, security, race, performance, or
  separation-of-concerns finding. Findings: add direct focused coverage for
  `resolveFinalizedOpTiming` guard cases and the nil-pricer branch in
  `resolveFinalizedOpCost`.

Resolution:

- Delegated a test-only follow-up in `internal/ingest/writer_test.go`.
- Corrected the stale closed-transaction test comment so it no longer claims to
  reach the persisted-op lookup or references old source line numbers.
- Added `TestResolveFinalizedOpTiming`, a table-driven unit test covering zero
  end timestamp, invalid start timestamp, non-positive start timestamp,
  end-before-start clock skew, and the valid duration case.
- Added `TestResolveFinalizedOpCostNilPricerReturnsEventCost`, proving the
  nil-pricer guard returns before transaction/pricer use when the op would
  otherwise be priceable.
- Recorded the focused validation evidence above.
- Review remains open until the same broad scope is rerun on the integrated
  Round 14 fixes.

### Round 15 - 2026-06-05

Scope: same broad SOW file and current uncommitted diff review for the third
SOW-0047 production slice, with the Round 14 comment/test fixes, staticcheck
fix, final gate evidence, and transient unrelated tailer-test investigation
included.

Reviewers:

- `codex`: no findings. Verified side-effect order, SQL/bind equivalence,
  orphan-finalize lookup handling, Round 14 timing and nil-pricer tests, and
  SOW validation/review ledger consistency.
- `glm`: no blocking correctness, security, race, performance, behavior-change,
  or SOW/spec finding. Noted only non-actionable observations: the nil
  transaction in the nil-pricer helper test is safe because the guard returns
  before transaction use, and the transient `aiagent_v3` tailer-test entry is
  correctly documented.
- `mimo`: no blocking correctness, security, race, performance, behavior, test
  coverage, separation-of-concerns, or SOW/spec finding. Noted only
  non-actionable comment-density and equivalent-guard observations.
- `qwen`: no correctness, security, race, performance, behavior, coverage, or
  SOW/spec finding. Noted only non-actionable test-style observations about
  nested `t.Parallel()` and the pure helper being package-level.

Resolution:

- Accepted the nil-transaction test observation as non-actionable: the test is
  deliberately proving the nil-pricer early return before transaction/pricer
  use, and production has a single caller that always passes a real transaction.
- Accepted the test-style observations as non-actionable: the current table test
  is valid, lint-clean, and focused on the helper guard conditions.
- External review converged with no actionable findings remaining.

### Round 16 - 2026-06-05

Scope: same broad SOW file and current PR #52 diff for the third SOW-0047
production slice, with the Codacy Cloud test-complexity finding and test-only
helper extraction included.

Reviewers:

- `codex`: no blocking correctness, race, security, performance,
  separation-of-concerns, or test-coverage finding. Verified the full branch
  diff against merge base, side-effect order, lookup error handling, SQL/bind
  equivalence, dirty/catalog sequence, and that the latest test helper
  extraction only moved setup.
- `glm`: no blocking finding. Verified the test body keeps the lookup-error
  assertion, the helper setup order remains source row -> committed session ->
  schema break -> fresh transaction -> finalize assertion, and the SOW evidence
  is accurate.
- `qwen`: no blocking finding. Verified the test-only extraction keeps the same
  DB state and event payloads, preserves assertions, keeps the ALTER TABLE
  schema error isolated to the test-local database, and leaves the sibling
  closed-transaction test alone.
- `mimo`: no blocking finding. Verified focused tests and also ran
  `go test -race -count=1 ./internal/ingest` and `go vet ./internal/ingest`,
  both clean. Confirmed no production code changed in the follow-up and no
  unwanted side effects.

Resolution:

- No code changes required from Round 16.
- External review converged again with no actionable findings remaining.

### Round 17 - 2026-06-05

Scope: same broad SOW file and current uncommitted fourth-slice catalog diff:
`internal/ingest/catalog.go`, untracked `internal/ingest/catalog_alias_test.go`,
and this SOW.

Reviewers:

- `codex`: no blocking production correctness, race, security, SQL-order, or
  performance finding. Findings: the new alias test file was still untracked,
  and the SOW needed a fourth-slice review entry before merge.
- `glm`: no blocking correctness, security, race, performance, or spec-drift
  finding. Findings: restore concise `ctx_max` why-comments near the model
  finalization SQL; noted the package-level tool-start helper asymmetry as
  non-blocking.
- `qwen`: no blocking correctness, race, security, performance, behavior, or
  SOW/spec finding. Findings: add direct tool-namespace correction migration
  coverage; optionally scalarize helper parameters instead of passing a whole
  finalize event where only scalar fields are needed.
- `mimo`: no blocking correctness, race, security, performance, SQL/bind,
  or SOW finding. Findings: scalarize `updateFinalizedTool` to take `endTs`
  instead of the full event, and assert that the old provider-alias row still
  exists with `call_count=0` after migration.

Resolution:

- Accepted the untracked-file warning as an integration requirement; the final
  staging step must add both `internal/ingest/catalog_alias_test.go` and
  `internal/ingest/catalog_finalize.go` explicitly.
- Split finalized helper structs/functions into
  `internal/ingest/catalog_finalize.go`, reducing `internal/ingest/catalog.go`
  from 413 to 244 physical lines and keeping both production files under the
  400-line guideline.
- Changed `updateFinalizedTool` to accept `endTs int64`, matching the scalar
  dependency style used by provider/model update helpers.
- Restored a concise `ctx_max` comment at the model finalization SQL site:
  pricing metadata seeds a floor; adapter observations only raise the value.
- Hardened `TestCatalog_ProviderAliasCorrectionMigratesContribution` so it
  asserts the old provider-alias row exists and has `call_count=0`.
- Added `TestCatalog_ToolNamespaceCorrectionMigratesContribution`, proving a
  tool re-emit migrates call/failure/token/cost/duration totals from
  `builtin` to the corrected non-empty namespace exactly once.
- Review remains open until the same broad reviewer scope is rerun on the
  integrated Round 17 fixes.

### Round 18 - 2026-06-05

Scope: same broad SOW file and current uncommitted fourth-slice catalog diff:
`internal/ingest/catalog.go`, `internal/ingest/catalog_finalize.go`,
`internal/ingest/catalog_alias_test.go`, and this SOW, with the Round 17 fixes
integrated.

Reviewers:

- `codex`: no blocking catalog correctness, race, security, SQL-order, or
  performance finding. Finding: the SOW staging reminder named only
  `internal/ingest/catalog_alias_test.go` even though
  `internal/ingest/catalog_finalize.go` is also new and required by
  `catalog.go`.
- `glm`: no blocking correctness, security, race, performance, behavior-change,
  error-handling, or SOW/spec finding. Noted only the accepted package-level
  tool-helper asymmetry as non-blocking.
- `mimo`: no blocking correctness, race, security, SQL-order, performance, or
  SOW finding. Finding: add an explicit old `builtin/read_file`
  `call_count=0` assertion to mirror the provider-alias old-row assertion.
- `qwen`: no blocking issue after re-tracing its own NULL-duration concern
  through `writer.go`'s `COALESCE` duration persistence. Findings: add focused
  coverage for completed-to-failed LLM status correction and the provider-only
  LLM catalog path with no model row.

Resolution:

- Updated the Round 17 staging note so final staging explicitly includes both
  new files: `internal/ingest/catalog_alias_test.go` and
  `internal/ingest/catalog_finalize.go`.
- Hardened `TestCatalog_ToolNamespaceCorrectionMigratesContribution` with a
  direct old `builtin/read_file` `call_count=0` assertion.
- Added `TestCatalog_StatusCorrectionLLMDeltaOnce`, proving a
  completed-to-failed LLM re-finalize increments `failure_count` exactly once
  without double-counting calls, tokens, cost, or duration.
- Added `TestCatalog_ProviderWithoutModelBooksProviderOnly`, proving an LLM op
  with provider and no model books provider totals while leaving
  `catalog_models` empty for that provider.
- Review remains open until the same broad reviewer scope is rerun on the
  integrated Round 18 test/SOW hardening.

### Round 19 - 2026-06-06

Scope: same broad SOW file and staged fourth-slice catalog diff:
`internal/ingest/catalog.go`, `internal/ingest/catalog_finalize.go`,
`internal/ingest/catalog_alias_test.go`, this SOW, and exact staging evidence.

Reviewers:

- `codex`: no blocking runtime correctness, security, SQL-ordering, race,
  call-count, alias, duration, cache-token, cost, or `ctx_max` finding.
  Finding: `.agents/sow/specs/data-model.md` had stale `ctx_max` code
  references to old `catalog.go` line ranges after the finalize split.
- `glm`: no blocking correctness, security, race, performance, behavior,
  error-handling, or SOW/spec finding. Verified all catalog tests, package
  validation, lint, Lizard, and unchanged writer call sites.
- `qwen`: no blocking correctness, security, race, performance, SQL-ordering,
  SOW, or spec-drift finding. Noted only non-blocking future test/comment
  polish: tool re-finalize changed-value coverage, non-`ErrNoRows`
  `readFinalizedCatalogRow` error-path coverage, and a comment explaining the
  package-level tool-helper asymmetry.
- `mimo`: no blocking correctness, race, security, SQL-order, performance,
  SOW, or spec-drift finding. Verified the Round 18 test hardening and staged
  file set.

Resolution:

- Accepted the `data-model.md` stale-reference finding as real durable-memory
  drift. Updated the `ctx_max` code references to stable function/file
  references: `catalogWriter.ctxMaxSeed` / `upsertStartedModel` in
  `internal/ingest/catalog.go`, and `updateFinalizedModel` in
  `internal/ingest/catalog_finalize.go`.
- Accepted qwen's tool re-finalize and error-path coverage notes as
  non-blocking for this behavior-preserving complexity slice: the shared delta
  mechanism is already covered by LLM status-correction tests, identity
  migration tests, and existing catalog tests. No production behavior or
  unresolved SOW scope changes are required.
- Accepted qwen's tool-helper asymmetry comment note as non-blocking:
  package-level tool helpers are deliberately receiver-free because they do not
  use pricer or `ctx_max` state, and the SOW records that rationale.
- Review remains open until quick checks and full gates pass after the
  `data-model.md` spec reference fix.

### Round 20 - 2026-06-06

Scope: same broad SOW/spec/staged-code scope after the `data-model.md`
reference fix: this SOW, `.agents/sow/specs/data-model.md`,
`internal/ingest/catalog.go`, `internal/ingest/catalog_finalize.go`, and
`internal/ingest/catalog_alias_test.go`.

Reviewers:

- `codex`: no runtime correctness, SQL-ordering, transaction, race, security,
  alias, cache-token, cost, duration, or `ctx_max` bug found. Findings: final
  full-gate evidence still needed before merge, and the `onOpStarted`
  `call_count` comment was stale after accepting identity-change migration as a
  call-count movement case.
- `glm`: no blocking correctness, security, race, performance, behavior,
  error-handling, or SOW/spec finding. Noted only non-blocking style
  observations about package-level tool helpers and small struct/value-helper
  polish.
- `qwen`: no blocking correctness, security, performance, SQL-ordering,
  SOW/spec, or unwanted-side-effect finding. Noted only non-blocking future
  polish around package-level tool helper naming and SOW length.
- `mimo`: no blocking correctness, race, security, SQL-ordering, performance,
  SOW, or spec-drift finding. Verified the `data-model.md` stable-reference
  fix and the focused catalog validation.

Resolution:

- Accepted the stale `onOpStarted` comment finding as real maintainability
  drift. Updated the comment to state that `call_count` moves for genuine
  inserts and identity-change migrations, while plain re-emitted `OpStarted`
  updates remain idempotent.
- Accepted the full-gate evidence finding as a completion-gate reminder, not a
  code defect. Full `./scripts/gates.sh` remains required on the final staged
  state before this SOW can be marked complete.
- Accepted the remaining package-level tool helper, struct/value-helper, and
  SOW-length notes as non-blocking. They do not change the behavior-preserving
  complexity objective and do not justify another production refactor in this
  slice.
- External review converged with no actionable findings remaining before the
  final full-gate run.

### Round 21 - 2026-06-06

Scope: broad staged fifth-slice Claude-code benchmark-baseline diff: this SOW,
`.agents/skills/project-quality-gates/SKILL.md`, benchmark inventory specs,
`.github/workflows/ci.yml`, `bench/baseline.txt`,
`internal/adapters/claude_code/bench_test.go`,
`internal/adapters/claude_code/bench_types_test.go`, and
`scripts/check-bench.sh`.

Reviewers:

- `codex`: no code-level blocker. Found stale runtime-skill wording in
  `.agents/skills/project-quality-gates/SKILL.md`: it still described 5
  performance-critical benchmark paths while the spec, CI, script, and baseline
  had moved to 7 benchmark functions.
- `glm`: no benchmark correctness, race, security, determinism, gate-wiring, or
  baseline-refresh blocker. Found the same stale skill wording. Verified the
  scan benchmark drainer goroutine is consumed correctly, the tail cursor copy
  is not mutating the seed cursor in place, and the baseline has 7 names with 6
  samples each.
- `qwen`: no blocking finding. Flagged two low-severity benchmark polish items:
  the tail benchmark's `SetBytes` included seed bytes even though the timed
  read starts at the append offset, and the synthetic assistant usage shape did
  not include the production `server_tool_use` / `service_tier` fields.

Resolution:

- Accepted the stale skill wording as real durable-memory drift and updated the
  runtime quality-gates skill to list 7 performance-critical benchmark paths:
  ai-agent v2 Scan/Tail, claude-code Scan/Tail, SQLite batch insert, REST query,
  and SSE fanout.
- Accepted the tail `SetBytes` finding as real metric inaccuracy and changed
  `BenchmarkClaudeTail_SyntheticAppend` to report append bytes only.
- Accepted the assistant-usage shape parity finding and added deterministic
  synthetic `server_tool_use` plus `service_tier` fields to benchmark assistant
  records.
- Refreshed `bench/baseline.txt` again for all seven benchmarks after the
  review fixes, because both Tail byte accounting and assistant usage shape
  affect benchmark output and allocation metrics.
- Review continued in Round 22 with the same broad reviewer scope.

### Round 22 - 2026-06-06

Scope: same broad staged fifth-slice Claude-code benchmark-baseline diff after
the Round 21 fixes: this SOW, `.agents/skills/project-quality-gates/SKILL.md`,
benchmark inventory specs, `.github/workflows/ci.yml`, `bench/baseline.txt`,
`internal/adapters/claude_code/bench_test.go`,
`internal/adapters/claude_code/bench_types_test.go`, and
`scripts/check-bench.sh`.

Reviewers:

- `codex`: found one real benchmark-guard weakness. The Scan benchmark used
  default adapter options, so adapter `OnError` reports were no-ops; Tail prime
  and measured flush also passed no-op error callbacks; and event counts were
  only reported as metrics rather than asserted. Also flagged Claude benchmark
  sample variance as a low/medium confidence risk for the >20% regression gate.
- `glm`: no actionable finding. Reported the staged diff clean across benchmark
  correctness, determinism, race/leak, security, maintainability, SOW/spec/skill
  consistency, and baseline-refresh authorization.
- `qwen`: invocation exited without a final review report and was not counted.

Resolution:

- Accepted the benchmark-guard weakness as real. Added benchmark error
  recorders so Claude Scan, Tail cursor priming, and measured Tail flush fail
  when adapter parse/map errors are reported through callbacks.
- Added exact deterministic event-count assertions: full Claude Scan must emit
  2058 canonical events per corpus scan, and measured Claude Tail append flush
  must emit 6 canonical events per iteration.
- Refreshed all seven benchmark baseline samples again because the new
  assertions and callback checks changed the timed benchmark body.
- Treated the sample-variance warning as a real gate-risk to verify. Standalone
  `-count=10` Claude Scan and Tail runs were stable; one immediate
  `scripts/check-bench.sh` run after repeated benchmark work failed on Claude
  Scan, and the subsequent uncontended full benchmark gate passed. Final full
  gates remain required before commit.
- Review continued in Round 23 with the same broad reviewer scope.

### Round 23 - 2026-06-06

Scope: same broad staged fifth-slice Claude-code benchmark-baseline diff after
the Round 22 fixes: this SOW, `.agents/skills/project-quality-gates/SKILL.md`,
benchmark inventory specs, `.github/workflows/ci.yml`, `bench/baseline.txt`,
`internal/adapters/claude_code/bench_test.go`,
`internal/adapters/claude_code/bench_types_test.go`, and
`scripts/check-bench.sh`.

Reviewers:

- `codex`: found real durable-memory drift and benchmark metric issues.
  `.agents/skills/project-testing/SKILL.md` still omitted
  `internal/adapters/claude_code/bench_test.go`; `bench/README.md` still
  described 5 benchmarks / 4 packages; Claude Tail `SetBytes` was append-only
  even though `readTranscript` replays the whole file from offset 0 to rebuild
  mapper state; and this SOW still recorded stale benchmark file line counts.
- `glm`: no actionable finding. Reported the staged diff clean across benchmark
  correctness, determinism, race/leak, security, maintainability, SOW/spec/skill
  consistency, and baseline-refresh authorization.
- `qwen`: no blocking finding. Flagged two low-severity benchmark accuracy and
  cleanup issues: Scan `totalBytes` did not include `.meta.json` sidecar bytes,
  and `runClaudeBenchScan` could leave its drainer goroutine blocked on the
  `a.Scan` error path before `b.Fatalf`.

Resolution:

- Accepted the durable-memory drift as real and updated
  `.agents/skills/project-testing/SKILL.md` plus `bench/README.md` to describe
  the 7-benchmark / 5-package suite and the Claude-code benchmark file.
- Accepted the Tail byte-accounting finding as real. Tail now reports full
  parsed file bytes (`seedBody + appendBody`) because the measured flush path
  replays from offset 0 and only gates emission by cursor offset.
- Accepted the Scan byte-accounting finding as real. The synthetic corpus byte
  count now includes `.meta.json` sidecar bytes because Scan reads them while
  building subagent metadata maps.
- Accepted the Scan error-path cleanup. `runClaudeBenchScan` now closes the
  output channel and waits for the drainer before failing on `a.Scan` errors.
- Updated stale SOW line-count evidence to the current staged files:
  `bench_test.go` 402 lines and `bench_types_test.go` 68 lines.
- Refreshed all seven benchmark baseline samples again because the byte
  accounting changed reported throughput metrics.
- Review remains open until the same broad reviewer scope is rerun on the Round
  23 fixes. The next review scope includes the newly changed
  `.agents/skills/project-testing/SKILL.md` and `bench/README.md`.

### Round 24 - 2026-06-06

Scope: same broad staged fifth-slice Claude-code benchmark-baseline diff after
the Round 23 fixes, including the newly changed
`.agents/skills/project-testing/SKILL.md` and `bench/README.md`.

Reviewers:

- `codex`: no code-level blocker. Verified event counts, CI benchmark-smoke
  coverage, and the 7-benchmark / 6-sample baseline shape. Found stale SOW
  line-count evidence: staged `bench_test.go` has 402 lines while two SOW
  references still said 399.
- `glm`: no benchmark correctness, determinism, race/leak, security,
  maintainability, SOW/spec/skill consistency, gate-wiring, or
  baseline-refresh blocker. Found the same stale SOW line-count evidence.
- `qwen`: no actionable finding. Verified benchmark correctness,
  determinism, channel/goroutine hygiene, gate logic, CI benchmark count,
  documentation consistency, baseline shape, sensitive-data handling, and the
  non-goal that production scanner/tailer/parser code is unchanged.

Resolution:

- Accepted the SOW evidence drift as real durable-memory drift and updated the
  two stale line-count references from 399 to 402 for
  `internal/adapters/claude_code/bench_test.go`.
- No production, test, script, workflow, spec, skill, or benchmark-baseline
  change was needed from Round 24.
- Review remains open until the same broad reviewer scope is rerun on the Round
  24 SOW-only fix.

### Round 25 - 2026-06-06

Scope: same broad staged fifth-slice Claude-code benchmark-baseline diff after
the Round 24 SOW-only line-count fix.

Reviewers:

- `codex`: no actionable finding. Verified the benchmark fixtures are synthetic
  and isolated under `b.TempDir()`, adapter-reported errors fail benchmarks,
  Scan and Tail event counts are asserted, Scan drains its output goroutine on
  error, gate wiring includes claude-code, the baseline has 7 names with 6
  samples each, and no production Claude-code scanner/tailer/parser/adapter
  behavior is staged. Also ran focused checks including the local benchmark
  gate, the Claude-code race test, benchmark self-test, and secret scan.
- `glm`: no actionable finding. Verified functional correctness, security,
  code quality, error-prone patterns, performance, test quality, architecture,
  documentation consistency, SOW line-count evidence, and baseline provenance.
- `qwen`: no actionable finding. Verified benchmark correctness,
  determinism, tail deferral state behavior, gate logic, CI count guard,
  sensitive-data handling, documentation consistency, baseline shape, and the
  non-goal that production scanner/tailer/parser code is unchanged.

Resolution:

- External review converged. No production, test, script, workflow, spec,
  skill, SOW, or benchmark-baseline fix was required from Round 25.
- Residual non-blocking risks are documented: workstation benchmark variance,
  Tail `SetBytes` assumes the two fixed append variants remain equal length,
  and a future Tail benchmark fixture with subagents would need to reset or
  deliberately exercise `tailDeferral` state per iteration.
- Final full local gates passed before commit; see Validation.

### Round 26 - 2026-06-06

Scope: broad staged sixth-slice Claude-code scanner decomposition diff: this
SOW, `.agents/sow/specs/adapter-claude-code.md`, and the split scanner files.

Reviewers:

- `codex`: found a real performance/test gap in orphan-root timestamp probing:
  the first implementation could scan a whole child transcript to EOF when the
  first parseable timestamp appeared early.
- `glm`: found the same real issue and classified it as a medium performance
  regression risk for large orphan child files.
- `qwen`: found low-severity missing coverage for orphan roots whose child
  transcripts are empty or all oversized.
- `kimi`: found real `readOneLine` extraction risks: non-`ErrBufferFull`
  reader errors could be misclassified as `errLineTooLong`, and the direct line
  reader tests did not pin the error ordering.

Resolution:

- Changed orphan timestamp probing to stop at the first parseable timestamp in
  each append-only child transcript after skipping malformed, oversized,
  known-no-op, timestampless, and unparsable records.
- Added zero-result orphan-root tests for all-oversized, empty, and
  timestampless child transcripts.
- Changed `readOneLine` error ordering so non-`ErrBufferFull` reader errors
  propagate instead of becoming oversized-line errors.
- Added direct `readOneLine` tests for oversized complete lines, oversized
  partial EOF, and underlying reader errors.
- Review remained open until the same broad scope was rerun on the integrated
  fixes.

### Round 27 - 2026-06-06

Scope: same broad staged sixth-slice Claude-code scanner decomposition diff
after the Round 26 fixes.

Reviewers:

- `codex`: found two real remaining issues. First, the oversized partial EOF
  test used a too-large reader buffer and did not exercise the production 64
  KiB `streamLines` path; in production, an in-flight oversized line without a
  trailing newline could still be skipped instead of parked. Second,
  orphan-root timestamp probing did not reject non-positive parsed timestamps,
  while the spec now says the synthetic root uses the minimum positive
  timestamp.
- `glm`: no actionable finding after the Round 26 fixes. Verified behavior
  preservation, zero Lizard warnings, race-clean package tests, and the
  intentional orphan timestamp spec update.
- `kimi`: no blocking finding. Verified pointer receivers on the hot
  `lineStreamer` path and value receivers only on immutable `transcriptReader`
  methods. Noted a low maintainability suggestion to expand the inline
  Agent-op deferral comment.
- `qwen`: invocation produced no final review report before the converged
  reviewer set was available and was not counted.

Resolution:

- Changed oversized partial EOF handling so a line that exceeds
  `scanBufferMax` but reaches EOF before newline returns `io.EOF` with consumed
  `0`, parking the cursor until the producer appends a newline. Newline-
  terminated oversized lines still return `errLineTooLong` and advance past the
  newline.
- Updated `TestReadOneLineOversizedPartialEOFHoldsBack` to use the production
  64 KiB reader path and a body larger than `scanBufferMax`.
- Changed orphan timestamp parsing to reject parsed timestamps `<= 0` and
  added a non-positive timestamp zero-fallback case.
- Expanded the `recordChildCompletion` invariant comment to document ADD,
  RETRACT, and replay no-op cases because this SOW is about maintainability.
- Review remained open until the same broad scope was rerun on the integrated
  fixes.

### Round 28 - 2026-06-06

Scope: same broad staged sixth-slice Claude-code scanner decomposition diff
after the Round 27 fixes and the Agent-op deferral comment expansion.

Reviewers:

- `codex`: no actionable finding. Verified scanner replay, Tail's shared
  `readTranscript` path, oversized-line behavior, orphan-root timestamp
  semantics, symlink containment, meta size caps, staged diff hygiene, and
  staged Go formatting.
- `glm`: no actionable finding. Verified the decomposition, the intentional
  oversized partial EOF behavior, orphan-root timestamp filtering, Tail
  side-effect boundaries, race-clean focused tests, and `85.7%` package
  coverage in its read-only run.
- `kimi`: no actionable finding. Noted only low-risk observations: value
  receivers on immutable `transcriptReader` methods and a redundant defensive
  `len(line) == 0` guard.
- `mimo`: no blocking finding. Flagged advisory maintainability notes around
  defensive line-reader branches, the redundant zero-length guard, and the
  pre-existing `metaHashes(root, resolvedRoot, ...)` unused `root` parameter.

Resolution:

- Accepted `kimi`'s value-receiver and zero-length-guard notes as
  non-actionable: the hot `lineStreamer` path uses pointer receivers, and the
  defensive guard preserves the old reader shape without runtime cost.
- Accepted `mimo`'s line-reader branch note as non-actionable after manual
  control-flow review: the branch preserves old semantics for completed
  oversized lines when a future caller uses a larger buffer, while production
  64 KiB callers still take the `ErrBufferFull` drain path.
- Accepted the `metaHashes` signature note as non-actionable in this slice:
  the unused `root` parameter and explanatory comment pre-existed the split,
  and changing it would touch Tail-adjacent call sites for style rather than
  fixing a scanner decomposition defect.
- External review converged with no actionable findings remaining.
- Final full local gates and local Codacy file analysis passed after review
  convergence; see Validation.

### Round 29 - 2026-06-06

Scope: broad staged seventh-slice Claude-code tailer/parser decomposition diff:
this SOW plus the staged Claude-code tailer/parser production and test files.

Reviewers:

- `codex`: no actionable finding. Verified the focused tests, package tests,
  race test with an isolated cache, benchmark smoke, secret scan, spec drift,
  attribution scan, and strict Lizard. Found no correctness, race, security,
  sensitive-data, SOW/spec drift, or unwanted-side-effect issue.
- `glm`: no blocking finding. Reported only low/info observations about
  root-level transcript path classification, meta repair events using
  `SourceSeq: 0` / `Ts: 0`, and a test callback using `t.Fatalf`; classified
  them as pre-existing or intentional and recommended no code action.
- `qwen`: no blocker. Verified package tests, race test, vet/build, benchmark
  smoke, and the staged SOW evidence. Flagged one low maintainability issue:
  `transcriptSessionDir` used nested `append`, which could mutate the backing
  array behind the `projParts` subslice when spare capacity exists.
- `kimi`: no blocker. Verified focused tests, race test, benchmark gate, strict
  Lizard, staged diff checks, secrets, attribution, spec drift, build, and vet.
  Flagged a non-blocking benchmark observation: Claude-code Tail `allocs/op`
  increased from 152 to 170 while `sec/op` remained within the benchmark gate.
  Also noted the pre-existing parser raw-copy behavior and the same
  `transcriptSessionDir` allocation shape.

Resolution:

- Accepted the `transcriptSessionDir` slice-aliasing finding as real
  maintainability debt and fixed it immediately. The helper now allocates a
  fresh slice, appends `root`, appends `projParts...`, appends `sessionID`, and
  then joins.
- Accepted the `allocs/op` observation as non-blocking and not a follow-up SOW:
  this project gate intentionally enforces statistically significant `sec/op`
  regressions, the seventh-slice full gates and reviewer benchmark run both
  passed, and no production symptom or capacity risk was demonstrated. The
  benchmark output remains durable evidence in Validation for future profiling
  if Tail allocation pressure becomes a measured problem.
- Accepted the parser raw-copy note as non-actionable because it preserves the
  old defensive copy semantics for records that retain raw JSON; optimizing it
  for skipped types would be a behavior-neutral micro-optimization without
  benchmark justification.
- Review remains open until the same broad reviewer scope is rerun after the
  `transcriptSessionDir` fix.

### Round 30 - 2026-06-06

Scope: same broad staged seventh-slice Claude-code tailer/parser decomposition
diff after the Round 29 `transcriptSessionDir` fix and SOW review record.

Reviewers:

- `codex`: no findings. Verified the staged diff, old/new tailer and parser
  shapes, no TODO/FIXME/nolint/#nosec additions, and the SOW's Round 29 record.
- `glm`: no blocking finding. Verified the `transcriptSessionDir` fresh-slice
  fix, focused tests, race test, vet, and file reads. Noted pre-existing or
  intentional low-risk behavior around remove/rename event swallowing,
  value-copy `tailFlush`, and the documented ignored `root` parameter.
- `kimi`: no actionable finding. Verified build, package tests, race test,
  focused tests, benchmark smoke, fuzz seeds, vet, strict Lizard, secret scan,
  spec drift, and staged diff hygiene. Noted a theoretical watcher leak only if
  a panic occurs inside panic-free startup helpers; classified it as
  non-blocking.
- `qwen`: no blocker. Verified package tests, race test, vet, focused tests,
  and that an unrelated `internal/ingest` race timeout reproduces on the base
  commit. Raised three low/medium code-clarity/test recommendations:
  `tailDirtySets.mark` used a value receiver while mutating maps,
  `tailFlush` used value receivers while mutating `*Cursor`, and the
  `transcriptSessionDir` fix had only indirect coverage.

Resolution:

- Accepted the dirty-set receiver clarity finding and changed the mutating
  `tailDirtySets.mark` method to a pointer receiver.
- Accepted the flush receiver clarity finding and changed the flush object API
  to use pointer receivers. The first implementation used a pointer type alias;
  that was simplified before acceptance to the idiomatic shape: concrete
  `tailFlush` struct, `newTailFlush(...) *tailFlush`, and `*tailFlush` method
  receivers.
- Accepted the direct-test finding and added
  `TestTranscriptSessionDirDoesNotMutateProjectParts`, which passes a
  spare-capacity subslice and asserts the caller backing array is not mutated.
- Classified the theoretical panic-path watcher leak as non-actionable because
  the relevant startup helpers return errors rather than panic, normal
  `prepareRoot` failure still closes the watcher, and panics are process-level
  defects outside Tail's recover contract.
- Review remains open until the same broad reviewer scope is rerun after these
  Round 30 fixes.

### Round 31 - 2026-06-06

Scope: same broad staged seventh-slice Claude-code tailer/parser decomposition
diff after the Round 30 receiver/test fixes and SOW review record.

Reviewers:

- `codex`: no blocking finding. Performed staged-diff review and reported no
  correctness, race, path traversal, parser-safety, sensitive-data, or SOW/spec
  drift issue. Noted it did not run tests because the review prompt was
  read-only.
- `glm`: no blocking finding. Verified package tests, race test, vet, build,
  gofmt, benchmark smoke, parser tests, SOW evidence, and the Round 30 fixes.
  Reported only low-severity observations about internal parameter shape and
  confirmed no significant code smell.
- `qwen`: no blocking finding. Verified build, vet, package race test, focused
  scanner/tailer tests, and the prior fix notes. Reported only low-severity
  observations: `tailDirtySets` is still passed by value around reference-type
  maps, `metaRepair` appropriately uses value receivers, and a focused subagent
  transcript path unit test could be added later but is already integration
  covered.
- `kimi`: no blocking finding. Verified package tests, race test, benchmark
  smoke, strict Lizard, benchmark gate, vet, gofmt, compile, fuzz targets,
  secret scan, and spec drift. Reported only non-blocking info: Tail
  `allocs/op` increased while `sec/op` remains green, and the pre-existing
  unused `metaHashes` `root` parameter remains out of scope for this slice.

Resolution:

- External review converged with no actionable finding remaining.
- Classified the remaining low/info notes as non-actionable for this slice:
  `tailDirtySets` value copies still share map backing storage and are covered
  by direct event tests; `metaRepair` value receivers are correct because the
  struct itself is immutable during repair; subagent path reconstruction is
  exercised by Tail/flush integration tests; Tail allocation changes remain
  below the enforced `sec/op` regression gate; and the old `metaHashes` `root`
  parameter is unrelated scanner-slice debt already accepted as out of scope in
  Round 28.

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
