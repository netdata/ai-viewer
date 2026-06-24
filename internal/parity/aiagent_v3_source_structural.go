package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
)

func (s *aiAgentV3SourceState) recordAIAgentV3TurnEnd(rec aiAgentV3SourceRecord, tsUs int64, lineNo int64) ([]Artifact, error) {
	turn := s.ensureAIAgentV3Turn(rec.Turn, tsUs, lineNo)
	turn.status = mapAIAgentV3Status(rec.Status)
	turn.endedAt = ptrInt64(tsUs)
	turn.finalized = true

	turnArtifact, err := s.aiAgentV3TurnBoundary(turn)
	if err != nil {
		return nil, err
	}
	artifacts := []Artifact{turnArtifact}
	for _, op := range rec.Ops {
		opArtifacts, err := s.aiAgentV3OpArtifacts(rec, op, tsUs, lineNo)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, opArtifacts...)
	}
	artifacts = append(artifacts, s.aiAgentV3TurnLogArtifacts(rec)...)
	return artifacts, nil
}

func (s *aiAgentV3SourceState) finalizeAIAgentV3AtEOF() ([]Artifact, error) {
	var artifacts []Artifact
	if s.sessionStarted {
		if s.syntheticSessions != nil {
			s.syntheticSessions.noteReal(s.realSessionCandidate())
		} else {
			session, err := s.aiAgentV3SessionBoundary()
			if err != nil {
				return nil, err
			}
			if session.NativeArtifactID != "" {
				artifacts = append(artifacts, session)
				metadata, err := s.aiAgentV3SessionMetadata()
				if err != nil {
					return nil, err
				}
				if metadata.NativeArtifactID != "" {
					artifacts = append(artifacts, metadata)
				}
			}
		}
	}

	turnNos := make([]int, 0, len(s.turns))
	for turnNo := range s.turns {
		turnNos = append(turnNos, turnNo)
	}
	sort.Ints(turnNos)
	for _, turnNo := range turnNos {
		turn := s.turns[turnNo]
		if turn.finalized {
			continue
		}
		artifact, err := s.aiAgentV3TurnBoundary(turn)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func (s *aiAgentV3SourceState) aiAgentV3SessionBoundary() (Artifact, error) {
	if !s.sessionStarted {
		return Artifact{}, nil
	}
	nativeID := s.sessionID()
	rootID := s.rootNativeSessionID
	if rootID == "" {
		rootID = nativeID
	}
	identity := sessionBoundaryIdentity{
		NativeSessionID:       nativeID,
		ParentNativeSessionID: s.parentNativeSessionID,
		RootNativeSessionID:   rootID,
		Kind:                  s.sessionKind,
		Status:                s.sessionStatus,
		StartedAt:             s.sessionStartedAt,
		EndedAt:               s.sessionEndedAt,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  nativeID,
		nativeArtifactID: "session:" + nativeID,
		class:            ClassSessionBoundary,
		selectorURI:      aiAgentV3LineSelectorURI(s.sourceFile, s.sessionLineNo),
		identity:         identity,
	})
}

func (s *aiAgentV3SourceState) aiAgentV3SessionMetadata() (Artifact, error) {
	if !s.sessionStarted {
		return Artifact{}, nil
	}
	attributes, err := decodeAIAgentV3AttributeMap(s.sessionAttributes)
	if err != nil {
		return Artifact{}, err
	}
	nativeID := s.sessionID()
	identity := aiAgentV3SessionMetadataIdentity{
		NativeSessionID:       nativeID,
		OriginID:              s.rootNativeSessionID,
		AgentID:               s.agentID,
		CallPath:              s.callPath,
		ParentNativeSessionID: s.parentNativeSessionID,
		ParentOpID:            s.parentOpID,
		HeadendID:             s.headendID,
		CapturePayloads:       s.capturePayloads,
		Attributes:            attributes,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          aiAgentV3Format,
		sourceFile:       s.sourceFile,
		nativeSessionID:  nativeID,
		nativeArtifactID: "session:" + nativeID + ":metadata",
		class:            ClassSessionMetadata,
		selectorURI:      aiAgentV3LineSelectorURI(s.sourceFile, s.sessionLineNo),
		identity:         identity,
	})
}

func (s *aiAgentV3SourceState) realSessionCandidate() aiAgentV3RealSessionCandidate {
	return aiAgentV3RealSessionCandidate{
		sourceID:               s.sourceID,
		sourceFile:             s.sourceFile,
		lineNo:                 s.sessionLineNo,
		sessionID:              s.sessionID(),
		rootSessionID:          s.rootNativeSessionID,
		parentSessionID:        s.parentNativeSessionID,
		parentOpID:             s.parentOpID,
		agentID:                s.agentID,
		callPath:               s.callPath,
		headendID:              s.headendID,
		capturePayloads:        s.capturePayloads,
		attributes:             cloneRawMessageMap(s.sessionAttributes),
		sessionKind:            s.sessionKind,
		sessionStatus:          s.sessionStatus,
		sessionStartedAt:       s.sessionStartedAt,
		sessionEndedAt:         s.sessionEndedAt,
		sessionMetadataPresent: s.sessionStarted,
	}
}

func (s *aiAgentV3SourceState) aiAgentV3TurnBoundary(turn *aiAgentV3SourceTurn) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turn.seq)
	identity := turnBoundaryIdentity{
		NativeSessionID: s.sessionID(),
		TurnSeq:         turn.seq,
		Status:          turn.status,
		StartedAt:       turn.startedAt,
		EndedAt:         turn.endedAt,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeTurnID,
		class:            ClassTurnBoundary,
		selectorURI:      "aiagent-v3-source://turns/" + url.PathEscape(nativeTurnID),
		identity:         identity,
	})
}

func (s *aiAgentV3SourceState) aiAgentV3OpArtifacts(rec aiAgentV3SourceRecord, op aiAgentV3SourceOp, recordTsUs int64, lineNo int64) ([]Artifact, error) {
	startUs, endUs, err := aiAgentV3OpTimes(op, recordTsUs)
	if err != nil {
		return nil, err
	}
	nativeTurnID := fmt.Sprintf("turn:%d", rec.Turn)
	nativeOpID := fmt.Sprintf("op:%d:%d", rec.Turn, op.OpIndex)
	namespace := ""
	if op.Kind == "tool" {
		namespace = op.Provider
	}
	identity := opBoundaryIdentity{
		NativeSessionID: s.sessionID(),
		TurnSeq:         int64(rec.Turn),
		OpSeq:           int64(op.OpIndex),
		Kind:            op.Kind,
		Name:            op.Name,
		ToolNamespace:   namespace,
		Status:          mapAIAgentV3Status(op.Status),
		StartedAt:       startUs,
		EndedAt:         ptrInt64(endUs),
	}
	opArtifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID,
		class:            ClassOpBoundary,
		selectorURI:      "aiagent-v3-source://ops/" + url.PathEscape(nativeOpID),
		identity:         identity,
	})
	if err != nil {
		return nil, err
	}
	artifacts := []Artifact{opArtifact}
	if op.Kind == "system" {
		systemArtifact, err := s.aiAgentV3SystemOp(int64(rec.Turn), op, nativeOpID, startUs, endUs)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, systemArtifact)
	}
	if op.Kind == "session" && op.Provider == "history-compaction" {
		compactionArtifact, err := s.aiAgentV3CompactionEvent(int64(rec.Turn), op, nativeOpID, startUs, endUs)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, compactionArtifact)
	}

	ordinals := map[string]int64{}
	for _, ref := range op.PayloadRefs {
		ordinals[ref.Kind]++
		payloadArtifact, err := s.aiAgentV3PayloadArtifact(int64(rec.Turn), int64(op.OpIndex), ref, ordinals[ref.Kind])
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, payloadArtifact)
	}
	for _, child := range op.ChildSessions {
		if s.syntheticSessions != nil {
			s.syntheticSessions.noteChild(s.sessionID(), s.rootNativeSessionID, child, recordTsUs, s.sourceFile, lineNo)
		}
		linkArtifact, err := s.aiAgentV3SubagentLink(int64(rec.Turn), int64(op.OpIndex), nativeOpID, child.SessionID)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, linkArtifact)
	}
	if op.Status == "failed" && op.Error != "" {
		errorArtifact, err := s.aiAgentV3OpError(int64(rec.Turn), int64(op.OpIndex), nativeOpID, op.Kind, op.Error)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, errorArtifact)
	}
	return artifacts, nil
}

func (s *aiAgentV3SourceState) aiAgentV3SystemOp(turnSeq int64, op aiAgentV3SourceOp, nativeOpID string, startUs int64, endUs int64) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	identity := systemOpIdentity{
		NativeSessionID: s.sessionID(),
		TurnSeq:         turnSeq,
		OpSeq:           int64(op.OpIndex),
		OpKind:          op.Kind,
		Name:            op.Name,
		Status:          mapAIAgentV3Status(op.Status),
		StartedAt:       startUs,
		EndedAt:         ptrInt64(endUs),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID + ":system",
		class:            ClassSystemOp,
		selectorURI:      "aiagent-v3-source://ops/" + url.PathEscape(nativeOpID) + "#system",
		identity:         identity,
	})
}

func (s *aiAgentV3SourceState) aiAgentV3CompactionEvent(turnSeq int64, op aiAgentV3SourceOp, nativeOpID string, startUs int64, endUs int64) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	archivedTurn, err := int64JSONField(op.Attributes, "archivedTurn")
	if err != nil {
		return Artifact{}, err
	}
	currentTurn, err := int64JSONField(op.Attributes, "currentTurn")
	if err != nil {
		return Artifact{}, err
	}
	name := op.Name
	if name == "" {
		name, err = stringJSONField(op.Attributes, "name")
		if err != nil {
			return Artifact{}, err
		}
	}
	provider := op.Provider
	if provider == "" {
		provider, err = stringJSONField(op.Attributes, "provider")
		if err != nil {
			return Artifact{}, err
		}
	}
	childNativeID := ""
	if len(op.ChildSessions) > 0 {
		childNativeID = op.ChildSessions[0].SessionID
	}
	identity := aiAgentV3CompactionEventIdentity{
		NativeSessionID:      s.sessionID(),
		TurnSeq:              turnSeq,
		OpSeq:                int64(op.OpIndex),
		Trigger:              "history_compaction",
		Name:                 name,
		Provider:             provider,
		ChildNativeSessionID: childNativeID,
		ArchivedTurn:         archivedTurn,
		CurrentTurn:          currentTurn,
		Status:               mapAIAgentV3Status(op.Status),
		StartedAt:            startUs,
		EndedAt:              ptrInt64(endUs),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID + ":compaction",
		class:            ClassCompactionEvent,
		selectorURI:      "aiagent-v3-source://ops/" + url.PathEscape(nativeOpID) + "#compaction",
		identity:         identity,
	})
}

func (s *aiAgentV3SourceState) aiAgentV3SubagentLink(turnSeq int64, opSeq int64, nativeOpID string, childNativeID string) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	nativeArtifactID := nativeOpID + ":child_session:" + childNativeID
	identity := subagentLinkIdentity{
		ParentNativeSessionID: s.sessionID(),
		ParentTurnSeq:         turnSeq,
		ParentOpSeq:           opSeq,
		ChildNativeSessionID:  childNativeID,
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeArtifactID,
		class:            ClassSubagentLink,
		selectorURI:      "aiagent-v3-source://ops/" + url.PathEscape(nativeOpID) + "#child_session",
		identity:         identity,
	})
}

func (s *aiAgentV3SourceState) aiAgentV3OpError(turnSeq int64, opSeq int64, nativeOpID string, opKind string, message string) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	class := ClassToolError
	if opKind == "llm" {
		class = ClassLLMError
	}
	identity := opErrorIdentity{
		NativeSessionID:    s.sessionID(),
		TurnSeq:            turnSeq,
		OpSeq:              opSeq,
		OpKind:             opKind,
		ErrorMessageSHA256: stringSHA256(message),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "aiagent_v3",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID + ":error",
		class:            class,
		selectorURI:      "aiagent-v3-source://ops/" + url.PathEscape(nativeOpID) + "#error",
		identity:         identity,
	})
}

func aiAgentV3OpTimes(op aiAgentV3SourceOp, recordTsUs int64) (int64, int64, error) {
	startUs := recordTsUs
	endUs := recordTsUs
	if op.StartedAt != "" {
		parsed, err := parseAIAgentV3Timestamp(op.StartedAt)
		if err != nil {
			return 0, 0, fmt.Errorf("op.startedAt: %w", err)
		}
		startUs = parsed
	}
	if op.EndedAt != "" {
		parsed, err := parseAIAgentV3Timestamp(op.EndedAt)
		if err != nil {
			return 0, 0, fmt.Errorf("op.endedAt: %w", err)
		}
		endUs = parsed
	}
	return startUs, endUs, nil
}

type aiAgentV3SyntheticSessionTracker struct {
	sourceID   string
	real       map[string]struct{}
	candidates map[string]aiAgentV3SyntheticSessionCandidate
	realByID   map[string]aiAgentV3RealSessionCandidate
}

type aiAgentV3SyntheticSessionCandidate struct {
	sessionID       string
	parentSessionID string
	parentOpID      string
	rootSessionID   string
	sourceFile      string
	lineNo          int64
	startedAt       int64
}

type aiAgentV3RealSessionCandidate struct {
	sourceID               string
	sourceFile             string
	lineNo                 int64
	sessionID              string
	rootSessionID          string
	parentSessionID        string
	parentOpID             string
	agentID                string
	callPath               string
	headendID              string
	capturePayloads        bool
	attributes             map[string]json.RawMessage
	sessionKind            string
	sessionStatus          string
	sessionStartedAt       int64
	sessionEndedAt         *int64
	sessionMetadataPresent bool
}

func newAIAgentV3SyntheticSessionTracker(sourceID string) *aiAgentV3SyntheticSessionTracker {
	return &aiAgentV3SyntheticSessionTracker{
		sourceID:   sourceID,
		real:       map[string]struct{}{},
		candidates: map[string]aiAgentV3SyntheticSessionCandidate{},
		realByID:   map[string]aiAgentV3RealSessionCandidate{},
	}
}

func (t *aiAgentV3SyntheticSessionTracker) noteReal(candidate aiAgentV3RealSessionCandidate) {
	if candidate.sessionID == "" {
		return
	}
	t.real[candidate.sessionID] = struct{}{}
	t.realByID[candidate.sessionID] = candidate
}

func (t *aiAgentV3SyntheticSessionTracker) noteChild(parentSessionID string, rootSessionID string, child aiAgentV3ChildSession, tsUs int64, sourceFile string, lineNo int64) {
	if child.SessionID == "" {
		return
	}
	rootID := child.OriginID
	if rootID == "" {
		rootID = rootSessionID
	}
	if rootID == "" {
		rootID = parentSessionID
	}
	candidate := aiAgentV3SyntheticSessionCandidate{
		sessionID:       child.SessionID,
		parentSessionID: parentSessionID,
		parentOpID:      child.ParentOpID,
		rootSessionID:   rootID,
		sourceFile:      sourceFile,
		lineNo:          lineNo,
		startedAt:       tsUs,
	}
	existing, ok := t.candidates[child.SessionID]
	if ok && candidate.startedAt >= existing.startedAt {
		t.candidates[child.SessionID] = existing.withMissingFrom(candidate)
		return
	}
	if ok {
		candidate = candidate.withMissingFrom(existing)
	}
	if !ok || candidate.startedAt < existing.startedAt {
		t.candidates[child.SessionID] = candidate
	}
}

func (t *aiAgentV3SyntheticSessionTracker) writeSessionArtifacts(ctx context.Context, writer ArtifactWriter) error {
	if err := t.writeRealSessions(ctx, writer); err != nil {
		return err
	}
	return t.writePartialBoundaries(ctx, writer)
}

func (t *aiAgentV3SyntheticSessionTracker) writeRealSessions(ctx context.Context, writer ArtifactWriter) error {
	ids := make([]string, 0, len(t.realByID))
	for id := range t.realByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidate := t.realByID[id]
		boundaryCandidate := candidate
		if parent, ok := t.candidates[id]; ok {
			boundaryCandidate = boundaryCandidate.withParentEvidence(parent)
		}
		boundary, err := boundaryCandidate.boundaryArtifact()
		if err != nil {
			return err
		}
		if err := writer.WriteArtifact(ctx, boundary); err != nil {
			return fmt.Errorf("write aiagent_v3 real session boundary: %w", err)
		}
		metadata, err := candidate.metadataArtifact()
		if err != nil {
			return err
		}
		if metadata.NativeArtifactID == "" {
			continue
		}
		if err := writer.WriteArtifact(ctx, metadata); err != nil {
			return fmt.Errorf("write aiagent_v3 real session metadata: %w", err)
		}
	}
	return nil
}

func (t *aiAgentV3SyntheticSessionTracker) writePartialBoundaries(ctx context.Context, writer ArtifactWriter) error {
	ids := make([]string, 0, len(t.candidates))
	for id := range t.candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := t.real[id]; ok {
			continue
		}
		artifact, err := t.candidates[id].artifact(t.sourceID)
		if err != nil {
			return err
		}
		if err := writer.WriteArtifact(ctx, artifact); err != nil {
			return fmt.Errorf("write aiagent_v3 partial child session boundary: %w", err)
		}
	}
	return nil
}

func (c aiAgentV3SyntheticSessionCandidate) withMissingFrom(other aiAgentV3SyntheticSessionCandidate) aiAgentV3SyntheticSessionCandidate {
	if c.parentSessionID == "" {
		c.parentSessionID = other.parentSessionID
	}
	if c.parentOpID == "" {
		c.parentOpID = other.parentOpID
	}
	if c.rootSessionID == "" || c.rootSessionID == c.sessionID {
		if other.rootSessionID != "" {
			c.rootSessionID = other.rootSessionID
		}
	}
	return c
}

func (c aiAgentV3RealSessionCandidate) withParentEvidence(parent aiAgentV3SyntheticSessionCandidate) aiAgentV3RealSessionCandidate {
	if c.parentSessionID == "" {
		c.parentSessionID = parent.parentSessionID
	}
	if c.parentOpID == "" {
		c.parentOpID = parent.parentOpID
	}
	if c.rootSessionID == "" || c.rootSessionID == c.sessionID {
		if parent.rootSessionID != "" {
			c.rootSessionID = parent.rootSessionID
		}
	}
	return c
}

func (c aiAgentV3RealSessionCandidate) boundaryArtifact() (Artifact, error) {
	rootID := c.rootSessionID
	if rootID == "" {
		rootID = c.sessionID
	}
	identity := sessionBoundaryIdentity{
		NativeSessionID:       c.sessionID,
		ParentNativeSessionID: c.parentSessionID,
		RootNativeSessionID:   rootID,
		Kind:                  c.sessionKind,
		Status:                c.sessionStatus,
		StartedAt:             c.sessionStartedAt,
		EndedAt:               c.sessionEndedAt,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         c.sourceID,
		adapter:          aiAgentV3Format,
		sourceFile:       c.sourceFile,
		nativeSessionID:  c.sessionID,
		nativeArtifactID: "session:" + c.sessionID,
		class:            ClassSessionBoundary,
		selectorURI:      aiAgentV3LineSelectorURI(c.sourceFile, c.lineNo),
		identity:         identity,
	})
}

func (c aiAgentV3RealSessionCandidate) metadataArtifact() (Artifact, error) {
	if !c.sessionMetadataPresent {
		return Artifact{}, nil
	}
	attributes, err := decodeAIAgentV3AttributeMap(c.attributes)
	if err != nil {
		return Artifact{}, err
	}
	identity := aiAgentV3SessionMetadataIdentity{
		NativeSessionID:       c.sessionID,
		OriginID:              c.rootSessionID,
		AgentID:               c.agentID,
		CallPath:              c.callPath,
		ParentNativeSessionID: c.parentSessionID,
		ParentOpID:            c.parentOpID,
		HeadendID:             c.headendID,
		CapturePayloads:       c.capturePayloads,
		Attributes:            attributes,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         c.sourceID,
		adapter:          aiAgentV3Format,
		sourceFile:       c.sourceFile,
		nativeSessionID:  c.sessionID,
		nativeArtifactID: "session:" + c.sessionID + ":metadata",
		class:            ClassSessionMetadata,
		selectorURI:      aiAgentV3LineSelectorURI(c.sourceFile, c.lineNo),
		identity:         identity,
	})
}

func (c aiAgentV3SyntheticSessionCandidate) artifact(sourceID string) (Artifact, error) {
	identity := sessionBoundaryIdentity{
		NativeSessionID:       c.sessionID,
		ParentNativeSessionID: c.parentSessionID,
		RootNativeSessionID:   c.rootSessionID,
		Kind:                  "sub_agent",
		Status:                "running",
		StartedAt:             c.startedAt,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         sourceID,
		adapter:          aiAgentV3Format,
		sourceFile:       c.sourceFile,
		nativeSessionID:  c.sessionID,
		nativeArtifactID: "session:" + c.sessionID,
		class:            ClassSessionBoundary,
		selectorURI:      aiAgentV3LineSelectorURI(c.sourceFile, c.lineNo),
		identity:         identity,
	})
	if err != nil {
		return Artifact{}, err
	}
	artifact.Availability = AvailabilityPartialSource
	return artifact, nil
}

func aiAgentV3LineSelectorURI(path string, lineNo int64) string {
	return (&url.URL{Scheme: "file", Path: path, Fragment: fmt.Sprintf("L%d", lineNo)}).String()
}

func decodeAIAgentV3AttributeMap(rawAttrs map[string]json.RawMessage) (map[string]any, error) {
	if len(rawAttrs) == 0 {
		return nil, nil
	}
	attrs := make(map[string]any, len(rawAttrs))
	for key, raw := range rawAttrs {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode aiagent_v3 session attribute %q: %w", key, err)
		}
		attrs[key] = value
	}
	return attrs, nil
}

func cloneRawMessageMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, raw := range in {
		out[key] = append(json.RawMessage(nil), raw...)
	}
	return out
}
