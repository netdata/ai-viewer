package codex

import (
	"encoding/json"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file handles the codex sub-agent collab lifecycle event_msgs (F3): the
// parent→child spawn link (collab_agent_spawn_end) and the recognized-but-op-less
// markers (collab_close_end / collab_waiting_end, dispatched in ops_event.go).
// Kept separate from ops_enrich.go so the enrichment path stays focused.

// mapCollabSpawn handles event_msg.collab_agent_spawn_end (spec "Sub-Agent
// Linkage", F3). It emits a session op (Kind=session, Name=spawn) whose
// ChildSessionNativeID is the spawned thread's new_thread_id, so the topology
// view links the parent rollout to the child sub-agent rollout. The real wire
// link is sender_thread_id→new_thread_id (NOT agent_ref.thread_id, which the
// earlier spec wrongly named). The child rollout lands as its own file with
// source.subagent.thread_spawn.parent_thread_id; the ingester relaxes the FK and
// re-links when the child arrives (canonical-events.md out-of-order child). The
// spawn metadata (nickname/role/model) goes into the op Extras for the UI. When
// new_thread_id is absent the op is suppressed and a DBG log keeps it visible.
func (m *fileMapper) mapCollabSpawn(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	sp := decodeCollabSpawn(rec.Raw)
	if sp.newThreadID == "" {
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "collab_agent_spawn_end_no_child", nil)}
	}
	ts := m.ensureTurn(tsUs)
	out := make([]canonical.Event, 0, 3)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	turnSeq, opSeq := m.nextOp(ts)
	extras := collabSpawnExtras(sp)
	out = append(out,
		canonical.OpStartedEvent{
			EventBase:            advance(tsUs),
			SessionNativeID:      m.nativeID,
			TurnSeq:              turnSeq,
			Seq:                  opSeq,
			ParentOpSeq:          -1,
			Kind:                 canonical.OpSession,
			Name:                 "spawn",
			ChildSessionNativeID: sp.newThreadID,
			Extras:               extras,
		},
		canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         turnSeq,
			Seq:             opSeq,
			Status:          spawnStatus(sp.status),
			EndTs:           tsUs,
		},
	)
	return out
}

// collabSpawn is the decoded subset of a collab_agent_spawn_end payload (F3).
// Field names match the real wire form (5 real files; keys sender_thread_id,
// new_thread_id, new_agent_nickname, new_agent_role, model, reasoning_effort,
// status).
type collabSpawn struct {
	senderThreadID  string
	newThreadID     string
	newAgentNick    string
	newAgentRole    string
	model           string
	reasoningEffort string
	status          string
}

// decodeCollabSpawn reads the collab_agent_spawn_end payload fields off the
// verbatim envelope (F3). Unknown siblings are dropped (forward-compat).
func decodeCollabSpawn(raw []byte) collabSpawn {
	var env struct {
		Payload struct {
			SenderThreadID  string `json:"sender_thread_id"`
			NewThreadID     string `json:"new_thread_id"`
			NewAgentNick    string `json:"new_agent_nickname"`
			NewAgentRole    string `json:"new_agent_role"`
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoning_effort"`
			Status          string `json:"status"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return collabSpawn{}
	}
	p := env.Payload
	return collabSpawn{
		senderThreadID:  p.SenderThreadID,
		newThreadID:     p.NewThreadID,
		newAgentNick:    p.NewAgentNick,
		newAgentRole:    p.NewAgentRole,
		model:           p.Model,
		reasoningEffort: p.ReasoningEffort,
		status:          p.Status,
	}
}

// collabSpawnExtras builds the spawn op's Extras from the decoded fields (F3),
// surfacing the spawned agent's nickname/role/model and the relationship marker
// so the UI can label the sub-agent edge. Returns a non-empty map (the relation
// is always set when a child exists).
func collabSpawnExtras(sp collabSpawn) map[string]any {
	extras := map[string]any{"relationship": "sub_agent"}
	if sp.senderThreadID != "" {
		extras["sender_thread_id"] = sp.senderThreadID
	}
	if sp.newAgentNick != "" {
		extras["new_agent_nickname"] = sp.newAgentNick
	}
	if sp.newAgentRole != "" {
		extras["new_agent_role"] = sp.newAgentRole
	}
	if sp.model != "" {
		extras["model"] = sp.model
	}
	if sp.reasoningEffort != "" {
		extras["reasoning_effort"] = sp.reasoningEffort
	}
	return extras
}

// spawnStatus maps a collab_agent_spawn_end.status to a canonical op status: a
// "failed"/"error" reported status is failed; anything else (including the
// common "completed"/"") is completed (F3).
func spawnStatus(status string) string {
	switch status {
	case "failed", "error":
		return "failed"
	default:
		return "completed"
	}
}
