#!/usr/bin/env bash
# Self-test for scripts/check-coverage.sh using synthetic coverage profiles
# with known per-package statement counts. Proves the gate exits non-zero on a
# gated (non-/cmd/) miss and zero when all gated packages + the aggregate pass,
# including that /cmd/ packages (binaries + nested dev-tools) are excluded.
#
# Run: scripts/test/check-coverage-test.sh   (exit 0 = all assertions pass)
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECK="${REPO_ROOT}/scripts/check-coverage.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
M="github.com/netdata/ai-viewer"
pass=0; fail=0

# assert <expected-exit> <profile> <description>
assert() {
  local want="$1" prof="$2" desc="$3" got=0
  COVERAGE_THRESHOLD=80 bash "$CHECK" "$prof" >"$TMP/lastout" 2>&1 || got=$?
  if [[ "$got" -eq "$want" ]]; then
    echo -e "  ${GREEN}PASS${NC} ($desc): exit $got"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} ($desc): expected exit $want, got $got"; fail=$((fail+1))
  fi
}

# 1) All gated packages >= 80% (foo exactly 80%, bar 100%) → PASS (exit 0).
cat > "$TMP/pass.out" <<EOF
mode: atomic
$M/internal/foo/a.go:1.1,3.10 8 1
$M/internal/foo/a.go:5.1,6.5 2 0
$M/internal/bar/b.go:1.1,4.2 10 3
EOF
assert 0 "$TMP/pass.out" "all gated >= 80%"

# 2) A gated internal package below 80% (baz 70%) → FAIL (exit 1).
cat > "$TMP/fail.out" <<EOF
mode: atomic
$M/internal/baz/c.go:1.1,3.10 7 1
$M/internal/baz/c.go:5.1,6.5 3 0
EOF
assert 1 "$TMP/fail.out" "gated internal/baz at 70%"

# 3) /cmd/ packages excluded: a 20% binary + a 0% nested dev-tool, with the only
#    gated package at 100% → PASS (exit 0).
cat > "$TMP/cmd.out" <<EOF
mode: atomic
$M/internal/foo/a.go:1.1,3.10 10 1
$M/cmd/ai-viewer-serve/main.go:1.1,3.10 2 1
$M/cmd/ai-viewer-serve/main.go:5.1,9.5 8 0
$M/internal/adapters/aiagent_v2/cmd/genfixtures/main.go:1.1,9.5 10 0
EOF
assert 0 "$TMP/cmd.out" "/cmd/ binary + nested dev-tool excluded"

# 4) Gated aggregate below 80%. (Per-package-all-pass mathematically implies
#    aggregate-pass, so a pure aggregate-only miss cannot exist; this fixture
#    fails BOTH per-package (qux 0%) and the aggregate — the extra grep below
#    proves the aggregate line actually fires, not just the per-package one.)
cat > "$TMP/agg.out" <<EOF
mode: atomic
$M/internal/foo/a.go:1.1,2.2 2 1
$M/internal/qux/d.go:1.1,9.9 100 0
EOF
assert 1 "$TMP/agg.out" "gated aggregate + per-package below 80%"
if grep -q 'gated aggregate' "$TMP/lastout" && grep -q '< 80%' "$TMP/lastout"; then
  echo -e "  ${GREEN}PASS${NC} (aggregate-fail line fires): the aggregate check ran"; pass=$((pass+1))
else
  echo -e "  ${RED}FAIL${NC} (aggregate-fail line missing from output)"; fail=$((fail+1))
fi

# 5) Missing profile → usage error (exit 2).
assert 2 "$TMP/does-not-exist.out" "missing profile"

echo
if [[ "$fail" -eq 0 ]]; then
  echo -e "${GREEN}[ok]${NC} check-coverage self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} check-coverage self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
