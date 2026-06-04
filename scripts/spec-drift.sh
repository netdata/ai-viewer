#!/usr/bin/env bash
# Spec ↔ code drift detector.
#
# Authoritative gate: .agents/sow/specs/quality-gates.md §"Spec Drift" and the
# runtime companion .agents/skills/project-quality-gates/SKILL.md §"Spec Drift".
# Specs are the assistant's durable memory; silent drift corrupts future SOWs.
# This script lints FIVE structural spec↔code indicators and exits non-zero on
# ANY drift, naming the offending indicator + the specific code/spec token so a
# regression is actionable, not a mystery.
#
# Each indicator is bidirectional (code-not-in-spec AND spec-not-in-code) except
# where a one-direction exemption is documented inline (REST Phase-2 endpoints,
# data-model column prose). The code/spec surfaces are line-oriented and regular
# (route literals, `case "<kind>"` strings, `EventKind = "<value>"` consts, SQL
# CREATE/ALTER, and `format: "<name>"` discovery structs), so grep/awk is
# structurally sufficient — no `go/ast` parse is required. (Decision recorded in
# quality-gates.md §Spec Drift; revisit only if a handler-factory or codegen
# pattern makes a surface non-regular.)
#
# Self-tested by scripts/test/spec-drift-test.sh, which plants a synthetic
# mismatch of EACH indicator class in a throwaway repo copy and asserts the
# detector catches it (exit non-zero, names the offender), plus a clean case
# (exit 0). A gate that cannot prove it still detects can silently rot.
#
# Takes no arguments; resolves the repo root from its own location so it runs
# from any CWD. Contains no secrets and no operator identity.
set -euo pipefail

# Colors for transparent command tracing (mirrors scan-secrets.sh). Collapse to
# empty when stderr is not a TTY so CI logs stay plain.
if [[ -t 2 ]]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; GRAY=$'\033[0;90m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; GRAY=""; NC=""
fi
run() {
  printf >&2 '%s%s >%s ' "$GRAY" "$(pwd)" "$NC"
  printf >&2 '%s' "$YELLOW"; printf >&2 '%q ' "$@"; printf >&2 '%s\n' "$NC"
  local ec=0; "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then printf >&2 '%s[ERROR]%s exit %s: %s\n' "$RED" "$NC" "$ec" "$*"; return "$ec"; fi
}

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

# Authoritative locations (single source so a relocation is one edit here).
PRESENTER_GO="internal/presenter/presenter.go"
PRESENTER_DIR="internal/presenter"
EVENTS_SSE_GO="internal/presenter/events_sse.go"
SUB_FILTER_GO="internal/presenter/subscription_filter.go"
CANONICAL_GO="internal/canonical/events.go"
MIGRATIONS_DIR="internal/store/migrations"
DISCOVERY_GO="cmd/ai-viewer-ingest/sources.go"
SPEC_DIR=".agents/sow/specs"
REST_SPEC="${SPEC_DIR}/rest-api.md"
SSE_SPEC="${SPEC_DIR}/sse-protocol.md"
DATA_SPEC="${SPEC_DIR}/data-model.md"
CANON_SPEC="${SPEC_DIR}/canonical-events.md"

# Deferred REST marker vocabulary. A spec-only REST endpoint is exempt only when
# its own section body contains one of these explicit phrases.
DEFERRED_REST_MARKER_RE='not implemented|not registered|Phase 2'

# drift accumulates one "<indicator>: <detail>" line per finding. Indicators
# never abort early — every class runs so one report lists ALL drift at once.
drift=""
note_drift() { drift="${drift}${1}"$'\n'; }

# contains_line <haystack> <needle> — membership test that is safe under
# `set -o pipefail`. Avoid `printf ... | grep -q`: grep exits early on a match,
# which can SIGPIPE printf and turn a real match into a false drift report.
contains_line() {
  local haystack="$1" needle="$2"
  grep -qxF -- "$needle" <<< "$haystack"
}

# grep_o_or_empty <pattern> [files...] — grep extractor for optional token
# classes. Exit 1 ("no matches") is a valid empty set so the caller can emit its
# own indicator-specific diagnostic; real grep errors (>1) still fail closed.
grep_o_or_empty() {
  local ec=0
  set +e
  grep -hoE "$@"
  ec=$?
  set -e
  case "$ec" in
    0 | 1) return 0 ;;
    *) return "$ec" ;;
  esac
}

# require_file <path> <indicator> — a missing authoritative source is itself
# drift (fail-closed): the gate cannot certify "no drift" against a file it
# could not read.
require_file() {
  local f="$1" ind="$2"
  if [[ ! -f "$f" ]]; then
    note_drift "${ind}: required source file is missing: ${f}"
    return 1
  fi
  return 0
}

# --- Indicator (a): REST endpoints -----------------------------------------
#
# CODE: mux.HandleFunc("/api/…") registrations in presenter.go joined to each
#   handler's in-function r.Method gate. Drop the "/api/" catch-all
#   (notImplemented) and any non-/api route. Normalize Go 1.22 wildcards so both
#   sides compare on one path style.
# SPEC: "### <VERB> /api/…" headings in rest-api.md. Normalize the catalog group
#   "{tools,models,agents}" → :catalog, and single-value wildcards ({id}, :ref)
#   → :id.
# Direction: code→spec is UNCONDITIONAL (a registered route MUST be documented).
#   spec→code EXEMPTS an endpoint whose section is explicitly marked "not
#   registered / Phase 2 / not implemented" — documenting a future route ahead
#   of its handler is allowed (a viewer must not advertise a route it does not
#   serve, but the planned contract may be written down).
normalize_rest_path() {
  sed -E 's#\{tools,models,agents\}#:catalog#g; s#\{[a-zA-Z_]+\}#:id#g; s#:ref#:id#g'
}

collect_handler_method_tokens() {
  local handler="$1"
  local -n out_tokens="$2"
  out_tokens=()
  local src token extracted
  extracted="$(
    for src in "$PRESENTER_DIR"/*.go; do
      [[ -e "$src" ]] || continue
      [[ "$src" == *_test.go ]] && continue
      awk -v fn="$handler" '
        $0 ~ "^func \\(p \\*Presenter\\) " fn "\\(" {infn=1; next}
        infn && /^func / {exit}
        infn && /r\.Method[[:space:]]*!=[[:space:]]*http\.Method/ {print}
      ' "$src"
    done | grep_o_or_empty 'http\.Method[A-Za-z]+' | sort -u
  )"
  while IFS= read -r token; do
    [[ -z "$token" ]] && continue
    out_tokens+=("$token")
  done <<< "$extracted"
}

rest_spec_headings() {
  grep_o_or_empty '^###[[:space:]]+(GET|POST|PUT|DELETE|PATCH)[[:space:]]+/api/[^[:space:]?]+' "$REST_SPEC"
}

extract_rest_registrations() {
  grep_o_or_empty 'mux\.HandleFunc\("/api/[^"]*", p\.[A-Za-z0-9_]+' "$PRESENTER_GO" \
    | sed -E 's#^mux\.HandleFunc\("([^"]*)", p\.([A-Za-z0-9_]+)#\2 \1#'
}

count_deferred_rest_markers() {
  local heading="$1"
  local count_file
  count_file="$(mktemp -t spec-drift-rest-markers.XXXXXX)"

  local -a pipe_statuses
  local awk_status grep_status
  set +e
  awk -v h="$heading" '
    {line=$0; sub(/[[:space:]]+$/, "", line)}
    line==h {inx=1; next}
    inx && /^###[[:space:]]+/ {exit}
    inx {print}
  ' "$REST_SPEC" | grep -ciE "$DEFERRED_REST_MARKER_RE" > "$count_file"
  pipe_statuses=("${PIPESTATUS[@]}")
  set -e
  awk_status="${pipe_statuses[0]}"
  grep_status="${pipe_statuses[1]}"

  if [[ "$awk_status" -ne 0 ]]; then
    rm -f "$count_file"
    return "$awk_status"
  fi
  case "$grep_status" in
    0)
      cat "$count_file"
      rm -f "$count_file"
      return 0
      ;;
    1)
      rm -f "$count_file"
      printf '0\n'
      return 0
      ;;
    *)
      rm -f "$count_file"
      return "$grep_status"
      ;;
  esac
}

check_rest() {
  local ind="rest-endpoints"
  require_file "$PRESENTER_GO" "$ind" || return 0
  require_file "$REST_SPEC" "$ind" || return 0

  local code_routes="" spec_routes registrations
  registrations="$(extract_rest_registrations)"

  local handler raw_path norm_path token verb emitted
  local -a method_tokens
  while read -r handler raw_path; do
    [[ -z "$handler" || -z "$raw_path" ]] && continue
    [[ "$raw_path" == "/api/" ]] && continue
    norm_path="$(normalize_rest_path <<< "$raw_path")"
    collect_handler_method_tokens "$handler" method_tokens
    if [[ "${#method_tokens[@]}" -eq 0 ]]; then
      note_drift "${ind}: handler p.${handler} registered for ${raw_path} has no explicit r.Method gate (method extraction failed — investigate)"
      continue
    fi
    emitted=0
    for token in "${method_tokens[@]}"; do
      [[ -z "$token" ]] && continue
      case "$token" in
        http.MethodGet)    verb="GET" ;;
        http.MethodPost)   verb="POST" ;;
        http.MethodPut)    verb="PUT" ;;
        http.MethodDelete) verb="DELETE" ;;
        http.MethodPatch)  verb="PATCH" ;;
        http.MethodHead)   continue ;;
        *)
          note_drift "${ind}: handler p.${handler} registered for ${raw_path} uses unsupported method token ${token} (method extraction failed — investigate)"
          continue
          ;;
      esac
      code_routes="${code_routes}${verb} ${norm_path}"$'\n'
      emitted=1
    done
    if [[ "$emitted" -eq 0 ]]; then
      note_drift "${ind}: handler p.${handler} registered for ${raw_path} only exposes HEAD, with no documentable REST method (method extraction failed — investigate)"
    fi
  done <<< "$registrations"
  code_routes="$(printf '%s' "$code_routes" | sort -u)"
  if [[ -z "$code_routes" ]]; then
    note_drift "${ind}: no REST verb+path pairs extracted from ${PRESENTER_GO} (extraction failed — investigate)"
  fi

  # Spec headings → normalized "VERB path" pairs. Query strings in headings
  # (e.g. /api/events?sub=:id) document parameters, not distinct mux paths.
  spec_routes="$(
    while read -r heading; do
      [[ -z "$heading" ]] && continue
      printf '%s %s\n' "$(awk '{print $2}' <<< "$heading")" "$(awk '{print $3}' <<< "$heading" | normalize_rest_path)"
    done <<< "$(rest_spec_headings)" | sort -u
  )"

  # code→spec: every registered route must appear in the spec set.
  local r
  while IFS= read -r r; do
    [[ -z "$r" ]] && continue
    if ! contains_line "$spec_routes" "$r"; then
      note_drift "${ind}: route registered in ${PRESENTER_GO} but no '### ${r}' heading in ${REST_SPEC##*/} (code→spec)"
    fi
  done <<< "$code_routes"

  # spec→code: every spec endpoint must be registered, UNLESS its section is
  # marked not-registered/Phase-2. Re-extract spec headings WITH their verb so
  # we can locate each section and inspect its marker.
  local heading path section_markers pair
  while IFS= read -r heading; do
    [[ -z "$heading" ]] && continue
    verb="$(awk '{print $2}' <<< "$heading")"
    path="$(awk '{print $3}' <<< "$heading")"
    norm_path="$(normalize_rest_path <<< "$path")"
    pair="${verb} ${norm_path}"
    # Is this normalized verb+path registered in code?
    if contains_line "$code_routes" "$pair"; then
      continue
    fi
    # Not registered. Allowed ONLY if the spec section is marked deferred. Read
    # the section body from this "### " heading up to the next "### " heading.
    section_markers="$(count_deferred_rest_markers "$heading")"
    if [[ "${section_markers:-0}" -eq 0 ]]; then
      note_drift "${ind}: '${verb} ${path}' documented in ${REST_SPEC##*/} but not registered in ${PRESENTER_GO##*/}, and its section carries no 'not implemented / not registered / Phase 2' marker (spec→code)"
    fi
  done <<< "$(rest_spec_headings)"
}

# --- Indicator (b): SSE event types ----------------------------------------
#
# CODE: the wire kinds the server emits. The authoritative set is the
#   eventPayload switch in events_sse.go (the `case "<kind>"` arms) plus the
#   `default` arm's kinds (stats_invalidated), which are produced/filtered in
#   subscription_filter.go's `case "<kind>"` arms, plus the literal control
#   frames `event: resync` and `: keepalive`.
# SPEC: "### `<type>`" headings under §Event Types in sse-protocol.md, PLUS
#   `resync` which the spec documents under §Reconnect Behavior (a control
#   frame, not an Event-Types heading) — treated as a known control frame.
# Direction: bidirectional. resync/keepalive are control frames present on both
#   sides by construction (keepalive has its own ### heading; resync is the
#   §Reconnect frame), so they never drift.
check_sse() {
  local ind="sse-event-types"
  require_file "$EVENTS_SSE_GO" "$ind" || return 0
  require_file "$SUB_FILTER_GO" "$ind" || return 0
  require_file "$SSE_SPEC" "$ind" || return 0

  # Code kinds: case-arm strings from the two switch sites + the two control
  # frames. `grep -oE 'case "<kind>"'` then strip to the bare kind.
  local code_kinds
  code_kinds="$( {
      grep_o_or_empty 'case "[a-z_]+"' "$EVENTS_SSE_GO" "$SUB_FILTER_GO" | grep_o_or_empty '"[a-z_]+"' | tr -d '"'
      grep_o_or_empty 'event: resync' "$EVENTS_SSE_GO" | sed 's/event: //'
      grep_o_or_empty ': keepalive' "$EVENTS_SSE_GO" | sed 's/: //'
    } | sort -u )"
  if [[ -z "$code_kinds" ]]; then
    note_drift "${ind}: no SSE wire kinds found in ${EVENTS_SSE_GO} / ${SUB_FILTER_GO} (extraction failed — investigate)"
    return 0
  fi

  # Spec kinds: ### `<type>` headings + the resync frame mention.
  local spec_kinds
  # The backticks in the grep pattern below are LITERAL markdown backticks
  # inside a single-quoted string (no shell expansion happens), so SC2016's
  # "expressions don't expand in single quotes" warning is a false positive —
  # single quotes are exactly what makes the backticks literal here.
  # shellcheck disable=SC2016
  spec_kinds="$( {
      grep_o_or_empty '^### `[a-z_]+`' "$SSE_SPEC" | tr -d '`' | awk '{print $2}'
      grep_o_or_empty 'event: resync' "$SSE_SPEC" | sed 's/event: //'
    } | sort -u )"
  if [[ -z "$spec_kinds" ]]; then
    note_drift "${ind}: no SSE event headings or control-frame mentions found in ${SSE_SPEC} (extraction failed — investigate)"
    return 0
  fi

  local k
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    if ! contains_line "$spec_kinds" "$k"; then
      note_drift "${ind}: wire kind '${k}' emitted in code but not documented in ${SSE_SPEC##*/} (code→spec)"
    fi
  done <<< "$code_kinds"
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    if ! contains_line "$code_kinds" "$k"; then
      note_drift "${ind}: event type '${k}' documented in ${SSE_SPEC##*/} but not emitted in code (spec→code)"
    fi
  done <<< "$spec_kinds"
}

# --- Indicator (c): SQLite columns -----------------------------------------
#
# CODE: every table + column name across migrations/*.sql — CREATE TABLE
#   columns, CREATE VIRTUAL TABLE … USING fts5(…) tokens, and
#   ALTER TABLE … ADD COLUMN. Aggregated across ALL migrations so a column added
#   by a later ALTER is part of its table's set.
# SPEC: table.column pairs extracted from SQL schema blocks in data-model.md.
# Direction: COLUMN pairs are checked code→spec only (every migration
#   table.column pair must be present in the corresponding data-model.md SQL
#   block — the direction that catches the real "added a column to a table,
#   forgot to document it" drift). The reverse is intentionally not enforced as
#   drift: the spec legitimately names many column identifiers in prose (joins,
#   synthetic columns, examples) and a spec→code column sweep would be all false
#   positives. TABLE names ARE bidirectional (a migration table must be
#   documented, and a `### <table>` schema heading must be a real migration
#   table).
extract_sql_column_pairs() {
  local fenced_only="$1"
  shift
  awk -v fenced_only="$fenced_only" '
    function trim(s) {
      gsub(/\r/, "", s)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
      return s
    }
    function strip_comment(s) {
      sub(/--.*/, "", s)
      return s
    }
    function fail(msg) {
      printf "spec-drift SQL extractor: %s:%s: %s\n", stmt_file, stmt_line, msg > "/dev/stderr"
      failed=1
    }
    function emit(col) {
      if (table != "" && col ~ /^[a-z_][a-z_0-9]*$/) {
        print table "." col
        emitted_count++
      }
    }
    function split_top_level(body, parts,    i, ch, depth, part, n) {
      n=0
      part=""
      depth=0
      for (i = 1; i <= length(body); i++) {
        ch=substr(body, i, 1)
        if (ch == "(") {
          depth++
        } else if (ch == ")" && depth > 0) {
          depth--
        }
        if (ch == "," && depth == 0) {
          n++
          parts[n]=trim(part)
          part=""
          continue
        }
        part=part ch
      }
      if (trim(part) != "") {
        n++
        parts[n]=trim(part)
      }
      return n
    }
    function statement_body(stmt,    body) {
      body=stmt
      sub(/^[^(]*\(/, "", body)
      sub(/\)[[:space:]]*[^)]*$/, "", body)
      return body
    }
    function parse_create_column(def, is_fts,    parts, col) {
      def=trim(def)
      if (def == "") {
        return
      }
      if (def ~ /^(CONSTRAINT|PRIMARY|FOREIGN|UNIQUE|CHECK|KEY)([[:space:]]|[(])/) {
        return
      }
      if (is_fts && def ~ /=/) {
        return
      }
      split(def, parts, /[[:space:]]+/)
      col=parts[1]
      if (col ~ /^[a-z_][a-z_0-9]*$/) {
        emit(col)
        return
      }
      fail("could not extract column identifier from column definition: " def)
    }
    function parse_create_statement(stmt, is_fts,    fields, header, body, parts, count, i, emitted, before_emitted) {
      if (is_fts) {
        if (stmt !~ /^CREATE VIRTUAL TABLE [a-z_][a-z_0-9]*[[:space:]]+USING[[:space:]]+fts5[[:space:]]*\(/) {
          fail("unsupported or malformed CREATE VIRTUAL TABLE statement")
          return
        }
        header=stmt
        sub(/^CREATE VIRTUAL TABLE /, "", header)
        sub(/[[:space:]]+USING.*/, "", header)
      } else {
        if (stmt !~ /^CREATE TABLE [a-z_][a-z_0-9]*[[:space:]]*\(/) {
          fail("unsupported or malformed CREATE TABLE statement")
          return
        }
        header=stmt
        sub(/^CREATE TABLE /, "", header)
        sub(/[[:space:]]*\(.*/, "", header)
      }
      split(header, fields, /[[:space:]]+/)
      table=fields[1]
      if (table !~ /^[a-z_][a-z_0-9]*$/) {
        fail("could not extract table name from CREATE statement")
        table=""
        return
      }
      body=statement_body(stmt)
      if (body == stmt || trim(body) == "") {
        fail("could not extract column list from CREATE statement")
        table=""
        return
      }
      count=split_top_level(body, parts)
      emitted=0
      for (i = 1; i <= count; i++) {
        before_emitted=emitted_count
        parse_create_column(parts[i], is_fts)
        if (emitted_count > before_emitted) {
          emitted=1
        }
      }
      if (!emitted) {
        fail("no columns extracted from CREATE statement for table " table)
      }
      table=""
    }
    function parse_alter_add_column(stmt, parts, tbl, col) {
      if (stmt !~ /^ALTER TABLE [a-z_][a-z_0-9]*[[:space:]]+ADD COLUMN[[:space:]]+[a-z_][a-z_0-9]*/) {
        return
      }
      tbl=stmt
      sub(/^ALTER TABLE /, "", tbl)
      sub(/[[:space:]]+ADD COLUMN.*/, "", tbl)
      col=stmt
      sub(/^ALTER TABLE [a-z_][a-z_0-9]*[[:space:]]+ADD COLUMN[[:space:]]+/, "", col)
      split(col, parts, /[[:space:]]+/)
      if (tbl ~ /^[a-z_][a-z_0-9]*$/) {
        table=tbl
        emit(parts[1])
        table=""
      }
    }
    function parse_statement(stmt) {
      stmt=trim(stmt)
      gsub(/[[:space:]]+/, " ", stmt)
      if (stmt == "") {
        return
      }
      if (stmt ~ /^CREATE VIRTUAL TABLE /) {
        parse_create_statement(stmt, 1)
        return
      }
      if (stmt ~ /^CREATE TABLE /) {
        parse_create_statement(stmt, 0)
        return
      }
      if (stmt ~ /^ALTER TABLE /) {
        parse_alter_add_column(stmt)
      }
    }
    function starts_tracked_statement(stmt) {
      stmt=trim(stmt)
      return stmt ~ /^(CREATE (VIRTUAL )?TABLE|ALTER TABLE)[[:space:]]/
    }
    function append_sql(line,    pos, part) {
      if (stmt == "") {
        stmt_file=FILENAME
        stmt_line=FNR
      }
      stmt=stmt (stmt == "" ? "" : " ") line
      while ((pos = index(stmt, ";")) > 0) {
        part=substr(stmt, 1, pos - 1)
        parse_statement(part)
        stmt=trim(substr(stmt, pos + 1))
        if (stmt != "") {
          stmt_file=FILENAME
          stmt_line=FNR
        }
      }
    }
    function close_file_state() {
      if (stmt != "") {
        if (starts_tracked_statement(stmt)) {
          fail("unterminated tracked SQL statement")
        }
        stmt=""
      }
      if (fenced_only == "1" && in_sql) {
        fail("unterminated sql fence")
        in_sql=0
      }
    }
    {
      if (current_file != FILENAME) {
        if (current_file != "") {
          close_file_state()
        }
        current_file=FILENAME
        stmt=""
        in_sql=0
      }
      raw=$0
      if (fenced_only == "1") {
        if (raw ~ /^```sql[[:space:]]*$/) {
          in_sql=1
          next
        }
        if (in_sql && raw ~ /^```[[:space:]]*$/) {
          if (stmt != "") {
            if (starts_tracked_statement(stmt)) {
              fail("unterminated tracked SQL statement before closing sql fence")
            }
            stmt=""
          }
          in_sql=0
          next
        }
        if (!in_sql) {
          next
        }
      }
      line=trim(strip_comment(raw))
      if (line == "") {
        next
      }
      append_sql(line)
    }
    END {
      close_file_state()
      exit failed
    }
  ' "$@" | sort -u
}

check_data_model() {
  local ind="data-model-columns"
  require_file "$DATA_SPEC" "$ind" || return 0
  if [[ ! -d "$MIGRATIONS_DIR" ]]; then
    note_drift "${ind}: required migrations dir is missing: ${MIGRATIONS_DIR}"
    return 0
  fi
  local migration_files=("$MIGRATIONS_DIR"/*.sql)
  if [[ ! -e "${migration_files[0]}" ]]; then
    note_drift "${ind}: no migration SQL files found in ${MIGRATIONS_DIR}"
    return 0
  fi

  # Table names (CREATE [VIRTUAL] TABLE <name>).
  local mig_tables
  mig_tables="$(grep_o_or_empty 'CREATE (VIRTUAL )?TABLE [a-z_][a-z_0-9]*' "${migration_files[@]}" \
    | awk '{print $NF}' | sort -u)"
  if [[ -z "$mig_tables" ]]; then
    note_drift "${ind}: no CREATE TABLE statements found in ${MIGRATIONS_DIR}/ (extraction failed — investigate)"
    return 0
  fi

  local mig_column_pairs spec_column_pairs
  mig_column_pairs="$(extract_sql_column_pairs 0 "${migration_files[@]}")"
  spec_column_pairs="$(extract_sql_column_pairs 1 "$DATA_SPEC")"

  # Table names → bidirectional.
  local t
  while IFS= read -r t; do
    [[ -z "$t" ]] && continue
    if ! grep -qw "$t" "$DATA_SPEC"; then
      note_drift "${ind}: table '${t}' created in ${MIGRATIONS_DIR}/ but not mentioned in ${DATA_SPEC##*/} (code→spec)"
    fi
  done <<< "$mig_tables"
  # Spec → code for table NAMES: a `### <name>` schema heading whose name is a
  # plausible table identifier (snake_case) but is not a real migration table.
  local h
  while IFS= read -r h; do
    [[ -z "$h" ]] && continue
    if ! contains_line "$mig_tables" "$h"; then
      note_drift "${ind}: '### ${h}' schema heading in ${DATA_SPEC##*/} has no matching CREATE TABLE in ${MIGRATIONS_DIR}/ (spec→code)"
    fi
  done <<< "$(grep_o_or_empty '^### [a-z_][a-z_0-9]+[[:space:]]*$' "$DATA_SPEC" | awk '{print $2}' | sort -u)"

  # Columns → code→spec only, scoped by owning table.
  local pair
  while IFS= read -r pair; do
    [[ -z "$pair" ]] && continue
    if ! contains_line "$spec_column_pairs" "$pair"; then
      note_drift "${ind}: column pair '${pair}' present in ${MIGRATIONS_DIR}/ but not documented in the matching ${DATA_SPEC##*/} SQL schema block (code→spec)"
    fi
  done <<< "$mig_column_pairs"
}

# --- Indicator (d): canonical event kinds ----------------------------------
#
# CODE: the `EvXxx EventKind = "<value>"` discriminator constants in events.go.
# SPEC: the identical fenced block in canonical-events.md.
# Direction: bidirectional, exact (the value set must match byte-for-byte).
check_canonical() {
  local ind="canonical-event-kinds"
  require_file "$CANONICAL_GO" "$ind" || return 0
  require_file "$CANON_SPEC" "$ind" || return 0

  local code_kinds spec_kinds
  code_kinds="$(grep_o_or_empty 'EventKind = "[a-z_]+"' "$CANONICAL_GO" | grep_o_or_empty '"[a-z_]+"' | tr -d '"' | sort -u)"
  spec_kinds="$(grep_o_or_empty 'EventKind = "[a-z_]+"' "$CANON_SPEC" | grep_o_or_empty '"[a-z_]+"' | tr -d '"' | sort -u)"

  if [[ -z "$code_kinds" ]]; then
    note_drift "${ind}: no 'EventKind = \"…\"' constants found in ${CANONICAL_GO} (extraction failed — investigate)"
    return 0
  fi
  if [[ -z "$spec_kinds" ]]; then
    note_drift "${ind}: no 'EventKind = \"…\"' constants found in ${CANON_SPEC} (extraction failed — investigate)"
    return 0
  fi
  local k
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    if ! contains_line "$spec_kinds" "$k"; then
      note_drift "${ind}: kind '${k}' defined in ${CANONICAL_GO##*/} but not in ${CANON_SPEC##*/} (code→spec)"
    fi
  done <<< "$code_kinds"
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    if ! contains_line "$code_kinds" "$k"; then
      note_drift "${ind}: kind '${k}' documented in ${CANON_SPEC##*/} but not defined in ${CANONICAL_GO##*/} (spec→code)"
    fi
  done <<< "$spec_kinds"
}

# --- Indicator (e): adapter discovery probes -------------------------------
#
# CODE: the `format: "<name>"` probe structs in the autoDiscoverSources probe
#   list (sources.go). Each is an adapter the binary auto-discovers.
# SPEC: a `specs/adapter-<name>.md` must EXIST (format→file maps '_' → '-':
#   aiagent_v3 → adapter-aiagent-v3.md, claude-code → adapter-claude-code.md)
#   AND that spec must mention the adapter's default probe path (a basename
#   anchor derived per format) so the documented default cannot silently drift
#   from the probe.
# Direction: code→spec (every discovered format needs a documented spec). The
#   reverse (an adapter-*.md with no probe) is not drift: the adapter-contract.md
#   has no probe, and a spec may precede a discovery probe.
check_adapters() {
  local ind="adapter-probes"
  require_file "$DISCOVERY_GO" "$ind" || return 0

  local formats
  formats="$(grep_o_or_empty 'format: +"[a-z0-9_-]+"' "$DISCOVERY_GO" | grep_o_or_empty '"[a-z0-9_-]+"' | tr -d '"' | sort -u)"
  if [[ -z "$formats" ]]; then
    note_drift "${ind}: no 'format: \"…\"' probe structs found in ${DISCOVERY_GO} (extraction failed — investigate)"
    return 0
  fi

  # Per-format default probe-path basename anchor (the literal the spec must
  # mention). These mirror cmd/ai-viewer-ingest/discovery.go's resolvers; a
  # format with no known anchor only requires the spec file to exist.
  probe_anchor() {
    case "$1" in
      aiagent_v3|aiagent_v2) printf '%s' ".ai-agent/sessions" ;;
      claude-code)           printf '%s' ".claude/projects" ;;
      codex)                 printf '%s' ".codex/sessions" ;;
      opencode)              printf '%s' "opencode.db" ;;
      *)                     printf '%s' "" ;;
    esac
  }

  local fmt specfile anchor
  while IFS= read -r fmt; do
    [[ -z "$fmt" ]] && continue
    specfile="${SPEC_DIR}/adapter-${fmt//_/-}.md"
    if [[ ! -f "$specfile" ]]; then
      note_drift "${ind}: discovery probes format '${fmt}' (${DISCOVERY_GO##*/}) but ${specfile} does not exist (code→spec)"
      continue
    fi
    anchor="$(probe_anchor "$fmt")"
    if [[ -n "$anchor" ]] && ! grep -qF "$anchor" "$specfile"; then
      note_drift "${ind}: ${specfile##*/} does not mention the '${fmt}' default probe path anchor '${anchor}' (code→spec)"
    fi
  done <<< "$formats"
}

# --- main -------------------------------------------------------------------

check_rest
check_sse
check_data_model
check_canonical
check_adapters

if [[ -n "${drift//[$'\n\t ']/}" ]]; then
  printf >&2 '%s[FAIL]%s spec ↔ code drift detected:\n' "$RED" "$NC"
  printf '%s' "$drift" | grep -v '^$' >&2
  printf >&2 '%sFix the spec or the code so they agree — do NOT weaken an indicator. See quality-gates.md §Spec Drift.%s\n' "$RED" "$NC"
  exit 1
fi

printf '%s[PASS]%s no spec ↔ code drift across all 5 indicators (rest, sse, data-model, canonical, adapter-probes).\n' "$GREEN" "$NC"
