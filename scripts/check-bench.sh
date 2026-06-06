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

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; GRAY='\033[0;90m'; NC='\033[0m'
THRESH="${BENCH_THRESHOLD:-20}"
# The 20% sec/op threshold is the documented contract (quality-gates.md). The
# env override exists only for local experimentation; warn loudly so it can
# never be a quiet way to land a regressing change.
if [ "$THRESH" != "20" ]; then
  echo -e "${YELLOW}[warn]${NC} BENCH_THRESHOLD=${THRESH} overrides the contract 20% sec/op gate — for local experimentation only, never to land a regression." >&2
fi
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Resolve benchstat (GOBIN, then GOPATH/bin, then PATH).
BENCHSTAT="$(go env GOBIN 2>/dev/null)/benchstat"
[ -x "$BENCHSTAT" ] || BENCHSTAT="$(go env GOPATH)/bin/benchstat"
[ -x "$BENCHSTAT" ] || BENCHSTAT="$(command -v benchstat 2>/dev/null || true)"
if [ -z "$BENCHSTAT" ] || [ ! -x "$BENCHSTAT" ]; then
  echo -e "${RED}[ERROR]${NC} benchstat not found — install: go install golang.org/x/perf/cmd/benchstat@v0.0.0-20260512194132-3cf34090a3db" >&2
  exit 2
fi

# The 6 benchmark-bearing packages, 9 benchmarks (Scan + Tail share aiagent_v2,
# Claude-code Scan + Tail share claude_code, and Codex Scan + Tail share codex;
# canonical has no encode/decode benchmark).
BENCH_PKGS=(
  ./internal/adapters/aiagent_v2/
  ./internal/adapters/claude_code/
  ./internal/adapters/codex/
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

# Vacuous-pass guard. benchstat emits a "vs base" sec/op comparison row ONLY for
# a benchmark present in BOTH files under the SAME goos/goarch/pkg/cpu config. If
# the current run renamed/dropped a benchmark, or the two files land in disjoint
# config groups, that benchmark gets NO comparison row — and the regression scan
# below would then silently see "no regression" and PASS, certifying nothing.
# So: require every benchmark in the BASELINE to have a sec/op comparison row;
# any missing one is a tooling error (exit 2), never a pass. The baseline is the
# source of truth for what MUST be compared (auto-syncs on baseline refresh).
# NOTE: matching is by benchmark NAME (package/config stripped). It assumes names
# are unique across packages — true today (Claude* adapter benchmarks,
# aiagent_v2 Scan/Tail, BatchInsert, SessionsListQuery, HubFanout are distinct).
# A future duplicate name in two packages could mask a dropped comparison; keep
# benchmark names unique.
expected="$(grep -E '^Benchmark' "$base" | awk '{print $1}' | sed -E 's/^Benchmark//; s/-[0-9]+$//' | sort -u)"
# A benchmark counts as "compared" ONLY if its sec/op row carries the vs-base
# verdict "(p=… n=…)" — benchstat emits that only when BOTH files contributed
# samples. A one-sided row (benchmark present in just one file) prints a single
# measurement with NO "(p=", so it must NOT count as compared.
compared="$(printf '%s\n' "$report" | awk '
  /vs base/ { insec = ($0 ~ /sec\/op/) ? 1 : 0; next }
  insec && $1 != "geomean" && $1 ~ /^[A-Za-z]/ && /\(p=/ { name = $1; sub(/-[0-9]+$/, "", name); print name }' | sort -u)"
if [ -n "$expected" ]; then
  missing="$(comm -23 <(printf '%s\n' "$expected") <(printf '%s\n' "$compared"))"
  if [ -n "$missing" ]; then
    echo -e "${RED}[ERROR]${NC} benchstat produced no sec/op comparison for:" >&2
    while IFS= read -r bench; do
      printf '  %s\n' "$bench" >&2
    done <<< "$missing"
    echo -e "${RED}        baseline and current are disjoint (different goos/goarch/pkg/cpu config, a renamed/removed benchmark) — the gate cannot certify 'no regression'.${NC}" >&2
    exit 2
  fi
fi

# Reverse direction: WARN (do not fail) when the CURRENT run has a benchmark
# absent from the baseline — it is silently un-gated until a baseline-refresh SOW
# adds it. A new benchmark cannot regress (no baseline to compare against), so
# this is a heads-up, not a gate failure.
current="$(grep -E '^Benchmark' "$cur" | awk '{print $1}' | sed -E 's/^Benchmark//; s/-[0-9]+$//' | sort -u)"
newbench="$(comm -13 <(printf '%s\n' "$expected") <(printf '%s\n' "$current"))"
if [ -n "$newbench" ]; then
  echo -e "${YELLOW}[warn]${NC} current run has benchmark(s) absent from the baseline (un-gated until a baseline-refresh SOW):" >&2
  while IFS= read -r bench; do
    printf '  %s\n' "$bench" >&2
  done <<< "$newbench"
fi

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
