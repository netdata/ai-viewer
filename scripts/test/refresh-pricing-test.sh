#!/usr/bin/env bash
#
# refresh-pricing-test.sh — smoke-level checks for the CLI input
# validation in scripts/refresh-pricing.sh. Each test runs the script
# with a deliberately-bad argument and asserts the script rejects it
# with a non-zero exit and a clear error message. These tests cover
# the iter-3 security fixes:
#
#   - iter3-3 / iter3-9 : --add-provider / --add-model name validation
#                         (no tabs, newlines, shell metachars, jq
#                          string breakers).
#   - iter3-7           : --out resolves through symlink outside the
#                         repo root.
#   - iter3-11          : --db is not a regular SQLite file.
#
# All tests run with --dry-run where possible to avoid touching the
# checked-in pricing.json. The actual write path is guarded by the
# prompt + path validation, but --dry-run is the belt-and-suspenders
# default for these tests.

set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/refresh-pricing.sh"

# Iter-9 fix iter9-2: tests run OFFLINE via $LITELLM_URL /
# $OPENROUTER_URL pointing at file:// fixtures (pre-iter9 tests
# silently used network defaults — codex iter-8 P2#2).
FIXTURES_DIR="${SCRIPT_DIR}/fixtures/refresh-pricing"
export LITELLM_URL="file://${FIXTURES_DIR}/litellm.json"
export OPENROUTER_URL="file://${FIXTURES_DIR}/openrouter.json"
[ -f "${FIXTURES_DIR}/litellm.json" ] || { echo "missing LiteLLM fixture: ${FIXTURES_DIR}/litellm.json" >&2; exit 2; }
[ -f "${FIXTURES_DIR}/openrouter.json" ] || { echo "missing OpenRouter fixture: ${FIXTURES_DIR}/openrouter.json" >&2; exit 2; }

PASS=0
FAIL=0

pass() { echo "PASS $1"; PASS=$((PASS+1)); }
fail() { echo "FAIL $1: $2"; FAIL=$((FAIL+1)); }

# expect_reject runs the script and asserts (a) it exits non-zero and
# (b) the stderr contains a substring identifying the rejection. The
# command is passed as a single string for legibility; eval is bounded
# to test harness inputs only.
expect_reject() {
  local name="$1" want_substr="$2" cmd="$3"
  local out exit_code=0
  out="$(eval "$cmd" 2>&1)" || exit_code=$?
  if [ "$exit_code" -eq 0 ]; then
    fail "$name" "expected non-zero exit, got 0; output: ${out}"
    return
  fi
  if printf '%s' "$out" | grep -qF -- "$want_substr"; then
    pass "$name"
  else
    fail "$name" "exit=${exit_code} but output missing substring ${want_substr}; got: ${out}"
  fi
}

# --- iter3-3 / iter3-9 : --add-provider name validation -------------

# Tab in provider name (TSV injection vector).
expect_reject \
  "validate_name::add_provider_with_tab" \
  "invalid --add-provider name" \
  "${SCRIPT} --dry-run \"--add-provider=foo$(printf '\t')bar\""

# Newline in provider name. We construct the arg through a bash array
# so the actual newline survives until argv. Using eval-style strings
# collapses whitespace.
nl_test() {
  "${SCRIPT}" --dry-run "--add-provider=foo
bar" 2>&1
  return $?
}
nl_out=""
nl_exit=0
nl_out="$(nl_test)" || nl_exit=$?
if [ "$nl_exit" -ne 0 ] && printf '%s' "$nl_out" | grep -qF "invalid --add-provider name"; then
  pass "validate_name::add_provider_with_newline"
else
  fail "validate_name::add_provider_with_newline" "exit=${nl_exit}; out=${nl_out}"
fi

# jq string-literal break: a value with double-quote and parens.
expect_reject \
  "validate_name::add_provider_jq_injection" \
  "invalid --add-provider name" \
  "${SCRIPT} --dry-run '--add-provider=foo\"); malicious; (\"bar'"

# Shell semicolon — already shell-escaped at this point, but
# validate_name rejects it independently.
expect_reject \
  "validate_name::add_provider_shell_meta" \
  "invalid --add-provider name" \
  "${SCRIPT} --dry-run '--add-provider=foo; rm -rf /'"

# Space in name (would split the field downstream).
expect_reject \
  "validate_name::add_provider_with_space" \
  "invalid --add-provider name" \
  "${SCRIPT} --dry-run '--add-provider=foo bar'"

# Leading dot (not a leading alphanumeric).
expect_reject \
  "validate_name::add_provider_leading_dot" \
  "invalid --add-provider name" \
  "${SCRIPT} --dry-run '--add-provider=.foo'"

# --- iter3-3 / iter3-9 : --add-model name validation ---------------

# Missing slash (not provider/model).
expect_reject \
  "validate_name::add_model_missing_slash" \
  "--add-model expects provider/model" \
  "${SCRIPT} --dry-run '--add-model=just-a-model'"

# Tab on the model side.
expect_reject \
  "validate_name::add_model_with_tab" \
  "invalid --add-model model name" \
  "${SCRIPT} --dry-run \"--add-model=anthropic/foo$(printf '\t')bar\""

# --- iter3-11 : --db path validation ------------------------------

# /dev/null is not a regular file.
expect_reject \
  "validate_db::not_regular_file" \
  "--db must be a regular file" \
  "${SCRIPT} --dry-run --db=/dev/null"

# A non-SQLite file (use this very script — it's regular but has the
# wrong magic bytes).
expect_reject \
  "validate_db::wrong_magic_bytes" \
  "not a SQLite database" \
  "${SCRIPT} --dry-run --db=${SCRIPT}"

# --- iter3-7 / iter4-3 : --out path containment ----------------

tmp_dir="$(mktemp -d -t refresh-pricing-test.XXXXXX)"
trap 'rm -rf "$tmp_dir"' EXIT

# iter4-3: an in-tree path that is NOT internal/pricing/pricing.json
# must be rejected even though it is "under REPO_ROOT". The previous
# iter-3 fix only checked REPO_ROOT containment which allowed
# overwriting README.md, internal/store/store.go, or any other file.
expect_reject \
  "validate_out::repo_path_outside_pricing_dir_rejected" \
  "--out must resolve" \
  "${SCRIPT} --dry-run --out=${REPO_ROOT}/README.md"

# Subdir under internal/ but not internal/pricing/ is also rejected.
expect_reject \
  "validate_out::other_internal_subdir_rejected" \
  "--out must resolve" \
  "${SCRIPT} --dry-run --out=${REPO_ROOT}/internal/store/store.go"

# iter3-7 (still valid): a symlink at REPO_ROOT that points outside
# must be rejected because the resolved path is neither
# internal/pricing/pricing.json nor under \$TMPDIR.
symlink_inside="${REPO_ROOT}/.refresh-pricing-test-symlink.json"
# The symlink target lives in a custom directory outside both
# REPO_ROOT and $TMPDIR so the resolution rejects it.
escape_dir="$(mktemp -d /var/tmp/refresh-pricing-escape.XXXXXX 2>/dev/null || mktemp -d "${HOME}/.refresh-pricing-escape.XXXXXX")"
target_outside="${escape_dir}/escaped.json"
: > "$target_outside"
ln -sf "$target_outside" "$symlink_inside"
trap 'rm -f "$symlink_inside"; rm -rf "$tmp_dir" "$escape_dir"' EXIT

expect_reject \
  "validate_out::symlink_escape" \
  "--out must resolve" \
  "${SCRIPT} --dry-run --out=${symlink_inside}"

# Sanity check: a path under \$TMPDIR is accepted (validation should
# fall through to the discover/fetch stages). We assert that the
# subsequent failure is NOT the path validation but the seed
# discovery — proving --out itself was accepted.
tmp_out="${tmp_dir}/proposed.json"
tmp_out_check_out=""
tmp_out_check_exit=0
tmp_out_check_out="$("${SCRIPT}" --dry-run --out="${tmp_out}" 2>&1)" || tmp_out_check_exit=$?
if [ "$tmp_out_check_exit" -ne 0 ] && \
   ! printf '%s' "$tmp_out_check_out" | grep -q "must resolve"; then
  pass "validate_out::tmpdir_path_accepted"
else
  fail "validate_out::tmpdir_path_accepted" \
    "expected --out path validation to pass, then later step to fail; got exit=${tmp_out_check_exit} output=${tmp_out_check_out}"
fi

# --- iter8-1 : missing seed gate (refuses to write partial file) -------
#
# Pricing spec §"Failure modes": when any requested (provider, model)
# seed is missing from BOTH LiteLLM and OpenRouter, the script must
# exit non-zero BEFORE validate/diff/prompt. Use a fake --add-model
# that no source knows; assert (a) the gate-specific message fires,
# (b) the missing pair is listed, (c) the proposed file was NOT
# written. --dry-run blocks the write regardless; the assertion is on
# the EARLIER gate firing first.
partial_tmp_out="${tmp_dir}/partial-proposed.json"
rm -f "$partial_tmp_out"
partial_out=""
partial_exit=0
# Use a different temp file for --out so we can later assert it does
# not exist (proves no partial write).
partial_out="$("${SCRIPT}" --out="${partial_tmp_out}" \
  --add-model=fake-vendor-99/fake-model-impossible 2>&1)" || partial_exit=$?
if [ "$partial_exit" -ne 0 ] \
   && printf '%s' "$partial_out" | grep -qF "refusing to write a partial pricing.json" \
   && printf '%s' "$partial_out" | grep -qF "fake-vendor-99/fake-model-impossible" \
   && [ ! -e "$partial_tmp_out" ]; then
  pass "iter8-1::missing_seed_exits_before_write"
else
  fail "iter8-1::missing_seed_exits_before_write" \
    "exit=${partial_exit} wrote=$([ -e "$partial_tmp_out" ] && echo yes || echo no) out=${partial_out}"
fi

# --allow-partial flips the gate off: the same input should reach the
# diff+prompt stage. With --dry-run the script will not actually
# write; we assert that the gate message is NOT present and the
# script either exits with the dry-run "not writing" log or proceeds
# past the missing-seed gate. Either way it must NOT contain the
# "refusing to write" string.
allow_partial_tmp_out="${tmp_dir}/allow-partial-proposed.json"
rm -f "$allow_partial_tmp_out"
allow_partial_out=""
allow_partial_exit=0
allow_partial_out="$("${SCRIPT}" --dry-run --out="${allow_partial_tmp_out}" \
  --allow-partial --add-model=fake-vendor-99/fake-model-impossible 2>&1)" || allow_partial_exit=$?
if ! printf '%s' "$allow_partial_out" | grep -qF "refusing to write a partial pricing.json"; then
  pass "iter8-1::allow_partial_disables_gate"
else
  fail "iter8-1::allow_partial_disables_gate" \
    "exit=${allow_partial_exit} out=${allow_partial_out}"
fi

# --- iter10-3 : show_review_diff falls back when git is missing -----
#
# codex iter-9 P2#3: prompt_apply called `git diff --no-index ... || :`
# but require_tools only checks for `diff` (not `git`). On a minimal
# environment WITHOUT git the operator was prompted to overwrite with
# NO diff shown — the `|| :` swallowed the missing-git failure.
#
# We exercise the show_review_diff fallback path directly by sourcing
# refresh-pricing.sh in a PATH that hides `git` while keeping `diff`.
# show_review_diff must NOT die (diff is available) and must produce a
# unified diff via the `diff -u` fallback. Output is captured via the
# stderr redirect the production caller uses.
#
# Mutation evidence: if show_review_diff's body is reverted to the
# single `git diff ... || :` line (and `|| :` swallows the missing
# git), the captured output is EMPTY — the assertion below fires.
sanitized_path_dir="${tmp_dir}/no-git-path"
mkdir -p "$sanitized_path_dir"
# Mirror every relevant binary EXCEPT git into the sanitized PATH so
# the rest of the script + diff itself still work.
for tool in curl jq diff sqlite3 awk sort mktemp wc cat date printf grep sed find readlink cp mv rm ln chmod stat dirname basename head tail bash sh env true false; do
  real="$(command -v "$tool" 2>/dev/null || true)"
  if [ -n "$real" ]; then
    ln -sf "$real" "${sanitized_path_dir}/${tool}"
  fi
done
if PATH="$sanitized_path_dir" command -v git >/dev/null 2>&1; then
  echo "test setup error: git unexpectedly present in no-git PATH" >&2
fi
if ! PATH="$sanitized_path_dir" command -v diff >/dev/null 2>&1; then
  echo "test setup error: diff missing from no-git PATH" >&2
fi

# Two on-disk inputs to diff.
diff_cur="${tmp_dir}/diff-cur.json"
diff_proposed="${tmp_dir}/diff-proposed.json"
printf '{"a":1}\n' > "$diff_cur"
printf '{"a":2}\n' > "$diff_proposed"
# Source the script in a subshell so we can call show_review_diff
# under the sanitized PATH without exec'ing the whole script.
diff_out="$(
  PATH="${sanitized_path_dir}" bash -c '
    set -euo pipefail
    SCRIPT="$1"; cur="$2"; proposed="$3"
    # Source by line-extracting show_review_diff + die so we do not
    # run main(). The body lives between "show_review_diff() {" and
    # the next blank line. die is needed by show_review_diff'\''s
    # missing-both branch (not exercised here).
    die()  { printf >&2 "die: %s\n" "$*"; exit 1; }
    # shellcheck disable=SC1091
    eval "$(awk "/^show_review_diff\\(\\) \\{/,/^\\}/" "$SCRIPT")"
    show_review_diff "$cur" "$proposed"
  ' _ "$SCRIPT" "$diff_cur" "$diff_proposed" 2>&1 || true
)"
# The diff -u fallback must produce a unified-diff body — it shows
# both the old "a":1 line and the new "a":2 line. Use `grep -- PAT`
# to keep grep from parsing the leading `-` as an option.
if printf '%s' "$diff_out" | grep -qF -- '-{"a":1}' && \
   printf '%s' "$diff_out" | grep -qF -- '+{"a":2}'; then
  pass "iter10-3::show_review_diff_falls_back_to_plain_diff_without_git"
else
  fail "iter10-3::show_review_diff_falls_back_to_plain_diff_without_git" \
    "expected unified-diff output containing the changed lines; got: ${diff_out}"
fi

# Also exercise the both-missing path: hide BOTH git AND diff. The
# show_review_diff function must die with the explicit message rather
# than silently producing no output.
neither_path_dir="${tmp_dir}/no-git-no-diff-path"
mkdir -p "$neither_path_dir"
for tool in awk grep sed bash sh cat printf; do
  real="$(command -v "$tool" 2>/dev/null || true)"
  if [ -n "$real" ]; then
    ln -sf "$real" "${neither_path_dir}/${tool}"
  fi
done
neither_out="$(
  PATH="${neither_path_dir}" bash -c '
    set -euo pipefail
    SCRIPT="$1"; cur="$2"; proposed="$3"
    die()  { printf >&2 "die: %s\n" "$*"; exit 1; }
    # shellcheck disable=SC1091
    eval "$(awk "/^show_review_diff\\(\\) \\{/,/^\\}/" "$SCRIPT")"
    show_review_diff "$cur" "$proposed"
  ' _ "$SCRIPT" "$diff_cur" "$diff_proposed" 2>&1 || true
)"
if printf '%s' "$neither_out" | grep -qF "neither git nor diff"; then
  pass "iter10-3::show_review_diff_dies_when_both_missing"
else
  fail "iter10-3::show_review_diff_dies_when_both_missing" \
    "expected 'neither git nor diff' die message; got: ${neither_out}"
fi

# --- iter11-3 : require_tools rejects PATH without git AND without diff -
#
# codex iter-10 P3: require_tools demanded `diff` unconditionally, which
# contradicted show_review_diff's git-first fallback. Iter-11 split the
# gate: curl/jq/sqlite3 always required, plus AT LEAST ONE of git/diff.
# Mutation: reverting either branch changes the die message or lets the
# script continue past require_tools — the assertions below fire.
require_tools_path_dir="${tmp_dir}/no-git-no-diff-require"
mkdir -p "$require_tools_path_dir"
# Mirror every needed tool EXCEPT git AND diff so the script reaches
# the new gate (not a different missing-dep die).
for tool in curl jq sqlite3 awk sort mktemp wc cat date printf grep sed find readlink cp mv rm ln chmod stat dirname basename head tail bash sh env true false; do
  real="$(command -v "$tool" 2>/dev/null || true)"
  if [ -n "$real" ]; then
    ln -sf "$real" "${require_tools_path_dir}/${tool}"
  fi
done
if PATH="$require_tools_path_dir" command -v git >/dev/null 2>&1; then
  echo "test setup error: git unexpectedly present in require_tools PATH" >&2
fi
if PATH="$require_tools_path_dir" command -v diff >/dev/null 2>&1; then
  echo "test setup error: diff unexpectedly present in require_tools PATH" >&2
fi
require_tools_out=""
require_tools_exit=0
require_tools_out="$(PATH="${require_tools_path_dir}" "${SCRIPT}" --dry-run 2>&1)" || require_tools_exit=$?
if [ "$require_tools_exit" -ne 0 ] && \
   printf '%s' "$require_tools_out" | grep -qF "neither 'git' nor 'diff' available"; then
  pass "iter11-3::require_tools_rejects_no_git_no_diff"
else
  fail "iter11-3::require_tools_rejects_no_git_no_diff" \
    "expected new die message about neither git nor diff; exit=${require_tools_exit} out=${require_tools_out}"
fi

# Positive: git present + diff absent must PASS require_tools — the
# script proceeds past the gate (later steps may fail unrelated, but
# the new die must NOT fire). iter-10 fix contract.
git_only_path_dir="${tmp_dir}/git-only-require"
mkdir -p "$git_only_path_dir"
for tool in curl jq sqlite3 git awk sort mktemp wc cat date printf grep sed find readlink cp mv rm ln chmod stat dirname basename head tail bash sh env true false; do
  real="$(command -v "$tool" 2>/dev/null || true)"
  if [ -n "$real" ]; then
    ln -sf "$real" "${git_only_path_dir}/${tool}"
  fi
done
if PATH="$git_only_path_dir" command -v diff >/dev/null 2>&1; then
  echo "test setup error: diff unexpectedly present in git-only PATH" >&2
fi
if ! PATH="$git_only_path_dir" command -v git >/dev/null 2>&1; then
  echo "test setup error: git missing from git-only PATH" >&2
fi
git_only_out=""
git_only_exit=0
git_only_out="$(PATH="${git_only_path_dir}" "${SCRIPT}" --dry-run 2>&1)" || git_only_exit=$?
if ! printf '%s' "$git_only_out" | grep -qF "neither 'git' nor 'diff' available"; then
  pass "iter11-3::require_tools_accepts_git_only"
else
  fail "iter11-3::require_tools_accepts_git_only" \
    "expected require_tools to accept git-only PATH; exit=${git_only_exit} out=${git_only_out}"
fi

# --- summary -----------------------------------------------------

echo
echo "${PASS} passed, ${FAIL} failed"
[ "$FAIL" -eq 0 ]
