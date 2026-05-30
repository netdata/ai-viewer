package opencode

import (
	"encoding/json"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file is the PURE row→event mapper for the opencode adapter (SOW-0005
// chunk B). Given one session row plus its ordered assistant/user messages and
// each message's ordered parts, mapSession emits the full canonical event
// stream for that session. It is DETERMINISTIC and RE-EMITTABLE: chunk C's
// tailer re-feeds an affected session's WHOLE tree on any change, and the
// ingester's idempotent upserts + the (post-SOW-0004) idempotent catalog
// absorb the re-emission. The mapper performs NO I/O and runs NO SQL — chunk C
// owns the database; the mapper consumes the chunk-A typed rows only
// (adapter-opencode.md §"Mapping to Canonical Events"; SOW-0005 Pre-Impl Gate
// "Canonical mapping").
//
// File split (each ≤ ~400 lines per the chunk brief): this file drives the
// session + turn loop and terminal-status decision; mapper_parts.go walks a
// message's parts; mapper_ops.go holds the op emitters, computeStepDeltas, the
// tool-namespace + provider-canonicalization helpers, and the PayloadRef seam
// that chunk D fills with the opencode-sqlite:// URI builder.

// Format is the stable adapter identifier ("opencode"). It is the Source the
// mapper stamps on every LogEntry and the name chunk D's adapter.go registers
// with the adapter registry. It is defined here (the mapper is the first
// non-test consumer) so chunk B compiles and is testable in isolation; chunk D
// references this const rather than redefining it (mirrors codex, where one
// Format const is shared by mapper.go and adapter.go).
const Format = "opencode"

// messageWithParts pairs one message row with its parts, already ordered by
// (time_created, id) — the unit chunk C hands the mapper. The mapper does not
// re-sort; ordering is the query layer's job (adapter-opencode.md §"Mapping to
// Canonical Events": parts walked in id order, assistant messages ordered by
// (time_created, id)).
type messageWithParts struct {
	Message messageRow
	Parts   []partRow
}

// mapSession projects one opencode session tree onto the canonical event
// stream. It is the package's single mapper entry point.
//
// Emission order (deterministic):
//  1. SessionStartedEvent (always, first).
//  2. For each assistant message in input order: TurnStartedEvent, then its
//     parts' ops/payloads/logs in part order, then TurnFinalizedEvent. User
//     messages anchor the following assistant turn but emit no events of their
//     own (opencode pairs a user→assistant cycle; the assistant message IS the
//     turn — adapter-opencode.md §"Turn synthesis").
//  3. SessionFinalizedEvent IFF a terminal signal is present (archived →
//     completed; last assistant message carries data.error → failed). A session
//     with neither stays running with no finalize, like claude-code/codex
//     (adapter-opencode.md §"Per-table emit rules", "Canonical Model Gaps" #5).
//
// SourceSeq is a deterministic per-event counter (observability only; the
// durable resume state is the watermark cursor, not SourceSeq — see
// canonical.EventBase). It is packed from a monotonically increasing record
// index so a re-emit of the same tree yields identical SourceSeqs.
func mapSession(sourceID string, s sessionRow, msgs []messageWithParts, opts ...MapOption) ([]canonical.Event, error) {
	m := newSessionMapper(sourceID, s)
	for _, o := range opts {
		o(m)
	}
	out := make([]canonical.Event, 0, 16+4*len(msgs))

	out = append(out, m.sessionStarted())

	for i := range msgs {
		evs, err := m.mapMessage(msgs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}

	if fin := m.sessionFinalized(); fin != nil {
		out = append(out, fin)
	}
	return out, nil
}

// sessionMapper threads the per-session inference state: turn numbering, the
// previous assistant message's cumulative token totals (for the message-level
// per-turn delta — SOW decision #4), and the running SourceSeq record index.
// One sessionMapper processes exactly one session start-to-finish; it is not
// reused (turn/op numbering is per-session).
type sessionMapper struct {
	sourceID string
	session  sessionRow

	// turnSeq is the last assigned 1-based turn Seq (assistant-message order).
	turnSeq int

	// prevTurnTokens is the PRIOR assistant message's cumulative token totals,
	// used to compute the current turn's per-turn delta (SOW decision #4: the
	// message-level tokens are the session-running total at completion of the
	// turn, so the per-turn value is this turn's cumulative minus the previous
	// turn's cumulative). havePrevTurn distinguishes "no prior turn" (turn 1,
	// whose delta is its own cumulative) from a genuine zero prior.
	//
	// IMPLEMENTER-VERIFY-ON-LIVE-DB (SOW decision #4 / spec adapter-opencode.md
	// "turn.tokens_in/out/cost" row): the STEP-LEVEL cumulative pattern is
	// verified (AC#3); this MESSAGE-LEVEL cumulative pattern is the analogous
	// one level up and is NOT yet independently confirmed against the live DB.
	// Before pinning a committed golden in chunk E, confirm message.data.tokens
	// is cumulative-across-the-session and not already per-turn; if it turns out
	// per-turn, drop the delta here and emit the value verbatim. The arithmetic
	// is isolated here so that flip is a one-line change.
	prevTurnTokens tokenCounts
	havePrevTurn   bool

	// recordIdx is the running 0-based record ordinal feeding SourceSeq. It is
	// advanced once per emitted event via the seq() closure; its absolute value
	// is never load-bearing (SourceSeq is observability-only), only its
	// determinism across re-emits matters.
	recordIdx uint64

	// failError / failEndUs carry the session's failed-terminal signal,
	// populated by mapAssistantTurn when an assistant message carries
	// data.error. The LAST erroring message wins (messages are walked in order),
	// matching adapter-opencode.md §"Per-table emit rules" ("the last assistant
	// message carrying data.error"). sessionFinalized consumes them when the
	// session is not archived. failError stays nil for a clean session.
	failError *assistantError
	failEndUs int64

	// uriBuilder is the chunk-D-injectable PayloadRef LocationURI builder (the
	// opencode-sqlite:// seam — see mapper_ops.go payloadURIBuilder). nil in
	// mapper-only unit tests, where defaultPayloadURI is used.
	uriBuilder payloadURIBuilder
}

// MapOption configures mapSession. The only option today injects the chunk-D
// PayloadRef URI builder; it is variadic so chunk C/D can call mapSession
// without it (mapper-only tests) and inherit the deterministic default.
type MapOption func(*sessionMapper)

// WithPayloadURIBuilder injects the production PayloadRef LocationURI builder
// (chunk D). When unset, mapSession uses defaultPayloadURI (the relative
// opencode-sqlite:// form with no database basename).
func WithPayloadURIBuilder(b payloadURIBuilder) MapOption {
	return func(m *sessionMapper) { m.uriBuilder = b }
}

// newSessionMapper constructs a mapper for one session.
func newSessionMapper(sourceID string, s sessionRow) *sessionMapper {
	return &sessionMapper{sourceID: sourceID, session: s}
}

// nativeID is the session's canonical native id (session.id).
func (m *sessionMapper) nativeID() string { return m.session.ID }

// rootNativeID returns the root of this session's tree: the parent when this is
// a sub-agent (so the ingester resolver has a meaningful root pointer even
// before the parent row lands — mirrors codex/claude_code), else the session's
// own id (adapter-opencode.md §"Per-table emit rules").
func (m *sessionMapper) rootNativeID() string {
	if m.session.ParentID != "" {
		return m.session.ParentID
	}
	return m.session.ID
}

// nextBase returns the next EventBase with a deterministic SourceSeq and the
// given canonical (microsecond) timestamp. Each call advances recordIdx, so the
// emitted stream's SourceSeqs are stable across re-emits of the same tree.
func (m *sessionMapper) nextBase(tsUs int64) canonical.EventBase {
	b := canonical.EventBase{SourceID: m.sourceID, SourceSeq: m.recordIdx, Ts: tsUs}
	m.recordIdx++
	return b
}

// sessionStarted builds the once-per-session SessionStartedEvent (adapter-
// opencode.md §"Per-table emit rules"). Kind = sub_agent when parent_id is set
// (+ParentNativeID), else root. Model is session.model $.id; Cwd is the start-
// of-session directory; AgentName is session.agent. Per-session extras carry
// the provider alias, version, slug, title, project id, and directory so the UI
// can attribute the capture (turn-extras like per-turn cwd are deferred to
// SOW-0021; no canonical turn Extras carrier exists — SOW decision #4).
func (m *sessionMapper) sessionStarted() canonical.SessionStartedEvent {
	kind := canonical.KindRoot
	if m.session.ParentID != "" {
		kind = canonical.KindSubAgent
	}
	mr := m.sessionModel()
	ev := canonical.SessionStartedEvent{
		EventBase:      m.nextBase(msToMicros(m.session.TimeCreatedMs)),
		NativeID:       m.session.ID,
		RootNativeID:   m.rootNativeID(),
		ParentNativeID: m.session.ParentID,
		Kind:           kind,
		AgentName:      m.session.Agent,
		Model:          mr.modelID(),
		Cwd:            m.session.Directory,
		Extras:         m.sessionExtras(mr),
	}
	return ev
}

// sessionModel decodes the session.model JSON ({id, providerID, variant?}),
// returning a zero modelRef when absent or malformed (forward-compatible: a
// missing model column on an older schema yields nil bytes).
func (m *sessionMapper) sessionModel() modelRef {
	if len(m.session.Model) == 0 {
		return modelRef{}
	}
	var mr modelRef
	if json.Unmarshal(m.session.Model, &mr) != nil {
		return modelRef{}
	}
	return mr
}

// sessionExtras builds sessions.extras_json from the session row. Only non-empty
// values are included so an older-schema row missing a column contributes
// nothing rather than an empty string. The provider alias is surfaced verbatim
// (the canonical provider mapping for ops is best-effort; see canonicalProvider).
func (m *sessionMapper) sessionExtras(mr modelRef) map[string]any {
	extras := map[string]any{}
	putStr(extras, "providerID", mr.ProviderID)
	putStr(extras, "variant", mr.Variant)
	putStr(extras, "version", m.session.Version)
	putStr(extras, "slug", m.session.Slug)
	putStr(extras, "title", m.session.Title)
	putStr(extras, "project_id", m.session.ProjectID)
	putStr(extras, "directory", m.session.Directory)
	if len(extras) == 0 {
		return nil
	}
	return extras
}

// sessionFinalized decides the session's terminal classification (adapter-
// opencode.md §"Per-table emit rules", "Canonical Model Gaps" #5):
//
//   - time_archived set  → SessionFinalized(completed, EndTs = archived ms→µs).
//     Archival is the only clean terminal signal opencode records.
//   - else last assistant message carries data.error → SessionFinalized(failed,
//     ErrorClass = error.name, EndTs = that message's completed-or-created ts).
//   - else running: NO SessionFinalized (opencode never finalizes a session, it
//     only archives — like claude-code/codex which have no per-session terminal).
//
// Archival WINS over an error: an archived session is a user action and its
// archive timestamp is the authoritative terminal, even if its last turn
// errored. Returns nil when the session stays running.
func (m *sessionMapper) sessionFinalized() canonical.Event {
	if m.session.TimeArchivedMs > 0 {
		ev := canonical.SessionFinalizedEvent{
			EventBase: m.nextBase(msToMicros(m.session.TimeArchivedMs)),
			NativeID:  m.session.ID,
			Status:    canonical.StatusCompleted,
			EndTs:     msToMicros(m.session.TimeArchivedMs),
		}
		return ev
	}
	if m.failError != nil {
		ev := canonical.SessionFinalizedEvent{
			EventBase:  m.nextBase(m.failEndUs),
			NativeID:   m.session.ID,
			Status:     canonical.StatusFailed,
			ErrorClass: m.failError.Name,
			EndTs:      m.failEndUs,
		}
		return ev
	}
	return nil
}

// msToMicros converts opencode's native milliseconds to canonical microseconds.
// A non-positive input (absent timestamp) maps to 0 so an unset column never
// fabricates a 1970-adjacent time (adapter-opencode.md §"Edge Cases" #6 — the
// single ms→µs conversion point for session-level times; per-part/turn times
// convert in mapper_parts.go via the same helper).
func msToMicros(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms * 1000
}

// putStr inserts k=v into the extras map only when v is non-empty, so an
// older-schema zero value contributes nothing.
func putStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}
