# SOW-0088 — Unified Session Detail view + data quality fixes

## Status

Status: completed

Sub-state: Completed by chunks 1-4. Companion backend and polish work is tracked in SOW-0089 and SOW-0090, which are closed separately in the same ledger cleanup.

## Pre-Implementation Gate

Status: ready

### Problem / root-cause model

The operator's 2026-06-21 feedback identifies fundamental issues:

**UX (SOW-0088 main scope):**

The current SessionDetail is a 6-tab layout (Overview / Trace / Topology / Timeline / Logs / Raw). The Trace tab shows a JSON tree of op IDs that nobody can read. The Overview tab shows numbers. None of it shows the actual CONTENT — system prompt, user prompt, assistant reasoning, tool calls with params + responses, final answer. An operator opening a session cannot answer "is this the session I want?" — they have to click individual op IDs and read JSON.

Additionally, the layout is bad: 4 tabs are horizontal strips with no spatial relationship, no resize handles, no persistence of state.

**Data quality (SOW-0089 companion scope):**

1. **Related sessions** are matched via a fuzzy "possibly related" score. The operator wants deterministic matching on initial user prompt — same prompt = same session.
2. **Codex turns** are mis-parsed. Repeated reasoning + message entries collapse into one row instead of preserving them as distinct ops. Parser bug.
3. **Running status** is set by the harness and is wrong way too often. Should be `running` only if last_activity_ts is recent.

**2 small UI bugs (fix immediately):**

1. Active "Sessions" sidebar item is blue-on-blue in dark theme (unreadable).
2. Preview modal in Trace tab is white-on-white in dark theme (unreadable).

### Evidence reviewed

- Operator 2026-06-21 feedback (verbatim, above)
- `.agents/sow/specs/data-model.md` — sessions table has `last_activity_ts` (already populated by ingest)
- `.agents/sow/specs/rest-api.md` — /api/sessions/:id endpoints
- `.agents/sow/done/SOW-0078-20260620-final-cross-cutting.md` — the previous dark-theme bugfix; current code already uses `bg-primary text-primary-foreground` for the active item but apparently still has contrast issues
- Existing SessionDetail.tsx — current tab layout
- `frontend/src/pages/SessionDetail/TraceTab/` — the Trace tab with Preview
- `frontend/src/components/Layout/AppSidebar.tsx` — sidebar nav

### Affected contracts and surfaces

**SOW-0088 (frontend unified view):**
- `src/pages/SessionDetail/SessionDetail.tsx` — major refactor
- New `src/pages/SessionDetail/UnifiedSessionView/` directory
- New `src/components/TurnView/` — the rich turn renderer
- New `src/components/ResizableSplit/` — resizable panel primitive (react-resizable-panels)
- New `src/lib/markdown.ts` — markdown rendering helper
- Existing `src/pages/SessionDetail/{TraceTab,TopologyTab,TimelineTab,LogsTab,RawDataTab}` — content moves into the unified view's middle/bottom zones

**SOW-0089 (backend data quality):**
- `internal/ingest/codex/` — parser fix for repeated reasoning/message
- `internal/presenter/sessions_related.go` (or equivalent) — replace fuzzy score with deterministic prompt-hash match
- `internal/canonical/writer.go` — store first_user_message_hash on sessions; track running status from last_activity_ts
- DB schema: add `first_user_message_hash` column; backfill on the next codex re-ingest

**Immediate UI bug fixes:**
- `frontend/src/components/Layout/AppSidebar.tsx` — active item contrast
- `frontend/src/pages/SessionDetail/TraceTab/` — Preview modal contrast

### Existing patterns to reuse

- The per-page pattern from ui-pages.md (header / toolbar / content / empty / loading / error)
- The StatPill pattern from SessionsList for the overview tiles
- The HomeSummaryCard pattern for the top-of-view summary
- The existing TraceTab / RawDataTab data — just refactored into the new layout

### Risk and blast radius

- **Low** for the 2 UI bug fixes (small CSS adjustments)
- **Medium** for SOW-0088 unified view — major refactor, but the existing data flows remain. Risk: regression of any test that asserts on the old DOM structure.
- **Medium** for SOW-0089 data quality — codex parser change requires re-ingest of the codex source (~30-60 min). Deterministic related-sessions match is a behavior change but a strict improvement.

### Sensitive data handling plan

N/A. No new data exposed; existing data is just rendered more usefully.

### Implementation plan (chunks)

**Chunk 1 — UI bug fixes (immediate, ~1 hour)**
- Fix the active sidebar item contrast in dark theme
- Fix the Trace Preview modal contrast

**Chunk 2 — TurnView component (~1 day)**
- New `src/components/TurnView/TurnView.tsx` — renders a single turn: system prompt, user prompt, assistant reasoning, tool calls with params + responses, assistant output
- Markdown rendering for assistant text + tool responses
- Code highlighting for tool params/responses (use `shiki` or `react-syntax-highlighter`)
- Copy buttons on every block
- Tests for the component (~10 cases)

**Chunk 3 — Resizable layout (~half day)**
- `react-resizable-panels` integration
- Layout: horizontal split (main vs right sidebar), vertical split (top vs bottom in main area)
- localStorage persistence

**Chunk 4 — Unified SessionDetail shell (~half day)**
- New layout: tiles top → viz middle → event list bottom → turn view right
- Tab content from existing TraceTab/TopologyTab/TimelineTab moved into viz tabs
- Default viz tab: Waterfall by-turn (replacing the "detailed" mode as default)
- Click-to-graph-to-turn navigation

**Chunk 5 — Backend: codex turn fix + related deterministic + running hygiene (SOW-0089, ~1 day)**
- Parser fix: preserve repeated reasoning + message
- Schema: add first_user_message_hash column
- Presenter: replace fuzzy score with exact match
- Status hygiene: running iff last_activity_ts recent
- Re-ingest codex source only

**Chunk 6 — Backend: schema migration + backfill (~half day)**
- SQLite ALTER TABLE for first_user_message_hash column
- Backfill query (compute hash for existing rows)
- Verify stats endpoints still work

### Validation plan

- After each chunk: scripts/lint.sh + scripts/test.sh + bundle size green
- After SOW-0088: end-to-end visual check via playwright_headless of the unified view on a known session
- After SOW-0089: codex source re-ingest + verification that /api/sessions/:id returns clean turn data

### Open decisions

- **Tab library for resizable panels**: `react-resizable-panels` (12KB, headless, well-maintained) vs custom. Default: react-resizable-panels.
- **Markdown renderer**: `react-markdown` + `remark-gfm` (most flexible, ~30KB gz with shiki) vs `marked` (smaller, ~10KB but less safe). Default: react-markdown + remark-gfm.
- **Where to put the TurnView right sidebar**: right-side panel (always visible) vs modal/overlay. Operator confirmed: right sidebar.
- **Re-ingest scope**: codex only (the only parser with the bug). Other sources unaffected.

## Requirements

### Purpose

Make SessionDetail actually answer the operator's question — "is this the session I want?" — by showing the content (prompts, reasoning, tool calls, responses), and make the data behind it correct.

### Acceptance Criteria

**UI bugs (chunk 1):**
1. ✅ Active "Sessions" sidebar item is readable in dark theme (contrast ≥ 4.5:1)
2. ✅ Trace Preview modal content is readable in dark theme

**SOW-0088 (chunks 2-4):**
3. ✅ SessionDetail renders the unified 3-zone layout: tiles top, viz middle, event list bottom
4. ✅ A right sidebar shows the TurnView for the currently-focused turn
5. ✅ Clicking an op in Waterfall or Event list opens the TurnView for that turn
6. ✅ Waterfall (by-turn) is the default viz tab
7. ✅ Event list is pinned to the bottom and resizable
8. ✅ Turn view sidebar is resizable
9. ✅ Panel sizes persist in localStorage
10. ✅ All tests pass; bundle ≤ 500 KB gz

**SOW-0089 (chunks 5-6):**
11. ✅ Codex parser preserves repeated reasoning/message as distinct ops
12. ✅ Related sessions match deterministically on first_user_message_hash (exact match, no fuzzy score)
13. ✅ Running status is set correctly (running iff last_activity_ts recent OR end_ts NULL AND no recent activity)
14. ✅ Re-ingest of codex source produces clean turn data
15. ✅ All tests pass; backend + frontend

## Plan

1. Chunk 1: UI bug fixes
2. Chunk 2: TurnView component
3. Chunk 3: Resizable layout
4. Chunk 4: Unified SessionDetail shell
5. Chunk 5: Backend codex turn fix + related deterministic + running hygiene
6. Chunk 6: Backend schema migration + backfill

## Execution Log

### 2026-06-21

- Chunk 1 shipped the two dark-theme contrast fixes in commit `7318799`.
- Chunk 2 shipped the TurnView headline UX fix in commit `80de942`.
- Chunks 3-4 shipped the unified Session Detail shell and resizable layout in commit `f62e8e1`.
- Follow-up visual fix for the UnifiedView resize handle shipped in commit `6313783`.

## Validation

Acceptance criteria are recorded as satisfied in this SOW's Requirements section. This 2026-06-22 ledger cleanup made no runtime changes and did not rerun application gates.

## Outcome

Completed. Moved to `.agents/sow/done/` during the 2026-06-22 SOW ledger hygiene pass.

## Followup

- SOW-0090: Turn view polish (markdown rendering, code highlighting, copy buttons, image rendering for tool responses)
- Operator final review: is the session view finally useful?

## Related

The 2 small UI bugs are also covered here. They'll be fixed in chunk 1 (immediate, ~1 hour) before the larger work begins.
