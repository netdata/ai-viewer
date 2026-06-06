package claude_code

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var rawOnlyRecordTypes = map[recordType]struct{}{
	recAttachment:          {},
	recQueueOperation:      {},
	recLastPrompt:          {},
	recAITitle:             {},
	recCustomTitle:         {},
	recPermissionMode:      {},
	recPRLink:              {},
	recBridgeSession:       {},
	recFileHistorySnapshot: {},
}

// parseLine decodes one JSONL line into a record. Whitespace-only / empty
// lines and known no-op record types return skip=true without surfacing errors.
func parseLine(line []byte) (record, bool, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return record{}, true, nil
	}
	env, err := decodeEnvelope(trimmed)
	if err != nil {
		return record{}, false, err
	}
	return decodeRecord(trimmed, env)
}

func decodeEnvelope(raw []byte) (envelope, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if env.Type == "" {
		return envelope{}, errors.New("record.type is required")
	}
	return env, nil
}

func decodeRecord(raw []byte, env envelope) (record, bool, error) {
	rec := record{Env: env, Raw: append([]byte(nil), raw...)}
	if _, ok := rawOnlyRecordTypes[env.Type]; ok {
		return rec, false, nil
	}
	if _, ok := knownNoOpTypes[env.Type]; ok {
		return rec, true, nil
	}
	return decodeTypedRecord(rec, raw, env)
}

func decodeTypedRecord(rec record, raw []byte, env envelope) (record, bool, error) {
	switch env.Type {
	case recUser:
		return decodeUserRecord(rec, raw, env.Message)
	case recAssistant:
		return decodeAssistantRecord(rec, env.Message)
	case recSystem:
		return decodeSystemRecord(rec, raw)
	default:
		return record{}, false, &unknownTypeError{Type: string(env.Type)}
	}
}

func decodeUserRecord(rec record, raw []byte, message json.RawMessage) (record, bool, error) {
	msg, err := decodeUserMessage(message)
	if err != nil {
		return record{}, false, err
	}
	rec.User = &msg
	rec.HasToolUseResult = hasTopLevelToolUseResult(raw)
	return rec, false, nil
}

func decodeUserMessage(message json.RawMessage) (userMessage, error) {
	var msg userMessage
	if len(message) == 0 {
		return msg, nil
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return userMessage{}, fmt.Errorf("decode user.message: %w", err)
	}
	return msg, nil
}

func hasTopLevelToolUseResult(raw []byte) bool {
	var probe struct {
		ToolUseResult json.RawMessage `json:"toolUseResult"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return false
	}
	tur := bytes.TrimSpace(probe.ToolUseResult)
	return len(tur) > 0 && !bytes.Equal(tur, []byte("null"))
}

func decodeAssistantRecord(rec record, message json.RawMessage) (record, bool, error) {
	msg, err := decodeAssistantMessage(message)
	if err != nil {
		return record{}, false, err
	}
	rec.Assistant = &msg
	return rec, false, nil
}

func decodeAssistantMessage(message json.RawMessage) (assistantMessage, error) {
	var msg assistantMessage
	if len(message) == 0 {
		return msg, nil
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return assistantMessage{}, fmt.Errorf("decode assistant.message: %w", err)
	}
	return msg, nil
}

func decodeSystemRecord(rec record, raw []byte) (record, bool, error) {
	var body systemBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return record{}, false, fmt.Errorf("decode system: %w", err)
	}
	rec.System = &body
	return rec, false, nil
}
