package opencode

import (
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
// the running previous cumulative. A non-monotonic value (a reset or an
// out-of-order observation) would yield a negative delta, which would corrupt
// cost; it is CLAMPED to 0 (spec gap #3 — reconciliation recomputes the whole
// message; a clamp keeps a transient observation from emitting negatives). The
// first snapshot's delta is itself (previous = zero).
func computeStepDeltas(cumulative []tokenCounts) []tokenCounts {
	if len(cumulative) == 0 {
		return nil
	}
	out := make([]tokenCounts, len(cumulative))
	var prev tokenCounts
	for i, cur := range cumulative {
		out[i] = tokenCounts{
			Input:     nonNeg(cur.Input - prev.Input),
			Output:    nonNeg(cur.Output - prev.Output),
			Reasoning: nonNeg(cur.Reasoning - prev.Reasoning),
			Total:     nonNeg(cur.Total - prev.Total),
			Cache: cacheTokens{
				Read:  nonNeg(cur.Cache.Read - prev.Cache.Read),
				Write: nonNeg(cur.Cache.Write - prev.Cache.Write),
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
		delta = tokenCounts{
			Input:     nonNeg(cum.Input - m.prevTurnTokens.Input),
			Output:    nonNeg(cum.Output - m.prevTurnTokens.Output),
			Reasoning: nonNeg(cum.Reasoning - m.prevTurnTokens.Reasoning),
			Cache: cacheTokens{
				Read:  nonNeg(cum.Cache.Read - m.prevTurnTokens.Cache.Read),
				Write: nonNeg(cum.Cache.Write - m.prevTurnTokens.Cache.Write),
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

// turnStatus derives a turn's terminal status (adapter-opencode.md §"Per-table
// emit rules"): data.error present → failed (ErrorClass = error.name); else
// completed (data.finish="stop" or any non-error finish all map to completed —
// opencode does not record a per-turn aborted distinct from a session error).
func turnStatus(data *messageData) (status, errClass string) {
	if data.Error != nil && data.Error.Name != "" {
		return "failed", data.Error.Name
	}
	return "completed", ""
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
// no production builder is injected (mapper-only unit tests). It emits the
// relative opencode-sqlite:// form WITHOUT a database basename:
//
//	opencode-sqlite://?part_id=<id>&field=<field>
//
// Chunk D replaces it with a builder that prefixes the resolved db basename
// (opencode-sqlite://opencode.db?part_id=…&field=…). The relative form is a
// valid, deterministic anchor for tests and keeps the op→payload linkage intact.
func defaultPayloadURI(partID, field string) string {
	return "opencode-sqlite://?part_id=" + partID + "&field=" + field
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
