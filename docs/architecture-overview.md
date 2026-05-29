# ai-viewer Architecture Overview (operator-facing)

A short, plain-English summary of how ai-viewer is built and why. For the full
engineering spec see `.agents/sow/specs/architecture.md`.

## The shape

```
your agent session files            one SQLite DB              your browser
(~/.ai-agent, ~/.claude, …)   ┌──────────────────────┐
        │  read-only           │  ai-viewer-ingest    │  writes
        └─────────────────────▶│  (watch + parse +    │────────┐
                               │   normalize)         │        ▼
                               └──────────────────────┘   index.db (canonical rows)
                                                                │ reads
                                          ┌──────────────────────▼─────────────┐
                                          │  ai-viewer-serve                    │
                                          │   GET /            embedded SPA      │──▶ http://127.0.0.1:7710
                                          │   GET /api/...     REST              │
                                          │   GET /api/events  SSE (live updates)│
                                          └─────────────────────────────────────┘
```

## Two binaries, one database

- **`ai-viewer-ingest`** watches your session directories, parses each format
  with a per-format *adapter*, and writes a single **canonical** representation
  into SQLite. It never calls a live agent system and never writes to your
  source files.
- **`ai-viewer-serve`** reads that database and serves the embedded web UI plus
  a REST + SSE API. It knows nothing about the source formats.

They are split so they can scale and (later) run on different hosts; today they
share the SQLite file plus a small in-DB notify table that lets serve push live
updates to the browser.

## Why these choices

- **SQLite as the canonical store** — indexed queries over millions of rows, a
  single-file deploy, concurrent reads while the ingester writes (WAL mode).
- **One canonical model, many adapters** — each source format (ai-agent v2/v3,
  claude-code, codex, opencode) is one Go adapter implementing one interface.
  New formats are additive; the UI and API never change shape.
- **SSE, not WebSocket** — the server pushes small "something changed"
  invalidation events; the browser re-fetches via REST. Trivially debuggable
  with `curl -N`, automatic reconnect, no extra protocol.
- **Single self-contained binary** — `scripts/build.sh` embeds the built UI into
  `ai-viewer-serve` via `go:embed`, so running the one binary serves the whole
  app; no Node or separate web server at run time.

## Live updates

The ingester records a row in a notify table as it commits changes; the server
polls that table read-only and fans out invalidation events over SSE. The
browser reacts by re-fetching the affected views — so the sessions list, a open
session, and the sources panel refresh on their own as new data arrives.

## Boundaries

Localhost-only, no authentication, read-only on your files, zero outbound
network calls. See [SECURITY.md](../SECURITY.md).
