package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type aiAgentV2SessionVisit struct {
	parentNativeID string
	kind           string
	depth          int
	jsonPointer    string
}

type aiAgentV2OpScope struct {
	sessionTrace   string
	turnSeq        int64
	depth          int
	jsonPointer    string
	stepKind       string
	stepAttributes map[string]json.RawMessage
}

type aiAgentV2CompactionEventIdentity struct {
	NativeSessionID      string `json:"native_session_id"`
	TurnSeq              int64  `json:"turn_seq"`
	OpSeq                int64  `json:"op_seq"`
	Trigger              string `json:"trigger"`
	StepKind             string `json:"step_kind"`
	Name                 string `json:"name,omitempty"`
	Provider             string `json:"provider,omitempty"`
	ChildNativeSessionID string `json:"child_native_session_id,omitempty"`
	ArchivedTurn         int64  `json:"archived_turn,omitempty"`
	CurrentTurn          int64  `json:"current_turn,omitempty"`
	Status               string `json:"status"`
	StartedAt            int64  `json:"started_at"`
	EndedAt              *int64 `json:"ended_at,omitempty"`
}

func (s *aiAgentV2SourceState) recordSession(node aiAgentV2OpTree, visit aiAgentV2SessionVisit) error {
	if visit.depth > aiAgentV2MaxChildDepth {
		return fmt.Errorf("aiagent_v2 child session depth %d exceeds cap %d", visit.depth, aiAgentV2MaxChildDepth)
	}
	sessionPointer := visit.jsonPointer
	if sessionPointer == "" {
		sessionPointer = "/opTree"
	}
	session, err := s.aiAgentV2SessionBoundary(node, visit)
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, session)
	if err := s.recordSessionMetadata(node, visit); err != nil {
		return err
	}
	if err := s.recordFinalReport(node); err != nil {
		return err
	}
	for i := range node.Turns {
		if err := s.recordTurn(node, node.Turns[i], visit.depth, fmt.Sprintf("%s/turns/%d", sessionPointer, i)); err != nil {
			return err
		}
	}
	for i := range node.Steps {
		if err := s.recordStep(node, node.Steps[i], visit.depth, fmt.Sprintf("%s/steps/%d", sessionPointer, i)); err != nil {
			return err
		}
	}
	s.recordSessionErrorLog(node, sessionPointer)
	return nil
}

func (s *aiAgentV2SourceState) recordTurn(node aiAgentV2OpTree, turn aiAgentV2Turn, depth int, turnPointer string) error {
	turnSeq := int64(turn.Index)
	turnArtifact, err := s.aiAgentV2TurnBoundary(node.TraceID, turnSeq, turn.StartedAt, turn.EndedAt, turnStatusFromAIAgentV2Ops(turn.Ops))
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, turnArtifact)
	return s.recordOps(turn.Ops, aiAgentV2OpScope{
		sessionTrace: node.TraceID,
		turnSeq:      turnSeq,
		depth:        depth,
		jsonPointer:  turnPointer,
	})
}

func (s *aiAgentV2SourceState) recordStep(node aiAgentV2OpTree, step aiAgentV2Step, depth int, stepPointer string) error {
	turnSeq := int64(aiAgentV2StepIndexOffset + step.Index)
	turnArtifact, err := s.aiAgentV2TurnBoundary(node.TraceID, turnSeq, step.StartedAt, step.EndedAt, turnStatusFromAIAgentV2Ops(step.Ops))
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, turnArtifact)
	return s.recordOps(step.Ops, aiAgentV2OpScope{
		sessionTrace:   node.TraceID,
		turnSeq:        turnSeq,
		depth:          depth,
		jsonPointer:    stepPointer,
		stepKind:       step.Kind,
		stepAttributes: step.Attributes,
	})
}

func (s *aiAgentV2SourceState) recordOps(ops []aiAgentV2Operation, scope aiAgentV2OpScope) error {
	nextReasoningSeq := int64(len(ops))
	for i := range ops {
		opSeq := int64(i)
		opPointer := fmt.Sprintf("%s/ops/%d", scope.jsonPointer, i)
		if err := s.recordOp(ops[i], scope, opSeq, opPointer); err != nil {
			return err
		}
		if aiAgentV2HasReasoningOp(ops[i]) {
			if err := s.recordReasoningOp(ops[i], scope, nextReasoningSeq); err != nil {
				return err
			}
			nextReasoningSeq++
		}
		if ops[i].ChildSession != nil {
			if err := s.recordSession(*ops[i].ChildSession, aiAgentV2SessionVisit{
				parentNativeID: scope.sessionTrace,
				kind:           "sub_agent",
				depth:          scope.depth + 1,
				jsonPointer:    opPointer + "/childSession",
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *aiAgentV2SourceState) recordOp(op aiAgentV2Operation, scope aiAgentV2OpScope, opSeq int64, opPointer string) error {
	startUs, endUs := aiAgentV2OpTimes(op, s.rootTs)
	kind := op.Kind
	nativeOpID := aiAgentV2NativeOpID(scope.turnSeq, opSeq)
	opArtifact, err := s.aiAgentV2OpBoundary(op, scope, opSeq, kind, startUs, endUs)
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, opArtifact)

	payloads, err := s.aiAgentV2PayloadArtifacts(op, scope.sessionTrace, scope.turnSeq, opSeq, kind, opPointer)
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, payloads...)

	s.recordOpLogs(op, scope, opPointer)
	s.recordFailedOpLog(op, scope, opPointer)
	if err := s.recordSystemOp(op, scope, opSeq, startUs, endUs, nativeOpID); err != nil {
		return err
	}
	if err := s.recordSubagentLink(op, scope, opSeq, nativeOpID); err != nil {
		return err
	}
	if err := s.recordCompactionEvent(op, scope, opSeq, startUs, endUs, nativeOpID); err != nil {
		return err
	}
	if err := s.recordOpError(op, scope, opSeq, kind, nativeOpID); err != nil {
		return err
	}
	return nil
}

func (s *aiAgentV2SourceState) recordSystemOp(op aiAgentV2Operation, scope aiAgentV2OpScope, opSeq int64, startUs int64, endUs int64, nativeOpID string) error {
	if op.Kind != "system" {
		return nil
	}
	identity := systemOpIdentity{
		NativeSessionID: scope.sessionTrace,
		TurnSeq:         scope.turnSeq,
		OpSeq:           opSeq,
		OpKind:          op.Kind,
		Name:            aiAgentV2OpName(op),
		Status:          aiAgentV2OpStatus(op.Status),
		StartedAt:       startUs,
		EndedAt:         ptrInt64(endUs),
		OriginalKind:    op.Kind,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  scope.sessionTrace,
		nativeTurnID:     aiAgentV2NativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID + ":system",
		class:            ClassSystemOp,
		selectorURI:      aiAgentV2SelectorURI("ops", scope.sessionTrace, nativeOpID) + "#system",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func (s *aiAgentV2SourceState) recordReasoningOp(op aiAgentV2Operation, scope aiAgentV2OpScope, opSeq int64) error {
	startUs, endUs := aiAgentV2OpTimes(op, s.rootTs)
	nativeOpID := aiAgentV2NativeOpID(scope.turnSeq, opSeq)
	identity := opBoundaryIdentity{
		NativeSessionID: scope.sessionTrace,
		TurnSeq:         scope.turnSeq,
		OpSeq:           opSeq,
		Kind:            "reasoning",
		Status:          "completed",
		StartedAt:       startUs,
		EndedAt:         ptrInt64(endUs),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  scope.sessionTrace,
		nativeTurnID:     aiAgentV2NativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID,
		class:            ClassOpBoundary,
		selectorURI:      aiAgentV2SelectorURI("ops", scope.sessionTrace, nativeOpID),
		identity:         identity,
	})
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	if op.Reasoning != nil && op.Reasoning.Final != "" {
		s.artifacts = append(s.artifacts, semanticTextArtifact(semanticTextArtifactInput{
			sourceID:         s.sourceID,
			adapter:          aiAgentV2Format,
			sourceFile:       s.sourceFile,
			nativeSessionID:  scope.sessionTrace,
			nativeTurnID:     aiAgentV2NativeTurnID(scope.turnSeq),
			nativeArtifactID: nativeOpID + ":reasoning.final",
			class:            ClassReasoningText,
			selector: Selector{
				URI:       aiAgentV2SelectorURI("ops", scope.sessionTrace, nativeOpID),
				FieldPath: "reasoning.final",
			},
			text: op.Reasoning.Final,
		}))
	}
	return nil
}

func (s *aiAgentV2SourceState) recordSubagentLink(op aiAgentV2Operation, scope aiAgentV2OpScope, opSeq int64, nativeOpID string) error {
	childNativeID := ""
	if op.ChildSession != nil {
		childNativeID = op.ChildSession.TraceID
	} else if op.ChildSessionRef != nil {
		childNativeID = op.ChildSessionRef.SessionID
	}
	if childNativeID == "" {
		return nil
	}
	identity := subagentLinkIdentity{
		ParentNativeSessionID: scope.sessionTrace,
		ParentTurnSeq:         scope.turnSeq,
		ParentOpSeq:           opSeq,
		ChildNativeSessionID:  childNativeID,
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  scope.sessionTrace,
		nativeTurnID:     aiAgentV2NativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID + ":child_session:" + childNativeID,
		class:            ClassSubagentLink,
		selectorURI:      aiAgentV2SelectorURI("ops", scope.sessionTrace, nativeOpID) + "#child_session",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func (s *aiAgentV2SourceState) recordOpError(op aiAgentV2Operation, scope aiAgentV2OpScope, opSeq int64, kind string, nativeOpID string) error {
	if op.Status != "failed" {
		return nil
	}
	errorClass := aiAgentV2AttrString(op.Attributes, "error")
	if errorClass == "" {
		return nil
	}
	class := ClassToolError
	if kind == "llm" {
		class = ClassLLMError
	}
	identity := opErrorIdentity{
		NativeSessionID:    scope.sessionTrace,
		TurnSeq:            scope.turnSeq,
		OpSeq:              opSeq,
		OpKind:             kind,
		ErrorClass:         errorClass,
		ErrorMessageSHA256: stringSHA256(""),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  scope.sessionTrace,
		nativeTurnID:     aiAgentV2NativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID + ":error",
		class:            class,
		selectorURI:      aiAgentV2SelectorURI("ops", scope.sessionTrace, nativeOpID) + "#error",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func (s *aiAgentV2SourceState) recordCompactionEvent(op aiAgentV2Operation, scope aiAgentV2OpScope, opSeq int64, startUs int64, endUs int64, nativeOpID string) error {
	if !aiAgentV2IsCompactionOp(op, scope) {
		return nil
	}
	archivedTurn, err := aiAgentV2Int64AttrWithFallback(op.Attributes, scope.stepAttributes, "archivedTurn")
	if err != nil {
		return err
	}
	currentTurn, err := aiAgentV2Int64AttrWithFallback(op.Attributes, scope.stepAttributes, "currentTurn")
	if err != nil {
		return err
	}
	identity := aiAgentV2CompactionEventIdentity{
		NativeSessionID:      scope.sessionTrace,
		TurnSeq:              scope.turnSeq,
		OpSeq:                opSeq,
		Trigger:              "history_compaction",
		StepKind:             scope.stepKind,
		Name:                 aiAgentV2AttrString(op.Attributes, "name"),
		Provider:             aiAgentV2AttrString(op.Attributes, "provider"),
		ChildNativeSessionID: aiAgentV2ChildNativeSessionID(op),
		ArchivedTurn:         archivedTurn,
		CurrentTurn:          currentTurn,
		Status:               aiAgentV2OpStatus(op.Status),
		StartedAt:            startUs,
		EndedAt:              ptrInt64(endUs),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  scope.sessionTrace,
		nativeTurnID:     aiAgentV2NativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID + ":compaction",
		class:            ClassCompactionEvent,
		selectorURI:      aiAgentV2SelectorURI("ops", scope.sessionTrace, nativeOpID) + "#compaction",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func aiAgentV2IsCompactionOp(op aiAgentV2Operation, scope aiAgentV2OpScope) bool {
	return scope.stepKind == "internal" &&
		op.Kind == "session" &&
		aiAgentV2AttrString(op.Attributes, "provider") == "history-compaction"
}

func aiAgentV2ChildNativeSessionID(op aiAgentV2Operation) string {
	if op.ChildSession != nil {
		return op.ChildSession.TraceID
	}
	if op.ChildSessionRef != nil {
		return op.ChildSessionRef.SessionID
	}
	return ""
}

func aiAgentV2Int64AttrWithFallback(primary map[string]json.RawMessage, fallback map[string]json.RawMessage, key string) (int64, error) {
	if aiAgentV2AttrValuePresent(primary, key) {
		return int64JSONField(primary, key)
	}
	return int64JSONField(fallback, key)
}

func aiAgentV2AttrValuePresent(attrs map[string]json.RawMessage, key string) bool {
	raw, ok := attrs[key]
	if !ok {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func (s *aiAgentV2SourceState) aiAgentV2SessionBoundary(node aiAgentV2OpTree, visit aiAgentV2SessionVisit) (Artifact, error) {
	rootID := node.TraceID
	if visit.parentNativeID != "" {
		rootID = s.rootTrace
	}
	identity := sessionBoundaryIdentity{
		NativeSessionID:       node.TraceID,
		ParentNativeSessionID: visit.parentNativeID,
		RootNativeSessionID:   rootID,
		Kind:                  visit.kind,
		Status:                aiAgentV2SessionStatus(node),
		StartedAt:             aiAgentV2TimestampOrRoot(node.StartedAt, s.rootTs),
		EndedAt:               aiAgentV2OptionalTimestamp(node.EndedAt),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  node.TraceID,
		nativeArtifactID: "session:" + node.TraceID,
		class:            ClassSessionBoundary,
		selectorURI:      aiAgentV2SelectorURI("sessions", node.TraceID, "session:"+node.TraceID),
		identity:         identity,
	})
}

func (s *aiAgentV2SourceState) aiAgentV2TurnBoundary(sessionTrace string, turnSeq int64, startedAt int64, endedAt *int64, status string) (Artifact, error) {
	if endedAt == nil {
		status = "running"
	}
	identity := turnBoundaryIdentity{
		NativeSessionID: sessionTrace,
		TurnSeq:         turnSeq,
		Status:          status,
		StartedAt:       aiAgentV2TimestampOrRoot(startedAt, s.rootTs),
		EndedAt:         aiAgentV2OptionalTimestamp(endedAt),
	}
	nativeTurnID := aiAgentV2NativeTurnID(turnSeq)
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  sessionTrace,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeTurnID,
		class:            ClassTurnBoundary,
		selectorURI:      aiAgentV2SelectorURI("turns", sessionTrace, nativeTurnID),
		identity:         identity,
	})
}

func (s *aiAgentV2SourceState) aiAgentV2OpBoundary(op aiAgentV2Operation, scope aiAgentV2OpScope, opSeq int64, kind string, startUs int64, endUs int64) (Artifact, error) {
	namespace := ""
	if kind == "tool" {
		namespace = aiAgentV2AttrString(op.Attributes, "provider")
	}
	identity := opBoundaryIdentity{
		NativeSessionID: scope.sessionTrace,
		TurnSeq:         scope.turnSeq,
		OpSeq:           opSeq,
		Kind:            kind,
		Name:            aiAgentV2OpName(op),
		ToolNamespace:   namespace,
		Status:          aiAgentV2OpStatus(op.Status),
		StartedAt:       startUs,
		EndedAt:         ptrInt64(endUs),
	}
	nativeOpID := aiAgentV2NativeOpID(scope.turnSeq, opSeq)
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV2Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  scope.sessionTrace,
		nativeTurnID:     aiAgentV2NativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID,
		class:            ClassOpBoundary,
		selectorURI:      aiAgentV2SelectorURI("ops", scope.sessionTrace, nativeOpID),
		identity:         identity,
	})
}
