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

---

# ui-turn-view chunk 2 — turn-view polish (SOW-0090, deferred to a follow-up)

The TurnView component (chunk 2) is functional but visually rough:

  - The system-prompt text dumps as inline JSON because the payload body is
    the JSON-enveloped response_item. Markdown rendering doesn't help —
    it's literal JSON. We should pre-extract the readable text from
    response_item.message envelopes before feeding it to the markdown
    renderer.
  - The Copy-turn button copies the rendered DOM text (good) but doesn't
    include the JSON metadata. A 'copy as markdown' option might be useful.
  - The StaleBadge + OverviewTiles combination can show "stale · 26m" +
    Status: stale at the same time — redundant. The OverviewTiles Status
    tile should suppress the stale badge when the session is already stale
    (one indicator, not two).
  - The session detail URL `?op=<id>` highlights the step but doesn't
    scroll the page to it when the page is taller than the viewport. (The
    TurnView component itself scrolls correctly; the page-level
    IntersectionObserver is missing.)

These are all nice-to-haves, not blocking. The headline UX win (the operator
can now see "is this the turn I'm interested in?") is shipped via chunk 2.

## chunk 5c — codex sub-turn splitting at user_input boundaries

### Problem

Codex files (rollout-*.jsonl) carry N user prompts in a single codex task
(task_started ↔ task_complete). The current adapter emits ONE turn per codex
task — so a 1,000-exchange session appears in the UI as 1 turn with 2,000+
ops. The operator's verbatim: "codex sessions miss turns, although it is
obvious there are repeated 'reasoning' and 'message' entries".

Verified against the production SQLite: the busiest codex session has 18
turns (one per codex task) but 4,186 ops across them — average 230 ops per
turn. Three of those turns have 524+, 1901+, and 777+ ops respectively
(because each contains many user/assistant/tool exchanges).

### Fix

Codex-only. When emitUserInput fires AND the current codex turn has already
seen at least one user_input op AND there is no in-flight tool call pending,
the mapper:

  1. Synthesizes a TurnFinalizedEvent for the active turn (status=completed,
     end_ts=now). This is a sub-turn boundary, NOT a codex task boundary —
     the actual task_complete still fires later and closes the LAST sub-turn.
  2. Opens a new sub-turn with a synthetic codex_turn_id ("sub:N"). The
     new sub-turn inherits the model's known sandbox/effort/approval_policy
     from the prior turn (snapped from turn_context, which doesn't repeat
     per user_input within a codex task).
  3. The new user_input lands in the new sub-turn.

Implementation details:

  - per-turn userInputCount + per-turn hasOpenToolCall tracking (state on
    fileMapper.turns[codexTurnID])
  - the synthetic TurnFinalizedEvent carries status="completed" and the
    actual end_ts of the user_input op (so the operator sees accurate
    durations)
  - on the next task_complete, the mapper closes the LAST open sub-turn
    (the rest are already completed); earlier sub-turns are NOT
    re-finalized (idempotent via the turnState.finalized flag)

### Re-ingestion cost

The codex re-ingest is bounded: ~3,157 rollout files in the production
codex source. The adapter is purely additive (sub-turn Seq values are
monotone per session), so a full re-ingest preserves all existing data
while populating the new sub-turn boundaries.

Estimated re-ingest time: ~5–10 minutes on the production workstation
(matches the SOW-0010 baseline; no optimization needed for this scope).