# SOW-0051 - Frontend Residual Complexity Reduction

## Status

Status: open

Sub-state: pending from SOW-0047 closeout. Not active yet.

## Requirements

### Purpose

Reduce frontend trace/topology visualization, non-visual component/state/
utility, and frontend tooling complexity while preserving the current design and
user-visible behavior.

### User Request

Continue maintainability cleanup autonomously. The operator owns visual design;
technical decomposition belongs to the assistant.

### Assistant Understanding

Facts:

- SOW-0047 closeout found frontend residual warnings in trace and topology
  surfaces, including `Waterfall.tsx`, `TraceTab.tsx`, `FlameGraph.tsx`,
  `TimelineRenderer.tsx`, `TopologyRenderer.tsx`, `ByTurnWaterfall.tsx`,
  `SpanDetailDrawer.tsx`, `Stats.tsx`, `SessionsList.tsx`, and visualization
  helpers under `frontend/src/viz/`.
- The same residual class includes smaller non-visual frontend helpers such as
  `StatusBadge.tsx`, `Tabs.tsx`, `frontend/src/lib/format.ts`, and
  `frontend/src/state/theme.ts`.
- A broader source/tooling scan also reports frontend tooling complexity in
  `frontend/scripts/check-bundle-size.js`; this SOW tracks that bucket if it is
  still present when activated.
- These are user-facing paths, so refactors must preserve layout, labels,
  interactions, accessibility, and existing visual behavior.

Inferences:

- Many warnings are likely from render callbacks and visualization layout
  builders. The right fix is separation of pure data preparation from compact
  React/D3 rendering components.

Unknowns:

- Which warnings can be reduced without visual changes must be determined by
  reading component tests, Playwright coverage, and current screenshots only if
  needed.

### Acceptance Criteria

- Frontend residual warnings are ranked by user-facing risk, tooling risk, and
  test coverage.
- Refactors preserve the current design and user-visible behavior.
- Component/unit tests and Playwright/axe coverage protect changed behavior.
- Strict Lizard/local Codacy findings are reduced, explicitly justified, or
  split into a narrower follow-up SOW before completion.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0047 closeout warning-only Lizard scan.

Current state:

- Backend and adapter cleanup has progressed further than frontend visualization
  cleanup. Trace/topology rendering now deserves its own focused SOW.

Risks:

- Visual regressions can reduce operator trust even when tests pass.
- Over-extracting React render logic can make components harder to follow.
- D3/Canvas/SVG changes can affect performance and accessibility.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Frontend visualization files combine data preparation, layout, styling
  decisions, and render callbacks. Smaller component/state/utility and tooling
  helpers also carry strict complexity warnings. That structure makes future
  visual and frontend-gate changes risky.

Evidence reviewed:

- SOW-0047 closeout warning buckets and affected frontend files.

Affected contracts and surfaces:

- Session detail trace, timeline, topology, span detail drawer, sessions list,
  stats page, visualization helpers, shared UI components, formatting/theme
  helpers, and frontend gate tooling scripts.

Existing patterns to reuse:

- Existing React component tests, Playwright/axe routes, `frontend/src/viz/`
  isolation for visualization logic, frontend tooling self-tests, and the
  SOW-0047 frontend filter characterization approach.

Risk and blast radius:

- Medium user-facing risk for UI surfaces, medium gate-risk for frontend
  tooling scripts, and low backend risk. No REST, SSE, schema, or adapter
  behavior change is expected.

Sensitive data handling plan:

- Use existing mock data and sanitized fixtures only. Do not add real prompts,
  tool output, personal data, or private session content.

Implementation plan:

1. Rank frontend visualization, component/state/utility, and tooling warning
   clusters by user-facing risk, gate risk, and current test coverage.
2. Add or strengthen component/Playwright tests before refactoring.
3. Extract pure data-preparation helpers from render callbacks where it reduces
   real complexity; split utility/tooling branches only when tests preserve the
   current behavior.
4. Preserve current design and interactions; do not make visual design changes
   without operator design input.
5. Validate with focused Vitest, Playwright/axe, strict Lizard, local Codacy,
   full gates, and external review.

Validation plan:

- Focused Vitest files selected after coverage audit.
- `cd frontend && npm run lint`
- `cd frontend && npm run typecheck`
- `cd frontend && npm test -- --run --coverage`
- `cd frontend && npm run e2e`
- `cd frontend && npm run e2e:a11y`
- Direct strict Lizard on changed frontend files.
- Local Codacy analysis on changed files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: `frontend-architecture.md` or `ui-pages.md` only if component
  contracts or user-visible behavior change.
- Runtime project skills: likely unaffected unless a new frontend decomposition
  pattern emerges.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal frontend maintainability work. External references are
  required only if a selected slice introduces or changes library behavior.

Open decisions:

- None for technical decomposition. Visual design changes are out of scope.

## Outcome

Pending.
