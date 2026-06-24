#!/usr/bin/env bash
# Hermetic self-test for scripts/check-ingestion-parity.sh. The wrapper is a
# named quality gate; this proves it invokes the required Go parity test command,
# propagates failures, and rejects unsupported modes fail-closed.
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECK="${REPO_ROOT}/scripts/check-ingestion-parity.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0

pass_case() {
  echo -e "  ${GREEN}PASS${NC} $1"
  pass=$((pass + 1))
}

fail_case() {
  echo -e "  ${RED}FAIL${NC} $1"
  [[ -n "${2:-}" ]] && printf '       %s\n' "$2"
  fail=$((fail + 1))
}

write_fake_go() {
  local exit_code="$1"
  mkdir -p "$TMP/bin"
  cat > "$TMP/bin/go" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$TMP/go.args"
if [[ "\$*" == *"-list=^Fuzz"* ]]; then
  printf '%s\n' \
    FuzzDiffManifests \
    FuzzExtractAIAgentV2Source \
    FuzzExtractAIAgentV3Source \
    FuzzExtractClaudeCodeSource \
    FuzzExtractCodexSource \
    FuzzExtractOpencodeSource
fi
exit "$exit_code"
EOF
  chmod +x "$TMP/bin/go"
}

go_was_invoked_with() {
  local pattern="$1"
  [[ -f "$TMP/go.args" ]] && grep -Fq -- "$pattern" "$TMP/go.args"
}

case_fixtures_invokes_go_tests() {
  local name="fixtures::invokes_required_go_test_command"
  rm -f "$TMP/go.args"
  write_fake_go 0
  local out rc=0
  out="$(PATH="$TMP/bin:$PATH" bash "$CHECK" --fixtures 2>&1)" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    fail_case "$name" "exit $rc, output: $out"
    return
  fi
  local args
  args="$(<"$TMP/go.args")"
  if ! go_was_invoked_with "test -count=1 ./internal/parity ./internal/ingest ./cmd/ai-viewer-ingest"; then
    fail_case "$name" "unexpected go args: $args"
    return
  fi
  if ! go_was_invoked_with "test -list=^Fuzz ./internal/parity"; then
    fail_case "$name" "parity fuzz target-set lock command missing; go args: $args"
    return
  fi
  if ! go_was_invoked_with "test -run=^Fuzz ./internal/parity"; then
    fail_case "$name" "parity fuzz seed command missing; go args: $args"
    return
  fi
  if ! go_was_invoked_with "CheckParity"; then
    fail_case "$name" "CLI parity test surface missing; go args: $args"
    return
  fi
  if ! go_was_invoked_with "Matrix"; then
    fail_case "$name" "matrix drift test surface missing; go args: $args"
    return
  fi
  pass_case "$name"
}

case_go_failure_propagates() {
  local name="fixtures::go_failure_propagates"
  write_fake_go 43
  local rc=0
  PATH="$TMP/bin:$PATH" bash "$CHECK" --fixtures >"$TMP/fail.out" 2>&1 || rc=$?
  if [[ "$rc" -eq 0 ]]; then
    fail_case "$name" "wrapper returned 0 while fake go failed"
    return
  fi
  pass_case "$name"
}

case_invalid_mode_rejected() {
  local name="usage::invalid_mode_rejected"
  rm -f "$TMP/go.args"
  write_fake_go 0
  local rc=0
  PATH="$TMP/bin:$PATH" bash "$CHECK" >"$TMP/usage.out" 2>&1 || rc=$?
  if [[ "$rc" -ne 2 ]]; then
    fail_case "$name" "exit $rc, want 2"
    return
  fi
  if [[ -f "$TMP/go.args" ]]; then
    fail_case "$name" "go was invoked for invalid usage"
    return
  fi
  pass_case "$name"
}

case_fixtures_invokes_go_tests
case_go_failure_propagates
case_invalid_mode_rejected

echo
if [[ "$fail" -eq 0 ]]; then
  echo -e "${GREEN}[ok]${NC} check-ingestion-parity self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} check-ingestion-parity self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
