#!/usr/bin/env bash
#
# Hermetic self-test for scripts/check-contract-matrix.sh. The field matrix is
# a quality gate for DB/API/TypeScript/UI contracts; this harness proves the
# checker fails closed on malformed rows, stale exposed-field evidence, broken
# payload-kind artifact-class normalization, and missing test references.
set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CHECKER="${REPO_ROOT}/scripts/check-contract-matrix.sh"
TMP_ROOT="$(mktemp -d -t contract-matrix-test-XXXXXX)"
trap 'rm -rf "$TMP_ROOT"' EXIT

if [ -t 1 ]; then
  C_RED=$'\033[0;31m'; C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[1;33m'
  C_GRAY=$'\033[0;90m'; C_RESET=$'\033[0m'
else
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_GRAY=""; C_RESET=""
fi

pass=0
fail=0
failed_names=""

pass_case() { printf '%sPASS%s %s\n' "$C_GREEN" "$C_RESET" "$1"; pass=$((pass + 1)); }
fail_case() {
  printf '%sFAIL%s %s\n' "$C_RED" "$C_RESET" "$1"
  [ -n "${2:-}" ] && printf '%s     %s%s\n' "$C_GRAY" "$2" "$C_RESET"
  fail=$((fail + 1)); failed_names="${failed_names} ${1}"
}
contains_text() {
  local haystack="$1" needle="$2"
  grep -qF -- "$needle" <<< "$haystack"
}

matrix_test_refs() {
  awk -F': ' '
    /^[[:space:]]+test_refs:/ {
      v=$2
      gsub(/^"/, "", v)
      gsub(/"$/, "", v)
      n=split(v, parts, ",")
      for (i=1; i<=n; i++) {
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", parts[i])
        if (parts[i] != "") print parts[i]
      }
    }
  ' "$1" | sort -u
}

new_fixture() {
  local dir="${TMP_ROOT}/fix.$RANDOM.$RANDOM"
  mkdir -p "$dir/scripts/lib/check-contract-matrix" \
    "$dir/scripts/test" \
    "$dir/testdata/contracts" \
    "$dir/frontend/src/api" \
    "$dir/frontend/src/viz" \
    "$dir/internal/presenter"

  cp "$REPO_ROOT/go.mod" "$dir/go.mod"
  cp "$REPO_ROOT/scripts/check-contract-matrix.sh" "$dir/scripts/check-contract-matrix.sh"
  cp "$REPO_ROOT/scripts/lib/check-contract-matrix/main.go" "$dir/scripts/lib/check-contract-matrix/main.go"
  cp "$REPO_ROOT/testdata/contracts/field-matrix.yaml" "$dir/testdata/contracts/field-matrix.yaml"
  cp "$REPO_ROOT/frontend/src/api/types.ts" "$dir/frontend/src/api/types.ts"
  cp "$REPO_ROOT/frontend/src/api/payloads.ts" "$dir/frontend/src/api/payloads.ts"
  cp "$REPO_ROOT/frontend/src/viz/trace.ts" "$dir/frontend/src/viz/trace.ts"

  local presenter_sources=()
  local src
  shopt -s nullglob
  presenter_sources=("$REPO_ROOT"/internal/presenter/*.go)
  shopt -u nullglob
  for src in "${presenter_sources[@]}"; do
    [[ "$src" == *_test.go ]] && continue
    cp "$src" "$dir/internal/presenter/"
  done

  while IFS= read -r ref; do
    [[ -z "$ref" ]] && continue
    mkdir -p "$dir/$(dirname "$ref")"
    if [[ -f "$REPO_ROOT/$ref" ]]; then
      cp "$REPO_ROOT/$ref" "$dir/$ref"
    else
      : > "$dir/$ref"
    fi
  done < <(matrix_test_refs "$dir/testdata/contracts/field-matrix.yaml")

  chmod +x "$dir/scripts/check-contract-matrix.sh"
  printf '%s' "$dir"
}

run_checker() {
  RC=0
  OUT="$( ( cd "$1" && ./scripts/check-contract-matrix.sh ) 2>&1 )" || RC=$?
}

assert_drift() {
  local name="$1" fix="$2" needle="$3"
  run_checker "$fix"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "checker exited 0 but a ${needle} mismatch was planted:
$OUT"; return
  fi
  if ! contains_text "$OUT" "$needle"; then
    fail_case "$name" "non-zero exit but report did not name '${needle}':
$OUT"; return
  fi
  pass_case "$name"
}

case_clean_passes() {
  local name="clean::unmodified_fixture_passes"
  local fix; fix="$(new_fixture)"
  run_checker "$fix"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "clean fixture was flagged as drift:
$OUT"; return
  fi
  if ! contains_text "$OUT" "[PASS] contract-matrix"; then
    fail_case "$name" "clean fixture exited 0 but printed no [PASS] summary:
$OUT"; return
  fi
  pass_case "$name"
}

case_invalid_enum_fails() {
  local name="schema::invalid_state_enum"
  local fix; fix="$(new_fixture)"
  sed -i '0,/state: "exposed"/s//state: "bogus-state"/' \
    "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "invalid value"
}

case_missing_matrix_fails() {
  local name="schema::missing_matrix"
  local fix; fix="$(new_fixture)"
  rm -f "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "field-matrix.yaml"
}

case_empty_matrix_fails() {
  local name="schema::empty_matrix"
  local fix; fix="$(new_fixture)"
  printf 'version: 1\nrows:\n' > "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "no rows found"
}

case_duplicate_row_fails() {
  local name="schema::duplicate_row"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/testdata/contracts/field-matrix.yaml" <<'EOF'

  - entity: "session"
    field: "provider"
    entity_kind: "session"
    db_column: "sessions.provider"
    derived_from: ""
    rest_surfaces: "/api/sessions"
    typescript_types: "SessionListItem"
    ui_surfaces: "Session list,Compare"
    state: "exposed"
    intent: "primary_list"
    include_token: ""
    privacy_class: "public"
    adapter_population: "broad"
    index_status: "indexed"
    stats_dimension_eligible: "eligible"
    subscription_filter_eligible: "excluded"
    internal_reason: ""
    sow_ref: "SOW-0105"
    pending_ref: ""
    test_refs: "internal/presenter/sessions_list_test.go,frontend/src/api/types.contract.test.ts"
    artifact_class: ""
EOF
  assert_drift "$name" "$fix" "duplicate row identity"
}

case_missing_required_key_fails() {
  local name="schema::missing_required_key"
  local fix; fix="$(new_fixture)"
  perl -0pi -e 's/\n    privacy_class: "public"//' "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "missing required field"
}

case_missing_include_token_fails() {
  local name="schema::missing_include_token_for_exposed_via_include"
  local fix; fix="$(new_fixture)"
  sed -i '0,/include_token: "proof"/s//include_token: ""/' \
    "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "exposed-via-include rows must name include_token"
}

case_missing_internal_reason_fails() {
  local name="schema::missing_internal_reason"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/testdata/contracts/field-matrix.yaml" <<'EOF'

  - entity: "session"
    field: "phantom_internal"
    entity_kind: "session"
    db_column: "sessions.provider"
    derived_from: ""
    rest_surfaces: "/api/sessions/:id"
    typescript_types: "SessionDetail"
    ui_surfaces: ""
    state: "internal-only"
    intent: "internal_only"
    include_token: ""
    privacy_class: "internal"
    adapter_population: "none"
    index_status: "not_applicable"
    stats_dimension_eligible: "excluded"
    subscription_filter_eligible: "excluded"
    internal_reason: ""
    sow_ref: "SOW-0105"
    pending_ref: ""
    test_refs: ""
    artifact_class: ""
EOF
  assert_drift "$name" "$fix" "internal-only rows must explain internal_reason"
}

case_exposed_field_missing_fails() {
  local name="evidence::exposed_field_missing_from_go_and_ts"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/testdata/contracts/field-matrix.yaml" <<'EOF'

  - entity: "session"
    field: "phantom_provider"
    entity_kind: "session"
    db_column: "sessions.provider"
    derived_from: ""
    rest_surfaces: "/api/sessions/:id"
    typescript_types: "SessionDetail"
    ui_surfaces: "Session detail"
    state: "exposed"
    intent: "detail"
    include_token: ""
    privacy_class: "public"
    adapter_population: "broad"
    index_status: "indexed"
    stats_dimension_eligible: "eligible"
    subscription_filter_eligible: "excluded"
    internal_reason: ""
    sow_ref: "SOW-0105"
    pending_ref: ""
    test_refs: "internal/presenter/session_detail_test.go"
    artifact_class: ""
EOF
  assert_drift "$name" "$fix" "lacks json field"
}

case_unknown_typescript_type_fails() {
  local name="evidence::unknown_typescript_contract_type"
  local fix; fix="$(new_fixture)"
  sed -i '0,/typescript_types: "SessionListItem"/s//typescript_types: "MissingFrontendType"/' \
    "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "unknown TypeScript contract type"
}

case_tokens_field_evidence_is_not_skipped() {
  local name="evidence::tokens_cache_field_missing_from_ts"
  local fix; fix="$(new_fixture)"
  perl -0pi -e 's/(export interface TurnDetail \{.*?\n  )tokens_cache_read: number;/${1}tokens_cache_read_removed: number;/s' \
    "$fix/frontend/src/api/types.ts"
  assert_drift "$name" "$fix" "tokens_cache_read row is exposed but TypeScript interface TurnDetail lacks field"
}

case_payload_kind_artifact_class_fails() {
  local name="payload-kind::artifact_class_mismatch"
  local fix; fix="$(new_fixture)"
  sed -i '0,/artifact_class: "llm_request"/s//artifact_class: ""/' \
    "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "artifact_class"
}

case_missing_test_ref_fails() {
  local name="test-refs::missing_referenced_test"
  local fix; fix="$(new_fixture)"
  rm -f "$fix/frontend/src/api/types.contract.test.ts"
  assert_drift "$name" "$fix" "test_refs path does not exist"
}

main() {
  if [ ! -x "$CHECKER" ]; then
    printf '%sFATAL%s checker not executable: %s\n' "$C_RED" "$C_RESET" "$CHECKER" >&2
    exit 1
  fi
  printf '%s==> check-contract-matrix-test%s\n' "$C_YELLOW" "$C_RESET"

  case_clean_passes
  case_missing_matrix_fails
  case_empty_matrix_fails
  case_duplicate_row_fails
  case_missing_required_key_fails
  case_invalid_enum_fails
  case_missing_include_token_fails
  case_missing_internal_reason_fails
  case_exposed_field_missing_fails
  case_unknown_typescript_type_fails
  case_tokens_field_evidence_is_not_skipped
  case_payload_kind_artifact_class_fails
  case_missing_test_ref_fails

  echo
  printf '%s%d passed%s, %s%d failed%s\n' \
    "$C_GREEN" "$pass" "$C_RESET" "$C_RED" "$fail" "$C_RESET"
  if [ "$fail" -gt 0 ]; then
    printf '%sFailed:%s%s\n' "$C_RED" "$C_RESET" "$failed_names"
    exit 1
  fi
}

main "$@"
