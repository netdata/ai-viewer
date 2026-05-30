package codex

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// previewMax bounds the rune length of a non-sensitive Extras preview string
// (compaction message, last_agent_message). Full bodies live behind PayloadRefs.
const previewMax = 200

// mapTurnContext handles a turn_context record (spec rule #2). It opens (or
// re-activates) the turn for turn_id, emits TurnStartedEvent the first time the
// turn is seen (idempotent with task_started), emits SessionUpdatedEvent(Model)
// the first time a model is learned, and snapshots the sandbox/effort/approval
// policy into the turn's extras (spec gap #3). A turn_context after mid-turn
// compaction re-activates the same turn_id without re-emitting TurnStarted.
func (m *fileMapper) mapTurnContext(rec record, advance func(int64) canonical.EventBase) []canonical.Event {
	p := rec.TurnContext
	if p == nil {
		return nil
	}
	tsUs := m.recordTs(rec)
	out := make([]canonical.Event, 0, 3)
	// A new turn_id opening supersedes the prior open turn (F1/F2, spec edge #2/#3).
	// supersedePriorTurn decides the prior turn's close status from ITS OWN format
	// (NEW-format → failed/replaced; OLD-format → completed) and is a no-op for a
	// re-activating turn_context with the SAME turn_id (post-compaction).
	out = append(out, m.supersedePriorTurn(p.TurnID, advance, tsUs)...)
	ts := m.openTurn(p.TurnID, tsUs)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	// Snapshot per-turn policy (spec rule #2, gap #3).
	if sb := p.sandboxType(); sb != "" {
		ts.sandbox = sb
	}
	if p.Effort != "" {
		ts.effort = p.Effort
	}
	if p.ApprovalPolicy != "" {
		ts.approvalPolicy = p.ApprovalPolicy
	}
	// Learn the model and announce it once (spec rule #2). The active turn
	// always uses the latest model so later ops are stamped correctly.
	if p.Model != "" {
		m.model = p.Model
		if !m.modelSeen {
			m.modelSeen = true
			out = append(out, canonical.SessionUpdatedEvent{
				EventBase: advance(tsUs),
				NativeID:  m.nativeID,
				Model:     p.Model,
			})
		}
	}
	return out
}

// mapCompacted handles a top-level compacted line (spec rule #20). It emits a
// single compaction op (Kind=compaction, Name=compaction) with the
// replacement_history size and a message preview in Extras; the full summary
// body goes to a PayloadRef. This is the data-bearing representation; the
// adjacent event_msg.context_compacted bare marker (same timestamp) is its
// companion and is SUPPRESSED so ONE op is emitted per compaction (F5, spec rule
// #20). recordIdx-1 is the current record's index (mapRecord pre-incremented
// recordIdx); recording it lets the immediately-following context_compacted
// recognize itself as the companion.
func (m *fileMapper) mapCompacted(rec record, advance func(int64) canonical.EventBase) []canonical.Event {
	p := rec.Compacted
	tsUs := m.recordTs(rec)
	extras := map[string]any{"trigger": "auto"}
	if p != nil {
		extras["replacement_history_size"] = p.replacementHistorySize()
		if prev := trimPreview(p.Message, previewMax); prev != "" {
			extras["message_preview"] = prev
		}
	}
	// recordIdx was pre-incremented in mapRecord, so recordIdx-1 is THIS line's
	// index; recording it lets the immediately-following context_compacted detect
	// adjacency (F5). recordIdx is always >= 1 here (this record was counted).
	m.compactedSeen = true
	m.compactedRecordIdx = m.recordIdx - 1
	return m.emitCompactionOp(advance, tsUs, extras, "json")
}

// suppressContextCompacted reports whether an event_msg.context_compacted record
// is the bare companion marker of the immediately-preceding top-level `compacted`
// line and must NOT emit a second compaction op (F5). The real wire pair is two
// adjacent lines (compacted then context_compacted) with identical timestamps;
// only the data-bearing `compacted` produces the op. A context_compacted with no
// preceding compacted (defensive) is NOT suppressed and emits the op itself.
// recordIdx-1 is the current record's index; the companion is suppressed when the
// recorded `compacted` index is exactly one before it (adjacent).
func (m *fileMapper) suppressContextCompacted() bool {
	// recordIdx-1 is THIS context_compacted's index; it is the companion when the
	// recorded `compacted` index is exactly one before it (adjacent). recordIdx is
	// >= 2 here (a session_meta/turn at minimum precedes any compaction pair), so
	// recordIdx-2 does not underflow.
	return m.compactedSeen && m.compactedRecordIdx+1 == m.recordIdx-1
}

// emitCompactionOp emits the OpStarted+OpFinalized compaction pair plus a
// PayloadRef for the summary body (spec rule #20, gap #4). It opens a turn 0
// fallback when compaction precedes any turn_context so the op attaches to a
// real turn row. format is the PayloadRef Format ("json" for a structured
// compaction body). The op carries no tokens; preTokens/postTokens are unknown
// for codex (the rollout records only the summary), so trigger is the only
// scalar.
func (m *fileMapper) emitCompactionOp(advance func(int64) canonical.EventBase, tsUs int64, extras map[string]any, format string) []canonical.Event {
	ts := m.ensureTurn(tsUs)
	out := make([]canonical.Event, 0, 3)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	ts.opSeq++
	opSeq := ts.opSeq
	out = append(out,
		canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         ts.seq,
			Seq:             opSeq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpCompaction,
			Name:            "compaction",
			Extras:          extras,
		},
		canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         ts.seq,
			Seq:             opSeq,
			Status:          "completed",
			EndTs:           tsUs,
		},
		m.payloadRef(advance(tsUs), ts.seq, opSeq, "log", format, -1),
	)
	return out
}

// ensureTurn returns the active turn, opening a fallback turn (under the active
// turn_id, or the absent-turn_id bucket "") when no turn is open yet. Used by
// ops that can legitimately precede a turn boundary (compaction, a stray
// message in an old-CLI rollout — spec edge #3).
func (m *fileMapper) ensureTurn(tsUs int64) *turnState {
	if m.haveActiveTurn {
		if ts, ok := m.turns[m.activeTurnID]; ok {
			return ts
		}
	}
	return m.openTurn("", tsUs)
}

// nextOp allocates the next op Seq in the turn and returns (turnSeq, opSeq).
func (m *fileMapper) nextOp(ts *turnState) (int, int) {
	ts.opSeq++
	return ts.seq, ts.opSeq
}

// trackOp records an in-flight op by call_id so its matching *_output (or an
// enrichment event) finalizes/enriches the SAME op (spec rule #9, #14-16). A
// call_id of "" is not tracked (an unpaired op finalizes inline or at turn end).
// namespace is stored so a late-enrichment OpStarted re-emit (F4) restates the
// op's tool_namespace faithfully.
func (m *fileMapper) trackOp(callID, turnID string, turnSeq, opSeq int, kind canonical.OpKind, name, namespace string) {
	if callID == "" {
		return
	}
	m.openOps[callID] = &openOp{
		turnID:    turnID,
		turnSeq:   turnSeq,
		opSeq:     opSeq,
		kind:      kind,
		name:      name,
		namespace: namespace,
		extras:    map[string]any{},
	}
}

// sortByOpSeq sorts a slice in place ascending by the int key fn returns. A
// tiny generic helper so dangling-op finalize order is deterministic.
func sortByOpSeq[T any](s []T, key func(T) int) {
	sort.Slice(s, func(i, j int) bool { return key(s[i]) < key(s[j]) })
}

// bytesTrimSpace trims ASCII whitespace from a json.RawMessage. Wraps
// bytes.TrimSpace so callers in mapper_turn.go need not import bytes.
func bytesTrimSpace(b []byte) []byte { return bytes.TrimSpace(b) }

// tokenUsage is the subset of a TokenUsage block the rollup consumes
// (protocol.rs:1895-1979). Field names match the codex wire form; unknown
// siblings are dropped by encoding/json (forward-compat).
type tokenUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CachedInputTokens        int64 `json:"cached_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
}

// tokenCountInfo is the decoded token_count.info block plus the sibling
// model_context_window (spec rule #17). last is the per-call usage summed into
// the turn rollup (C#1); total is the cumulative session usage that feeds only
// CtxUsed on the turn's last LLM op.
type tokenCountInfo struct {
	last               tokenUsage
	total              tokenUsage
	modelContextWindow int64
}

// decodeTokenCount extracts last_token_usage / total_token_usage /
// model_context_window from an event_msg.token_count line (spec rule #17). The
// fields live under the envelope's "payload"; the shape is
// {info:{total_token_usage, last_token_usage, model_context_window}} in newer
// rollouts, with model_context_window also appearing as a sibling of info in
// some versions, so both placements are checked (forward-compat). raw is the
// verbatim envelope line.
func decodeTokenCount(raw []byte) tokenCountInfo {
	var env struct {
		Payload struct {
			Info struct {
				Total              tokenUsage `json:"total_token_usage"`
				Last               tokenUsage `json:"last_token_usage"`
				ModelContextWindow int64      `json:"model_context_window"`
			} `json:"info"`
			ModelContextWindow int64 `json:"model_context_window"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return tokenCountInfo{}
	}
	p := env.Payload
	mcw := p.Info.ModelContextWindow
	if mcw == 0 {
		mcw = p.ModelContextWindow
	}
	return tokenCountInfo{last: p.Info.Last, total: p.Info.Total, modelContextWindow: mcw}
}

// userFingerprint builds the dedup key for user input shared across
// response_item.message(role=user) and event_msg.user_message (spec rule #6,
// #18). It hashes the trimmed message text so the second arrival is suppressed
// regardless of which form arrives first. An empty body fingerprints to "" and
// is never deduped (distinct empty inputs are rare and harmless to keep).
func userFingerprint(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	return t
}

// firstSeenUser reports whether this user-input fingerprint is the first
// occurrence on the file, recording it so the companion form is suppressed
// (spec rule #6, #18). An empty fingerprint is always "first" (never deduped).
func (m *fileMapper) firstSeenUser(fp string) bool {
	if fp == "" {
		return true
	}
	if _, ok := m.seenUser[fp]; ok {
		return false
	}
	m.seenUser[fp] = struct{}{}
	return true
}
