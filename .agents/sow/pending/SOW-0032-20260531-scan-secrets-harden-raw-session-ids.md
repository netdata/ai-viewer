# SOW-0032 - harden scan-secrets.sh to catch raw session ids + exhaustive UUID sweep

## Status

Status: open

Sub-state: proposed follow-up (P2, repo-wide hygiene). Discovered 2026-05-31 during SOW-0029 rounds 4-5 external review: the secret/PII gate (`scripts/scan-secrets.sh`) detects the operator NAME/email/home-with-username and known secret shapes, but does NOT detect raw real session UUIDs. codex (manual) repeatedly surfaced pre-existing raw session ids the gate misses — a per-round whack-a-mole that only a hardened gate ends. Not blocking SOW-0029 (whose change set is clean and CI-green; the one real UUID it newly touched was removed, and the one codex flagged at `SOW-0001:106` was sanitized in that PR).

## Requirements

### Purpose

Make the automated gate enforce the operator's absolute "no raw session ids" rule so it cannot regress, and remove the remaining pre-existing real session ids from tracked artifacts. Today the rule is enforced only for the operator NAME; raw session UUIDs slip through and are caught (if at all) by manual review. The fix is twofold: (1) sweep + classify every UUID-shaped string in tracked files and sanitize the genuinely-real ones; (2) extend `scan-secrets.sh` with a real-session-id detector that flags UUIDs appearing in real-data contexts while NOT false-positiving on synthetic test fixtures, documented format examples, or HTTP request-id demos.

### Assistant Understanding

Facts (verified 2026-05-31):
- `scripts/scan-secrets.sh` derives operator identity (name/email/home basename) from git metadata and flags those + secret shapes. It does NOT flag bare UUIDs.
- Tracked UUID-shaped strings fall into classes:
  - **Real session ids** (must sanitize): e.g. the ai-agent session at `SOW-0001:106` (sanitized in SOW-0029's PR) and `00ce0ef4-…` in `adapter-aiagent-v2.md` (removed in SOW-0029's PR). There may be others in `done/` SOWs.
  - **Synthetic test fixtures** (must NOT flag): `11111111-…`, `22222222-…`, `33333333-…`, `77777777-…` used across adapter tests/genfixtures.
  - **Documented codex rollout-format examples** (policy decision): the `019aa234-…` UUIDv7 and `5556f03d-…` legacy ids — used as the codex test fixture id throughout `adapter-codex.md` + codex tests; never labeled "real". Likely acceptable as documented examples; the SOW must DECIDE and document the policy (and the hardened scanner must not false-positive on them).
  - **HTTP request-id demos** (must NOT flag): the `request_id` UUIDs in `SOW-0001` observability examples (`04c4228f`, `bc936e69`, …) — synthetic demo ids, not session data.
- Generic tool paths (`~/.ai-agent/sessions/`, `~/.claude/projects/`, `~/.codex/sessions/`) contain NO operator username and are NOT PII — must not be flagged.

Inferences (decide in the gate):
- The detector likely needs a context-aware rule: flag a UUID only when adjacent to real-data markers ("Real sample", a real `.json.gz`/`.jsonl` filename tied to operator data) OR maintain an allowlist of known-synthetic/example ids. Pick the lower-false-positive design; document it. Mirror the existing scanner's careful token-boundary approach.

### Acceptance Criteria

1. An exhaustive sweep classifies every UUID-shaped string in tracked files; every genuinely-real session id is sanitized to a placeholder. **Verification**: documented classification table in this SOW; post-sweep grep shows only synthetic/example/demo ids remain.
2. `scan-secrets.sh` gains a real-session-id detector. **Verification**: a scanner self-test (fixture with a real-looking session-id-in-real-context FAILS; a fixture with synthetic test ids + request-id demos + generic tool paths PASSES). Add to the gate's existing test harness if present, else a `scan-secrets` unit test.
3. The codex format-example policy (`019aa234`/`5556f03d`) is decided and documented (allowlist vs sanitize). **Verification**: SOW note + scanner behavior matches.
4. No false positives introduced: the full existing tracked tree PASSES the hardened scanner. **Verification**: `scan-secrets.sh` exits 0 on the repo.

## Analysis

Sources: `scripts/scan-secrets.sh`, all tracked `.md` + fixtures. Discovered 2026-05-31 (SOW-0029 R4/R5, codex). Risks:

- **R1 — False positives.** A naive UUID regex flags every synthetic test id and breaks the gate. Mitigation: context-aware detection or an explicit synthetic/example allowlist; self-tests both ways.
- **R2 — done/ SOW edits.** Sanitizing `done/` artifacts is allowed (PII removal, no narrative change) but must preserve the audit trail's meaning. Mitigation: replace ids with placeholders, keep surrounding facts.
- **R3 — Scope.** Keep this to PII-gate hardening; do not entangle with feature work.

## Pre-Implementation Gate

(To be filled on pickup. Must: enumerate + classify every tracked UUID; decide the detector design (context vs allowlist) + the format-example policy; write the scanner self-test BEFORE hardening; confirm zero false positives on the full tree.)

## Implementation / Validation / Reviews / Outcome

(Empty placeholders.)

## Lessons / Follow-Ups

Pending. Parent: SOW-0029 (token/cost/cache semantics — its review rounds 4-5 surfaced this). The recurring lesson: an absolute policy ("no raw session ids") is only durable when the automated gate enforces it; manual review finds them one-per-round and never converges.
