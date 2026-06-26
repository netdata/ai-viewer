#!/usr/bin/env bash
#
# Static self-test for scripts/install-system.sh.
#
# The system installer upgrades binaries that may already be running under
# systemd. It must not copy directly over mapped executables (`Text file busy`),
# and it must stop active units before ownership repair, then start and verify
# the exact ai-viewer units so a successful upgrade actually runs the new binary.
set -euo pipefail
IFS=$'\n\t'

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; NC=$'\033[0m'
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALLER="${REPO_ROOT}/scripts/install-system.sh"

fail() {
  echo -e "${RED}[FAIL]${NC} $*" >&2
  exit 1
}

[[ -f "$INSTALLER" ]] || fail "missing installer: ${INSTALLER}"
bash -n "$INSTALLER"

grep -Eq '^install_binary_atomically\(\) \{' "$INSTALLER" \
  || fail "installer must define install_binary_atomically"
grep -Fq 'sudorun install -m 0755 "$src" "$tmp"' "$INSTALLER" \
  || fail "installer must stage binaries with install -m 0755"
grep -Fq 'sudorun mv -f "$tmp" "$dst"' "$INSTALLER" \
  || fail "installer must atomically rename staged binaries into place"
grep -Fq 'render_dir="$(mktemp -d -t ai-viewer-units.XXXXXX)"' "$INSTALLER" \
  || fail "installer must render units into a temp directory"
grep -Fq 'rendered_ingest="$render_dir/ai-viewer-ingest.service"' "$INSTALLER" \
  || fail "rendered ingest unit must keep a valid .service basename"
grep -Fq 'run systemd-analyze verify "$rendered_ingest" "$rendered_serve"' "$INSTALLER" \
  || fail "installer must fail closed when rendered unit validation fails"

if grep -Eq 'sudorun cp .*ai-viewer-(ingest|serve)' "$INSTALLER"; then
  fail "installer must not copy directly over running ai-viewer binaries"
fi
if grep -F 'systemd-analyze verify' "$INSTALLER" | grep -Fq '|| true'; then
  fail "installer must not ignore rendered unit verification failures"
fi
if grep -Eq 'systemctl enable --now ai-viewer-(ingest|serve)\.service' "$INSTALLER"; then
  fail "installer must stop/start existing units, not only enable --now"
fi

if grep -Eq 'systemctl restart ai-viewer-(ingest|serve)\.service' "$INSTALLER"; then
  fail "installer must use explicit stop/start ordering, not systemctl restart"
fi

grep -Eq '^stop_unit_if_active\(\) \{' "$INSTALLER" \
  || fail "installer must define guarded stop_unit_if_active"
grep -Fq 'systemctl is-active --quiet "$unit"' "$INSTALLER" \
  || fail "guarded stop helper must check is-active before stop"
grep -Fq 'sudorun systemctl stop "$unit"' "$INSTALLER" \
  || fail "guarded stop helper must stop only the requested active unit"
grep -Eq '^wait_for_ingester_active\(\) \{' "$INSTALLER" \
  || fail "installer must define wait_for_ingester_active"
grep -Fq 'systemctl is-active --quiet ai-viewer-ingest.service' "$INSTALLER" \
  || fail "installer must poll ingester liveness with systemctl is-active"
grep -Fq 'for _ in $(seq 1 15); do' "$INSTALLER" \
  || fail "ingester liveness poll must run once per second for up to 15 attempts"
grep -Fq 'sudorun systemctl start ai-viewer-ingest.service' "$INSTALLER" \
  || fail "installer must start ai-viewer-ingest.service explicitly"
grep -Fq 'sudorun systemctl start ai-viewer-serve.service' "$INSTALLER" \
  || fail "installer must start ai-viewer-serve.service explicitly"

stop_line="$(grep -n 'stop_unit_if_active ai-viewer-ingest.service' "$INSTALLER" | head -1 | cut -d: -f1 || true)"
chown_line="$(grep -n 'sudorun chown -R "\${op_user}:\${op_group}" "$OPT_DIR"' "$INSTALLER" | head -1 | cut -d: -f1 || true)"
start_ingest_line="$(grep -n 'sudorun systemctl start ai-viewer-ingest.service' "$INSTALLER" | head -1 | cut -d: -f1 || true)"
wait_ingest_line="$(grep -n 'wait_for_ingester_active "post-start"' "$INSTALLER" | head -1 | cut -d: -f1 || true)"
start_serve_line="$(grep -n 'sudorun systemctl start ai-viewer-serve.service' "$INSTALLER" | head -1 | cut -d: -f1 || true)"
post_wait_line="$(grep -n 'wait_for_ingester_active "post-server"' "$INSTALLER" | head -1 | cut -d: -f1 || true)"

[[ -n "$stop_line" && -n "$chown_line" && "$stop_line" -lt "$chown_line" ]] \
  || fail "installer must stop ingester before chown -R"
[[ -n "$chown_line" && -n "$start_ingest_line" && "$chown_line" -lt "$start_ingest_line" ]] \
  || fail "installer must chown before starting ingester"
[[ -n "$start_ingest_line" && -n "$wait_ingest_line" && "$start_ingest_line" -lt "$wait_ingest_line" ]] \
  || fail "installer must verify ingester after starting it"
[[ -n "$wait_ingest_line" && -n "$start_serve_line" && "$wait_ingest_line" -lt "$start_serve_line" ]] \
  || fail "installer must verify ingester before starting serve"
[[ -n "$start_serve_line" && -n "$post_wait_line" && "$start_serve_line" -lt "$post_wait_line" ]] \
  || fail "installer must re-check ingester after server readiness"

echo -e "${GREEN}PASS${NC} install-system.sh upgrades running services atomically"
