package claude_code

import (
	"encoding/json"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func (m *fileMapper) applySnapshot(typ recordType, fields map[string]any, ev *canonical.SessionUpdatedEvent) {
	switch typ {
	case recLastPrompt:
		applyStringExtra(fields, ev, "lastPrompt")
	case recAITitle:
		m.applyAITitleSnapshot(fields, ev)
	case recCustomTitle:
		m.applyCustomTitleSnapshot(fields, ev)
	case recPermissionMode:
		applyStringExtra(fields, ev, "permissionMode")
	case recBridgeSession:
		applyBridgeSessionSnapshot(fields, ev)
	case recFileHistorySnapshot:
		applyFileHistorySnapshot(fields, ev)
	}
}

func applyStringExtra(fields map[string]any, ev *canonical.SessionUpdatedEvent, key string) {
	if v, ok := stringField(fields, key); ok {
		ev.Extras[key] = v
	}
}

func (m *fileMapper) applyAITitleSnapshot(fields map[string]any, ev *canonical.SessionUpdatedEvent) {
	v, ok := stringField(fields, "aiTitle")
	if !ok {
		return
	}
	// The AI-generated title is a human-readable label for the session, NOT the
	// agent's identity. It belongs in Extras (the UI can show it as a
	// session-label/tooltip), not in AgentName. Previously this set
	// ev.AgentName = v, which made the "Agent" column show the session title
	// instead of the agent type — confusing for the operator (feedback #4).
	ev.Extras["aiTitle"] = v
}

func (m *fileMapper) applyCustomTitleSnapshot(fields map[string]any, ev *canonical.SessionUpdatedEvent) {
	v, ok := stringField(fields, "customTitle")
	if !ok {
		return
	}
	// The operator's custom title is a label for the session, NOT the agent's
	// identity — same reasoning as aiTitle above. Keep it in Extras only.
	ev.Extras["customTitle"] = v
	m.customTitleSeen = true
}

func applyBridgeSessionSnapshot(fields map[string]any, ev *canonical.SessionUpdatedEvent) {
	for _, k := range []string{"bridgeSessionId", "lastSequenceNum"} {
		if v, ok := fields[k]; ok {
			ev.Extras["bridge."+k] = v
		}
	}
}

func applyFileHistorySnapshot(fields map[string]any, ev *canonical.SessionUpdatedEvent) {
	if backups := fileHistoryBackups(fields); backups != nil {
		ev.Extras["fileHistory"] = backups
	}
}

// snapshotUpdateOrNil returns nil when a SessionUpdatedEvent carries nothing
// useful (no extras and no agent-name update). Agent-name updates come only
// from the sub-agent metadata path (.meta.json agentType), not from title
// snapshots (which populate Extras only).
func snapshotUpdateOrNil(ev canonical.SessionUpdatedEvent) canonical.Event {
	if len(ev.Extras) == 0 && ev.AgentName == "" && ev.Model == "" && ev.Status == "" {
		return nil
	}
	return ev
}

// decodeRawFields decodes a record's raw bytes into a flat field map for
// snapshot extraction. Returns an empty map on failure (defensive).
func decodeRawFields(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// stringField returns the string value at key, or ("", false) when absent or
// not a string.
func stringField(fields map[string]any, key string) (string, bool) {
	v, ok := fields[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// fileHistoryBackups extracts the snapshot.trackedFileBackups object from a
// file-history-snapshot record's decoded fields (spec §3.11, P3). Returns nil
// when absent or empty, so the caller stores only non-empty backup maps
// (last-non-empty wins) rather than a meaningless boolean.
func fileHistoryBackups(fields map[string]any) map[string]any {
	snap, ok := fields["snapshot"].(map[string]any)
	if !ok {
		return nil
	}
	backups, ok := snap["trackedFileBackups"].(map[string]any)
	if !ok || len(backups) == 0 {
		return nil
	}
	return backups
}
