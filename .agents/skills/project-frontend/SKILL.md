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
- E2E tests live under `frontend/tests/` and use Playwright. One scenario per primary user flow. They run against the EMBEDDED SPA served by the built `ai-viewer-serve` binary on a deterministically seeded temp DB (`scripts/e2e-serve.sh`), never `vite preview`. Session ids / agent names are derived at runtime from `/api/sessions` (never hardcoded) so specs track the seed.

### E2E scenarios + scripts (SOW-0012)

- The five core flows and their specs: **sessions-list filter** (`tests/sessions-filter.spec.ts`), **session-detail load** (`tests/deep-link.spec.ts`), **sources panel** (`tests/routes.spec.ts`), **deterministic SSE update** (`tests/sse-update.spec.ts`), **theme toggle OS+override** (`tests/theme.spec.ts`).
- **Deterministic SSE pattern (the server is read-only — no writer):** to drive a live-update assertion deterministically, install a fake `EventSource` via `page.addInitScript` BEFORE app scripts run (it records each instance on `window.__sse` and exposes `dispatchFrame(name, { data })`), wait on `window.__sse` for the `/api/events` stream to open, then dispatch a controlled `session_changed` frame and assert the documented invalidation (`['sessions']` → a fresh `GET /api/sessions`). This exercises the real `SseConnection` listener + TanStack-Query invalidation end-to-end with zero timing-luck. `tests/realtime.spec.ts` / `tests/viz-sse.spec.ts` separately assert the REAL stream opens at the network level.
- **Timeouts / retries:** `playwright.config.ts` sets `timeout: 15_000` and `retries: 0` (NO blanket retries — a flake elsewhere is a real defect). The SSE flows opt into `test.describe.configure({ retries: 1, timeout: 30_000 })` (the EventSource open is the slowest checkpoint and the one legitimate CI-timing risk). Add a retry ONLY to a genuine connection/timing checkpoint, never broadly.
- **Quarantine, never `test.skip`:** a genuinely-flaky spec is MOVED to `frontend/tests/quarantine/` with the linked SOW filename in its file header. Two Playwright projects make this automatic: `chromium` is the gating project with `testIgnore: '**/quarantine/**'`; `quarantine` (its own `testDir`) runs only that dir. `npm run e2e` / `npm run e2e:a11y` name `--project=chromium` (gating); `npm run e2e:quarantine` runs `--project=quarantine --pass-with-no-tests` (non-gating, diagnostic). The directory is empty on delivery — policy in `frontend/tests/quarantine/README.md`.
- **`npm run e2e:a11y`** runs the three axe specs (`a11y`, `viz-a11y`, `stats-a11y`) on the gating project; axe scans **every** route under both themes, including the `/sessions/:id` **logs** tab. Threshold: zero serious/critical.

### viz a11y waivers (SOW-0012)

Each D3/canvas chart has a waiver doc at `frontend/src/viz/<chart>/a11y.md` (`waterfall`, `flamegraph`, `timeline`, `topology`). It records any per-selector axe exclusion (selector + rule id + rationale + owning issue) and any known a11y limitation. **Axe exclusions are per-selector** (`new AxeBuilder({ page }).exclude('<selector>')`), **never** a global `disableRules`. Known tracked limitation (fix deferred to SOW-0012 `## Followup`): the **`WaterfallCanvas` + `FlameGraph` Canvas renderers** (above `SVG_SPAN_CEILING`) have **no focusable-span fallback** → Canvas-mode keyboard span-selection is missing; the lint gate is **blind** to the FlameGraph case (`onClick` on the `<canvas>` element, which jsx-a11y treats as interactive). The Timeline + Topology Canvas renderers DO provide a focusable `<button>` fallback list (`canvasFallbackList`), so they have no gap. axe cannot see inside `<canvas>`, so these limitations are invisible to the axe gate — the waiver docs are their record.

## Coverage thresholds

`vitest.config.ts` enforces a **global** aggregate floor AND a **per-directory ≥ 80% lines** floor for every measured dir under `src/components/` and `src/pages/`, via Vitest's **native glob-keyed `coverage.thresholds`** (SOW-0012 Chunk C — no wrapper script). The same `npm run test -- --run --coverage` command applies both; a single under-covered dir fails the run with `ERROR: Coverage for lines (NN%) does not meet "<glob>" threshold (80%)`.

- **The dir lists live in `frontend/vitest.coverage.mjs` (SOW-0012 review F3), not inline in `vitest.config.ts`.** That shared ESM module exports `COVERAGE_INCLUDE` (measured set), `PER_DIR_GLOBS` (per-dir floors), `COVERAGE_EXCLUDED` (intentional-exclusion ledger), and `PER_DIR_LINES`; `vitest.config.ts` imports the measuring lists, and the verifier `scripts/check-coverage-config.mjs` imports all of them — one source of truth, no copy to drift. (`.mjs` so the Node verifier needs no TS loader; typed for the config via `vitest.coverage.d.mts`.)
- **A new implemented + tested component/page dir must be added to BOTH lists in `vitest.coverage.mjs`:** `COVERAGE_INCLUDE` (so it is measured + covered by the global floor) AND `PER_DIR_GLOBS` (so it gets its own per-dir floor). They must stay in lockstep. A new source dir/flat-file you intentionally do NOT measure must instead be added to `COVERAGE_EXCLUDED` with a rationale (the disk-completeness check fails closed otherwise).
- **Why a per-dir key without `include` is a silent no-op:** an unmatched glob group's lines pct is `"Unknown"`, and `"Unknown" < 80` is `false` in JS — an empty group **vacuously passes**. So a per-dir key is added only for a measured dir. Dirs/files intentionally not measured go in `COVERAGE_EXCLUDED`: `Layout` + `StatCard` are **real** components with Vitest **unit** coverage **deferred** (Playwright exercises them) — NOT placeholders; `Agents`/`Models`/`Tools` are Phase-3 `<ComingSoon/>` wrappers; `NotFound.tsx` is the trivial 404. The shared `ComingSoon.tsx` component **is measured** (it has a unit test) and is a flat file so it carries no per-dir floor.
- A shared `PER_DIR_LINES` constant ties the global floor and every per-dir group together so they cannot silently diverge. Never lower it to make a dir pass — a dir under the floor is a finding to close with tests.
- **Two independent guards (do not conflate):** (1) the gate MECHANISM is self-tested by `scripts/check-coverage-thresholds.test.sh` (`npm run check:coverage-thresholds:selftest`), which runs Vitest on a throwaway 50%-lines fixture dir and proves the per-dir threshold fails closed under the floor / passes above it — it does NOT read the real config; (2) the REAL config is verified by `scripts/check-coverage-config.mjs` (`npm run check:coverage-config`), which fails closed on non-vacuity (a real per-dir glob matching zero files), lockstep (a measured component/page dir lacking a per-dir floor), AND disk-completeness (a source dir/flat-file under `src/components/`/`src/pages/` in neither `COVERAGE_INCLUDE` nor `COVERAGE_EXCLUDED`) — plus broad whole-root include globs, **per-dir includes that are not the EXACT canonical whole-dir shape `<root>/<Dir>/**/*.{ts,tsx}`** (a bare dir, an extension-narrowed `**/*.tsx`/`**/*.ts`, a narrow filename, or a deeper subpath — all reject so no sibling source escapes a "measured" dir), **`PER_DIR_GLOBS` entries that are not the EXACT canonical threshold shape `<root>/<Dir>/**`** (a bare dir matches no file path so its floor vacuously passes; only a canonical entry contributes its dir to the gated set), and malformed `.`/`..` segments. Its own decision logic is self-tested by `scripts/check-coverage-config.test.sh` (`npm run check:coverage-config:selftest`). All run in `scripts/lint.sh` and as dedicated CI `frontend` steps. Reporters: `text`, `text-summary`, `json` (emits `coverage/coverage-final.json`), `html` (the CI-uploaded report).

## Lint

ESLint flat config (`eslint.config.ts`) with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, and `eslint-plugin-import` (+ `eslint-import-resolver-typescript`). Zero-warnings policy: the package.json `lint` script bakes in `--max-warnings=0` (the single source of truth); both `scripts/lint.sh` and the CI Lint step run plain `npm run lint` (neither re-passes the flag).

> **Local aggregate runner.** The repo-wide `scripts/lint.sh` is the build-free static-analysis entrypoint: its frontend section runs `npm run lint` (the npm script bakes in `--max-warnings=0`; lint.sh does not re-pass it), `npm run typecheck`, the bundle-size gate self-test (`check:bundle-size:selftest`), the coverage-config verifier (`check:coverage-config` — the REAL per-dir floors), and the per-dir coverage gate self-test (`check:coverage-thresholds:selftest`), fail-fast, after the Go gates. It ensures `frontend/node_modules` is present (reusing `build.sh`'s `npm ci`/`npm install` fallback, only when missing) so the self-tests find their binaries. It does NOT run the REAL bundle-size gate (`npm run check:bundle-size`, needs a `vite build`) or the REAL coverage run (`npm run test -- --run --coverage`) — those need a build/full test run and stay in the CI `frontend` job + `scripts/{build,test}.sh` (`build.sh` runs `check:bundle-size` on the built `dist/`; `test.sh` runs the frontend coverage run after the Go suite).

- **Config builder**: ESLint core's `defineConfig()` + `globalIgnores()` from `eslint/config`, NOT `tseslint.config()` (typescript-eslint's helper is `@deprecated` in favour of core `defineConfig()`). `tseslint` is kept only for its parser/plugin + `recommendedTypeChecked` preset.
- **jsx-a11y / import flat-config**: both ship native flat-config support at the pinned versions (`jsxA11y.flatConfigs.recommended`, `importPlugin.flatConfigs.recommended` + `.typescript`) — NO `FlatCompat` bridge. The import/typescript preset + the `import/resolver.typescript` setting teach `import/no-unresolved` to follow `.ts`/`.tsx`/extensionless and type-only specifiers. import/recommended's three `'warn'` rules (`no-named-as-default`, `no-named-as-default-member`, `no-duplicates`) are promoted to `'error'` (a warning fails the zero-warnings gate anyway; explicit is clearer).
- **Scrollable regions**: `jsx-a11y/no-noninteractive-tabindex` is configured with `roles: ['tabpanel', 'region']` so a `tabIndex={0}` on a `role="region"` scroll container (the WAI-ARIA APG scrollable-region pattern — lets keyboard users focus + arrow-scroll an overflow area) is allowed. This is a deliberate rule config, not a disable.
- **Untyped-plugin typing**: `eslint-plugin-jsx-a11y` ships no `.d.ts`, so an ambient `declare module` shim lives at `src/types/eslint-plugin-jsx-a11y.d.ts` (typed via `eslint/config`'s `Config`). `eslint-plugin-react-hooks` 7.x's `configs` field is not assignable to core's strict `Plugin` type, so it is registered with a narrow `as Plugins[string]` cast (`Plugins = NonNullable<Config['plugins']>`), never `any`. The config file's own untyped-plugin access (`importPlugin.flatConfigs`/`jsxA11y.flatConfigs` are `any`) is relaxed for `eslint.config.ts` ONLY via a trailing scoped block turning off `@typescript-eslint/no-unsafe-{argument,member-access,assignment}` + `import/no-named-as-default-member`; every app-source file keeps full coverage.
- **Per-line disables** (each with an inline rationale; never global): Vite `?worker` virtual-module imports trip `import/default` (resolver sees the suffix-stripped `.ts` with no synthesized default); the WAI-ARIA tabs container trips `interactive-supports-focus` (roving tabindex puts focus on the tab buttons); a modal backdrop click + a `role="dialog"` keydown trip the static/noninteractive-interaction rules (full keyboard path exists elsewhere); the `<canvas>` viz wrappers (timeline + waterfall) trip `click-events-have-key-events` — the timeline has a focusable DOM-fallback span list, the **waterfall Canvas mode does NOT** (a tracked keyboard-access gap, flagged for the `src/viz/<chart>/a11y.md` waiver work, not silently accepted).

## Bundle Size

`vite.config.ts` sets `build.manifest: true` so Vite emits `dist/.vite/manifest.json`. The bundle-size gate (`scripts/check-bundle-size.js`, SOW-0012) classifies chunks from that manifest's `ManifestChunk` flags rather than from filenames: `isEntry` ⇒ main chunk (≤ 500 KB gz), `isDynamicEntry` ⇒ per-route lazy chunk (≤ 200 KB gz each). Keep `build.manifest` enabled — the gate fails closed without the manifest. When you add a route split, do it via `React.lazy` + dynamic `import()` so the split chunk is marked `isDynamicEntry` and falls under the 200 KB lazy budget.

- **Each entry's budget is its transitive static-import CLOSURE, not its `.file` alone.** For every `isEntry`/`isDynamicEntry` chunk the gate sums the gz of the entry's own file PLUS the transitive closure of its static `imports` (follow `manifest[key].imports` recursively; each key contributes its `.file`). Files are de-duplicated WITHIN one entry's closure (a shared chunk reached via two import edges counts once); each entry's closure is summed independently (a chunk shared by two entries is budgeted under EACH — that is the real per-route transfer cost). `dynamicImports` are **not** followed (they point at separately-budgeted lazy chunks the browser does not fetch on the entry's initial load). This is why a Rollup-split SHARED chunk (neither `isEntry` nor `isDynamicEntry`, reachable only via an entry's `imports`) is gated under the importing entry — budgeting the entry's `.file` alone would let a small lazy route that statically imports a huge shared chunk pass (a fail-open).
- A `?worker` bundle (e.g. `viz/forceWorker.ts` imported as `?worker`) is emitted as its own chunk that is neither an entry/dynamic-entry nor inside any entry's static-import closure (it is absent from the manifest); it is reported as "ungated", not gated.
- **Fail-closed on manifest-contract violations:** the gate exits non-zero on a missing/empty dist, a missing/invalid manifest (incl. a JSON array), zero classified JS chunks, no main (`isEntry`) chunk, **any** manifest entry (JS **or** non-JS, e.g. CSS) whose `.file` is absent on disk (an up-front sweep over every chunk), a JS chunk flagged **both** `isEntry` **and** `isDynamicEntry` (mutually exclusive — a both-flagged route chunk would be under-budgeted as MAIN), a static-import graph referencing a missing/invalid chunk key, an `imports`/`dynamicImports` that is present but **not an array**, an `imports` array holding a **non-string** element, a `dynamicImports` array holding a **non-string** element, a `dynamicImports` element referencing a **missing** manifest key, or a `dynamicImports` element referencing a **JS chunk that is not `isDynamicEntry`** (the last three close the lazy-chunk half: a mis-flagged/dangling dynamicImport target would otherwise slip to "ungated" instead of being budgeted). It never certifies "within budget" without measuring real, classified chunks.
