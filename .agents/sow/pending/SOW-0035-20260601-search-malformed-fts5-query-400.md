# SOW-0035 — `/api/search` malformed FTS5 query should return 400, not 503

Status: pending
Created: 2026-06-01
Origin: flagged during SOW-0007 Chunk 8b (`/api/search`) implementation.

## Problem

`GET /api/search?q=<malformed FTS5 expression>` (e.g. an unbalanced quote `"`,
a trailing `NEAR/`, or a bare `*`) currently returns **503 `DB_UNAVAILABLE`**
via the standard `p.writeDBError` path: SQLite's FTS5 `MATCH` parser rejects the
expression at query time, the error propagates as a generic DB error, and the
handler maps any DB error to 503.

This is a **client-input error misclassified as a server error**. It is surfaced
and logged (NOT a silent failure), but the operator typing a bad query gets a
scary "service unavailable" instead of "your query is malformed". Per
`AGENTS.md` §"No silent failures" the error is already visible; this SOW is about
correct HTTP semantics, not a hidden bug.

## Scope

- In `internal/presenter/search.go`, detect the FTS5 query-syntax error returned
  by `modernc.org/sqlite` for a malformed `MATCH` argument and map it to
  **400 `BAD_REQUEST`** with a clear message (e.g. `"malformed search query"`),
  distinguishing it from a genuine DB-unavailable 503.
- This needs the `modernc.org/sqlite` error-code/type import to classify the
  error (the FTS5 syntax error has a specific SQLite result code — confirm the
  exact code/`*sqlite.Error` shape against modernc; do NOT string-match the
  message, which is fragile).
- Spec delta: `rest-api.md` §"GET /api/search" — document that a malformed FTS5
  `q` returns 400 `BAD_REQUEST` (currently the spec only says empty/whitespace
  `q` → 400).

## Tests

- A malformed `q` (unbalanced quote) → 400 `BAD_REQUEST` (not 503).
- A genuine DB error still → 503 (do not over-broaden the 400 mapping).
- The classifier matches modernc's actual FTS5-syntax error (use a real
  malformed MATCH against the migrated schema, not a hand-built error value).

## Notes

- Small, additive, presenter-only. Mirror the existing `writeJSONError` /
  `writeBadFilter` patterns + the `#nosec`/`?`-bound conventions.
- The `q` value is already `?`-bound (no injection surface); this is purely
  error classification.
