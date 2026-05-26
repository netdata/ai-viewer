#!/usr/bin/env bash
# Regenerate the synthetic ai-agent v2 testdata fixtures used by the
# aiagent_v2 adapter's golden tests.
#
# This is operator tooling only. CI does NOT invoke it; the .json.gz
# fixtures it produces are committed under testdata/aiagent_v2/. Run
# locally whenever the fixtures need to be refreshed after a mapper
# behaviour change. After regeneration, also rerun:
#
#   go test ./internal/adapters/aiagent_v2 -run TestGolden -update-golden
#
# to refresh the expected.jsonl files.
#
# Determinism: the fixtures are byte-for-byte stable because the
# generator pins all timestamps and UUIDs into compile-time constants.
#
# Safety: this script writes ONLY under testdata/aiagent_v2/. It never
# touches real operator data under ~/.ai-agent.
set -euo pipefail
IFS=$'\n\t'

cd "$(dirname "$0")/.."

go run ./internal/adapters/aiagent_v2/cmd/genfixtures
