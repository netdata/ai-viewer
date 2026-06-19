#!/usr/bin/env bash
# Hermetic self-test for Codacy tool/pattern and path-exclusion policy.
#
# The effective .codacy.yml shape is intentionally exact. Any new root or
# tool-scoped exclusion must update this guard and the SOW/spec rationale
# together, so scanner scope cannot drift silently.
#
# Run: scripts/test/codacy-config-test.sh
set -euo pipefail
IFS=$'\n\t'

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CODACY_JSON="${REPO_ROOT}/.codacy/codacy.config.json"
CODACY_YAML="${REPO_ROOT}/.codacy.yml"

CODACY_REPOSITORY_EXCLUDES=(
  ".agents/sow/current/**"
  ".agents/sow/done/**"
  ".agents/sow/pending/**"
  ".codacy/generated/**"
  "CLAUDE.md"
  "GEMINI.md"
  "cmd/ai-viewer-serve/frontend_dist/**"
  "frontend/node_modules/**"
  "frontend/coverage/**"
  "frontend/dist/**"
  "frontend/playwright-report/**"
  "frontend/test-results/**"
  "testdata/**"
  "bin/**"
)

CODACY_ESLINT_TEST_TOOLING_EXCLUDES=(
  "frontend/tests/**"
  "frontend/src/**/*.test.ts"
  "frontend/src/**/*.test.tsx"
  "frontend/src/test/**"
  "frontend/scripts/**"
)

FORBIDDEN_BROAD_EXCLUDES=(
  "frontend/src/**"
  "frontend/src/**/*"
  "frontend/**"
  "internal/**"
  "internal/**/*"
  "cmd/**"
  "cmd/**/*"
  "scripts/**"
  "scripts/**/*"
  "**/*"
  "**"
)

GENERATED_ARCHIVE_EXCLUDES=(
  ".codacy/generated/**"
  ".codacy/generated/**/*"
  "frontend/dist/**"
  "frontend/dist/**/*"
  "cmd/ai-viewer-serve/frontend_dist/**"
  "cmd/ai-viewer-serve/frontend_dist/**/*"
  "archive/**"
  "archive/**/*"
)

if [[ -t 1 ]]; then
  RED=$'\033[0;31m'
  GREEN=$'\033[0;32m'
  NC=$'\033[0m'
else
  RED=""
  GREEN=""
  NC=""
fi

pass() {
  printf '%sPASS%s %s\n' "$GREEN" "$NC" "$1"
}

fail() {
  printf '%sFAIL%s %s\n' "$RED" "$NC" "$1" >&2
  exit 1
}

require_file() {
  local file="$1"
  [[ -f "$file" ]] || fail "required file is absent: ${file#"$REPO_ROOT/"}"
}

require_command() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || fail "required command is absent: $cmd"
}

json_filter() {
  local filter="$1"
  jq -e "$filter" "$CODACY_JSON" >/dev/null
}

array_contains() {
  local needle="$1"
  shift
  local value
  for value in "$@"; do
    [[ "$value" == "$needle" ]] && return 0
  done
  return 1
}

assert_line_set_equals() {
  local label="$1"
  local expected="$2"
  local actual="$3"
  local expected_sorted actual_sorted
  expected_sorted="$(printf '%s\n' "$expected" | sed '/^$/d' | sort -u)"
  actual_sorted="$(printf '%s\n' "$actual" | sed '/^$/d' | sort -u)"
  if [[ "$expected_sorted" != "$actual_sorted" ]]; then
    printf '%s\n' "Expected ${label}:" >&2
    printf '%s\n' "$expected_sorted" >&2
    printf '%s\n' "Actual ${label}:" >&2
    printf '%s\n' "$actual_sorted" >&2
    diff -u <(printf '%s\n' "$expected_sorted") <(printf '%s\n' "$actual_sorted") >&2 || true
    fail "${label} mismatch"
  fi
}

assert_required_tool() {
  local tool="$1"
  json_filter "any(.tools[]; .toolId == \"${tool}\")" \
    || fail ".codacy/codacy.config.json is missing required Codacy tool: $tool"
}

assert_required_pattern() {
  local tool="$1"
  local pattern="$2"
  jq -e --arg tool "$tool" --arg pattern "$pattern" '
    any(.tools[]; .toolId == $tool and any(.patterns[]; .patternId == $pattern))
  ' "$CODACY_JSON" >/dev/null \
    || fail ".codacy/codacy.config.json is missing required pattern ${tool}/${pattern}"
}

extract_yaml_list_values() {
  local section="$1"
  awk -v wanted="$section" '
    function clean(line) {
      sub(/^[[:space:]]*-[[:space:]]+/, "", line)
      sub(/[[:space:]]*#.*/, "", line)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
      if (line ~ /^".*"$/ || line ~ /^\047.*\047$/) {
        line = substr(line, 2, length(line) - 2)
      }
      return line
    }

    /^exclude_paths:[[:space:]]*$/ {
      state = "repository"
      next
    }
    /^engines:[[:space:]]*$/ {
      state = ""
      in_engines = 1
      next
    }
    in_engines && /^  eslint-8:[[:space:]]*$/ {
      in_eslint = 1
      next
    }
    in_eslint && /^    exclude_paths:[[:space:]]*$/ {
      state = "eslint"
      next
    }
    in_eslint && /^  [^[:space:]-][^:]*:[[:space:]]*$/ {
      state = ""
      in_eslint = 0
      next
    }
    /^[^[:space:]-][^:]*:[[:space:]]*$/ && $0 !~ /^exclude_paths:/ && $0 !~ /^engines:/ {
      state = ""
      in_engines = 0
      in_eslint = 0
      next
    }

    state == wanted && /^[[:space:]]+-[[:space:]]+/ {
      print clean($0)
    }
  ' "$CODACY_YAML"
}

extract_json_exclude_values() {
  jq -r '.exclude[]' "$CODACY_JSON"
}

extract_json_tool_exclude_values() {
  local tool="$1"
  jq -r --arg tool "$tool" '
    .tools[] | select(.toolId == $tool) | (.exclude // [])[]
  ' "$CODACY_JSON"
}

expected_codacy_repository_excludes() {
  printf '%s\n' "${CODACY_REPOSITORY_EXCLUDES[@]}"
}

expected_codacy_eslint_test_tooling_excludes() {
  printf '%s\n' "${CODACY_ESLINT_TEST_TOOLING_EXCLUDES[@]}"
}

assert_no_forbidden_broad_excludes() {
  local surface="$1"
  shift
  local value forbidden
  for value in "$@"; do
    for forbidden in "${FORBIDDEN_BROAD_EXCLUDES[@]}"; do
      [[ "$value" != "$forbidden" ]] \
        || fail "${surface} contains forbidden broad runtime exclusion: $value"
    done
  done
}

assert_json_policy() {
  require_file "$CODACY_JSON"
  require_command jq

  local json_error
  if ! json_error="$(jq empty "$CODACY_JSON" 2>&1 >/dev/null)"; then
    fail ".codacy/codacy.config.json is invalid JSON: $json_error"
  fi

  json_filter '
    type == "object"
    and .version == 1
    and (.exclude | type == "array")
    and all(.exclude[]; type == "string" and length > 0)
    and (.tools | type == "array" and length > 0)
    and all(.tools[];
      (.toolId | type == "string" and length > 0)
      and (.patterns | type == "array")
      and all(.patterns[]; (.patternId | type == "string" and length > 0))
      and ((has("exclude") | not) or (.exclude | type == "array" and all(.[]; type == "string" and length > 0)))
    )
  ' || fail ".codacy/codacy.config.json has unexpected shape"

  local duplicate_tools
  duplicate_tools="$(jq -r '.tools[].toolId' "$CODACY_JSON" | sort | uniq -d)"
  [[ -z "$duplicate_tools" ]] \
    || fail ".codacy/codacy.config.json has duplicate toolId entries: $duplicate_tools"

  local duplicate_excludes
  duplicate_excludes="$(extract_json_exclude_values | sort | uniq -d)"
  [[ -z "$duplicate_excludes" ]] \
    || fail ".codacy/codacy.config.json has duplicate exclude entries: $duplicate_excludes"

  assert_required_tool Trivy
  assert_required_tool Semgrep
  # ESLint8 + Agentlinter removed from JSON config (SOW-0072): ESLint8 runs a
  # default Codacy config that doesn't match our project eslint.config.ts;
  # Agentlinter analyzes instruction files, not application code.

  assert_required_pattern Trivy Trivy_vulnerability_high
  assert_required_pattern Trivy Trivy_vulnerability_critical
  assert_required_pattern Trivy Trivy_secret
  assert_required_pattern Semgrep \
    Semgrep_go.lang.security.injection.tainted-sql-string.tainted-sql-string
  assert_required_pattern Semgrep \
    Semgrep_go.lang.security.audit.crypto.math_random.math-random-used
  assert_required_pattern Semgrep \
    Semgrep_yaml.github-actions.security.third-party-action-not-pinned-to-commit-sha.third-party-action-not-pinned-to-commit-sha
  # SOW-0046: PMD and SQLint stay absent from the local Codacy Analysis CLI
  # config because their Cloud-imported findings are Cloud/local-noise for this
  # repository. Reintroducing either tool requires a renewed SOW disposition.
  # ESLint8 + Agentlinter were removed from the JSON config: ESLint8 runs a
  # default Codacy config that doesn't match our project eslint.config.ts
  # (the CI lint job is the authoritative gate); Agentlinter analyzes
  # instruction files, not application code.
  if jq -e '
    any(.tools[];
      ((.toolId | ascii_downcase) == "pmd")
      or ((.toolId | ascii_downcase) == "sqlint")
    )
  ' "$CODACY_JSON" >/dev/null; then
    fail "PMD/SQLint must remain absent unless SOW-0046 is superseded"
  fi

  if ! awk '
    /^[[:space:]]*# SOW-0046: PMD and SQLint stay absent/ { saw_sow = 1 }
    /^[[:space:]]*# config because their Cloud-imported findings are Cloud\/local-noise/ { saw_noise = 1 }
    /^[[:space:]]*# repository\. Reintroducing either tool requires a renewed SOW disposition\./ { saw_reentry = 1 }
    END { exit (saw_sow && saw_noise && saw_reentry) ? 0 : 1 }
  ' "${BASH_SOURCE[0]}"; then
    fail "PMD/SQLint local-removal rationale is missing from this self-test"
  fi

  assert_json_exclude_policy

  pass "Codacy JSON tool/pattern policy"
}

assert_json_exclude_policy() {
  local expected_repository_excludes
  expected_repository_excludes="$(expected_codacy_repository_excludes)"

  mapfile -t json_excludes < <(extract_json_exclude_values)
  assert_line_set_equals "JSON Codacy repository/global excludes" \
    "$expected_repository_excludes" "$(extract_json_exclude_values)"
  assert_no_forbidden_broad_excludes ".codacy/codacy.config.json exclude" "${json_excludes[@]}"
}

assert_no_broad_yaml_excludes() {
  local value bad
  while IFS= read -r value; do
    assert_no_forbidden_broad_excludes ".codacy.yml" "$value"
  done < <(extract_yaml_list_values repository)

  while IFS= read -r value; do
    assert_no_forbidden_broad_excludes ".codacy.yml" "$value"
    for bad in "${GENERATED_ARCHIVE_EXCLUDES[@]}"; do
      [[ "$value" != "$bad" ]] \
        || fail ".codacy.yml mixes generated/archive exclusions into eslint-8 policy: $value"
    done
  done < <(extract_yaml_list_values eslint)
}

assert_yaml_policy() {
  require_file "$CODACY_YAML"

  local first_line
  IFS= read -r first_line < "$CODACY_YAML" \
    || fail ".codacy.yml is empty"
  [[ "$first_line" == "---" ]] \
    || fail ".codacy.yml must start with the YAML document marker ---"

  assert_no_broad_yaml_excludes

  if ! awk '
    /^# SOW-0046: repository-wide exclusions are intentionally limited to non-runtime/ { saw_repository = 1 }
    /^# SOW work-ledger files, duplicate instruction symlinks, generated analyzer/ { saw_work_ledger = 1 }
    /^# material, embedded\/build output, dependencies, coverage reports, local test/ { saw_artifacts = 1 }
    /^# output, and local binary output\. They also carry the ignored-file policy/ { saw_local_outputs = 1 }
    /^# explicitly because Codacy ignores UI ignored-file settings when this file exists\./ { saw_ui = 1 }
    /^# SOW-0046: eslint-8 exclusions are tool-scoped/ { saw_scope = 1 }
    /^# test support, and standalone frontend scripts only\. These paths are not/ { saw_not_global = 1 }
    /^# repository-wide Codacy exclusions\./ { saw_repository_scope = 1 }
    /^# Tests and test support stay covered by native ESLint\/TypeScript\/Vitest\/Playwright/ { saw_native = 1 }
    /^# as applicable\. Standalone frontend scripts stay covered by dedicated script/ { saw_script_scope = 1 }
    /^# self-tests\/build integration plus repository-wide secrets\/spec-drift gates\./ { saw_script_gates = 1 }
    /^# Runtime frontend and Go source remain analyzable\./ { saw_runtime = 1 }
    END { exit (saw_repository && saw_work_ledger && saw_artifacts && saw_local_outputs && saw_ui && saw_scope && saw_not_global && saw_repository_scope && saw_native && saw_script_scope && saw_script_gates && saw_runtime) ? 0 : 1 }
  ' "$CODACY_YAML"; then
    fail ".codacy.yml is missing the SOW-0046 native-gate rationale comments"
  fi

  local expected_yaml actual_yaml
  expected_yaml="$(cat <<'YAML'
---
exclude_paths:
  - ".agents/sow/current/**"
  - ".agents/sow/done/**"
  - ".agents/sow/pending/**"
  - ".codacy/generated/**"
  - "CLAUDE.md"
  - "GEMINI.md"
  - "cmd/ai-viewer-serve/frontend_dist/**"
  - "frontend/node_modules/**"
  - "frontend/coverage/**"
  - "frontend/dist/**"
  - "frontend/playwright-report/**"
  - "frontend/test-results/**"
  - "testdata/**"
  - "bin/**"
engines:
  eslint-8:
    enabled: false
    exclude_paths:
      - "frontend/tests/**"
      - "frontend/src/**/*.test.ts"
      - "frontend/src/**/*.test.tsx"
      - "frontend/src/test/**"
      - "frontend/scripts/**"
  lizard:
    enabled: false
  tsqllint:
    enabled: false
  stylelint:
    enabled: false
  agentlinter:
    enabled: false
YAML
)"
  actual_yaml="$(
    awk '
      /^[[:space:]]*#/ { next }
      /^[[:space:]]*$/ { next }
      { sub(/[[:space:]]+$/, ""); print }
    ' "$CODACY_YAML"
  )"

  if [[ "$actual_yaml" != "$expected_yaml" ]]; then
    printf '%s\n' "Expected effective .codacy.yml policy:" >&2
    printf '%s\n' "$expected_yaml" >&2
    printf '%s\n' "Actual effective .codacy.yml policy:" >&2
    printf '%s\n' "$actual_yaml" >&2
    diff -u <(printf '%s\n' "$expected_yaml") <(printf '%s\n' "$actual_yaml") >&2 || true
    fail ".codacy.yml must contain only the exact eslint-8 test/tooling exclusions"
  fi

  local expected_paths actual_paths
  expected_paths="$(expected_codacy_eslint_test_tooling_excludes)"
  actual_paths="$(extract_yaml_list_values eslint)"
  [[ "$actual_paths" == "$expected_paths" ]] \
    || fail "eslint-8 exclusions are not exactly the allowed test/tooling paths"

  local expected_repository_paths actual_repository_paths
  expected_repository_paths="$(expected_codacy_repository_excludes)"
  actual_repository_paths="$(extract_yaml_list_values repository)"
  assert_line_set_equals "YAML Codacy repository/global excludes" \
    "$expected_repository_paths" "$actual_repository_paths"

  local json_repository_paths
  json_repository_paths="$(extract_json_exclude_values)"
  assert_line_set_equals "JSON/YAML Codacy repository exclude parity" \
    "$actual_repository_paths" "$json_repository_paths"

  # ESLint8 was removed from the JSON config (disabled). No parity check needed.

  pass "Codacy YAML repository and eslint-8 exclusion policy"
}

assert_json_policy
assert_yaml_policy

printf '%sPASS%s codacy-config-test\n' "$GREEN" "$NC"
