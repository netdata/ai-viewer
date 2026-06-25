# Frontend Architecture

## TL;DR

React + TypeScript + Vite. Single-page app served from Go via `go:embed`. Minimal state: TanStack Query for server state, React Router for routes, URL-synced filters for shareable views. D3 for topology and timeline.

## Stack

| Concern | Choice | Why |
|---|---|---|
| Build | **Vite 8.x (current stable)** | fast dev loop, native ESM, simple config |
| Language | **TypeScript 6.x (current stable, strict mode)** | type safety, IDE support |
| UI library | **React 19.x (current stable)** | ecosystem maturity, hiring familiarity, embedded-binary friendly |
| Routing | **React Router 7.x (current stable)** | the standard |
| Server state | **TanStack Query 5.x (current stable)** | caching, retry, SSE-aware invalidation |
| Styling | **Tailwind CSS 4.x + shadcn/ui primitives + Radix Primitives** | utility-first + accessible primitives + Tailwind v4 runtime-token bridge for instant theme flips |
| CSS Modules | **Backwards-compat only** | legacy CSS Modules continue to work via the legacy-token shim in `src/theme/app.css`; new code is Tailwind utilities |
| Charts | **D3 (current stable)** for topology/timeline (legacy) + **@visx/visx 4.x** for the redesigned Stats line + bar charts (planned SOW-0076) | D3 where its expressive power is needed; visx for declarative charts |
| Tables | **@tanstack/react-table 8.x** for the redesigned sessions/agents/tools tables (headless) | headless = full visual control |
| Animations | **motion 12.x (formerly framer-motion)** | tiny footprint, hybrid WAAPI + JS engine |
| Icons | **lucide-react 1.x** | tree-shakable, ISC, 1000+ icons, shadcn-default |
| Fonts | **Geist Sans + Geist Mono** self-hosted via **@fontsource/* 5.x** | SIL OFL; no Google Fonts dependency; modern + legible |
| Tests | **Vitest 4.x** unit + RTL component tests + **Playwright 1.x** for E2E | matches Vite tooling |
| Lint | **ESLint 9.x + typescript-eslint 8.x** | zero warnings policy |

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
│   │   ├── tokens.css           # color/spacing/font tokens, dark+light
│   │   └── global.css           # base/reset styles
│   ├── api/                     # one module per endpoint + shared client
│   │   ├── client.ts            # fetch wrapper (API_BASE, error envelope)
│   │   ├── queryClient.ts       # React Query client
│   │   ├── sessions.ts          # /api/sessions + /api/sessions/:id + /api/sessions/compare
│   │   ├── logs.ts              # /api/sessions/:id/logs
│   │   ├── sources.ts           # /api/sources
│   │   ├── stats.ts             # /api/stats
│   │   ├── catalog.ts           # catalog client stub (Phase-2 routes)
│   │   ├── payloads.ts          # typed /api/payloads/:id GET/HEAD client
│   │   ├── sse.ts               # SSE subscription + EventSource wrapper
│   │   └── types.ts             # response types (mirror Go DTOs)
│   ├── state/
│   │   ├── filters.ts           # URL-synced filter store
│   │   ├── theme.ts             # theme provider (auto/dark/light)
│   │   └── useLiveUpdates.ts    # SSE-driven query invalidation
│   ├── components/              # reusable UI primitives
│   │   ├── Layout/
│   │   ├── FilterBar/
│   │   ├── SessionRow/
│   │   ├── StatCard/
│   │   ├── StatusViews/         # loading / empty / error states
│   │   ├── Tabs/
│   │   ├── LogRow/
│   │   ├── LoadMore/
│   │   ├── ComingSoon/          # placeholder panel — now only backs the
│   │   │                        #   still-future /tools, /models, /agents
│   │   └── ThemeToggle/
│   ├── pages/
│   │   ├── SessionsList/
│   │   ├── SessionDetail/       # OverviewTab/ + TraceTab/ + TopologyTab/ +
│   │   │   ├── OverviewTab/     #   TimelineTab/ + LogsTab/ — Trace/Topology/
│   │   │   └── LogsTab/         #   Timeline tabs SHIPPED (SOW-0006)
│   │   ├── Sources/
│   │   ├── NotFound.tsx
│   │   ├── Topology/            # cross-session actor graph — SHIPPED (SOW-0006)
│   │   ├── Tools/               # Phase 3 (ComingSoon)
│   │   ├── Models/              # Phase 3 (ComingSoon)
│   │   └── Agents/              # Phase 3 (ComingSoon)
│   ├── lib/
│   │   ├── format.ts            # ts/duration/bytes/cost formatters
│   │   └── tree.ts              # session tree helpers
│   ├── test/                    # vitest setup (setup.ts, matchMedia.ts)
│   └── viz/                     # D3 renderers — populated (SOW-0006): trace/
│       │                        #   topology/timeline/color/spanFade/
│       │                        #   zoomInteraction/forceWorker
├── public/                      # static assets (favicon)
└── tests/                       # Playwright E2E specs
```

## State Management

- **Filters** live in the URL (`?from=...&agents=a,b`) via React Router search params. A `useFilters()` hook reads and updates them. No filter state in components.
- **Server data** lives in TanStack Query. Cache key = endpoint + filter hash. SSE events invalidate matching keys.
- **UI state** (open tab, hover state, modal open) is per-component `useState`.
- **No global app state library.** If we ever need it, Zustand goes in; not before.

### Same-Origin API Links

Frontend links to ai-viewer's own API endpoints use same-origin relative paths
(`/api/...`), never a hardcoded loopback host or port. This keeps installed,
developer, and Playwright E2E binds equivalent: a UI served from an alternate
port still opens the matching server's `/api/health`, `/api/sources`, and other
internal endpoints instead of silently crossing to another local ai-viewer
instance.

### URL Filter Contract

`state/filters.ts` is the single frontend boundary for translating browser
query parameters into component-facing filters and SSE subscription filters.
React Router's `useSearchParams()` is the only route-state primitive used for
this surface; components must not mirror filters in local state.

Filter dimensions:

- Array dimensions are exactly `agents`, `models`, `tools`, `status`, and
  `sources`, defined by `ARRAY_FILTER_KEYS` and serialized as one comma-joined
  query parameter per dimension.
- `from` and `to` are strict safe-integer UNIX microsecond bounds. Invalid,
  fractional, trailing-garbage, empty, or unsafe-integer values are treated as
  absent rather than coerced.
- `q` is free-text agent-name search. Whitespace-only `q` is treated as absent;
  non-empty text is preserved as provided.

Mutation rules:

- `applyPatch(current, patch)` copies the current `URLSearchParams`, writes only
  keys present in `patch`, and preserves unrelated query parameters.
- Empty arrays delete their array query parameter. Explicit `undefined` scalar
  patches delete `from`, `to`, or `q`. Fields absent from `patch` are left
  unchanged.
- `clearFilters()` deletes all filter keys and preserves non-filter query
  parameters.

SSE subscription mapping:

- `filtersToSubscription(filters)` emits only non-empty structured dimensions.
  It never sends present-but-empty arrays.
- `time_range` is emitted only when at least one of `from` or `to` is set, and
  contains only the bounds that are present.
- `q` is deliberately dropped because the SSE subscription contract has no
  free-text filter; list refetches still apply `q`.
- `cwd`, `provider_alias`, `call_path`, and `error_class` are not subscription
  filter dimensions. The frontend must not send them to
  `POST /api/subscriptions`.

### Include Tokens

`api/client.ts` owns the shared include-token builder for REST endpoints that
accept `?include=`. Endpoint clients pass an allowlisted token set and receive a
stable query string:

- empty/missing token arrays omit the `include` parameter;
- duplicates are collapsed;
- output order is deterministic;
- current tokens are `payload_refs`, `proof`, and `cursors`;
- endpoint clients never hand-concatenate include strings.

Session detail and trace use `payload_refs` and `proof`; the dedicated
payload-ref endpoint accepts `proof` and treats `payload_refs` as a no-op
compatibility token; sources use `cursors`.

## SSE Integration

```ts
// api/sse.ts
const sub = await createSubscription(filter);
const es = new EventSource(`/api/events?sub=${sub.id}`);
es.addEventListener('session_changed', (e) => {
  const { session_id } = JSON.parse(e.data);
  queryClient.invalidateQueries({ queryKey: ['session', session_id] });
  queryClient.invalidateQueries({ queryKey: ['sessions'] }); // list refresh
  // Logs belong to the session: a log write marks the session dirty, so the
  // open Logs tab must refresh too. The key family is ['logs', id, severities];
  // a partial-match on ['logs', session_id] invalidates every severity sub-key.
  queryClient.invalidateQueries({ queryKey: ['logs', session_id] });
  // The Trace/Timeline/Topology tabs read per-session viz endpoints; invalidate
  // their keys so the open tab live-refreshes (SOW-0006 AC#6).
  queryClient.invalidateQueries({ queryKey: ['session-timeline', session_id] });
  queryClient.invalidateQueries({ queryKey: ['session-topology', session_id] });
  queryClient.invalidateQueries({ queryKey: ['topology'] }); // cross-session graph
});
es.addEventListener('stats_invalidated', () => {
  queryClient.invalidateQueries({ queryKey: ['stats'] });
});
es.addEventListener('resync', () => queryClient.invalidateQueries());
```

One subscription per active page. Filter changes → new subscription, old one cancelled (server expires it after 60 s of no client anyway).

**Cancellation / teardown contract.** `connectSse(queryClient, filter, handlers?, signal?)` must leave NO leaked EventSource or undeleted server subscription under any unmount/filter-change/StrictMode-double-invoke timing. Because subscription creation is async (a POST that resolves before the EventSource opens), the wrapper: rejects with an `SseCanceledError` sentinel if the `AbortSignal` fires during the POST; after the POST resolves, re-checks `signal.aborted` and, if set, `close()`s the just-created connection (best-effort `DELETE /api/subscriptions/:id`) before returning; and registers a one-shot `abort` listener so an abort arriving after `open()` still tears the stream down. `close()` is idempotent. The SSE client is unit-tested with a fake `EventSource` (all five frames → the correct `invalidateQueries` keys — including `session_changed` invalidating the `['logs', id]` family alongside `['session', id]` and `['sessions']` — plus every cancellation timing) and is INCLUDED in the coverage gate — it is load-bearing and must not be excluded.

**Malformed frames.** A frame whose `data` is not valid JSON is never silently dropped (AGENTS.md §"No silent failures"): it is routed to an optional `onMalformedEvent` handler, else `console.warn`ed with the event name; the stream stays alive (a single bad frame does not kill the connection).

**API client empty-body.** `api/client.ts` supports `HEAD` and treats any bodiless success (`HEAD`, `204`, or `Content-Length: 0`) as `undefined` rather than attempting a JSON parse, so HEAD parity (`/api/health`, `/api/sources`, `/api/events`) and the `204` from subscription `DELETE` are handled without throwing.

**Payload byte-streaming client.** `api/payloads.ts` is the typed client for
`GET` and `HEAD /api/payloads/:id`. It returns text plus payload metadata from
response headers (`X-Payload-Format`, truncation state, total bytes, preview
bytes) and preserves caller-owned abort behavior. `TurnView/payloadStore.ts`
keeps the per-id cache and concurrency limit; `SpanDetailDrawer` consumes the
same client rather than owning separate fetch/header parsing logic.

**`useLiveUpdates(filter)` — the per-view connection lifecycle hook.** Pages do
not call `connectSse` directly; they call `useLiveUpdates(filter)`
(`state/useLiveUpdates.ts`). The hook owns the connection lifecycle in a
`useEffect` keyed on a stable serialization of the subscription `filter`:

- On mount / filter change it creates an `AbortController`, calls
  `connectSse(queryClient, filter, {}, controller.signal)`, and stores the
  resolved `SseConnection`.
- The effect cleanup `abort()`s the controller AND `close()`s any connection
  already resolved. Aborting drives the Chunk-14 cancellation contract: an
  abort during the in-flight POST rejects with `SseCanceledError` (swallowed by
  the hook — cancellation is not an error), and an abort after `open()` tears
  the stream down via the one-shot abort listener. `close()` is idempotent, so
  the belt-and-suspenders close is safe.
- A `connectSse` rejection that is NOT `SseCanceledError` is surfaced via
  `console.warn` (no silent failure) and does not throw out of the effect.
- Exactly ONE active subscription exists per mounted view at any time;
  re-subscription on filter change is automatic because the effect re-runs when
  its serialized-filter dependency changes. StrictMode's double-invoke is
  covered by the same abort-safe teardown.

The hook returns nothing observable to the page (the SSE client already maps
events → `invalidateQueries`); its sole job is lifecycle management. It is
unit-tested by mocking `connectSse` and asserting: one call on mount, the
controller aborted + `close()` called on unmount, and a re-subscription (new
`connectSse` call with the new filter) on a filter change.

**`useSessionLogs(id, opts)` — keyset log pagination.** `api/logs.ts` adds
`fetchSessionLogs(id, { severities?, cursor?, limit? })` hitting
`GET /api/sessions/:id/logs?severity=...&cursor=...&limit=...` and a
`useSessionLogs(id, { severities? })` hook built on TanStack
`useInfiniteQuery`. Query key: `['logs', id, severities]` (the severity set is
part of the key because the server binds the cursor to the session id + the
severity set — `rest-api.md` §Conventions). `getNextPageParam` returns the
response `next_cursor` (or `undefined` to stop). The present-but-empty severity
rule is honored: an empty `severities` array omits the `severity` param
entirely (= all severities), so the client never sends the `?severity=`
`BAD_REQUEST` form. `id` and `severities` are passed straight through; pages
flatten `data.pages[].items` for rendering.

## Theming

**Operator decision (2026-05-26): theme matches the operating system by default; a manual override is available and persisted.**

**Updated 2026-06-19 (SOW-0073):** the theme file is now `theme/app.css` (single file, replaces the old `tokens.css` + `global.css` split). It defines semantic CSS custom properties (shadcn-style: `--background`, `--foreground`, `--primary`, `--secondary`, `--muted`, `--accent`, `--destructive`, `--border`, `--input`, `--ring`, `--card`, `--popover`, `--chart-1..5`, `--status-*`, `--source-*`) under two selectors: `:root` for dark (the default), `:root[data-theme="light"]` for light. Tailwind v4's `@theme inline` block bridges the CSS variables to Tailwind utilities (`bg-background`, `text-foreground`, `border-border`, etc.) so the theme switch is instant with zero JS. Dark and light are first-class equals — both are designed, not hue-inversions of each other.

```css
:root {
  /* DARK (default) — OKLCH-based dashboard palette */
  --background: oklch(0.145 0 0);
  --foreground: oklch(0.985 0 0);
  --card: oklch(0.18 0 0);
  --card-foreground: oklch(0.985 0 0);
  --primary: oklch(0.7 0.15 250);
  --primary-foreground: oklch(0.145 0 0);
  --muted: oklch(0.22 0 0);
  --muted-foreground: oklch(0.65 0 0);
  --border: oklch(1 0 0 / 12%);
  --ring: oklch(0.7 0.15 250);
  --status-completed: oklch(0.72 0.17 145);
  --status-running:   oklch(0.78 0.16 75);
  --status-failed:    oklch(0.65 0.22 25);
  --status-abandoned: oklch(0.55 0.01 250);
  --status-interrupted: oklch(0.7 0.18 50);
  /* … */
}

:root[data-theme="light"] {
  /* LIGHT — inverted L* on the same hue families */
  --background: oklch(1 0 0);
  --foreground: oklch(0.145 0 0);
  /* … */
}
```

### Typography

Geist Sans (variable weight) for UI text; Geist Mono for tabular / code content. Self-hosted via `@fontsource/geist-sans` + `@fontsource/geist-mono` (SIL OFL; no Google Fonts CDN). Type scale (Tailwind defaults are the foundation, extended via `--font-sans` / `--font-mono` tokens):

- `text-xs` — 0.75 rem, uppercase tracking-wider for table column headers
- `text-sm` — 0.875 rem, default body
- `text-base` — 1 rem, page titles / hero numbers
- `text-lg` — 1.125 rem, page hero (Sessions, Topology, etc.)
- `text-2xl` — 1.5 rem, oversized hero
- Numeric columns use `font-variant-numeric: tabular-nums` so digits align.
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
- Timeline: Canvas rendering (not SVG) when > 500 visible spans OR when the lane stack is taller than the Canvas viewport (`lanes × laneHeight > viewport`) — so a high-lane-count / tall timeline uses the bounded, viewport-culled Canvas path instead of a full-height SVG. The Canvas backing store is bounded to the viewport (never the full lane-stack height, which could exceed the browser ~32767px canvas limit), and lanes are virtualized by native vertical scroll (`scrollTop`); X time-zoom (d3-zoom) and native Y lane-scroll coexist (SOW-0006).

### Web Worker for D3 force simulation

Above the 100-node threshold the topology view runs `d3-force` off the main thread
so the layout's O(n²)-per-tick math never janks scrolling or the React tree. Vite
has first-class worker support, so no bundler config is needed — the worker is a
normal module imported with the `?worker` query suffix:

```ts
// viz/forceWorker.ts — runs in the worker context (no DOM, no React).
import { forceSimulation, forceManyBody, forceLink, forceCenter } from "d3-force";
self.onmessage = (e: MessageEvent<{ nodes: Node[]; edges: Edge[] }>) => {
  const { nodes, edges } = e.data;
  const sim = forceSimulation(nodes)
    .force("charge", forceManyBody().theta(0.9))   // quad-tree approximation
    .force("link", forceLink(edges).id((d) => d.id))
    .force("center", forceCenter())
    .stop();
  const ticks = Math.min(300, Math.ceil(Math.log(nodes.length + 1) * 60));
  for (let i = 0; i < ticks; i++) sim.tick();      // run to convergence off-thread
  (self as unknown as Worker).postMessage({ nodes }); // post settled positions back
};
```

```ts
// In the renderer (main thread):
import ForceWorker from "../viz/forceWorker?worker"; // Vite worker-import idiom
const worker = new ForceWorker();
worker.onmessage = (e) => applyPositions(e.data.nodes);
worker.postMessage({ nodes, edges });
// terminate on unmount so a navigated-away view leaks no worker:
//   useEffect(() => () => worker.terminate(), []);
```

Rules:

- **Suffix matters.** `import X from "./w?worker"` gives a constructable worker
  class; a plain import would just run the module on the main thread. Use
  `?worker&inline` only if a future deploy must avoid a separate worker chunk
  (it base64-inlines the worker, growing the main bundle — measure against the
  gzip budget first).
- **The worker is DOM-free and React-free.** It receives plain
  `{nodes, edges}` data, runs the simulation to convergence with a capped tick
  count (the convergence check from Acceptance Criterion 2 — "last 5 ticks move
  every node < 1 px" — runs here), and posts back settled `{x, y}` coordinates.
  All rendering (SVG/Canvas) stays on the main thread.
- **Below 100 nodes, skip the worker.** The simulation is cheap enough to run
  inline on a `requestAnimationFrame` loop; spinning up a worker for a 10-node
  graph is pure overhead.
- **Always `terminate()` on unmount** (effect cleanup) so navigating away from a
  topology view does not leak a running worker.
- D3 in the worker is still confined to `viz/` per the D3-boundary rule above; the
  worker module lives under `viz/` like every other D3 consumer.

## Accessibility

- Keyboard navigation for all interactive elements.
- ARIA labels on icon-only buttons.
- Focus rings visible in both themes.
- Color is never the only signal (status text + color, not just color).
- **Scrollable table wrappers are focusable named regions.** Every horizontally
  scrollable container (the `.tableWrap` `overflow-x:auto` wrappers on the
  sessions list, sources table, and logs table) carries `tabIndex={0}` +
  `role="region"` + an `aria-label`, so a keyboard-only user can scroll it and a
  screen reader announces it. Without `tabIndex`, axe's `scrollable-region-focusable`
  rule (serious) fails the moment the content overflows the viewport — which is
  viewport- and data-dependent, so it must be fixed structurally, not avoided by
  a wide test viewport. (Surfaced by the Chunk-18 E2E axe gate on `/sources`.)

## Build & Embed

`scripts/build.sh`:

1. `cd frontend && npm ci && npm run build`
2. `rm -rf cmd/ai-viewer-serve/frontend_dist/* && cp -r frontend/dist/. cmd/ai-viewer-serve/frontend_dist/`
3. `go build -o bin/ai-viewer-serve ./cmd/ai-viewer-serve`

The embedded build is the single-binary deploy target.

## Compare page contract (SOW-0095)

- **Route**: `GET /compare?ids=<csv, 2-4 ids>` registered in `App.tsx`.
- **Hook**: `useCompareSessions(ids: string[])` lives in `frontend/src/api/sessions.ts`
  next to `useSessionDetail`. Contract:
  - `enabled: ids.length >= 2 && ids.length <= 4` (1-id or 5+ ids → no fetch; the
    page renders the empty / error state directly).
  - `queryKey: ['compare', ids.join(',')]` (cache by full id set, ordered).
  - `queryFn`: `GET /api/sessions/compare?ids=<csv>`.
  - Returns `{ data: CompareResponse | undefined; isLoading: boolean; error: Error | null }`.
- **Types**: `CompareResponse`, `CompareSummary`, `CompareToolBucket`, `CompareErrorRef`,
  `CompareModelBucket`, `CompareAgentBucket`, `CompareKindDistribution` are added to
  `frontend/src/api/types.ts` mirroring the Go DTOs in `internal/presenter/compare.go`.
- **Entry point**: a "Compare" button on `SessionRow` and on the `SessionDetail`
  header navigates to `/compare?ids=<currentId>`. The compare page itself prompts
  for the remaining ids when only one is present (a multi-select over the
  recent-sessions query).
