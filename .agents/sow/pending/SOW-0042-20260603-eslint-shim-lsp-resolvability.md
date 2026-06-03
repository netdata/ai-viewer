# SOW-0042 - Make the jsx-a11y ambient-shim resolvable by the editor LSP (match tsc)

## Status

Status: open

Sub-state: filed 2026-06-03 as a tracked follow-up of SOW-0012 (frontend quality stack). The ambient `declare module` shim added in SOW-0012 Chunk B resolves under the `tsc` gate but the standalone editor TS LSP flags it (TS7016/TS2307). Filed as a pending SOW (not left only in SOW-0012 `## Followup`) per AGENTS.md "tech debt is paid or filed in pending/".

## Requirements

### Purpose

Remove the editor-only TypeScript diagnostic noise on the ESLint config so contributors see the same green state `tsc`/CI see — a developer-experience + signal-hygiene fix, not a gate change (the gate is already green).

### User Request

Implicit follow-up created during SOW-0012 review convergence (operator's standing backlog mandate). No new operator request. Recorded so the known LSP-vs-tsc divergence is not lost.

### Assistant Understanding

Facts:

- `eslint-plugin-jsx-a11y` ships no type declarations, so SOW-0012 added an ambient `declare module 'eslint-plugin-jsx-a11y'` shim at `frontend/src/types/eslint-plugin-jsx-a11y.d.ts`.
- `npm run typecheck` (`tsc -p frontend/tsconfig.json`) RESOLVES the shim and exits 0 — `frontend/tsconfig.json` `include` covers `src` (hence `src/types`) + `eslint.config.ts`. The gate is GREEN.
- The standalone editor TS language server, opening `eslint.config.ts` outside the full project program, does NOT pick up the ambient shim and reports TS7016 (no declaration file) / TS2307 — pure editor noise; it does not affect the gate, the build, or CI.

Inferences:

- The standard remedies are one of: a `/// <reference types="…" />` (or `/// <reference path="…" />`) directive in `eslint.config.ts` pointing at the shim; relocating the shim to a top-level `types/` dir the LSP picks up; or a `typeRoots`/`types` tsconfig tweak. The right choice is the smallest one that makes the editor match `tsc` without weakening the gate.

Unknowns:

- Which remedy the installed TS/editor versions honor most reliably for a flat-config `.ts` file opened standalone; to be verified during implementation (the SOW-0012 Lessons note this is LSP-resolution-specific).

### Acceptance Criteria

1. **Editor LSP resolves the shim.** Opening `frontend/eslint.config.ts` in the editor no longer reports TS7016/TS2307 for `eslint-plugin-jsx-a11y` (or the chosen remedy is documented as the canonical one). **Verification**: the LSP diagnostic is gone after the change (manual editor check) AND `npm run typecheck` still exits 0.
2. **No gate weakening.** `tsc`, `eslint`, and `scripts/lint.sh` stay green; no `tsconfig` strictness or lint coverage is reduced. **Verification**: `bash scripts/lint.sh` exit 0.
3. **Docs updated.** The SOW-0012 `## Followup` / project-frontend skill note about LSP-resolvability is updated to "resolved". **Verification**: spec/skill diff in the same commit.

## Analysis

Sources checked:

- `frontend/src/types/eslint-plugin-jsx-a11y.d.ts`, `frontend/eslint.config.ts`, `frontend/tsconfig.json`, SOW-0012 Lessons + `## Followup`.

Current state:

- Gate green; editor-only diagnostic. Cosmetic/DX, not correctness.

Risks:

- Very low. The change is a reference directive / file relocation / tsconfig `types` tweak; risk is only that a chosen remedy doesn't fully satisfy the LSP — mitigated by verifying in-editor before closing.

## Pre-Implementation Gate

Status: blocked

(Filled when approved + moved to `current/`. Pick the smallest remedy that makes the editor match `tsc`; verify in-editor; do not weaken the gate.)

## Implications And Decisions

None yet (no open decisions; pending operator prioritization).

## Plan

1. Choose + apply the minimal remedy (reference directive vs `types/` relocation vs tsconfig `types`); verify the editor LSP no longer flags it and `tsc` stays green.
2. Update the SOW-0012 Followup / skill note to resolved. Quality gates + external review; PR; self-merge.

## Execution Log

(none yet)

## Validation

(filled at close)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
