#!/usr/bin/env node
// Verify the REAL Vitest coverage config's per-directory floors against the
// source tree on disk (SOW-0012 review F3). This is NOT the gate-mechanism
// self-test (scripts/check-coverage-thresholds.test.sh proves Vitest's
// glob-keyed thresholds fail closed on a throwaway fixture); this verifier
// enforces the lockstep + non-vacuity of the ACTUAL lists the gate uses.
//
// Mechanism (why this cannot drift)
//   vitest.config.ts and this verifier BOTH import the same two lists from
//   vitest.coverage.mjs — PER_DIR_GLOBS (the per-dir line floors) and
//   COVERAGE_INCLUDE (the measured set). So the lists checked here are byte-for-
//   byte the lists Vitest enforces; there is no second copy to fall out of sync.
//
// Checks, all FAIL CLOSED (a returned error message → exit 1, naming the offender):
//   (a) NON-VACUITY: every PER_DIR_GLOBS entry must match >= 1 real source file
//       on disk. A per-dir glob matching ZERO files has lines pct "Unknown" in
//       Vitest, and "Unknown" < 80 is false, so an empty group VACUOUSLY PASSES —
//       silently disabling that dir's floor. A glob over a deleted/renamed/empty
//       dir is therefore a defect, not a pass.
//   (b) LOCKSTEP: every immediate directory under src/components/ and src/pages/
//       that is MEASURED (matched by a COVERAGE_INCLUDE entry AND containing >= 1
//       non-test .ts/.tsx file) must have a corresponding PER_DIR_GLOBS key. A
//       measured component/page dir with no per-dir floor is only gated by the
//       weaker global aggregate — a silent coverage hole.
//   (c) UNSUPPORTED BROAD SHAPE: a COVERAGE_INCLUDE entry under a per-dir root
//       (src/components/ or src/pages/) whose first path segment after the root
//       is `*` or `**` MEASURES the whole root, not a named immediate dir, so the
//       lockstep check (b) derives ZERO dirs from it — a broad glob there is a
//       fail-OPEN hole (it can measure page files while (b) checks nothing). The
//       verifier rejects it (it does NOT silently ignore it): replace it with
//       explicit per-dir include entries (e.g. `src/pages/Foo/**/*.{ts,tsx}`) so
//       each measured dir is lockstep-checkable, or extend the verifier first.
//
// Build-free and dependency-free: walks the tree with node:fs only (no glob
// engine, so behavior is identical across Node 22 [CI] and newer). Run from
// frontend/ (the dir lists are repo-relative to frontend/). Wired into
// scripts/lint.sh's frontend section and a dedicated CI `frontend` step.
//
// Structure: the check logic is the EXPORTED PURE function checkCoverageConfig()
// so it can be exercised hermetically by check-coverage-config.test.sh against a
// throwaway fixture tree. The CLI block at the bottom imports the REAL lists from
// vitest.coverage.mjs, calls the function with the real frontend dir, prints any
// errors, and exits 1 if there are any.
//
// Usage:  node scripts/check-coverage-config.mjs [srcRoot]
//         (srcRoot default: <script>/.. , i.e. the frontend/ dir; the optional
//          arg lets a self-test point it at a fixture tree.)
// Exit:   0 = config is non-vacuous and in lockstep; 1 = a defect (named).

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const RED = '\x1b[0;31m';
const GREEN = '\x1b[0;32m';
const GRAY = '\x1b[0;90m';
const NC = '\x1b[0m';

// The two component/page roots whose immediate subdirs the per-dir floor covers
// (quality-gates.md §Frontend — Unit/Component). The lockstep check (b) and the
// broad-shape check (c) are scoped to these — src/lib, src/state, src/api,
// src/viz are measured in aggregate but have no per-dir floor by design.
const PER_DIR_ROOTS = ['src/components', 'src/pages'];

/** dirGlobToDir maps a per-dir glob ("src/components/FilterBar/**") to its
 *  directory ("src/components/FilterBar") by stripping a trailing "/**" (and any
 *  trailing slash). Pure string work — no glob engine. */
function dirGlobToDir(glob) {
  return glob.replace(/\/\*\*$/, '').replace(/\/$/, '');
}

/** firstSegmentUnderRoot returns the first path segment of `entry` AFTER a
 *  per-dir root prefix, or null if `entry` is not under any per-dir root. E.g.
 *    "src/components/FilterBar/**\/*.{ts,tsx}" -> { root: "src/components", first: "FilterBar" }
 *    "src/pages/**\/*.{ts,tsx}"                 -> { root: "src/pages",      first: "**" }
 *    "src/lib/**\/*.ts"                           -> null (not under a per-dir root) */
function firstSegmentUnderRoot(entry) {
  for (const root of PER_DIR_ROOTS) {
    const prefix = `${root}/`;
    if (entry.startsWith(prefix)) {
      const rest = entry.slice(prefix.length);
      const first = rest.split('/')[0];
      return { root, first };
    }
  }
  return null;
}

/** hasSourceFile walks `relDir` (relative to `frontendDir`) recursively and
 *  returns true if it contains >= 1 NON-TEST, NON-DECLARATION .ts/.tsx file.
 *  Test/spec files (*.test.ts(x), *.spec.ts(x)) and ambient declaration files
 *  (*.d.ts) do not count — a dir of only tests or only `.d.ts` declarations has
 *  no MEASURED source (v8 coverage reports no executable lines for either, so
 *  the per-dir floor would be vacuous). Missing dir => false. */
function hasSourceFile(frontendDir, relDir) {
  const abs = path.join(frontendDir, relDir);
  let stat;
  try {
    stat = fs.statSync(abs);
  } catch {
    return false;
  }
  if (!stat.isDirectory()) {
    return false;
  }
  for (const ent of fs.readdirSync(abs, { withFileTypes: true })) {
    if (ent.isDirectory()) {
      if (hasSourceFile(frontendDir, path.posix.join(relDir, ent.name))) {
        return true;
      }
      continue;
    }
    const n = ent.name;
    if (/\.(test|spec)\.(ts|tsx)$/.test(n)) {
      continue;
    }
    if (/\.(ts|tsx)$/.test(n) && !n.endsWith('.d.ts')) {
      return true;
    }
  }
  return false;
}

/**
 * checkCoverageConfig is the pure verifier: given the two coverage dir lists, the
 * frontend dir they are relative to, and the per-dir roots, it returns an array
 * of human-readable error strings (empty == config is sound). It performs no I/O
 * beyond reading the source tree under `frontendDir`, prints nothing, and never
 * exits — the caller decides how to report. Extracted as a pure function so the
 * self-test can drive it against a fixture tree.
 *
 * @param {{ include: readonly string[], perDirGlobs: readonly string[],
 *           frontendDir: string }} opts
 * @returns {string[]} error messages (each naming the offending entry/dir)
 */
export function checkCoverageConfig({ include, perDirGlobs, frontendDir }) {
  const errors = [];

  // Local hasSource bound to this frontendDir (the module helper takes it as an
  // arg so it stays pure across fixture roots).
  const hasSource = (relDir) => hasSourceFile(frontendDir, relDir);

  // --- (a) non-vacuity: every per-dir glob matches >= 1 real source file ------
  const gatedDirs = new Set();
  for (const glob of perDirGlobs) {
    const dir = dirGlobToDir(glob);
    gatedDirs.add(dir);
    if (!hasSource(dir)) {
      errors.push(
        `per-dir glob "${glob}" matches ZERO source files on disk (dir "${dir}" missing or has no non-test .ts/.tsx). ` +
          `An empty glob group vacuously PASSES Vitest's threshold ("Unknown" < 80 is false), silently disabling this floor — ` +
          `fix the glob/dir or remove the key (and its coverage.include entry).`,
      );
    }
  }

  // --- (b)+(c) walk COVERAGE_INCLUDE entries under a per-dir root --------------
  // For each include entry under src/components/ or src/pages/:
  //   (c) if its first segment after the root is `*`/`**`, it measures the whole
  //       root (not a named immediate dir) — an unsupported broad shape the
  //       lockstep check cannot derive a dir from. REJECT (fail closed), naming
  //       the entry, rather than silently ignore it (the prior fail-open hole).
  //   otherwise it names an immediate dir -> add to the measured set for (b).
  const measuredDirs = new Set();
  for (const entry of include) {
    const seg = firstSegmentUnderRoot(entry);
    if (seg === null) {
      continue; // not under a per-dir root (src/lib, src/api, src/viz, ...)
    }
    if (!seg.first || seg.first === '*' || seg.first === '**') {
      errors.push(
        `coverage.include entry "${entry}" measures the whole "${seg.root}" root via a broad glob ` +
          `("${seg.first || '<empty>'}" as the first segment), not a named immediate dir. ` +
          `The per-dir verifier cannot derive a lockstep-checkable dir from it, so a measured page/component ` +
          `would have NO enforced per-dir floor (a fail-open hole). Replace it with explicit per-dir entries ` +
          `(e.g. "${seg.root}/<Dir>/**/*.{ts,tsx}") so each measured dir has a per-dir floor, or extend this verifier first.`,
      );
      continue;
    }
    measuredDirs.add(`${seg.root}/${seg.first}`);
  }

  // --- (b) lockstep: every MEASURED component/page dir has a per-dir floor ----
  for (const dir of measuredDirs) {
    if (!hasSource(dir)) {
      // Measured-by-include but empty on disk: that is the (a)-class vacuity trap
      // on the include side; report it so a stale include entry is not silently
      // contributing nothing.
      errors.push(
        `coverage.include measures "${dir}" but it has no non-test .ts/.tsx source on disk — ` +
          `remove the stale include entry (and any matching per-dir glob).`,
      );
      continue;
    }
    if (!gatedDirs.has(dir)) {
      errors.push(
        `measured component/page dir "${dir}" has NO per-dir line floor (missing from PER_DIR_GLOBS) — ` +
          `it is gated only by the weaker global aggregate. Add "${dir}/**" to PER_DIR_GLOBS in vitest.coverage.mjs.`,
      );
    }
  }

  return errors;
}

// --- CLI: run the verifier on the REAL config + real source tree -------------
// Guarded so importing the module (the self-test) does NOT execute the CLI. The
// real lists are imported lazily inside the guard so a fixture-only import of the
// function never depends on vitest.coverage.mjs.
const __filename = fileURLToPath(import.meta.url);
const invokedAsScript = process.argv[1] && path.resolve(process.argv[1]) === __filename;
if (invokedAsScript) {
  const { COVERAGE_INCLUDE, PER_DIR_GLOBS } = await import('../vitest.coverage.mjs');
  const __dirname = path.dirname(__filename);
  const frontendDir = process.argv[2] ? path.resolve(process.argv[2]) : path.resolve(__dirname, '..');

  const errors = checkCoverageConfig({
    include: COVERAGE_INCLUDE,
    perDirGlobs: PER_DIR_GLOBS,
    frontendDir,
  });

  process.stdout.write(`${GRAY}coverage-config verifier — frontend: ${frontendDir}${NC}\n`);
  if (errors.length > 0) {
    process.stderr.write(`${RED}COVERAGE CONFIG: FAIL${NC}\n`);
    for (const e of errors) {
      process.stderr.write(`  ${e}\n`);
    }
    process.exit(1);
  }
  // Count only the lockstep-relevant measured dirs (those under a per-dir root,
  // named — broad shapes would already have produced an error above).
  let measured = 0;
  for (const entry of COVERAGE_INCLUDE) {
    const seg = firstSegmentUnderRoot(entry);
    if (seg !== null && seg.first && seg.first !== '*' && seg.first !== '**') {
      measured += 1;
    }
  }
  process.stdout.write(
    `${GREEN}COVERAGE CONFIG: PASS${NC} (${PER_DIR_GLOBS.length} per-dir floors, all non-vacuous; ` +
      `${measured} measured component/page dirs, all with a per-dir floor)\n`,
  );
}
