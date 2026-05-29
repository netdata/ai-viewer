#!/usr/bin/env bash
# Smoke-test the single ai-viewer-serve binary: prove it serves the REAL
# built UI same-origin with /api.
#
# Pipeline (see .agents/sow/specs/deployment.md §"Build Pipeline" and
# presenter.md §"serveIndex contract"):
#   1. seed a schema'd temp DB by running ai-viewer-ingest against an empty
#      source until its last migration logs, then kill ONLY that ingester
#   2. boot ai-viewer-serve against the seeded DB on 127.0.0.1:$PORT
#   3. curl /api/health, GET /, the hashed asset, and /favicon.svg and assert
#      the served UI is the real build (hashed asset ref, long-cache), NOT the
#      not-built notice
#
# Prerequisites: bin/ai-viewer-serve and bin/ai-viewer-ingest must already
# exist — run scripts/build.sh first. CI calls this after the build step; a
# local run does `bash scripts/build.sh && bash scripts/embed-smoke.sh`.
#
# Usage: scripts/embed-smoke.sh [PORT]   (PORT defaults to 7710)
# All paths are repo-relative (resolved from this script's own dir); the only
# argument is an optional port and the script contains no secrets.
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

# Resolve repo root from the script's own location so the smoke works from any
# CWD.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

PORT="${1:-7710}"
SERVE_BIN="bin/ai-viewer-serve"
INGEST_BIN="bin/ai-viewer-ingest"

# The binaries are build output, not built here: fail with a clear pointer if
# the caller skipped scripts/build.sh.
for bin in "$SERVE_BIN" "$INGEST_BIN"; do
  if [[ ! -x "$bin" ]]; then
    echo -e "${RED}[ERROR]${NC} $bin not found; run scripts/build.sh first." >&2
    exit 1
  fi
done

tmp="$(mktemp -d)"
db="$tmp/index.db"
emptysrc="$tmp/empty-source"
mkdir -p "$emptysrc"

# Seed a schema'd DB: ai-viewer-serve refuses to start unless the store carries
# the expected schema_meta.version (CheckSchema at startup). The ingester runs
# all migrations synchronously inside OpenWriter and logs each "migration
# applied" before it does anything else; the last migration (0004_notify.sql)
# sets the version this binary expects. Poll for that line — it is emitted on a
# fresh DB regardless of source discovery, so we never kill the process
# mid-migration (a bare file-exists check is race-prone: OpenWriter creates the
# file before migrations finish). An explicit --source on an empty dir bypasses
# auto-discovery, so the seed never scans a developer's real ~/.ai-agent on a
# local run. Info level so the migration lines are emitted.
echo -e "${GRAY}seeding schema'd DB at${NC} $db" >&2
"$INGEST_BIN" --db "$db" --state-dir "$tmp" --log-level info \
  --source "aiagent_v3:$emptysrc" > "$tmp/ingest.log" 2>&1 &
ing_pid=$!
seeded=0
for _ in $(seq 1 100); do
  if grep -q '0004_notify.sql' "$tmp/ingest.log"; then
    seeded=1; break
  fi
  # Bail early if the ingester died before migrations completed.
  if ! kill -0 "$ing_pid" 2>/dev/null; then break; fi
  sleep 0.1
done
# Kill ONLY the ingester PID we started.
kill "$ing_pid" 2>/dev/null || true
wait "$ing_pid" 2>/dev/null || true
if [[ "$seeded" -ne 1 || ! -s "$db" ]]; then
  echo -e "${RED}[ERROR]${NC} seed failed: ingester did not finish migrations" >&2
  cat "$tmp/ingest.log" >&2
  exit 1
fi
echo -e "${GREEN}seed ok${NC} (schema migrations applied)" >&2

# Boot the serve binary on 127.0.0.1:$PORT.
echo -e "${GRAY}booting${NC} $SERVE_BIN ${GRAY}on${NC} 127.0.0.1:$PORT" >&2
"$SERVE_BIN" --db "$db" --state-dir "$tmp" --log-level warn \
  --bind "127.0.0.1:$PORT" > "$tmp/serve.log" 2>&1 &
serve_pid=$!
# Always kill ONLY the serve PID we started, on any exit.
trap 'kill "$serve_pid" 2>/dev/null || true' EXIT

base="http://127.0.0.1:$PORT"
ready=0
for _ in $(seq 1 100); do
  if curl -fsS "$base/api/health" >/dev/null 2>&1; then
    ready=1; break
  fi
  if ! kill -0 "$serve_pid" 2>/dev/null; then break; fi
  sleep 0.1
done
if [[ "$ready" -ne 1 ]]; then
  echo -e "${RED}[ERROR]${NC} server did not become ready on $base" >&2
  cat "$tmp/serve.log" >&2
  exit 1
fi

# /api/health must be 200.
curl -fsS -o /dev/null -w 'GET /api/health -> HTTP %{http_code}\n' \
  "$base/api/health"

# GET / must return the REAL built index.html, i.e. reference a hashed
# /assets/index-*.{js,css} bundle, NOT the not-built notice.
body="$(curl -fsS "$base/")"
asset="$(printf '%s' "$body" | grep -oE '/assets/index-[A-Za-z0-9_-]+\.js' | head -1)"
if [[ -z "$asset" ]]; then
  echo -e "${RED}[ERROR]${NC} GET / did not reference a hashed /assets/index-*.js bundle:" >&2
  printf '%s\n' "$body" >&2
  exit 1
fi
if printf '%s' "$body" | grep -q 'scripts/build.sh'; then
  echo -e "${RED}[ERROR]${NC} GET / served the not-built notice instead of the real UI" >&2
  exit 1
fi
echo "GET / -> real index.html referencing $asset"

# The hashed asset must be 200 with a long immutable cache.
headers="$(curl -fsS -D - -o /dev/null "$base$asset")"
printf '%s' "$headers" | grep -qE '^HTTP/[0-9.]+ 200' || {
  echo -e "${RED}[ERROR]${NC} asset $asset did not return 200:" >&2; printf '%s\n' "$headers" >&2; exit 1; }
printf '%s' "$headers" | grep -qiE 'cache-control:.*max-age=31536000' || {
  echo -e "${RED}[ERROR]${NC} asset $asset missing long max-age cache header:" >&2; printf '%s\n' "$headers" >&2; exit 1; }
echo "GET $asset -> 200 with long-cache header"

# Root public assets Vite copies to dist/ root (referenced by index.html at the
# site root) must be served too. favicon.svg is the Phase 1 case; assert it is
# 200 (the build copies the whole dist/).
curl -fsS -o /dev/null -w 'GET /favicon.svg -> HTTP %{http_code}\n' \
  "$base/favicon.svg"

# SPA deep-link fallback (presenter.md §"SPA fallback"): a hard navigation to a
# client-side route must serve the REAL built shell (referencing a hashed
# /assets/index-*.js bundle), NOT the not-built notice and NOT a JSON 404. This
# is what makes reload/bookmark of /sessions/<id> load the app.
route_body="$(curl -fsS "$base/sessions/smoke-test-id")"
if ! printf '%s' "$route_body" | grep -qE '/assets/index-[A-Za-z0-9_-]+\.js'; then
  echo -e "${RED}[ERROR]${NC} GET /sessions/smoke-test-id did not serve the real shell (no hashed /assets/index-*.js):" >&2
  printf '%s\n' "$route_body" >&2
  exit 1
fi
if printf '%s' "$route_body" | grep -q 'scripts/build.sh'; then
  echo -e "${RED}[ERROR]${NC} GET /sessions/smoke-test-id served the not-built notice instead of the real UI" >&2
  exit 1
fi
echo "GET /sessions/smoke-test-id -> real index.html shell (SPA deep-link fallback)"

# The fallback must NOT swallow unknown /api/* paths: they stay structured JSON
# 404 (NOT_FOUND envelope, Content-Type application/json), never the HTML shell
# (presenter.md §"SPA fallback": /api/* is exempt). Assert status AND content-type
# AND body so a shell leaking into /api/* (200 html, or 404 html) is caught — a
# bare status check would pass even if the shell were served. No -f: a 404 is the
# expected, healthy outcome here.
api_resp="$(curl -s -i "$base/api/this-route-does-not-exist")"
if ! printf '%s' "$api_resp" | grep -qE '^HTTP/[0-9.]+ 404'; then
  echo -e "${RED}[ERROR]${NC} GET /api/this-route-does-not-exist did not return 404:" >&2
  printf '%s\n' "$api_resp" >&2
  exit 1
fi
if ! printf '%s' "$api_resp" | grep -qiE '^content-type: *application/json'; then
  echo -e "${RED}[ERROR]${NC} unknown /api/* 404 was not application/json (SPA shell may be leaking into /api/*):" >&2
  printf '%s\n' "$api_resp" >&2
  exit 1
fi
if ! printf '%s' "$api_resp" | grep -q 'NOT_FOUND'; then
  echo -e "${RED}[ERROR]${NC} unknown /api/* 404 body missing the NOT_FOUND envelope:" >&2
  printf '%s\n' "$api_resp" >&2
  exit 1
fi
echo "GET /api/this-route-does-not-exist -> 404 application/json NOT_FOUND (not swallowed by SPA fallback)"

echo -e "${GREEN}embed-smoke passed${NC} (single binary serves the real built UI same-origin with /api)" >&2
