# SOW-0047 - Codacy Complexity Backlog Reduction

## Status

Status: open

Sub-state: pending; created by SOW-0046 complexity triage and not started.

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

Status: blocked

Problem / root-cause model:

- Production complexity exists in adapter parsers, tailers, ingest loops, and a
  few frontend visualization/state helpers. The root cause is accumulated
  state-machine logic and scanner-driven false-positive cleanup, not one single
  architectural defect.

Evidence reviewed:

- SOW-0046 final Codacy result summary:
  - production: 195 findings, 84 files
  - tests: 406 findings, 143 files
  - scripts/tooling: 7 findings, 2 files
  - docs/specs: 0 findings

Affected contracts and surfaces:

- Adapter parsing and tailing behavior.
- Ingest ordering, deduplication, and SQLite write behavior.
- Frontend trace/topology/stat visualization helpers.
- Quality-gate and Codacy maintainability reporting.

Existing patterns to reuse:

- Spec -> test -> code workflow.
- Adapter fixture, fuzz, and benchmark coverage.
- Focused frontend unit tests plus Playwright coverage for UI-visible behavior.

Risk and blast radius:

- Medium to high for adapter and ingest hotspots.
- Low for isolated frontend helper simplifications with existing tests.
- No schema or external API change is expected.

Sensitive data handling plan:

- Codacy exports stay in `/tmp`. Durable SOW/spec artifacts record aggregate
  counts, sanitized file paths, and rationale only.

Implementation plan:

1. Rank production complexity hotspots by runtime risk, finding count, fixture
   coverage, and benchmark coverage.
2. Update relevant specs for selected refactors before tests.
3. Add or strengthen tests/benchmarks for each selected hotspot.
4. Delegate production refactors in small, reviewable slices.
5. Re-run local Codacy, full gates, and external reviewers until converged.

Validation plan:

- `scripts/test/codacy-config-test.sh`
- Targeted Go/frontend tests for each touched hotspot.
- Adapter fuzz seed corpus for touched adapter packages.
- Benchmarks for ingest/parser hot paths touched.
- Full `./scripts/gates.sh`.
- External second-opinion review with at least three reviewers.

Artifact impact plan:

- AGENTS.md: likely unaffected unless a new permanent workflow rule emerges.
- Runtime project skills: update `project-quality-gates` if complexity policy
  changes.
- Specs: update adapter, ingest, frontend, and quality-gate specs as touched.
- End-user/operator docs: likely unaffected unless setup/quality reporting
  changes.
- End-user/operator skills: likely unaffected.
- SOW lifecycle: remains pending until SOW-0046 is complete and this SOW is
  selected for implementation.

Open-source reference evidence:

- Not checked yet. Complexity-reduction work is local maintainability work; if
  adapter parser structure changes, relevant upstream examples should be
  reviewed before implementation.

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

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
