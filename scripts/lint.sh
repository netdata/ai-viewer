#!/usr/bin/env bash
# Local mirror of the CI static-analysis gates: the "Go — Lint" + "Go — Security"
# steps of the `lint` job, plus the build-free frontend static gates of the
# `frontend` job.
#
# Authoritative gate list: .agents/sow/specs/quality-gates.md "Go — Lint" /
# "Go — Security" / "Frontend — Lint" / "Frontend — Type Check". Runtime
# companion: .agents/skills/project-quality-gates/SKILL.md.
#
# This is the BUILD-FREE static-analysis entrypoint: nothing here compiles a
# shippable artifact (no `go build`, no `vite build`). Both the Go section and
# the frontend section run only analysis + the gate-logic self-tests. The REAL
# bundle-size-vs-built-manifest gate (`npm run check:bundle-size`) and the REAL
# coverage run (`npm run test -- --run --coverage`) need a build / full test run
# and therefore live in CI's `frontend` job + scripts/{build,test}.sh, NOT here;
# this script runs their hermetic SELF-TESTS, which verify the gate LOGIC.
#
# Runs, fail-fast, in this order:
#   Go section:
#   1. golangci-lint run   — the umbrella gate (with formatters enabled it also
#                            enforces Go — Format, and `govet` covers Go — Vet),
#                            driven by .golangci.yml.
#   2. gosec               — standalone (a newer build than golangci's bundled
#                            copy; G-series analyzers), -severity/-confidence medium.
#   3. govulncheck         — known-CVE scan of the module + its call graph.
#   Frontend section (build-free; skipped if frontend/ is absent):
#   4. ensure deps present — reuse scripts/build.sh's npm ci / npm install
#                            fallback, but only when node_modules is missing
#                            (this is a fast static-analysis pass, not a build).
#   5. npm run lint        — eslint flat config (the `lint` npm script bakes in
#                            --max-warnings=0; not re-passed here).
#   6. npm run typecheck   — tsc --noEmit (strict).
#   7. bundle-size self-test     — hermetic; verifies the bundle-size GATE LOGIC.
#   8. coverage-config verifier  — checks the REAL Vitest per-dir floors against
#                                  the source tree (non-vacuity + lockstep);
#                                  build-free (node:fs only).
#   8b. coverage-config self-test — hermetic; verifies the verifier's OWN logic
#                                   (vacuity / lockstep / broad-glob / .d.ts-only).
#   9. coverage-thresholds self-test — hermetic; verifies the per-dir coverage
#                                      GATE LOGIC.
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
# Resolve the install dir the way `go install` does: GOBIN if set, else
# GOPATH/bin. Hardcoding GOPATH/bin would run a stale/missing binary when the
# developer has GOBIN set elsewhere.
GOBIN="$(go env GOBIN)"; [ -n "$GOBIN" ] || GOBIN="$(go env GOPATH)/bin"

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
# Intentional WARN (not auto-install): golangci-lint is the developer's own
# primary tool and is heavy to reinstall over; CI enforces the exact pin
# authoritatively via golangci-lint-action. gosec/govulncheck below ARE
# auto-(re)installed at the pin because lint.sh manages them and they are cheap.
if [[ "$HAVE_VERSION" != "$WANT_VERSION" ]]; then
  echo -e "${YELLOW}[warn]${NC} golangci-lint ${HAVE_VERSION} != pinned ${WANT_VERSION} (.golangci-lint-version); CI enforces the pin via golangci-lint-action — re-pin locally if results differ." >&2
fi
run golangci-lint run --timeout=5m

# --- 2. gosec (standalone, pinned) ------------------------------------------
# Always (re)install the pinned version — exactly as CI does. A `go install`d
# gosec self-reports "dev" (release version ldflags are not injected), so the
# binary's version cannot be verified after the fact; pinning at install time
# is the only reliable guarantee. The install is fast once the module+build
# caches are warm, and it overwrites any stale gosec a dev may already have.
echo -e "${GRAY}installing pinned gosec ${GOSEC_VERSION}...${NC}" >&2
run go install "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}"
run "${GOBIN}/gosec" -severity medium -confidence medium ./...

# --- 3. govulncheck (latest, as CI) -----------------------------------------
# Always install latest — the CVE database tooling should be current, and CI
# does the same. Idempotent + fast when cached.
echo -e "${GRAY}installing govulncheck (latest)...${NC}" >&2
run go install golang.org/x/vuln/cmd/govulncheck@latest
run "${GOBIN}/govulncheck" ./...

echo -e "${GREEN}[ok]${NC} Go section: golangci-lint + gosec + govulncheck all clean." >&2

# === Frontend static-analysis section (build-free, fail-fast) ===============
# Spec-first repos may not have a frontend on every commit; skip cleanly when
# absent (mirrors CI's `frontend`-job presence check) rather than erroring.
if [[ ! -f frontend/package.json ]]; then
  echo -e "${GRAY}no frontend/package.json — skipping frontend static gates.${NC}" >&2
else
  (
    run cd frontend

    # --- 4. ensure deps present -----------------------------------------------
    # Reuse scripts/build.sh's install pattern (npm ci when the lockfile is
    # present, else npm install), but only when node_modules is MISSING: this is
    # the fast static-analysis entrypoint, so we never reinstall on a warm tree.
    # The self-tests below need frontend/node_modules/.bin (eslint, tsc, vitest).
    if [[ ! -d node_modules ]]; then
      if [[ -f package-lock.json ]]; then
        run npm ci
      else
        echo -e "${YELLOW}[warn]${NC} frontend/package-lock.json missing; using 'npm install'" >&2
        run npm install
      fi
    fi

    # --- 5. eslint (flat config, zero warnings) -------------------------------
    # The package.json `lint` script already bakes in `--max-warnings=0` (single
    # source of truth), so we do NOT re-pass it here (it would be a redundant
    # doubled flag). CI's `frontend` job passes it explicitly via `npm run lint --
    # --max-warnings=0` for legibility; both resolve to the same zero-warnings run.
    run npm run lint

    # --- 6. tsc --noEmit (strict type check) ----------------------------------
    run npm run typecheck

    # --- 7. bundle-size GATE-LOGIC self-test (hermetic, build-free) -----------
    # The REAL bundle-size gate (npm run check:bundle-size) classifies chunks
    # from a BUILT dist/.vite/manifest.json and runs in CI's frontend job + after
    # the vite build; it is intentionally NOT here. This self-test builds its own
    # synthetic dist fixtures and verifies the gate's logic (all three exit codes).
    run npm run check:bundle-size:selftest

    # --- 8. coverage-config verifier (REAL config, build-free) ----------------
    # Verifies the ACTUAL Vitest per-dir floors against the source tree: every
    # per-dir glob matches >= 1 real file (no vacuous "Unknown" pass) and every
    # measured component/page dir has a per-dir floor (lockstep). Reads the same
    # shared lists vitest.config.ts imports, so it checks the gate Vitest enforces.
    # Distinct from the gate-mechanism self-test below (which uses a fixture).
    run npm run check:coverage-config

    # --- 8b. coverage-config verifier SELF-TEST (hermetic, build-free) --------
    # Proves the verifier's decision LOGIC still fires (vacuity / lockstep /
    # unsupported broad-glob / .d.ts-only) against a throwaway fixture tree, so a
    # regression in the verifier is caught even though the real config is sound.
    run npm run check:coverage-config:selftest

    # --- 9. per-dir coverage GATE-LOGIC self-test (hermetic, build-free) -------
    # The REAL coverage run (npm run test -- --run --coverage, with native
    # per-dir thresholds) needs the full test suite and runs in CI's frontend
    # job; it is intentionally NOT here. This self-test drives the installed
    # Vitest on a throwaway fixture project to prove the per-dir threshold wiring
    # fails closed under-floor and passes above.
    run npm run check:coverage-thresholds:selftest
  )
  echo -e "${GREEN}[ok]${NC} Frontend section: eslint + tsc + bundle-size self-test + coverage-config verifier + coverage-config self-test + coverage gate self-test all clean." >&2
fi

echo -e "${GREEN}[ok]${NC} lint.sh: Go + frontend static analysis all clean." >&2
