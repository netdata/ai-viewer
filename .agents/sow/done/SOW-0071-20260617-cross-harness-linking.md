# SOW-0071 — Cross-harness session linking (heuristic)

## Status

Status: open (follow-up to SOW-0070)

## Requirements

### Purpose

Link sessions across different harnesses when one harness spawns another via a shell tool (e.g. claude-code running `codex` via Bash, or opencode running `claude` via Bash). These links are NOT deterministic in the source data — the harnesses don't know about each other. This SOW detects heuristic links and surfaces them as "possibly related."

### Scope

- Detect heuristic links: same cwd, overlapping timestamps, one session's tool_use content mentioning another harness's name/CLI.
- Surface as soft links (not parent-child edges) in the session detail and topology.
- The operator can see "this claude-code session spawned a codex session via Bash" even though neither source records the link.

### Acceptance Criteria

1. Heuristic cross-harness links detected and surfaced. **Verification**: a claude-code session that ran `codex` via Bash shows a "possibly related" link to the codex session.
2. Soft links are visually distinct from deterministic parent-child edges. **Verification**: different color/style.

## Status

Status: completed (moving to done/). Both ACs met. 6-reviewer loop converged
round 2: 6/6 PRODUCTION GRADE (avg ~9.5).

## Pre-Implementation Gate

### Problem / root-cause model

Different AI harnesses don't record parent-child relationships when one spawns
another via a shell tool (e.g. claude-code running `codex` via Bash). The
operator sees two unrelated sessions and can't tell one spawned the other. No
deterministic link exists in the source data; this SOW detects HEURISTIC links.

### Evidence reviewed (self-review, file:line)

Available signals for heuristic detection:
- **Signal #1 (same cwd)**: `sessions.cwd` exists with an index
  (`idx_sessions_cwd`, 0001_initial.sql:39,66). Two sessions from different
  harnesses in the same working directory are related.
- **Signal #2 (overlapping timestamps)**: `sessions.start_ts` + `end_ts`. A
  candidate that started WHILE the current session was running (started between
  its start_ts and end_ts) is a spawn signal.
- **Signal #3 (tool_use command mentioning another harness)**: NOT available
  without a schema migration. The tool command text (e.g.
  `{"command":"codex ..."}`) lives only in `payload_refs` byte ranges
  (source-file previews), NOT in a DB column. Adapters capture the tool NAME
  (e.g. "Bash") but not the arguments. `extras_json` for tool ops stores
  exit_code/duration/cwd — not the command string. `fts_ops` indexes
  name/model/provider/tool_namespace/error_text — NOT the command text.

### Design (CTO decision)

Implement with signals #1 + #2 (same cwd + started-during-overlap + different
source_format). This is a defensible heuristic: if a session from a different
harness started in the same working directory while the current session was
running, it very likely was spawned by it (or is a close sibling). Signal #3
would strengthen this but requires a schema migration + adapter changes across
all 5 formats to capture tool command text into a queryable column — a separate
SOW's worth of work, not needed for a defensible "possibly related" link.

New endpoint **`GET /api/sessions/:id/related`**: bounded query on indexed
columns (idx_sessions_cwd + start_ts) finding sessions with the same cwd,
different source_format, that started during the current session's run. Returns
soft links with a human-readable reason.

Frontend: a "Possibly related" section in the Overview tab, visually distinct
from the child-sessions tree (dashed border — AC2), with a note explaining
these are heuristic links (not deterministic parent-child edges).

### Affected contracts and surfaces

- `internal/presenter/session_related.go` (NEW) — the endpoint + query.
- `internal/presenter/presenter.go` — register `/api/sessions/{id}/related`.
- `frontend/src/api/sessions.ts` — `useSessionRelated(id)` hook.
- `frontend/src/api/types.ts` — `RelatedSession` + `RelatedResponse`.
- `frontend/src/pages/SessionDetail/OverviewTab/OverviewTab.tsx` — the "Possibly
  related" section.
- `.agents/sow/specs/rest-api.md` — `GET /api/sessions/:id/related`.
- `.agents/sow/specs/ui-pages.md` — the soft-link section.

### Spec deltas before any code

1. `rest-api.md` — new `### GET /api/sessions/:id/related` (heuristic cross-
   harness links: same cwd + started during + different source_format).
2. `ui-pages.md` §/sessions/:id — a "Possibly related" soft-link section.

### Risk and blast radius

Low. New endpoint (additive); no schema change (uses existing indexed columns);
bounded query (LIMIT 10). The heuristic is honest about its limitations (a
`reason` field explains the match; the UI labels it "possibly related," not
"parent/child").

### Implementation plan

- **A. Specs** — rest-api.md + ui-pages.md.
- **B. Backend** — `session_related.go` (endpoint + query + reason text).
- **C. Frontend** — type + hook + OverviewTab section.
- **D. Tests** — backend: the query finds a different-harness session in the
  same cwd that started during; does NOT find same-harness or non-overlapping;
  frontend: the section renders with dashed styling.
- **E. Gates** — full local gate aggregate.

### Open decisions

None blocking (CTO): signals #1+#2 only (signal #3 filed as a future
enhancement if the heuristic proves too noisy); LIMIT 10; "possibly related"
labeling throughout.

## Implementation

Both ACs implemented.

- **Backend** `internal/presenter/session_related.go` (NEW): `GET /api/sessions/:id
  /related` — finds sessions with the same cwd, different source_format, whose
  start_ts falls within the current session's `[start_ts, COALESCE(end_ts, now)]`
  window. Single query on indexed columns (idx_sessions_cwd + start_ts), LIMIT 10.
  Returns each with a human-readable `reason` field.
- **Frontend types** `api/types.ts`: `RelatedSession` + `RelatedResponse`.
- **Frontend hook** `api/sessions.ts`: `useSessionRelated(id)`.
- **Frontend** `OverviewTab.tsx`: a "Possibly related" section with a DASHED
  border (AC2: visually distinct from the solid-border child-sessions tree) +
  a hint explaining these are heuristic soft links. Each row links to the
  related session's detail.
- **Specs**: `rest-api.md` new §GET /api/sessions/:id/related; `ui-pages.md` the
  soft-link section.
- **Seed helper**: `sessionRow` gains a `cwd` field + `seedSessionWithCwd` helper
  (existing tests unaffected; cwd defaults to NULL).

## Validation

- `session_related_test.go` — finds a cross-harness link (same cwd + overlap +
  different format); excludes same-harness; excludes non-overlapping; 404;
  empty; 405.
- `OverviewTab.test.tsx` — the section renders with dashed styling when links
  exist; absent when empty.
- Gates: go test -race PASS (presenter), coverage PASS, 679 frontend tests
  PASS, tsc + eslint clean, spec-drift PASS, lint clean.

## Reviews

### Round 1 (6 reviewers)

Scores: glm 9.5 PG, mimo 9 PG, kimi 9.5 PG, qwen 9 PG, minimax 9 NEEDS WORK,
deepseek 9 NEEDS WORK. 4/6 PG. The two holdouts raised the same points, fixed:
- **FIXED (P2) error handling** (minimax/deepseek) — the related query error
  was silently swallowed (Hard Rule #6). Added `console.error` on isError (the
  section's absence-on-error is the right behavior — a heuristic enhancement
  must not break the Overview — but the error is now surfaced).
- **FIXED (P2) NULL-cwd test** — SQL `NULL = NULL` is `UNKNOWN` so NULL-cwd
  sessions don't match (the conservative choice). Pinned by a new test.
- **FIXED (P2) running-session test** (glm/deepseek) — the `COALESCE(end_ts,
  now)` path for a still-running session. Pinned by a new test.

Round 2 confirms.

### Round 2 — CONVERGED (6/6 PRODUCTION GRADE)

Scores: glm 9.5, minimax ~9.5, mimo 9.5, kimi 9.5, qwen 9, deepseek 10. No
P0/P1/P2. All three round-1 fixes verified correct + pinned by tests.

## Outcome

**Status: completed — both ACs met, 6/6 PG, all gates green.** AC1 (heuristic
cross-harness detection via same-cwd + overlap + different-format) + AC2
(visually distinct dashed-border soft links) delivered. Error handling + NULL-cwd
+ running-session edge cases pinned by tests.

## Outcome

(filled on completion)
