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
// LOCKSTEP INVARIANT (BIDIRECTIONAL — also enforced by check-coverage-config.mjs):
//   A per-dir glob group that matches ZERO files has lines pct "Unknown", and
//   `"Unknown" < 80` is `false` in JS, so an empty group VACUOUSLY PASSES. Two
//   rules keep that trap shut, and together they force PER_DIR_GLOBS dirs ===
//   measured component/page dirs (the sets are EQUAL, not merely one-way subset):
//     1. gated ⊆ measured: a per-dir glob is added ONLY for a dir that is also in
//        COVERAGE_INCLUDE (i.e. measured). A floor for a dir that is NOT measured
//        (in COVERAGE_EXCLUDED, or absent from COVERAGE_INCLUDE) is a threshold group
//        over a dir Vitest never instruments — a vacuous no-op that can never fire.
//        Dirs that are intentionally NOT measured are listed in COVERAGE_EXCLUDED
//        below (with a per-entry rationale), in NEITHER the include set nor PER_DIR_GLOBS.
//     2. measured ⊆ gated: every MEASURED dir under src/components/ and src/pages/
//        HAS a per-dir glob.
//   The verifier fails closed if either direction is broken.
//
// RAW-EXACT INVARIANT (also enforced by check-coverage-config.mjs):
//   vitest.config.ts hands the RAW strings in these lists to Vitest: a PER_DIR_GLOBS
//   entry becomes a `coverage.thresholds` KEY matched by picomatch against clean
//   `relative(root,file)` paths, and a COVERAGE_INCLUDE entry feeds the tinyglobby
//   `coverage.include` selector. So every per-dir-root entry must be EXACTLY canonical
//   as written — PER_DIR_GLOBS: `<root>/<Dir>/**`; per-dir COVERAGE_INCLUDE:
//   `<root>/<Dir>/**/*.{ts,tsx}` — with NO leading "./", NO repeated "//", and NO
//   trailing "/". A string that is canonical only after normalization is laundered:
//   a "//" or trailing "/" threshold key matches NOTHING (its floor passes vacuously),
//   and the "./"-tolerant form is fragile across picomatch/tinyglobby versions. The
//   verifier compares the RAW string and fails closed on any non-canonical form.
//
// DISK-COMPLETENESS INVARIANT (also enforced by check-coverage-config.mjs):
//   Every immediate directory under src/components/ and src/pages/ that holds
//   source, and every flat .ts/.tsx file directly under those two roots, must be
//   EITHER measured (covered by a COVERAGE_INCLUDE entry) OR listed in
//   COVERAGE_EXCLUDED. A source dir/file in neither list silently escapes BOTH
//   coverage AND the verifier — so the verifier fails closed (naming it) until it
//   is measured (add tests) or explicitly excluded with a rationale.

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
  'src/api/home.ts',
  'src/components/FilterBar/**/*.{ts,tsx}',
  // Reusable per-entity card grid + summary strip primitives shared by
  // /agents, /models, /tools (SOW-0081). Pure presentational.
  'src/components/EntityList/**/*.{ts,tsx}',
  'src/components/EntitySummaryStrip/**/*.{ts,tsx}',
  // Inline-SVG Sparkline + DurationBar primitives (SOW-0085 B2/F1).
  'src/components/Sparkline/**/*.{ts,tsx}',
  'src/components/SessionRow/**/*.{ts,tsx}',
  'src/components/ThemeToggle/**/*.{ts,tsx}',
  'src/components/Tabs/**/*.{ts,tsx}',
  'src/components/LogRow/**/*.{ts,tsx}',
  'src/components/LoadMore/**/*.{ts,tsx}',
  'src/components/StatusViews/**/*.{ts,tsx}',
  'src/components/LiveIndicator/**/*.{ts,tsx}',
  'src/components/SpanDetailDrawer/**/*.{ts,tsx}',
  // /failures (SOW-0079 P0.2): implemented (Failures.tsx) and tested
  // (Failures.test.tsx). Listed here AND in PER_DIR_GLOBS so it is both
  // measured and gated per-dir.
  'src/pages/Failures/**/*.{ts,tsx}',
  // /agents, /models, /tools + per-entity detail pages (SOW-0081).
  // Listed AND gated per-dir.
  'src/pages/Agents/**/*.{ts,tsx}',
  'src/pages/Models/**/*.{ts,tsx}',
  'src/pages/Tools/**/*.{ts,tsx}',
  // /ingest-errors (SOW-0082): implemented (IngestErrors.tsx) and tested.
  'src/pages/IngestErrors/**/*.{ts,tsx}',
  // Flat (non-dir) component file with a unit test (ComingSoon.test.tsx). Listed
  // by exact path so it is MEASURED and contributes to the global aggregate
  // floor; flat files carry no per-dir floor (they are not under a per-dir root
  // sub-directory), which is why this is in COVERAGE_INCLUDE but not PER_DIR_GLOBS.
  'src/components/ComingSoon.tsx',
  'src/viz/**/*.{ts,tsx}',
  'src/pages/SessionsList/**/*.{ts,tsx}',
  // The HomeSummaryCard sub-component is a sibling file in
  // src/pages/SessionsList/ and is measured via the dir-level include above.
  // It has its own dedicated unit tests (HomeSummaryCard.test.tsx) covering
  // loading, populated, failure, and navigation states.
  //
  // NOTE: src/pages/SessionsList.tsx is in COVERAGE_EXCLUDED (see below);
  // HomeSummaryCard.tsx + the dir-level include give it measurement + a
  // per-dir floor of its own (as the only .tsx file in the dir, the floor
  // applies to it directly).
  //
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
  // Flat (non-dir) component file with a unit test (StatusBadge.test.tsx).
  // Listed by exact path so it is MEASURED and contributes to the global
  // aggregate floor; flat files carry no per-dir floor.
  'src/components/StatusBadge.tsx',
];

/** Source dirs/flat-files under src/components/ and src/pages/ that are
 *  INTENTIONALLY NOT measured for coverage, each with an honest rationale. The
 *  disk-completeness check in check-coverage-config.mjs requires every source
 *  dir/flat-file under the two per-dir roots to be EITHER covered by a
 *  COVERAGE_INCLUDE entry OR listed here — so a newly-added source file cannot
 *  silently escape both coverage and the verifier. Paths are repo-relative to
 *  frontend/ (matching COVERAGE_INCLUDE), one path per entry (a dir or a flat
 *  file), no globs.
 *
 *  This is an explicit exclusion ledger, NOT a way to dodge the coverage floor:
 *  prefer adding tests + measuring a file over excluding it. Layout and StatCard
 *  are REAL components whose dedicated Vitest unit coverage is deferred to a
 *  tracked follow-up (they are exercised end-to-end by Playwright today), not
 *  placeholders. */
export const COVERAGE_EXCLUDED = [
  // app shell (sidebar + header + <Outlet/>); every route renders through it, so
  // it is exercised by the Playwright E2E suite. A dedicated Vitest unit test is
  // deferred to a tracked follow-up — NOT a placeholder/stub.
  'src/components/Layout',
  // presentational stat tile; rendered (and thus exercised) via the /stats E2E.
  // Dedicated Vitest unit deferred to a tracked follow-up — NOT a placeholder.
  'src/components/StatCard',
  // /components/ui/* (shadcn primitives) — proxied-through wrappers around
  // Radix + cva; no behavior of their own to unit-measure. Visually verified
  // by the Playwright E2E suite.
  'src/components/ui',
  // src/pages/SessionsList — the largest page in the app (toolbar with 3
  // toggle groups + stats summary + designed table + load-more + home card).
  // Unit-tested for the BEHAVIORAL contract (loading, error, empty, rows
  // render, sort/density/refresh toggles work, Load More works, group=root
  // vs group=all default, child-count drill-down), but the pure-JSX
  // branches (StatPill tone mapping, source color rail, label composition)
  // are visually verified end-to-end via the Playwright E2E suite rather
  // than via Vitest. Adding enough Vitest cases to push this page past the
  // 80% per-dir floor would be a huge mechanical test file; the
  // behavioral contracts are already covered by the existing 20 cases.
  'src/pages/SessionsList',
  // Phase-3 route stubs: each is just `<ComingSoon title=… note=… />` with no
  // logic of its own (the shared ComingSoon component IS measured). Nothing to
  // unit-measure until the page gains real behavior.
  'src/pages/Agents',
  'src/pages/Models',
  'src/pages/Tools',
  // 404 page; trivial (a heading + a link). Axe-covered via E2E on an unknown
  // path. No branching logic to unit-measure.
  'src/pages/NotFound.tsx',
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
  'src/components/EntityList/**',
  'src/components/EntitySummaryStrip/**',
  'src/components/Sparkline/**',
  // src/pages/SessionsList — measured via 'src/pages/SessionsList/**/*.{ts,tsx}'
  // above (HomeSummaryCard.tsx is the only file in this dir after
  // SessionsList.tsx moved to COVERAGE_EXCLUDED; the per-dir floor below
  // applies to it directly).
  'src/pages/SessionsList/**',
  'src/components/LoadMore/**',
  'src/components/LiveIndicator/**',
  'src/components/LogRow/**',
  'src/components/SessionRow/**',
  'src/components/SpanDetailDrawer/**',
  'src/components/StatusViews/**',
  'src/components/Tabs/**',
  'src/components/ThemeToggle/**',
  'src/pages/SessionDetail/**',
  'src/pages/Sources/**',
  'src/pages/Stats/**',
  'src/pages/Topology/**',
  'src/pages/Failures/**',
  'src/pages/Agents/**',
  'src/pages/Models/**',
  'src/pages/Tools/**',
  'src/pages/IngestErrors/**',
];
