# SOW-0034 - Topology presenter polish (3 minor backend items)

## Status

Status: completed

Sub-state: proposed follow-up (P3 cluster). Filed 2026-06-01 from the SOW-0006 Round-4 external review (glm). These are minor backend code-quality items in the topology presenter — NOT user-facing defects and NOT hard-rule violations, so they were split out of SOW-0006 (which fixed the user-facing P2s + the no-silent-failure items in-line) to keep that milestone converging. Each must be verified on pickup before any change (the adjudicate-on-ground-truth rule — a reviewer finding is not a fact until confirmed against the code).

## Requirements

### Purpose

Keep the topology presenter (`internal/presenter/topology_cross.go`, `session_topology*.go`) clean and side-effect-free, with test seams that do not rely on mutable package state and no raw untrusted input echoed in error bodies.

### Assistant Understanding (to verify on pickup — reported, not yet confirmed)

1. **`maxTopologyNodes` is a mutable package `var` used for test injection.** A test overrides the 500-node cap by reassigning a package-level `var`. Mutable package state shared across tests is a smell (ordering coupling, no parallelism). Prefer a const default + an explicit cap parameter/option threaded through the builder, or a per-call field, so tests pass the cap rather than mutating a global. Verify the current shape first.
2. **Duplicate tree-sessions SQL query.** The per-session topology path may run the "sessions in this tree" query twice (once for nodes, once elsewhere). If confirmed, fetch once and reuse. Verify it is actually duplicated (not two different bounded queries) before changing — do not trade clarity for a micro-optimization that is not real.
3. **Raw user input echoed in a validation error message.** A bad `?metric=` value may be reflected verbatim in the `BAD_REQUEST` body. The response is JSON on a localhost-only bind (no HTML context → no XSS), so this is hygiene, not a security hole — but echoing arbitrary bytes into an error string is still poor practice. Confirm whether the raw value is included; if so, either omit it or whitelist/escape it (and reject control chars, consistent with the existing `rejectControlChars` pattern on path params).

NOTE (explicitly OUT of scope — already correct): the cross-session `ctx_pct` returning 0 is a DOCUMENTED limitation (`rest-api.md §GET /api/topology`: "cross-session ctx_pct over ops is out of scope for 6b — documented, not silently wrong"). Not a defect; no action.

### Acceptance Criteria

1. `maxTopologyNodes` is not a mutable package var relied on by tests (const default + injectable cap, or equivalent). **Verification**: the cap test passes the cap explicitly; no test mutates package state. `go test -race` clean.
2. The tree-sessions query is run at most once per request (if duplication is confirmed). **Verification**: a focused read + the existing presenter tests still pass; no behavior change.
3. No raw untrusted `?metric=` (or other untrusted input) is echoed verbatim in an error body. **Verification**: a test asserting a crafted bad `metric` does not appear raw in the response; control chars rejected.

## Analysis

Sources: `internal/presenter/topology_cross.go`, `internal/presenter/session_topology.go`, `internal/presenter/session_topology_builder.go`, and their `_test.go` files. Discovered 2026-06-01 (SOW-0006 R4 review, glm). Risk: low (presenter-internal, no contract change). Each item is independently revertible.

## Pre-Implementation Gate

(To be filled on pickup. Must: confirm each of the 3 items against the current code with file:line; decide the cap-injection shape; confirm the query duplication is real; write the error-message test first.)

## Implementation / Validation / Reviews / Outcome

(Empty placeholders.)

## Lessons / Follow-Ups

Pending. Parent: SOW-0006 (APM tracing UI). All three are presenter-local; none blocks SOW-0006.
