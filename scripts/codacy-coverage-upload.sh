#!/usr/bin/env bash
# Reporting-only Codacy coverage upload state machine for GitHub Actions.
set -euo pipefail

readonly COVERAGE_ROOT=".codacy-coverage"
readonly GO_COVERAGE_DIR="${COVERAGE_ROOT}/go"
readonly FRONTEND_COVERAGE_DIR="${COVERAGE_ROOT}/frontend"
readonly REPORTER_URL="https://coverage.codacy.com/get.sh"

notice() {
  echo "::notice::$1"
}

annotate_error() {
  echo "::error::$1"
}

annotate_file_error() {
  local file="$1"
  local message="$2"
  echo "::error file=${file}::${message}"
}

select_token_mode_or_skip() {
  if [[ "${GITHUB_EVENT_NAME:-}" == "pull_request" ]]; then
    notice "Codacy coverage upload is skipped on pull_request events before token-mode selection."
    exit 0
  fi

  if [[ -n "${CODACY_PROJECT_TOKEN:-}" ]]; then
    echo "Using Codacy project-token mode."
    unset CODACY_API_TOKEN CODACY_ORGANIZATION_PROVIDER CODACY_USERNAME CODACY_PROJECT_NAME
    return 0
  fi

  if [[ -z "${CODACY_API_TOKEN:-}" ]]; then
    notice "No Codacy coverage token is configured; skipping Codacy coverage upload."
    exit 0
  fi

  echo "Using Codacy account-token mode."
  unset CODACY_PROJECT_TOKEN
}

find_reports() {
  local candidate

  mkdir -p "$GO_COVERAGE_DIR" "$FRONTEND_COVERAGE_DIR"

  go_report="$(find "$GO_COVERAGE_DIR" -type f -name coverage.out -size +0c -print -quit)"
  if [[ -z "$go_report" ]]; then
    candidate="$(find "$GO_COVERAGE_DIR" -type f -name coverage.out -print -quit)"
    annotate_file_error "${candidate:-$GO_COVERAGE_DIR}" "Go coverage report is missing or empty"
  fi

  frontend_report="$(find "$FRONTEND_COVERAGE_DIR" -type f -name lcov.info -size +0c -print -quit)"
  if [[ -z "$frontend_report" ]]; then
    candidate="$(find "$FRONTEND_COVERAGE_DIR" -type f -name lcov.info -print -quit)"
    annotate_file_error "${candidate:-$FRONTEND_COVERAGE_DIR}" "frontend LCOV report is missing or empty"
  fi
}

normalize_frontend_lcov() {
  local input="$1"
  local output="$2"

  awk '
    /^SF:/ {
      path = substr($0, 4)
      if (path ~ /^src\//) {
        print "SF:frontend/" path
        next
      }
      if (path ~ /^\.\/src\//) {
        sub(/^\.\//, "", path)
        print "SF:frontend/" path
        next
      }
      if (path ~ /\/frontend\/src\//) {
        sub(/^.*\/frontend\/src\//, "frontend/src/", path)
        print "SF:" path
        next
      }
    }
    { print }
  ' "$input" > "$output"
}

download_reporter() {
  local reporter="$1"

  if ! curl -fsSL --retry 3 --retry-delay 2 "$REPORTER_URL" -o "$reporter"; then
    annotate_error "Codacy reporter download failed"
    exit 0
  fi
  if [[ ! -s "$reporter" ]]; then
    annotate_error "Codacy reporter download produced an empty file"
    exit 0
  fi
  if ! head -n 1 "$reporter" | grep -Eq '^#!.*(bash|/sh| sh)([[:space:]]|$)'; then
    annotate_error "Codacy reporter download did not produce a shell script"
    exit 0
  fi
  if ! bash -n "$reporter"; then
    annotate_error "Codacy reporter download produced an invalid shell script"
    exit 0
  fi
}

upload_reports() {
  local reporter="$1"
  local go_report_path="$2"
  local frontend_report_path="$3"
  local upload_rc=0
  local partial_attempted=0

  if [[ -n "$go_report_path" ]]; then
    partial_attempted=1
    if ! bash "$reporter" report --partial --force-coverage-parser go -r "$go_report_path"; then
      annotate_file_error "$go_report_path" "Codacy Go coverage partial upload failed"
      upload_rc=1
    fi
  fi

  if [[ -n "$frontend_report_path" ]]; then
    partial_attempted=1
    if ! bash "$reporter" report --partial --force-coverage-parser lcov -r "$frontend_report_path"; then
      annotate_file_error "$frontend_report_path" "Codacy frontend coverage partial upload failed"
      upload_rc=1
    fi
  fi

  if [[ "$partial_attempted" -eq 1 ]]; then
    if ! bash "$reporter" final; then
      annotate_error "Codacy coverage final notification failed"
      upload_rc=1
    fi
  fi

  if [[ "$upload_rc" -ne 0 ]]; then
    annotate_error "Codacy coverage upload failed; see reporter output above"
    exit 0
  fi
}

go_report=""
frontend_report=""
frontend_report_root=""

select_token_mode_or_skip
find_reports

if [[ -z "$go_report" && -z "$frontend_report" ]]; then
  notice "No coverage reports found; skipping Codacy reporter download."
  exit 0
fi

if [[ -n "$frontend_report" ]]; then
  frontend_report_root="${FRONTEND_COVERAGE_DIR}/lcov-root.info"
  normalize_frontend_lcov "$frontend_report" "$frontend_report_root"
fi

reporter="$(mktemp)"
trap 'rm -f "$reporter"' EXIT
download_reporter "$reporter"
upload_reports "$reporter" "$go_report" "$frontend_report_root"
