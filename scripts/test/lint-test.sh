#!/usr/bin/env bash
#
# Hermetic self-test for scripts/lint.sh formatter scoping. It proves the
# standalone Go formatters use tracked Go files only, not local ignored/untracked
# files that CI will never see.

set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TMP_ROOT="$(mktemp -d -t lint-test-XXXXXX)"
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
  fail=$((fail + 1))
  failed_names="${failed_names} ${1}"
}

assert_contains() {
  local name="$1" file="$2" needle="$3"
  if ! grep -Fq "$needle" "$file"; then
    fail_case "$name" "${file} does not contain required contract: ${needle}"
    return 1
  fi
  return 0
}

find_dot_formatter_walks() {
  local file="$1"
  awk '
    /^[[:space:]]*#/ { next }
    {
      line = $0
      gsub(/"/, "", line)
      gsub(sprintf("%c", 39), "", line)
      if (line ~ /gofmt[[:space:]]+-l[[:space:]]+(\.|\.\/|\.\/\.\.\.)([[:space:]]|$|[;&|)])/) {
        print FILENAME ":" FNR ":" $0
        bad = 1
      }
      if (line ~ /goimports[[:space:]]+-l[[:space:]]+(\.|\.\/|\.\/\.\.\.)([[:space:]]|$|[;&|)])/) {
        print FILENAME ":" FNR ":" $0
        bad = 1
      }
    }
    END { exit bad ? 1 : 0 }
  ' "$file"
}

assert_no_dot_formatter_walk() {
  local name="$1" file="$2" out
  if out="$(find_dot_formatter_walks "$file")"; then
    return 0
  fi
  fail_case "$name" "dot-walk formatter invocation found:
${out}"
  return 1
}

case_static_guard_detects_directory_walks() {
  local name="static::directory_walk_detection"
  local dir="${TMP_ROOT}/${name//[^A-Za-z0-9]/_}"
  local bad_file="${dir}/bad.sh"
  local good_file="${dir}/good.sh"
  local out
  mkdir -p "$dir"

  cat > "$bad_file" <<'EOF'
gofmt -l "."
goimports -l '.'
gofmt -l ./
goimports -l "./"
gofmt -l './...'
goimports -l ./...
EOF
  if out="$(find_dot_formatter_walks "$bad_file")"; then
    fail_case "$name" "formatter directory walks were not detected"
    return 0
  fi
  if ! printf '%s\n' "$out" | grep -Fq 'gofmt -l "."'; then
    fail_case "$name" "quoted gofmt exact-dot walk was not reported"
    return 0
  fi
  if ! printf '%s\n' "$out" | grep -Fq "goimports -l '.'"; then
    fail_case "$name" "quoted goimports exact-dot walk was not reported"
    return 0
  fi
  if ! printf '%s\n' "$out" | grep -Fq 'gofmt -l ./'; then
    fail_case "$name" "gofmt ./ directory walk was not reported"
    return 0
  fi
  if ! printf '%s\n' "$out" | grep -Fq 'goimports -l "./"'; then
    fail_case "$name" "quoted goimports ./ directory walk was not reported"
    return 0
  fi
  if ! printf '%s\n' "$out" | grep -Fq "gofmt -l './...'"; then
    fail_case "$name" "quoted gofmt ./... directory walk was not reported"
    return 0
  fi
  if ! printf '%s\n' "$out" | grep -Fq 'goimports -l ./...'; then
    fail_case "$name" "goimports ./... directory walk was not reported"
    return 0
  fi

  cat > "$good_file" <<'EOF'
out="$(gofmt -l "./$f" 2>&1)"
out="$(goimports -l "./$f" 2>&1)"
EOF
  if ! out="$(find_dot_formatter_walks "$good_file")"; then
    fail_case "$name" "per-file formatter form was falsely reported:
${out}"
    return 0
  fi

  pass_case "$name"
}

run_formatter_on_tracked_go() {
  local label="$1"
  shift
  local f out
  while IFS= read -r -d '' f; do
    if ! out="$("$@" -l "./$f" 2>&1)"; then
      printf '%s failed on tracked file %q:\n%s\n' "$label" "$f" "$out" >&2
      return 1
    fi
    if [[ -n "$out" ]]; then
      printf '%s reported unformatted tracked file %q:\n%s\n' "$label" "$f" "$out" >&2
      return 1
    fi
  done < <(git ls-files -z -- '*.go')
}

go_bin_dir() {
  command -v go >/dev/null 2>&1 || {
    printf 'go is required to install goimports@latest\n' >&2
    return 1
  }

  local gobin gopath
  if ! gobin="$(GO111MODULE=off go env GOBIN 2>/dev/null)"; then
    printf 'failed to resolve GOBIN with go env\n' >&2
    return 1
  fi
  if [[ -z "$gobin" ]]; then
    if ! gopath="$(GO111MODULE=off go env GOPATH 2>/dev/null)"; then
      printf 'failed to resolve GOPATH with go env\n' >&2
      return 1
    fi
    if [[ -z "$gopath" ]]; then
      printf 'go env returned empty GOBIN and GOPATH\n' >&2
      return 1
    fi
    gobin="${gopath}/bin"
  fi
  printf '%s\n' "$gobin"
}

find_or_install_goimports() {
  if command -v goimports >/dev/null 2>&1; then
    command -v goimports
    return 0
  fi

  local gobin
  gobin="$(go_bin_dir)" || return 1
  if [[ -x "${gobin}/goimports" ]]; then
    printf '%s\n' "${gobin}/goimports"
    return 0
  fi

  printf 'installing goimports@latest for lint formatter-scope self-test\n' >&2
  if ! go install golang.org/x/tools/cmd/goimports@latest; then
    printf 'failed to install goimports@latest\n' >&2
    return 1
  fi
  if [[ ! -x "${gobin}/goimports" ]]; then
    printf 'goimports was not installed at expected path: %s\n' "${gobin}/goimports" >&2
    return 1
  fi
  printf '%s\n' "${gobin}/goimports"
}

case_static_contract() {
  local name="static::production_formatter_contract"
  local lint_script="${REPO_ROOT}/scripts/lint.sh"
  local ci_workflow="${REPO_ROOT}/.github/workflows/ci.yml"

  assert_contains "$name" "$lint_script" "git ls-files -z -- '*.go'" || return 0
  assert_contains "$name" "$ci_workflow" "git ls-files -z -- '*.go'" || return 0
  assert_no_dot_formatter_walk "$name" "$lint_script" || return 0
  assert_no_dot_formatter_walk "$name" "$ci_workflow" || return 0

  pass_case "$name"
}

case_tracked_formatter_scope_excludes_local_files() {
  local name="behavior::tracked_formatter_scope_excludes_local_files"
  local repo="${TMP_ROOT}/${name//[^A-Za-z0-9]/_}"
  mkdir -p "$repo"
  local out
  if ! out="$(
    {
    cd "$repo"
    git init -q

    cat > .gitignore <<'EOF'
frontend/node_modules/
EOF
    mkdir -p "pkg with spaces" frontend/node_modules/bad
    cat > "pkg with spaces/clean.go" <<'EOF'
package clean

func Value() int {
	return 1
}
EOF
    cat > frontend/node_modules/bad/bad.go <<'EOF'
package bad

func Broken( {
EOF
    cat > scratch_untracked.go <<'EOF'
package scratch

func Broken( {
EOF

    git add -- .gitignore "pkg with spaces/clean.go"

    if ! git check-ignore -q frontend/node_modules/bad/bad.go; then
      printf 'ignored malformed Go file is not ignored by git\n' >&2
      exit 1
    fi
    if ! git ls-files --others --exclude-standard -- scratch_untracked.go | grep -Fxq scratch_untracked.go; then
      printf 'untracked malformed Go file is not visible as untracked\n' >&2
      exit 1
    fi
    if gofmt -l . >/dev/null 2>&1; then
      printf 'precondition failed: gofmt -l . did not trip on malformed local Go files\n' >&2
      exit 1
    fi

    run_formatter_on_tracked_go "gofmt" gofmt
    goimports_bin=""
    if ! goimports_bin="$(find_or_install_goimports)"; then
      exit 1
    fi
    if "$goimports_bin" -l . >/dev/null 2>&1; then
      printf 'precondition failed: goimports -l . did not trip on malformed local Go files\n' >&2
      exit 1
    fi
    run_formatter_on_tracked_go "goimports" "$goimports_bin"
    } 2>&1
  )"; then
    fail_case "$name" "$out"
    return 0
  fi
  [ -n "$out" ] && printf '%s\n' "$out"

  pass_case "$name"
}

main() {
  printf '%s==> lint-test%s\n' "$C_YELLOW" "$C_RESET"

  case_static_guard_detects_directory_walks
  case_static_contract
  case_tracked_formatter_scope_excludes_local_files

  echo
  printf '%s%d passed%s, %s%d failed%s\n' \
    "$C_GREEN" "$pass" "$C_RESET" "$C_RED" "$fail" "$C_RESET"

  if [ "$fail" -gt 0 ]; then
    printf '%sFailed:%s%s\n' "$C_RED" "$C_RESET" "$failed_names"
    exit 1
  fi
}

main "$@"
