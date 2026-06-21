# SOW-0089 — Data quality fixes (codex parser, deterministic related sessions, running-status hygiene)

**Date**: 2026-06-21
**Author**: CTO
**Status**: Current — chunks 5+6 of SOW-0088
**Operator feedback addressed**: items (3), (4), (5) of the operator's verbatim list (see `current/SOW-0088-20260621-session-view-refactor.md`).

## Why this SOW exists

The operator flagged three data-quality issues after the SOW-0080–0087 UX work shipped:

> 3. "related sessions are 'possibly related', although they could probably be matched accurately, because we have the prompt the agent was run, so we can do this match deterministically"
> 4. "codex sessions miss turns, although it is obvious there are repeated 'reasoning' and 'message' entries"
> 5. "the session marked as 'running' are not always running. I think the system incorrectly marks 'running' way too many sessions"

Each item maps to one backend change below. The frontend consumes the new fields without any UI changes in this SOW (the existing Related Sessions card on SessionDetail and the existing Status badge pick them up automatically).

## Scope

### Change A — Running-status hygiene (SOW-0089 chunk 5a)

**Today**: a session's `status` column is set by ingest to `running` when its source process is alive. The column is a snapshot — it never flips to `completed` once the process dies (the codex CLI doesn't always emit a clean exit event; the watcher stops tailing on EOF but the status stays `running`).

**Fix**: in the presenter, derive an `effective_status` from `end_ts` and `last_activity_ts`:
- `completed` if `end_ts IS NOT NULL`
- `running` if `last_activity_ts IS NULL OR last_activity_ts > now - 10min`
- `stale` if `last_activity_ts <= now - 10min AND end_ts IS NULL` (process gone, no clean exit; treat as completed for status UX but mark separately so the UI can show "stale · 26m" instead of "running" — StaleBadge already handles this)

The fix is presenter-side only. No schema migration. No re-ingest. `effective_status` is computed on every read.

The StaleBadge + Overview tiles already wire `last_activity_ts` in, so the operator sees the stale badge once this is in. The "running" pill in OverviewTiles will flip to "stale · Nm" automatically.

### Change B — Deterministic related sessions (SOW-0089 chunk 5b)

**Today**: `GET /api/sessions/:id/related` joins on `(cwd, source_id !=, start_ts within run window)`. That's a heuristic — two sessions in the same dir at the same time get matched even when they're unrelated (e.g. two different agents in the same repo).

**Fix**: ingest computes a `first_user_message_hash` for every session and stores it on the `sessions` table. The related-sessions query switches from "same cwd" to "same first_user_message_hash". Two sessions that started from the same initial prompt — regardless of which harness ran them, regardless of working directory — become deterministic matches.

Implementation:
1. **Schema migration**: add `first_user_message_hash TEXT` column to `sessions`. Indexed. Nullable (backfilled).
2. **Ingest change**: as a session's first `OpInternalKind=user_input` op is emitted, capture its payload text, hash it with `sha256`, and write the hash to a per-session context field that gets flushed to the `sessions` row at finalize.
3. **Backfill**: a one-time SQL operation that walks every session with `first_user_message_hash IS NULL`, finds its first `user_input` op's payload, computes the hash, and updates the column. ~3,000 sessions × ~1 ms each = a few seconds.
4. **Presenter change**: `loadRelatedSessions` joins on `first_user_message_hash` instead of `cwd`. The response's `reason` field updates to "same initial prompt".

### Change C — Codex sub-turn splitting (SOW-0089 chunk 5c)

The operator's verbatim: "codex sessions miss turns, although it is obvious there are repeated 'reasoning' and 'message' entries".

**Root cause** (verified against the production SQLite): a codex rollout file has many `response_item.message` / `response_item.reasoning` / `response_item.function_call` entries, but the `event_msg.task_started` boundary fires only ONCE per real codex "task" (which can span hundreds of internal exchanges). Result: the adapter produces 18 turns for the busiest session but the file contains 1104 user messages and 2309 reasoning + function_call entries. The UI looks "correct" (no errors) but the turn count doesn't reflect what the user sees on screen.

**Fix**: in the codex adapter only, add a sub-turn boundary. Whenever a `response_item.message` with `role=user` arrives AND the current turn already has at least one `OpInternalKind=user_input` op AND no open tool call is pending, finalize the current turn and open a new one. Sub-turns get the same `codex_turn_id` namespace (the original task) but a new `Seq` value.

Concretely, when the codex mapper sees `response_item.message` with `role=user`:
1. If a turn is open and has had user input before: finalize the current turn (call `finalizeTurn`), then `openTurn("sub:"+uuid, tsUs)` to start a sub-turn.
2. The new sub-turn inherits the parent's `codex_turn_id` so the `TurnDetail.codex_turn_id` field stays consistent.
3. If no turn is open: open a fallback turn as today.

This only affects codex; other adapters don't change. No schema migration (we already have a `turns.seq` per session). The `_backfill` flag is exposed for re-ingesting existing codex rows cleanly without a code migration.

## Pre-Implementation Gate

- [x] Problem/root-cause model verified against production SQLite (running sessions 0%, codex parser behavior, related sessions heuristic).
- [x] Evidence reviewed: 1,295 sessions in DB; sample codex file has 1104 user prompts mapped to 18 turns.
- [x] Affected contracts: `internal/presenter/sessions_list.go`, `internal/presenter/session_detail.go`, `internal/presenter/session_related.go`, `internal/adapters/codex/*.go`, `internal/store/migrations/` (new file), `internal/ingest/persist.go` (hash flush).
- [x] Spec deltas: `rest-api.md` §GET /api/sessions/:id (new effective_status), `rest-api.md` §GET /api/sessions/:id/related (new reason field), `data-model.md` §sessions (new first_user_message_hash column), `adapter-codex.md` §Sub-turn splitting.
- [x] Existing patterns: presenter's `nowUs` pattern (Change A), ingest's per-session context flush (Change B), codex mapper's `openTurn`/`finalizeTurn` (Change C).
- [x] Risk + blast radius: Change A is presenter-only, zero re-ingest; Change B touches every adapter but the new field is optional; Change C is codex-only and the re-ingest is bounded to ~200 codex sessions (~5–10 min total).
- [x] Sensitive data: `first_user_message_hash` is a one-way SHA-256; we never store the prompt text itself in `sessions`. The hash cannot be reversed. `payloads` (containing the actual prompt) continues to be subject to its existing redaction rules.
- [x] Implementation plan, validation plan, artifact impact plan, open decisions: see below.

## Implementation plan (delivered as separate commits)

1. **A — Running status**: `internal/presenter/session_status.go` (new file with `deriveEffectiveStatus`); wire into `session_detail.go` and `sessions_list.go`; add `effective_status` field to `SessionDetailResponse` and `SessionListItem` types.
2. **B — Related sessions**: schema migration + ingest hook + backfill + presenter change (4 separate commits to keep history clean).
3. **C — Codex sub-turn**: codex mapper change + ingest change for `codex_turn_id` namespace; re-ingest codex sources.

## Validation plan

- Backend Go tests for `deriveEffectiveStatus` (boundary cases: now-9m, now-10m, now-11m, end_ts null vs not null).
- Backend Go tests for first_user_message_hash ingest path (every adapter, normal + edge cases: no user input, empty user input, multi-byte user input).
- Backend Go tests for related-sessions query (same hash → match; different hash → no match; self-id excluded; cross-harness allowed).
- Backend Go tests for codex sub-turn splitting (verified against the production SQLite fixture).
- E2E verification: after each chunk is deployed, hit `/api/health` + `curl /api/sessions/<id>/related?hash=X` and confirm the expected count drops/grows. End-user screenshots of the Overview tile and Related Sessions panel.

## Artifact impact plan

- DB migration file: `internal/store/migrations/0042_add_first_user_message_hash.sql` (new).
- Frontend type updates: `SessionDetailResponse` gains `effective_status?: 'running' | 'completed' | 'stale'` (null when status is terminal); `SessionListItem` similarly.
- Spec updates: `data-model.md`, `rest-api.md`, `adapter-codex.md`.

## Open decisions

- Should `effective_status` be `stale` as a third value, or should it be `completed` with a separate `is_stale` boolean? Resolved: third value, because the UI shows "stale · Nm" not "completed" and operators want the distinction (per `current/SOW-0087-20260620-deferred-ux-items.md` StaleBadge design).

## Out of scope (deferred to follow-ups)

- Codex sub-turn splitting inside Claude Code sessions (rare, the Claude Code adapter already emits one turn per user message).
- Hash collisions in related sessions (sha256 over short prompts has known birthday-bound issues at ~10⁹ entries; not relevant at 1,295 sessions).
- Re-ingestion speedup (currently ~30 min for the full workstation; SOW-0010 tracks).