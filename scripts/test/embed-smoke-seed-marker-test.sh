#!/usr/bin/env bash
# Regression pin for SOW-0007 → SOW-0010: scripts/embed-smoke.sh must seed its
# temp DB by waiting for the ACTUAL last migration, derived dynamically from
# internal/store/migrations/, NOT a hardcoded earlier migration filename.
#
# The bug: embed-smoke.sh hardcoded `grep -q '0005_op_duration_backfill.sql'`
# and killed the ingester the moment 0005 logged. When SOW-0007 added migrations
# 0006 + 0007 (bumping the schema_meta.version serve's CheckSchema requires to 7),
# the seed kept stopping at 0005 → seeded an un-finished v5 DB → ai-viewer-serve
# refused to boot ("schema version mismatch ... is 5, want 7"). It surfaced as a
# flaky embed-smoke (the kill usually lost the race to 0006/0007, but not always).
#
# This guard fails if the seed marker is a hardcoded migration filename again, and
# requires the script to derive it from the migrations directory so it can never
# drift when a new migration lands. The embed-smoke CI gate itself is the
# behavioral integration test (it boots serve against the seeded DB); this is the
# fast, dependency-free guard against re-introducing a static marker.
#
# Run: scripts/test/embed-smoke-seed-marker-test.sh   (exit 0 = pass)
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SMOKE="${REPO_ROOT}/scripts/embed-smoke.sh"
MIGRATIONS="${REPO_ROOT}/internal/store/migrations"
fail=0

# 1) No hardcoded `000N_*.sql` migration filename literal in ACTIVE (non-comment)
#    code — that is the stale marker that caused the regression. Comment lines
#    (which document the historical bug by name) are stripped first.
if grep -vE '^[[:space:]]*#' "$SMOKE" | grep -nE "0[0-9]{3}_[a-z0-9_]+\.sql"; then
  echo -e "  ${RED}FAIL${NC}: embed-smoke.sh hardcodes a migration filename in active code — the seed marker must be derived dynamically (it drifts when a new migration lands)." >&2
  fail=1
else
  echo -e "  ${GREEN}PASS${NC}: no hardcoded migration filename in embed-smoke.sh active code"
fi

# 2) ACTIVE code (comments stripped) must derive the marker from the migrations
#    directory AND the seed poll must grep for that derived variable. Checking
#    active code only means the guard still fails if the dynamic derivation is
#    removed and replaced with a non-hardcoded-but-wrong early marker (e.g.
#    `grep -q 'migration applied'`), even though a comment still names the dir.
active="$(grep -vE '^[[:space:]]*#' "$SMOKE")"
if printf '%s\n' "$active" | grep -q 'internal/store/migrations/.*\.sql' \
   && printf '%s\n' "$active" | grep -q 'last_migration='; then
  echo -e "  ${GREEN}PASS${NC}: active code derives the last migration from internal/store/migrations/"
else
  echo -e "  ${RED}FAIL${NC}: active code must derive the seed marker from internal/store/migrations/ (assign \$last_migration)." >&2
  fail=1
fi
if printf '%s\n' "$active" | grep -qE 'grep .*"\$last_migration"'; then
  echo -e "  ${GREEN}PASS${NC}: the seed poll greps for the dynamically-derived \$last_migration"
else
  echo -e "  ${RED}FAIL${NC}: the seed poll must grep for the derived \$last_migration, not a literal or non-specific marker." >&2
  fail=1
fi

# 3) Sanity: the migrations dir is non-empty and its last entry is what serve expects.
last="$(basename "$(ls "$MIGRATIONS"/*.sql | sort | tail -1)")"
if [[ -n "$last" ]]; then
  echo -e "  ${GREEN}PASS${NC}: last migration resolves to ${last}"
else
  echo -e "  ${RED}FAIL${NC}: could not resolve the last migration in ${MIGRATIONS}" >&2
  fail=1
fi

if [[ "$fail" -eq 0 ]]; then
  echo -e "${GREEN}[ok]${NC} embed-smoke seed-marker guard: pass."
else
  echo -e "${RED}[FAIL]${NC} embed-smoke seed-marker guard." >&2
  exit 1
fi
