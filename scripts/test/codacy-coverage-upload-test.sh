#!/usr/bin/env bash
# Hermetic self-test for scripts/codacy-coverage-upload.sh.
#
# Run: scripts/test/codacy-coverage-upload-test.sh
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/codacy-coverage-upload.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0

record_pass() {
  echo -e "  ${GREEN}PASS${NC} $1"
  pass=$((pass + 1))
}

record_fail() {
  echo -e "  ${RED}FAIL${NC} $1"
  fail=$((fail + 1))
}

assert_file_contains() {
  local file="$1" needle="$2" desc="$3"
  if grep -Fq -- "$needle" "$file"; then
    record_pass "$desc"
  else
    record_fail "$desc"
    sed 's/^/      /' "$file" || true
  fi
}

assert_file_not_contains() {
  local file="$1" needle="$2" desc="$3"
  if grep -Fq -- "$needle" "$file"; then
    record_fail "$desc"
    sed 's/^/      /' "$file" || true
  else
    record_pass "$desc"
  fi
}

assert_file_empty() {
  local file="$1" desc="$2"
  if [[ -s "$file" ]]; then
    record_fail "$desc"
    sed 's/^/      /' "$file" || true
  else
    record_pass "$desc"
  fi
}

assert_line_count() {
  local file="$1" pattern="$2" want="$3" desc="$4"
  local got
  got="$(grep -Ec -- "$pattern" "$file" || true)"
  if [[ "$got" == "$want" ]]; then
    record_pass "$desc"
  else
    record_fail "$desc (want ${want}, got ${got})"
    sed 's/^/      /' "$file" || true
  fi
}

assert_final_after_partials() {
  local file="$1" desc="$2"
  local final_line last_partial_line
  final_line="$(grep -nE '^ARGS final$' "$file" | tail -n 1 | cut -d: -f1 || true)"
  last_partial_line="$(grep -nE '^ARGS report ' "$file" | tail -n 1 | cut -d: -f1 || true)"
  if [[ -n "$final_line" && -n "$last_partial_line" && "$final_line" -gt "$last_partial_line" ]]; then
    record_pass "$desc"
  else
    record_fail "$desc"
    sed 's/^/      /' "$file" || true
  fi
}

assert_exit_zero() {
  local rc="$1" desc="$2" out="$3"
  if [[ "$rc" -eq 0 ]]; then
    record_pass "$desc"
  else
    record_fail "$desc (exit ${rc})"
    sed 's/^/      /' "$out" || true
  fi
}

assert_codacy_workflow_pr_boundary() {
  local workflow="${REPO_ROOT}/.github/workflows/ci.yml"
  local job_block="${TMP}/codacy-coverage-job.yml"
  awk '
    /^  codacy-coverage:[[:space:]]*$/ {
      in_job = 1
      print
      next
    }
    in_job && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
      exit
    }
    in_job {
      print
    }
  ' "$workflow" > "$job_block"

  if [[ ! -s "$job_block" ]]; then
    record_fail "codacy-coverage job exists in CI workflow"
    return
  fi
  record_pass "codacy-coverage job exists in CI workflow"

  local steps_line if_line condition first_sensitive_line
  steps_line="$(awk '/^    steps:[[:space:]]*$/ { print NR; exit }' "$job_block")"
  if [[ -z "$steps_line" ]]; then
    record_fail "codacy-coverage job has steps"
    sed 's/^/      /' "$job_block" || true
    return
  fi

  if_line="$(awk -v steps_line="$steps_line" 'NR < steps_line && /^    if:[[:space:]]*/ { print NR; exit }' "$job_block")"
  if [[ -z "$if_line" ]]; then
    record_fail "codacy-coverage PR exclusion is a job-level if before steps"
    sed 's/^/      /' "$job_block" || true
    return
  fi
  record_pass "codacy-coverage PR exclusion is a job-level if before steps"

  condition="$(
    awk -v if_line="$if_line" '
      NR < if_line {
        next
      }
      NR == if_line {
        sub(/^    if:[[:space:]]*/, "", $0)
        print
        next
      }
      /^    [A-Za-z0-9_-]+:[[:space:]]*$/ {
        exit
      }
      /^      / {
        print
        next
      }
      /^[[:space:]]*$/ {
        next
      }
      {
        exit
      }
    ' "$job_block" | tr '\n' ' '
  )"

  if printf '%s\n' "$condition" | grep -Eq "github[.]event_name[[:space:]]*!=[[:space:]]*['\"]pull_request['\"]"; then
    record_pass "codacy-coverage job-level if excludes pull_request"
  else
    record_fail "codacy-coverage job-level if excludes pull_request"
    printf '      job if: %s\n' "$condition"
  fi

  if printf '%s\n' "$condition" | grep -Eq "needs[.]test[.]result[[:space:]]*==[[:space:]]*['\"]success['\"]"; then
    record_pass "codacy-coverage job-level if preserves test success dependency"
  else
    record_fail "codacy-coverage job-level if preserves test success dependency"
    printf '      job if: %s\n' "$condition"
  fi

  if printf '%s\n' "$condition" | grep -Eq "needs[.]frontend[.]result[[:space:]]*==[[:space:]]*['\"]success['\"]"; then
    record_pass "codacy-coverage job-level if preserves frontend success dependency"
  else
    record_fail "codacy-coverage job-level if preserves frontend success dependency"
    printf '      job if: %s\n' "$condition"
  fi

  first_sensitive_line="$(grep -nE 'actions/checkout|actions/download-artifact|secrets[.]CODACY_|scripts/codacy-coverage-upload[.]sh' "$job_block" | head -n 1 | cut -d: -f1 || true)"
  if [[ -n "$first_sensitive_line" && "$if_line" -lt "$first_sensitive_line" ]]; then
    record_pass "codacy-coverage PR exclusion runs before checkout, artifact download, secrets, and repository script"
  else
    record_fail "codacy-coverage PR exclusion runs before checkout, artifact download, secrets, and repository script"
    sed 's/^/      /' "$job_block" || true
  fi
}

setup_case() {
  local dir
  dir="$(mktemp -d "${TMP}/case.XXXXXX")"
  mkdir -p "$dir/bin" "$dir/home" "$dir/work"
  : > "$dir/curl.log"
  : > "$dir/reporter.log"

  cat > "$dir/reporter.sh" <<'REPORTER'
#!/usr/bin/env bash
set -euo pipefail

{
  printf 'ARGS'
  printf ' %s' "$@"
  printf '\n'
  printf 'ENV project=%s api=%s provider=%s username=%s project_name=%s\n' \
    "${CODACY_PROJECT_TOKEN:+set}" \
    "${CODACY_API_TOKEN:+set}" \
    "${CODACY_ORGANIZATION_PROVIDER:+set}" \
    "${CODACY_USERNAME:+set}" \
    "${CODACY_PROJECT_NAME:+set}"
} >> "${REPORTER_LOG:?}"

case "${1:-}" in
  report)
    if [[ "${REPORTER_FAIL_REPORT:-0}" == "1" ]]; then
      exit 17
    fi
    ;;
  final)
    if [[ "${REPORTER_FAIL_FINAL:-0}" == "1" ]]; then
      exit 19
    fi
    ;;
esac
REPORTER

  cat > "$dir/bin/curl" <<CURL
#!/usr/bin/env bash
set -euo pipefail

printf 'curl' >> "$dir/curl.log"
printf ' %s' "\$@" >> "$dir/curl.log"
printf '\\n' >> "$dir/curl.log"

if [[ "\${CURL_FAIL:-0}" == "1" ]]; then
  exit 22
fi

out=""
while [[ "\$#" -gt 0 ]]; do
  case "\$1" in
    -o)
      out="\${2:-}"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -z "\$out" ]]; then
  exit 2
fi

case "\${REPORTER_BOOTSTRAP:-valid}" in
  valid)
    cp "$dir/reporter.sh" "\$out"
    ;;
  empty)
    : > "\$out"
    ;;
  invalid-shebang)
    printf 'echo invalid\\n' > "\$out"
    ;;
  invalid-syntax)
    printf '#!/usr/bin/env bash\\nif then\\n' > "\$out"
    ;;
  *)
    exit 3
    ;;
esac
CURL
  chmod +x "$dir/bin/curl"

  echo "$dir"
}

run_upload() {
  local dir="$1"
  shift
  local rc=0
  (
    cd "$dir/work"
    env -i \
      PATH="$dir/bin:/usr/bin:/bin" \
      HOME="$dir/home" \
      REPORTER_LOG="$dir/reporter.log" \
      "$@" \
      bash "$SCRIPT"
  ) > "$dir/out" 2>&1 || rc=$?
  echo "$rc" > "$dir/rc"
}

make_go_report() {
  local dir="$1"
  mkdir -p "$dir/work/.codacy-coverage/go"
  cat > "$dir/work/.codacy-coverage/go/coverage.out" <<'EOF_GO'
mode: atomic
github.com/netdata/ai-viewer/internal/foo/foo.go:1.1,2.1 1 1
EOF_GO
}

make_empty_go_report() {
  local dir="$1"
  mkdir -p "$dir/work/.codacy-coverage/go"
  : > "$dir/work/.codacy-coverage/go/coverage.out"
}

make_frontend_report() {
  local dir="$1"
  mkdir -p "$dir/work/.codacy-coverage/frontend"
  cat > "$dir/work/.codacy-coverage/frontend/lcov.info" <<'EOF_LCOV'
TN:
SF:src/RootRelative.tsx
DA:1,1
end_of_record
SF:./src/App.tsx
DA:1,1
end_of_record
SF:frontend/src/Already.tsx
DA:1,1
end_of_record
SF:/tmp/workspace/frontend/src/Absolute.tsx
DA:1,1
end_of_record
SF:/tmp/workspace/other/src/Other.tsx
DA:1,1
end_of_record
EOF_LCOV
}

make_empty_frontend_report() {
  local dir="$1"
  mkdir -p "$dir/work/.codacy-coverage/frontend"
  : > "$dir/work/.codacy-coverage/frontend/lcov.info"
}

test_no_token_skip() {
  local dir
  dir="$(setup_case)"
  run_upload "$dir" GITHUB_EVENT_NAME=push
  assert_exit_zero "$(cat "$dir/rc")" "no token exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::notice::No Codacy coverage token is configured; skipping Codacy coverage upload." "no token emits notice"
  assert_file_empty "$dir/curl.log" "no token skips reporter download"
}

test_account_token_pr_skip() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=pull_request \
    CODACY_API_TOKEN=1 \
    CODACY_ORGANIZATION_PROVIDER=gh \
    CODACY_USERNAME=owner \
    CODACY_PROJECT_NAME=repo
  assert_exit_zero "$(cat "$dir/rc")" "account token on PR exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::notice::Codacy coverage upload is skipped on pull_request events before token-mode selection." "account token PR emits pre-token-selection notice"
  assert_file_not_contains "$dir/out" "Using Codacy account-token mode." "account token PR does not select account-token mode"
  assert_file_empty "$dir/curl.log" "account token PR skips reporter download"
  assert_file_empty "$dir/reporter.log" "account token PR skips reporter execution"
}

test_project_token_pr_skip() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=pull_request \
    CODACY_PROJECT_TOKEN=1 \
    CODACY_API_TOKEN=1 \
    CODACY_ORGANIZATION_PROVIDER=gh \
    CODACY_USERNAME=owner \
    CODACY_PROJECT_NAME=repo
  assert_exit_zero "$(cat "$dir/rc")" "project token on PR exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::notice::Codacy coverage upload is skipped on pull_request events before token-mode selection." "project token PR emits pre-token-selection notice"
  assert_file_not_contains "$dir/out" "Using Codacy project-token mode." "project token PR does not select project-token mode"
  assert_file_not_contains "$dir/out" "Using Codacy account-token mode." "project token PR does not select account-token mode"
  assert_file_empty "$dir/curl.log" "project token PR skips reporter download"
  assert_file_empty "$dir/reporter.log" "project token PR skips reporter execution"
}

test_project_token_precedence() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=push \
    CODACY_PROJECT_TOKEN=1 \
    CODACY_API_TOKEN=1 \
    CODACY_ORGANIZATION_PROVIDER=gh \
    CODACY_USERNAME=owner \
    CODACY_PROJECT_NAME=repo
  assert_exit_zero "$(cat "$dir/rc")" "project token precedence exits zero" "$dir/out"
  assert_file_contains "$dir/out" "Using Codacy project-token mode." "project token mode selected"
  assert_file_contains "$dir/reporter.log" "ENV project=set api= provider= username= project_name=" "account token variables unset before reporter execution"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser go -r .codacy-coverage/go/coverage.out$' 1 "project token uploads Go partial"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "project token sends final"
}

test_both_reports_missing_skip() {
  local dir
  dir="$(setup_case)"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "both missing exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/go::Go coverage report is missing or empty" "missing Go report annotated"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/frontend::frontend LCOV report is missing or empty" "missing frontend report annotated"
  assert_file_contains "$dir/out" "::notice::No coverage reports found; skipping Codacy reporter download." "both missing emits no-download notice"
  assert_file_empty "$dir/curl.log" "both missing skips reporter download"
}

test_empty_go_report_does_not_block_frontend_upload() {
  local dir
  dir="$(setup_case)"
  make_empty_go_report "$dir"
  make_frontend_report "$dir"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "empty Go plus frontend exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/go/coverage.out::Go coverage report is missing or empty" "empty Go report annotated"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser go ' 0 "empty Go report is not uploaded"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser lcov -r .codacy-coverage/frontend/lcov-root.info$' 1 "empty Go does not block frontend partial"
  assert_line_count "$dir/reporter.log" '^ARGS report ' 1 "empty Go case sends one partial total"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "empty Go case sends final"
}

test_empty_frontend_report_does_not_block_go_upload() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  make_empty_frontend_report "$dir"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "Go plus empty frontend exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/frontend/lcov.info::frontend LCOV report is missing or empty" "empty frontend report annotated"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser go -r .codacy-coverage/go/coverage.out$' 1 "empty frontend does not block Go partial"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser lcov ' 0 "empty frontend report is not uploaded"
  assert_line_count "$dir/reporter.log" '^ARGS report ' 1 "empty frontend case sends one partial total"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "empty frontend case sends final"
}

test_both_reports_empty_skip() {
  local dir
  dir="$(setup_case)"
  make_empty_go_report "$dir"
  make_empty_frontend_report "$dir"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "both empty exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/go/coverage.out::Go coverage report is missing or empty" "empty Go report annotated when both empty"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/frontend/lcov.info::frontend LCOV report is missing or empty" "empty frontend report annotated when both empty"
  assert_file_contains "$dir/out" "::notice::No coverage reports found; skipping Codacy reporter download." "both empty emits no-download notice"
  assert_file_empty "$dir/curl.log" "both empty skips reporter download"
  assert_file_empty "$dir/reporter.log" "both empty skips reporter execution"
}

test_go_only_upload() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "Go-only upload exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/frontend::frontend LCOV report is missing or empty" "Go-only annotates missing frontend"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser go -r .codacy-coverage/go/coverage.out$' 1 "Go-only sends one Go partial"
  assert_line_count "$dir/reporter.log" '^ARGS report ' 1 "Go-only sends one partial total"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "Go-only sends final"
}

test_frontend_only_upload() {
  local dir
  dir="$(setup_case)"
  make_frontend_report "$dir"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "frontend-only upload exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/go::Go coverage report is missing or empty" "frontend-only annotates missing Go"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser lcov -r .codacy-coverage/frontend/lcov-root.info$' 1 "frontend-only sends one frontend partial"
  assert_line_count "$dir/reporter.log" '^ARGS report ' 1 "frontend-only sends one partial total"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "frontend-only sends final"
}

test_both_reports_upload() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  make_frontend_report "$dir"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "both reports upload exits zero" "$dir/out"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser go -r .codacy-coverage/go/coverage.out$' 1 "both reports sends one Go partial"
  assert_line_count "$dir/reporter.log" '^ARGS report --partial --force-coverage-parser lcov -r .codacy-coverage/frontend/lcov-root.info$' 1 "both reports sends one frontend partial"
  assert_line_count "$dir/reporter.log" '^ARGS report ' 2 "both reports sends two partials total"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "both reports sends one final"
  assert_final_after_partials "$dir/reporter.log" "both reports sends final after partials"
  assert_file_not_contains "$dir/out" "::error file=.codacy-coverage/go::Go coverage report is missing" "both reports does not annotate missing Go"
  assert_file_not_contains "$dir/out" "::error file=.codacy-coverage/frontend::frontend LCOV report is missing" "both reports does not annotate missing frontend"
}

test_partial_failure_still_final() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=push \
    CODACY_PROJECT_TOKEN=1 \
    REPORTER_FAIL_REPORT=1
  assert_exit_zero "$(cat "$dir/rc")" "partial failure exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error file=.codacy-coverage/go/coverage.out::Codacy Go coverage partial upload failed" "partial failure annotated"
  assert_file_contains "$dir/out" "::error::Codacy coverage upload failed; see reporter output above" "partial failure emits aggregate annotation"
  assert_line_count "$dir/reporter.log" '^ARGS report ' 1 "partial failure attempted report"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "partial failure still sends final"
}

test_final_failure_annotation() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=push \
    CODACY_PROJECT_TOKEN=1 \
    REPORTER_FAIL_FINAL=1
  assert_exit_zero "$(cat "$dir/rc")" "final failure exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error::Codacy coverage final notification failed" "final failure annotated"
  assert_file_contains "$dir/out" "::error::Codacy coverage upload failed; see reporter output above" "final failure emits aggregate annotation"
  assert_line_count "$dir/reporter.log" '^ARGS report ' 1 "final failure attempted report"
  assert_line_count "$dir/reporter.log" '^ARGS final$' 1 "final failure attempted final"
}

test_reporter_download_failure() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=push \
    CODACY_PROJECT_TOKEN=1 \
    CURL_FAIL=1
  assert_exit_zero "$(cat "$dir/rc")" "download failure exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error::Codacy reporter download failed" "download failure annotated"
  assert_file_empty "$dir/reporter.log" "download failure skips reporter execution"
}

test_empty_reporter_bootstrap() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=push \
    CODACY_PROJECT_TOKEN=1 \
    REPORTER_BOOTSTRAP=empty
  assert_exit_zero "$(cat "$dir/rc")" "empty reporter exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error::Codacy reporter download produced an empty file" "empty reporter annotated"
  assert_file_empty "$dir/reporter.log" "empty reporter skips execution"
}

test_invalid_shebang_reporter_bootstrap() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=push \
    CODACY_PROJECT_TOKEN=1 \
    REPORTER_BOOTSTRAP=invalid-shebang
  assert_exit_zero "$(cat "$dir/rc")" "invalid shebang reporter exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error::Codacy reporter download did not produce a shell script" "invalid shebang annotated"
  assert_file_empty "$dir/reporter.log" "invalid shebang skips execution"
}

test_invalid_syntax_reporter_bootstrap() {
  local dir
  dir="$(setup_case)"
  make_go_report "$dir"
  run_upload "$dir" \
    GITHUB_EVENT_NAME=push \
    CODACY_PROJECT_TOKEN=1 \
    REPORTER_BOOTSTRAP=invalid-syntax
  assert_exit_zero "$(cat "$dir/rc")" "invalid syntax reporter exits zero" "$dir/out"
  assert_file_contains "$dir/out" "::error::Codacy reporter download produced an invalid shell script" "invalid syntax annotated"
  assert_file_empty "$dir/reporter.log" "invalid syntax skips execution"
}

test_frontend_lcov_normalization() {
  local dir
  dir="$(setup_case)"
  make_frontend_report "$dir"
  run_upload "$dir" GITHUB_EVENT_NAME=push CODACY_PROJECT_TOKEN=1
  assert_exit_zero "$(cat "$dir/rc")" "LCOV normalization run exits zero" "$dir/out"
  local normalized="$dir/work/.codacy-coverage/frontend/lcov-root.info"
  assert_file_contains "$normalized" "SF:frontend/src/RootRelative.tsx" "relative src path normalized"
  assert_file_contains "$normalized" "SF:frontend/src/App.tsx" "dot-relative src path normalized"
  assert_file_contains "$normalized" "SF:frontend/src/Already.tsx" "already-prefixed frontend path preserved"
  assert_file_contains "$normalized" "SF:frontend/src/Absolute.tsx" "absolute frontend path normalized"
  assert_file_contains "$normalized" "SF:/tmp/workspace/other/src/Other.tsx" "unrelated absolute path preserved"
  assert_file_not_contains "$normalized" "SF:src/RootRelative.tsx" "relative src path not left behind"
  assert_file_not_contains "$normalized" "SF:./src/App.tsx" "dot-relative src path not left behind"
  assert_file_not_contains "$normalized" "SF:/tmp/workspace/frontend/src/Absolute.tsx" "absolute frontend path not left behind"
}

assert_codacy_workflow_pr_boundary

test_no_token_skip
test_account_token_pr_skip
test_project_token_pr_skip
test_project_token_precedence
test_both_reports_missing_skip
test_empty_go_report_does_not_block_frontend_upload
test_empty_frontend_report_does_not_block_go_upload
test_both_reports_empty_skip
test_go_only_upload
test_frontend_only_upload
test_both_reports_upload
test_partial_failure_still_final
test_final_failure_annotation
test_reporter_download_failure
test_empty_reporter_bootstrap
test_invalid_shebang_reporter_bootstrap
test_invalid_syntax_reporter_bootstrap
test_frontend_lcov_normalization

echo
if [[ "$fail" -eq 0 ]]; then
  echo -e "${GREEN}[ok]${NC} codacy coverage upload self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} codacy coverage upload self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
