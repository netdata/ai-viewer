# SOW-0019 - SSE subscription cap (defense-in-depth)

## Status

Status: open

Sub-state: awaits operator approval before moving to current/. Surfaced by glm during the SOW-0001 Chunk-21 PR-A review as a P3 defense-in-depth note. Not blocking — ai-viewer is localhost-only/single-operator in v1, so the practical risk is near-zero; this matters only if/when network exposure is ever added (its own SOW).

## Requirements

### Purpose

Bound the resources a single client can consume by creating SSE subscriptions, so a buggy or malicious client cannot exhaust server memory by opening unbounded subscriptions. Fit-for-purpose: a small, clearly-documented cap that is invisible in normal single-operator use but closes an unbounded-growth path before any future network exposure.

### User Request

Not a direct operator request. Raised by the external reviewer (glm) reviewing the secret-scanner PR: `POST /api/subscriptions` has no cap, and each subscription holds a 256-event channel + a 100-event replay ring + hub map entries. On localhost with one operator this is harmless; it is noted for defense-in-depth.

### Assistant Understanding

Facts:
- `internal/presenter/subscriptions.go` creates a subscription per `POST /api/subscriptions` with no per-client or global limit; the hub (`internal/notify/hub.go`) holds each until DELETE or the 60 s reconnect window lapses.
- v1 is `127.0.0.1`-only with no auth (`security.md` §Scope), so there is no untrusted client today. The original cross-cutting review (SOW-0001 Chunk 21) explicitly classified this as a Phase-2 hardening, not a Phase-1 defect.

Unknowns:
- Whether the cap should be global, per-client (there is no client identity in v1 — all requests are loopback), or simply a global ceiling. To be decided at the Pre-Implementation Gate.

### Acceptance Criteria

- A bounded maximum number of concurrent SSE subscriptions, returning a structured error (e.g. `429`/`SERVICE_UNAVAILABLE`-class) when exceeded, documented in `rest-api.md` + `sse-protocol.md`.
- A test that the cap is enforced and that normal single-operator usage never hits it.
- No regression to the existing subscribe/replay/reconnect behavior.

## Analysis

Sources: `internal/presenter/subscriptions.go`, `internal/notify/hub.go`, `.agents/sow/specs/sse-protocol.md` §Backpressure, `.agents/sow/specs/security.md` §Scope.

Risks: low — additive limit. The main design question is the cap value + scope (global vs per-connection) given v1 has no client identity; pick a generous global ceiling so legitimate multi-tab use is unaffected. This SOW should not be started before there is a concrete need (e.g. the network-exposure SOW), per "do not design for hypothetical future requirements" — it is filed so the observation is not lost, not as immediate work.
