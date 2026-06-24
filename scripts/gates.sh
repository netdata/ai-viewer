#!/usr/bin/env bash
# The canonical aggregate gate: run EVERY quality gate in order, fail-fast.
#
# Authoritative catalog: .agents/sow/specs/quality-gates.md. Runtime companion:
# .agents/skills/project-quality-gates/SKILL.md. This is the single command the
# operator runs before every commit; CI enforces the same gates as parallel
# jobs (.github/workflows/ci.yml) so local and CI cannot diverge
# (quality-gates.md §"CI Workflow Mirror Invariant").
#
# It composes the existing per-gate scripts rather than re-implementing them, so
# there is one source of truth per gate:
#   1. scripts/test/lint-test.sh        tracked-file formatter-scope self-test.
#   2. scripts/lint.sh                  Go module tidy + tracked formatters +
#                                       vet + golangci-lint + gosec +
#                                       govulncheck, plus frontend static checks.
#   3. scripts/scan-secrets.sh + its self-test   secrets + operator-PII (fail-closed).
#   4. scripts/scan-ai-attribution.sh   no-AI-attribution rule on the public repo.
#   5. scripts/spec-drift.sh + its self-test     the 5 spec↔code drift indicators.
#   6. scripts/test/check-ingestion-parity-test.sh + scripts/check-ingestion-parity.sh --fixtures
#                                       deterministic ingestion parity fixture gate.
#   7. scripts/test/codacy-coverage-upload-test.sh   Codacy coverage upload self-test.
#   8. scripts/test/codacy-config-test.sh   Codacy tool/pattern + path policy self-test.
#   9. scripts/test/systemd-units-test.sh   systemd unit contract (when present).
#  10. scripts/build.sh                 frontend build + REAL bundle-size gate +
#                                       embed + both Go binaries.
#  11. scripts/test/check-bench-test.sh + scripts/check-bench.sh   benchmark
#                                       gate self-test + local regression gate.
#  12. scripts/test.sh + scripts/check-coverage.sh   Go -race suite + statement
#                                       coverage gate + frontend Vitest.
#  13. deterministic adapter fuzz seed corpus + target-set lock.
#  14. frontend Playwright E2E (includes axe a11y specs) against the built binary.
#
# ORDERING: fast static gates first so a quick failure surfaces early. The
# benchmark regression gate runs after the build while the workstation is still
# relatively cool; it is a local performance signal and must not be distorted by
# the long CPU-heavy -race suite or Playwright run. Thermal-heavy correctness
# gates run after the benchmark. Each section prints a header and its own
# wall-clock; a final summary prints the per-section + total time.
#
# PERFORMANCE (quality-gates.md §Performance Target): the target is < 5 min on
# the operator's workstation, but the Go `-race` suite alone is the long pole
# and pushes the full aggregate ABOVE that — see the MEASURED note below. The
# gate is kept COMPLETE; no gate is dropped to hit the target. A `--fast`
# profile / internal parallelization is a tracked follow-up SOW, never a reason
# to weaken a gate.
#
#   MEASURED: see SOW-0013 validation for per-run measurements. The exact total
#   varies with cold/warm caches and workstation load, but the conclusion stays
#   the same: the aggregate is the complete workstation gate, not the fast loop.
#   The < 5 min target is therefore NOT met today; this is documented, not
#   masked. Run `scripts/lint.sh` alone for a ~sub-minute static-only pass while
#   iterating; run full `gates.sh` before commit. The follow-up SOW
#   (.agents/sow/pending/) tracks a `--fast` profile / parallelization; no gate
#   is dropped to chase the target.
#
# Repo-relative; takes no arguments; contains no secrets.
set -euo pipefail

# Colors as real escape bytes ($'...' ANSI-C quoting) so they render when passed
# through printf '%s' (the format-string position is never colored, which keeps
# the SC2059 check clean — mirrors scan-secrets.sh). Collapse to empty when
# stderr is not a TTY (CI logs / captured output stay plain — no literal \033).
if [[ -t 2 ]]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; GRAY=$'\033[0;90m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; GRAY=""; CYAN=""; NC=""
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Section bookkeeping: parallel arrays of name + measured seconds, in run order.
SECTION_NAMES=()
SECTION_SECS=()
START_TS=$SECONDS

# section <label> <command...> — print a header, run the command (streaming its
# output), record its wall-clock, and on failure abort the whole aggregate
# fail-fast (the failing section's own output is the diagnosis). Uses `time`
# semantics via $SECONDS deltas (whole seconds — adequate for minute-scale
# gates and dependency-free).
section() {
  local label="$1"; shift
  local t0=$SECONDS
  printf '%s\n' "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}" >&2
  printf '%s▶ %s%s\n' "$CYAN" "$label" "$NC" >&2
  printf '%s\n' "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}" >&2
  local ec=0
  "$@" || ec=$?
  local dt=$((SECONDS - t0))
  SECTION_NAMES+=("$label")
  SECTION_SECS+=("$dt")
  if [[ "$ec" -ne 0 ]]; then
    printf '%s✗ %s FAILED (exit %s, %ds)%s\n' "$RED" "$label" "$ec" "$dt" "$NC" >&2
    print_summary "$label"
    exit "$ec"
  fi
  printf '%s✓ %s (%ds)%s\n' "$GREEN" "$label" "$dt" "$NC" >&2
}

# print_summary [failed-label] — per-section timing table + total. When a label
# is passed, marks it as the failing section.
print_summary() {
  local failed="${1:-}"
  local total=$((SECONDS - START_TS))
  printf '\n%s──────── gates.sh summary ────────%s\n' "$GRAY" "$NC" >&2
  local i
  for i in "${!SECTION_NAMES[@]}"; do
    local mark="  "
    [[ -n "$failed" && "${SECTION_NAMES[$i]}" == "$failed" ]] && mark="${RED}✗ ${NC}"
    printf '%s%-44s %4ds\n' "$mark" "${SECTION_NAMES[$i]}" "${SECTION_SECS[$i]}" >&2
  done
  printf '%s%-44s %4ds%s\n' "$GRAY" "TOTAL" "$total" "$NC" >&2
  if (( total > 300 )); then
    printf '%s[note]%s total %ds exceeds the 5-min target — the -race suite is the long pole (see header; SOW-0013 records per-run validation).%s\n' \
      "$YELLOW" "$NC" "$total" "$NC" >&2
  fi
}

run_fuzz_seed_gate() {
  go test -run='^Fuzz' ./internal/adapters/... || return $?

  local fuzz_lines="" got p target targets want
  for p in aiagent_v2 aiagent_v3 claude_code codex opencode; do
    targets="$(go test -list='^Fuzz' "./internal/adapters/${p}/")" || return $?
    while IFS= read -r target; do
      [[ "$target" == Fuzz* ]] || continue
      fuzz_lines+="${p}:${target}"$'\n'
    done <<< "$targets"
  done
  got="$(printf '%s' "$fuzz_lines" | sort | tr '\n' ',')" || return $?
  want='aiagent_v2:FuzzParseCursor,aiagent_v2:FuzzParseSnapshot,aiagent_v3:FuzzParseCursor,aiagent_v3:FuzzParseLine,claude_code:FuzzParseCursor,claude_code:FuzzParseLine,codex:FuzzParseCursor,codex:FuzzParseLine,opencode:FuzzDecodeMessageData,opencode:FuzzDecodePartData,'
  if [[ "$got" != "$want" ]]; then
    printf '%s[ERROR]%s adapter fuzz (package:target) set changed (got [%s] want [%s]) — update gates.sh, ci.yml, and fuzz-nightly.yml together.\n' \
      "$RED" "$NC" "$got" "$want" >&2
    return 1
  fi
}

validate_tcp_port() {
  local port="$1" n
  [[ "$port" =~ ^[0-9]+$ ]] || return 1
  n=$((10#$port))
  (( n >= 1 && n <= 65535 ))
}

port_in_use() {
  local port="$1"
  command -v ss >/dev/null 2>&1 || return 1
  ss -ltn "sport = :$port" 2>/dev/null | awk 'NR > 1 { found=1 } END { exit found ? 0 : 1 }'
}

choose_frontend_e2e_port() {
  local port
  if [[ -n "${AI_VIEWER_E2E_PORT:-}" ]]; then
    if ! validate_tcp_port "$AI_VIEWER_E2E_PORT"; then
      printf '%s[ERROR]%s AI_VIEWER_E2E_PORT must be an integer TCP port from 1 to 65535.\n' "$RED" "$NC" >&2
      return 2
    fi
    printf '%s\n' "$AI_VIEWER_E2E_PORT"
    return 0
  fi

  for port in 7710 17710 17711 17712 17713; do
    if ! port_in_use "$port"; then
      printf '%s\n' "$port"
      return 0
    fi
  done

  printf '%s[ERROR]%s no free frontend E2E port found in 7710, 17710-17713; set AI_VIEWER_E2E_PORT to a free localhost port.\n' "$RED" "$NC" >&2
  return 1
}

run_frontend_e2e() {
  if [[ ! -f frontend/package.json ]]; then
    printf '%sno frontend/package.json — skipping frontend E2E.%s\n' "$GRAY" "$NC" >&2
    return 0
  fi
  local port
  port="$(choose_frontend_e2e_port)" || return $?
  if [[ -z "${AI_VIEWER_E2E_PORT:-}" && "$port" != "7710" ]]; then
    printf '%s[info]%s 127.0.0.1:7710 is occupied; running Playwright E2E on 127.0.0.1:%s via AI_VIEWER_E2E_PORT.%s\n' \
      "$YELLOW" "$NC" "$port" "$NC" >&2
  fi
  (cd frontend && AI_VIEWER_E2E_PORT="$port" npm run e2e) || return $?
}

run_bench_gate() {
  bash scripts/test/check-bench-test.sh || return $?
  bash scripts/check-bench.sh || return $?
}

# A frontend self-test (lint.sh frontend section + test.sh frontend section)
# needs frontend/node_modules; those scripts install it on demand only when
# missing, so gates.sh need not pre-provision anything.

# --- fast static gates first -------------------------------------------------

# 1. Formatter-scope self-test. Run before lint.sh so a regression to walking
#    local ignored/untracked Go fails with the narrow diagnostic first.
section "lint formatter-scope self-test" bash scripts/test/lint-test.sh

# 2. Static analysis (Go + frontend, build-free).
section "lint.sh (Go + frontend static analysis)" bash scripts/lint.sh

# 3. Secrets + operator-PII scan. Self-test first (prove the scanner still
#    detects), then the scan itself. Both fail-closed (the files must exist).
section "scan-secrets self-test" bash scripts/test/scan-secrets-test.sh
section "scan-secrets" bash scripts/scan-secrets.sh

# 4. No-AI-attribution scan. Required fail-closed gate.
section "scan-ai-attribution" bash scripts/scan-ai-attribution.sh

# 5. Spec ↔ code drift. Self-test first, then the detector on the live tree.
section "spec-drift self-test" bash scripts/test/spec-drift-test.sh
section "spec-drift" bash scripts/spec-drift.sh

# 6. Ingestion parity fixture gate. Self-test first, then the named fixture
# parity wrapper over source/canonical/diff/CLI parity tests.
section "ingestion parity self-test" bash scripts/test/check-ingestion-parity-test.sh
section "ingestion parity fixtures" bash scripts/check-ingestion-parity.sh --fixtures

# 7. Codacy coverage upload state-machine self-test. Fast hermetic gate for the
#    reporting-only upload orchestration script.
section "codacy coverage upload self-test" bash scripts/test/codacy-coverage-upload-test.sh

# 8. Codacy tool/pattern + path-exclusion policy self-test. Fast hermetic gate.
section "codacy config self-test" bash scripts/test/codacy-config-test.sh

# 9. systemd unit static lint (present in this repo; skip cleanly if removed).
if [[ -f scripts/test/systemd-units-test.sh ]]; then
  section "systemd units" bash scripts/test/systemd-units-test.sh
fi

# --- slow gates last ---------------------------------------------------------

# 10. Full build + the REAL bundle-size gate on the built dist/ + embed + both
#    binaries. (Slower than the static gates; faster than the -race suite.)
section "build.sh (frontend build + bundle-size gate + embed + binaries)" bash scripts/build.sh

# 11. Benchmark gate self-test + local workstation regression gate. This is not
#     comparable on CI hardware, but it is a required local/workstation gate.
#     Run it before the thermal-heavy correctness gates so it measures code, not
#     residual load from the aggregate itself.
section "benchmark regression gate" run_bench_gate

# 12. The long pole: Go -race suite + statement-coverage gate, then (inside
#    test.sh) the frontend Vitest run. check-coverage.sh consumes the
#    coverage.out test.sh writes.
section "test.sh (Go -race + coverage + frontend Vitest)" bash scripts/test.sh
section "check-coverage.sh (Go statement coverage gate)" bash scripts/check-coverage.sh coverage.out

# 13. Explicit adapter fuzz seed gate + exact target-set lock. `go test ./...`
#    exercises seeds too, but this named section makes the gate visible and pins
#    the package:target matrix against fuzz-nightly.yml.
section "adapter fuzz seed corpus" run_fuzz_seed_gate

# 14. Playwright E2E against the built embedded binary. The gating chromium
#    project includes the axe a11y specs, so this covers Frontend — E2E and
#    Frontend — Accessibility without a duplicate second Playwright run.
section "frontend E2E + axe" run_frontend_e2e

# --- done --------------------------------------------------------------------

print_summary
printf '%s[PASS]%s gates.sh: every quality gate green.\n' "$GREEN" "$NC" >&2
