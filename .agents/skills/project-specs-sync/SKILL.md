---
name: project-specs-sync
description: Keep ai-viewer specs synchronized with code. Use whenever a code change touches runtime behavior, defaults, schemas, contracts, file layouts, or external interfaces.
---

# Specs Sync

## Why

Specs under `.agents/sow/specs/` are the **assistant's durable memory** of what ai-viewer does. The operator does not read specs. The assistant writes specs for itself — for the next session, the next compaction, the next reviewer. When code drifts from specs, the assistant is silently betraying future-self: future SOWs base plans on a spec that no longer matches reality.

## Core Rule: Specs Lead

**Specs change first, before tests, before code.** This is the inverse of the common "document at the end" pattern. The reasoning: a spec that describes the *target* behavior turns into the executable contract — tests are written against the spec, code makes the tests pass against the spec. If the spec lags, the implementation has nothing to be judged against.

The order on any non-trivial change:

1. Spec update lands first (in the SOW Pre-Implementation Gate's `Spec Deltas` section, then in the actual spec file).
2. Failing tests are written against the new spec.
3. Implementation makes the tests pass.
4. All four (spec + tests + code + docs) ship in a single commit.

Drift between spec and code is a regression by definition.

## When Specs Must Update

| Code change | Spec(s) to update |
|---|---|
| Adapter parsing change | `adapter-<name>.md` |
| Canonical event type change | `canonical-events.md` |
| SQLite schema change | `data-model.md` |
| REST endpoint change | `rest-api.md` |
| SSE event type change | `sse-protocol.md` |
| New UI route or page | `ui-pages.md` |
| Frontend state pattern change | `frontend-architecture.md` |
| Ingester behavior change | `ingester.md` |
| Server behavior change | `presenter.md` |
| New default value | wherever it's documented |
| Bind/port/path default change | `deployment.md` |
| Security-relevant change | `security.md` |
| New external dependency | the package's spec + AGENTS.md tech-stack table |
| Test strategy change | `testing-strategy.md` |
| Health/logging change | `observability.md` |

If the relevant spec doesn't exist: **create it in the same commit**. If multiple specs are affected: update all.

## When Specs Must NOT Update

- Refactors with no external behavior change (purely internal organization).
- Lint/format fixes.
- Adding tests without changing surface.
- Comment-only changes.

## Process

When opening any SOW that involves a code change:

1. The Pre-Implementation Gate **must** contain a `## Spec Deltas` heading listing every spec the SOW will touch, with a short diff description per spec (what changes, what stays).
2. The implementation plan in the SOW has the spec update as **the first chunk**, not the last.

Before any test or any line of code:

1. Apply the spec delta. The spec now describes the target behavior.
2. Re-read the spec end to end to confirm internal consistency.

During implementation:

1. Specs are immutable for the duration of the chunk — if implementation reveals the spec is wrong, pause, update the spec, then resume tests/code.
2. Use spec citations (file path + line) in code comments only when the code embodies a non-obvious spec decision.

Before marking SOW completed:

1. Read every spec listed in `Spec Deltas`.
2. Confirm it reflects the **final** code (the spec may have iterated during implementation).
3. Refresh examples that became stale.
4. Run `scripts/spec-drift.sh` (the automated detector — see "Spec Drift Detection" below) and do a manual audit of any spec it does not cover.
5. Bump dated notes only where the spec explicitly versions itself.

## Spec Drift Detection

`scripts/spec-drift.sh` (SOW-0013) is the automated detector. It is grep/awk-based, **fail-closed** (exits non-zero naming the offending indicator + token), runs in the CI `gates` job, and ships with a hermetic self-test (`scripts/test/spec-drift-test.sh`). It lints five spec↔code indicators against the **actual** code/spec locations:

| Indicator | Code surface | Spec |
|---|---|---|
| REST endpoints | `mux.HandleFunc("/api/…", p.<handler>)` in `internal/presenter/presenter.go` plus the handler's `r.Method` guard in the presenter package | `### <VERB> /api/…` in `rest-api.md`; compared as `<VERB> <normalized-path>` with Phase-2/"not registered" sections exempt spec→code |
| SSE event types | `eventPayload` `case "<kind>"` + control frames in `internal/presenter/events_sse.go` (+ `subscription_filter.go`) | `### \`<type>\`` in `sse-protocol.md` (`resync` = §Reconnect control frame) |
| SQLite columns | `internal/store/migrations/*.sql` (`CREATE TABLE`/`CREATE VIRTUAL TABLE fts5`/`ALTER … ADD COLUMN`) | `data-model.md` (column dir = code→spec; table names bidirectional) |
| Canonical event kinds | `EvXxx EventKind = "<value>"` in `internal/canonical/events.go` | identical fenced block in `canonical-events.md` (bidirectional, exact) |
| Adapter discovery probes | `format: "<name>"` structs in `cmd/ai-viewer-ingest/sources.go` | `adapter-<name>.md` exists + names the probe path (underscore→hyphen) |

`scripts/spec-drift.sh` covers the **structural** indicators above; it does NOT prove prose accuracy. So a manual audit still applies for everything else:

Periodic audit during retrospection:

- Pick a spec at random.
- Read it.
- Read the corresponding code.
- Note any divergence as a new SOW under `pending/`.

## What Goes In A Spec

- Current behavior (not aspirational).
- Invariants and their rationale.
- Edge cases the design accounts for.
- Pointer to the code (file path; line numbers where stable).
- Pointer to upstream evidence for foreign-format specs.

## What Does NOT Go In A Spec

- TODO lists (those belong in SOWs).
- "Future work" sections beyond a brief pointer (use SOWs).
- Raw sensitive data (see AGENTS.md sensitive-data section).
- Long code snippets duplicating the implementation (cite paths instead).
- Marketing language ("blazingly fast", "robust", "industry-leading").

## Spec Tone

Direct, factual, declarative. Bullet points over prose. Tables for structured comparisons. Code blocks for shapes and schemas. The reader should be able to use the spec without reading any other doc.

## On Conflict Between Spec and Code

If you discover divergence:

1. Determine which is right.
2. If the spec is right (e.g. the code accidentally regressed): fix the code, add a test that pins the behavior the spec describes.
3. If the code is right and the divergence is small: update spec to match in the next commit, note the drift in the commit message.
4. If the code is right and the divergence reflects a real undocumented decision: open a SOW to capture the rationale and update the spec — drift this large suggests a decision was made without being documented.
5. Either way, record the discrepancy in the active SOW (or create a new one). Drift is never silently absorbed.

## Cross-References

- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Coding: `.agents/skills/project-coding/SKILL.md`
- Gates (includes spec-drift check): `.agents/skills/project-quality-gates/SKILL.md`
