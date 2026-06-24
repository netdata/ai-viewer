package aiagent_v2

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type rollupTotals struct {
	tokensIn         int64
	tokensOut        int64
	tokensCacheRead  int64
	tokensCacheWrite int64
	costUSD          float64
}

func (r *rollupTotals) add(next rollupTotals) {
	r.tokensIn += next.tokensIn
	r.tokensOut += next.tokensOut
	r.tokensCacheRead += next.tokensCacheRead
	r.tokensCacheWrite += next.tokensCacheWrite
	r.costUSD += next.costUSD
}

func rollupFromFinalized(ev canonical.OpFinalizedEvent) rollupTotals {
	return rollupTotals{
		tokensIn:         ev.TokensIn,
		tokensOut:        ev.TokensOut,
		tokensCacheRead:  ev.TokensCacheRead,
		tokensCacheWrite: ev.TokensCacheWrite,
		costUSD:          ev.CostUSD,
	}
}

// mapTurns walks node.Turns in stored order and emits canonical events
// for each turn.
func (m *mapEmitter) mapTurns(node opTree, depth int, sessionPath string, sessionPointer string) {
	for i := range node.Turns {
		m.mapTurn(node, node.Turns[i], depth, sessionPath, fmt.Sprintf("%s/turns/%d", sessionPointer, i))
	}
}

func (m *mapEmitter) mapTurn(node opTree, turn turnNode, depth int, sessionPath string, turnPointer string) {
	turnPath := fmt.Sprintf("%s::T:%d", sessionPath, turn.Index)
	turnStart := timestampOrRoot(m.ctx, turn.StartedAt)
	m.append(canonical.TurnStartedEvent{
		EventBase:       baseEvent(m.ctx, turnPath+"::start", turnStart),
		SessionNativeID: node.TraceID,
		Seq:             turn.Index,
	})

	scope := opScope{sessionTrace: node.TraceID, turnSeq: turn.Index, depth: depth, path: turnPath, jsonPointer: turnPointer}
	totals := m.mapOps(turn.Ops, scope)
	if turn.EndedAt != nil {
		m.append(buildTurnFinalized(m.ctx, turnFinalizedInput{
			sessionTrace: node.TraceID,
			seq:          turn.Index,
			path:         turnPath,
			endedAt:      *turn.EndedAt,
			status:       turnStatusFromOps(turn.Ops),
			totals:       totals,
		}))
	}
}

// mapSteps walks node.Steps and emits a canonical turn per step using
// a reserved offset so step seqs do not collide with turn seqs.
func (m *mapEmitter) mapSteps(node opTree, depth int, sessionPath string, sessionPointer string) {
	for i := range node.Steps {
		m.mapStep(node, node.Steps[i], depth, sessionPath, fmt.Sprintf("%s/steps/%d", sessionPointer, i))
	}
}

func (m *mapEmitter) mapStep(node opTree, step stepNode, depth int, sessionPath string, stepPointer string) {
	stepSeq := stepIndexOffset + step.Index
	stepPath := fmt.Sprintf("%s::S:%d", sessionPath, step.Index)
	stepStart := timestampOrRoot(m.ctx, step.StartedAt)
	m.append(canonical.TurnStartedEvent{
		EventBase:       baseEvent(m.ctx, stepPath+"::start", stepStart),
		SessionNativeID: node.TraceID,
		Seq:             stepSeq,
	})

	m.emitStepKind(node.TraceID, step, stepPath, stepStart)
	scope := opScope{
		sessionTrace:   node.TraceID,
		turnSeq:        stepSeq,
		depth:          depth,
		path:           stepPath,
		jsonPointer:    stepPointer,
		stepKind:       step.Kind,
		stepAttributes: step.Attributes,
	}
	totals := m.mapOps(step.Ops, scope)
	if step.EndedAt != nil {
		m.append(buildTurnFinalized(m.ctx, turnFinalizedInput{
			sessionTrace: node.TraceID,
			seq:          stepSeq,
			path:         stepPath,
			endedAt:      *step.EndedAt,
			status:       turnStatusFromOps(step.Ops),
			totals:       totals,
		}))
	}
}

func (m *mapEmitter) emitStepKind(sessionTrace string, step stepNode, stepPath string, stepStart int64) {
	if step.Kind == "" {
		return
	}
	m.append(canonical.SessionUpdatedEvent{
		EventBase: baseEvent(m.ctx, stepPath+"::kind", stepStart),
		NativeID:  sessionTrace,
		Extras: map[string]any{
			"step." + fmt.Sprintf("%d", step.Index) + ".kind": step.Kind,
		},
	})
}

func (m *mapEmitter) mapOps(ops []operationNode, scope opScope) rollupTotals {
	var totals rollupTotals
	nextReasoningSeq := len(ops)
	for j := range ops {
		op := ops[j]
		opPath := fmt.Sprintf("%s::O:%d:%s", scope.path, j, op.OpID)
		visit := opVisit{op: op, scope: scope, seq: j, reasoningSeq: -1, path: opPath, jsonPointer: fmt.Sprintf("%s/ops/%d", scope.jsonPointer, j)}
		if needsReasoningSeq(op) {
			visit.reasoningSeq = nextReasoningSeq
			nextReasoningSeq++
		}
		totals.add(m.mapOp(visit))
	}
	return totals
}

func needsReasoningSeq(op operationNode) bool {
	return shouldEmitReasoning(op, mapOpKind(op.Kind))
}

type turnFinalizedInput struct {
	sessionTrace string
	seq          int
	path         string
	endedAt      int64
	status       string
	totals       rollupTotals
}

func buildTurnFinalized(ctx *mapContext, in turnFinalizedInput) canonical.TurnFinalizedEvent {
	endTs := msToMicros(in.endedAt)
	return canonical.TurnFinalizedEvent{
		EventBase:        baseEvent(ctx, in.path+"::end", endTs),
		SessionNativeID:  in.sessionTrace,
		Seq:              in.seq,
		Status:           in.status,
		EndTs:            endTs,
		TokensIn:         in.totals.tokensIn,
		TokensOut:        in.totals.tokensOut,
		TokensCacheRead:  in.totals.tokensCacheRead,
		TokensCacheWrite: in.totals.tokensCacheWrite,
		CostUSD:          in.totals.costUSD,
	}
}

func timestampOrRoot(ctx *mapContext, ms int64) int64 {
	ts := msToMicros(ms)
	if ts == 0 {
		return ctx.rootTs
	}
	return ts
}
