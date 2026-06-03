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
// Two checks, both fail closed (exit 1, naming the offender):
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
//
// Build-free and dependency-free: walks the tree with node:fs only (no glob
// engine, so behavior is identical across Node 22 [CI] and newer). Run from
// frontend/ (the dir lists are repo-relative to frontend/). Wired into
// scripts/lint.sh's frontend section and a dedicated CI `frontend` step.
//
// Usage:  node scripts/check-coverage-config.mjs [srcRoot]
//         (srcRoot default: <script>/.. , i.e. the frontend/ dir; the optional
//          arg lets a self-test point it at a fixture tree.)
// Exit:   0 = config is non-vacuous and in lockstep; 1 = a defect (named).

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { COVERAGE_INCLUDE, PER_DIR_GLOBS } from '../vitest.coverage.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const RED = '\x1b[0;31m';
const GREEN = '\x1b[0;32m';
const GRAY = '\x1b[0;90m';
const NC = '\x1b[0m';

// The two component/page roots whose immediate subdirs the per-dir floor covers
// (quality-gates.md §Frontend — Unit/Component). The verifier's lockstep check
// (b) is scoped to these — src/lib, src/state, src/api, src/viz are measured in
// aggregate but have no per-dir floor by design.
const PER_DIR_ROOTS = ['src/components', 'src/pages'];

/** frontendDir: the dir the coverage globs are relative to (frontend/), or the
 *  optional fixture root a self-test passes. */
const frontendDir = process.argv[2] ? path.resolve(process.argv[2]) : path.resolve(__dirname, '..');

/** dirGlobToDir maps a per-dir glob ("src/components/FilterBar/**") to its
 *  directory ("src/components/FilterBar") by stripping a trailing "/**" (and any
 *  trailing slash). Pure string work — no glob engine. */
function dirGlobToDir(glob) {
  return glob.replace(/\/\*\*$/, '').replace(/\/$/, '');
}

/** includeEntryToDir maps a COVERAGE_INCLUDE entry under a per-dir root to that
 *  entry's immediate dir under the root, or null if the entry is not under a
 *  per-dir root (e.g. src/lib, src/api, src/viz). E.g.
 *    "src/components/FilterBar/**\/*.{ts,tsx}" -> "src/components/FilterBar"
 *    "src/pages/Stats/**\/*.{ts,tsx}"          -> "src/pages/Stats"
 *    "src/lib/**\/*.ts"                          -> null */
function includeEntryToDir(entry) {
  for (const root of PER_DIR_ROOTS) {
    const prefix = `${root}/`;
    if (entry.startsWith(prefix)) {
      const rest = entry.slice(prefix.length);
      const first = rest.split('/')[0];
      if (first && first !== '*' && first !== '**') {
        return `${root}/${first}`;
      }
    }
  }
  return null;
}

/** hasSourceFile walks `relDir` (relative to frontendDir) recursively and returns
 *  true if it contains >= 1 NON-TEST .ts/.tsx file. Test/spec files
 *  (*.test.ts(x), *.spec.ts(x)) do not count — a dir of only tests is not
 *  measured source. Missing dir => false. */
function hasSourceFile(relDir) {
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
      if (hasSourceFile(path.posix.join(relDir, ent.name))) {
        return true;
      }
      continue;
    }
    const n = ent.name;
    if (/\.(test|spec)\.(ts|tsx)$/.test(n)) {
      continue;
    }
    if (/\.(ts|tsx)$/.test(n)) {
      return true;
    }
  }
  return false;
}

const errors = [];

// --- (a) non-vacuity: every per-dir glob matches >= 1 real source file --------
const gatedDirs = new Set();
for (const glob of PER_DIR_GLOBS) {
  const dir = dirGlobToDir(glob);
  gatedDirs.add(dir);
  if (!hasSourceFile(dir)) {
    errors.push(
      `per-dir glob "${glob}" matches ZERO source files on disk (dir "${dir}" missing or has no non-test .ts/.tsx). ` +
        `An empty glob group vacuously PASSES Vitest's threshold ("Unknown" < ${'80'} is false), silently disabling this floor — ` +
        `fix the glob/dir or remove the key (and its coverage.include entry).`,
    );
  }
}

// --- (b) lockstep: every MEASURED component/page dir has a per-dir floor ------
// Derive the measured component/page dirs from COVERAGE_INCLUDE, keep those that
// actually contain non-test source, and require each to be in PER_DIR_GLOBS.
const measuredDirs = new Set();
for (const entry of COVERAGE_INCLUDE) {
  const dir = includeEntryToDir(entry);
  if (dir !== null) {
    measuredDirs.add(dir);
  }
}
for (const dir of measuredDirs) {
  if (!hasSourceFile(dir)) {
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

// --- report -------------------------------------------------------------------
process.stdout.write(`${GRAY}coverage-config verifier — frontend: ${frontendDir}${NC}\n`);
if (errors.length > 0) {
  process.stderr.write(`${RED}COVERAGE CONFIG: FAIL${NC}\n`);
  for (const e of errors) {
    process.stderr.write(`  ${e}\n`);
  }
  process.exit(1);
}
process.stdout.write(
  `${GREEN}COVERAGE CONFIG: PASS${NC} (${PER_DIR_GLOBS.length} per-dir floors, all non-vacuous; ` +
    `${measuredDirs.size} measured component/page dirs, all with a per-dir floor)\n`,
);
