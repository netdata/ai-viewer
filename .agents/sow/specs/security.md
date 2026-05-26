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
3. **Localhost bind.** The default bind is `127.0.0.1`. Binding to anything else requires `--allow-non-localhost` flag (Phase 2; v1 does not even accept that flag).
4. **Bounded parsing.** Every adapter parser has explicit limits:
   - max line length: 16 MB (anything larger logged as SourceError and skipped)
   - max event payload depth: 64 (anything deeper logged and skipped)
   - max events per file scan call: unbounded but periodically flushed
5. **No shell-out.** Never `exec.Command` for any user-facing operation. Pure Go parsing only.
6. **No symlink traversal escape.** When walking source directories, the adapter checks each path resolved via `filepath.EvalSymlinks` is still inside the configured root. Refuses to read files outside.
7. **No template injection.** All HTML rendered by the frontend uses React's auto-escaping. The server never renders HTML server-side; it serves static embedded files plus JSON.

## Payload Serving Safety

`GET /api/payloads/:ref` could be a vector if the `ref` is attacker-controlled. Defense:

- Refs are integer IDs from the `payload_refs` table, not arbitrary paths from the URL.
- The server looks up the `location_uri` from SQLite and validates it starts with `file://` and resolves inside one of the configured source roots before opening.
- The Content-Disposition header is set to `attachment` for any non-text payload; the browser will not execute it as a script.

## Sensitive Data In Fixtures

Test fixtures committed to the repo go through sanitization (see `testing-strategy.md`). CI grep-scans for common secret patterns (`AKIA`, `sk-`, `xoxb-`, bearer tokens, private IPs) and fails the build if any are found in `testdata/`.

## Dependency Hygiene

- `dependabot.yml` enables security updates for Go modules and npm.
- `golangci-lint` runs `gosec` linters as part of the standard config.
- `npm audit --audit-level=high` runs in CI; high/critical vulns fail the build.

## Future Auth Design (v2+)

When network exposure is needed, the design will be:

- HTTP Basic Auth at the reverse proxy layer (simplest), OR
- OIDC if the operator integrates with their existing identity provider.

The choice is the operator's; the SOW will record it. No auth is implemented in v1 because none is needed for localhost.

## Reporting Issues

If a security issue is found in ai-viewer, the operator should open a private GitHub security advisory on the repository. A `SECURITY.md` will be added at Phase 1 close.
