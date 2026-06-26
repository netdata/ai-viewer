#!/usr/bin/env bash
# Self-test for scripts/check-bench.sh. Feeds synthetic benchstat-input files and
# hermetic fake real-mode toolchains (no real benchmarks run) and asserts the gate
# FAILS on a > 20% sec/op regression and PASSES within threshold and on an
# improvement. The gate must be correct — it is itself code.
#
# Run: scripts/test/check-bench-test.sh   (exit 0 = all assertions pass)
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECK="${REPO_ROOT}/scripts/check-bench.sh"
NOTIFY_BENCH="${REPO_ROOT}/internal/notify/bench_test.go"
CI_WORKFLOW="${REPO_ROOT}/.github/workflows/ci.yml"
GATES="${REPO_ROOT}/scripts/gates.sh"
QUALITY_GATES_SPEC="${REPO_ROOT}/.agents/sow/specs/quality-gates.md"
TESTING_STRATEGY_SPEC="${REPO_ROOT}/.agents/sow/specs/testing-strategy.md"
PROJECT_QUALITY_GATES_SKILL="${REPO_ROOT}/.agents/skills/project-quality-gates/SKILL.md"
PROJECT_TESTING_SKILL="${REPO_ROOT}/.agents/skills/project-testing/SKILL.md"
BENCH_README="${REPO_ROOT}/bench/README.md"
BASELINE="${REPO_ROOT}/bench/baseline.txt"
AGENTS="${REPO_ROOT}/AGENTS.md"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0

CI_BENCHMARK_NAMES=(
  BenchmarkBatchInsert
  BenchmarkClaudeScan_SyntheticCorpus
  BenchmarkClaudeTail_SyntheticAppend
  BenchmarkCodexScan_SyntheticCorpus
  BenchmarkCodexTail_SyntheticAppend
  BenchmarkHubFanout
  BenchmarkOpencodeScan_SyntheticDB
  BenchmarkOpencodeTail_SyntheticAppend
  BenchmarkScan_SyntheticCorpus
  BenchmarkSessionsListQuery
  BenchmarkTail_SyntheticAppend
)

# Write 6 BenchmarkFoo samples at the given ns/op values (slight spread so
# benchstat can compute a 0.95 CI).
mkbench() {
  local f="$1"; shift
  { echo "goos: linux"; echo "goarch: amd64"; echo "pkg: selftest"; echo "cpu: selftest"
    for v in "$@"; do echo "BenchmarkFoo-16   10000   ${v} ns/op   100 B/op   2 allocs/op"; done; } > "$f"
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

count_file_value() {
  local file="$1" value=0
  if [ -f "$file" ]; then
    read -r value < "$file" || value=0
  fi
  printf '%s' "$value"
}

write_fake_go() {
  local path="$1"
  cat > "$path" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  env)
    case "${2:-}" in
      GOBIN) printf '%s\n' "${FAKE_GOBIN:?}" ;;
      GOPATH) printf '%s\n' "${FAKE_GOPATH:?}" ;;
      *) echo "unexpected fake go env key: ${2:-}" >&2; exit 90 ;;
    esac
    ;;
  version)
    echo "go version go1.fake linux/amd64"
    ;;
  run)
    count=0
    if [ -n "${FAKE_GO_RUN_COUNT:-}" ] && [ -f "$FAKE_GO_RUN_COUNT" ]; then
      read -r count < "$FAKE_GO_RUN_COUNT" || count=0
    fi
    if [ -n "${FAKE_GO_RUN_COUNT:-}" ]; then
      count=$((count + 1))
      printf '%s\n' "$count" > "$FAKE_GO_RUN_COUNT"
    fi
    if [ "${FAKE_GO_RUN_FAIL:-0}" = "1" ]; then
      echo "fake go run failure" >&2
      exit 43
    fi
    echo "${FAKE_GOMAXPROCS:-16}"
    ;;
  test)
    saw_parallel=0
    for arg in "$@"; do
      if [ "$arg" = "-p=1" ]; then
        saw_parallel=1
      fi
    done
    if [ "$saw_parallel" -ne 1 ]; then
      echo "fake go test missing -p=1" >&2
      exit 44
    fi

    count=0
    if [ -f "${FAKE_GO_TEST_COUNT:?}" ]; then
      read -r count < "$FAKE_GO_TEST_COUNT" || count=0
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$FAKE_GO_TEST_COUNT"

    if [ "${FAKE_GO_TEST_FAIL:-0}" = "1" ]; then
      echo "fake go test failure" >&2
      exit 42
    fi

    if [ "$count" -eq 1 ] && [ -n "${FAKE_MUTATE_LOADAVG_AFTER_FIRST:-}" ]; then
      printf '%s\n' "$FAKE_MUTATE_LOADAVG_AFTER_FIRST" > "${BENCH_LOADAVG_FILE:?}"
    fi

    echo "goos: linux"
    echo "goarch: amd64"
    echo "pkg: fake-real-mode"
    echo "cpu: fake-cpu"
    awk '/^Benchmark/ { print $1 " 1000 100 ns/op 1 B/op 0 allocs/op" }' bench/baseline.txt
    echo "PASS"
    ;;
  *)
    echo "unexpected fake go command: $*" >&2
    exit 91
    ;;
esac
SH
  chmod +x "$path"
}

write_fake_benchstat() {
  local path="$1"
  cat > "$path" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

count=0
if [ -f "${FAKE_BENCHSTAT_COUNT:?}" ]; then
  read -r count < "$FAKE_BENCHSTAT_COUNT" || count=0
fi
count=$((count + 1))
printf '%s\n' "$count" > "$FAKE_BENCHSTAT_COUNT"

case "${FAKE_BENCHSTAT_MODE:?}" in
  retry-pass) if [ "$count" -eq 1 ]; then regress_name="HubFanout"; else regress_name=""; fi ;;
  retry-fail) regress_name="HubFanout" ;;
  retry-disjoint) if [ "$count" -eq 1 ]; then regress_name="HubFanout"; else regress_name="BatchInsert"; fi ;;
  pass) regress_name="" ;;
  *) echo "unexpected fake benchstat mode: ${FAKE_BENCHSTAT_MODE}" >&2; exit 92 ;;
esac

base="${1:?}"
printf 'name old new sec/op vs base\n'
awk '/^Benchmark/ {
  name = $1
  sub(/^Benchmark/, "", name)
  sub(/-[0-9]+$/, "", name)
  seen[name] = 1
}
END {
  for (name in seen) print name
}' "$base" | sort | while IFS= read -r name; do
  if [ -z "$name" ]; then
    continue
  fi
  if [ -n "$regress_name" ] && [ "$name" = "$regress_name" ]; then
    printf '%s 100ns 130ns +25.00%% (p=0.001 n=6+6)\n' "$name"
  else
    printf '%s 100ns 101ns ~ (p=0.999 n=6+6)\n' "$name"
  fi
done
SH
  chmod +x "$path"
}

assert_fake_real_mode() {
  local mode="$1" go_fail="$2" want="$3" pattern="$4" desc="$5" want_go_tests="$6" want_benchstats="$7" want_go_runs="$8"
  local dir bin got go_tests benchstats go_runs
  dir="$(mktemp -d "$TMP/fake-real.XXXXXX")"
  bin="$dir/bin"
  mkdir -p "$bin" "$dir/gopath"
  write_fake_go "$bin/go"
  write_fake_benchstat "$bin/benchstat"
  printf '%s\n' '1.0 0.5 0.25 1/100 123' > "$dir/loadavg"

  got=0
  PATH="$bin:$PATH" \
    FAKE_GOBIN="$bin" \
    FAKE_GOPATH="$dir/gopath" \
    FAKE_GO_TEST_COUNT="$dir/go-test-count" \
    FAKE_GO_RUN_COUNT="$dir/go-run-count" \
    FAKE_BENCHSTAT_COUNT="$dir/benchstat-count" \
    FAKE_BENCHSTAT_MODE="$mode" \
    FAKE_GO_TEST_FAIL="$go_fail" \
    BENCH_SELF_TEST=1 \
    BENCH_LOADAVG_FILE="$dir/loadavg" \
    bash "$CHECK" >"$TMP/out" 2>&1 || got=$?

  go_tests="$(count_file_value "$dir/go-test-count")"
  benchstats="$(count_file_value "$dir/benchstat-count")"
  go_runs="$(count_file_value "$dir/go-run-count")"
  if [ "$got" -eq "$want" ] && grep -Eq -- "$pattern" "$TMP/out" && [ "$go_tests" -eq "$want_go_tests" ] && [ "$benchstats" -eq "$want_benchstats" ] && [ "$go_runs" -eq "$want_go_runs" ]; then
    echo -e "  ${GREEN}PASS${NC} (${desc}): exit ${got}, fake go test ${go_tests}, fake go run ${go_runs}, fake benchstat ${benchstats}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): want exit ${want}/go-test ${want_go_tests}/go-run ${want_go_runs}/benchstat ${want_benchstats}, got exit ${got}/go-test ${go_tests}/go-run ${go_runs}/benchstat ${benchstats}"
    sed 's/^/      /' "$TMP/out"
    fail=$((fail+1))
  fi
}

assert_fake_real_mode_mutating_load() {
  local dir bin got go_tests benchstats go_runs
  dir="$(mktemp -d "$TMP/fake-retry-load.XXXXXX")"
  bin="$dir/bin"
  mkdir -p "$bin" "$dir/gopath"
  write_fake_go "$bin/go"
  write_fake_benchstat "$bin/benchstat"
  printf '%s\n' '1.0 0.5 0.25 1/100 123' > "$dir/loadavg"

  got=0
  PATH="$bin:$PATH" \
    FAKE_GOBIN="$bin" \
    FAKE_GOPATH="$dir/gopath" \
    FAKE_GO_TEST_COUNT="$dir/go-test-count" \
    FAKE_GO_RUN_COUNT="$dir/go-run-count" \
    FAKE_BENCHSTAT_COUNT="$dir/benchstat-count" \
    FAKE_BENCHSTAT_MODE=retry-fail \
    FAKE_GO_TEST_FAIL=0 \
    FAKE_MUTATE_LOADAVG_AFTER_FIRST='20.0 15.0 10.0 1/100 123' \
    BENCH_SELF_TEST=1 \
    BENCH_LOADAVG_FILE="$dir/loadavg" \
    bash "$CHECK" >"$TMP/out" 2>&1 || got=$?

  go_tests="$(count_file_value "$dir/go-test-count")"
  benchstats="$(count_file_value "$dir/benchstat-count")"
  go_runs="$(count_file_value "$dir/go-run-count")"
  if [ "$got" -eq 2 ] && [ "$go_tests" -eq 1 ] && [ "$benchstats" -eq 1 ] && [ "$go_runs" -eq 2 ] && grep -Eq 'benchmark host too busy|BENCH GATE: first attempt' "$TMP/out"; then
    echo -e "  ${GREEN}PASS${NC} (retry preflight blocks second attempt after load fixture mutation): exit ${got}, fake go test ${go_tests}, fake go run ${go_runs}, fake benchstat ${benchstats}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (retry preflight blocks second attempt after load fixture mutation): got exit ${got}/go-test ${go_tests}/go-run ${go_runs}/benchstat ${benchstats}"
    sed 's/^/      /' "$TMP/out"
    fail=$((fail+1))
  fi
}

assert_fake_real_mode_env_override_resistant() {
  local dir bin got go_tests benchstats go_runs
  dir="$(mktemp -d "$TMP/fake-env-override.XXXXXX")"
  bin="$dir/bin"
  mkdir -p "$bin" "$dir/gopath"
  write_fake_go "$bin/go"
  write_fake_benchstat "$bin/benchstat"
  printf '%s\n' '9.0 0.5 0.25 1/100 123' > "$dir/loadavg"

  got=0
  PATH="$bin:$PATH" \
    FAKE_GOBIN="$bin" \
    FAKE_GOPATH="$dir/gopath" \
    FAKE_GO_TEST_COUNT="$dir/go-test-count" \
    FAKE_GO_RUN_COUNT="$dir/go-run-count" \
    FAKE_BENCHSTAT_COUNT="$dir/benchstat-count" \
    FAKE_BENCHSTAT_MODE=pass \
    BENCH_SELF_TEST=1 \
    BENCH_LOADAVG_FILE="$dir/loadavg" \
    BENCH_PKG_PARALLELISM=99 \
    BENCH_MAX_LOAD_PER_EFFECTIVE_CPU=0.99 \
    bash "$CHECK" >"$TMP/out" 2>&1 || got=$?

  go_tests="$(count_file_value "$dir/go-test-count")"
  benchstats="$(count_file_value "$dir/benchstat-count")"
  go_runs="$(count_file_value "$dir/go-run-count")"
  if [ "$got" -eq 2 ] && [ "$go_tests" -eq 0 ] && [ "$benchstats" -eq 0 ] && [ "$go_runs" -eq 1 ] && grep -Eq 'benchmark -p: 1' "$TMP/out" && grep -Eq 'busy-host max load per effective CPU: 0\.50' "$TMP/out" && grep -Eq 'comparison: 9\.0 >= 8\.00' "$TMP/out"; then
    echo -e "  ${GREEN}PASS${NC} (benchmark physical-validity constants ignore environment overrides): exit ${got}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (benchmark physical-validity constants ignore environment overrides): got exit ${got}/go-test ${go_tests}/go-run ${go_runs}/benchstat ${benchstats}"
    sed 's/^/      /' "$TMP/out"
    fail=$((fail+1))
  fi
}

assert_passing_preflight_diagnostics() {
  local dir bin got
  dir="$(mktemp -d "$TMP/fake-diag.XXXXXX")"
  bin="$dir/bin"
  mkdir -p "$bin" "$dir/gopath"
  write_fake_go "$bin/go"
  write_fake_benchstat "$bin/benchstat"
  printf '%s\n' '1.0 0.5 0.25 1/100 123' > "$dir/loadavg"

  got=0
  PATH="$bin:$PATH" \
    FAKE_GOBIN="$bin" \
    FAKE_GOPATH="$dir/gopath" \
    FAKE_GO_TEST_COUNT="$dir/go-test-count" \
    FAKE_GO_RUN_COUNT="$dir/go-run-count" \
    FAKE_BENCHSTAT_COUNT="$dir/benchstat-count" \
    FAKE_BENCHSTAT_MODE=pass \
    BENCH_SELF_TEST=1 \
    BENCH_LOADAVG_FILE="$dir/loadavg" \
    bash "$CHECK" >"$TMP/out" 2>&1 || got=$?

  if [ "$got" -eq 0 ] &&
     grep -Eq 'benchmark -p: 1' "$TMP/out" &&
     grep -Eq 'busy-host max load per effective CPU: 0\.50' "$TMP/out" &&
     grep -Eq 'busy-host load threshold: 8\.00' "$TMP/out" &&
     grep -Eq 'comparison: 1\.0 < 8\.00' "$TMP/out"; then
    echo -e "  ${GREEN}PASS${NC} (passing real-mode diagnostics include package parallelism and busy-host threshold)"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (passing real-mode diagnostics include package parallelism and busy-host threshold): got exit ${got}"
    sed 's/^/      /' "$TMP/out"
    fail=$((fail+1))
  fi
}

assert_preflight_case() {
  local desc="$1" self_test_value="$2" load_mode="$3" load_content="$4" gomax="$5" go_run_fail="$6" want="$7" required_patterns="$8" want_go_tests="$9" want_benchstats="${10}" want_go_runs="${11}" forbidden_patterns="${12:-}"
  local dir bin got go_tests benchstats go_runs load_file
  dir="$(mktemp -d "$TMP/preflight.XXXXXX")"
  bin="$dir/bin"
  mkdir -p "$bin" "$dir/gopath"
  write_fake_go "$bin/go"
  write_fake_benchstat "$bin/benchstat"

  load_file="$dir/loadavg"
  case "$load_mode" in
    set) printf '%s\n' "$load_content" > "$load_file" ;;
    missing) load_file="$dir/missing-loadavg" ;;
    empty) : > "$load_file" ;;
    dir) mkdir -p "$load_file" ;;
    unset) load_file="" ;;
    *) echo "unexpected load_mode: $load_mode" >&2; exit 99 ;;
  esac

  local -a env_args
  env_args=(
    "PATH=$bin:$PATH"
    "FAKE_GOBIN=$bin"
    "FAKE_GOPATH=$dir/gopath"
    "FAKE_GO_TEST_COUNT=$dir/go-test-count"
    "FAKE_GO_RUN_COUNT=$dir/go-run-count"
    "FAKE_BENCHSTAT_COUNT=$dir/benchstat-count"
    "FAKE_BENCHSTAT_MODE=pass"
    "FAKE_GOMAXPROCS=$gomax"
    "FAKE_GO_RUN_FAIL=$go_run_fail"
  )
  if [ "$self_test_value" != "__unset__" ]; then
    env_args+=("BENCH_SELF_TEST=$self_test_value")
  fi
  if [ "$load_mode" != "unset" ]; then
    env_args+=("BENCH_LOADAVG_FILE=$load_file")
  fi

  got=0
  env "${env_args[@]}" bash "$CHECK" >"$TMP/out" 2>&1 || got=$?
  go_tests="$(count_file_value "$dir/go-test-count")"
  benchstats="$(count_file_value "$dir/benchstat-count")"
  go_runs="$(count_file_value "$dir/go-run-count")"

  local ok=1 pattern
  if [ "$got" -ne "$want" ] || [ "$go_tests" -ne "$want_go_tests" ] || [ "$benchstats" -ne "$want_benchstats" ] || [ "$go_runs" -ne "$want_go_runs" ]; then
    ok=0
  fi
  while IFS= read -r pattern; do
    [ -z "$pattern" ] && continue
    if ! grep -Eq -- "$pattern" "$TMP/out"; then
      ok=0
    fi
  done <<< "$required_patterns"
  while IFS= read -r pattern; do
    [ -z "$pattern" ] && continue
    if grep -Eq -- "$pattern" "$TMP/out"; then
      ok=0
    fi
  done <<< "$forbidden_patterns"

  if [ "$ok" -eq 1 ]; then
    echo -e "  ${GREEN}PASS${NC} (${desc}): exit ${got}, fake go test ${go_tests}, fake go run ${go_runs}, fake benchstat ${benchstats}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): want exit ${want}/go-test ${want_go_tests}/go-run ${want_go_runs}/benchstat ${want_benchstats}, got exit ${got}/go-test ${go_tests}/go-run ${go_runs}/benchstat ${benchstats}"
    sed 's/^/      /' "$TMP/out"
    fail=$((fail+1))
  fi
}

assert_compare_file_ignores_loadavg_guard() {
  local got=0
  BENCH_LOADAVG_FILE="$TMP/compare-mode-should-ignore-loadavg" \
    bash "$CHECK" "$TMP/base" "$TMP/within" >"$TMP/out" 2>&1 || got=$?
  if [ "$got" -eq 0 ] && grep -Eq 'BENCH GATE: PASS' "$TMP/out"; then
    echo -e "  ${GREEN}PASS${NC} (compare-file mode ignores BENCH_LOADAVG_FILE without BENCH_SELF_TEST): exit ${got}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (compare-file mode ignores BENCH_LOADAVG_FILE without BENCH_SELF_TEST): got exit ${got}"
    sed 's/^/      /' "$TMP/out"
    fail=$((fail+1))
  fi
}

assert_file_contains() {
  local file="$1" pattern="$2" desc="$3"
  if grep -Eq -- "$pattern" "$file"; then
    echo -e "  ${GREEN}PASS${NC} (${desc})"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): missing pattern ${pattern}"; fail=$((fail+1))
  fi
}

assert_file_lacks() {
  local file="$1" pattern="$2" desc="$3"
  if grep -Eq -- "$pattern" "$file"; then
    echo -e "  ${RED}FAIL${NC} (${desc}): forbidden pattern ${pattern}"; fail=$((fail+1))
  else
    echo -e "  ${GREEN}PASS${NC} (${desc})"; pass=$((pass+1))
  fi
}

extract_ci_require_benchmarks_script() {
  local out="$1"
  awk '
    /^[[:space:]]*- name: Require benchmarks$/ { in_step = 1; next }
    in_step && /^[[:space:]]*run: \|$/ { in_run = 1; next }
    in_run {
      if ($0 ~ /^      - name: /) {
        exit
      }
      if ($0 ~ /^          /) {
        sub(/^          /, "")
        print
        next
      }
      if ($0 ~ /^[[:space:]]*$/) {
        print ""
        next
      }
      exit 2
    }
    END { if (!in_run) exit 1 }
  ' "$CI_WORKFLOW" > "$out"
}

write_ci_benchmark_fixture() {
  local dir="$1" row_mode="$2" limit="$3" missing_func="${4:-}"
  local idx=0 name row
  mkdir -p "$dir/bench" "$dir/pkg"

  {
    echo "goos: linux"
    echo "goarch: amd64"
    echo "pkg: ci-require-benchmark-selftest"
    echo "cpu: selftest"
    for name in "${CI_BENCHMARK_NAMES[@]}"; do
      if [ "$idx" -ge "$limit" ]; then
        break
      fi
      row="$name"
      case "$row_mode" in
        unsuffixed) ;;
        suffixed) row="${name}-16" ;;
        mixed)
          if [ $((idx % 2)) -eq 0 ]; then
            row="${name}-16"
          fi
          ;;
        *) echo "unexpected CI benchmark row mode: $row_mode" >&2; return 1 ;;
      esac
      printf '%s 1000 100 ns/op 1 B/op 0 allocs/op\n' "$row"
      idx=$((idx + 1))
    done
  } > "$dir/bench/baseline.txt"

  {
    echo "package fakebench"
    echo 'import "testing"'
    for name in "${CI_BENCHMARK_NAMES[@]}"; do
      if [ "$name" = "$missing_func" ]; then
        continue
      fi
      printf 'func %s(b *testing.B) {}\n' "$name"
    done
  } > "$dir/pkg/bench_test.go"
}

assert_ci_require_benchmarks() {
  local row_mode="$1" limit="$2" missing_func="$3" want="$4" pattern="$5" desc="$6"
  local dir script out combined got=0
  dir="$(mktemp -d "$TMP/ci-require.XXXXXX")"
  script="$TMP/ci-require-benchmarks.sh"
  out="$dir/out"
  combined="$dir/combined"

  if ! extract_ci_require_benchmarks_script "$script" || [ ! -s "$script" ]; then
    echo -e "  ${RED}FAIL${NC} (${desc}): could not extract CI Require benchmarks run block"; fail=$((fail+1))
    return
  fi

  write_ci_benchmark_fixture "$dir" "$row_mode" "$limit" "$missing_func"
  (cd "$dir" && GITHUB_OUTPUT="$dir/github-output" bash "$script") > "$out" 2>&1 || got=$?
  cat "$out" > "$combined"
  if [ -f "$dir/github-output" ]; then
    cat "$dir/github-output" >> "$combined"
  fi

  if [ "$got" -eq "$want" ] && grep -Eq -- "$pattern" "$combined"; then
    echo -e "  ${GREEN}PASS${NC} (${desc}): exit ${got}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): want exit ${want} with pattern ${pattern}, got ${got}"
    sed 's/^/      /' "$combined"
    fail=$((fail+1))
  fi
}

assert_compare_mode_single_pass() {
  if awk '
    /\$#.*-eq 2/ { in_compare = 1 }
    in_compare && /run_real_bench_attempt/ { bad = 1 }
    in_compare && /compare_bench_files "\$base" "\$cur" "\$base" 0/ { saw_compare = 1 }
    in_compare && /elif \[ "\$#" -ne 0 \]/ { in_compare = 0 }
    END { exit (saw_compare && !bad) ? 0 : 1 }
  ' "$CHECK"; then
    echo -e "  ${GREEN}PASS${NC} (compare-file mode remains single-pass)"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (compare-file mode must compare once and must not run real benchmark retry)"; fail=$((fail+1))
  fi
}

hubfanout_const_value() {
  local name="$1"
  awk -v name="$name" '
    $1 == "const" && $2 == name && $3 == "=" && $4 ~ /^[0-9]+$/ { print $4; found = 1; exit }
    END { if (!found) exit 1 }
  ' "$NOTIFY_BENCH"
}

assert_hubfanout_batch_bounded() {
  local subs deliveries
  subs="$(hubfanout_const_value "subs" || true)"
  deliveries="$(hubfanout_const_value "deliveriesPerOp" || true)"
  if [ -n "$subs" ] && [ -n "$deliveries" ] && [ "$deliveries" -ge 64 ] && [ "$deliveries" -lt "$subs" ]; then
    echo -e "  ${GREEN}PASS${NC} (HubFanout uses bounded fixed batch ${deliveries} < subs ${subs})"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (HubFanout fixed batch must be numeric, >=64, and smaller than subs; got deliveriesPerOp=${deliveries:-missing}, subs=${subs:-missing})"; fail=$((fail+1))
  fi
}

assert_script_contains() {
  assert_file_contains "$CHECK" "$1" "$2"
}

line_number_matching() {
  local file="$1" pattern="$2"
  awk -v pat="$pattern" '$0 ~ pat { print NR; exit }' "$file"
}

assert_line_order() {
  local file="$1" before="$2" after="$3" desc="$4" before_line after_line
  before_line="$(line_number_matching "$file" "$before")"
  after_line="$(line_number_matching "$file" "$after")"
  if [ -n "$before_line" ] && [ -n "$after_line" ] && [ "$before_line" -lt "$after_line" ]; then
    echo -e "  ${GREEN}PASS${NC} (${desc})"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): before line=${before_line:-missing}, after line=${after_line:-missing}"; fail=$((fail+1))
  fi
}

bench_cpu_pattern="go test .* -cpu=\"\\\$BENCH_CPU\""
bench_package_parallelism_pattern="go test (.* )?-p=\"\\\$BENCH_PKG_PARALLELISM\""
first_attempt_pattern="run_real_bench_attempt 1 \"\\\$base\""
second_attempt_pattern="run_real_bench_attempt 2 \"\\\$base\""

assert_script_contains '^BENCH_CPU="1"$' "real benchmark CPU policy is pinned to 1"
assert_script_contains '^BENCH_PKG_PARALLELISM="1"$' "real benchmark package parallelism is pinned to 1"
assert_script_contains '^BENCH_MAX_LOAD_PER_EFFECTIVE_CPU="0\.50"$' "busy-host preflight threshold is pinned to 0.50 per effective CPU"
assert_script_contains '^export LC_ALL=C$' "benchmark gate pins C locale for numeric awk parsing"
assert_file_lacks "$CHECK" '^loadavg_values\(\)' "benchmark gate has no orphaned loadavg helper"
assert_script_contains "$bench_cpu_pattern" "real go test command passes -cpu=1"
assert_script_contains "$bench_package_parallelism_pattern" "real go test command passes -p=1"
assert_script_contains 'go version:' "diagnostics include Go version"
assert_script_contains 'effective GOMAXPROCS:' "diagnostics include effective GOMAXPROCS"
assert_script_contains 'benchmark -cpu:' "diagnostics include benchmark CPU setting"
assert_script_contains 'benchmark -p:' "diagnostics include package parallelism"
assert_script_contains 'busy-host max load per effective CPU:' "diagnostics include busy-host threshold factor"
assert_script_contains 'busy-host load threshold:' "diagnostics include computed busy-host threshold"
assert_script_contains 'comparison:' "diagnostics include busy-host pass/fail comparison"
assert_script_contains 'benchmark packages:' "diagnostics include package list"
assert_script_contains 'baseline path:' "diagnostics include baseline path"
assert_script_contains 'current path:' "diagnostics include temporary current path"
assert_script_contains '/proc/loadavg' "diagnostics handle /proc/loadavg"
assert_script_contains 'loadavg \(1m 5m 15m\):' "diagnostics include load averages"
assert_script_contains 'BENCH_SELF_TEST' "loadavg fixture hook is guarded by strict self-test sentinel"
assert_script_contains '\$\{BENCH_SELF_TEST:-\}' "self-test sentinel is nounset-safe and exact-match friendly"
assert_script_contains '\$#.*-eq 2' "compare-file self-test mode remains available"
assert_compare_mode_single_pass
assert_script_contains "$first_attempt_pattern" "real mode runs first benchmark attempt"
assert_script_contains "$second_attempt_pattern" "real mode runs second benchmark attempt after regression"
assert_script_contains 'first-attempt regression was not reproduced' "real retry pass reports local measurement noise"
assert_script_contains 'reproduced on retry' "real retry failure reports reproduced regression"
assert_script_contains '\$\{#cleanup_files\[@\]\}.*-eq 0' "cleanup handles empty temp array under nounset"
assert_hubfanout_batch_bounded
assert_file_contains "$NOTIFY_BENCH" 'deliver < deliveriesPerOp' "HubFanout loops over the fixed delivery batch"
assert_file_contains "$NOTIFY_BENCH" 'h\.Deliver\(ids\[nextSub\], ev\)' "HubFanout still measures serial Hub.Deliver calls"
assert_file_contains "$NOTIFY_BENCH" 'ReportMetric\(float64\(delivered\)/wallSec, "deliveries/sec"\)' "HubFanout reports deliveries/sec"
assert_file_contains "$NOTIFY_BENCH" 'ReportMetric\(float64\(subs\), "subscriptions"\)' "HubFanout reports subscriptions"
assert_file_lacks "$NOTIFY_BENCH" '\bgo func\(' "HubFanout benchmark has no helper drainer goroutine"
assert_ci_require_benchmarks unsuffixed "${#CI_BENCHMARK_NAMES[@]}" "" 0 '^present=true$' "CI Require benchmarks accepts unsuffixed -cpu=1 baseline rows"
assert_ci_require_benchmarks mixed "${#CI_BENCHMARK_NAMES[@]}" "" 0 '^present=true$' "CI Require benchmarks normalizes mixed suffixed and unsuffixed rows"
assert_ci_require_benchmarks suffixed 10 "" 1 'expected exactly 11 required benchmark names' "CI Require benchmarks preserves the 11-name fail-closed check"
assert_ci_require_benchmarks unsuffixed "${#CI_BENCHMARK_NAMES[@]}" BenchmarkHubFanout 1 'required benchmark function\(s\).*missing|Missing benchmark function\(s\):' "CI Require benchmarks compares required names to implemented Benchmark functions"

assert_file_contains "$QUALITY_GATES_SPEC" 'go test -p=1 -run=\^\$ -bench=\. -benchmem -count=6 -cpu=1' "quality-gates spec documents -p=1 benchmark command"
assert_file_contains "$QUALITY_GATES_SPEC" '0\.50 \* effective_GOMAXPROCS|0\.50 \* effective `GOMAXPROCS`' "quality-gates spec documents busy-host threshold"
assert_file_contains "$TESTING_STRATEGY_SPEC" 'go test -p=1 -run=\^\$ -bench=\. -benchmem -count=6 -cpu=1' "testing strategy documents -p=1 benchmark command"
assert_file_contains "$PROJECT_QUALITY_GATES_SKILL" 'go test -p=1 -run=\^\$ -bench=\. -benchmem -count=6 -cpu=1' "quality-gates skill documents -p=1 benchmark command"
assert_file_contains "$PROJECT_QUALITY_GATES_SKILL" 'busy-host preflight|host load is at or above `0\.50 \* effective_GOMAXPROCS`' "quality-gates skill documents busy-host preflight"
assert_file_contains "$PROJECT_TESTING_SKILL" '-p=1 -count=6 -cpu=1' "testing skill documents -p=1 benchmark command"
assert_file_contains "$BENCH_README" 'go test -p=1 -run=\^\$ -bench=\. -benchmem -count=6 -cpu=1' "bench README documents -p=1 full local gate command"
assert_file_contains "$BASELINE" '#   go test -p=1 -run='\''\^\$'\'' -bench=\. -benchmem -count=6 -cpu=1 \\' "baseline header command includes -p=1"
assert_file_contains "$BASELINE" 'SOW-0058 pre-refresh repository HEAD' "baseline provenance no longer implies SOW-0097 pre-refresh HEAD"
assert_file_contains "$AGENTS" 'scripts/check-bench\.sh.*-p=1' "AGENTS quality-gate row mentions -p=1"
assert_file_contains "$AGENTS" 'too busy for valid wall-time benchmark evidence|invalid-measurement result' "AGENTS records busy-host exit 2 interpretation"
assert_file_contains "$CHECK" 'busy-host/unavailable-preflight.*exit 2|busy-host.*exit 2' "check-bench header documents busy-host exit 2"
assert_file_contains "$CHECK" 'go test -p=1' "check-bench header documents -p=1 command"
assert_file_contains "$GATES" 'bash scripts/check-bench\.sh \|\| return \$\?' "gates.sh preserves check-bench exit status"
assert_file_contains "$GATES" 'exit "\$ec"' "gates.sh section exits with captured section status"
assert_file_contains "$QUALITY_GATES_SPEC" 'benchmark regression gate runs immediately' "quality-gates spec documents early benchmark ordering"
assert_file_contains "$PROJECT_QUALITY_GATES_SKILL" 'benchmark regression gate runs immediately after the cheap lint formatter-scope self-test' "quality-gates skill documents early benchmark ordering"
assert_line_order "$GATES" 'section "lint formatter-scope self-test"' 'section "benchmark regression gate"' "gates.sh keeps cheap formatter self-test before benchmark"
assert_line_order "$GATES" 'section "benchmark regression gate"' 'section "lint[.]sh' "gates.sh runs benchmark before lint/static analysis"
assert_line_order "$GATES" 'section "benchmark regression gate"' 'section "build[.]sh' "gates.sh runs benchmark before build"
assert_line_order "$GATES" 'section "benchmark regression gate"' 'section "test[.]sh' "gates.sh runs benchmark before race/Vitest"
assert_line_order "$GATES" 'section "benchmark regression gate"' 'section "frontend E2E' "gates.sh runs benchmark before Playwright E2E"

mkbench "$TMP/base"    100 101  99 100 102  98
mkbench "$TMP/regress" 130 131 129 130 132 128   # +30% sec/op -> must FAIL
mkbench "$TMP/within"  110 111 109 110 112 108   # +10% sec/op -> within 20%, PASS
mkbench "$TMP/improve"  80  81  79  80  82  78   # -20% sec/op -> improvement, PASS

assert 1 "$TMP/base" "$TMP/regress" "+30% sec/op regression"
assert 0 "$TMP/base" "$TMP/within"  "+10% within threshold"
assert 0 "$TMP/base" "$TMP/improve" "improvement (faster)"
# missing file -> usage/tool error (exit 2)
assert 2 "$TMP/base" "$TMP/does-not-exist" "missing current file"
: > "$TMP/empty_base"
assert 2 "$TMP/empty_base" "$TMP/within" "empty baseline file"
{ echo "# metadata-only baseline"; echo "goos: linux"; echo "goarch: amd64"; echo "pkg: selftest"; echo "cpu: selftest"; } > "$TMP/benchmarkless_base"
assert 2 "$TMP/benchmarkless_base" "$TMP/within" "metadata-only baseline without Benchmark rows"

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

# Reverse direction: a NEW current benchmark absent from the baseline -> WARN but
# exit 0 (it cannot regress against a nonexistent baseline; not a gate failure).
mkbench_named "$TMP/base_foo"   selftest Foo 100 101 99 100 102 98
mkbench_two   "$TMP/cur_foobar" selftest
assert 0 "$TMP/base_foo" "$TMP/cur_foobar" "new current benchmark (Bar) warns, does not fail"
if grep -q 'absent from the baseline' "$TMP/out"; then
  echo -e "  ${GREEN}PASS${NC} (reverse-direction warn emitted): Bar flagged un-gated"; pass=$((pass+1))
else
  echo -e "  ${RED}FAIL${NC} (reverse-direction warn missing)"; sed 's/^/      /' "$TMP/out"; fail=$((fail+1))
fi

assert_compare_file_ignores_loadavg_guard

assert_preflight_case "loadavg fixture without self-test sentinel is rejected before host interaction (unset)" "__unset__" set '1.0 0.5 0.25 1/100 123' 16 0 2 $'BENCH_LOADAVG_FILE requires BENCH_SELF_TEST=1' 0 0 0 $'effective GOMAXPROCS:\nloadavg \\(1m 5m 15m\\):'
assert_preflight_case "loadavg fixture without self-test sentinel is rejected before host interaction (empty)" "" set '1.0 0.5 0.25 1/100 123' 16 0 2 $'BENCH_LOADAVG_FILE requires BENCH_SELF_TEST=1' 0 0 0 $'effective GOMAXPROCS:\nloadavg \\(1m 5m 15m\\):'
assert_preflight_case "loadavg fixture without self-test sentinel is rejected before host interaction (0)" "0" set '1.0 0.5 0.25 1/100 123' 16 0 2 $'BENCH_LOADAVG_FILE requires BENCH_SELF_TEST=1' 0 0 0 $'effective GOMAXPROCS:\nloadavg \\(1m 5m 15m\\):'
assert_preflight_case "loadavg fixture without self-test sentinel is rejected before host interaction (true)" "true" set '1.0 0.5 0.25 1/100 123' 16 0 2 $'BENCH_LOADAVG_FILE requires BENCH_SELF_TEST=1' 0 0 0 $'effective GOMAXPROCS:\nloadavg \\(1m 5m 15m\\):'
assert_preflight_case "loadavg fixture without self-test sentinel is rejected before host interaction (2)" "2" set '1.0 0.5 0.25 1/100 123' 16 0 2 $'BENCH_LOADAVG_FILE requires BENCH_SELF_TEST=1' 0 0 0 $'effective GOMAXPROCS:\nloadavg \\(1m 5m 15m\\):'
assert_preflight_case "self-test sentinel without loadavg fixture is rejected before host interaction" "1" unset '' 16 0 2 $'BENCH_SELF_TEST=1 requires BENCH_LOADAVG_FILE' 0 0 0 $'effective GOMAXPROCS:\nloadavg \\(1m 5m 15m\\):'
assert_preflight_case "missing loadavg fixture fails closed before fake go test" "1" missing '' 16 0 2 $'benchmark loadavg source unavailable\nmissing-loadavg' 0 0 0
assert_preflight_case "empty loadavg fixture fails closed before fake go test" "1" empty '' 16 0 2 $'benchmark loadavg source invalid\nexpected at least three fields' 0 0 0
assert_preflight_case "unreadable/non-file loadavg fixture fails closed before fake go test" "1" dir '' 16 0 2 $'benchmark loadavg source unavailable|benchmark loadavg source invalid' 0 0 0
assert_preflight_case "short loadavg fixture fails closed before fake go test" "1" set '1.0 0.5' 16 0 2 $'benchmark loadavg source invalid\nexpected at least three fields' 0 0 0
assert_preflight_case "non-numeric first loadavg field fails closed" "1" set 'x 0.5 0.25 1/100 123' 16 0 2 $'benchmark loadavg source invalid\nfirst' 0 0 0
assert_preflight_case "non-numeric second loadavg field fails closed" "1" set '1.0 x 0.25 1/100 123' 16 0 2 $'benchmark loadavg source invalid\nsecond' 0 0 0
assert_preflight_case "non-numeric third loadavg field fails closed" "1" set '1.0 0.5 x 1/100 123' 16 0 2 $'benchmark loadavg source invalid\nthird' 0 0 0
assert_preflight_case "negative first loadavg field fails closed" "1" set '-1.0 0.5 0.25 1/100 123' 16 0 2 $'benchmark loadavg source invalid\nfirst' 0 0 0
assert_preflight_case "negative second loadavg field fails closed" "1" set '1.0 -0.5 0.25 1/100 123' 16 0 2 $'benchmark loadavg source invalid\nsecond' 0 0 0
assert_preflight_case "negative third loadavg field fails closed" "1" set '1.0 0.5 -0.25 1/100 123' 16 0 2 $'benchmark loadavg source invalid\nthird' 0 0 0
assert_preflight_case "busy host above threshold exits 2 before fake go test" "1" set '20.0 15.0 10.0 1/100 123' 16 0 2 $'benchmark host too busy\nloadavg 1m: 20.0\neffective GOMAXPROCS: 16\nbusy-host threshold: 8.00\nbenchmark -p: 1\ncomparison: 20.0 >= 8.00\nwait for a quieter window' 0 0 1 $'COMMAND\n/proc/[0-9]+/cmdline'
assert_preflight_case "exact busy-host threshold exits 2 before fake go test" "1" set '8.0 1.0 1.0 1/100 123' 16 0 2 $'comparison: 8.0 >= 8.00' 0 0 1
assert_preflight_case "unavailable effective GOMAXPROCS string exits 2 before fake go test" "1" set '1.0 0.5 0.25 1/100 123' unavailable 0 2 $'effective GOMAXPROCS unavailable\nunavailable' 0 0 1
assert_preflight_case "failed effective GOMAXPROCS probe exits 2 before fake go test" "1" set '1.0 0.5 0.25 1/100 123' 16 1 2 $'effective GOMAXPROCS unavailable\nfake go run failure|unavailable' 0 0 1

assert_fake_real_mode retry-pass 0 0 'PASS after retry.*first-attempt regression was not reproduced' "real mode retries once, then passes local noise" 2 2 2
assert_fake_real_mode retry-fail 0 1 'reproduced on retry' "real mode fails when regression reproduces" 2 2 2
assert_fake_real_mode retry-disjoint 0 0 'first-attempt regression was not reproduced by the same benchmark' "real mode passes when retry regressions are disjoint" 2 2 2
assert_fake_real_mode pass 1 2 'benchmark command failed.*go test exit 42' "real mode fails closed when go test fails" 1 0 1
assert_fake_real_mode_mutating_load
assert_fake_real_mode_env_override_resistant
assert_passing_preflight_diagnostics

echo
if [ "$fail" -eq 0 ]; then
  echo -e "${GREEN}[ok]${NC} check-bench self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} check-bench self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
