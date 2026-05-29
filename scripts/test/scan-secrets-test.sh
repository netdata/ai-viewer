#!/usr/bin/env bash
#
# scan-secrets-test.sh
#
# Negative self-test for scripts/scan-secrets.sh. The scanner is a quality
# gate; a gate that cannot prove it still detects is a gate that can silently
# rot. This harness plants known-dirty content in a THROWAWAY git repo (the
# scanner drives off `git ls-files`, so it must run inside a repo whose tracked
# set we control) and asserts:
#
#   - operator-identity strings (Rule 1) are flagged ANYWHERE, including under
#     scripts/test/fixtures/*/INPUT/** (zero tolerance)
#   - generic secret shapes (Rule 2) are flagged outside INPUT/ ...
#   - ... but ALLOWED under scripts/test/fixtures/*/INPUT/** (synthetic dirt)
#   - dirt inside a *.gz file is detected (decompression path works)
#   - a fully clean tree (only allow-listed placeholders) passes (exit 0)
#   - sanctioned placeholders ([REDACTED_*], *.example.invalid, example.com)
#     never trip a hit
#
# No operator-identity string is ever written to a TRACKED file in THIS repo:
# every fixture lives under a mktemp dir that is removed on exit. The literal
# identity tokens used to probe detection are assembled at runtime from parts
# so this test file itself stays clean of the patterns it exercises (and so the
# real scan-secrets.sh gate never flags this test).

set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SCANNER="${REPO_ROOT}/scripts/scan-secrets.sh"
TMP_ROOT="$(mktemp -d -t scan-secrets-test-XXXXXX)"
trap 'rm -rf "$TMP_ROOT"' EXIT

# Colors only when stdout is a TTY (mirrors sanitize-fixture-test.sh).
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

# Probe tokens assembled at runtime from fragments so THIS source file never
# contains a contiguous operator-identity literal — otherwise the real
# scan-secrets.sh gate would (correctly) flag this very test. Each value below
# reconstructs exactly what scan-secrets.sh Rule 1 / Rule 2 hunts for.
OP_NAME="co""sta"                       # operator given-name (Rule 1: name)
OP_DOMAIN="netdata"".cloud"             # operator email domain (Rule 1: email)
OP_EMAIL="${OP_NAME}@${OP_DOMAIN}"
OP_HOME="/home/""${OP_NAME}"            # operator home path (Rule 1: home)
# A real-looking Anthropic key shape (Rule 2). NOT the sanctioned
# sk-ant-EXAMPLE placeholder, so it must be flagged outside INPUT/.
SECRET_KEY="sk-""ant-api03-DeadBeefDeadBeefDeadBeef1234567890"
# The sanctioned SYNTHETIC placeholder key (Rule 2 allow-list). Assembled from
# parts only for symmetry; it is already safe to write literally.
PLACEHOLDER_KEY="sk-""ant-EXAMPLEdeadbeefdeadbeef"

# --- helpers ----------------------------------------------------------------

# new_repo <name> -> prints an initialized empty git repo path under TMP_ROOT
# with scan-secrets.sh copied into scripts/ so the scanner resolves its repo
# root to this throwaway tree.
new_repo() {
  local name="$1"
  local dir="${TMP_ROOT}/${name}"
  mkdir -p "$dir/scripts"
  cp "$SCANNER" "$dir/scripts/scan-secrets.sh"
  chmod +x "$dir/scripts/scan-secrets.sh"
  (
    cd "$dir"
    git init -q
    # Local identity so `git` commands work; never the operator's.
    git config user.email "test@example.invalid"
    git config user.name "fixture"
    git config commit.gpgsign false
  )
  printf '%s' "$dir"
}

# track <repo> <relpath> — create a tracked file via git add (content on stdin).
track() {
  local repo="$1" rel="$2"
  mkdir -p "$repo/$(dirname "$rel")"
  cat > "$repo/$rel"
  ( cd "$repo" && git add -- "$rel" )
}

# track_gz <repo> <relpath> — same, but gzip the stdin content first.
track_gz() {
  local repo="$1" rel="$2"
  mkdir -p "$repo/$(dirname "$rel")"
  gzip -n -c > "$repo/$rel"
  ( cd "$repo" && git add -- "$rel" )
}

# run_scanner <repo> -> sets RC and OUT (combined stdout+stderr).
run_scanner() {
  local repo="$1"
  RC=0
  OUT="$( ( cd "$repo" && ./scripts/scan-secrets.sh ) 2>&1 )" || RC=$?
}

# --- cases ------------------------------------------------------------------

# 1. Operator email anywhere -> flagged.
case_detect_operator_email() {
  local name="detect::operator_email"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf 'contact: %s\n' "$OP_EMAIL" | track "$repo" "docs/notes.md"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "scanner passed but should have flagged an operator email"; return
  fi
  if ! printf '%s' "$OUT" | grep -q 'docs/notes.md'; then
    fail_case "$name" "non-zero exit but report did not name the offending file:
$OUT"; return
  fi
  pass_case "$name"
}

# 2. Operator home path anywhere -> flagged.
case_detect_operator_home() {
  local name="detect::operator_home"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"workdir":"%s/src/x"}\n' "$OP_HOME" | track "$repo" "data/session.json"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "scanner passed but should have flagged the operator home path"; return
  fi
  pass_case "$name"
}

# 3. Operator name (word-bounded) -> flagged; an unrelated token sharing the
#    substring (cost_usd) must NOT, proving the boundary.
case_detect_operator_name_word_bounded() {
  local name="detect::operator_name_word_bounded"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf 'author: %s\n' "$OP_NAME" | track "$repo" "AUTHORS.txt"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "scanner passed but should have flagged the operator name"; return
  fi
  # Now a clean repo whose only token is cost_usd (substring of the name).
  local clean; clean="$(new_repo "${name//[^A-Za-z0-9]/_}_clean")"
  printf '{"cost_usd":0.04,"costing":true}\n' | track "$clean" "metrics.json"
  run_scanner "$clean"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "scanner falsely flagged cost_usd / costing (boundary broken):
$OUT"; return
  fi
  pass_case "$name"
}

# 4. Operator identity INSIDE an INPUT/ fixture -> still flagged (Rule 1 has no
#    INPUT exemption).
case_detect_operator_identity_in_input() {
  local name="detect::operator_identity_in_input_fixture"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"operatorEmail":"%s"}\n' "$OP_EMAIL" \
    | track "$repo" "scripts/test/fixtures/aiagent_v3/x/INPUT/s.jsonl"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "operator identity under INPUT/ was NOT flagged (Rule 1 must apply everywhere)"; return
  fi
  pass_case "$name"
}

# 5. Generic secret shape OUTSIDE INPUT/ -> flagged.
case_detect_secret_shape_outside_input() {
  local name="detect::secret_shape_outside_input"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"api_key":"%s"}\n' "$SECRET_KEY" | track "$repo" "config/app.json"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "scanner passed but should have flagged a generic secret shape"; return
  fi
  pass_case "$name"
}

# 6. Same secret shape INSIDE INPUT/ -> allowed (Rule 2 exempt there).
case_secret_shape_allowed_in_input() {
  local name="exempt::secret_shape_allowed_in_input_fixture"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"api_key":"%s"}\n' "$SECRET_KEY" \
    | track "$repo" "scripts/test/fixtures/aiagent_v2/y/INPUT/s.json"
  run_scanner "$repo"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "secret shape under INPUT/ was flagged but Rule 2 must be exempt there:
$OUT"; return
  fi
  pass_case "$name"
}

# 7. Dirt inside a *.gz -> detected (decompression path).
case_detect_secret_in_gz() {
  local name="detect::operator_identity_in_gz"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"operatorEmail":"%s"}\n' "$OP_EMAIL" \
    | track_gz "$repo" "data/snap.json.gz"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "operator identity inside a .gz was NOT detected (decompress path broken)"; return
  fi
  if ! printf '%s' "$OUT" | grep -q 'snap.json.gz'; then
    fail_case "$name" "gz hit reported but did not name the .gz file:
$OUT"; return
  fi
  pass_case "$name"
}

# 8. Fully clean tree (only sanctioned placeholders) -> passes.
case_clean_tree_passes() {
  local name="clean::placeholders_pass"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  {
    printf 'email: user@example.invalid\n'
    printf 'host: https://api.example.invalid/anthropic/v1\n'
    printf 'token: Bearer [REDACTED_SECRET]\n'
    printf 'docs ref: see example.com and example.org\n'
    printf 'home: <HOME>/src/project\n'
    printf 'metric: cost_usd accounting\n'
  } | track "$repo" "README.md"
  # A clean INPUT fixture with a sanctioned synthetic placeholder.
  printf '{"key":"sk-ant-EXAMPLEdeadbeefdeadbeef"}\n' \
    | track "$repo" "scripts/test/fixtures/aiagent_v3/z/INPUT/s.jsonl"
  run_scanner "$repo"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "clean tree was flagged:
$OUT"; return
  fi
  if ! printf '%s' "$OUT" | grep -q '\[PASS\]'; then
    fail_case "$name" "clean tree exited 0 but printed no [PASS] summary:
$OUT"; return
  fi
  pass_case "$name"
}

# 9. The real scanner self-excludes: a repo containing ONLY scan-secrets.sh
#    (which lists every pattern) must pass.
case_scanner_self_excludes() {
  local name="self::scanner_excludes_itself"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  ( cd "$repo" && git add -- scripts/scan-secrets.sh )
  run_scanner "$repo"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "scanner flagged its own pattern list (self-exclusion broken):
$OUT"; return
  fi
  pass_case "$name"
}

# 10. Rule 1 is NEVER allow-listed: the operator email sharing a line with an
#     allow-listed token (example.com) must still be flagged. This is the
#     whole-line-drop bypass the per-rule semantics close.
case_rule1_not_allowlisted_on_shared_line() {
  local name="detect::operator_email_on_allowlisted_line"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"operatorEmail":"%s"} see example.com\n' "$OP_EMAIL" \
    | track "$repo" "docs/leak.md"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "operator email on a line with example.com was NOT flagged (whole-line allow-list bypass):
$OUT"; return
  fi
  if ! printf '%s' "$OUT" | grep -q 'docs/leak.md'; then
    fail_case "$name" "non-zero exit but report did not name the offending file:
$OUT"; return
  fi
  pass_case "$name"
}

# 11. Rule 2 allow-listing is PER MATCH, not per line: a real secret shape
#     sharing a line with [REDACTED_SECRET] must still be flagged (the
#     placeholder must not exempt the whole line).
case_rule2_per_match_on_shared_line() {
  local name="detect::secret_shape_on_redacted_line"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"key":"%s","old":"[REDACTED_SECRET]"}\n' "$SECRET_KEY" \
    | track "$repo" "config/keys.json"
  run_scanner "$repo"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "real secret shape on a line with [REDACTED_SECRET] was NOT flagged (per-line allow-list bypass):
$OUT"; return
  fi
  if ! printf '%s' "$OUT" | grep -q 'config/keys.json'; then
    fail_case "$name" "non-zero exit but report did not name the offending file:
$OUT"; return
  fi
  pass_case "$name"
}

# 12. Regression guard: a token that is itself the sanctioned synthetic
#     placeholder (sk-ant-EXAMPLE…) is the ONLY secret-shape token on the line,
#     outside INPUT/ -> still exempt per token (exit 0).
case_rule2_placeholder_token_still_exempt() {
  local name="exempt::placeholder_token_still_exempt"
  local repo; repo="$(new_repo "${name//[^A-Za-z0-9]/_}")"
  printf '{"key":"%s"}\n' "$PLACEHOLDER_KEY" | track "$repo" "docs/example.json"
  run_scanner "$repo"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "sanctioned placeholder key was flagged but must stay exempt per token:
$OUT"; return
  fi
  pass_case "$name"
}

# --- main -------------------------------------------------------------------

main() {
  if [ ! -x "$SCANNER" ]; then
    printf '%sFATAL%s scanner not executable: %s\n' "$C_RED" "$C_RESET" "$SCANNER" >&2
    exit 1
  fi
  if ! command -v git >/dev/null 2>&1; then
    printf '%sFATAL%s git is required for this test\n' "$C_RED" "$C_RESET" >&2
    exit 1
  fi

  printf '%s==> scan-secrets-test%s\n' "$C_YELLOW" "$C_RESET"

  case_detect_operator_email
  case_detect_operator_home
  case_detect_operator_name_word_bounded
  case_detect_operator_identity_in_input
  case_detect_secret_shape_outside_input
  case_secret_shape_allowed_in_input
  case_detect_secret_in_gz
  case_clean_tree_passes
  case_scanner_self_excludes
  case_rule1_not_allowlisted_on_shared_line
  case_rule2_per_match_on_shared_line
  case_rule2_placeholder_token_still_exempt

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
