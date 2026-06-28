# SOW-0107 - Session Detail Workbench Redesign

## Status

Status: open

Sub-state: gap analysis drafted; external gap review rounds 1 through 28
completed and findings incorporated; gap-review rerun pending after round-28
changes. This is now the umbrella SOW for the Session Detail redesign;
implementation work is split into SOW-0108 through SOW-0113 so each major
concern gets its own fit-for-purpose analysis and reviewer gate. The
gap-analysis reviewer gate has not converged yet.

## Requirements

### Purpose

Make the single-session view fast, serious, and fit for an operator inspecting a
large AI-agent session on laptop and desktop screens. The screen must preserve
vertical space, reduce nested menus, keep visualization/content/table areas
predictable, and avoid sidebar/drawer behavior that hides the content the user
is trying to inspect.

### User Request

The user reported that the current single-session UI is complex, strange, and
patchwork-like. The requested direction is a segmented screen with:

1. Top title + stats ribbon, optimized for vertical space.
2. Controls and filters in their own ribbon.
3. Visualization area.
4. Raw data/table area.
5. Content area.

The user explicitly does not want a sidebar. The desired shape is:

```text
--------------------------------
HEADER                         |
--------------------------------
TITLE | STATS | STATS | STATS  |
--------------------------------
CONTROLS AND FILTERS           |
--------------------------------
                 |             |
  VISUALIZATION  |             |
                 |   CONTENT   |
-----------------+    AREA     |
      TABLE      |             |
--------------------------------
```

Follow-up user direction split this umbrella into focused child SOWs:

- SOW-0108: session identity, title, and ai-agent v3 data integrity.
- SOW-0109: turn-first table and content area.
- SOW-0110: turn-first waterfall visualization.
- SOW-0111: recursive topology stress map.
- SOW-0112: statistics and aggregate analysis.
- SOW-0113: shared selection, URL state, and cross-view synchronization.

### Assistant Understanding

Facts:

- The current UI is not literal iframe nesting: headless Playwright measured
  zero `iframe` elements and one React root on the session page.
- The visual problem is real: the screen is a nested workbench with multiple
  independent scroll containers, nested controls, and a fixed right overlay
  drawer.
- The current screen has already required a regression fix for zero-size resize
  handles and fixed Event list height; that fix does not address the larger
  product/layout concern.

Inferences:

- The current SOW-0088 "unified view" solved the earlier problem of many
  top-level tabs, but it over-corrected into a dense nested-pane tool.
- The next design should make the content area the primary inspection surface,
  not a drawer or narrow right rail.
- Visualization, table/raw data, and content should be simultaneously visible
  when space allows, but each must have clear ownership of scroll/zoom.

Unknowns:

- Whether the content area should show the selected op by default, the selected
  turn by default, or a split "turn summary + selected op detail" view.
- The content-area default is coupled to SOW-0113: a split view requires both a
  selected turn and selected op state, while a turn-only view can run on one
  selected turn identifier.
- Whether raw/log/event data should be one table with mode columns or separate
  table modes.
- Whether resizable splits should remain or the first pass should use fixed
  responsive proportions with only content-level zoom controls.
- The fate of existing `TimelineTab`, `OverviewTab`, `TurnsTab`, `LogsTab`, and
  `RawDataTab` must be explicit before implementation.

### Acceptance Criteria

- A 1366x768 viewport shows the title/stats ribbon, controls ribbon, useful
  visualization area, table area, and content area without the page-level main
  scroller appearing.
- A 1024x768 viewport exercises the stacked/narrow layout: header and controls
  stay at the top, visualization/table/content stack in order, and no overlay
  drawer or incoherent nested scrollbar appears.
  The stacked layout must avoid a page-level main scroller at 1024x768 for the
  default no-selection state. If selected content cannot fit without violating
  pane minimums, content scrolls inside its pane or lower-priority table/content
  panes collapse behind the defined table/view controls; the page itself must
  not become the primary scroller.
- The first-pass support target is laptop/desktop workbench use at 1024 CSS px
  width and above. Viewports below 1024px are not the primary acceptance gate,
  but must still use the same stacked flow without a sidebar/drawer, overlapping
  panes, or hidden primary actions. `1024px and above` means viewport width
  `>= 1024px`; below 1024px is best-effort, must not visually break, but is not
  the full workbench acceptance target.
  Browser zoom is treated as effective CSS viewport reduction: primary geometry
  budgets are proven at 100% zoom, while 125% and 150% zoom must fall into the
  same stacked/narrow behavior when the effective CSS width drops below the
  desktop threshold. The UI may be denser at zoom, but it must not overlap,
  hide primary controls, or reintroduce drawer-like panes.
- The title/stats area consumes no more than 96px vertical space below the
  global app topbar at 1366x768.
- The implementation plan proves the 96px ribbon target with concrete geometry:
  breadcrumb/title/id/pin, status, source/client metadata, and compact stats
  must either fit inside that budget or be deliberately rehomed to the content
  default. The old 88px title block plus 83.5px stats strip is not acceptable.
- Non-negotiable 96px ribbon contents: display title, status, source/client
  identity, compact time/status signal, and primary pin action. Secondary stats
  may move to the content default or a compact overflow only if the plan proves
  the ribbon cannot fit them without harming scanability.
  Compact overflow content is part of the header contract, not decoration: if
  used, it sits adjacent to the title/stats ribbon, uses one accessible
  disclosure control, and contains only displaced secondary stats such as
  tokens, cost, duration, failure count, or op count. It must never hide the
  non-negotiable title, status, source/client identity, time/status signal, or
  pin action.
  Operator-facing session status uses `effective_status` when the presenter
  provides it, falling back to raw `status` only when absent. Turn/op status
  remains raw turn/op status. Tests must cover an idle session whose raw
  `status` is `running` but whose `effective_status` is not `running`, proving
  the header ribbon shows the effective value.
  Contract definition: `effective_status` is the existing presenter-side
  derivation in `internal/presenter/session_status.go` from persisted
  `sessions.status`, `sessions.end_ts`, `sessions.last_activity_ts`, and server
  `now`, with the current 10-minute stale threshold. It is not a stored column
  in this SOW and must not be redefined independently by child views.
  Cross-endpoint consistency is bounded by response time: each workbench
  endpoint captures one `now` / `generated_at` value per response and uses that
  value for every `effective_status` it computes inside that response. Separate
  endpoints may differ by the elapsed time between requests; this is accepted in
  the first pass and must be visible through `generated_at` metadata rather than
  hidden behind inconsistent client-side recomputation.
- Header breadcrumb behavior is part of the 96px proof. When the user navigates
  from a parent session into a child session, the header shows a compact
  breadcrumb trail using `display_title` / `display_agent_label` where
  available. Each ancestor segment links back to that session. The
  implementation plan must prove this fits the 96px budget or explicitly move
  secondary stats out of the ribbon.
- Breadcrumb data source is part of the contract. The first implementation must
  not fetch full session detail responses one ancestor at a time. First-pass
  route is an inline `ancestors[]` field on `GET /api/sessions/:id`, matching
  SOW-0108. A named slim companion endpoint is allowed only if the implementation
  plan proves the inline parent walk breaks the 500ms first-paint budget and
  updates `rest-api.md` before tests. The `ancestors[]` array is ordered
  root-to-parent with each item carrying
  `id`, `display_title`, `display_title_source`, `display_agent_label`, and
  `display_agent_label_source`; optional client/source metadata may be included
  if the 96px proof needs it. The backend must resolve it with one bounded,
	  indexed parent walk and cycle/depth protection. First-pass depth cap is 64
	  ancestors. Cycles and chains beyond that cap fail closed with a visible
	  "ancestor chain unavailable" state and structured backend error context; they
	  must not hang, recurse unbounded, or fetch full session details one by one.
	  A broken chain is distinct from a cycle: if a parent id points to a missing
	  parent row during partial ingest or repair, the breadcrumb shows resolvable
	  ancestors up to the break, appends a compact "parent unavailable" terminal
	  segment, and logs structured backend context. It must not auto-navigate to an
	  ancestor or silently hide the break.
	  Producer ownership is explicit: SOW-0108 owns the `ancestors[]` presenter DTO,
	  label fields, bounded parent walk, and missing-parent marker; this umbrella
	  owns the header layout, 96px proof, route-unavailable behavior, and
	  breadcrumb consumption.
- Controls are consolidated into one ribbon. There is no nested Waterfall menu
  inside a Waterfall tab and no separate topology-only control island that
  visually changes the shell.
- Control/filter persistence is explicit: global workbench filters live in the
  URL state owned by SOW-0113; view-local controls are reset only when the SOW
  states that reset behavior and tests it.
- The right-side fixed `SpanDetailDrawer` is not used by Session Detail. Clicking
  a visualization span or table row updates the persistent content area.
- The visualization area owns exactly one primary scroll/zoom surface. Canvas/SVG
  dimensions are derived from the pane viewport, not hard-coded to much larger
  virtual surfaces that expose random inner scrollbars.
- The table/raw area owns exactly one table scroll surface and never overlays or
  escapes its pane.
- The content area is wide enough to render payload/turn/op information without
  the current narrow drawer presentation.
  Minimum width contract: at 1366x768 and above the content area must have at
  least 480px usable width. In the 1024px stacked layout it must not collapse
  below 320px. If the viewport cannot satisfy pane minimums, lower-priority
  table/visualization surfaces collapse behind the defined controls rather than
  squeezing content back into a drawer-like strip.
- The content area owns exactly one vertical scroll surface for selected-turn
  rendering. It does not grow indefinitely, escape its pane, or create the page
  scroller; large turns use SOW-0109 windowing/chunking inside this content
  scroll surface.
- Focused Playwright checks assert geometry at 1366x768, 1440x900, and
  1600x1000; component tests assert URL state and selection behavior. Geometry
  assertions must read computed pane rectangles, not rely only on screenshots:
  content width, visualization height, table height, page scroll height, and
  count of scrollable containers are recorded for each viewport.
  At least one Playwright geometry route runs in both dark and light themes so
  the redesigned shell, stats ribbon, controls overflow, and pane borders are not
  validated in only one theme.
- Reference workstation for performance budgets: this development workstation
  running the current Linux desktop environment, with CPU/RAM/GPU/OS captured by
  the implementation plan before the first measurement. The initial SOW budget
  target is this workstation only; CI uses smoke/functional checks unless a
  stable CI performance profile is added later. Browser-specific first-pass
  performance and geometry budgets are Chromium/Playwright budgets; Firefox and
  Safari are out of first-pass scope unless the implementation plan explicitly
  adds smoke coverage. The SOW must record this as scope, not accidentally imply
  cross-browser certification.
- Every existing Session Detail tab/component has an explicit disposition:
  removed, folded into the new table/content/visualization areas, or kept as a
  named visualization/table mode.
- Shared selection and URL state are defined once by SOW-0113 and consumed by
  the table, waterfall, topology, statistics, and content areas.
- Child-session navigation is defined once by this umbrella and SOW-0113: child
  links in content, waterfall, and topology navigate to `/sessions/:childId`,
  reparent the workbench to that session, and clear session-local `sel`,
  `focus`, and `hi` state. Non-session-local mode params such as `view` and
  `table` may remain only when valid for the new session. Browser Back returns
  to the prior parent workbench state.
- Child-session data scope is defined once here: after navigation to a child
  session, workbench data defaults to that child session's subtree. Table,
  content, waterfall, topology, and statistics must not silently mix child
  subtree data with sibling/root-tree data. If an area offers a root-tree or
  whole-session comparison mode, the controls ribbon shows an explicit scope
  indicator and the URL state records the scope. Existing root-resolving
  endpoints such as `/api/sessions/:id/trace` cannot be used for a child-session
  view unless the implementation filters them to the viewed subtree or extends
  the API contract before child SOW plan review.
  Backend subtree discovery is shared: the implementation plan must provide one
  tested subtree session-id resolver/helper, with optional single-flight/cache
  keyed by `(session_id, scope, generated_at/source revision)` if caching is
  used. SOW-0109, SOW-0110, SOW-0111, and SOW-0112 must reuse that resolver
  instead of each adding a separate recursive walk or CTE pattern. Cache entries
  invalidate on `session_changed`, `resync`, or any source revision change that
  affects the subtree.
  Implementation ownership is explicit: this umbrella SOW owns creating the
  shared resolver package/helper before child backend plans finalize. First-pass
  target name is `internal/presenter/subtree` or an equivalent reviewed package
  with tests. It returns a bounded flat session-id set for the viewed session
  subtree, enforces cycle/depth protection, supports the child-session default
  scope, and exposes cache/single-flight only through a named API. Child SOWs may
  consume it but must not create parallel subtree walkers.
- The workbench defines loading, error, empty, and stale-selection states for
  each area: header/stats, controls, visualization, table/raw, and content.
- Existing `react-resizable-panels` persisted layout keys are reset, migrated,
  or deliberately ignored by the new layout. Old saved split sizes must not
  distort the redesigned workbench.
- The implementation plan measures the existing `GET /api/sessions/:id`
  response size and latency on the shared 1000-turn / 10k-op fixture before
  child plans assume it is adequate. If the all-turns/all-ops response misses
  the first-paint budget, the plan must introduce a lightweight turn-summary
  contract before SOW-0109 starts implementation.
- Measurement trigger for the lightweight contract: on the shared 1000-turn /
  10k-op fixture, if `GET /api/sessions/:id` plus first table/waterfall paint
  exceeds 500ms on the reference workstation, or if the uncompressed JSON body
  exceeds the plan-recorded response-size cap, the first implementation must use
  a summary/detail split instead of the all-detail payload. Planning baseline
  for the cap is 10 MiB uncompressed unless measurement proves a stricter cap is
  needed for the browser heap budget.
- Measurement method must be falsifiable before child planning: run one warm-up
  and at least five measured local runs against the same generated SQLite
  fixture, record median and slowest response latency, uncompressed JSON bytes,
  first table/waterfall paint time, fixture hash, command/test name, and
  reference-workstation load conditions. Browser paint measurement uses headless
  Playwright on the target viewport set; backend response measurement uses the
  presenter route or an equivalent benchmark that records the exact route and
  query params.
  If this measurement cannot complete during the first implementation milestone
  because the workstation cannot be quieted, the fixture is not yet available,
  or the all-detail result remains ambiguous, child plans must default to the
  lightweight turn-summary / selected-turn contract instead of blocking or
  guessing. That default is conservative: it may be replaced by the all-detail
  path only after recorded measurements prove the all-detail path meets the
  budget.
- Draft fallback API if the measurement fails:
  - `GET /api/sessions/:id/turns` returns a slim workbench envelope, not a bare
    array. The envelope carries the session metadata needed by the header and
    child navigation plus ordered turn summaries:
    `{ session: { id, display_title, display_title_source,
    display_agent_label, display_agent_label_source, display_client_label?,
    display_client_label_source?, status, effective_status, start_ts, end_ts,
    last_activity_ts }, ancestors, turns }`.
    `ancestors` uses the SOW-0108 breadcrumb DTO and the same 64-depth/cycle
    protection as the all-detail path. `last_activity_ts` is the
    server-provided latest activity timestamp for the fetched snapshot and is
    required by SOW-0110 when `end_ts` is null. It is the persisted
    `sessions.last_activity_ts` maintained by ingest, including the current
    aggregate refresh behavior that preserves the session value and advances it
    to the latest closed op `end_ts`. If implementation changes that
    computation, it must update `data-model.md`, presenter tests, and every
    workbench endpoint consuming the value in the same change.
  - Each turn summary contains:
    `{ id, seq, status, error_class, start_ts, end_ts, duration_us, op_count,
    failure_count, cost_usd, tokens_in, tokens_out, child_spawn_count }`.
    `op_count` is sourced from stored `turns.op_count` unless implementation
    planning proves the stored value is unavailable or stale and updates the
    store/writer contract. `failure_count` and `child_spawn_count` must not be
    produced by one SQL query per turn. The plan must choose either persisted
    turn-level columns with migration/writer/backfill tests
    (`turns.failure_count`, `turns.child_spawn_count`) or one grouped query over
    the returned turn ids with EXPLAIN/benchmark evidence. `child_spawn_count`
    counts direct child-session spawn ops owned by that turn, using
    `kind='session'` and a non-empty child-session/native-child reference in the
    canonical row/projection. It is distinct from SOW-0108 child-summary
    `direct_child_count`, which counts direct children of a child session.
    For cross-SOW alignment, turn-summary `child_spawn_count` is the DTO-local
    alias for the shared stress metric `subagent_call_count` defined by
    SOW-0111/rest-api.md; matched turns must produce the same numeric value in
    both surfaces.
    `duration_us` is computed as `end_ts - start_ts` when `end_ts` is known and
    otherwise `null`; it is not a stored `turns` column unless implementation
    planning proves storage is needed. The canonical failure predicate for
    `failure_count` uses the shared terminal-failure family for workbench stress
    metrics: `failed`, `abandoned`, `interrupted`, and `aborted`. This
    intentionally aligns the failure metric with SOW-0113 `status=failed`
    filtering and session `effective_status` semantics. If existing persisted
    rollups still use the narrower historical predicate `status='failed'`, the
    implementation must update the session/turn rollup predicate,
    data-model/spec text, backfill/reprocess path, and stats/topology semantics
    in the same spec/test/code change instead of drifting turn counts away from
    session and stats counts. This umbrella SOW owns that coordinated
    failure-rollup alignment as a named prerequisite; SOW-0111 and SOW-0112
    consume the aligned predicate rather than re-deriving a competing one.
    Counting non-empty `error_class` as failure remains rejected unless the same
    shared predicate is updated everywhere.
    If the lightweight contract is chosen, child-session boundary data must be
    explicit before child SOW planning closes: each turn summary either carries
    a bounded `child_summaries[]` projection for child calls spawned by that
    turn, or the plan defines a bounded co-fetch endpoint keyed by turn id.
    Required projection fields are the SOW-0108 child-summary baseline: child
    id/native id, display labels, raw `status`, `effective_status`,
    `last_activity_ts`, `error_class`, start/end, `duration_us`, cost, tokens,
    op/failure counts, `direct_child_count`, and child-session link. Loading the
    full child session recursively to render parent-session boundaries is
    rejected.
  - `GET /api/sessions/:id/turns/:turnId/detail` returns the selected turn's
    ordered ops/events plus payload-ref metadata and proof/availability fields
    needed by SOW-0109 and SOW-0110.
  - If `table=events` remains enabled while the lightweight contract is active,
    `GET /api/sessions/:id/events` returns the paged flat workbench event
    envelope described below for chronological session-subtree inspection. The
    events endpoint is not required only if the chosen table/raw-area decision
    removes `table=events` from first-pass controls or the all-detail payload is
    measured and retained as the event source. The lightweight path must not keep
    using `/trace` as an implicit all-events backdoor after `/trace` is retired
    for waterfall/data-loading purposes.
  - If legacy `?op=` support remains enabled under the lightweight contract,
    SOW-0107 owns the producer-side REST/API contract for
    `GET /api/sessions/:id/ops/:opId` or an equivalent op-to-turn lookup because
    it owns the lightweight data contract. SOW-0113 owns the frontend URL/cold
    deep-link consumption path. Preferred shape:
    `{ op_id, turn_id, status }`, with standard REST method/error behavior,
    same-session validation, `404` for unknown op/session, `400` for invalid
    ids, and `HEAD` parity where the final REST/API spec requires it.
  - Both endpoints use the standard error envelope, `404` unknown session/turn,
    `400` invalid ids, and `HEAD` parity where the final REST/API spec requires
    it. The exact response shape is finalized in the implementation plan before
    child SOWs consume it.
- The implementation plan reviews `sse-protocol.md` for workbench live-update
  needs, including selection invalidation, new-work markers, and matching-count
  signals for active filters/highlights. If the existing `session_changed` event
  misses the measured interaction budget and a server-side matching-count or
  new-work event is required, SOW-0107 owns the `sse-protocol.md` delta that
  defines that event. SOW-0113 records the "existing event is sufficient" branch
  and consumes any SOW-0107 protocol delta; child views do not define SSE events
  independently.

## Analysis

Sources checked:

- `frontend/src/pages/SessionDetail/SessionDetail.tsx`
- `frontend/src/pages/SessionDetail/UnifiedView/UnifiedView.tsx`
- `frontend/src/pages/SessionDetail/TraceTab/TraceTab.tsx`
- `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx`
- `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.module.css`
- `internal/presenter/session_detail.go`
- `.agents/sow/specs/rest-api.md`
- `.agents/sow/specs/sse-protocol.md`
- `.agents/sow/specs/ui-turn-view.md`
- Headless Playwright screenshots:
  - `/tmp/ai-viewer-session-gap-laptop-1366x768-default.png`
  - `/tmp/ai-viewer-session-gap-laptop-1366x768-topology.png`
  - `/tmp/ai-viewer-session-gap-laptop-1366x768-drawer.png`
  - corresponding 1440x900 and 1600x1000 captures

Current state:

- At 1366x768, below the global topbar:
  - Title/id/pin block: 88px high.
  - Stats ribbon: 83.5px high.
  - Visualization pane: 325px high.
  - Event/table pane: 175px high.
  - Content/turn pane: 323px wide.
- The current default Waterfall view exposes these scrollable containers:
  - Main page scroller.
  - Visualization content scroller.
  - Waterfall canvas/SVG scroller with huge virtual height.
  - Event list table scroller.
  - Turn list scroller.
- The visualization control surface is nested:
  - Top-level viz tabs: Waterfall, Topology, Timeline, Statistics.
  - Waterfall-specific view selector: Waterfall / Flame.
  - Waterfall-specific mode selector: Detailed / By-turn.
  - Waterfall filters: kind, status, sub-agent.
  - Topology swaps in a different internal control set: size metric, layout
    mode, freeze layout.
- `SpanDetailDrawer` is fixed to the right edge with `width: min(420px, 92vw)`.
  On desktop it overlays the same side of the screen as the current turn/content
  pane instead of using that pane.

External gap review round 1 findings incorporated:

- All six reviewers voted `NEEDS WORK` for the gap-analysis package.
- The highest-priority umbrella gap was the missing shared selection and URL
  state contract. This is now split into SOW-0113 and is a prerequisite for
  SOW-0109 through SOW-0112.
- Reviewers found that the draft did not explicitly say what happens to the
  existing Timeline, Overview, Turns, Logs, and Raw Data surfaces. This SOW now
  requires a tab/component disposition table before implementation.
- Reviewers found that drawer removal was under-specified: `TraceTab`,
  `TopologyTab`, and `TimelineTab` click-to-detail behavior must be rehomed
  into the persistent content area.
- Reviewers found that accessibility and deep-link migration need first-class
  contracts, not generic "run a11y tests" language.

External gap review round 2 findings incorporated:

- Reviewers found the 96px title/stats target under-specified against the
  measured 171.5px current header+stats height. This SOW now requires the
  implementation plan to prove what stays in that budget and what moves.
- Reviewers found `OverviewTab` and `TurnsTab` are not active route surfaces in
  the current `SessionDetail` path; their disposition is now dead-code cleanup
  or reuse-by-extraction, not active tab folding.
- Reviewers found `TraceTab`, `TopologyTab`, and `TimelineTab` still import
  `SpanDetailDrawer`; drawer removal must cover all Session Detail child views,
  not only `UnifiedView`.
- Reviewers found the current layout persistence keys
  `ai-viewer.session.vright` and `ai-viewer.session.vbottom` are durable UI
  state and must be reset/migrated/ignored explicitly.
- Reviewers found `LogsTab` and `RawDataTab` disposition was too vague. The
  umbrella keeps both as table/raw modes unless a later operator decision
  explicitly removes them.
- Reviewers found Timeline, Detailed Waterfall, and Flame disposition was split
  across SOWs. This umbrella owns the final mode inventory; SOW-0110 owns the
  behavior of any waterfall/flame mode that survives.

External gap review round 3 findings incorporated:

- Successful reviewers found the remaining umbrella gaps are mostly ownership
  and precision: `SpanDetailDrawer` has no confirmed non-Session-Detail
  consumers, Timeline/Overview work must be explicitly owned here, old persisted
  layout keys need one selected strategy, and the target `ui-turn-view.md` delta
  is a rewrite of the SOW-0088 shell section rather than a small update.
- Reviewers also required SOW-0113 to be marked as the hard prerequisite for
  the table, waterfall, topology, and statistics child SOWs at the gate level.

Risks:

- A superficial CSS-only cleanup will not fix the mental-model problem. The
  component boundaries currently encode the nested layout.
- Removing the drawer changes existing tests and accessibility behavior; the
  replacement content area needs equal or better keyboard/focus behavior.
- Visualization sizing changes can regress large-session performance if the
  canvas/SVG windowing logic is not preserved.
- URL state must remain stable enough for deep links and browser back/forward.

## Pre-Implementation Gate

Status: blocked

Problem / root-cause model:

- The current screen is a composition of old tab surfaces inside the newer
  SOW-0088 unified shell. It is technically "one page", but it still behaves as
  several pages embedded together: each zone carries its own header, controls,
  scroll model, and selected-op behavior.
- The fixed right overlay drawer is especially incompatible with the intended
  persistent content area. It hides or competes with the right pane instead of
  replacing it.

Evidence reviewed:

- `SessionDetail.tsx` renders breadcrumb, title, pin button, and full id before
  the stats/workbench, creating an 88px title block at 1366x768.
- `UnifiedView.tsx` creates nested horizontal and vertical panel groups and a
  right "Turn view" pane.
- `TraceTab.tsx` embeds Waterfall/Flame, Detailed/By-turn, kind/status/agent
  filters, and the Event list as its own internal view.
- `SpanDetailDrawer.module.css` fixes a right-edge panel at `min(420px, 92vw)`.
- Headless measurements show five independent scrollable areas at 1366x768.

Affected contracts and surfaces:

- Session Detail route `/sessions/:id`.
- URL params for visualization mode, table mode, selected op/turn, filters, and
  persisted layout. SOW-0113 owns the canonical state shape and migration path.
- `TraceTab`, `TopologyTab`, `TimelineTab`, `LogsTab`, `RawDataTab`, `TurnView`,
  `SpanDetailDrawer`, and their tests.
- Specs: `ui-turn-view.md` and `ui-pages.md`.
- E2E tests: deep-link, trace, topology, timeline, a11y, new layout geometry
  assertions.

Existing patterns to reuse:

- CSS Modules and CSS tokens.
- URL-synced state via `useSearchParams`.
- D3/canvas code remains isolated under `src/viz/`.
- Playwright route tests derive session IDs from `/api/sessions`.
- Existing `TurnView` rendering can be reused inside the content area after
  removing the drawer path.

Risk and blast radius:

- Medium/high frontend blast radius: this touches the most complex screen and
  multiple visualization tabs.
- Low backend risk for the umbrella shell itself. Child SOWs may introduce
  backend/API/schema work; those risks are tracked in the child SOWs.
- Performance risk: large sessions need measured viewport-driven virtualization
  and canvas sizing, not DOM expansion.
- Accessibility risk: replacing a focus-trapped drawer with an in-page content
  panel requires clear focus movement and keyboard selection states.

Component and tab disposition required before implementation:

| Existing surface | Target disposition |
|---|---|
| `SessionDetail.tsx` | Keep route shell; compact header/title/stat composition. |
| `UnifiedView.tsx` | Replace nested panel shell with five-area workbench. |
| `TraceTab` / Waterfall | Fold into visualization area; SOW-0110 owns waterfall behavior. Remove `SpanDetailDrawer` usage from this path. |
| `TopologyTab` | Fold into visualization area; SOW-0111 owns topology behavior. |
| `TopologyTab` detail behavior | Remove `SpanDetailDrawer` usage; node clicks update the shared content/selection model. |
| `TimelineTab` | First-pass baseline: remove the separate Timeline tab and fold any distinct timeline value into turn-first waterfall/topology/statistics. If the user explicitly chooses to retain Timeline before implementation planning, this SOW must add retained-mode behavior/a11y/performance requirements before child plans proceed. The existing `TimelineRenderer` Canvas accessibility mirror pattern must be extracted or explicitly referenced by SOW-0110 before deleting timeline code. |
| `StatsTab` | Replace the existing placeholder/statistics tab with the SOW-0112 statistics surface. Delete/archive old placeholder code once SOW-0112 tests cover the new statistics workbench mode. |
| `OverviewTab` | Already out of the active `SessionDetail` route path; delete/archive or extract useful summaries into `OverviewTiles`/compact stats after SOW-0109 selected-turn tests are in place and before this umbrella closes. |
| `OverviewTiles` | Existing reusable compact stats component at `frontend/src/pages/SessionDetail/UnifiedView/OverviewTiles.tsx`; keep or refactor into the title/stats ribbon instead of recreating the summary strip. |
| `TurnsTab` | Already out of the active `SessionDetail` route path; replace any useful code with SOW-0109 turn-first table/content and delete/archive dead tab code after SOW-0109's turn table lands and before this umbrella closes. |
| `LogsTab` | Keep as a `table=logs` mode in the table/raw area. |
| `RawDataTab` | Keep `frontend/src/pages/SessionDetail/RawDataTab.tsx` as a `table=raw` mode in the table/raw area. SOW-0107 owns adding a focused raw-mode test before relying on it in the redesigned shell; SOW-0109 may extend that test only for table-selection interactions. |
| Detailed op waterfall | First-pass baseline: remove as a primary nested mode and ship only turn-first waterfall. If the user explicitly chooses to retain it, it becomes a named visualization mode in the controls ribbon and SOW-0110 must add mode-specific behavior before implementation planning proceeds. |
| Flame graph | First-pass baseline: remove from the first redesign. If the user explicitly chooses to retain it, it becomes a named visualization mode in the controls ribbon and SOW-0110 must add mode-specific behavior before implementation planning proceeds. |
| `UnifiedView` persisted panel IDs | Recommended first-pass strategy: reset/ignore `ai-viewer.session.vright` and `ai-viewer.session.vbottom` with a new versioned layout key. Do not migrate old proportions into the redesigned layout. |
| `SpanDetailDrawer` | Remove from all Session Detail child views. Known consumers are `TraceTab`, `TopologyTab`, and `TimelineTab`; `UnifiedView` only references the drawer in comments. Before deletion, grep the entire `frontend/src/` tree for both `SpanDetailDrawer` and exported `SpanDetail` type imports, check `frontend/src/components/SpanDetailDrawer/index.ts`, and verify no tests import the component/type as fixtures. If no non-Session-Detail consumers remain, delete/archive the whole `SpanDetailDrawer/` directory and all imports rather than leaving dead code. |

Backend endpoint disposition required before implementation:

| Existing endpoint | Target disposition |
|---|---|
| `GET /api/sessions/:id/trace` | Retain as the waterfall data source only if the SOW-0107 API measurement proves the trace/all-detail payload meets the workbench budget. Otherwise replace it with the turn-summary / selected-turn contract and retire it only after a repo caller audit proves no remaining caller. |
| `GET /api/sessions/:id/topology` | Retain as the legacy per-session topology endpoint until the old topology tab path is removed and caller audits prove no remaining dependency. If retained after SOW-0108 lands, it must expose display-safe session labels or a structured deprecation marker; it must not keep surfacing raw ai-agent v3 `parent` when a verified display label exists. |
| `GET /api/topology` | Retain as the global cross-session topology endpoint only with the SOW-0108 display-label propagation contract. Global topology responses/tests must prove ai-agent v3 `parent` does not render when `display_agent_label` exists. |
| `GET /api/sessions/:id/timeline` | Retire when Timeline UI is removed, unless SOW-0110's Canvas a11y extraction proves an endpoint dependency still exists. Retirement requires a caller audit across API helpers and tests. |

New endpoint inventory required before implementation:

| New endpoint | Owner | Purpose | HTTP/spec contract |
|---|---|---|---|
| `GET /api/sessions/:id/turns` | SOW-0107 | Lightweight turn-summary envelope when all-detail is too heavy. | This SOW's lightweight contract; `rest-api.md` delta before tests/code. |
| `GET /api/sessions/:id/turns/:turnId/detail` | SOW-0107 + SOW-0109 consumer | Selected-turn ordered ops/events, payload-ref metadata, and proof/availability fields. | SOW-0107 producer contract, SOW-0109 content renderer contract, standard GET/HEAD/404/405 behavior. |
| `GET /api/sessions/:id/events` | SOW-0107 + SOW-0109 consumer | Paged flat chronological op/event list for `table=events` when all-detail is not the data source. | SOW-0107 producer contract and SOW-0109 secondary event-table consumer; stable cursor, standard GET/HEAD/404/405 behavior, and no `/trace` fallback after lightweight mode is chosen. |
| `GET /api/sessions/:id/ops/:opId` or equivalent lookup | SOW-0107 + SOW-0113 consumer | Legacy op deep-link repair to owning turn/focus when lightweight data is active. | SOW-0113 URL repair and op-to-turn resolver contract. |
| `GET /api/sessions/:id/topology/workbench` | SOW-0111 | Turn-level recursive topology/stress graph. | SOW-0111 graph contract and SOW-0113 highlight resolver integration. |
| `GET /api/sessions/:id/stats` | SOW-0112 | Current-session-subtree statistics and heatmap rows. | SOW-0112 statistics contract and SOW-0113 URL/filter/highlight integration. |
| `GET /api/sessions/:id/highlights/:source/:key` | SOW-0113 | Server-key highlight resolver for `stat` and `topology` keys. | SOW-0113 canonical resolver schema, retry/cache/stale behavior, and source-owned resolver functions. |

Existing test disposition required before implementation:

| Existing test area | Target disposition |
|---|---|
| `TraceTab`, `Waterfall`, `ByTurnWaterfall`, `EventList` tests | Migrate useful assertions into workbench waterfall/table tests; delete tests that only cover removed nested controls. |
| `FlameGraph` tests | Delete with Flame if first-pass baseline removal stands; otherwise SOW-0110 must add retained-mode requirements before implementation planning. |
| `TopologyTab` / `TopologyRenderer` tests | Preserve or migrate; add a regression that global `/topology` remains agent-only when workbench topology mode is added. |
| `TimelineTab` / `TimelineRenderer` tests | Extract Canvas a11y mirror/list assertions for SOW-0110 before deleting timeline tests, unless Timeline is retained. |
| `OverviewTab` / `TurnsTab` tests | Delete or migrate reusable summary/table assertions into `OverviewTiles` and SOW-0109 turn-table tests. |
| `LogsTab` / `RawDataTab` tests | Keep or add focused tests for the retained table modes. Current `RawDataTab` has no dedicated test; new coverage must include section switching, copy behavior, and readable dark/light rendering. |
| `SpanDetailDrawer` tests | Delete only after replacement content-pane selection tests prove the drawer flow is gone and an automated pre-deletion check proves no imports remain. The required check is a focused grep over `frontend/src/` for `SpanDetailDrawer`, barrel imports from `components/SpanDetailDrawer`, and direct exported `SpanDetail` type imports from `components/SpanDetailDrawer/SpanDetailDrawer`, excluding the drawer directory itself until the deletion commit. The drawer `index.ts` must stop exporting `SpanDetail`, and `frontend/vitest.coverage.mjs` `PER_DIR_GLOBS` or equivalent coverage entries must remove the deleted drawer directory in the same change. |
| Legacy URL/link tests | SOW-0113 owns updating existing assertions in `frontend/src/components/SessionRow/SessionRow.test.tsx` and `frontend/tests/viz-sse.spec.ts` when the URL contract changes. This umbrella records them here so legacy `?tab=` behavior is not lost during the shell refactor. |

Layout, control, and state contract required before implementation:

- Minimum tested viewport for the desktop workbench: 1366x768. At widths below
  1100px and down to the 1024px first-pass support floor, the workbench uses a
  stacked/narrow layout: header and controls stay fixed at the top,
  visualization comes first, table/raw comes second, and content becomes a
  full-width pane below the table instead of an overlay. The stacked layout may
  collapse secondary stats/controls into overflow menus, but it must not expose
  overlapping panes or random mid-visualization scrollbars.
- At 1366x768, the implementation plan must name target minimums for the
  visualization height, table height, and content width and prove them with
  Playwright screenshots or geometry assertions before implementation is called
  ready. Intermediate widths from 1100px through 1365px stay in the desktop
  three-pane model only if the computed content width remains at least 480px and
  table/visualization pane minimums are met. Otherwise the same stacked/narrow
  layout used below 1100px applies; the first implementation must not create a
  third "squeezed drawer" mode between desktop and stacked layouts.
- The controls ribbon is owned by this umbrella. Child SOWs contribute controls
  through one shared workbench contract; they do not create nested control
  islands. The implementation plan must define the contract before child SOW
  implementation planning proceeds:
  - controls are grouped by active `view` and `table` mode;
  - each contribution has stable id, label, order, disabled/loading state, and
    typed setter callback;
  - the parent workbench renders all controls in the ribbon and owns wrapping,
    overflow, keyboard order, and responsive layout;
  - child views may expose control descriptors or slot components, but may not
    render persistent duplicate toolbars inside visualization/table panes.
- The first implementation should prefer a descriptor contract over arbitrary
  child-rendered slots unless a child proves it needs custom rendering. The
  planned TypeScript shape must be at least:

  ```ts
  type WorkbenchControlContribution = {
    id: string;
    scope: "global" | "view" | "table";
    owner: "waterfall" | "topology" | "stats" | "turn-table" | "logs" | "raw";
    label: string;
    order: number;
    disabled?: boolean;
    loading?: boolean;
    kind?: "select" | "toggle" | "action" | "range";
    value?: string | number | boolean;
    options?: Array<{ value: string; label: string }>;
    min?: number;
    max?: number;
    step?: number;
    setValue?: (next: string | number | boolean) => void;
    onAction?: () => void;
  };
  ```

  The implementation plan may refine names, but it must preserve the same
  ownership, ordering, disabled/loading, value, action, and typed-setter
  semantics. Select/toggle/range controls provide `setValue`; range controls
  must provide numeric `min`, `max`, and `step`; action controls provide
  `onAction` and no persistent value. Topology controls such as fit-to-view are
  action controls, while freeze/layout/metric controls remain toggle/select
  controls unless a child SOW proves a real range control is needed. The
  callbacks exposed by a contribution must be stable across
  renders. The parent workbench must not invoke a child setter/action during
  render or from a layout effect; component tests must cover that controls do
  not create render loops. Render-loop test strategy: mount the workbench with a
  mocked control contribution whose visible value changes across controlled
  parent updates, plus one action contribution, then assert with a render counter
  or React Profiler wrapper that callback and descriptor identity remain stable
  except when visible control state changes. More than a plan-defined small
  render count for two deliberate state changes fails the test.
	- Controls overflow strategy is part of the contract, not visual polish. At
	  desktop widths the ribbon may wrap to a second compact row; it must not create
	  horizontal scrolling. Below 1100px, secondary mode-specific controls collapse
	  behind one accessible More/menu control while primary view/table mode controls
	  remain directly reachable. First-pass primary controls are visualization mode
	  and table/raw mode; secondary controls are metric/group, step/status/subagent
	  filters, zoom/range, and view-specific toggles unless a child SOW proves a
	  control is needed for basic navigation. The implementation plan must name the
	  exact breakpoint and keyboard order. The overflow control is a disclosure button
  with `aria-expanded`; the revealed panel uses `role=group` and normal button,
  checkbox, select, and range semantics. Do not use an ARIA menu pattern unless
  the plan proves the contained controls really are menu items.
- The controls render-loop test is a Vitest + React Testing Library component
  test using a render counter or React Profiler wrapper. Playwright may cover the
  keyboard path, but the render-loop assertion belongs in component tests.
- Controls-ribbon keyboard behavior is umbrella-owned: mode switching between
  visualization values (`waterfall`, `topology`, `stats`) and table values
  (`turns`, `events`, `logs`, `raw`) must be reachable without tabbing through
  every row/bar/cell. The implementation plan must define the keyboard model
  and add component tests for wrapping/overflow, collapsed controls, tab order,
  disabled/loading states, and mode switching.
- Mode switchers use one tab stop per segmented group or native select. Arrow
  keys move within the group, Enter/Space activates the focused option, Home/End
  jump to the first/last option when the chosen widget supports them, and focus
  stays in the controls ribbon after activation unless the user moves it.
- When `view=stats`, the Statistics surface uses the visualization area for the
  primary heatmap/summary visualization and the table/raw area for grouped
  metric tables. Normal `table=<turns|events|logs|raw>` modes are disabled or
  visibly inactive while the stats view owns that table area, unless the
  implementation plan proves a clearer split. URL handling is owned by SOW-0113:
  `view=stats&table=<turns|events|logs|raw>` preserves the table param for
  round-trip/back-navigation but ignores it for rendering while stats is active;
  the table control shows a disabled/derived "statistics tables" state instead
  of pretending the preserved table mode is active.
- `Logs` and `Raw` remain table modes in the bottom area. If later evidence
  proves they are not useful, their removal requires an explicit SOW decision.
- `table=events` is retained as a secondary flat session-subtree op/event list
  mode for chronological inspection, not as the primary table. Row clicks follow
  the shared selection contract: rows with an op set `sel=turn:<turn-id>` plus
  `focus=op:<op-id>`; rows without an op use local row focus only and do not
  replace the selected turn. Data source: canonical `ops` rows for the current
  session subtree, ordered by stable timestamp/turn/op sequence with
  deterministic tie-breaking. Under the all-detail path, the table may derive
  rows from the measured all-detail response. Under the lightweight path,
  retained `table=events` uses `GET /api/sessions/:id/events`; if that endpoint
  is not implemented, `table=events` is omitted/disabled rather than silently
  reusing retired `/trace` data. First-pass shape is a paged workbench event
  envelope:
  `{ rows: [{ id, session_id, turn_id?, op_id?, ts, seq?, kind, name, status,
  error_class?, duration_us?, display_label, source }], next_cursor?,
  total_estimate?, generated_at }`, scoped to the current session subtree and
  using the standard error envelope, HEAD parity, and stable cursor/tie-breaker
  rules. The SOW-0107 API measurement must explicitly decide whether events reuse
  all-detail data, use `/api/sessions/:id/events`, or are removed/disabled for
  the lightweight first pass before SOW-0109 plan review closes. First-pass
  cursor encoding is a stable composite cursor over
	  `(last_ts, last_turn_seq, last_op_seq, last_id, generated_at)`; same-session
	  `session_changed` invalidates existing event cursors and forces a fresh first
	  page for the next explicit events query. The UI follows the live-insertion
	  policy: it shows a "new data available" marker and does not jump from the
	  user's current events scroll/page to page 1 until the user refreshes, changes
	  mode, or requests another page. Events paging controls live inside the
	  table/raw pane, not as extra global-ribbon controls: first-pass controls are
	  keyboard-reachable `Load more`, `Refresh`, and a compact "newer data
	  available" affordance with loading/disabled states and polite live-region
	  text. When a cursor is invalidated, the current visible page remains on
	  screen with the marker; the next `Load more`, explicit `Refresh`, mode
	  change, or table re-entry starts from a fresh first page instead of appending
	  from the stale cursor. `table=turns` remains the default and only primary
	  turn-first table.
- `table=logs` is a diagnostic log-row mode. A log row that carries a stable
  turn/op target may set the same `sel=turn` plus `focus=op` pair; otherwise it
  uses local row focus and leaves the content pane unchanged. First-pass data
  source is direct current-session logs only:
  `log_entries.session_id = <viewed-session-id>`, ordered by timestamp/id. Logs
  are labelled "current session logs" so users do not confuse them with recursive
  subtree diagnostics. Child-session logs are reached by navigating to the child
  session; a recursive subtree log collection requires a later endpoint/spec
  delta. `table=logs` is not a replacement for canonical ops. Logs must be
  paginated or virtualized from the first implementation. Preferred path is to
  reuse the existing paginated session-logs endpoint and render pane-local
  `Load more` / `Refresh` controls with disabled/loading states; loading every
  log row for a large session into the workbench DOM is rejected.
- `table=raw` is a read-only diagnostic metadata mode for the current Session
  Detail response: session metadata, display labels, source metadata, extras,
  raw ids, payload-ref metadata/proof fields, and API envelope fields render as
  a safe JSON/tree view. It does not fetch or inline payload bodies, source
  snapshot bytes, or recursive child raw records in the first pass. Row/tree
  expand/collapse and copy are local UI actions, copy safe text only, and never
  mutate `sel`, `focus`, or server-key highlights. Existing highlights may still
  render where the table can map them to visible rows. If operators need raw
  source snapshot bytes later, that is a separate SOW with source-read and
  sensitive-data contracts. Raw rendering has a first-pass safety budget: the
  JSON/tree renderer virtualizes or truncates after a plan-defined cap
  (planning baseline: 1 MiB serialized metadata or 10,000 visible tree rows)
  and shows a clear truncation indicator with copy/export limited to the loaded
  safe metadata subset. When the user is viewing a child session,
  `table=raw` shows raw metadata for the currently viewed URL session id, not
  the root or sibling subtree, matching the child-session scope rule.
  Under the lightweight turn-summary contract, `table=raw` shows that slim
  envelope plus any selected-turn detail already loaded by the workbench; it
  must not trigger an unbudgeted full-detail fetch just to populate diagnostics.
  `table=raw` is a diagnostic operator mode, not a public/share-safe export. It
  may show raw source metadata and `extras_json` as stored for debugging. If the
  implementation chooses to sanitize values inside this raw tree, it must reuse
  the SOW-0108 shared sanitizer and label the view as sanitized; inventing a
  weaker raw-mode-only filter is rejected.
- Loading/error/empty states use existing status-view components where possible
  and are defined per workbench area, not as one page-level spinner.
- Partial failure behavior is per-area: a table failure must not blank the
  header, visualization, or content; a topology/stats resolver failure must not
  blank the table; and a selected-turn detail failure must show a retryable
  content-area error without clearing valid visualization/table state. Each
  area gets its own compact retry affordance and error text using sanitized
  messages.
- Runtime rendering failures are also per-area. The header/stats, controls,
  visualization, table/raw, and content areas each sit behind a React error
  boundary so a D3/canvas/table/content render exception cannot blank the whole
  Session Detail route. The boundary renders the same compact area-local error
	  state and retry affordance as fetch failures, with sanitized error text.
  Component tests mount each area with a throwing child, verify the compact error
  state and retry affordance, and verify sibling areas remain rendered and
  interactive. Content-area retry re-renders the same selected turn/focus first;
  if retry throws again, the valid selection is preserved and the content area
  shows a persistent error state with a "clear selection" action that returns to
  the no-selection summary. Error-boundary retry must not silently clear or
  change selection.
  The controls ribbon also protects each child control contribution or descriptor
  group with a local boundary. A throwing topology metric selector, stats group
  selector, or table-mode contribution must render only that control's compact
  error state while the view/table mode switchers and unrelated controls remain
  interactive. A full ribbon-level failure is treated as a P0 implementation
  defect unless the failing code is genuinely shared shell code.
	- If the viewed session itself disappears, becomes inaccessible, or returns `404`
	  during a re-fetch, the workbench enters a route-level "session no longer
	  available" state with a link back to the sessions list. It must not loop
	  loaders, keep stale content as if valid, or show unrelated generic area errors.
	  Breadcrumbs collapse into this route-level unavailable state; stale ancestor
	  links are not rendered or revalidated after the viewed session returns `404`.
- The content pane's no-selection state is umbrella-owned: it renders a compact
  session summary/empty state using existing summary/status components. SOW-0109
  owns selected-turn rendering, not the default no-turn content.
- The shared large-session performance fixture/generator is umbrella-owned
  because SOW-0109, SOW-0110, SOW-0111, and SOW-0112 all depend on the same
  1000-turn / 10k-op seeded session tree. The fixture must be deterministic,
  sanitized, ingestible into SQLite, include a multi-depth child-session subtree,
  and be reusable by Go presenter tests, frontend component tests, and
  Playwright/performance smoke tests.
- The fixture generator and committed fixture are the first concrete
  implementation artifacts of this umbrella SOW. Child SOW performance tests
  must not start until `scripts/gen-large-session-fixture.sh` and
  `testdata/fixtures/large-session-v1/` exist or the child SOW records an
  explicit smaller-fixture exception for non-performance behavior. A smaller
  fixture may prove correctness only; any performance budget or renderer-scale
  claim made before the shared fixture hash is recorded is provisional and cannot
  close the child SOW.
- The fixture generator must first prove that ai-agent v3 source format can
  express every required scenario: depth-4 child subtree, >=500 ops in one turn,
  >=200 ops in one turn, failures, payload refs, empty/success/error turns, and
  `agentId` placeholder cases such as `parent` / `spawn-parent`. If v3 cannot
  express a scenario cleanly, the umbrella records the limitation and permits a
  separate tiny fixture for that scenario before child SOWs depend on it. If the
  exception scenario is part of a child SOW performance budget, the tiny fixture
  must also carry a recorded performance threshold measured on the reference
  workstation; correctness coverage alone is not enough for performance-risk
  scenarios.
- Fixture format: `scripts/gen-large-session-fixture.sh` generates a compressed
  sanitized source fixture under `testdata/fixtures/large-session-v1/` from a
  deterministic seed. First-pass source format is ai-agent v3 because it can
  exercise session trees, turn/ops, payload refs, and the `parent` /
  `spawn-parent` label cases needed by SOW-0108; additional adapter-specific
  tiny fixtures remain allowed. The fixture package also provides a reproducible
  SQLite test snapshot or helper-generated clone for backend performance tests,
  so Go tests do not pay source-ingest cost inside every benchmark. The
  committed fixture may be compressed; the generator is the authoritative repair
  path. The shape must include at least one depth-4 child-session subtree, at
  least one selected-turn candidate with 500 or more ops, at least one turn with
  200 or more ops, representative tool/subagent fan-out, failures, payload refs,
  and empty/success/error turns. Child SOWs may add scenario-specific tiny
  fixtures, but their large-session performance tests must use this shared
  fixture. The first committed fixture hash is recorded in this SOW and child
  SOWs; regenerating the fixture requires re-measuring every budget that cites
  it.
  For the SOW-0109 content stress path, the 500 threshold is measured as
  rendered `TurnStep`/step-card instances after UI grouping, not merely raw
  `ops` rows. The fixture generator or verifier must fail if the selected stress
  turn produces fewer than 500 rendered step instances in the planned renderer,
  unless SOW-0109 records an equivalent higher-risk stress case and budget.
  The fixture also needs non-zero per-turn failure and child-spawn examples so
  the lightweight turn-summary `failure_count` and `child_spawn_count` fields
  cannot be satisfied by all-zero placeholders.
  Before the production renderer exists, the verifier may use a named
  renderer-planning mapper that converts op kinds/names into expected
  `TurnStep` instances; the later SOW-0109 component test must prove the
  production renderer follows the same count. A raw op count alone is never
  enough evidence for the content stress path.
  Fixture consumption is explicit: Go presenter benchmarks load the generated
  SQLite snapshot/helper clone; frontend component tests use serialized API
  fixtures derived from the same snapshot; Playwright/performance tests run the
  dev server against that same SQLite. The generator records its own SHA-256 and
  the git commit/ref used to generate the fixture beside the fixture manifest, so
  budget evidence is tied to both data and generator code.
- Fixture inventory owned by the umbrella:

  | Fixture | Owner | Target path | Purpose |
  |---|---|---|---|
  | large session performance tree | SOW-0107 | `testdata/fixtures/large-session-v1/` plus `scripts/gen-large-session-fixture.sh` | shared 1000-turn / 10k-op performance and layout budgets |
  | v3 identity/title edge cases | SOW-0108 | `testdata/fixtures/session-identity-v1/` | `parent` / `spawn-parent`, title-like fields, first-user hash writer |
  | deep-link stability | SOW-0113 | `testdata/fixtures/deeplink-stability/` | turn/op id stability across ai-agent v2, ai-agent v3, claude-code, codex, opencode |
  | topology collapse/stress | SOW-0111 | child file under `testdata/fixtures/large-session-v1/` or a named tiny fixture if v3 cannot express the edge case | depth/collapse/fan-out/stress resolver behavior |
  | stats grouping/error classes | SOW-0112 | child file under `testdata/fixtures/large-session-v1/` or a named tiny fixture if needed | grouped stats, heatmap, `error_class`, source-table counts |

  The first implementation must add a hash verification step for the shared
  large-session fixture. If the recorded hash in this SOW or child SOWs differs
  from the on-disk generated fixture hash, performance claims are invalid until
  the budgets are re-measured or the fixture is regenerated from the recorded
  seed. The implementation plan must also add a lightweight verifier, either in
  `.agents/sow/audit.sh` or a named script under `scripts/`, that fails when a
  child SOW claims a shared-fixture performance budget without recording the
  fixture hash it measured. This is a SOW/process gate, not a runtime endpoint.
  The verifier reads the recorded fixture hash, computes `sha256` over the
  generated/committed fixture manifest or fixture files named by the child SOW,
  and exits nonzero with a concise diff when they differ. The deterministic
  baseline is a manifest file listing every fixture file in sorted relative-path
  order with byte length and per-file SHA-256; the recorded fixture hash is the
  SHA-256 of that manifest's canonical bytes. It is a local SOW/process gate
  before closing child SOWs; CI inclusion can be added when the fixture lands,
  but a verifier failure invalidates any performance claim. The verifier also
  fails when a child SOW claims a shared-fixture performance budget without
  recording the fixture hash it measured.
- Live insertion policy is shared workbench behavior, owned here and consumed by
  SOW-0113/child views: when a watched source appends new turns/ops while a
  valid turn/op is selected, the selected turn's table/content scroll anchor is
  preserved; new work appends without jumping the user; waterfall/topology/stats
  may mark that newer data is available and rescale only on an explicit refresh,
  fit-to-view, or mode change unless the selected item disappears.
  If the selected turn itself receives new ops or payload metadata, the content
  pane shows the same compact "new work available" marker near the selected-turn
  heading, preserves the content scroll anchor, and updates only on explicit
  refresh, navigation, or a user action that accepts the new data.
- If no turn/op is selected and the user is scrolled away from the newest work,
  live insertion preserves the current table/content scroll position and shows a
  "new work available" state instead of jumping to the bottom. If the user is
  already pinned to the newest visible work, appends may keep the tail pinned.
- With active filters or stat/topology highlights, "new work available" counts
  only newly matching visible rows/bars for the current filter/highlight state.
  New non-matching work may update compact totals but must not jump the table or
  visualization.
  Indicator form is shared, not per-child ad hoc: the first pass uses one
  compact badge or inline text adjacent to the affected area's heading/controls,
  using the design-system neutral/info token. It is not a toast, modal, overlay,
  or full-width banner. The marker clears on explicit refresh, on view/table mode
  change that re-queries the affected area, or when the user navigates/scrolls to
  the newest matching work. Counts are for newly matching visible rows/bars under
  active filters/highlights, not total new source rows. The marker respects
  `prefers-reduced-motion`; no pulse/bounce animation is allowed when reduced
  motion is active.
  Breadcrumbs participate in the same debounced re-fetch. When `session_changed`
  fires for the viewed session or a session in the viewed subtree, the next
  accepted re-fetch includes the `ancestors[]` payload. Newly-discovered parent
  links, repaired broken chains, or updated display labels appear in the
  breadcrumb on that re-fetch; stale breadcrumb segments must not remain pinned
  after the route data refreshes.
	- Live-update transport planning baseline: use the existing `session_changed`
	  event plus debounced REST re-fetch and client-side matching-count recompute
	  on the shared fixture. If that misses the workbench interaction budget, the
	  implementation plan must add an explicit `sse-protocol.md` delta for a
	  server-side matching-count/new-work event before child views depend on the
	  count. SOW-0113 owns server-key highlight cache invalidation and debounced
	  re-resolution on `session_changed`; the umbrella's matching-count recompute
	  must reuse that state rather than keeping stale stat/topology highlight
	  memberships. Current spec evidence: `presenter.md` and `sse-protocol.md`
	  define `session_changed` as a notification carrying session identity
	  (`session_id`, plus `root_session_id`/`ts_us` in the SSE spec), not changed
	  row ids or matching counts. That is sufficient only for cache invalidation,
	  debounced REST re-fetch, and client-side matching-count recompute. Child views
	  must not invent their own SSE semantics.
- Single-flight/coalescing is a shared pattern, not copy/paste glue. SOW-0113
  owns the frontend resolver cache and in-flight request behavior for highlight
  keys. If SOW-0111 topology or SOW-0112 statistics add backend or frontend
  single-flight for heavy endpoint requests, the implementation plan must either
  reuse a shared helper/hook or justify a separate implementation with measured
  differences. Duplicating cache invalidation and `resync` handling across
  children without a named reason is rejected.
- Layout-key reset behavior: the new first-pass layout key is
  `ai-viewer.session.workbench.v1`. If old `ai-viewer.session.vright` or
  `ai-viewer.session.vbottom` keys exist on first load, the workbench shows one
  compact dismissible notice that the layout was redesigned and old split sizes
  were discarded. The plan must decide whether old keys are removed after the
  notice or simply ignored, and must test the chosen behavior.
- New workbench components use the existing design-system tokens and CSS custom
  properties from `.agents/sow/specs/design-system.md`; new CSS modules must not
  introduce hard-coded hex/rgb colors except for documented data-visualization
  palettes in a named `frontend/src/viz/*` color module with light/dark tests.
  Heatmap and topology stress scales must be legible in both themes and meet at
  least WCAG 2.2 non-text contrast 3:1 for non-text selection/failure/stress
  indicators against the visualization background.
  The same design-system delta defines the shared "new work available" indicator
  pattern so table, waterfall, topology, and stats do not invent different
  badges.
- `prefers-reduced-motion` is an umbrella accessibility requirement. Graph
  force-layout animation, scroll-anchor smoothing, canvas zoom tweens, and any
  layout transition added by the workbench must disable non-essential animation
  when the user requests reduced motion. Selection/focus changes still happen
  instantly and remain visible. Child SOWs consume this paragraph as the
  umbrella reduced-motion contract and must not define a weaker local rule.
- If layout decision 3 chooses fixed responsive proportions for the first pass,
  Session Detail stops using `react-resizable-panels` in the redesigned shell.
  The dependency is removed only if a repo-wide import audit proves no remaining
  surface uses it; otherwise it remains for other surfaces and old Session Detail
  persistence keys are ignored or removed per the chosen reset behavior. The
  implementation plan must record the repo-wide audit command and result before
  changing the dependency, for example an `rg` search for
  `react-resizable-panels` imports across `frontend/src`, tests, and package
  manifests.
- The `ui-turn-view.md` change is a rewrite/replacement of the SOW-0088
  Session Detail unified-shell section, not a narrow wording edit. SOW-0107 owns
  the full shell-section rewrite, including replacing the stale standalone
  step-renderer table with the actual `TurnStep` dispatcher model; SOW-0109 only
  adds turn-table/content-specific deltas to the rewritten section.
- SOW-0107 may delete Timeline UI code only after SOW-0110 has extracted or
  preserved the Timeline Canvas accessibility mirror/list pattern in a named,
  tested reusable form, expected first-pass target
  `frontend/src/viz/canvasA11yMirror.tsx` or an equivalent reviewed module.
  Deleting Timeline first is a sequencing violation. SOW-0110's extraction step
  may land as a standalone pre-shell change before this umbrella shell refactor
  removes Timeline; that is the intended ordering and is not circular.
  If extraction from `TimelineRenderer` proves too tightly coupled, SOW-0110
  must implement an equivalent tested Canvas accessibility mirror for the
  waterfall before Timeline code is deleted.
	- New frontend component directories created by the workbench refactor must
	  update `frontend/vitest.coverage.mjs` in the same implementation commit
	  (`PER_DIR_GLOBS` and `PER_DIR_LINES`) or explicitly prove they are covered by
  an existing gated directory. The coverage-config verifier must not be allowed
	  to fail after the refactor. The implementation plan must run the coverage
	  config verifier before adding new workbench directories and resolve existing
	  contradictions, including any directory listed as both measured/gated and
	  excluded, so new work does not compound stale coverage drift. If
	  `SpanDetailDrawer/` is deleted, remove its `src/components/SpanDetailDrawer/**`
	  entry from `PER_DIR_GLOBS` in the same commit; deletion without coverage-config
	  cleanup is a gate failure.
- Required spec-delta outline before tests/code:
  - `ui-turn-view.md`: replace the SOW-0088 three-zone resizable shell with the
    five-area workbench; define the compact title/stats ribbon, controls ribbon,
    visualization/table/content panes, stacked/narrow layout, selection owner,
    step-kind filter enum, resolver consumption, no-sidebar rule, and per-area
    loading/error/empty/stale states.
  - `ui-pages.md`: replace the stale Session Detail tabbed-layout entry with
    the five-area workbench route, record the first-pass mode inventory
    (`waterfall`, `topology`, `stats`; `turns/events/logs/raw` table modes),
    remove/deprecate the `SpanDetailDrawer` detail-flow description when the
    component is deleted, and delegate identity/title wording to SOW-0108.
  - `rest-api.md`: record the chosen Session Detail data-loading shape after
    measuring the current all-turns/all-ops `GET /api/sessions/:id` response on
    the shared fixture. If a turn-summary or selected-turn endpoint/mode is
    needed, define it before SOW-0109 or SOW-0113 implementation planning.
  - `sse-protocol.md`: record any workbench-specific live-update semantics the
    redesign relies on, especially stale selection, new-work markers, and
    matching-count signals for active filters/highlights.
  - Child SOW specs: SOW-0108 owns title/agent-label REST/data-model/canonical
    deltas; SOW-0109 owns turn-table/content deltas; SOW-0110 owns waterfall
    geometry/a11y deltas; SOW-0111 owns topology/stress/resolver deltas;
    SOW-0112 owns stats/resolver deltas; SOW-0113 owns selection/URL/resolver
    consumption deltas.
  - `rest-api.md` stress ownership: SOW-0113 creates the "Stress metrics and
    dimensions" section if it does not already exist and writes only the URL
    enum skeleton. SOW-0111 is the first writer of metric definitions and
    topology meanings. SOW-0112 extends the same section with stats row shapes
    and resolver source-table counts. No child SOW may create a competing stress
    glossary.
- SOW-0113 is the hard prerequisite for SOW-0109, SOW-0110, SOW-0111, and
  SOW-0112. Those child implementation plans must not start until the selection
  and URL contract is approved or explicitly scoped.
- The `ui-turn-view.md` shell-section rewrite is also a prerequisite for
  SOW-0109 and SOW-0113 implementation-plan finalization. Child plans must not
  cite the old sidebar/drawer contract.
- SOW-0107 implementation-plan step 6, the Session Detail API measurement on the
  shared fixture, is a hard prerequisite for SOW-0109 and SOW-0110 plan
  finalization. That measurement decides whether child SOWs consume the existing
  all-detail payload or a new turn-summary / selected-turn contract; child data
  loading sections cannot finalize until the result is recorded.
  If the measurement chooses a lightweight turn-summary/selected-turn contract,
  the plan must also define the cheap legacy `op_id -> turn_id` resolution path
  required by SOW-0113 before preserving `?op=` links: either an op lookup
  endpoint, an op-to-turn index embedded in the turn-summary response, or an
  explicit visible degradation for legacy op links. Loading all ops only to
  resolve a cold legacy op link is rejected unless the performance measurement
  proves it stays within the shared budget.
- SOW-0107 open decision 3 (fixed proportions vs retained resizing) and target
  pane proportions must be recorded before SOW-0110 plan review. SOW-0109,
  SOW-0111, and SOW-0112 may draft independent data/API plans earlier, but any
  geometry proof waits for that decision.
- The child-session subtree-scope rule above is a prerequisite for SOW-0109,
  SOW-0110, SOW-0111, and SOW-0112 plan review. A child SOW may add an explicit
  comparison/root-tree mode, but its default must stay aligned with the current
  viewed session subtree or show a visible scope indicator.
- The breadcrumb ancestor-chain data contract is a prerequisite for child-link
  implementation in SOW-0109, SOW-0110, and SOW-0111. Child views may render
  links before the breadcrumb exists only in isolated unit tests; the integrated
  workbench cannot accept child navigation until the header can display the
  server-provided ancestor chain without N full-detail REST calls.
- Server-key highlight resolution is part of that prerequisite, not an optional
  child detail. SOW-0113 owns the shared URL/React consumption path; SOW-0111
  owns topology-key production and resolver semantics; SOW-0112 owns stat-key
  production and resolver semantics. Child plans must not use
  `hi=stat:<key>` or `hi=topology:<key>` until the matching resolver can map the
  key back to bounded turn/op/node highlight targets.
- Migration chain-head coordination is umbrella-owned for the UI SOW package:
  only the highest-numbered migration present in the branch sets
  `presenter.SchemaVersion` and owns the chain-head migration test. Planned
  landing order is `0012_display_title`, `0013_display_agent_label`,
  `0014_display_client_label`, `0015_ops_error_class_index` when needed, then
  any `0016_*` topology-support migration. If SOWs land in a different order,
  the landing SOW updates the single chain-head constant and chain-head test in
  the same commit; lower-numbered migrations must not keep stale tests asserting
  that their own number remains the chain head. If multiple branches/SOWs touch
  migrations concurrently, the landing SOW rebases on the latest master, renumbers
  or updates the next migration if needed, and owns resolving `SchemaVersion` /
  chain-head test conflicts before merge.
- Layout persistence strategy: the first redesign does not read
  `ai-viewer.session.vright` or `ai-viewer.session.vbottom`. If it persists any
  workbench layout state, it uses the new key
  `ai-viewer.session.workbench.v1`. The first implementation ignores old keys
  rather than migrating their proportions; it does not delete old localStorage
  keys unless a focused compatibility test proves deletion is harmless.

Area state ownership:

| Area | Owner | Loading | Error | Empty | Stale/live update |
|---|---|---|---|---|---|
| Header/stats/breadcrumb | SOW-0107 shell, with labels from SOW-0108 | compact status skeleton | source/session status warning | compact session metadata fallback | "new work available" count only |
| Controls | SOW-0107 ribbon, descriptors from child SOWs | disabled controls with loading affordance | invalid filter/mode repair message | no secondary controls for mode | controls stay stable |
| Visualization | active child SOW (`waterfall`, `topology`, `stats`) | viewport-sized loading surface | graph/waterfall error state | empty graph/waterfall state | new matching work marker |
| Table/raw | SOW-0109 for turns/events, SOW-0107 for raw/log mode placement | table skeleton | table/query error row | empty table with current filters | anchor preserved, matching-count marker |
| Content | SOW-0107 for no-selection summary, SOW-0109 for selected turn | compact session summary fallback | selected content load error | no-turn selected summary | selected-anchor preserved or stale cleared |

Accessibility contract required before implementation:

- Keyboard focus moves predictably from visualization/table controls to the
  persistent content area after selection.
- Cross-area tab order is explicit and tested: global app topbar first, then the
  Session Detail header/stats/breadcrumb and pin action, controls ribbon,
  visualization area, table/raw area, and content area. The stacked layout keeps
  the same logical order even when panes move vertically.
- Selection changes announce one short sanitized status update through a polite
  live region, for example selected turn number/status or highlighted match
  count. The announcement must not include raw prompt or payload text.
- Turn rows, visualization nodes/bars, and statistics rows expose keyboard
  selection behavior.
- The replacement for the drawer documents ARIA role/label behavior and no
  longer depends on modal focus trapping.
- Graph-heavy views provide a text/list alternative or equivalent keyboard
  traversal for the same selection targets.

Sensitive data handling plan:

- Do not write raw prompts, tool outputs, payload contents, private paths, or
  source-specific session IDs into durable artifacts. Use counts, dimensions,
  and component/file evidence only.

Implementation plan:

1. Finalize product goals and layout decisions with the user.
2. Run external gap-analysis reviewers for this SOW gate.
3. Resolve round-1 reviewer findings and rerun the gap-analysis gate.
4. Define the controls-ribbon contribution contract, content-pane no-selection
   owner, Raw/Logs table-mode test plan, retained visualization inventory,
   live-insertion behavior, and shared large-session fixture/generator contract.
5. Create the deterministic shared large-session fixture/generator before child
   performance tests depend on it.
5a. Implement the shared subtree session-id resolver/helper owned by this
    umbrella SOW, with tests for root scope, child-session scope, broken parent
    chains, cycles, depth cap, cache invalidation hooks, and reuse by at least
    one presenter endpoint test. Child backend implementation plans may not
    close before this helper exists or is explicitly scheduled as their first
    consumed dependency.
5b. Align persisted failure-count rollups with the shared terminal-failure
    family before topology/statistics acceptance depends on them. The plan must
    name every writer/query touched, including `internal/ingest/aggregates.go`
    and any turn-level rollup writer, update `data-model.md`/related specs,
    add backfill or reprocess coverage, and prove SOW-0111/SOW-0112 consume the
    same persisted predicate rather than a local workaround. Discovery is a
    repo-wide predicate audit, not a memory exercise: search for raw SQL and Go
    forms such as `status = 'failed'`, `status='failed'`,
    `StatusFailed`, `failureInc`, and `failure_count` across `internal/ingest`,
    `internal/presenter`, and migration/backfill tests. Known non-obvious sites
    to include in the plan are `internal/ingest/catalog_finalize.go`,
    `internal/ingest/catalog_migrate.go`, `internal/presenter/stats.go`, and
    `internal/presenter/stats_breakdowns.go`; presenter raw-SQL predicates do
    not automatically inherit stored-rollup changes. If global `/api/stats`
    remains intentionally narrower than the workbench failure family, that
    divergence must be documented in specs and a follow-up SOW before this
    prerequisite can be considered closed.
5c. Before SOW-0111 and SOW-0112 both implement 128-bit `selection_key` /
    aggregate-key hashing, the implementation plan must either create one
    shared backend helper for URL-safe hash output, duplicate detection,
    deterministic collision suffixing, and the >64-collision fail-closed test
    seam, or record why separate implementations are safer. Silent duplicate
    hash/collision logic across topology and statistics is rejected.
6. Measure current `GET /api/sessions/:id` and waterfall data-source options on
   the shared fixture. Record whether SOW-0109/SOW-0113 use the existing detail
   payload or a lightweight turn-summary/selected-turn contract, and whether
   SOW-0110 uses `/trace`, all-detail `/sessions/:id`, or the summary/detail
   split. If `/trace` remains in use, the plan must state how it becomes
   current-subtree scoped for child sessions and how it receives SOW-0108 display
   labels and child-summary projections.
   The measurement is valid only under a recorded quiet-host condition. The
   plan samples local contention before each run, records reference-workstation
   load/process conditions, discards and retries runs taken while parallel
   reviewer/build/agent load is present, and marks budgets provisional if the
   workstation cannot be quieted. First-pass quiet-host threshold: no known
   reviewer/build/test process running for this repo, 1-minute load average
   below 25% of logical CPU count, and at least 4 GiB available memory before
   each measured run. If those checks are not available on the workstation, the
   plan records the substitute command and why it is equivalent. If the
   workstation cannot meet the quiet threshold after three attempts, the plan
   records the observed load, marks the performance result provisional, and does
   not claim the budget as validated until a quiet run is obtained. A separate
   optional noisy-ingest smoke can be recorded, but quiet-host budgets remain the
   acceptance baseline.
6a. Measure combined initial workbench load on the same fixture and host:
    header/session envelope, chosen table data source, chosen waterfall data
    source, topology workbench endpoint when enabled, and stats endpoint when
    enabled. The implementation plan records first interactive state and total
    settled wall-clock time, plus a plan-defined combined budget. Planning
    baseline: first interactive workbench state within 800ms and fully settled
    initial visible workbench within 1500ms on the recorded quiet reference
    workstation. Changing those numbers requires the plan to record evidence and
    risks before child plans rely on it. Area-local loading may remain
    progressive, but the first screen must not issue an unbounded request burst.
    "Fully settled" means all visible-area loading indicators are gone, all
    default-view fetches have completed, and no initial resolver promises remain.
    Planning baseline: the initial workbench load does not preload topology or
    statistics data. Topology/stats endpoint timing is recorded separately when
    those views are switched in. If the final shell intentionally preloads them,
    the combined load budget must be re-measured and recorded before claiming the
    800ms/1500ms target. The same
    run records peak JS heap where the browser exposes it and heap delta after
    100 synthetic or fixture-backed `session_changed` updates plus route unmount,
    proving D3/Canvas/SSE listeners and observers are cleaned up.
6b. Measure cross-area selection latency on the same fixture: one click on a
    turn row/bar updates table selection, waterfall emphasis, and content pane
    focus/rendering within the slowest relevant child-area budget plus at most
    50ms coordination overhead. A selected-turn content fetch may show an
    area-local loading state, but selection indication and URL state must update
    immediately. Measurement separates shared selection-owner work
    (click-handler start to canonical URL/state commit) from child-area render
    work (state commit to visible table/waterfall/content update), so the 50ms
    coordination overhead is falsifiable.
7. Define the child-session navigation and breadcrumb contract in the workbench
   spec before SOW-0109, SOW-0110, or SOW-0111 implement child links. This
   includes the server-provided ancestor-chain data source, ordering, fallback
   labels, cycle/depth protection, and tests proving child navigation shows the
   breadcrumb without fetching every ancestor's full session detail.
8. Update `ui-turn-view.md`, `ui-pages.md`, `rest-api.md`, and
   `sse-protocol.md` with the target session workbench and its API/live-update
   dependencies.
   The `sse-protocol.md` pass must reconcile field names used by workbench live
   update code with the current notification contract, including whether the
   timestamp field is `ts`, `ts_us`, or both during compatibility. Child views
   must consume the reconciled name rather than invent local SSE payload shapes.
8a. Update `.agents/sow/specs/design-system.md` with the workbench token and
    geometry contract: 96px title/stats ribbon, controls wrapping/spacing,
    pane minimums, stacked breakpoint, data-viz color-scale rules, reduced
    motion behavior, and the `ai-viewer.session.workbench.v1` persistence key.
9. Add Playwright geometry tests for 1024x768 stacked layout plus 1366x768,
   1440x900, and 1600x1000 desktop layout before implementation.
10. Add focused events/logs/raw table-mode coverage, including the existing
   `RawDataTab` as `table=raw`, plus the existing test-file disposition table
   above.
11. Refactor `SessionDetail` + `UnifiedView` into a five-area workbench:
   title/stats ribbon, controls ribbon, visualization pane, table pane, content
   pane.
12. Replace Session Detail's `SpanDetailDrawer` usage with content-pane
   selection rendering, then delete/archive the drawer component and tests if no
   non-Session-Detail consumers remain. Before deletion, run the focused import
   audit described in the test disposition table and record the command output
   in the SOW evidence. This import audit is also a hard prerequisite before
   SOW-0109 or SOW-0110 writes new components, so child work cannot accidentally
   depend on a component scheduled for deletion.
13. Delete/archive `OverviewTab` and `TurnsTab` after SOW-0109's turn table and
   content pane land; delete/archive `StatsTab` placeholder after SOW-0112's
   statistics mode is tested; delete/archive `TimelineTab`/`TimelineRenderer`
   only after SOW-0110's Canvas a11y mirror extraction or equivalent passes.
   If any component is retained, the implementation plan must record the
   remaining consumer and why it belongs in the redesigned workbench.
14. Normalize visualization sizing and scroll ownership for the first-pass
   retained modes: turn-first Waterfall, Topology, and Statistics. Timeline,
   Detailed Waterfall, and Flame are not implementation targets unless the user
   explicitly chooses retention before plan finalization.
15. Reset, migrate, or deliberately ignore old `react-resizable-panels` persisted
   layout keys before enabling the redesigned shell. If fixed proportions remove
   the dependency from Session Detail, run and record the repo-wide import audit
   before removing the package or leaving it for other consumers.
	16. Run focused frontend tests, Playwright E2E/a11y, build, lint, and full gates
	   appropriate to the final scope. `scripts/build.sh` must record the main
	   gzipped chunk delta and prove the existing 500KB budget remains green after
	   new workbench components, Canvas helpers, and controls are added. If the
	   budget fails, the first mitigation is view-level code splitting for heavy
	   visualization modes (`waterfall`, `topology`, `stats`) with `React.lazy` /
	   dynamic import or the repo-equivalent pattern; the controls ribbon, selection
	   owner, and route shell stay in the main chunk.

Validation plan:

- Component tests:
  - URL state round-trips for selected visualization, table mode, selected op,
    and selected turn.
  - Content area renders selected op/turn without using `SpanDetailDrawer`.
  - Raw mode renders through the redesigned table/raw pane without relying on
    untested `RawDataTab` behavior.
  - Session Detail API size/latency on the shared fixture is measured and the
    chosen all-detail vs turn-summary contract is recorded.
  - Every per-area error boundary catches a thrown child, shows the compact error
    state with retry affordance, and leaves other areas interactive.
  - Session-unavailable/404 state renders the route-level unavailable state and
    sessions-list link.
- Playwright:
  - 1024x768 stacked layout and 1366x768 / 1440x900 / 1600x1000 desktop
    geometry assertions.
  - 1366x768 at 125% and 150% browser zoom or equivalent effective CSS viewport
    emulation, proving the stacked/narrow fallback has no overlap or hidden
    primary controls.
  - No main page scroller at 1366x768.
  - Exactly one primary scroll container per visualization/table/content area.
  - Clicking visualization span/table row updates content area and URL.
  - Clicking a child-session link from content, waterfall, and topology
    navigates to `/sessions/:childId`, shows the breadcrumb, clears session-local
    selection/highlights, scopes every workbench area to the child subtree, and
    browser Back restores the parent workbench.
  - Topology controls and any explicitly retained non-default visualization
    controls stay in the unified controls ribbon.
  - End-to-end workbench integration: load the shared fixture session, select a
    turn in the table, verify waterfall expansion and content rendering, switch
    topology and verify selection sync, switch statistics and resolve one
    highlight, then navigate to a child session and use Browser Back to restore
    the parent state.
  - Content scroll position is preserved when switching visualization modes while
    the selected turn is unchanged; it resets to top only when selected turn
    changes.
  - First-pass touch behavior is explicit. Either pinch/drag/tap are mapped to
    zoom/pan/select for touch-capable laptops/tablets, or the plan records touch
    as best-effort/out of scope with no hidden broken controls.
- Existing E2E/a11y:
  - deep-link, trace/waterfall, topology, drawer/content replacement, a11y, and
    retained-mode tests only for modes the user keeps.

Artifact impact plan:

- AGENTS.md: likely unaffected.
- Runtime project skills: likely unaffected unless a new frontend layout pattern
  becomes reusable.
- Specs: update `ui-turn-view.md` and `ui-pages.md`.
- End-user/operator docs: likely unaffected unless README screenshots or usage
  docs reference the old layout.
- End-user/operator skills: none expected.
- SOW lifecycle: this SOW stays pending until the user approves goals/options,
  then moves to current for spec/test/code work.

Open-source reference evidence:

- Not checked yet. The gap is product-specific and already grounded in local UI
  evidence. If implementation needs reference patterns, check observability and
  tracing UIs for dense session/span inspectors before plan finalization.

Open decisions:

The recommendations below are the planning baseline until the user approves or
overrides them. If the user approves this SOW without edits, the recommended
options become the recorded implementation defaults. Implementation must not
begin from this pending SOW until the chosen options are recorded in the SOW.

Downstream blockers are explicit so child SOWs do not infer defaults before the
operator approval gate:

| Decision | Blocks |
|---|---|
| 1. Content area default | SOW-0109 selected-turn content layout and no-selection handoff |
| 2. Controls ribbon model | SOW-0109/SOW-0110/SOW-0111/SOW-0112/SOW-0113 control contribution plans |
| 3. Layout resizing | SOW-0109 table/content geometry, SOW-0110 canvas/waterfall sizing, SOW-0111 topology visible-node geometry |
| 4. Table/raw area | SOW-0109 table-mode contract and SOW-0112 stats table-area ownership |
| 5. Timeline disposition | SOW-0110 Canvas a11y extraction/deletion sequencing |
| 6. Detailed/Flame disposition | SOW-0110 retained-mode contracts |

1. Content area default:
   - A. Selected turn summary + selected op detail in one panel.
   - B. Selected op detail only, with turn context as compact metadata.
   - C. Full selected turn render only.
   Recommendation: A, long-term-best. It answers "what happened here?" without
   losing turn context.
2. Controls ribbon model:
   - A. One adaptive ribbon whose controls change based on active visualization.
   - B. One always-visible superset of all controls.
   Recommendation: A, long-term-best. It avoids irrelevant controls while
   keeping controls in one predictable place.
3. Layout resizing:
   - A. Fixed responsive proportions in the first redesign, then optional resize
     handles later.
   - B. Keep resizable splits from the start.
   Recommendation: A, surgical. The current resize system contributed to
   random-feeling geometry; first stabilize the layout, reset/ignore the old
   persisted keys, then add resizing only where clearly useful.
   This decision blocks SOW-0109, SOW-0110, and SOW-0111 plan review wherever
   they need final pane geometry, canvas sizing, or visible-node budgets.
4. Table/raw area:
   - A. One bottom table with modes: Events, Logs, Raw.
   - B. Separate stacked tables.
   Recommendation: A, long-term-best. One table area is easier to scan and
   easier to test for scroll ownership.
5. Timeline disposition:
   - A. Fold timeline capability into the turn-first visualization model and
     remove the separate Timeline tab from the first redesign.
   - B. Keep Timeline as a separate visualization mode in the unified controls
     ribbon.
   Recommendation: A, long-term-best unless the current Timeline proves a
   distinct workflow not covered by waterfall/topology/statistics.
6. Detailed waterfall and Flame disposition:
   - A. Keep them as named visualization modes in the unified controls ribbon.
   - B. Remove them from the first redesign and keep only turn-first waterfall.
   Recommendation: B for the first redesign unless implementation planning
   proves a distinct operator workflow that the turn-first waterfall cannot
   satisfy. If A is chosen, the controls still live in the single ribbon and
   SOW-0110 owns the behavior.

## Implications And Decisions

Pending operator review. Implementation must not begin until the user confirms
or adjusts the goals and open decisions above.

## Plan

1. Use SOW-0107 as the umbrella design contract.
2. Run fit-for-purpose gap review on SOW-0108 through SOW-0113.
3. Resolve reviewer findings and operator decisions.
4. Execute child SOWs in dependency order:
   - Capture the reference workstation baseline and create the shared large-session
     fixture/generator before any child SOW claims performance budgets.
   - Land SOW-0110's Timeline Canvas a11y extraction before deleting Timeline UI
     code in the umbrella shell refactor.
   - SOW-0113 shared selection/URL contract.
   - SOW-0108 identity/title can run in parallel if it does not depend on the
     frontend shell, but SOW-0111 and SOW-0112 cannot verify backend acceptance
     criteria that group/label by `display_agent_label` until SOW-0108's
     migration/backfill or explicit non-storage decision lands.
   - SOW-0109 table/content after SOW-0107 records the data-loading contract.
   - SOW-0110 waterfall after SOW-0107 records layout decision 3 and pane
     proportions.
   - SOW-0111 topology and SOW-0112 statistics after the selection/stress
     contracts are stable and the shared fixture hash is recorded.
5. Close this umbrella only after all child SOWs are completed or deliberately
   re-scoped.

## Execution Log

### 2026-06-26

- Created SOW from the user's layout critique and headless Playwright evidence.
- No implementation code written for this redesign.
- Split the redesign into child SOWs SOW-0108 through SOW-0112 per operator
  request so each major feature gets separate fit-for-purpose analysis and
  reviewer gates.
- Added SOW-0113 after reviewer round 1 identified shared selection and URL
  state as a prerequisite owned by no child SOW.

## Validation

Acceptance criteria evidence:

- Pending.

Tests or equivalent validation:

- Pending.

Fixture status:

- Pending. `testdata/fixtures/large-session-v1/` and its first committed hash
  must be recorded here before child SOW performance-budget validation begins.

Real-use evidence:

- Headless screenshots and geometry measurements captured before planning.

Reviewer findings:

- Round 1 completed on 2026-06-26 using glm, minimax, kimi, mimo, deepseek, and
  qwen. All reviewers voted `NEEDS WORK`; P0/P1/P2 findings were incorporated
  into this SOW and child SOWs. Gap-review rerun is pending after these edits.
- Round 2 completed on 2026-06-26 using the same reviewer set. Successful
  reviewers again voted `NEEDS WORK`. Findings about header geometry, dead
  tabs, drawer removal scope, layout persistence, logs/raw disposition,
  controls persistence, and Timeline/Detailed/Flame ownership were incorporated.
  Gap-review rerun is still required.
- Round 3 completed on 2026-06-26 with usable reviews from glm, minimax, and
  deepseek; kimi/mimo produced no usable final review and qwen was interrupted
  after looping on the same read-only search. Findings about `SpanDetailDrawer`
  dead-code cleanup, Timeline ownership, `ui-turn-view.md` rewrite scope, the
  selected layout-persistence strategy, and the hard SOW-0113 dependency were
  incorporated. Gap-review rerun is still required.
- Round 4 completed on 2026-06-26 with usable reviews from glm, minimax, mimo,
  and qwen; kimi/deepseek sessions ended without usable final output. Findings
  about the controls-ribbon contribution contract, content-pane no-selection
  owner, RawDataTab test coverage, and child-SOW spec-edit sequencing were
  incorporated.
- Round 5 completed on 2026-06-26 with usable reviews from glm, kimi, mimo, and
  qwen; minimax/deepseek did not produce usable final votes. Findings about the
  shared 1000-turn/10k-op fixture, live-insertion behavior, and first-pass
  Timeline/Detailed/Flame baseline disposition were incorporated.
- Round 6 completed on 2026-06-27 with all six reviewers returning usable
  output. Kimi, DeepSeek, and Qwen voted clean; GLM, Mimo, and Minimax found
  additional P1/P2/P3 gaps. Incorporated findings about server-key highlight
  resolution ownership, no-selection live insertion, formal control descriptor
  shape, required `ui-pages.md` update, stacked/narrow layout, drawer consumer
  enumeration/deletion, fixture creation, and first-pass mode inventory.
- Round 7 completed on 2026-06-27 with usable reviews from GLM, Kimi, Minimax,
  and DeepSeek; Mimo and Qwen did not produce retrievable final votes after
  process-handle loss. Incorporated findings about exact layout persistence key,
  fixture format/shape, 1024x768 stacked-layout testing, active-filter live
  insertion, per-area states, and explicit spec-delta outlines.
- Round 8 completed on 2026-06-27 with all six reviewers returning usable
  output. Incorporated findings about Session Detail API payload-size
  measurement, `rest-api.md` and `sse-protocol.md` ownership, fixture
  creation as the first umbrella artifact, RawDataTab focused-test ownership,
  below-1024 scope, SpanDetailDrawer spec cleanup, and per-area owner
  assignment.
- Round 9 completed on 2026-06-27 with usable reviews from GLM, Kimi, Minimax,
  Mimo, and DeepSeek; Qwen did not produce a retrievable final vote after
  process-handle loss. Incorporated findings about live-insertion transport
  baseline, turn-summary endpoint ownership, statistics zone layout, existing
  test-file disposition, SpanDetailDrawer deletion checklist, fixture format/
  hash/performance mechanics, controls-ribbon keyboard tests, UI spec rewrite
  prerequisite, child-SOW sequencing, and SOW-0108 dependency for topology/stats
  display labels.
- Round 10 completed on 2026-06-27 with usable reviews from GLM, Minimax,
  Mimo, Qwen, and DeepSeek; Kimi exited without a usable vote. Incorporated
  findings about child-session navigation and breadcrumbs, existing legacy URL
  test ownership, `StatsTab` disposition, `SpanDetail` export checks before
  drawer deletion, controls callback stability, layout-key reset UX, and v3
  fixture expressiveness proof.
- Round 11 completed on 2026-06-27 with usable reviews from GLM, Minimax,
  Mimo, Qwen, and DeepSeek; Kimi again exited without a usable vote.
  Incorporated findings about reference workstation capture, partial failure
  behavior, the fallback turn-summary/detail API trigger and draft shape,
  Timeline a11y extraction as a hard gate, and frontend coverage-config updates
  for new workbench directories.
- Round 12 completed on 2026-06-27 with positive votes from Minimax, Qwen, and
  DeepSeek, and usable needs-work findings from GLM, Kimi, and Mimo. Incorporated
  concrete umbrella findings about backend endpoint disposition, controls
  render-loop test strategy, performance thresholds for exception fixtures,
  `ui-turn-view.md` ownership, named Timeline Canvas a11y extraction target,
  coverage-config preflight cleanup, and the API-measurement hard gate for
  SOW-0109/SOW-0110. The reviewer finding that operator open decisions must
  already be answered is not patched because those decisions require user
  approval before implementation, not assistant invention during gap analysis.
- Round 13 completed on 2026-06-27 with positive votes from Minimax, Kimi, Mimo,
  Qwen, and DeepSeek. GLM's sole P2 finding concerned SOW-0113
  metric/group-bound server-key highlights; the umbrella SOW received no new
  actionable findings beyond recording the round state.
- Round 14 completed on 2026-06-27 with usable `NEEDS WORK` reviews from GLM,
  Kimi, Mimo, Qwen, and DeepSeek; Minimax failed technically after initial
  output and will be rerun with the full batch. Accepted umbrella findings
  clarified definitive child-session state clearing, falsifiable API/payload
  measurement methodology, computed `duration_us` / `failure_count` semantics
  for the fallback turn-summary API, controls overflow and render-loop testing,
  `react-resizable-panels` dependency fate, the layout decision's downstream
  blocking effect, and the cross-view Playwright integration test.
- Round 15 completed on 2026-06-27 with usable `NEEDS WORK` reviews from GLM,
  Minimax, Kimi, Mimo, and DeepSeek; Qwen failed technically and will rerun with
  the full batch. Accepted umbrella findings clarified child-session subtree
  scoping across all areas, trace/waterfall data-source disposition, stress
  glossary ownership, fixture inventory and hash verification, automated drawer
  deletion/import audits, repo-wide `react-resizable-panels` audit ownership,
  cross-area tab order, selection live-region announcements, and mode-switching
  keyboard behavior.
- Round 16 completed on 2026-06-27 with clean votes from DeepSeek, Qwen, and
  Mimo, usable `NEEDS WORK` reviews from GLM and Minimax, and a technical Kimi
  failure before final vote. Accepted umbrella findings added the missing
  breadcrumb ancestor-chain data contract and action-button support in the
  controls contribution descriptor, including fit-to-view style controls and
  render-loop tests for action callbacks.
- Round 17 completed on 2026-06-27 with accepted umbrella findings clarifying
  ancestor-chain depth cap/fail-closed behavior, `view=stats` table-param
  rendering, fixture-hash verifier ownership, reference-workstation/fixture
  sequencing, and non-circular Timeline Canvas a11y extraction ordering.
- Round 18 completed on 2026-06-27 with accepted umbrella findings clarifying
  legacy `?op=` resolution under the lightweight data-loading contract,
  quiet-host measurement preconditions, fixture-hash verifier mechanics,
  design-system/token requirements, reduced-motion behavior, and the
  design-system spec delta owned by the workbench shell.
- Round 19 completed on 2026-06-27 with accepted umbrella findings clarifying
  `effective_status` in the header, the lightweight workbench metadata envelope,
  `last_activity_ts`, `direct_child_count`, canonical failure-count predicate,
  the `ops/:opId` lookup owner, SSE delta ownership, 1024px scroll behavior,
  content scroll ownership, events/logs/raw table semantics, range controls,
  overflow disclosure semantics, per-area React error boundaries, WCAG data-viz
  contrast, shared single-flight ownership, quiet-host thresholds, and Timeline/
  dead-tab cleanup sequencing.
- Round 20 completed on 2026-06-27 with clean votes from Mimo and DeepSeek,
  `NEEDS WORK` votes from GLM and Minimax, and technical Kimi/Qwen failures
  without final votes. Accepted umbrella findings clarified the concrete
  `effective_status` and `last_activity_ts` definitions, child-summary data
  required by the lightweight API, `ops/:opId` producer/consumer ownership,
  events/logs/raw data sources, deterministic fixture hashing, shared
  new-work indicator shape, migration chain-head coordination, content-pane
  minimum width, combined initial-load measurement, and explicit old-tab cleanup
  sequencing. A full reviewer rerun was required before implementation planning;
  later execution-log entries record the subsequent reruns.
- Round 21 completed on 2026-06-27 with clean votes from Kimi and Qwen, usable
  `NEEDS WORK` reviews from GLM, Mimo, and DeepSeek, and a Minimax technical
  timeout before final vote. Accepted umbrella findings clarified computed
  geometry proof, intermediate 1100-1365px layout behavior, concrete
  `table=events` producer contract, 500 rendered-step fixture validation,
  combined initial-load baseline budgets, and cross-area selection-latency
  measurement.
- Round 22 completed on 2026-06-27 with accepted umbrella findings clarifying
  the child-SOW blocker matrix, first-pass browser/performance scope, shared
  fixture generation/consumption/hash verification, per-area error boundaries,
  viewed-session unavailable state, new-work marker lifecycle, combined load and
  heap-cleanup budgets, bundle-size gate evidence, `table=events` cursor/source
  ownership, `SpanDetailDrawer` import audit, and quiet-host fallback handling.
- Round 23 completed on 2026-06-27 with accepted umbrella findings clarifying
  broken ancestor-chain behavior, primary/secondary control overflow ownership,
  `table=events` cursor invalidation UX, content error-boundary retry semantics,
  route-unavailable breadcrumb behavior, current `session_changed` evidence,
  `SpanDetailDrawer` coverage-config deletion, and bundle-size code-splitting
  mitigation.
- Round 24 completed on 2026-06-27 with accepted umbrella findings clarifying
  breadcrumb producer ownership, compact stats overflow contents, shared subtree
  resolver/helper ownership, `table=events` paging/cursor invalidation controls,
  current-session-only `table=logs`, metadata-only `table=raw`, selected-turn
  new-work markers, shared-fixture performance invalidation, rendered-step
  fixture verification, theme coverage, and precise `SpanDetailDrawer` import/
  coverage cleanup. The Mimo migration-number finding was rejected as stale
  because SOW-0111 already references `0015`/`0016`.
- Round 25 completed on 2026-06-27 with accepted umbrella findings clarifying
  browser zoom/effective-width behavior, per-response `effective_status` /
  `generated_at` consistency, explicit shared subtree resolver ownership,
  lightweight data-contract fallback when measurement cannot complete,
  turn-summary `op_count`/`failure_count`/`child_spawn_count` sourcing, logs
  pagination, raw rendering budgets, per-control error boundaries, non-zero
  fixture failure/child-spawn cases, SSE timestamp-name reconciliation, and zoom
  Playwright coverage.
- Round 26 completed on 2026-06-27 with accepted findings aligning
  `failure_count` with the shared terminal-failure family, clarifying
  `table=raw` child-session scope, breadcrumb refresh on `session_changed`, and
  the first-pass no-preload baseline for topology/statistics during initial
  workbench load.
- Round 27 completed on 2026-06-27 with accepted findings assigning explicit
  ownership for failure-rollup predicate alignment, adding legacy topology
  endpoint dispositions, clarifying `child_spawn_count` as the turn-summary
  alias of `subagent_call_count`, strengthening reduced-motion cross-references,
  and pinning `table=raw` behavior under the lightweight envelope.
- Round 28 completed on 2026-06-27 with accepted findings adding the new
  endpoint inventory, pinning raw diagnostic privacy/sanitization boundaries,
  expanding the failure-predicate audit to catalog and presenter raw-SQL sites,
  and requiring a shared-hash-helper decision before topology/statistics duplicate
  selection-key logic.
- Round 29 completed on 2026-06-27 with one accepted cross-SOW P2: SOW-0111 now
  mirrors SOW-0112's degraded display-label branch when SOW-0108 is delayed or
  rejected. P3 wording findings were swept in the child SOWs before rerun.
- Round 30 completed on 2026-06-27 with one accepted P1: retained
  `table=events` under the lightweight contract now has an explicit paged
  `GET /api/sessions/:id/events` producer contract, or the mode must be
  omitted/disabled instead of reusing retired `/trace` data. DeepSeek's
  `table=raw` observation was rejected as stale because this SOW already states
  raw uses the slim envelope plus loaded selected-turn detail under lightweight
  mode.
- Round 31 completed on 2026-06-27. GLM, Minimax, Kimi, DeepSeek, and Qwen voted
  `NOTHING MORE CAN BE DONE`. Mimo voted `NEEDS WORK`, but its blockers were
  rejected as phase-boundary false positives: SOW-0107 open decisions are
  intentionally recorded operator sign-off gates, SOW-0108 v3 identity proof is
  intentionally an implementation-plan evidence gate, and SOW-0107 step 5b
  already owns the verified failure-predicate alignment work before
  topology/statistics acceptance.

Same-failure scan:

- Pending.

Sensitive data gate:

- Durable artifacts contain only dimensions, counts, and repo-relative file
  evidence. No prompts, tool outputs, secrets, personal data, private endpoints,
  or raw payloads are recorded.

Artifact maintenance gate:

- AGENTS.md: no update yet.
- Runtime project skills: no update yet.
- Specs: planned, not yet updated for target implementation.
- End-user/operator docs: no update yet.
- End-user/operator skills: no update expected.
- SOW lifecycle: pending.

Specs update:

- Pending.

Project skills update:

- Pending.

End-user/operator docs update:

- Pending.

End-user/operator skills update:

- Pending.

Lessons:

- The previous "unified" design removed top-level tabs but left too much nested
  per-view chrome and scroll ownership inside the page.

Follow-up mapping:

- SOW-0108: session identity/title/data integrity.
- SOW-0109: turn table and content rendering.
- SOW-0110: turn waterfall.
- SOW-0111: topology stress map.
- SOW-0112: statistics.
- SOW-0113: shared selection, URL state, and cross-view synchronization.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated
`## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend
regression content above the original SOW narrative.
