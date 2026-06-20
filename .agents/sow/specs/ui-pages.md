# ui-pages — Per-page UI contract catalog

**Date**: 2026-06-20 (SOW-0081)
**Author**: CTO
**Status**: Living spec. New pages added incrementally as SOW-0081 through SOW-0086 ship.

## Why this document exists

The per-page pattern was established in SOW-0073 (header + summary stats + toolbar + designed table + empty/loading/error states) and refined in SOW-0079 (Home summary card + /failures). SOW-0080 ships 6 SOWs that add /agents, /models, /tools, /ingest-errors, and per-entity detail pages. This spec is the per-page contract catalog: for each route, what the header looks like, what the summary stats are, what the toolbar offers, what the content is, and what the empty / loading / error states look like. New pages copy the pattern; deviations are explicit and justified.

## The base pattern (from SOW-0073 + SOW-0079)

Every page in the app follows this shape:

```
<section aria-labelledby="<page>-title" className="flex flex-col gap-6 px-6 py-5">
  {/* Page header: title + count + subtitle + optional actions (time window, refresh, etc) */}
  <div className="flex flex-wrap items-end justify-between gap-4">
    <div>
      <h1 id="<page>-title" className="text-2xl font-semibold tracking-tight">Title</h1>
      <p className="mt-1 text-sm text-muted-foreground">Subtitle / what this page is.</p>
    </div>
    <div className="actions">{/* time window, refresh, etc */}</div>
  </div>

  {/* Summary stat strip — only when the data is loaded and non-empty */}
  {data ? <SummaryStatStrip data={data} /> : null}

  {/* Content: table, grid, chart, etc. Empty/loading/error states inside. */}
  {isPending ? <Skeleton /> : isError ? <ErrorState /> : items.length === 0 ? <EmptyState /> : <Content data={items} />}

  {/* Pagination / load-more if applicable */}
</section>
```

## Page catalog

### `/` — Sessions

The home page. After SOW-0079:
- Header: "Sessions" + count
- Subtitle: "Live snapshot of every AI coding-agent session across all configured sources."
- Above header: Home summary card (5 tiles: Active / Today's spend / Failed today / Sessions today / Reliability)
- Toolbar: Roots only / Sub-agents and forks toggle, sort direction (Newest/Oldest first), density (Comfortable/Compact), Refresh
- Content: Sessions table (designed)
- Empty: "No sessions match these filters"
- Loading: Skeleton rows
- Error: ErrorState with the API error message

### `/failures` — Recent failures (SOW-0079 P0.2)

- Header: "Recent failures" + subtitle "Sessions that ended with a failure, an abandonment, or an interrupt."
- Toolbar: time window selector (24 hours / 7 days / 30 days / all time) — drives the URL `from` bound
- Summary strip: Failures / Cost / Tokens / Top error class (4 tiles)
- Error class chip strip (clickable, filterable)
- Content: failures table (agent, model, error class, started, cost, tokens, status, drill-down)
- Empty: "No failures in this window — that's good."

### `/topology` — Cross-session topology

(SOW-0006, polished in SOW-0075)
- Header: "Topology" + subtitle
- Content: D3 graph of actor + tool calls

### `/stats` — Statistics

(SOW-0007 + SOW-0076)
- Header: "Statistics"
- Content: charts (line chart + horizontal bar chart + sparkline trends)

### `/sources` — Sources

(SOW-0077)
- Header: "Sources"
- Content: source list with health indicators

### `/agents` — Agents list (SOW-0081)

- Header: "Agents" + subtitle "Every distinct agent name across your sessions, ranked by activity."
- Summary strip: Total agents / Total sessions / Total cost / Average reliability (4 tiles)
- Content: agent grid — each card shows agent name, session count, cost, reliability %, last seen; click → /agents/:name
- Empty: "No agents in this time window."

### `/agents/:name` — Agent detail (SOW-0081)

- Header: agent name (h1) + subtitle "Back to all agents" link + summary stat strip for this agent only
- Toolbar: time window selector (24h / 7d / 30d / all)
- Summary strip: Sessions / Cost / Tokens / Failures / Reliability (5 tiles, scoped to this agent)
- Content: sessions table filtered to this agent (uses useSessionsInfinite with agents=:name filter)
- Empty: "No sessions for this agent in this window."

### `/models` — Models list (SOW-0081)

Same shape as /agents, but for models. Each card shows model + provider + sessions + cost + pct_of_cost.

### `/models/:name` — Model detail (SOW-0081)

Same shape as /agents/:name, but filtered to the model.

### `/tools` — Tools list (SOW-0081)

Same shape as /agents, but for tools. Each card shows tool namespace + name + calls + failures + pct_of_calls.

### `/tools/:name` — Tool detail (SOW-0081)

Same shape as /agents/:name, but filtered to the tool.

### `/ingest-errors` — Ingest errors (SOW-0082)

- Header: "Ingest errors" + subtitle "Recent parse + ingest errors across all sources."
- Toolbar: source filter + severity filter
- Content: log entries table (source, file, line, severity, message, time)

### `/sessions/:id` — Session Detail (SOW-0074, SOW-0083)

- Breadcrumb above H1: "Sessions / [parent session] / [current id]" with a "← Back to sessions" link.
- Tabbed layout: Overview / Trace / Topology / Timeline / Logs / Raw Data.
- H1 shows the agent name (not just "Session detail").
- The breadcrumb's parent link lets the operator walk up the sub-session tree without using browser back.

### `/failures` — Recent failures (SOW-0079, SOW-0083)

- Rows expand on click to show a 1-line summary (root vs sub-session position, turns/ops/failures counts) plus a "View parent" link for sub-sessions.
- Second click on the same row collapses the summary.
- The existing "Open" link still navigates to the full Session Detail (with `e.stopPropagation`).

### ⌘K Command Palette (SOW-0074.1, SOW-0084)

- Route navigation (existing): Sessions / Topology / Stats / etc.
- Theme switching (existing): Auto / Dark / Light.
- **Search results (SOW-0084)**: when the query has non-whitespace content, fetches `/api/search` and shows the top 4 ops + top 4 logs above the route list. Each result links to the matching session.

## Per-page state machine

Every page implements the same 4-state UI:

1. **Pending**: shows a Skeleton of the content (not a spinner — keeps the layout stable)
2. **Error**: shows the ErrorState component with the API error message (or a generic "Could not load X" if not an Error)
3. **Empty**: shows a designed empty state with an icon + headline + helper text. Tone: not alarming for "no data" (e.g. /failures), alarming for "no data AND filters active" (clear-filters CTA).
4. **Loaded**: shows the content + summary strip if applicable

## Add a new page (checklist)

To add a new page following the pattern:

1. Add the route in `App.tsx`
2. Add the page title mapping in `AppSidebar.tsx` (`getPageTitle`)
3. Create `src/pages/<Route>/<Route>.tsx` (the page component)
4. Create `src/pages/<Route>/index.ts` (re-export)
5. If a per-page summary strip is needed, follow the StatPill pattern from SessionsList
6. Tests: 1 file covering loading / error / empty / populated / drill-down
7. Add the route to the `COVERAGE_INCLUDE` and `PER_DIR_GLOBS` in `vitest.coverage.mjs` (the verifier enforces lockstep)
8. Screenshot in light + dark via `playwright_headless`
9. Run all gates; run the 5-reviewer cycle; commit
10. Add the page to this catalog under "Page catalog"

## What lives outside the pattern

These pages don't follow the cataloged pattern (documented deviations):

- **`/sessions/:id` — Session Detail** — full-width tabs layout (Overview / Topology / Raw data), not the standard pattern. The chrome (header + tabs) follows SOW-0074.
- **`/topology`** — D3-graph-centric; header is minimal, content is the graph itself.
- **`/stats`** — chart-centric; the "summary strip" is replaced by the chart controls (granularity, group-by, metric).

Pages that grow deviations should justify them in their own SOW.
