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
// extras); compaction → INF log; retry → WRN log; file → INF log (an attachment,
// round-4 P2-3); an unknown $.type is forward-compat data skipped with one WARN.

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

	// llmOpSeq is the Seq of the MOST RECENT LLM op, whether still open or
	// already closed by a step-finish. It is the ParentOpSeq for reasoning/tool
	// ops and the OpSeq for text PayloadRefs and patch extras within (or after)
	// the step (adapter-opencode.md "Op seq numbering within a turn"). It stays
	// set after a step-finish so a trailing reasoning/tool/text still nests under
	// the LLM call that produced it; openLLMState reports whether it is OPEN.
	llmOpSeq int
	// llmOpOpen reports whether the most-recent LLM op (llmOpSeq) is still OPEN
	// (a step-start with no intervening step-finish). It distinguishes the
	// force-close case (a new step-start while the prior op is still open →
	// emit a cancelled finalize, adapter-opencode.md §"Edge Cases" #5) from the
	// normal case (a new step-start after the prior op already closed). Reset to
	// false by closeLLMOp.
	llmOpOpen bool
	// llmStartUs is the open LLM op's start timestamp (µs), carried so the
	// step-finish that closes it can supply a sane EndTs floor.
	llmStartUs int64
	// llmExtras accumulates patch info (and any future non-op step metadata) to
	// be re-emitted onto the LLM op at step-finish via an idempotent OpStarted
	// re-emit (mirrors codex's enrichment re-emit; the writer upserts (turn,seq)).
	llmExtras map[string]any
	// llmName / llmModel / llmProvider / llmProviderAlias snapshot the open LLM op's
	// identity (from the assistant message) so the patch-enrichment OpStarted
	// re-emit in closeLLMOp is SELF-CONTAINED. The ingest writer updates ops.name
	// UNCONDITIONALLY (model/provider are COALESCE-protected, name is NOT — writer.go
	// :587), so a re-emit that omitted Name would BLANK ops.name. Carrying the full
	// identity makes the re-emit survive the unconditional update (SOW-0005 round-2
	// P2-D).
	llmName          string
	llmModel         string
	llmProvider      string
	llmProviderAlias string

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
		// One structured WRN session LogEntry (detail view) AND route through onError
		// so /api/health degrades: message.data is NOT-NULL, so an undecodable blob is
		// a corruption signal, not benign forward-compat drift (SOW-0005 round-3 P2-2).
		// The row is skipped, not aborted (adapter-opencode.md §"Edge Cases" #1).
		m.mwarn(fmt.Errorf("opencode: undecodable message.data (table=message id=%s); row skipped: %w", mwp.Message.ID, err))
		base := m.nextBase(m.msToMicrosWarn(mwp.Message.TimeCreatedMs, "message.time_created (undecodable)"))
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
		base := m.nextBase(m.msToMicrosWarn(mwp.Message.TimeCreatedMs, "message.time_created (unknown role)"))
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
		stepDeltas: computeStepDeltas(stepFinishTokens(mwp.Parts), m.mwarn),
	}
	startUs := m.msToMicrosWarn(mwp.Message.TimeCreatedMs, "message.time_created (turn start)")
	out := make([]canonical.Event, 0, 4+2*len(mwp.Parts))

	out = append(out, canonical.TurnStartedEvent{
		EventBase:       m.nextBase(startUs),
		SessionNativeID: m.nativeID(),
		Seq:             tc.turnSeq,
	})

	hasStepFinish := false
	for i := range mwp.Parts {
		evs, err := m.mapPart(tc, &data, mwp.Parts[i])
		if err != nil {
			return nil, err
		}
		if mwp.Parts[i].isStepFinish() {
			hasStepFinish = true
		}
		out = append(out, evs...)
	}

	// A step-start still OPEN at turn end (no step-finish closing it) stays a
	// RUNNING LLM op with no finalize (adapter-opencode.md §"Edge Cases" #4/#5:
	// orphan step-start is a running LLM op). It is the within-message force-close
	// (a NEW step-start arriving) that synthesizes a cancelled finalize — handled
	// in openLLMOp; the trailing open op is intentionally left running here.

	// Finalize the turn ONLY when it is terminal (adapter-opencode.md §"Per-table
	// emit rules": data.time.completed set, OR data.error, OR ≥1 step-finish
	// part). opencode writes the assistant message row live while the turn is
	// still running; a non-terminal turn stays RUNNING (TurnStarted with no
	// TurnFinalized) and a later poll re-emits + finalizes it once it completes
	// (idempotent). Without this gate every live row would be wrongly finalized.
	if turnIsTerminal(&data, hasStepFinish) {
		out = append(out, m.finalizeTurn(tc, &data, mwp.Message))
	}

	// Track the session's failed-terminal signal as the LAST assistant turn's
	// terminal error, NOT a sticky OR (SOW-0005 round-2 P1-B). Messages are walked
	// in order, so: if THIS turn carries an error, record it (error PRESENCE, not a
	// non-empty name — P2-A); if it does NOT, CLEAR any previously-tracked error
	// (a later turn recovered, so the session did not end failed). sessionFinalized
	// then reflects only the last turn's state — a session that errored on turn 3
	// but succeeded on turn 5 is NOT marked failed.
	if data.Error != nil {
		m.failError = data.Error
		m.failEndUs = m.turnEndUs(&data, mwp.Message)
	} else {
		m.failError = nil
		m.failEndUs = 0
	}
	return out, nil
}

// mapPart dispatches one part to its emitter per the part-type table (adapter-
// opencode.md §"Per-table emit rules"). Returns the events for that part,
// advancing tc's op/LLM/step state. An unknown $.type is skipped with one WRN.
//
//nolint:unparam // error return is required by the mapper family contract: the sibling mapMessage/mapAssistantTurn return real errors and the caller loop propagates mapPart's error through the same (evs, error) shape
func (m *sessionMapper) mapPart(tc *turnContext, msg *messageData, p partRow) ([]canonical.Event, error) {
	data, err := decodePartData(p.Data)
	if err != nil {
		// Session LogEntry (detail view) AND onError (health): part.data is NOT-NULL,
		// so an undecodable blob is corruption that must degrade /api/health, not just
		// a per-session WRN (SOW-0005 round-3 P2-2). The part is skipped, not aborted.
		m.mwarn(fmt.Errorf("opencode: undecodable part.data (table=part id=%s); part skipped: %w", p.ID, err))
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
		return m.emitFileLog(tc, p, data), nil
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
