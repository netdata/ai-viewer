#!/usr/bin/env bash
# Self-test for the EXPORTED pure verifier in
# frontend/scripts/check-coverage-config.mjs (SOW-0012 review R3-1). The CLI half
# of that module verifies the REAL Vitest coverage config; this test exercises
# its decision LOGIC hermetically, against a throwaway fixture source tree, so a
# regression in the verifier (a check that stops firing) is caught even though
# the real config is currently sound.
#
# It drives `checkCoverageConfig({ include, perDirGlobs, frontendDir })` via a
# tiny inline ESM harness (node --input-type=module reading the case as JSON on
# argv) and asserts the function:
#   (a) VACUITY    — returns an error for a per-dir glob whose dir is missing/empty.
#   (b) LOCKSTEP   — returns an error for a measured component/page dir that has
#                    no matching per-dir glob.
#   (c) BROAD GLOB — returns an error for a broad `src/pages/**/*.{ts,tsx}` include
#                    entry (the unsupported whole-root shape the lockstep check
#                    cannot derive a dir from — a fail-open hole, now fail-closed).
#   (d) .d.ts ONLY — returns a (vacuity) error for a dir whose only .ts file is a
#                    `*.d.ts` declaration (no measured/executable source).
#   (e) CLEAN      — returns NO errors for a sound fixture config.
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
#   OnlyDecl/    — has ONLY a .d.ts (no measured source) -> (d) vacuity.
#   OnlyTest/    — has ONLY a *.test.ts (no measured source) -> also vacuity-class.
mkdir -p "$TMP/src/components/Good" "$TMP/src/components/OnlyDecl" "$TMP/src/components/OnlyTest" "$TMP/src/pages/Page"
printf 'export const good = 1;\n'                 > "$TMP/src/components/Good/good.ts"
printf 'export declare const onlyDecl: number;\n' > "$TMP/src/components/OnlyDecl/types.d.ts"
printf "import { it } from 'vitest'; it('x', () => {});\n" > "$TMP/src/components/OnlyTest/only.test.ts"
printf 'export const page = 1;\n'                 > "$TMP/src/pages/Page/page.tsx"

# run_harness <include-json-array> <perDirGlobs-json-array>
# Prints to stdout: first line "ERRORS=<n>", then one line per error message.
# Exit always 0 (the harness reports, the bash assertions judge).
HARNESS='
import { checkCoverageConfig } from "'"$VERIFIER"'";
// node -e inserts NO script path: argv[0] is node, argv[1] is the first user arg.
const [includeJson, globsJson, frontendDir] = process.argv.slice(1);
const errors = checkCoverageConfig({
  include: JSON.parse(includeJson),
  perDirGlobs: JSON.parse(globsJson),
  frontendDir,
});
process.stdout.write("ERRORS=" + errors.length + "\n");
for (const e of errors) process.stdout.write(e + "\n");
'
run_harness() {
  node --input-type=module -e "$HARNESS" "$1" "$2" "$TMP"
}

# assert <want-error-count> <include-json> <globs-json> <desc> [needle-regex]
# Asserts the verifier returns exactly <want-error-count> errors; if a needle is
# given, at least one error must match it (pins WHICH check fired, not just count).
assert() {
  local want="$1" inc="$2" globs="$3" desc="$4" needle="${5:-}"
  local out got ok=1
  out="$(run_harness "$inc" "$globs")"
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

# JSON list literals reused below. Good is the only sound dir; Page is sound too.
CLEAN_INCLUDE='["src/components/Good/**/*.{ts,tsx}","src/pages/Page/**/*.{ts,tsx}"]'
CLEAN_GLOBS='["src/components/Good/**","src/pages/Page/**"]'

# (e) CLEAN — sound config: every per-dir glob non-vacuous, every measured dir
#     gated, no broad shapes -> ZERO errors. (Asserted first: a green baseline.)
assert 0 "$CLEAN_INCLUDE" "$CLEAN_GLOBS" "clean fixture config -> no errors"

# (a) VACUITY — a per-dir glob over a dir that does not exist on disk, also
#     measured by an include entry. BOTH checks fire (the per-dir vacuity error
#     AND the include-side "measures X but no source" error) — 2 errors,
#     defense-in-depth; we pin the vacuity wording.
assert 2 \
  '["src/components/Good/**/*.{ts,tsx}","src/components/Ghost/**/*.{ts,tsx}"]' \
  '["src/components/Good/**","src/components/Ghost/**"]' \
  "(a) per-dir glob over a missing dir -> vacuity error" \
  'matches ZERO source files'

# (b) LOCKSTEP — a measured component dir (Good) with NO per-dir glob. Include it
#     but omit it from PER_DIR_GLOBS; it has real source, so it must be flagged as
#     missing a per-dir floor.
assert 1 \
  '["src/components/Good/**/*.{ts,tsx}"]' \
  '[]' \
  "(b) measured dir absent from PER_DIR_GLOBS -> lockstep error" \
  'has NO per-dir line floor'

# (c) BROAD GLOB — an include entry that measures the whole src/pages root via
#     `**` as its first segment. Unsupported shape -> fail-closed error naming it.
#     (No per-dir glob involved, so this is the ONLY error.)
assert 1 \
  '["src/pages/**/*.{ts,tsx}"]' \
  '[]' \
  "(c) broad src/pages/**/*.{ts,tsx} include -> unsupported-shape error" \
  'measures the whole "src/pages" root'

# (d) .d.ts ONLY — OnlyDecl has only a *.d.ts file (no measured source). A per-dir
#     glob over it must be vacuity-flagged exactly like a missing dir (the .d.ts is
#     NOT counted as source). Included too, so BOTH the per-dir vacuity error and
#     the include-side stale-measure error fire (2) — we pin the vacuity wording,
#     which proves the .d.ts exclusion (without it, this dir would look sourced).
assert 2 \
  '["src/components/OnlyDecl/**/*.{ts,tsx}"]' \
  '["src/components/OnlyDecl/**"]' \
  "(d) dir whose only .ts is a .d.ts -> vacuity error (not counted as source)" \
  'matches ZERO source files'

# (d2) .test.ts ONLY — same vacuity class via a test-only dir (no measured source);
#     likewise both checks fire (2).
assert 2 \
  '["src/components/OnlyTest/**/*.{ts,tsx}"]' \
  '["src/components/OnlyTest/**"]' \
  "(d2) dir whose only .ts is a *.test.ts -> vacuity error" \
  'matches ZERO source files'

echo
if [ "$fail" -eq 0 ]; then
  echo -e "${GREEN}[ok]${NC} check-coverage-config self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} check-coverage-config self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
