#!/usr/bin/env bash
#
# install-systemd-user.sh — install/uninstall ai-viewer as systemd USER units.
#
# Workstation-only, localhost-only, NO privilege. Copies the two release
# binaries to ~/.local/bin/ and the repo-template units from deploy/systemd/ to
# the user systemd dir, then `daemon-reload`. It does NOT enable or start the
# services — it PRINTS the `systemctl --user enable --now` command for the
# operator to run, because enabling a run-on-login service is a persistent
# action on the operator's machine (SOW-0001 Chunk 19 D2/D4).
#
# Subcommands (default: install):
#   install    copy binaries + units, daemon-reload, print enable command
#   uninstall  disable+stop if present, remove units, daemon-reload (keeps data)
#   status     systemctl --user status for both units
#   --help|-h  usage
#
# All paths are repo-relative (resolved from this script's own dir) or derived
# from $HOME/XDG vars; no sudo, no secrets, no hardcoded home-path literals.

set -euo pipefail

# Colors for transparent command tracing. ANSI-C quoting ($'...') stores the
# real escape bytes so both `printf '%s'` and `echo -e` render them; only
# colorize when stderr is a TTY (diagnostics go to stderr).
if [ -t 2 ]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'
  GRAY=$'\033[0;90m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; GRAY=""; NC=""
fi

# Print the command, then run it capturing its REAL exit code (never via
# `if ! "$@"`, which would always read 0 under set -e and mask failures).
# Colors go through %s (not the format string) so shellcheck SC2059 stays clean.
run() {
  printf >&2 '%s%s >%s ' "$GRAY" "$(pwd)" "$NC"
  printf >&2 '%s' "$YELLOW"; printf >&2 '%q ' "$@"; printf >&2 '%s\n' "$NC"
  local ec=0; "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then echo -e >&2 "${RED}[ERROR]${NC} exit ${ec}: $*"; return "$ec"; fi
}

# Resolve repo root from the script's own location so it works from any CWD.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Install destinations (XDG-aware; %h equivalents for the shell side).
BIN_DIR="${HOME}/.local/bin"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/ai-viewer"

# Source artifacts in the repo.
SRC_UNIT_DIR="${REPO_ROOT}/deploy/systemd"
INGEST_UNIT="ai-viewer-ingest.service"
SERVE_UNIT="ai-viewer-serve.service"
INGEST_BIN="${REPO_ROOT}/bin/ai-viewer-ingest"
SERVE_BIN="${REPO_ROOT}/bin/ai-viewer-serve"

usage() {
  cat <<'EOF'
Usage: scripts/install-systemd-user.sh [command]

Install ai-viewer as systemd USER units (localhost-only, no privilege).

Commands:
  install     (default) Always rebuild from current source via scripts/build.sh,
              copy the binaries to ~/.local/bin and units to the user systemd
              dir, run `daemon-reload`, then print the enable command.
  uninstall   Disable+stop the units (if present), remove them, daemon-reload.
              Binaries and the data dir under ~/.local/share/ai-viewer are kept.
  status      Show `systemctl --user status` for both units.
  --help, -h  Show this help.

After `install`, run the printed command to enable + start on login:
  systemctl --user enable --now ai-viewer-ingest.service ai-viewer-serve.service
EOF
}

cmd_install() {
  # 1. ALWAYS build from current source, then copy. install is the
  #    first-time-AND-update entrypoint; building only "if bin/ is missing"
  #    would silently reinstall STALE binaries on `git pull && install`
  #    (the user units run ~/.local/bin, so a restart would keep the old code).
  #    scripts/build.sh is the canonical "embed the UI + build both binaries"
  #    step; running it unconditionally keeps install == fresh-from-source.
  echo -e >&2 "${YELLOW}[info]${NC} building current source via scripts/build.sh"
  run "${REPO_ROOT}/scripts/build.sh"
  if [[ ! -x "$INGEST_BIN" || ! -x "$SERVE_BIN" ]]; then
    echo -e >&2 "${RED}[ERROR]${NC} build did not produce both binaries; check scripts/build.sh output"
    return 1
  fi

  # 2. Create destination dirs.
  run mkdir -p "$BIN_DIR" "$UNIT_DIR"

  # 3. Install binaries (0755) and units (0644).
  run install -m 0755 "$INGEST_BIN" "${BIN_DIR}/ai-viewer-ingest"
  run install -m 0755 "$SERVE_BIN"  "${BIN_DIR}/ai-viewer-serve"
  run install -m 0644 "${SRC_UNIT_DIR}/${INGEST_UNIT}" "${UNIT_DIR}/${INGEST_UNIT}"
  run install -m 0644 "${SRC_UNIT_DIR}/${SERVE_UNIT}"  "${UNIT_DIR}/${SERVE_UNIT}"

  # 4. Reload the user manager so it sees the new units.
  run systemctl --user daemon-reload

  # 5. Print (do NOT execute) the enable+start command. Enabling a run-on-login
  #    service is a persistent action — the operator runs it themselves (D4).
  echo -e >&2 ""
  echo -e >&2 "${GREEN}installed${NC}:"
  echo -e >&2 "  binaries -> ${BIN_DIR}/ai-viewer-{ingest,serve}"
  echo -e >&2 "  units    -> ${UNIT_DIR}/{${INGEST_UNIT},${SERVE_UNIT}}"
  echo -e >&2 ""
  echo -e >&2 "Next, enable + start the services on login (run this yourself):"
  echo -e >&2 "  ${YELLOW}systemctl --user enable --now ${INGEST_UNIT} ${SERVE_UNIT}${NC}"
  echo -e >&2 ""
  echo -e >&2 "Then open the UI: ${GREEN}http://127.0.0.1:7710${NC}"
}

cmd_uninstall() {
  # Idempotent: only disable+stop units that are currently enabled/active, and
  # tolerate a missing user manager (|| true) so re-running never errors.
  for unit in "$INGEST_UNIT" "$SERVE_UNIT"; do
    if systemctl --user is-enabled "$unit" >/dev/null 2>&1 \
       || systemctl --user is-active "$unit" >/dev/null 2>&1; then
      # The unit is known enabled/active, so a disable failure is a REAL error
      # (do NOT `|| true` it — that would print "uninstalled" while the service
      # keeps running). set -euo pipefail then aborts loudly. The is-enabled/
      # is-active probes above already tolerate a missing user manager.
      run systemctl --user disable --now "$unit"
    fi
    if [[ -f "${UNIT_DIR}/${unit}" ]]; then
      run rm -f "${UNIT_DIR}/${unit}"
    fi
  done
  run systemctl --user daemon-reload || true

  echo -e >&2 ""
  echo -e >&2 "${GREEN}uninstalled${NC}: units removed from ${UNIT_DIR}"
  echo -e >&2 "Binaries in ${BIN_DIR} were left in place."
  echo -e >&2 "Data under ${DATA_DIR} is preserved — removing it is your choice:"
  echo -e >&2 "  ${YELLOW}rm -rf ${DATA_DIR}${NC}"
}

cmd_status() {
  # Never fail the script if the units are not installed/loaded. --no-pager so a
  # script subcommand never drops into an interactive pager.
  run systemctl --user --no-pager status "$INGEST_UNIT" "$SERVE_UNIT" || true
}

main() {
  local sub="${1:-install}"
  case "$sub" in
    install)            cmd_install ;;
    uninstall)          cmd_uninstall ;;
    status)             cmd_status ;;
    -h|--help|help)     usage ;;
    *)
      echo -e >&2 "${RED}[ERROR]${NC} unknown command: ${sub}"
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
