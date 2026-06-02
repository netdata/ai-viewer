#!/usr/bin/env bash
# Self-test for scripts/check-bench.sh. Feeds synthetic benchstat-input files (no
# real benchmarks run) and asserts the gate FAILS on a > 20% sec/op regression and
# PASSES within threshold and on an improvement. The gate must be correct — it is
# itself code.
#
# Run: scripts/test/check-bench-test.sh   (exit 0 = all assertions pass)
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECK="${REPO_ROOT}/scripts/check-bench.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0

# Write 6 BenchmarkFoo samples at the given ns/op values (slight spread so
# benchstat can compute a 0.95 CI).
mkbench() {
  local f="$1"; shift
  { echo "pkg: selftest"; for v in "$@"; do echo "BenchmarkFoo-16   10000   ${v} ns/op   100 B/op   2 allocs/op"; done; } > "$f"
}

# mkbench_named writes ONE benchmark block (explicit pkg + name + samples) with a
# full goos/goarch/pkg/cpu header — for the vacuous-pass guard cases below.
mkbench_named() {
  local f="$1" pkg="$2" name="$3"; shift 3
  { echo "goos: linux"; echo "goarch: amd64"; echo "pkg: ${pkg}"; echo "cpu: selftest"
    for v in "$@"; do echo "Benchmark${name}-16   10000   ${v} ns/op   100 B/op   2 allocs/op"; done; } > "$f"
}

# mkbench_two writes TWO benchmark blocks (Foo + Bar) under one pkg.
mkbench_two() {
  local f="$1" pkg="$2"
  { echo "goos: linux"; echo "goarch: amd64"; echo "pkg: ${pkg}"; echo "cpu: selftest"
    for v in 100 101 99 100 102 98; do echo "BenchmarkFoo-16   10000   ${v} ns/op   100 B/op   2 allocs/op"; done
    for v in 200 201 199 200 202 198; do echo "BenchmarkBar-16   10000   ${v} ns/op   100 B/op   2 allocs/op"; done; } > "$f"
}

# assert <want-exit> <base> <cur> <desc>
assert() {
  local want="$1" base="$2" cur="$3" desc="$4" got=0
  bash "$CHECK" "$base" "$cur" >"$TMP/out" 2>&1 || got=$?
  if [ "$got" -eq "$want" ]; then
    echo -e "  ${GREEN}PASS${NC} (${desc}): exit ${got}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): want exit ${want}, got ${got}"; sed 's/^/      /' "$TMP/out"; fail=$((fail+1))
  fi
}

mkbench "$TMP/base"    100 101  99 100 102  98
mkbench "$TMP/regress" 130 131 129 130 132 128   # +30% sec/op -> must FAIL
mkbench "$TMP/within"  110 111 109 110 112 108   # +10% sec/op -> within 20%, PASS
mkbench "$TMP/improve"  80  81  79  80  82  78   # -20% sec/op -> improvement, PASS

assert 1 "$TMP/base" "$TMP/regress" "+30% sec/op regression"
assert 0 "$TMP/base" "$TMP/within"  "+10% within threshold"
assert 0 "$TMP/base" "$TMP/improve" "improvement (faster)"
# missing file -> usage/tool error (exit 2)
assert 2 "$TMP/base" "$TMP/does-not-exist" "missing current file"

# Vacuous-pass guard: the gate must NEVER pass by comparing nothing.
#  - a baseline benchmark missing from the current run (renamed/removed) ->
#    benchstat prints a one-sided row with no "(p=...)" verdict -> exit 2.
mkbench_two   "$TMP/base_two" selftest
mkbench_named "$TMP/cur_one"  selftest Foo 100 101 99 100 102 98
assert 2 "$TMP/base_two" "$TMP/cur_one" "baseline benchmark dropped from current -> vacuous guard"
#  - disjoint config groups (different pkg) -> benchstat compares nothing -> exit 2.
mkbench_named "$TMP/base_pkgA" pkgA Foo 100 101 99 100 102 98
mkbench_named "$TMP/cur_pkgB"  pkgB Foo 100 101 99 100 102 98
assert 2 "$TMP/base_pkgA" "$TMP/cur_pkgB" "disjoint config groups -> vacuous guard"

echo
if [ "$fail" -eq 0 ]; then
  echo -e "${GREEN}[ok]${NC} check-bench self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} check-bench self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
