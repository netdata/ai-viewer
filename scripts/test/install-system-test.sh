#!/usr/bin/env bash
#
# Static self-test for scripts/install-system.sh.
#
# The system installer upgrades binaries that may already be running under
# systemd. It must not copy directly over mapped executables (`Text file busy`),
# and it must restart the exact ai-viewer units so a successful upgrade actually
# runs the new binary.
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
  fail "installer must restart existing units, not only enable --now"
fi

grep -Fq 'sudorun systemctl restart ai-viewer-ingest.service' "$INSTALLER" \
  || fail "installer must restart ai-viewer-ingest.service"
grep -Fq 'sudorun systemctl restart ai-viewer-serve.service' "$INSTALLER" \
  || fail "installer must restart ai-viewer-serve.service"

echo -e "${GREEN}PASS${NC} install-system.sh upgrades running services atomically"
