import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// Vitest config kept separate from vite.config.ts so the production build
// never pulls in test-only globals/jsdom. Coverage is scoped to the source
// dirs implemented in this chunk; placeholder pages and Phase-2 stubs are
// excluded so the gate measures real, exercised code (AGENTS.md gate:
// >= 80% lines on implemented component/lib dirs).

// Per-directory line-coverage floor (quality-gates.md §Frontend — Unit/Component:
// ">= 80% lines per component directory under src/components/ and src/pages/").
// One named constant so the global floor and every per-dir glob group below can
// never silently diverge. Never lower this to make a dir pass — a dir under the
// floor is a finding to close with tests, not a reason to weaken the gate.
const PER_DIR_LINES = 80;

// Every immediate directory under src/components/ and src/pages/ that holds
// IMPLEMENTED, exercised source (i.e. is in coverage.include below). Each entry
// becomes its OWN Vitest threshold group: Vitest matches the glob against each
// file path relative to root, aggregates the matched files into one coverage map,
// and fails the run (exit 1) if that group's lines% < PER_DIR_LINES — emitting
//   ERROR: Coverage for lines (NN%) does not meet "<glob>" threshold (80%)
// This is Vitest 4's NATIVE glob-keyed `coverage.thresholds` mechanism
// (vitest/dist/chunks: BaseCoverageProvider.resolveThresholds/checkThresholds);
// no wrapper script is needed. Placeholder/stub dirs (ComingSoon, Layout,
// StatCard, Agents, Models, Tools, NotFound) are deliberately NOT listed: they
// are excluded from coverage.include, so a glob for them would match zero files
// and an empty group's lines pct is "Unknown" — a vacuous PASS. The per-dir
// floor therefore covers exactly the measured dirs; the global floor below still
// gates every included file in aggregate. Adding a dir here without adding it to
// coverage.include is a no-op (empty group) — keep the two lists in lockstep.
const PER_DIR_GLOBS = [
  'src/components/FilterBar/**',
  'src/components/LoadMore/**',
  'src/components/LogRow/**',
  'src/components/SessionRow/**',
  'src/components/SpanDetailDrawer/**',
  'src/components/StatusViews/**',
  'src/components/Tabs/**',
  'src/components/ThemeToggle/**',
  'src/pages/SessionDetail/**',
  'src/pages/SessionsList/**',
  'src/pages/Sources/**',
  'src/pages/Stats/**',
  'src/pages/Topology/**',
] as const;

const perDirThresholds = Object.fromEntries(
  PER_DIR_GLOBS.map((glob) => [glob, { lines: PER_DIR_LINES }]),
);

export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: true,
    // Unit tests are co-located under src/; the Playwright E2E specs live in
    // tests/ and must NOT be swept up by vitest's default glob (they use the
    // @playwright/test runner, not vitest). Scope discovery to src/ only.
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      // `html` is the human report CI uploads (quality-gates.md §Frontend —
      // Unit/Component: "CI artifact contains the HTML report"). `json` emits
      // coverage/coverage-final.json so per-dir line% is inspectable/diffable
      // outside the text table (the dir-level rollup the text reporter collapses);
      // the threshold self-test runs its own fixture, so it does not depend on
      // this file.
      reporter: ['text', 'text-summary', 'json', 'html'],
      include: [
        'src/state/**/*.ts',
        'src/lib/**/*.ts',
        'src/api/client.ts',
        'src/api/sse.ts',
        'src/api/sessions.ts',
        'src/api/stats.ts',
        'src/api/sources.ts',
        'src/api/logs.ts',
        'src/components/FilterBar/**/*.{ts,tsx}',
        'src/components/SessionRow/**/*.{ts,tsx}',
        'src/components/ThemeToggle/**/*.{ts,tsx}',
        'src/components/Tabs/**/*.{ts,tsx}',
        'src/components/LogRow/**/*.{ts,tsx}',
        'src/components/LoadMore/**/*.{ts,tsx}',
        'src/components/StatusViews/**/*.{ts,tsx}',
        'src/components/SpanDetailDrawer/**/*.{ts,tsx}',
        'src/viz/**/*.{ts,tsx}',
        'src/pages/SessionsList/**/*.{ts,tsx}',
        'src/pages/SessionDetail/**/*.{ts,tsx}',
        'src/pages/Sources/**/*.{ts,tsx}',
        // /stats dashboard (SOW-0007 Chunk 9a charts + Chunk 9b page): the
        // presentational chart components, the page that wires the data hooks +
        // controls, and the deep-search box are all implemented and tested.
        'src/pages/Stats/**/*.{ts,tsx}',
        // /topology cross-session page (SOW-0006): implemented (Topology.tsx)
        // and tested (Topology.test.tsx, 96.7% lines). Listed here AND in
        // PER_DIR_GLOBS so it is both measured and gated per-dir.
        'src/pages/Topology/**/*.{ts,tsx}',
      ],
      thresholds: {
        // Global floor — applies to EVERY included file in aggregate (the
        // pre-existing project-wide gate; unchanged).
        lines: PER_DIR_LINES,
        statements: 80,
        functions: 80,
        branches: 75,
        // Per-directory floors — each glob group is checked independently so a
        // single under-covered component/page dir fails the run even when the
        // global aggregate stays green (quality-gates.md §Frontend —
        // Unit/Component). Spread last; glob keys and the reserved
        // lines/statements/... keys share the same object per Vitest's schema.
        ...perDirThresholds,
      },
    },
  },
});
