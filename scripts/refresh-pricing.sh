#!/usr/bin/env bash
#
# refresh-pricing.sh — operator-runnable refresh for
# internal/pricing/pricing.json. Pulls per-model prices from LiteLLM
# (primary) + OpenRouter (cross-check); builds a proposed file, prints
# a diff against the current one, prompts the operator, writes on yes.
#
# Spec: .agents/sow/specs/pricing.md. Library layout (extracted to
# keep this entry under the file-size budget):
#   scripts/lib/pricing-merge.jq           — merge logic
#   scripts/lib/pricing-validate.jq        — schema-equivalent jq filter
#   scripts/lib/pricing-sources.sh         — fetch/lookup/build_record
#   scripts/lib/pricing-validate-input.sh  — CLI / DB / path validators

set -euo pipefail
IFS=$'\n\t'

# --- paths and transparency -----------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PRICING_JSON_DEFAULT="${REPO_ROOT}/internal/pricing/pricing.json"
PRICING_SCHEMA="${REPO_ROOT}/internal/pricing/pricing.schema.json"
JQ_LIB_DIR="${SCRIPT_DIR}/lib"

if [ -t 2 ]; then
  RED=$'\033[0;31m'
  YELLOW=$'\033[1;33m'
  GRAY=$'\033[0;90m'
  NC=$'\033[0m'
else
  RED=""
  YELLOW=""
  GRAY=""
  NC=""
fi

# run echoes the command to stderr (with cwd) then executes it. The
# direct-exec form preserves the failing command's true exit code.
run() {
  printf >&2 '%s\n' "${GRAY}$(pwd) >${NC} ${YELLOW}$(_quote "$@")${NC}"
  "$@"
  local exit_code=$?
  if [ "$exit_code" -ne 0 ]; then
    printf >&2 '%s\n' "${RED}refresh-pricing: command failed with exit ${exit_code}: $1${NC}"
    return "$exit_code"
  fi
}

_quote() {
  local arg
  for arg in "$@"; do
    printf '%q ' "$arg"
  done
}

log()  { printf >&2 '%s\n' "$*"; }
warn() { printf >&2 'refresh-pricing: WARN: %s\n' "$*"; }
die()  { printf >&2 'refresh-pricing: ERROR: %s\n' "$*"; exit 1; }

# Source the input-validation library now that die/warn are defined.
# shellcheck source=./lib/pricing-validate-input.sh disable=SC1091
. "${JQ_LIB_DIR}/pricing-validate-input.sh"

# --- defaults ------------------------------------------------------------

SOURCE="all"
DB_PATH="${HOME}/.local/share/ai-viewer/ingest.db"
OUT_PATH="${PRICING_JSON_DEFAULT}"
DRY_RUN="0"
# ALLOW_PARTIAL is read by enforce_missing_seed_gate in pricing-sources.sh;
# export marks the cross-source dependency for static checkers.
export ALLOW_PARTIAL="0"
ADD_PROVIDERS=()
ADD_MODELS=()

usage() {
  cat <<'USAGE'
refresh-pricing.sh - refresh internal/pricing/pricing.json.

Usage: scripts/refresh-pricing.sh [--source=<NAME>] [--db=<PATH>] [--out=<PATH>]
                                  [--dry-run] [--allow-partial]
                                  [--add-provider=<NAME>]...
                                  [--add-model=<provider/model>]...

  --source=NAME       litellm | openrouter | cli:<tool> | all (default: all)
  --db=PATH           SQLite ingest DB for seed discovery (default
                      ~/.local/share/ai-viewer/ingest.db)
  --out=PATH          output JSON (default internal/pricing/pricing.json).
                      Must resolve to internal/pricing/pricing.json or
                      a path under $TMPDIR.
  --dry-run           build + diff, do not write.
  --allow-partial     proceed even when one or more requested seeds had
                      no pricing data in any source (default: refuse to
                      write a partial pricing.json before diff/prompt).
  --add-provider=NAME expand all <NAME>/<*> models from LiteLLM. Repeatable.
  --add-model=P/M     add one (provider, model) pair. Repeatable.
  --help, -h          show this message and exit 0.

Full spec: .agents/sow/specs/pricing.md.
USAGE
}

# --- argument parsing ----------------------------------------------------

parse_args() {
  local pname pmodel
  for arg in "$@"; do
    case "$arg" in
      --source=*)         SOURCE="${arg#*=}" ;;
      --db=*)             DB_PATH="${arg#*=}" ;;
      --out=*)            OUT_PATH="${arg#*=}" ;;
      --dry-run)          DRY_RUN="1" ;;
      --allow-partial)    ALLOW_PARTIAL="1" ;;
      --add-provider=*)
        # Validate RAW first: tab/newline are TSV corruption vectors
        # that must be rejected (not silently stripped).
        # sanitize_cli_field is a belt-and-suspenders strip applied
        # AFTER validation in case a future path bypasses validate_name.
        pname="${arg#*=}"
        validate_name "--add-provider" "$pname"
        ADD_PROVIDERS+=("$(sanitize_cli_field "$pname")")
        ;;
      --add-model=*)
        pmodel="${arg#*=}"
        case "$pmodel" in
          */*)
            validate_name "--add-model provider" "${pmodel%%/*}"
            validate_name "--add-model model"    "${pmodel#*/}"
            ;;
          *) die "--add-model expects provider/model (got: $(printf %q "$pmodel"))" ;;
        esac
        ADD_MODELS+=("$(sanitize_cli_field "$pmodel")")
        ;;
      --help|-h)          usage; exit 0 ;;
      *)                  usage >&2; die "unknown argument: $arg" ;;
    esac
  done

  case "$SOURCE" in
    litellm|openrouter|all) ;;
    cli:*)
      [ -n "${SOURCE#cli:}" ] || die "--source=cli: must specify a tool (e.g. cli:codex)"
      ;;
    *) die "--source must be one of: litellm, openrouter, cli:<tool>, all (got: $SOURCE)" ;;
  esac

  validate_out_path
  validate_db_path
}

# --- environment checks --------------------------------------------------

require_tools() {
  local missing=""
  for tool in curl jq sqlite3; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      missing="${missing} ${tool}"
    fi
  done
  if [ -n "$missing" ]; then
    die "missing required tools:${missing}"
  fi
  # show_review_diff prefers git, falls back to diff;
  # require at least one (both missing was previously rejected at this
  # gate before the fallback could run).
  if ! command -v git >/dev/null 2>&1 && ! command -v diff >/dev/null 2>&1; then
    die "neither 'git' nor 'diff' available; need one to show the review diff"
  fi
  [ -e "$PRICING_SCHEMA" ] || die "schema file missing: $PRICING_SCHEMA"
  [ -e "${JQ_LIB_DIR}/pricing-merge.jq" ] || die "jq library missing: ${JQ_LIB_DIR}/pricing-merge.jq"
  [ -e "${JQ_LIB_DIR}/pricing-validate.jq" ] || die "jq library missing: ${JQ_LIB_DIR}/pricing-validate.jq"
  [ -e "${JQ_LIB_DIR}/pricing-sources.sh" ] || die "bash library missing: ${JQ_LIB_DIR}/pricing-sources.sh"
  [ -e "${JQ_LIB_DIR}/pricing-validate-input.sh" ] || die "bash library missing: ${JQ_LIB_DIR}/pricing-validate-input.sh"
}

# --- seed list discovery -------------------------------------------------
# Build the (provider, model) seed list from the operator's DB plus
# --add-* extensions (<P>\t__ALL__ is expanded by expand_add_providers
# against LiteLLM). DB rows re-validated against namePattern; names
# lowercased (Go loader case-folds; loader.go:179,:204).
seeds_from_db() {
  local seeds_tmp="$1" extras_present="$2"
  log "discover: querying ${DB_PATH}"
  local raw_db="${seeds_tmp}.db"
  if ! sqlite3 "file:${DB_PATH}?mode=ro" \
      "SELECT DISTINCT provider, model FROM ops WHERE kind='llm' AND provider <> '' AND model <> '' ORDER BY provider, model;" \
      | awk -F'|' 'NF==2 {printf "%s\t%s\n", $1, $2}' > "$raw_db" 2>&1; then
    if [ "$extras_present" -eq 1 ]; then
      warn "DB query failed; continuing with --add-* extensions"
    else
      die "DB query failed and no --add-* extensions supplied"
    fi
  fi
  local db_prov db_model
  while IFS=$'\t' read -r db_prov db_model; do
    [ -n "$db_prov" ] || continue
    [ -n "$db_model" ] || continue
    validate_db_seed_name "DB ops.provider" "$db_prov"
    validate_db_seed_name "DB ops.model"    "$db_model"
    printf '%s\t%s\n' "${db_prov,,}" "${db_model,,}" >> "$seeds_tmp"
  done < "$raw_db"
  rm -f "$raw_db"
}

discover_seed_list() {
  local seeds_tmp="$1"
  : > "$seeds_tmp"

  local extras_present=0
  if [ "${#ADD_PROVIDERS[@]}" -gt 0 ] || [ "${#ADD_MODELS[@]}" -gt 0 ]; then
    extras_present=1
  fi

  if [ -f "$DB_PATH" ]; then
    seeds_from_db "$seeds_tmp" "$extras_present"
  else
    log "discover: --db not found (${DB_PATH}); using --add-* extensions only"
  fi

  # --add-* names are lowered for the same reason DB seeds are.
  local provider
  for provider in "${ADD_PROVIDERS[@]+"${ADD_PROVIDERS[@]}"}"; do
    printf '%s\t__ALL__\n' "${provider,,}" >> "$seeds_tmp"
  done

  local pair p_lc
  for pair in "${ADD_MODELS[@]+"${ADD_MODELS[@]}"}"; do
    # parse_args already validated each half via validate_name; just
    # split + case-fold here.
    p_lc="${pair,,}"
    printf '%s\t%s\n' "${p_lc%%/*}" "${p_lc#*/}" >> "$seeds_tmp"
  done

  sort -u "$seeds_tmp" -o "$seeds_tmp"

  if [ ! -s "$seeds_tmp" ]; then
    die "discover: zero seeds. Either point --db at a populated ingest DB or pass --add-provider/--add-model."
  fi

  local count
  count=$(wc -l < "$seeds_tmp")
  log "discover: ${count} (provider, model) seeds"
}

# --- fetch helpers -------------------------------------------------------

# Iter-9 fix iter9-2: URLs are env-overridable (test harness points to
# file:// fixtures for offline runs); curl handles both schemes.
LITELLM_URL="${LITELLM_URL:-https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json}"
OPENROUTER_URL="${OPENROUTER_URL:-https://openrouter.ai/api/v1/models}"

fetch_litellm() {
  local out="$1"
  log "fetch: LiteLLM model_prices_and_context_window.json"
  if ! run curl -fsSL --connect-timeout 15 --max-time 60 -o "$out" "$LITELLM_URL"; then
    die "fetch: LiteLLM curl failed (network down? URL changed? see ${LITELLM_URL})"
  fi
  jq -e 'type=="object"' "$out" >/dev/null || die "fetch: LiteLLM payload is not a JSON object"
}

fetch_openrouter() {
  local out="$1"
  log "fetch: OpenRouter /api/v1/models"
  if ! run curl -fsSL --connect-timeout 15 --max-time 60 -o "$out" "$OPENROUTER_URL"; then
    if [ "$SOURCE" = "litellm" ]; then
      warn "OpenRouter unreachable but --source=litellm so continuing"
      printf '{"data":[]}' > "$out"
      return 0
    fi
    die "fetch: OpenRouter curl failed (network down?)"
  fi
  jq -e '.data | type=="array"' "$out" >/dev/null || die "fetch: OpenRouter payload has no .data array"
}

# fetch_sources orchestrates per-source fetch. On return both $1
# (litellm) and $2 (openrouter) exist — empty JSON stubs when disabled.
fetch_sources() {
  local litellm_json="$1" or_json="$2"
  case "$SOURCE" in
    litellm)
      fetch_litellm "$litellm_json"
      printf '{"data":[]}' > "$or_json"
      ;;
    openrouter)
      fetch_openrouter "$or_json"
      printf '{}' > "$litellm_json"
      ;;
    cli:*)
      die "cli:<tool> source not yet implemented (placeholder; tracked under follow-up SOW)"
      ;;
    all)
      fetch_litellm "$litellm_json"
      fetch_openrouter "$or_json"
      ;;
  esac
}

# --- merge + validate ---------------------------------------------------

today_iso() { date -u +%Y-%m-%d; }
now_iso()   { date -u +%Y-%m-%dT%H:%M:%SZ; }

merge_into_existing() {
  local existing="$1" records="$2" generator="$3"
  local base
  if [ -f "$existing" ]; then
    base="$(cat "$existing")"
  else
    base='{"version":2,"schema_url":"https://github.com/netdata/ai-viewer/blob/master/internal/pricing/pricing.schema.json","currency":"USD","providers":[]}'
  fi
  jq -s -L "$JQ_LIB_DIR" \
    --argjson base "$base" \
    --arg today "$(today_iso)" \
    --arg now "$(now_iso)" \
    --arg generated_by "$generator" \
    'include "pricing-merge"; merge_pricing($base; .; $today; $now; $generated_by)' \
    "$records"
}

# validate_proposed re-checks schema invariants via the jq filter.
validate_proposed() {
  jq -e -f "${JQ_LIB_DIR}/pricing-validate.jq" "$1" >/dev/null
}

# --- diff + apply prompt ------------------------------------------------

# show_review_diff renders the cur→proposed diff: never reach
# prompt_apply without showing a diff. Prefer git (colored),
# fall back to diff -u, die if neither is available.
show_review_diff() {
  local cur="$1" proposed="$2"
  if command -v git >/dev/null 2>&1; then
    git diff --no-index --color=always -- "$cur" "$proposed" >&2 || :
  elif command -v diff >/dev/null 2>&1; then
    diff -u "$cur" "$proposed" >&2 || :
  else
    die "neither git nor diff is available; cannot show review diff"
  fi
}

prompt_apply() {
  local cur="$1" proposed="$2"
  if [ -f "$cur" ]; then
    log "diff against current pricing.json:"
    show_review_diff "$cur" "$proposed"
  else
    log "no existing $cur — proposed will be created"
  fi
  if [ "$DRY_RUN" = "1" ]; then
    log "dry-run: not writing"
    return 1
  fi
  printf >&2 'apply changes? (yes/no): '
  local answer
  read -r answer || die "no answer given"
  case "$answer" in
    yes|YES|y|Y) return 0 ;;
    *) log "not applied (got '${answer}')"; return 1 ;;
  esac
}

# --- main ---------------------------------------------------------------

main() {
  parse_args "$@"
  require_tools
  # shellcheck source=./lib/pricing-sources.sh disable=SC1091
  . "${JQ_LIB_DIR}/pricing-sources.sh"
  cd "$REPO_ROOT"

  local tmp
  tmp="$(mktemp -d -t ai-viewer-refresh-pricing.XXXXXX)"
  trap 'rm -rf "$tmp"' EXIT

  local seeds="${tmp}/seeds.tsv"
  local litellm_json="${tmp}/litellm.json"
  local or_json="${tmp}/openrouter.json"
  local records="${tmp}/records.jsonl"
  local proposed="${tmp}/proposed.json"

  discover_seed_list "$seeds"
  fetch_sources "$litellm_json" "$or_json"
  expand_add_providers "$seeds" "$litellm_json"
  build_records_from_seeds "$seeds" "$litellm_json" "$or_json" "$records"

  # iter-8 missing-seed gate (lib: pricing-sources.sh). Must fire BEFORE
  # the "no records" guard so partial seed lists are rejected pre-merge.
  enforce_missing_seed_gate

  if [ ! -s "$records" ]; then
    die "no records produced (all ${MISSING_COUNT} seeds missing from sources)"
  fi

  merge_into_existing "$OUT_PATH" "$records" "scripts/refresh-pricing.sh --source=${SOURCE}" > "$proposed"
  validate_or_preserve "$proposed"
  write_proposed "$proposed"
}

main "$@"
