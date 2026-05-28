#!/usr/bin/env bash
# Development loop: run the Vite dev server and ai-viewer-serve together.
#
# The Go server owns /api + SSE on 127.0.0.1:7710; the Vite dev server
# serves the live UI and proxies /api to 127.0.0.1:7710 (see
# frontend/vite.config.ts), so the same relative fetch URLs work in dev
# and in the single-binary build. In this mode the Go binary usually has
# no built UI embedded and serves the not-built notice at / — the live UI
# is the Vite dev server. See .agents/sow/specs/deployment.md §"dev.sh".
#
# Both children are tracked by PID and killed on exit; this script never
# uses pkill/killall and only signals the specific PIDs it started.
set -euo pipefail

# Colors for transparent command tracing.
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; GRAY='\033[0;90m'; NC='\033[0m'
run() {
  printf >&2 "${GRAY}$(pwd) >${NC} "; printf >&2 "${YELLOW}"; printf >&2 "%q " "$@"; printf >&2 "${NC}\n"
  # Capture the command's own exit code directly (NOT via `if ! "$@"`, which
  # would make $? the negated-expression status — always 0 — and silently mask
  # failures under `set -e`).
  local ec=0; "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then echo -e >&2 "${RED}[ERROR]${NC} exit ${ec}: $*"; return "$ec"; fi
}

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

# PIDs of the children we start, so cleanup signals only those.
SERVE_PID=""
VITE_PID=""
# Temp build dir for the dev server binary (see below); removed on exit.
DEV_TMP="$(mktemp -d)"

cleanup() {
  # Kill ONLY the specific PIDs this script started (never pkill/killall).
  for pid in "$SERVE_PID" "$VITE_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      run kill "$pid" 2>/dev/null || true
    fi
  done
  rm -rf "$DEV_TMP"
}
trap cleanup EXIT INT TERM

# Build the server to a temp binary and run THAT directly, rather than
# `go run`. `go run` compiles to its own temp binary and execs it as a child,
# so $! would be the `go` wrapper PID — killing it does not reliably stop the
# server bound to 127.0.0.1:7710. Tracking the real binary's PID guarantees
# cleanup actually frees the port.
echo -e "${GREEN}building dev server binary${NC}" >&2
run go build -o "$DEV_TMP/ai-viewer-serve" ./cmd/ai-viewer-serve

echo -e "${GREEN}starting ai-viewer-serve on 127.0.0.1:7710${NC}" >&2
"$DEV_TMP/ai-viewer-serve" "$@" &
SERVE_PID=$!

# Start the Vite dev server in frontend/ and record PID. `exec` replaces the
# subshell with vite itself, so $! is the real vite PID (not an npm/subshell
# wrapper whose children would survive a kill of the wrapper — the same class
# of bug avoided above for the Go server). Invoke vite's binary directly.
echo -e "${GREEN}starting vite dev server (proxying /api -> 127.0.0.1:7710)${NC}" >&2
( cd frontend && exec ./node_modules/.bin/vite ) &
VITE_PID=$!

# Wait for either child to exit, then the EXIT trap cleans up the other.
wait -n "$SERVE_PID" "$VITE_PID"
