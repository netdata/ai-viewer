#!/usr/bin/env bash
# Whole-repository secret + operator-PII scanner.
#
# This is the enforced safety net behind .agents/sow/specs/quality-gates.md
# §"Secrets + Operator-PII Scan" and .agents/sow/specs/security.md
# §"Sensitive Data In Fixtures and Durable Artifacts". It scans EVERY tracked
# file (git ls-files), decompressing *.gz to scan their contents too, reading
# tracked symlinks as their target PATH STRING (not the dereferenced target),
# and exits non-zero on any hit so a leak can never reach master.
#
# Two rule classes are enforced:
#
#   Rule 1 — operator identity. Banned EVERYWHERE with zero tolerance,
#            case-insensitively. Real email domains, the real home path
#            (literal + URL-encoded), and the given/sur-name (word-bounded so
#            unrelated tokens like cost_usd or costing never match). The
#            patterns are assembled from non-contiguous fragments at runtime so
#            this PUBLIC file never contains the operator identity as a literal.
#
#   Rule 2 — generic secret shapes (sk-/sk-ant-, xox[bpas]-, AKIA…, high-entropy
#            Bearer tokens, ghp_/github_pat_ GitHub PATs, glpat- GitLab PATs).
#            Banned EVERYWHERE, including scripts/test/fixtures/*/INPUT/**. A
#            secret-shape token is exempt ONLY when the matched token itself
#            carries the literal marker "EXAMPLE" (case-sensitive) — that is how
#            sanitize-fixture.sh's synthetic dirty inputs (sk-ant-EXAMPLE…,
#            sk-EXAMPLEcustomer…) declare themselves non-real. A real key (no
#            EXAMPLE) is flagged anywhere; there is NO directory carve-out.
#
# Never a hit: [REDACTED_…] placeholders, EXAMPLE-marked secret shapes, and this
# script's own path. Public provider hostnames (api.anthropic.com,
# api.openai.com) are NOT secrets and are not scanned — they are a sanitizer
# concern, not a leak.
#
# This script MUST exclude its own path from the scan: a scanner necessarily
# contains the very patterns it hunts for. It takes no arguments and contains
# no real secrets and no operator identity — only pattern definitions assembled
# from fragments.
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
#     explicit \b boundaries here, and -i is applied to ALL three Rule-1
#     patterns below so mixed-/upper-case forms of the email, home path, and
#     name cannot bypass them).
#
# CRITICAL — the patterns are assembled from NON-CONTIGUOUS fragments at
# runtime so this PUBLIC script file never contains the operator's real
# identity as a literal string. Self-exclusion (SELF_REL skip below) stops a
# self-hit, but it does NOT stop publication: a contiguous literal here would
# leak the identity into the repo regardless. Splitting each token across a
# string concatenation means `grep` over this file's bytes finds no contiguous
# operator literal, while the assembled regexes still match correctly at run
# time. The self-test (scripts/test/scan-secrets-test.sh) uses the same
# fragment-assembly technique to probe detection.
_op_name="co""sta"                          # operator given-name
_op_surname="tsa""ousis"                    # operator sur-name
_op_domain1="net""data"'\.cloud'            # operator email domain 1 (ERE)
_op_domain2="tsa""ousis"'\.gr'              # operator email domain 2 (ERE)
R1_EMAIL="@${_op_domain1}|@${_op_domain2}"
R1_HOME="/home/${_op_name}|%2[Ff]home%2[Ff]${_op_name}"
R1_NAME="\\b${_op_name}\\b|\\b${_op_surname}\\b"
# The fragments served their purpose; drop them so they do not linger in the
# environment of any child process this script spawns.
unset _op_name _op_surname _op_domain1 _op_domain2

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
# Version-control provider tokens. The sanitizer already redacts ghp_…; the
# scanner must hunt the same shapes (plus GitHub fine-grained PATs and GitLab
# PATs) so a committed VCS credential outside INPUT/ is caught. Same left
# token-boundary convention as the shapes above.
R2_GH_PAT="${BOUNDARY}ghp_[A-Za-z0-9]{30,}"
R2_GH_FINE="${BOUNDARY}github_pat_[A-Za-z0-9_]{20,}"
R2_GITLAB="${BOUNDARY}glpat-[A-Za-z0-9_-]{16,}"

# Rule-2 per-token synthetic marker. A secret-SHAPE token is exempt — anywhere
# in the tree, INPUT/ or not — IFF the matched token itself contains the
# literal "EXAMPLE" (case-sensitive). This is the ONLY thing that distinguishes
# a sanctioned synthetic fixture secret (e.g. sk-ant-EXAMPLE…,
# sk-EXAMPLEcustomer…) from a real leaked key. There is no directory exemption:
# a real-shaped token WITHOUT "EXAMPLE" is flagged everywhere, including under
# scripts/test/fixtures/*/INPUT/** — closing the original blanket-INPUT hole
# where any secret shape passed unreviewed. Case-sensitivity is deliberate so a
# real key cannot smuggle itself in as the lowercase word "example".
SECRET_MARKER='EXAMPLE'

# --- scanning helpers --------------------------------------------------------

# scan_text <file-label> < text-on-stdin
#
# Greps the stdin text against every rule. Prints one line per hit and returns
# 1 if any hit was found, 0 otherwise.
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
#   Rule 2 (generic secret shapes) is exempted PER MATCHED TOKEN, not per line
#   and not per directory. Each secret-shape token is reported unless the token
#   itself carries the synthetic marker "EXAMPLE" (case-sensitive). A real
#   secret therefore still fires even when it shares a line with a placeholder,
#   and even when it lives under an INPUT/ fixture dir.
#
# Both rules run for EVERY file: there is no INPUT/ carve-out anymore.
scan_text() {
  local label="$1"
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
  # its line number (grep -noE), then reports only tokens that are NOT marked
  # synthetic. The exemption is per token, so a real secret on a line that also
  # carries a synthetic placeholder is still flagged.
  # Args: <regex> <rule-label>.
  emit_tokens() {
    local re="$1" rule="$2" out lineno token
    out="$(printf '%s' "$text" | grep -noE "$re" || true)"
    [[ -z "$out" ]] && return 0
    while IFS= read -r line; do
      lineno="${line%%:*}"
      # The matched token is everything after the first "<lineno>:". The left
      # token boundary in the Rule-2 regexes can capture one leading delimiter
      # byte (quote/space/=/:) — harmless for reporting and for the marker
      # check, which uses a fixed-string substring match.
      token="${line#*:}"
      # Per-token synthetic exemption: skip only tokens that carry the literal
      # "EXAMPLE" marker (case-sensitive — grep -F, no -i). Everything else is a
      # real hit, INPUT/ fixture or not.
      if printf '%s' "$token" | grep -qF "$SECRET_MARKER"; then
        continue
      fi
      printf '%s:%s [%s] %s\n' "$label" "$lineno" "$rule" "$token"
      hits=1
    done <<< "$out"
  }

  # Rule 1 — always, raw, never allow-listed, case-insensitive on all three so
  # mixed-/upper-case forms of the email, home path, and name cannot bypass.
  emit_raw "$R1_EMAIL" "operator-email" "-i"
  emit_raw "$R1_HOME"  "operator-home"  "-i"
  emit_raw "$R1_NAME"  "operator-name"  "-i"

  # Rule 2 — every file; exempted per token via the EXAMPLE marker. Secret
  # shapes are case-specific (AKIA upper, xox lower, ghp_/glpat- exact prefix),
  # so these are matched case-SENSITIVELY (no -i in emit_tokens).
  emit_tokens "$R2_OPENAI"  "secret-sk"
  emit_tokens "$R2_SLACK"   "secret-slack"
  emit_tokens "$R2_AWS"     "secret-aws"
  emit_tokens "$R2_BEARER"  "secret-bearer"
  emit_tokens "$R2_GH_PAT"  "secret-github-pat"
  emit_tokens "$R2_GH_FINE" "secret-github-fine"
  emit_tokens "$R2_GITLAB"  "secret-gitlab"

  return "$hits"
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

  if [[ -L "$f" ]]; then
    # Tracked symlinks (CLAUDE.md, GEMINI.md, .claude/skills are mode 120000).
    # `-f`/`cat` would DEREFERENCE them and scan the TARGET's content; the git
    # blob is the target PATH STRING. Scan that string so operator PII or a
    # secret embedded in a symlink target is caught, and so we never re-scan
    # the (already-scanned) target file's bytes through the link.
    scanned=$((scanned + 1))
    link_target="$(readlink "$f")"
    if out="$(printf '%s' "$link_target" | scan_text "$f:(symlink)")"; then
      :
    else
      violations="${violations}${out}"$'\n'
    fi
    continue
  fi

  [[ -f "$f" ]] || continue

  if [[ "$f" == *.gz ]]; then
    # Decompress and scan the contents. Rules apply identically to text files;
    # there is no INPUT/ carve-out.
    gz_scanned=$((gz_scanned + 1))
    # Capture gunzip's exit status SEPARATELY from its output: `|| true` alone
    # would mask a corrupt archive as empty text and let secret bytes hidden in
    # a malformed .gz pass silently (fail-open). On decompression FAILURE for a
    # non-empty file, fall back to scanning the RAW bytes AND record an explicit
    # violation so the corrupt archive can never slip through. A genuinely
    # 0-byte .gz (a tracked empty payload fixture) is fine — skip it.
    gz_rc=0
    gz_text="$(gunzip -c "$f" 2>/dev/null)" || gz_rc=$?
    if [[ "$gz_rc" -ne 0 ]]; then
      if [[ -s "$f" ]]; then
        violations="${violations}${f}:(gz) [gz-decompress-failed] gunzip exit ${gz_rc}; scanning raw bytes as fallback"$'\n'
        gz_text="$(cat "$f")"
      else
        # Empty .gz: nothing to scan, no violation.
        continue
      fi
    fi
    if out="$(printf '%s' "$gz_text" | scan_text "$f:(gz)")"; then
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
    if out="$(printf '%s' "$plain_text" | scan_text "$f")"; then
      :
    else
      violations="${violations}${out}"$'\n'
    fi
  fi
done < <(git ls-files -z)

if [[ -n "${violations//[$'\n\t ']/}" ]]; then
  printf >&2 '%s[FAIL]%s secret / operator-PII scan found hits:\n' "$RED" "$NC"
  printf '%s' "$violations" | grep -v '^$' >&2
  printf >&2 '%sRemove the operator real identity entirely; replace generic secret shapes with [REDACTED_...] or a synthetic EXAMPLE-marked value (fixtures only). Do NOT weaken the scanner.%s\n' "$RED" "$NC"
  exit 1
fi

printf '%s[PASS]%s no secrets or operator-PII in %d tracked files (%d decompressed from .gz).\n' \
  "$GREEN" "$NC" "$((scanned + gz_scanned))" "$gz_scanned"
