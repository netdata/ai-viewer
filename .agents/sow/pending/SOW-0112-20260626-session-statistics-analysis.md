# SOW-0112 - Session Statistics And Aggregate Analysis

## Status

Status: open

Sub-state: fit-for-purpose gap analysis drafted; external gap review rounds 1
through 28 completed and findings incorporated; gap-review rerun pending after
round-28 changes.

## Requirements

### Purpose

Make Statistics explain where cost, time, tokens, failures, and volume went in
the selected session tree. This is the high-level diagnostic view, not another
raw event list.

### User Request

Statistics should provide counts, sums, averages, and heatmaps grouped by model,
tool, agent, client, and related dimensions. It should explain where cost and
time went.

### Assistant Understanding

Facts:

- Current Session Detail "Statistics" is a placeholder/coming-soon surface.
- The database contains sessions, turns, ops, models/providers/tools, tokens,
  costs, durations, failures, and source/client-ish fields.
- Existing global Stats page has some aggregate patterns but is not necessarily
  session-tree scoped.
- Current `/api/stats` is global/filter scoped. There is no verified
  `/api/sessions/:id/stats` contract for one session subtree.
- `client` is not currently a first-class canonical/store dimension; it must be
  defined before it appears as a grouping.

Inferences:

- Session statistics should aggregate over the full recursive session tree by
  default when viewing a root session, and over the current session's subtree
  when viewing a child session, with an explicit scope toggle if root-tree
  comparison is needed.
- Heatmaps need a clear axis: default should be turn sequence on X, selected
  grouping dimension on Y, and selected metric as color.
- The view should support both "where did money go?" and "where did latency/fail
  pressure go?".

Unknowns / required decisions:

- Whether session-scoped stats should be a new endpoint or an extension of the
  existing stats endpoints.
- Exact "client" dimension needs definition across adapters/sources; candidate
  names may be `source_format`, `source_id`, ai-agent v3 `headendId`, or a new
  display client label from SOW-0108.

### Acceptance Criteria

- Statistics view is implemented, not a placeholder.
- Aggregates cover the current session subtree. For a root session that is the
  full recursive tree; for a child session it is that child session and its
  descendants, not sibling subtrees.
- It provides at least:
  - cost by model/provider/agent/tool/client,
  - duration by model/provider/agent/tool/client,
  - token totals/cache by model/provider/agent,
  - context pressure (`ctx_pct`) where model/token capacity is known,
  - failure counts/rates by tool/agent/model,
  - turn-level heatmap for cost/time/failures,
  - counts and averages for turns, ops, tools, subagents.
- The first implementation must either expose a standalone `client` dimension
  from SOW-0108 `display_client_label` / `display_client_label_source`, or this
  SOW must explicitly cut that user requirement with a follow-up SOW before
  implementation planning. The preferred path is to consume SOW-0108's
  normalized client label. A fallback `json_extract(extras_json, '$.headendId')`
  path is allowed only with explicit performance/index evidence and sanitizer
  tests. Supported dimensions are explicit: `model`, `provider`, `agent`,
  `tool`, `client`, `source_format`, `status`, and `error_class`.
- Selecting a statistic can filter/highlight the table, waterfall, topology, or
  content area where practical, using SOW-0113 shared selection/highlight state.
- Results match database/presenter tests for representative fixtures.
- Session-scoped stats API responds within 500ms for a 10k-op / 1000-turn
  seeded session tree on the reference workstation, or the implementation must
  add caching/materialization before the SOW can close.
- Large-result degradation is explicit: turn heatmaps cap rendered X buckets at
  200, grouping dimensions keep top 10 values plus `other`, and the endpoint
  returns aggregation/truncation metadata when compaction occurs.

## Analysis

Sources to check during implementation:

- `internal/presenter` stats/session endpoints
- `internal/store` rollups/catalogs
- `frontend/src/pages/Stats`
- `frontend/src/pages/SessionDetail`
- API types for stats charts

Current state:

- The current session Statistics tab is not useful for the user's stated
  purpose.
- There is no explicit session-tree aggregate contract for this view.
- Existing global stats patterns can be reused, but the endpoint and query scope
  must be different for a single session subtree.

External gap review round 1 findings incorporated:

- All reviewers voted `NEEDS WORK`.
- Reviewers found no session-tree-scoped statistics endpoint today. This SOW now
  requires a concrete API contract and query plan.
- Reviewers found `client` undefined as a data dimension. This SOW now blocks
  client grouping until it is mapped to verified source/canonical data.
- Reviewers found the heatmap vague. This SOW now requires axes, metric, scale,
  and selection behavior to be specified before implementation.
- Reviewers found statistic selection/highlighting duplicated the shared
  selection problem. SOW-0113 now owns the shared state contract.

External gap review round 2 findings incorporated:

- Reviewers found the query strategy was still deferred. First implementation
  now recommends live-folding subtree totals/detail rows from sessions/turns/ops
  with a 500ms performance gate; existing session aggregate columns are own-row
  values and must not be treated as recursive subtree totals.
- Reviewers found the existing global `Heatmap` component is day/hour-specific
  and not fit for turn-sequence x dimension heatmaps. This SOW now treats the
  session heatmap as a new component unless implementation planning proves
  reuse.
- Reviewers found `client` was still used before being defined. It is removed
  from first-pass supported dimensions unless SOW-0108 defines it.
- Reviewers found `stat-bucket` selection/filter semantics were undefined. This
  SOW now distinguishes filtering/highlighting from content selection.

External gap review round 3 findings incorporated:

- Reviewers found dimension queryability should be recorded now: `headendId` is
  adapter extras unless normalized, while `source_format` requires a join to
  `sources`.
- Reviewers found the 500ms budget needed a degradation/cap contract for very
  large sessions.
- Reviewers found error-class attribution should be explicit.

Risks:

- Aggregates can be misleading if scoped inconsistently between root session,
  child sessions, and selected turn.
- Heatmaps can become decorative if not tied to actionable selection/highlight.
- Backend query cost may be high on very large session trees.

## Pre-Implementation Gate

Status: needs-review

Problem / root-cause model:

- Statistics currently lacks a session-tree analytical contract. It must become
  the high-level "where did cost/time/stress go?" surface.
- Global stats cannot simply be dropped into Session Detail. The view needs
  subtree-scoped aggregates that match the same turns, tools, agents, and
  subagents shown by table/waterfall/topology.

Evidence reviewed:

- User feedback.
- Existing placeholder in `UnifiedView` for per-session statistics.
- Prior UI/DB contract work showing available cost/token/duration fields.

Affected contracts and surfaces:

- Session Detail stats view, presenter/API stats endpoints, store queries,
  frontend chart components, tests.

Existing patterns to reuse:

- Existing global Stats chart components.
- Existing store/presenter aggregate patterns.
- Existing chart components where fit.
- Existing `Heatmap` is for day-of-week x hour-of-day global failure views; the
  session turn heatmap is a new component unless proven reusable during
  implementation planning.

Session statistics contract required before implementation:

- API contract:
  - Preferred direction: `GET /api/sessions/:id/stats`, scoped to the current
    session subtree by default.
  - New endpoint HTTP behavior follows the existing REST contract: `HEAD`
    returns the same status and JSON content type with an empty body,
    non-GET/HEAD methods return `405`, unknown session ids return `404`, and
    invalid/control-character path parameters fail closed with the existing
    error envelope.
  - Response includes totals, breakdowns, heatmap data, and selection keys that
    can map back to table/waterfall/topology.
  - First-pass response shape:
    `{ scope, totals, breakdowns, heatmap, limits, generated_at }`.
    `generated_at` is the server wall-clock timestamp captured at response
    serialization start; it is display/cache metadata only and does not replace
    source snapshot timestamps.
    `totals` contains subtree totals for cost, duration, tokens, failures,
    fan-out, turns, ops, tools, and subagents. `breakdowns[]` is a flat list of
    grouped rows:
    `{ dimension, dimension_value, display_value, source_table, count,
    cost_usd, duration_us, tokens_in, tokens_out, tokens_cache_read,
    tokens_cache_write, failure_count, failure_rate, fan_out_count, ctx_pct?,
    ctx_pct_scope?, selection_key }`. When `ctx_pct` is present,
    `ctx_pct_scope` is required and first-pass value is `group_peak`.
    First-pass averages are derived from these sums using the row's own `count`
    denominator: average cost/duration/token values for a breakdown row are
    `sum / count`, where `count` is the matching source rows for the row's
    `source_table` and active filter tuple. The UI must label the denominator,
    for example "avg per op" or "avg per turn", instead of presenting an
    unlabeled average.
    `display_value` is sanitized through SOW-0108 and carries SOW-0108 display
    labels for `agent`/`client` dimensions where available.
    `failure_count` uses the same shared terminal-failure family as SOW-0107
    turn summaries and SOW-0113 `status=failed` filtering: `failed`,
    `abandoned`, `interrupted`, and `aborted`. If existing persisted rollups
    still use a narrower historical predicate, this SOW must consume the
    coordinated rollup/spec/test update before claiming statistics failure
    metrics are correct.
  - First implementation direction: compute subtree totals and heatmap/detail
    rows by live-folding the current session subtree from `sessions`, `turns`,
    and `ops`. Existing `sessions.*` aggregate columns such as `cost_usd`,
    tokens, `turn_count`, `op_count`, and `failure_count` are own-session values;
    they are valid for an own-session row but are not recursive subtree totals.
    Add caching/materialization only if the 500ms performance gate fails.
  - This endpoint does not inherit global `/api/stats` filter semantics. Its
    primary scope is the current session subtree; view filters are explicit URL
    state owned by SOW-0113.
	- Scope:
	  - For a root session, default scope is the full recursive tree.
	  - For a child session, default scope is that child session's subtree, not
	    unrelated siblings.
	  - Statistics intentionally uses the full recursive subtree for totals even
	    though SOW-0111 topology collapses visual depth after level 4. Stats answers
	    "where did all cost/time go?"; topology caps depth for readability. If the
	    full-subtree live fold fails the 500ms budget on deeper trees, this SOW must
	    add materialization or an explicit depth/aggregation degradation path before
	    closing.
	  - Optional root-tree comparison can be a later toggle if needed.
- Heatmap:
  - X axis: turn sequence when turn count is `<= 200`; compacted turn buckets
    when turn count is `> 200`, with a maximum of 200 X buckets. Compacted
    buckets use contiguous turn-sequence ranges with approximately equal turn
    counts, not wall-clock duration, so bucket identity remains stable when turn
    durations vary.
    This is a deliberate visual-balance tradeoff: equal-turn-count buckets make
    dense sessions readable, but bucket boundaries and `selection_key` values can
    change after explicit refresh when new turns are inserted before or inside
    existing ranges. Live `session_changed` does not silently re-bucket; it shows
    the shared "new data available" marker until the user refreshes. The heatmap
    legend must state that compacted buckets are grouped by turn count, not
    wall-clock time, and that refreshed live sessions may invalidate old stat
    highlight keys visibly. Caption baseline: "Compacted buckets group turns by
    equal count, not by wall-clock time. Refreshed live sessions may invalidate
    highlighted stat buckets; refresh statistics to issue new keys." The caption
    is visible near the visualization and exposed through accessible text via
    `aria-describedby`, a table-alternative caption, or an equivalent tested
    relationship. A fixed turn-sequence-width bucket mode is deferred unless
    operator feedback shows key stability is more important than visual balance.
  - Y axis: selected grouping dimension (`model`, `provider`, `tool`, `agent`,
    `client`, `source_format`, `status`, or `error_class`).
  - Color: selected metric (`cost`, `duration`, `tokens`, `failures`, `fan_out`,
    or `ctx_pct` when model/token capacity is known).
	    `ctx_pct` cells with no known model/token capacity are `null`/unavailable
	    cells, not zero. They render with a neutral unavailable style, are excluded
	    from color-scale domain calculations, and expose a coverage count so users
	    can see how many rows contributed to context pressure. Sparse availability
	    does not silently switch the metric; the UI shows an unavailable/partial-data
	    state and lets the user choose a different metric. If `ctx_max` is `0`,
	    missing, or invalid, `ctx_pct` is `null` / unavailable, never `0`,
	    `Infinity`, or `NaN`. If every visible `ctx_pct` cell is unavailable, the
	    heatmap renders a neutral "no context pressure data" state and does not
	    build a color-scale domain from null cells.
	  - Large dimensions use top-N plus `other` aggregation. Empty/null dimension
	    values are never silently folded into `other`: empty `error_class` renders
	    as a distinct `(none)`/success bucket, while empty `agent` or `client`
	    display labels during migration/backfill render as `(unlabeled)` with
	    transition metadata.
  - If the turn count is not evenly divisible by the compacted bucket count, the
    earliest buckets receive one extra turn from left to right so bucket
    identities and `selection_key` inputs are deterministic.
	  - A session with zero turns renders summary totals plus an explicit empty
	    "no turns" heatmap/table state. It must not render a blank visualization
	    pane or a misleading one-bucket heatmap.
	    The API remains available for `view=stats`: it returns zero-valued totals,
	    `breakdowns: []`, `heatmap: { cells: [], empty_reason: "no_turns" }`,
	    normal `limits`, `generated_at`, and scope metadata. Controls remain
	    visible but disabled or retain defaults with an explicit no-turns state;
	    the table/raw area renders the stats-owned empty table state, not the
	    normal `table=turns` mode.
- Workbench zone behavior: when `view=stats`, the heatmap and primary summary
  visuals occupy the visualization area, while grouped metric tables occupy the
  table/raw area. Normal table modes are disabled or visibly inactive while
  stats owns the table area unless SOW-0107 records a different layout decision.
  SOW-0113 owns URL normalization: preserved `table=` values are ignored for
  rendering while stats is active and restored only after leaving stats view.
  The content area keeps the standard SOW-0107/SOW-0113 selected-turn contract:
  stat buckets are highlights/filters, not primary content selections, so the
  content pane shows the current selected turn/op or the shared no-selection
  state.
- Heatmap accessibility contract: each rendered cell exposes its metric,
  dimension value, turn range, and count as text via a table alternative or
  accessible cell label; color is never the only signal. The sortable table
  alternative is mandatory and is the primary screen-reader/keyboard path for
  all heatmap renderings. This heatmap table alternative is distinct from the
  grouped metric tables that occupy the workbench table/raw area in stats view:
	  it lives in or adjacent to the visualization area as a collapsible/visually
	  integrated tabular representation of the same heatmap cells, with equivalent
	  selection keys and keyboard selection behavior. The visual heatmap may continue
	  as DOM/SVG, Canvas, or windowed rendering per the 2200-cell rendering budget,
	  but the table alternative remains available with equivalent selection keys.
	  The visual heatmap uses one tab stop plus arrow-key navigation for cells;
	  Enter/Space activates `hi=stat:<selection_key>`. The heatmap table alternative
	  sorts by active metric descending by default and exposes columns for turn
	  range, dimension value, metric value, count, and selection action. It uses the
	  same `selection_key` values as visual cells. When the X bucket count exceeds
	  20, the accessible layout avoids a 200-column screen-reader trap by using
	  transposed rows, grouped row sections, or an equivalent compact structure;
	  keyboard navigation supports Home/End for first/last bucket, Page Up/Down by
	  visible section, and a jump-to-bucket control for direct navigation.
- Dimension queryability:
  - `source_format` is available through `sessions.source_id -> sources.format`;
  - `model`, `provider`, `status`, `error_class`, cost, tokens, duration, and
    operation status are first-class `ops` columns; `agent` groups by SOW-0108
    `sessions.display_agent_label` when populated and falls back to
    `sessions.agent_name` through the op/session join; `tool` can be grouped
    from `ops.tool_namespace` and `ops.name`;
  - `client` groups by SOW-0108 `sessions.display_client_label` when populated
    and falls back only to verified `source_format`/`source_id` or a tested
    `json_extract(extras_json, '$.headendId')` path; the implementation must not
    claim client grouping from raw adapter-specific JSON without EXPLAIN and
    sanitizer evidence;
    The implementation plan must prove whether `client` is meaningfully distinct
    from `source_format` for ai-agent v3 using SOW-0108 fixture evidence. If the
    first-pass data cannot distinguish them, the API still returns a `client`
    dimension only with explicit limitation metadata and the SOW records a
    follow-up; it must not present `client=aiagent_v3` as if it were a verified
    client/headend grouping.
  - `headend` remains an implementation source for `client`, not a separate
    first-pass dimension unless SOW-0108 or this SOW explicitly exposes it;
  - `kind` derived from ai-agent v3 `headendId` is already a session column and
    may be used where the desired grouping is normalized session kind. `headend`
    remains distinct from `kind` unless SOW-0108 defines a normalized headend
    column or this endpoint accepts a tested JSON extraction path.
	  - `agent` grouping and its EXPLAIN evidence depend on SOW-0108 migration
	    `0013_display_agent_label.sql` and its backfill/reingest path. `client`
	    grouping and its EXPLAIN evidence depend on SOW-0108 migration
	    `0014_display_client_label.sql` unless the plan proves an indexed
	    source-format fallback. SOW-0112 may draft independent API shapes earlier,
	    but it cannot finalize backend acceptance for those dimensions before the
	    dependent SOW-0108 migrations land or are explicitly scoped out.
	    If SOW-0108 is delayed or rejected, this SOW's implementation plan must
	    choose a degraded branch explicitly: `agent` grouping may use
	    `sessions.agent_name` with a visible caveat that ai-agent v3 can still show
	    raw `parent`, and `client` grouping may fall back only to `sources.format`
	    or another indexed, tested source. It must not quietly claim normalized
	    agent/client grouping without the display-label storage.
- Heatmap row shape required from the API:
  `(turn_bucket, turn_range_start, turn_range_end, dimension, dimension_value,
  metric, value, count, selection_key, source_table)`, where `turn_bucket` is a
  turn seq for small sessions and an inclusive turn range for compacted sessions.
  `source_table` is `ops`, `turns`, or `mixed` and is required for ambiguous
  dimensions such as `error_class`. The `selection_key` is an opaque
  server-issued stable key for the bucket/filter expression; it must not encode
  raw prompt text, payload text, private paths, or unsanitized dimension values.
  First-pass stability mechanism: `selection_key` is a deterministic hash of the
  sanitized bucket predicate (`session_id`, scope, turn bucket/range, dimension
  name, sanitized dimension value, metric, source_table, sanitizer version, and
  canonical global filter tuple for `step`, `status`, and `subagent`), so the
  same bucket recomputes to the same key across refreshes and SSE updates only
  when the filter context is the same. Changing any global filter makes old
  stat keys issuance-context stale and SOW-0113 clears/re-resolves them through
  the shared filter-change behavior.
- `selection_key` hash width and collision handling must be specified before
  code. First-pass target: at least 128 bits of hex/base64url-safe hash output.
  After all keys for one response are computed, detect duplicates with a map. If
  a collision is detected inside one response, append deterministic
  response-local sequence suffixes such as `-1`, `-2` to the colliding keys and
  expose `collision_resolved=true` in a test fixture that forces the collision
  through a stubbed hash function. If a single response would require more than
  64 collision suffixes, the endpoint fails with the standard error envelope
  instead of emitting ambiguous selection keys. SOW-0107 step 5c owns the
  shared-vs-separate hash-helper decision for topology/statistics keys; the
  inline contract here is the fallback definition if planning proves separate
  implementations are safer.
- Dimension values are sanitized before hashing and display through the shared
  SOW-0108 display sanitizer. For `error_class`, remove paths, stack snippets,
  and high-entropy tokens before producing the dimension value.
  First-pass `error_class` sanitization happens at query/response construction
  time through one shared sanitizer helper used for both display values and
  `selection_key` predicates. The helper/version becomes part of the key input.
  If query-time sanitization misses the 500ms budget or fragments grouping too
  heavily, the implementation must add a stored sanitized display/error-class
  field or materialized rollup through a new migration/backfill before claiming
  the SOW complete; ad hoc frontend-only normalization is rejected. Fragmentation
  is too heavy if the shared fixture produces more than 50 distinct sanitized
  `error_class` values for one session after path/token/stack removal, unless
  implementation-plan evidence proves those values are genuinely different
  operator-actionable errors.
- `error_class` first pass groups sanitized raw stored values. Fuzzy semantic
  deduplication of similar error classes is deferred unless implementation
  planning explicitly owns it; if raw buckets fragment the UI materially, open a
  follow-up SOW instead of adding ad hoc normalization. `error_class`
  attribution is op-level for op failures and turn-level only for turn
  finalization/status rows; API rows must state `source_table` so the UI can
  explain which source produced the error bucket.
	- `status` first pass groups `ops.status` only. Turn-level status is a distinct
	  dimension and must not be silently mixed into `status` rows unless the API adds
	  a named turn-status dimension with its own `source_table` behavior.
	  The global `status=` filter may normalize
	  `failed|abandoned|interrupted|aborted` to `failed`; the first-pass `status`
	  statistics dimension remains raw op status and must label that distinction
	  so users do not confuse dimension values with normalized filter values.
	- Unknown raw op kinds that remain visible under SOW-0113 `step=all` contribute
	  to op-sourced `count`, `op_count`, and `failure_count` when applicable. They
	  do not contribute to `tool_call_count`, `subagent_call_count`, or
	  `fan_out_count` unless a later spec classifies them as tool or subagent work.
	  If a future `step`/kind dimension is added, unknown kinds display as
	  `(unclassified)` rather than being folded into another category.
	- Global workbench filters from SOW-0113 are reflected in statistics results
	  rather than ignored silently. First pass applies `step=` to op-derived metric
	  rows by the shared StepFilter mapping, applies `status=` to op status for
	  op-derived rows, and applies `subagent=` to turn/session membership using the
	  same direct-child/subagent predicate as table and topology. If a total cannot
	  be filtered without mixing incompatible scopes, the response returns both the
	  unfiltered total and a `filter_scope` label explaining which rows were
	  filtered.
	- `stat-bucket` clicks are filters/highlights by default, not primary content
	  selection. Example: clicking `model=<value>` highlights matching turns/ops in
  table/waterfall/topology and sets SOW-0113 filter/highlight params; it does
  not replace the selected turn unless the bucket maps to exactly one turn.
- Large stat buckets use the SOW-0113 server-key highlight path instead of
  listing every matching id in `hi=`. This SOW owns producing the
  `selection_key`; SOW-0113 owns URL serialization and parse/clear behavior.
- Stat server-key resolution is owned here for stat keys. A
  `hi=stat:<selection_key>` value is not valid until the stats backend can
  resolve the key to a sanitized highlight result with bounded `turn_ids`,
  `op_ids`, optional turn ranges, match counts, source-table counts, and
  `truncated/stale` status for SOW-0113 to consume through
  `GET /api/sessions/:id/highlights/stat/:key`. Stale or unknown keys clear with
  the visible stale-highlight state from SOW-0113. The resolver response schema
  is defined once by SOW-0113 and the REST/API spec delta.
  This SOW supplies stat-key staleness semantics to SOW-0113: a key is `stale`
  when the stored/recomputed sanitized predicate is still recognized but its
  membership differs from the membership at issuance time because ingest/SSE
  changed the session subtree; a key is `unknown` when the key cannot be mapped
  to any known sanitized predicate for the session.
- Resolver performance budget: resolving `hi=stat:<selection_key>` must stay
  within the same 500ms session-stats API budget on the shared fixture. The
  implementation plan must state whether the resolver recomputes from the
  sanitized predicate or reuses cached aggregation state, and must test the
  chosen path.
	- Concurrent identical stats requests are a planning decision. The implementation
	  plan must either add single-flight coalescing for identical
	  `(session_id, scope, metric, group)` requests or explicitly accept duplicate
	  concurrent queries with measured justification on the shared fixture.
	- Grouped metric tables are sortable by the active metric and `count` in the
	  first pass. A text filter is optional for small groups but required if a
	  grouped table can exceed the visible table-area height in the shared fixture.
	  Pagination is deferred unless the 500ms API or 100ms interaction budgets fail.
- Frontend heatmap render budget: the 200 X-bucket x top-10-plus-`other`
  dimension cap yields up to 2200 visible cells. The first implementation must
  render that capped shape without layout shift or blocking selection. SVG/DOM is
  allowed up to the capped 2200-cell shape only if measurements pass; otherwise
  Canvas/windowing is required. Above 2200 visible cells, Canvas/windowing is the
  default unless implementation measurements justify a higher SVG/DOM limit.
  The earlier 500-cell threshold was superseded by this 2200-cell capped-shape
  analysis and is no longer the first-pass contract.
	  Measurement pass criteria: heatmap hover/click selection response stays under
	  100ms on the shared fixture, filter/group changes do not create visible layout
	  shift, and pointer/keyboard navigation remains visibly responsive without long
	  main-thread stalls. Dimmest/lowest-value cells still meet WCAG 2.2 3:1
	  non-text contrast for their boundary/indicator against the surrounding cell
	  background; if fill color cannot meet it, add a stroke/border/marker that
	  does. The implementation plan records the exact measurement method and
	  switches to Canvas/windowing if DOM/SVG misses it.
	- Default controls: first-pass default metric is `cost` and default group is
	  `model`. A different default is allowed only with recorded user-value and
	  performance evidence before implementation plan review.
- Subtree live-fold SQL must avoid the known planner-hostile pattern of a
  recursive CTE driving a large join. Preferred direction: compute the subtree id
  set first, then query with `WHERE session_id IN (SELECT id FROM subtree)` or a
  Go-side id set, and prove the query shape in tests before frontend work.
- The implementation plan must audit indexes in the current migrations for
  `ops.session_id`, `ops.turn_id`, `ops.tool_namespace`, `ops.name`,
  `ops.model`, `ops.provider`, `ops.status`, `ops.error_class`, and
  `sessions.source_id`, `sessions.display_agent_label`, and
  `sessions.display_client_label`. Missing indexes needed for the selected
  query plan must be added before claiming the 500ms budget. For display-label
  groupings, an index may be unnecessary when the query first materializes a
  bounded subtree session-id set; the implementation plan must prove that with
  SQL/EXPLAIN evidence or consume supporting indexes from SOW-0108 migrations.
  The current migration baseline must be recorded accurately: `idx_ops_tool`,
  `idx_ops_model`,
  `idx_ops_provider`, and `idx_ops_compaction` are partial indexes;
  `idx_ops_kind_name` is a plain `(kind, name)` index; there is no index on
  `ops.error_class`. EXPLAIN output on the shared 1000-turn / 10k-op fixture is
  required evidence for each selected grouped query.
  Current model/provider indexes are partial for LLM rows in the initial schema;
  if the selected query groups model/provider across non-LLM rows, the plan must
  add or justify a non-partial index before claiming the budget. If `error_class`
  is a first-pass dimension, the plan must either add a supporting index or
  prove the scan stays under budget. The default implementation-plan branch is a
  migration `0015_ops_error_class_index.sql` owned by this SOW with a supporting
  `idx_ops_error_class` on `ops(error_class)`. A partial index is allowed only
  if the chosen aggregate SQL includes a matching `WHERE error_class IS NOT
  NULL AND error_class <> ''` predicate and EXPLAIN on the shared fixture proves
  the planner uses it. Concrete first-pass SQL for the default branch is
  `CREATE INDEX idx_ops_error_class ON ops(error_class);`. If the implementation
  chooses the partial branch, the SQL is
  `CREATE INDEX idx_ops_error_class ON ops(error_class) WHERE error_class IS NOT NULL AND error_class <> '';`
	  and every query using the index must include the same predicate. If the
	  migration lands, it must bump
	  `presenter.SchemaVersion` and add/update the single chain-head migration test
	  only according to SOW-0107's umbrella chain-head rule.
	  If the partial-index branch is chosen, the implementation must include a
	  static SQL-builder test or equivalent query-contract test proving every
	  aggregate query intended to use `idx_ops_error_class` includes the matching
	  `WHERE error_class IS NOT NULL AND error_class <> ''` predicate. A reviewer
	  grep is not sufficient as the only guard once SQL builders exist.
	  The implementation plan must include the actual aggregate SQL/SQL builder
	  shape and EXPLAIN evidence for `model`, `provider`, `tool`, `agent`,
	  `client`, `status`, and `error_class` groupings before frontend work depends
	  on the API budget.
- Shared stress terms, especially `fan_out` / "too many", must use the same
  canonical `rest-api.md` "Stress metrics and dimensions" section as SOW-0111
  and SOW-0113. The shared section owns the canonical group enum:
  `model`, `provider`, `agent`, `tool`, `client`, `source_format`, `status`,
  and `error_class`. `ctx_pct` uses the same max `ctx_used / ctx_max`
  definition as the current topology builder; this endpoint must not introduce a
  different formula. For grouped statistics, `ctx_pct` is `MAX(ctx_used/ctx_max)`
  across matching ops in the active session/subtree and group; it is not an
  average unless a later SOW explicitly adds a separate metric. Stats responses
  and labels must identify this as group/subtree peak context pressure, for
  example with `ctx_pct_scope=group_peak`, so it is not confused with topology's
  node/subtree peak display.
	  SOW-0111 is the first writer of the metric definitions for this shared
	  glossary. SOW-0112 may add row-shape and statistics-specific scope fields only
	  after those definitions exist, or it must explicitly record that it is landing
	  against the temporary SOW-0113 enum skeleton without redefining metric
	  meanings. When `ctx_max` varies across ops in the same group, compute each
	  op's `ctx_used / ctx_max` ratio first and take the maximum ratio; do not divide
	  summed `ctx_used` by a single model capacity.
	- Stat `selection_key` values are deterministic within a sanitizer-version epoch.
	  If the SOW-0108 shared sanitizer algorithm changes, old bookmarked
	  `hi=stat:<key>` URLs may resolve as `unknown` and clear visibly. The
	  implementation plan must either prefix keys with a sanitizer/version marker or
	  document graceful invalidation as the accepted tradeoff before changing the
	  sanitizer.
- Live-update behavior follows the umbrella policy: `session_changed` marks
  statistics data stale and shows a compact "new data available" marker. The
  heatmap and grouped tables refresh on explicit user action, mode change, or
  explicit workbench refresh rather than silently re-fetching and shifting the
  current analysis. If a `hi=stat:*` bucket is active when `session_changed`
  arrives, the visible bucket enters a stale state tied to the old response.
  Resolver revalidation may verify that the key is recognized, but it must not
  silently replace the highlighted membership with a new bucket while the
  heatmap still shows old data. The user refreshes statistics or changes the
  filter/group/metric context to issue a new key. SOW-0113 owns the shared
  resolver-state wording and visible stale-highlight behavior.
- Performance validation depends on SOW-0107's shared large-session fixture.
  Non-performance component tests may use smaller fixtures, but the 500ms API
  budget and heatmap rendering budget cannot be claimed until
  `testdata/fixtures/large-session-v1/` exists and this SOW records its hash. If
  the fixture does not exist, EXPLAIN-dependent migration decisions cannot choose
  the "scan is safe" branch; use the default supporting-index branch or defer the
  claim until the fixture lands.
  If live-fold stats miss the 500ms gate after the index/query plan is corrected,
  the first fallback architecture is materialized subtree/stat rollups maintained
  by ingest or an explicit repair/reprocess path, with invalidation tied to the
  same session/source revision events used by the workbench. An in-memory LRU may
  be used only as a secondary optimization after the persistent/materialized
  freshness contract is specified; it is not sufficient by itself for correctness
  because missing/stale cache entries must be repairable after restart.

Risk and blast radius:

- Medium backend and frontend risk.

Sensitive data handling plan:

- Aggregate views should not expose raw prompt/payload content. Tests use
  synthetic or sanitized fixture data.

Implementation plan:

1. Define session-tree aggregate API contract and response row shapes, including
   `source_table`, sanitized dimension values, hash width/collision behavior,
   active global filter tuple in stat-key inputs, query-time `error_class`
   sanitization/versioning, and stat-key resolver output.
1a. Consume SOW-0107's shared subtree session-id resolver/helper for the current
    session subtree before live-folding or materializing stats. This SOW must not
    create a parallel recursive subtree walker or CTE pattern.
2. Define supported dimensions; `client` is blocked unless SOW-0108 defines it.
3. Add a dedicated heatmap component plan, including component tests for cell
   rendering, keyboard navigation, table fallback, Canvas/windowing threshold,
   2200-cell cap behavior, and stat-key selection wiring.
4. Add backend tests proving root/child subtree totals differ from own-session
   totals when descendants exist, plus the 500ms seeded performance budget,
   index audit, EXPLAIN evidence, and stale-key resolver behavior.
4a. Add or extend the stats failure fixture owned by this SOW under the shared
    large-session fixture package or a named tiny fixture. It must include at
    least three failed turns with non-empty sanitized `error_class` values so
    `failure_count`, `failure_rate`, heatmap failure coloring, and
    `error_class` grouping are tested without assuming successful turns exist.
5. Add frontend tests for grouping/heatmap rendering and filter/highlight
   behavior.
6. Implement session stats view and consume SOW-0113 shared
   selection/highlight hooks.
7. Validate performance on large ingested session tree.

Validation plan:

		- Backend aggregate tests, endpoint method/error tests, frontend component
		  tests, Playwright visual/selection/a11y tests, heatmap table-alternative
		  tests, performance smoke against the first committed shared fixture hash, and
		  a Playwright live-update test where `session_changed` shows the stats
		  "new data available" marker and explicit refresh re-queries/re-renders the
		  heatmap and grouped tables. Fixtures must include an all-failed-turns session
		  owned by this SOW so failure counts/rates, non-empty `error_class` grouping,
		  and heatmap failure coloring cannot accidentally assume at least one
		  successful turn.

Artifact impact plan:

- Specs: `rest-api.md`, `ui-pages.md`, statistics. `data-model.md` is required only if
  materialized rollups or new indexes/schema are added; live-fold-only work
  updates query/API specs without changing the schema contract.
- SOW lifecycle: child of SOW-0107.

Open-source reference evidence:

- Check observability trace/statistics dashboards for grouping/heatmap patterns
  before implementation plan.

Open decisions:

1. Scope default:
   - A. Full recursive session tree.
   - B. Root session only with toggle for children.
   - C. Current session subtree by default, which equals full tree when the
     current session is root.
   Recommendation: C, long-term-best. It satisfies the user's whole-tree need
   for root sessions and avoids surprising sibling aggregation when inspecting a
   child session.
2. Session stats API:
   - A. New `GET /api/sessions/:id/stats` endpoint.
   - B. Extend global `/api/stats` with a session/tree filter.
   Recommendation: A, long-term-best. It keeps the single-session contract clear
   and avoids overloading global stats semantics.
3. Query/materialization strategy:
   - A. Live-fold all rows from `ops`/`turns`.
   - B. Create session-tree-scoped rollups during ingest.
   - C. Reuse existing cached session aggregates where valid and live-fold only
     heatmap/detail rows not materialized today.
   Recommendation: A for first implementation, with B as the next step if the
   measured 500ms gate fails. C is valid only for own-session fields and must not
   be used for recursive subtree totals.

## Plan

1. Run external gap review.
2. Resolve aggregate contract findings.
3. Rerun the gap-analysis gate.
4. Draft implementation plan.

## Execution Log

### 2026-06-26

- Created focused SOW from statistics feedback.
- Incorporated external reviewer round-1 findings: session-scoped stats API,
  client dimension definition, heatmap axes/metric contract, subtree scope, and
  shared selection/highlight dependency.
- Incorporated external reviewer round-2 findings: query strategy, 500ms
  performance budget, new session heatmap component, removal of undefined
  `client`, stat-bucket filter semantics, and explicit API row shape.
- Incorporated external reviewer round-3 findings: dimension queryability,
  headend/source_format caveats, top-10/200-bucket degradation, truncation
  metadata, and error-class attribution.
- Incorporated external reviewer round-4 findings: `sessions.cost_usd` and other
  session aggregates are own-row values, not recursive subtree totals; subtree
  stats must live-fold or materialize; dimension queryability needs column/join
  evidence; `selection_key`/server-key ownership must be shared with SOW-0113;
  heatmap rendering needs a frontend budget.
- Incorporated external reviewer round-5 findings: `headend` is deferred from
  first-pass dimensions/URL contract, stat `selection_key` uses deterministic
  hash-of-sanitized-predicate, heatmap SVG/DOM threshold starts at 500 cells, and
  subtree live-folding must avoid recursive-CTE planner pitfalls.
- Incorporated external reviewer round-6 findings: heatmap bucketing is
  contiguous equal-turn-count ranges, API rows include `source_table`, stat
  resolver behavior is owned here, hash collision handling is required,
  heatmap rendering threshold now reconciles with the 2200-cell capped shape,
  and index/EXPLAIN evidence is required for the live-fold query plan.
- Incorporated external reviewer round-7 findings: `ctx_pct` is retained as a
  metric where computable, stat resolver path/schema are delegated to SOW-0113's
  shared endpoint, data-model spec impact is conditional on schema/index changes,
  and the index audit must prove the actual grouped query rather than assuming
  current partial indexes are enough.
- Incorporated external reviewer round-8 findings: acceptance criteria now
  distinguish root full-tree scope from child-subtree scope, the `agent`
  dimension consumes SOW-0108 `display_agent_label` before raw `agent_name`,
  collision handling has a concrete duplicate-detection/suffix mechanism,
  stat-key staleness semantics are defined for the shared resolver, and the
  500ms performance gate explicitly depends on SOW-0107's shared fixture.
- Incorporated external reviewer round-9 findings: stats view owns both the
  heatmap visualization area and grouped metric table area, new endpoint method
  behavior is explicit, resolver latency shares the 500ms stats budget, heatmap
  accessibility and table fallback are required, the 2200-cell cap supersedes
  the earlier 500-cell threshold, model/provider partial-index limits are noted,
  and collision suffixes have a 64-suffix failure bound.
- Incorporated external reviewer round-10 findings: client grouping is restored
  as a first-pass requirement through SOW-0108 `display_client_label` or an
  explicit cut/follow-up, dimension values consume the shared sanitizer, the
  index-audit baseline now distinguishes partial from plain indexes and flags
  the missing `ops.error_class` index, each grouped query needs SQL/EXPLAIN
  evidence, and shared stress metrics point to one `rest-api.md` glossary.
- Incorporated external reviewer round-11 findings: the heatmap/group dimension
  enum now includes both `provider` and `client`, the shared glossary owns the
  canonical dimensions, and `ctx_pct` must reuse the same formula as topology.
- Incorporated external reviewer round-12 findings: `fan_out` spelling is
  normalized in the heatmap metric list, agent/client grouping evidence is
  explicitly sequenced after the SOW-0108 display-label migrations or scoped
  fallback, `error_class` has a default supporting-index branch unless EXPLAIN
  proves a scan is safe, and the heatmap gets its own implementation-plan step
  with rendering, keyboard, fallback, cap, and selection tests.
- Round 13 reviewer rerun returned no new actionable findings for this SOW; the
  only P2 finding was scoped to SOW-0113 clearing cached server-key highlights
  when metric/group controls change.
- Round 14 completed on 2026-06-27 with accepted findings clarifying stats-view
  content-pane behavior, stat resolver source-table counts, `ctx_pct` grouping
  scope, SOW-owned `0015` migration numbering, and EXPLAIN/fixture-hash
  dependency.
- Round 15 completed on 2026-06-27 with accepted findings clarifying
  `ctx_pct` scope labeling, the default/non-partial `ops.error_class` index
  branch unless EXPLAIN proves a matching partial index, and the
  `SchemaVersion`/chain-head test companion for the `0015` migration.
- Round 16 completed on 2026-06-27 with accepted findings adding
  `sessions.display_agent_label` and `sessions.display_client_label` to the
  index audit, with explicit SQL/EXPLAIN proof required if bounded-subtree
  grouping does not need supporting label indexes.
- Round 17 completed on 2026-06-27 with accepted findings clarifying stats
  ownership of the table area while preserving `table=` URL round-trips and
  adding the single-flight or measured-duplicate decision for heavy stats
  endpoint requests.
- Round 18 completed on 2026-06-27 with accepted findings clarifying
  `error_class` raw-value grouping, op-level `status` grouping scope, heatmap
  bucket remainder determinism, stats live-update refresh behavior, and
  sequencing against SOW-0111's shared metric definitions.
- Round 19 completed on 2026-06-27 with accepted findings clarifying the
  first-pass stats response schema for totals/breakdowns, zero-turn heatmap
  behavior, heatmap a11y threshold, canonical failure-count predicate
  consumption, and explicit live-update validation coverage.
- Round 20 completed on 2026-06-27 with accepted findings adding
  `generated_at` semantics, `ctx_pct_scope` to breakdown rows, a concrete
  200-turn bucket threshold, mandatory heatmap table-alternative behavior,
  concrete heatmap interaction pass criteria, and chain-head coordination with
  SOW-0107.
- Round 21 completed on 2026-06-27 with accepted findings clarifying sparse/null
  `ctx_pct` cells, separating the heatmap's accessible cell table from grouped
  metric tables, and pinning concrete `idx_ops_error_class` SQL branches.
- Round 22 completed on 2026-06-27 with accepted findings clarifying `ctx_pct`
  zero/missing-capacity behavior, all-null heatmap rendering, grouped metric
  table sorting/filtering, degraded agent/client grouping when SOW-0108 storage
  is unavailable, heatmap keyboard/table layout for large bucket counts, contrast
  requirements, all-failed-turn fixtures, and default metric/group controls.
- Round 23 completed on 2026-06-27 with accepted findings clarifying full-depth
  stats rationale versus topology depth caps, null/empty dimension buckets,
  heatmap table alternative sort and keyboard navigation, global filter
  reflection in stats rows, `ctx_pct` with varying `ctx_max`, and stat
  `selection_key` sanitizer-version durability.
- Round 24 completed on 2026-06-27 with accepted findings clarifying zero-turn
  `view=stats` API/UI behavior, partial `idx_ops_error_class` predicate test
  requirements, and SOW-owned all-failed/non-empty-`error_class` fixture
  coverage.
- Round 25 completed on 2026-06-27 with accepted findings clarifying that stat
  `selection_key` inputs include the active global filter tuple, that `client`
  grouping must prove whether it is distinct from `source_format`, that
  `error_class` sanitization is query-time/versioned unless performance forces a
  stored field, and that a failed 500ms live-fold gate falls back to a
  materialized rollup/freshness contract rather than an ad hoc cache.
- Round 26 completed on 2026-06-27 with accepted findings aligning statistics
  `failure_count` with the shared terminal-failure family, recording the
  equal-turn-count heatmap bucketing tradeoff and visible stale-key behavior on
  refresh, and explicitly consuming SOW-0107's shared subtree resolver/helper.
- Round 27 completed on 2026-06-27 with accepted findings pinning average
  denominators, accessible heatmap legend wording, `error_class` fragmentation
  fallback threshold, raw status versus normalized filter labels, unknown raw
  op-kind count semantics, and stale stat-key behavior during live updates.
- Round 28 completed on 2026-06-27 with no statistics-specific P0/P1/P2 changes;
  accepted shared-key and failure-predicate concerns are handled by SOW-0107's
  shared hash-helper decision and expanded failure-predicate audit.
- Round 29 completed on 2026-06-27 with no statistics-specific P0/P1/P2 changes;
  P3 wording clarified that SOW-0107 step 5c owns the shared-vs-separate
  hash-helper decision for topology/statistics keys.
- Round 31 completed on 2026-06-27 with no statistics P0/P1/P2 changes. Mimo's
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
