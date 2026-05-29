#!/usr/bin/env bash
#
# sanitize-fixture.sh
#
# Strip sensitive content from a real AI-agent session sample (v3 ledger
# or v2 single-gzipped-snapshot) so a sanitized copy can be committed to
# testdata/<adapter>/<scenario>/INPUT/.
#
# Per AGENTS.md "Sensitive Data In Durable Artifacts" and
# .agents/sow/specs/security.md "Sensitive Data In Fixtures", this is the
# first line of defense; the CI secret scanner is the second.
#
# Usage: see --help below.
#
# Determinism: same input + same --id-seed -> byte-identical output.

set -euo pipefail
IFS=$'\n\t'

# --- paths -------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RULES_LIB="${SCRIPT_DIR}/lib/sanitize-rules.jq"

# --- defaults ----------------------------------------------------------------

FORMAT=""
INPUT=""
OUTPUT=""
ID_SEED="1"
DRY_RUN="0"
DIFF_MODE="0"
FORCE="0"

# --- helpers -----------------------------------------------------------------

log()  { printf >&2 '%s\n' "$*"; }
warn() { printf >&2 'sanitize-fixture: WARN: %s\n' "$*"; }
die()  { printf >&2 'sanitize-fixture: ERROR: %s\n' "$*"; exit 1; }

usage() {
  cat <<'USAGE'
sanitize-fixture.sh - strip sensitive content from session fixtures.

Usage:
  scripts/sanitize-fixture.sh --format=<aiagent_v3|aiagent_v2> \
                              --input=<path> --output=<dir> \
                              [--id-seed=<int>] [--dry-run] [--diff] [--force]

Required:
  --format=<NAME>   one of: aiagent_v3, aiagent_v2
  --input=<PATH>    source file or directory:
                    - aiagent_v3: a <sessionId>.jsonl file, OR a directory
                      containing session/<sessionId>.jsonl plus optional
                      payloads/<sessionId>/turn-NNNN/*.gz
                    - aiagent_v2: a <originId>.json.gz file
  --output=<DIR>    output directory (created if missing). The script mirrors
                    the input structure under this directory.

Optional:
  --id-seed=<INT>   integer seed for deterministic UUID mapping. Default: 1.
                    Different seeds produce different (but still consistent)
                    pseudonymous UUIDs.
  --dry-run         report what would be written; do not write anything.
  --diff            print a unified diff of sanitized vs original to stderr
                    (intended for human review before commit).
  --force           overwrite existing output files. Default refuses.
  --help, -h        show this message and exit 0.

Behavior:
  - All UUID-shaped originId / sessionId / parentSessionId values are
    replaced by a deterministic pseudonymous UUID derived from
    sha256("<seed>:<original>"), preserving cross-record linkage.
  - User / assistant / tool / reasoning bodies and free-form log lines are
    wholesale replaced with [REDACTED_...] placeholders.
  - Payload .gz files (raw HTTP/SSE/JSON-RPC bodies) are replaced with the
    single text "[REDACTED_PAYLOAD_BODY]\n" recompressed with gzip.
  - File-path strings under the operator's $HOME, or under any generic home
    root (/home/<user>, /Users/<user>, /root), are rewritten to "<HOME>".
    Redacting generic roots (not just the literal $HOME) keeps sanitized
    fixtures byte-identical regardless of the machine that runs the script.
  - Email addresses are rewritten to "user@example.invalid".
  - API URLs and bearer tokens / classic secret patterns are redacted.

Output:
  Sanitized file paths are printed to stdout (one per line, suitable for
  piping into xargs). All progress / warnings go to stderr.

Exit codes:
  0  success
  1  argument error or runtime failure
  2  input contains data the script could not safely sanitize
USAGE
}

# --- argument parsing --------------------------------------------------------

parse_args() {
  if [ "$#" -eq 0 ]; then
    usage >&2
    die "no arguments"
  fi

  for arg in "$@"; do
    case "$arg" in
      --format=*)   FORMAT="${arg#*=}" ;;
      --input=*)    INPUT="${arg#*=}" ;;
      --output=*)   OUTPUT="${arg#*=}" ;;
      --id-seed=*)  ID_SEED="${arg#*=}" ;;
      --dry-run)    DRY_RUN="1" ;;
      --diff)       DIFF_MODE="1" ;;
      --force)      FORCE="1" ;;
      --help|-h)    usage; exit 0 ;;
      *)            usage >&2; die "unknown argument: $arg" ;;
    esac
  done

  [ -n "$FORMAT" ] || { usage >&2; die "--format is required"; }
  [ -n "$INPUT" ]  || { usage >&2; die "--input is required"; }
  [ -n "$OUTPUT" ] || { usage >&2; die "--output is required"; }

  case "$FORMAT" in
    aiagent_v3|aiagent_v2) ;;
    *) die "--format must be one of: aiagent_v3, aiagent_v2 (got: $FORMAT)" ;;
  esac

  if ! [[ "$ID_SEED" =~ ^[0-9]+$ ]]; then
    die "--id-seed must be a non-negative integer (got: $ID_SEED)"
  fi

  [ -e "$INPUT" ] || die "input not found: $INPUT"
  [ -e "$RULES_LIB" ] || die "rules library not found: $RULES_LIB"
}

# --- environment checks ------------------------------------------------------

require_tools() {
  local missing=""
  for tool in jq gunzip gzip sha256sum diff; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      missing="${missing} ${tool}"
    fi
  done
  if [ -n "$missing" ]; then
    die "missing required tools:${missing}"
  fi
}

# --- deterministic UUID mapping ---------------------------------------------
#
# uuid_for "<original-uuid>" -> "<pseudonymous-uuid>"
#
# Hash the seed and original together with sha256; format the first 32 hex
# digits as a UUID (8-4-4-4-12). Pure function of (ID_SEED, input) — no
# stored state, no order dependence; same inputs always produce the same
# output.

uuid_for() {
  local original="$1"
  # Skip empty / non-UUID strings.
  if [ -z "$original" ]; then
    printf '%s' "$original"
    return 0
  fi
  if ! printf '%s' "$original" \
     | grep -Eq '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
  then
    printf '%s' "$original"
    return 0
  fi
  local hex
  hex="$(printf '%s:%s' "$ID_SEED" "$original" | sha256sum | cut -c1-32)"
  # Format as 8-4-4-4-12.
  printf '%s-%s-%s-%s-%s' \
    "${hex:0:8}" "${hex:8:4}" "${hex:12:4}" "${hex:16:4}" "${hex:20:12}"
}

# Build the jq $id_map argument: an object {<original>: <pseudonymous>}.
# Walks all UUID-shaped strings appearing anywhere in $1 (treated as JSON
# text) and produces the mapping object on stdout.
build_id_map() {
  local source_text="$1"
  printf '%s' "$source_text" \
    | grep -Eoh '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}' \
    | sort -u \
    | while IFS= read -r original; do
        local mapped
        mapped="$(uuid_for "$original")"
        printf '%s\t%s\n' "$original" "$mapped"
      done \
    | jq -R -s '
        split("\n")
        | map(select(length > 0) | split("\t") | {(.[0]): .[1]})
        | add // {}
      '
}

# --- string-level sanitization ----------------------------------------------
#
# Applied to raw text (line for v3 jsonl, full JSON text for v2) AFTER jq
# has done structural redactions. The order matters: do the most specific
# patterns first so a generic secret regex doesn't shadow a URL rewrite.
#
# All substitutions are pure sed; no shell-out beyond that.

sanitize_text() {
  # Read stdin, write to stdout. All rules are POSIX-ERE so the same
  # script works under both GNU and BSD sed.

  # Separator for the HOME substitution must not appear inside $HOME.
  # Forward-slash is universal in $HOME, so use a vertical bar.
  local home_path="${HOME}"

  # $HOME is interpolated as the PATTERN of an ERE sed rule below. Interpolating
  # it raw would let a home path containing ERE metacharacters ([ ] . ( ) { } *
  # + ? ^ $) or the `|` delimiter over/under-redact or break sed entirely. Escape
  # every ERE-special byte and the `|` delimiter with a leading backslash so the
  # path is matched literally. (The replacement side is the constant "<HOME>",
  # which contains no sed-special bytes, so only the pattern needs escaping.)
  local home_path_re
  # Character class lists every ERE-special byte plus the `|` delimiter and a
  # literal backslash; `$` is placed last (not adjacent to `(`) so shellcheck
  # does not misread it as a command substitution inside the single quotes.
  home_path_re="$(printf '%s' "$home_path" | sed 's/[][\.|(){}?+*^$]/\\&/g')"

  sed -E \
    -e 's|Bearer +[A-Za-z0-9._-]+|Bearer [REDACTED_SECRET]|g' \
    -e 's|https?://api\.openai\.com(/[^" '"'"']*)?|https://api.example.invalid/openai\1|g' \
    -e 's|https?://api\.anthropic\.com(/[^" '"'"']*)?|https://api.example.invalid/anthropic\1|g' \
    -e 's|https?://api\.groq\.com(/[^" '"'"']*)?|https://api.example.invalid/groq\1|g' \
    -e 's|https?://openrouter\.ai(/[^" '"'"']*)?|https://api.example.invalid/openrouter\1|g' \
    -e 's|https?://api\.x\.ai(/[^" '"'"']*)?|https://api.example.invalid/xai\1|g' \
    -e 's|https?://api\.deepseek\.com(/[^" '"'"']*)?|https://api.example.invalid/deepseek\1|g' \
    -e 's|https?://api\.mistral\.ai(/[^" '"'"']*)?|https://api.example.invalid/mistral\1|g' \
    -e 's|https?://generativelanguage\.googleapis\.com(/[^" '"'"']*)?|https://api.example.invalid/google\1|g' \
    -e 's|https?://llm\.netdata\.cloud(/[^" '"'"']*)?|https://api.example.invalid/litellm\1|g' \
    -e 's|sk-[A-Za-z0-9_-]{20,}|[REDACTED_SECRET]|g' \
    -e 's|xox[bpas]-[A-Za-z0-9_-]{10,}|[REDACTED_SECRET]|g' \
    -e 's|AKIA[0-9A-Z]{16}|[REDACTED_SECRET]|g' \
    -e 's|ghp_[A-Za-z0-9]{30,}|[REDACTED_SECRET]|g' \
    -e 's|github_pat_[A-Za-z0-9_]{20,}|[REDACTED_SECRET]|g' \
    -e 's|glpat-[A-Za-z0-9_-]{16,}|[REDACTED_SECRET]|g' \
    -e 's@"([Aa][Pp][Ii][_-]?[Kk][Ee][Yy]|[Ss][Ee][Cc][Rr][Ee][Tt]|[Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd]|[Tt][Oo][Kk][Ee][Nn])" *: *"[^"]*"@"\1": "[REDACTED_SECRET]"@g' \
    -e 's|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}|user@example.invalid|g' \
    -e "s|${home_path_re}|<HOME>|g" \
    -e 's|/home/[^/"'"'"' \t]+|<HOME>|g' \
    -e 's|/Users/[^/"'"'"' \t]+|<HOME>|g' \
    -e 's@/root([/"'"'"' \t]|$)@<HOME>\1@g'
}

# Sanitize one JSON object (already parsed-ready text) against the v3 rule
# library. The rules file is included as a jq library module via -L/include.
sanitize_v3_json_line() {
  local id_map_json="$1"
  jq -c \
    --argjson id_map "$id_map_json" \
    -L "$(dirname "$RULES_LIB")" \
    'include "sanitize-rules"; sanitize_v3_record'
}

# Sanitize a v2 JSON document (multi-line allowed) against the v2 rules.
sanitize_v2_json() {
  local id_map_json="$1"
  jq \
    --argjson id_map "$id_map_json" \
    -L "$(dirname "$RULES_LIB")" \
    'include "sanitize-rules"; sanitize_v2_snapshot'
}

# --- output helpers ---------------------------------------------------------

ensure_output_dir() {
  local dir="$1"
  if [ "$DRY_RUN" = "1" ]; then
    return 0
  fi
  mkdir -p "$dir"
}

# write_file <abs_path> <content_var_name>
# Refuses to overwrite an existing file unless --force.
write_file() {
  local path="$1"
  local content="$2"

  if [ "$DRY_RUN" = "1" ]; then
    log "DRY-RUN would write: $path (${#content} bytes)"
    printf '%s\n' "$path"
    return 0
  fi

  if [ -e "$path" ] && [ "$FORCE" != "1" ]; then
    die "output file exists, refusing to overwrite (pass --force to override): $path"
  fi

  ensure_output_dir "$(dirname "$path")"
  printf '%s' "$content" > "$path"
  printf '%s\n' "$path"
}

# write_gzipped_file <abs_path> <plain_content>
write_gzipped_file() {
  local path="$1"
  local content="$2"

  if [ "$DRY_RUN" = "1" ]; then
    log "DRY-RUN would write gzipped: $path"
    printf '%s\n' "$path"
    return 0
  fi

  if [ -e "$path" ] && [ "$FORCE" != "1" ]; then
    die "output file exists, refusing to overwrite (pass --force to override): $path"
  fi

  ensure_output_dir "$(dirname "$path")"
  # gzip -n strips name/mtime headers, which is required for byte-identical
  # determinism across runs.
  printf '%s' "$content" | gzip -n -c > "$path"
  printf '%s\n' "$path"
}

# show_diff <original_text> <sanitized_text> <label>
show_diff() {
  local original="$1"
  local sanitized="$2"
  local label="$3"
  if [ "$DIFF_MODE" != "1" ]; then
    return 0
  fi
  diff -u \
    --label "${label} (original)" \
    --label "${label} (sanitized)" \
    <(printf '%s' "$original") \
    <(printf '%s' "$sanitized") >&2 || true
}

# --- placeholder leak check -------------------------------------------------
#
# If the source already contains [REDACTED_...] placeholders, the operator
# likely ran the script twice; warn but proceed (the rules are idempotent
# so a second pass is a no-op apart from regenerated files).

check_placeholder_leak() {
  local text="$1"
  local label="$2"
  if printf '%s' "$text" | grep -q '\[REDACTED_'; then
    warn "$label already contains [REDACTED_...] markers; input appears to be already-sanitized"
  fi
}

# --- v3 processing ----------------------------------------------------------

# Process a single .jsonl ledger file.
# Args: <input_jsonl> <output_jsonl>
process_v3_ledger() {
  local in_path="$1"
  local out_path="$2"

  if [ ! -s "$in_path" ]; then
    warn "skipping zero-byte ledger: $in_path"
    return 0
  fi

  local raw
  raw="$(cat "$in_path")"
  check_placeholder_leak "$raw" "ledger $in_path"

  # Validate JSON structure line-by-line before mutating anything; this
  # catches malformed input early with a helpful error.
  local lineno=0
  while IFS= read -r line; do
    lineno=$((lineno + 1))
    # Allow blank lines (defensive); they pass through.
    [ -z "$line" ] && continue
    if ! printf '%s' "$line" | jq -e . >/dev/null 2>&1; then
      die "malformed JSON in $in_path at line $lineno"
    fi
  done <<< "$raw"

  # Build the id map across the WHOLE file in one pass — this is what
  # preserves parent->child UUID linkage when parentSessionId references an
  # id introduced earlier in the file.
  local id_map
  id_map="$(build_id_map "$raw")"

  # First pass: jq-structural rewrite per line.
  local sanitized_lines=""
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    local jq_out
    jq_out="$(printf '%s' "$line" | sanitize_v3_json_line "$id_map")"
    sanitized_lines+="${jq_out}"$'\n'
  done <<< "$raw"

  # Second pass: string-level rewrite over the whole jsonl. This catches
  # URLs, bearer tokens, secret patterns, $HOME paths, and emails which are
  # outside the per-field structural rules.
  local sanitized
  sanitized="$(printf '%s' "$sanitized_lines" | sanitize_text)"

  show_diff "$raw" "$sanitized" "$in_path"
  write_file "$out_path" "$sanitized"
}

# Process a single payload .gz file under payloads/<sessionId>/turn-NNNN/.
# The body is wholesale replaced with [REDACTED_PAYLOAD_BODY]\n.
# Args: <input_gz> <output_gz>
process_v3_payload() {
  local in_path="$1"
  local out_path="$2"

  if [ ! -s "$in_path" ]; then
    warn "skipping zero-byte payload: $in_path"
    return 0
  fi

  # Sanity-check: confirm the original is actually gzip (defensive — we
  # decompress only to verify it is well-formed; the resulting bytes are
  # discarded). Use gunzip -t which tests integrity without writing data.
  if ! gunzip -t "$in_path" 2>/dev/null; then
    die "payload is not valid gzip: $in_path"
  fi

  local placeholder="[REDACTED_PAYLOAD_BODY]"$'\n'
  write_gzipped_file "$out_path" "$placeholder"
}

# Map a v3 input path string (relative to the v3 root or absolute) to its
# sanitized counterpart. UUIDs in the path components are remapped using
# the id_map_lookup function so the on-disk layout still cross-references.
remap_path_uuids() {
  local path="$1"
  # Replace any UUID-looking token with its sanitized form. Use a Python-
  # free pure-bash loop over `grep` matches.
  local uuids
  uuids="$(printf '%s' "$path" | grep -Eoh '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}' | sort -u || true)"
  local result="$path"
  if [ -n "$uuids" ]; then
    while IFS= read -r u; do
      [ -z "$u" ] && continue
      local mapped
      mapped="$(uuid_for "$u")"
      # Use bash parameter substitution; safe because UUIDs cannot contain
      # the sed metacharacters and pure-string replacement is what we want.
      result="${result//$u/$mapped}"
    done <<< "$uuids"
  fi
  printf '%s' "$result"
}

process_v3_input() {
  if [ -f "$INPUT" ]; then
    # Single-file mode: must be <sessionId>.jsonl.
    local base
    base="$(basename "$INPUT")"
    case "$base" in
      *.jsonl) ;;
      *) die "aiagent_v3 single-file input must end in .jsonl (got: $base)" ;;
    esac
    local new_base
    new_base="$(remap_path_uuids "$base")"
    local out_path="${OUTPUT%/}/${new_base}"
    process_v3_ledger "$INPUT" "$out_path"
    return 0
  fi

  if [ -d "$INPUT" ]; then
    # Directory mode: expect session/<sessionId>.jsonl and optional
    # payloads/<sessionId>/turn-NNNN/*.gz.
    local session_dir="${INPUT%/}/session"
    local payloads_dir="${INPUT%/}/payloads"
    if [ ! -d "$session_dir" ]; then
      die "aiagent_v3 directory input must contain session/ (got: $INPUT)"
    fi

    # Ledgers.
    local found=0
    while IFS= read -r -d '' ledger; do
      found=$((found + 1))
      local base
      base="$(basename "$ledger")"
      local new_base
      new_base="$(remap_path_uuids "$base")"
      local out_ledger="${OUTPUT%/}/session/${new_base}"
      process_v3_ledger "$ledger" "$out_ledger"
    done < <(find "$session_dir" -maxdepth 1 -type f -name '*.jsonl' -print0 | sort -z)
    if [ "$found" = "0" ]; then
      warn "no .jsonl ledgers under $session_dir"
    fi

    # Payloads (optional).
    if [ -d "$payloads_dir" ]; then
      while IFS= read -r -d '' payload; do
        local rel
        rel="${payload#"$payloads_dir/"}"
        local new_rel
        new_rel="$(remap_path_uuids "$rel")"
        local out_payload="${OUTPUT%/}/payloads/${new_rel}"
        process_v3_payload "$payload" "$out_payload"
      done < <(find "$payloads_dir" -type f -name '*.gz' -print0 | sort -z)
    fi
    return 0
  fi

  die "unsupported input type for aiagent_v3: $INPUT"
}

# --- v2 processing ----------------------------------------------------------

process_v2_input() {
  if [ ! -f "$INPUT" ]; then
    die "aiagent_v2 input must be a single .json.gz file (got: $INPUT)"
  fi
  local base
  base="$(basename "$INPUT")"
  case "$base" in
    *.json.gz) ;;
    *) die "aiagent_v2 input must end in .json.gz (got: $base)" ;;
  esac

  if [ ! -s "$INPUT" ]; then
    warn "skipping zero-byte snapshot: $INPUT"
    return 0
  fi

  # Decompress.
  local raw
  if ! raw="$(gunzip -c "$INPUT" 2>/dev/null)"; then
    die "could not decompress $INPUT (not valid gzip?)"
  fi

  # Validate JSON shape up-front.
  if ! printf '%s' "$raw" | jq -e . >/dev/null 2>&1; then
    die "decompressed v2 snapshot is not valid JSON: $INPUT"
  fi

  check_placeholder_leak "$raw" "snapshot $INPUT"

  # Build id-map across the whole decoded JSON text.
  local id_map
  id_map="$(build_id_map "$raw")"

  # Structural pass.
  local structural
  structural="$(printf '%s' "$raw" | sanitize_v2_json "$id_map")"

  # Text-level pass.
  local sanitized
  sanitized="$(printf '%s' "$structural" | sanitize_text)"

  show_diff "$raw" "$sanitized" "$INPUT"

  # Rename file using mapped UUID.
  local new_base
  new_base="$(remap_path_uuids "$base")"
  local out_path="${OUTPUT%/}/${new_base}"

  write_gzipped_file "$out_path" "$sanitized"
}

# --- entry point ------------------------------------------------------------

main() {
  parse_args "$@"
  require_tools

  if [ "$DRY_RUN" != "1" ] && [ ! -d "$OUTPUT" ]; then
    mkdir -p "$OUTPUT"
  fi

  case "$FORMAT" in
    aiagent_v3) process_v3_input ;;
    aiagent_v2) process_v2_input ;;
  esac
}

main "$@"
