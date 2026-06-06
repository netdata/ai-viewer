# SOW-0052 - Residual Adapter Complexity Reduction

## Status

Status: open

Sub-state: pending from SOW-0047 closeout. Not active yet.

## Requirements

### Purpose

Reduce or justify remaining adapter complexity not covered by the Codex and
Opencode follow-up SOWs, while preserving source-format behavior.

### User Request

Continue maintainability cleanup autonomously, SOW by SOW, with tests,
benchmarks, security checks, and external review before completion.

### Assistant Understanding

Facts:

- SOW-0047 closeout left residual strict source-only warnings in ai-agent v2,
  ai-agent v3, and Claude-code adapter paths after the main high-risk mapper
  and Claude-code scanner/tailer/parser/ops slices were merged.
- Residual examples include ai-agent v2 cursor/scanner/tailer/helper command
  warnings, ai-agent v3 cursor/mapper/parser/scanner/tailer warnings, and
  Claude-code cursor/mapper warnings.
- Codex and Opencode residuals are intentionally tracked separately by
  SOW-0048 and SOW-0049 because they are larger adapter-specific clusters.

Inferences:

- Some residual adapter warnings may be deliberate state-machine density or
  helper-command complexity. They still need explicit triage so they are not
  hidden by SOW-0047 closeout.
- Parser/scanner/tailer residuals remain higher risk than cursor or helper
  command warnings because they process untrusted source bytes.

Unknowns:

- Which residual adapter warnings are worth refactoring versus documenting as
  intentional must be decided package by package after reading tests and specs.

### Acceptance Criteria

- Residual ai-agent v2, ai-agent v3, and Claude-code adapter warnings are
  ranked with file/function evidence.
- Parser, scanner, and tailer changes have focused characterization tests and
  benchmark evidence where the path is performance-sensitive.
- Cursor and helper-command warnings are either simplified safely or explicitly
  justified.
- Any remaining residual adapter warnings are tracked by narrower follow-ups or
  justified in this SOW.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0047 closeout warning-only Lizard scan.

Current state:

- The largest pre-existing adapter clusters were reduced first, but smaller
  ai-agent v2/v3 and Claude-code warnings remain outside the Codex/Opencode
  follow-up SOWs.

Risks:

- Parser/scanner/tailer regressions can hide malformed input, drop records,
  duplicate events, or corrupt cursor state.
- Helper-command changes can affect benchmark/fixture generation and therefore
  test reliability.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Residual adapter complexity is distributed across smaller functions and
  helper commands rather than one dominant hotspot. It needs a triage-first
  SOW so each warning is either fixed with coverage or intentionally deferred.

Evidence reviewed:

- SOW-0047 closeout strict warning buckets and external closeout review.

Affected contracts and surfaces:

- ai-agent v2 adapter cursor, scanner, tailer, and helper commands.
- ai-agent v3 adapter cursor, parser, mapper, scanner, and tailer.
- Claude-code adapter cursor and residual mapper dispatch.
- Adapter benchmarks and fixtures if helper commands or hot paths are touched.

Existing patterns to reuse:

- SOW-0047 adapter decomposition, characterization, race-test, benchmark, and
  external-review workflow.
- `.agents/skills/project-adapters/SKILL.md` adapter modification checklist.

Risk and blast radius:

- Medium to high within selected adapters; no REST, SSE, SQLite schema, or
  frontend behavior change is expected.

Sensitive data handling plan:

- Use synthetic or committed sanitized fixtures only. Do not add raw prompts,
  tool output, source IDs, session IDs, private paths, secrets, or personal data
  to durable artifacts.

Implementation plan:

1. Audit residual ai-agent v2, ai-agent v3, and Claude-code warning functions.
2. Rank by parser/tailer/scanner risk first, then cursor/helper-command risk.
3. Add characterization tests before any production refactor.
4. Refactor selected warnings in small package-local slices.
5. Validate with focused tests, package race tests, direct strict Lizard, local
   Codacy, benchmark gate where applicable, full gates, and external review.

Validation plan:

- Focused adapter tests selected after coverage audit.
- `go test ./internal/adapters/aiagent_v2 -count=1`
- `go test ./internal/adapters/aiagent_v3 -count=1`
- `go test ./internal/adapters/claude_code -count=1`
- Race tests for each touched adapter package.
- Direct strict Lizard on changed files.
- Local Codacy analysis on changed files.
- `scripts/check-bench.sh` when hot paths or helper benchmark commands change.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: affected adapter specs only if behavior/contracts change.
- Runtime project skills: likely unaffected unless a new adapter-decomposition
  convention emerges.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- No new source-format claim is made yet. If implementation changes an adapter's
  interpretation of source data, inspect upstream source or mirrored
  repositories first and cite upstream repository identity plus commit.

Open decisions:

- None for the operator.

## Outcome

Pending.
