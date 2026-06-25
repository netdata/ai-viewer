# ui-turn-view — Turn View component contract

**Date**: 2026-06-21 (SOW-0088 chunk 2)
**Author**: CTO
**Status**: Living spec. New step renderers added incrementally as new op kinds are encountered.

## Why this document exists

The Session Detail view previously surfaced individual ops only as IDs + small field chips in the SpanDetailDrawer. The operator's feedback (SOW-0088 inception message): *"the user sees IDs, but there is nothing to answer the question 'is this the session/turn I am interested?'"*. The Turn View is the rich rendering of ONE turn that makes the answer to that question fast and obvious.

This spec is the contract for the Turn View component: what it renders, how it fetches data, how it scrolls, and what its sub-components look like. The wire-up to the unified Session Detail shell is in `SOW-0088` chunk 4.

## What a turn is

A turn is a sequence of ops the AI agent takes in response to ONE user message. The canonical schema records one row per turn in the `turns` table (presenter `TurnDetail`) with its ordered `ops`. Op kinds span: `'llm' | 'tool' | 'session' | 'reasoning' | 'internal' | 'system' | 'compaction'` (canonical OpKind).

A typical turn is:

```
1. internal  name=user_input      ← the user prompt
2. reasoning                       ← the assistant's chain-of-thought
3. tool       name=exec_command    ← the assistant's tool call
4. llm       name=message          ← the assistant's final text reply
```

Some turns are short (just `user_input → llm`). Some are long (multiple `tool` rounds interleaved with `reasoning`). Some spawn a sub-session (`session` op + `child_session_id`).

## Component contract

`TurnView` renders ONE `TurnDetail` as a vertical timeline. Each op becomes a "step card" (`<TurnStep>`). The component:

- **Header**: turn seq + op count + status + tokens in/out + cost + duration. Right-side: a `Copy turn` button (copies the rendered text to clipboard).
- **Body**: a scrollable list of `<TurnStep>` cards in `op.start_ts` order.
- **Empty/loading/error states**: handled by the parent (the Session Detail shell).

### Step renderers

| Op kind + name | Renderer | Notes |
|---|---|---|
| `internal` + `user_input` | `<UserPromptStep>` | Plain markdown, large text. Icon: user. |
| `reasoning` (any name) | `<ReasoningStep>` | Distinctly styled (italic + tinted background, slight indent). Markdown. Icon: brain. |
| `llm` + `message` | `<AssistantStep>` | Plain markdown, large text. Icon: message. |
| `tool` (any name) | `<ToolStep>` | Tool name as header. Two stacked sections: Params (mono, syntax-highlighted JSON or text) + Response (same). Each with a copy button. Icon: tool. |
| `llm` + other name (rare) | `<AssistantStep>` | Same as message. |
| `session` | `<SubSessionStep>` | One-line "Spawned sub-session <child_session_id>". Link to the sub-session. |
| `compaction` | `<CompactionStep>` | Info pill: "Compaction". |
| anything else | `<GenericStep>` | Header with op kind + name + status. Semantic fallback for the first available payload. |

### Data flow

Each step chooses payload refs by semantic kind/class, not array position. The
raw `payload_refs.kind` remains available in payload metadata for provenance;
the backend-derived `artifact_class` drives rendering:

- LLM request/response: `llm_request`, `llm_response`.
- SDK request/response: `llm_sdk_request`, `llm_sdk_response`, rendered through
  the LLM request/response path with an `SDK` label.
- Reasoning text: `reasoning_text`, rendered as prose/markdown.
- Tool parameters/results: `tool_request`, `tool_response`.
- Logs/fallback: `log`.

Legacy fixture-only payload kinds such as `request`, `response`, `reasoning`,
and `raw` are not valid payload-ref dispatch keys. Missing, duplicate, or
`source_unavailable` refs render an explicit fallback state instead of silently
choosing a different array element.

Each step lazily fetches selected payload content on mount via a shared hook
`usePayloadContent(payloadId)`:

- `useState<string | null>` for the fetched text
- `useState<boolean>` for loading
- `useState<string | null>` for error
- AbortController to cancel on unmount
- 4 KB hard cap (server-enforced; mirror the X-Payload-Truncated / X-Payload-Total-Bytes headers)
- Per-op cache: a Map of payloadId → { text, status } in a top-level context so revisiting the same op doesn't refetch
- No revalidation on focus; payloads are immutable

### Markdown rendering

`react-markdown` + `remark-gfm` for content with prose (UserPromptStep, AssistantStep, ReasoningStep). `rehype-highlight` (which wraps `lowlight` ≈ 5 KB gz) for fenced code blocks. No `react-syntax-highlighter` (too heavy). Highlight theme: `github-dark` for both themes — slightly higher contrast in light theme, consistent in dark.

Code in tool Params/Response blocks uses the same `rehype-highlight` path; we wrap the params/response text as a fenced code block with the detected language (best-effort JSON detection for tool params).

### Visual structure

```
┌─ Turn #1 ─ 4 ops · 2.3s · $0.012 · completed ─────────────────[Copy turn]┐
│                                                                            │
│  👤  User                                                                  │
│  ──────────────────────────────────────────────────────────────────────    │
│  Please fix the broken CSS in the sidebar. The active item is              │
│  blue-on-blue in dark theme...                                            │
│                                                                            │
│  🧠  Reasoning                                                             │
│  ──────────────────────────────────────────────────────────────────────    │
│  The CSS bug is in app.css. There's an `a { color: var(--primary) }`      │
│  rule that's overriding the Tailwind utility...                            │
│                                                                            │
│  🛠   exec_command                                                          │
│  ──────────────────────────────────────────────────────────────────────    │
│  Params                                       Response                     │
│  ┌────────────────────────────────┐  ┌────────────────────────────────┐    │
│  │ $ npm test                    │  │ > PASS 42 tests                │    │
│  │                                │  │                                │    │
│  └────────────────────────────────┘  └────────────────────────────────┘    │
│   [📋 copy]                             [📋 copy]                          │
│                                                                            │
│  💬  Assistant                                                             │
│  ──────────────────────────────────────────────────────────────────────    │
│  I've fixed the CSS bug. The issue was in `app.css` line 300 — an         │
│  element-level rule was overriding the Tailwind utility...                 │
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
```

### Scroll-to behavior

The component accepts a `focusOpId?: string` prop. When set, on mount + on prop change, the component scrolls the matching `<TurnStep data-op-id={...}>` into view and applies a 2-second pulse animation (CSS keyframes) so the operator can see which step was focused by a click on the trace graph.

The pulse: `outline: 2px solid var(--accent)` for 200ms, then fade out over 1800ms. Implemented as a CSS keyframe, not JS, so it's cheap.

### State

- The component is fully controlled by its parent (the Session Detail shell).
- No global state. The payload cache (per-id) is local to one TurnView instance.
- URL params: the parent may pass `focusOpId` from a URL query param `?op=<id>` for shareable links.

### Accessibility

- Each step has `data-op-id={op.id}` for programmatic access + tests.
- Step headers use `<h3>` with a clear visual hierarchy (one per step).
- Copy buttons have `aria-label="Copy code"` / `"Copy prose"` etc.
- The scroll-into-view + pulse animation respects `prefers-reduced-motion: reduce` (skips the animation, still scrolls).
- Markdown content is keyboard-navigable (default react-markdown behavior).

### Failure modes

- `usePayloadContent` errors render an inline error block ("Failed to load payload: <reason>") with a `Retry` button.
- A truncated payload shows a "Showing first <preview> of <total>" footer using
  the payload streaming headers.
- A network failure shows an error block (NOT a silent empty block — Hard Rule #6).
- A proof/debug affordance may show masked selector/path metadata; full selector
  copy is an explicit action and is never primary step chrome.

## Where this fits in the app

The TurnView is rendered in the right sidebar of the unified Session Detail shell (SOW-0088 chunk 4). Until that ships, the Trace tab's `SpanDetailDrawer` exposes a "Show in turn" button that opens the TurnView in a modal so the operator can validate the component end-to-end.

## Out of scope (deferred)

- Image/media rendering for tool responses (vision inputs). Future SOW.
- Diff rendering between consecutive assistant messages. Future SOW.
- Inline rendering of the sub-session's own turns (a sub-session step currently just links out). Future SOW.
- Token-level highlighting in LLM responses (requires tokenizer + offset tables). Not planned.

---

# ui-session-unified-view — unified Session Detail shell (SOW-0088 chunks 3+4)

**Date**: 2026-06-21
**Status**: Living spec for the new session-detail chrome.

## Why this exists

The operator's verbatim feedback (SOW-0088 inception): *"I have the impression that all the different session views should only be one."* The current Session Detail page is 6 tabs (Overview, Trace, Turns, Topology, Timeline, Logs, Raw). Switching tabs throws away the operator's mental context — they were looking at a span, clicked Overview to check tokens, and now have to find the span again. The unified view collapses every per-session view into ONE 3-zone shell so the operator never loses context.

This spec is the contract for the unified shell. It does NOT replace the TurnView component (covered by `ui-turn-view.md` above) — TurnView is one of the panes in this shell.

## The shape

```
┌────────────────────────────────────────────────────────────────────┐
│  Breadcrumb · Session header · Pin button                          │
├────────────────────────────────────────────────────────────────────┤
│  Overview tiles (full width):                                      │
│   [Status] [Active] [Duration] [Tokens in→out] [Cost] [Failures]  │
├──────────────────────────────────┬─────────────────────────────────┤
│  Visualization tabs              │                                 │
│  [Waterfall][Topology][Timeline] │   Turn View (right sidebar)      │
│  [Statistics]                    │                                 │
│  ┌────────────────────────────┐  │   [turn picker chips]            │
│  │   <viz with inner scroll>  │  │   ┌──────────────────────────┐  │
│  └────────────────────────────┘  │   │   Turn #1 · 4 ops        │  │
│                                  │   │   ────────────────────  │  │
│  Bottom tabs (resizable ↑↓)      │   │   👤 User prompt         │  │
│  [Event list][Logs][Raw]         │   │   🧠 Reasoning           │  │
│  ┌────────────────────────────┐  │   │   🛠️ Tool call/response  │  │
│  │   <event list / logs / raw │  │   │   💬 Assistant           │  │
│  │    with inner scroll>      │  │   │                          │  │
│  └────────────────────────────┘  │   │   [focused step pulses]   │  │
│                                  │   └──────────────────────────┘  │
│  <resize handle between viz and bottom>                          │
└──────────────────────────────────┴─────────────────────────────────┘
        ↑ resize handle between left (viz+bottom) and right (turn view) ↑
```

## Zones

| Zone | Component | Resizable? | Scrollable? | Persisted |
|---|---|---|---|---|
| Header (breadcrumb + pin) | existing `SessionDetail` header | no | no | n/a |
| Overview tiles | condensed `OverviewStats` row | no | no | n/a |
| Left column (viz + bottom) | `PanelGroup` (vertical) | yes (inner split) | each pane scrolls independently | localStorage `ai-viewer.session.vbottom` |
| Right column (turn view) | single panel | yes (width vs left column) | yes (turn view scrolls) | localStorage `ai-viewer.session.vright` |
| Visualization tabs | `<VizTabs>` inside the left top panel | no | inner scroll | n/a |
| Bottom tabs | `<BottomTabs>` inside the left bottom panel | no | inner scroll | n/a |

The viz and bottom tabs share the LEFT column; the turn view is the RIGHT column. Both columns are resizable horizontally; the viz/bottom split is resizable vertically.

## Click-on-graph → scroll-to-turn

This is the critical UX bridge. When the operator clicks ANY span / op / event in the LEFT pane:

1. The span detail drawer opens (existing behavior, unchanged) — local to the left pane.
2. **NEW**: a URL search param `?op=<op.id>` is set. The right pane reads it.
3. The right pane finds the turn that owns this op and renders it.
4. The right pane scrolls the matching step into view + pulses it (existing focusOpId behavior from `ui-turn-view.md`).

The click-on-graph path:

- Waterfall click → setSelected(op) → URL updates `?op=<id>` → right pane focuses
- Event list row click → same path
- By-turn card click → same path
- Topology node click → does NOT update ?op (topology nodes aren't ops)

The URL is shareable: paste `?op=<id>` and the right pane lands on the matching turn.

## Visualization tabs

The current Trace tab becomes the Waterfall tab in the new shell. Other tabs in the viz zone:

- **Waterfall** (default) — the existing Waterfall + ByTurnWaterfall with Detailed/By-turn toggle.
- **Topology** — the existing per-session Topology tab.
- **Timeline** — the existing Timeline tab.
- **Statistics** — the existing /stats page limited to this session (token rollup, cost, duration histogram).

## Bottom tabs

Three bottom tabs, all reading the same session data:

- **Event list** — the existing EventList (rename from current Trace tab's bottom section).
- **Logs** — the existing LogsTab.
- **Raw** — the existing RawDataTab.

These three are equivalent in importance; the default is Event list.

## Overview tiles

A condensed strip of 6 stat tiles:

| Tile | Source | Meaning |
|---|---|---|
| Status | sessions.status | completed / failed / running / abandoned / interrupted |
| Duration | sessions.start_ts / end_ts | human-readable |
| Tokens in | aggregate over all turns | compact (e.g. "70.9k") |
| Tokens out | aggregate | compact |
| Cost | sum(cost_usd) over all turns | `$X.XXXX` |
| Failures | count of ops with error_class != null | integer |

The current full OverviewTab is removed from the tab strip but its components are reused inside the tiles row. The "Status pulse" running animation, the ContextPressure badge, the agent/model summary — all stay reachable via the tiles' tooltips.

## Persistence

- Left/bottom vertical split: `react-resizable-panels` `autoSaveId="ai-viewer.session.vbottom"` (default 65/35).
- Left/right horizontal split: `autoSaveId="ai-viewer.session.vright"` (default 70/30).
- Default layout: 65% viz / 35% event list vertically; 70% left / 30% right horizontally. The defaults make the right pane usable (~480px on a 1440px viewport) without overwhelming the visualization.
- Active tab in the viz zone: URL `?tab=viz:waterfall` (etc). Falls back to `waterfall`.
- Active tab in the bottom zone: URL `?tab=bottom:events` (etc). Falls back to `events`.
- Focused op: URL `?op=<id>` (shared with the existing per-tab `focusOpId` plumbing).

## Empty / loading / error states

The unified view inherits all per-pane empty/loading/error states (e.g. Topology shows "no edges yet" if there are no agent↔tool relationships). The outer shell renders a single `<LoadingState>` / `<ErrorState>` if the session itself fails to load.

## What this replaces

- Existing `TraceTab` → its top (viz) becomes the Waterfall viz tab; its bottom (Event list) becomes the Event list bottom tab.
- Existing `TurnsTab` → removed as a top-level tab; its content is now the right sidebar (Turn View).
- Existing `TopologyTab`, `TimelineTab`, `LogsTab`, `RawDataTab` → folded into viz/bottom tabs.
- Existing `OverviewTab` → condensed to the top tiles row.

The legacy tabs are removed from the `?tab=` URL schema. A bookmark to `?tab=trace` falls back to the unified view with Waterfall/Event list pre-selected.

## Accessibility

- Each panel has `role="region"` and `aria-label`.
- The resize handles have `aria-label="Resize panel"` and are keyboard-resizable (arrow keys).
- The URL params for focused op and active tabs are keyboard-accessible via standard browser back/forward.
- The turn view's scroll-to behavior respects `prefers-reduced-motion` (existing).

## Out of scope (deferred)

- Drag-and-drop to reorder steps within a turn.
- Inline editing of a turn's steps.
- Cross-session comparison in the right sidebar.
- Pinning specific turns to the top of the right pane.
