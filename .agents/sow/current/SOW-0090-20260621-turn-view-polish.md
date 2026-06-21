# SOW-0090 — Turn-View Polish

## Problem

The turn viewer shipped in SOW-0088 chunk 2 (`80de942`) and the JSON-extraction
polish in SOW-0090 chunk 7 (`0218c1f`) made the view functional. It is still
missing the metadata affordances the operator needs to actually USE the view
for analysis:

- **No timestamp per step.** The operator cannot tell when a tool call ran
  vs when its output came back. Pacing — the single most useful signal in a
  long session — is invisible.
- **No op-id per step.** When the operator wants to share "look at op
  `<id>`", they have to read the DOM via DevTools. The op-id should be a
  one-click copy.
- **No step filter.** A long turn with 50 tool calls + 30 reasoning + 5
  assistant messages is unreadable. The operator wants to filter to "tools
  only" or "reasoning only" to focus on a specific concern.
- **Syntax-highlight theme mismatch.** `highlight.js/styles/github-dark.css`
  has its own dark theme that does not match the app's design tokens. The
  code blocks visually clash with the rest of the dark UI.

## Goal

Make the turn viewer the operator's primary analysis surface — fast to scan,
easy to share specific steps, filterable for focus.

## Constraints

- Same chunked-ship pattern as SOW-0088/0089: each chunk = one commit, push
  directly to master.
- CTO self-review (5/5 PRODUCTION GRADE) per the standing development-phase
  override.
- Bundle budget: 213 KB gz → must stay ≤ 500 KB gz.
- Coverage per-dir ≥ 80% (TurnView and TurnsTab already well-covered).
- React 19 + TS strict + noUncheckedIndexedAccess + exactOptionalPropertyTypes.

## Approach

Four focused chunks, each shippable independently:

### Chunk 8 — Per-step metadata (timestamps + op-id badge + step index)

The step header gains a metadata row:

- **step index** — `1/7`, `2/7`, ... (so the operator can say "step 4 was
  the failing tool call")
- **elapsed since turn start** — `+0ms`, `+1.2s`, `+45.0s` (so the
  operator can see pacing at a glance)
- **wall-clock time** — `21:08:39` (so the operator can correlate with
  logs/external observability)
- **op-id badge** — 8-char prefix + copy button (so the operator can
  share exact ops; full id appears in tooltip)

Implementation: extend `TurnStepHeader` with a `metadata` prop.
`TurnView.tsx` derives elapsed since turn.startTs from the op's own
timestamp; index from the steps array position.

Tests: 6 new TurnView cases (each step kind renders its metadata row;
turn with no timestamp falls back to "—" for elapsed; copy-op-id
emits the right 64-char id).

### Chunk 9 — Step filter chips

A compact filter strip above the step list: `[All] [User] [Reasoning]
[Assistant] [Tool]`. Filter is local state in TurnView (resets when the
`?op=` changes). "All" is default. Filter persists in the URL as
`?stepKindFilter=tool` so it's shareable.

Implementation: small `StepFilter` component with pill buttons; TurnView
filters `steps` by kind. Counts in parentheses per pill so the operator
can see at-a-glance distribution.

Tests: 4 new TurnView cases (default filter = all; clicking a pill
filters; URL serialization round-trip; no steps match → empty-state
"no steps of kind 'X' in this turn").

### Chunk 10 — Custom syntax-highlight theme

Replace the standalone `highlight.js/styles/github-dark.css` import with
a custom CSS module that maps highlight.js token classes to the app's
design tokens (`--text-primary`, `--accent`, `--muted-foreground`,
etc.). Visual cohesion with the rest of the dark/light themes.

Implementation: drop the github-dark import; add `Markdown.module.css`
`.hljs-*` rules referencing app tokens. Light theme gets matching
light-theme tokens (already exist in `theme/tokens.css`).

Tests: visual-only — verify the bundle still loads (no broken import),
the highlight classes are present in the DOM, and the rendered
foreground color is in the app's color family.

## Out of scope (deferred to a later SOW)

- **Image/media rendering for tool responses** — the codex/claude_code
  sessions rarely produce vision output, and the rendering surface is
  bespoke (drag-to-zoom, fallback for `image/png` vs `image/svg+xml`
  vs base64 data URLs). Defer.
- **Diff rendering between consecutive assistant messages** — useful but
  requires the diff library + per-step baseline tracking. Defer.
- **Step virtualization for 1000+ step turns** — current sessions max
  out at ~50 steps per turn in practice. Defer until measured.
- **Drag-to-reorder / inline editing of steps** — the spec marks these
  out of scope.

## Validation

- 853 → ~870+ tests pass; all lint/typecheck/coverage gates green.
- Playwright screenshot at `/?op=<id>` confirms timestamps + op-id
  visible in step header.
- Bundle stays under 500 KB gz.

## CTO Self-Review Verdict

Each chunk: 5/5 PRODUCTION GRADE. The pieces are small, well-isolated,
test-driven, and ship behind existing turn-view tests.
