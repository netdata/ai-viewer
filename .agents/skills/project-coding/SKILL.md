---
name: project-coding
description: Apply ai-viewer coding standards for production-quality Go and TypeScript changes. Use for implementation, refactoring, runtime behavior changes, and any change where clean code, separation of concerns, modularity, or maintainability matters.
---

# Coding

Use this skill for code changes in this repository. The assistant is the maintainer of the whole codebase and must deliver production-quality, fully tested work.

## Operating Contract

- Investigate before asking. Read SOWs, specs, skills, code, and similar local patterns first.
- Surface architecture and design decisions to user via SOW before implementation.
- Once the SOW is approved, work autonomously.
- Prefer delegation and parallelization for non-trivial SOW work.
- Milestone reports are not stop points. Continue until SOW is delivered, failed with evidence, blocked on a real user decision, or superseded by newer instructions.
- Keep communication concise and evidence-based.
- Update specs, project skills, and `AGENTS.md` when behavior, conventions, or repeated workflows change.

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

## Quality Gate

Before any commit:

- `golangci-lint run` clean (zero warnings, zero errors).
- `npm --prefix frontend run lint` clean.
- `go test -race ./...` passes.
- `npm --prefix frontend test` passes.
- `./scripts/build.sh` succeeds (frontend builds + embeds + Go binaries build).
- Affected specs updated.

## Forbidden Patterns

- `panic()` outside of `main()` initialization.
- `log.Fatalf` outside of `main()`.
- `interface{}` or `any` as a function parameter or return type unless absolutely needed.
- `os.Exit` outside of `main()`.
- Init functions (`func init()`) that do anything beyond registering with a package-level registry.
- Global mutable state. Use struct dependencies passed via constructors.
- `exec.Command` for any user-facing functionality. Pure Go only.
- Skipping tests with `t.Skip()` without a referenced GitHub issue link in the comment.
