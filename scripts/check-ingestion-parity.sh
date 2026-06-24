#!/usr/bin/env bash
# Run the deterministic ingestion parity fixture gate.
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; GRAY='\033[0;90m'; NC='\033[0m'

run() {
  printf >&2 "${GRAY}$(pwd) >${NC} "
  printf >&2 "${YELLOW}"
  printf >&2 "%q " "$@"
  printf >&2 "${NC}\n"
  local ec=0
  "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then
    echo -e >&2 "${RED}[ERROR]${NC} exit ${ec}: $*"
    return "$ec"
  fi
}

usage() {
  cat >&2 <<'EOF'
Usage: scripts/check-ingestion-parity.sh --fixtures

Runs the source-to-canonical ingestion parity fixture gate.
EOF
}

if [[ "$#" -ne 1 || "${1:-}" != "--fixtures" ]]; then
  usage
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

run go test -count=1 ./internal/parity ./internal/ingest ./cmd/ai-viewer-ingest -run 'Parity|Source|Manifest|Diff|Canonical|Matrix|CheckParity'

parity_fuzz_targets="$(run go test -list='^Fuzz' ./internal/parity | grep '^Fuzz' | sort | tr '\n' ',')"
expected_parity_fuzz_targets='FuzzDiffManifests,FuzzExtractAIAgentV2Source,FuzzExtractAIAgentV3Source,FuzzExtractClaudeCodeSource,FuzzExtractCodexSource,FuzzExtractOpencodeSource,'
if [[ "$parity_fuzz_targets" != "$expected_parity_fuzz_targets" ]]; then
  echo -e >&2 "${RED}[ERROR]${NC} parity fuzz target set changed (got [${parity_fuzz_targets}] want [${expected_parity_fuzz_targets}])"
  exit 1
fi
run go test -run='^Fuzz' ./internal/parity

echo -e "${GREEN}[ok]${NC} ingestion parity fixture gate passed." >&2
