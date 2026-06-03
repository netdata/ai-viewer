import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
// The coverage dir lists (measured set + per-directory line floors) live in a
// shared ESM module that BOTH this config and scripts/check-coverage-config.mjs
// read, so the gate Vitest enforces and the verifier that checks it for vacuous
// globs / missing floors cannot diverge (SOW-0012 review F3). See
// vitest.coverage.mjs for the lockstep invariant and the full rationale.
import { COVERAGE_INCLUDE, PER_DIR_GLOBS, PER_DIR_LINES } from './vitest.coverage.mjs';

// Vitest config kept separate from vite.config.ts so the production build
// never pulls in test-only globals/jsdom. Coverage is scoped to the source
// dirs implemented in this chunk; placeholder pages and Phase-2 stubs are
// excluded so the gate measures real, exercised code (AGENTS.md gate:
// >= 80% lines on implemented component/lib dirs).
//
// Per-directory mechanism: each PER_DIR_GLOBS entry becomes its OWN Vitest
// threshold group — Vitest matches the glob against each file path relative to
// root, aggregates the matched files into one coverage map, and fails the run
// (exit 1) if that group's lines% < PER_DIR_LINES, emitting
//   ERROR: Coverage for lines (NN%) does not meet "<glob>" threshold (80%)
// This is Vitest 4's NATIVE glob-keyed `coverage.thresholds` mechanism
// (vitest/dist/chunks: BaseCoverageProvider.resolveThresholds/checkThresholds);
// no wrapper script is needed.

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
      // Measured set — shared with the config verifier (vitest.coverage.mjs).
      // `include` wants a mutable array; the shared constant is readonly, so copy.
      include: [...COVERAGE_INCLUDE],
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
