#!/usr/bin/env bash
# Self-test for the NATIVE per-directory Vitest coverage gate configured in
# frontend/vitest.config.ts (quality-gates.md §Frontend — Unit/Component:
# ">= 80% lines per component directory under src/components/ and src/pages/").
#
# Why this exists
#   The per-directory floor is enforced by Vitest's native glob-keyed
#   `coverage.thresholds` (no wrapper script), so there is no gate *script* to
#   unit-test. But the CONFIG WIRING is still code that can silently rot: a
#   future edit that drops the glob keys, a Vitest major bump that changes the
#   threshold schema, or a glob group that matches ZERO files (whose lines pct is
#   "Unknown" and so vacuously PASSES) would all disable per-dir enforcement
#   while the suite still goes green. This test proves, hermetically, that
#   Vitest's glob-keyed threshold mechanism on THIS installed Vitest:
#     (1) FAILS the run (exit 1) and NAMES the offending dir when a per-dir glob
#         group is under its lines floor, and
#     (2) PASSES (exit 0) when the same group meets its floor,
#   mirroring the fail-closed discipline of scripts/check-bundle-size.test.sh.
#   It runs against a throwaway fixture project (its own source + test), so it is
#   independent of the app's real coverage numbers and never flakes when those
#   numbers move.
#
# Run: frontend/scripts/check-coverage-thresholds.test.sh   (exit 0 = all pass)
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FRONTEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
pass=0; fail=0

if ! command -v node >/dev/null 2>&1; then
  echo -e "${RED}[ERROR]${NC} node not found on PATH — required for the per-dir coverage self-test." >&2
  exit 2
fi

# Resolve the vitest binary from the frontend node_modules so the self-test
# exercises the SAME installed Vitest the real gate uses (not a global one).
VITEST_BIN="$FRONTEND_DIR/node_modules/.bin/vitest"
if [[ ! -x "$VITEST_BIN" ]]; then
  echo -e "${RED}[ERROR]${NC} vitest binary not found at $VITEST_BIN — run 'npm ci' in frontend/ first." >&2
  exit 2
fi

# The fixture project's `vitest.config.ts` does `import ... from 'vitest/config'`,
# so it must resolve frontend/node_modules. Node walks parent dirs for
# node_modules, so the fixture MUST live UNDER frontend/ (a sibling /tmp dir has
# no node_modules and the import fails). Create it inside frontend/ and remove it
# on exit; the dir name is unique per run so concurrent runs do not collide.
TMP="$(mktemp -d "$FRONTEND_DIR/.coverage-selftest.XXXXXX")"; trap 'rm -rf "$TMP"' EXIT

# --- Build a throwaway fixture project ---------------------------------------
# One source dir 'src/comp/Widget' with a function that has a covered branch and
# an UNCOVERED branch, so its line coverage is a known, fixed fraction (exactly
# 50% lines — 3 of 6 covered; the uncovered 'high' branch is 3 lines), and a test
# that exercises only the covered branch. The fixture's vitest config is generated
# per-case with a chosen per-dir threshold so we can drive it both clearly under
# and clearly over the 50% floor (no boundary-fragile equality cases).
mkdir -p "$TMP/src/comp/Widget"
cat > "$TMP/src/comp/Widget/widget.ts" <<'TS'
// Fixture source: classify() has two branches; the test below covers only the
// 'low' path, leaving the 'high' path's lines uncovered -> ~57% line coverage.
export function classify(n: number): string {
  const label = n < 10 ? 'low' : 'high';
  if (label === 'high') {
    const detail = 'over-ten';
    const note = 'big';
    return `${label}:${detail}:${note}`;
  }
  return label;
}
TS
cat > "$TMP/src/comp/Widget/widget.test.ts" <<'TS'
import { describe, it, expect } from 'vitest';
import { classify } from './widget';
describe('classify', () => {
  it('covers only the low branch', () => {
    expect(classify(3)).toBe('low');
  });
});
TS

# write_config <per-dir-threshold>
# Emits a minimal vitest config whose ONLY per-dir threshold group is the fixture
# dir 'src/comp/Widget/**', at the given lines floor. Mirrors the real config's
# glob-keyed shape so a schema regression there breaks here too.
write_config() {
  local thr="$1"
  cat > "$TMP/vitest.config.ts" <<TS
import { defineConfig } from 'vitest/config';
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text-summary'],
      include: ['src/comp/Widget/**/*.ts'],
      thresholds: {
        'src/comp/Widget/**': { lines: ${thr} },
      },
    },
  },
});
TS
}

# run_case <want-exit> <threshold> <desc> [grep-regex-required-in-output]
run_case() {
  local want="$1" thr="$2" desc="$3" needle="${4:-}" got=0
  write_config "$thr"
  ( cd "$TMP" && "$VITEST_BIN" run --coverage --root "$TMP" ) >"$TMP/out" 2>&1 || got=$?
  local ok=1
  [[ "$got" -eq "$want" ]] || ok=0
  if [[ -n "$needle" ]] && ! grep -qE "$needle" "$TMP/out"; then ok=0; fi
  if [[ "$ok" -eq 1 ]]; then
    echo -e "  ${GREEN}PASS${NC} (${desc}): exit ${got}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): want exit ${want}${needle:+ + /$needle/}, got exit ${got}"
    sed 's/^/      /' "$TMP/out"; fail=$((fail+1))
  fi
}

# (1) Impossible floor (95% on a 50% dir) -> FAIL (exit 1) AND the offending dir
#     glob is NAMED in the error. Proves per-dir enforcement is LIVE and fails
#     closed; the regex pins Vitest's documented threshold-error wording.
run_case 1 95 "per-dir group under floor fails + names the dir" \
  'ERROR: Coverage for lines .* does not meet .*src/comp/Widget'

# (2) Satisfiable floor (40% on a 50% dir) -> PASS (exit 0). Proves the same
#     wiring does NOT spuriously fail a dir that meets its floor (no false
#     positive), so a green per-dir run is meaningful.
run_case 0 40 "per-dir group at/above floor passes" ''

echo
if [[ "$fail" -eq 0 ]]; then
  echo -e "${GREEN}[ok]${NC} per-dir coverage-threshold self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} per-dir coverage-threshold self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
