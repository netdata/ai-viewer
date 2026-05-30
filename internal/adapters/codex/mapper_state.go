package codex

import "github.com/netdata/ai-viewer/internal/canonical"

// This file holds the per-file inference STATE TYPES the mapper threads through
// one rollout: the synthesized turn, the in-flight and finalized op trackers, and
// the positional web_search ref. The mapper's dispatch and bootstrap live in
// mapper.go; these types are split out to keep that file focused.

// turnState tracks one synthesized turn's accumulation between its open
// (turn_context or task_started) and its close (task_complete / turn_aborted /
// stale finalize). Token rollup is the C#1 model: TokensIn/Out are the SUM of
// per-call last_token_usage over the token_count events attributed to this turn
// — never a delta of the cumulative total_token_usage (spec rule #4, #17).
type turnState struct {
	// seq is the canonical 1-based turn Seq.
	seq int
	// codexTurnID is the source turn_id (UUID), surfaced in
	// turns.extras_json.codex_turn_id (spec "Canonical Model Gaps" #2). Empty
	// for the absent-turn_id fallback turn.
	codexTurnID string
	// opSeq is the 1-based op counter within this turn.
	opSeq int
	// started reports whether a TurnStartedEvent was already emitted for this
	// turn (idempotency across turn_context + task_started — spec rule #2, #3).
	started bool
	// finalized reports whether a TurnFinalizedEvent was already emitted, so a
	// duplicate task_complete / a later stale finalize does not double-close.
	finalized bool
	// sawTaskStarted reports whether an event_msg.task_started ever opened/touched
	// this turn (F1/F2). It discriminates NEW-format turns (task_started present —
	// close failed/incomplete only at stale EOF, and a replacing task_started
	// closes the prior failed/replaced) from OLD-format turns (turn_context only,
	// cli < ~0.93 — close completed at EOF or when a different turn_context opens,
	// spec edge #2/#3). Set by mapTaskStarted; never cleared.
	sawTaskStarted bool
	// startTsUs is the turn's open timestamp (micros), used as a floor for the
	// synthetic stale-finalize EndTs (spec rule #23).
	startTsUs int64
	// tokensIn / tokensOut accumulate the C#1 per-call last_token_usage rollup
	// (spec rule #4, #17).
	tokensIn  int64
	tokensOut int64
	// tokensCacheRead / tokensCacheWrite accumulate the per-call cached-token
	// split when newer rollouts report it (canonical-events.md codex cache row).
	tokensCacheRead  int64
	tokensCacheWrite int64
	// ctxMax is the model_context_window stashed from task_started /
	// token_count, applied to the turn's last LLM op at finalize (spec rule #3,
	// #17).
	ctxMax int64
	// sandbox is the sandbox_policy.type snapshotted from the turn's
	// turn_context, surfaced in turns.extras_json.sandbox (spec rule #2,
	// "Canonical Model Gaps" #3).
	sandbox string
	// effort / approvalPolicy are turn_context policy snapshots for turn extras.
	effort         string
	approvalPolicy string
	// ttftMs is task_complete.time_to_first_token_ms, surfaced in
	// turns.extras_json.ttft_ms (spec "Canonical Model Gaps" #8).
	ttftMs int64
	// lastAgentMessage is event_msg.agent_message.message, surfaced in
	// TurnFinalized extras as the UI "latest answer" preview (spec rule #19).
	lastAgentMessage string
	// lastLLMOpSeq is the op Seq of the most recent LLM op in this turn, so a
	// trailing token_count attaches CtxUsed/CtxMax to it (spec rule #17). 0 when
	// no LLM op has been emitted yet.
	lastLLMOpSeq int
	// lastLLMEndTs is the EndTs of the turn's last LLM op, preserved so a
	// token_count re-finalize that adds CtxUsed/CtxMax does not clobber the op's
	// real end timestamp (the ingester reconciles fields on the (turn,seq)
	// upsert — canonical-events.md §Idempotency).
	lastLLMEndTs int64
	// lastLLMCtxUsed is the cumulative total_token_usage observed for the turn's
	// last LLM op (spec rule #17). The op's CtxUsed is set from this at finalize.
	lastLLMCtxUsed int64
}

// openOp records where an in-flight op was emitted so its finalize / enrichment
// lands under the same turn/op (spec rule #9, #14-16).
type openOp struct {
	turnID    string
	turnSeq   int
	opSeq     int
	kind      canonical.OpKind
	name      string
	namespace string
	// extras accumulates enrichment (exec_command_end, mcp_tool_call_end,
	// patch_apply_end) merged onto the op via an OpStarted re-emit (spec rule
	// #14-16). The adapter does NOT emit a SECOND op for an enrichment event — the
	// re-emit is an idempotent UPDATE on (turn,seq).
	extras map[string]any
	// enrichStatus / enrichErrClass carry a terminal status derived from an
	// enrichment event (exec_command_end exit_code) that arrived BEFORE the op's
	// *_output (the ~68-85% exec-first ordering, F4). The op stays open so its
	// *_output finalizes it, but the *_output's finalize PREFERS this exec-derived
	// status (a non-zero exit_code is authoritative over a benign-looking output
	// string). Empty when no enrichment status was observed.
	enrichStatus   string
	enrichErrClass string
	// finalized guards against a second *_output finalizing the same op.
	finalized bool
}

// finalizedOp records where a now-finalized op lives so a LATE enrichment event
// (F4, spec rule #14) can merge Extras onto it via an idempotent OpStarted
// re-emit. The kind/name/namespace are preserved so the re-emit restates the op
// faithfully (the writer's ON CONFLICT UPDATE keeps the original start_ts via
// MIN and grafts the resolver stash, so re-emitting with the op's known
// identity only adds the enrichment Extras).
type finalizedOp struct {
	turnSeq   int
	opSeq     int
	kind      canonical.OpKind
	name      string
	namespace string
}

// openWebSearchRef records the most-recent open web_search op in the active turn
// for POSITIONAL pairing with event_msg.web_search_end (F7). web_search_call
// carries no correlation key, so the end pairs by position, not call_id.
// syntheticCallID is the openOps key the call was tracked under, so the end can
// finalize the SAME op and remove it from the dangling set.
type openWebSearchRef struct {
	turnID          string
	turnSeq         int
	opSeq           int
	syntheticCallID string
}
