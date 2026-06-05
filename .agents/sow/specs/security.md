# Security

## TL;DR

v1 is **localhost-only, no auth, read-only on source files**. The threat model is "what could go wrong if this software has a bug" — not "an attacker is trying to break in". Defense-in-depth is bounded to that scope.

## v1 Threat Model

In scope:

- ai-viewer crashes do not corrupt source files (read-only opens).
- ai-viewer cannot be reached by another host (bind 127.0.0.1).
- ai-viewer cannot leak source data over the network (no outbound calls at all).
- A malicious snapshot file does not crash the ingester or escalate (defensive parsing, bounded buffers, no shell-out).

Out of scope:

- Multi-user authentication.
- Per-user data isolation.
- Network exposure beyond localhost.
- Audit trail of who viewed what.

## Hard Rules

1. **Read-only on sources.** All file opens for source data use `os.O_RDONLY`. SQLite source connections use `?mode=ro`. There is no code path that writes to a source.
2. **No outbound network calls.** The ingester and server make zero outgoing HTTP, DNS, or any other network call. Adapters do not fetch anything from the network (cost lookup is from a static table, not an API). CI enforces this with a test that runs the binary under a network-blocked context.
3. **Localhost bind.** The default bind is `127.0.0.1:7710`. The `--bind` flag accepts ONLY literal loopback IPs (`127.0.0.1` or `::1`). The string `"localhost"` is REJECTED at flag-parse time because `/etc/hosts` (and NSS/DNS) can be manipulated to point it at a non-loopback IP, which would silently expose the server. An empty host (`:7710`) is also rejected because the Go HTTP server treats it as `0.0.0.0:7710` — bound to every interface. Binding to a non-loopback IP requires `--allow-non-localhost` (Phase 2; v1 does not even accept that flag).
4. **Bounded parsing.** Every adapter parser has explicit limits:
   - max line length: 16 MB (anything larger logged as SourceError and skipped)
   - max event payload depth: 64 (anything deeper logged and skipped)
   - max events per file scan call: unbounded but periodically flushed
5. **No shell-out.** Never `exec.Command` for any user-facing operation. Pure Go parsing only.
6. **No symlink traversal escape.** When walking source directories, the adapter checks each path resolved via `filepath.EvalSymlinks` is still inside the configured root. Refuses to read files outside.
7. **No template injection.** All HTML rendered by the frontend uses React's auto-escaping. The server never renders HTML server-side; it serves static embedded files plus JSON.

## Payload Serving Safety

**Phase 2 — the payload route is not implemented in Phase 1** (it is not
registered, and `payload_refs` deliberately carries no `url`). The defenses below
are the design for when the streaming route lands; they are not yet active code.

`GET /api/payloads/:ref` could be a vector if the `ref` is attacker-controlled. Defense:

- Refs are integer IDs from the `payload_refs` table, not arbitrary paths from the URL.
- The server looks up the `location_uri` from SQLite and validates it starts with `file://` and resolves inside one of the configured source roots before opening.
- The Content-Disposition header is set to `attachment` for any non-text payload; the browser will not execute it as a script.

## Sensitive Data In Fixtures and Durable Artifacts

Test fixtures committed to the repo go through sanitization (see
`testing-strategy.md`). The enforced gate is `scripts/scan-secrets.sh`
(`quality-gates.md` §Secrets + Operator-PII Scan), which scans **every tracked
file** — not just `testdata/` — and fails the build on any hit. It enforces two
rule classes:

- **Operator identity** (real emails, real home path, name) — banned in every
  tracked file with zero tolerance, including the sanitizer's `INPUT/` fixtures.
- **Generic secret shapes** (`AKIA…`, `sk-…`, `sk-ant-…`, `xox[bpas]-…`,
  high-entropy bearer tokens, and VCS PATs `ghp_…`/`github_pat_…`/`glpat-…`) —
  flagged in **every** tracked file; the only exemption is a token carrying the
  synthetic marker `EXAMPLE` (e.g. `sk-ant-EXAMPLE…`), the convention for the
  sanitizer's dirty-input fixtures. A real secret-shape (no `EXAMPLE`) is flagged
  even under `scripts/test/fixtures/*/INPUT/**`, so the synthetic-only rule is
  enforced by the gate. (Public provider hostnames are not secrets; the sanitizer
  rewrites them to `*.example.invalid`.)

Sanitizer dirty inputs must therefore be synthetic: they may contain
secret-*shaped* strings (marked `EXAMPLE`) but never the operator's real
identity. The scanner is **fail-closed in CI** — an absent scanner fails the
`gates` job rather than passing silently — and ships with a negative self-test
that plants an operator-identity string and asserts detection.

## Dependency Hygiene

- `dependabot.yml` enables security updates for Go modules and npm.
- `golangci-lint` runs `gosec` linters as part of the standard config.
- `npm audit --audit-level=high` runs in CI; high/critical vulns fail the build.

## Code Scanning Triage

CodeQL and Codacy are the semantic/cloud scanning layers on top of the local
gates. Their security findings are not cosmetic.

- **CodeQL critical/high alerts:** fix in code, prove false-positive with a
  query/path-scoped suppression and SOW or issue reference, or file a follow-up
  SOW before completing the active work.
- **Codacy critical/high findings:** fix, prove false-positive, or track. Cloud
  noise is tuned by disabling only the narrow tool/pattern/path that produces
  false positives; broad security categories are not disabled just to reduce
  counts.
- **CICD/SCA findings:** action pinning, dependency issues, and workflow risks
  are treated as supply-chain security work. If a mitigation conflicts with
  Dependabot freshness or action major-version updates, the SOW records the
  tradeoff and the chosen policy.
- **Coverage and quality metrics:** Codacy coverage/complexity trends are
  signals for maintainability, not a substitute for the enforced local coverage
  and lint gates.

Scanner output may be stored in `/tmp` for analysis. Durable artifacts record
aggregate counts, tool names, and sanitized rationale only; they never include
tokens, account details, raw sensitive source snippets, or private customer data.

## Future Auth Design (v2+)

When network exposure is needed, the design will be:

- HTTP Basic Auth at the reverse proxy layer (simplest), OR
- OIDC if the operator integrates with their existing identity provider.

The choice is the operator's; the SOW will record it. No auth is implemented in v1 because none is needed for localhost.

## Reporting Issues

If a security issue is found in ai-viewer, the operator should open a private GitHub security advisory on the repository. A `SECURITY.md` will be added at Phase 1 close.
