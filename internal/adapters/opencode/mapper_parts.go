package opencode

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file walks one message's parts and synthesizes the turn + its ops. The
// session driver (mapper.go) calls mapMessage once per message in input order;
// the op emitters it delegates to live in mapper_ops.go. The part dispatch
// table is adapter-opencode.md §"Per-table emit rules" (the part-type table):
// step-start/step-finish bound an LLM op; reasoning/tool are nested ops;
// text/patch are not ops (text → PayloadRef on the LLM op; patch → LLM-op
// extras); compaction → INF log; retry → WRN log; file → PayloadRef; an unknown
// $.type is forward-compat data skipped with one WARN.

// turnContext holds the per-turn inference state mapMessage threads while
// walking parts: the canonical turn Seq, the running op-seq counter, the
// currently-open LLM op (parent for reasoning/tool ops and the attach point for
// text PayloadRefs and patch extras), and the ordered cumulative token snapshots
// from the message's step-finish parts (deltad at finalize via computeStepDeltas).
type turnContext struct {
	turnSeq int
	// opSeq is the 1-based op counter within this turn; it increments for EVERY
	// op the mapper emits (LLM, reasoning, tool, session), so ParentOpSeq always
	// names a real, already-emitted op. text/patch do not consume a seq (they
	// are not ops).
	opSeq int

	// llmOpSeq is the Seq of the currently-open LLM op (the most recent
	// step-start not yet closed by a step-finish), or 0 when none is open. It is
	// the ParentOpSeq for reasoning/tool ops and the OpSeq for text PayloadRefs
	// and patch extras within the step (adapter-opencode.md "Op seq numbering
	// within a turn").
	llmOpSeq int
	// llmStartUs is the open LLM op's start timestamp (µs), carried so the
	// step-finish that closes it can supply a sane EndTs floor.
	llmStartUs int64
	// llmExtras accumulates patch info (and any future non-op step metadata) to
	// be re-emitted onto the LLM op at step-finish via an idempotent OpStarted
	// re-emit (mirrors codex's enrichment re-emit; the writer upserts (turn,seq)).
	llmExtras map[string]any

	// stepCumIdx is the index of the NEXT step-finish within the message, used
	// to address the precomputed per-step deltas. The deltas are computed once
	// for the whole message up front (computeStepDeltas) because a step-finish's
	// tokens are cumulative within the message (AC#3); walking sequentially and
	// indexing keeps the emit loop simple.
	stepCumIdx int
	// stepDeltas is the precomputed per-step token delta sequence for this
	// message's step-finish parts, in order.
	stepDeltas []tokenCounts
}

// mapMessage projects one message (assistant or user) plus its parts onto
// canonical events. A user message anchors the following assistant turn but
// emits nothing of its own. A malformed/empty message body is skipped with one
// WRN log (forward-compat; the column is NOT NULL so an empty body is a
// corruption signal — types.go errEmptyData). The assistant turn is opened,
// its parts walked in order, and the turn finalized with the message-level
// per-turn token delta + cost (SOW decision #4).
func (m *sessionMapper) mapMessage(mwp messageWithParts) ([]canonical.Event, error) {
	data, err := decodeMessageData(mwp.Message.Data)
	if err != nil {
		// One structured WRN; skip the row rather than abort the session
		// (adapter-opencode.md §"Edge Cases" #1).
		base := m.nextBase(msToMicros(mwp.Message.TimeCreatedMs))
		return []canonical.Event{m.logEntry(base, "WRN", 0, 0,
			"message data undecodable: "+err.Error(),
			map[string]any{"message_id": mwp.Message.ID})}, nil
	}

	switch data.role() {
	case roleAssistant:
		return m.mapAssistantTurn(mwp, data)
	case roleUser:
		// User messages are turn anchors only; opencode pairs user→assistant and
		// the assistant message IS the turn (adapter-opencode.md §"Turn
		// synthesis"). Nothing to emit.
		return nil, nil
	default:
		// Unknown role: forward-compat skip with one WRN (types.go roleUnknown).
		base := m.nextBase(msToMicros(mwp.Message.TimeCreatedMs))
		return []canonical.Event{m.logEntry(base, "WRN", 0, 0,
			fmt.Sprintf("unknown message role %q", data.Role),
			map[string]any{"message_id": mwp.Message.ID})}, nil
	}
}

// mapAssistantTurn opens a turn for an assistant message, walks its parts, and
// finalizes the turn. Turn Seq is the assistant-message order (1-based). It also
// records the failed-terminal signal: when this message carries data.error, the
// session's terminal becomes failed (the LAST such message wins because messages
// are walked in order — adapter-opencode.md §"Per-table emit rules").
func (m *sessionMapper) mapAssistantTurn(mwp messageWithParts, data messageData) ([]canonical.Event, error) {
	m.turnSeq++
	tc := &turnContext{
		turnSeq:    m.turnSeq,
		stepDeltas: computeStepDeltas(stepFinishTokens(mwp.Parts)),
	}
	startUs := msToMicros(mwp.Message.TimeCreatedMs)
	out := make([]canonical.Event, 0, 4+2*len(mwp.Parts))

	out = append(out, canonical.TurnStartedEvent{
		EventBase:       m.nextBase(startUs),
		SessionNativeID: m.nativeID(),
		Seq:             tc.turnSeq,
	})

	for i := range mwp.Parts {
		evs, err := m.mapPart(tc, &data, mwp.Parts[i])
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}

	// Close any still-open LLM op at turn end (a step-start with no step-finish —
	// adapter-opencode.md §"Edge Cases" #5: orphan step-start is a running LLM op
	// with no finalize). We intentionally do NOT synthesize a cancelled finalize
	// here in chunk B: the spec's force-close semantics are a tailer/reconcile
	// concern; an unclosed op stays running, which the ingester records as
	// running (matches the running-tool/running-reasoning behavior and AC edge).

	out = append(out, m.finalizeTurn(tc, &data, mwp.Message))

	// Record the failed-terminal signal for the session (last error wins).
	if data.Error != nil && data.Error.Name != "" {
		m.failError = data.Error
		m.failEndUs = turnEndUs(&data, mwp.Message)
	}
	return out, nil
}

// mapPart dispatches one part to its emitter per the part-type table (adapter-
// opencode.md §"Per-table emit rules"). Returns the events for that part,
// advancing tc's op/LLM/step state. An unknown $.type is skipped with one WRN.
func (m *sessionMapper) mapPart(tc *turnContext, msg *messageData, p partRow) ([]canonical.Event, error) {
	data, err := decodePartData(p.Data)
	if err != nil {
		base := m.nextBase(0)
		return []canonical.Event{m.logEntry(base, "WRN", tc.turnSeq, tc.llmOpSeq,
			"part data undecodable: "+err.Error(),
			map[string]any{"part_id": p.ID})}, nil
	}

	switch data.kind() {
	case partStepStart:
		return m.openLLMOp(tc, msg, p, data), nil
	case partStepFinish:
		return m.closeLLMOp(tc, p, data), nil
	case partReasoning:
		return m.emitReasoningOp(tc, p, data), nil
	case partTool:
		return m.emitToolOp(tc, p, data), nil
	case partText:
		return m.emitTextPayload(tc, p), nil
	case partPatch:
		return m.recordPatch(tc, data), nil
	case partCompaction:
		return m.emitCompactionLog(tc, p, data), nil
	case partRetry:
		return m.emitRetryLog(tc, p, data), nil
	case partFile:
		return m.emitFilePayload(tc, p, data), nil
	case partSnapshot, partSubtask, partAgent:
		// Known-but-not-an-op part types observed as 0-count on the live DB
		// (adapter-opencode.md §"part" distribution). They carry no op/payload
		// the mapper materializes in v1; recorded as no-ops here (NOT a WARN —
		// they are known, just unused). A future SOW may surface subtask as a
		// session op once the part type is populated.
		return nil, nil
	default:
		// Unknown $.type: forward-compatibility data, skipped with one WRN
		// (types.go partUnknown; adapter-opencode.md §"Edge Cases" #1).
		base := m.nextBase(0)
		return []canonical.Event{m.logEntry(base, "WRN", tc.turnSeq, tc.llmOpSeq,
			fmt.Sprintf("unknown part type %q", data.RawType),
			map[string]any{"part_id": p.ID})}, nil
	}
}

// stepFinishTokens extracts the ordered cumulative token snapshots from a
// message's step-finish parts, in part order. The result feeds computeStepDeltas
// so per-op tokens are the deltas between successive cumulative values (AC#3).
// A part that fails to decode is skipped (it cannot contribute a snapshot); the
// mapPart walk surfaces its WRN separately.
func stepFinishTokens(parts []partRow) []tokenCounts {
	var out []tokenCounts
	for i := range parts {
		d, err := decodePartData(parts[i].Data)
		if err != nil {
			continue
		}
		if d.kind() == partStepFinish {
			out = append(out, d.Tokens)
		}
	}
	return out
}

// logEntry builds a LogEntryEvent attached to the session and the given turn/op
// scope (0 when not turn/op-scoped). Source is the adapter Format.
func (m *sessionMapper) logEntry(base canonical.EventBase, severity string, turnSeq, opSeq int, message string, extras map[string]any) canonical.LogEntryEvent {
	if extras == nil {
		extras = map[string]any{}
	}
	return canonical.LogEntryEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID(),
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		Severity:        severity,
		Source:          Format,
		Message:         message,
		Extras:          extras,
	}
}
