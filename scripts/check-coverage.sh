#!/usr/bin/env bash
# Enforce Go STATEMENT-coverage thresholds from a coverage profile.
#
# Authoritative gate: .agents/sow/specs/quality-gates.md "Go — Coverage".
#
# Policy (SOW-0010 re-scope, 2026-06-02):
#   - Metric: statement coverage (Go's `-covermode=atomic`). Go has no
#     first-class BRANCH coverage; the branch threshold is deferred (see spec).
#   - Gated set: every NON-`/cmd/` package (the unit-testable core, i.e.
#     `internal/*`) must be >= THRESHOLD, and their aggregate must be >= THRESHOLD.
#   - Excluded: any package whose import path contains `/cmd/` — the binaries
#     (`cmd/ai-viewer-{ingest,serve}`: main()/flag/signal wiring, covered by
#     Playwright E2E + embed-smoke + cmd binary tests) and the dev-only tools
#     (`internal/adapters/aiagent_v2/cmd/{genfixtures,backfillbench}`). These are
#     reported for visibility but not gated. New-code-in-PR coverage is a
#     separate deferred follow-up SOW.
#
# Usage: scripts/check-coverage.sh [coverage.out]   (default: ./coverage.out)
#        COVERAGE_THRESHOLD=80 overrides the percent (default 80).
# Exit: 0 = all gated packages + aggregate >= threshold; 1 = a gated miss;
#       2 = usage/profile error.
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; GRAY='\033[0;90m'; NC='\033[0m'

PROFILE="${1:-coverage.out}"
THRESHOLD="${COVERAGE_THRESHOLD:-80}"

if [[ ! -f "$PROFILE" ]]; then
  echo -e "${RED}[ERROR]${NC} coverage profile not found: ${PROFILE}" >&2
  echo "        Produce it first: scripts/test.sh (writes coverage.out)." >&2
  exit 2
fi

# Parse the coverage profile directly. Line format (after the `mode:` header):
#   <importpath>/<file>.go:<sLine>.<sCol>,<eLine>.<eCol> <numStmts> <count>
# Sum numStmts per package (covered when count>0); a package is EXCLUDED from
# the gate iff its path contains "/cmd/".
awk -v thr="$THRESHOLD" '
  NR==1 && $1=="mode:" { next }
  NF<3 { next }
  {
    split($1, a, ":"); file=a[1]
    pkg=file; sub(/\/[^\/]*\.go$/, "", pkg)   # dirname = import path of the package
    stmts=$2+0; cnt=$3+0
    tot[pkg]+=stmts
    if (cnt>0) cov[pkg]+=stmts
    seen[pkg]=1
    excl=(index(pkg,"/cmd/")>0)
    if (!excl) { gtot+=stmts; if (cnt>0) gcov+=stmts }
  }
  END {
    fail=0
    gpct = (gtot>0) ? gcov*100.0/gtot : 100.0
    printf "Gated aggregate (non-/cmd/): %.1f%% (%d/%d stmts)  [threshold %d%%]\n", gpct, gcov, gtot, thr
    if (gpct+0.0499 < thr) { printf "  %sFAIL%s gated aggregate %.1f%% < %d%%\n", "\033[0;31m","\033[0m", gpct, thr; fail=1 }
    print  "Per-package:"
    for (p in seen) {
      pct = (tot[p]>0) ? cov[p]*100.0/tot[p] : 100.0
      excl=(index(p,"/cmd/")>0)
      tag = excl ? "excl" : "gate"
      printf "  [%s] %6.1f%%  %s\n", tag, pct, p
      if (!excl && pct+0.0499 < thr) {
        printf "       %sFAIL%s %s at %.1f%% needs >= %d%% (short %.1f points)\n", "\033[0;31m","\033[0m", p, pct, thr, thr-pct
        fail=1
      }
    }
    if (fail) { printf "%sCOVERAGE GATE: FAIL%s\n","\033[0;31m","\033[0m"; exit 1 }
    printf "%sCOVERAGE GATE: PASS%s (every gated package + aggregate >= %d%% statements)\n","\033[0;32m","\033[0m", thr
  }
' "$PROFILE"
