#!/usr/bin/env bash
# Self-test for the EXPORTED pure verifier in
# frontend/scripts/check-coverage-config.mjs (SOW-0012 review R3-1). The CLI half
# of that module verifies the REAL Vitest coverage config; this test exercises
# its decision LOGIC hermetically, against a throwaway fixture source tree, so a
# regression in the verifier (a check that stops firing) is caught even though
# the real config is currently sound.
#
# It drives `checkCoverageConfig({ include, perDirGlobs, excluded, frontendDir })`
# via a tiny inline ESM harness (node --input-type=module reading the case as JSON
# on argv) and asserts the function:
#   (a) VACUITY    — returns an error for a per-dir glob whose dir is missing/empty.
#   (b) LOCKSTEP   — returns an error for a measured component/page dir that has
#                    no matching per-dir glob.
#   (c) BROAD GLOB — returns an error for a broad `src/pages/**/*.{ts,tsx}` include
#                    entry (the unsupported whole-root shape the lockstep check
#                    cannot derive a dir from — a fail-open hole, now fail-closed).
#   (d) .d.ts ONLY — returns a (vacuity) error for a dir whose only .ts file is a
#                    `*.d.ts` declaration (no measured/executable source).
#   (e) CLEAN      — returns NO errors for a sound fixture config (every source
#                    dir/flat-file is measured or excluded).
#   (f) DISK-DIR   — returns an error for a source DIR on disk in NEITHER
#                    COVERAGE_INCLUDE nor COVERAGE_EXCLUDED (disk-completeness).
#   (g) DISK-FILE  — returns an error for a flat source FILE on disk in neither list.
#   (h) EXCLUDED   — a dir/file present in COVERAGE_EXCLUDED is accounted for (no
#                    disk-completeness error).
#   (i) NORM-GLOB  — returns broad-shape errors for normalized broad first segments
#                    (`./src/pages/**/*.{ts,tsx}` and `src/pages/*o/**`).
#   (j) BAD-SEG    — returns a malformed-entry error for a `.`/`..` path segment.
#   (k) NARROW-DIR — returns a named error for a per-file include UNDER a measured
#                    dir (e.g. `src/components/Foo/Foo.tsx`): not the canonical
#                    whole-dir shape, so Vitest would instrument only that file
#                    while a sibling (`helper.ts`) escapes BOTH instrumentation and
#                    the disk-completeness check. The verifier requires the EXACT
#                    canonical shape (`<root>/<Dir>/**/*.{ts,tsx}`).
#   (l) EXT-NARROW — returns a named error for a recursive-but-extension-narrowed
#                    per-dir include (`src/components/Foo/**/*.tsx`): the `.ts`
#                    siblings of `Foo` would escape instrumentation while the dir is
#                    marked measured. Only the canonical `**/*.{ts,tsx}` is accepted.
#   (m) FILE-NARROW— returns a named error for a recursive narrow-FILENAME per-dir
#                    include (`src/components/Foo/**/Foo.tsx`): only files named
#                    `Foo.tsx` anywhere under the dir are instrumented; the sibling
#                    `helper.ts` escapes. Rejected as not the canonical shape.
#   (n) BARE-DIR   — returns a named error for a BARE directory include
#                    (`src/components/Foo`, no glob): Vitest does not recursively
#                    instrument the whole dir from a bare path; the verifier would
#                    otherwise mark the dir measured. Rejected as not the canonical
#                    shape (an exact match cannot be narrowed — the tightest rule).
#   (o) CANON-OK   — the canonical whole-dir shape (`src/components/Good/**/*.{ts,tsx}`)
#                    is ACCEPTED with no error (proves the exact rule does not over-reject).
#   (p) BARE-FLOOR — returns a named error for a PER_DIR_GLOBS entry that is a BARE
#                    dir (`src/components/Foo`, no trailing `/**`): Vitest's threshold
#                    KEY must be `<Dir>/**` to match any file path, so a bare-dir key
#                    matches NOTHING and that per-dir floor is VACUOUS (parallel to the
#                    vacuous-include class, on the threshold list). The dir is on disk
#                    with a canonical COVERAGE_INCLUDE; the verifier rejects the floor
#                    shape AND (since a rejected entry never enters `gatedDirs`) the
#                    lockstep check reports the dir ungated — 2 errors, defense-in-depth
#                    (parallel to case (a)). The verifier requires the EXACT threshold
#                    shape `<root>/<Dir>/**` for every PER_DIR_GLOBS entry.
#   (q) FLOOR-DOTSLASH / (r) FLOOR-DBLSLASH / (s) FLOOR-TRAILSLASH (R8-1) — a
#                    PER_DIR_GLOBS entry that is canonical only AFTER normalization:
#                    a leading `./` (`./src/components/Foo/**`), a repeated `//`
#                    (`src/components//Foo/**`), or a trailing `/`
#                    (`src/components/Foo/**/`). vitest.config.ts passes the RAW
#                    threshold key to Vitest, and Vitest's picomatch matcher runs on
#                    that RAW key against clean `relative(root,file)` paths — `//` and
#                    a trailing `/` match NOTHING (vacuous floor), and even the
#                    `./`-tolerant case is non-canonical + fragile across
#                    picomatch/tinyglobby versions. So the verifier must compare the
#                    RAW string, requiring `String(entry) === "<root>/<Dir>/**"`, and
#                    REJECT a string that only becomes canonical after normalization
#                    (normalization is still used to DETECT `.`/`..` and to derive the
#                    dir, but must not LAUNDER a raw entry into passing). Each → a
#                    named floor-shape rejection (the dir is excluded + unmeasured, so
#                    the rejection is the ONLY error).
#   (t) INC-DOTSLASH / (u) INC-DBLSLASH / (v) INC-TRAILSLASH (R8-1) — the same three
#                    non-canonical forms on a COVERAGE_INCLUDE whole-dir entry
#                    (`./src/components/Foo/**/*.{ts,tsx}`, `src/components//Foo/...`,
#                    `src/components/Foo/**/*.{ts,tsx}/`). coverage.include is consumed
#                    RAW by Vitest's tinyglobby selector; relying on its incidental
#                    tolerance of `./`//`//`/trailing-`/` is fragile, so the verifier
#                    requires the RAW include string to be EXACTLY the canonical
#                    whole-dir shape `<root>/<Dir>/**/*.{ts,tsx}`. Each → a named
#                    whole-dir-shape rejection (Foo excluded, no floor, so the
#                    rejection is the ONLY error).
#   (w) REV-LOCKSTEP (R8-2) — a PER_DIR_GLOBS floor for a dir that is NOT measured:
#                    the dir is in COVERAGE_EXCLUDED and has a canonical
#                    `<dir>/**` floor but NO COVERAGE_INCLUDE entry. The floor passes
#                    shape + non-vacuity (the dir has source) so it enters `gatedDirs`,
#                    but its Vitest threshold group is a no-op because the dir is not
#                    instrumented (absent from the coverage map). The forward lockstep
#                    (measured⊆gated) cannot catch this; the REVERSE lockstep
#                    (gated⊆measured) must — making gatedDirs === measuredDirs. → a
#                    named "floor for an unmeasured dir" rejection (the ONLY error).
# Mirrors the fail-closed discipline + ANSI/printf style of
# frontend/scripts/check-bundle-size.test.sh.
#
# The fixture tree is built under frontend/ (mktemp there) so the harness's
# relative import of ../check-coverage-config.mjs resolves the same way it does
# for the real CLI; it is removed on exit and the dir name is unique per run so
# concurrent runs do not collide.
#
# Run: frontend/scripts/check-coverage-config.test.sh   (exit 0 = all assertions pass)
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERIFIER="${SCRIPT_DIR}/check-coverage-config.mjs"
pass=0; fail=0

if ! command -v node >/dev/null 2>&1; then
  echo -e "${RED}[ERROR]${NC} node not found on PATH — required to run the coverage-config verifier self-test." >&2
  exit 2
fi
if [[ ! -f "$VERIFIER" ]]; then
  echo -e "${RED}[ERROR]${NC} verifier module not found at $VERIFIER" >&2
  exit 2
fi

# Fixture root under frontend/ so the harness's relative module import resolves.
FRONTEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TMP="$(mktemp -d "$FRONTEND_DIR/.coverage-config-selftest.XXXXXX")"; trap 'rm -rf "$TMP"' EXIT

# Build a fixture source tree. Each immediate dir under src/components|pages is a
# distinct case so the verifier sees a realistic shape:
#   Good/        — a normal measured+gated dir (has real source).
#   OnlyDecl/    — has ONLY a .d.ts (no measured source) -> (d) vacuity; NOT
#                  enumerated by the disk-completeness check (no measured source).
#   OnlyTest/    — has ONLY a *.test.ts (no measured source) -> also vacuity-class;
#                  likewise NOT enumerated by disk-completeness.
#   Unlisted/    — has real source; used by the disk-completeness DIR case (when it
#                  is in NEITHER list -> error) and the EXCLUDED case.
#   Page/        — a normal measured+gated page dir (has real source).
#   Foo/         — has TWO real source files (Foo.tsx + a sibling helper.ts); used
#                  by the narrow-per-file-include rejection case (k): a
#                  `Foo/Foo.tsx` include names only one file, leaving helper.ts to
#                  escape both instrumentation and disk-completeness.
#   flat.tsx     — a FLAT source file directly under src/components/ (no dir); used
#                  by the flat-file disk-completeness + flat-file-include cases.
mkdir -p "$TMP/src/components/Good" "$TMP/src/components/OnlyDecl" "$TMP/src/components/OnlyTest" "$TMP/src/components/Unlisted" "$TMP/src/components/Foo" "$TMP/src/pages/Page"
printf 'export const good = 1;\n'                 > "$TMP/src/components/Good/good.ts"
printf 'export declare const onlyDecl: number;\n' > "$TMP/src/components/OnlyDecl/types.d.ts"
printf "import { it } from 'vitest'; it('x', () => {});\n" > "$TMP/src/components/OnlyTest/only.test.ts"
printf 'export const unlisted = 1;\n'             > "$TMP/src/components/Unlisted/unlisted.ts"
printf 'export const foo = 1;\n'                  > "$TMP/src/components/Foo/Foo.tsx"
printf 'export const helper = 2;\n'               > "$TMP/src/components/Foo/helper.ts"
printf 'export const page = 1;\n'                 > "$TMP/src/pages/Page/page.tsx"
printf 'export const flat = 1;\n'                 > "$TMP/src/components/flat.tsx"

# run_harness <include-json-array> <perDirGlobs-json-array> <excluded-json-array>
# Prints to stdout: first line "ERRORS=<n>", then one line per error message.
# Exit always 0 (the harness reports, the bash assertions judge).
HARNESS='
import { checkCoverageConfig } from "'"$VERIFIER"'";
// node -e inserts NO script path: argv[0] is node, argv[1] is the first user arg.
const [includeJson, globsJson, excludedJson, frontendDir] = process.argv.slice(1);
const errors = checkCoverageConfig({
  include: JSON.parse(includeJson),
  perDirGlobs: JSON.parse(globsJson),
  excluded: JSON.parse(excludedJson),
  frontendDir,
});
process.stdout.write("ERRORS=" + errors.length + "\n");
for (const e of errors) process.stdout.write(e + "\n");
'
run_harness() {
  node --input-type=module -e "$HARNESS" "$1" "$2" "$3" "$TMP"
}

# assert <want-error-count> <include-json> <globs-json> <excluded-json> <desc> [needle-regex]
# Asserts the verifier returns exactly <want-error-count> errors; if a needle is
# given, at least one error must match it (pins WHICH check fired, not just count).
assert() {
  local want="$1" inc="$2" globs="$3" excl="$4" desc="$5" needle="${6:-}"
  local out got ok=1
  out="$(run_harness "$inc" "$globs" "$excl")"
  got="$(printf '%s\n' "$out" | sed -n 's/^ERRORS=//p')"
  [[ "$got" -eq "$want" ]] || ok=0
  if [[ -n "$needle" ]] && ! printf '%s\n' "$out" | grep -qE "$needle"; then ok=0; fi
  if [[ "$ok" -eq 1 ]]; then
    echo -e "  ${GREEN}PASS${NC} (${desc}): ${got} error(s)"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): want ${want} error(s)${needle:+ + /$needle/}, got ${got}"
    printf '%s\n' "$out" | sed 's/^/      /'; fail=$((fail+1))
  fi
}

# JSON list literals reused below. The enumerated source surface of the fixture
# tree is: dirs Good, Unlisted, Foo, Page and the flat file src/components/flat.tsx
# (OnlyDecl/OnlyTest have no measured source and are NOT enumerated). For each
# case, every enumerated item must be measured (include) OR excluded, else the
# disk-completeness check fires — so cases not testing disk-completeness EXCLUDE
# the items they do not measure to keep the case isolated to its one defect.
CLEAN_INCLUDE='["src/components/Good/**/*.{ts,tsx}","src/pages/Page/**/*.{ts,tsx}"]'
CLEAN_GLOBS='["src/components/Good/**","src/pages/Page/**"]'
# Everything in the enumerated surface EXCEPT Good+Page (which the clean include
# measures): used so the CLEAN case is disk-complete.
CLEAN_EXCLUDED='["src/components/Unlisted","src/components/Foo","src/components/flat.tsx"]'
# The full enumerated surface as an excluded list — lets a case that measures
# nothing-relevant stay disk-complete (used to isolate non-disk-completeness
# defects). Good+Page included here too where a case does not measure them.
ALL_EXCLUDED='["src/components/Good","src/components/Unlisted","src/components/Foo","src/components/flat.tsx","src/pages/Page"]'

# (e) CLEAN — sound config: every per-dir glob non-vacuous, every measured dir
#     gated, no broad shapes, every source dir/file measured-or-excluded -> ZERO
#     errors. (Asserted first: a green baseline.)
assert 0 "$CLEAN_INCLUDE" "$CLEAN_GLOBS" "$CLEAN_EXCLUDED" "clean fixture config -> no errors"

# (a) VACUITY — a per-dir glob over a dir that does not exist on disk, also
#     measured by an include entry. BOTH checks fire (the per-dir vacuity error
#     AND the include-side "measures X but no source" error) — 2 errors,
#     defense-in-depth; we pin the vacuity wording. (Page/Unlisted/flat excluded so
#     disk-completeness stays silent and the case isolates the vacuity defect.)
assert 2 \
  '["src/components/Good/**/*.{ts,tsx}","src/components/Ghost/**/*.{ts,tsx}"]' \
  '["src/components/Good/**","src/components/Ghost/**"]' \
  '["src/pages/Page","src/components/Unlisted","src/components/Foo","src/components/flat.tsx"]' \
  "(a) per-dir glob over a missing dir -> vacuity error" \
  'matches ZERO source files'

# (b) LOCKSTEP — a measured component dir (Good) with NO per-dir glob. Include it
#     but omit it from PER_DIR_GLOBS; it has real source, so it must be flagged as
#     missing a per-dir floor. (Other source items excluded to isolate the defect.)
assert 1 \
  '["src/components/Good/**/*.{ts,tsx}"]' \
  '[]' \
  '["src/pages/Page","src/components/Unlisted","src/components/Foo","src/components/flat.tsx"]' \
  "(b) measured dir absent from PER_DIR_GLOBS -> lockstep error" \
  'has NO per-dir line floor'

# (c) BROAD GLOB — an include entry that measures the whole src/pages root via
#     `**` as its first segment. Unsupported shape -> fail-closed error naming it.
#     (All source items excluded -> this is the ONLY error.)
assert 1 \
  '["src/pages/**/*.{ts,tsx}"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(c) broad src/pages/**/*.{ts,tsx} include -> unsupported-shape error" \
  'measures the whole "src/pages" root'

# (d) .d.ts ONLY — OnlyDecl has only a *.d.ts file (no measured source). A per-dir
#     glob over it must be vacuity-flagged exactly like a missing dir (the .d.ts is
#     NOT counted as source). Included too, so BOTH the per-dir vacuity error and
#     the include-side stale-measure error fire (2) — we pin the vacuity wording,
#     which proves the .d.ts exclusion (without it, this dir would look sourced).
#     (OnlyDecl is not enumerated by disk-completeness; the real source items are
#     excluded to isolate the defect.)
assert 2 \
  '["src/components/OnlyDecl/**/*.{ts,tsx}"]' \
  '["src/components/OnlyDecl/**"]' \
  "$ALL_EXCLUDED" \
  "(d) dir whose only .ts is a .d.ts -> vacuity error (not counted as source)" \
  'matches ZERO source files'

# (d2) .test.ts ONLY — same vacuity class via a test-only dir (no measured source);
#     likewise both checks fire (2).
assert 2 \
  '["src/components/OnlyTest/**/*.{ts,tsx}"]' \
  '["src/components/OnlyTest/**"]' \
  "$ALL_EXCLUDED" \
  "(d2) dir whose only .ts is a *.test.ts -> vacuity error" \
  'matches ZERO source files'

# (f) DISK-DIR — Unlisted has real source but is in NEITHER list (measured Good+Page,
#     excluded only flat). The disk-completeness check must flag it by name. (Good+
#     Page measured, flat excluded -> Unlisted is the ONLY uncovered item.)
assert 1 \
  "$CLEAN_INCLUDE" \
  "$CLEAN_GLOBS" \
  '["src/components/Foo","src/components/flat.tsx"]' \
  "(f) source dir in neither list -> disk-completeness error names it" \
  'Unlisted exists on disk but is in neither COVERAGE_INCLUDE nor COVERAGE_EXCLUDED'

# (g) DISK-FILE — the flat file src/components/flat.tsx is in neither list (measured
#     Good+Page, excluded only Unlisted). Disk-completeness must flag the flat file.
assert 1 \
  "$CLEAN_INCLUDE" \
  "$CLEAN_GLOBS" \
  '["src/components/Unlisted","src/components/Foo"]' \
  "(g) flat source file in neither list -> disk-completeness error names it" \
  'src/components/flat.tsx exists on disk but is in neither COVERAGE_INCLUDE nor COVERAGE_EXCLUDED'

# (h) EXCLUDED — both Unlisted (dir) and flat.tsx (flat file) are in COVERAGE_EXCLUDED;
#     with Good+Page measured the config is disk-complete -> ZERO errors. Proves the
#     exclusion ledger satisfies disk-completeness (the escape hatch works).
assert 0 \
  "$CLEAN_INCLUDE" \
  "$CLEAN_GLOBS" \
  "$CLEAN_EXCLUDED" \
  "(h) dir + flat file in COVERAGE_EXCLUDED -> no disk-completeness error"

# (i) NORM-GLOB (leading ./) — a broad include entry written with a leading "./"
#     must normalize to the bare path and STILL be rejected as a broad whole-root
#     shape (proves normalization happens before classification). (All excluded.)
assert 1 \
  '["./src/pages/**/*.{ts,tsx}"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(i) normalized leading-./ broad glob -> unsupported-shape error" \
  'measures the whole "src/pages" root'

# (i2) NORM-GLOB (metachar first segment) — `src/pages/*o/**` has a glob metachar in
#     its FIRST segment, so it cannot name a single immediate dir; rejected as broad
#     (proves the check is metachar-based, not just the exact `*`/`**` strings).
assert 1 \
  '["src/pages/*o/**"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(i2) metachar-in-first-segment glob (src/pages/*o/**) -> unsupported-shape error" \
  'measures the whole "src/pages" root'

# (j) BAD-SEG — an include entry with a ".." path segment is malformed (ambiguous to
#     match on disk) -> a named malformed-entry error, fail closed. (All excluded so
#     this is the ONLY error.)
assert 1 \
  '["src/components/../evil/**/*.{ts,tsx}"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(j) entry with a .. path segment -> malformed-entry error" \
  'contains a "\.\." path segment'

# (k) NARROW-DIR — a per-FILE include UNDER a measured dir (Foo/Foo.tsx names one
#     file, not the whole dir). Vitest would instrument only Foo.tsx, leaving the
#     sibling Foo/helper.ts to escape BOTH instrumentation and the
#     disk-completeness check (which would see Foo "measured"). The verifier must
#     reject it (named) and require the EXACT canonical shape. (All source excluded
#     so the narrow-shape rejection is the ONLY error.)
assert 1 \
  '["src/components/Foo/Foo.tsx"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(k) per-file include under a measured dir (Foo/Foo.tsx) -> narrow-shape error" \
  'must be the canonical whole-directory include shape'

# (l) EXT-NARROW — a recursive-but-extension-narrowed include: `Foo/**/*.tsx` is the
#     whole-dir glob but for `.tsx` ONLY, so the sibling `Foo/helper.ts` escapes
#     Vitest instrumentation while disk-completeness marks Foo measured. The exact
#     rule rejects anything but the canonical `**/*.{ts,tsx}` (the brace is a
#     superset that already covers `.ts`-only dirs). (All source excluded.)
assert 1 \
  '["src/components/Foo/**/*.tsx"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(l) extension-narrowed include (Foo/**/*.tsx drops .ts siblings) -> narrow-shape error" \
  'must be the canonical whole-directory include shape'

# (m) FILE-NARROW — a recursive narrow-FILENAME include: `Foo/**/Foo.tsx` instruments
#     only files named Foo.tsx anywhere under Foo, so `helper.ts` escapes. Rejected
#     as not the canonical shape. (All source excluded.)
assert 1 \
  '["src/components/Foo/**/Foo.tsx"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(m) narrow-filename include (Foo/**/Foo.tsx) -> narrow-shape error" \
  'must be the canonical whole-directory include shape'

# (n) BARE-DIR — a BARE directory path (`src/components/Foo`, no glob). Vitest does
#     NOT recursively instrument the whole dir from a bare path, yet the verifier
#     would otherwise mark Foo measured. Rejected as not the canonical shape (an
#     exact match is the tightest possible rule). (All source excluded.)
assert 1 \
  '["src/components/Foo"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(n) bare dir include (src/components/Foo, no glob) -> narrow-shape error" \
  'must be the canonical whole-directory include shape'

# (o) CANON-OK — the canonical whole-dir shape is ACCEPTED with ZERO errors (proves
#     the exact rule does not OVER-reject the one legitimate per-dir include shape).
#     Good is measured + gated; every other enumerated source item is excluded so
#     the only thing under test is that the canonical shape passes.
assert 0 \
  '["src/components/Good/**/*.{ts,tsx}"]' \
  '["src/components/Good/**"]' \
  '["src/components/Unlisted","src/components/Foo","src/components/flat.tsx","src/pages/Page"]' \
  "(o) canonical whole-dir include (Good/**/*.{ts,tsx}) -> accepted, no error"

# (p) BARE-FLOOR — a PER_DIR_GLOBS entry that is a BARE dir (`src/components/Foo`,
#     no trailing `/**`). Vitest's threshold KEY must end in `/**` to match file
#     paths, so a bare-dir key matches NOTHING -> that per-dir floor is VACUOUS.
#     Foo is on disk and has its canonical COVERAGE_INCLUDE entry. BOTH checks fire
#     (2 errors, defense-in-depth, parallel to case (a)): the (a) floor-shape error
#     rejects the bare-dir key AND — because a rejected entry is NOT added to
#     `gatedDirs` — the (b) lockstep error reports Foo as measured-but-ungated. We
#     pin the floor-shape wording (the primary defect under test). (Every other
#     enumerated source item is excluded so these are the ONLY errors.)
assert 2 \
  '["src/components/Foo/**/*.{ts,tsx}"]' \
  '["src/components/Foo"]' \
  '["src/components/Good","src/components/Unlisted","src/components/flat.tsx","src/pages/Page"]' \
  "(p) bare-dir PER_DIR_GLOBS floor (src/components/Foo, no /**) -> vacuous-floor error" \
  'must be the canonical per-dir threshold shape'

# (q/r/s) RAW-NON-CANONICAL PER_DIR_GLOBS floors (R8-1) — each floor is canonical
#     only AFTER normalization. vitest.config.ts hands the RAW key to Vitest, whose
#     picomatch matcher runs on the RAW string: `//` and a trailing `/` match NOTHING
#     (vacuous floor); the `./`-tolerant form is still non-canonical + fragile. The
#     verifier must compare the RAW string and reject anything that only canonicalizes
#     after normalization. Foo is EXCLUDED and NOT measured (empty include), so the
#     floor-shape rejection is the ONLY error in each case (no lockstep, no vacuity:
#     check (a) rejects on shape before the hasSource probe). Empirically (picomatch
#     vs `src/components/Foo/good.tsx`): `./…` matches=true but `//…`/`…/**/` match=false.
assert 1 \
  '[]' \
  '["./src/components/Foo/**"]' \
  "$ALL_EXCLUDED" \
  "(q) raw leading-./ PER_DIR_GLOBS floor (./src/components/Foo/**) -> raw-canonical floor error" \
  'must be the canonical per-dir threshold shape'
assert 1 \
  '[]' \
  '["src/components//Foo/**"]' \
  "$ALL_EXCLUDED" \
  "(r) raw repeated-// PER_DIR_GLOBS floor (src/components//Foo/**) -> raw-canonical floor error" \
  'must be the canonical per-dir threshold shape'
assert 1 \
  '[]' \
  '["src/components/Foo/**/"]' \
  "$ALL_EXCLUDED" \
  "(s) raw trailing-/ PER_DIR_GLOBS floor (src/components/Foo/**/) -> raw-canonical floor error" \
  'must be the canonical per-dir threshold shape'

# (t/u/v) RAW-NON-CANONICAL COVERAGE_INCLUDE whole-dir entries (R8-1) — same three
#     forms on an include entry. coverage.include is consumed RAW by Vitest's
#     tinyglobby selector; relying on its incidental tolerance of these forms is
#     fragile, so the verifier requires the RAW include to be EXACTLY the canonical
#     whole-dir shape `<root>/<Dir>/**/*.{ts,tsx}`. Foo is EXCLUDED with NO floor, so
#     the whole-dir-shape rejection is the ONLY error (rejected include never enters
#     measuredDirs; empty PER_DIR_GLOBS means no reverse-lockstep).
assert 1 \
  '["./src/components/Foo/**/*.{ts,tsx}"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(t) raw leading-./ COVERAGE_INCLUDE (./src/components/Foo/**/*.{ts,tsx}) -> raw-canonical include error" \
  'must be the canonical whole-directory include shape'
assert 1 \
  '["src/components//Foo/**/*.{ts,tsx}"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(u) raw repeated-// COVERAGE_INCLUDE (src/components//Foo/**/*.{ts,tsx}) -> raw-canonical include error" \
  'must be the canonical whole-directory include shape'
assert 1 \
  '["src/components/Foo/**/*.{ts,tsx}/"]' \
  '[]' \
  "$ALL_EXCLUDED" \
  "(v) raw trailing-/ COVERAGE_INCLUDE (src/components/Foo/**/*.{ts,tsx}/) -> raw-canonical include error" \
  'must be the canonical whole-directory include shape'

# (w) REVERSE-LOCKSTEP (R8-2) — a canonical per-dir floor for a dir that is NOT
#     measured. Unlisted has real source and a `src/components/Unlisted/**` floor (so
#     it passes shape + non-vacuity and enters gatedDirs), is in COVERAGE_EXCLUDED,
#     and has NO COVERAGE_INCLUDE entry — so its Vitest threshold group is a no-op
#     (the dir is not instrumented). The forward lockstep (measured⊆gated) cannot see
#     it; the reverse lockstep (gated⊆measured) must flag it, making gatedDirs ===
#     measuredDirs. (All other source items excluded -> this is the ONLY error.)
assert 1 \
  '[]' \
  '["src/components/Unlisted/**"]' \
  "$ALL_EXCLUDED" \
  "(w) per-dir floor for an excluded/unmeasured dir (Unlisted) -> reverse-lockstep error" \
  'has a per-dir floor for "src/components/Unlisted" but that dir is not measured'

echo
if [ "$fail" -eq 0 ]; then
  echo -e "${GREEN}[ok]${NC} check-coverage-config self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} check-coverage-config self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
