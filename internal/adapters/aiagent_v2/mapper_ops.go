package aiagent_v2

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapOp emits the op lifecycle events plus attached payload refs,
// reasoning spans, logs, failure logs, and embedded child sessions.
func (m *mapEmitter) mapOp(v opVisit) rollupTotals {
	times := opEventTimes(m.ctx, v.op)
	kind := mapOpKind(v.op.Kind)
	m.append(buildOpStarted(m.ctx, v, kind, times.startUs))

	finalized := buildOpFinalized(m.ctx, v, kind, times.endUs)
	m.append(finalized)

	m.emitPayloadRefs(payloadEmitContext{visit: v, kind: kind})
	if shouldEmitReasoning(v.op, kind) {
		m.emitReasoningOp(reasoningEmitContext{visit: v, times: times})
	}
	m.emitOpLogs(v, times.endUs)
	m.emitFailedOpLog(v, times.endUs)
	m.emitChildSession(v)

	return rollupFromFinalized(finalized)
}

func opEventTimes(ctx *mapContext, op operationNode) opTimes {
	startUs := msToMicros(op.StartedAt)
	if startUs == 0 {
		startUs = ctx.rootTs
	}
	endUs := startUs
	if op.EndedAt != nil {
		endUs = msToMicros(*op.EndedAt)
	}
	return opTimes{startUs: startUs, endUs: endUs}
}

func buildOpStarted(ctx *mapContext, v opVisit, kind canonical.OpKind, startUs int64) canonical.OpStartedEvent {
	started := canonical.OpStartedEvent{
		EventBase:       baseEvent(ctx, v.path+"::start", startUs),
		SessionNativeID: v.scope.sessionTrace,
		TurnSeq:         v.scope.turnSeq,
		Seq:             v.seq,
		ParentOpSeq:     -1,
		Kind:            kind,
		Name:            attrString(v.op.Attributes, "name"),
		Provider:        attrString(v.op.Attributes, "provider"),
		Model:           attrString(v.op.Attributes, "model"),
		Extras:          opStartedExtras(v.op),
	}
	populateStartedToolFields(&started, v.op, kind)
	populateStartedChildSession(&started, v.op)
	return started
}

func populateStartedToolFields(started *canonical.OpStartedEvent, op operationNode, kind canonical.OpKind) {
	if kind == canonical.OpTool {
		started.ToolNamespace = attrString(op.Attributes, "provider")
	}
}

func populateStartedChildSession(started *canonical.OpStartedEvent, op operationNode) {
	if op.ChildSession != nil {
		started.ChildSessionNativeID = op.ChildSession.TraceID
		return
	}
	if op.ChildSessionRef != nil {
		started.ChildSessionNativeID = op.ChildSessionRef.SessionID
	}
}

// buildOpFinalized assembles the OpFinalizedEvent with accounting and
// payload sizes pulled from the op.
func buildOpFinalized(ctx *mapContext, v opVisit, kind canonical.OpKind, endUs int64) canonical.OpFinalizedEvent {
	ev := canonical.OpFinalizedEvent{
		EventBase:       baseEvent(ctx, v.path+"::end", endUs),
		SessionNativeID: v.scope.sessionTrace,
		TurnSeq:         v.scope.turnSeq,
		Seq:             v.seq,
		Status:          mapOpStatus(v.op.Status),
		ErrorClass:      attrString(v.op.Attributes, "error"),
		EndTs:           endUs,
	}
	applyOpAccounting(&ev, v.op, kind)
	applyPayloadSizes(&ev, v.op)
	return ev
}

func applyOpAccounting(ev *canonical.OpFinalizedEvent, op operationNode, kind canonical.OpKind) {
	if len(op.Accounting) == 0 {
		return
	}
	applyAccounting(ev, op.Accounting[0])
	if kind == canonical.OpTool {
		applyToolCharacterAccounting(ev, op.Accounting[0])
	}
}

func applyPayloadSizes(ev *canonical.OpFinalizedEvent, op operationNode) {
	if op.Request != nil {
		ev.BytesIn = op.Request.Size
	}
	if op.Response != nil {
		ev.BytesOut = op.Response.Size
	}
}

// applyAccounting copies an llm-type accounting entry into the op
// finalize event. Tool entries route through CharsIn/CharsOut in the
// caller; the token / cost fields here only fire for llm rows.
func applyAccounting(ev *canonical.OpFinalizedEvent, acc accountingEntry) {
	if acc.Type != "llm" || acc.Tokens == nil {
		return
	}
	ev.TokensIn = acc.Tokens.InputTokens
	ev.TokensOut = acc.Tokens.OutputTokens
	ev.TokensCacheRead = acc.Tokens.CacheReadInputTokens + acc.Tokens.CachedTokens
	ev.TokensCacheWrite = acc.Tokens.CacheWriteInputTokens
	ev.CostUSD = acc.CostUSD
	// Canonical CtxUsed = TokensIn + TokensCacheRead + TokensCacheWrite + TokensOut
	// (SOW-0031: the old 3-term formula omitted cache_write).
	ev.CtxUsed = acc.Tokens.InputTokens + ev.TokensCacheRead + ev.TokensCacheWrite + acc.Tokens.OutputTokens
}

func applyToolCharacterAccounting(ev *canonical.OpFinalizedEvent, acc accountingEntry) {
	if acc.CharactersIn > 0 {
		ev.CharsIn = acc.CharactersIn
	}
	if acc.CharactersOut > 0 {
		ev.CharsOut = acc.CharactersOut
	}
}

func opStartedExtras(op operationNode) map[string]any {
	out := map[string]any{}
	addOriginalKindExtra(out, op)
	addReasoningExtras(out, op.Reasoning)
	addChildSessionExtras(out, op)
	addAccountingCacheExtras(out, op.Accounting)
	return out
}

func addOriginalKindExtra(out map[string]any, op operationNode) {
	if op.Kind != "" {
		out["original_kind"] = op.Kind
	}
}

func addReasoningExtras(out map[string]any, r *reasoning) {
	if r == nil {
		return
	}
	if r.Final != "" {
		out["reasoning.final"] = r.Final
	}
	if r.ChunkCount > 0 {
		out["reasoning.chunkCount"] = r.ChunkCount
	}
}

func addChildSessionExtras(out map[string]any, op operationNode) {
	if op.ChildSessionRef != nil {
		out["childSessionRef"] = op.ChildSessionRef.SessionID
	}
	if op.ChildSessionSummary != nil {
		out["childSessionSummary"] = op.ChildSessionSummary
	}
}

func addAccountingCacheExtras(out map[string]any, entries []accountingEntry) {
	if len(entries) == 0 || entries[0].Tokens == nil {
		return
	}
	addTokenCacheExtras(out, entries[0].Tokens)
}

func addTokenCacheExtras(out map[string]any, t *tokens) {
	cacheRead := t.CacheReadInputTokens + t.CachedTokens
	if cacheRead > 0 {
		out["tokensCacheRead"] = cacheRead
	}
	if t.CacheWriteInputTokens > 0 {
		out["tokensCacheWrite"] = t.CacheWriteInputTokens
	}
}

func (m *mapEmitter) emitOpLogs(v opVisit, fallbackTs int64) {
	for li := range v.op.Logs {
		l := v.op.Logs[li]
		m.append(buildOpLog(m.ctx, v, l, li, fallbackTs))
	}
}

func buildOpLog(ctx *mapContext, v opVisit, l logEntry, idx int, fallbackTs int64) canonical.LogEntryEvent {
	ts := msToMicros(l.Timestamp)
	if ts == 0 {
		ts = fallbackTs
	}
	return canonical.LogEntryEvent{
		EventBase:       baseEvent(ctx, fmt.Sprintf("%s::log:%d", v.path, idx), ts),
		SessionNativeID: v.scope.sessionTrace,
		TurnSeq:         v.scope.turnSeq,
		OpSeq:           v.seq,
		Severity:        normaliseSeverity(l.Severity),
		Source:          Format,
		Message:         l.Message,
		Extras:          logExtras(l),
	}
}

func logExtras(l logEntry) map[string]any {
	if l.Path == "" {
		return nil
	}
	return map[string]any{"path": l.Path}
}

func (m *mapEmitter) emitFailedOpLog(v opVisit, endUs int64) {
	if v.op.Status != "failed" {
		return
	}
	if msg := attrString(v.op.Attributes, "error"); msg != "" {
		m.append(canonical.LogEntryEvent{
			EventBase:       baseEvent(m.ctx, v.path+"::failure", endUs),
			SessionNativeID: v.scope.sessionTrace,
			TurnSeq:         v.scope.turnSeq,
			OpSeq:           v.seq,
			Severity:        "ERR",
			Source:          Format,
			Message:         msg,
		})
	}
}

func (m *mapEmitter) emitChildSession(v opVisit) {
	if v.op.ChildSession == nil {
		return
	}
	m.mapSession(sessionVisit{
		node:           *v.op.ChildSession,
		parentNativeID: v.scope.sessionTrace,
		parentOpKey:    v.op.OpID,
		kind:           canonical.KindSubAgent,
		depth:          v.scope.depth + 1,
	})
}

func shouldEmitReasoning(op operationNode, kind canonical.OpKind) bool {
	return kind == canonical.OpLLM && op.Reasoning != nil && op.Reasoning.Final != ""
}

type reasoningEmitContext struct {
	visit opVisit
	times opTimes
}

// emitReasoningOp surfaces an OpStarted/OpFinalized pair representing
// the model's reasoning span as a nested op of the parent LLM op.
func (m *mapEmitter) emitReasoningOp(in reasoningEmitContext) {
	reasoningPath := in.visit.path + "::reasoning"
	m.append(canonical.OpStartedEvent{
		EventBase:       baseEvent(m.ctx, reasoningPath+"::start", in.times.startUs),
		SessionNativeID: in.visit.scope.sessionTrace,
		TurnSeq:         in.visit.scope.turnSeq,
		Seq:             in.visit.reasoningSeq,
		ParentOpSeq:     in.visit.seq,
		Kind:            canonical.OpReasoning,
		ReasoningKind:   "summary",
		Extras: map[string]any{
			"reasoning.final": in.visit.op.Reasoning.Final,
		},
	})
	m.append(canonical.OpFinalizedEvent{
		EventBase:       baseEvent(m.ctx, reasoningPath+"::end", in.times.endUs),
		SessionNativeID: in.visit.scope.sessionTrace,
		TurnSeq:         in.visit.scope.turnSeq,
		Seq:             in.visit.reasoningSeq,
		Status:          "completed",
		EndTs:           in.times.endUs,
	})
}
