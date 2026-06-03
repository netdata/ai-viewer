#!/usr/bin/env node
// Verify the REAL Vitest coverage config's per-directory floors against the
// source tree on disk (SOW-0012 review F3). This is NOT the gate-mechanism
// self-test (scripts/check-coverage-thresholds.test.sh proves Vitest's
// glob-keyed thresholds fail closed on a throwaway fixture); this verifier
// enforces the lockstep + non-vacuity of the ACTUAL lists the gate uses.
//
// Mechanism (why this cannot drift)
//   vitest.config.ts and this verifier BOTH import the same lists from
//   vitest.coverage.mjs — PER_DIR_GLOBS (the per-dir line floors), COVERAGE_INCLUDE
//   (the measured set), and COVERAGE_EXCLUDED (the intentional-exclusion ledger).
//   So the lists checked here are byte-for-byte the lists Vitest enforces; there is
//   no second copy to fall out of sync.
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
//   (c) DISK-COMPLETENESS: every immediate directory under src/components/ and
//       src/pages/ that holds source, and every flat .ts/.tsx file directly under
//       those two roots, must be EITHER covered by a COVERAGE_INCLUDE entry OR
//       listed in COVERAGE_EXCLUDED. A source dir/file in NEITHER list silently
//       escapes BOTH coverage AND checks (a)/(b) — so it is rejected (named) until
//       it is measured (add tests) or explicitly excluded with a rationale. This
//       closes the "shipped source no one is measuring" hole.
//   (d) UNSUPPORTED BROAD SHAPE: a COVERAGE_INCLUDE entry under a per-dir root
//       (src/components/ or src/pages/) whose first path segment after the root
//       contains a glob metacharacter (`* ? [ ] { }`, e.g. `*`/`**`/`*o`) MEASURES
//       more than one named immediate dir, so the lockstep check (b) derives no
//       single dir from it — a broad glob there is a fail-OPEN hole (it can measure
//       page files while (b) checks nothing). The verifier rejects it (it does NOT
//       silently ignore it): replace it with explicit per-dir include entries
//       (e.g. `src/pages/Foo/**/*.{ts,tsx}`) so each measured dir is
//       lockstep-checkable, or extend the verifier first.
//   (e) MALFORMED ENTRY: a COVERAGE_INCLUDE / COVERAGE_EXCLUDED entry containing a
//       `.` or `..` path segment (after stripping a leading `./` and collapsing
//       repeated `/`) is rejected (named) — such relative segments make on-disk
//       matching ambiguous, so fail closed rather than guess.
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
// (quality-gates.md §Frontend — Unit/Component). The lockstep check (b), the
// disk-completeness check (c), and the broad-shape check (d) are scoped to these —
// src/lib, src/state, src/api, src/viz are measured in aggregate but have no
// per-dir floor by design.
const PER_DIR_ROOTS = ['src/components', 'src/pages'];

/** dirGlobToDir maps a per-dir glob ("src/components/FilterBar/**") to its
 *  directory ("src/components/FilterBar") by stripping a trailing "/**" (and any
 *  trailing slash). Pure string work — no glob engine. */
function dirGlobToDir(glob) {
  return glob.replace(/\/\*\*$/, '').replace(/\/$/, '');
}

// Glob metacharacters that make a path segment non-literal. A first segment under
// a per-dir root containing ANY of these cannot name a single immediate dir, so a
// broad shape there is rejected (check (d)) — not just the exact `*`/`**` strings.
const GLOB_META = /[*?[\]{}]/;

/** normalizeEntry canonicalises a coverage-list entry for on-disk matching:
 *  strips a single leading "./", collapses repeated "/", and trims a trailing
 *  "/". It returns either { value } (the normalized path) or { error } if the
 *  entry contains a "." or ".." path segment (which would make matching against
 *  a clean POSIX disk path ambiguous — fail closed rather than guess). Pure
 *  string work; no glob engine, no I/O. */
function normalizeEntry(entry) {
  let value = String(entry);
  if (value.startsWith('./')) {
    value = value.slice(2);
  }
  value = value.replace(/\/{2,}/g, '/').replace(/\/$/, '');
  // Reject "." / ".." segments anywhere (a leading "./" was already stripped;
  // any survivor is an embedded relative segment we will not resolve).
  for (const seg of value.split('/')) {
    if (seg === '.' || seg === '..') {
      return {
        error:
          `coverage list entry "${entry}" contains a "${seg}" path segment after normalization ` +
          `("${value}") — relative segments make on-disk matching ambiguous. Use a clean repo-relative ` +
          `path (e.g. "src/components/Foo/**/*.{ts,tsx}").`,
      };
    }
  }
  return { value };
}

/** firstSegmentUnderRoot returns the first path segment of `entry` AFTER a
 *  per-dir root prefix, or null if `entry` is not under any per-dir root. `entry`
 *  is expected ALREADY NORMALIZED (see normalizeEntry). E.g.
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

/** isSourceFileName reports whether a plain file name is a MEASURED source file:
 *  a .ts/.tsx that is not a test/spec and not an ambient .d.ts declaration. Used
 *  for the flat-file half of the disk-completeness enumeration. */
function isSourceFileName(name) {
  if (/\.(test|spec)\.(ts|tsx)$/.test(name)) {
    return false;
  }
  return /\.(ts|tsx)$/.test(name) && !name.endsWith('.d.ts');
}

/** enumerateRoot lists, for one per-dir root (e.g. "src/components"), the on-disk
 *  source surface the disk-completeness check must account for:
 *    - dirs:      immediate sub-directories holding >= 1 measured source file
 *                 (recursively; reuses hasSourceFile), as "<root>/<name>".
 *    - flatFiles: flat .ts/.tsx source files DIRECTLY under the root (not in a
 *                 sub-dir), as "<root>/<name>".
 *  A missing root yields empty lists (a root with no files is not itself a
 *  defect). Pure but for reading the tree under `frontendDir`. */
function enumerateRoot(frontendDir, root) {
  const abs = path.join(frontendDir, root);
  const dirs = [];
  const flatFiles = [];
  let entries;
  try {
    entries = fs.readdirSync(abs, { withFileTypes: true });
  } catch {
    return { dirs, flatFiles };
  }
  for (const ent of entries) {
    const rel = path.posix.join(root, ent.name);
    if (ent.isDirectory()) {
      if (hasSourceFile(frontendDir, rel)) {
        dirs.push(rel);
      }
    } else if (isSourceFileName(ent.name)) {
      flatFiles.push(rel);
    }
  }
  return { dirs, flatFiles };
}

/**
 * checkCoverageConfig is the pure verifier: given the coverage dir lists and the
 * frontend dir they are relative to, it returns an array of human-readable error
 * strings (empty == config is sound). It performs no I/O beyond reading the source
 * tree under `frontendDir`, prints nothing, and never exits — the caller decides
 * how to report. Extracted as a pure function so the self-test can drive it
 * against a fixture tree.
 *
 * @param {{ include: readonly string[], perDirGlobs: readonly string[],
 *           excluded?: readonly string[], frontendDir: string }} opts
 *           `excluded` is COVERAGE_EXCLUDED (intentional-exclusion ledger);
 *           omitted ⇒ treated as empty.
 * @returns {string[]} error messages (each naming the offending entry/dir/file)
 */
export function checkCoverageConfig({ include, perDirGlobs, excluded = [], frontendDir }) {
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

  // --- normalize COVERAGE_INCLUDE + classify entries under a per-dir root ------
  // For each include entry under src/components/ or src/pages/, after normalizing
  // (check (e): reject `.`/`..` segments):
  //   - a single-segment SOURCE FILE (e.g. "src/components/ComingSoon.tsx") names a
  //     flat MEASURED file -> recorded in measuredFlatFiles (covers the disk file;
  //     flat files carry no per-dir floor, so they are NOT added to measuredDirs).
  //   - (d) a first segment containing a glob metachar (`*`/`**`/`*o`/...) measures
  //     more than one named immediate dir — an unsupported broad shape the lockstep
  //     check cannot derive a single dir from. REJECT (fail closed), naming the
  //     entry, rather than silently ignore it (the prior fail-open hole).
  //   - (f) a NARROW per-file include UNDER a dir (e.g. "src/components/Foo/Foo.tsx",
  //     where the segment after <Dir> is a filename/subpath, not `**`): Vitest would
  //     instrument ONLY that file while a sibling source (Foo/helper.ts) escapes BOTH
  //     instrumentation AND the disk-completeness check (which would see <Dir>
  //     "measured" as a whole). REJECT (fail closed): a measured per-dir include must
  //     be the WHOLE-DIRECTORY shape `<root>/<Dir>/**/...` so no sibling escapes.
  //   - otherwise it names an immediate dir (whole-dir glob, or a bare dir) -> add to
  //     the measured set for (b)/(c).
  const measuredDirs = new Set(); // "src/components/Foo"  (dir include entries)
  const measuredFlatFiles = new Set(); // "src/components/Foo.tsx" (flat include entries)
  for (const entry of include) {
    const norm = normalizeEntry(entry);
    if (norm.error) {
      errors.push(norm.error);
      continue;
    }
    const seg = firstSegmentUnderRoot(norm.value);
    if (seg === null) {
      continue; // not under a per-dir root (src/lib, src/api, src/viz, ...)
    }
    const rest = norm.value.slice(`${seg.root}/`.length);
    // A flat MEASURED file directly under the root: exactly one segment, a source
    // filename, no glob metachar. (Vitest measures it via its exact path.)
    if (!rest.includes('/') && !GLOB_META.test(rest) && isSourceFileName(rest)) {
      measuredFlatFiles.add(norm.value);
      continue;
    }
    if (!seg.first || GLOB_META.test(seg.first)) {
      errors.push(
        `coverage.include entry "${entry}" measures the whole "${seg.root}" root via a broad glob ` +
          `("${seg.first || '<empty>'}" as the first segment), not a single named immediate dir. ` +
          `The per-dir verifier cannot derive a lockstep-checkable dir from it, so a measured page/component ` +
          `would have NO enforced per-dir floor (a fail-open hole). Replace it with explicit per-dir entries ` +
          `(e.g. "${seg.root}/<Dir>/**/*.{ts,tsx}") so each measured dir has a per-dir floor, or extend this verifier first.`,
      );
      continue;
    }
    // (f) The first segment names a literal immediate dir. If the entry has MORE
    // segments after <Dir>, the one immediately after it MUST be the whole-dir glob
    // `**`. Anything else (a filename like "Foo.tsx" or a subpath like "sub/x.ts")
    // is a NARROW per-file include: Vitest instruments only that file, yet the
    // verifier would otherwise mark the whole <Dir> measured — so a sibling source
    // file escapes BOTH instrumentation and the disk-completeness check. Fail closed.
    const restSegments = rest.split('/');
    if (restSegments.length > 1 && restSegments[1] !== '**') {
      errors.push(
        `coverage.include entry "${entry}" names a specific file/subpath under "${seg.root}/${seg.first}" ` +
          `("${restSegments[1]}" after the dir, not "**") — the per-dir verifier requires a whole-directory ` +
          `include shape ("${seg.root}/${seg.first}/**/*.{ts,tsx}") so no sibling source escapes measurement. ` +
          `Narrow per-file includes under a measured dir are rejected; measure the whole dir, or list specific ` +
          `files in COVERAGE_EXCLUDED.`,
      );
      continue;
    }
    measuredDirs.add(`${seg.root}/${seg.first}`);
  }

  // --- normalize COVERAGE_EXCLUDED into the intentional-exclusion set ----------
  // Same normalization/validation as include (check (e)). Each entry is a clean
  // repo-relative dir OR flat-file path; compared by exact match against disk
  // paths in the disk-completeness pass below.
  const excludedSet = new Set();
  for (const entry of excluded) {
    const norm = normalizeEntry(entry);
    if (norm.error) {
      errors.push(norm.error);
      continue;
    }
    excludedSet.add(norm.value);
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

  // --- (c) disk-completeness: no shipped source escapes both lists ------------
  // Every immediate source dir, and every flat .ts/.tsx file, under each per-dir
  // root must be EITHER measured (a COVERAGE_INCLUDE entry covers it) OR listed in
  // COVERAGE_EXCLUDED. A source dir/file in NEITHER list silently escapes coverage
  // AND checks (a)/(b) — fail closed, naming it.
  for (const root of PER_DIR_ROOTS) {
    const { dirs, flatFiles } = enumerateRoot(frontendDir, root);
    for (const dir of dirs) {
      if (measuredDirs.has(dir) || excludedSet.has(dir)) {
        continue;
      }
      errors.push(
        `${dir} exists on disk but is in neither COVERAGE_INCLUDE nor COVERAGE_EXCLUDED — ` +
          `add tests + measure it (a "${dir}/**/*.{ts,tsx}" include entry + a "${dir}/**" per-dir floor), ` +
          `or add it to COVERAGE_EXCLUDED with a rationale.`,
      );
    }
    for (const file of flatFiles) {
      if (measuredFlatFiles.has(file) || excludedSet.has(file)) {
        continue;
      }
      errors.push(
        `${file} exists on disk but is in neither COVERAGE_INCLUDE nor COVERAGE_EXCLUDED — ` +
          `add tests + measure it (a "${file}" include entry), or add it to COVERAGE_EXCLUDED with a rationale.`,
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
  const { COVERAGE_INCLUDE, PER_DIR_GLOBS, COVERAGE_EXCLUDED } = await import('../vitest.coverage.mjs');
  const __dirname = path.dirname(__filename);
  const frontendDir = process.argv[2] ? path.resolve(process.argv[2]) : path.resolve(__dirname, '..');

  const errors = checkCoverageConfig({
    include: COVERAGE_INCLUDE,
    perDirGlobs: PER_DIR_GLOBS,
    excluded: COVERAGE_EXCLUDED,
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
  // Count only the lockstep-relevant measured DIRS (those under a per-dir root
  // whose first segment is a literal dir name AND whose shape is the whole-dir glob
  // or a bare dir — flat-file include entries, any broad shape, and any narrow
  // per-file include would already have been classified/errored inside the function;
  // this block runs only when there were zero errors, so it mirrors measuredDirs).
  let measured = 0;
  for (const entry of COVERAGE_INCLUDE) {
    const norm = normalizeEntry(entry);
    if (norm.error) {
      continue;
    }
    const seg = firstSegmentUnderRoot(norm.value);
    if (seg === null) {
      continue;
    }
    const rest = norm.value.slice(`${seg.root}/`.length);
    if (!rest.includes('/') && !GLOB_META.test(rest) && isSourceFileName(rest)) {
      continue; // flat-file entry — not a per-dir floor
    }
    const restSegments = rest.split('/');
    if (restSegments.length > 1 && restSegments[1] !== '**') {
      continue; // narrow per-file include under a dir — errored above, not a floor
    }
    if (seg.first && !GLOB_META.test(seg.first)) {
      measured += 1;
    }
  }
  process.stdout.write(
    `${GREEN}COVERAGE CONFIG: PASS${NC} (${PER_DIR_GLOBS.length} per-dir floors, all non-vacuous; ` +
      `${measured} measured component/page dirs, all with a per-dir floor; disk-complete: ` +
      `${COVERAGE_EXCLUDED.length} dir(s)/file(s) explicitly excluded)\n`,
  );
}
