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
//
// Force-close (adapter-opencode.md §"Edge Cases" #5): if the PREVIOUS LLM op is
// still open (a step-start with no intervening step-finish), it is force-closed
// with Status="cancelled" and a synthetic EndTs = THIS step-start's start ts,
// emitted BEFORE the new OpStarted so the prior op is finalized in order. An op
// still open at TURN end stays running (no finalize) per Edge #4 — only a NEW
// step-start triggers the cancel.
func (m *sessionMapper) openLLMOp(tc *turnContext, msg *messageData, p partRow, _ partData) []canonical.Event {
	startUs := m.msToMicrosWarn(p.TimeCreatedMs, "part.time_created (step-start)")
	out := make([]canonical.Event, 0, 2)
	if tc.llmOpOpen {
		out = append(out, m.cancelOpenLLMOp(tc, startUs))
	}

	tc.opSeq++
	tc.llmOpSeq = tc.opSeq
	tc.llmOpOpen = true
	tc.llmStartUs = startUs
	tc.llmExtras = map[string]any{}
	alias := msg.ProviderID
	// Snapshot the op identity so the patch-enrichment re-emit (closeLLMOp) is
	// self-contained and survives the writer's unconditional ops.name update (P2-D).
	tc.llmName = msg.ModelID
	tc.llmModel = msg.ModelID
	tc.llmProvider = canonicalProvider(alias)
	tc.llmProviderAlias = alias
	out = append(out, canonical.OpStartedEvent{
		EventBase:       m.nextBase(startUs),
		SessionNativeID: m.nativeID(),
		TurnSeq:         tc.turnSeq,
		Seq:             tc.llmOpSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpLLM,
		Name:            tc.llmName,
		Model:           tc.llmModel,
		Provider:        tc.llmProvider,
		ProviderAlias:   tc.llmProviderAlias,
	})
	return out
}

// cancelOpenLLMOp synthesizes the cancelled OpFinalizedEvent for a previously-
// open LLM op that a new step-start supersedes (adapter-opencode.md §"Edge Cases"
// #5). EndTs is the new step-start's start ts (nextStartUs), floored to the open
// op's start so a clock anomaly never produces end < start. No tokens are folded
// in — a cancelled step never finished its accounting; its step-finish (if any)
// is consumed normally by the next closeLLMOp via stepCumIdx. The caller has
// already confirmed tc.llmOpOpen.
func (m *sessionMapper) cancelOpenLLMOp(tc *turnContext, nextStartUs int64) canonical.OpFinalizedEvent {
	endUs := nextStartUs
	if endUs < tc.llmStartUs {
		endUs = tc.llmStartUs
	}
	tc.llmOpOpen = false
	return canonical.OpFinalizedEvent{
		EventBase:       m.nextBase(endUs),
		SessionNativeID: m.nativeID(),
		TurnSeq:         tc.turnSeq,
		Seq:             tc.llmOpSeq,
		Status:          "cancelled",
		EndTs:           endUs,
	}
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
	endUs := m.msToMicrosWarn(p.TimeCreatedMs, "part.time_created (step-finish)")
	if endUs < tc.llmStartUs {
		endUs = tc.llmStartUs
	}
	// The op is now closed; a new step-start opens a fresh one. Clearing this
	// before any cancelled-finalize check means a normal close is never
	// force-cancelled (Edge #5 fires only when the prior op was still open).
	tc.llmOpOpen = false
	out := make([]canonical.Event, 0, 2)
	// Re-emit the LLM OpStarted carrying any accumulated patch extras so they
	// reach ops.extras_json before the finalize (idempotent UPDATE on (turn,seq)).
	// The re-emit carries the FULL op identity (Name/Model/Provider/ProviderAlias),
	// not just Extras: the ingest writer updates ops.name UNCONDITIONALLY (model/
	// provider are COALESCE-protected, name is NOT — writer.go:587), so an
	// identity-less re-emit would BLANK ops.name (SOW-0005 round-2 P2-D).
	if len(tc.llmExtras) > 0 {
		out = append(out, canonical.OpStartedEvent{
			EventBase:       m.nextBase(tc.llmStartUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			Seq:             tc.llmOpSeq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Name:            tc.llmName,
			Model:           tc.llmModel,
			Provider:        tc.llmProvider,
			ProviderAlias:   tc.llmProviderAlias,
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
		// context occupancy is a level, not a per-step increment. Saturating add with
		// a WARN on overflow so a crafted/corrupt pair cannot wrap to a negative
		// ctx_used (SOW-0005 round-3 P2-1).
		CtxUsed: addClampWarn(data.Tokens.Input, data.Tokens.Cache.Read, "ctx_used (tokens.input+tokens.cache.read)", m.mwarn),
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
	startUs := m.msToMicrosWarn(data.Time.Start, "part.data.time.start (reasoning)")
	if startUs == 0 {
		startUs = m.msToMicrosWarn(p.TimeCreatedMs, "part.time_created (reasoning)")
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
		endUs := m.msToMicrosWarn(*data.Time.End, "part.data.time.end (reasoning)")
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
	childID, metaMalformed := taskChildSessionID(data)
	if metaMalformed {
		// Present-but-unparseable task metadata: a possible sub-agent linkage was
		// dropped. Surface it (SOW-0005 P2.6) rather than silently losing the edge;
		// the tool op below is still emitted so the task invocation is recorded.
		m.mwarn(fmt.Errorf("opencode: malformed task metadata (table=part id=%s field=state.metadata); sub-agent linkage omitted", p.ID))
	}
	if childID != "" {
		tc.opSeq++
		sessSeq := tc.opSeq
		out = append(out, canonical.OpStartedEvent{
			EventBase:            m.nextBase(m.toolStartUs(data, p)),
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
	startUs := m.toolStartUs(data, p)
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
		endUs := m.msToMicrosWarn(*endPtr, "part.data.state.time.end (tool)")
		if endUs < startUs {
			endUs = startUs
		}
		// A failed tool op carries an ErrorClass label alongside the opencode detail
		// (state.error → ErrorMessage). opencode's tool error is a bare string with
		// no class, so the class is the generic defaultErrorClass (SOW-0005 round-2
		// P1-C). A non-failed status carries neither.
		var errClass string
		if status == "failed" {
			errClass = defaultErrorClass
		}
		out = append(out, canonical.OpFinalizedEvent{
			EventBase:       m.nextBase(endUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			Seq:             seq,
			Status:          status,
			ErrorClass:      errClass,
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

// The non-op part emitters (emitTextPayload, recordPatch, emitCompactionLog,
// emitRetryLog, emitFileLog) live in mapper_emitters.go; finalizeTurn, the
// cumulative→delta token math, provider canonicalization, the turnContext
// op-parent helper, and the PayloadRef URI seam live in mapper_turn.go (split out
// to keep each file ≤400 lines).
