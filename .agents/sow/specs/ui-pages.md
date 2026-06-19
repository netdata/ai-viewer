# UI Pages

## TL;DR

Single-page React app. Five primary routes plus a global filter bar that is always visible. Modern dark/light theme.

## Global Layout

```
┌─────────────────────────────────────────────────────────────┐
│ ai-viewer    [Sessions][Topology][Tools][Models][Agents]    │ ← nav
│ ────────────────────────────────────────────────────────────│
│ Time range: [Today ▾] [from] [to]                           │
│ Sources: [☑ all] | Agents: [...] | Models: [...] | Tools[..]│ ← global filter bar
│ Status: [☐ running ☐ completed ☐ failed]                    │
│ ────────────────────────────────────────────────────────────│
│                                                             │
│                    <Route content>                          │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

The global filter bar is always visible and applies to every page; routes interpret it appropriately. Filters are serialized into the URL query string so views are shareable.

A **"Copy share link"** button in the FilterBar (SOW-0007) serializes the current
filter state into the URL query string — reusing the existing URL-state mechanism
(`readFilters`, §`/` SessionsList) — and copies the resulting URL to the clipboard.
Opening the copied URL restores the exact filtered view. If a deeply-filtered URL
would exceed ~2 KB, the button falls back to a compressed encoding of the filter
state (the only case compression is applied); short URLs stay plain and
human-readable.

## Routes

### `/` — Sessions list

- **Primary by default** — the list shows PRIMARY (root) sessions only, most
  recent first. A "Show secondary" toggle (SOW-0068) switches the query to
  `group=all`, revealing secondary sessions — sub-agent (`sub_agent`),
  tool-internal (`tool_internal`), and fork (`fork`) kinds — each marked with a
  small kind badge next to the agent name so primary vs secondary is obvious at
  a glance. Primary (root) rows render no badge (the default).
- **Secondary drill-in (SOW-0068)** — a secondary row whose `parent_session_id`
  is set carries a "↩ parent" link to its parent session's Topology tab
  (`/sessions/<parent>?tab=topology`), so the operator can jump straight to the
  tree that spawned it. Root rows keep the existing `child_session_count`
  expander → the session's own detail (Overview lists its children).
- Columns: agent, model, start time, duration, status (with color), turns, ops, tokens in/out, cost, failures.
- Click a row → session detail page.
- Live updates: as new sessions appear within the timeframe + filters, they fade-in at the top of the list. *(Phase-1: the list refreshes live via SSE query-invalidation; the fade-in animation itself is Phase-2 — see §"Realtime UX Rules" and SOW-0018.)*

### `/sessions/:id` — Session detail

Tab layout:

1. **Overview** — header with agent/model/status; session-level statistics
   (tokens in — labeled "fresh"/uncached — tokens out, cache read, cache write,
   cache hit rate, cost, turn/op/failure counts) read from the session-detail
   response `GET /api/sessions/:id` (the session row already carries these
   aggregates) plus a tools-used summary derivable from its ops. NOTE:
   `/api/stats` does NOT support a `session_id` filter (it is the cross-session
   analytics endpoint); per-session *breakdowns* (e.g. this session's cost
   by-model/by-tool) are a Phase-2 enhancement that would require adding
   `session_id` support to `/api/stats`. Phase 1 uses the detail-endpoint
   aggregates only.
2. **Topology** — D3 graph of the agents and tools that participated; node size encodes the user-selected metric (cost/tokens/duration/calls/ctx-pct), node color encodes failures, node icon distinguishes agent vs tool; tooltip shows high-level stats per actor. **Layout is operator-selectable via a toggle (SOW-0006 decision): (a) seeded force-directed + a "freeze layout" button, (b) plain force-directed, (c) hierarchical tree.** All three render the same `/topology` node/edge data; the operator compares them live and a default is chosen later. **Freeze** pins node POSITIONS (stops the simulation) while node data — labels, sizes, failure ratios — stays live on metric/filter/SSE updates. Force simulation runs in a Web Worker above the 100-node threshold (`frontend-architecture.md`).
3. **Trace (APM)** — the primary "what happened, in what order, how long" view (SOW-0006 decision). Toggleable renderings of the same session op tree, plus a list:
   - **Waterfall** (default) — a Chrome-DevTools-Network-tab-style horizontal waterfall: one row per op (agent / tool / llm / reasoning / …), positioned on a shared time axis by `start_ts`, nested parent op → children → child-session transitions. Color = `op.kind`; failed ops outlined red. **Source-aware rendering (SOW-0006 decision, from real-data review):** ops with a measured span (`end_ts > start_ts` — tools everywhere, and ai-agent LLM/reasoning which record `durationMs`) draw as bars sized by duration; **point-event ops** (`end_ts == start_ts`, e.g. claude-code LLM/reasoning — the source records the message at one timestamp with no call duration) draw as instant **ticks/markers**, never zero-width bars, so the view never implies a duration the source did not record.
   - **Big-session navigation (SOW-0006 decision, from real-data review) — two user-selectable views of the same waterfall data:** real sessions are huge (observed: 11,928 ops over ~5 days), so both are first-class and the user picks. (a) **Detailed** — every op on a zoomable/pannable time axis (shift+wheel zooms time, drag pans time; **the zoom scales the time/X axis ONLY** — the left row-label gutter stays fixed and row height is constant, so plain wheel scrolls the rows vertically while only the time track zooms/pans, mirroring the Timeline tab's X-only zoom convention; Canvas + viewport culling above the SVG span ceiling, `frontend-architecture.md`), default fit-to-window; **turn boundaries are delineated** so inter-turn gaps read as "between turns", not stalls. (b) **By-turn** — one aggregated bar per turn (rolling up its ops), click a turn to expand into its ops. Performance is the hard constraint (200 ms render budget) regardless of view.
   - **Flame-graph** — an alternate view of the same tree (stacked spans by depth). A large flame-graph is acceptable as long as it stays fast: Canvas rendering + viewport culling above the SVG span ceiling (`frontend-architecture.md`); the 200 ms render budget holds.
   - **Event list** — a scrollable, virtualized list of everything that happened (every op/turn in order: ts, kind, name, duration, status), click-to-detail. Point-event ops show "—" (not "0µs") for duration. Always available alongside whichever visual rendering is active.
   Clicking any span/row opens the shared **span detail drawer** (item below).
4. **Timeline** — video-editor style. Horizontal time axis; one lane per session (root + children stacked). Spans drawn as bars; overlap is intentional (parallel sub-agents are visible). Compaction ops (`kind='compaction'`) render as full-height vertical breakpoints. Pan + zoom; **shift+wheel zooms time, plain wheel pans** (SOW-0006 default; zoom scales the time/X axis only — lane height stays constant). Compaction breakpoints are full-height and time-window-culled (never per-lane culled). SVG under the visible-span ceiling; **above it — OR when the lane stack is taller than the Canvas viewport** — a bounded Canvas with viewport-clipped culling (`frontend-architecture.md`): its backing store never exceeds the viewport, and lanes are virtualized by native vertical scroll (`scrollTop`) so a high-lane-count timeline never allocates an over-tall canvas. In that Canvas path plain wheel scrolls the lanes (vertical) while shift+wheel still zooms time (X); the keyboard-fallback list mirrors only the viewport spans (no DOM node per span at scale).
5. **Logs** — filterable log entries (severity, op, source).

**Span detail drawer (shared across Trace / Topology / Timeline — SOW-0006 decision):** clicking any span/node/row opens a right-side drawer (NOT a modal — the visualization stays visible behind it). The drawer is **source-aware and never fabricates unavailable fields as zero** (SOW-0006 decision): from the **Trace** tab it shows the full op row (model, provider, tokens, cost, context, child-session, error) + its `payload_refs` list; from the **Timeline** tab a span shows only the fields the lane/span model carries (kind, name, status, start/end, derived duration) and directs the operator to the Trace tab for token/cost/payload detail (the timeline payload has no op metrics — the drawer does NOT show `$0.00` / `0 tokens` / "No payloads" for them); from the **per-session Topology** tab a node shows the actor's real aggregate (kind, label, failure %, and the value of the currently-selected size metric) — a node is not an op, so op-only fields are omitted, not zeroed. (The 4 KB payload BYTE-preview + "download full" link is **deferred to SOW-0033** — that endpoint reads source-file byte ranges, a security surface; until it lands the Trace-op drawer shows a disabled "preview coming soon" control next to each ref.) Esc / outside-click closes; focus-trapped for a11y. Op-kind/status colors come from the theme tokens (SOW-0006 default), consistent with the Overview status badges.

### `/topology` — Cross-session topology

A larger D3 view aggregating all sessions in the active filter. Same node/edge model as the per-session topology, but counts span the full filtered set. Useful for "which tools does my agent actually use, weighted by cost".

### `/stats` — Statistics dashboard (SOW-0007, redesigned SOW-0067)

The unified statistics-and-analytics dashboard. Its **own route** — `/` stays
focused on the sessions list. Uses the global FilterBar (timeframe `from`/`to`
plus the dimensional filters) like every other page; the charts re-fetch on filter
change. **SOW-0067 adds a time-range preset control to the FilterBar itself**
(Last 1h / 24h / 7d / 30d / All) so the operator can scope every chart without
hand-editing the URL — the preset writes the already-supported `from`/`to` params.

Layout:

- **Trends (multi-series line chart)** — per-day cost / tokens / sessions
  (`session_starts`) / failures / **failure rate** / duration over the selected
  range, from `GET /api/stats/aggregate?bucket=daily&group_by=<dim>` (SOW-0067).
  A **"Group by" control** selects the overlay dimension
  (`total`/`model`/`provider`/`tool`/`agent`/`cwd`/`source_format`); the chart
  renders one polyline per group value (≤8 visible series, the rest rolled into
  an "other" bucket) with a text legend so color is never the sole signal. The
  LineChart primitive already supports multiple series (one `<path>` per series);
  SOW-0067 only wires the control through. **Failure rate** is a client-derived
  pseudo-metric: the page fetches the `failures` and `calls` aggregates for the
  same group_by and divides per bucket — ratios do not aggregate, so the server
  cannot SUM a rate; this two-fetch + per-bucket divide is the correct approach.
  Sessions are plotted from the additive `session_starts` metric
  (`data-model.md` §Rollup tables), never a non-additive distinct count. A series
  with a SINGLE data point (one bucket) renders a small filled dot at that point —
  a lone-`M` polyline draws nothing, so the marker keeps a one-bucket trend
  visible; multi-point series draw the polyline only.
- **Horizontal bar charts (Top-N)** — top-N model / provider / tool / agent / cwd,
  from `GET /api/stats/top` (dimension/metric selectable). Each bar's value label
  sits just after the bar end by default; a long bar whose outside label would
  clip the right edge draws the label INSIDE the bar end (end-anchored,
  higher-contrast text token) so it always stays within the SVG viewport.
- **Multi-metric comparison table (SOW-0067)** — the operator picks a "group by"
  dimension (model / source / agent / tool / status / error_class) and sees a
  STABLE column set across all values: Name · (Calls or Sessions) · Failures ·
  Failure% · Cost · Tokens in · Tokens out · Cache read · Cache-hit% · Duration.
  Cells render the value where the dimension owns that metric and an em-dash "—"
  where it genuinely does not (e.g. tokens/cache for tools) — columns are NEVER
  silently hidden (SOW-0067 honesty fix). Cache-hit% is client-derived
  (`cache_read/(cache_read+tokens_in)`). Each row is **clickable** (SOW-0067
  drill-down): clicking a model row sets the global `models` filter (and so on),
  re-scoping the whole dashboard to that slice. Sortable by clicking a column
  header. A "Comparison table" header carries a truncation footer when a dimension
  exceeds the row cap.
- **Failure analysis (SOW-0067)** — error-class distribution table + horizontal
  share bar over the failed-session set, sourced from the `by_error_class`
  breakdown. Shown when at least one failed session has a classified error_class.
- **Deep-search box** — a text input that posts its query to `GET /api/search`;
  results list matched ops and logs and link to the relevant session/op
  (`rest-api.md` — `op_id`/`session_id`/`log_id` linkage). When the matched source
  has log indexing disabled the results note `logs_indexed: false`
  (`data-model.md` §Full-text search).

**CSV export (SOW-0067, AC#5):** every data section — trends, top-N, the
comparison table, and failure analysis — carries an "Export CSV" button producing
the rows currently in view (header row names the dimension + metric).

Charts reuse the D3 line + bar primitives from SOW-0006 in `viz/` — **no new chart
library** — and honor the same rendering conventions: SVG below the per-chart point
ceiling, Canvas above it (`frontend-architecture.md`), and the ≤500 KB gzipped
main-chunk budget (`quality-gates.md` §Bundle Size). a11y: color is never the sole
signal (series are labelled / patterned), every control is keyboard-reachable, and
the route is axe-clean. Live: the charts are TanStack Query-backed and invalidated
by the `stats_invalidated` SSE event (already wired — the ingester emits one
`stats_invalidated` notify row per batch when rollups change, `ingester.md` §Notify
Channel; `sse-protocol.md`).

### `/tools` — Tools analytics

Table of `catalog_tools` filtered by the active timeframe.

- Columns: namespace, name, calls, failures, failure rate, total duration, avg duration, total tokens, total cost.
- Sortable. Search box. Click → drills into a filter view (`/sessions?tools=...`).

### `/models` — Models analytics

Table of `catalog_models`. Same shape, scoped to models.

### `/agents` — Agents analytics

Table of `catalog_agents`. Same shape, scoped to agents.

### `/sources` — Admin / status panel

- Each source with: format, location, last_seen, ingest lag, parse_errors count, enabled toggle (disabled in v1 — read-only since we don't write to source config; v2 SOW will add).
- `/api/health` data surfaced here as well.

## Theme

- **OS preference is the default** — the app reads `prefers-color-scheme` and matches the operating system. Auto-switches when the OS toggles its preference.
- A three-state theme control in the header — **Auto** (follow OS), **Dark**, **Light** — persists the operator's choice in `localStorage`. Auto-mode has no `localStorage` entry; explicit choices override OS.
- Dark and light are first-class equals; both are polished. Neither is the "real" one with the other as a stripped-down sibling.
- Color tokens defined in `frontend/src/theme/tokens.css` (see `frontend-architecture.md` for the resolution algorithm and inline-script no-flash trick).
- Status colors: green (completed), amber (running), red (failed/interrupted/abandoned distinguished by icon + label), gray (unknown).
- Font: system stack (SF Pro / Segoe UI / Inter); monospace for IDs and timestamps (JetBrains Mono / Menlo).

## Realtime UX Rules

These describe the eventual target. The Phase-1 status of each is noted; the
forward-looking visual rules (fade-in, value animation, the visible live
indicator) are NOT yet implemented — `useLiveUpdates` connects the SSE stream
and invalidates queries (data refreshes live), but the connection state is not
surfaced to any DOM element. The visible live indicator + entrance/value
animations are tracked in `.agents/sow/pending/SOW-0018-…live-indicator…` (the
deferred half of SOW-0001 Chunk-18 D4).

- Items entering view fade in over 200ms (no abrupt jumps). *(Implemented for new spans in the Trace + Timeline SVG views — SOW-0006 AC#6, honoring `prefers-reduced-motion`; the sessions-list fade-in remains Phase-2.)*
- Counters and stats animate from old value to new (200ms ease). *(Phase-2: not yet implemented.)*
- Live indicator (small pulsing dot) in the header when an active SSE subscription is connected. *(Phase-2: not yet implemented — Phase-1 has no visible connection indicator; Chunk-18 E2E asserts SSE liveness at the subscription/EventSource protocol level instead.)*
- If SSE disconnects: indicator goes amber, with a tooltip "reconnecting…". Auto-reconnect handled by EventSource. *(Phase-2: not yet implemented; EventSource auto-reconnect itself is active.)*
- If `resync` event arrives: silently re-fetch current view. *(Implemented — `sse.ts` invalidates all queries on `resync`.)*

## Keyboard Shortcuts

- `/` — focus the global search/filter
- `t` — cycle theme control (Auto → Dark → Light → Auto)
- `?` — show keyboard shortcuts modal
- `Esc` — close any open modal

More to be added as needs emerge (operator-driven via feedback).

## Phase Mapping

| Phase | Routes delivered |
|---|---|
| 1 | `/`, `/sessions/:id` (Overview + Logs tab), `/sources`, with ai-agent v2 + v3 adapters |
| 2 | `/sessions/:id` Trace + Topology + Timeline tabs |
| 3 | `/topology`, `/tools`, `/models`, `/agents` cross-session analytics |
| 4 (SOW-0007) | `/stats` unified statistics dashboard (line + bar charts + deep search) and the "Copy share link" FilterBar button |
| 4 | claude-code, codex, opencode adapters |
| 5 | Polish, advanced filters, keyboard shortcuts modal, deep search |

SOW-0007 delivers the unified `/stats` dashboard (cost/token/session/failure trends,
top-N breakdowns, and the `/api/search` deep-search box). It does **not** build the
per-dimension `/tools`, `/models`, `/agents` table pages — those remain the
Phase-3 `ComingSoon` routes; `/stats` is the single analytics surface SOW-0007
ships.

## Phase-1 Implemented Behavior

The pages delivered in Phase 1 implement the following concrete behavior. This
section is the durable record of WHAT the shipped UI does (the forward-looking
sections above describe the eventual target); reviewers and the next session
read this to know the current contract without re-reading the code.

### `/` — SessionsList (live)

- Data: `useSessions(filters, 'root')` — `filters` come from `useFilters()`
  (the global FilterBar drives them via the URL). Root sessions only.
- Renders a `<table>` of `SessionRow` (agent, model, start, duration, status,
  turns, ops, tokens in/out, cost, failures). A leading cell shows the
  `child_session_count` as a drill-down affordance: when `> 0` it links to the
  session **detail** page (`/sessions/:id`), whose Overview lists the session's
  `child_sessions`; when `0` it is a plain dash. (The link must not emit a
  `?root=` query — no list filter consumes `root`; only `group` is parsed by
  `readFilters`, and `/api/sessions` has no `root`/`root_session_id` filter.)
- **Keyset pagination**: a "Load more" control (the shared `LoadMore`
  primitive) appends the next page using `next_cursor`. Implemented with
  TanStack `useInfiniteQuery`; pages are concatenated, never reset on cursor
  (the cursor contract in `rest-api.md` forbids replaying a cursor under a
  changed query — a filter change starts a fresh query, which mints a fresh
  first page). The control is hidden when no `next_cursor` is present.
- States: distinct **loading** (`LoadingState`), **error** (`ErrorState`
  showing the `ApiError.message`), and **empty** (`EmptyState`) renders. A row
  click navigates to `/sessions/:id` (id `encodeURIComponent`-encoded — done by
  `SessionRow`'s `<Link>`).
- Live: `useLiveUpdates(subscriptionFilter)` keeps exactly one SSE subscription
  open for the active filter; `session_changed` / `resync` frames invalidate
  the `['sessions']` key and the list refetches.

### `/sessions/:id` — SessionDetail (live)

- Data: `useSessionDetail(id)`. The active tab is stored in the URL query param
  `?tab=` (so a tab is shareable / back-button friendly), defaulting to
  `overview`; an unknown `?tab=` value falls back to `overview`.
- **404**: when the detail query fails with `ApiError.status === 404` (unknown
  id), the page renders a clean "session not found" state instead of the tabs.
- **Overview tab**: header (agent / model / status badge) plus per-session
  aggregate `StatCard`s in order: "Tokens in (fresh)" (hint "uncached input"),
  "Tokens out", "Cache read", "Cache write", "Cache hit rate" (hint "cache
  read / total input"; `data-testid="cache-hit-rate"`; "—" when there is no
  input), "Cost", "Turns", "Ops", "Failures" — all read from the **detail
  response** `session` row — NOT `/api/stats`
  (cross-session only; see `rest-api.md` §GET /api/stats). Plus a tools-used
  summary derived from the response's ops (`kind === 'tool'`), aggregated by
  op `name` with call + failure counts. Plus a **child-sessions** section
  listing `detail.child_sessions` (agent, model, status, ops, failures, cost),
  each row linking to that child's detail page `/sessions/:child.id`; rendered
  only when the session has children (a leaf session shows no child-sessions
  section). This is the drill-down target the SessionsList child-count link
  promises (§`/` SessionsList).
- **Logs tab**: `useSessionLogs(id, { severities })` with a severity
  multi-select (DBG/INF/WRN/ERR) and a "Load more" control. Renders log rows
  (ts, severity, source, op id, message) via the `LogRow` primitive. The
  severity set is local tab state; selecting none = all severities (an empty
  `severity` set omits the param, per the present-but-empty rule in
  `rest-api.md`). Distinct loading / error / empty states.
- **Trace / Topology / Timeline tabs**: shipped (SOW-0006) — see the tab
  contract above (items 2-4) and the shared span-detail drawer.
- Live: `useLiveUpdates({ session_id: id })` invalidates `['session', id]` on
  `session_changed`, so the open session refreshes in place.

### `/sources` — Sources (live)

- Data: `useSources()` + `useHealth()`. Renders an overall health badge
  (`ok` / `degraded` / `down` from `/api/health`) and a `<table>` of sources
  (id, format, enabled, parse_errors, lag, last_seq). Lag is rendered from the
  health row's `lag_us` (the sources list itself carries no lag field).
- States: loading / error / empty.
- **Independent error surfacing (no silent failures, AGENTS.md §6)**: the two
  queries fail independently. A `/api/health` failure renders a health-error
  banner (showing the `ApiError.message`) ABOVE the still-rendered sources
  table — health being unavailable is never hidden behind dashes for lag. A
  `/api/sources` failure renders the `ErrorState` for the table. If only one
  query fails, the other still renders.
- **Stale health is suppressed on error**: when `useHealth` is in an error
  state (`isError`), the status badge and health-derived lag are suppressed
  even if a prior successful `health.data` is still cached — TanStack Query
  retains the last good payload across a failed background refetch (which is
  the live `source_status_changed` path). The error banner is then the sole
  health indicator and every lag cell falls back to '—'. This prevents a
  contradictory UI (red banner beside a stale green badge / stale lag).
- Live: `useLiveUpdates({})` — a `source_status_changed` frame invalidates both
  `['sources']` and `['health']`.

### Shared UI primitives (Phase 1)

- `Tabs` — accessible tablist driven by a controlled active key. Implements the
  WAI-ARIA tabs pattern: **roving tabindex** (the selected tab has `tabIndex=0`,
  all others `tabIndex=-1`) and keyboard navigation — `ArrowLeft`/`ArrowRight`
  move selection between tabs (wrapping at the ends), `Home`/`End` jump to the
  first/last tab; each arrow/Home/End also calls `onSelect` and focuses the
  newly-selected tab. Each tab carries `id=tab-<key>` and
  `aria-controls=tabpanel-<key>`; the caller's single panel is
  `role=tabpanel`, `id=tabpanel-<active>`, `aria-labelledby=tab-<active>`, and
  `aria-live="polite"` so a content change on tab switch is announced.
- `LogRow` — one log-entry row.
- `LoadMore` — a button that triggers fetching the next keyset page; shows a
  busy label while fetching; renders nothing when there is no next page.
- `LoadingState` / `ErrorState` / `EmptyState` — small status primitives shared
  by every page. `ErrorState` surfaces the `ApiError.message` (no silent
  failures — AGENTS.md).
