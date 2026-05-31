#!/usr/bin/env bash
# Scan shipped source for comments that attribute code to an external AI
# reviewer by name. Such attributions (a reviewer name next to an
# iteration/priority tag, or "per <reviewer>" / "<reviewer> flagged")
# are a breach of the no-AI-attribution rule on the public repo — the
# work stands on its own. This gate fails (non-zero) if any reappear, and
# passes (zero) when the tree is clean.
#
# Scope: cmd/, internal/, scripts/, frontend/src/, frontend/tests/ (tests
# INCLUDED — the attributions were in tests too; frontend/ is scoped to its
# source trees so node_modules/ and dist/ are never scanned). The match is
# deliberately narrow: a reviewer NAME adjacent
# to an iteration/priority tag or an attribution verb. It must NOT fire on
# legitimate DOMAIN uses of the same words — the session-storage formats
# the tool ingests, priced model names, the redaction rules, or the
# internal "Iter-N fix iterN-M" changelog labels (which carry no reviewer
# name). This script itself is excluded from the scan because it must
# enumerate the reviewer names in its pattern.
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

# Resolve repo root from the script's own location so the scan works from
# any CWD.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

# Reviewer names we have ever used for second-opinion review.
NAMES='codex|minimax|glm|qwen|kimi|mimo|gemini|deepseek'

# Attribution forms (case-insensitive):
#   <name> iter-N        a reviewer name followed by an iteration tag
#   <name> P<digit>      a reviewer name followed by a priority tag
#   per <name>           "per" followed by a reviewer name
#   pins <name>          "pins" followed by a reviewer name
#   <name> <verb>        a reviewer name followed by an attribution verb
# The reviewer name is REQUIRED in every alternative, so bare domain terms
# (a format row, a priced model name) never match.
PATTERN="(${NAMES})[ -]?(iter|P)[ -]?[0-9]"
PATTERN="${PATTERN}|(per|pins) (${NAMES})"
PATTERN="${PATTERN}|(${NAMES}) (flagged|caught|noted|found|suggested|wants|requires|review)"

# Scan only the shipped source trees. -I skips binary files; tests are
# intentionally included. This script is excluded so its own pattern
# definition (which must list the reviewer names) is not a self-hit.
hits="$(grep -rniIE --exclude=scan-ai-attribution.sh "$PATTERN" cmd internal scripts frontend/src frontend/tests || true)"

if [[ -n "$hits" ]]; then
  echo -e "${RED}[FAIL]${NC} AI-reviewer attribution comments found in shipped source:" >&2
  echo "$hits" >&2
  echo -e "${RED}Reword each comment to keep the technical reason and drop the reviewer name + issue id.${NC}" >&2
  exit 1
fi

echo -e "${GREEN}[PASS]${NC} no AI-reviewer attribution comments in cmd/, internal/, scripts/, frontend/."
