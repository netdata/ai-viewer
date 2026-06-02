#!/usr/bin/env bash
# Benchmark regression gate (SOW-0011).
#
# Runs the marked Go benchmarks, compares them to bench/baseline.txt via
# `benchstat`, and FAILS on a > 20% sec/op regression for any benchmark.
#
# Policy:
#   - Only **sec/op** (wall-time) is gated. The custom ReportMetric values
#     (B/s, events/sec, peak_heap_mb, ...) are reported by benchstat for context
#     but are NOT gated: peak_heap_mb is benchtime-sensitive, and B/s is just the
#     inverse of sec/op. sec/op is the load-bearing perf signal.
#   - `-count=6` is benchstat's minimum for a 0.95 confidence interval, so the
#     baseline and every gate run use it; benchstat's significance filtering
#     (it prints "~" for changes inside the noise band) keeps a noisy benchmark
#     from tripping the gate.
#   - Baseline refresh requires an explicit SOW. This script has NO auto-update
#     mode — it never writes bench/baseline.txt.
#
# Usage:
#   scripts/check-bench.sh                 # run the benchmarks, compare to bench/baseline.txt
#   scripts/check-bench.sh BASE CURRENT    # compare two existing benchstat-input files (self-test)
# Exit: 0 = within threshold; 1 = a > 20% sec/op regression; 2 = usage/tooling error.
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; GRAY='\033[0;90m'; NC='\033[0m'
THRESH="${BENCH_THRESHOLD:-20}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Resolve benchstat (GOBIN, then GOPATH/bin, then PATH).
BENCHSTAT="$(go env GOBIN 2>/dev/null)/benchstat"
[ -x "$BENCHSTAT" ] || BENCHSTAT="$(go env GOPATH)/bin/benchstat"
[ -x "$BENCHSTAT" ] || BENCHSTAT="$(command -v benchstat 2>/dev/null || true)"
if [ -z "$BENCHSTAT" ] || [ ! -x "$BENCHSTAT" ]; then
  echo -e "${RED}[ERROR]${NC} benchstat not found — install: go install golang.org/x/perf/cmd/benchstat@latest" >&2
  exit 2
fi

# The 5 benchmark-bearing packages (canonical has no encode/decode benchmark).
BENCH_PKGS=(
  ./internal/adapters/aiagent_v2/
  ./internal/ingest/
  ./internal/presenter/
  ./internal/notify/
)

cleanup=""; trap '[ -n "$cleanup" ] && rm -f "$cleanup"' EXIT
if [ "$#" -eq 2 ]; then
  base="$1"; cur="$2"            # compare two existing files (self-test mode)
elif [ "$#" -eq 0 ]; then
  base="$REPO_ROOT/bench/baseline.txt"
  cur="$(mktemp)"; cleanup="$cur"
  echo -e "${GRAY}running benchmarks (-count=6) for the gate...${NC}" >&2
  ( cd "$REPO_ROOT" && go test -run='^$' -bench=. -benchmem -count=6 "${BENCH_PKGS[@]}" ) > "$cur"
else
  echo -e "${RED}[ERROR]${NC} usage: scripts/check-bench.sh [BASE CURRENT]" >&2
  exit 2
fi
[ -f "$base" ] || { echo -e "${RED}[ERROR]${NC} baseline not found: ${base}" >&2; exit 2; }
[ -s "$cur" ]  || { echo -e "${RED}[ERROR]${NC} current bench output is empty: ${cur}" >&2; exit 2; }

report="$("$BENCHSTAT" "$base" "$cur" 2>&1)" || { echo "$report" >&2; echo -e "${RED}[ERROR]${NC} benchstat failed" >&2; exit 2; }
echo "$report"

# Gate ONLY the sec/op metric block. benchstat emits one table per metric, each
# introduced by a "│ ... │ <metric>  vs base │" header. Inside the sec/op block,
# a regression shows as a signed "+NN.N%" token (improvements are "-NN.N%";
# noise-band changes are "~" with no signed token). Fail on any "+>THRESH%".
regress="$(printf '%s\n' "$report" | awk -v thr="$THRESH" '
  /vs base/ { insec = ($0 ~ /sec\/op/) ? 1 : 0; next }   # metric-table header: track only the sec/op block
  # Skip the per-block "geomean" aggregate: it always carries a point-estimate
  # delta (no significance marker), so a noisy benchmark can move it even when no
  # individual benchmark significantly regressed. Gate per-benchmark only.
  insec && $1 != "geomean" {
    # benchstat prints a signed "+NN.N%" token ONLY for a statistically-significant
    # change (insignificant ones are "~"), so matching "+NN.N%" gates exactly the
    # significant regressions.
    for (i = 1; i <= NF; i++) if ($i ~ /^\+[0-9]+(\.[0-9]+)?%$/) {
      d = $i; gsub(/[+%]/, "", d)
      if (d + 0 > thr) printf "  %s  +%s%% sec/op\n", $1, d
    }
  }')"

if [ -n "$regress" ]; then
  echo -e "${RED}BENCH GATE: FAIL${NC} (sec/op regression > ${THRESH}%):" >&2
  echo "$regress" >&2
  echo -e "${GRAY}If intentional, refresh bench/baseline.txt via an explicit SOW — do not silently widen the gate.${NC}" >&2
  exit 1
fi
echo -e "${GREEN}BENCH GATE: PASS${NC} (no sec/op regression > ${THRESH}%)"
