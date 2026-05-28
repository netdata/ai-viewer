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

**Cancellation / teardown contract.** `connectSse(queryClient, filter, handlers?, signal?)` must leave NO leaked EventSource or undeleted server subscription under any unmount/filter-change/StrictMode-double-invoke timing. Because subscription creation is async (a POST that resolves before the EventSource opens), the wrapper: rejects with an `SseCanceledError` sentinel if the `AbortSignal` fires during the POST; after the POST resolves, re-checks `signal.aborted` and, if set, `close()`s the just-created connection (best-effort `DELETE /api/subscriptions/:id`) before returning; and registers a one-shot `abort` listener so an abort arriving after `open()` still tears the stream down. `close()` is idempotent. The SSE client is unit-tested with a fake `EventSource` (all five frames → the correct `invalidateQueries` keys, plus every cancellation timing) and is INCLUDED in the coverage gate — it is load-bearing and must not be excluded.

**Malformed frames.** A frame whose `data` is not valid JSON is never silently dropped (AGENTS.md §"No silent failures"): it is routed to an optional `onMalformedEvent` handler, else `console.warn`ed with the event name; the stream stays alive (a single bad frame does not kill the connection).

**API client empty-body.** `api/client.ts` supports `HEAD` and treats any bodiless success (`HEAD`, `204`, or `Content-Length: 0`) as `undefined` rather than attempting a JSON parse, so HEAD parity (`/api/health`, `/api/sources`, `/api/events`) and the `204` from subscription `DELETE` are handled without throwing.

## Theming

**Operator decision (2026-05-26): theme matches the operating system by default; a manual override is available and persisted.**

Dark and light are first-class equals — both are polished, neither is the "real" one with the other as an afterthought.

### Token file

`theme/tokens.css` defines CSS custom properties under two selectors. Dark is the `:root` default; light overrides via `[data-theme="light"]`. The single `data-theme` attribute on `<html>` is the switch.

```css
:root {
  /* DARK (default) */
  --bg-primary: #0d1117;
  --bg-secondary: #161b22;
  --bg-tertiary: #21262d;
  --border: #30363d;
  --text-primary: #c9d1d9;
  --text-secondary: #8b949e;
  --accent: #58a6ff;
  --success: #3fb950;
  --warning: #d29922;
  --error: #f85149;
  --info: #a5a5ff;
  /* … */
}

:root[data-theme="light"] {
  --bg-primary: #ffffff;
  --bg-secondary: #f6f8fa;
  --bg-tertiary: #eaeef2;
  --border: #d0d7de;
  --text-primary: #1f2328;
  --text-secondary: #59636e;
  --accent: #0969da;
  --success: #1a7f37;
  --warning: #9a6700;
  --error: #cf222e;
  --info: #5a5aff;
}
```

### Theme resolution algorithm

The theme provider (`state/theme.ts`) resolves the active theme on every render using this precedence:

1. **Manual override**: if `localStorage.aiViewerTheme === 'dark' | 'light'` is set, that value is the active theme.
2. **OS preference**: otherwise, `window.matchMedia('(prefers-color-scheme: dark)')` decides — `dark` if matches, `light` otherwise.

The `data-theme` attribute on `<html>` is updated by the provider on:

- Initial mount (sets the resolved value).
- `localStorage` write (manual override changes).
- `matchMedia('(prefers-color-scheme: dark)').addEventListener('change', ...)` — OS changes its preference (user enables Night Shift, system dark-mode toggle flips, etc.). Only re-applies when no manual override is set; if the operator has explicitly chosen a theme, it stays.

### User control

The header includes a three-state theme control:

- **Auto** (default) — follows OS preference. No `localStorage` entry. Auto-switches when the OS preference changes.
- **Dark** — `localStorage.aiViewerTheme = 'dark'`. Locks dark regardless of OS.
- **Light** — `localStorage.aiViewerTheme = 'light'`. Locks light regardless of OS.

A "Reset to auto" affordance clears the override. The current state is announced via the toggle's `aria-label`.

### Server-side / SSR considerations

ai-viewer renders client-side only — the Go binary serves a static HTML shell with the React app. To avoid a flash-of-wrong-theme on first paint, the shell `index.html` includes a tiny inline script that runs before React mounts:

```html
<script>
(function () {
  try {
    var pref = localStorage.getItem('aiViewerTheme');
    var resolved = pref || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    document.documentElement.setAttribute('data-theme', resolved);
  } catch (e) { /* localStorage disabled */ }
})();
</script>
```

This inline script is the **only** JS that runs synchronously before React; everything else loads as a module.

### Tests

- Vitest unit test on the resolution algorithm: every combination of `localStorage` value × `matchMedia` value → expected active theme.
- Playwright E2E with axe: visits each route under both `dark` and `light` (forced via `localStorage`), asserts zero serious/critical a11y violations.
- Playwright E2E: changes the manual override; asserts the `data-theme` attribute updates and the chosen color tokens flow through (sampling computed style on a key element per theme).
- Playwright E2E: simulates OS preference change while no override is set; asserts the theme auto-switches.

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
