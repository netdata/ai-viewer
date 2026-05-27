#!/usr/bin/env bash
#
# pricing-validate-input.sh — sourced library for
# scripts/refresh-pricing.sh. Holds every CLI / DB / path input
# validator so the entry script stays under the project file-size
# budget. Extracted in iter-4.
#
# All functions assume the caller exports REPO_ROOT, PRICING_JSON_DEFAULT,
# and provides die() / warn() helpers. They DO NOT call exit themselves
# except via die() so the calling script remains in control of error
# flow.
#
# Spec: .agents/sow/specs/pricing.md §"What the script does NOT do".

# validate_name rejects any CLI-supplied provider/model name that
# contains a character outside the safe set. The same regex bounds
# pricing.schema.json's provider/model name patterns. Tab, newline,
# quote, and shell metacharacters are explicitly excluded so a
# malicious or fat-fingered --add-provider / --add-model cannot
# corrupt TSV output, break out of jq string literals, or escape
# bash quoting.
validate_name() {
  local kind="$1" val="$2"
  if [[ ! "$val" =~ ^[a-zA-Z0-9][a-zA-Z0-9._/-]*$ ]]; then
    die "invalid ${kind} name: $(printf %q "$val")"
  fi
}

# sanitize_cli_field strips any character that could corrupt the TSV
# (tab, newline, carriage return) from a CLI-supplied value. Belt-
# and-suspenders defence; validate_name already rejects these but
# this guards against any code path that bypasses validation.
sanitize_cli_field() {
  printf '%s' "$1" | tr -d '\t\n\r'
}

# validate_db_seed_name rejects DB-discovered provider/model names that
# do not match validate_name's pattern. Iter-3 trusted DB-origin
# values; iter-4 closes that gap because a misbehaving adapter
# (historical or third-party) might have inserted a row with
# whitespace, quotes, or other characters that would later fail the
# Go loader's namePattern. Failing at discover time is cheaper than
# producing a JSON the loader refuses.
validate_db_seed_name() {
  local kind="$1" val="$2"
  if [[ ! "$val" =~ ^[a-zA-Z0-9][a-zA-Z0-9._/-]*$ ]]; then
    die "DB seed row has invalid ${kind} name $(printf %q "$val") — refusing to build a pricing.json the Go loader would reject"
  fi
}

# validate_out_path enforces the spec line: the script "never reads
# or modifies any file outside internal/pricing/, a \$TMPDIR scratch,
# and the read-only ingest DB". The resolved OUT_PATH MUST be either
# (a) the default pricing.json under internal/pricing/, or
# (b) somewhere under $TMPDIR (for tests / dry-run experiments).
# Anything else — including any other path inside the repo — is
# rejected. Without this guard an --out=README.md or
# --out=internal/store/store.go would silently clobber unrelated
# files after the operator's "yes" prompt.
validate_out_path() {
  local out_dir abs_out_dir abs_out tmpdir
  out_dir="$(dirname -- "$OUT_PATH")"
  if [ ! -d "$out_dir" ]; then
    die "--out parent directory does not exist: $out_dir"
  fi
  abs_out_dir="$(cd "$out_dir" && pwd -P)"
  abs_out="${abs_out_dir}/$(basename -- "$OUT_PATH")"
  if [ -e "$abs_out" ]; then
    local resolved
    if resolved="$(readlink -f -- "$abs_out" 2>/dev/null)" && [ -n "$resolved" ]; then
      abs_out="$resolved"
    fi
  fi
  tmpdir="${TMPDIR:-/tmp}"
  tmpdir="$(cd "$tmpdir" && pwd -P)"
  local pricing_dir
  pricing_dir="$(cd "$(dirname -- "$PRICING_JSON_DEFAULT")" && pwd -P)"
  case "$abs_out" in
    "$PRICING_JSON_DEFAULT"|"${pricing_dir}/pricing.json")
      OUT_PATH="$abs_out"
      return 0
      ;;
    "${tmpdir}"/*)
      OUT_PATH="$abs_out"
      return 0
      ;;
  esac
  die "--out must resolve to ${PRICING_JSON_DEFAULT} or a path under ${tmpdir} (got ${abs_out})"
}

# validate_db_path verifies --db points to a regular file containing
# a SQLite database. Rejects FIFOs, devices, and broken symlinks so a
# typo or malicious input never reaches sqlite3. The "file" must
# either not exist (the operator may not have created the DB yet —
# handled by the discover step) or be a regular SQLite file with the
# right magic bytes.
validate_db_path() {
  if [ ! -e "$DB_PATH" ]; then
    return 0
  fi
  if [ ! -f "$DB_PATH" ]; then
    die "--db must be a regular file (got non-file: ${DB_PATH})"
  fi
  # SQLite databases start with the 16-byte literal "SQLite format 3\0".
  # `dd` is portable across linux and macos; head -c is GNU-only.
  local magic
  magic="$(dd if="$DB_PATH" bs=1 count=15 2>/dev/null)"
  if [ "$magic" != "SQLite format 3" ]; then
    die "--db is not a SQLite database (magic bytes mismatch): ${DB_PATH}"
  fi
  local resolved
  if resolved="$(readlink -f -- "$DB_PATH" 2>/dev/null)" && [ -n "$resolved" ]; then
    DB_PATH="$resolved"
  fi
}
