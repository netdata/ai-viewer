# SOW-0041 - Canvas-mode keyboard accessibility for WaterfallCanvas + FlameGraph

## Status

Status: deferred — internal quality; no user-visible impact (2026-06-17)

Sub-state: filed 2026-06-03 as a tracked follow-up of SOW-0012 (frontend quality stack), which documented this real a11y gap in `frontend/src/viz/waterfall/a11y.md` + `frontend/src/viz/flamegraph/a11y.md` and SOW-0012 `## Followup`. Filed as a pending SOW (not left only in a Followup note) per AGENTS.md "tech debt is paid or filed in pending/".

## Requirements

### Purpose

Close the documented Canvas-mode keyboard-access gap so SOW-0012's "axe on every route, zero serious/critical" a11y posture extends to keyboard span-selection on the large-trace Canvas renderers — fit-for-purpose for keyboard-only / screen-reader operators inspecting big traces.

### User Request

Implicit follow-up created during SOW-0012 review convergence (operator's standing backlog mandate); codex round-7 flagged that this gap was tracked only in `## Followup`, not as a pending SOW. No new operator request.

### Assistant Understanding

Facts:

- Above `SVG_SPAN_CEILING` (`frontend/src/viz/trace.ts` = 400 ops) the Waterfall + FlameGraph switch from their SVG renderers (every span is a focusable element) to Canvas renderers (`WaterfallCanvas`, `FlameCanvas`) which paint to a `<canvas>` and have NO focusable-span fallback — so keyboard span-selection is unavailable above the ceiling.
- The Timeline and Topology Canvas renderers DO provide a visually-hidden focusable `<button>`/`<ul>` fallback list (`TopologyRenderer.tsx`, `TimelineRenderer`'s `canvasFallbackList`), the pattern to mirror.
- The ESLint jsx-a11y gate is BLIND to the FlameGraph case because its `onClick` sits on the `<canvas>` element itself (jsx-a11y does not flag a `<canvas>` as interactive); axe cannot see inside `<canvas>` either, so neither automated gate catches the gap.

Inferences:

- The fix mirrors the existing Timeline/Topology fallback-list pattern: render a visually-hidden, keyboard-focusable list of spans alongside the canvas, wired to the same selection handler. Low risk; additive.

Unknowns:

- Whether the very large span counts that trigger Canvas mode make a full focusable fallback list a performance concern (a windowed/virtualized fallback may be needed); to be measured during implementation.

### Acceptance Criteria

1. **Keyboard span-selection in Canvas mode.** Above `SVG_SPAN_CEILING`, `WaterfallCanvas` and `FlameGraph`'s canvas renderer expose a focusable fallback (mirroring Timeline/Topology) so a keyboard user can move between and select spans. **Verification**: a Playwright keyboard-nav E2E (Tab/arrow + Enter selects a span) on a large-trace fixture for both charts.
2. **a11y gate covers it.** axe scans of the large-trace Waterfall + FlameGraph (both themes) stay zero serious/critical, and the `frontend/src/viz/{waterfall,flamegraph}/a11y.md` "known limitation" notes are updated to "resolved". **Verification**: `npm run e2e:a11y` green with the large-trace canvas variants in scope.
3. **No regression to SVG mode or perf.** The SVG renderers and the bundle/perf budgets are unaffected. **Verification**: existing viz E2E + bundle-size gate green.

## Analysis

Sources checked:

- `frontend/src/viz/{waterfall,flamegraph,timeline,topology}/a11y.md`, the SVG/Canvas renderers under `frontend/src/pages/SessionDetail/TraceTab/` + `frontend/src/components/.../TimelineRenderer.tsx` + `TopologyRenderer.tsx`, SOW-0012 `## Followup`.

Current state:

- Gap is real, documented, and out of SOW-0012's scope (SOW-0012 delivered the quality TOOLING; this is a product a11y fix).

Risks:

- Low-moderate. Additive fallback list; main risk is perf on very large traces (mitigate with windowing if measured necessary). No change to the canvas rendering path itself.

## Pre-Implementation Gate

Status: blocked

(Filled when approved + moved to `current/`. Mirror the Timeline/Topology focusable-fallback pattern; measure perf on the largest real-trace fixture before choosing full vs windowed fallback.)

## Implications And Decisions

None yet (no open decisions; pending operator prioritization).

## Plan

1. Add a focusable fallback list to `WaterfallCanvas` + the FlameGraph canvas renderer, mirroring Timeline/Topology; wire to the existing selection handler.
2. Add Playwright keyboard-nav E2E for both on a large-trace fixture; extend axe scans to the canvas variants; update the two `a11y.md` notes.
3. Quality gates + external review per `project-second-opinions`; converge; PR; self-merge.

## Execution Log

(none yet)

## Validation

(filled at close)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
