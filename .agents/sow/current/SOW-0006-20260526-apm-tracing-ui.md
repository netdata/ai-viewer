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
- **Timeline tab** (subagent-built, orchestrator-verified): `viz/timeline.ts` pure module (`layoutTimeline`/`timelineScale`/`cullSpans`/`isInstant`/`isCompaction`, `VISIBLE_SPAN_CEILING=500`) + `TimelineRenderer` (SVG ≤500 spans / Canvas + viewport-cull above; d3-zoom: shift+wheel zooms time, plain wheel pans) + `TimelineTab` (one lane per session root+children stacked; nullable `end_ts` → instant tick not a bar [source-aware]; `kind='compaction'` → full-height dashed breakpoint; span-click → SpanDetailDrawer). `useTimeline` helper; no new deps (d3-scale/d3-zoom already present). Gates (orchestrator-run): tsc 0, eslint 0, vitest 420 pass, build main chunk 129.40 KB gzipped, scan PASS.
- **a11y (AC#5, component-level) + SSE fade (AC#6)** (subagent-built, orchestrator-verified): viz containers `role="img"`→`role="group"` (closed a pre-existing `nested-interactive` axe violation from 6a/6b), Canvas keyboard-fallback `<button>` lists (Topology/Timeline), failure-% as on-graph TEXT (color-not-sole-signal), per-tab `jest-axe` assertions clean; `viz/spanFade.ts` + `useNewlyAppeared.ts` → 200ms span fade-in on Trace/Timeline (honors `prefers-reduced-motion`). **Real seam bug fixed:** `sse.ts onSessionChanged` only invalidated `['session',id]` → Timeline/Topology tabs never live-updated; now invalidates `['session-timeline',id]`/`['session-topology',id]`/`['topology']`. Spec deltas landed (`sse-protocol.md`, `frontend-architecture.md`, `ui-pages.md` realtime rule). Gates (orchestrator-run): tsc 0, eslint 0, vitest 451 pass, build main chunk 129.97 KB gzipped, scan PASS.
- **Playwright E2E** (subagent-built, orchestrator-verified tsc/lint; CI re-runs the suite): `viz-trace/topology/timeline/drawer/a11y/sse.spec.ts` — routes render, 3-layout switch, drawer open/Esc/outside-click, real-browser **axe zero serious/critical on all 4 routes (both themes)**, SSE subscription/stream. Fixed a stale `routes.spec.ts` assertion (it still expected `/topology`=ComingSoon; 6b shipped the real page). Perf assertions (200ms flame / 500ms topology / 300ms timeline) are best-effort + `testInfo.annotations`-logged (the deterministic seed is too small for 500-op/100-node/1K-session numbers — needs a large fixture; not faked). Full E2E `34 passed`; tsc 0, eslint 0, scan PASS.
- **AC#4 scope finding (CTO call):** the span drawer delivers its LIVE part — op fields + `payload_refs` list + open/Esc/outside-click/focus-trap a11y — but the **payload BYTE-preview (first 4 KB via `GET /api/payloads/:ref` + download-full link) is NOT implemented**; it is a DOCUMENTED Phase-2 deferral (`rest-api.md §GET /api/payloads`; `presenter.go` falls through to notImplemented; the drawer renders a disabled "preview coming soon" structured to wire in). Because that endpoint reads source-file byte ranges (a path-traversal / arbitrary-read security surface), it deserves its own SOW with a security design — **filed SOW-0033**. AC#4 is hereby NARROWED to the delivered drawer; the byte-preview tracked in SOW-0033. (Operator may override: if the preview must ship inside SOW-0006, say so and it folds back in.)
- **External review Round 1** (codex+glm+minimax) → operator "fix everything now" → Round-1 fix cycle (2 P1 + 4 P2). **Round 2** → codex found the Detailed-waterfall zoom/pan was only partially done (P2 blocker) + 2 real P3 + 1 rejected P3; Round-2 fix cycle landed the X-only waterfall zoom/pan (shared `viz/zoomInteraction`), spec/test drift fixes, and a frontend AI-attribution scrub + gate extension. See `## Reviews`.
- **Remaining:** re-review (Round 3, same scope + R2 fix notes) → SOW-0006 PR (squash-merge) → close.

## Validation

(Filled at end. Performance numbers, test summary, review summary.)

## Reviews

### Round 1 — 2026-05-31 (codex + glm + minimax) on commit `02ba377`

glm = "ready to merge, no blockers"; minimax = "ready, minor a11y conditions". BOTH WRONG — codex (decisive) found 2 P1 + 4 P2, all verified by the orchestrator against ground truth. SQL-injection-safety + HEAD/405/metric + bundle + no-regression confirmed by all three. Findings:

- **P1 (codex): cross-session `/api/topology` shows NO lineage by default.** `topology_cross.go:51` uses `parseSessionFilter`, which defaults `group:groupRoot` (`filters.go:78`) → `whereClause` adds `kind='root'` (`filters.go:324`). So the node set is root sessions only; lineage edges (parent→child) have no child endpoints → the cross-session graph is disconnected dots. The frontend doesn't pass `group=all`. **VERIFIED REAL.** Fix: the cross-session endpoint must default to all sessions (include sub_agent/fork) so lineage renders.
- **P1 (codex): Trace nesting uses a temporal-containment heuristic, not the persisted `parent_op_id`.** The ops table DOES persist `parent_op_id` (`writer.go:539,581` from `ParentOpSeq`), but `opDetail` (`session_detail.go:17-31`) does not expose it, so `trace.ts` derives nesting by temporal containment (`trace.ts:12`), whose stack only pops on `end<start` (`trace.ts:73`) → point events (end==start) become false ancestors. **VERIFIED REAL.** Fix: expose `parent_op_id` in `opDetail`; build the Trace tree from it (temporal only as fallback when null).
- **P2 (codex): recorded Trace UX decisions (#6 dual detailed-zoom/by-turn views, #7 source-aware point-event ticks + event-list "—") NOT implemented** (the Trace tab was built BEFORE those decisions were recorded). `Waterfall.tsx` always draws `<rect>` bars; `EventList.tsx:100` always formats `duration_us`. **VERIFIED REAL.**
- **P2 (codex): Timeline Canvas correctness/perf.** zoom transform scales lane HEIGHT too (should be X/time only); `cullSpans` drops full-height compaction breakpoints by lane; Canvas renders one hidden `<button>` per span (defeats the Canvas-above-500 DOM-scaling goal). **VERIFIED plausible (file:line cited); to fix.**
- **P2 (codex): Topology freeze pins stale DATA, not just positions** — `TopologyTab.tsx:97/153` store `PositionedNode[]`; changing metric/filter/SSE while frozen keeps old labels/radii/failure-ratios. Fix: freeze positions only, re-apply data.
- **P2 (codex): AC#6 (append→SSE→span appears) + perf ACs are annotation-only, not asserted** — acceptable only once the Canvas defects are fixed + a large fixture or component-level Canvas/worker tests are added.
- minimax Finding 5 (Trace Canvas keyboard fallback missing): **REJECTED** — `TraceTab.tsx:99` renders the keyboard-operable `EventList` unconditionally, which IS the Trace keyboard path. minimax Findings 1/6/7 (aria-hidden on fallback lists / dead `?? NEUTRAL_TOKEN` / `['topology']` over-fetch): minor, fold into the fix pass. glm P3 (`maxTopologyNodes` mutable test var): negligible.

Verdict: SOW-0006 NOT mergeable; fix the 2 P1 + 4 P2, then re-review. AC#4 byte-preview→SOW-0033 deferral and the security-clean backend stand.

**Operator decision (2026-05-31, AskUserQuestion): "Fix everything now (incl. Trace UX)."** All codex findings — the 2 P1 + all 4 P2, INCLUDING the recorded Trace UX redesign (decision #6 dual detailed-zoom/by-turn views + #7 source-aware point-event ticks) — are fixed inside SOW-0006 (NOT split to a follow-up), then re-reviewed before the PR.

### Round-1 fix cycle — 2026-05-31

- Backend (P1#1 + P1#2): `opDetail` exposes `parent_op_id`; `/api/topology` defaults to all sessions so lineage edges render.
- Trace (P1#1-fe + P2#3): nesting from `parent_op_id`; dual detailed-zoom/by-turn views; source-aware point-event ticks + event-list "—".
- Timeline (P2#4): zoom X/time only (not lane height); cull keeps full-height compaction breakpoints; Canvas keyboard fallback bounded so it does not reintroduce per-span DOM.
- Topology (P2#5): freeze pins POSITIONS only, re-applies node data on metric/filter/SSE.
- Coverage (P2#6) + minimax minors (aria-hidden fallback, dead `?? NEUTRAL_TOKEN`).

### Round 2 — 2026-05-31 (codex + glm + minimax) on the R1-fixed state `ec4f743`

glm + minimax = "ready to merge" — BOTH WRONG AGAIN. codex (decisive) confirmed the R1 fixes correct + complete for **P1#1, P1#2, P2#4, P2#5**, but found the recorded-Trace-UX fix **P2#3 was only PARTIAL** + 3 doc/test-drift P3. Verified each against ground truth:

- **P2 (blocker, codex): the Detailed waterfall is NOT zoomable/pannable.** `Waterfall.tsx:29` `const TRACK_WIDTH = 720` (fixed) with only a vertical `scrollTop`; no d3-zoom/wheel/drag on the time axis. `ui-pages.md:51` + decision #6 require "every op on a zoomable/pannable time axis (shift+wheel zooms time, drag pans)". The R1 work shipped the dual-view toggle + By-turn + point-ticks but never made Detailed zoomable. **VERIFIED REAL.**
- **P3 (codex): spec drift.** `ui-pages.md:58` still promised the drawer byte-preview (already deferred → SOW-0033); `rest-api.md` carried a DUPLICATE contradictory `GET /api/sessions/:id/timeline` section ("Phase 2 — not implemented") alongside the real "Implemented by SOW-0006" one. **VERIFIED.**
- **P3 (codex): SSE `['topology']` invalidation untested.** `sse.test.ts` asserted `['session-timeline']`/`['session-topology']` but not the cross-session `['topology']` (which `sse.ts:249` does invalidate); `viz-sse.spec.ts:16-19` claimed coverage. **VERIFIED.**
- **P3 (codex): dead `?? NEUTRAL_TOKEN` in `colorForFailureRatio` (`color.ts:125/128/130`). REJECTED on ground truth.** `frontend/tsconfig.json:19` sets `noUncheckedIndexedAccess: true` and `STATUS_TOKEN` is a `Record<string,string>` (`color.ts:13`), so even a literal-key access (`STATUS_TOKEN.completed`) is typed `string | undefined` — the fallback is type-REQUIRED (removing it fails `tsc` with TS2345). codex's finding does not hold under this tsconfig; documented the rationale in-code. (This is the adjudicate-on-ground-truth rule applied to REJECT an invalid reviewer finding.)

Verdict: SOW-0006 NOT mergeable on the P2 blocker; fix it + the 2 real P3, reject the color.ts P3.

### Round-2 fix cycle — 2026-05-31

- **P2 blocker (Detailed waterfall zoom/pan):** extracted the Timeline's zoom interaction into a shared `viz/zoomInteraction.ts` (`zoomEventFilter` + `attachZoom`, parameterized `plainWheelPan`; `SCALE_EXTENT`) consumed by BOTH tabs (one wheel convention, no drift; Timeline behavior-preserving — `{plainWheelPan:true}`, its tests unchanged). `Waterfall.tsx` now applies an X-only time transform `matrix(k,0,0,1,tx,0)` to the time track only — SVG via a clipped track `<g>` with the row-label gutter + turn-rules in a FIXED layer outside it; Canvas via manual `screenX = LABEL_WIDTH + trackX*k + tx` + an X-window cull + a track clip (NOT `ctx.transform`, which would drag the fixed gutter). SHIFT+wheel zooms time, drag pans time, plain wheel scrolls rows (native scroller). `ui-pages.md:51` clarified with the X-only / fixed-gutter decision. Test-first: 5 new Waterfall zoom tests + 15 `zoomInteraction` unit tests.
- **P3 spec drift:** `ui-pages.md` drawer note → byte-preview deferred to SOW-0033 (disabled "preview coming soon" until then); removed the stale duplicate `/timeline` "Phase 2 — not implemented" section from `rest-api.md`.
- **P3 SSE:** added the `['topology']` invalidation assertion to `sse.test.ts` (makes the `viz-sse.spec.ts` coverage claim honest).
- **P3 color.ts:** REJECTED (above), rationale documented in-code.
- **Attribution hygiene (SOW-0017 enforcement, found during this round):** the R1 subagents had left **22** AI-reviewer attribution comments/test-names in the frontend (`codex P2#4/5`, `minimax Finding 6`) — a standing breach of the no-AI-attribution rule on the public repo. The attribution gate `scripts/scan-ai-attribution.sh` only scanned Go trees (`cmd`/`internal`/`scripts`), so they slipped in. Scrubbed all 22 (kept the technical "why") AND extended the gate to scan `frontend/src` + `frontend/tests` so the class cannot reappear. `claude-code`/`codex`/`opencode` domain (session-format) terms preserved — the narrow pattern never matched them.
- **Gates (orchestrator-run, all green):** `tsc` 0, `eslint --max-warnings 0` 0, `vitest` **507 pass (41 files)**, `build` main chunk **131.34 KB gzipped** (≤500; d3-zoom already a dep, no new lib), `scan-secrets` PASS, `scan-ai-attribution` PASS (now incl. frontend).
- Next: re-review (Round 3, same scope + these fix notes) → squash-merge SOW-0006 PR → close.

### Round 3 — 2026-05-31 (codex + glm + minimax) on `fa72b27`

glm + minimax = "ready to merge" — missed everything again (the R1/R2 pattern). codex (decisive) CONFIRMED every R2 fix correct + complete (Waterfall zoom/pan, SSE `['topology']`, color rejection, attribution scrub) AND every R1 fix still holding, but found 3 NEW deeper findings — all verified real on ground truth:

- **P2 (codex): topology worker renders STALE identity via a counts-only cache key.** `graphKey(metric, mode, nodeCount, edgeCount)` (`TopologyTab.tsx:82`, `Topology.tsx:60`) keys on counts only; the render returns `workerResult.positioned` — the worker graph's FULL nodes (ids/labels/session-ids) — whenever that weak key matches (`TopologyTab.tsx:164`, `Topology.tsx:144`). Two different graphs with equal node+edge counts (a filter change / SSE refresh / session navigation) collide on the key → the UI shows the OLD graph's nodes for the NEW data, and a cross-session node click then navigates to the WRONG session, until the fresh worker result lands. **VERIFIED REAL.**
- **P3 (codex): timeline `end_ts:null` contract drift.** `rest-api.md` + the `session_timeline.go` comment said a null end "draws to the current viewport edge", but the runtime (`timeline.ts isInstant`: null or `<=start` → instant) + the frontend API type draw it as an instant marker (the source-aware rule). A SEPARATE contradiction from the duplicate-section fix. **VERIFIED.**
- **P3 (codex): per-session topology emits dangling child-session edges.** `session_topology_builder.go:117` adds `agent:<session> → agent:<childSessionID>` unconditionally; `finish()` appended every edge with no endpoint-presence check — violating the spec's "an edge whose target is outside the tree is dropped defensively" (`rest-api.md`). **VERIFIED.**

Verdict: NOT mergeable on the P2; fix the P2 + 2 P3.

### Round-3 fix cycle — 2026-05-31

- **P2 worker stale-identity:** both topology views' worker matched branch now re-joins the worker's POSITIONS onto the CURRENT `nodes` via `reapplyFrozenPositions(nodes, positionsOf(workerResult.positioned), opts)` (the exact helper the freeze path already uses) — node identity is always the live `nodes` (the worker supplies only x,y by id; a new node is id-seeded, a vanished node dropped), so a key collision can no longer render stale nodes or navigate to the wrong session; the layout snaps to the fresh positions when the new worker result lands. +2 tests (same-count, different-identity swap → current identity rendered + cross-session click navigates to the CURRENT session).
- **P3 timeline drift:** corrected `rest-api.md` (a null / `<=start_ts` end = instant marker at `start_ts`, NOT a viewport-edge bar) and the `session_timeline.go` comment to match the runtime + the source-aware rule.
- **P3 dangling edges:** `session_topology_builder.go finish()` now drops any edge whose endpoint is not a materialised node (matches the spec); +1 test (`TestTopology_DropsOutOfTreeChildEdge`). Verified the cross-session builder (`topology_cross.go:126`) already drops out-of-set endpoints — no change there.
- **Gates (orchestrator-run):** tsc 0, eslint 0, vitest **509 pass**, `go vet` + `go test -race ./...` clean, `scan-secrets` + `scan-ai-attribution` PASS.
- Next: re-review (Round 4, same scope + these fix notes) → squash-merge SOW-0006 PR → close.

### Round 4 — 2026-06-01 (codex + glm + minimax) on `edd1050`

minimax = "ready to merge". glm = "ready" but (digging with Explore subagents) flagged 2 P2 + 4 P3 it judged non-blockers. codex (decisive) CONFIRMED all R3 fixes + R1/R2 still correct, but found 2 NEW P2 + 2 P3 in the DELIVERED viz/drawer surface — all verified real on ground truth:

- **P2 (codex): the shared drawer FABRICATED missing data as real zeroes.** `TimelineTab.spanToOpDetail` + `TopologyTab.nodeToOpDetail` synthesized an `OpDetail` with `cost_usd:0`, `tokens_in/out:0`, `payload_refs:[]`; the drawer renders Cost/Tokens unconditionally + "No payloads" (`SpanDetailDrawer.tsx:152-154,174`). So clicking a Timeline span or a Topology node showed `$0.00 / 0 tokens / No payloads` as FACT — contradicting the same op's real values in the Trace tab. **VERIFIED REAL.**
- **P2 (codex): the Timeline Canvas path (>500 spans) lost lane identity** — `TimelineCanvas` painted no lane bands / no lane labels (SVG has both) and the keyboard-fallback buttons omitted the lane label → in the large path the operator could not tell which session lane a span belonged to (the lanes ARE the Timeline's purpose). **VERIFIED REAL.**
- **P3 (codex): Canvas compaction breakpoint hit-test was restricted to the span's lane band** despite the full-height visual (SVG has a full-height target). **VERIFIED.**
- **P3 (codex) + glm-P2: `buildOpTree` silently DROPPED ops on a `parent_op_id` cycle** (a closed cycle has no root → unreachable from `roots` → omitted from the tree). "No silent failures." **VERIFIED.**
- **glm-P2: `forceWorker.ts` had no error handling** — a thrown layout = the worker dies silently, the graph stays empty forever with no log/fallback. "No silent failures." **VERIFIED.**
- glm P3s (`maxTopologyNodes` mutable test var, duplicate tree-sessions query, raw `?metric=` echoed in a validation error) → minor backend code-quality, NOT user-facing, filed as **SOW-0034** (follow-up). glm's "cross-session `ctx_pct` = 0" is an ALREADY-DOCUMENTED limitation (no action).

Verdict: NOT mergeable on the 2 P2 + the 2 no-silent-failure items; fix in-line, defer the 3 minor backend P3s to SOW-0034.

### Round-4 fix cycle — 2026-06-01

- **Drawer honesty (P2):** `SpanDetailDrawer` is now source-aware via a discriminated `detail` prop (`{kind:'op'|'span'|'node'}`): Trace → full op row + payloads (unchanged); Timeline span → kind/name/status/timing + an "open this op in the Trace tab" note (NO fabricated cost/tokens/payloads); per-session Topology node → kind/label/failure-% + the selected metric's REAL value. `spanToOpDetail`/`nodeToOpDetail` deleted. `ui-pages.md` drawer note rewritten (source-aware, never zeroes).
- **Canvas lane identity (P2):** the Timeline Canvas path now paints lane zebra bands + lane labels (mirroring SVG, under the same zoom transform) and the keyboard-fallback buttons carry the lane/session label.
- **Canvas compaction hit-test (P3):** a compaction breakpoint now hit-tests full-height in Canvas, matching its full-height draw + the SVG behavior.
- **`buildOpTree` cycle (P3 / no-silent-failures):** a reachability + hoist pass keeps every op in the tree even under a `parent_op_id` cycle (unreachable cycle members are deterministically hoisted to roots) — none dropped. +cycle tests.
- **`forceWorker` error handling (no-silent-failures):** the worker wraps the layout in try/catch and posts `{error}`; `ForceWorkerResponse` is now `{positioned} | {error}`; both consumers (TopologyTab + Topology) log with structured context and fall back to the inline layout so the graph never stays permanently empty. +error-path tests.
- **Gates (orchestrator-run, combined final state):** tsc 0, eslint 0, vitest **531 pass (41 files)**, build main chunk **132.02 KB gzipped** (≤500), `go test -race` clean, `scan-secrets` + `scan-ai-attribution` PASS.
- Next: re-review (Round 5, same scope + these fix notes) → squash-merge SOW-0006 PR → close.

### Round 5 — 2026-06-01 (codex + glm + minimax) on `9e289ff`

glm + minimax = "ready to merge" (no new findings; SOW-0033/0034 deferrals reasonable). codex (decisive) CONFIRMED all R4 fixes correct + complete (drawer source-aware, Canvas lane identity + labels + fallback, compaction full-height hit-test, `buildOpTree` cycle hoist, `forceWorker` fallback) AND all prior rounds still holding, but found 1 P2 + 2 P3 — all verified real:

- **P2 (codex): the drawer still fabricated a `0µs` MEASURED duration for a point event.** `TimelineTab.spanDurationUs` used `end_ts >= start_ts`, so a point event (`end_ts == start_ts`) returned `0` (not `null`) → the drawer rendered `formatDuration(0)` = "0µs", implying a measured zero-duration the source never recorded (violates the source-aware rule, decision #7). **VERIFIED.**
- **P3 (codex): doc/spec drift** — `docs/runbook.md`, `frontend-architecture.md`, `README.md`, `ui-pages.md:187`, `presenter.md`, `App.tsx`, `types.ts` still described Trace/Topology/Timeline (and the `/api/sessions/:id/{topology,timeline}` endpoints) as Phase-2 placeholders / "ComingSoon" / "not yet implemented" — contradicting the shipped UI. SOW-0006 cannot honestly close with that drift. **VERIFIED.**
- **P3 (codex): `types.ts` topology-metric comment** conflated an ABSENT `?metric=` (defaults to `duration`) with an UNKNOWN value (rejected `BAD_REQUEST`). **VERIFIED.**
- **CI gosec blocker (orchestrator-found, not a reviewer):** CI installs `gosec@latest`; gosec v2.26.1 added the **G701** taint rule + re-attributed **G202** to the cross-session query concat (`topology_cross.go:173-183`); the existing `#nosec G201 G202` covered neither → the `lint` CI job failed. The query is injection-safe (enum `crossSizeExpr` + static `whereClause` + `?`-bound `args`) — a conservative false positive.

Verdict: NOT mergeable on the P2 + the CI gosec block; fix both + the 2 P3.

### Round-5 fix cycle — 2026-06-01

- **P2 (point-event `0µs`):** `spanDurationUs` now uses `> start_ts` (a point event → `null`, no duration); `formatDuration(null)` already renders "—", so the drawer (both `SpanBody` and `OpBody`) shows "—", never "0µs". +3 tests.
- **P3 doc/spec drift:** swept `docs/` + `.agents/sow/specs/` + `README.md` + `App.tsx` + `types.ts` — every stale "Phase-2 placeholder / ComingSoon / not-yet-implemented" reference to the shipped Trace/Topology/Timeline tabs, the cross-session `/topology` page, and the `/api/sessions/:id/{topology,timeline}` endpoints updated to reflect reality. (Legit unrelated placeholders kept: SQL `?` placeholders, PII tokens, the sessions-list-fade Phase-2, opencode Phase-2, the still-future `/tools|/models|/agents` ComingSoon tabs.)
- **P3 metric comment:** `types.ts` now distinguishes absent (→ `duration`) vs unknown (→ `BAD_REQUEST`).
- **CI gosec:** `topology_cross.go` `#nosec` now covers `G202` (the concat statement) + `G201 G202 G701` (the `QueryContext` sink) with a justification — `gosec -severity medium -confidence medium` reports **0 issues** (presenter + whole-repo); query logic unchanged. **CI hardening:** pinned `gosec@latest` → `gosec@v2.26.1` in `.github/workflows/ci.yml` so a future gosec release cannot silently break CI (Dependabot can bump it via a reviewed PR).
- **Gates:** tsc 0, eslint 0, vitest **534 pass (41 files)**, build main chunk ≤500 KB gz, `go vet` + `go build` + `gosec` (0) + `go test -race` clean, `scan-secrets` + `scan-ai-attribution` PASS.
- Next: re-review (Round 6, same scope + these fix notes) → squash-merge SOW-0006 PR → close.

### Round 6 — 2026-06-01 (codex + glm + minimax) on `f6f4e3d`

glm + minimax = "ready to merge" (no NEW findings; glm re-confirmed only the already-filed SOW-0034 item). codex (decisive) CONFIRMED the R5 gosec + metric-comment fixes correct and all prior rounds holding, but found the R5 point-event fix was INCOMPLETE + 2 P3 — all verified real on ground truth:

- **P2 (codex): the R5 `0µs` fix was Timeline-span-only; the TRACE op path still fabricated `0µs`.** Point-event ops are PERSISTED with `duration_us = 0` (NOT null): the ingest writer computes `end_ts - start_ts = 0` when `end_ts == start_ts` (`writer.go:721`). So the Trace drawer OpBody (`SpanDetailDrawer.tsx:240`), the Waterfall aria-label (`:233`), and the FlameGraph label (`:55`) formatted raw `op.duration_us` → "0µs". (EventList already guarded with `isInstantOp`.) The R5 tests used `duration_us: null` — the WRONG shape for a point event — so they missed it. **VERIFIED REAL** (untested-≡-broken: the R5 test exercised a data shape that does not occur in production; lesson recorded — tests must use the real persisted shape).
- **P3 (codex): `presenter.md` prose** still listed topology/timeline as "still-missing routes" (R5 fixed the route TABLE but not the surrounding prose). **VERIFIED.**
- **P3 (codex): timeline `end_ts` wire-shape drift** — `session_timeline.go` + `rest-api.md` + `types.ts` comments said a point event emits `end_ts: null`, but the server emits `end_ts == start_ts` for a point event (only a still-running op is null). **VERIFIED.**

Verdict: NOT mergeable on the residual P2; fix it comprehensively + the 2 P3.

### Round-6 fix cycle — 2026-06-01

- **P2 (Trace op `0µs`) — fixed COMPREHENSIVELY (not per-site whack-a-mole):** every per-op duration DISPLAY site now guards with `isInstantOp(op)` — the drawer OpBody Duration → "—"; the Waterfall + FlameGraph labels → "instant" (EventList already did "—"). A full audit of `formatDuration(...)` across TraceTab + the drawer confirmed no other per-op site is unguarded (axis-tick VALUES, the by-turn AGGREGATE rollup, the topology-node metric, and the already-null Timeline `span.duration_us` are correctly left). +3 tests using the REAL persisted point-event shape (`end_ts == start_ts`, `duration_us: 0`), proven red→green.
- **P3 presenter prose:** updated to record SOW-0006 shipped the per-session topology/timeline + the cross-session `/api/topology`; the still-missing list is now just `catalog`/`payloads`.
- **P3 timeline wire-shape:** `session_timeline.go` + `rest-api.md` + `types.ts` comments corrected — a still-running op emits `end_ts: null`; a point event emits `end_ts == start_ts`; the client treats `null` OR `<= start_ts` as an instant marker.
- **Gates:** tsc 0, eslint 0, vitest **537 pass (41 files)**, build main chunk ≤500 KB gz, `go vet` + `go build` + `gosec` (0, presenter + whole-repo) + `go test -race` clean, `scan-secrets` + `scan-ai-attribution` PASS.
- Next: re-review (Round 7, same scope + these fix notes) → squash-merge SOW-0006 PR → close.

### Round 7 — 2026-06-01 (codex + glm + minimax) on `9dc6704`

glm + minimax = "ready to merge". codex (decisive) CONFIRMED all R6 fixes correct+complete and all prior rounds holding, but found 1 P2 + 2 P3 — all verified real:

- **P2 (codex): the Timeline Canvas viewport-cull was a NO-OP in production AND the canvas backing store was unbounded.** `TimelineTab` passed the FULL content height (`AXIS_HEIGHT + lanes.length * LANE_HEIGHT`) as the renderer's `height`, so `cullWindowFor`'s visible lane band = ALL lanes (no Y-cull) AND `TimelineCanvas` allocated the canvas backing store at full height (~1K lanes → ~40000px → at DPR 2, ~80000px → EXCEEDS the browser ~32767px canvas limit → blank/broken). This violates **AC#3** ("zooming … across 1K sessions renders < 300 ms") and **R3** (the documented mitigation: "only render spans whose lane is in the visible viewport in Y"). The `TimelineRenderer` tests passed an artificial SHORT `height`, masking the production full-height path (same "wrong test shape" class as R6). **VERIFIED REAL.**
- **P3 (codex): `viz/timeline.ts` comments + `timeline.test.ts`** still called a nullable `end_ts` a "point event" (a point event emits `end_ts == start_ts`; only a still-running op is null). **VERIFIED.**
- **P3 (codex): stale `duration_us: null` "point-event" fixtures** in `EventList`/`Waterfall`/`FlameGraph`/`SpanDetailDrawer` tests (the null shape is a RUNNING op; a point event is `end == start` / `duration_us = 0`) — these contributed to the earlier `0µs` miss. **VERIFIED.**

Verdict: NOT mergeable on the P2 (violates the SOW's own AC#3/R3 perf contract + risks a blank canvas for the high-density case it was designed for); fix it + the 2 P3.

### Round-7 fix cycle — 2026-06-01

- **P2 (Timeline Canvas Y-virtualization):** `TimelineCanvas` rewritten to MIRROR the proven `WaterfallCanvas` — the canvas backing store is bounded to `min(CANVAS_VIEWPORT=460, contentHeight)` (NEVER the full lane stack), a tall spacer + sticky canvas + native vertical `scrollTop`, and `cullWindowFor` is computed from `scrollTop` + the bounded viewport so off-screen lanes are actually culled; X stays d3-zoom (time), Y is native scroll (`plainWheelPan:false`, the Waterfall convention). The Canvas path now also triggers when the lane stack exceeds the viewport (`spans > VISIBLE_SPAN_CEILING || height > CANVAS_VIEWPORT`), so a many-lane / few-span timeline never renders a 40000px SVG or canvas. +test: 900 lanes (full content 36022px) → the canvas backing store is bounded (< 2000px, not 36022px) and only the visible lane band is painted/listed (red→green; the existing short-height tests still pass).
- **P3 timeline running-vs-point-event:** `viz/timeline.ts` + `timeline.test.ts` comments/fixtures corrected (null `end_ts` = still-running; a point event has `end_ts == start_ts`).
- **P3 stale Trace fixtures:** the `EventList`/`Waterfall`/`FlameGraph` point-event fixtures converted to the REAL persisted shape (`end_ts == start_ts`, `duration_us: 0`); the `SpanDetailDrawer` null-end fixture relabeled "still-running op".
- **Gates:** tsc 0, eslint 0, vitest **539 pass (41 files)**, build main chunk ≤500 KB gz, `scan-secrets` + `scan-ai-attribution` PASS (no Go change this round; backend gates unchanged since `9dc6704`).
- Next: re-review (Round 8, same scope + these fix notes) → squash-merge SOW-0006 PR → close.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
