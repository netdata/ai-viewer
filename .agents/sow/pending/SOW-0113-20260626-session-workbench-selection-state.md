# SOW-0113 - Session Workbench Selection State

## Status

Status: open

Sub-state: created from external gap review round 1 for SOW-0107 through
SOW-0112; external gap review rounds 1 through 28 completed and findings
incorporated; gap-review rerun pending after round-28 changes.

## Requirements

### Purpose

Define one shared selection, URL, highlight, and focus contract for the Session
Detail workbench. The table, waterfall, topology, statistics, and content pane
must agree on what is selected and how that selection survives deep links,
browser back/forward, live updates, and keyboard use.

### User Request

The user expects table, waterfall, topology, statistics, raw data, and content
to behave like one coherent workbench:

- Clicking a turn in the table expands the table, focuses the waterfall, and
  renders the turn in the content area.
- Clicking a turn in the waterfall does the same.
- Clicking topology nodes and statistic rows should update the content area or
  highlight related work where practical.
- No sidebar/drawer should own detail state.

### Assistant Understanding

Facts:

- The current Session Detail URL and selection model is op-first. Existing code
  uses selected op state and visualization/table tab params.
- SOW-0109, SOW-0110, SOW-0111, and SOW-0112 all need shared selection, but none
  should define it independently.
- External reviewers unanimously identified this as a cross-cutting prerequisite
  before implementation planning.

Inferences:

- Selection should be turn-first, with optional op/step focus inside the selected
  turn.
- Topology and statistics may select broader objects than one turn, so the
  contract must support typed selections rather than a single bare id.
- URL state must be stable enough for screenshots, support links, and browser
  navigation, while local UI-only state such as temporary hover can remain local.

Unknowns / required decisions:

- Whether the first implementation supports one selected object at a time or
  multi-select.
- Whether legacy `?op=<id>` links are migrated to `?sel=op:<id>` or preserved
  as aliases.
- Which selection types are first-class in the initial implementation.

### Acceptance Criteria

- One spec section defines the Session Detail selection state contract.
- URL params have stable names, defaults, precedence, and legacy aliases.
- At minimum the contract supports:
  - selected turn,
  - optional selected op/step inside that turn,
  - selected topology node,
  - selected statistic bucket/dimension,
  - active visualization mode,
  - active table/raw mode,
  - active filters/highlights.
- The table, waterfall, topology, statistics, and content pane consume the same
  selection contract instead of maintaining incompatible selected state.
- Browser back/forward replays selection changes without losing the active view.
- Live SSE/session updates do not clear selection unless the selected object no
  longer exists; in that case the UI degrades to the nearest available parent
  selection with a visible state update.
- Keyboard and screen-reader focus behavior is defined for selection changes.
- Tests cover URL round trips, legacy op deep links, invalid selection params,
  selection synchronization between table and waterfall, and content pane
  rendering after selection.
- Legacy bare `tab`, `tab:viz`, `tab:bottom`, `stepKindFilter`, and `op` params
  remain accepted aliases or are explicitly dropped with evidence. Existing app
  links still emit bare `?tab=topology`, so that alias must be handled before
  implementation can close.
  Before implementation, audit every search param currently emitted or accepted
  by `SessionDetail`, `TraceTab`, `TopologyTab`, `TimelineTab`, `LogsTab`, and
  `RawDataTab`; any param not listed in this SOW must be explicitly mapped,
  ignored with visible repair, or rejected.
- Existing persisted split-layout state is covered by SOW-0107 and must not
  override the redesigned selection/layout model.

## Analysis

Sources to check during implementation:

- `frontend/src/pages/SessionDetail/UnifiedView/UnifiedView.tsx`
- `frontend/src/pages/SessionDetail/TraceTab/TraceTab.tsx`
- `frontend/src/pages/SessionDetail/TraceTab/ByTurnWaterfall.tsx`
- `frontend/src/pages/SessionDetail/TraceTab/EventList.tsx`
- `frontend/src/pages/SessionDetail/TopologyTab/TopologyTab.tsx`
- `frontend/src/pages/SessionDetail/TimelineTab/TimelineTab.tsx`
- `frontend/src/components/TurnView`
- `frontend/src/api/types.ts`
- `internal/presenter/session_detail.go`
- `.agents/sow/specs/ui-turn-view.md`
- `.agents/sow/specs/rest-api.md`
- `.agents/sow/specs/sse-protocol.md`

Current state:

- The current workbench has independent view state and click-to-detail behavior.
- The drawer path makes op detail feel separate from table/visualization
  selection.
- URL state is not yet designed around turn-first selection.

External gap review round 1 findings incorporated:

- All six external reviewers flagged missing shared selection/URL state as a
  blocking cross-SOW gap.
- Reviewers warned that duplicating the selection contract in table and
  waterfall SOWs would create incompatible behavior.
- Reviewers required explicit URL state, deep-link migration, browser
  back/forward, a11y/focus, and live-update behavior.

External gap review round 2 findings incorporated:

- Reviewers found legacy `tab:viz` / `tab:bottom` migration missing. The
  contract now accepts both legacy names and new names.
- Reviewers found colons in ids and typed selection parsing needed a precise
  serialization rule. The contract now splits on the first colon and URL-encodes
  the id portion.
- Reviewers found `sel`, `focus`, filters, and highlights were conflated. This
  SOW now separates primary selection, in-turn focus, and set-like highlights.
- Reviewers found stale selection, SSE invalidation, browser history
  push/replace, keyboard flow, and filter scope were under-specified. These are
  now contract requirements.

External gap review round 3 findings incorporated:

- Reviewers found the legacy alias inventory was incomplete: existing
  `SessionRow` links still emit bare `?tab=topology`, and older tests cover
  `?tab=trace`. This SOW now owns bare `tab` migration.
- Reviewers found JSON-node focus ids, highlight-set caps, filter/highlight
  precedence, and invalid selection type fallback needed explicit rules.

Risks:

- Overloading URL params with every transient interaction can make links brittle.
- Under-specifying selection state can make each visualization behave like a
  separate mini-app.
- Multi-select can complicate performance and content rendering. Single-select
  is safer for the first implementation.

## Pre-Implementation Gate

Status: needs-review

Problem / root-cause model:

- The redesigned Session Detail screen only works if every major area agrees on
  one selection model. Without this SOW, child SOWs would each invent their own
  state shape and the view would remain patchwork-like.

Evidence reviewed:

- External review outputs for SOW-0107 through SOW-0112.
- Existing SOW drafts all referenced shared selection but did not own it.
- Current code has op-first URL state and separate tab-local state.

Affected contracts and surfaces:

- Frontend route `/sessions/:id`.
- URL search params.
- Shared React state/context or equivalent local state owner.
- Table, waterfall, topology, statistics, content pane, and tests.
- Specs: `ui-turn-view.md`, `ui-pages.md`, and possibly `rest-api.md` if links
  or selection keys depend on API identity fields.

Existing patterns to reuse:

- URL-synced state via `useSearchParams`.
- Current selected-op deep-link behavior.
- Existing stable ids from presenter responses.
- Existing Playwright deep-link tests.

Risk and blast radius:

- Medium frontend blast radius and high coordination value.
- Backend risk is low unless selection keys require additional presenter fields.

Sensitive data handling plan:

- URL state must never contain raw prompt text, payload content, private paths,
  or unredacted tool output. Selection params use stable ids, types, and small
  enum values only.

Target contract to decide before code:

- Recommended state shape:
  - `view=<waterfall|topology|stats>` for the first-pass visualization mode,
    limited by SOW-0107's final mode inventory. `timeline`, `flame`, and
    detailed-waterfall values are not first-pass valid values unless SOW-0107
    explicitly retains those modes before implementation planning.
  - `table=<turns|events|logs|raw>` for the table/raw area.
    When `view=stats`, `table` is preserved for round-trip/back-navigation but
    ignored for rendering because stats owns the table/raw area with grouped
    metric tables. The controls ribbon must show the table selector disabled or
    derived, not an active stale `turns`/`events`/`logs`/`raw` state. Leaving
    stats restores the preserved table mode if it is still valid.
  - `sel=<type>:<urlencoded-id>` as the primary selected object. Parsers split
    on the first colon only, then URL-decode the id portion.
  - `op=<op_id>` remains a legacy alias and migrates internally to `sel=op:<id>`
    plus the owning selected turn. The schema requires every op to have a
    `turn_id`, including synthetic turn-0/system ops, so this derivation is
    required rather than best-effort.
    If SOW-0107 selects a lightweight turn-summary/selected-turn data contract,
    this SOW must consume a cheap cold-link resolver before preserving `?op=`:
    preferred first pass is `GET /api/sessions/:id/ops/:opId` returning at least
    `{ op_id, turn_id, status }` with normal REST method/error behavior. An
    embedded op-to-turn index in the turn-summary response is acceptable only if
    the measured payload stays within budget. Without one of those paths, legacy
    `?op=` links must degrade visibly instead of loading every op in the session
    solely to find the owning turn. SOW-0107 owns the producer-side REST/API
	    entry when the lightweight contract is chosen; this SOW owns URL parsing,
	    resolver invocation, and canonical state emission. Transport failures for
	    this op-to-turn resolver are distinct from "op not found": timeout, network
	    error, and 5xx keep a compact "selection temporarily unavailable" state and
	    retry with the same bounded defaults used by highlight resolvers (base 500ms,
	    max 8s, 3 consecutive failures) before clearing. `404` remains the visible
	    op-not-found repair state; invalid ids remain `400`/invalid-link state.
	    If SOW-0107 chooses the all-detail Session Detail payload instead of the
	    lightweight path, legacy `op`, `sel=op`, and `focus=op` repair resolve from
	    the already-loaded all-turn/all-op response; they must not issue the
	    lightweight `ops/:opId` request solely for repair. If the lightweight path is
	    chosen, use `GET /api/sessions/:id/ops/:opId`. If neither the all-detail
	    response nor the resolver is available, the UI shows a visible unresolved
	    link state and does not load every op in the session just to repair the URL.
  - `tab=<trace|topology|timeline|stats|overview|turns|logs|raw>` remains a
    legacy alias while existing code emits it. Current `SessionDetail` ignores
    bare `tab`, so this SOW intentionally repairs currently-broken legacy links
    and transition-period links. Mappings: `trace -> view=waterfall`,
    `topology -> view=topology`, `stats -> view=stats`, `logs -> table=logs`,
    `raw -> table=raw`, `overview` and `turns` normalize to the default
    workbench state (`view=waterfall`, `table=turns`). SOW-0107 assigns both
    legacy tabs to dead-code cleanup or extraction; there is no separate
    replacement mode unless a later SOW adds one. `timeline` maps to
    the retained timeline mode only if SOW-0107 keeps Timeline; otherwise it
    normalizes to `view=waterfall` with a visible legacy-link repair state.
  - `tab:viz=<mode>` remains a legacy alias for `view=<mode>`.
  - `tab:bottom=<mode>` remains a legacy alias for `table=<mode>`.
	  - `stepKindFilter=<kind>` remains a legacy alias for `step=<kind>` when the
	    value is one of the existing `StepFilter` values (`user`, `reasoning`,
	    `assistant`, `tool`, `session`, `compaction`, or `all`). There is no
	    verified legacy URL contract for `stepKindFilter=llm`, but `llm` is a raw
	    `ops.kind` value and current TypeScript types allow it. To avoid broken old
	    or hand-authored links, `stepKindFilter=llm` is accepted as a legacy input and
	    normalized to `step=assistant`. The canonical workbench param is `step=`,
	    not `kind=`, because `kind` already names the raw `ops.kind` column. If a
	    future raw op-kind filter is needed, it must use a separate `op_kind=` param.
	    The implementation plan must cite the exact current frontend type/source
	    that allows `llm` if any emitted link currently depends on it. Even if no
	    emitted link is found, hand-authored `stepKindFilter=llm` URLs still
	    normalize deterministically to `step=assistant`; the audit only decides
	    whether this is documented as an emitted legacy contract or as
	    hand-authored-link repair coverage.
	    `all` removes the `step` filter on the next normalized write. The enum must
	    be specified once in
    `ui-turn-view.md` and mirrored by shared frontend constants/tests consumed
    by SOW-0109, SOW-0110, and SOW-0111 plans instead of duplicated ad hoc. The
    spec must document derivation rules, not just literal values: `user` maps to current
    `kind=internal && name=user_input`, `assistant` maps to current
    `kind=llm && name=message`, `tool` maps to tool ops, `session` maps to
    sub-session ops, `reasoning` maps to reasoning steps, `compaction` maps to
    compaction steps, and `all` removes the filter on normalized writes.
    If both canonical `step` and legacy `stepKindFilter` are present, `step`
    wins. If only `stepKindFilter` is present, it normalizes to `step` on the
    next canonical write.
  - `focus=<type>:<urlencoded-id>` only for optional in-turn focus. First-pass
    stable focus ids are `op:<op_id>` and any `step:<stable-step-id>` the plan
    proves from presenter data. Payload ids are not URL-stable in the first pass
    unless the plan proves their stability across reingest/SSE; otherwise
    payload expansion focus remains local UI state. JSON-node path focus is not
    part of the first-pass URL contract unless a stable path identity is
    specified and tested.
    If `focus` is present without `sel`, the selection owner attempts to resolve
    the focus target to its owning turn. For `focus=op:<id>`, successful
    resolution canonicalizes to `sel=turn:<derived>` plus the same `focus` on the
    next write and shows a compact link-repair state. If resolution fails, the
    workbench clears to no-selection with a visible "op not found" repair state.
    If both `sel` and `focus` are present but `focus=op:<id>` belongs to a
    different turn than `sel=turn:<id>`, the op owner is authoritative for the
    canonical turn selection because `focus` points to a concrete server row. The
    next normalized write repairs `sel` to the op's owning turn and shows a
    compact link-repair state. If the op resolver returns 404, the `focus` value
    is dropped and the valid `sel=turn` is kept; if the resolver has a transport
    failure, the UI keeps the current `sel` while showing the temporary resolver
    error state.
  - `hi=<type>:<urlencoded-id>[,<type>:<urlencoded-id>...]` for highlights that
    can represent sets or statistic/filter matches without replacing `sel`.
	  - `metric=<cost|duration|tokens|failures|fan_out|ctx_pct|multi>` and
	    `group=<model|provider|agent|tool|client|source_format|status|error_class>`
	    for view-specific metric controls. This group enum mirrors the canonical
	    `rest-api.md` "Stress metrics and dimensions" glossary consumed by
	    SOW-0111 and SOW-0112. Invalid values fall back to defaults.
	    Metric defaults are view-dependent. If `metric` is absent and
	    `view=topology`, topology uses its SOW-0111 multi-channel stress encoding
	    (`metric=multi`). If `metric` is absent and `view=stats`, statistics uses
	    `metric=cost`. `metric=multi` is valid only with `view=topology`; when
	    `view=stats`, it normalizes to `cost` with a visible repair state on the
	    next canonical write. `metric=multi` is URL-stable while topology is active;
	    switching to another view repairs it only for that view and switching back to
	    topology without an explicit metric restores the topology default.
	    If `view=waterfall`, `metric` and `group` are inactive in the first pass:
	    the controls ribbon hides or disables those controls and canonical URL
	    emission removes inactive metric/group params on the next workbench write.
	    Waterfall remains time-axis driven; any future color-by-metric waterfall
	    mode must add a view-local control and tests before consuming the shared
	    metric enum.
	    `ctx_pct` is context-pressure percentage and is available only when the
	    source rows include enough model/token data to compute it.
    SOW-0113 owns creating the `rest-api.md` "Stress metrics and dimensions"
    section if it is absent and writing the initial URL enum skeleton there so
    parsing can land before SOW-0111/SOW-0112. SOW-0111 later owns the metric
    definitions and SOW-0112 owns statistics row-shape extensions; this SOW must
    not duplicate divergent semantics.
    Initial `rest-api.md` skeleton owned here:
    "Stress metrics and dimensions define URL-safe enum names shared by Session
    Detail workbench controls. First-pass metrics are `cost`, `duration`,
    `tokens`, `failures`, `fan_out`, and `ctx_pct`; first-pass groups are
    `model`, `provider`, `agent`, `tool`, `client`, `source_format`, `status`,
    and `error_class`. SOW-0111 owns topology metric definitions and SOW-0112
    owns statistics row-shape semantics."
    Skeleton minimum fields are enum name, display label, unit/scale when known,
    source table/column when known, null/unavailable semantics, owner SOW, and a
    pointer to the final-definition SOW. SOW-0113 may leave formula fields empty
    only with an explicit "defined by SOW-0111/SOW-0112" marker; it must not
    invent temporary formulas.
	  - `step=<...>` / `status=<...>` / `subagent=<...>` for global workbench
	    filters when implemented. `step` is the StepFilter enum; it is not raw
	    `ops.kind`.
	    First-pass `status` values are normalized operator-facing filter values
	    `failed`, `running`, `stale`, `completed`, and `other`; child views document
	    whether they apply these to raw op/turn status or session
	    `effective_status`. First-pass `subagent` is a tri-state enum
		    `with_subagents|without_subagents|all` filter, not a specific subagent-name
		    selector. All three filters are URL state when visible in the global controls
		    ribbon; view-local refinements must use separate view-owned params.
		    Status normalization is shared and explicit:
		    `completed -> completed`, `running -> running`,
		    `failed|abandoned|interrupted|aborted -> failed`, derived session `stale -> stale`,
		    and any unknown or future value -> `other`. Raw op rows cannot be derived as
		    `stale`; only session/effective-status contexts can match `status=stale`.
		    The `failed` normalized value is the same terminal-failure family used by
		    SOW-0107 turn-summary `failure_count`, SOW-0111 topology failure stress,
		    and SOW-0112 statistics `failure_count`/`failure_rate`.
		    The implementation plan must audit representative raw `ops.status` values
		    before emitting the final enum constants, so op-status filtering does not
		    silently drop a source-specific status.
		    When multiple global filters are active, `step`, `status`, and `subagent`
		    combine by intersection. No child view may treat them as a union unless a
	    later SOW adds a separate explicit mode and tests.
	    Shared first-pass application semantics: filters preserve the turn-first
    workbench structure unless a child SOW explicitly defines a filtered-axis
    mode. The table may hide non-matching nested rows while keeping their parent
    turn context; waterfall/topology emphasize matches and dim or annotate
    non-matches rather than deleting timeline/graph context; content renders the
    selected turn with matching steps emphasized and non-matching sections
    collapsed only when the collapse is reversible and announced. `status=`
    applies to session `effective_status` for session nodes/child summaries and
    raw turn/op status for turn/op rows, bars, and steps. UI labels must identify
    which scope is being filtered.
- Recommended first implementation: single selected object at a time, plus
  optional in-turn focus. Multi-select is deferred unless a child SOW proves it
  is necessary.
- Selection types to support initially:
  - `turn`
  - `op` as a parse-only legacy/deep-link input that resolves to a selected
    owning turn plus in-turn op focus before canonical URL emission.
- Topology clicks map into the turn-first model: turn nodes set
  `sel=turn:<turn-id>`; single-call tool nodes set the owning turn plus
  `focus=op:<op-id>` when an owning op is known; multi-call / aggregate tool
  nodes set the owning turn plus `hi=topology:<key>` or local graph expansion,
  not an arbitrary single op focus. Aggregate topology nodes use
  `hi=topology:<key>` or local graph expansion, not primary content selection.
- Table clicks use the same turn-first model: clicking a nested op row inside an
  expanded turn sets `sel=turn:<turn-id>` and `focus=op:<op-id>`. It must not set
  `sel=op:<op-id>` in the first implementation, so table, waterfall, and
  topology use the same in-turn focus contract.
  Clicking an already-selected turn in table, waterfall, or topology is a no-op
  in the first implementation: the turn remains selected/expanded and content
  remains visible. Collapse-on-reclick, deselect-on-reclick, and multi-expanded
  turns are deferred unless a later SOW adds explicit controls and tests. Tests
  assert the second click does not clear `sel`, `focus`, or the content pane.
- `table=events` is a secondary flat session-subtree op/event list. Event rows
  with stable op targets set `sel=turn:<turn-id>` plus `focus=op:<op-id>`;
  event rows without stable targets use local row focus only. `table=logs`
  follows the same rule only when a log row carries a stable turn/op target;
  otherwise log focus is local and content remains unchanged. `table=raw` never
  mutates `sel`, `focus`, or `hi`; raw row expansion is local state only.
- Statistic buckets are not primary selections. They use `hi=stat:<selection_key>`
  or a server-key URL state owned jointly with SOW-0112.
- Changing `metric=` or `group=` clears existing server-key highlights
  (`hi=stat:*` and `hi=topology:*`) and evicts their cached resolver entries,
  because aggregate and selection keys encode the active metric/group at
  issuance. Direct `hi=<type>:<id>` highlights remain subject to normal stale
  target validation.
  Changing global filters (`step`, `status`, or `subagent`) follows the same
  rule for server-key highlights and resolver cache entries because SOW-0112
  stat keys include the canonical filter tuple and SOW-0111 topology keys may
  encode filter-sensitive aggregate predicates. Direct id highlights remain
  valid only if the target still exists and still passes the current filter
  intersection.
  Changing `view=` also cancels or discards in-flight resolver requests for the
  previous view's server-key source when that source is no longer active. A stale
  topology resolver result must not populate cache while stats is active, and a
  stale stat resolver result must not populate cache while topology is active. If
  the user later returns to the view and the key is still present and valid, the
  owner re-resolves it against the current `metric`, `group`, filters, and
  session revision before child views consume it.
- Reverse synchronization is required. When `sel=turn:<turn-id>` is set by the
  table, waterfall, URL, or browser history and `view=topology`, topology
  highlights and, when feasible, centers/focuses the matching turn node. When the
  node is collapsed, topology highlights the containing aggregate with a visible
  selected-descendant state. When `view=waterfall`, the waterfall expands/focuses
  the selected turn without requiring a second click.
- Invalid or stale selection params show the nearest valid parent state and do
  not crash or clear the whole workbench.
- URL precedence:
  - New params win over legacy aliases.
  - If both `sel` and legacy `op` are present, `sel` wins and `op` is ignored.
  - Invalid `view` / `table` values fall back to defaults and are normalized on
    next write.
  - `view=stats&table=<turns|events|logs|raw>` is valid but table rendering is
    stats-owned; the next write preserves `table` unless the user changes it or
    leaves stats. Component tests must cover this state.
  - Unknown `sel` types are invalid and clear to session-level default/no-turn
    state with a visible state update.
  - `sel=op:<op-id>` is accepted only as a compatibility/deep-link input. On
    parse it uses the same owning-turn resolver as legacy `op=<op-id>`, exposes
    workbench state as `sel=turn:<derived-turn-id>` plus `focus=op:<op-id>`, and
    re-emits canonical state that way on the next write. The content pane never
    renders directly from `sel=op` without an owning turn. If `sel=op:<id>` or
    legacy `op=<id>` resolves to an unknown op, the selection clears to
    no-selection with a visible "op not found" state; the workbench must not
    guess an adjacent or first turn.
  - Malformed typed params are invalid and clear only that state slice: missing
    colon, empty type, empty id after URL-decoding, invalid percent-encoding, or
    unsupported type all normalize to the relevant default with a visible repair
    state. Double colons are allowed inside the id because parsers split on the
    first colon only.
- URL emission policy: new workbench code writes canonical params (`view`,
  `table`, `sel`, `focus`, `hi`, `metric`, `group`, and `step`) and reads legacy
  aliases only for compatibility. Legacy aliases are normalized to canonical
  params on the next explicit user write or visible legacy-link repair. Existing
  emitted links such as `SessionRow` parent links must either keep working as
  read aliases or be changed to canonical `?view=...` output in the same SOW.
  Compare page links are a first-pass canonical emitter: normal Compare ->
  Session Detail navigation must use canonical workbench params such as
  `?view=waterfall&table=turns` and must not emit legacy `tab=` / `op=` unless a
  dedicated legacy repair test is intentionally exercising those aliases.
  When multiple highlight items are valid, canonical emission writes one `hi`
  param with comma-separated `<type>:<encoded-id>` items, not repeated `hi`
  params. Emission order is deterministic: direct highlights first in stable
  parsed order, then `stat:<key>`, then `topology:<key>`.
- History policy:
  - Visualization/table mode changes push history.
  - Global filter changes (`step`, `status`, `subagent`) push history unless the
    implementation plan proves this creates noisy browser behavior.
  - View metric/group changes (`metric`, `group`) use replace by default.
  - Initial legacy-link repair and initial canonical normalization also use
    replace, not push, so the browser history does not gain a synthetic entry
    for the same logical page load.
  - Selection/focus changes inside the same mode use replace by default, so the
    browser Back button does not replay every hovered/clicked row.
  - A deliberate "open as deep link" action may push if implementation planning
    defines one.
	- Stale/live-update policy:
	  - If selected op disappears and its parent turn still exists, fall back to
	    `sel=turn:<parent-turn-id>` and show a visible state update.
	  - If selected turn disappears, clear to the session-level default/no-turn
	    state and show a visible state update. Do not silently move to an adjacent
	    turn.
	  - If the active session itself disappears or a required re-fetch returns
	    `404`, clear session-local selection/highlight/focus state and hand off to
	    SOW-0107's route-level "session no longer available" state. Do not render
	    stale child-view data for a deleted/missing session.
	  - If the selected turn and focused op still exist but their rows/payload
    metadata changed after reingest, a debounced `session_changed` re-fetches the
    selected-turn detail and re-renders content without clearing selection.
    Tests cover "turn present, op present, content changed" so the content pane
    cannot keep stale payload metadata indefinitely.
  - If highlighted topology aggregate or statistic bucket no longer exists, clear
    the highlight/filter and show the same visible state update used for stale
    selections.
  - The visible state update is one shared workbench notice component with
    variants for `selection`, `highlight`, or `both`, role `status`, polite
    announcement, and a sanitized human-readable subject.
- Filter scope:
  - Global filters such as kind/status/subagent apply to table, waterfall, and
    content. Topology/statistics may show full-tree data but must visibly report
    whether filters are ignored or reflected.
  - Manual filters and stat-bucket highlights combine by intersection when both
    are active. A stat-bucket click replaces the previous stat-driven highlight
    but does not delete manual filters unless the user clears filters. If the
    user changes a global filter after a server-key stat/topology highlight was
    issued, the server-key highlight clears with the visible highlight-state
    update because its issuance context no longer matches the canonical filter
    tuple. The user can then click a bucket/aggregate again under the new filter
    context.
- Highlight behavior:
  - `hi` is replace-on-set for the same highlight source; deletion of the param
    clears highlights;
  - first pass caps URL-encoded highlights at 50 ids. Larger result sets use a
    server-side key form rather than listing every id in the URL:
    `hi=<source>:<selection_key>`, where `<source>` is `stat` or `topology`.
    SOW-0112 owns stat `selection_key` production; SOW-0111 owns topology
    aggregate keys; SOW-0113 owns URL parsing, clearing, precedence, and the
    shared frontend consumption path.
    The 50-id cap applies to direct, non-server-key highlight ids total across
    all sources in one URL. First-pass server-key highlights are capped to one
    `stat:<key>` and one `topology:<key>` at a time; setting a new key for the
    same source replaces the previous key.
	    Resolved server-key highlights also have a combined first-pass consumption
	    cap: after resolving the active stat key and topology key, the shared owner
	    may expose at most 5000 unique turn ids, 5000 unique op ids, and 5000 unique
	    node ids total to child views. Matches beyond those caps, including exactly
	    capped responses where `total_*` exceeds the returned id count, remain
	    counted in resolver totals with `truncated=true` and degrade to aggregate/
	    count indicators rather than applying per-row/per-node highlight classes to
	    every match.
	  - Hand-authored URLs with more than 50 direct highlight ids are invalid: the
	    owner clears the highlight with the same visible stale/invalid-highlight
	    notice rather than truncating silently. Fifty URL-encoded UUID-like ids is
	    roughly below 2 KiB, leaving room for the rest of the workbench URL. If the
	    total normalized URL would exceed a plan-defined 4096-byte first-pass cap,
	    direct highlights are rejected and the UI must use server-key highlights
	    instead.
	    The 4096-byte cap is a Chromium/Playwright first-pass browser cap; if
	    Firefox/Safari support becomes in-scope, the cap must be re-tested and
	    lowered if needed. Decoded direct ids in `sel`, `focus`, and direct `hi`
	    items are capped at 256 bytes each. Decoded server-key highlight keys are
	    capped at 512 bytes each before resolver dispatch. Over-limit ids/keys fail
	    closed with the visible invalid-link/highlight state.
  - When one `hi=stat:<key>` and one `hi=topology:<key>` are active at the same
    time, the server-key result exposed to child views is the intersection of
    the two resolved sets by target type. Empty intersection is a visible
    "no overlapping matches" state, not a fallback union. The notice names the
    conflicting highlight sources (`stat` and `topology`) and offers separate
    clear actions for each source. Direct manual filters still intersect with
    the resulting server-key set as described above. Direct highlight ids remain
    additive: after the server-key intersection is computed, direct ids are
    unioned with that server-key result, and the combined set is then capped by
    the normal resolver caps. Tests must cover overlapping and non-overlapping
    stat/topology keys plus mixed direct/server-key highlights.
  - `hi` parsing is explicit: a comma-separated value is parsed as a list of
    `<type>:<id>` highlight items. A single value without commas is parsed by
    source/type; `stat:<key>` and `topology:<key>` are server-key highlights,
    while any other recognized type is a one-item direct highlight. Unknown
    types are invalid and clear with a visible state update. Mixed direct and
    server-key items are parsed item-by-item: direct ids are added as direct
    highlights and server-key items are resolved before child views consume the
    final set. If both `stat` and `topology` server-key sources are present, the
    intersection rule above overrides simple union for those server-key results;
    otherwise resolved server-key results are unioned by source. Direct ids are
    always unioned with the resolved server-key result, then the normal caps
    apply. Parsing splits on unencoded commas before URL-decoding each id
    portion; encoded commas (`%2C`) inside an id are decoded only after item
    splitting so they cannot split the list.
  - server-key highlights are not usable by child views until resolved. The
    shared selection owner calls the appropriate resolver, normalizes the result
    to bounded `turn_ids`, `op_ids`, optional `node_ids`, optional turn ranges,
    match counts, optional source-table counts, and `truncated/stale` status,
    then exposes that resolved set to table, waterfall, topology, statistics,
    and content. A stale/unknown key clears with a visible state update. SOW-0111
    owns topology resolver semantics; SOW-0112 owns stat resolver semantics.
  - shared resolver endpoint contract:
    `GET /api/sessions/:id/highlights/:source/:key`, where `source` is `stat`
    or `topology` and `key` is URL-encoded. `200 OK` returns
    `{ source, key, status, turn_ids, op_ids, node_ids?, turn_ranges?,
    total_turns, total_ops, total_nodes?, source_table_counts?, truncated,
    summary }`, with
    `status` one of `resolved`, `stale`, or `unknown`. Unknown sessions still
    use the normal session `404`; stale/unknown keys use `200` with status so
    the UI can clear the highlight with a visible state update. `node_ids` is
    optional and normally present only for topology keys. First-pass response
    cap: at most 5000 `turn_ids`, 5000 `op_ids`, and 5000 `node_ids` per
    response; broader matches set `truncated=true` and still return counts and
    optional `turn_ranges`. `summary` is a sanitized object, not a raw string:
    `{ label, reason?, dimensions? }`, where every value follows the same
    display sanitization rules as SOW-0108 and never contains prompt/payload
    text. `source_table_counts` is required for stat keys whose source can mix
    `ops` and `turns` rows, such as `error_class`; direct `op_ids` imply `ops`
    and direct `turn_ids` imply `turns`, but the counts let statistics preserve
    the source-table distinction after resolution. `stale` means the key is
    recognized but the resolved membership no longer matches issuance-time
    membership after ingest/SSE changes; `unknown`
    means the key cannot be mapped to a known sanitized predicate for this
    session. The REST/API spec delta must define this schema once before
    SOW-0111 or SOW-0112 implementation plans finalize.
  - Successful resolver responses are cached by
    `(session_id, source, key, metric, group, filters)` until the next
    `session_changed` for that session or until the metric/group/filter tuple
    changes. Pending resolver promises are stored in the same cache so
    concurrent callers for the same tuple await one in-flight request. A matching
    SSE event marks the cached response stale and schedules the debounced
    revalidation path; repeated clicks on the same key before a session change
    and with the same metric/group/filter tuple reuse the cached result. On
    active session-id change,
    cache entries and in-flight requests for the previous active session are
    evicted before child views consume the new session state. Frontend resolver
    fetches use `AbortController` or an equivalent cancellation mechanism; a
    previous-session request that resolves after cancellation discards its result
    and does not update state. Tests cover in-flight navigation separately from
    cache-hit navigation. A bounded LRU for non-active sessions may be added
    later only if separately tested, but it must not preserve stale in-flight
    work across active-session navigation. On SSE `resync`, the resolver cache
    for all sessions is flushed
	    because the client missed events and `resync` carries no affected-session list.
	    Re-resolution occurs on next access; tests must cover `resync` -> full cache
	    flush -> re-resolve separately from per-session `session_changed`
	    revalidation. First pass has at most two active server-key sources
	    (`stat` and `topology`), so per-source deduplication plus the one-key-per-source
		    cap bounds automatic resolver concurrency after `resync` to two requests.
		    Adding a third server-key source requires either a global concurrency cap or
		    a recorded reason why the existing bound still holds.
	    If `metric`, `group`, or a global filter changes while post-`resync`
	    re-resolution is in flight, the owner keeps at most two active resolver
	    requests and at most two queued replacement re-resolutions. Superseded queued
	    work is coalesced by latest `(session_id, source, key, metric, group,
	    filters)` tuple, and stale completed requests are discarded before cache
	    write. Tests cover `resync` followed by metric/group changes for both stat
	    and topology keys.
    On unmount of the Session Detail route, all pending resolver requests are
    cancelled and active-session cache entries are evicted so stale async results
    cannot update an unmounted route.
  - Initial async highlight resolution is non-blocking. The workbench renders the
    unhighlighted table/visualization state plus a compact highlight-loading
    indicator until the resolver returns; it does not blank the active view.
  - SOW-0113 owns the shared Go presenter route registration, response type,
    common validation/error envelope, and frontend resolver hook/cache for this
    endpoint. SOW-0111 supplies the `source=topology` resolver function and
    SOW-0112 supplies the `source=stat` resolver function. Backend registration
    contract: SOW-0113 defines a small resolver registry injected into presenter
    construction, equivalent to
    `type HighlightResolver func(ctx context.Context, sessionID string, key string) (HighlightResult, error)`
    and `type HighlightResolverRegistry map[string]HighlightResolver`. The
    registry is an explicit field on presenter construction options or a
    constructor argument, matching the existing `presenter.Options` dependency
    pattern; hidden package-level `init()` side effects are rejected. The
    registry maps source strings (`stat`, `topology`) to resolvers and owns
    common validation, caps, sanitized summaries, and error-envelope translation.
    SOW-0111 and SOW-0112 implement resolver functions for their sources and pass
    them through that registry in `cmd/ai-viewer-serve` or test constructors.
    Unknown or unregistered `source` values fail closed with the standard client
    error envelope before key parsing. `source` must be one of `stat` or
    `topology` before `key` is decoded or resolved.
  - Resolver route robustness is explicit for `GET
    /api/sessions/:id/highlights/:source/:key`: encoded separators such as
    `%2F` / `%5C`, NUL/control characters, empty segments after decoding, and
    malformed percent-encoding in `source` or `key` fail closed with the standard
    error envelope. Tests must pin the router behavior so the opaque key cannot
    route to the wrong handler or bypass source validation.
  - Resolver latency budget: resolving a key must stay within the same budget as
    the originating endpoint (`topology/workbench` for topology keys,
    `/sessions/:id/stats` for stat keys) on the shared fixture. Child SOWs must
    state whether they recompute from the sanitized predicate or reuse cached
    aggregation state.
	  - Resolver transport failure behavior: on timeout, network error, or 5xx, the
	    selection owner keeps the last resolved set if one exists, shows a compact
	    resolver-error state, deduplicates in-flight requests for the same
	    `(source,key)`, and retries with bounded exponential backoff. After the
	    consecutive failure cap, the highlight clears with the same visible state
	    used for stale keys. Planning defaults are base delay 500ms, maximum delay
	    8s, 3 consecutive failures before clear, and deterministic-testable jitter
	    of +/- 25% around each scheduled delay to avoid synchronized retry storms;
	    changing these numbers requires recording the reason and tests. After the
	    cap clears a failed server-key highlight, the next canonical URL write removes
	    the failed `hi` item so reloading the page does not immediately retry a known
	    failed key. The visible resolver-error state remains until the user clears it
	    or changes selection/view.
	  - Resolver revalidation under SSE is debounced. Multiple `session_changed`
	    events for the same active session coalesce into one re-resolution per
	    active key after a 1000ms first-pass debounce window; child views must not
	    issue independent resolver storms. Changing the debounce window requires a
	    recorded reason and tests for bursty ingest. For `hi=stat:*`, SOW-0112's
	    heatmap live-update policy overrides silent membership replacement: the
	    visible bucket becomes stale on `session_changed`, and resolver
	    revalidation may mark the key recognized/stale but must not replace the
	    highlighted membership until the user refreshes statistics or changes the
	    metric/group/filter context.
- no-selection live insertion is not the same as stale selection. When `sel`
  and `focus` are absent, SOW-0107 owns the scroll-anchor/new-work marker UX;
  this selection owner only exposes the current filters/highlights needed to
  count newly matching rows.
- Child-session navigation: links emitted by content, waterfall, and topology
  navigate to `/sessions/:childId`. On session-id change, the owner reparses the
  URL for the new session, clears session-local `sel`, `focus`, and any resolved
  highlight set from the previous session, evicts resolver cache entries for the
  previous session per the cache rule above, and preserves only valid
  non-session-local params such as `view`, `table`, `metric`, and `group` under
  the canonical normalization policy. Unsupported metric/group values for the
  new session normalize to defaults and clear any derived server-key highlights.
  If a future link explicitly preserves a supported `hi=stat:*` or
  `hi=topology:*`, the owner shows the compact highlight-loading state and
  re-resolves the key against the child session before child views consume it.
  Browser Back restores the prior URL and parent workbench state.
- Deep-link restoration flow is a contract, not incidental rendering order:
  parse and normalize the URL; validate session id and typed params; fetch the
  selected session data; render header/controls/table/visualization/content
  loading states independently; resolve server-key highlights asynchronously
  through the shared resolver; distribute resolved selection/highlight state to
  child views; show partial failures per area; then emit the canonical URL on
  the next user write. Tests must cover a deep link with `view`, `table`, `sel`,
  `focus`, and a server-key `hi` through loading, resolution, and final state.
- Turn metadata source contract:
  - The shared selection owner needs a stable turn index containing at least
    `turn.id`, `seq`, timing/status/error fields, and op/failure counts before
    it can route table, waterfall, and content state.
  - SOW-0107 owns measuring whether the existing `GET /api/sessions/:id`
    all-turns/all-ops detail response is acceptable on the shared fixture.
  - If that response misses the budget, this SOW consumes the lightweight
    turn-summary/selected-turn contract defined by SOW-0107/SOW-0109 rather
    than parsing all ops only to derive selection state.
- SSE/spec contract:
  - `sse-protocol.md` must be reviewed and updated if the workbench relies on
    new event semantics for stale selection, new-work markers, or matching-count
    signals.
  - If current `session_changed` plus REST re-fetch is sufficient, the SOW must
    record that explicitly in the spec delta so child views do not invent their
    own SSE meanings. If SOW-0107 measurement requires a new matching-count or
    new-work SSE event, SOW-0107 owns that `sse-protocol.md` delta and this SOW
    consumes it.
- Stable-id precondition:
  - `sel=turn:<id>`, `sel=op:<id>`, and legacy `op=<id>` deep links are durable
    only if turn ids and op ids are deterministic across reingest. The
    implementation plan must cite the writer/adapter id-derivation paths and add
    reingest stability tests before claiming turn/op deep-link durability.
    The plan must enumerate the adapter turn-id and op-id derivation files it
    relies on for ai-agent v2, ai-agent v3, claude-code, codex, and opencode,
    then re-ingest a fixture and assert id equality. If an adapter cannot provide
    stable turn ids, `sel=turn:` links degrade to a visible "turn no longer
    found" state; if it cannot provide stable op ids, focus links degrade to the
    selected turn plus visible "op no longer found" state. Never open a different
    turn/op silently.
- StepFilter derivation evidence: before implementation, the plan must cite the
  canonical op-kind definitions and representative stored `ops.kind`/`ops.name`
  values from every adapter for `user`, `assistant`, `tool`, `session`,
  `reasoning`, `system`, and `compaction`. Raw `system` ops are visible under
  `step=all` only unless a later SOW adds a dedicated system filter. If no
  current adapter emits `system` or `compaction`, the value may remain parse-only
  for forward compatibility, but it must be removed from the first-pass visible
  controls enum. A permanently empty visible pill without fixture evidence is
  rejected. If an adapter emits a raw kind outside the StepFilter enum, it must
  map to a documented fallback rather than silently disappearing from filters.
  First-pass fallback: unknown
  raw op kinds remain visible under `step=all` and are excluded from narrower
  step filters until a later SOW adds a named category. They must not disappear
  from the unfiltered table/content/waterfall. They contribute to visible
  `op_count`/row `count` totals and to `failure_count` when their status is in
  the shared terminal-failure family, but they do not contribute to
  `tool_call_count`, `subagent_call_count`, or `fan_out_count` unless a later
  spec classifies them as tool or subagent work.
- Keyboard mode-switching contract:
  - The shared owner exposes typed setters for `view` and `table` so the controls
    ribbon can support keyboard mode switching without each child view parsing URL
    params independently.
  - Mode selector groups use a single tab stop plus arrow-key navigation, or a
    native select with equivalent keyboard behavior. Enter/Space commits the
    focused option, updates canonical URL params, and keeps focus in the controls
    ribbon unless the user explicitly moves it. If focus was inside a visualization
    that is being replaced by a mode switch, focus moves to the new visualization's
    first selectable item; if the new visualization has no selectable item, focus
    returns to the relevant controls-ribbon group. Focus must not fall through to
    `body`.
  - Table mode switches preserve `sel=turn:<id>` and `focus=op:<id>` when that
    target can exist in the new table mode. Focus moves to the selected row when
    present, otherwise to the first focusable row/control in the new table area.
    Switching to `table=raw` never mutates selection; raw-row focus remains local.
- Keyboard scale contract: table and waterfall use roving tabindex or an
  equivalent single-tab-stop pattern for large row/bar sets, so the content pane
  remains reachable without tabbing through hundreds of turns.
- React ownership:
  - Implement a single Session Detail workbench selection owner as a React
    context provider plus hook backed by `useSearchParams`, returning parsed
    state and typed setters. Do not introduce a new external state library for
    this first-pass selection owner. Child views do not parse or mutate search
    params independently.
  - The implementation plan must explicitly remove or route current child-local
    selection/expansion state through this owner, including `TraceTab` selected
    op state, `ByTurnWaterfall` expanded turn state, and `TopologyTab` selected
    node state.
- Keyboard/focus:
  - Turn rows and waterfall bars are focusable; Enter/Space selects.
  - After selection, focus remains stable unless the user explicitly moves to
    the content pane. The content pane is reachable immediately after the active
    table/visualization control in tab order.
  - Escape is deterministic. When focus is inside an expanded JSON or payload
    control, Escape collapses that local control first. A second Escape, or
    Escape from table/visualization/content chrome with no local expandable
    control consuming it, clears `sel` and `focus` and returns the workbench to
    the no-selection state without changing `view`, `table`, filters, or
    server-key highlights.
  - Selection and stale-selection changes emit one polite live-region
    announcement with sanitized text and no raw prompt/payload content.
  - Topology keyboard behavior is completed by SOW-0111's a11y contract.

Implementation plan:

1. Update specs with the selection/URL contract before tests or code. This step
   creates the `rest-api.md` "Stress metrics and dimensions" section with the
   initial `metric`/`group` URL enum skeleton before SOW-0111 or SOW-0112 plan
   review relies on it.
2. Add `ui-turn-view.md` spec text for the canonical StepFilter enum and
   derivation rules, then add a test that frontend constants match that spec
   inventory. Add a targeted `stepKindFilter=llm -> step=assistant`
   compatibility test and a test that the canonical emitted param is `step`, not
   `kind`. This spec delta is a hard prerequisite for SOW-0109, SOW-0110, and
   SOW-0111 plan review where step filters affect table/waterfall/topology
   behavior.
2a. Update Compare-to-Session navigation emitters under
    `frontend/src/pages/Compare/` or the current Compare link component named by
    implementation-plan audit so normal links emit canonical
    `/sessions/:id?view=waterfall&table=turns` and append `sel=turn:<id>` only
    when a concrete turn is known. Add a component or route test proving normal
    Compare navigation does not emit legacy `tab=` / `op=`.
3. Add pure parse/serialize tests for URL state, including URL-encoded ids,
   encoded commas inside ids, legacy aliases, precedence, invalid values,
   `sel=op` canonicalization to `sel=turn` plus `focus=op`, and push/replace
   policy.
3a. Add Playwright URL-cap validation that varies direct-highlight URL length
    around the 4096-byte first-pass cap and confirms over-limit URLs repair to a
    visible invalid-highlight state. If Chromium/Playwright behavior shows a
    lower practical cap on the target viewport/browser, lower the SOW cap before
    implementation proceeds.
	4. Add component tests for state synchronization between table, waterfall, and
	   content pane.
5. Add tests for legacy `?op=`, bare `?tab=`, `?tab:viz=`, `?tab:bottom=`, and
   `?stepKindFilter=` deep links.
5a. Update existing legacy-URL assertions in
    `frontend/src/components/SessionRow/SessionRow.test.tsx` and
    `frontend/tests/viz-sse.spec.ts`: the former must assert the chosen
    post-migration parent-link href, and the latter must assert that
    `?tab=trace` lands on the canonical waterfall view after normalization.
		6. Add resolver tests for `hi=stat:<key>` and `hi=topology:<key>` covering
		   successful resolution, stale keys, unknown keys, capped/truncated result
		   sets, sanitized `summary`, transport failures, in-flight deduplication,
		   SSE-driven debounced re-resolution, stat/topology intersection including
		   empty overlap with per-source clear actions, cancellation on active-session
		   navigation, cancellation/discard on visualization `view` switch, retry-cap
		   URL cleanup, bounded post-`resync` queueing, and interaction with manual
		   filters. Tests must include an active `hi=stat:<key>`, then a `step`,
		   `status`, or `subagent` filter change, and assert the server-key
		   highlight/cache entry clears or rekeys visibly rather than reusing a stale
		   filtered result.
	6a. Add rapid navigation tests that switch through at least parent -> child ->
	    grandchild sessions while resolver/detail requests are in flight. The final
	    session state must win, stale async results must be ignored, and
	    previous-session selection/highlight/cache state must not leak.
7. Add tests proving the selection owner can consume the chosen turn metadata
   source without requiring every op to be mounted for initial table/waterfall
   state.
8. Add reingest-stability tests for `sel=op:<id>` / legacy `op=<id>`.
   The plan must name the exact fixture files used for each adapter
   (ai-agent v2, ai-agent v3, claude-code, codex, opencode) before claiming
   cross-adapter durability.
9. Implement a shared selection owner in the Session Detail workbench.
10. Wire child views to consume the shared selection owner.

Validation plan:

- Unit tests for parse/serialize/normalization.
- Component tests for table -> content and waterfall -> content sync.
	- Playwright tests for browser back/forward, legacy op deep link, stale
	  selection, deleted-session/unavailable state handoff, rapid child-session
	  navigation, and keyboard focus after selection and visualization mode
	  switching.
- A11y tests for selectable rows/bars/nodes where implemented.

Artifact impact plan:

- Specs: `ui-turn-view.md`, `ui-pages.md`, `rest-api.md`, and
  `sse-protocol.md` when live-update semantics require a protocol delta.
- SOW lifecycle: prerequisite child of SOW-0107.

Open-source reference evidence:

- Optional before implementation plan: inspect trace viewers with stable
  deep-link selection behavior if local patterns are insufficient.

Open decisions:

1. Selection cardinality:
   - A. Single selected object plus optional in-turn focus.
   - B. Multi-select from the first implementation.
   Recommendation: A, long-term-best. It keeps cross-view behavior predictable
   and avoids performance traps in the first redesign.
2. Legacy `?op=`:
   - A. Preserve as accepted alias and normalize internally.
   - B. Drop support and require new links only.
   Recommendation: A, surgical. Existing deep links and tests keep working while
   the implementation moves to a turn-first state model.
3. Stat bucket behavior:
   - A. Treat `stat-bucket` as a filter/highlight, not primary content
     selection.
   - B. Treat `stat-bucket` as primary content selection with a custom content
     panel.
   Recommendation: A for the first implementation. It keeps the content area
   turn-first while allowing statistics to highlight related work.

## Plan

1. Run external gap review with SOW-0107 through SOW-0112 after incorporation.
2. Resolve findings and finalize the selection/URL contract.
3. Draft implementation plan.

## Execution Log

### 2026-06-26

- Created after reviewer round 1 identified shared selection/URL state as a
  missing prerequisite across the Session Detail redesign SOWs.
- Incorporated external reviewer round-2 findings: legacy URL aliases,
  serialization, precedence, history policy, stale/live-update behavior,
  filter scope, keyboard model, and shared React ownership.
- Incorporated external reviewer round-3 findings: bare `?tab=` migration,
  `stepKindFilter` inventory, unknown selection fallback, stable focus-id
  restriction, filter/highlight precedence, and highlight-set cap.
- Incorporated external reviewer round-4 findings: `stat-bucket` and
  `topology-node` are not first-pass primary `sel` types, `stepKindFilter` maps
  to `kind`, large highlight sets use server-issued keys, filter/history policy
  is explicit, and roving tabindex is required for large row/bar sets.
- Incorporated external reviewer round-5 findings: bare `tab` support is an
  intentional repair for currently ignored legacy links, `op` always derives an
  owning turn, `kind` is a step-filter enum rather than raw `ops.kind`, `headend`
  is removed from first-pass group URL values, and local selection state
  refactors are explicitly owned.
- Incorporated external reviewer round-6 findings: first-pass `view` enum is
  limited to `waterfall`, `topology`, and `stats`; legacy Timeline behavior is
  conditional on SOW-0107; the `kind` enum is assigned to `ui-turn-view.md`;
  payload URL focus stays blocked unless stability is proven; and server-key
  highlight resolution consumption is explicit.
- Incorporated external reviewer round-7 findings: metric enum includes
  `ctx_pct`, shared resolver endpoint/schema is pinned, stale/unknown keys use
  `200 OK` with status, no-selection live insertion is separated from stale
  selection, and StepFilter derivation rules are specified.
- Incorporated external reviewer round-8 findings: resolver response caps,
  staleness semantics, and sanitized summary shape are explicit; SOW-0107/SOW-
  0109 own the turn metadata/API payload decision consumed by the selection
  owner; and `sse-protocol.md` ownership is required when live-update behavior
  relies on new semantics.
- Incorporated external reviewer round-9 findings: SOW-0113 owns the shared
  highlight resolver handler and frontend resolver cache, `hi` parsing is
  disambiguated,
  resolver latency/transport failure/deduplication/SSE-debounce behavior is
  explicit, op-id stability must be proven before durable op links are claimed,
  and duplicate round-6/round-7 execution-log entries were removed.
- Incorporated external reviewer round-10 findings: canonical URL emission is
  explicit, `stepKindFilter` is no longer conflated with TraceTab's local raw
  `llm` dropdown, existing `SessionRow` and `viz-sse` legacy URL tests are
  owned here, aggregate tool nodes use topology highlight keys when they group
  multiple ops, `hi` over-limit and mixed direct/server-key parsing behavior is
  defined, child-session navigation clears session-local state on session-id
  change, stale notices have one shared component contract, and op-id stability
  planning must enumerate adapter derivation paths.
- Incorporated external reviewer round-11 findings: the `group=` URL enum now
  includes `client` and mirrors the shared REST/API dimensions glossary, direct
  `hi=` id caps are total across sources, server-key highlights are capped per
  source, successful resolver responses cache until `session_changed`, and
  StepFilter derivation must be proven against canonical op kinds and
  representative adapter-stored op values.
- Incorporated external reviewer round-12 findings: legacy `overview`/`turns`
  tab aliases normalize to the default workbench state, reverse synchronization
  from `sel=turn` into topology/waterfall is explicit, resolver caches flush on
  SSE `resync`, the shared backend resolver registry/interface shape is pinned,
  and durable deep links require turn-id as well as op-id reingest stability.
- Incorporated external reviewer round-13 finding: changing `metric=` or
  `group=` now clears metric/group-bound server-key highlights and evicts their
  cached resolver entries, because topology/stat keys encode the active
  metric/group at issuance.
- Round 14 completed on 2026-06-27 with accepted findings clarifying canonical
  `step=` naming, `stepKindFilter=llm` compatibility, table nested op-click URL
  emission, malformed URL handling, URL length cap, resolver response
  `source_table_counts`, in-flight/cache eviction semantics, concrete backend
  resolver registry injection, definitive child-session state clearing, React
  context ownership, StepFilter `system`/`compaction` evidence, and
  cross-adapter fixture naming for deep-link stability tests.
- Round 15 completed on 2026-06-27 with accepted findings clarifying
  `step` precedence over legacy `stepKindFilter`, REST stress-glossary skeleton
  creation, combined server-key resolution caps, mandatory active-session cache
  eviction, deep-link restoration flow, StepFilter evidence for `system` and
  `compaction`, mode-switching keyboard behavior, live-region announcements, and
  the StepFilter spec delta as a hard child-plan prerequisite.
- Round 16 completed on 2026-06-27 with reviewer findings scoped to SOW-0107,
  SOW-0108, and SOW-0112. The selection contract is unchanged except
  child-session navigation now depends on the SOW-0107/SOW-0108 server-provided
  ancestor chain.
- Round 17 completed on 2026-06-27 with accepted findings clarifying
  `view=stats&table=` normalization, metric/group child-navigation preservation,
  the initial REST stress-glossary skeleton, initial highlight loading behavior,
  resolver source validation, unregistered-source behavior, and encoded-separator
  fail-closed tests for the highlight route.
- Round 18 completed on 2026-06-27 with accepted findings clarifying cold
  legacy `?op=` resolution under the lightweight contract, replace-based initial
  URL normalization, stat/topology server-key intersection semantics, selected
  content re-fetch when rows change but ids survive, and child-session
  navigation behavior for surviving supported highlights.
- Round 19 completed on 2026-06-27 with accepted findings clarifying
  `sel=op` parse-only canonicalization, events/logs/raw table-mode selection
  behavior, unknown raw op-kind fallback for StepFilter, encoded-comma parsing,
  resolver cancellation on navigation, and SSE delta ownership with SOW-0107.
- Round 20 completed on 2026-06-27 with accepted findings clarifying legacy
  search-param audit requirements, `ops/:opId` consumption ownership, bare
  `focus` repair behavior, first-pass `status` and `subagent` filters, unknown
  op fallback, direct/server-key highlight union semantics, route-unmount
  resolver cancellation, and concrete resolver retry defaults.
- Round 21 completed on 2026-06-27 with accepted findings clarifying op-to-turn
  resolver transport failures, `sel`/`focus` mismatch repair, minimum REST stress
  glossary skeleton fields, and shared global filter application semantics across
  table, waterfall, topology, and content.
- Round 22 completed on 2026-06-27 with accepted findings clarifying
  `stepKindFilter=llm` evidence requirements, global filter intersection,
  first-pass URL/id/key length caps, resolver retry jitter, deleted-session state
  handoff, focus behavior on visualization mode switches, and rapid
  parent/child/grandchild navigation coverage.
- Round 23 completed on 2026-06-27 with accepted findings clarifying highlight
  truncation semantics at resolver caps, bounded resolver concurrency after SSE
  `resync`, a concrete 1000ms SSE revalidation debounce window, and table-mode
  focus preservation.
- Round 24 completed on 2026-06-27 with accepted findings clarifying legacy
  op/focus repair under the all-detail data path, view-dependent `metric`
  defaults including topology-only `multi`, shared status-filter normalization,
  resolver cancellation/discard on view switches, empty stat/topology
  intersection clear actions, bounded queued re-resolution after `resync`,
  retry-cap URL cleanup, canonical comma-separated `hi` emission, and Playwright
  URL-length cap validation.
- Round 25 completed on 2026-06-27 with accepted findings clarifying
  filter-sensitive server-key highlight invalidation/cache keys, canonical
  Compare -> Session Detail URL emission, and resolver tests for changing
  `step`/`status`/`subagent` while a stat/topology key is active.
- Round 26 completed on 2026-06-27 with accepted findings clarifying that
  waterfall does not consume global metric/group params in the first pass, and
  aligning `status=failed` normalization with the shared terminal-failure family
  used by turn summaries, topology, and statistics.
- Round 27 completed on 2026-06-27 with accepted findings hardening
  `stepKindFilter=llm` repair, unknown raw op-kind count semantics, direct versus
  server-key highlight combination order, stat-key stale behavior under
  `session_changed`, and explicit Compare canonical URL implementation
  ownership.
- Round 28 completed on 2026-06-27 with accepted findings pinning selected-turn
  re-click as a no-op, Escape key behavior, and parse-only handling for
  forward-compatible StepFilter values that have no fixture evidence.
- Round 29 completed on 2026-06-27 with no selection-state P0/P1/P2 changes; P3
  wording clarified `metric=multi` URL stability for topology and renamed the
  `subagent` filter from boolean to tri-state enum.
- Round 31 completed on 2026-06-27 with no selection-state P0/P1/P2 changes.

## Validation

Pending.

## Outcome

Pending.

## Followup

None yet.

## Regression Log

None yet.
