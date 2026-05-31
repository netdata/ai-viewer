# SOW-0006 - APM-Style Tracing UI (Milestone 3)

## Status

Status: in-progress

Sub-state: activated 2026-05-31. Approved under the operator's blanket Phase-2 backlog sign-off; prerequisites MET (SOW-0001 in `done/`; SOW-0003/0004/0005 adapters all in `done/` and merged — 5 source formats render cross-format topology/lineage). Operator UX decisions captured below ("Implications And Decisions") before any code, per the decisions-before-work rule. Scope corrected to FULL-STACK (the `/topology` + `/timeline` endpoints are spec'd but unbuilt). Moved to `current/`.

## Requirements

### Purpose

Deliver the three visualization tabs that turn raw canonical rows into an operator-grade APM experience for AI-agent sessions: **Trace** (flame-graph span tree per session), **Topology** (D3 force-directed lineage across sessions), and **Timeline** (zoomable video-editor-style horizontal timeline across the filtered set). These views are what make ai-viewer worth running over `grep` and `jq` against the source files. Without them, the project is a session list; with them, it is an observability tool.

### User Request

From the Phase Mapping in `.agents/sow/specs/ui-pages.md`:

> Phase 3 (Milestone 3): `/sessions/:id` Trace + Topology + Timeline tabs.

And the operator's standing direction in `AGENTS.md`:

> "give the operator a fast, beautiful, low-friction way to see what their AI coding agents have been doing — across time, across formats, across sub-agents."

### Assistant Understanding

Facts:

- The canonical schema already carries everything required: `ops.parent_op_id` (nested span tree), `ops.session_id` and `ops.child_session_id` (parent → sub-agent transitions), `sessions.parent_session_id` / `sessions.root_session_id` (lineage), `ops.start_ts` / `ops.end_ts` / `ops.duration_us` (timeline geometry), `ops.kind` (color/legend), `ops.status` (failure highlighting), `ops.cost_usd` and `ops.tokens_*` (node sizing). See `.agents/sow/specs/data-model.md`.
- REST surface already specifies the three required endpoints: `GET /api/sessions/:id` (turns + ops with `payload_refs`), `GET /api/sessions/:id/topology` (nodes + edges with size-metric selector), `GET /api/sessions/:id/timeline` (lanes + spans, `t_start`/`t_end`). See `.agents/sow/specs/rest-api.md`.
- Compaction events are first-class via `ops.kind='compaction'` with `pre_tokens` / `post_tokens` / `duration_ms` / `trigger` in `extras_json`. The Timeline tab must render them as visible breakpoints. See `.agents/sow/specs/canonical-events.md`.
- D3 is the agreed visualization library and is constrained to `frontend/src/viz/` only per `.agents/sow/specs/frontend-architecture.md` (canvas rendering above the 500-span threshold, Web Worker for force simulation above 100 nodes).
- The frontend already has a working filter bar with URL-synced state, a session list, and a session detail page with Overview + Logs tabs from SOW-0001.

Inferences:

- The cross-session `/topology` route is explicitly Milestone 3 in the phase map, but the per-session Topology tab is delivered earlier. This SOW delivers BOTH per-session and cross-session topology in one block because they share the renderer.
- A flame-graph for 500+ ops in a single session needs careful virtualization; the natural unit is the turn (turns rarely exceed a few hundred ops). Render-per-turn with the turn list virtualized covers the 10K-op case without bespoke virtual scrolling inside the flame chart itself.
- The cross-session timeline at 1K sessions × dozens of ops each will easily exceed the 500-span SVG ceiling. Canvas rendering is mandatory at this scale.
- Force-directed graphs of 100+ nodes are visually noisy when sub-agent fan-out is high (one parent → 20 children all repelling each other). A clustering hint (group children of the same parent in the same initial position) plus an optional "collapse sub-tree" toggle prevents the worst layouts.

Unknowns:

- Real-world distribution of ops per session on the operator's workstation — only measurable after at least one adapter has run a full backfill. The < 200 ms flame-graph target assumes ≤ 500 ops per session; if real data shows a long tail, virtualization strategy may need revisiting.
- Whether D3 force simulation in a Web Worker is straightforward to wire up under Vite's worker-import semantics. The skill `project-frontend` documents the existing pattern, but no force-graph worker has shipped yet.
- Whether the cross-session topology at 1K nodes converges to a stable layout fast enough on the operator's workstation; if not, a fixed `Math.min(iterations, 300)` cap is the fallback.

### Acceptance Criteria

1. **Trace tab renders nested span tree** per session. Each op is one horizontal bar; bar offset = `start_ts - turn.start_ts`; bar width ∝ `duration_us`; bar color = `kind`; failed ops outlined red; nested ops indent one level. Parent → sub-agent transitions (`ops.kind='session'`, `child_session_id IS NOT NULL`) render as a special "→ open sub-agent" affordance that scrolls/expands the child's span tree inline. **Verification**: component test against a fixture with 3 nested levels; visual regression test via Playwright screenshot; flame-graph for one session with 500 ops renders in < 200 ms wall-clock (Playwright `performance.now()` instrumentation).
2. **Topology tab renders force-directed graph** per session AND at `/topology` cross-session. Nodes = sessions (per-session view: ops; cross-session view: sessions). Node color = `status`; node size = configurable metric (`cost|tokens|duration|calls|ctx_pct` via REST `?metric=` param); edges = `parent_op_id` + `child_session_id` + `forked_from_id`. Filter by time range, agent, model, cwd, source format (reuses the global FilterBar). **Verification**: component test on `/api/sessions/:id/topology` fixture; cross-session topology of 100 sessions renders < 500 ms; layout converges (last 5 simulation ticks move every node < 1 px).
3. **Timeline tab renders zoomable horizontal timeline** across all sessions in the current filter. X = time; one lane per session (root + children stacked); spans render colored by op `kind`; compaction ops render as full-height vertical breakpoints; pan via drag, zoom via scroll wheel (shift+wheel zooms time, plain wheel pans, per `ui-pages.md`). **Verification**: component test on `/api/sessions/:id/timeline` fixture; zooming to a 1-hour window across 1K sessions renders < 300 ms (Playwright `performance.now()` instrumentation); the Timeline switches from SVG to Canvas above 500 visible spans (assertion on `<canvas>` element presence).
4. **Span detail drawer**: clicking any span in any of the three views opens a right-side drawer showing the full op row (id, kind, name, model, provider, tokens, cost, duration, status, error) plus a payload preview (first 4 KB via `/api/payloads/:ref`, with a "download full" link). Drawer closes on Esc or clicking outside. **Verification**: E2E Playwright test exercising click → drawer open → payload visible → Esc → drawer closed.
5. **a11y**: axe checks zero serious/critical violations on `/sessions/:id` (Trace/Topology/Timeline tabs) and on `/topology`. Keyboard navigation reaches the span detail drawer and back. Color is never the only signal (status text alongside color per `frontend-architecture.md`).
6. **SSE-driven updates**: when a new op arrives for a session currently displayed, the Trace and Timeline views animate the new span in (200ms fade per `ui-pages.md` realtime rules). **Verification**: Playwright E2E test that writes a new fixture record, observes the SSE `session_changed` event, and asserts the new span appears.
7. **Specs updated**: `.agents/sow/specs/ui-pages.md` and `.agents/sow/specs/rest-api.md` reflect any field additions discovered during implementation. **Verification**: spec drift check (`scripts/spec-drift.sh`) green on the SOW commit.

## Analysis

Sources checked (at SOW drafting):

- `.agents/sow/specs/canonical-events.md` — `OpKind` enum, compaction extras, parent-op chain.
- `.agents/sow/specs/data-model.md` — `ops.parent_op_id`, `ops.child_session_id`, indexes (`idx_ops_session_start`, `idx_ops_compaction`).
- `.agents/sow/specs/rest-api.md` — `/api/sessions/:id`, `/api/sessions/:id/topology`, `/api/sessions/:id/timeline`.
- `.agents/sow/specs/sse-protocol.md` — `session_changed` envelope drives the realtime span append.
- `.agents/sow/specs/frontend-architecture.md` — D3 boundary, Web Worker rule, Canvas-above-500 rule, theme tokens.
- `.agents/sow/specs/ui-pages.md` — tab inventory (Overview/Topology/Trace/Timeline/Logs), realtime rules.

Current state:

- After SOW-0001: Trace/Topology/Timeline tabs are placeholders or absent; the Overview and Logs tabs work. `/topology` route is not registered.
- `frontend/src/viz/` exists per spec but contains no renderer yet.
- No D3 dependency is installed in `frontend/package.json` after SOW-0001 (the Phase 1 scope explicitly stopped before D3).

Risks:

- **R1 — D3 force-graph performance on 1K nodes**. Force simulation is O(n²) per tick without optimization. Mitigation: use `d3-force`'s built-in quad-tree (`d3.forceManyBody().theta(0.9)`), cap iterations, run in Web Worker per `frontend-architecture.md`. If still slow, fall back to a static hierarchical layout for the cross-session view above a node-count threshold.
- **R2 — Force layout instability with many small clusters**. Many independent sub-trees produce chaotic motion. Mitigation: seed initial positions by root-session id hash (deterministic), use `forceX`/`forceY` weak anchors per root, and offer a "freeze layout" button that pins all positions after the first convergence.
- **R3 — Timeline virtualization at high span density**. 1K sessions × 50 ops average = 50K spans. SVG cannot render that many nodes. Mitigation: Canvas renderer above 500 visible spans, computed virtualization (only render spans whose `[start_ts, end_ts]` overlaps the visible viewport in X AND whose lane is in the visible viewport in Y).
- **R4 — Flame-graph horizontal-overflow on long sessions**. A 30-minute session at constant ms-resolution overflows even a 4K monitor. Mitigation: the Trace tab is per-turn (each turn renders its own flame chart with its own X-axis fitting turn duration); a vertical accordion lists turns and renders flame charts on expand.
- **R5 — Sub-agent inline expansion vs separate page**. Inlining a child session's span tree inside the parent's Trace tab can hide the child's own context (its turns, its logs). Decision: clicking the "→ open sub-agent" affordance navigates to the child's `/sessions/:id` page (Trace tab pre-selected) instead of inlining. The Topology tab is the right place for "see the whole tree at once".
- **R6 — Span detail drawer payload preview size**. Some `llm_response` payloads exceed 1 MB; previewing inline kills the UI. Mitigation: server-side `Range: bytes=0-4095` request via `/api/payloads/:ref`, show truncation marker, link to full download.
- **R7 — D3 vs theme tokens**. D3 picks colors imperatively; CSS vars are runtime. Mitigation: a tiny `viz/color.ts` that reads `getComputedStyle(document.documentElement).getPropertyValue('--accent')` once and re-reads on theme change (subscribe via `MutationObserver` on `<html data-theme>`).

## Pre-Implementation Gate

Status: SATISFIED (2026-05-31) — prerequisites met (SOW-0001 + the 5 adapters in `done/`), operator UX decisions captured ("Implications And Decisions"), full-stack scope correction recorded. Cleared to implement.

Problem / root-cause model:

- ai-viewer currently shows sessions as flat rows in a list and a single Overview/Logs detail page. The session structure (parent op → child op → sub-agent session → its own ops) is invisible. Without span/topology/timeline views, the tool cannot answer the operator's primary questions: "which sub-agent burned the most cost?", "what's the critical path of this 15-minute session?", "did compaction help or hurt?". This SOW makes those questions answerable.

Evidence reviewed:

- All five specs cited above.
- SOW-0001's Acceptance Criterion #5 (Overview + Logs only) — confirming Trace/Topology/Timeline are deliberately Phase 3.
- SOW-0002's canonical model — confirming every field the views need is already in the schema.

Affected contracts and surfaces:

- Frontend: three new `pages/SessionDetail/*Tab/` components; one new `pages/Topology/` page; expansion of `viz/` with `topology.ts`, `timeline.ts`, `flame.ts`, `color.ts`.
- REST: **CORRECTION (2026-05-31, verified against `internal/presenter/`):** the `/api/sessions/:id/topology` and `/api/sessions/:id/timeline` routes are **spec'd but NOT implemented** — they currently fall through to `notImplemented` (404), pinned by `coverage_test.go`. SOW-0006 is therefore **full-stack**: it must build these two Go handlers + presenter queries (per `rest-api.md` shapes) BEFORE the frontend tabs can consume them. `GET /api/sessions/:id` (handler `presenter.go:235 handleSessionDetail`) is already live. Response shapes may also need additive fields (drawer payload preview, compaction extras pass-through).
- Specs: `ui-pages.md` (Trace/Topology/Timeline details), `rest-api.md` (any field additions), possibly `frontend-architecture.md` (Web Worker pattern documented for future viz work).
- Build: D3 + its tree-shake config added to `vite.config.ts`; bundle budget (≤ 500 KB gzipped main chunk per quality gates) re-validated.

Existing patterns to reuse:

- The Overview tab's TanStack Query + URL-synced filter pattern from SOW-0001.
- The SSE subscription pattern (`api/sse.ts`).
- The theme token reading approach (CSS custom properties + `getComputedStyle`).
- Subagent for D3 dependency research: check Grafana, Jaeger UI, Pixie, and SigNoz for reference flame-graph implementations under `/opt/baddisk/monitoring/repos/` per the `mirrored-repos` skill.

Risk and blast radius:

- Frontend-only change (no schema changes, no ingester changes). Worst case: the new tabs render badly or slowly; existing pages unaffected; rollback = revert the frontend commits.
- Bundle size is the one cross-cutting concern; CI gate (`vite build` size budget) catches regression.

Sensitive data handling plan:

- No new fixture commits expected — the existing `testdata/` from SOW-0001 covers the test surface. Any new fixtures must pass `scripts/sanitize-fixture.sh`.
- Span detail drawer payload previews read from real source files at runtime; no payload data lands in committed artifacts.

Implementation plan (ordered chunks):

1. **Spec deltas (lands FIRST, no code)**: update `ui-pages.md` with exact Trace/Topology/Timeline rendering contract (size metric controls, color legend, compaction breakpoint shape, span detail drawer fields, keyboard shortcuts for tab switching); update `rest-api.md` with any additive payload-preview parameter and explicit `compaction_extras` field on the timeline response; add a brief "Web Worker for D3 force simulation" section to `frontend-architecture.md` documenting Vite's worker-import idiom.
2. **D3 dependency + bundle-budget validation**: add `d3-selection`, `d3-force`, `d3-zoom`, `d3-scale`, `d3-axis`, `d3-shape` as tree-shakeable individual packages (not the umbrella `d3`); confirm bundle still under 500 KB gzipped.
3. **`viz/color.ts`**: theme-aware color reader; subscribes to `data-theme` MutationObserver; exposes `colorForOpKind(kind)`, `colorForStatus(status)`.
4. **`viz/flame.ts` + Trace tab**: per-turn flame-graph renderer (SVG, since turns are small); `pages/SessionDetail/TraceTab/`; click handler wires to the span detail drawer; SSE-driven append animation.
5. **Span detail drawer component**: reusable across all three views; pulls payload preview via `/api/payloads/:ref?range=0-4095`; Esc/outside-click close; focus trap.
6. **`viz/topology.ts` + per-session Topology tab**: D3 force-directed renderer; SVG mode under 100 nodes, Web Worker simulation above; size-metric selector wired to REST `?metric=` param.
7. **Cross-session `/topology` page**: same renderer; nodes = sessions; edges = parent/child + fork; uses the global FilterBar; "collapse sub-tree" toggle; "freeze layout" button.
8. **`viz/timeline.ts` + Timeline tab**: SVG renderer under 500 visible spans, Canvas renderer above; viewport-clipped span culling; compaction breakpoints; pan/zoom via `d3-zoom`.
9. **a11y pass**: tab navigation, ARIA labels on viz containers, axe-clean on all four routes.
10. **SSE wiring**: invalidate Trace/Topology/Timeline queries on matching `session_changed` events; spans animate in via 200ms CSS fade.
11. **E2E Playwright tests**: one per acceptance criterion; performance assertions via `performance.now()`.
12. **External review round**: codex + gemini + glm + qwen per `project-second-opinions` skill, scoped to the full SOW changeset.
13. **Address review findings**, re-review, mark SOW completed, move to `done/`.

Validation plan:

- Per-chunk vitest unit + RTL component tests (mandatory before next chunk).
- Playwright E2E for each acceptance criterion.
- Performance assertions in CI (`performance.now()` thresholds: 200 ms flame-graph, 500 ms topology, 300 ms timeline-zoom).
- axe-core checks integrated into Playwright per `frontend-architecture.md`.
- Bundle size gate (`vite build`) re-validated after D3 addition.
- External review converges (no actionable findings remain) before SOW close.

Artifact impact plan:

- `AGENTS.md`: no expected change.
- Runtime project skills: `project-frontend` may grow a "D3 viz boundary" section with the Web Worker pattern.
- Specs: `ui-pages.md`, `rest-api.md`, `frontend-architecture.md` updated in Chunk 1.
- End-user/operator docs: `docs/runbook.md` gets a "Tracing and topology views" section.
- End-user/operator skills: none expected.
- SOW lifecycle: standard — completed + moved to `done/` in the final commit.

Open-source reference evidence:

- Reference flame-graph and topology implementations to be researched per `mirrored-repos` skill during Chunk 4-7. Candidates: `grafana/grafana` (flame graph), `jaegertracing/jaeger-ui` (span tree + service graph), `pixie-io/pixie` (force-directed agent topology), `signoz/signoz` (timeline). Cite `owner/repo @ commit` and repository-relative paths only.

Open decisions:

- Resolved by the operator 2026-05-31 (recorded in "Implications And Decisions" below). The "sub-agent inline vs navigate" question stays navigate (R5). Two low-stakes points taken as CTO defaults (operator may override after seeing them live): op-kind/status colors = existing theme tokens; Timeline zoom = shift+wheel (per `ui-pages.md`).

## Implications And Decisions

Operator UX decisions, captured 2026-05-31 BEFORE any code (the operator owns visual judgment; recorded here per the decisions-before-work rule). The operator's framing was "try something, we change it later" — so these are starting points, built to be cheap to iterate:

1. **Trace tab — operator chose a Chrome-DevTools-Network-style view, not only a flame-graph.** Deliver a horizontal **waterfall** (one row per op/agent/tool, positioned + sized by `start_ts`/`duration_us`, like the Network tab's request timeline) that makes the SEQUENCE and DURATION of agents/tools obvious, PAIRED with a scrollable **list of everything that happened** (the full op/turn log, click-to-detail). A **flame-graph** view is also wanted ("would be nice too") — offer it as an alternate view of the same data. The operator explicitly does NOT mind a LARGE flame-graph "as long as it remains fast to work with it" → **performance is the hard constraint** (Canvas + viewport culling where span counts are high; the 200ms render budget holds). So Chunk 4 becomes: waterfall + event-list + flame-graph (toggle between views), all performance-first. Supersedes the Gate's narrower "per-turn flame-graph SVG."
2. **Topology tab — build ALL THREE layouts behind a toggle.** The operator: "probably all of them. If I don't see it, I cannot understand which would be more useful." So the Topology renderer exposes a layout selector: (a) seeded force + freeze-layout button, (b) plain force, (c) hierarchical tree — the operator compares them live and we settle on a default later. Chunks 6/7 deliver the multi-layout toggle, not a single force layout.
3. **Span/op detail — side drawer with inline payload preview** (the recommended option). Confirmed as Gate Chunk 5.

CTO defaults (operator may override on sight):
4. **Colors** = existing theme tokens (`--success`/`--warning`/`--error` for status; fixed per-kind palette) for consistency with the Overview status badges.
5. **Timeline zoom** = shift+wheel zooms time / plain wheel pans (per `ui-pages.md`).

Operator UX decisions captured 2026-05-31 from the REAL-DATA review (serving the operator's own claude-code sessions on the corrected backend surfaced these; recorded before code):

6. **Trace big-session views — build BOTH, user-selectable.** Real sessions are huge (operator's own session: 11,928 ops over ~5 days, which compressed the waterfall to a sliver at t=0). Operator: "zoom+pan AND aggregate by turn, user selectable — so both views." The Trace waterfall offers a view toggle: (a) **Detailed** — zoomable/pannable waterfall (shift+wheel zoom, drag pan, Canvas + viewport culling, turn-boundary delineation so inter-turn gaps read as "between turns"); (b) **By-turn** — one aggregated bar per turn, expand-on-click. Same "build all, choose later" approach as the Topology layouts.
7. **Trace source-aware durations.** Real data showed claude-code records LLM/reasoning as point events (no call duration → `end_ts == start_ts` → `0µs`); only tool ops (and ai-agent LLM/reasoning, which record `durationMs`) carry measured spans. Operator chose **source-aware** rendering: measured-span ops draw as bars; point-event ops draw as instant ticks/markers (never zero-width bars); the event list shows "—" not "0µs". Keeps the view honest to what each source records.

These reshape the chunk plan (Chunk 4 = waterfall+list+flame, now + source-aware rendering + dual big-session views; Chunks 6-7 = multi-layout topology) and confirm the full-stack scope (build the two backend endpoints first). The chunk plan above is the guide; per-chunk adjustments are logged in the Execution Log as they land. The same real-data review found two claude-code adapter robustness bugs (`retryInMs` float parse error; root `start_ts=0`) — filed as a separate follow-up SOW (out of SOW-0006's frontend scope).

## Plan

(Mirror of Implementation Plan above; expanded with commit refs as chunks land.)

## Execution Log

### 2026-05-31

- Activated; operator UX decisions captured (see "Implications And Decisions").
- **Chunk 1** — `ui-pages.md` tab contract for Trace/Topology/Timeline (3-layout toggle, freeze, Web-Worker-above-100, metric selector, shared drawer).
- **Chunk 2** — `/api/sessions/:id/topology` + `/timeline` session endpoints (Go presenter).
- **Chunks 3-4** — Trace tab: waterfall + flame-graph + event-list (view toggle), span detail drawer, `viz/color.ts`, source-aware durations + dual big-session views.
- **Merge master** — brought SOW-0029 (token/cache semantics) + SOW-0026 (op duration) into the branch so the Trace tab shows corrected tokens/cache; fixed one integration defect (TraceTab test fixture gained the new required `tokens_cache_read/write` fields). Branch verified healthy.
- **Chunk 6a** — per-session **Topology tab** (subagent-built, orchestrator-verified): `viz/topology.ts` (pure layout: force-seeded deterministic / force-plain / hierarchical) + `viz/forceWorker.ts` (Vite `?worker`, >100 nodes) + shared `TopologyRenderer` (SVG ≤100 / Canvas >100, d3-zoom) + `TopologyTab` (metric selector, 3-layout toggle, freeze button, node-click → SpanDetailDrawer). Deps added: d3-force/selection/zoom/hierarchy. Gates (orchestrator-run): tsc 0, eslint 0, vitest 363 pass, build main chunk 126.57 KB gzipped (≤500 budget; forceWorker code-split), scan-secrets PASS.
- **Chunk 6b** — cross-session **Topology** (`GET /api/topology` + the `/topology` page). Spec delta `rest-api.md §GET /api/topology` (sessions-as-nodes over the active filter; lineage edges via `parent_session_id` — which covers BOTH sub-agent spawns AND forks, since the codex adapter resolves source `forked_from_id` → `parent_session_id`+`kind='fork'` at ingest, so there is NO `forked_from_id` canonical column; 500-node cap + `truncated` flag). Go: `topology_cross.go` handler/builder (reuses `parseSessionFilter`/`whereClause` + the shared `topoNode`/`topoEdge`/`topologyResponse`+`Truncated`); registered in `presenter.go`. Frontend: `Topology.tsx` page (was ComingSoon) reuses the chunk-6a `TopologyRenderer`/`viz/topology`/`forceWorker` + global FilterBar, 3-layout toggle + freeze + metric, "showing top N" notice, node-click → `/sessions/:id`; `api/topology.ts` helper; `sse.ts` invalidates `['topology']` on `session_changed`. Gates (orchestrator-run): go build/vet 0, `go test -race` presenter ok, tsc 0, eslint 0, vitest 379 pass, build main chunk 127.18 KB gzipped, scan-secrets PASS.
- **Remaining:** Timeline tab; a11y pass (axe on all four routes); SSE span-append animation (Trace/Timeline); Playwright E2E (per-AC) + performance assertions; external review → SOW-0006 PR.

## Validation

(Filled at end. Performance numbers, test summary, review summary.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
