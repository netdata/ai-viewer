#!/usr/bin/env bash
#
# sanitize-fixture-test.sh
#
# Plain-shell harness for scripts/sanitize-fixture.sh. Walks every scenario
# under scripts/test/fixtures/<format>/<scenario>/ and verifies the sanitized
# output matches the committed EXPECTED/ tree byte-for-byte (and that
# behavior-only scenarios like zero-byte and malformed produce the expected
# exit code).
#
# Asserted invariants:
#  - byte-identical reproducibility under fixed --id-seed
#  - no committed fixture contains a residual [REDACTED_...] placeholder in
#    its INPUT/ tree (which would prove the input was already-sanitized and
#    thus not exercising the rules)
#  - behavior-only scenarios exit with the expected status

set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SANITIZER="${REPO_ROOT}/scripts/sanitize-fixture.sh"
FIXTURES_ROOT="${SCRIPT_DIR}/fixtures"
TMP_ROOT="$(mktemp -d -t sanitize-test-XXXXXX)"
trap 'rm -rf "$TMP_ROOT"' EXIT

# Colors only when stdout is a TTY.
if [ -t 1 ]; then
  C_RED=$'\033[0;31m'
  C_GREEN=$'\033[0;32m'
  C_YELLOW=$'\033[1;33m'
  C_GRAY=$'\033[0;90m'
  C_RESET=$'\033[0m'
else
  C_RED=""
  C_GREEN=""
  C_YELLOW=""
  C_GRAY=""
  C_RESET=""
fi

pass=0
fail=0
failed_names=""

pass_case() {
  printf '%sPASS%s %s\n' "$C_GREEN" "$C_RESET" "$1"
  pass=$((pass + 1))
}

fail_case() {
  printf '%sFAIL%s %s\n' "$C_RED" "$C_RESET" "$1"
  if [ -n "${2:-}" ]; then
    printf '%s     %s%s\n' "$C_GRAY" "$2" "$C_RESET"
  fi
  fail=$((fail + 1))
  failed_names="${failed_names} ${1}"
}

# --- meta-test: no INPUT/ file contains placeholders ------------------------
#
# We deliberately allow placeholders in scenario 05_already_sanitized (that
# scenario PROVES the rules are idempotent). All other scenarios must keep
# their INPUTs free of placeholders, so a future maintainer cannot smuggle a
# fake "passing" fixture by pre-sanitizing the input.

meta_test_no_placeholder_in_inputs() {
  local violations=""
  while IFS= read -r -d '' f; do
    case "$f" in
      */05_already_sanitized/*) continue ;;
    esac
    # For .gz files, decompress before scanning.
    local sample
    if [[ "$f" == *.gz ]]; then
      sample="$(gunzip -c "$f" 2>/dev/null || true)"
    else
      sample="$(cat "$f")"
    fi
    if printf '%s' "$sample" | grep -q '\[REDACTED_'; then
      violations="${violations}${f}"$'\n'
    fi
  done < <(find "$FIXTURES_ROOT" -path '*/INPUT/*' -type f -print0)

  if [ -n "$violations" ]; then
    fail_case "meta::no_placeholder_in_inputs" "INPUT fixtures contain [REDACTED_]:
${violations}"
  else
    pass_case "meta::no_placeholder_in_inputs"
  fi
}

# --- common runner for "diff INPUT-vs-EXPECTED" scenarios -------------------
#
# Args: <scenario_name> <format> <input_path> <expected_dir>

run_diff_scenario() {
  local name="$1"
  local format="$2"
  local input="$3"
  local expected="$4"

  local out="${TMP_ROOT}/${name}"
  mkdir -p "$out"

  if ! HOME="$HOME" "$SANITIZER" \
        --format="$format" --input="$input" --output="$out" \
        --id-seed=42 --force >/dev/null 2>"${TMP_ROOT}/${name}.stderr"
  then
    fail_case "$name" "sanitizer exited non-zero. stderr:
$(cat "${TMP_ROOT}/${name}.stderr")"
    return
  fi

  if ! diff -ru "$expected" "$out" > "${TMP_ROOT}/${name}.diff" 2>&1; then
    fail_case "$name" "EXPECTED/ vs produced output differ:
$(cat "${TMP_ROOT}/${name}.diff")"
    return
  fi

  pass_case "$name"
}

# --- runner for "behavior-only" scenarios (no diff) -------------------------
# Args: <scenario_name> <format> <input_path> <expected_exit> <expected_stderr_substring>

run_behavior_scenario() {
  local name="$1"
  local format="$2"
  local input="$3"
  local expected_exit="$4"
  local expected_msg="$5"

  local out="${TMP_ROOT}/${name}"
  mkdir -p "$out"

  local actual_exit=0
  HOME="$HOME" "$SANITIZER" \
      --format="$format" --input="$input" --output="$out" \
      --id-seed=42 --force >/dev/null 2>"${TMP_ROOT}/${name}.stderr" \
      || actual_exit=$?

  if [ "$actual_exit" != "$expected_exit" ]; then
    fail_case "$name" "expected exit $expected_exit, got $actual_exit. stderr:
$(cat "${TMP_ROOT}/${name}.stderr")"
    return
  fi

  if [ -n "$expected_msg" ] \
     && ! grep -qF -- "$expected_msg" "${TMP_ROOT}/${name}.stderr"
  then
    fail_case "$name" "stderr did not contain expected substring: '$expected_msg'
actual stderr:
$(cat "${TMP_ROOT}/${name}.stderr")"
    return
  fi

  pass_case "$name"
}

# --- determinism test: same input + same seed -> byte-identical output ------

run_determinism_test() {
  local name="determinism::v3_happy_path_twice_same_seed"
  local input="${FIXTURES_ROOT}/aiagent_v3/01_happy_path/INPUT/aaaaaaaa-1111-2222-3333-aaaaaaaaaaaa.jsonl"

  local out1="${TMP_ROOT}/det1"
  local out2="${TMP_ROOT}/det2"
  mkdir -p "$out1" "$out2"

  HOME="$HOME" "$SANITIZER" \
      --format=aiagent_v3 --input="$input" --output="$out1" \
      --id-seed=42 --force >/dev/null 2>&1
  HOME="$HOME" "$SANITIZER" \
      --format=aiagent_v3 --input="$input" --output="$out2" \
      --id-seed=42 --force >/dev/null 2>&1

  if ! diff -ru "$out1" "$out2" > "${TMP_ROOT}/det.diff" 2>&1; then
    fail_case "$name" "two runs with the same seed produced different output:
$(cat "${TMP_ROOT}/det.diff")"
    return
  fi
  pass_case "$name"
}

# --- different-seed test: same input, different seed -> different output ----

run_different_seed_test() {
  local name="determinism::v3_happy_path_different_seed_differs"
  local input="${FIXTURES_ROOT}/aiagent_v3/01_happy_path/INPUT/aaaaaaaa-1111-2222-3333-aaaaaaaaaaaa.jsonl"

  local out1="${TMP_ROOT}/seed1"
  local out2="${TMP_ROOT}/seed2"
  mkdir -p "$out1" "$out2"

  HOME="$HOME" "$SANITIZER" \
      --format=aiagent_v3 --input="$input" --output="$out1" \
      --id-seed=1 --force >/dev/null 2>&1
  HOME="$HOME" "$SANITIZER" \
      --format=aiagent_v3 --input="$input" --output="$out2" \
      --id-seed=999 --force >/dev/null 2>&1

  if diff -rq "$out1" "$out2" >/dev/null 2>&1; then
    fail_case "$name" "different seeds produced identical output (UUIDs not seeded correctly)"
    return
  fi
  pass_case "$name"
}

# --- help / usage tests -----------------------------------------------------

run_help_test() {
  local name="usage::help_exits_zero"
  if ! "$SANITIZER" --help >/dev/null 2>&1; then
    fail_case "$name" "--help exited non-zero"
    return
  fi
  pass_case "$name"
}

run_missing_format_test() {
  local name="usage::missing_format_exits_nonzero"
  local rc=0
  "$SANITIZER" --input=/dev/null --output=/tmp/never >/dev/null 2>&1 || rc=$?
  if [ "$rc" = "0" ]; then
    fail_case "$name" "expected non-zero exit when --format is missing"
    return
  fi
  pass_case "$name"
}

# --- main -------------------------------------------------------------------

main() {
  if [ ! -x "$SANITIZER" ]; then
    printf '%sFATAL%s sanitizer not executable: %s\n' "$C_RED" "$C_RESET" "$SANITIZER" >&2
    exit 1
  fi

  printf '%s==> sanitize-fixture-test%s\n' "$C_YELLOW" "$C_RESET"

  # Meta tests.
  meta_test_no_placeholder_in_inputs

  # Diff scenarios — single-file inputs.
  run_diff_scenario "v3::01_happy_path" \
    aiagent_v3 \
    "${FIXTURES_ROOT}/aiagent_v3/01_happy_path/INPUT/aaaaaaaa-1111-2222-3333-aaaaaaaaaaaa.jsonl" \
    "${FIXTURES_ROOT}/aiagent_v3/01_happy_path/EXPECTED"

  run_diff_scenario "v3::02_sub_agent" \
    aiagent_v3 \
    "${FIXTURES_ROOT}/aiagent_v3/02_sub_agent/INPUT/bbbbbbbb-1111-2222-3333-bbbbbbbbbbbb.jsonl" \
    "${FIXTURES_ROOT}/aiagent_v3/02_sub_agent/EXPECTED"

  # Diff scenarios — directory input (ledger + payloads).
  run_diff_scenario "v3::03_with_payloads" \
    aiagent_v3 \
    "${FIXTURES_ROOT}/aiagent_v3/03_with_payloads/INPUT" \
    "${FIXTURES_ROOT}/aiagent_v3/03_with_payloads/EXPECTED"

  run_diff_scenario "v3::05_already_sanitized" \
    aiagent_v3 \
    "${FIXTURES_ROOT}/aiagent_v3/05_already_sanitized/INPUT/8e01e6e4-a120-00a7-e861-6ecf48ffb85e.jsonl" \
    "${FIXTURES_ROOT}/aiagent_v3/05_already_sanitized/EXPECTED"

  run_diff_scenario "v3::06_clean_input" \
    aiagent_v3 \
    "${FIXTURES_ROOT}/aiagent_v3/06_clean_input/INPUT/eeeeeeee-1111-2222-3333-eeeeeeeeeeee.jsonl" \
    "${FIXTURES_ROOT}/aiagent_v3/06_clean_input/EXPECTED"

  run_diff_scenario "v2::04_deep_optree" \
    aiagent_v2 \
    "${FIXTURES_ROOT}/aiagent_v2/04_deep_optree/INPUT/11111111-2222-3333-4444-555555555555.json.gz" \
    "${FIXTURES_ROOT}/aiagent_v2/04_deep_optree/EXPECTED"

  # Behavior-only scenarios.
  run_behavior_scenario "v3::07_zero_byte" \
    aiagent_v3 \
    "${FIXTURES_ROOT}/aiagent_v3/07_zero_byte/INPUT/ffffffff-1111-2222-3333-ffffffffffff.jsonl" \
    "0" \
    "skipping zero-byte ledger"

  run_behavior_scenario "v3::08_malformed_json" \
    aiagent_v3 \
    "${FIXTURES_ROOT}/aiagent_v3/08_malformed/INPUT/00000000-aaaa-bbbb-cccc-dddddddddddd.jsonl" \
    "1" \
    "malformed JSON"

  # Determinism + usage tests.
  run_determinism_test
  run_different_seed_test
  run_help_test
  run_missing_format_test

  echo
  printf '%s%d passed%s, %s%d failed%s\n' \
    "$C_GREEN" "$pass" "$C_RESET" "$C_RED" "$fail" "$C_RESET"

  if [ "$fail" -gt 0 ]; then
    printf '%sFailed:%s%s\n' "$C_RED" "$C_RESET" "$failed_names"
    exit 1
  fi
  exit 0
}

main "$@"
