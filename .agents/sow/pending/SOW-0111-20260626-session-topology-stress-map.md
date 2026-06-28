# SOW-0111 - Recursive Topology Stress Map

## Status

Status: open

Sub-state: fit-for-purpose gap analysis drafted; external gap review rounds 1
through 28 completed and findings incorporated; gap-review rerun pending after
round-28 changes.

## Requirements

### Purpose

Make topology a recursive stress map for the whole session tree. The user should
see where fan-out, tool use, cost, time, tokens, failures, and subagent depth
concentrated.

### User Request

- Topology is turn-based.
- Tools and subagents attach to turns.
- The entire tree is visualized: session, all turns, tools, subagents, their
  turns/tools/subagents, recursively.
- The view answers where the "stress" was and what was "too many".
- It must be an interactive force graph with drag, pan, zoom.

### Assistant Understanding

Facts:

- Current topology is a per-session visualization with controls nested inside
  the visualization pane.
- Existing source/canonical data records sessions, child sessions, turns, ops,
  tools, costs, tokens, failures, durations.
- Current per-session topology already gathers sessions under the same root and
  draws agent/tool nodes with lineage edges, while the frontend already supports
  force layout, pan/zoom, and node drag. It is not a recursive turn/tree
  structure today. The real missing contract is turn-level graph topology,
  stress/fan-out encoding, collapse/expand, and content selection.

Inferences:

- Topology should not be a decorative graph; it should encode stress metrics.
- Nodes should include session/agent, turn, tool, and subagent-call concepts.
  Model nodes are likely statistics-only for the first pass to avoid unnecessary
  graph cardinality.
- Edges should reflect containment/call relationships, not only generic links.
- Collapse/expand is necessary to make large trees usable.

Unknowns / required decisions:

- Which stress metric is the default visual encoding.
- Whether model nodes should be first-class topology nodes or statistics-only.
- Whether turn-level topology is served by a new endpoint or an explicit mode on
  the existing per-session topology endpoint.

### Acceptance Criteria

- Topology renders the whole recursive session tree.
- Every turn is represented and connected to its session/agent.
- Tools and subagent calls attach to the owning turn.
- Subagent sessions recursively show their own turns/tools/subagents.
- Default visual encoding highlights stress using at least duration, cost,
  tokens, failures, and fan-out.
- The graph reuses existing pan, zoom, drag, and layout machinery where fit, and
  adds fit-to-view and collapse/expand where missing.
- Clicking a turn/tool/subagent updates the shared selection/content area.
- Large trees degrade predictably with aggregation/collapse, not unusable node
  clutter.
- The response contract names node types, edge types, metrics, aggregation
  markers, and truncation/collapse behavior.
- The implementation plan treats turn-level topology as a high backend
  change unless code evidence proves it can be additive. Current topology
  folding is op-stream oriented and not turn-group oriented.
- Performance budget for plan drafting: graph endpoint build under 500ms for a
  1000-turn / 10k-op seeded session tree capped to 500 visible nodes and depth 4;
  frontend layout reaches a stable interactive state under 1000ms for the same
  capped graph on the reference workstation. The validation record includes the
  shared fixture hash from SOW-0107; a fixture hash change invalidates topology
  performance acceptance until re-measured.

## Analysis

Sources to check during implementation:

- `internal/presenter` topology endpoints
- `frontend/src/pages/SessionDetail/TopologyTab`
- `frontend/src/viz/topology.ts`
- `frontend/src/pages/Topology`
- canonical session/turn/op child-session contracts

Current state:

- Topology exists and already covers the recursive session tree at agent/tool
  level, but it is not turn-based and does not make fan-out/stress the primary
  visual language.
- The current controls are embedded inside the viz pane, contributing to
  patchwork UI.
- Current backend evidence: `internal/presenter/session_topology.go` builds
  agent/tool nodes only; `session_topology_builder.go` folds an op stream into
  agent/tool aggregates; global `/api/topology` uses a separate cross-session
  endpoint; frontend `TopologyRenderer` is shared by Session Detail and the
  global Topology page.

External gap review round 1 findings incorporated:

- All reviewers voted `NEEDS WORK`.
- Reviewers found that the draft understated existing capability: the presenter
  already scopes topology to the recursive session tree, and the frontend
  already has force layout, pan/zoom, and drag.
- Reviewers identified the true missing backend/API work as turn nodes,
  turn-to-tool/subagent edges, fan-out/stress metrics, aggregation/collapse
  metadata, and shared selection wiring.
- Reviewers required a node/edge inventory, node cap/degradation behavior,
  collapse/expand contract, and accessibility fallback.

External gap review round 2 findings incorporated:

- Reviewers found current topology builder complexity was underestimated: adding
  turn nodes likely requires a turn-keyed intermediate accumulator or two-pass
  builder, not only adding frontend nodes.
- Reviewers found node cap, recursion cap, and collapse heuristics were too
  vague. This SOW now names initial caps and a degradation strategy to test.
- Reviewers found `subagent_call` as a visible node may double graph size. The
  recommended first pass treats it as an annotated edge unless a visible node is
  proven necessary.
- Reviewers found stress/fan-out definitions must be shared with SOW-0112.
- Reviewers found content-pane behavior for topology selections must stay
  turn-first and explicit.

External gap review round 3 findings incorporated:

- Reviewers found `subagent_call` was still described as both a node and an
  edge annotation. The first-pass schema now treats it as edge metadata only.
- Reviewers found direct-vs-recursive metric scope, collapse metric, endpoint
  consumer map, performance budget, and stress-glossary spec ownership needed to
  be explicit.
- Reviewers found topology controls must move to the unified controls ribbon,
  not remain nested inside `TopologyTab`.

Risks:

- Backend query performance for recursive whole-tree graph.
- Visual clutter and force-layout instability on large sessions.
- Pan/zoom/drag interactions can regress a11y and tests.

## Pre-Implementation Gate

Status: needs-review

Problem / root-cause model:

- Current topology is not fit-for-purpose because it aggregates at the
  agent/tool level and does not expose turn-level containment, fan-out, or
  "too many" stress in the user's mental model.

Evidence reviewed:

- User feedback.
- Current topology controls and pane measurements from headless Playwright.
- Existing session/turn/op data model from prior ingestion SOWs.

Affected contracts and surfaces:

- Potential backend graph endpoint, topology frontend, shared selection state,
  stats/stress metric definitions, Playwright visual tests.

Existing patterns to reuse:

- Existing D3 topology renderer.
- Existing force-worker pattern.
- Existing session tree/subagent lineage contracts.

Topology graph contract required before implementation:

- Node types:
  - `session`: one session/agent in the current session subtree.
  - `turn`: one turn belonging to a session.
  - `tool`: aggregate or call node for tool usage owned by a turn.
  - `subagent_call`: not a visible node in the first pass. It is edge metadata
    on `spawns`. A later visible node type requires a separate decision.
- Edge types:
  - `contains`: session -> turn.
  - `calls`: turn -> tool.
  - `spawns`: turn -> child session, with subagent-call metadata on the edge in
    the first pass.
- Metrics carried per node where applicable: duration, cost, tokens, failures,
  context pressure (`ctx_pct`) when model/token capacity is known, op count,
  child session count, fan-out count, and status.
  Duration semantics are shared with SOW-0108/SOW-0110: session nodes use
  `end_ts - start_ts` when both timestamps are known, turn nodes use
  turn `end_ts - start_ts`, and op/tool nodes use op duration. Running or
  unavailable durations are null/unavailable with an explicit label, not zero.
	  Session nodes expose both direct/own-session values and recursive subtree
	  values. Because this view is a stress map, first-pass visual encoding for
	  session-node cost, tokens, duration, failures, and `ctx_pct` uses explicit
	  `subtree_*` values by default, while tool/turn/op nodes use their own direct
	  values. Direct session values remain available in node detail/tooltip fields
	  with `own_*` or `metric_scope=own` labels. The response exposes metric scope
	  metadata such as `metric_scope=subtree` / `metric_scope=own` or per-metric
	  `*_scope` fields so the UI does not confuse topology subtree-stress encoding
	  with direct session work.
  Session nodes use `effective_status` for operator-facing status when present
  and fall back to raw `status` only when absent; turn/tool/op nodes use their
  own raw status. The workbench topology endpoint computes `effective_status`
  for every session node, root and descendants, using the same presenter
  derivation as the header from raw `status`, `end_ts`, `last_activity_ts`, and
  server `now`. `now` is captured once per response before node derivation so
  sibling session nodes cannot disagree because the clock advanced mid-build.
  This reconciles with earlier child-summary minimization
  decisions: those applied to generic `child_sessions` DTOs, while this endpoint
  has an independent workbench response. Running session/child nodes with null
  `end_ts` render a visible running/stale marker and expose `last_activity_ts` in
  the response so the UI can explain the snapshot age without treating the node
  as completed.
- Zero-turn sessions still render the root session node with an explicit "no
  turns" empty-state annotation and no synthetic turn/tool nodes. This is the
  topology equivalent of the table/content empty-state contract; a blank canvas is
  rejected. If a zero-turn session gains its first turn through
  `session_changed`, the next accepted re-fetch removes the empty annotation and
  renders the new turn with SOW-0107's "new work available" indicator pattern.
- Session/agent node labels use SOW-0108 `display_agent_label` when populated,
  with raw `agent_name` only as a fallback. `display_client_label` is available
  as secondary metadata when SOW-0108 can derive it. The topology view must not
  reintroduce generic v3 labels such as `parent` after SOW-0108 has normalized
  display identity.
  If SOW-0108 is delayed or rejected, this SOW's implementation plan must choose
  the degraded branch explicitly: workbench topology session-node labels may use
  raw `agent_name` with a visible caveat that ai-agent v3 can still show raw
  `parent`, until SOW-0108's display-label derivation/backfill lands. It must
  not quietly claim normalized topology labels without the display-label storage.
- Stable node-id scheme required before implementation:
  - `session:<session_id>`;
  - `turn:<turn_id>`;
  - `tool:<turn_id>:<tool_namespace>.<tool_name>` for turn-owned tool groups;
  - `other-turns:<session_id>:<hash-of-sanitized-bucket-predicate>` and
    `other-tools:<turn_id>:<hash-of-sanitized-bucket-predicate>` for collapsed
    aggregate nodes.
  These ids are graph identities and highlight/focus anchors; primary content
  selection remains turn-first through SOW-0113. Rank-window positional ids are
  rejected for first pass because live updates would re-key aggregates when ranks
  shift.
- Aggregate-node hash inputs must be explicit and sanitized before hashing:
  viewed `session_id`, subtree scope, parent node id, aggregate kind
  (`other-turns` or `other-tools`), depth cap or parent turn/session, ranking
  metric set, active `metric` value at issuance, included turn range or tool
  namespace/name predicate, and the collapse reason (`depth`, `visible_cap`,
  `top_n_tail`). For non-contiguous predicates, the hash includes a sorted list
  of canonical turn ids or tool namespace/name pairs instead of a false
  contiguous range. The hash inputs are passed through the shared SOW-0108
  display sanitizer where they contain user/source-derived text. The hash must
  not include raw prompt text, payload text, private paths, or rank positions.
  First-pass aggregate ids use the same hash rigor as SOW-0112 stat
  `selection_key`: at least 128 bits of URL-safe hash output, duplicate
  detection within one response, deterministic response-local suffixes if a
  collision is detected, and a test fixture that forces collision handling
  through a stubbed hash function. If a single response would require more than
  64 collision suffixes, the endpoint fails with the standard error envelope
  instead of emitting an ambiguous graph. Presenter tests must include the
  >64-collision failure path by using the same stubbed hash seam; the shared
  fixture is not expected to trigger it naturally. SOW-0107 step 5c owns the
  shared-vs-separate hash-helper decision for topology/statistics keys; the
  inline contract here is the fallback definition if planning proves separate
  implementations are safer.
- Shared stress metric definitions:
  - `tool_call_count`: direct tool ops owned by a turn or aggregate unless a
    field is explicitly named `subtree_tool_call_count`. A single-call tool node
    carries `tool_call_count=1`; an aggregate `tool:` node carries the count of
    folded matching tool ops. Aggregate hash inputs include the sanitized tool
    namespace/name predicate and active filter tuple, not the derived count
    value.
  - `subagent_call_count`: direct child-session spawn ops owned by a turn. This
    is the shared canonical stress name for SOW-0107 turn-summary
    `child_spawn_count`; `direct_child_count` in SOW-0108 child summaries is a
    related but different child-session-row field.
  - `fan_out_count`: `tool_call_count + subagent_call_count`; displayed
    explicitly, not hidden behind a composite score. It remains a fan-out
    metric, so unclassified/unknown raw op kinds do not contribute unless they
    are later classified as a tool or subagent call by a spec change.
  - `op_count`: direct ops owned by the turn or aggregate unless a field is
    explicitly named `subtree_op_count`. Unknown raw op kinds that are visible
    under SOW-0113 `step=all` contribute to `op_count` and `failures` when their
    status is in the terminal-failure family, but not to `tool_call_count`,
    `subagent_call_count`, or `fan_out_count`.
  - `failures`: the shared terminal-failure family from SOW-0107/SOW-0113
    (`failed`, `abandoned`, `interrupted`, `aborted`). Topology stress colors
    and failure counts must not use a narrower predicate than statistics or the
    global `status=failed` filter.
  - `subtree_session_count`: all descendant sessions below a session node.
  - `max_subagent_depth`: maximum child-session depth below the current node.
  - `ctx_pct`: context-pressure percentage when model/token capacity is known.
    It exists in the current topology UI and must be retained as a stress metric
    or explicitly disabled per node with an unavailable reason; do not silently
	    drop it. The shared glossary must cite the existing topology computation in
	    `internal/presenter/session_topology_builder.go`, where current behavior is
	    a running maximum of `ctx_used / ctx_max`, so topology and statistics do not
	    drift to different formulas.
	    When `ctx_max` varies across ops in the same group or subtree, for example
	    after a model change, compute each op's `ctx_used / ctx_max` ratio first and
	    then take the maximum ratio. Do not divide a summed `ctx_used` by one chosen
	    model capacity.
	    Topology labels must identify this as node/subtree peak context pressure,
    not an average. Response metadata should expose a scope label such as
    `ctx_pct_scope=node_peak` or an equivalent field so SOW-0112 statistics can
    use a different row/group scope without user confusion.
  - SOW-0113 writes the initial URL enum skeleton for `metric` and `group` before
    it implements parsing, so its prerequisite role is not circular. SOW-0111 is
    the first writer of the canonical metric definitions in `rest-api.md`
    "Stress metrics and dimensions"; SOW-0112 extends/consumes the same section
    for aggregate table and heatmap row shapes. SOW-0113 must create that
    section if it is absent before SOW-0111/SOW-0112 implementation plans
    finalize. Child SOWs must not duplicate divergent metric definitions.
    SOW-0111's metric-definition write is a prerequisite for SOW-0112 extending
    row shapes. If SOW-0112 must land first, it may only consume the SOW-0113
    enum skeleton and must record a temporary inheritance path rather than
    creating competing metric definitions.
- Legacy topology `calls` is not an alias for `fan_out`. If the workbench needs
  to compare to the existing endpoint, document `calls` as legacy op/call volume
  and `fan_out` as direct tool-call plus subagent-call pressure. The UI must not
  silently map one to the other.
	- Default stress encoding should avoid a black-box composite for the first pass:
	  use explicit visual channels, such as subtree duration for session-node size,
	  own duration for turn/tool/op size, subtree failures for session-node color,
	  own failures for turn/tool/op color, and fan-out badges/edge emphasis.
	  Composite scoring can be added later only if explainable and tested. URL
	  state reflects this default as `metric=multi` only for `view=topology`; an
	  absent `metric` while topology is active also means this multi-channel default.
	  Statistics does not accept `metric=multi` and keeps its own metric default.
- Large graph behavior:
  - initial visible-node target cap: 500, matching the existing cross-session
    topology default unless implementation planning proves a different cap;
  - hard safety cap: 1000 visible nodes for turn-level topology in first pass;
  - recursion beyond depth 4, measured as child-session depth from the viewed
    root/current session, defaults to collapsed aggregate nodes with counts;
  - aggregation pipeline order is depth-collapse first, then visible-node cap,
    then top-10-per-parent tail folding. A different order changes graph
    identity and requires a spec update before implementation.
  - the backend must bound pre-aggregation traversal before relying on the
    visible-node cap. The implementation plan must use a depth-limited indexed
    traversal and either short-circuit counts or prove with EXPLAIN/benchmark
    evidence that raw traversal of the shared 1000-turn / 10k-op fixture stays
    under the 500ms graph-build budget. Recursive session-tree traversal follows
    the same anti-pattern avoidance as SOW-0112 statistics: compute the
    depth-limited subtree session-id set first, then query turns/ops with that
    bounded set. Avoid a recursive CTE that directly drives large joins. The
    implementation plan must include EXPLAIN evidence for the topology traversal
    query on the shared fixture.
  - low-stress turns/tools collapse into `N other turns` / `N other tools`
    aggregate nodes when the cap would be exceeded;
  - first-pass low-stress ranking uses direct `fan_out_count`, then failures,
    then duration as tie-breakers;
  - first-pass aggregation keeps top 10 visible turn/tool groups per parent
    before folding the tail into `other`;
  - the response exposes `truncated` / `collapsed` markers and counts.
- Aggregate-node interaction: clicking an `other-*` aggregate applies
  `hi=topology:<selection_key>` and does not become the primary content
  selection. Local expansion is an explicit aggregate action/control, not the
  default click behavior. Aggregate expansion must keep visible nodes at or
  below the hard cap by replacing the aggregate in place or refusing expansion
  with a visible "narrow scope" state. The plan must define and test
  force-layout stability when aggregate nodes expand/collapse. First-pass
  acceptance budget: after an aggregate expand/collapse, the layout either
  stabilizes within 500ms on the shared fixture or shows a visible still-layouting
  state, and no selected/visible node may remain more than 24px outside the
  viewport after the fit-to-view pass unless the user has manually panned/zoomed.
  The still-layouting state is bounded: if a force simulation does not stabilize
  within 5 seconds after any simulation start, including initial render,
  aggregate expand/collapse, metric/filter re-layout, accepted
  `session_changed` refresh, or SSE `resync`-driven full refresh, the
  implementation freezes the current layout, stops the simulation loop, shows a
  compact "layout did not stabilize; pan/zoom still available" state, and keeps
  selection/pan/zoom interactive. Tests must stub or force a non-converging
  initial simulation and aggregate re-layout and assert the timeout stops CPU
  work.
- Topology server-key resolution is owned here for topology keys. A
  `hi=topology:<key>` value is not valid until the topology backend can resolve
  the key to a sanitized highlight result with bounded `turn_ids`, `op_ids`,
  `node_ids`, optional turn ranges, match counts, and `truncated/stale` status
  for SOW-0113 to consume through
  `GET /api/sessions/:id/highlights/topology/:key`. Stale or unknown keys clear
  with the visible stale-highlight state from SOW-0113. The resolver response
  schema is defined once by SOW-0113 and the REST/API spec delta.
- Topology-key staleness semantics: a key is `stale` when the sanitized
  topology predicate is still recognized but membership differs from
  issuance-time membership because ingest/SSE changed the session subtree,
  visible-cap folding changed, depth-collapse flipped after new children
  arrived, or a target session/turn/op disappeared. A key is `unknown` when it
  cannot be mapped to any known sanitized topology predicate for the session.
- Model nodes are deferred unless the implementation plan proves they add value
  without making the graph unreadable.
- Topology endpoint direction: create a new workbench endpoint if the response
  shape diverges materially from existing `/api/sessions/:id/topology`; do not
  break existing topology consumers. Recommended path:
  `GET /api/sessions/:id/topology/workbench`.
- Workbench topology scope follows SOW-0107: the default graph is scoped to the
  currently viewed session subtree. A root-session view therefore shows the full
  recursive tree, while a child-session view shows that child and descendants.
  If implementation later adds a root-tree comparison from a child view, the
  response must echo the non-default scope and the controls ribbon must show it.
- Existing endpoint lifecycle: first-pass workbench topology keeps
  `GET /api/sessions/:id/topology` as the legacy agent/tool endpoint and adds
  the workbench endpoint rather than mutating the legacy shape in place. The
  `rest-api.md` delta must document both endpoints and mark the legacy endpoint
  as retained for compatibility until SOW-0107 removes the old topology tab
  path and tests prove no caller needs it.
- New endpoint HTTP behavior follows the existing REST contract: `HEAD` returns
  the same status and JSON content type with an empty body, non-GET/HEAD methods
  return `405`, unknown session ids return `404`, and invalid/control-character
  path parameters fail closed with the existing error envelope.
- Workbench endpoint response schema is part of the gap-analysis contract:
  `{ root_session_id, scope_session_id, nodes, edges, metrics, collapsed,
  truncated, limits, generated_at }`. `nodes[]` contains
  `{ id, type, label, session_id?, turn_id?, metrics, status?,
  effective_status?, last_activity_ts?, source }`;
  `edges[]` contains `{ id, source, target, type, metrics?, child_session_id? }`.
  Node-level `metrics` carries per-node values using the shared stress names
  above. Top-level `metrics` is response metadata for the active metric,
  available metric names, units, default scope per node type, and scope labels.
  `root_session_id` is the topmost ancestor of the viewed session.
  `scope_session_id` is the URL session id (`/sessions/:id`). They are equal
  for a root-session view and differ when the user has navigated into a child;
  the controls ribbon shows the scope indicator only when they differ.
  First-pass topology pan/zoom/fit state is view-local only. It is not persisted
  in URL or localStorage; `fit-to-view` runs on first render and accepted
  re-fetches only until the user manually pans or zooms in the current view.
	  first-pass shape is
	  `{ active_metric, available_metrics, units, node_scope_labels }`, where
	  `node_scope_labels` distinguishes session subtree encoding from turn/tool/op
	  own encoding. It is not another aggregate row. `collapsed` reports aggregate
	  counts/reasons; `limits` echoes depth and
	  visible-node caps. `generated_at` is the server wall-clock timestamp captured
  at response serialization start; it is display/cache metadata only and does not
  replace snapshot data timestamps such as `last_activity_ts`. The
  implementation plan may refine field names only by updating `rest-api.md`
  before tests.
- The workbench topology endpoint uses its own metric enum, aligned with
  SOW-0113: `multi` for the topology-only default multi-channel encoding plus
  `cost`, `duration`, `tokens`, `failures`, `fan_out`, and `ctx_pct` when
  computable. It must not silently reuse the legacy endpoint's
  `cost,tokens,duration,calls,ctx_pct` semantics without documenting the mapping.
- Consumer map to prove during planning: current per-session topology consumer
  is the Session Detail topology tab; global `/api/topology` consumers use a
  different endpoint and must remain unaffected. If no external
  `/api/sessions/:id/topology` consumers exist, extending or versioning the
  existing endpoint may be acceptable, but must be tested. Because ai-viewer is
  still a workstation development app, no external consumers are known today;
  the implementation plan must still state this assumption and search the repo
  before changing the endpoint shape.
  Separately, SOW-0108 owns display-label propagation to the global
  cross-session topology label source so the global page does not continue
  showing raw ai-agent v3 `parent` labels after display labels exist.
- Caller audit precondition: before implementing endpoint changes, the plan
  must enumerate every repo caller of `/api/sessions/:id/topology` and
  `/api/topology`, including API helpers and tests, and record whether each
  caller stays legacy, migrates to `/workbench`, or remains untouched. The audit
  output is committed into this SOW or its implementation-plan addendum with
  repo-relative path:line evidence and the date before endpoint code changes.
- Frontend consumer map must include `TopologyRenderer`, which is shared by the
  session topology path and the global topology page. New node/edge kinds must
  be routed through a workbench renderer mode or handled without breaking the
  global topology page.
- First-pass renderer strategy: add a separate workbench renderer path or an
  explicit `mode="workbench"` branch before introducing turn/subagent node kinds.
  Before choosing the branch, the implementation plan must cite the current
  `TopologyRenderer.tsx` prop interface and the global `Topology.tsx` consumer
  path so the mode split is grounded in current code.
  The global `/topology` page must keep its current agent-only renderer behavior.
  Existing global topology tests must be migrated or extended in the same work
  so the shared renderer API change cannot silently break the global page.
  Preserve the global page's node-click contract explicitly: current
  `frontend/src/pages/Topology/Topology.tsx` derives session navigation from
  `agent:` node ids. The workbench `session:`/`turn:`/`tool:` id schemes must not
  leak into global `/topology`, and regression tests must assert global topology
  clicks still produce valid `/sessions/<id>` URLs. Test files to preserve or
  extend include `frontend/src/pages/Topology/Topology.test.tsx`,
  `frontend/src/pages/SessionDetail/TopologyTab/TopologyRenderer.test.tsx`,
  `frontend/src/pages/SessionDetail/TopologyTab/TopologyTab.test.tsx`, and
  `frontend/src/viz/topology.test.ts`.
- Subagent-call edge metadata is computed during workbench graph build from the
  existing child-session relationship and summary fields; do not add a new
  recursive query per edge. If current child-summary fields are insufficient,
  extending the presenter row shape must be specified before implementation.
- Topology controls, including stress metric selector, layout mode, freeze, and
  fit-to-view, live in the SOW-0107 unified controls ribbon. The topology view
  must not retain its own nested control island.
	- Global workbench filters from SOW-0113 are represented as graph emphasis, not
	  silent node deletion, in the first pass. Matching turns/tools remain full
	  emphasis; non-matching visible nodes dim or show a non-match style while
	  preserving graph context. The `status=` filter uses session `effective_status`
	  for session nodes and raw turn/op status for turn/tool nodes. The `step=`
	  filter applies to turn/tool/op nodes by matching their underlying StepFilter
	  category; session nodes expose a count/badge of matching descendants and dim
	  only when no visible descendant matches. `subagent=all` clears subagent
	  emphasis and shows all visible nodes at normal emphasis. The topology control
	  surface must indicate when a filter is reflected as emphasis rather than
	  structural pruning. Dimming is bounded by the shared data-viz contrast
  contract: non-matching nodes retain enough opacity/stroke contrast that visible
  stress/failure indicators remain at least 3:1 against their background.
- Selection behavior stays turn-first:
  - clicking a turn node selects that turn;
  - clicking a single-call tool node selects the owning turn and sets
    `focus=op:<op-id>`;
  - clicking a multi-call / aggregate tool node selects the owning turn and uses
    `hi=topology:<selection_key>` or local expansion to highlight all owning
    ops. It must not invent one arbitrary `op_id` for the group;
  - clicking a child-session edge/link navigates or links to the child session,
    but does not inline the child session in the content pane. The link follows
    the umbrella/SOW-0113 child-navigation contract: `/sessions/:childId`, a
    breadcrumb in the header, and Browser Back to the parent workbench state.
  - clicking the root session node does not invent a primary turn selection. It
    either leaves the current selection unchanged or shows the shared
    session-level no-selection summary, per the final SOW-0107 content-default
    decision.
  - when SOW-0113 sets `sel=turn:<turn-id>` from the table, waterfall, URL, or
    browser history while topology is visible, the matching turn node is
    highlighted and, when feasible, centered/focused. If the selected turn is
    collapsed into an aggregate, the aggregate shows a visible "selected turn is
    inside this group" indicator rather than appearing unrelated to selection.
  - `focus=op:<op-id>` affects topology only when the focused op maps to a
    visible single-call tool node; otherwise topology ignores focus while keeping
    the owning selected turn highlighted. It must not invent an arbitrary node
    for non-node ops. If the focused op belongs to a turn currently collapsed
    into an aggregate, the aggregate shows the selected-turn-inside indicator
    only; the hidden op is not separately indicated until the aggregate is
    expanded into a visible single-call tool node.
    If the focused op belongs to a different turn than the current `sel=turn`
    during URL repair or stale async resolution, the focused op's owning turn is
    still represented: a visible single-call tool node receives a focused-op
    style in addition to the selected-turn highlight, and a collapsed aggregate
    receives a distinct focused-op-inside indicator separate from the
    selected-turn-inside indicator. The op is not separately indicated until the
    aggregate is expanded into a visible single-call tool node.
- Concurrent identical topology workbench requests are a planning decision, not
  incidental behavior. The implementation plan must either add single-flight
  coalescing for identical `(session_id, scope, metric)` requests or explicitly
  accept duplicate concurrent queries with measured justification on the shared
  fixture.

Accessibility contract required before implementation:

- Keyboard users can traverse the same selectable graph targets.
- Color/size are not the only stress signals; text labels or summaries expose
  metric values. Non-text selection, failure, and stress indicators meet at
  least WCAG 2.2 non-text contrast 3:1 against the visualization background and
  are tested against the SOW-0107 data-viz palette contract.
- A reduced-motion or stabilized layout path exists for force simulation.
- A nested text outline/list representation is available when the graph is too
  dense or for screen reader users. It follows session -> turns -> tools /
  child-session links, exposes metric values as text, and uses the same
  selection contract as the graph.
- Pan and zoom are pointer/wheel graph interactions in the first pass. Keyboard
  target reachability is provided by the nested text outline/list and by a
  keyboard-reachable fit-to-view action in the unified controls ribbon; the SOW
  does not require keyboard viewport panning unless implementation planning
  chooses to add it.

Risk and blast radius:

- Medium-high backend, high frontend visualization complexity.

Sensitive data handling plan:

- Graph labels in tests use sanitized or synthetic names. Do not record real
  prompt/tool content.

Implementation plan:

1. Define topology graph contract, stable node ids, aggregate interaction, and
   shared stress metrics. Write the stress glossary to the REST/API spec before
   SOW-0111 or SOW-0112 implementation begins.
1a. Consume SOW-0107's shared subtree session-id resolver/helper for the viewed
    session subtree and depth-limited graph build. This SOW must not create a
    parallel recursive subtree walker or CTE pattern.
2. Refactor topology building around a named backend strategy. Baseline:
   single-pass turn-keyed accumulator fed by ordered sessions/turns/ops/child
   links loaded in bounded SQL batches, with no per-node DB queries. If that
   benchmark fails or the code becomes unmaintainable, the implementation plan may
   switch to a pre-grouped two-pass builder with the same response contract and
   tests. The plan must pick one before presenter tests begin and benchmark it on
	   the shared fixture. Before choosing, benchmark the current
	   `session_topology_builder.go` op-stream builder on the same fixture as the
	   regression baseline, then benchmark the new turn-keyed accumulator. The
	   current builder cannot produce the required turn-level graph shape, so this
	   benchmark is not a keep-or-replace decision; it records regression baseline
	   evidence and validates the new builder's budget. The benchmark records
	   graph-build latency against the 500ms budget,
   SQL query count, allocation/peak-memory evidence available from the benchmark,
   and proves there are no per-node DB queries. If topology traversal needs a new
   index after SOW-0112 has claimed `0015`, this SOW owns the next available
   migration number, currently planned as `0016_*`. Any `0016` migration follows
   SOW-0107's umbrella chain-head rule: it bumps `presenter.SchemaVersion` and
   owns the single chain-head migration test only if it is the highest migration
   present when it lands.
3. Decide endpoint shape for turn-level topology without breaking existing
   topology consumers.
	4. Add presenter tests for recursive turn graph shape, cap/collapse behavior,
	   recursion-depth behavior, aggregation markers, aggregate-key resolver output,
	   topology-key stale/unknown behavior, HTTP method behavior, and endpoint
	   lifecycle compatibility. Add a node-presence parity regression test that runs
	   the legacy op-stream builder and the new workbench builder on the same
	   fixture and asserts the new output preserves or supersets the legacy
	   agent/tool node identities needed by global topology consumers. Concrete
	   parity mapping: a legacy `agent:<label>` identity must map to at least one
	   workbench `session:<id>` node whose display/raw agent label resolves to the
	   same sanitized label or to a documented SOW-0108 display-label successor; a
	   legacy `tool:<namespace>.<name>` identity must map to one or more workbench
	   `tool:<turn_id>:<namespace>.<name>` nodes or an aggregate preserving the same
	   namespace/name predicate. Missing mappings must be asserted explicitly, not
	   waived by comparing only node counts.
5. Add pure layout/graph tests for node/edge categories and a regression test
   that the global `/topology` page still renders the current agent-only graph
   after workbench topology mode is added.
6. Reuse existing interactive graph machinery and add collapse/fit-to-view where
   missing.
7. Wire graph selection to SOW-0113 shared content state.

Validation plan:

- Backend contract tests, pure graph tests, global `/topology` regression tests,
  Playwright pan/zoom/selection tests, a11y checks, performance smoke against
  the first committed shared fixture hash.
- Node-presence parity regression test: run the legacy op-stream topology
  builder and the new workbench builder on the same fixture and assert that
  legacy agent/tool identities needed by global topology consumers are preserved
  or explicitly superseded by tested workbench nodes. Agent parity maps legacy
  `agent:<label>` to workbench `session:<id>` display/raw agent labels; tool
  parity maps legacy `tool:<namespace>.<name>` to workbench tool nodes/groups by
  the same namespace/name. This is validation coverage, not only an execution-log
  note.

Artifact impact plan:

- Specs: `rest-api.md`, `ui-pages.md`, topology/a11y docs.
- SOW lifecycle: child of SOW-0107.

Open-source reference evidence:

- Check observability topology/trace graph patterns before implementation plan.

Open decisions:

1. Default stress metric:
   - A. Composite score from time, cost, failures, fan-out.
   - B. Duration by default with metric switcher.
   - C. Explicit multi-channel encoding without a composite default.
   Recommendation: C, long-term-best. It answers "where was the stress" without
   hiding the meaning behind an opaque score. Composite can be added later if it
   is explainable and shares definitions with SOW-0112.
2. API shape:
   - A. New endpoint for turn-level workbench topology.
   - B. Existing endpoint with a new explicit mode/version parameter.
   Recommendation: A if response shape diverges materially; B only if backward
   compatibility remains simple and tested.

## Plan

1. Run external gap review.
2. Resolve graph contract and stress metric decision.
3. Rerun the gap-analysis gate.
4. Draft implementation plan.

## Execution Log

### 2026-06-26

- Created focused SOW from topology feedback.
- Incorporated external reviewer round-1 findings: existing recursive topology
  capability, existing pan/zoom/drag, true turn-node contract, stress metric
  definitions, collapse/aggregation, API shape, and accessibility fallback.
- Incorporated external reviewer round-2 findings: backend complexity,
  explicit caps/depth/collapse behavior, subagent-call edge-vs-node decision,
  shared stress metric glossary, endpoint isolation, and turn-first topology
  selection behavior.
- Incorporated external reviewer round-3 findings: subagent-call as edge
  metadata only, direct-vs-subtree metric names, low-stress ranking/top-10
  collapse rule, performance budget, endpoint consumer map, unified controls
  ownership, and REST/API spec ownership for stress metrics.
- Incorporated external reviewer round-4 findings: current topology is a flat
  agent/tool graph with lineage edges rather than a recursive turn tree, turn
  nodes require backend builder restructuring, stable node ids and aggregate
  expand/collapse behavior are required, and shared `TopologyRenderer` impact
  must be included in the consumer map.
- Incorporated external reviewer round-5 findings: turn-level topology is a high
  backend change, collapsed aggregate ids use hash-of-sanitized-predicate rather
  than rank windows, and depth-4 semantics are measured from the viewed session
  subtree root.
- Incorporated external reviewer round-6 findings: aggregate hash predicates now
  name their sanitized inputs, aggregation order is depth-collapse then
  visible-cap then top-10 folding, pre-aggregation traversal must be bounded or
  benchmarked, and topology server-key resolver behavior is owned here.
- Incorporated external reviewer round-7 findings: current builder and consumer
  evidence is recorded, `ctx_pct` is retained or explicitly unavailable instead
  of silently dropped, resolver path/schema are delegated to SOW-0113's shared
  endpoint, the workbench topology metric enum is explicit, and renderer
  strategy must protect the global topology page.
- Incorporated external reviewer round-8 findings: topology labels must consume
  SOW-0108 `display_agent_label`, aggregate-node hashes now share SOW-0112's
  128-bit/collision contract, legacy `calls` is explicitly distinct from
  workbench `fan_out`, the no-known-external-consumer assumption must be
  verified before endpoint changes, and global topology renderer tests must be
  preserved or migrated with the workbench renderer strategy.
- Incorporated external reviewer round-9 findings: legacy topology endpoint
  lifecycle and HTTP method behavior are explicit, repo caller audit is a
  precondition, topology-key staleness semantics are defined, aggregate
  expansion cannot exceed the hard node cap, subagent edge metadata must avoid
  per-edge recursive queries, and validation now includes global topology
  regression coverage.
- Incorporated external reviewer round-10 findings: aggregate hash inputs now
  include active metric and non-contiguous predicate ids, topology labels may
  carry SOW-0108 client metadata, shared sanitizer use is explicit, stress
  metrics live in one `rest-api.md` glossary, aggregate tool clicks use
  highlight keys or expansion rather than arbitrary op focus, and child-session
  links follow the shared navigation/breadcrumb contract.
- Incorporated external reviewer round-11 findings: the shared `ctx_pct` metric
  definition must cite and preserve the existing topology-builder max
  `ctx_used / ctx_max` formula, and the global topology page's `agent:` node id
  navigation contract must be preserved by workbench renderer isolation tests.
- Incorporated external reviewer round-12 findings: SOW-0111 is now the first
  writer for the shared stress glossary, topology traversal must avoid recursive
  CTE driven large joins, global topology regression test files are named,
  selection reverse-sync from table/waterfall/URL into topology is required, and
  the backend builder strategy must choose and benchmark either a single-pass
  turn accumulator or a two-pass builder before presenter tests begin.
- Round 13 reviewer rerun returned no new actionable findings for this SOW; the
  only P2 finding was scoped to SOW-0113 clearing cached server-key highlights
  when metric/group controls change.
- Round 14 completed on 2026-06-27 with accepted findings clarifying stress
  glossary sequencing with SOW-0113, concrete workbench topology response schema,
  caller-audit evidence requirements, aggregate expand/collapse re-layout budget,
  builder benchmark criteria, migration-number ownership with SOW-0112, and
  fixture-hash dependency.
- Round 15 completed on 2026-06-27 with accepted findings clarifying topology's
  default current-subtree scope, `ctx_pct` scope labeling, the hard prerequisite
  that SOW-0113 creates the shared REST stress glossary section before child
  plan review, and the `SchemaVersion`/chain-head test companion for any `0016`
  migration.
- Round 16 completed on 2026-06-27 with reviewer findings scoped to SOW-0107,
  SOW-0108, and SOW-0112. This SOW consumes the umbrella breadcrumb
  prerequisite: topology child-link navigation cannot close until Session Detail
  exposes a server-side ancestor chain.
- Round 17 completed on 2026-06-27 with accepted findings clarifying top-level
  `metrics` response metadata, global topology display-label dependency,
  renderer-branch evidence, `focus=op` handling, and the single-flight or
  measured-duplicate decision for heavy topology endpoint requests.
- Round 18 completed on 2026-06-27 with accepted findings clarifying duration
  semantics for topology nodes, aggregate behavior for focused ops, keyboard
  pan/zoom scope, reduced-motion consumption, and the SOW-0111/SOW-0112 stress
  glossary sequencing gate.
- Round 19 completed on 2026-06-27 with accepted findings clarifying
  `effective_status` use for session nodes, visible running-node behavior,
  `last_activity_ts` in topology node responses, and WCAG 2.2 3:1 non-text
  contrast for topology stress indicators.
- Round 20 completed on 2026-06-27 with accepted findings requiring
  `effective_status` to be computed for every session node, defining
  `generated_at`, specifying root session node click behavior, and aligning any
  topology-support migration with SOW-0107's umbrella chain-head rule.
- Round 21 completed on 2026-06-27 with accepted findings clarifying zero-turn
  graph behavior, filter-emphasis semantics, and the default topology builder
  strategy before benchmark fallback.
- Round 22 completed on 2026-06-27 with accepted findings clarifying own-session
  versus subtree metric scopes for session nodes, one captured `now` value per
  topology response, zero-turn live insertion behavior, hash-collision failure
  tests through a stub seam, contrast preservation under filter dimming, and the
  requirement to benchmark the current topology builder before replacing it.
- Round 23 completed on 2026-06-27 with accepted findings clarifying default
  subtree stress encoding for session nodes, `ctx_pct` handling when `ctx_max`
  varies across ops, explicit `step=`/`subagent=all` filter behavior, top-level
  metrics metadata shape, mandatory replacement framing for the turn-level
  builder benchmark, and node-presence parity regression tests against legacy
  topology output.
- Round 24 completed on 2026-06-27 with accepted findings clarifying
  `metric=multi` as the topology-only URL/default representation for
  multi-channel stress encoding and moving the legacy node-presence parity
  regression into the validation plan. The Mimo migration-number finding was
  rejected as stale: the current text already says SOW-0112 owns `0015` and
  topology uses the next available `0016_*` if needed.
- Round 25 completed on 2026-06-27 with accepted findings clarifying the
  force-layout non-convergence timeout and the concrete legacy-to-workbench
  node-presence parity mapping for agent and tool identities.
- Round 26 completed on 2026-06-27 with accepted findings aligning topology
  failure metrics with the shared terminal-failure family, extending force-layout
  freeze behavior to initial and refresh-driven simulations, clarifying focused
  op indicators when the owning turn is collapsed or differs during repair, and
  explicitly consuming SOW-0107's shared subtree resolver/helper.
- Round 27 completed on 2026-06-27 with accepted findings clarifying shared
  stress metric aliases/count semantics, unknown raw op-kind count behavior,
  aggregate tool count semantics, `root_session_id` versus `scope_session_id`,
  topology pan/zoom persistence, and `resync`-driven force-layout timeout
  coverage.
- Round 28 completed on 2026-06-27 with no topology-specific P0/P1/P2 changes;
  accepted shared-key concerns are handled by SOW-0107's new shared hash-helper
  decision before topology/statistics duplicate selection-key logic.
- Round 29 completed on 2026-06-27 with one accepted P2: topology now has the
  same explicit degraded display-label branch as statistics if SOW-0108 is
  delayed or rejected. P3 wording also clarified `metric=multi` endpoint
  semantics and SOW-0107 ownership of the hash-helper decision.
- Round 31 completed on 2026-06-27 with no topology P0/P1/P2 changes. Mimo's
  non-positive failure-count concern is already owned by SOW-0107 step 5b before
  topology/statistics acceptance.

## Validation

Pending.

## Outcome

Pending.

## Followup

None yet.

## Regression Log

None yet.
