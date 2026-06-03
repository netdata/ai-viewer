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

## Coverage thresholds

`vitest.config.ts` enforces a **global** aggregate floor AND a **per-directory ≥ 80% lines** floor for every measured dir under `src/components/` and `src/pages/`, via Vitest's **native glob-keyed `coverage.thresholds`** (SOW-0012 Chunk C — no wrapper script). The same `npm run test -- --run --coverage` command applies both; a single under-covered dir fails the run with `ERROR: Coverage for lines (NN%) does not meet "<glob>" threshold (80%)`.

- **A new implemented + tested component/page dir must be added to THREE places in `vitest.config.ts`:** `coverage.include` (so it is measured), `PER_DIR_GLOBS` (so it gets its own per-dir floor), and — implicitly — it is then covered by the global floor too. The `include` list and `PER_DIR_GLOBS` must stay in lockstep.
- **Why a per-dir key without `include` is a silent no-op:** an unmatched glob group's lines pct is `"Unknown"`, and `"Unknown" < 80` is `false` in JS — an empty group **vacuously passes**. So placeholder/stub dirs (`ComingSoon`, `Layout`, `StatCard`, `Agents`, `Models`, `Tools`, `NotFound`) are excluded from `include` and deliberately carry NO per-dir key; adding one would falsely imply enforcement.
- A shared `PER_DIR_LINES` constant ties the global floor and every per-dir group together so they cannot silently diverge. Never lower it to make a dir pass — a dir under the floor is a finding to close with tests.
- The gate's WIRING is itself self-tested: `scripts/check-coverage-thresholds.test.sh` (`npm run check:coverage-thresholds:selftest`) runs Vitest on a throwaway 50%-lines fixture dir and proves the per-dir threshold fails closed under the floor / passes above it. Reporters: `text`, `text-summary`, `json` (emits `coverage/coverage-final.json`), `html` (the CI-uploaded report).

## Lint

ESLint flat config (`eslint.config.ts`) with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, and `eslint-plugin-import` (+ `eslint-import-resolver-typescript`). Zero-warnings policy enforced in CI (`eslint . --max-warnings 0`).

- **Config builder**: ESLint core's `defineConfig()` + `globalIgnores()` from `eslint/config`, NOT `tseslint.config()` (typescript-eslint's helper is `@deprecated` in favour of core `defineConfig()`). `tseslint` is kept only for its parser/plugin + `recommendedTypeChecked` preset.
- **jsx-a11y / import flat-config**: both ship native flat-config support at the pinned versions (`jsxA11y.flatConfigs.recommended`, `importPlugin.flatConfigs.recommended` + `.typescript`) — NO `FlatCompat` bridge. The import/typescript preset + the `import/resolver.typescript` setting teach `import/no-unresolved` to follow `.ts`/`.tsx`/extensionless and type-only specifiers. import/recommended's three `'warn'` rules (`no-named-as-default`, `no-named-as-default-member`, `no-duplicates`) are promoted to `'error'` (a warning fails the zero-warnings gate anyway; explicit is clearer).
- **Scrollable regions**: `jsx-a11y/no-noninteractive-tabindex` is configured with `roles: ['tabpanel', 'region']` so a `tabIndex={0}` on a `role="region"` scroll container (the WAI-ARIA APG scrollable-region pattern — lets keyboard users focus + arrow-scroll an overflow area) is allowed. This is a deliberate rule config, not a disable.
- **Untyped-plugin typing**: `eslint-plugin-jsx-a11y` ships no `.d.ts`, so an ambient `declare module` shim lives at `src/types/eslint-plugin-jsx-a11y.d.ts` (typed via `eslint/config`'s `Config`). `eslint-plugin-react-hooks` 7.x's `configs` field is not assignable to core's strict `Plugin` type, so it is registered with a narrow `as Plugins[string]` cast (`Plugins = NonNullable<Config['plugins']>`), never `any`. The config file's own untyped-plugin access (`importPlugin.flatConfigs`/`jsxA11y.flatConfigs` are `any`) is relaxed for `eslint.config.ts` ONLY via a trailing scoped block turning off `@typescript-eslint/no-unsafe-{argument,member-access,assignment}` + `import/no-named-as-default-member`; every app-source file keeps full coverage.
- **Per-line disables** (each with an inline rationale; never global): Vite `?worker` virtual-module imports trip `import/default` (resolver sees the suffix-stripped `.ts` with no synthesized default); the WAI-ARIA tabs container trips `interactive-supports-focus` (roving tabindex puts focus on the tab buttons); a modal backdrop click + a `role="dialog"` keydown trip the static/noninteractive-interaction rules (full keyboard path exists elsewhere); the `<canvas>` viz wrappers (timeline + waterfall) trip `click-events-have-key-events` — the timeline has a focusable DOM-fallback span list, the **waterfall Canvas mode does NOT** (a tracked keyboard-access gap, flagged for the `src/viz/<chart>/a11y.md` waiver work, not silently accepted).

## Bundle Size

`vite.config.ts` sets `build.manifest: true` so Vite emits `dist/.vite/manifest.json`. The bundle-size gate (`scripts/check-bundle-size.js`, SOW-0012) classifies chunks from that manifest's `ManifestChunk` flags rather than from filenames: `isEntry` ⇒ main chunk (≤ 500 KB gz), `isDynamicEntry` ⇒ per-route lazy chunk (≤ 200 KB gz each). Keep `build.manifest` enabled — the gate fails closed without the manifest. A `?worker` bundle (e.g. `viz/forceWorker.ts` imported as `?worker`) is emitted as its own chunk that is neither `isEntry` nor `isDynamicEntry`; it is reported but not gated. When you add a route split, do it via `React.lazy` + dynamic `import()` so the split chunk is marked `isDynamicEntry` and falls under the 200 KB lazy budget.
