# Security Policy

## Scope (v1)

ai-viewer v1 is **workstation-only, localhost-only, no authentication, read-only
on your source files**. The threat model is *"what could go wrong if this
software has a bug"* — not *"an attacker is trying to break in"*. It is a local
tool that reads the session trails your AI coding agents leave on disk and shows
them in a browser at `http://127.0.0.1:7710`.

## Guarantees

- **Read-only on source files.** The ingester opens your agent session files
  read-only; an ai-viewer bug cannot corrupt or delete them. It writes only its
  own SQLite database under `~/.local/share/ai-viewer/`.
- **Localhost bind.** Both the default and the only accepted bind are loopback.
  `ai-viewer-serve --bind` accepts ONLY literal `127.0.0.1` or `::1`. The string
  `localhost` is rejected (NSS/DNS or `/etc/hosts` could resolve it to a
  non-loopback IP and silently expose the server), and an empty host (`:7710`)
  is rejected (Go would bind `0.0.0.0`, every interface). There is no flag to
  bind a non-loopback address in v1.
- **No outbound network calls.** Neither binary makes any outgoing HTTP, DNS, or
  other network request. Cost/pricing data is a static table compiled into the
  binary, not an API lookup. Nothing you view leaves your machine.
- **No privilege.** The systemd integration is USER-level (`systemctl --user`),
  needs no root, and the install script never uses `sudo`.

## Not in scope for v1

Multi-user authentication, network exposure beyond localhost, and remote/hosted
deployment are explicitly out of scope. If those are ever added, they will land
in their own design with an explicit security review — not by loosening the
above.

## Sensitive data

Your session files can contain prompts, tool I/O, and other private content.
ai-viewer keeps all of it local (see "No outbound network calls"). The
committed test fixtures under `testdata/` are sanitized; do not commit real
session data.

## Reporting a vulnerability

This is an early-stage local tool. If you find a security issue, please open an
issue on the repository describing the problem and how to reproduce it. Because
v1 is localhost-only with no auth, the most valuable reports are bugs that break
one of the guarantees above (e.g. a path that escapes read-only, a way to bind
beyond loopback, or any outbound network call).
