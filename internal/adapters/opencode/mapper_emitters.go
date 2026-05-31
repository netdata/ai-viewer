package opencode

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the NON-OP part emitters the part walker (mapper_parts.go)
// delegates to: text → PayloadRef, patch → LLM-op extras, compaction → INF log,
// retry → WRN log, file → INF log (SOW-0005 round-4 P2-3: a file part is an
// attachment, NOT a payload-with-op; it is surfaced as an INF LogEntry carrying
// filename/url/mime in extras rather than a PayloadRef with a non-canonical
// PayloadKind). Split out of mapper_ops.go to keep each file ≤400 lines (SOW-0005
// round-2; the P1-C/P2-D additions pushed mapper_ops.go over budget). The OP
// emitters (LLM/reasoning/tool + the task→session op) stay in mapper_ops.go; the
// turn finalizer + token math in mapper_turn.go.

// emitTextPayload handles a text part (adapter-opencode.md §"Per-table emit
// rules": text is NOT an op; emit a PayloadRef for the assistant text scoped to
// the turn's most-recent LLM op). When no LLM op is open yet (a text part before
// any step-start) the ref is DROPPED — payload_refs.op_id is NOT NULL, so a ref
// with no op would FK-roll-back the ingest batch (mirrors codex's discipline).
func (m *sessionMapper) emitTextPayload(tc *turnContext, p partRow) []canonical.Event {
	if tc.llmOpSeq == 0 {
		return nil
	}
	return []canonical.Event{m.payloadRef(m.nextBase(m.msToMicrosWarn(p.TimeCreatedMs, "part.time_created (text)")), tc.turnSeq, tc.llmOpSeq, "llm_response", "text", p.ID, "text", -1)}
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
	base := m.nextBase(m.msToMicrosWarn(p.TimeCreatedMs, "part.time_created (compaction)"))
	return []canonical.Event{m.logEntry(base, "INF", tc.turnSeq, tc.llmOpSeq,
		fmt.Sprintf("session compacted (auto=%t)", data.Auto),
		map[string]any{"auto": data.Auto})}
}

// emitRetryLog handles a retry part (adapter-opencode.md §"Per-table emit rules":
// retry → WRN LogEntry message `API retry attempt <n>: <error.name>`). It records
// the attempt number AND the triggering error's name (opencode's RetryPart carries
// an `error: ApiError` whose `name` classifies the failure — SOW-0005 round-6 P3-1).
// When the error name is absent (older/forward-compat retry part), the message and
// extras omit it so an empty `: ` suffix never leaks.
func (m *sessionMapper) emitRetryLog(tc *turnContext, p partRow, data partData) []canonical.Event {
	base := m.nextBase(m.msToMicrosWarn(p.TimeCreatedMs, "part.time_created (retry)"))
	msg := fmt.Sprintf("API retry attempt %d", data.Attempt)
	extras := map[string]any{"attempt": data.Attempt}
	if data.Error.Name != "" {
		msg += ": " + data.Error.Name
		extras["error.name"] = data.Error.Name
	}
	return []canonical.Event{m.logEntry(base, "WRN", tc.turnSeq, tc.llmOpSeq, msg, extras)}
}

// emitFileLog handles a file part (adapter-opencode.md §"Per-table emit rules":
// file → INF LogEntry). SOW-0005 round-4 P2-3: a file part is a user file
// ATTACHMENT, not an op-scoped payload artifact. The canonical PayloadRefEvent
// PayloadKind set (internal/canonical/events.go) is exactly
// llm_request|llm_response|llm_sdk_request|llm_sdk_response|llm_reasoning|
// tool_request|tool_response|log — none of which is a user file attachment — so
// emitting a "user_attachment" PayloadRef violated the canonical contract. Instead
// the attachment is surfaced as an INF LogEntry carrying filename/url/mime in its
// extras, scoped to the turn and (when open) the LLM op — mirroring how
// compaction/retry parts emit a LogEntry. A richer canonical attachment
// PayloadKind is deferred to a follow-up SOW. Unlike the dropped PayloadRef path,
// this is NOT gated on an open LLM op (a LogEntry's OpSeq may be 0): a file
// attachment before any step-start is still surfaced, turn-scoped, op 0. A part
// with no url/filename/mime at all emits nothing (no attachment to record).
func (m *sessionMapper) emitFileLog(tc *turnContext, p partRow, data partData) []canonical.Event {
	if data.URL == "" && data.Filename == "" && data.MIME == "" {
		return nil
	}
	extras := map[string]any{}
	putStr(extras, "filename", data.Filename)
	putStr(extras, "url", data.URL)
	putStr(extras, "mime", data.MIME)
	base := m.nextBase(m.msToMicrosWarn(p.TimeCreatedMs, "part.time_created (file)"))
	return []canonical.Event{m.logEntry(base, "INF", tc.turnSeq, tc.llmOpSeq, "file attachment", extras)}
}
