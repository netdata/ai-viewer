package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type claudeCodeAPIErrorInfo struct {
	status  string
	typ     string
	message string
}

func (s *claudeCodeSourceState) opError(opSeq int64, opKind string, errorClass string, errorMessage string, lineNo int64) (Artifact, error) {
	return s.opErrorAtTurn(s.turnSeq, opSeq, opKind, errorClass, errorMessage, lineNo)
}

func (s *claudeCodeSourceState) opErrorAtTurn(turnSeq int64, opSeq int64, opKind string, errorClass string, errorMessage string, lineNo int64) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
	class := ClassToolError
	if opKind == "llm" {
		class = ClassLLMError
	}
	identity := opErrorIdentity{
		NativeSessionID:    s.nativeSessionID,
		TurnSeq:            turnSeq,
		OpSeq:              opSeq,
		OpKind:             opKind,
		ErrorClass:         errorClass,
		ErrorMessageSHA256: stringSHA256(errorMessage),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID + ":error",
		class:            class,
		selectorURI:      s.lineSelectorURI(lineNo),
		identity:         identity,
	})
}

func claudeCodeSourceAPIErrorInfo(rec claudeCodeSourceRecord) claudeCodeAPIErrorInfo {
	if len(bytes.TrimSpace(rec.APIError)) == 0 {
		return claudeCodeAPIErrorInfo{}
	}
	var payload struct {
		Status  json.RawMessage `json:"status"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(rec.APIError, &payload); err != nil {
		return claudeCodeAPIErrorInfo{}
	}
	return claudeCodeAPIErrorInfo{
		status:  claudeCodeSourceAPIErrorStatus(payload.Status),
		typ:     payload.Type,
		message: payload.Message,
	}
}

func claudeCodeSourceAPIErrorStatus(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return number.String()
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	return ""
}

func claudeCodeSourceAPIErrorClass(info claudeCodeAPIErrorInfo) string {
	if info.status != "" {
		return "api_error_" + info.status
	}
	return "api_error"
}

func claudeCodeSourceAPIErrorMessage(rec claudeCodeSourceRecord, info claudeCodeAPIErrorInfo) string {
	if info.message != "" {
		return info.message
	}
	if rec.Content != "" {
		return rec.Content
	}
	if info.typ != "" {
		return info.typ
	}
	return "api_error"
}
