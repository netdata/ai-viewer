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
| anything else | `<GenericStep>` | Header with op kind + name + status. Raw JSON of first payload. |

### Data flow

Each step lazily fetches its payload content on mount via a shared hook `usePayloadContent(payloadId)`:

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
- A 4 KB truncation shows a "Showing first 4 KB of <total>" footer.
- A network failure shows an error block (NOT a silent empty block — Hard Rule #6).

## Where this fits in the app

The TurnView is rendered in the right sidebar of the unified Session Detail shell (SOW-0088 chunk 4). Until that ships, the Trace tab's `SpanDetailDrawer` exposes a "Show in turn" button that opens the TurnView in a modal so the operator can validate the component end-to-end.

## Out of scope (deferred)

- Image/media rendering for tool responses (vision inputs). Future SOW.
- Diff rendering between consecutive assistant messages. Future SOW.
- Inline rendering of the sub-session's own turns (a sub-session step currently just links out). Future SOW.
- Token-level highlighting in LLM responses (requires tokenizer + offset tables). Not planned.