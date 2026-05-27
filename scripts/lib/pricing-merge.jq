# pricing-merge.jq — pure jq library used by scripts/refresh-pricing.sh.
#
# Public function:
#   merge_pricing($base; $records; $today; $now; $generated_by) -> doc
#
# For each $records[] (each shaped like build_record's output:
#   { provider, model, source, citation_url, ctx_max?, prices })
# this folds the record into $base.providers[], either prepending a
# fresh tier dated $today (when prices differ from the most-recent
# existing tier) or leaving the existing tier alone.
#
# New providers and new models are inserted in-place; existing models'
# ctx_max is refreshed from the incoming record so a vendor update is
# reflected on the next run. Older tiers are never lost.
#
# Invariants verified by the bash caller (validate_proposed):
#   - .version == 2
#   - every provider has >=1 model; every model has >=1 tier
#   - every tier has effective_date, citation_url, source, prices
#   - prices.input_per_million and prices.output_per_million are numbers
#
# Why this lives outside refresh-pricing.sh: the bash script was over
# the project 400-line file budget. Moving the heavy jq logic here
# leaves the entry script under budget and lets jq syntax be linted
# independently.

# tier_from_record($r; $today) -> { effective_date, citation_url, source, prices }
def tier_from_record($r; $today):
  {
    effective_date: $today,
    citation_url:   $r.citation_url,
    source:         $r.source,
    prices:         $r.prices,
  };

# prepend_or_skip($m; $r; $today) returns an updated model object whose
# tiers either get a new tier prepended (when prices differ from the
# most-recent existing tier) or remain unchanged.
def prepend_or_skip($m; $r; $today):
  ($m.tiers // []) as $tiers
  | ($tiers | sort_by(.effective_date) | reverse | first) as $latest
  | if $latest != null and ($latest.prices == $r.prices) then
      $m
    else
      $m | .tiers = [tier_from_record($r; $today)] + $tiers
    end;

# new_model_from_record($r; $today; $name): build a new model object.
# $name is the (already case-folded) canonical model name. ctx_max is
# omitted entirely when the incoming record has no value, because the
# validator (and schema) reject `ctx_max: null` — the field is
# optional but must be an integer when present.
def new_model_from_record($r; $today; $name):
  ({ name: $name, tiers: [tier_from_record($r; $today)] })
  + (if $r.ctx_max != null then { ctx_max: $r.ctx_max } else {} end);

# apply_record($state; $r; $today) folds one record into the
# accumulator. Provider and model names are case-folded so two seeds
# that differ only in casing converge on a single canonical entry —
# the Go loader enforces case-insensitive uniqueness, so leaking
# case-variant duplicates here would produce JSON that passes the jq
# validator but fails pricing.New().
def apply_record($state; $r; $today):
  ($r.provider | ascii_downcase) as $rprov
  | ($r.model    | ascii_downcase) as $rmod
  | $state
  | (if any(.providers[]; (.name | ascii_downcase) == $rprov) then
       .
     else
       .providers += [{name: $rprov, models: []}]
     end)
  | .providers |= map(
      if (.name | ascii_downcase) == $rprov then
        (if any(.models[]; (.name | ascii_downcase) == $rmod) then
           # Update existing model: refresh ctx_max from incoming
           # record (vendors do change ctx_max occasionally) and
           # delegate the tier decision to prepend_or_skip.
           .models |= map(
             if (.name | ascii_downcase) == $rmod then
               (if $r.ctx_max != null then .ctx_max = $r.ctx_max else . end)
               | prepend_or_skip(.; $r; $today)
             else . end
           )
         else
           .models += [ new_model_from_record($r; $today; $rmod) ]
         end)
      else . end
    );

# merge_pricing is the public entry point used by refresh-pricing.sh.
def merge_pricing($base; $records; $today; $now; $generated_by):
  reduce $records[] as $r ($base; apply_record(.; $r; $today))
  | .generated_at = $now
  | .generated_by = $generated_by;
