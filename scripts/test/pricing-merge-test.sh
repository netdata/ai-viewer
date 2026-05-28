#!/usr/bin/env bash
#
# pricing-merge-test.sh — covers scripts/lib/pricing-merge.jq and the
# validate filter at scripts/lib/pricing-validate.jq. Verifies the
# iter-2 fixes for the new-provider bug and ctx_max update
# during merge.
#
# These are smoke-level checks the operator can run locally; they
# exercise the jq filters in isolation against synthetic inputs so a
# jq-syntax or logic regression is caught without a live network run.

set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
LIB="${REPO_ROOT}/scripts/lib"

PASS=0
FAIL=0

pass() { echo "PASS $1"; PASS=$((PASS+1)); }
fail() { echo "FAIL $1: $2"; FAIL=$((FAIL+1)); }

merge() {
  local records="$1" base="$2" today="$3" generator="$4"
  printf '%s\n' "$records" | jq -s -L "$LIB" \
    --argjson base "$base" \
    --arg today "$today" \
    --arg now "2026-05-27T00:00:00Z" \
    --arg generated_by "$generator" \
    'include "pricing-merge"; merge_pricing($base; .; $today; $now; $generated_by)'
}

# assert_valid runs the merge output through pricing-validate.jq so a
# structurally-invalid merge is caught at the smoke layer instead of
# slipping into a real run. Called from every test case that produces
# a merge output (Fix iter3-13).
assert_valid() {
  local name="$1" out="$2"
  if printf '%s' "$out" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
    pass "${name}::output_passes_validate"
  else
    fail "${name}::output_passes_validate" "merge output failed pricing-validate.jq"
  fi
}

# --- Fix #4 regression: new provider must land with its model ----------
empty_base='{"version":2,"schema_url":"u","currency":"USD","providers":[]}'
record1='{"provider":"anthropic","model":"claude-opus-4-7","source":"litellm","citation_url":"u","ctx_max":1000000,"prices":{"input_per_million":5,"output_per_million":25}}'
out1="$(merge "$record1" "$empty_base" "2026-05-27" "test")"

if [ "$(printf '%s' "$out1" | jq '.providers | length')" = "1" ]; then
  pass "fix4::new_provider_added"
else
  fail "fix4::new_provider_added" "providers length != 1: ${out1}"
fi

assert_valid "fix4::new_provider" "$out1"

if [ "$(printf '%s' "$out1" | jq -r '.providers[0].name')" = "anthropic" ]; then
  pass "fix4::new_provider_name"
else
  fail "fix4::new_provider_name" "name != anthropic"
fi

if [ "$(printf '%s' "$out1" | jq '.providers[0].models | length')" = "1" ]; then
  pass "fix4::new_provider_has_model"
else
  fail "fix4::new_provider_has_model" "models length != 1"
fi

# --- Fix #4: two records adding two models to the same NEW provider ----
record_pair="$(printf '%s\n%s' \
  '{"provider":"anthropic","model":"claude-opus-4-7","source":"litellm","citation_url":"u","ctx_max":1000000,"prices":{"input_per_million":5,"output_per_million":25}}' \
  '{"provider":"anthropic","model":"claude-sonnet-4-5","source":"litellm","citation_url":"u","ctx_max":200000,"prices":{"input_per_million":3,"output_per_million":15}}')"
out2="$(merge "$record_pair" "$empty_base" "2026-05-27" "test")"
if [ "$(printf '%s' "$out2" | jq '.providers[0].models | length')" = "2" ]; then
  pass "fix4::two_models_one_new_provider"
else
  fail "fix4::two_models_one_new_provider" "models len != 2; out=${out2}"
fi
assert_valid "fix4::two_models" "$out2"

# --- Fix #18: ctx_max refreshes when an existing model is updated ------
base_with_existing='{"version":2,"schema_url":"u","currency":"USD","providers":[{"name":"anthropic","models":[{"name":"claude-opus-4-7","ctx_max":200000,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"manual_seed","prices":{"input_per_million":5,"output_per_million":25}}]}]}]}'
record_new_ctxmax='{"provider":"anthropic","model":"claude-opus-4-7","source":"litellm","citation_url":"u","ctx_max":1000000,"prices":{"input_per_million":5,"output_per_million":25}}'
out3="$(merge "$record_new_ctxmax" "$base_with_existing" "2026-05-27" "test")"
got_ctxmax="$(printf '%s' "$out3" | jq '.providers[0].models[0].ctx_max')"
if [ "$got_ctxmax" = "1000000" ]; then
  pass "fix18::ctx_max_refreshes"
else
  fail "fix18::ctx_max_refreshes" "got ctx_max=${got_ctxmax}, want 1000000"
fi
assert_valid "fix18::ctx_max_refreshes" "$out3"

# --- happy path: same prices means no new tier prepended ---------------
record_same='{"provider":"anthropic","model":"claude-opus-4-7","source":"litellm","citation_url":"u","ctx_max":1000000,"prices":{"input_per_million":5,"output_per_million":25}}'
out4="$(merge "$record_same" "$base_with_existing" "2026-05-27" "test")"
tier_count="$(printf '%s' "$out4" | jq '.providers[0].models[0].tiers | length')"
if [ "$tier_count" = "1" ]; then
  pass "merge::same_prices_no_new_tier"
else
  fail "merge::same_prices_no_new_tier" "tier count = ${tier_count}, want 1"
fi
assert_valid "merge::same_prices" "$out4"

# --- different prices => new tier prepended ---------------------------
record_diff='{"provider":"anthropic","model":"claude-opus-4-7","source":"litellm","citation_url":"u","ctx_max":1000000,"prices":{"input_per_million":6,"output_per_million":30}}'
out5="$(merge "$record_diff" "$base_with_existing" "2026-05-27" "test")"
tier_count2="$(printf '%s' "$out5" | jq '.providers[0].models[0].tiers | length')"
first_date="$(printf '%s' "$out5" | jq -r '.providers[0].models[0].tiers[0].effective_date')"
if [ "$tier_count2" = "2" ] && [ "$first_date" = "2026-05-27" ]; then
  pass "merge::diff_prices_new_tier_prepended"
else
  fail "merge::diff_prices_new_tier_prepended" "tiers=${tier_count2} first=${first_date}"
fi
assert_valid "merge::diff_prices" "$out5"

# --- validate: ctx_max=null is rejected (iter-5 fix iter5-1) -----
# pricing.schema.json declares ctx_max as integer-or-absent; null is
# not a valid value. The Go loader also rejects ctx_max <0 and would
# fail on a null Go-side. iter-5 tightened the jq validator so a doc
# with ctx_max=null is caught BEFORE write rather than at Go-load.
bad_ctxmax='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","ctx_max":null,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_ctxmax" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_ctx_max_null" "ctx_max=null was accepted"
else
  pass "validate::rejects_ctx_max_null"
fi

# --- validate: ctx_max field omitted entirely is accepted --------
ok_no_ctx='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$ok_no_ctx" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  pass "validate::accepts_ctx_max_omitted"
else
  fail "validate::accepts_ctx_max_omitted" "ctx_max omitted was rejected"
fi

# --- validate: invalid calendar date "2025-99-99" rejected -------
bad_date='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-99-99","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_date" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_invalid_calendar_date" "2025-99-99 accepted"
else
  pass "validate::rejects_invalid_calendar_date"
fi

# --- validate: month-specific day overflow "2025-02-30" rejected -
bad_feb30='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-02-30","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_feb30" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_feb_30" "2025-02-30 accepted"
else
  pass "validate::rejects_feb_30"
fi

# --- validate: case-fold duplicate providers rejected ------------
dup_prov='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"Anthropic","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]},{"name":"ANTHROPIC","models":[{"name":"m2","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$dup_prov" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_case_fold_dup_providers" "Anthropic + ANTHROPIC accepted"
else
  pass "validate::rejects_case_fold_dup_providers"
fi

# --- validate: case-fold duplicate models rejected ---------------
dup_model='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"Claude","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]},{"name":"CLAUDE","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$dup_model" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_case_fold_dup_models" "Claude + CLAUDE accepted"
else
  pass "validate::rejects_case_fold_dup_models"
fi

# --- validate: alias with a space rejected -----------------------
bad_alias='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","aliases":["bad name"],"models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_alias" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_alias_with_space" "alias 'bad name' accepted"
else
  pass "validate::rejects_alias_with_space"
fi

# --- merge: case-fold normalises new provider+model names --------
# DB seeds like `Anthropic / Claude-3-5-Sonnet`
# must not propagate to the merged JSON as a case-variant entry that
# the Go loader (case-insensitive duplicate check) later rejects.
# pricing-merge.jq lowercases NEW provider+model names in apply_record.
mixed_record='{"provider":"Foo","model":"Bar","source":"litellm","citation_url":"u","ctx_max":1000,"prices":{"input_per_million":1,"output_per_million":2}}'
out_mixed="$(merge "$mixed_record" "$empty_base" "2026-05-27" "test")"
got_prov="$(printf '%s' "$out_mixed" | jq -r '.providers[0].name')"
got_model="$(printf '%s' "$out_mixed" | jq -r '.providers[0].models[0].name')"
if [ "$got_prov" = "foo" ] && [ "$got_model" = "bar" ]; then
  pass "merge::case_folds_new_provider_and_model"
else
  fail "merge::case_folds_new_provider_and_model" "got prov=${got_prov} model=${got_model}, want foo/bar"
fi
assert_valid "merge::case_folded" "$out_mixed"

# --- merge: ctx_max=null is OMITTED from the model entry ---------
# build_record emits ctx_max:null when LiteLLM has no context window.
# merge.jq must drop the field (validator rejects null) rather than
# carry it through.
no_ctx_record='{"provider":"baz","model":"qux","source":"litellm","citation_url":"u","ctx_max":null,"prices":{"input_per_million":1,"output_per_million":2}}'
out_no_ctx="$(merge "$no_ctx_record" "$empty_base" "2026-05-27" "test")"
has_ctx_key="$(printf '%s' "$out_no_ctx" | jq '.providers[0].models[0] | has("ctx_max")')"
if [ "$has_ctx_key" = "false" ]; then
  pass "merge::omits_ctx_max_when_null"
else
  fail "merge::omits_ctx_max_when_null" "ctx_max key still present"
fi
assert_valid "merge::ctx_max_null_omitted" "$out_no_ctx"

# --- validate: negative price is rejected ------------------------------
bad_neg='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":-1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_neg" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_negative_input" "negative price was accepted"
else
  pass "validate::rejects_negative_input"
fi

# --- validate: missing required input_per_million ---------------------
bad_missing='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_missing" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_missing_input" "missing input_per_million accepted"
else
  pass "validate::rejects_missing_input"
fi

# --- validate: ctx_max=1.5 (non-integer) is rejected (iter-6 fix iter6-1) ---
# Schema declares ctx_max as integer; Go validator rejects negatives but
# also encodes the field as int64 which means a float in JSON would fail
# Go-side parse. iter-6 ctx_max tightening also rejects floats jq-side.
bad_ctxmax_float='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","ctx_max":1.5,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_ctxmax_float" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_ctx_max_float" "ctx_max=1.5 was accepted"
else
  pass "validate::rejects_ctx_max_float"
fi

# --- validate: ctx_max=-1 (negative) is rejected ---------------------------
bad_ctxmax_neg='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","ctx_max":-1,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_ctxmax_neg" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_ctx_max_negative" "ctx_max=-1 was accepted"
else
  pass "validate::rejects_ctx_max_negative"
fi

# --- validate: leap-year Feb 29 rules (iter-6 fix iter6-1) -----------------
# 2024 is divisible by 4 and not a century year -> leap, 2024-02-29 OK.
ok_feb29_2024='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2024-02-29","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$ok_feb29_2024" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  pass "validate::accepts_feb29_leap_2024"
else
  fail "validate::accepts_feb29_leap_2024" "2024-02-29 (leap) was rejected"
fi

# 2025 is not divisible by 4 -> not leap, 2025-02-29 must be rejected.
bad_feb29_2025='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-02-29","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_feb29_2025" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_feb29_nonleap_2025" "2025-02-29 (non-leap) was accepted"
else
  pass "validate::rejects_feb29_nonleap_2025"
fi

# 2000 is divisible by 400 -> leap, 2000-02-29 OK.
ok_feb29_2000='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2000-02-29","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$ok_feb29_2000" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  pass "validate::accepts_feb29_leap_2000"
else
  fail "validate::accepts_feb29_leap_2000" "2000-02-29 (div-400 leap) was rejected"
fi

# 1900 is divisible by 100 but not 400 -> not leap, 1900-02-29 rejected.
bad_feb29_1900='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"1900-02-29","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_feb29_1900" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_feb29_century_nonleap_1900" "1900-02-29 (century non-leap) was accepted"
else
  pass "validate::rejects_feb29_century_nonleap_1900"
fi

# --- validate: numeric citation_url rejected (iter-6 fix iter6-1) ----------
bad_cite_num='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":42,"source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_cite_num" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_numeric_citation_url" "citation_url=42 was accepted"
else
  pass "validate::rejects_numeric_citation_url"
fi

# --- validate: numeric source rejected (iter-6 fix iter6-1) ----------------
bad_src_num='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":42,"prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_src_num" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_numeric_source" "source=42 was accepted"
else
  pass "validate::rejects_numeric_source"
fi

# --- validate: unknown top-level key rejected (additionalProperties:false) -
bad_top_extra='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","extra_top":"x","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_top_extra" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_unknown_top_level_key" "extra_top accepted at root"
else
  pass "validate::rejects_unknown_top_level_key"
fi

# --- validate: unknown per-provider key rejected ----------------------------
bad_prov_extra='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","extra_prov":1,"models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_prov_extra" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_unknown_provider_key" "extra_prov accepted on provider"
else
  pass "validate::rejects_unknown_provider_key"
fi

# --- validate: unknown per-model key rejected -------------------------------
bad_model_extra='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","extra_model":1,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_model_extra" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_unknown_model_key" "extra_model accepted on model"
else
  pass "validate::rejects_unknown_model_key"
fi

# --- validate: unknown per-tier key rejected --------------------------------
bad_tier_extra='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","extra_tier":1,"citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}'
if printf '%s' "$bad_tier_extra" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_unknown_tier_key" "extra_tier accepted on tier"
else
  pass "validate::rejects_unknown_tier_key"
fi

# --- validate: unknown per-prices key rejected ------------------------------
bad_prices_extra='{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"extra_price":3}}]}]}]}'
if printf '%s' "$bad_prices_extra" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
  fail "validate::rejects_unknown_prices_key" "extra_price accepted in prices"
else
  pass "validate::rejects_unknown_prices_key"
fi

# --- iter7-3: aliases array + optional price numeric ---
# Build a strict-mode doc from a snippet inserted into either the
# provider object (placeholder "__PROV__") or the model object
# ("__MODEL__") or the prices object ("__PRICES__"). expect_reject /
# expect_accept evaluate the doc through pricing-validate.jq.
v_doc() {
  # $1 = provider-extra json text (may be empty)
  # $2 = model-extra json text   (may be empty)
  # $3 = prices-extra json text  (may be empty)
  local provx="$1" modelx="$2" pricesx="$3"
  local provsep="" modelsep="" pricessep=""
  [ -n "$provx" ]   && provsep=","
  [ -n "$modelx" ]  && modelsep=","
  [ -n "$pricesx" ] && pricessep=","
  printf '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p"%s%s,"models":[{"name":"m"%s%s,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2%s%s}}]}]}]}' \
    "$provsep" "$provx" "$modelsep" "$modelx" "$pricessep" "$pricesx"
}
expect_reject() {
  local name="$1" doc="$2"
  if printf '%s' "$doc" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
    fail "$name" "accepted: ${doc:0:120}..."
  else
    pass "$name"
  fi
}
expect_accept() {
  local name="$1" doc="$2"
  if printf '%s' "$doc" | jq -e -f "${LIB}/pricing-validate.jq" >/dev/null 2>&1; then
    pass "$name"
  else
    fail "$name" "rejected: ${doc:0:120}..."
  fi
}

# aliases on provider/model: null + non-array are rejected; array OK;
# absent OK (already covered earlier as accepts_aliases_omitted-style).
expect_reject "validate::rejects_provider_aliases_null"  "$(v_doc '"aliases":null' '' '')"
expect_reject "validate::rejects_provider_aliases_int"   "$(v_doc '"aliases":42'   '' '')"
expect_reject "validate::rejects_model_aliases_null"     "$(v_doc '' '"aliases":null' '')"
expect_reject "validate::rejects_model_aliases_int"      "$(v_doc '' '"aliases":42'   '')"
expect_accept "validate::accepts_provider_aliases_array" "$(v_doc '"aliases":["claude"]' '' '')"
expect_accept "validate::accepts_aliases_omitted"        "$(v_doc '' '' '')"

# Optional price fields: null/string/negative rejected; absent OK.
expect_reject "validate::rejects_cache_read_null"     "$(v_doc '' '' '"cache_read_per_million":null')"
expect_reject "validate::rejects_cache_read_string"   "$(v_doc '' '' '"cache_read_per_million":"x"')"
expect_reject "validate::rejects_cache_read_negative" "$(v_doc '' '' '"cache_read_per_million":-1')"
expect_reject "validate::rejects_reasoning_null"      "$(v_doc '' '' '"reasoning_per_million":null')"
expect_accept "validate::accepts_cache_read_omitted"  "$(v_doc '' '' '')"

# --- validate: embedded pricing.json still passes the strict validator -----
# Sanity check: the production embedded data MUST keep passing after every
# tightening of pricing-validate.jq. If this fails, the strictness change
# introduced a false positive against real data.
if jq -e -f "${LIB}/pricing-validate.jq" "${REPO_ROOT}/internal/pricing/pricing.json" >/dev/null 2>&1; then
  pass "validate::embedded_pricing_json_passes_strict_validator"
else
  fail "validate::embedded_pricing_json_passes_strict_validator" "embedded pricing.json failed strict validator"
fi

# --- iter8-2: providers/models/tiers must be REAL arrays --------------
# The validator previously checked length and
# iterated `.providers[]` but never verified `type == "array"`. An
# input like `providers: {}` returned false silently (because {} has
# length 0); `providers: "x"` or `providers: 42` died with an
# uncontrolled jq runtime error rather than a clean structural
# rejection. The fix adds explicit array-type checks at every
# top-level position the schema declares as array
# (pricing.schema.json:38, :63, :92).
expect_reject "iter8-2::providers_object_rejected"   '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":{}}'
expect_reject "iter8-2::providers_string_rejected"   '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":"x"}'
expect_reject "iter8-2::providers_number_rejected"   '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":42}'
expect_reject "iter8-2::providers_null_rejected"     '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":null}'
expect_reject "iter8-2::models_object_rejected"      '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":{}}]}'
expect_reject "iter8-2::models_number_rejected"      '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":42}]}'
expect_reject "iter8-2::models_null_rejected"        '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":null}]}'
expect_reject "iter8-2::tiers_object_rejected"       '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":{}}]}]}'
expect_reject "iter8-2::tiers_null_rejected"         '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":null}]}]}'
expect_reject "iter8-2::tiers_string_rejected"       '{"version":2,"schema_url":"u","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":"x"}]}]}'

# --- LiteLLM cache-write 5m/1h mapping (Fix #5) -----------------------
# Source the bash library to get litellm_to_prices.
# shellcheck source=../lib/pricing-sources.sh disable=SC1091
. "${LIB}/pricing-sources.sh"
# Consumed by build_record inside the sourced library above.
# shellcheck disable=SC2034
LITELLM_URL="x"
# shellcheck disable=SC2034
OPENROUTER_URL="x"

# Anthropic published rates: 5m=$3.75/M, 1h=$6/M for Sonnet 4.5
# (3/M base × 1.25 / 2.0). LiteLLM stores these as per-token costs.
sonnet_record='{"input_cost_per_token":3e-06,"output_cost_per_token":1.5e-05,"cache_creation_input_token_cost":3.75e-06,"cache_creation_input_token_cost_above_1hr":6e-06}'
got_prices="$(printf '%s' "$sonnet_record" | litellm_to_prices)"
got_5m="$(printf '%s' "$got_prices" | jq -r '.cache_write_5m_per_million')"
got_1h="$(printf '%s' "$got_prices" | jq -r '.cache_write_1h_per_million')"
if [ "$got_5m" = "3.75" ] && [ "$got_1h" = "6" ]; then
  pass "fix5::cache_write_ttl_mapping_correct"
else
  fail "fix5::cache_write_ttl_mapping_correct" "got 5m=${got_5m} 1h=${got_1h}, want 5m=3.75 1h=6"
fi

# --- summary ---------------------------------------------------------
echo
echo "${PASS} passed, ${FAIL} failed"
[ "$FAIL" -eq 0 ]
