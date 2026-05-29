#!/usr/bin/env bash
# Whole-repository secret + operator-PII scanner.
#
# This is the enforced safety net behind .agents/sow/specs/quality-gates.md
# §"Secrets + Operator-PII Scan" and .agents/sow/specs/security.md
# §"Sensitive Data In Fixtures and Durable Artifacts". It scans EVERY tracked
# file (git ls-files), decompressing *.gz to scan their contents too, and exits
# non-zero on any hit so a leak can never reach master.
#
# Two rule classes are enforced:
#
#   Rule 1 — operator identity. Banned EVERYWHERE, including the sanitizer's
#            */INPUT/** fixtures, with zero tolerance. Real emails, the real
#            home path, and the given/sur-name (word-bounded so unrelated
#            tokens like cost_usd or costing never match).
#
#   Rule 2 — generic secret shapes (sk-/sk-ant-, xox[bpas]-, AKIA…, high-entropy
#            Bearer tokens). Banned everywhere EXCEPT
#            scripts/test/fixtures/*/INPUT/** — those dirty inputs legitimately
#            carry SYNTHETIC secret-shaped strings whose entire purpose is to
#            exercise sanitize-fixture.sh's redaction. Rule 1 still applies to
#            them, so they may never carry the operator's real identity.
#
# Allow-listed (never a hit): [REDACTED_…] placeholders, *.example.invalid,
# RFC-2606 reserved example.com / example.org, and this script's own path.
# Public provider hostnames (api.anthropic.com, api.openai.com) are NOT secrets
# and are not scanned — they are a sanitizer concern, not a leak.
#
# This script MUST exclude its own path from the scan: a scanner necessarily
# contains the very patterns it hunts for. It takes no arguments and contains
# no real secrets — only pattern definitions.
set -euo pipefail

# Colors for transparent command tracing (mirrors scan-ai-attribution.sh).
# Defined with $'...' ANSI-C quoting so the bytes are real escape sequences;
# they are then emitted through printf '%s' (never the format string), which
# keeps shellcheck SC2059 clean and renders correctly. Colors collapse to empty
# when stdout/stderr is not a TTY so logs and CI output stay plain.
if [[ -t 2 ]]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; GRAY=$'\033[0;90m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; GRAY=""; NC=""
fi
run() {
  printf >&2 '%s%s >%s ' "$GRAY" "$(pwd)" "$NC"
  printf >&2 '%s' "$YELLOW"; printf >&2 '%q ' "$@"; printf >&2 '%s\n' "$NC"
  # Capture the command's own exit code directly (NOT via `if ! "$@"`, which
  # would make $? the negated-expression status — always 0 — and silently mask
  # failures under `set -e`).
  local ec=0; "$@" || ec=$?
  if [[ "$ec" -ne 0 ]]; then printf >&2 '%s[ERROR]%s exit %s: %s\n' "$RED" "$NC" "$ec" "$*"; return "$ec"; fi
}

# Resolve repo root from the script's own location so the scan works from any
# CWD, then drive everything off `git ls-files` (tracked files only).
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
run cd "$REPO_ROOT"

# This script's own repo-relative path; excluded from the scan because it
# enumerates the patterns it hunts for.
SELF_REL="scripts/scan-secrets.sh"

# --- rule definitions --------------------------------------------------------
#
# All patterns are POSIX-ERE (grep -E). Each is paired with a human label so a
# hit reports which rule fired.

# Rule 1 — operator identity. Banned everywhere (no INPUT exemption).
#   - explicit emails and their private domains
#   - home path (literal and URL-encoded)
#   - given/sur-name, word-bounded + case-insensitive (-w on the whole line is
#     too coarse for JSON, so the name rule is matched case-insensitively with
#     explicit \b boundaries here and -i is applied only to that rule below)
R1_EMAIL='@netdata\.cloud|@tsaousis\.gr'
R1_HOME='/home/costa|%2[Ff]home%2[Ff]costa'
R1_NAME='\bcosta\b|\btsaousis\b'

# Rule 2 — generic secret shapes. Banned everywhere EXCEPT */INPUT/** fixtures.
#
# Each shape requires a LEFT TOKEN BOUNDARY ((^|[^A-Za-z0-9])) so the prefix is
# the start of a real token, not the tail of an English word: this stops
# "di_sk_-footprint" / "baddi_sk_-monitoring" style substrings from matching
# `sk-…` while still catching every real key (which always begins at a quote,
# space, `=`, `:`, or line start). The boundary is a non-capturing concern —
# grep -n reports the whole line regardless.
BOUNDARY='(^|[^A-Za-z0-9])'
R2_OPENAI="${BOUNDARY}sk-[A-Za-z0-9_-]{8,}"
R2_SLACK="${BOUNDARY}xox[bpas]-[A-Za-z0-9-]{8,}"
R2_AWS="${BOUNDARY}AKIA[0-9A-Z]{16}"
R2_BEARER='Bearer [A-Za-z0-9._-]{16,}'

# Allow-list: lines containing any of these tokens are never a hit. These are
# the sanctioned placeholders/examples the project uses in clean artifacts.
#   [REDACTED_…]            sanitizer output
#   *.example.invalid       sanitizer-rewritten provider hosts
#   example.com/example.org RFC-2606 reserved domains used in docs/fixtures
#   sk-ant-EXAMPLE…         the canonical SYNTHETIC key placeholder used by the
#                           sanitizer INPUT fixtures and documented in the specs
#                           /SOW that describe this scanner; never a real key
#                           (real Anthropic keys are sk-ant-api03-…).
ALLOW='\[REDACTED_|example\.invalid|example\.com|example\.org|sk-ant-EXAMPLE'

# --- scanning helpers --------------------------------------------------------

# scan_text <file-label> <is_input_dir:0|1> < text-on-stdin
#
# Greps the stdin text against every rule appropriate for the file. Prints
# one line per hit and returns 1 if any hit was found, 0 otherwise.
#
# Allow-listing is deliberately NOT applied as an up-front whole-line filter:
# that would silently drop a line — and any real secret or real operator
# identity on it — the moment the line also happened to contain a sanctioned
# placeholder (e.g. `example.com`). The semantics are split per rule class:
#
#   Rule 1 (operator identity) is matched against the RAW text and is NEVER
#   allow-listed: there is no legitimate placeholder form of the operator's
#   real email/home/name, so an "exemption" could only ever hide a leak.
#
#   Rule 2 (generic secret shapes) is allow-listed PER MATCHED TOKEN, not per
#   line. Each secret-shape token found on a line is reported only if the token
#   itself is not a sanctioned placeholder (e.g. sk-ant-EXAMPLE…). A real
#   secret therefore still fires even when it shares a line with a placeholder.
#
# Rule 1 runs for every file. Rule 2 runs only when is_input_dir == 0 (the
# sanitizer's dirty inputs are the sole place synthetic secret shapes live).
scan_text() {
  local label="$1" is_input="$2"
  local text hits=0
  text="$(cat)"
  [[ -z "$text" ]] && return 0

  # emit_raw — Rule 1 reporter. Matches <regex> against the RAW text (no
  # allow-list filtering, ever) and reports the whole offending line.
  # Args: <regex> <rule-label> <grep-iflag>.
  emit_raw() {
    local re="$1" rule="$2" iflag="$3" out
    out="$(printf '%s' "$text" | grep -nE ${iflag:+-i} "$re" || true)"
    if [[ -n "$out" ]]; then
      while IFS= read -r line; do
        printf '%s:%s [%s] %s\n' "$label" "${line%%:*}" "$rule" "${line#*:}"
      done <<< "$out"
      hits=1
    fi
  }

  # emit_tokens — Rule 2 reporter. Extracts each matched secret-shape TOKEN with
  # its line number (grep -noE), then reports only tokens that are NOT
  # themselves allow-listed. Allow-listing is per token, so a real secret on a
  # line that also carries a placeholder is still flagged.
  # Args: <regex> <rule-label>.
  emit_tokens() {
    local re="$1" rule="$2" out lineno token
    out="$(printf '%s' "$text" | grep -noE "$re" || true)"
    [[ -z "$out" ]] && return 0
    while IFS= read -r line; do
      lineno="${line%%:*}"
      # The matched token is everything after the first "<lineno>:". The left
      # token boundary in the Rule-2 regexes can capture one leading delimiter
      # byte (quote/space/=/:) — harmless for reporting and for the allow-list
      # check, which uses a substring match.
      token="${line#*:}"
      # Per-token allow-list: skip only tokens that are themselves sanctioned
      # placeholders (e.g. sk-ant-EXAMPLE…). Everything else is a real hit.
      if printf '%s' "$token" | grep -qE "$ALLOW"; then
        continue
      fi
      printf '%s:%s [%s] %s\n' "$label" "$lineno" "$rule" "$token"
      hits=1
    done <<< "$out"
  }

  # Rule 1 — always, raw, never allow-listed.
  emit_raw "$R1_EMAIL" "operator-email" ""
  emit_raw "$R1_HOME"  "operator-home"  ""
  emit_raw "$R1_NAME"  "operator-name"  "-i"

  # Rule 2 — only outside sanitizer INPUT fixtures; allow-listed per token.
  if [[ "$is_input" -eq 0 ]]; then
    emit_tokens "$R2_OPENAI" "secret-sk"
    emit_tokens "$R2_SLACK"  "secret-slack"
    emit_tokens "$R2_AWS"    "secret-aws"
    emit_tokens "$R2_BEARER" "secret-bearer"
  fi

  return "$hits"
}

# is_input_fixture <repo-relative-path> -> 0 if under scripts/test/fixtures/*/INPUT/**
is_input_fixture() {
  case "$1" in
    scripts/test/fixtures/*/INPUT/*) return 0 ;;
    *) return 1 ;;
  esac
}

# --- main --------------------------------------------------------------------

violations=""
scanned=0
gz_scanned=0

# Iterate tracked files (NUL-delimited to survive any path). Binary files other
# than *.gz are skipped via grep -I semantics inside scan_text (we feed text;
# `git ls-files` does not classify, so we rely on the gz branch for the only
# binaries we must inspect and let scan_text harmlessly process the rest — the
# patterns are text-only so binary noise cannot create a false operator-PII
# hit, and any genuine ASCII secret in a binary is still worth flagging).
while IFS= read -r -d '' f; do
  # Never scan this script (it lists the patterns) — would be a guaranteed
  # self-hit.
  [[ "$f" == "$SELF_REL" ]] && continue
  [[ -f "$f" ]] || continue

  input_flag=1
  is_input_fixture "$f" || input_flag=0

  if [[ "$f" == *.gz ]]; then
    # Decompress and scan the contents. A *.gz under an INPUT/ dir keeps its
    # Rule-2 exemption; Rule 1 still applies.
    gz_scanned=$((gz_scanned + 1))
    gz_text="$(gunzip -c "$f" 2>/dev/null || true)"
    if out="$(printf '%s' "$gz_text" | scan_text "$f:(gz)" "$input_flag")"; then
      :
    else
      violations="${violations}${out}"$'\n'
    fi
  else
    scanned=$((scanned + 1))
    # Read the file into a variable first; feeding it to scan_text via a pipe
    # (rather than a redirect that shares the same name as the outer
    # assignment) keeps the read/write paths unambiguous.
    plain_text="$(cat "$f")"
    if out="$(printf '%s' "$plain_text" | scan_text "$f" "$input_flag")"; then
      :
    else
      violations="${violations}${out}"$'\n'
    fi
  fi
done < <(git ls-files -z)

if [[ -n "${violations//[$'\n\t ']/}" ]]; then
  printf >&2 '%s[FAIL]%s secret / operator-PII scan found hits:\n' "$RED" "$NC"
  printf '%s' "$violations" | grep -v '^$' >&2
  printf >&2 '%sRemove the operator real identity entirely; replace generic secret shapes outside INPUT/ fixtures with [REDACTED_...] or an example value. Do NOT weaken the scanner.%s\n' "$RED" "$NC"
  exit 1
fi

printf '%s[PASS]%s no secrets or operator-PII in %d tracked files (%d decompressed from .gz).\n' \
  "$GREEN" "$NC" "$((scanned + gz_scanned))" "$gz_scanned"
