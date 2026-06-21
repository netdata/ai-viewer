# SOW-0093 — Perf: Sources / Search / Topology

**Status:** in-progress (chunks 1-3 shipped)
**Date:** 2026-06-21
**Goal:** Make the highest-traffic operator endpoints fast enough to feel
instant on the production DB (530k sessions, 7 sources, 8k-op biggest session).

## Problem

The operator's daily flow touches three endpoints that were either
catastrophically slow or timing out at 504 on a 7-source install:

| Endpoint | Before | Cause |
|---|---|---|
| `GET /api/sources` (default) | **1.66 s, 23 MB wire** | Cursor JSON blob of file offsets carried in every source item even though the operator's Sources page never reads it. |
| `GET /api/search?q=<term>` | **504 timeout (>30 s)** | (a) 3 sub-queries run sequentially; (b) the dominant sub-query evaluated `snippet()` + `bm25()` on every MATCH-matching row (60k for a common term like "permissions") before applying `LIMIT 51`. |
| `GET /api/topology?limit=200` (no time filter) | **1.46 s** | `ORDER BY size_metric DESC` on 530k sessions required a full scan + temp B-tree sort. With a time filter the same query is 16 ms. |

## Chunks

### Chunk 1 — `/api/sources` cursor opt-in ✅ `52883a1`
- Added `?include=cursors` opt-in flag.
- Default response: no cursor column projected, 546 B wire.
- 99.998 % size reduction, 97.7 % latency reduction on the default path.

### Chunk 2 — `/api/search` parallel + two-stage CTE ✅ `7b89ab4`
- Parallelized the 3 FTS5 sub-queries (ops / content / gated logs).
- Replaced single-shot FTS5 query with two-stage CTE: inner CTE does
  MATCH + ORDER BY bm25 + LIMIT; outer query joins back + computes
  snippet() (with outer MATCH re-stated so snippet() picks the
  matching column).
- Drop timeout → ~2.5 s wall clock for common terms.

### Chunk 3 — `/api/topology` index (this chunk)
- Add migration 0011 with indexes on the two ORDER BY columns the
  topology endpoint uses:
  - `idx_sessions_op_count` on `(op_count DESC, id ASC)` — metric=calls
  - `idx_sessions_duration` on `(total_duration_us_calc DESC, id ASC)` — metric=duration (default)
- Note: `total_duration_us` is the stored aggregate; the default
  `duration` metric actually uses `end_ts - start_ts` (only when
  end_ts IS NOT NULL), so the index needs a computed column OR a
  stored `total_duration_us_calc`. Decision: add a stored column
  populated by a one-shot backfill, then index it.

## Pre-Implementation Gate

**Root-cause model**: each chunk addresses a specific architectural
issue; tests exist for each; quality gates are green; SOW is up to
date. The chunks are independent and shippable separately.

**Affected surfaces**:
- Backend: `internal/presenter/{sources,search,search_content,search_logs,topology_cross}.go`, `internal/store/migrations/0011_*.sql`, schema_contract_test
- Frontend: no changes (all 3 fixes are server-side; existing client behavior unchanged)

**Spec deltas**:
- `rest-api.md` §GET /api/sources — note `?include=cursors` (chunk 1 done)
- `rest-api.md` §GET /api/search — note two-stage CTE (chunk 2 done)
- `rest-api.md` §GET /api/topology — note new index (chunk 3)

**Validation plan**:
- Each chunk has unit + integration tests; gates enforced.
- Production measurement: documented in each commit's message.

**Open decisions**:
- For chunk 3: stored column vs expression index? SQLite doesn't
  have expression indexes in older versions but modernc supports
  them. Decision: stored column is simpler + queryable by the
  UI, so use that.
- For chunk 3: backfill cost? 530k rows × 1 column = fast (<5s).

## Status

- [x] Chunk 1: `/api/sources` cursor opt-in
- [x] Chunk 2: `/api/search` parallel + CTE
- [ ] Chunk 3: `/api/topology` indexes
- [ ] (Follow-up) Chunk 4: `/api/sessions/:id` 1.3s on biggest session
