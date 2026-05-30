package opencode

import (
	"fmt"
	"math"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the turn finalizer, the cumulative→delta token math
// (computeStepDeltas, AC#3), the best-effort provider canonicalization (AC#7),
// the turnContext op-parent helper, and the PayloadRef URI seam chunk D fills
// with the opencode-sqlite:// builder. The per-part op emitters live in
// mapper_ops.go; the session/turn driver in mapper.go; the part dispatch in
// mapper_parts.go. Split out of mapper_ops.go to keep each file ≤ ~400 lines.

// --- cumulative→delta token math (AC#3) ---------------------------------------

// computeStepDeltas converts a message's ORDERED cumulative step-finish token
// snapshots into per-step deltas (AC#3). opencode reports step-finish tokens
// CUMULATIVELY within a message (adapter-opencode.md §"Tool calls and Models",
// "Canonical Model Gaps" #3): input 100,250,410 → per-op 100,150,160. Each field
// (input/output/reasoning/cache.read/cache.write) is deltad independently against
// the running previous cumulative via a CHECKED subtraction (subClampWarn): a
// non-monotonic value yields a negative delta CLAMPED to 0, and a crafted/corrupt
// value whose subtraction would OVERFLOW int64 is clamped to [0, MaxInt64] with a
// WARN rather than wrapping (SOW-0005 round-2 P2-F). The first snapshot's delta is
// itself (previous = zero). onWarn (may be nil) surfaces an overflow with context.
func computeStepDeltas(cumulative []tokenCounts, onWarn func(error)) []tokenCounts {
	if len(cumulative) == 0 {
		return nil
	}
	out := make([]tokenCounts, len(cumulative))
	var prev tokenCounts
	for i, cur := range cumulative {
		out[i] = tokenCounts{
			Input:     subClampWarn(cur.Input, prev.Input, "step-finish tokens.input", onWarn),
			Output:    subClampWarn(cur.Output, prev.Output, "step-finish tokens.output", onWarn),
			Reasoning: subClampWarn(cur.Reasoning, prev.Reasoning, "step-finish tokens.reasoning", onWarn),
			Total:     subClampWarn(cur.Total, prev.Total, "step-finish tokens.total", onWarn),
			Cache: cacheTokens{
				Read:  subClampWarn(cur.Cache.Read, prev.Cache.Read, "step-finish tokens.cache.read", onWarn),
				Write: subClampWarn(cur.Cache.Write, prev.Cache.Write, "step-finish tokens.cache.write", onWarn),
			},
		}
		prev = cur
	}
	return out
}

// nonNeg clamps a delta to a non-negative value so a non-monotonic cumulative
// observation never emits a negative token count.
func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// subClampWarn returns a-b clamped to [0, MaxInt64], detecting int64 overflow on
// the subtraction (a crafted/corrupt cumulative value) and surfacing a WARN with
// the field label rather than wrapping (SOW-0005 round-2 P2-F). On overflow it
// clamps: a positive overflow (a huge, b very negative) saturates to MaxInt64; a
// negative overflow clamps to 0 (negative token counts are meaningless). The
// normal non-monotonic case (a<b, no overflow) still clamps to 0 via nonNeg.
func subClampWarn(a, b int64, field string, onWarn func(error)) int64 {
	d := a - b
	// Overflow on a-b iff the sign of b differs from the sign of a AND the sign of
	// the result differs from the sign of a (standard signed-subtraction overflow).
	if (b < 0) != (a < 0) && (d < 0) != (a < 0) {
		if onWarn != nil {
			onWarn(fmt.Errorf("opencode: %s delta overflow (%d-%d); clamped (P2-F)", field, a, b))
		}
		if a > 0 {
			return math.MaxInt64
		}
		return 0
	}
	return nonNeg(d)
}

// jsonTrimBytes returns the raw JSON with surrounding whitespace trimmed,
// treating a bare null (or empty) as no bytes so an absent input does not
// contribute a phantom 4-byte ("null") size to bytes_in.
func jsonTrimBytes(raw []byte) []byte {
	b := strings.TrimSpace(string(raw))
	if b == "" || b == "null" {
		return nil
	}
	return []byte(b)
}

// --- turn finalize ------------------------------------------------------------

// finalizeTurn builds the TurnFinalizedEvent for an assistant message. Per-turn
// tokens are the message-level cumulative totals MINUS the previous assistant
// message's cumulative totals (SOW decision #4 — see sessionMapper.prevTurnTokens
// for the implementer-verify note); cost is the message cost verbatim (already
// per-message in opencode, not cumulative). Per-turn cache tokens DO work via
// TurnFinalizedEvent's TokensCacheRead/Write fields (per-turn extras like cwd are
// deferred to SOW-0021). Status derives from data.finish/error (stop or any
// non-error finish → completed; data.error → failed). EndTs is the message's
// completed-or-created ts. The previous-cumulative snapshot is advanced AFTER
// computing this turn's delta.
func (m *sessionMapper) finalizeTurn(tc *turnContext, data *messageData, msg messageRow) canonical.TurnFinalizedEvent {
	cum := data.Tokens
	var delta tokenCounts
	if m.havePrevTurn {
		// Checked subtraction (subClampWarn): a crafted/corrupt cumulative value
		// clamps with a WARN instead of wrapping int64 (SOW-0005 round-2 P2-F).
		delta = tokenCounts{
			Input:     subClampWarn(cum.Input, m.prevTurnTokens.Input, "turn tokens.input", m.mwarn),
			Output:    subClampWarn(cum.Output, m.prevTurnTokens.Output, "turn tokens.output", m.mwarn),
			Reasoning: subClampWarn(cum.Reasoning, m.prevTurnTokens.Reasoning, "turn tokens.reasoning", m.mwarn),
			Cache: cacheTokens{
				Read:  subClampWarn(cum.Cache.Read, m.prevTurnTokens.Cache.Read, "turn tokens.cache.read", m.mwarn),
				Write: subClampWarn(cum.Cache.Write, m.prevTurnTokens.Cache.Write, "turn tokens.cache.write", m.mwarn),
			},
		}
	} else {
		delta = tokenCounts{
			Input:     cum.Input,
			Output:    cum.Output,
			Reasoning: cum.Reasoning,
			Cache:     cacheTokens{Read: cum.Cache.Read, Write: cum.Cache.Write},
		}
	}
	m.prevTurnTokens = cum
	m.havePrevTurn = true

	status, errClass := turnStatus(data)
	endUs := turnEndUs(data, msg)
	return canonical.TurnFinalizedEvent{
		EventBase:        m.nextBase(endUs),
		SessionNativeID:  m.nativeID(),
		Seq:              tc.turnSeq,
		Status:           status,
		ErrorClass:       errClass,
		EndTs:            endUs,
		TokensIn:         delta.Input,
		TokensOut:        delta.Output,
		TokensCacheRead:  delta.Cache.Read,
		TokensCacheWrite: delta.Cache.Write,
		CostUSD:          data.Cost,
	}
}

// defaultErrorClass is the safe ErrorClass label for an error object that
// carries no name (SOW-0005 round-2 P2-A). It is a CLASS string (a human label
// for the failure category), NOT a canonical op/turn status, so a generic
// constant here is correct — the terminal status is "failed", and this only
// names the error class when the source did not.
const defaultErrorClass = "error"

// errorClass returns the ErrorClass for an assistant error, defaulting to
// defaultErrorClass when the source supplied an error object with an empty name
// (SOW-0005 round-2 P2-A: error PRESENCE is what makes a turn failed; a missing
// name must not blank the class). err must be non-nil.
func errorClass(err *assistantError) string {
	if err.Name != "" {
		return err.Name
	}
	return defaultErrorClass
}

// turnStatus derives a turn's terminal status (adapter-opencode.md §"Per-table
// emit rules"): an error OBJECT being PRESENT → failed (ErrorClass = error.name,
// or defaultErrorClass when the name is empty — SOW-0005 round-2 P2-A); else
// completed (data.finish="stop" or any non-error finish all map to completed —
// opencode does not record a per-turn aborted distinct from a session error).
// The predicate is error PRESENCE (data.Error != nil), not a non-empty name: an
// opencode error object with an empty name is still a failure.
func turnStatus(data *messageData) (status, errClass string) {
	if data.Error != nil {
		return "failed", errorClass(data.Error)
	}
	return "completed", ""
}

// turnIsTerminal reports whether an assistant message represents a COMPLETED
// turn — the predicate that gates TurnFinalizedEvent emission (adapter-opencode
// .md §"Per-table emit rules": finalize ONLY when data.time.completed is set, or
// the message carries an error, or it has at least one step-finish part).
// opencode writes a turn's message row LIVE while the turn is still in progress
// (data.time.completed nil, no step-finish part yet), so finalizing every
// assistant message would wrongly mark an in-flight turn completed. A turn that
// is not terminal stays RUNNING (TurnStarted with no TurnFinalized); a later
// poll re-emits the whole tree and finalizes it once it actually completes (the
// re-emit is idempotent — adapter-opencode.md §"Edge Cases" #4). hasStepFinish
// is supplied by the part walk (mapMessage), which already decoded the parts.
// Error PRESENCE (data.Error != nil) is terminal regardless of the error name
// (SOW-0005 round-2 P2-A).
func turnIsTerminal(data *messageData, hasStepFinish bool) bool {
	if data.Time.Completed != nil {
		return true
	}
	if data.Error != nil {
		return true
	}
	return hasStepFinish
}

// turnEndUs returns a turn's end timestamp (µs): the assistant message's
// data.time.completed when set, else the message row's time_created (a turn with
// no completed ts is still ordered by its creation).
func turnEndUs(data *messageData, msg messageRow) int64 {
	if data.Time.Completed != nil {
		return msToMicros(*data.Time.Completed)
	}
	return msToMicros(msg.TimeCreatedMs)
}

// --- provider canonicalization (AC#7) -----------------------------------------

// knownProviderAliases maps a handful of well-known opencode provider aliases to
// their canonical vendor name. opencode provider ids are USER-DEFINED aliases
// (adapter-opencode.md §"Multi-provider awareness"), so this is intentionally
// SMALL and conservative: only aliases whose canonical vendor is unambiguous are
// listed. Everything else passes through unchanged (the alias IS the catalog
// provider name until a future SOW normalizes — SOW decision #6). The canonical
// model has no shared providers table yet (internal/canonical has no
// providers.go), so this best-effort map lives in the adapter package.
var knownProviderAliases = map[string]string{
	"openrouter": "openrouter",
	"deepseek":   "deepseek",
	"openai":     "openai",
	"anthropic":  "anthropic",
	"google":     "google",
}

// canonicalProvider maps an opencode provider alias to a best-effort canonical
// vendor name, defaulting to the alias UNCHANGED when unknown (AC#7; SOW decision
// #6). An empty alias yields an empty provider (the LLM op then carries no
// provider and the catalog does not seed a provider row — catalog.go gates on
// Provider != ""). The mapper sets ProviderAlias to the verbatim alias regardless.
func canonicalProvider(alias string) string {
	if alias == "" {
		return ""
	}
	if c, ok := knownProviderAliases[alias]; ok {
		return c
	}
	return alias
}

// --- turnContext helpers ------------------------------------------------------

// parentSeq returns the ParentOpSeq for a reasoning/tool/session op: the open
// (or most-recently-closed) LLM op's seq, so the op nests under the LLM call that
// produced it (adapter-opencode.md "Op seq numbering within a turn"). Returns -1
// when no LLM op has been emitted in the turn (a tool/reasoning part before any
// step-start), making the op top-level within its turn (canonical ParentOpSeq=-1).
func (tc *turnContext) parentSeq() int {
	if tc.llmOpSeq == 0 {
		return -1
	}
	return tc.llmOpSeq
}

// --- PayloadRef seam (chunk D fills the opencode-sqlite:// URI builder) --------

// payloadURIBuilder turns an owning part id + field path into a PayloadRef
// LocationURI. The mapper is pure and DB-agnostic (adapter-opencode.md
// §"Payload references", "Mapper/URI seam"): it knows the part id and field but
// NOT the resolved database basename or the escaping, which live with the
// connection/discovery layer (chunk D). Chunk D injects the production builder
// (prefixing the resolved opencode.db basename); mapper-only tests inherit the
// deterministic default (defaultPayloadURI) so the seam is testable now. This
// mirrors codex, whose mapper defers file:// construction to payloadURI.
type payloadURIBuilder func(partID, field string) string

// defaultPayloadURI is the mapper's built-in PayloadRef URI builder, used when
// no production builder is injected (mapper-only unit tests). It delegates to
// buildPayloadURI in payloads.go — the single source of truth for the
// opencode-sqlite:// grammar (SOW-0005 chunk D) — so the relative form here and
// the form an injected production builder uses share one definition. The form is:
//
//	opencode-sqlite://?part_id=<id>&field=<field>
//
// Behaviour is byte-identical to the pre-chunk-D literal: for the Sonyflake part
// ids and fixed field names opencode emits, every character is URL-unreserved.
func defaultPayloadURI(partID, field string) string {
	return buildPayloadURI(partID, field)
}

// payloadURI builds a PayloadRef LocationURI for the given part/field via the
// injected builder, falling back to defaultPayloadURI when none is set (the
// zero-value mapper case in unit tests).
func (m *sessionMapper) payloadURI(partID, field string) string {
	if m.uriBuilder != nil {
		return m.uriBuilder(partID, field)
	}
	return defaultPayloadURI(partID, field)
}

// payloadRef builds a PayloadRefEvent scoped to the owning op (turnSeq/opSeq) so
// it references an op that EXISTS — payload_refs.op_id is NOT NULL REFERENCES
// ops(id), so an orphan ref would FK-roll-back the ingest batch (mirrors codex's
// discipline). The LocationURI is built from the part id + field via the
// chunk-D-injectable seam. originalBytes is the body byte length when known
// (-1 otherwise).
func (m *sessionMapper) payloadRef(base canonical.EventBase, turnSeq, opSeq int, kind, format, partID, field string, originalBytes int64) canonical.PayloadRefEvent {
	return canonical.PayloadRefEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID(),
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PayloadKind:     kind,
		Format:          format,
		LocationURI:     m.payloadURI(partID, field),
		OriginalBytes:   originalBytes,
	}
}
