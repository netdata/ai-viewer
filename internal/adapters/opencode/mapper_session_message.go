package opencode

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

const (
	sessionMessageAgentSwitched = "agent-switched"
	sessionMessageModelSwitched = "model-switched"
)

func (m *sessionMapper) mapSessionMessages() ([]canonical.Event, error) {
	if len(m.sessionMessages) == 0 {
		return nil, nil
	}
	out := make([]canonical.Event, 0, len(m.sessionMessages))
	for _, row := range m.sessionMessages {
		ev, ok, err := m.mapSessionMessage(row)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *sessionMapper) mapSessionMessage(row sessionMessageRow) (canonical.Event, bool, error) {
	message, ok := sessionMessageLogMessage(row.Type)
	if !ok {
		m.mwarn(fmt.Errorf("opencode: unknown session_message type %q (table=session_message id=%s); skipping unrecognized event variant", row.Type, row.ID))
		return nil, false, nil
	}
	data, err := decodeSessionMessageData(row.Data)
	if err != nil {
		return nil, false, fmt.Errorf("opencode: undecodable session_message.data (table=session_message id=%s): %w", row.ID, err)
	}
	hash, err := canonicalJSONSHA256(row.Data)
	if err != nil {
		return nil, false, fmt.Errorf("opencode: hash session_message.data (table=session_message id=%s): %w", row.ID, err)
	}
	extras := map[string]any{
		"session_message_id":   row.ID,
		"session_message_type": row.Type,
		"data_sha256":          hash,
		"aiViewer": map[string]any{
			"parity": map[string]any{
				"class":            "log_entry",
				"nativeArtifactId": "session_message:" + row.ID + ":log",
				"selectorURI":      buildTableSelectorURI("session_message", row.ID),
			},
		},
	}
	if row.Seq > 0 {
		extras["seq"] = row.Seq
	}
	putStr(extras, "agent", data.Agent)
	if data.Model != nil {
		putStr(extras, "model_id", data.Model.modelID())
		putStr(extras, "provider_id", data.Model.ProviderID)
		putStr(extras, "variant", data.Model.Variant)
	}
	base := m.nextBase(m.msToMicrosWarn(row.TimeCreatedMs, "session_message.time_created"))
	return m.logEntry(base, "INF", 0, 0, message, extras), true, nil
}

func sessionMessageLogMessage(typ string) (string, bool) {
	switch typ {
	case sessionMessageAgentSwitched:
		return "session agent switched", true
	case sessionMessageModelSwitched:
		return "session model switched", true
	default:
		return "", false
	}
}
