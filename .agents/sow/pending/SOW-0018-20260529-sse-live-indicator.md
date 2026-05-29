# SOW-0018 - Visible SSE live indicator + realtime entrance animations

## Status

Status: open

Sub-state: awaits operator approval before moving to current/. Surfaced during SOW-0001 Chunk-18 (Playwright E2E): D4's deferred half. Not blocking — Phase-1 data already refreshes live; this is the visible UX layer on top.

## Requirements

### Purpose

Implement the forward-looking Realtime UX rules in `ui-pages.md` §"Realtime UX Rules" that Phase-1 left unbuilt: a visible **live indicator** (small pulsing dot in the header) reflecting SSE connection state, going amber with a "reconnecting…" tooltip on disconnect; plus the entrance/value animations (rows fade in, counters animate). Fit-for-purpose: the operator gets at-a-glance confidence the view is live, and new data arrives without jarring jumps.

### User Request

Not a direct operator request. Raised by the assistant during Chunk-18 E2E: the SSE liveness could only be asserted at the protocol level (subscription POST + EventSource GET) because `useLiveUpdates` surfaces no connection state to the DOM and no component renders an indicator. `ui-pages.md` §"Realtime UX Rules" specs these as the eventual target; they are now explicitly marked "Phase-2: not yet implemented" there.

### Assistant Understanding

Facts:
- `frontend/src/api/sse.ts` already exposes a `ConnectionStatus`/`onStatus` concept, but `frontend/src/state/useLiveUpdates.ts` does not surface it, and no component (Layout/header) renders an indicator. Verified during Chunk-18 (grep: no `onStatus`/`connected`/live-dot wiring).
- `resync` handling IS implemented (`sse.ts` invalidates all queries); only the VISIBLE indicator + animations are missing.
- Chunk-18 E2E (`frontend/tests/realtime.spec.ts`) asserts SSE at the protocol level; once a visible indicator exists, an E2E assertion on its connected/amber state should be added, plus (if feasible) a true "new session fades in" test — which needs a way to append a session to the seeded DB mid-test (the ingester is read-only over static fixtures, so this needs a test-only writer or a second ingest pass against a live-watched dir).

Unknowns:
- The cleanest test-only mechanism to trigger a live append for the fade-in test (second ingest pass into a watched source dir vs. a test seam). To be decided at gate time.

### Acceptance Criteria

- A header live indicator reflects SSE connection state (connected = steady/pulsing dot; disconnected/reconnecting = amber + accessible "reconnecting…" affordance), driven by `useLiveUpdates` surfacing `sse.ts` connection status. Accessible (not color-only).
- Row entrance fade-in (≤200ms) and counter/stat value animation per `ui-pages.md`.
- Unit tests for the indicator state mapping; an E2E assertion on the connected indicator; and (stretch) an E2E that a newly-ingested session fades into the list.
- `ui-pages.md` §"Realtime UX Rules" updated to mark these Implemented.

## Analysis

Sources: `frontend/src/api/sse.ts` (ConnectionStatus), `frontend/src/state/useLiveUpdates.ts`, `frontend/src/components/Layout*`, `ui-pages.md` §"Realtime UX Rules", `frontend/tests/realtime.spec.ts`.

Risks: low — additive UI. The fade-in animation must not regress a11y (respect `prefers-reduced-motion`). The live-append E2E is the only non-trivial part (needs a controlled way to mutate the seeded DB).

## Pre-Implementation Gate

Status: needs-gate (fill when activated). Open question: the live-append test mechanism (above).

## Execution Log

None yet.
