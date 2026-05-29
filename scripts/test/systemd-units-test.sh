#!/usr/bin/env bash
#
# systemd-units-test.sh
#
# Static lint for the repo-template systemd USER units under deploy/systemd/.
# Asserts each unit carries the directives SOW-0001 Chunk 19 / deployment.md
# §"systemd User Units" require, and that the serve unit is ordered After the
# ingester. No session bus is touched: the directive checks are plain greps,
# and `systemd-analyze verify` (when available) parses/validates the files
# offline.
#
# Asserted invariants (both units, unless noted):
#   - [Unit]    Description=
#   - [Service] ExecStart=%h/.local/bin/ai-viewer-...
#   - [Service] Restart=on-failure
#   - [Install] WantedBy=default.target
#   - serve only: [Unit] After=ai-viewer-ingest.service
#
# Exit 0 with a PASS line when every check passes; non-zero with a clear
# message naming the missing directive + file on any failure.

set -euo pipefail
IFS=$'\n\t'

# Colors only when stderr is a TTY (this script prints diagnostics to stderr).
if [ -t 2 ]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'
  GRAY=$'\033[0;90m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; GRAY=""; NC=""
fi

# Transparent command tracing (matches scripts/build.sh): print the command,
# capture its real exit code (never via `if ! "$@"`, which masks failures).
# Colors go through %s (not the format string) so shellcheck SC2059 stays clean.
run() {
  printf >&2 '%s%s >%s ' "$GRAY" "$(pwd)" "$NC"
  printf >&2 '%s' "$YELLOW"; printf >&2 '%q ' "$@"; printf >&2 '%s\n' "$NC"
  local ec=0; "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then echo -e >&2 "${RED}[ERROR]${NC} exit ${ec}: $*"; return "$ec"; fi
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
UNIT_DIR="${REPO_ROOT}/deploy/systemd"
INGEST_UNIT="${UNIT_DIR}/ai-viewer-ingest.service"
SERVE_UNIT="${UNIT_DIR}/ai-viewer-serve.service"

fail() {
  echo -e >&2 "${RED}[FAIL]${NC} $*"
  exit 1
}

# assert_directive <file> <extended-regex> <human-description>
# Fails loudly if the regex is not present in the file.
assert_directive() {
  local file="$1" regex="$2" desc="$3"
  if [[ ! -f "$file" ]]; then
    fail "missing unit file: ${file}"
  fi
  if ! grep -Eq -- "$regex" "$file"; then
    fail "${file##*/}: missing ${desc} (expected /${regex}/)"
  fi
}

echo -e >&2 "${GRAY}linting systemd units under ${UNIT_DIR}${NC}"

# --- Both units: shared required directives. ----------------------------------
for unit in "$INGEST_UNIT" "$SERVE_UNIT"; do
  assert_directive "$unit" '^Description=.+'            '[Unit] Description='
  assert_directive "$unit" '^Restart=on-failure$'       '[Service] Restart=on-failure'
  assert_directive "$unit" '^RestartSec=3s$'            '[Service] RestartSec=3s'
  assert_directive "$unit" '^WantedBy=default\.target$' '[Install] WantedBy=default.target'
done

# --- Per-unit EXACT ExecStart (anchored to the full binary name). -------------
# A prefix match ('ai-viewer-') would let a typo like 'ai-viewer-serv' pass —
# and its `systemd-analyze verify` "not executable" error would then be
# suppressed by the filter below — so assert the exact command per unit.
assert_directive "$INGEST_UNIT" '^ExecStart=%h/\.local/bin/ai-viewer-ingest$' \
  '[Service] ExecStart=%h/.local/bin/ai-viewer-ingest (exact)'
assert_directive "$SERVE_UNIT" '^ExecStart=%h/\.local/bin/ai-viewer-serve$' \
  '[Service] ExecStart=%h/.local/bin/ai-viewer-serve (exact)'

# --- serve unit must start after the ingester. --------------------------------
assert_directive "$SERVE_UNIT" '^After=ai-viewer-ingest\.service$' \
  '[Unit] After=ai-viewer-ingest.service (start-order)'

echo -e >&2 "${GREEN}directive checks passed${NC} (both units)"

# --- Offline parse/validation via systemd-analyze when present. ---------------
# `systemd-analyze verify` parses + validates units without touching any
# session/system bus, so it is safe in CI and on the operator's box. Absent on
# minimal/non-systemd environments — skip (don't fail) there.
#
# IMPORTANT: verify also checks that the ExecStart= command exists and is
# executable (systemd-analyze(1)). These are repo TEMPLATES whose ExecStart is
# `%h/.local/bin/ai-viewer-...`; the binaries are installed there only by the
# operator's `install-systemd-user.sh install` step (SOW-0001 Chunk 19 D4 — we
# must NOT install/start anything during verification). So on a clean checkout
# verify ALWAYS emits "<unit>: Command %h/.local/bin/ai-viewer-... is not
# executable: No such file or directory" and exits non-zero. That single line
# is EXPECTED for an un-installed template and is filtered out; ANY OTHER verify
# diagnostic (unknown key, bad section, misspelt directive, ordering error) is a
# real failure and aborts the test. systemd-analyze offers no flag to suppress
# the executable check (only --man / --generators), hence the line filter.
if command -v systemd-analyze >/dev/null 2>&1; then
  printf >&2 '%s%s >%s systemd-analyze verify (template ExecStart-not-installed lines filtered)\n' \
    "$GRAY" "$(pwd)" "$NC"
  # Capture combined output; do not let the non-zero exit trip `set -e` here —
  # we classify the output ourselves below.
  verify_out="$(systemd-analyze verify "$INGEST_UNIT" "$SERVE_UNIT" 2>&1)" || true
  # Drop ONLY the two expected "not installed yet" lines for the EXACT known
  # binary paths (ai-viewer-ingest / ai-viewer-serve under ~/.local/bin). A
  # typo'd or unexpected ExecStart would emit a differently-named
  # "is not executable" line that does NOT match and therefore survives as a
  # real error. Whatever remains is a genuine verify failure.
  residual="$(printf '%s\n' "$verify_out" \
    | grep -vE '/\.local/bin/ai-viewer-(ingest|serve) is not executable: No such file or directory' \
    | grep -v '^[[:space:]]*$' || true)"
  if [[ -n "$residual" ]]; then
    echo -e >&2 "${RED}[FAIL]${NC} systemd-analyze verify reported errors beyond the expected"
    echo -e >&2 "       un-installed-ExecStart lines:"
    printf >&2 '%s\n' "$residual"
    exit 1
  fi
  echo -e >&2 "${GREEN}systemd-analyze verify clean${NC} (unit syntax valid; only the expected" \
    "un-installed-ExecStart notice present)"
else
  echo -e >&2 "${YELLOW}[skip]${NC} systemd-analyze not found; skipping offline unit verification"
fi

echo -e >&2 "${GREEN}PASS${NC} deploy/systemd units lint clean"
