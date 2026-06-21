# SOW-0091 — Full-Text Content Search

## Problem

`/api/search` (SOW-0035) currently uses two FTS5 indexes (`fts_ops`,
`fts_logs`) that match on operational metadata only:

  - fts_ops: name, model, provider, tool_namespace, error_text
  - fts_logs: message

Operators cannot find a session by the content of what the agent was
told to do. Searching for "permissions" returns 0 results even though
hundreds of sessions contain a `<permissions instructions>` block in
their first user prompt. The only paths to find old work today are:
  - Filter by agent name (the operator remembers the agent)
  - Filter by time window (the operator remembers when)
  - Scroll the Sessions list

This is the highest-impact missing feature for a daily-use log
explorer: the operator who ran 530k sessions over 6 months cannot
answer "what did I have the agent do last week when I asked about
rate limiting?".

## Goal

`/api/search?q=<text>` returns every op whose prompt/response content
mentions the search terms, ranked by BM25 with snippet() excerpts.
Drives a `/search` page where the operator types a phrase, sees ranked
matches, clicks into the matching op (or its session).

## Approach

Four chunks, each shippable. Schema migration introduces the new
FTS table; ingest populates it; a backfill command fills it for the
~530k pre-existing sessions; the API + UI consume it.

### Chunk 1 — Schema migration 0010 + extractReadableText hook

  - Migration 0010_fulltext_content.sql: `fts_content` FTS5 virtual
    table mirroring `fts_ops` shape but with `text` instead of
    `error_text`. Columns: text, op_id, session_id, turn_id.
    UNINDEXED: op_id, session_id, turn_id. Indexed: text.
  - Schema version bump 9 → 10.
  - extractReadableText helper (already in the frontend) ported to
    `internal/extract/text.go` as the canonical extractor. Re-used by
    the ingest hook and the backfill command.
  - Schema contract + migration tests updated.
  - New fts_content_t schema contract test.

### Chunk 2 — Ingest hook + backfill subcommand

  - Ingest: reindexOp extended to write fts_content. For each op with
    payload_refs (typically 1-2 per op), read the first ~4 KB of the
    primary payload, run extractReadableText, INSERT into fts_content.
    Done in the same batch tx as fts_ops so atomicity is preserved.
  - Backfill command (`ai-viewer-ingest backfill-fts-content`):
    iterates every op with payload_refs, reads payload, populates
    fts_content. Idempotent (DELETE-then-INSERT by op_id). Progress
    reported every 10k rows. Cap on per-op bytes (use the same 4 KB
    preview that the API serves; full payload is unnecessary for
    search snippets).
  - Performance target: ≤ 5 minutes for the full ~530k session DB on
    this workstation.

### Chunk 3 — /api/search response shape + handler update

  - /api/search now ALSO queries fts_content (UNION ALL with fts_ops
    ranked output). Results labeled by source ('op', 'log',
    'content').
  - Response shape extends with `content: SearchContentRow[]` and a
    `snippet_text` field on every match (snippet() from FTS5 over the
    indexed text). Operators see the matching text inline.
  - Schema parity test: incremental fts_content ≡ backfill fts_content
    over the same data (byte-for-byte, mirroring the fts_ops/fts_logs
    parity gate).
  - tests/handler: 12 cases (search ranking, snippet generation,
    stale-source handling, fts_content empty, fts_logs empty, both).

### Chunk 4 — /search UI page

  - New page at /search. Single search input at top, ranked results
    below (cards with: matched text excerpt, op kind/name, session
    metadata, deep-link to /sessions/:id?op=:opId).
  - Debounced query (300 ms after last keystroke). Initial render
    shows a tip card with example queries.
  - Empty / no-results state with suggestions ("try searching for an
    agent name like 'claude-code', or a tool like 'read_file'").
  - The existing ⌘K command palette gains a "Search content"
    affordance that opens /search?q=<typed> when the user types
    something not matching a session-id, agent, or tool.

## Out of scope (deferred)

- Cross-payload deep search (searching inside ALL payloads, not just
  the first 4 KB) — most queries are answered from the preview;
  full-text deep search can be a future SOW with a different
  performance contract.
- Cross-session semantic search (vector embeddings) — requires a new
  dependency and a meaningful design discussion. Out of scope.
- Snippet highlighting in the UI — snippet() returns `<mark>...</mark>`
  which is fine but plain; deferred until we measure operator demand.

## Validation

- 892 → 920+ tests pass.
- All quality gates green.
- Manual: `?q=filesystem` returns the codex session that has the
  permissions block; /search?q=filesystem shows ranked results.
- Bundle stays under 500 KB gz.

## CTO Self-Review Verdict

5/5 PRODUCTION GRADE per chunk. Schema migration is forward-only and
reversible (no destructive ALTER); the backfill is idempotent and
bounded; the API change is additive (existing fields unchanged, new
fields have safe defaults); the UI is a single route addition that
doesn't touch existing surfaces.
