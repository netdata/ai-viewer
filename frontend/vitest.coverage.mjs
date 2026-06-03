// Single source of truth for the Vitest coverage gate's two directory lists:
// the files MEASURED (`COVERAGE_INCLUDE`) and the per-directory line floors
// (`PER_DIR_GLOBS`, each gated at `PER_DIR_LINES`).
//
// Why a shared module (SOW-0012 review F3)
//   `vitest.config.ts` IMPORTS these lists, and `scripts/check-coverage-config.mjs`
//   READS the SAME lists, so the per-dir floors and the measured set can never
//   silently diverge from what the gate actually enforces. The verifier proves, on
//   the REAL lists, that (a) every per-dir glob matches >= 1 real source file on
//   disk (no vacuous "Unknown" pass — see the lockstep note below) and (b) every
//   measured component/page dir has a per-dir floor. Keeping the data in one place
//   is what makes those checks meaningful: they read precisely the config Vitest runs.
//
//   This is a plain ESM `.mjs` (not `.ts`) so the standalone Node verifier can
//   import it WITHOUT a TypeScript loader, while `vitest.config.ts` imports it
//   type-checked via the co-located `vitest.coverage.d.mts` declaration. The data
//   itself lives only here; the `.d.mts` declares only its shapes, so there is no
//   value to drift.
//
// LOCKSTEP INVARIANT (also enforced by check-coverage-config.mjs):
//   A per-dir glob group that matches ZERO files has lines pct "Unknown", and
//   `"Unknown" < 80` is `false` in JS, so an empty group VACUOUSLY PASSES. Two
//   rules keep that trap shut:
//     1. A per-dir glob is added ONLY for a dir that is also in COVERAGE_INCLUDE
//        (i.e. measured). Placeholder/stub dirs (ComingSoon, Layout, StatCard,
//        Agents, Models, Tools, NotFound) are in NEITHER list.
//     2. Every MEASURED dir under src/components/ and src/pages/ HAS a per-dir glob.
//   The verifier fails closed if either rule is broken.

/** Per-directory line-coverage floor (quality-gates.md §Frontend — Unit/Component:
 *  ">= 80% lines per component directory under src/components/ and src/pages/").
 *  One constant so the global floor (vitest.config.ts) and every per-dir glob
 *  group share it and cannot diverge. Never lower it to make a dir pass — a dir
 *  under the floor is a finding to close with tests, not a reason to weaken the gate. */
export const PER_DIR_LINES = 80;

/** Files measured for coverage: implemented, exercised source only. Placeholder
 *  pages and Phase-2/3 stubs are excluded so the gate measures real code (the
 *  global aggregate floor applies to every file listed here; per-dir floors apply
 *  to the component/page dirs in PER_DIR_GLOBS). */
export const COVERAGE_INCLUDE = [
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
  // /topology cross-session page (SOW-0006): implemented (Topology.tsx) and
  // tested (Topology.test.tsx, 96.7% lines). Listed here AND in PER_DIR_GLOBS so
  // it is both measured and gated per-dir.
  'src/pages/Topology/**/*.{ts,tsx}',
];

/** Every immediate directory under src/components/ and src/pages/ that holds
 *  IMPLEMENTED, exercised source (i.e. is measured via COVERAGE_INCLUDE). Each
 *  entry becomes its OWN Vitest threshold group keyed by this glob; Vitest fails
 *  the run (exit 1) if that group's lines % < PER_DIR_LINES, emitting
 *    ERROR: Coverage for lines (NN%) does not meet "<glob>" threshold (80%)
 *  This is Vitest 4's NATIVE glob-keyed `coverage.thresholds` mechanism; no
 *  wrapper script is needed. Keep this list and the component/page entries of
 *  COVERAGE_INCLUDE in lockstep (see the LOCKSTEP INVARIANT above). */
export const PER_DIR_GLOBS = [
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
];
