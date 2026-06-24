package parity

import (
	"fmt"
	"strings"
)

type codexSystemOpIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq,omitempty"`
	EventType       string `json:"event_type"`
	Severity        string `json:"severity"`
	Message         string `json:"message"`
	Timestamp       int64  `json:"timestamp"`
}

func (s *codexSourceState) genericLogEntryWithSystemOp(tsUs int64, severity string, message string) ([]Artifact, error) {
	logArtifact := s.genericLogEntry(tsUs, severity, message)
	artifacts := []Artifact{logArtifact}
	systemArtifact, ok, err := s.genericSystemOp(tsUs, severity, message)
	if err != nil {
		return nil, err
	}
	if ok {
		artifacts = append(artifacts, systemArtifact)
	}
	return artifacts, nil
}

func (s *codexSourceState) genericSystemOp(tsUs int64, severity string, message string) (Artifact, bool, error) {
	eventType, ok := codexSystemEventTypeFromMessage(message)
	if !ok {
		return Artifact{}, false, nil
	}
	nativeSessionID := s.sessionID()
	nativeTurnID := ""
	turnSeq := int64(0)
	scope := "session"
	if nativeSessionID == "source:"+s.sourceID {
		scope = "source"
	}
	if s.activeTurn != nil && s.activeTurn.seq > 0 {
		turnSeq = s.activeTurn.seq
		nativeTurnID = fmt.Sprintf("turn:%d", turnSeq)
		scope = nativeTurnID
	}
	nativeArtifactID := logNativeArtifactID(scope, tsUs, severity, codexFormat, message)
	identity := codexSystemOpIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
		EventType:       eventType,
		Severity:        severity,
		Message:         message,
		Timestamp:       tsUs,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          codexFormat,
		sourceFile:       s.sourceFile,
		nativeSessionID:  nativeSessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeArtifactID,
		class:            ClassSystemOp,
		selectorURI:      logSelectorURI(s.sourceID, nativeArtifactID),
		identity:         identity,
	})
	return artifact, true, err
}

func artifactFromCodexSystemLogEntry(row canonicalLogEntryRow) (Artifact, bool, error) {
	if row.adapter != codexFormat {
		return Artifact{}, false, nil
	}
	eventType, ok := codexSystemEventTypeFromMessage(row.message)
	if !ok {
		return Artifact{}, false, nil
	}
	nativeSessionID := "source:" + row.sourceID
	if row.nativeSessionID.Valid && row.nativeSessionID.String != "" {
		nativeSessionID = row.nativeSessionID.String
	}
	turnSeq := nullInt64(row.turnSeq)
	nativeTurnID := ""
	if turnSeq > 0 {
		nativeTurnID = fmt.Sprintf("turn:%d", turnSeq)
	}
	scope := logEntryScope(row)
	nativeArtifactID := logNativeArtifactID(scope, row.ts, row.severity, row.logSource, row.message)
	identity := codexSystemOpIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
		EventType:       eventType,
		Severity:        row.severity,
		Message:         row.message,
		Timestamp:       row.ts,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: nullString(row.sessionID),
		canonicalTurnID:    nullString(row.turnID),
		canonicalOpID:      nullString(row.opID),
		nativeSessionID:    nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeArtifactID,
		class:              ClassSystemOp,
		selectorURI:        logSelectorURI(row.sourceID, nativeArtifactID),
		identity:           identity,
	})
	return artifact, true, err
}

func codexSystemEventTypeFromMessage(message string) (string, bool) {
	switch message {
	case "thread_rolled_back", "entered_review_mode", "exited_review_mode", "item_completed":
		return message, true
	}
	if !strings.HasPrefix(message, "event_msg:") {
		return "", false
	}
	eventType := strings.TrimPrefix(message, "event_msg:")
	switch eventType {
	case "thread_goal_updated", "guardian_assessment", "view_image_tool_call",
		"dynamic_tool_call_request", "dynamic_tool_call_response":
		return eventType, true
	default:
		return "", false
	}
}
