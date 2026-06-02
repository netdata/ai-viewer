#!/usr/bin/env bash
# Run the Go test suite with the race detector + statement-coverage profiling.
#
# Authoritative gates: .agents/sow/specs/quality-gates.md "Go — Tests",
# "Go — Coverage", "Go — Race Stress". Coverage thresholds are enforced by the
# sibling scripts/check-coverage.sh (run it after this).
#
# Usage:
#   scripts/test.sh                # go test -race -count=1 + coverage.out
#   scripts/test.sh --stress [N]   # race stress: -count=N (default 10), no profile
#
# Repo-relative; no secrets. Go's coverage is STATEMENT coverage (atomic mode);
# Go has no first-class branch coverage (see quality-gates.md "Go — Coverage").
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; GRAY='\033[0;90m'; NC='\033[0m'
run() {
  printf >&2 "${GRAY}$(pwd) >${NC} "; printf >&2 "${YELLOW}"; printf >&2 "%q " "$@"; printf >&2 "${NC}\n"
  local ec=0; "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then echo -e >&2 "${RED}[ERROR]${NC} exit ${ec}: $*"; return "$ec"; fi
}

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

case "${1:-}" in
  "") : ;;  # normal mode (coverage profile)
  --stress)
    count="${2:-10}"
    if ! [[ "$count" =~ ^[1-9][0-9]*$ ]]; then
      echo -e "${RED}[ERROR]${NC} --stress needs a positive integer count (got '${count}')." >&2
      exit 2
    fi
    if [[ "$#" -gt 2 ]]; then
      echo -e "${RED}[ERROR]${NC} --stress takes at most one argument (count); unexpected: ${*:3} (usage: scripts/test.sh [--stress [N]])." >&2
      exit 2
    fi
    echo -e "${GRAY}race stress: -count=${count} (no coverage profile)${NC}" >&2
    run go test -race "-count=${count}" ./...
    echo -e "${GREEN}[ok]${NC} race stress (-count=${count}) clean." >&2
    exit 0
    ;;
  *)
    echo -e "${RED}[ERROR]${NC} unknown argument: ${1} (usage: scripts/test.sh [--stress [N]])." >&2
    exit 2
    ;;
esac

run go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
echo -e "${GRAY}coverage profile written: coverage.out${NC}" >&2
run go tool cover -func=coverage.out | tail -1
echo -e "${GREEN}[ok]${NC} tests pass (race-clean); enforce thresholds with scripts/check-coverage.sh." >&2
