# pricing-validate.jq — jq filter validating a proposed pricing.json
# against the structural invariants of pricing.schema.json AND the
# extra invariants the Go loader enforces. Consumed via `jq -e -f`.
#
# Line-budget note: this filter sits at ~161 lines vs the project's
# default 400-line cap; the per-jq-function structure is intentionally
# linear (one `valid_X` definition per object level: prices, tier,
# model, provider, doc) so each schema invariant is greppable in one
# place. Splitting it further would either duplicate the doc-comments
# above each rule or push the leap-year / per-month date helpers into
# a second file the operator would have to cross-reference. The
# linear shape is the better tradeoff; if this file ever grows past
# ~200 lines, the leap_year + valid_date helpers should move to
# scripts/lib/pricing-date.jq.
#
# This filter is schema-equivalent: a doc that passes here also passes
# the Go loader. Iter-6 fix iter6-1 tightens it so every constraint
# the JSON Schema declares is enforced here too:
#
#   - additionalProperties: false at every object level (top-level
#     doc, provider, model, tier, prices). Unknown keys are rejected
#     just as `DisallowUnknownFields()` rejects them in the loader.
#   - ctx_max must be a NON-NEGATIVE INTEGER (the schema declares
#     integer; floats like 1.5 are now rejected at the jq layer
#     instead of slipping through to the Go loader).
#   - effective_date is calendar-valid INCLUDING leap-year handling
#     for Feb 29: a year is leap iff divisible by 4 EXCEPT century
#     years that are not divisible by 400. Matches Go's time.Parse.
#   - citation_url, source, name, alias values, schema_url,
#     generated_at, generated_by are all string-type-checked. The
#     iter-5 jq used `length` which silently accepts numbers; iter-6
#     adds an explicit `type == "string"` first.
#   - Provider names within a doc are unique CASE-INSENSITIVELY (the
#     Go loader case-folds before duplicate-checking; see
#     internal/pricing/loader.go:179). Model names within a provider
#     are unique CASE-INSENSITIVELY (loader.go:204).
#   - `aliases` keys (model and provider) must be ARRAYS when present;
#     `null` and non-array values are rejected. Iter-7 fix iter7-3
#     replaces the earlier `(.aliases // [])` form (which accepted
#     null) with `has("aliases") ⇒ type == "array"`, matching the
#     schema declaration at pricing.schema.json:55-61 and :80-86.
#   - Optional price fields (`cache_read_per_million`,
#     `cache_write_per_million`, `cache_write_5m_per_million`,
#     `cache_write_1h_per_million`, `reasoning_per_million`) must be
#     NUMBERS when present; null is rejected (the schema declares
#     numeric type, not nullable). Iter-7 fix iter7-3 replaces
#     `(.X == null) or (.X | nn_number)` (which was true for null) with
#     `has("X") ⇒ nn_number`.
#   - `providers`, `models`, and `tiers` arrays must be REAL ARRAYS
#     (not objects, strings, numbers, or null). Iter-8 fix iter8-2
#     adds explicit `type == "array"` checks before iterating so a
#     malformed shape (e.g. `providers: {}`) is rejected cleanly with
#     a `false` outcome rather than via an uncontrolled jq runtime
#     error (`Cannot iterate over X`). The schema declares array type
#     at pricing.schema.json:38, :63, :92 — this filter now matches.

def is_str_nonempty: type == "string" and length > 0;
def safe_name: type == "string" and test("^[a-zA-Z0-9][a-zA-Z0-9._/-]*$");
def nn_number: type == "number" and . >= 0;
def nn_integer: type == "number" and . >= 0 and ((. | floor) == .);

# leap_year($y) -> true when $y is a Gregorian leap year. Divisible
# by 4, EXCEPT century years that are not also divisible by 400.
def leap_year($y):
  ($y % 4 == 0) and (($y % 100 != 0) or ($y % 400 == 0));

# valid_date: calendar-valid YYYY-MM-DD. jq's test() is regex-only so
# month-specific day bounds are checked by hand. Feb 29 uses leap_year.
def valid_date:
  type == "string"
  and test("^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$")
  and (
    (.[0:4] | tonumber) as $y
    | (.[5:7] | tonumber) as $m
    | (.[8:10] | tonumber) as $d
    | if $m == 2 then (if leap_year($y) then $d <= 29 else $d <= 28 end)
      elif $m == 4 or $m == 6 or $m == 9 or $m == 11 then $d <= 30
      else $d <= 31
      end
  );

# case_fold_unique($names) -> true when every lowercased entry is
# unique. Empty / single-element arrays trivially pass.
def case_fold_unique($names):
  ($names | map(ascii_downcase)) as $lc
  | ($lc | length) == ($lc | unique | length);

# only_keys($allowed): true when the input object has no keys outside
# the $allowed set. Enforces additionalProperties: false from the
# JSON Schema. Required-key presence is enforced separately by the
# specific field checks below.
def only_keys($allowed):
  type == "object"
  and ((keys_unsorted - $allowed) | length) == 0;

def valid_prices:
  only_keys([
    "input_per_million", "output_per_million",
    "cache_read_per_million", "cache_write_per_million",
    "cache_write_5m_per_million", "cache_write_1h_per_million",
    "reasoning_per_million"
  ])
  and (.input_per_million  | nn_number)
  and (.output_per_million | nn_number)
  # Optional price fields: schema declares them as numbers when present.
  # Explicit-null was previously accepted because
  # earlier wording used `(.X == null) or (.X | nn_number)`, which is
  # true for null. Use `has(...) | not` to distinguish "field absent"
  # (legal) from "field set to null" (rejected, since the schema
  # requires a number).
  and ((has("cache_read_per_million")     | not) or (.cache_read_per_million     | nn_number))
  and ((has("cache_write_per_million")    | not) or (.cache_write_per_million    | nn_number))
  and ((has("cache_write_5m_per_million") | not) or (.cache_write_5m_per_million | nn_number))
  and ((has("cache_write_1h_per_million") | not) or (.cache_write_1h_per_million | nn_number))
  and ((has("reasoning_per_million")      | not) or (.reasoning_per_million      | nn_number));

def valid_tier:
  only_keys(["effective_date", "citation_url", "source", "prices"])
  and (.effective_date | valid_date)
  and (.citation_url | is_str_nonempty)
  and (.source | is_str_nonempty)
  and (.prices | valid_prices);

def valid_model:
  only_keys(["name", "aliases", "ctx_max", "tiers"])
  and (.name | safe_name)
  # Schema declares aliases as an array; the prior
  # `(.aliases // [])` form silently accepted `aliases: null` and any
  # non-array value (e.g. `42`). When the key is present it must be a
  # real array of safe-name strings; when absent the model has no
  # aliases. (Matches internal/pricing/pricing.schema.json:80-86.)
  and ((has("aliases") | not) or ((.aliases | type) == "array" and (.aliases | all(safe_name))))
  and ((has("ctx_max") | not) or (.ctx_max | nn_integer))
  # Iter-8 fix iter8-2: tiers must be a real array. Schema declares
  # array at pricing.schema.json:92. Without this check `tiers: {}`
  # silently failed via `length=0 > 0 = false` and `tiers: 42` died
  # with an unhelpful "Cannot iterate over number" jq runtime error.
  and (.tiers | type) == "array"
  and (.tiers | length) > 0
  and all(.tiers[]; valid_tier);

def valid_provider:
  only_keys(["name", "aliases", "models"])
  and (.name | safe_name)
  # Same reasoning as valid_model above; aliases on the provider object
  # is e.g. `["claude"]` for anthropic. (Matches schema:55-61.)
  and ((has("aliases") | not) or ((.aliases | type) == "array" and (.aliases | all(safe_name))))
  # Iter-8 fix iter8-2: models must be a real array. Schema declares
  # array at pricing.schema.json:63.
  and (.models | type) == "array"
  and (.models | length) > 0
  and case_fold_unique([.models[].name])
  and all(.models[]; valid_model);

only_keys([
  "version", "schema_url", "generated_at", "generated_by",
  "currency", "providers"
])
and .version == 2
and (.schema_url | is_str_nonempty)
and (.generated_at | is_str_nonempty)
and (.generated_by | is_str_nonempty)
and .currency == "USD"
# Iter-8 fix iter8-2: providers must be a real array. Schema declares
# array at pricing.schema.json:38. Without this check `providers: {}`
# returned false (length=0) and `providers: "x"` died with an
# uncontrolled jq runtime error — neither matches the schema's clean
# "rejected, not an array" outcome.
and (.providers | type) == "array"
and (.providers | length) > 0
and case_fold_unique([.providers[].name])
and all(.providers[]; valid_provider)
