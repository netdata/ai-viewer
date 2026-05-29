#!/usr/bin/env bash
#
# pricing-sources.sh — sourced library for scripts/refresh-pricing.sh.
# Contains the per-record LiteLLM / OpenRouter lookup helpers and the
# price-shape converters. Extracted from refresh-pricing.sh to keep
# the entry script lean per the project file-size budget.
#
# All functions are pure (no exits, no global state writes except
# returning via stdout) so the calling script controls error flow.
#
# Required env vars: LITELLM_URL, OPENROUTER_URL set by caller.

# build_records_from_seeds walks the seed list, calls per-source
# lookup helpers, and emits one JSON record per priced (provider,
# model). Sets the global MISSING_COUNT to the number of seeds with
# no usable pricing data and appends each missing pair to the global
# MISSING_PAIRS array so the caller can list them in the gate error.
# The caller (refresh-pricing.sh) must define warn() before sourcing
# this library.
MISSING_COUNT=0
MISSING_PAIRS=()
build_records_from_seeds() {
  local seeds="$1" litellm_json="$2" or_json="$3" records="$4"
  : > "$records"
  MISSING_COUNT=0
  MISSING_PAIRS=()
  local provider model lit_record or_record record
  while IFS=$'\t' read -r provider model; do
    [ -z "$provider" ] && continue
    [ -z "$model" ] && continue
    lit_record="$(lookup_litellm "$provider" "$model" "$litellm_json")"
    or_record="$(lookup_openrouter "$provider" "$model" "$or_json")"
    if ! record="$(build_record "$provider" "$model" "$lit_record" "$or_record")"; then
      warn "no pricing found for ${provider}/${model} in any source"
      MISSING_COUNT=$((MISSING_COUNT+1))
      MISSING_PAIRS+=("${provider}/${model}")
      continue
    fi
    printf '%s\n' "$record" >> "$records"
  done < "$seeds"
}

# expand_add_providers replaces sentinel __ALL__ rows with every
# matching <provider>/<model> key from the LiteLLM JSON. Without
# LiteLLM data the sentinel rows are dropped with a warning. This
# function is sourced from the entry script (refresh-pricing.sh)
# which calls warn() — keep that helper available before sourcing.
expand_add_providers() {
  local seeds_tmp="$1"
  local litellm_json="$2"
  if ! grep -q '__ALL__' "$seeds_tmp"; then
    return 0
  fi
  if [ ! -s "$litellm_json" ] || ! jq -e 'type=="object"' "$litellm_json" >/dev/null 2>&1; then
    warn "--add-provider expansion skipped: LiteLLM JSON not available"
    grep -v $'\t__ALL__$' "$seeds_tmp" > "${seeds_tmp}.tmp" || true
    mv "${seeds_tmp}.tmp" "$seeds_tmp"
    return 0
  fi
  local expanded="${seeds_tmp}.expanded"
  grep -v $'\t__ALL__$' "$seeds_tmp" > "$expanded" || true
  local prov
  while IFS=$'\t' read -r prov _; do
    # $prov is passed as a jq `--arg` (data, never code) so a
    # malicious value cannot break out of the string literal in the
    # jq program. parse_args additionally validates each
    # --add-provider against namePattern, so this is layered defence.
    jq -r --arg p "${prov}/" --arg prov "$prov" \
      'keys[] | select(startswith($p)) | sub("^[^/]+/"; "") | "\($prov)\t" + .' \
      < "$litellm_json" >> "$expanded"
  done < <(awk -F'\t' '$2=="__ALL__" {print}' "$seeds_tmp")
  sort -u "$expanded" -o "$expanded"
  mv "$expanded" "$seeds_tmp"
}

# lookup_litellm: try "<provider>/<model>" then "<model>" forms.
lookup_litellm() {
  local provider="$1" model="$2" litellm_json="$3"
  local key result
  key="${provider}/${model}"
  result="$(jq -c --arg k "$key" '.[$k] // empty' < "$litellm_json")"
  if [ -z "$result" ]; then
    result="$(jq -c --arg k "$model" '.[$k] // empty' < "$litellm_json")"
  fi
  printf '%s' "$result"
}

lookup_openrouter() {
  local provider="$1" model="$2" or_json="$3"
  local key
  key="${provider}/${model}"
  jq -c --arg k "$key" '.data[] | select(.id == $k) // empty' < "$or_json"
}

# litellm_to_prices converts a LiteLLM record into our per-million
# shape. LiteLLM stores per-token; multiply by 1e6 and round to four
# decimals.
#
# Field mapping for Anthropic cache writes (verified against LiteLLM's
# Anthropic cost_calculation.py and Anthropic's published 5m=1.25x /
# 1h=2x multipliers): cache_creation_input_token_cost is the base
# (5-minute) rate; cache_creation_input_token_cost_above_1hr is the
# 1-hour rate. The iter-1 mapping had these swapped; iter-2 corrects.
litellm_to_prices() {
  jq -c '
    def per_million(x): if x == null then null else (x * 1000000 | . * 10000 | round / 10000) end;
    {
      input_per_million:           per_million(.input_cost_per_token),
      output_per_million:          per_million(.output_cost_per_token),
      cache_read_per_million:      per_million(.cache_read_input_token_cost),
      cache_write_per_million:     per_million(.cache_creation_input_token_cost),
      cache_write_5m_per_million:  per_million(.cache_creation_input_token_cost // null),
      cache_write_1h_per_million:  per_million(.cache_creation_input_token_cost_above_1hr // null),
      reasoning_per_million:       per_million(.output_cost_per_reasoning_token // null)
    }
    | with_entries(select(.value != null))
  '
}

openrouter_to_prices() {
  jq -c '
    def per_million(s): if s == null or s == "" then null else (s | tonumber * 1000000 | . * 10000 | round / 10000) end;
    {
      input_per_million:       per_million(.pricing.prompt // null),
      output_per_million:      per_million(.pricing.completion // null),
      cache_read_per_million:  per_million(.pricing.input_cache_read // null),
      cache_write_per_million: per_million(.pricing.input_cache_write // null)
    }
    | with_entries(select(.value != null))
  '
}

# diff_check warns when LiteLLM and OpenRouter disagree by > 20% on
# any metric for the same (provider, model). Non-fatal — OpenRouter
# bakes in its margin so divergence is expected.
diff_check() {
  local provider="$1" model="$2" lit_prices="$3" or_prices="$4"
  jq -nr \
    --arg p "$provider" --arg m "$model" \
    --argjson l "$lit_prices" --argjson o "$or_prices" \
    '
      ($l | to_entries | map({(.key): .value}) | add // {}) as $lmap |
      ($o | to_entries | map({(.key): .value}) | add // {}) as $omap |
      ($lmap | keys[])
      | . as $k
      | ($lmap[$k] // null) as $a
      | ($omap[$k] // null) as $b
      | select($a != null and $b != null and $a > 0 and $b > 0)
      | (($a - $b | fabs) / (($a + $b) / 2)) as $rel
      | select($rel > 0.20)
      | "DRIFT:\($p)/\($m):\($k):litellm=\($a):openrouter=\($b):rel=\($rel | . * 100 | round)%"
    ' \
    | while IFS= read -r line; do warn "$line"; done
}

# prices_have_required returns 0 (true) when the given jq-emitted
# prices object carries both required keys (input_per_million and
# output_per_million) with numeric values. Used by build_record to
# decide whether a source's record is usable. A LiteLLM record that
# parses to {} or to a partial object (no input_per_million, say,
# because LiteLLM tracks the model only for context window) must NOT
# block the OpenRouter fallback — the spec's layered-source behaviour
# is "use the FIRST source that produces a COMPLETE record".
prices_have_required() {
  local prices="$1"
  [ -n "$prices" ] || return 1
  jq -e 'type=="object" and (.input_per_million|type)=="number" and (.output_per_million|type)=="number"' \
    >/dev/null 2>&1 <<<"$prices"
}

# enforce_missing_seed_gate is the iter-8 missing-seed gate, extracted
# in iter-9 (line budget) so the
# refresh-pricing.sh entry script stays under its 400-line file budget
# AND its main() under the 60-line function budget. Reads the globals
# MISSING_COUNT / MISSING_PAIRS that build_records_from_seeds sets and
# the ALLOW_PARTIAL flag parse_args records. The caller must define
# log() and die() before sourcing this library — both are defined in
# refresh-pricing.sh.
enforce_missing_seed_gate() {
  if [ "$MISSING_COUNT" -gt 0 ] && [ "$ALLOW_PARTIAL" != "1" ]; then
    log "missing pricing data for ${MISSING_COUNT} requested seed(s):"
    local pair
    for pair in "${MISSING_PAIRS[@]+"${MISSING_PAIRS[@]}"}"; do
      log "  - ${pair}"
    done
    die "refusing to write a partial pricing.json. Re-run with --allow-partial to opt in to omitting these models, or correct the seeds."
  fi
}

# validate_or_preserve runs the jq schema validator on the proposed
# JSON; on failure it copies the file to a timestamped diagnostic
# path under internal/pricing/ (gitignored) BEFORE die()-ing so the
# operator can inspect the rejected payload — the EXIT trap in
# refresh-pricing.sh deletes $tmp on the way out, so an in-tmp copy
# would vanish. Extracted in iter-9 to keep main() ≤ 60 lines.
# Caller provides: validate_proposed (the jq -f filter wrapper),
# REPO_ROOT (env), warn(), die().
validate_or_preserve() {
  local proposed="$1"
  if validate_proposed "$proposed"; then
    return 0
  fi
  local stamp preserved
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  preserved="${REPO_ROOT}/internal/pricing/.proposed-failed-validation-${stamp}.json"
  cp -- "$proposed" "$preserved" 2>/dev/null \
    || warn "could not preserve diagnostic file at ${preserved}"
  die "proposed JSON failed schema validation. See ${preserved} for inspection."
}

# write_proposed asks the operator whether to apply, removes the
# existing file (defence against following a symlink — validate_out_path
# already rejected out-of-tree symlinks but this is the second line of
# defence), copies the proposed file, and warns about any unfilled
# seeds. Returns whatever prompt_apply returns so the caller can
# distinguish "applied" from "operator declined" / "dry-run". Extracted
# in iter-9 to keep main() small.
# Caller provides: OUT_PATH (env), MISSING_COUNT (global), prompt_apply,
# log(), warn().
write_proposed() {
  local proposed="$1"
  if prompt_apply "$OUT_PATH" "$proposed"; then
    rm -f -- "$OUT_PATH"
    cp -- "$proposed" "$OUT_PATH"
    log "wrote ${OUT_PATH}"
    if [ "$MISSING_COUNT" -gt 0 ]; then
      warn "${MISSING_COUNT} model(s) had no pricing data — they were not updated"
    fi
  else
    log "exit without write"
  fi
}

# build_record assembles a single record for jq merge consumption.
# Layered-source semantics: LiteLLM is preferred when its record
# carries the required input/output prices; otherwise OpenRouter is
# tried as a fallback. The schema's required[] (input_per_million,
# output_per_million) is the lower bound for "complete". Returns 1
# (no output) when neither source produces a complete record so the
# caller can warn + count missing.
build_record() {
  local provider="$1" model="$2" lit_record="$3" or_record="$4"
  local lit_prices="" or_prices="" ctx_max=""
  if [ -n "$lit_record" ]; then
    lit_prices="$(printf '%s' "$lit_record" | litellm_to_prices)"
    ctx_max="$(printf '%s' "$lit_record" | jq -r '.max_input_tokens // .max_tokens // empty')"
  fi
  if [ -n "$or_record" ]; then
    or_prices="$(printf '%s' "$or_record" | openrouter_to_prices)"
  fi
  if prices_have_required "$lit_prices" && prices_have_required "$or_prices"; then
    diff_check "$provider" "$model" "$lit_prices" "$or_prices"
  fi
  local source citation prices
  if prices_have_required "$lit_prices"; then
    source="litellm"; citation="${LITELLM_URL}"; prices="$lit_prices"
  elif prices_have_required "$or_prices"; then
    source="openrouter"; citation="${OPENROUTER_URL}"; prices="$or_prices"
  else
    return 1
  fi
  jq -nc \
    --arg p "$provider" --arg m "$model" \
    --arg s "$source" --arg c "$citation" \
    --arg cm "$ctx_max" \
    --argjson pr "$prices" \
    '{provider:$p, model:$m, source:$s, citation_url:$c,
      ctx_max:(if $cm == "" then null else ($cm|tonumber) end),
      prices:$pr}'
}
