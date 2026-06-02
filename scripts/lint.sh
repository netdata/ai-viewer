#!/usr/bin/env bash
# Local mirror of the CI "Go — Lint" + "Go — Security" gates.
#
# Authoritative gate list: .agents/sow/specs/quality-gates.md "Go — Lint" /
# "Go — Security". Runtime companion: .agents/skills/project-quality-gates/SKILL.md.
#
# Runs, fail-fast, in this order:
#   1. golangci-lint run   — the umbrella gate (with formatters enabled it also
#                            enforces Go — Format, and `govet` covers Go — Vet),
#                            driven by .golangci.yml.
#   2. gosec               — standalone (a newer build than golangci's bundled
#                            copy; G-series analyzers), -severity/-confidence medium.
#   3. govulncheck         — known-CVE scan of the module + its call graph.
#
# Tool versions mirror .github/workflows/ci.yml exactly so local == CI:
#   - golangci-lint: pinned in .golangci-lint-version (single source).
#   - gosec: v2.26.1 (the version CI installs).
#   - govulncheck: latest (matches CI).
#
# Repo-relative; takes no arguments; contains no secrets.
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

GOSEC_VERSION="v2.26.1"
GOBIN="$(go env GOPATH)/bin"

# --- 1. golangci-lint (umbrella: fmt + vet + all enabled linters) -----------
PINNED_VERSION="$(tr -d '[:space:]' < .golangci-lint-version)"
if ! command -v golangci-lint >/dev/null 2>&1; then
  echo -e "${RED}[ERROR]${NC} golangci-lint not found on PATH." >&2
  echo -e "        Install the pinned version: ${YELLOW}go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${PINNED_VERSION}${NC}" >&2
  exit 1
fi
HAVE_VERSION="$(golangci-lint version 2>/dev/null | grep -oE 'version v?[0-9]+\.[0-9]+\.[0-9]+' | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
# Normalize both to a leading 'v' for comparison.
[[ "$HAVE_VERSION" == v* ]] || HAVE_VERSION="v${HAVE_VERSION}"
WANT_VERSION="$PINNED_VERSION"; [[ "$WANT_VERSION" == v* ]] || WANT_VERSION="v${WANT_VERSION}"
if [[ "$HAVE_VERSION" != "$WANT_VERSION" ]]; then
  echo -e "${YELLOW}[warn]${NC} golangci-lint ${HAVE_VERSION} != pinned ${WANT_VERSION} (.golangci-lint-version); CI uses the pinned version." >&2
fi
run golangci-lint run --timeout=5m

# --- 2. gosec (standalone) --------------------------------------------------
if [[ ! -x "${GOBIN}/gosec" ]]; then
  echo -e "${GRAY}installing gosec ${GOSEC_VERSION}...${NC}" >&2
  run go install "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}"
fi
run "${GOBIN}/gosec" -severity medium -confidence medium ./...

# --- 3. govulncheck ---------------------------------------------------------
if [[ ! -x "${GOBIN}/govulncheck" ]]; then
  echo -e "${GRAY}installing govulncheck (latest)...${NC}" >&2
  run go install golang.org/x/vuln/cmd/govulncheck@latest
fi
run "${GOBIN}/govulncheck" ./...

echo -e "${GREEN}[ok]${NC} lint.sh: golangci-lint + gosec + govulncheck all clean." >&2
