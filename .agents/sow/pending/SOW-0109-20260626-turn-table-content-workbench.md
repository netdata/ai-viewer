# SOW-0109 - Turn Table And Content Workbench

## Status

Status: open

Sub-state: fit-for-purpose gap analysis drafted; external gap review rounds 1
through 28 completed and findings incorporated; gap-review rerun pending after
round-28 changes.

## Requirements

### Purpose

Make the turn the primary unit of inspection. The user should be able to scan
turns, expand one turn, and see the entire turn rendered in the content area
with all available reasoning, messages, tool requests/responses, timings,
payloads, and JSON structures.

### User Request

- The table should be multi-level: one row per turn, with nested rows when a
  turn is expanded.
- Clicking a turn expands/nests all views for this turn: table and visualization
  focus the turn, and content renders the entire turn.
- The content view renders the entire turn as a flat one-level timeline with all
  payloads expanded and JSON expand/collapse.
- The turn view must show reasoning, content, tool requests, tool responses,
  timings, and everything available for the view.
- It should not deep dive into subagent internals; show the subagent request and
  response only.

### Assistant Understanding

Facts:

- Current right pane is a turn picker, not a full content area.
- Current op detail opens in a fixed drawer overlay, not the content pane.
- Current table is an Event list of ops, not a turn-first multi-level table.
- Existing `TurnView`, `TurnStep`, payload fetch hooks, and the current
  `TurnStep` session-body renderer already render much of a turn. There is no
  separate `SubSessionStep` component today; richer subagent boundary rendering
  is new/refactor work built on the existing session op path.

Inferences:

- Selection state should be turn-first: selected turn drives table expansion,
  waterfall expansion, and content rendering.
- Op selection can exist inside the selected turn, but it should not replace the
  turn as the primary context.
- Payload expansion needs a clear performance boundary; loading every payload
  for every turn is not acceptable.
- "Turn" must be treated as a first-class UI container: one session plus one
  `turn_seq` / turn id grouping the ops/events between turn start and final
  state.

Unknowns:

- SOW-0113 owns the exact selected-turn / selected-op URL contract.
- Whether payload proof bytes are loaded as bounded previews at turn-selection
  time or lazily per payload section.

### Acceptance Criteria

- Session Detail table renders one top-level row per turn.
- Expanding a turn shows nested rows for that turn's ops/events only.
- Selecting a turn synchronizes:
  - table expansion,
  - waterfall expansion,
  - URL state,
  - content area.
- Content area renders the full selected turn in source order with:
  - user content,
  - assistant/content messages,
  - reasoning,
  - tool request/response payloads,
  - subagent request/response summary,
  - timing and status metadata,
  - expandable JSON for structured payloads.
- "All payloads expanded" means every payload-bearing step is represented and
  expandable in-place. Large payload bodies keep bounded previews by default and
  require an explicit load-full action.
- Payload load contract: selecting a turn loads the turn row, nested op list,
  payload reference metadata, proof fields, sizes, and bounded previews up to
  the existing preview limit. Current backend preview cap is 4096 bytes
  (`internal/presenter/payloads.go` `payloadPreviewBytes`); implementation must
  not increase it without measuring selected-turn render/network budget on the
  shared fixture. Full payload bodies load only after an explicit per-payload
  action.
- Subagent internals are not recursively expanded in the turn content area.
  Subagent calls render as boundary steps with parent op metadata, child session
  summary/status, and a link/selection target for the child session.
- Large sessions remain responsive with 1000+ turns or 10k+ ops; the
  implementation plan must include a measurable render budget for the seeded
  large-session fixture.
- The implementation plan must decide the Session Detail data-loading shape
  before frontend implementation: either prove the existing
  `GET /api/sessions/:id` all-turns/all-ops response meets the 1000-turn /
  10k-op first-paint budget, or add a lightweight turn-summary/selected-turn
  contract through SOW-0107's REST/API spec delta.
- The default selection behavior is explicit. First implementation default:
  select no turn until the user clicks, unless a URL/deep link provides a
  selection. The content pane then shows a compact session summary/empty state,
  not a drawer.

## Analysis

Sources to check during implementation:

- `frontend/src/components/TurnView`
- `frontend/src/pages/SessionDetail/UnifiedView`
- `frontend/src/pages/SessionDetail/TraceTab/EventList.tsx`
- `frontend/src/api/sessions.ts`
- `internal/presenter`
- payload lazy-fetch endpoints

Current state:

- The user has to choose from a narrow turn list and then often open a drawer for
  op details.
- The table does not expose turn-level structure as the main scan unit.
- Payload and JSON rendering exists in pieces but is not composed into one
  turn-level content experience.
- `TurnView` is the main reusable renderer for selected-turn content; this SOW
  should change placement, selection wiring, and defaults before creating new
  renderers.

External gap review round 1 findings incorporated:

- All reviewers voted `NEEDS WORK`.
- Reviewers found that shared selection was duplicated between this SOW and the
  waterfall/topology/statistics SOWs. SOW-0113 now owns the selection and URL
  contract.
- Reviewers found a contradiction between "all payloads expanded" and the
  existing bounded/lazy payload model. This SOW now requires all payload steps to
  be represented, with bounded previews and explicit expansion for large bodies.
- Reviewers found that existing `TurnView` and `SubSessionStep` were
  under-credited. Reuse is now a target requirement.
- Reviewers required a concrete turn definition and a large-session
  virtualization strategy.

External gap review round 2 findings incorporated:

- Reviewers found the payload policy still ambiguous. The contract now
  separates refs/proof/preview on turn selection from full-body loading on
  demand.
- Reviewers found the data-model evidence for a "turn" was missing. The
  implementation plan must cite the store/presenter turn identity fields before
  code.
- Reviewers found nested rows could still be unbounded for a huge single turn.
  Nested op rows now need a threshold and virtualization plan.
- Reviewers found subagent summary fields were vague. The boundary contract now
  requires a fixed field list and must cite `childSummary`/presenter evidence.
- Reviewers found selection defaults, copy-turn behavior, and legacy `?op=`
  handling needed explicit tests through SOW-0113.

External gap review round 3 findings incorporated:

- Reviewers found the SOW still treated turn identity as unknown despite the
  stored `turns` table and presenter turn-detail (`turnDetail`) response data.
  This SOW now
  records the repo answer directly.
- Reviewers found `SubSessionStep` does not exist as a component; subagent
  rendering currently lives inside `TurnStep` for `op.kind === "session"` and
  only links to the child session id. The current `ui-turn-view.md` standalone
  `<SubSessionStep>` wording is spec drift and must be corrected by this SOW's
  spec delta.
- Reviewers found `childSummary` fields already exist and should be cited
  directly, not deferred.
- Reviewers requested a concrete render budget and fixed nested-row strategy.

Risks:

- Rendering all payloads for all turns would be too slow.
- Expand/collapse JSON can become noisy unless default states are chosen well.
- URL state must avoid becoming too complex.

## Pre-Implementation Gate

Status: needs-review

Problem / root-cause model:

- The current table/content model is op-first and drawer-driven. The fit-for-
  purpose model is turn-first and content-pane-driven.
- The missing product unit is not a new low-level event type; it is a UI contract
  that groups existing canonical turn rows and ops into one scan/select/render
  object.

Evidence reviewed:

- User feedback.
- Current `UnifiedView` right pane and `SpanDetailDrawer` behavior.
- Existing EventList renders ops, not turn rows.

Affected contracts and surfaces:

- Session Detail frontend, payload fetch API usage, URL search params, tests,
  a11y/focus behavior.

Existing patterns to reuse:

	- `TurnView` component.
	- `TurnStep`, `StepFilter`, the existing session-op body inside `TurnStep`,
	  copy buttons, markdown rendering,
	  and existing payload preview/fetch logic.
	  The gap-analysis baseline is that these are reuse candidates, not proven
	  drop-ins. Before implementation planning closes, audit the current
	  `TurnView`/`TurnStep` props, loading/error behavior, payload-fetch ownership,
	  copy actions, focus assumptions, and any dependency on drawer-style width or
	  `SpanDetailDrawer` adjacency. Record the audit in this SOW or the plan before
	  writing the persistent content-pane code.
- `useTurnPayloadRefs` / `useOpPayloadRefs` from `frontend/src/api/sessions.ts`.
- Existing payload rendering and copy buttons where fit.
- There is no existing turn-first table component. `EventList` is op-first; the
  turn table itself is greenfield and should be named explicitly in the
  implementation plan, with only row/cell helpers reused where they fit.

Turn table and content contract required before implementation:

- Top-level table rows are turns. URL selection uses `sel=turn:<turn.id>`, where
  `turn.id` is the stable TEXT primary key, while display/order uses stored
  `turn.seq`. The `turns` table stores `(id, session_id, seq)` with
  `UNIQUE(session_id, seq)`, and the presenter exposes turn-detail
  (`turnDetail`) rows in the session detail response.
- Initial table rendering must not require mounting or transferring every op
  for every turn unless SOW-0107's API measurement proves the existing detail
  response fits the budget. Planning baseline: a turn-summary row contains
  `turn.id`, `seq`, timing/status/error fields, op count, failure count, and
  aggregate cost/token fields needed for the top-level table; selected-turn
  detail contains the ordered op/event rows and payload-ref metadata.
- Nested op/event source order is explicit and tested. The current trace
  presenter orders by `s.start_ts, s.id, o.start_ts, o.seq, o.id`; the turn
  table plan must either preserve that observed ordering for reused trace data
  or define a turn-detail API order of `turns.seq` plus `ops.seq, ops.id` for
  nested rows.
- Only the selected turn is expanded by default. Multiple expanded turns are a
  later enhancement unless SOW-0113 chooses multi-select.
- Nested rows under a selected turn represent ops/events in source order.
- The shared `step=` filter from SOW-0113 preserves the turn-first structure.
  Top-level turn rows are never hidden solely because no nested op/step matches
  the filter; non-matching top-level turns dim or show a zero-match badge while
  still preserving source-order context. Inside the selected/expanded turn,
  matching nested op/step rows remain visible and emphasized, non-matching nested
  rows may be hidden or collapsed behind a reversible "show non-matching steps"
  affordance, and the content pane emphasizes matching steps without losing the
  full selected-turn context.
  Top-level turn-summary numeric columns remain unfiltered source/session
  totals. When global filters are active and a row has a smaller filtered match
  set, the table shows a compact filtered-match badge or adjacent count instead
  of replacing totals with filtered values. This keeps the turn table traceable
  while SOW-0112 statistics can show explicitly filtered aggregate rows with
  `filter_scope` metadata. The compact stats ribbon follows the same rule:
  session totals remain totals, with filtered-match indicators when needed.
- `status=` and `subagent=` use the same global-filter semantics as SOW-0113
  and the waterfall/topology views. `status=` evaluates raw turn status for
  top-level turn rows and raw op status for nested rows; session
  `effective_status` applies only to session/child-summary contexts, not raw
  turn rows. `subagent=with_subagents` emphasizes turns with at least one direct
  child-session boundary and dims or zero-match-badges the rest while preserving
  source-order context. These filters must not structurally prune top-level turn
  rows in the first pass; they only affect emphasis, badges, and nested-step
  visibility.
- Top-level turn rows are virtualized. Nested rows are rendered only for the
  selected turn in the first implementation, avoiding variable-height
  virtualization across thousands of expanded rows.
  When shared selection sets `sel=turn:<id>` from any source other than a table
  click, including URL parse, browser history, waterfall/topology click, or
  legacy-link repair, the turn table scrolls the selected row into the
  virtualized viewport and renders the inline detail row. If the row is outside
  the current virtualization window, the table uses its virtualizer scroll-to-id
  path or a measured placeholder/load path; it must not leave a valid selected
  turn invisible while the table appears focused on another row.
- The selected-turn expansion still creates one variable-height detail region
  inside the top-level table. The first implementation must keep one logical
  table scroll surface by rendering a synthetic measured detail row immediately
  after the selected turn. That detail row may use dynamic measurement and
  fixed-height nested op rows, but it must not introduce a separate nested table
  scrollbar unless the implementation plan proves the single-scroll approach
  fails and updates SOW-0107's scroll ownership contract.
- If the selected turn has more than 200 nested ops/events, nested rows are also
  virtualized with fixed row height in the first implementation. Variable-height
  virtualization is deferred unless the implementation plan proves it is needed.
- Content pane renders the selected turn with `TurnView`-family components.
  The first-pass content model follows the SOW-0107 baseline: the full selected
  turn remains the primary context, rendered flat one level, and an optional
  focused op/detail is emphasized inside that same panel rather than replacing
  the turn. If SOW-0107 later chooses a different content default, this SOW's
  plan must be updated before implementation.
- The content pane's no-selection/empty state is owned by SOW-0107. This SOW
  owns selected-turn and selected-op rendering inside the persistent content pane
  only; it must not create an independent no-selection summary that can drift
  from the umbrella shell.
- The content pane must render the whole selected turn logically, but large
  turns cannot mount thousands of heavy step/payload components at once. If a
  selected turn exceeds 500 rendered `TurnStep` entries, the implementation uses
  windowing or chunked "load more" rendering while keeping all turn steps
  reachable and ordered. The threshold counts rendered steps, not raw ops,
  because one op can produce multiple visible sections. Counting rule: one
  rendered `TurnStep` card/component instance counts as one step regardless of
  how many internal payload/JSON sections it contains. The implementation plan
  may lower this threshold if measurements show the 500-step budget cannot meet
  the target.
	  Validation must include a component or Playwright fixture with more than 500
	  rendered steps, proving that every step remains reachable in order and the
	  chosen windowing/load-more path does not create layout jumps or lose the
	  selected/focused step.
  The implementation plan must name the verifier used to count rendered steps.
  Before production rendering exists, it may use the SOW-0107 renderer-planning
  mapper from op kind/name to expected `TurnStep` instances; after the component
  exists, a component test must assert the production output reaches the same
  threshold on the shared fixture. A raw `ops` row count alone is rejected.
  Planning baseline: chunked rendering, not full virtualization, for the first
  implementation: render the first 100 visible `TurnStep` instances, reveal the
  next 100 through keyboard continuation or an explicit "load next" control, and
  preserve stable step ids so a later switch to `@tanstack/react-virtual` or
  equivalent can be evaluated without changing the URL/focus contract.
- Optional selected op highlights a step inside the selected turn; it does not
  replace the selected turn as the primary context. Clicking a nested op row in
  the expanded turn table emits `sel=turn:<turn-id>` plus
  `focus=op:<op-id>`; it must not emit `sel=op:<op-id>` as the first-pass table
  click contract.
- Payload bodies use existing preview limits unless a specific payload is
  explicitly expanded to full content. Displayed payload previews and explicit
  full-payload views are faithful source data read through the existing payload
  contracts; they are not passed through the SOW-0108 display sanitizer by
  default because the operator is inspecting source artifacts. Derived copy-turn
  summaries, labels, and test fixtures are sanitized as described below.
  Faithful source display still uses safe text rendering. The implementation plan
  must verify existing payload/Markdown/JSON renderers escape HTML/JS or render
  as inert text; if not, escaping lands before this SOW renders raw payload
  previews in the persistent content pane.
- Bounded payload previews must not create an N-request burst on turn
  selection. The implementation plan must choose and test one strategy before
  code: lazy per-step preview loading as `TurnStep` rows mount/scroll into
  view, a batched selected-turn preview endpoint, or measured proof that
  parallel per-payload preview requests meet the selected-turn budget on the
  shared fixture. Sequential per-payload fetches on selection are rejected.
  The selected-turn preview budget is bounded both per payload and per turn.
  Planning baseline: each preview remains capped at the existing 4096-byte
  payload-preview limit, and a selected turn may render at most 50 previews or
  200 KiB of preview bytes before showing a "more payloads available" indicator
  with explicit per-payload expansion. The implementation plan may lower these
  numbers after measuring the shared fixture, but it must not allow an
  unbounded 100-payload turn to transfer/render every preview on selection.
	  Preview/detail requests are keyed to the selected turn id. When `sel=turn`
	  changes, in-flight selected-turn detail and payload-preview requests for the
	  previous turn are aborted where possible and always discard late responses
	  before they reach renderer state. Tests must simulate slow Turn A preview/detail
	  responses, select Turn B, and prove only Turn B content appears.
	  A selected-turn detail or payload-preview HTTP/network failure renders an
	  inline retry/error state scoped to that turn or payload and must not clear the
	  selected turn, poison later selections, or reuse a stale successful response.
- Structured JSON defaults to a shallow expanded state with controls to expand
  deeper nodes.
- Subagent boundary content shows fields already exposed by child summaries
  when available: child id/native id, `display_agent_label` /
  `display_agent_label_source`, optional `display_client_label`, kind,
  model/provider, status/error_class, start/end timestamps, tokens, cost,
  op_count,
  failure_count, server-provided direct child count, and a link to the
  child session. It does not inline the child session's internal turns. A total
  descendant/subtree count is not available from current `childSummary` and must
  come from SOW-0111 metrics if needed. Current presenter evidence to confirm
  during planning is `internal/presenter/session_detail.go`'s `childSummary`
  struct; SOW-0108 owns extending that struct with display labels and
  `direct_child_count`. If richer fields beyond those labels/counts are needed,
  adding them is a scope change.
- Child-session links use the umbrella/SOW-0113 navigation contract: navigate to
  `/sessions/:childId`, clear session-local selection/highlight state unless
  SOW-0113 records a safe preservation rule, show the breadcrumb, and rely on
  browser Back for returning to the parent workbench state.
- Copy actions from the existing turn/payload components remain available. The
  implementation plan must define copy formats before code: turn copy defaults
  to bounded sanitized structured JSON for the selected turn, while payload copy
  keeps the existing explicit payload-body copy behavior. Turn copy includes
  turn metadata, timing/status/error fields, op summaries, and bounded payload
  previews only; it does not include full payload bodies. Each copied payload
	  preview is truncated to a plan-defined byte/rune cap and passed through the
	  shared SOW-0108 sanitizer. Full-fidelity payload copying remains an explicit
	  per-payload action with the existing UI affordance, not the default turn-copy
	  command. Copy-turn excludes child-session summary projections by default; the
	  child-session link carries that context, and copying nested session metadata
	  inline would make the selected-turn copy less bounded and less predictable.
	  The UI/spec notes that turn-copy output is sanitized/redacted and may differ
	  from faithful on-screen source payload text.
- Legacy `?op=` deep links select the owning turn and focus the op through
  SOW-0113; they must not open `SpanDetailDrawer`.
- Performance budget for plan drafting: 1000 turns / 10k ops seeded fixture,
  first table paint under 500ms, selecting a typical turn under 200ms, and
  expanding/rendering a 500-op selected turn under 500ms on the reference
  workstation. The implementation plan may tighten these, but cannot omit a
  measured budget. Validation records the shared fixture hash from SOW-0107; if
  the fixture hash changes, these budgets must be re-measured before this SOW can
  be accepted. The 500-op selected-turn stress case must prove at least 500
  rendered `TurnStep`/step-card instances after grouping; if UI grouping reduces
  the rendered step count, the fixture must add enough source ops or an equivalent
  stress turn so the content windowing/chunking path is actually exercised.
- First-pass turn table sorting is explicit: rows are ordered by `turn.seq`
  ascending. Column sorting is deferred unless a later SOW adds sortable column
  semantics and tests. Statistics answers "where did time/cost go" at aggregate
  level; this table stays source-order for first-pass traceability.
- Accessibility contract: the table uses one reachable tab stop plus arrow-key
  navigation/roving tabindex for large row sets; Enter/Space toggles expansion;
  nested rows expose level/ownership; JSON expand/collapse controls are keyboard
  operable; the content pane is reachable immediately after the active table or
  visualization control.
- Content-pane keyboard scale is part of this SOW. The selected-turn content
  pane uses one primary tab stop for the pane or active step list, arrow-key
  navigation between visible `TurnStep` cards/sections, and Enter/Space to
  expand payload or JSON controls at the active step. Interactive controls
  inside an expanded step remain reachable, but keyboard users must not tab
  through hundreds of steps to leave the pane. Tests cover large-turn navigation,
  payload/JSON expansion, and focus moving out of the pane after the last
  reachable step. When content is windowed or chunked, arrow navigation at the
  last visible step loads or reveals the next chunk without losing focus, and
  Shift+Tab or an explicit skip/end affordance lets keyboard users leave the
  content pane without traversing every hidden or unloaded step.
  Deep links or table clicks with `focus=op:<id>` into a not-yet-rendered chunk
  must reveal/load the owning chunk and place focus on the target step without
  forcing the user to manually page through earlier chunks.
  Keyboard navigation inside a single step is explicit: each JSON/payload
  expand-collapse control inside a `TurnStep` is reachable in deterministic
  source order without requiring the user to tab through the whole page. First
  pass may use one tab stop per step plus a roving-tabindex list of internal
  JSON controls, or plain tab stops scoped to the active step, but tests must
  prove a step with at least three JSON payload sections can focus each control,
  expand/collapse it, and then return to next/previous step navigation without
  trapping focus.
- Zero-turn sessions show SOW-0107's compact no-selection session summary plus an
  empty table/content state; no turn is selected and no drawer opens. This SOW
  does not own a separate zero-turn summary surface.
- Expanding a turn with zero ops shows the turn row/bar and a compact "no ops"
  nested state, with no drawer and no synthetic op row.

Risk and blast radius:

- High frontend blast radius; low backend risk unless new batched payload API is
  needed.

Sensitive data handling plan:

- Do not record payload content in SOW/tests. Use sanitized fixtures and mocked
  payload bodies.

Implementation plan:

1. Consume SOW-0113's selected turn/op URL and shared state contract.
1a. Consume SOW-0107's shared subtree session-id resolver/helper for any
    current-session-subtree data scope needed by events, child-session links, or
    lightweight table data. This SOW must not create a parallel recursive
    subtree walker or CTE pattern.
2. Spec the stored turn identity/order fields and payload load policy.
3. Consume SOW-0107's API measurement result and define whether `TurnTable`
   uses the current detail payload or a new turn-summary/selected-turn API
   contract. If the all-detail path is chosen, the plan must enumerate every
   `sessionDetailResponse`, `turnDetail`, op, payload-ref, and child-summary
   field consumed by the turn table, waterfall synchronization, and content pane
   before frontend data-loading code is written.
4. Define the new `TurnTable` component, current `TurnView`/`TurnStep`
   capability matrix, the measured-detail-row table virtualization technique,
   and large-turn content virtualization/chunking strategy.
5. Add specs for turn table/content synchronization. The spec correction for
   stale step-renderer wording targets the entire `ui-turn-view.md`
   StepKindRenderer table, not only `<SubSessionStep>`: the current code uses a
   single `TurnStep` dispatcher with internal variants rather than standalone
   `<UserPromptStep>`, `<ReasoningStep>`, `<AssistantStep>`, `<ToolStep>`,
   `<SubSessionStep>`, `<CompactionStep>`, or `<GenericStep>` components. SOW-0107
   owns the full `ui-turn-view.md` shell-section rewrite and the corrected
   step-renderer table; this SOW adds only the selected-turn table and
   content-pane deltas to that rewritten section.
6. Add component tests for turn expansion, nested op virtualization, copy
   actions, selected-turn content rendering, and the >500 rendered-step
   windowing/load-more threshold.
6a. Update `frontend/vitest.coverage.mjs` for any new `TurnTable` or
    selected-turn content directories, or prove they are already covered by an
    existing gated directory before adding files.
7. Add Playwright test for table click -> content update, legacy `?op=`
   migration, and direct `focus=op:<id>` / step navigation into a chunk that is
   not initially rendered.
7a. Add Playwright coverage for child-session link navigation from content:
    child link -> `/sessions/:childId`, breadcrumb visible, Browser Back returns
    to the parent workbench.
8. Rehome/reuse `TurnView` and related payload components inside the persistent
   content pane.
9. Replace EventList-as-primary-table with a turn-first table. If `table=events`
   remains enabled per SOW-0107, it is a secondary flat op/event list mode that
   consumes the same `sel=turn` plus `focus=op` contract for rows with stable
   op targets; it is not the default table and must not reopen the drawer path.
   Under SOW-0107's lightweight contract, retained `table=events` consumes the
   paged `GET /api/sessions/:id/events` envelope; if that endpoint is not chosen,
   this mode is omitted/disabled instead of reusing retired `/trace` data.

Planning dependencies:

- Content-pane rendering defaults depend on SOW-0107 open decision 1. Table/raw
  area behavior depends on SOW-0107 open decision 4. This SOW may draft data/API
  plans earlier, but selected-turn content layout and table/raw geometry cannot
  complete plan review until those umbrella decisions are recorded.
- Existing test disposition for waterfall/event-list-related tests is recorded
  in SOW-0107's test-disposition table and must be consumed before moving or
  deleting legacy primary table tests.

Validation plan:

- Component tests, Playwright synchronization tests, a11y tests, rendered-step
  verifier coverage, and performance smoke on the large seeded session.

Artifact impact plan:

- Specs: `ui-turn-view.md`, `ui-pages.md`.
- SOW lifecycle: child of SOW-0107.

Open-source reference evidence:

- Check tracing/log viewers for master-detail table patterns if implementation
  plan needs reference.

Open decisions:

1. Payload loading for selected turn:
   - A. Fetch payload refs, proof metadata, sizes, and bounded previews for the
     selected turn; fetch full bodies lazily per explicit payload action.
   - B. Fetch full payload bodies for the whole selected turn immediately.
   Recommendation: A for performance and privacy. This is now the target
   contract unless the implementation plan proves an existing endpoint cannot
   support bounded previews.
2. JSON default expansion:
   - A. Expand structured JSON to depth 1 with explicit controls for deeper
     nodes.
   - B. Fully expand structured JSON by default.
   Recommendation: A, long-term-best. It keeps dense payloads readable and
   avoids turning the content pane into an unbounded raw dump.

## Plan

1. Run external gap review.
2. Resolve findings and finalize payload/loading decision.
3. Rerun the gap-analysis gate.
4. Draft implementation plan.

## Execution Log

### 2026-06-26

- Created focused SOW from turn table/content feedback.
- Incorporated external reviewer round-1 findings: shared selection dependency,
  `TurnView` reuse, payload preview bounds, turn definition, subagent summary,
  and virtualization strategy.
- Incorporated external reviewer round-2 findings: payload load contract,
  turn data-model evidence, selected-turn default, nested virtualization,
  subagent boundary field list, copy-action preservation, and legacy deep-link
  behavior.
- Incorporated external reviewer round-3 findings: stored turn identity,
  correction that `SubSessionStep` is not a real component, existing
  `childSummary` fields, fixed nested virtualization, render budgets, source
  ordering, and zero-turn empty state.
- Incorporated external reviewer round-4 findings: turn table is greenfield,
  large-turn content rendering needs a windowing/chunking strategy, a11y needs a
  roving-tabindex contract, and the implementation plan must include a
  `TurnView`/`TurnStep` capability matrix.
- Incorporated external reviewer round-5 findings: `sel=turn` uses the stable
  `turn.id`, and large selected-turn content uses a first-pass 500-step
  windowing/chunking threshold.
- Incorporated external reviewer round-6 findings: selected-turn expansion must
  use a measured synthetic detail row in the single table scroll surface, the
  500-step threshold counts rendered `TurnStep` entries, and copy-turn output
  must be specified before implementation.
- Incorporated external reviewer round-7 findings: current `ui-turn-view.md`
  `SubSessionStep` wording is spec drift to correct here, and "nested child
  count" is narrowed to current direct child count unless SOW-0111 subtree
  metrics are available.
- Incorporated external reviewer round-8 findings: the turn-table data-loading
  contract now depends on SOW-0107's measured Session Detail API size/latency,
  the planning baseline names a turn-summary vs selected-turn detail split, and
  the 500-step threshold counts rendered `TurnStep` component instances rather
  than raw ops or internal payload sections.
- Incorporated external reviewer round-9 findings: subagent boundary evidence
  is tied to the current presenter `childSummary` shape, and copy-turn output is
  now bounded sanitized structured JSON with full payload bodies excluded unless
  copied explicitly per payload.
- Incorporated external reviewer round-10 findings: content rendering follows
  the SOW-0107 full-turn-plus-optional-focus baseline, subagent boundaries
  consume SOW-0108 display labels from child summaries, child-session links use
  the shared navigation/breadcrumb contract, and the `SubSessionStep` spec-drift
  correction target is pinned.
- Incorporated external reviewer round-11 findings: selected-turn payload
  previews must use lazy loading, a batched endpoint, or measured parallel proof
  instead of sequential N-per-payload fetches, and the `ui-turn-view.md`
  step-renderer table correction covers every stale standalone step component,
  not only `SubSessionStep`.
- Incorporated external reviewer round-12 findings: SOW-0109 now explicitly
  acknowledges that SOW-0107 owns the content pane's no-selection state and the
  full `ui-turn-view.md` shell/step-renderer rewrite; this SOW owns only
  selected-turn/table/content deltas on top of that umbrella contract.
- Round 13 reviewer rerun returned no new actionable findings for this SOW; the
  only P2 finding was scoped to SOW-0113 server-key highlight behavior.
- Round 14 completed on 2026-06-27 with accepted findings clarifying the nested
  op-row URL contract, faithful payload display versus sanitized derived copy,
  and fixture-hash dependency for performance budgets.
- Round 15 completed on 2026-06-27 with accepted findings clarifying that the
  500-step content threshold needs explicit validation and that subagent
  boundary child counts come from SOW-0108's server-provided
  `direct_child_count`, not a future partial child slice length.
- Round 16 completed on 2026-06-27 with reviewer findings scoped to SOW-0107,
  SOW-0108, and SOW-0112. This SOW consumes the umbrella breadcrumb
  prerequisite: integrated child-link navigation cannot close until Session
  Detail exposes a server-side ancestor chain.
- Round 17 completed on 2026-06-27 with no table/content-specific contract
  changes beyond consuming the updated umbrella stats-table URL and ancestor-chain
  contracts.
- Round 18 completed on 2026-06-27 with no new table/content-only data contract
  beyond consuming the updated legacy `?op=` lookup requirement, highlight
  intersection semantics, reduced-motion umbrella requirement, and SOW-0108
  child-summary duration/count semantics.
- Round 19 completed on 2026-06-27 with accepted findings clarifying
  content-pane keyboard navigation for large selected turns and the secondary
  `table=events` contract as a flat list that still feeds shared turn/op focus.
- Round 20 completed on 2026-06-27 with accepted findings citing the 4096-byte
  payload preview cap, requiring all-detail field enumeration if that API path is
  chosen, adding `vitest.coverage.mjs` updates for new component directories,
  and making SOW-0107 content/table open decisions explicit planning
  prerequisites.
- Round 21 completed on 2026-06-27 with accepted findings clarifying that the
  large-turn fixture must exercise at least 500 rendered step cards after UI
  grouping, and that windowed/chunked content needs keyboard continuation and
  exit behavior.
- Round 22 completed on 2026-06-27 with accepted findings clarifying first-pass
  content windowing in 100-step chunks, safe text rendering for payload/Markdown/
  JSON display paths, copy-turn sanitized-output caveats, `focus=op` reveal for
  not-yet-rendered chunks, zero-op expanded-turn state, and explicit deferral of
  first-pass turn-table sorting beyond `turn.seq` ascending.
- Round 23 completed on 2026-06-27 with accepted findings requiring a current
  `TurnView`/`TurnStep` interface and drawer-dependency audit before implementation
  planning, plus an explicit copy-turn exclusion for child-session summary
  projections.
- Round 24 completed on 2026-06-27 with accepted findings clarifying
  `step=` filter behavior for turn rows versus nested rows/content, a
  rendered-step verifier before raw op counts can satisfy the 500-step stress
  contract, and direct focus/deep-link navigation into not-yet-rendered content
  chunks.
- Round 25 completed on 2026-06-27 with accepted findings clarifying the actual
  presenter turn-detail naming, per-turn aggregate payload-preview budget, and
  keyboard navigation for multiple JSON/payload controls inside one rendered
  step.
- Round 26 completed on 2026-06-27 with accepted findings clarifying table
  scroll-to-selected-turn behavior for external selections, unfiltered total vs
  filtered-match count display under global filters, and explicit consumption of
  SOW-0107's shared subtree resolver/helper.
- Round 27 completed on 2026-06-27 with accepted findings pinning table-side
  `status=` and `subagent=` behavior so global filters preserve turn order and
  remain consistent with waterfall/topology semantics.
- Round 28 completed on 2026-06-27 with accepted findings requiring selected-turn
  keyed cancellation/discard for turn-detail and payload-preview requests during
  rapid turn switching.
- Round 29 completed on 2026-06-27 with no table/content P0/P1/P2 changes; P3
  wording clarified failed selected-turn payload-preview behavior and that
  zero-turn no-selection summary ownership remains with SOW-0107.
- Round 30 completed on 2026-06-27 with one accepted upstream P1 from SOW-0107:
  retained `table=events` under the lightweight contract consumes the paged
  `GET /api/sessions/:id/events` envelope, or the mode is omitted/disabled.
- Round 31 completed on 2026-06-27 with no table/content P0/P1/P2 changes; the
  rerun confirmed the `table=events` lightweight endpoint disposition.

## Validation

Pending.

## Outcome

Pending.

## Followup

None yet.

## Regression Log

None yet.
