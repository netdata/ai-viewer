# SOW-0053 - Codex Residual Mapper and Stream Complexity Reduction

## Status

Status: open

Sub-state: pending from SOW-0048 split. Not active yet. This is a drafted
follow-up; before activation, narrow it to the first residual slice and replace
the provisional spec-delta bullets with exact section-level deltas or unchanged
section attestations.

## Requirements

### Purpose

Continue making the Codex adapter maintainable after the high-risk scan/tail
paths were benchmarked and decomposed.

### User Request

Continue reducing code-scanning complexity findings autonomously, SOW by SOW,
without weakening tests, performance gates, or security posture.

### Assistant Understanding

Facts:

- SOW-0048 added deterministic Codex scan/tail benchmarks and removed strict
  source-only Lizard warnings from `scanner.go` and `tailer.go`.
- The remaining Codex production warnings after SOW-0048 scan/tail work are:
  `cursor.go` `After`; `discovery.go` `discoverRollouts` and its walk callback;
  `mapper.go` `mapRecord`; `mapper_turn.go` `applySessionMeta`,
  `sessionExtras`, and `finalizeDanglingOps`; `ops_enrich.go` `enrichMcp`;
  `ops_event.go` `mapEventMsg`; `ops_response.go` `mapResponseItem`;
  `ops_tools.go` `mapToolOutput`, `namespaceForName`, and `outputStatus`;
  `parser.go` `parseLine`; `stream.go` `streamLines` and `readOneLine`; and
  `types.go` `classifySource`.
- These warnings are in mapper/parser/stream/enrichment dispatch, not the
  now-benchmarked scan/tail orchestration paths.

Inferences:

- `stream.go` and `parser.go` remain higher risk than cursor/type helpers
  because they process untrusted source bytes and partial-line boundaries.
- Mapper and ops dispatch is semantically dense; decomposition must preserve
  unknown-variant tolerance, order-independent enrichment, token accounting, and
  canonical event sequencing.

Unknowns:

- Which residual mapper/ops functions should be decomposed versus explicitly
  justified requires reading the focused tests and current branch coverage.

### Acceptance Criteria

- Remaining Codex warnings are ranked by parser/stream/mapper risk with
  file/function evidence.
- Any stream/parser change has focused tests or existing test evidence proving
  malformed input, oversized lines, and partial-line behavior is unchanged.
- Any mapper/ops change has focused tests or existing test evidence proving
  canonical event sequencing, status correction, token accounting, and unknown
  variant handling is unchanged.
- Codex scan/tail benchmarks remain in the benchmark gate and do not regress
  beyond the existing local threshold.
- Any warnings intentionally left in place are justified in this SOW or split to
  narrower follow-ups.

## Analysis

Sources checked:

- SOW-0048 strict source-only Lizard scan after scanner/tailer decomposition.

Current state:

- Codex scan/tail complexity has been reduced and protected by deterministic
  benchmarks.
- Residual Codex complexity is concentrated in mapper dispatch, stream/parser
  boundaries, discovery/cursor/type helpers, and tool-enrichment functions.

Risks:

- Stream/parser regressions can drop partial lines, accept corrupt input, or
  hide malformed source records.
- Mapper/ops regressions can duplicate or lose canonical events, change status
  correction semantics, or misclassify tools.
- Cursor/discovery regressions can replay old rows, miss files, or weaken source
  containment.

## Pre-Implementation Gate

Status: drafted for future activation. Not activation-ready until the first
residual slice has exact spec-delta handling recorded.

Problem / root-cause model:

- Residual Codex complexity is no longer one dominant scanner/tailer hotspot.
  It is distributed across semantic dispatch and helper boundaries. It should be
  handled in smaller slices so tests can prove behavior did not drift.

Evidence reviewed:

- SOW-0048 post-refactor strict Lizard scan.
- Existing Codex golden tests, scanner/tailer tests, parser fuzz seed, and
  benchmark coverage.

Affected contracts and surfaces:

- Codex cursor resume contract.
- Codex discovery source filtering and containment.
- Codex parser and stream line-boundary contract.
- Codex canonical mapper and tool-enrichment contract.
- Codex scan/tail benchmark gate.

Spec deltas to land before tests/code:

- This drafted follow-up is intentionally not activation-ready. The first
  activation edit must narrow the SOW to a concrete slice and replace these
  provisional bullets with exact section-level spec deltas before tests/code.
- If the first slice touches cursor or discovery helpers, record whether
  `.agents/sow/specs/adapter-codex.md` sections `Source Format`, `Watch
  Strategy`, and `Cursor` are unchanged or need edits.
- If the first slice touches stream or parser helpers, record whether
  `.agents/sow/specs/adapter-codex.md` sections `Authoritative Wire Format`,
  `Atomicity & Write Pattern`, `Versioning / Forward Compatibility`, and the
  rule-#24 first-record/session-meta behavior are unchanged or need edits,
  including first-record oversized-line boundedness.
- If the first slice touches mapper, ops, or enrichment helpers, record whether
  `.agents/sow/specs/adapter-codex.md` sections `Mapping to Canonical Events`,
  `Token accounting nuance`, `Known edge cases`, and collaboration/sub-agent
  behavior are unchanged or need edits.
- If benchmark, fuzz, coverage, or static-analysis inventory changes, update
  `.agents/sow/specs/quality-gates.md`, `.agents/sow/specs/testing-strategy.md`,
  and the runtime quality-gate skill before tests/code. If the inventory remains
  unchanged, record that unchanged attestation in the activated SOW.

Existing patterns to reuse:

- SOW-0048 benchmark-first scan/tail decomposition.
- Package-local helper extraction with existing behavior tests kept green.
- `.agents/skills/project-adapters/SKILL.md` adapter workflow.

Risk and blast radius:

- Medium to high within the Codex adapter. No REST, SSE, SQLite schema, or
  frontend changes are expected.

Sensitive data handling plan:

- Use synthetic or committed sanitized fixtures only. Do not write raw prompts,
  tool output, source IDs, session IDs, private paths, secrets, personal data,
  or private endpoints to durable artifacts.

Implementation plan:

1. Rank residual Codex warnings by parser/stream/mapper blast radius.
2. Add characterization tests only where existing coverage does not pin a helper
   boundary.
3. Refactor stream/parser and discovery/cursor/type helpers in small slices.
4. Refactor mapper/ops dispatch only when tests and golden fixtures pin the
   exact canonical event sequence.
5. Validate with focused tests, race tests, parser fuzz seeds, Codex benchmarks,
   direct strict Lizard, local Codacy, full gates, and external review.

Validation plan:

- `internal/adapters/codex/cursor_test.go`: cursor ordering, version parsing,
  legacy exclusion, clone independence, and `After` semantics.
- `internal/adapters/codex/scanner_test.go`,
  `internal/adapters/codex/scanner_branch_test.go`, and
  `internal/adapters/codex/coverage_branch_test.go`: discovery sorting,
  shard-depth filtering, missing/unreadable roots, resume, truncation,
  fail-soft read errors, source progress, stale finalization, unknown-type
  deduplication, oversized-line behavior, symlink containment, and context
  cancellation.
- `internal/adapters/codex/stream_test.go` and
  `internal/adapters/codex/final_branch_test.go`: complete/partial line reads,
  oversized line draining, read and parse error surfacing, context cancellation,
  first-record probing, source-root containment, and helper edge cases.
- `internal/adapters/codex/parser_test.go` and
  `internal/adapters/codex/parser_fuzz_test.go`: malformed JSON, missing or
  unknown top-level and nested types, payload variants, timestamp/raw
  preservation, cursor parsing, and fuzz seed stability.
- `internal/adapters/codex/mapper_test.go`,
  `internal/adapters/codex/mapper_coverage_test.go`,
  `internal/adapters/codex/mapper_helpers_test.go`, and
  `internal/adapters/codex/helpers_unit_test.go`: canonical event sequencing,
  turn boundaries, token rollups, cache accounting, status correction, tool
  pairing, enrichment, orphan/unmatched warnings, source IDs, payload anchors,
  and helper variant coverage.
- `internal/adapters/codex/bench_test.go`: Codex scan/tail benchmark event
  counts, throughput metrics, and heap reporting remain valid after changes.
- Commands: `go test ./internal/adapters/codex -count=1`;
  `go test -race -count=1 ./internal/adapters/codex`;
  `go test -run='^Fuzz' ./internal/adapters/...`;
  `go test ./internal/adapters/codex -run='^$' -bench='BenchmarkCodex' -benchmem -count=1`;
  direct strict Lizard on changed Codex files; `scripts/check-bench.sh` after
  hot-path changes; local Codacy analysis on changed files; full
  `./scripts/gates.sh`; external second-opinion review until convergence.

Artifact impact plan:

- Specs: affected only if runtime contracts or benchmark inventory change.
- Runtime project skills: likely unaffected.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- No new source-format claim is made yet. If implementation changes Codex format
  interpretation, inspect upstream source or mirrored repositories first and
  cite upstream repository identity plus commit.

Open decisions:

- None for the operator.

## Outcome

Pending.
