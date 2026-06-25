#!/usr/bin/env bash
#
# spec-drift-test.sh
#
# Hermetic self-test for scripts/spec-drift.sh. The drift detector is a quality
# gate; a gate that cannot prove it still detects can silently rot. This harness
# builds a THROWAWAY copy of the repo's relevant code + spec files (the detector
# resolves its inputs at fixed repo-relative paths, so it must run inside a tree
# whose contents we control), then for EACH of the six indicator classes plants
# synthetic mismatches covering every unidirectional indicator and both sides of
# every bidirectional indicator, then asserts the detector:
#
#   - exits non-zero, AND
#   - names the offending indicator + token in its report.
#
# Plus a CLEAN case: the unmodified copy exits 0. The synthetic copy is built
# from the REAL files so the clean case proves the detector agrees with the
# live tree's actual spec↔code state (the same thing `bash scripts/spec-drift.sh`
# asserts on the real repo), and each mutation proves one indicator/direction
# fires.
#
# Nothing is planted in the real tree: every mutation happens in a per-case
# fresh copy under a mktemp dir, removed on exit.
#
# Contains no secrets and no operator identity.
set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DETECTOR="${REPO_ROOT}/scripts/spec-drift.sh"
TMP_ROOT="$(mktemp -d -t spec-drift-test-XXXXXX)"
trap 'rm -rf "$TMP_ROOT"' EXIT

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
contains_text() {
  local haystack="$1" needle="$2"
  grep -qF -- "$needle" <<< "$haystack"
}

# The exact subset of non-presenter files the detector reads. Presenter sources
# are copied as a required directory below because REST method extraction joins
# mux registrations to handler method gates spread across presenter/*.go.
DETECTOR_INPUTS=(
  "scripts/spec-drift.sh"
  "scripts/check-contract-matrix.sh"
  "scripts/lib/check-contract-matrix/main.go"
  "go.mod"
  "internal/canonical/events.go"
  "cmd/ai-viewer-ingest/sources.go"
  "frontend/src/api/types.ts"
  "frontend/src/api/payloads.ts"
  "frontend/src/viz/trace.ts"
  "testdata/contracts/field-matrix.yaml"
  ".agents/sow/specs/rest-api.md"
  ".agents/sow/specs/sse-protocol.md"
  ".agents/sow/specs/data-model.md"
  ".agents/sow/specs/canonical-events.md"
)

matrix_test_refs() {
  awk -F': ' '
    /^[[:space:]]+test_refs:/ {
      v=$2
      gsub(/^"/, "", v)
      gsub(/"$/, "", v)
      n=split(v, parts, ",")
      for (i=1; i<=n; i++) {
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", parts[i])
        if (parts[i] != "") print parts[i]
      }
    }
  ' "$1" | sort -u
}

# new_fixture -> prints a fresh throwaway repo-root copy containing the detector
# inputs (real content) plus the migrations dir and adapter spec files the
# detector globs/stats. The detector resolves REPO_ROOT from its own path
# (scripts/spec-drift.sh -> ..), so it operates entirely within this copy.
new_fixture() {
  local dir="${TMP_ROOT}/fix.$RANDOM.$RANDOM"
  mkdir -p "$dir"
  local rel
  for rel in "${DETECTOR_INPUTS[@]}"; do
    mkdir -p "$dir/$(dirname "$rel")"
    cp "${REPO_ROOT}/${rel}" "$dir/$rel"
  done
  local presenter_sources=()
  local presenter_non_tests=()
  local migration_files=()
  local adapter_specs=()
  local src
  shopt -s nullglob
  presenter_sources=("${REPO_ROOT}"/internal/presenter/*.go)
  migration_files=("${REPO_ROOT}"/internal/store/migrations/*.sql)
  adapter_specs=("${REPO_ROOT}"/.agents/sow/specs/adapter-*.md)
  shopt -u nullglob

  for src in "${presenter_sources[@]}"; do
    [[ "$src" == *_test.go ]] && continue
    presenter_non_tests+=("$src")
  done
  if [ "${#presenter_non_tests[@]}" -eq 0 ]; then
    printf 'FATAL: no presenter source files found for spec-drift fixture: internal/presenter/*.go\n' >&2
    exit 1
  fi
  if [ "${#migration_files[@]}" -eq 0 ]; then
    printf 'FATAL: no migration SQL files found for spec-drift fixture: internal/store/migrations/*.sql\n' >&2
    exit 1
  fi
  if [ "${#adapter_specs[@]}" -eq 0 ]; then
    printf 'FATAL: no adapter spec files found for spec-drift fixture: .agents/sow/specs/adapter-*.md\n' >&2
    exit 1
  fi

  mkdir -p "$dir/internal/presenter"
  cp "${presenter_non_tests[@]}" "$dir/internal/presenter/"
  # migrations/*.sql (globbed) and adapter-*.md (stat'd) are required fixture
  # inputs. Missing matches fail closed above instead of truncating the fixture.
  mkdir -p "$dir/internal/store/migrations"
  cp "${migration_files[@]}" "$dir/internal/store/migrations/"
  cp "${adapter_specs[@]}" "$dir/.agents/sow/specs/"

  # The contract-matrix indicator validates that every matrix test_refs path
  # exists. Touch refs inside the fixture so this self-test isolates drift
  # detection behavior instead of depending on SOW-in-progress test files.
  while IFS= read -r ref; do
    [[ -z "$ref" ]] && continue
    mkdir -p "$dir/$(dirname "$ref")"
    if [[ -f "$REPO_ROOT/$ref" ]]; then
      cp "$REPO_ROOT/$ref" "$dir/$ref"
    else
      : > "$dir/$ref"
    fi
  done < <(matrix_test_refs "$dir/testdata/contracts/field-matrix.yaml")

  chmod +x "$dir/scripts/spec-drift.sh"
  chmod +x "$dir/scripts/check-contract-matrix.sh"
  printf '%s' "$dir"
}

# run_detector <fixture-root> -> sets RC and OUT (combined stdout+stderr).
run_detector() {
  RC=0
  OUT="$( ( cd "$1" && ./scripts/spec-drift.sh ) 2>&1 )" || RC=$?
}

# assert_drift <name> <fixture> <indicator-substring> — the detector must exit
# non-zero AND its report must contain <indicator-substring>.
assert_drift() {
  local name="$1" fix="$2" needle="$3"
  run_detector "$fix"
  if [ "$RC" -eq 0 ]; then
    fail_case "$name" "detector exited 0 but a ${needle} mismatch was planted:
$OUT"; return
  fi
  if ! contains_text "$OUT" "$needle"; then
    fail_case "$name" "non-zero exit but report did not name '${needle}':
$OUT"; return
  fi
  pass_case "$name"
}

# --- cases ------------------------------------------------------------------

# 0. CLEAN: an unmutated copy of the real files exits 0 (the detector agrees
#    with the live tree, exactly as on the real repo).
case_clean_passes() {
  local name="clean::unmodified_copy_passes"
  local fix; fix="$(new_fixture)"
  run_detector "$fix"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "clean copy of the real tree was flagged as drift:
$OUT"; return
  fi
  if ! contains_text "$OUT" "[PASS]"; then
    fail_case "$name" "clean copy exited 0 but printed no [PASS] summary:
$OUT"; return
  fi
  pass_case "$name"
}

# (a) REST: register a brand-new route in code with no spec heading -> code→spec
#     drift on rest-endpoints.
case_rest_code_not_in_spec() {
  local name="rest::code_route_not_in_spec"
  local fix; fix="$(new_fixture)"
  # Inject a new HandleFunc line next to the existing health route.
  sed -i 's#\(mux\.HandleFunc("/api/health", p\.handleHealth)\)#\1\n\tmux.HandleFunc("/api/phantom", p.handlePhantom)#' \
    "$fix/internal/presenter/presenter.go"
  assert_drift "$name" "$fix" "rest-endpoints"
}

# (a') REST: a spec endpoint that is NOT registered and NOT marked Phase-2 ->
#      spec→code drift (proves the Phase-2 exemption is conditional, not blanket).
case_rest_spec_not_in_code_unmarked() {
  local name="rest::spec_endpoint_unmarked_not_registered"
  local fix; fix="$(new_fixture)"
  # Append a normal endpoint heading with NO deferral marker in its body.
  cat >> "$fix/.agents/sow/specs/rest-api.md" <<'EOF'

### GET /api/ghostly

Returns the ghostly resource. (No Phase-2 marker on purpose.)
EOF
  assert_drift "$name" "$fix" "rest-endpoints"
}

# (a'') REST: mutate a handler's method gate while keeping the documented path
#       unchanged. A path-only detector misses this; the verb+path detector must
#       report GET /api/subscriptions as undocumented and/or POST as unregistered.
case_rest_method_mismatch() {
  local name="rest::method_mismatch"
  local fix; fix="$(new_fixture)"
  sed -i 's#r.Method != http.MethodPost#r.Method != http.MethodGet#' \
    "$fix/internal/presenter/subscriptions.go"
  assert_drift "$name" "$fix" "GET /api/subscriptions"
}

# (a''') REST: neutralize the method guard while leaving the route registered.
#        Method extraction failure must fail closed in check_rest's main shell,
#        not disappear inside command substitution.
case_rest_missing_method_gate() {
  local name="rest::missing_method_gate_fail_closed"
  local fix; fix="$(new_fixture)"
  sed -i '0,/if r\.Method != http\.MethodPost {/s//if false {/' \
    "$fix/internal/presenter/subscriptions.go"
  assert_drift "$name" "$fix" "has no explicit r.Method gate"
}

# (a'''') REST: neutralize every /api/ mux registration in the fixture. Grep
#         no-match must become an empty registration set that reaches check_rest's
#         own fail-closed diagnostic, not a masked success or a raw grep exit.
case_rest_no_registrations_extracted() {
  local name="rest::no_registrations_extracted_fail_closed"
  local fix; fix="$(new_fixture)"
  sed -i 's#mux\.HandleFunc("/api/#mux.HandleFunc("/neutralized-api/#g' \
    "$fix/internal/presenter/presenter.go"
  assert_drift "$name" "$fix" "no REST verb+path pairs extracted"
}

# (a''''') REST control: a spec endpoint that IS marked Phase-2 must NOT drift even
#         though it is unregistered — proves the exemption actually exempts.
case_rest_phase2_exempt() {
  local name="rest::phase2_marked_endpoint_exempt"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/.agents/sow/specs/rest-api.md" <<'EOF'

### GET /api/futurething

**Phase 2 — not implemented in Phase 1.** The route is not registered (returns a
structured NOT_FOUND today). Planned contract below.
EOF
  run_detector "$fix"
  if [ "$RC" -ne 0 ]; then
    fail_case "$name" "a Phase-2-marked unregistered endpoint was flagged as drift (exemption broken):
$OUT"; return
  fi
  pass_case "$name"
}

# (b) SSE: document a bogus event type heading with no code emitter -> spec→code
#     drift on sse-event-types.
case_sse_spec_not_in_code() {
  local name="sse::spec_event_not_in_code"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/.agents/sow/specs/sse-protocol.md" <<'EOF'

### `phantom_event`

A documented event that no code path emits.
EOF
  assert_drift "$name" "$fix" "sse-event-types"
}

# (b') SSE: emit a bogus event kind in code with no spec heading -> code→spec
#      drift on sse-event-types.
case_sse_code_not_in_spec() {
  local name="sse::code_event_not_in_spec"
  local fix; fix="$(new_fixture)"
  sed -i 's#\(case "source_status_changed":\)#case "phantom_event":\n\t\tv = map[string]any{"ts": ev.TS}\n\t\1#' \
    "$fix/internal/presenter/events_sse.go"
  assert_drift "$name" "$fix" "phantom_event"
}

# (b'') SSE: erase all code-side wire-kind extraction anchors. The detector must
#        report the extractor failure as sse-event-types drift instead of exiting
#        early under pipefail before the diagnostic is emitted.
case_sse_code_extractor_empty() {
  local name="sse::code_extractor_empty_fail_closed"
  local fix; fix="$(new_fixture)"
  sed -i -E 's/case "[a-z_]+":/case ev.Kind:/g; s/event: resync/event: /g; s/: keepalive/: /g' \
    "$fix/internal/presenter/events_sse.go" \
    "$fix/internal/presenter/subscription_filter.go"
  assert_drift "$name" "$fix" "no SSE wire kinds found"
}

# (b''') SSE: erase all spec-side event headings/control-frame anchors. The
#         detector must keep control and report an actionable spec extractor
#         failure rather than aborting inside command substitution.
case_sse_spec_extractor_empty() {
  local name="sse::spec_extractor_empty_fail_closed"
  local fix; fix="$(new_fixture)"
  # shellcheck disable=SC2016 # Literal markdown backticks in the heading pattern.
  sed -i -E 's/^### `[a-z_]+`/### Event Type Removed/g; s/event: resync/event: /g' \
    "$fix/.agents/sow/specs/sse-protocol.md"
  assert_drift "$name" "$fix" "no SSE event headings"
}

# (c) data-model: add a column to a migration that the spec never mentions ->
#     code→spec drift on data-model-columns.
case_data_model_column_not_in_spec() {
  local name="data-model::migration_column_not_in_spec"
  local fix; fix="$(new_fixture)"
  # Add a new ALTER ADD COLUMN with a clearly-undocumented name.
  printf '\nALTER TABLE sources ADD COLUMN phantomcolumnxyz INTEGER NOT NULL DEFAULT 0;\n' \
    >> "$fix/internal/store/migrations/0007_fts5_index_logs.sql"
  assert_drift "$name" "$fix" "phantomcolumnxyz"
}

# (c scoped) data-model: add a column to one table whose bare name is already
#            documented under other tables. A global-name detector misses this;
#            the table-scoped detector must report the owning table.column pair.
case_data_model_common_column_wrong_table() {
  local name="data-model::common_column_wrong_table"
  local fix; fix="$(new_fixture)"
  printf '\nALTER TABLE sources ADD COLUMN provider TEXT;\n' \
    >> "$fix/internal/store/migrations/0007_fts5_index_logs.sql"
  assert_drift "$name" "$fix" "sources.provider"
}

# (c state) data-model: two valid CREATE TABLE statements on one physical line,
#           with the second table documented but missing one migration column.
#           A line-state parser misses this class; the detector must parse by
#           completed SQL statement and report the table-scoped column pair.
case_data_model_statement_boundary_create() {
  local name="data-model::statement_boundary_create"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/internal/store/migrations/0007_fts5_index_logs.sql" <<'EOF'

CREATE TABLE parser_state_one (id TEXT PRIMARY KEY NOT NULL); CREATE TABLE parser_state_two (id TEXT PRIMARY KEY NOT NULL, parser_missing_column TEXT NOT NULL);
EOF
  cat >> "$fix/.agents/sow/specs/data-model.md" <<'EOF'

### parser_state_one

```sql
CREATE TABLE parser_state_one (
    id TEXT PRIMARY KEY NOT NULL
);
```

### parser_state_two

```sql
CREATE TABLE parser_state_two (
    id TEXT PRIMARY KEY NOT NULL
);
```
EOF
  assert_drift "$name" "$fix" "parser_state_two.parser_missing_column"
}

# (c extractor) data-model: erase every migration CREATE TABLE / CREATE VIRTUAL
#               TABLE anchor. The detector must report an actionable extraction
#               failure instead of aborting under pipefail before drift is noted.
case_data_model_no_create_tables() {
  local name="data-model::no_create_tables_fail_closed"
  local fix; fix="$(new_fixture)"
  local migration
  for migration in "$fix"/internal/store/migrations/*.sql; do
    sed -i -E 's/CREATE VIRTUAL TABLE/CREATE VIRTUAL_TBL/g; s/CREATE TABLE/CREATE_TBL/g' "$migration"
  done
  assert_drift "$name" "$fix" "no CREATE TABLE statements found"
}

# (c future type) data-model: a valid future SQLite type such as BOOLEAN must
#                 still be treated as a column definition. The old extractor
#                 silently ignored non-TEXT/INTEGER/REAL/BLOB/NUMERIC columns;
#                 this pins the fail-closed code→spec drift.
case_data_model_future_sql_type_column() {
  local name="data-model::future_sql_type_column"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/internal/store/migrations/0007_fts5_index_logs.sql" <<'EOF'

CREATE TABLE future_type_columns (
    id TEXT PRIMARY KEY NOT NULL,
    future_flag BOOLEAN NOT NULL DEFAULT 0
);
EOF
  cat >> "$fix/.agents/sow/specs/data-model.md" <<'EOF'

### future_type_columns

```sql
CREATE TABLE future_type_columns (
    id TEXT PRIMARY KEY NOT NULL
);
```
EOF
  assert_drift "$name" "$fix" "future_type_columns.future_flag"
}

# (c') data-model: add a migration table that the spec never mentions ->
#      table code→spec drift on data-model-columns.
case_data_model_table_not_in_spec() {
  local name="data-model::migration_table_not_in_spec"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/internal/store/migrations/0007_fts5_index_logs.sql" <<'EOF'

CREATE TABLE phantom_table (
    id TEXT PRIMARY KEY NOT NULL
);
EOF
  assert_drift "$name" "$fix" "phantom_table"
}

# (c'') data-model: document a table heading that no migration creates ->
#       table spec→code drift on data-model-columns.
case_data_model_spec_table_not_in_code() {
  local name="data-model::spec_table_not_in_migrations"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/.agents/sow/specs/data-model.md" <<'EOF'

### phantom_table

```sql
CREATE TABLE phantom_table (
    id TEXT PRIMARY KEY NOT NULL
);
```
EOF
  assert_drift "$name" "$fix" "phantom_table"
}

# (c''') data-model: document a bogus CREATE TABLE inside a SQL fenced block
#        under a prose/non-table heading. A heading-only table detector misses
#        this; SQL-block table extraction must report the table name.
case_data_model_sql_block_table_under_prose_heading_not_in_code() {
  local name="data-model::sql_block_table_under_prose_heading_not_in_migrations"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/.agents/sow/specs/data-model.md" <<'EOF'

### Synthetic prose section

This section is prose, not a `### <table>` schema heading.

```sql
CREATE TABLE sql_block_only_phantom (
    id TEXT PRIMARY KEY NOT NULL
);
```
EOF
  assert_drift "$name" "$fix" "sql_block_only_phantom"
}

# (d) canonical: add an EventKind const in code that the spec block lacks ->
#     code→spec drift on canonical-event-kinds.
case_canonical_kind_not_in_spec() {
  local name="canonical::code_kind_not_in_spec"
  local fix; fix="$(new_fixture)"
  # Insert a new const into the EventKind block in events.go.
  sed -i 's#\(EvSourceError      EventKind = "source_error"\)#\1\n\tEvPhantomKind      EventKind = "phantom_kind"#' \
    "$fix/internal/canonical/events.go"
  assert_drift "$name" "$fix" "phantom_kind"
}

# (d') canonical: document an EventKind in the spec that code lacks -> spec→code
#      drift on canonical-event-kinds.
case_canonical_spec_kind_not_in_code() {
  local name="canonical::spec_kind_not_in_code"
  local fix; fix="$(new_fixture)"
  sed -i 's#\(EvSourceError      EventKind = "source_error".*\)#\1\n    EvPhantomKind      EventKind = "phantom_kind"#' \
    "$fix/.agents/sow/specs/canonical-events.md"
  assert_drift "$name" "$fix" "phantom_kind"
}

# (d'') canonical: erase code-side EventKind value anchors. Empty extraction is
#       itself drift and must produce the canonical-event-kinds diagnostic.
case_canonical_code_extractor_empty() {
  local name="canonical::code_extractor_empty_fail_closed"
  local fix; fix="$(new_fixture)"
  sed -i -E 's/EventKind = "[a-z_]+"/EventKind = EventKind("")/g' \
    "$fix/internal/canonical/events.go"
  assert_drift "$name" "$fix" "constants found in internal/canonical/events.go"
}

# (d''') canonical: erase spec-side EventKind value anchors.
case_canonical_spec_extractor_empty() {
  local name="canonical::spec_extractor_empty_fail_closed"
  local fix; fix="$(new_fixture)"
  sed -i -E 's/EventKind = "[a-z_]+"/EventKind = EventKind("")/g' \
    "$fix/.agents/sow/specs/canonical-events.md"
  assert_drift "$name" "$fix" "constants found in .agents/sow/specs/canonical-events.md"
}

# (e) adapter-probes: add a discovery probe for a format with no adapter spec
#     file -> code→spec drift on adapter-probes.
case_adapter_probe_no_spec() {
  local name="adapter-probes::probe_without_spec_file"
  local fix; fix="$(new_fixture)"
  # Inject a probe struct for a format whose adapter-<name>.md does not exist.
  sed -i 's#\(format:   "aiagent_v3",\)#format: "phantomfmt",\n\t\t\t\1#' \
    "$fix/cmd/ai-viewer-ingest/sources.go"
  assert_drift "$name" "$fix" "phantomfmt"
}

# (e') adapter-probes: keep the spec file but remove the default probe-path
#      anchor it must document.
case_adapter_probe_spec_missing_anchor() {
  local name="adapter-probes::spec_missing_probe_anchor"
  local fix; fix="$(new_fixture)"
  sed -i 's#\.ai-agent/sessions#<sessions-dir>#g' \
    "$fix/.agents/sow/specs/adapter-aiagent-v3.md"
  assert_drift "$name" "$fix" ".ai-agent/sessions"
}

# (e'') adapter-probes: erase all discovery `format: "..."` anchors. The
#       detector must report extraction failure instead of exiting silently under
#       pipefail.
case_adapter_probe_extractor_empty() {
  local name="adapter-probes::extractor_empty_fail_closed"
  local fix; fix="$(new_fixture)"
  sed -i -E 's/format:[[:space:]]+"[a-z0-9_-]+"/format: probeFormat/g' \
    "$fix/cmd/ai-viewer-ingest/sources.go"
  assert_drift "$name" "$fix" "no 'format:"
}

# (f) contract-matrix: plant an invalid matrix enum. The delegated checker must
#     return a named contract-matrix drift finding through spec-drift.sh.
case_contract_matrix_invalid_enum() {
  local name="contract-matrix::invalid_enum"
  local fix; fix="$(new_fixture)"
  sed -i '0,/state: "exposed"/s//state: "bogus-state"/' \
    "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "contract-matrix"
}

# (f') contract-matrix: mark an exposed field that neither the Go presenter DTO
#      nor TypeScript interface exposes.
case_contract_matrix_exposed_field_missing() {
  local name="contract-matrix::exposed_field_missing"
  local fix; fix="$(new_fixture)"
  cat >> "$fix/testdata/contracts/field-matrix.yaml" <<'EOF'

  - entity: "session"
    field: "phantom_provider"
    entity_kind: "session"
    db_column: "sessions.provider"
    derived_from: ""
    rest_surfaces: "/api/sessions/:id"
    typescript_types: "SessionDetail"
    ui_surfaces: "Session detail"
    state: "exposed"
    intent: "detail"
    include_token: ""
    privacy_class: "public"
    adapter_population: "broad"
    index_status: "indexed"
    stats_dimension_eligible: "eligible"
    subscription_filter_eligible: "excluded"
    internal_reason: ""
    sow_ref: "SOW-0105"
    pending_ref: ""
    test_refs: "internal/presenter/session_detail_test.go"
    artifact_class: ""
EOF
  assert_drift "$name" "$fix" "phantom_provider"
}

# (f'') contract-matrix: remove a payload-kind artifact class. The matrix gate
#       protects the adapter-facing kind → UI artifact-class mapping.
case_contract_matrix_artifact_class_missing() {
  local name="contract-matrix::artifact_class_missing"
  local fix; fix="$(new_fixture)"
  sed -i '0,/artifact_class: "llm_request"/s//artifact_class: ""/' \
    "$fix/testdata/contracts/field-matrix.yaml"
  assert_drift "$name" "$fix" "artifact_class"
}

# --- main -------------------------------------------------------------------

main() {
  if [ ! -x "$DETECTOR" ]; then
    printf '%sFATAL%s detector not executable: %s\n' "$C_RED" "$C_RESET" "$DETECTOR" >&2
    exit 1
  fi
  printf '%s==> spec-drift-test%s\n' "$C_YELLOW" "$C_RESET"

  case_clean_passes
  case_rest_code_not_in_spec
  case_rest_spec_not_in_code_unmarked
  case_rest_method_mismatch
  case_rest_missing_method_gate
  case_rest_no_registrations_extracted
  case_rest_phase2_exempt
  case_sse_spec_not_in_code
  case_sse_code_not_in_spec
  case_sse_code_extractor_empty
  case_sse_spec_extractor_empty
  case_data_model_column_not_in_spec
  case_data_model_common_column_wrong_table
  case_data_model_statement_boundary_create
  case_data_model_no_create_tables
  case_data_model_future_sql_type_column
  case_data_model_table_not_in_spec
  case_data_model_spec_table_not_in_code
  case_data_model_sql_block_table_under_prose_heading_not_in_code
  case_canonical_kind_not_in_spec
  case_canonical_spec_kind_not_in_code
  case_canonical_code_extractor_empty
  case_canonical_spec_extractor_empty
  case_adapter_probe_no_spec
  case_adapter_probe_spec_missing_anchor
  case_adapter_probe_extractor_empty
  case_contract_matrix_invalid_enum
  case_contract_matrix_exposed_field_missing
  case_contract_matrix_artifact_class_missing

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
