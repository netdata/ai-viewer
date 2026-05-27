#!/usr/bin/env bash
# Run the ai-agent v2 backfill benchmark against the operator's real
# session directory.
#
# This script is operator tooling, not CI. It runs the in-package
# Go harness (internal/adapters/aiagent_v2/cmd/backfillbench) which
# reads ONLY (O_RDONLY) from --root, drains canonical events into
# memory counters (no SQLite, no ingester) and prints a SOW-0001
# Chunk 9 perf summary at the end.
#
# Usage:
#
#   ./scripts/bench-v2-backfill.sh [root] [workers]
#
# Defaults:
#
#   root    = "$HOME/.ai-agent/sessions"
#   workers = nproc on the host
#
# Read-only contract: the harness never writes/deletes/renames under
# --root. This wrapper does not touch --root at all; it only passes
# the value through to the Go program.
set -euo pipefail
IFS=$'\n\t'

cd "$(dirname "$0")/.."

ROOT="${1:-${HOME}/.ai-agent/sessions}"
WORKERS="${2:-$(nproc)}"

echo "bench-v2-backfill: root=${ROOT} workers=${WORKERS}"
go run ./internal/adapters/aiagent_v2/cmd/backfillbench \
    --root "${ROOT}" \
    --workers "${WORKERS}" \
    --progress-interval 5s
