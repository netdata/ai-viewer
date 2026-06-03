#!/usr/bin/env bash
# Build the single ai-viewer-serve binary that serves the UI.
#
# Pipeline (see .agents/sow/specs/deployment.md §"Build Pipeline"):
#   1. build the React SPA with Vite (frontend/dist/)
#   2. sync the build output into the go:embed dir, preserving the
#      tracked .gitkeep sentinel (cmd/ai-viewer-serve/frontend_dist/)
#   3. go build both binaries into bin/
#
# All paths are repo-relative (resolved from this script's own dir); the
# script takes no arguments and contains no secrets.
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

# Resolve repo root from the script's own location so the build works from
# any CWD.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

EMBED_DIR="cmd/ai-viewer-serve/frontend_dist"

# 1. Build the frontend. Prefer `npm ci` (reproducible, lockfile-driven);
#    fall back to `npm install` only when the lockfile is absent. Then enforce
#    the gzipped bundle-size budget on the just-built dist/ (the manifest exists
#    post-build): a local build fails fast on a budget violation, the same gate
#    CI runs after its build — so `scripts/build.sh` enforces the budget, not
#    just CI (quality-gates.md §Frontend — Bundle Size).
(
  run cd frontend
  if [[ -f package-lock.json ]]; then
    run npm ci
  else
    echo -e "${YELLOW}[warn]${NC} frontend/package-lock.json missing; using 'npm install'" >&2
    run npm install
  fi
  run npm run build
  run npm run check:bundle-size
)

# 2. Sync the WHOLE dist/ tree into the embed dir, keeping only the .gitkeep
#    sentinel. dist/ holds index.html, assets/ AND root public files Vite
#    copies from frontend/public/ (e.g. favicon.svg) that index.html
#    references at the site root — all must be embedded so the binary serves
#    the full SPA same-origin. Everything under EMBED_DIR is git-ignored build
#    output, so the working tree stays clean after the build. -maxdepth 1
#    keeps the cleanup to the dir's own top level (the only .gitkeep we ever
#    keep), so a stale nested file named .gitkeep cannot survive and we never
#    rm a dir while find is still traversing into it.
run find "$EMBED_DIR" -mindepth 1 -maxdepth 1 ! -name .gitkeep -exec rm -rf -- {} +
# Copy dist/ contents (not the dir itself) so they land directly under
# EMBED_DIR alongside .gitkeep. The trailing /. copies the directory's
# contents including any dotfiles Vite might emit.
run cp -R frontend/dist/. "$EMBED_DIR/"

# 3. Build both static binaries into bin/.
run mkdir -p bin
run go build -o bin/ai-viewer-serve ./cmd/ai-viewer-serve
run go build -o bin/ai-viewer-ingest ./cmd/ai-viewer-ingest

echo -e "${GREEN}build complete${NC}" >&2
echo "$REPO_ROOT/bin/ai-viewer-serve"
echo "$REPO_ROOT/bin/ai-viewer-ingest"
