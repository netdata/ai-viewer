---
name: project-frontend
description: Apply React/TypeScript/Vite patterns used in ai-viewer's frontend. Use when editing frontend/ — pages, components, API client, SSE handling, D3 visualizations.
---

# Frontend Patterns

## Stack Versions

Always the latest stable: React, TypeScript, Vite, TanStack Query, React Router, D3, Vitest, Playwright. Update on a rolling basis; major-version upgrades get a brief SOW.

## File Conventions

- One component per file. File name matches the default export (e.g. `SessionRow.tsx` exports `SessionRow`).
- No default exports outside of `pages/*` (which React Router prefers default). Components and helpers use named exports.
- Co-locate `Component.tsx` + `Component.module.css` + `Component.test.tsx`.
- One folder per page under `src/pages/<PageName>/`.

## TypeScript

- `strict: true`.
- `noUncheckedIndexedAccess: true`.
- `exactOptionalPropertyTypes: true`.
- No `any`. Use `unknown` and narrow.
- API response types live in `src/api/types.ts` and mirror the Go types.

## Hooks Patterns

```ts
// data fetching
export function useSession(id: string) {
  return useQuery({
    queryKey: ['session', id],
    queryFn: () => api.getSession(id),
    staleTime: 1000,
  });
}

// URL-synced filters
export function useFilters(): [Filters, (f: Partial<Filters>) => void] {
  const [params, setParams] = useSearchParams();
  const filters = parseFilters(params);
  const update = (patch: Partial<Filters>) => setParams(serializeFilters({ ...filters, ...patch }));
  return [filters, update];
}
```

- Custom hooks live next to the page/component that introduces them. Cross-cutting hooks live in `src/hooks/`.
- One hook per file.
- Always return tuples or named-object results, never anonymous arrays beyond two elements.

## SSE Handling

One subscription per page. Use a custom hook that handles create/cancel/reconnect:

```ts
useEffect(() => {
  let cancelled = false;
  let es: EventSource | null = null;
  let subId: string | null = null;

  (async () => {
    const sub = await api.createSubscription(filters);
    if (cancelled) { await api.cancelSubscription(sub.id).catch(() => {}); return; }
    subId = sub.id;
    es = new EventSource(`/api/events?sub=${sub.id}`);
    es.addEventListener('session_changed', (e) => { ... });
    es.addEventListener('resync', () => queryClient.invalidateQueries());
  })();

  return () => {
    cancelled = true;
    es?.close();
    if (subId) api.cancelSubscription(subId).catch(() => {});
  };
}, [filterHash(filters)]);
```

## Styling

CSS Modules + CSS custom properties (no Tailwind, no CSS-in-JS).

```css
/* SessionRow.module.css */
.row { background: var(--bg-secondary); }
.row.failed { color: var(--error); }
```

Theme tokens live in `src/theme/tokens.css` and are loaded once in `main.tsx`.

## D3 Patterns

D3 only inside `src/viz/`. Components consume `viz/` functions, never import `d3` directly:

```ts
// viz/topology.ts
export function renderTopology(svg: SVGSVGElement, graph: TopologyData, opts: TopologyOpts) {
  // d3 force simulation here, cleanup function returned
  return () => simulation.stop();
}

// pages/SessionDetail/TopologyTab.tsx
useEffect(() => {
  if (!svgRef.current || !data) return;
  const cleanup = renderTopology(svgRef.current, data, opts);
  return cleanup;
}, [data, opts]);
```

This boundary keeps D3 isolated and testable.

## State Management Rules

- Filters: URL only.
- Server data: TanStack Query.
- UI: `useState`/`useReducer` per component.
- No Redux, no Zustand, no MobX. If we ever need cross-page state beyond URL + server cache, that's a SOW.

## Performance

- `React.memo` only on expensive list children (after profiling).
- `useMemo` for derived heavy computations only.
- Virtualize lists > 200 rows with `@tanstack/react-virtual`.
- Lazy-load D3 page components (`React.lazy` + `Suspense`).

## Testing

- Every page has a render test (`pages/<Page>/<Page>.test.tsx`) against mocked API responses.
- Component tests use React Testing Library queries (`getByRole`, `getByText`), never test-id selectors as the primary mechanism.
- E2E tests live under `frontend/tests/` and use Playwright. One scenario per primary user flow.

## Lint

ESLint flat config with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`. Zero warnings policy enforced in CI.

## Bundle Size

`vite.config.ts` sets `build.manifest: true` so Vite emits `dist/.vite/manifest.json`. The bundle-size gate (`scripts/check-bundle-size.js`, SOW-0012) classifies chunks from that manifest's `ManifestChunk` flags rather than from filenames: `isEntry` ⇒ main chunk (≤ 500 KB gz), `isDynamicEntry` ⇒ per-route lazy chunk (≤ 200 KB gz each). Keep `build.manifest` enabled — the gate fails closed without the manifest. A `?worker` bundle (e.g. `viz/forceWorker.ts` imported as `?worker`) is emitted as its own chunk that is neither `isEntry` nor `isDynamicEntry`; it is reported but not gated. When you add a route split, do it via `React.lazy` + dynamic `import()` so the split chunk is marked `isDynamicEntry` and falls under the 200 KB lazy budget.
