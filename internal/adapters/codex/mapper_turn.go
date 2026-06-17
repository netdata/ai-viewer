package codex

import (
	"encoding/json"
	"path/filepath"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// applySessionMeta fills a SessionStartedEvent's Kind, parent linkage,
// AgentName, Cwd, and Extras from the session_meta payload (spec rule #1,
// "Sub-Agent Linkage", "Canonical Model Gaps" #3/#5/#10). The mapper's
// kind/parentNativeID/agentName are pre-seeded by the scanner (Chunk C, which
// classifies the file before constructing the mapper); when those are unset
// (mapper-only tests feeding session_meta directly) this derives them from the
// payload so the event is complete either way.
func applySessionMeta(ev *canonical.SessionStartedEvent, p *sessionMetaPayload, m *fileMapper) {
	// session_meta.payload.id is the AUTHORITATIVE native id (spec adapter-codex.md
	// :290). If the adapter detected a duplicate id (two rollout files with the
	// same payload.id), m.disambiguateSuffix carries ":<basename>" so the
	// second occurrence becomes a distinct canonical session (SOW-0022, edge #14).
	canonicalID := p.ID
	if m.disambiguateSuffix != "" {
		canonicalID = p.ID + ":" + m.disambiguateSuffix
	}
	if canonicalID != "" {
		ev.NativeID = canonicalID
		m.nativeID = canonicalID
	}
	kind, parent := p.classifySource()
	// forked_from_id wins as the parent only when source did not already name a
	// sub-agent parent (a fork and a sub-agent are mutually exclusive shapes;
	// spec rule #1 checks forked_from_id when source is not subagent).
	relationship := ""
	switch {
	case kind == sourceSubagent:
		ev.Kind = canonical.KindSubAgent
		relationship = "sub_agent"
		if parent != "" && ev.ParentNativeID == "" {
			ev.ParentNativeID = parent
		}
	case p.ForkedFromID != "":
		ev.Kind = canonical.KindFork
		relationship = "fork"
		if ev.ParentNativeID == "" {
			ev.ParentNativeID = p.ForkedFromID
		}
	case kind == sourceInternal:
		ev.Kind = canonical.KindToolInternal
		relationship = "tool_internal"
	case ev.Kind == "":
		ev.Kind = canonical.KindRoot
	}
	// thread_source="subagent" is a second signal for a sub-agent even when the
	// source enum did not resolve to one (spec rule #1).
	if ev.Kind == canonical.KindRoot && p.ThreadSource == "subagent" {
		ev.Kind = canonical.KindSubAgent
		relationship = "sub_agent"
	}
	// thread_source="memory_consolidation" → tool_internal (spec gap #6).
	if ev.Kind == canonical.KindRoot && p.ThreadSource == "memory_consolidation" {
		ev.Kind = canonical.KindToolInternal
		relationship = "tool_internal"
	}
	ev.RootNativeID = rootOf(ev.NativeID, ev.ParentNativeID)
	if ev.ParentNativeID != "" {
		m.parentNativeID = ev.ParentNativeID
	}
	m.kind = ev.Kind

	if ev.AgentName == "" {
		ev.AgentName = agentNameFromMeta(p)
		m.agentName = ev.AgentName
	}
	if p.Cwd != "" {
		ev.Cwd = p.Cwd
	}
	ev.Extras = sessionExtras(p, relationship)
}

// rootOf returns the root native id for a session: the parent when present
// (the resolver walks the chain), else the session's own id.
func rootOf(nativeID, parentNativeID string) string {
	if parentNativeID != "" {
		return parentNativeID
	}
	return nativeID
}

// agentNameFromMeta derives the session AgentName. For sub-agents:
// agent_nickname or agent_role if present. For root sessions: "codex:<originator>
// (<cwd_basename>)" so the operator can identify which project a session belongs
// to (SOW-0066 — "codex:codex_cli_rs" is meaningless; "codex:codex_cli_rs
// (netdata-ktsaou.git)" identifies the work).
func agentNameFromMeta(p *sessionMetaPayload) string {
	if p.AgentNickname != "" {
		return p.AgentNickname
	}
	if p.AgentRole != "" {
		return p.AgentRole
	}
	base := "codex"
	if p.Originator != "" {
		base = "codex:" + p.Originator
	}
	if p.Cwd != "" {
		kind, _ := p.classifySource()
		if kind != sourceSubagent {
			if cwdBase := filepath.Base(p.Cwd); cwdBase != "" && cwdBase != "." && cwdBase != "/" {
				return base + " (" + cwdBase + ")"
			}
		}
	}
	return base
}

// sessionExtras builds sessions.extras_json from session_meta (spec rule #1,
// "Versioning / Forward Compatibility" — surface cli_version + originator so the
// UI can show "captured by codex 0.93.0 (codex_exec)"). The sandbox is deferred
// here (it lands per-turn from turn_context, spec gap #3); relationship
// distinguishes fork vs sub_agent vs tool_internal (spec gap #5).
func sessionExtras(p *sessionMetaPayload, relationship string) map[string]any {
	extras := map[string]any{}
	if p.CLIVersion != "" {
		extras["cli_version"] = p.CLIVersion
	}
	if p.Originator != "" {
		extras["originator"] = p.Originator
	}
	if src := sourceString(p.Source); src != "" {
		extras["source"] = src
	}
	if p.Cwd != "" {
		extras["cwd"] = p.Cwd
	}
	if p.ModelProvider != "" {
		extras["model_provider"] = p.ModelProvider
	}
	if p.Git != nil {
		git := map[string]any{}
		if p.Git.CommitHash != "" {
			git["commit_hash"] = p.Git.CommitHash
		}
		if p.Git.Branch != "" {
			git["branch"] = p.Git.Branch
		}
		if p.Git.RepositoryURL != "" {
			git["repository_url"] = p.Git.RepositoryURL
		}
		if len(git) > 0 {
			extras["git"] = git
		}
	}
	if relationship != "" {
		extras["relationship"] = relationship
	}
	if depth := subagentDepth(p.Source); depth > 0 {
		extras["subagent_depth"] = depth
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

// sourceString renders the raw source enum back to a compact string for extras:
// a bare string verbatim, or the object's single key (custom/internal/subagent/
// other) for the object forms. Returns "" when absent.
func sourceString(raw json.RawMessage) string {
	body := jsonTrim(raw)
	if len(body) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(body, &s) == nil {
		return s
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return ""
	}
	for _, k := range []string{"subagent", "internal", "custom", "other"} {
		if _, ok := obj[k]; ok {
			return k
		}
	}
	return ""
}

// subagentDepth extracts source.subagent.thread_spawn.depth (spec gap #10),
// returning 0 when absent or not a thread_spawn shape.
func subagentDepth(raw json.RawMessage) int {
	body := jsonTrim(raw)
	if len(body) == 0 {
		return 0
	}
	var obj struct {
		Subagent struct {
			ThreadSpawn struct {
				Depth int `json:"depth"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(body, &obj) != nil {
		return 0
	}
	return obj.Subagent.ThreadSpawn.Depth
}

// openTurn opens (or returns the already-open) turn for a codex turn_id, marking
// it the active turn for token attribution and the absent-turn_id fallback (spec
// rule #2, #3, #17). It does NOT emit the TurnStartedEvent — emitTurnStarted does
// that idempotently so turn_context and task_started can both call openTurn but
// only the first emits the event.
func (m *fileMapper) openTurn(turnID string, startTsUs int64) *turnState {
	if ts, ok := m.turns[turnID]; ok {
		m.activeTurnID = turnID
		m.haveActiveTurn = true
		return ts
	}
	m.turnSeqCounter++
	ts := &turnState{
		seq:         m.turnSeqCounter,
		codexTurnID: turnID,
		startTsUs:   startTsUs,
	}
	m.turns[turnID] = ts
	m.turnOrder = append(m.turnOrder, turnID)
	m.activeTurnID = turnID
	m.haveActiveTurn = true
	return ts
}

// emitTurnStarted emits a TurnStartedEvent for the turn the first time it is
// opened (idempotent across turn_context + task_started — spec rule #2, #3,
// tabular summary "idempotent with task_started"). Returns nil on a repeat.
func (m *fileMapper) emitTurnStarted(ts *turnState, base canonical.EventBase) canonical.Event {
	if ts.started {
		return nil
	}
	ts.started = true
	return canonical.TurnStartedEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		Seq:             ts.seq,
	}
}

// finalizeTurn builds a TurnFinalizedEvent with the C#1 token rollup and the
// per-turn extras (codex_turn_id, sandbox, effort, ttft_ms, last_agent_message).
// It also marks the turn finalized so a duplicate close is a no-op. The caller
// supplies status ("completed" | "failed" | "aborted") and errClass. EndTs is
// the close timestamp (spec rule #4, #5, #23). It does NOT emit the dangling-op
// finalizes — the caller (mapTaskComplete / mapTurnAborted) does that around it
// so they share the close timestamp (spec rule #4, edge #9).
func (m *fileMapper) finalizeTurn(ts *turnState, base canonical.EventBase, endUs int64, status, errClass string) canonical.TurnFinalizedEvent {
	ts.finalized = true
	return canonical.TurnFinalizedEvent{
		EventBase:        base,
		SessionNativeID:  m.nativeID,
		Seq:              ts.seq,
		Status:           status,
		ErrorClass:       errClass,
		EndTs:            endUs,
		TokensIn:         ts.tokensIn,
		TokensOut:        ts.tokensOut,
		TokensCacheRead:  ts.tokensCacheRead,
		TokensCacheWrite: ts.tokensCacheWrite,
		Extras:           m.turnExtras(ts),
	}
}

// supersedePriorTurn closes the most-recent still-open turn when a NEW turn_id
// opens (via turn_context OR task_started), deciding the close status from the
// PRIOR turn's OWN format (F1/F2, spec edge #2/#3):
//   - NEW-format prior (it saw a task_started but no task_complete/turn_aborted):
//     close FAILED/replaced — the user interrupted and re-prompted (edge #2).
//     Dangling ops on it are cancelled.
//   - OLD-format prior (turn_context-only, never saw a task_started): close
//     COMPLETED — an old-format session has no task_complete, so the next
//     turn_context boundary is its only close signal (edge #3). Dangling ops on it
//     are completed (it finished cleanly).
//
// It is a no-op when there is no prior open turn or the prior turn IS this
// turn_id (a re-announce / a turn_context re-emitted post-compaction for the same
// turn). Called from BOTH the turn_context and task_started handlers; in a
// new-format session task_started follows turn_context, so the turn_context call
// supersedes the prior turn and the task_started call is then a same-id no-op.
// The finalized turn carries its per-turn extras via TurnFinalizedEvent.Extras
// (SOW-0021), so no separate LogEntry is emitted.
func (m *fileMapper) supersedePriorTurn(newTurnID string, advance func(int64) canonical.EventBase, atUs int64) []canonical.Event {
	prior := m.mostRecentOpenTurn()
	if prior == nil || prior.codexTurnID == newTurnID {
		return nil
	}
	status, errClass, danglingStatus := "completed", "", "completed"
	if prior.sawTaskStarted {
		status, errClass, danglingStatus = "failed", "replaced", "cancelled"
	}
	base := func() canonical.EventBase { return advance(atUs) }
	out := m.finalizeDanglingOps(prior.codexTurnID, base, atUs, danglingStatus)
	out = append(out, m.finalizeTurn(prior, base(), atUs, status, errClass))
	return out
}

// finalizeDanglingOps finalizes every op still open under the given turn at turn
// close, with the supplied status (spec rule #4 — "completed inferred or unknown
// if no output ever arrived" at task_complete; edge #9 — "cancelled" at abort/
// interrupt). Deterministic order by op Seq so the emitted stream is stable.
// Returns the finalize events plus drops the ops from openOps.
func (m *fileMapper) finalizeDanglingOps(turnID string, base func() canonical.EventBase, endUs int64, status string) []canonical.Event {
	type pending struct {
		callID string
		op     *openOp
	}
	var ops []pending
	for callID, op := range m.openOps {
		if op.turnID == turnID && !op.finalized {
			ops = append(ops, pending{callID: callID, op: op})
		}
	}
	sortByOpSeq(ops, func(p pending) int { return p.op.opSeq })
	out := make([]canonical.Event, 0, len(ops))
	for _, p := range ops {
		p.op.finalized = true
		// If a prior enrichment (e.g. exec_command_end) stashed Extras on this
		// still-open op, re-emit an OpStarted carrying them so they reach
		// ops.extras_json before the dangling finalize (F4, spec rule #14). The
		// writer upserts (turn,seq), so this is an idempotent UPDATE.
		if len(p.op.extras) > 0 {
			out = append(out, canonical.OpStartedEvent{
				EventBase:       base(),
				SessionNativeID: m.nativeID,
				TurnSeq:         p.op.turnSeq,
				Seq:             p.op.opSeq,
				ParentOpSeq:     -1,
				Kind:            p.op.kind,
				Name:            p.op.name,
				ToolNamespace:   p.op.namespace,
				Extras:          p.op.extras,
			})
		}
		// An exec-derived status (exit_code) from an exec_command_end with no
		// following *_output is authoritative over the generic dangling status (F4).
		opStatus, opErrClass := status, ""
		if p.op.enrichStatus != "" {
			opStatus, opErrClass = p.op.enrichStatus, p.op.enrichErrClass
		}
		out = append(out, canonical.OpFinalizedEvent{
			EventBase:       base(),
			SessionNativeID: m.nativeID,
			TurnSeq:         p.op.turnSeq,
			Seq:             p.op.opSeq,
			Status:          opStatus,
			ErrorClass:      opErrClass,
			EndTs:           endUs,
		})
		delete(m.openOps, p.callID)
		m.recordFinalizedOp(p.callID, p.op, opStatus, opErrClass)
	}
	m.pruneWebSearchQueue(turnID)
	return out
}

// pruneWebSearchQueue drops any open web_search refs belonging to a now-closed
// turn from the FIFO queue (G4), so a web_search_end in a LATER turn never pairs
// with a stale ref whose op was already dangling-finalized. Preserves FIFO order
// for the surviving refs.
func (m *fileMapper) pruneWebSearchQueue(turnID string) {
	if len(m.openWebSearch) == 0 {
		return
	}
	kept := m.openWebSearch[:0]
	for _, ws := range m.openWebSearch {
		if ws.turnID != turnID {
			kept = append(kept, ws)
		}
	}
	m.openWebSearch = kept
}

// nonNeg floors a token count at zero. Token counts are physically
// non-negative; a negative value is malformed source data that must not
// corrupt rollup totals or (via the pricer) cost.
func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// addTokenUsage folds one token_count event into the attributed turn's C#1
// rollup: TokensIn/Out += this call's last_token_usage; cache split likewise;
// CtxUsed candidate = cumulative total_token_usage.total_tokens; CtxMax =
// model_context_window (spec rule #4, #17, "Token accounting nuance"). The
// cumulative total NEVER feeds per-turn tokens — only CtxUsed on the turn's last
// LLM op.
func (ts *turnState) addTokenUsage(info tokenCountInfo) {
	// Canonical token contract (canonical-events.md, SOW-0029): TokensIn is the
	// FRESH/uncached input ONLY. Upstream codex reports input_tokens as the TOTAL
	// prompt (cached + uncached) — non_cached_input() = input_tokens −
	// cached_input_tokens (codex-rs protocol.rs). Subtract the cached portion so
	// TokensIn is fresh and the pricer (which charges cache_read separately) does
	// not double-charge the cached tokens. Mirror non_cached_input() exactly:
	// clamp cached to ≥0 BEFORE subtracting (so a malformed negative cached cannot
	// inflate fresh via a double-negative), then clamp fresh to ≥0 (cached > input).
	//
	// All FOUR per-call components are floored at ≥0 via nonNeg: token counts are
	// physically non-negative, so a malformed negative output_tokens or
	// cache_creation_input_tokens must not flow raw into the rollup (and via the
	// pricer, which multiplies each component by a rate, into a NEGATIVE cost
	// component that silently corrupts session/turn cost totals). The CLAMPED cached
	// value is what feeds TokensCacheRead, so no field can go negative.
	cached := nonNeg(info.last.CachedInputTokens)
	fresh := nonNeg(info.last.InputTokens - cached)
	ts.tokensIn += fresh
	ts.tokensOut += nonNeg(info.last.OutputTokens)
	ts.tokensCacheRead += cached
	ts.tokensCacheWrite += nonNeg(info.last.CacheCreationInputTokens)
	if info.total.TotalTokens > 0 {
		ts.lastLLMCtxUsed = info.total.TotalTokens
	}
	if info.modelContextWindow > 0 {
		ts.ctxMax = info.modelContextWindow
	}
}

// jsonTrim trims whitespace and treats a bare null as empty (shared helper).
func jsonTrim(raw json.RawMessage) json.RawMessage {
	b := bytesTrimSpace(raw)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}
