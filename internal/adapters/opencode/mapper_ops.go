package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the per-part OP EMITTERS the part walker (mapper_parts.go)
// delegates to: the LLM op (step-start/step-finish), reasoning, tool (+ the
// task→session op, AC#4), and the non-op text/patch/file/compaction/retry
// emitters. The pure tool helpers live in mapper_tools.go; the token math
// (computeStepDeltas, AC#3), turn finalizer (SOW decision #4), provider
// canonicalization (AC#7), and the PayloadRef opencode-sqlite:// seam (chunk D)
// live in mapper_turn.go.

// --- LLM op (step-start / step-finish) ----------------------------------------

// openLLMOp handles a step-start part: it opens a new LLM op (adapter-
// opencode.md §"Per-table emit rules": step-start → open LLM Op, name=modelID,
// provider=providerID from the parent message). The op stays open until the
// next step-finish closes it (closeLLMOp). Model/Provider/ProviderAlias come
// from the assistant message: ProviderAlias is data.providerID verbatim; Provider
// is the best-effort canonical mapping (default = alias) so the catalog seeds a
// provider row (catalog.go seeds only when Provider != "") (AC#7).
func (m *sessionMapper) openLLMOp(tc *turnContext, msg *messageData, p partRow, _ partData) []canonical.Event {
	tc.opSeq++
	tc.llmOpSeq = tc.opSeq
	tc.llmStartUs = msToMicros(p.TimeCreatedMs)
	tc.llmExtras = map[string]any{}
	alias := msg.ProviderID
	return []canonical.Event{canonical.OpStartedEvent{
		EventBase:       m.nextBase(tc.llmStartUs),
		SessionNativeID: m.nativeID(),
		TurnSeq:         tc.turnSeq,
		Seq:             tc.llmOpSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpLLM,
		Name:            msg.ModelID,
		Model:           msg.ModelID,
		Provider:        canonicalProvider(alias),
		ProviderAlias:   alias,
	}}
}

// closeLLMOp handles a step-finish part: it closes the currently-open LLM op
// with the per-step token DELTA (computed up front for the whole message;
// addressed by stepCumIdx) and the step's cost (adapter-opencode.md §"Per-table
// emit rules": step-finish → close LLM Op with per-op tokens via computeStepDeltas).
// If a patch part landed inside this step, its info was stashed in tc.llmExtras
// and is re-emitted onto the op via an idempotent OpStarted re-emit before the
// finalize (mirrors codex's enrichment re-emit; the writer upserts (turn,seq)).
// A step-finish with no open LLM op (orphan, adapter-opencode.md §"Edge Cases"
// #5) is a no-op rather than a crash.
func (m *sessionMapper) closeLLMOp(tc *turnContext, p partRow, data partData) []canonical.Event {
	if tc.llmOpSeq == 0 {
		// Orphan step-finish (no matching step-start). Forward-compat: nothing to
		// close. The step's tokens were already folded into stepDeltas, so they
		// are not lost for the turn rollup path; the op simply does not exist.
		tc.stepCumIdx++
		return nil
	}
	delta := tc.nextStepDelta()
	endUs := msToMicros(p.TimeCreatedMs)
	if endUs < tc.llmStartUs {
		endUs = tc.llmStartUs
	}
	out := make([]canonical.Event, 0, 2)
	// Re-emit the LLM OpStarted carrying any accumulated patch extras so they
	// reach ops.extras_json before the finalize (idempotent UPDATE on (turn,seq)).
	if len(tc.llmExtras) > 0 {
		out = append(out, canonical.OpStartedEvent{
			EventBase:       m.nextBase(tc.llmStartUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			Seq:             tc.llmOpSeq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Extras:          tc.llmExtras,
		})
	}
	out = append(out, canonical.OpFinalizedEvent{
		EventBase:        m.nextBase(endUs),
		SessionNativeID:  m.nativeID(),
		TurnSeq:          tc.turnSeq,
		Seq:              tc.llmOpSeq,
		Status:           "completed",
		EndTs:            endUs,
		TokensIn:         delta.Input,
		TokensOut:        delta.Output,
		TokensCacheRead:  delta.Cache.Read,
		TokensCacheWrite: delta.Cache.Write,
		CostUSD:          data.Cost,
		// CtxUsed = input + cache.read at this step-finish (the most-recent step's
		// cumulative input is the live context occupancy — adapter-opencode.md
		// "ctx_used" row). Uses the CUMULATIVE value (data.Tokens), not the delta:
		// context occupancy is a level, not a per-step increment.
		CtxUsed: data.Tokens.Input + data.Tokens.Cache.Read,
	})
	// The LLM op is now closed; subsequent reasoning/tool parts (until the next
	// step-start) have no parent step. They still attach to the just-closed op's
	// seq as ParentOpSeq so the topology stays under the LLM call that produced
	// them (matches the spec's "ParentOpSeq = the step-start's seq"); a new
	// step-start re-opens a fresh op.
	tc.llmExtras = map[string]any{}
	return out
}

// nextStepDelta returns the per-step token delta for the next step-finish in
// order, advancing stepCumIdx. Out-of-range (more step-finishes than precomputed
// deltas, which cannot happen for a well-formed message) yields a zero delta.
func (tc *turnContext) nextStepDelta() tokenCounts {
	if tc.stepCumIdx < 0 || tc.stepCumIdx >= len(tc.stepDeltas) {
		tc.stepCumIdx++
		return tokenCounts{}
	}
	d := tc.stepDeltas[tc.stepCumIdx]
	tc.stepCumIdx++
	return d
}

// computeStepDeltas (the cumulative→delta token math) and nonNeg / jsonTrimBytes
// live in mapper_turn.go alongside the turn finalizer that also uses them.

// --- reasoning op -------------------------------------------------------------

// emitReasoningOp handles a reasoning part (adapter-opencode.md §"Per-table emit
// rules": reasoning → reasoning Op, ParentOpSeq=current LLM Op). ReasoningKind is
// raw by default (the part is the model's raw chain-of-thought text) and summary
// when data.metadata.summary is truthy (spec firming). A missing time.end leaves
// the op running (no finalize); the reasoning body (data.text) is referenced as
// an llm_reasoning PayloadRef, never inlined.
func (m *sessionMapper) emitReasoningOp(tc *turnContext, p partRow, data partData) []canonical.Event {
	tc.opSeq++
	seq := tc.opSeq
	startUs := msToMicros(data.Time.Start)
	if startUs == 0 {
		startUs = msToMicros(p.TimeCreatedMs)
	}
	out := make([]canonical.Event, 0, 3)
	out = append(out, canonical.OpStartedEvent{
		EventBase:       m.nextBase(startUs),
		SessionNativeID: m.nativeID(),
		TurnSeq:         tc.turnSeq,
		Seq:             seq,
		ParentOpSeq:     tc.parentSeq(),
		Kind:            canonical.OpReasoning,
		ReasoningKind:   reasoningKind(p.Data),
	})
	// Body → llm_reasoning PayloadRef on the reasoning op (field=text).
	if data.Text != "" {
		out = append(out, m.payloadRef(m.nextBase(startUs), tc.turnSeq, seq, "llm_reasoning", "text", p.ID, "text", int64(len(data.Text))))
	}
	if data.Time.End != nil {
		endUs := msToMicros(*data.Time.End)
		if endUs < startUs {
			endUs = startUs
		}
		out = append(out, canonical.OpFinalizedEvent{
			EventBase:       m.nextBase(endUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			Seq:             seq,
			Status:          "completed",
			EndTs:           endUs,
		})
	}
	return out
}

// reasoningKind classifies a reasoning part as summary or raw (canonical-events
// .md:202). opencode carries no native discriminator, so the default is raw; a
// truthy data.metadata.summary flips it to summary (spec firming). The reasoning
// metadata is not a typed field on the chunk-A partData struct (which decodes
// only fields earlier chunks needed), so it is decoded locally from the raw part
// body here — keeping the new mapper-only concern out of the chunk-A types.
func reasoningKind(raw []byte) string {
	var d struct {
		Metadata struct {
			Summary bool `json:"summary"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &d) == nil && d.Metadata.Summary {
		return "summary"
	}
	return "raw"
}

// --- tool op (+ task→session op, AC#4) ----------------------------------------

// emitToolOp handles a tool part (adapter-opencode.md §"Per-table emit rules":
// tool → tool Op, namespace derived, status from state.status; tool='task' with
// state.metadata.sessionId → ALSO a session Op as the topology parent, AC#4).
// Start/end come from state.time; a missing end (running/pending) leaves the op
// running (no finalize). The output body (completed/error) is referenced as a
// tool_response PayloadRef. The session op is emitted FIRST so it becomes the
// topology parent the sub-agent attaches under.
func (m *sessionMapper) emitToolOp(tc *turnContext, p partRow, data partData) []canonical.Event {
	out := make([]canonical.Event, 0, 4)

	// task→session op (AC#4): emit the session op first so it is the topology
	// parent. The tool op follows so the turn still records the task invocation.
	if childID := taskChildSessionID(data); childID != "" {
		tc.opSeq++
		sessSeq := tc.opSeq
		out = append(out, canonical.OpStartedEvent{
			EventBase:            m.nextBase(toolStartUs(data, p)),
			SessionNativeID:      m.nativeID(),
			TurnSeq:              tc.turnSeq,
			Seq:                  sessSeq,
			ParentOpSeq:          tc.parentSeq(),
			Kind:                 canonical.OpSession,
			ChildSessionNativeID: childID,
		})
	}

	tc.opSeq++
	seq := tc.opSeq
	name, namespace := toolNameNamespace(data.Tool)
	startUs := toolStartUs(data, p)
	out = append(out, canonical.OpStartedEvent{
		EventBase:       m.nextBase(startUs),
		SessionNativeID: m.nativeID(),
		TurnSeq:         tc.turnSeq,
		Seq:             seq,
		ParentOpSeq:     tc.parentSeq(),
		Kind:            canonical.OpTool,
		Name:            name,
		ToolNamespace:   namespace,
	})

	status, endPtr, errMsg, hasOutput := toolTerminal(data)
	if hasOutput {
		out = append(out, m.payloadRef(m.nextBase(startUs), tc.turnSeq, seq, "tool_response", "json", p.ID, "state.output", -1))
	}
	if endPtr != nil {
		endUs := msToMicros(*endPtr)
		if endUs < startUs {
			endUs = startUs
		}
		out = append(out, canonical.OpFinalizedEvent{
			EventBase:       m.nextBase(endUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			Seq:             seq,
			Status:          status,
			ErrorMessage:    errMsg,
			EndTs:           endUs,
			BytesIn:         toolBytesIn(data),
			BytesOut:        toolBytesOut(data),
		})
	}
	return out
}

// The pure tool helpers (toolStartUs, toolTerminal, toolBytesIn/Out,
// taskChildSessionID, toolNameNamespace) live in mapper_tools.go.

// --- text / patch / file / compaction / retry ---------------------------------

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

// finalizeTurn, the cumulative→delta token math, provider canonicalization, the
// turnContext op-parent helper, and the PayloadRef URI seam live in
// mapper_turn.go (split out to keep this file ≤ ~400 lines).
