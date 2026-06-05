# SOW-0047 - Codacy Complexity Backlog Reduction

## Status

Status: open

Sub-state: active 2026-06-05. First production slice selected:
`frontend/src/state/filters.ts`; implementation may proceed only for this slice
under the Pre-Implementation Gate below.

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

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
