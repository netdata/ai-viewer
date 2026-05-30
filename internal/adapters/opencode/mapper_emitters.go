package opencode

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the NON-OP part emitters the part walker (mapper_parts.go)
// delegates to: text/file → PayloadRef, patch → LLM-op extras, compaction → INF
// log, retry → WRN log. Split out of mapper_ops.go to keep each file ≤400 lines
// (SOW-0005 round-2; the P1-C/P2-D additions pushed mapper_ops.go over budget).
// The OP emitters (LLM/reasoning/tool + the task→session op) stay in
// mapper_ops.go; the turn finalizer + token math in mapper_turn.go.

// emitTextPayload handles a text part (adapter-opencode.md §"Per-table emit
// rules": text is NOT an op; emit a PayloadRef for the assistant text scoped to
// the turn's most-recent LLM op). When no LLM op is open yet (a text part before
// any step-start) the ref is DROPPED — payload_refs.op_id is NOT NULL, so a ref
// with no op would FK-roll-back the ingest batch (mirrors codex's discipline).
func (m *sessionMapper) emitTextPayload(tc *turnContext, p partRow) []canonical.Event {
	if tc.llmOpSeq == 0 {
		return nil
	}
	return []canonical.Event{m.payloadRef(m.nextBase(msToMicros(p.TimeCreatedMs)), tc.turnSeq, tc.llmOpSeq, "llm_response", "text", p.ID, "text", -1)}
}

// recordPatch handles a patch part (adapter-opencode.md §"Per-table emit rules":
// patch is NOT an op; record file-change info in the surrounding LLM op's extras
// for the "Files changed" UI tab). The info is stashed on tc.llmExtras and
// re-emitted onto the LLM op at step-finish (closeLLMOp). When no LLM op is open
// the patch is dropped (no op to attach to); this is rare (patch always follows a
// step-start in practice). Returns no events of its own.
func (m *sessionMapper) recordPatch(tc *turnContext, data partData) []canonical.Event {
	if tc.llmOpSeq == 0 || tc.llmExtras == nil {
		return nil
	}
	if data.Hash != "" {
		tc.llmExtras["patch_hash"] = data.Hash
	}
	if len(data.Files) > 0 {
		tc.llmExtras["patch_files"] = data.Files
	}
	return nil
}

// emitCompactionLog handles a compaction part (adapter-opencode.md §"Per-table
// emit rules": compaction → INF LogEntry). It records the auto flag.
func (m *sessionMapper) emitCompactionLog(tc *turnContext, p partRow, data partData) []canonical.Event {
	base := m.nextBase(msToMicros(p.TimeCreatedMs))
	return []canonical.Event{m.logEntry(base, "INF", tc.turnSeq, tc.llmOpSeq,
		fmt.Sprintf("session compacted (auto=%t)", data.Auto),
		map[string]any{"auto": data.Auto})}
}

// emitRetryLog handles a retry part (adapter-opencode.md §"Per-table emit rules":
// retry → WRN LogEntry). It records the attempt number.
func (m *sessionMapper) emitRetryLog(tc *turnContext, p partRow, data partData) []canonical.Event {
	base := m.nextBase(msToMicros(p.TimeCreatedMs))
	return []canonical.Event{m.logEntry(base, "WRN", tc.turnSeq, tc.llmOpSeq,
		fmt.Sprintf("API retry attempt %d", data.Attempt),
		map[string]any{"attempt": data.Attempt})}
}

// emitFilePayload handles a file part (adapter-opencode.md §"Per-table emit
// rules": file → PayloadRef kind=user_attachment, LocationURI=data.url). Unlike
// the other PayloadRefs (which reference a SQLite field), the file URL is an
// external location used verbatim. The ref attaches to the turn's most-recent
// LLM op when one is open; when none is open it is DROPPED (op_id NOT NULL).
func (m *sessionMapper) emitFilePayload(tc *turnContext, p partRow, data partData) []canonical.Event {
	if tc.llmOpSeq == 0 || data.URL == "" {
		return nil
	}
	return []canonical.Event{canonical.PayloadRefEvent{
		EventBase:       m.nextBase(msToMicros(p.TimeCreatedMs)),
		SessionNativeID: m.nativeID(),
		TurnSeq:         tc.turnSeq,
		OpSeq:           tc.llmOpSeq,
		PayloadKind:     "user_attachment",
		Format:          "json",
		LocationURI:     data.URL,
	}}
}
