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
- Live updates: as new sessions appear within the timeframe + filters, they fade-in at the top of the list.

### `/sessions/:id` — Session detail

Tab layout:

1. **Overview** — header with agent/model/status; per-session statistics tailored from `/api/stats?session_id=...`.
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

- **Dark mode default** (sessions are usually viewed late at night when debugging).
- Light mode toggle in header. System-preference detection on first load.
- Color tokens defined in `frontend/src/theme/tokens.css`. Status colors: green (completed), amber (running), red (failed), gray (unknown).
- Font: system stack (SF Pro / Segoe UI / Inter); monospace for IDs and timestamps (JetBrains Mono / Menlo).

## Realtime UX Rules

- Items entering view fade in over 200ms (no abrupt jumps).
- Counters and stats animate from old value to new (200ms ease).
- Live indicator (small pulsing dot) in the header when an active SSE subscription is connected.
- If SSE disconnects: indicator goes amber, with a tooltip "reconnecting…". Auto-reconnect handled by EventSource.
- If `resync` event arrives: silently re-fetch current view.

## Keyboard Shortcuts

- `/` — focus the global search/filter
- `t` — toggle theme
- `?` — show keyboard shortcuts modal
- `Esc` — close any open modal

More to be added as needs emerge (user-driven via feedback).

## Phase Mapping

| Phase | Routes delivered |
|---|---|
| 1 | `/`, `/sessions/:id` (Overview + Logs tab), `/sources`, with ai-agent v2 + v3 adapters |
| 2 | `/sessions/:id` Trace + Topology + Timeline tabs |
| 3 | `/topology`, `/tools`, `/models`, `/agents` cross-session analytics |
| 4 | claude-code, codex, opencode adapters |
| 5 | Polish, advanced filters, keyboard shortcuts modal, deep search |
