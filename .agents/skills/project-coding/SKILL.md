---
name: project-coding
description: Apply ai-viewer coding standards for production-quality Go and TypeScript changes. Use for implementation, refactoring, runtime behavior changes, and any change where clean code, separation of concerns, modularity, or maintainability matters.
---

# Coding

Use this skill for code changes in this repository. The assistant is the **CTO
and QA lead** of the codebase and must deliver production-quality, fully tested
work. Meaningful chunks of work pass the applicable external reviewer gates
before they are closed.

## Operating Contract

- **The assistant is CTO.** Never ask the operator technical questions. Research, decide, document the decision in the SOW or spec, proceed.
- **Specs first, tests second, code last.** See `project-workflow`. Order is invariant.
- **The CTO writes implementation directly.** Do not delegate implementation by default. Use helper subagents only for bounded investigation or summarization.
- **Untested ≡ broken.** Manual UI walkthroughs are diagnostics, not proof. Every behavior the project ships has at least one automated test.
- **No silent failures.** Every error path logs structured context; every adapter parse error surfaces in `/api/health`.
- **External reviewers are milestone gates** for meaningful chunks: gap analysis, implementation plan, and implementation. See `project-second-opinions`.
- Investigate before asking. Read SOWs, specs, skills, code, similar local patterns first.
- Surface architecture and design decisions to the operator via SOW before any code is written.
- Once the SOW is approved, work autonomously.
- Parallelize independent helper investigations and local checks when it helps, without turning implementation over to subagents.
- Milestone reports are not stop points. Continue until SOW is delivered, failed with evidence, or blocked on a genuine operator decision.
- Keep communication concise and evidence-based: TL;DR + bullets + file:line + gate results.
- Update specs, project skills, and `AGENTS.md` in the same turn the lesson emerges. "I'll remember" is not valid.

## Design Principles (project-wide)

- **Strong separation of concerns.** Each package has one job (see `architecture.md`). A change in one package should rarely cascade into another.
- **Small files, small functions.** A file > 400 lines or a function > 60 lines is a smell; refactor unless there's a real reason.
- **Explicit domain types over loosely-shaped values.** Use named types and small interfaces; avoid `interface{}` / `any` outside of glue layers.
- **Move non-core behavior out of orchestration loops.** The ingest pipeline and HTTP routing layers must stay thin.
- **Mirror existing patterns** unless there's a concrete reason to diverge. Adding a 6th adapter should look just like adding the 5th.
- **No abstractions for the sake of abstractions.** Add interfaces only when there are real multiple implementations or a real testing need.

## ai-viewer Invariants

These are non-negotiable across all code:

- **Read-only on sources.** No code path writes to a source file or source DB. Open with `os.O_RDONLY` and `?mode=ro`.
- **No outbound network calls.** Ingester and server make zero outgoing HTTP/DNS/network calls. Static pricing tables, not API lookups.
- **Localhost bind by default.** Default bind is `127.0.0.1`. Non-localhost requires a flag added in a security-reviewed SOW.
- **No silent failures.** Every error is logged with structured context. Adapter parse errors surface in `/api/health` and the UI.
- **Idempotent ingest.** Re-scanning the same source produces no duplicate canonical rows.
- **Specs stay in sync with code.** Every change affecting behavior, schemas, defaults, or interfaces updates the relevant spec in the same commit.
- **Bounded buffers everywhere.** No unbounded `append`, no unbounded channel capacities. Every queue has a limit and a documented drop policy.
- **Two-binary separation honored.** `ai-viewer-ingest` does not serve HTTP; `ai-viewer-serve` does not write canonical rows.

## Quality Gates

Before any commit: run the full local aggregate and confirm green (`./scripts/gates.sh`). Use focused commands (`./scripts/lint.sh`, `./scripts/test.sh`, `scripts/spec-drift.sh`, frontend commands, etc.) for diagnosis while iterating, but the commit gate is the aggregate. The full catalog with commands and thresholds lives in `project-quality-gates`. Summary of the non-negotiables:

- All Go lints (golangci-lint, gosec, govulncheck) zero warnings.
- `go test -race -count=1 ./...` passes.
- Coverage thresholds met (≥ 80% statements per gated `internal/*` package + aggregate; new-code ≥ 90% deferred — SOW-0036).
- Fuzz targets run clean for the configured CI duration.
- Benchmarks within 20% of baseline.
- Frontend lint, typecheck, vitest with coverage, Playwright E2E, axe a11y all green.
- Bundle size within budget.
- Secrets scan clean.
- Spec↔code drift audited clean (`scripts/spec-drift.sh` for structural indicators, manual audit for prose).
- `./scripts/build.sh` succeeds.
- Affected specs updated in the same commit.
- Applicable external reviewer gate converged for the current feature/batch/SOW stage.

Weakening a gate to make it pass is a contract breach. Fix the root cause.

## Forbidden Patterns

- `panic()` outside of `main()` initialization.
- `log.Fatalf` outside of `main()`.
- `interface{}` or `any` as a function parameter or return type unless absolutely needed.
- `os.Exit` outside of `main()`.
- Init functions (`func init()`) that do anything beyond registering with a package-level registry.
- Global mutable state. Use struct dependencies passed via constructors.
- `exec.Command` for any user-facing functionality. Pure Go only.
- Skipping tests with `t.Skip()` without a linked GitHub issue and a SOW for removal.
- `// nolint` comments without a reason (per the `project-quality-gates` nolint policy: a permanent, architectural suppression needs a reason explaining why it is correct; a deferred-fix suppression needs an issue/SOW link).
- `_ = err` or empty `if err != nil { }` — every error is either handled or wrapped and returned.
- Treating reviewer claims as facts without verifying them against code/spec/tests.
- Claiming code "works" without automated tests proving it.

## Hot-Path Performance Rules (SOW-0118)

These rules are non-negotiable for code in the ingest pipeline (adapters,
coalescer, writer, resolver) and any path that runs per-event, per-file, or
per-row at scale (325K+ files, 552K+ sessions, 5M+ ops).

- **NEVER use value-receiver methods that clone maps or slices in hot paths.**
  A `func (c Cursor) withFile(...) Cursor { out := c.clone(); ... }` that calls
  `maps.Copy` is O(map_size) PER CALL. At 325K files × 162K average entries,
  that's ~52 billion map copies — the dominant single-core cost. Use pointer
  receivers and mutate in-place: `func (c *Cursor) withFile(...)`. Profile the
  REAL system at scale — fresh-DB benchmarks hide this because empty maps are
  cheap to copy.
- **Cache lookups that are constant within a batch.** Within a 1000-event
  batch, the same session's ops resolve to the same canonical ID. A per-batch
  `sessionIDs map[string]string` (cleared in `resetBatch`) eliminates ~600
  SQL SELECTs per batch. Same for turn existence.
- **Reuse prepared statements per-tx.** modernc/database/sql re-prepares on
  every `ExecContext(ctx, sqlString, args)`. Cache prepared statements per-tx
  via `writer.beginTx(tx)` / `writer.endTx()` + the `txExec` helper. Cuts
  per-event overhead by ~10×.
- **Gate expensive O(n) maintenance queries on a generation counter.** The
  resolver's `linkRoots` recursive CTE is O(sessions) (~30s at 552K rows).
  Running it on every 5s tick creates a busy-loop. Gate it on
  `lastRootsGen`: run only when the generation counter advanced since the last
  pass.
- **Defer read-model maintenance during the initial scan.** Resolver, idle
  rollup refresh, and per-source repair all do expensive DB work that is
  redundant during the initial scan (rebuilt post-scan). Gate them on
  `startupScanActive` (initialized to TRUE so the deferral covers the first
  tick).
- **Batch size matters when per-flush overhead is non-trivial.** With the
  coalescer (begin-wait=0%), batch size 1000 amortizes the progress+notify
  overhead 10× vs 100 (progress_notify dropped from 42% to 5% of flush).
- **NEVER experiment on the live production DB.** Clearing cursors or dropping
  indexes on the running daemon caused a data-loss incident (89 events
  dropped). Use throwaway DB copies for any index/scan experiment.
- **The index-drop lifecycle is safe ONLY with `DropNonUniqueIndexes`** (keeps
  PK + all UNIQUE). The naive `DROP INDEX` breaks upsert ON CONFLICT targets
  and silently drops events.
- **Profile the REAL system, not just benchmarks.** Fresh-DB benchmarks are
  circumstantial — index costs, map sizes, and query plans are all different
  at 31GB scale. Always verify on the real corpus before claiming a fix.

## Cross-References

- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Helper subagents: `.agents/skills/project-delegation/SKILL.md`
- Gates: `.agents/skills/project-quality-gates/SKILL.md`
- Testing: `.agents/skills/project-testing/SKILL.md`
- Reviews: `.agents/skills/project-second-opinions/SKILL.md`
- Specs sync: `.agents/skills/project-specs-sync/SKILL.md`
