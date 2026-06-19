#!/usr/bin/env bash
# Boot the PRE-BUILT ai-viewer-serve binary with deterministically seeded data,
# for Playwright E2E (frontend/playwright.config.ts §webServer).
#
# Pipeline (mirrors scripts/embed-smoke.sh §seed/boot; see
# .agents/sow/specs/deployment.md and SOW-0001 Chunk 18 D2/D3):
#   1. seed a schema'd temp DB by ingesting THREE committed fixtures
#      (happy_single_turn + multi_turn + sub_agent) so the UI renders a
#      multi-row sessions list, a detail with turns/ops, and a parent+children
#      session (exercises the SOW-0016 deep-link + the Overview child table);
#      poll the ingester log until EVERY source reports "adapter scan complete;
#      tail starting" (one line per source) so all fixtures have emitted their
#      events to the worker, then SIGTERM ONLY that ingester PID — Stop() runs the
#      final batch flush synchronously, committing the rows — and assert the
#      exact deterministic row invariants before booting the server.
#   2. exec ai-viewer-serve on 127.0.0.1:$PORT in the FOREGROUND so Playwright's
#      webServer owns (and can kill) the process. Because we exec, no EXIT trap
#      can fire here; the temp dir lives under $TMPDIR (mktemp -d) and is left
#      for the OS/temp reaper — it holds only the ephemeral test DB built from
#      already-sanitized fixtures, never the operator's real state dir.
#
# This script does NOT build: it requires bin/ai-viewer-serve AND
# bin/ai-viewer-ingest to already exist (run scripts/build.sh first; CI builds
# before the E2E step per Chunk 18 D1).
#
# Usage: scripts/e2e-serve.sh [PORT]   (PORT defaults to 7710)
# All paths are repo-relative (resolved from this script's own dir); the only
# argument is an optional port and the script contains no secrets/home-paths.
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

# Resolve repo root from the script's own location so this works from any CWD
# (Playwright invokes it with cwd=frontend/).
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

# The deterministic seed set (Chunk 18 D3). Absolute paths so --source resolves
# regardless of CWD.
FIXTURES=(
  "$REPO_ROOT/testdata/aiagent_v3/happy_single_turn/INPUT"
  "$REPO_ROOT/testdata/aiagent_v3/multi_turn/INPUT"
  "$REPO_ROOT/testdata/aiagent_v3/sub_agent/INPUT"
)
for fx in "${FIXTURES[@]}"; do
  if [[ ! -d "$fx" ]]; then
    echo -e "${RED}[ERROR]${NC} fixture missing: $fx" >&2
    exit 1
  fi
done

tmp="$(mktemp -d)"
db="$tmp/index.db"
state="$tmp/state"
mkdir -p "$state"

# Ingest all three fixtures in a SINGLE invocation: --source is a repeatable
# flag (cmd/ai-viewer-ingest repeatableFlag) and explicit --source flags replace
# auto-discovery, so the seed never scans a developer's real ~/.ai-agent. Info
# level surfaces both the per-source "adapter scan complete" line
# we gate on and any per-source parse error if a fixture is bad.
echo -e "${GRAY}seeding schema'd DB at${NC} $db ${GRAY}from${NC} ${#FIXTURES[@]} fixtures" >&2
"$INGEST_BIN" --db "$db" --state-dir "$state" --log-level info \
  --source "aiagent_v3:${FIXTURES[0]}" \
  --source "aiagent_v3:${FIXTURES[1]}" \
  --source "aiagent_v3:${FIXTURES[2]}" \
  > "$tmp/ingest.log" 2>&1 &
ing_pid=$!

# Wait until EVERY source has finished its initial scan. The ingester logs
# "adapter scan complete" exactly ONCE per source after Scan() has emitted all
# of that fixture's events to the worker (cmd/ai-viewer-ingest/sources.go); so
# #FIXTURES (3) occurrences proves every source emitted BEFORE we stop. This is
# the load-safe fix: gating on the schema/migration line only proved OpenWriter
# finished, so a SIGTERM under load could interrupt Scan() mid-fixture and
# commit a partial seed. Bounded ~15s (150 × 0.1s) with the same per-iteration
# PID-death bail.
want_scans="${#FIXTURES[@]}"
scan_msg='adapter scan complete'
# scan_count emits ONE clean integer: grep -c already prints 0 on no match and
# exits 1, so we only swallow that exit (a bare `|| echo 0` would append a second
# 0 and yield the two-line value "0\n0", which breaks the [[ -ge ]] arithmetic).
scan_count() { grep -c "$scan_msg" "$tmp/ingest.log" 2>/dev/null || true; }
scanned=0
for _ in $(seq 1 150); do
  if [[ "$(scan_count)" -ge "$want_scans" ]]; then
    scanned=1; break
  fi
  # Bail early if the ingester died before all scans completed.
  if ! kill -0 "$ing_pid" 2>/dev/null; then break; fi
  sleep 0.1
done
if [[ "$scanned" -ne 1 ]]; then
  echo -e "${RED}[ERROR]${NC} seed failed: only $(scan_count)/${want_scans} sources finished scanning" >&2
  cat "$tmp/ingest.log" >&2
  kill "$ing_pid" 2>/dev/null || true; wait "$ing_pid" 2>/dev/null || true
  exit 1
fi

# All sources have emitted; now request a CLEAN shutdown (SIGTERM) of ONLY this
# ingester and WAIT for it. The ingester cancels its adapters, drains the worker,
# and runs the final batch flush synchronously inside ing.Stop()
# (internal/ingest: Stop() -> wg.Wait(); TestStop_DrainsPendingBatch), so the
# clean exit GUARANTEES the emitted rows are committed.
kill -TERM "$ing_pid" 2>/dev/null || true
wait "$ing_pid" 2>/dev/null || true

# Verify the seed produced the EXACT deterministic shape the E2E specs rely on
# (read-only open so we never write a stray WAL/-journal into the temp DB;
# sqlite3 is already a repo dependency — scripts/refresh-pricing.sh). The fixtures
# are version-controlled, so the counts are FIXED and exact: 4 sessions (3 roots +
# 1 sub_agent child), 1 child row (deep-link/child-table coverage), 3
# source_progress rows (one per --source). The guard is EXACT, not >=, because
# both failure directions matter: too FEW = a partial seed (e.g. SIGTERM
# interrupted Scan() before a fixture flushed); too MANY = unexpected fixture
# drift or accidental duplication. Either must fail loudly so the E2E specs only
# ever run against the known-good shape.
if [[ ! -s "$db" ]]; then
  echo -e "${RED}[ERROR]${NC} seed produced no DB file at $db" >&2
  cat "$tmp/ingest.log" >&2
  exit 1
fi
EXPECT_SESSIONS=4
EXPECT_CHILDREN=1
EXPECT_SOURCES=3
sessions="$(sqlite3 "file:${db}?mode=ro" 'SELECT COUNT(*) FROM sessions;' 2>/dev/null || echo 0)"
children="$(sqlite3 "file:${db}?mode=ro" 'SELECT COUNT(*) FROM sessions WHERE parent_session_id IS NOT NULL;' 2>/dev/null || echo 0)"
sources="$(sqlite3 "file:${db}?mode=ro" 'SELECT COUNT(*) FROM source_progress;' 2>/dev/null || echo 0)"
if [[ "${sessions:-0}" -ne "$EXPECT_SESSIONS" || "${children:-0}" -ne "$EXPECT_CHILDREN" || "${sources:-0}" -ne "$EXPECT_SOURCES" ]]; then
  echo -e "${RED}[ERROR]${NC} partial/unexpected seed (exact counts required): sessions=${sessions} (want ${EXPECT_SESSIONS}), child sessions=${children} (want ${EXPECT_CHILDREN}), sources=${sources} (want ${EXPECT_SOURCES})" >&2
  cat "$tmp/ingest.log" >&2
  exit 1
fi
echo -e "${GREEN}seed ok${NC} (${sessions} sessions incl. ${children} child, ${sources} sources from ${#FIXTURES[@]} fixtures)" >&2

# Boot the serve binary on 127.0.0.1:$PORT in the FOREGROUND. exec replaces this
# shell so Playwright's webServer manages the process lifecycle directly and can
# terminate it cleanly on teardown. Warn level keeps E2E output quiet.
echo -e "${GRAY}exec${NC} $SERVE_BIN ${GRAY}on${NC} 127.0.0.1:$PORT" >&2
exec "$SERVE_BIN" --db "$db" --state-dir "$state" --log-level warn \
  --bind "127.0.0.1:$PORT"
