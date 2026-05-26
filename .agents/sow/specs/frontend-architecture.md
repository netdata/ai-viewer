# Frontend Architecture

## TL;DR

React + TypeScript + Vite. Single-page app served from Go via `go:embed`. Minimal state: TanStack Query for server state, React Router for routes, URL-synced filters for shareable views. D3 for topology and timeline.

## Stack

| Concern | Choice | Why |
|---|---|---|
| Build | **Vite (current stable)** | fast dev loop, native ESM, simple config |
| Language | **TypeScript (current stable, strict mode)** | type safety, IDE support |
| UI library | **React (current stable)** | ecosystem maturity, hiring familiarity, embedded-binary friendly |
| Routing | **React Router (current stable)** | the standard |
| Server state | **TanStack Query (current stable)** | caching, retry, SSE-aware invalidation |
| Styling | **CSS modules + CSS custom properties for theming** | no runtime CSS-in-JS overhead; theme switch via `:root` vars |
| Charts | **D3 (current stable)** for topology/timeline; native HTML/CSS tables elsewhere | D3 only where its expressive power is needed |
| Tests | **Vitest** unit + RTL component tests + **Playwright** for E2E | matches Vite tooling |
| Lint | **ESLint + typescript-eslint** | zero warnings policy |

## Directory Layout

```
frontend/
├── package.json
├── vite.config.ts
├── tsconfig.json
├── eslint.config.ts
├── index.html
├── src/
│   ├── main.tsx                 # app entry
│   ├── App.tsx                  # router + layout
│   ├── theme/
│   │   └── tokens.css           # color/spacing/font tokens, dark+light
│   ├── api/
│   │   ├── client.ts            # fetch wrapper
│   │   ├── sessions.ts          # session endpoints
│   │   ├── stats.ts
│   │   ├── catalog.ts
│   │   ├── payloads.ts
│   │   ├── sse.ts               # SSE subscription + EventSource wrapper
│   │   └── types.ts             # response types (mirror Go types)
│   ├── state/
│   │   ├── filters.ts           # URL-synced filter store
│   │   └── theme.ts             # theme provider
│   ├── components/              # reusable UI primitives
│   │   ├── Layout/
│   │   ├── FilterBar/
│   │   ├── SessionRow/
│   │   ├── SpanBar/
│   │   ├── StatCard/
│   │   └── ...
│   ├── pages/
│   │   ├── SessionsList/
│   │   ├── SessionDetail/
│   │   │   ├── OverviewTab/
│   │   │   ├── TopologyTab/    # D3 force-directed
│   │   │   ├── TraceTab/       # span tree
│   │   │   ├── TimelineTab/    # D3 time-axis
│   │   │   └── LogsTab/
│   │   ├── Topology/           # cross-session
│   │   ├── Tools/
│   │   ├── Models/
│   │   ├── Agents/
│   │   └── Sources/
│   ├── viz/
│   │   ├── topology.ts          # D3 force-directed renderer
│   │   ├── timeline.ts          # D3 timeline renderer
│   │   └── color.ts             # status/severity color mapping
│   └── lib/
│       ├── format.ts            # ts/duration/bytes/cost formatters
│       └── tree.ts              # session tree helpers
├── public/                      # static assets (logos, favicons)
└── tests/                       # Playwright E2E
```

## State Management

- **Filters** live in the URL (`?from=...&agents=a,b`) via React Router search params. A `useFilters()` hook reads and updates them. No filter state in components.
- **Server data** lives in TanStack Query. Cache key = endpoint + filter hash. SSE events invalidate matching keys.
- **UI state** (open tab, hover state, modal open) is per-component `useState`.
- **No global app state library.** If we ever need it, Zustand goes in; not before.

## SSE Integration

```ts
// api/sse.ts
const sub = await createSubscription(filter);
const es = new EventSource(`/api/events?sub=${sub.id}`);
es.addEventListener('session_changed', (e) => {
  const { session_id } = JSON.parse(e.data);
  queryClient.invalidateQueries({ queryKey: ['session', session_id] });
  queryClient.invalidateQueries({ queryKey: ['sessions'] }); // list refresh
});
es.addEventListener('stats_invalidated', () => {
  queryClient.invalidateQueries({ queryKey: ['stats'] });
});
es.addEventListener('resync', () => queryClient.invalidateQueries());
```

One subscription per active page. Filter changes → new subscription, old one cancelled (server expires it after 60 s of no client anyway).

## Theming

`theme/tokens.css`:

```css
:root {
  --bg-primary: #0d1117;
  --bg-secondary: #161b22;
  --text-primary: #c9d1d9;
  --accent: #58a6ff;
  --success: #3fb950;
  --warning: #d29922;
  --error: #f85149;
  /* … */
}

:root[data-theme="light"] {
  --bg-primary: #ffffff;
  --bg-secondary: #f6f8fa;
  --text-primary: #1f2328;
  --accent: #0969da;
  --success: #1a7f37;
  --warning: #9a6700;
  --error: #cf222e;
}
```

Toggle: write `data-theme` to `<html>`, persist in `localStorage`. Default to `prefers-color-scheme`.

## Performance Budgets

- Initial bundle (gzipped): under 200 KB. D3 is the biggest dependency; tree-shake aggressively.
- First contentful paint: under 1 s on workstation localhost.
- Sessions list: virtualized when > 200 rows (TanStack Virtual).
- Topology: D3 force simulation runs in a Web Worker when > 100 nodes.
- Timeline: Canvas rendering (not SVG) when > 500 spans.

## Accessibility

- Keyboard navigation for all interactive elements.
- ARIA labels on icon-only buttons.
- Focus rings visible in both themes.
- Color is never the only signal (status text + color, not just color).

## Build & Embed

`scripts/build.sh`:

1. `cd frontend && npm ci && npm run build`
2. `rm -rf cmd/ai-viewer-serve/frontend_dist/* && cp -r frontend/dist/. cmd/ai-viewer-serve/frontend_dist/`
3. `go build -o bin/ai-viewer-serve ./cmd/ai-viewer-serve`

The embedded build is the single-binary deploy target.
