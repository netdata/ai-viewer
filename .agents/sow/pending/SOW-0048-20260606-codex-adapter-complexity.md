# SOW-0048 - Codex Adapter Complexity Reduction

## Status

Status: open

Sub-state: pending from SOW-0047 closeout. Not active yet.

## Requirements

### Purpose

Keep the Codex adapter maintainable and safe before it grows more source-format
surface area.

### User Request

Continue reducing code-scanning complexity findings autonomously, SOW by SOW,
without weakening tests, performance gates, or security posture.

### Assistant Understanding

Facts:

- SOW-0047 closeout measured 141 strict source-only Lizard warnings after
  excluding tests and generated embedded frontend assets.
- The Codex adapter is the largest remaining single adapter cluster.
- Current strict warning evidence includes:
  `internal/adapters/codex/tailer.go` with 5 warnings,
  `scanner.go` with 3 warnings, `mapper_turn.go` with 3 warnings,
  `ops_tools.go` with 3 warnings, `stream.go` and `discovery.go` with 2 each,
  and single warnings in `cursor.go`, `mapper.go`, `ops_event.go`,
  `ops_enrich.go`, `ops_response.go`, `parser.go`, and `types.go`.

Inferences:

- The scanner/tailer warnings are high blast-radius because they cover file
  discovery, line streaming, cursor offsets, and real-time updates.
- A Codex adapter benchmark baseline should be added before scanner/tailer
  decomposition, mirroring the Claude-code pattern from SOW-0047.

Unknowns:

- Which Codex warnings are true maintainability defects versus parser/tailer
  state-machine density must be determined by reading the adapter tests and
  current source.

### Acceptance Criteria

- Codex adapter complexity findings are ranked by risk with file/function
  evidence.
- Deterministic Codex `Scan` and `Tail` benchmarks exist before hot-path
  scanner/tailer refactors.
- Behavior-preserving refactors are covered by focused adapter tests, package
  race tests, fuzz seed corpus, benchmark gate, local Codacy/Lizard analysis,
  full gates, and external review convergence.
- Any remaining Codex complexity is justified in this SOW or split into a
  narrower follow-up.

## Analysis

Sources checked:

- SOW-0047 closeout warning-only Lizard scan.
- Existing Codex adapter warning files listed above.

Current state:

- Codex adapter complexity is now the highest adapter-specific residual
  complexity cluster.

Risks:

- Tail/scan regressions can drop or replay source records.
- Parser regressions can hide malformed input or change canonical mapping.
- Benchmark baseline changes can add workstation-gate noise if the benchmark is
  not deterministic.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- The Codex adapter combines source discovery, rollout streaming, cursor
  handling, parser dispatch, and mapper state transitions in a few dense files.
- The correct fix is not a semantic rewrite. It is benchmark-guarded
  decomposition around existing adapter contracts.

Evidence reviewed:

- SOW-0047 closeout strict warning buckets and function list.

Affected contracts and surfaces:

- Codex adapter `Scan`, `Tail`, cursor persistence, parser classification, and
  canonical event mapping.
- Benchmark inventory if Codex benchmarks are added to `scripts/check-bench.sh`.

Existing patterns to reuse:

- Claude-code benchmark prerequisite and decomposition sequence from SOW-0047.
- Adapter spec/test workflow in `.agents/skills/project-adapters/SKILL.md`.

Risk and blast radius:

- High within the Codex adapter. Schema, REST, SSE, and frontend changes are not
  expected.

Sensitive data handling plan:

- Use synthetic or already-sanitized Codex fixtures only. Do not write raw
  source transcripts or prompts to durable artifacts.

Implementation plan:

1. Read the Codex adapter spec, tests, fixtures, and current warning functions.
2. Add deterministic Codex `Scan` and `Tail` benchmarks and wire them into the
   local benchmark gate if the scanner/tailer paths are selected.
3. Add focused characterization tests for any helper-boundary gaps.
4. Decompose the highest-risk Codex scanner/tailer/parser/mapper functions in
   small slices.
5. Run focused tests, package race tests, direct strict Lizard, local Codacy
   analysis, benchmark gate, full gates, and external review.

Validation plan:

- Focused Codex adapter tests chosen after reading existing coverage.
- `go test ./internal/adapters/codex -count=1`
- `go test -race -count=1 ./internal/adapters/codex`
- Direct strict Lizard on changed Codex production and test files.
- Local Codacy analysis on changed files.
- `scripts/check-bench.sh` when benchmarks are added or hot paths change.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: likely `adapter-codex.md`, `quality-gates.md`, and
  `testing-strategy.md` if benchmarks are added.
- Runtime project skills: likely unaffected.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- No new source-format claim is made yet. If implementation changes Codex
  format interpretation, inspect upstream source or mirrored repositories first
  and cite upstream repository identity plus commit.

Open decisions:

- None for the operator.

## Outcome

Pending.
