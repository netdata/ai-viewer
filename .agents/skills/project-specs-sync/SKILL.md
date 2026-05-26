---
name: project-specs-sync
description: Keep ai-viewer specs synchronized with code. Use whenever a code change touches runtime behavior, defaults, schemas, contracts, file layouts, or external interfaces.
---

# Specs Sync

## Why

Specs under `.agents/sow/specs/` are the **durable memory** of what ai-viewer does. The user does not have to repeat decisions because specs preserve them. When code drifts from specs, the system silently betrays itself: future SOWs base plans on a spec that no longer matches reality.

**Rule**: every code change affecting behavior updates the relevant spec **in the same commit**. No exceptions.

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

Before opening any SOW that involves code change:

1. List specs the SOW will touch (in the SOW's `## Artifact Impact Plan`).
2. Plan the spec update as part of the implementation chunks, not as a final cleanup step.

During implementation:

1. Update specs as you change code, not after.
2. Use spec citations (file path + line) in code comments only when the code embodies a non-obvious spec decision.

Before marking SOW completed:

1. Read every spec listed in the Artifact Impact Plan.
2. Confirm it reflects the new code.
3. Refresh any examples that became stale.
4. Bump version/dated notes only where the spec explicitly versions itself.

## Spec Drift Detection (manual until automated)

Periodic audit during retrospection:

- Pick a spec at random.
- Read it.
- Read the corresponding code.
- Note any divergence as a new SOW under `pending/`.

Future work (Phase 2+): a `scripts/spec-drift.sh` that lints common drift indicators (e.g. spec mentions a field, code does not; spec lists endpoints, server registers a different set).

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

1. Determine which is right (usually code; specs do drift).
2. If code is right: update spec to match in the next commit, note the drift in the commit message.
3. If spec is right (e.g. the code accidentally regressed): fix the code, add a test that pins the behavior the spec describes.
4. Either way, record the discrepancy in the active SOW (or create a new one).
