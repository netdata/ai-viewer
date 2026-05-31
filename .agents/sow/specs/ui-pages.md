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

## Routes

### `/` — Sessions list

- Hierarchical list: root sessions at the top level, expandable to show child (sub-agent) sessions.
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
2. **Topology** — D3 force-directed: nodes are agents and tools that participated; node size encodes the user-selected metric (cost/tokens/duration/calls/ctx-pct); node color encodes failures; node icon distinguishes agent vs tool. Tooltip shows high-level stats per actor.
3. **Trace (APM)** — spans laid out as nested rows (parent op → children → child sessions). One row per op. Color and width encode duration; failures are red. Expanding a row shows its log lines and payload links.
4. **Timeline** — video-editor style. Horizontal time axis; one lane per session (root + children stacked). Spans drawn as bars; overlap is intentional (parallel sub-agents are visible). Pan + zoom; shift+wheel zooms time.
5. **Logs** — filterable log entries (severity, op, source).

### `/topology` — Cross-session topology

A larger D3 view aggregating all sessions in the active filter. Same node/edge model as the per-session topology, but counts span the full filtered set. Useful for "which tools does my agent actually use, weighted by cost".

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

- Items entering view fade in over 200ms (no abrupt jumps). *(Phase-2: not yet implemented.)*
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
| 4 | claude-code, codex, opencode adapters |
| 5 | Polish, advanced filters, keyboard shortcuts modal, deep search |

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
- **Trace / Topology / Timeline tabs**: `ComingSoon` (Phase 2).
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
