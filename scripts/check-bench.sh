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
#   - `-cpu=1` pins the benchmark CPU list for these serial hot-path checks
#     instead of inheriting workstation-wide scheduler noise.
#   - `-p=1` serializes package benchmark binaries so the gate does not create
#     cross-package contention.
#   - Real benchmark attempts fail closed with exit 2 before collecting samples
#     when the workstation is too busy or load/CPU preflight evidence is
#     unavailable.
#   - Baseline refresh requires an explicit SOW. This script has NO auto-update
#     mode — it never writes bench/baseline.txt.
#
# Usage:
#   scripts/check-bench.sh                 # run go test -p=1 -run='^$' -bench=. -benchmem -count=6 -cpu=1, compare to bench/baseline.txt
#   scripts/check-bench.sh BASE CURRENT    # compare two existing benchstat-input files (self-test)
# Exit: 0 = within threshold; 1 = a reproduced > 20% sec/op regression; 2 = usage/tooling, busy-host, or unavailable-preflight error.
# busy-host exit 2 and unavailable-preflight exit 2 happen before sample collection.
set -euo pipefail
export LC_ALL=C

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; GRAY='\033[0;90m'; NC='\033[0m'
THRESH="${BENCH_THRESHOLD:-20}"
BENCH_COUNT="6"
BENCH_CPU="1"
BENCH_PKG_PARALLELISM="1"
BENCH_MAX_LOAD_PER_EFFECTIVE_CPU="0.50"
# The 20% sec/op threshold is the documented contract (quality-gates.md). The
# env override exists only for local experimentation; warn loudly so it can
# never be a quiet way to land a regressing change.
if [ "$THRESH" != "20" ]; then
  echo -e "${YELLOW}[warn]${NC} BENCH_THRESHOLD=${THRESH} overrides the contract 20% sec/op gate — for local experimentation only, never to land a regression." >&2
fi
# Package parallelism and busy-host load are physical validity constraints, not
# operator-tunable knobs. Keep them as hard assignments even if same-named
# environment variables are present; only BENCH_THRESHOLD remains overridable for
# loud local experiments.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Resolve benchstat (GOBIN, then GOPATH/bin, then PATH).
BENCHSTAT="$(go env GOBIN 2>/dev/null)/benchstat"
[ -x "$BENCHSTAT" ] || BENCHSTAT="$(go env GOPATH)/bin/benchstat"
[ -x "$BENCHSTAT" ] || BENCHSTAT="$(command -v benchstat 2>/dev/null || true)"
if [ -z "$BENCHSTAT" ] || [ ! -x "$BENCHSTAT" ]; then
  echo -e "${RED}[ERROR]${NC} benchstat not found — install: go install golang.org/x/perf/cmd/benchstat@v0.0.0-20260512194132-3cf34090a3db" >&2
  exit 2
fi

# The 7 benchmark-bearing packages, 11 benchmarks (Scan + Tail share aiagent_v2,
# Claude-code Scan + Tail share claude_code, Codex Scan + Tail share codex, and
# Opencode Scan + Tail share opencode; canonical has no encode/decode benchmark).
BENCH_PKGS=(
  ./internal/adapters/aiagent_v2/
  ./internal/adapters/claude_code/
  ./internal/adapters/codex/
  ./internal/adapters/opencode/
  ./internal/ingest/
  ./internal/presenter/
  ./internal/notify/
)

effective_gomaxprocs() {
  local probe out
  probe="$(mktemp "${TMPDIR:-/tmp}/ai-viewer-gomaxprocs.XXXXXX.go")" || { printf 'unavailable'; return; }
  {
    printf '%s\n' 'package main'
    printf '%s\n' 'import ('
    printf '%s\n' '  "fmt"'
    printf '%s\n' '  "runtime"'
    printf '%s\n' ')'
    printf '%s\n' 'func main() { fmt.Println(runtime.GOMAXPROCS(0)) }'
  } > "$probe" || { rm -f "$probe"; printf 'unavailable'; return; }
  out="$(go run "$probe" 2>/dev/null)" || out="unavailable"
  rm -f "$probe"
  printf '%s' "$out"
}

loadavg_source() {
  if [ -n "${BENCH_LOADAVG_FILE:-}" ] && [ "${BENCH_SELF_TEST:-}" != "1" ]; then
    echo -e "${RED}[ERROR]${NC} BENCH_LOADAVG_FILE requires BENCH_SELF_TEST=1; this fixture hook is only for hermetic self-tests." >&2
    return 2
  fi
  if [ "${BENCH_SELF_TEST:-}" = "1" ]; then
    if [ -z "${BENCH_LOADAVG_FILE:-}" ]; then
      echo -e "${RED}[ERROR]${NC} BENCH_SELF_TEST=1 requires BENCH_LOADAVG_FILE; hermetic benchmark self-test must not read host /proc/loadavg." >&2
      return 2
    fi
    printf '%s' "$BENCH_LOADAVG_FILE"
    return 0
  fi
  printf '%s' '/proc/loadavg'
}

is_nonnegative_float() {
  awk -v value="$1" 'BEGIN { exit (value ~ /^([0-9]+([.][0-9]*)?|[.][0-9]+)$/) ? 0 : 1 }'
}

is_positive_integer() {
  case "$1" in
    ''|0|*[!0-9]*) return 1 ;;
    *) return 0 ;;
  esac
}

read_loadavg_source() {
  local source="$1" one five fifteen _rest
  if [ ! -e "$source" ]; then
    echo -e "${RED}[ERROR]${NC} benchmark loadavg source unavailable: ${source} (missing)." >&2
    echo -e "${GRAY}        This Linux workstation benchmark gate needs readable load evidence; wait for a quieter window or fix the load source.${NC}" >&2
    return 2
  fi
  if [ ! -f "$source" ] || [ ! -r "$source" ]; then
    echo -e "${RED}[ERROR]${NC} benchmark loadavg source unavailable: ${source} (not a readable file)." >&2
    echo -e "${GRAY}        This Linux workstation benchmark gate needs readable load evidence; wait for a quieter window or fix the load source.${NC}" >&2
    return 2
  fi
  read -r one five fifteen _rest < "$source" || {
    echo -e "${RED}[ERROR]${NC} benchmark loadavg source invalid: ${source} (expected at least three fields)." >&2
    return 2
  }
  if [ -z "${one:-}" ] || [ -z "${five:-}" ] || [ -z "${fifteen:-}" ]; then
    echo -e "${RED}[ERROR]${NC} benchmark loadavg source invalid: ${source} (expected at least three fields)." >&2
    return 2
  fi
  if ! is_nonnegative_float "$one"; then
    echo -e "${RED}[ERROR]${NC} benchmark loadavg source invalid: ${source} (first field must be a non-negative number)." >&2
    return 2
  fi
  if ! is_nonnegative_float "$five"; then
    echo -e "${RED}[ERROR]${NC} benchmark loadavg source invalid: ${source} (second field must be a non-negative number)." >&2
    return 2
  fi
  if ! is_nonnegative_float "$fifteen"; then
    echo -e "${RED}[ERROR]${NC} benchmark loadavg source invalid: ${source} (third field must be a non-negative number)." >&2
    return 2
  fi

  LOADAVG_SOURCE="$source"
  LOADAVG_ONE="$one"
  LOADAVG_FIVE="$five"
  LOADAVG_FIFTEEN="$fifteen"
}

compute_load_threshold() {
  awk -v gomax="$1" -v max="$BENCH_MAX_LOAD_PER_EFFECTIVE_CPU" 'BEGIN { printf "%.2f", gomax * max }'
}

load_at_or_above_threshold() {
  awk -v one_minute_load="$1" -v threshold="$2" 'BEGIN { exit (one_minute_load >= threshold) ? 0 : 1 }'
}

preflight_benchmark_host() {
  local attempt="$1" source gomax threshold
  source="$(loadavg_source)" || return 2
  read_loadavg_source "$source" || return 2

  gomax="$(effective_gomaxprocs)"
  if ! is_positive_integer "$gomax"; then
    echo -e "${RED}[ERROR]${NC} effective GOMAXPROCS unavailable: ${gomax}" >&2
    echo -e "${GRAY}        Cannot validate workstation load for benchmark attempt ${attempt}; wait for a quieter window or fix the effective CPU probe.${NC}" >&2
    return 2
  fi

  threshold="$(compute_load_threshold "$gomax")"
  PREFLIGHT_LOAD_SOURCE="$LOADAVG_SOURCE"
  PREFLIGHT_LOAD_ONE="$LOADAVG_ONE"
  PREFLIGHT_LOAD_FIVE="$LOADAVG_FIVE"
  PREFLIGHT_LOAD_FIFTEEN="$LOADAVG_FIFTEEN"
  PREFLIGHT_GOMAX="$gomax"
  PREFLIGHT_THRESHOLD="$threshold"

  if load_at_or_above_threshold "$PREFLIGHT_LOAD_ONE" "$PREFLIGHT_THRESHOLD"; then
    echo -e "${RED}[ERROR]${NC} benchmark host too busy for valid wall-time evidence before attempt ${attempt}." >&2
    printf '        loadavg source: %s\n' "$PREFLIGHT_LOAD_SOURCE" >&2
    printf '        loadavg 1m: %s\n' "$PREFLIGHT_LOAD_ONE" >&2
    printf '        loadavg (1m 5m 15m): %s %s %s\n' "$PREFLIGHT_LOAD_ONE" "$PREFLIGHT_LOAD_FIVE" "$PREFLIGHT_LOAD_FIFTEEN" >&2
    printf '        effective GOMAXPROCS: %s\n' "$PREFLIGHT_GOMAX" >&2
    printf '        busy-host max load per effective CPU: %s\n' "$BENCH_MAX_LOAD_PER_EFFECTIVE_CPU" >&2
    printf '        busy-host threshold: %s\n' "$PREFLIGHT_THRESHOLD" >&2
    printf '        benchmark -p: %s\n' "$BENCH_PKG_PARALLELISM" >&2
    printf '        comparison: %s >= %s\n' "$PREFLIGHT_LOAD_ONE" "$PREFLIGHT_THRESHOLD" >&2
    echo -e "${GRAY}        action: wait for a quieter window or request approval to stop the exact workload causing contention.${NC}" >&2
    return 2
  fi
}

display_path() {
  case "$1" in
    "$REPO_ROOT"/*) printf '%s' "${1#"$REPO_ROOT"/}" ;;
    *) printf '%s' "$1" ;;
  esac
}

emit_benchmark_diagnostics() {
  local attempt="$1" base="$2" cur="$3" go_version
  go_version="$(go version 2>/dev/null || true)"
  [ -n "$go_version" ] || go_version="unavailable"

  echo -e "${GRAY}benchmark diagnostics (attempt ${attempt}):${NC}" >&2
  printf '  go version: %s\n' "$go_version" >&2
  printf '  effective GOMAXPROCS: %s\n' "$PREFLIGHT_GOMAX" >&2
  printf '  benchmark -cpu: %s\n' "$BENCH_CPU" >&2
  printf '  benchmark -p: %s\n' "$BENCH_PKG_PARALLELISM" >&2
  printf '  busy-host max load per effective CPU: %s\n' "$BENCH_MAX_LOAD_PER_EFFECTIVE_CPU" >&2
  printf '  busy-host load threshold: %s\n' "$PREFLIGHT_THRESHOLD" >&2
  printf '  comparison: %s < %s\n' "$PREFLIGHT_LOAD_ONE" "$PREFLIGHT_THRESHOLD" >&2
  printf '  benchmark packages:\n' >&2
  printf '    %s\n' "${BENCH_PKGS[@]}" >&2
  printf '  baseline path: %s\n' "$(display_path "$base")" >&2
  printf '  current path: %s\n' "$(display_path "$cur")" >&2
  printf '  loadavg source: %s\n' "$PREFLIGHT_LOAD_SOURCE" >&2
  printf '  loadavg (1m 5m 15m): %s %s %s\n' "$PREFLIGHT_LOAD_ONE" "$PREFLIGHT_LOAD_FIVE" "$PREFLIGHT_LOAD_FIFTEEN" >&2
}

cleanup_files=()
trap '[ "${#cleanup_files[@]}" -eq 0 ] || rm -f "${cleanup_files[@]}"' EXIT

benchmark_names_from_file() {
  awk '
    /^Benchmark/ {
      name = $1
      sub(/^Benchmark/, "", name)
      sub(/-[0-9]+$/, "", name)
      seen[name] = 1
    }
    END {
      for (name in seen) print name
    }
  ' "$1" | sort -u
}

require_valid_baseline() {
  local base="$1"
  [ -f "$base" ] || { echo -e "${RED}[ERROR]${NC} baseline not found: ${base}" >&2; return 2; }
  [ -s "$base" ] || { echo -e "${RED}[ERROR]${NC} baseline is empty: ${base}" >&2; return 2; }

  local benches
  benches="$(benchmark_names_from_file "$base")"
  [ -n "$benches" ] || { echo -e "${RED}[ERROR]${NC} baseline has no Benchmark rows: ${base}" >&2; return 2; }
}

regression_names() {
  printf '%s\n' "$1" | awk 'NF >= 2 { print $1 }' | sort -u
}

filter_regressions_by_names() {
  local regress="$1" names="$2"
  awk '
    NR == FNR { wanted[$1] = 1; next }
    NF >= 2 && ($1 in wanted) { print }
  ' <(printf '%s\n' "$names") <(printf '%s\n' "$regress")
}

run_real_bench_attempt() {
  local attempt="$1" base="$2" cur status
  preflight_benchmark_host "$attempt" || return 2
  cur="$(mktemp)"
  cleanup_files+=("$cur")
  emit_benchmark_diagnostics "$attempt" "$base" "$cur"
  echo -e "${GRAY}running benchmarks attempt ${attempt} (-count=${BENCH_COUNT}, -cpu=${BENCH_CPU}, -p=${BENCH_PKG_PARALLELISM}) for the gate...${NC}" >&2
  if ( cd "$REPO_ROOT" && go test -p="$BENCH_PKG_PARALLELISM" -run='^$' -bench=. -benchmem -count="$BENCH_COUNT" -cpu="$BENCH_CPU" "${BENCH_PKGS[@]}" ) > "$cur"; then
    REAL_BENCH_CUR="$cur"
    return 0
  else
    status="$?"
  fi
  echo -e "${RED}[ERROR]${NC} benchmark command failed on attempt ${attempt} (go test exit ${status})" >&2
  return 2
}

compare_bench_files() {
  local base="$1" cur="$2" benchstat_base="$3" real_mode="$4" report expected compared missing current newbench regress

  require_valid_baseline "$base" || return 2
  [ -s "$cur" ]  || { echo -e "${RED}[ERROR]${NC} current bench output is empty: ${cur}" >&2; return 2; }

  if [ "$real_mode" -eq 1 ]; then
    report="$(cd "$REPO_ROOT" && "$BENCHSTAT" "$benchstat_base" "$cur" 2>&1)" || { echo "$report" >&2; echo -e "${RED}[ERROR]${NC} benchstat failed" >&2; return 2; }
  else
    report="$("$BENCHSTAT" "$benchstat_base" "$cur" 2>&1)" || { echo "$report" >&2; echo -e "${RED}[ERROR]${NC} benchstat failed" >&2; return 2; }
  fi
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
  expected="$(benchmark_names_from_file "$base")"
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
      return 2
    fi
  fi

  # Reverse direction: WARN (do not fail) when the CURRENT run has a benchmark
  # absent from the baseline — it is silently un-gated until a baseline-refresh SOW
  # adds it. A new benchmark cannot regress (no baseline to compare against), so
  # this is a heads-up, not a gate failure.
  current="$(benchmark_names_from_file "$cur")"
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
    COMPARE_REGRESS="$regress"
    return 1
  fi
  COMPARE_REGRESS=""
  return 0
}

if [ "$#" -eq 2 ]; then
  base="$1"; cur="$2"            # compare two existing files (self-test mode)
  if compare_bench_files "$base" "$cur" "$base" 0; then
    echo -e "${GREEN}BENCH GATE: PASS${NC} (no sec/op regression > ${THRESH}%)"
    exit 0
  else
    status="$?"
    if [ "$status" -eq 1 ]; then
      echo -e "${RED}BENCH GATE: FAIL${NC} (sec/op regression > ${THRESH}%):" >&2
      echo "$COMPARE_REGRESS" >&2
      echo -e "${GRAY}If intentional, refresh bench/baseline.txt via an explicit SOW — do not silently widen the gate.${NC}" >&2
    fi
    exit "$status"
  fi
elif [ "$#" -ne 0 ]; then
  echo -e "${RED}[ERROR]${NC} usage: scripts/check-bench.sh [BASE CURRENT]" >&2
  exit 2
fi

base="$REPO_ROOT/bench/baseline.txt"
benchstat_base="bench/baseline.txt"
require_valid_baseline "$base" || exit 2
run_real_bench_attempt 1 "$base" || exit 2
cur="$REAL_BENCH_CUR"
if compare_bench_files "$base" "$cur" "$benchstat_base" 1; then
  echo -e "${GREEN}BENCH GATE: PASS${NC} (no sec/op regression > ${THRESH}%)"
  exit 0
else
  status="$?"
fi
if [ "$status" -eq 2 ]; then
  exit 2
fi

first_regress="$COMPARE_REGRESS"
echo -e "${YELLOW}[warn]${NC} BENCH GATE: first attempt found sec/op regression > ${THRESH}%; rerunning once to require reproduction." >&2
echo "$first_regress" >&2

run_real_bench_attempt 2 "$base" || exit 2
cur="$REAL_BENCH_CUR"
if compare_bench_files "$base" "$cur" "$benchstat_base" 1; then
  echo -e "${YELLOW}[warn]${NC} BENCH GATE: PASS after retry — first-attempt regression was not reproduced and is treated as local measurement noise." >&2
  echo -e "${GREEN}BENCH GATE: PASS${NC} (no reproduced sec/op regression > ${THRESH}%)"
  exit 0
else
  status="$?"
fi
if [ "$status" -eq 2 ]; then
  exit 2
fi

second_regress="$COMPARE_REGRESS"
reproduced_names="$(comm -12 <(regression_names "$first_regress") <(regression_names "$second_regress"))"
if [ -z "$reproduced_names" ]; then
  echo -e "${YELLOW}[warn]${NC} BENCH GATE: PASS after retry — first-attempt regression was not reproduced by the same benchmark and is treated as local measurement noise." >&2
  echo -e "${GREEN}BENCH GATE: PASS${NC} (no reproduced sec/op regression > ${THRESH}%)"
  exit 0
fi

echo -e "${RED}BENCH GATE: FAIL${NC} (sec/op regression > ${THRESH}% reproduced on retry):" >&2
filter_regressions_by_names "$second_regress" "$reproduced_names" >&2
echo -e "${GRAY}If intentional, refresh bench/baseline.txt via an explicit SOW — do not silently widen the gate.${NC}" >&2
exit 1
