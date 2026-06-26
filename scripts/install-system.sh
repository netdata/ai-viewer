#!/usr/bin/env bash
# Install ai-viewer as a SYSTEM service under /opt/ai-viewer, RUNNING AS THE
# INSTALLING OPERATOR, reading the operator's agent data via explicit --source
# flags.
#
# Why run-as-operator (long-term-best for this app):
#   - The app's purpose is "the operator's viewer for the operator's agent
#     data." The operator's ~/.ai-agent / ~/.claude / ~/.codex /
#     ~/.local/share/opencode dirs are owner-only (0700), so a dedicated
#     service user CANNOT read them without invasive recursive ACLs (fragile,
#     surprises other tools). Running as the operator is correct + simple.
#   - Localhost-only, read-only on sources by design (os.O_RDONLY); the same
#     privilege level the operator already grants every other agent CLI they
#     run (opencode/codex/claude). A dedicated user for this one tool is
#     security theater relative to the rest of the operator's stack.
#   - SOURCES ARE EXPLICIT (--source flags rendered into the unit), not
#     auto-discovery: auditable (the unit file shows exactly what's read) and
#     independent of $HOME resolution.
#
# Layout:
#   /opt/ai-viewer/bin/ai-viewer-{ingest,serve}   (binaries)
#   /opt/ai-viewer/data/index.db                  (SQLite; owned by operator)
#   /opt/ai-viewer/logs/                          (fallback; journald preferred)
#   /etc/systemd/system/ai-viewer-{ingest,serve}.service
#
# Usage:
#   scripts/install-system.sh             # install (or upgrade) + start
#   scripts/install-system.sh uninstall   # stop + disable + remove units + /opt/ai-viewer
#   scripts/install-system.sh status      # systemctl status for both + the URL
#
# Repo-relative paths; no secrets. Requires passwordless sudo.
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; GRAY='\033[0;90m'; NC='\033[0m'
run() {
  printf >&2 "${GRAY}$(pwd) >${NC} ${YELLOW}"; printf >&2 "%q " "$@"; printf >&2 "${NC}\n"
  local ec=0; "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then echo -e >&2 "${RED}[ERROR]${NC} exit ${ec}: $*"; return "$ec"; fi
}
sudorun() {
  printf >&2 "${GRAY}>${NC} ${YELLOW}sudo "; printf >&2 "%q " "$@"; printf >&2 "${NC}\n"
  local ec=0; sudo "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then echo -e >&2 "${RED}[ERROR]${NC} sudo exit ${ec}: $*"; return "$ec"; fi
}

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OPT_DIR="/opt/ai-viewer"
BIN_DIR="$OPT_DIR/bin"
DATA_DIR="$OPT_DIR/data"
LOG_DIR="$OPT_DIR/logs"
UNIT_DIR="/etc/systemd/system"
PORT="7710"
URL="http://127.0.0.1:${PORT}/"
SRC_TOKEN="__AI_VIEWER_SOURCES__"
USER_TOKEN="__OPERATOR_USER__"
GROUP_TOKEN="__OPERATOR_GROUP__"

require_sudo() {
  if ! sudo -n true 2>/dev/null; then
    echo -e "${RED}[ERROR]${NC} this installer needs passwordless sudo." >&2
    exit 1
  fi
}

# The OPERATOR is the invoking user (SUDO_USER under sudo, else $USER). The
# service runs as this user and reads this user's agent data.
operator_user() { echo "${SUDO_USER:-$USER}"; }
operator_home() {
  local u; u=$(operator_user)
  local h; h=$(getent passwd "$u" | cut -d: -f6)
  if [[ -z "$h" || ! -d "$h" ]]; then
    echo -e "${RED}[ERROR]${NC} could not resolve home for operator '$u'." >&2
    exit 1
  fi
  echo "$h"
}
operator_group() {
  local u; u=$(operator_user)
  local gid; gid=$(id -g "$u")
  getent group "$gid" | cut -d: -f1
}

# Probe the operator's well-known agent-data paths; emit --source flags for
# every one that exists. ai-agent sessions dir covers both v2 and v3.
render_sources() {
  local home="$1"
  local -a srcs=()
  local aiagent="$home/.ai-agent/sessions"
  local claude="$home/.claude/projects"
  local codex="$home/.codex/sessions"
  local opencode="$home/.local/share/opencode/opencode.db"
  [[ -e "$aiagent"  ]] && srcs+=(--source "aiagent_v3:$aiagent" --source "aiagent_v2:$aiagent")
  [[ -e "$claude"   ]] && srcs+=(--source "claude-code:$claude")
  [[ -e "$codex"    ]] && srcs+=(--source "codex:$codex")
  [[ -e "$opencode" ]] && srcs+=(--source "opencode:$opencode")
  if [[ ${#srcs[@]} -eq 0 ]]; then
    echo -e "${RED}[ERROR]${NC} no agent-data sources found under $home." >&2
    echo "        Looked for: $aiagent, $claude, $codex, $opencode" >&2
    exit 1
  fi
  printf '%s\0' "${srcs[@]}"
}

# Render a unit template: substitute the three tokens. Renders to a temp file
# (sed has no -o output flag; we read template → write tmp → install into UNIT_DIR).
render_unit() {
  local tmpl="$1" out="$2" op_user="$3" op_group="$4" src_flags="$5"
  sed \
    -e "s|${USER_TOKEN}|${op_user}|g" \
    -e "s|${GROUP_TOKEN}|${op_group}|g" \
    -e "s|${SRC_TOKEN}|${src_flags}|" \
    "$tmpl" > "$out"
}

install_binary_atomically() {
  local src="$1" dst="$2" tmp
  tmp="${dst}.tmp.$$.$RANDOM"
  sudorun rm -f "$tmp"
  sudorun install -m 0755 "$src" "$tmp"
  sudorun mv -f "$tmp" "$dst"
}

stop_unit_if_active() {
  local unit="$1"
  if systemctl is-active --quiet "$unit"; then
    sudorun systemctl stop "$unit"
  else
    echo -e "${GRAY}skip stop ${unit}: not active${NC}" >&2
  fi
}

wait_for_ingester_active() {
  local phase="$1"
  for _ in $(seq 1 15); do
    if systemctl is-active --quiet ai-viewer-ingest.service; then
      return 0
    fi
    sleep 1
  done
  echo -e "${RED}[ERROR]${NC} ingester not active during ${phase} validation." >&2
  sudorun systemctl status ai-viewer-ingest.service --no-pager -l || true
  sudorun journalctl -u ai-viewer-ingest --since '2 min ago' --no-pager -n 80 || true
  return 1
}

do_build() {
  run cd "$REPO_ROOT"
  echo -e "${GRAY}== build (frontend + Go binaries) ==${NC}" >&2
  run ./scripts/build.sh
}

do_install() {
  require_sudo

  local op_user op_group home
  op_user=$(operator_user)
  op_group=$(operator_group)
  home=$(operator_home)
  echo -e "${GRAY}== operator (service runs as this user): ${op_user} — home ${home} ==${NC}" >&2

  local -a sources=()
  while IFS= read -r -d '' s; do sources+=("$s"); done < <(render_sources "$home")
  echo -e "${GRAY}== discovered sources ==${NC}" >&2
  local i=0
  while [[ $i -lt ${#sources[@]} ]]; do
    echo -e "  ${sources[$i]} ${sources[$((i+1))]}" >&2
    i=$((i+2))
  done
  local n_sources=$(( ${#sources[@]} / 2 ))

  do_build

  echo -e "${GRAY}== lay out ${OPT_DIR} ==${NC}" >&2
  sudorun mkdir -p "$BIN_DIR" "$DATA_DIR" "$LOG_DIR"
  install_binary_atomically "$REPO_ROOT/bin/ai-viewer-ingest" "$BIN_DIR/ai-viewer-ingest"
  install_binary_atomically "$REPO_ROOT/bin/ai-viewer-serve" "$BIN_DIR/ai-viewer-serve"

  echo -e "${GRAY}== install systemd units (run-as ${op_user}, explicit sources) ==${NC}" >&2
  local src_flags=""
  for s in "${sources[@]}"; do src_flags+=" $(printf '%q' "$s")"; done
  local render_dir rendered_ingest rendered_serve
  render_dir="$(mktemp -d -t ai-viewer-units.XXXXXX)"
  rendered_ingest="$render_dir/ai-viewer-ingest.service"
  rendered_serve="$render_dir/ai-viewer-serve.service"
  render_unit "$REPO_ROOT/deploy/systemd-system/ai-viewer-ingest.service" "$rendered_ingest" "$op_user" "$op_group" "$src_flags"
  render_unit "$REPO_ROOT/deploy/systemd-system/ai-viewer-serve.service"  "$rendered_serve"  "$op_user" "$op_group" ""
  # Validate the rendered units (catches token misses / typos).
  run systemd-analyze verify "$rendered_ingest" "$rendered_serve"
  sudorun install -m 0644 "$rendered_ingest" "$UNIT_DIR/ai-viewer-ingest.service"
  sudorun install -m 0644 "$rendered_serve"  "$UNIT_DIR/ai-viewer-serve.service"
  rm -rf "$render_dir"
  sudorun systemctl daemon-reload

  echo -e "${GRAY}== stop active units before ownership repair ==${NC}" >&2
  stop_unit_if_active ai-viewer-serve.service
  stop_unit_if_active ai-viewer-ingest.service

  # Data dir owned by the operator (the ingester runs as them).
  sudorun chown -R "${op_user}:${op_group}" "$OPT_DIR"

  echo -e "${GRAY}== enable + start ==${NC}" >&2
  sudorun systemctl enable ai-viewer-ingest.service
  sudorun systemctl enable ai-viewer-serve.service
  sudorun systemctl start ai-viewer-ingest.service
  wait_for_ingester_active "post-start"
  sudorun systemctl start ai-viewer-serve.service

  echo -e "${GRAY}== waiting for server to come up ==${NC}" >&2
  local ok=""
  for _ in $(seq 1 20); do
    if curl -sf -o /dev/null "$URL"; then ok=1; break; fi
    sleep 1
  done
  if [[ -z "$ok" ]]; then
    echo -e "${YELLOW}[warn]${NC} server not responding at ${URL} after 20s." >&2
    echo -e "       Check: systemctl status ai-viewer-serve ai-viewer-ingest" >&2
    echo -e "             journalctl -u ai-viewer-serve -u ai-viewer-ingest --since '1 min ago'" >&2
    exit 1
  fi
  wait_for_ingester_active "post-server"

  echo >&2
  echo -e "${GREEN}install complete${NC}" >&2
  echo -e "UI:        ${GREEN}${URL}${NC}"
  echo -e "Operator:  ${op_user} (home ${home}) — service runs as this user"
  echo -e "Sources:   ${n_sources} explicit --source flags (see $UNIT_DIR/ai-viewer-ingest.service)"
  echo -e "Data:      ${DATA_DIR}/index.db"
  echo -e "Logs:      journalctl -u ai-viewer-ingest -u ai-viewer-serve -f"
  echo -e "Status:    scripts/install-system.sh status"
  echo -e "Uninstall: scripts/install-system.sh uninstall"
  echo "$URL"
}

do_uninstall() {
  require_sudo
  echo -e "${GRAY}== stop + disable units ==${NC}" >&2
  sudorun systemctl disable --now ai-viewer-serve.service 2>/dev/null || true
  sudorun systemctl disable --now ai-viewer-ingest.service 2>/dev/null || true
  echo -e "${GRAY}== remove unit files ==${NC}" >&2
  sudorun rm -f "$UNIT_DIR/ai-viewer-ingest.service" "$UNIT_DIR/ai-viewer-serve.service"
  sudorun systemctl daemon-reload
  echo -e "${GRAY}== remove ${OPT_DIR} (data is disposable; re-ingest recreates it) ==${NC}" >&2
  sudorun rm -rf "$OPT_DIR"
  echo -e "${GREEN}uninstall complete${NC}" >&2
}

do_status() {
  sudorun systemctl status ai-viewer-ingest.service --no-pager -l || true
  sudorun systemctl status ai-viewer-serve.service --no-pager -l || true
  echo
  if curl -sf -o /dev/null "$URL"; then
    echo -e "UI: ${GREEN}${URL}${NC} (responding)"
  else
    echo -e "UI: ${YELLOW}${URL}${NC} (not responding — check journalctl -u ai-viewer-ingest -u ai-viewer-serve)"
  fi
}

case "${1:-install}" in
  install)   do_install ;;
  uninstall) do_uninstall ;;
  status)    do_status ;;
  -h|--help|help)
    sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//'
    exit 0 ;;
  *) echo "usage: $0 [install|uninstall|status]" >&2; exit 2 ;;
esac
