package parity

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

func (s *opencodeSourceState) rootNativeID(sessionID string) (string, error) {
	seen := map[string]struct{}{}
	current := sessionID
	for {
		if _, cycle := seen[current]; cycle {
			return current, nil
		}
		seen[current] = struct{}{}

		parentID, ok, err := s.parentNativeSessionID(current)
		if err != nil {
			return "", err
		}
		if !ok || parentID == "" {
			return current, nil
		}
		current = parentID
	}
}

func (s *opencodeSourceState) parentNativeSessionID(sessionID string) (string, bool, error) {
	var parentID string
	err := s.querier.QueryRowContext(s.ctx, `SELECT COALESCE(parent_id, '') FROM session WHERE id = ?`, sessionID).Scan(&parentID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("query opencode source parent session %q: %w", sessionID, err)
	}
	return parentID, true, nil
}

func opencodeTurnTerminal(data opencodeSourceMessageData, parts []opencodeSourcePart) bool {
	if data.Time.Completed != nil || data.Error != nil {
		return true
	}
	for _, part := range parts {
		var body struct {
			Type string `json:"type"`
		}
		if decodeErr := json.Unmarshal(part.Data, &body); decodeErr == nil && body.Type == "step-finish" {
			return true
		}
	}
	return false
}

func opencodeTurnEndUS(message opencodeSourceMessage, data opencodeSourceMessageData) int64 {
	if data.Time.Completed != nil {
		return opencodeMsToUS(*data.Time.Completed)
	}
	return opencodeMsToUS(message.TimeCreatedMs)
}

func opencodeMsToUS(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms * 1000
}

func opencodeNativeTurnID(turnSeq int64) string {
	return fmt.Sprintf("turn:%d", turnSeq)
}

func opencodeNativeOpID(turnSeq int64, opSeq int64) string {
	return fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
}

func opencodeAssistantErrorNativeID(turnSeq int64) string {
	return fmt.Sprintf("turn:%d:assistant_error", turnSeq)
}

func opencodeAssistantErrorClass(err *opencodeSourceAssistantError) string {
	if err == nil || err.Name == "" {
		return "error"
	}
	return err.Name
}

func opencodeAssistantErrorMessage(err *opencodeSourceAssistantError) string {
	if err == nil || len(err.Data) == 0 {
		return ""
	}
	var body struct {
		Message string `json:"message"`
	}
	if decodeErr := json.Unmarshal(err.Data, &body); decodeErr != nil {
		return ""
	}
	return body.Message
}
