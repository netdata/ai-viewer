#!/usr/bin/env bash
#
# systemd-units-test.sh
#
# Static lint for the repo-template systemd units under deploy/systemd/ and
# deploy/systemd-system/. No session bus is touched: directive checks are plain
# greps, and `systemd-analyze verify` (when available) parses/validates the
# files offline.
#
# Asserted invariants:
#   - [Unit]    Description=
#   - [Service] Restart=on-failure
#   - [Service] RestartSec=3s
#   - [Service] TimeoutStopSec=45s
#   - [Install] WantedBy=default.target
#   - per-variant ExecStart, After, Type, hardening, resource directives
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
USER_UNIT_DIR="${REPO_ROOT}/deploy/systemd"
SYSTEM_UNIT_DIR="${REPO_ROOT}/deploy/systemd-system"
USER_INGEST_UNIT="${USER_UNIT_DIR}/ai-viewer-ingest.service"
USER_SERVE_UNIT="${USER_UNIT_DIR}/ai-viewer-serve.service"
SYSTEM_INGEST_UNIT="${SYSTEM_UNIT_DIR}/ai-viewer-ingest.service"
SYSTEM_SERVE_UNIT="${SYSTEM_UNIT_DIR}/ai-viewer-serve.service"

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

# assert_absent <file> <extended-regex> <human-description>
# Fails loudly if the regex is present in the file.
assert_absent() {
  local file="$1" regex="$2" desc="$3"
  if [[ ! -f "$file" ]]; then
    fail "missing unit file: ${file}"
  fi
  if grep -Eq -- "$regex" "$file"; then
    fail "${file##*/}: unexpected ${desc} (matched /${regex}/)"
  fi
}

echo -e >&2 "${GRAY}linting systemd units under ${USER_UNIT_DIR} and ${SYSTEM_UNIT_DIR}${NC}"

# --- All units: shared required directives. -----------------------------------
for unit in "$USER_INGEST_UNIT" "$USER_SERVE_UNIT" "$SYSTEM_INGEST_UNIT" "$SYSTEM_SERVE_UNIT"; do
  assert_directive "$unit" '^Description=.+'            '[Unit] Description='
  assert_directive "$unit" '^Restart=on-failure$'       '[Service] Restart=on-failure'
  assert_directive "$unit" '^RestartSec=3s$'            '[Service] RestartSec=3s'
  assert_directive "$unit" '^TimeoutStopSec=45s$'       '[Service] TimeoutStopSec=45s'
  assert_absent "$unit" '^Type=notify$'                 '[Service] Type=notify'
  assert_absent "$unit" '^WatchdogSec='                 '[Service] WatchdogSec='
done

# --- User units. --------------------------------------------------------------
for unit in "$USER_INGEST_UNIT" "$USER_SERVE_UNIT"; do
  assert_directive "$unit" '^WantedBy=default\.target$' '[Install] WantedBy=default.target'
  assert_absent "$unit" '^Type='                        '[Service] Type= (user units rely on simple default)'
  assert_absent "$unit" '^MemoryHigh='                  '[Service] MemoryHigh='
  assert_absent "$unit" '^MemoryMax='                   '[Service] MemoryMax='
  assert_absent "$unit" '^OOMPolicy='                   '[Service] OOMPolicy='
done

assert_directive "$USER_INGEST_UNIT" '^After=default\.target$' \
  '[Unit] After=default.target'
assert_directive "$USER_INGEST_UNIT" '^ExecStart=%h/\.local/bin/ai-viewer-ingest$' \
  '[Service] ExecStart=%h/.local/bin/ai-viewer-ingest (exact)'
assert_directive "$USER_SERVE_UNIT" '^After=ai-viewer-ingest\.service$' \
  '[Unit] After=ai-viewer-ingest.service (start-order)'
assert_directive "$USER_SERVE_UNIT" '^ExecStart=%h/\.local/bin/ai-viewer-serve$' \
  '[Service] ExecStart=%h/.local/bin/ai-viewer-serve (exact)'

# --- System units. ------------------------------------------------------------
for unit in "$SYSTEM_INGEST_UNIT" "$SYSTEM_SERVE_UNIT"; do
  assert_directive "$unit" '^Type=simple$'                  '[Service] Type=simple'
  assert_directive "$unit" '^WantedBy=multi-user\.target$'  '[Install] WantedBy=multi-user.target'
  assert_directive "$unit" '^User=__OPERATOR_USER__$'       '[Service] User=__OPERATOR_USER__'
  assert_directive "$unit" '^Group=__OPERATOR_GROUP__$'     '[Service] Group=__OPERATOR_GROUP__'
  assert_directive "$unit" '^NoNewPrivileges=true$'         '[Service] NoNewPrivileges=true'
  assert_directive "$unit" '^ProtectSystem=strict$'         '[Service] ProtectSystem=strict'
  assert_directive "$unit" '^ProtectHome=read-only$'        '[Service] ProtectHome=read-only'
  assert_directive "$unit" '^PrivateTmp=true$'              '[Service] PrivateTmp=true'
done

assert_directive "$SYSTEM_INGEST_UNIT" '^After=network\.target$' \
  '[Unit] After=network.target'
assert_directive "$SYSTEM_INGEST_UNIT" '^ExecStart=/opt/ai-viewer/bin/ai-viewer-ingest --db /opt/ai-viewer/data/index\.db --state-dir /opt/ai-viewer/data --log-level info __AI_VIEWER_SOURCES__$' \
  '[Service] ExecStart system ingester (exact)'
assert_directive "$SYSTEM_INGEST_UNIT" '^ReadWritePaths=/opt/ai-viewer/data /opt/ai-viewer/logs$' \
  '[Service] ReadWritePaths=/opt/ai-viewer/data /opt/ai-viewer/logs'
assert_directive "$SYSTEM_INGEST_UNIT" '^OOMPolicy=stop$'       '[Service] OOMPolicy=stop'
assert_directive "$SYSTEM_INGEST_UNIT" '^MemoryHigh=4G$'        '[Service] MemoryHigh=4G'
assert_directive "$SYSTEM_INGEST_UNIT" '^MemoryMax=8G$'         '[Service] MemoryMax=8G'
assert_directive "$SYSTEM_INGEST_UNIT" '^IOSchedulingClass=idle$' '[Service] IOSchedulingClass=idle'
assert_directive "$SYSTEM_INGEST_UNIT" '^LimitNOFILE=65536$'    '[Service] LimitNOFILE=65536'

assert_directive "$SYSTEM_SERVE_UNIT" '^After=network\.target ai-viewer-ingest\.service$' \
  '[Unit] After=network.target ai-viewer-ingest.service'
assert_directive "$SYSTEM_SERVE_UNIT" '^ExecStart=/opt/ai-viewer/bin/ai-viewer-serve --db /opt/ai-viewer/data/index\.db --state-dir /opt/ai-viewer/data --bind 127\.0\.0\.1:7710 --log-level info$' \
  '[Service] ExecStart system serve (exact)'
assert_absent "$SYSTEM_SERVE_UNIT" '^ReadWritePaths='        '[Service] ReadWritePaths= on read-only serve'
assert_absent "$SYSTEM_SERVE_UNIT" '^OOMPolicy='             '[Service] OOMPolicy= on serve'
assert_absent "$SYSTEM_SERVE_UNIT" '^MemoryHigh='            '[Service] MemoryHigh= on serve'
assert_absent "$SYSTEM_SERVE_UNIT" '^MemoryMax='             '[Service] MemoryMax= on serve'
assert_absent "$SYSTEM_SERVE_UNIT" '^IOSchedulingClass='     '[Service] IOSchedulingClass= on serve'

echo -e >&2 "${GREEN}directive checks passed${NC} (user + system units)"

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
  verify_out="$(systemd-analyze verify "$USER_INGEST_UNIT" "$USER_SERVE_UNIT" "$SYSTEM_INGEST_UNIT" "$SYSTEM_SERVE_UNIT" 2>&1)" || true
  # Drop ONLY the two expected "not installed yet" lines for the EXACT known
  # binary paths (ai-viewer-ingest / ai-viewer-serve under ~/.local/bin). A
  # typo'd or unexpected ExecStart would emit a differently-named
  # "is not executable" line that does NOT match and therefore survives as a
  # real error. Whatever remains is a genuine verify failure.
  residual="$(printf '%s\n' "$verify_out" \
    | grep -vE '(/\.local/bin|/opt/ai-viewer/bin)/ai-viewer-(ingest|serve) is not executable: No such file or directory' \
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

echo -e >&2 "${GREEN}PASS${NC} systemd unit templates lint clean"
