# SOW-0110 - Turn-First Waterfall Workbench

## Status

Status: open

Sub-state: fit-for-purpose gap analysis drafted; external gap review rounds 1
through 28 completed and findings incorporated; gap-review rerun pending after
round-28 changes.

## Requirements

### Purpose

Make the waterfall explain time at the turn level first, then reveal the nested
ops for the selected turn. It must use the same turn selection model as the
table and content area.

### User Request

- Waterfall should be multi-level: one bar per turn at top level.
- Clicking a turn expands/nests the waterfall.
- Clicking a turn also expands/nests table and shows the turn in the content
  view, exactly like table row selection.

### Assistant Understanding

Facts:

- Current Waterfall renders a large op-level surface.
- `ByTurnWaterfall` already implements the core idea of aggregated turn bars
  that expand into the selected turn's ops. The gap is promotion, integration,
  sizing, and synchronization, not a greenfield renderer.
- Large sessions can produce huge canvas/SVG scroll areas.

Inferences:

- Default waterfall should be turn bars.
- Expanded turn should reveal nested op bars in that turn only, not the whole
  session's op list at once.
- Subagent calls should be represented as boundary/request-response spans, not
  recursive inline expansion inside the selected turn's waterfall.

Unknowns:

- SOW-0113 owns whether selected turn/op state is single-select or multi-select.
- The nested timing contract must explicitly distinguish global session time
  from local turn-relative time.

### Acceptance Criteria

- Default waterfall renders one bar per turn.
- Selecting a turn from waterfall updates table expansion and content area.
- Selecting a turn from table updates waterfall expansion and content area.
- Expanded waterfall shows nested op timing for the selected turn.
- It is visually clear which time is turn-level and which bars are op-level.
- The top-level turn bars use the session/global time scale. Expanded op bars
  use a clearly labeled turn-relative nested scale, with enough context to avoid
  misleading duration comparisons.
- Running-session axis behavior is explicit. If the session `end_ts` is null,
  the top-level waterfall right edge uses the server-provided latest activity
  timestamp for the fetched snapshot, not the browser's continuously advancing
  wall clock. Null-duration running turns/ops render from their start timestamp
  to the snapshot latest-activity edge with a visible running marker. The axis
  does not animate continuously; it refreshes on explicit refresh, fit-to-view,
  mode change, or accepted `session_changed` re-fetch per SOW-0107/SOW-0113.
  Whichever data source SOW-0107 measurement chooses for waterfall must expose
  that session-level `last_activity_ts` or equivalent snapshot timestamp for the
  currently viewed session. When the user has navigated to a child session, this
  means the viewed child session's timestamp, not the root/parent session's
  timestamp. If
  `/trace` remains the source, `/trace` must be extended or bounded-co-fetched
  to provide it; if the lightweight summary path is chosen, the slim
  `/turns` envelope provides it.
  The axis label states that the right edge is the fetched snapshot/latest
  activity time, not the browser's current wall clock.
  If `end_ts` is null and `last_activity_ts` is absent, zero, or earlier than
  `start_ts`, the waterfall must not use Unix epoch time or the browser wall
	  clock. When `start_ts` is available, the axis right edge is `start_ts + 1ms`
	  solely to render a minimum visible running-start marker plus a "no activity
	  recorded yet" label. If `start_ts` is also unavailable, the visualization shows
	  a no-time-data state instead of drawing a misleading bar.
	  Loading and fetch-error states use SOW-0107's per-area state contract, rendered
	  as a lightweight canvas/visualization overlay that preserves the allocated
	  workbench layout instead of drawing an empty or stale waterfall.
- Expanded op bars include a visible `relative to selected turn` axis/label.
  Turn-level bars and op-level bars cannot share unlabeled axes.
  If the selected turn has zero duration but contains one or more ops, the
  relative-time axis uses a minimum visible width (planning baseline: at least
  40px) with an explicit `turn duration 0ms` label. Op bars are sized/placed
  against that minimum axis and remain selectable; they must not disappear due
  to a zero-width scale.
- Bars shorter than the visible minimum still render with at least a 2px hit
  target and show duration text/tooltip so micro-ops are not invisible.
  Width rounding is explicit in the implementation plan; first-pass behavior is
  `Math.max(2, Math.ceil(rawPixelWidth))` for non-zero durations so sub-pixel
  bars stay hittable without disappearing.
  Completed turns or ops where `start_ts == end_ts` also render as minimum-width
  markers with visible `0ms` duration text; they must not disappear.
- Existing Detailed/By-turn/Flame controls are either removed from the primary
  flow or rehomed into the unified controls ribbon by SOW-0107; no nested
  waterfall-only menus remain.
- Canvas/SVG size is derived from the visualization pane viewport and virtualized
  row count, with no random middle scrollbars.
- Large sessions remain responsive.
- Performance budget for plan drafting: on the same 1000-turn / 10k-op seeded
  fixture used by SOW-0109, default turn-level waterfall first paint is under
  500ms, selecting/expanding one turn is under 250ms, and pan/zoom/hover stays
  visually responsive without layout shift. Pan/zoom/hover handlers target one
  frame of work (about 16ms on the reference workstation) or use
  throttling/debouncing so pointer interaction remains visibly responsive. First
  pass does not persist waterfall zoom/pan in URL or localStorage; zoom/pan state
  resets when leaving the waterfall view and returning, unless implementation
  planning deliberately adds persistence and tests it.
- Reduced-motion behavior consumes the umbrella SOW-0107 contract: waterfall
  zoom tweens, scroll smoothing, hover transitions, and expand/collapse easing
  must disable non-essential animation when `prefers-reduced-motion: reduce` is
  active while preserving visible selection/focus state.
- Clicking a turn calls the SOW-0113 shared selection owner. The old local
  `ByTurnWaterfall` expanded-state toggle cannot remain the source of truth.

## Analysis

Sources to check during implementation:

- `frontend/src/pages/SessionDetail/TraceTab/Waterfall.tsx`
- `frontend/src/pages/SessionDetail/TraceTab/ByTurnWaterfall.tsx`
- `frontend/src/pages/SessionDetail/TimelineTab/TimelineRenderer.tsx`
- `frontend/src/pages/SessionDetail/TimelineTab/TimelineRenderer.test.tsx`
- `frontend/src/viz/trace.ts`
- `frontend/src/pages/SessionDetail/UnifiedView`
- `frontend/tests/viz-trace.spec.ts`
- Existing waterfall/event-list test disposition is recorded in SOW-0107's
  test-disposition table; this SOW consumes that table before moving, deleting,
  or replacing legacy tests.

Current state:

- Waterfall has two separate modes and many filters inside the visualization
  pane.
- The view is op-first, not turn-first.
- Existing large-session screenshot showed huge scroll height in the waterfall
  scroller.
- `ByTurnWaterfall` already provides useful turn-rollup behavior but is hidden
  behind a secondary control path and does not own the shared selection/content
  synchronization model.

External gap review round 1 findings incorporated:

- All reviewers voted `NEEDS WORK`.
- Reviewers found that the draft mischaracterized the waterfall as greenfield.
  `ByTurnWaterfall` already implements turn bars plus selected-turn op
  expansion, so the SOW now focuses on promotion and fit-for-purpose integration.
- Reviewers found the shared selection contract duplicated with SOW-0109; SOW
  0113 now owns it.
- Reviewers required explicit fate for detailed waterfall, flame graph, and
  nested waterfall controls.
- Reviewers required an explicit global-vs-local time-scale contract and
  subagent boundary span definition.

External gap review round 2 findings incorporated:

- Reviewers found `ByTurnWaterfall` hard-codes row/label/track dimensions. This
  SOW now requires those dimensions to derive from the visualization pane.
- Reviewers found local expanded state would break shared table/content
  selection. SOW-0113 must own selected/expanded turn state.
- Reviewers found Detailed/Flame disposition still unowned. SOW-0107 now owns
  mode inventory; this SOW owns behavior only for retained waterfall/flame
  modes.
- Reviewers found subagent boundary metadata and turn-relative scale labeling
  were too vague. They are now explicit implementation-plan requirements.

External gap review round 3 findings incorporated:

- Reviewers found pane-derived dimensions still needed derivation rules and
  minimum bounds.
- Reviewers found subagent boundary timing could mislead if it shows only the
  short parent spawn op while the child session runs much longer.
- Reviewers found the waterfall needed its own performance and a11y contracts,
  not only generic validation notes.
- Reviewers found retained Flame/Detailed modes need conditional requirements if
  SOW-0107 keeps them.

Risks:

- Rewriting waterfall layout can regress trace performance and a11y.
- Multi-level rendering needs clear keyboard/focus behavior.
- Time-scale choices can be misleading if global vs local scaling is unclear.

## Pre-Implementation Gate

Status: needs-review

Problem / root-cause model:

- The current waterfall exposes too many low-level ops at once and hides the
  operator's first question: which turn matters?
- The current implementation already has a by-turn renderer, but it is treated
  as an internal mode instead of the default mental model and is not synchronized
  with the table/content workbench.

Evidence reviewed:

- User feedback.
- Current `TraceTab` Waterfall/By-turn split.
- Headless measurement showing large inner waterfall scroll area.

Affected contracts and surfaces:

- Waterfall visualization, turn selection URL state, table/content
  synchronization, Playwright viz tests, a11y waivers.

Existing patterns to reuse:

- Existing trace tree layout helpers.
- Existing Canvas threshold/windowing logic.
- Existing `ByTurnWaterfall` as the starting point for the default waterfall.

Waterfall behavior contract required before implementation:

- Default view is turn-first.
- Single selected turn is expanded in the first implementation unless SOW-0113
  deliberately chooses multi-select.
- Top-level turn bars preserve global session timing.
- Expanded op bars use a nested turn-relative scale labeled as relative time.
- Row height, label width, track width, and canvas/SVG viewport size are derived
  from the actual visualization pane dimensions, not fixed constants such as
  720px track width.
- Dimension derivation rules:
  - `gutter = 16px` unless the implementation consumes a named design-system
    spacing token with the same effective size;
  - `trackWidth = max(240px, paneWidth - labelWidth - gutter)`;
  - `labelWidth = clamp(160px, 24% of paneWidth, 260px)`;
  - row height remains stable per mode and does not grow from label wrapping;
  - if the pane cannot satisfy minimum label+track width, labels truncate and
    expose full text via accessible title/tooltip rather than creating a
    horizontal inner scroller.
  - The implementation plan must include at least one worked geometry example
    for the 1366x768 target viewport, using the actual post-redesign pane width.
    This is sequenced after SOW-0107 fixes first-pass pane proportions; if those
    proportions are still undecided, this SOW cannot complete plan review.
  - If `design-system.md` has no named tokens for these values, the SOW-0110
    spec delta must either add the tokens or record the px values above as
    first-pass local visualization constants with tests. The constants must not
    stay as unexplained magic numbers in the renderer.
- Canvas/SVG resize contract: pane dimensions come from `ResizeObserver`;
  Canvas rendering accounts for `devicePixelRatio`; resize redraws are
  throttled through `requestAnimationFrame`; tests cover the threshold where SVG
  switches to Canvas for `turns + expanded ops`. First-pass threshold: switch to
  Canvas when visible turn bars plus expanded op bars exceed 500 rendered bar
  elements, unless implementation measurements justify a different threshold.
  The threshold is a planning baseline; implementation planning must record the
  measured SVG and Canvas behavior on the shared fixture hash from SOW-0107
  before hard-coding or changing it.
- Canvas mode does not make every painted bar a DOM tab stop. Instead, Canvas
  mode must provide an equivalent keyboard/a11y path: a synchronized offscreen
  or adjacent DOM outline/list using the same SOW-0113 selection contract for
  visible turn/op targets, with text duration/status/failure values and the same
  Enter/Space selection behavior. SVG mode may make individual bars focusable.
  The implementation plan must name the mirror/list shape, focus management,
  and how it stays synchronized with pan/zoom/filter state before Canvas mode is
  accepted. Existing Timeline Canvas accessibility mirror/list behavior is the
  reference implementation to extract or replicate before the Timeline tab is
  deleted by SOW-0107. The plan must sequence that extraction first if SOW-0107
  deletes Timeline code. SOW-0107's Timeline deletion is blocked until this
  extraction or equivalent tested preservation has landed. Named first-pass
  target: extract a reusable module such as
  `frontend/src/viz/canvasA11yMirror.tsx` that renders a synchronized
  button/list mirror for Canvas-visible items and can be tested without
  `TimelineRenderer`. Before extracting, the implementation plan must cite the
  current `TimelineRenderer.tsx` / `TimelineRenderer.test.tsx` mirror behavior:
  DOM list shape, focus behavior, synchronization with pan/zoom/filter state,
  and Enter/Space selection. The unit test must prove mirror item labels,
  Enter/Space selection behavior, and viewport/filter synchronization. SOW-0110
  owns the module creation and tests; SOW-0107 only consumes the passing test as
  a deletion precondition for Timeline code.
- SVG mode must also have a keyboard path. The implementation may use roving
  tabindex on SVG bars or the same synchronized mirror/list as Canvas mode, but
  Enter/Space selection, accessible names with turn/op label, duration, status,
  and visible focus state are mandatory. SVG cannot be accepted as pointer-only.
- The selected turn's expanded op section renders inline below the selected turn
  row in the first implementation, sharing the same vertical scroll surface.
- A subagent call renders as a boundary span using the parent session op's time
  extent. The implementation plan must either prove which child summary fields
  are available in the current response or add them through the presenter before
  rendering richer labels.
- Current waterfall code consumes `/api/sessions/:id/trace`, whose
  `internal/presenter/session_trace.go` `traceOp` shape carries op/session fields
  such as `session_id`, `session_agent_name`, and `session_kind`, but not the
  `childSummary` tree. Whichever data source SOW-0107 measurement chooses for
  waterfall (`/trace`, all-detail `/sessions/:id`, or the new summary/detail
  contract) must carry or co-fetch a child-summary projection sufficient for
  boundary annotation: child id, SOW-0108 display labels, raw `status`,
  `effective_status`, `last_activity_ts`, error, start/end, `duration_us`, cost,
  tokens, op/failure counts, direct child count, and child link. If `/trace`
  remains the source, this SOW must extend `/trace` or perform one bounded
  co-fetch; it must not discover the missing data mid-implementation. The
  implementation plan must cite the current `traceOp` fields before choosing the
  delta.
- If `/trace` remains the waterfall source, SOW-0110 must also satisfy the
  SOW-0107 child-session subtree-scope rule. The current trace route's
  root-resolution behavior cannot be used as-is for a child-session view unless
  the route adds a subtree scope parameter/behavior or the client filters with
  tested semantics before rendering. The same `/trace` extension or co-fetch
  must carry SOW-0108 display-safe session labels so `session_agent_name=parent`
  is not rendered in waterfall bars or event rows when
  `session_display_agent_label` exists.
- Current first-pass evidence is the presenter `childSummary` shape in
  `internal/presenter/session_detail.go`, which already carries child
  status/error, model/provider, timestamps, tokens, cost, op count, failure
  count, and nested children. Those fields are sufficient for first-pass
  boundary annotation after SOW-0108 extends child summaries with
  `display_agent_label` / `display_agent_label_source` and optional
  `display_client_label`. Additional labels beyond that require an explicit
  presenter/API scope change. First-pass boundary annotations omit `ctx_pct`
  unless the chosen waterfall data source explicitly carries model/token
  capacity for the child/session scope. If `ctx_pct` is added later, the plan
  must co-fetch or extend `childSummary`/`traceOp` with a scope label instead of
  guessing context pressure from incomplete data.
- Global workbench filters from SOW-0113 do not distort the top-level waterfall
  time axis. First-pass behavior: all turn bars remain in sequence; turns with
  matching ops are emphasized, non-matching turns are dimmed, and the expanded
  selected-turn section highlights matching ops. Hiding non-matching turn bars
  is rejected unless a later SOW introduces a distinct filtered-time-axis mode.
  The `step=` filter emphasizes turn bars that contain at least one matching
  op/step and dims turns with no matches. Expanded op bars apply the same
  StepFilter matching to individual ops/steps; non-matching op bars may dim or
  collapse behind a reversible control, but the top-level time axis is never
  structurally pruned by `step=`.
  The `status=` filter uses turn status for collapsed turn bars and raw op status
  for expanded op bars; session `effective_status` applies only to child-session
  boundary badges. The UI labels this scope so users do not mistake op filters
  for session-health filters. `status=stale` can match only child-session
  boundary badges in the waterfall because raw turn/op statuses cannot be
  derived as stale; if no boundary matches, the waterfall shows a compact "no
  stale child sessions" state while preserving the time axis. Zero-op turns
  remain visible as zero-width/minimum marker turn rows with status/duration
  text, not as missing gaps in the time axis. Expanding a zero-op turn renders
  the selected turn marker plus a visible nested-track label such as "no ops";
  it does not synthesize fake op bars.
  The `subagent=` filter is emphasis-based in the first pass:
  `with_subagents` emphasizes turns with at least one child-session boundary,
  `without_subagents` emphasizes turns without child-session boundaries, and
  `all` clears that emphasis. It does not prune the time axis.
  The shared `metric=` / `group=` controls are inactive for `view=waterfall` in
  the first pass. Waterfall width and ordering remain time-based. The unified
  controls ribbon hides or disables metric/group controls while waterfall is
  active, and SOW-0113 canonical URL emission removes inactive metric/group
  params on the next workbench write. Any future waterfall color-by-cost/token
  mode requires a separate view-local control and tests before it can consume
  the global metric enum.
- Clicking a subagent boundary selects the boundary in the content area and
  provides a link to the child session; it does not recursively inline the child
  waterfall. The link follows the umbrella/SOW-0113 child-session navigation
  contract: navigate to `/sessions/:childId`, show the breadcrumb, and let
  Browser Back restore the parent workbench state.
- Because the parent spawn op can be much shorter than the child session, the
  boundary span must visibly annotate child-session duration/status/cost when
  available and provide a clear jump/link. It must not imply that the short
  parent span is the full child runtime. These annotations are sourced from the
  parent session presenter `childSummary` fields after SOW-0108 adds display
  labels. If `childSummary` lacks child `end_ts`, `duration_us`, cost, or tokens
  because the child is still running or unavailable, the boundary shows the
  parent op duration with a visible "child session duration unavailable" state
  and omits child totals rather than loading the child session recursively.
  Running child boundaries render a visible `Running` status badge adjacent to
  the boundary label only when `effective_status` is `running`; stale children
  render the same stale/running distinction as the header/topology. They use
  `last_activity_ts` for snapshot-age text and `unavailable` text for
  duration/cost/token values that cannot be computed yet.
- Detailed op-level waterfall and flame graph are not nested controls inside
  the default waterfall. Their mode inventory is decided by SOW-0107; this SOW
  owns behavior only for modes retained there.
- First-pass baseline from SOW-0107 is removal of Detailed waterfall and Flame
  as primary modes. If the user overrides that baseline, this SOW must add the
  retained-mode contracts before plan review, not during implementation.
- If SOW-0107 keeps Flame or Detailed waterfall, this SOW must add mode-specific
  sizing, selection, a11y, and performance requirements before implementation
  planning proceeds.
- Waterfall a11y contract: turn rows and bars are keyboard focusable; Enter or
  Space selects; selected/focused state is visible without relying only on
  color; duration/status/failure values are available as text; bar/failure/status
  indicators meet at least WCAG 2.2 non-text contrast 3:1 against the
  visualization background, and failure/selection state is also encoded by
  stroke/shape/text, not hue alone. Expanded op bars inside the selected turn
  are keyboard reachable too: either each visible op bar is a focusable/selectable
  target with roving tabindex, or the visualization exposes a synchronized
  accessible mirror/list with the same Enter/Space selection behavior and
  `focus=op:<id>` emission. Pointer-only expanded ops are rejected. Tests must
  focus a turn bar, expand it, move to at least one expanded op target, and
  select it without using the mouse.

Risk and blast radius:

- Medium/high frontend viz risk.

Sensitive data handling plan:

- Visualization tests use sanitized fixture labels and generated data.

Implementation plan:

1. Consume SOW-0113's selected turn/op state and replace local expanded-turn
   state as source of truth.
1a. Consume SOW-0107's shared subtree session-id resolver/helper for the viewed
    session subtree when the selected waterfall data source needs subtree scope.
    This SOW must not create a parallel recursive subtree walker or CTE pattern.
2. Consume SOW-0107's Session Detail API measurement before choosing the
   waterfall data source. If the all-turns/all-ops detail payload misses the
   first-paint budget, this SOW uses the same lightweight turn-summary /
   selected-turn contract as SOW-0109 instead of assuming full detail data.
3. Spec turn-first waterfall behavior and consume SOW-0107's detailed/flame
   disposition.
4. Add pure layout tests for pane-derived sizing, turn rows, relative axis
   labels, minimum bar width, inline expanded nested ops, and child-session
   boundary annotation.
5. Extract or preserve the Timeline Canvas a11y mirror/list into the named
   reusable module before Timeline code can be deleted.
   If extraction from `TimelineRenderer` proves too tightly coupled, implement
   an equivalent tested Canvas a11y mirror for waterfall before Timeline code is
   deleted.
6. Add resize/canvas tests for `ResizeObserver`, `devicePixelRatio`,
   SVG/Canvas threshold, Canvas-mode a11y mirror/list, and redraw throttling.
7. Add Playwright selection synchronization and child-session link navigation
   tests.
8. Promote/refactor `ByTurnWaterfall` around selected/expanded turn state.
9. Validate performance and geometry across target viewports.

Validation plan:

- Pure geometry tests, component tests, Playwright, a11y, performance smoke.

Artifact impact plan:

- Specs: `ui-turn-view.md`, visualization a11y docs if behavior changes.
- SOW lifecycle: child of SOW-0107.

Open-source reference evidence:

- Potentially check Jaeger/Grafana/Chrome trace nested waterfall patterns before
  implementation plan.

Open decisions:

1. Expansion model:
   - A. Single selected turn expanded at a time.
   - B. Multiple turns expandable.
   Recommendation: A for clarity and performance in the first implementation.
2. Time scale:
   - A. Global scale for turn rows; local turn-relative scale for expanded ops.
   - B. Global scale for both turn rows and expanded ops.
   - C. Local scale for both levels.
   Recommendation: A, long-term-best. It preserves session context while making
   short-turn ops readable, as long as labels make the scale switch explicit.

Planning dependency:

- The worked 1366x768 geometry example depends on SOW-0107 open decision 3
  (fixed responsive proportions vs. retained resizing). SOW-0110 plan review
  cannot complete until SOW-0107 records that layout decision and target pane
  proportions.
- SOW-0107 open decision 6 (Detailed/Flame disposition) is also a plan-review
  blocker. If the operator keeps either mode, SOW-0110 must add retained-mode
  sizing, selection, accessibility, and performance requirements before code.

## Plan

1. Run external gap review.
2. Resolve findings and finalize expansion model.
3. Rerun the gap-analysis gate.
4. Draft implementation plan.

## Execution Log

### 2026-06-26

- Created focused SOW from waterfall feedback.
- Incorporated external reviewer round-1 findings: `ByTurnWaterfall` reuse,
  shared selection dependency, detailed/flame disposition, time scale contract,
  and subagent boundary span behavior.
- Incorporated external reviewer round-2 findings: pane-derived dimensions,
  shared-selection click ownership, relative axis labeling, minimum bar width,
  flame/detailed ownership, and subagent metadata sourcing.
- Incorporated external reviewer round-3 findings: dimension formulas,
  inline-expanded layout, waterfall render budget, subagent runtime annotation,
  conditional Flame/Detailed requirements, and explicit a11y contract.
- Incorporated external reviewer round-4 findings: hard-coded
  `ByTurnWaterfall` dimensions require full decoupling, canvas resize/redraw must
  account for `devicePixelRatio`, inline expanded ops share one scroll surface,
  and pan/zoom/hover need a numeric frame budget.
- Incorporated external reviewer round-5 findings: `gutter` is now defined,
  SVG-to-Canvas threshold has a first-pass value, color accessibility no longer
  references an undefined topology rule, and the implementation plan must include
  a worked 1366x768 geometry example.
- Incorporated external reviewer round-6 findings: Canvas mode now has an
  explicit keyboard/a11y fallback, and the 1366x768 geometry proof is sequenced
  after SOW-0107 locks first-pass pane proportions.
- Incorporated external reviewer round-7 findings: the SOW-0107 layout decision
  dependency is now explicit in the planning section.
- Incorporated external reviewer round-8 findings: pane-sizing constants must
  be tied to `design-system.md` tokens or recorded as tested first-pass
  visualization constants, Canvas-mode a11y mirror/list design is an explicit
  implementation-plan work item, and Detailed/Flame remain removed from the
  first-pass baseline unless the user overrides SOW-0107.
- Incorporated external reviewer round-9 findings: the existing Timeline Canvas
  accessibility mirror/list is the reference pattern for waterfall Canvas mode,
  and current child-summary presenter fields are sufficient for first-pass
  subagent boundary annotation unless richer labels are explicitly scoped.
- Incorporated external reviewer round-10 findings: Timeline Canvas a11y
  extraction must happen before Timeline deletion, waterfall data loading
  depends on SOW-0107's measured detail-payload budget, subagent boundaries
  consume SOW-0108 display labels, and child-session links use the shared
  navigation/breadcrumb contract.
- Incorporated external reviewer round-11 findings: SOW-0107 Timeline deletion
  is explicitly blocked until the Timeline Canvas accessibility mirror/list
  pattern has been extracted or equivalently preserved with tests.
- Incorporated external reviewer round-12 findings: the Canvas a11y extraction
  now has a named first-pass reusable module target and its own unit-test
  contract, and child-session boundary duration/status/cost annotations are
  sourced from parent-session `childSummary` fields without recursively loading
  child sessions.
- Round 13 reviewer rerun returned no new actionable findings for this SOW; the
  only P2 finding was scoped to SOW-0113 server-key highlight behavior.
- Round 14 completed on 2026-06-27 with accepted findings clarifying that
  `/trace` does not currently carry child summaries, that waterfall must extend
  or co-fetch the needed child-summary projection if `/trace` remains the data
  source, that SOW-0110 owns the Canvas a11y module/tests, and that render-mode
  thresholds depend on the recorded shared fixture hash.
- Round 15 completed on 2026-06-27 with accepted findings clarifying that SVG
  waterfall mode needs a mandatory keyboard path, `/trace` cannot remain a
  root-resolving data source for child-session workbench views without explicit
  subtree scoping, and `/trace` must carry or co-fetch SOW-0108 display labels if
  it remains the waterfall/event-list source.
- Round 16 completed on 2026-06-27 with reviewer findings scoped to SOW-0107,
  SOW-0108, and SOW-0112. This SOW consumes the umbrella breadcrumb
  prerequisite: waterfall child-link navigation cannot close until Session
  Detail exposes a server-side ancestor chain.
- Round 17 completed on 2026-06-27 with accepted findings clarifying Timeline
  Canvas a11y extraction evidence, `/trace` response-shape evidence, and
  preserving the extraction-before-deletion sequence required by SOW-0107.
- Round 18 completed on 2026-06-27 with accepted findings clarifying
  running-session/null-duration time-axis behavior, omission of `ctx_pct` from
  boundary annotations unless a scoped source exists, and consumption of the
  umbrella reduced-motion requirement.
- Round 19 completed on 2026-06-27 with accepted findings clarifying the
  `last_activity_ts` requirement on the chosen waterfall data source, filtered
  turn-bar behavior, running-child boundary UI, Timeline a11y extraction
  fallback, and sub-pixel bar rounding.
- Round 20 completed on 2026-06-27 with accepted findings requiring child
  boundary projections to carry `effective_status` and `last_activity_ts`,
  making stale-vs-running child display consistent with header/topology, and
  cross-referencing SOW-0107's legacy waterfall/event-list test-disposition
  table before test moves/deletions.
- Round 21 completed on 2026-06-27 with accepted findings clarifying
  status-filter scope for turn bars, op bars, and child-session boundaries, plus
  zero-op turn rendering.
- Round 22 completed on 2026-06-27 with accepted findings clarifying that the
  right time-axis edge is fetched snapshot/latest-activity time, zero-duration
  completed bars render visible minimum-width markers, `subagent=` filters dim or
  emphasize instead of pruning the waterfall, Detailed/Flame retention remains a
  SOW-0107 blocker, and first-pass zoom/pan state is not persisted.
- Round 23 completed on 2026-06-27 with no waterfall-only P2 findings; this SOW
  consumes the SOW-0107 `table=events`/error-boundary clarifications and SOW-0113
  table-mode focus/selection clarifications where waterfall synchronizes with
  shared workbench state.
- Round 24 completed on 2026-06-27 with accepted findings clarifying
  no-epoch/no-browser-wall-clock fallback when `last_activity_ts` is absent or
  invalid, `step=` filter emphasis behavior for top-level turn bars and expanded
  op bars, and expanded zero-op turn rendering with a visible "no ops" track
  label and no synthetic op bars.
- Round 25 completed on 2026-06-27 with accepted findings clarifying keyboard
  reachability and selection behavior for expanded op bars through either
  roving tabindex or a synchronized accessible mirror/list.
- Round 26 completed on 2026-06-27 with accepted findings clarifying that
  waterfall does not consume the global metric/group controls in the first pass,
  child-session `last_activity_ts` must belong to the viewed child session,
  zero-duration selected-turn relative axes remain visible/selectable, and this
  SOW consumes SOW-0107's shared subtree resolver/helper.
- Round 27 completed on 2026-06-27 with no waterfall-specific P0/P1/P2 changes;
  wording now points directly to the SOW-0107 umbrella reduced-motion contract
  so the requirement is not only present in review history.
- Round 28 completed on 2026-06-27 with accepted findings clarifying
  `status=stale` behavior in waterfall: only child-session boundary badges can
  match stale, and no-match state must be visible without pruning the time axis.
- Round 29 completed on 2026-06-27 with no waterfall P0/P1/P2 changes; P3 wording
  clarified that loading/fetch-error states use SOW-0107's per-area state
  contract as a canvas-preserving overlay.
- Round 31 completed on 2026-06-27 with no waterfall P0/P1/P2 changes.

## Validation

Pending.

## Outcome

Pending.

## Followup

None yet.

## Regression Log

None yet.
