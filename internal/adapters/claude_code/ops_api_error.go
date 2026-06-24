package claude_code

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type apiErrorInfo struct {
	Status    string
	Type      string
	Message   string
	RequestID string
}

func (m *fileMapper) mapAPIError(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	if m.turnSeq == 0 {
		return m.mapAPIErrorWithTurnStart(rec, advance, tsUs)
	}
	return m.mapAPIErrorInCurrentTurn(rec, advance, tsUs)
}

func (m *fileMapper) mapAPIErrorWithTurnStart(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	turn := m.startUserTurn(advance, tsUs)
	events := m.mapAPIErrorInCurrentTurn(rec, advance, tsUs)
	return append([]canonical.Event{turn}, events...)
}

func (m *fileMapper) mapAPIErrorInCurrentTurn(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	body := rec.System
	info := parseAPIErrorInfo(body)
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
	started := canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpLLM,
		Name:            "api_error",
		Provider:        provider,
		Extras:          apiErrorExtras(body, info),
	}
	finalized := canonical.OpFinalizedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		Status:          "failed",
		ErrorClass:      apiErrorClass(info),
		ErrorMessage:    apiErrorMessage(body, info),
		EndTs:           tsUs,
	}
	logEntry := m.logEntry(advance(tsUs), "ERR", "api_error", rec)
	if body != nil && len(body.APIError) > 0 {
		logEntry.Extras["error"] = body.APIError
	}
	return []canonical.Event{started, finalized, logEntry}
}

func parseAPIErrorInfo(body *systemBody) apiErrorInfo {
	if body == nil || len(bytes.TrimSpace(body.APIError)) == 0 {
		return apiErrorInfo{}
	}
	var payload struct {
		Status    json.RawMessage `json:"status"`
		Type      string          `json:"type"`
		Message   string          `json:"message"`
		RequestID string          `json:"requestID"`
	}
	if err := json.Unmarshal(body.APIError, &payload); err != nil {
		return apiErrorInfo{}
	}
	return apiErrorInfo{
		Status:    apiErrorStatusString(payload.Status),
		Type:      payload.Type,
		Message:   payload.Message,
		RequestID: payload.RequestID,
	}
}

func apiErrorStatusString(raw json.RawMessage) string {
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

func apiErrorClass(info apiErrorInfo) string {
	if info.Status != "" {
		return "api_error_" + info.Status
	}
	return "api_error"
}

func apiErrorMessage(body *systemBody, info apiErrorInfo) string {
	if info.Message != "" {
		return info.Message
	}
	if body != nil && body.Content != "" {
		return body.Content
	}
	if info.Type != "" {
		return info.Type
	}
	return "api_error"
}

func apiErrorExtras(body *systemBody, info apiErrorInfo) map[string]any {
	extras := map[string]any{}
	if info.Status != "" {
		extras["apiErrorStatus"] = info.Status
	}
	if info.Type != "" {
		extras["apiErrorType"] = info.Type
	}
	if info.RequestID != "" {
		extras["apiErrorRequestID"] = info.RequestID
	}
	if body != nil && body.RetryMs != nil {
		extras["retryInMs"] = *body.RetryMs
	}
	if body != nil && body.RetryNumber != nil {
		extras["retryAttempt"] = *body.RetryNumber
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}
