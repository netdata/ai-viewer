package codex

import (
	"fmt"
	"path/filepath"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// subEventBits bounds the number of sub-events a single record may emit. A
// codex record fans out to at most a handful of events (a function_call emits
// an OpStarted + tool_request PayloadRef; a message emits an OpStarted +
// OpFinalized + PayloadRef); 12 bits (4096) is ample headroom. SourceSeq is an
// observability counter only — the durable resume key is the byte offset in the
// cursor (cursor.go), so the exact packing is never load-bearing. Mirrors
// claude_code/mapper.go.
const subEventBits = 12

// maxSubEventsPerRecord is the cap subEventBits implies.
const maxSubEventsPerRecord = 1 << subEventBits

// Format is the stable adapter identifier (SOW-0004 decision C#3: Format =
// "codex"). It is the source name the mapper stamps onto every LogEntry.Source
// and the name Chunk D's adapter.go registers with the adapter registry; it is
// defined here (the mapper is the first non-test consumer) so Chunk B compiles
// and is testable in isolation. Chunk D references this const for registration
// rather than redefining it (mirrors claude_code, where one Format const is
// shared by mapper.go and adapter.go).
const Format = "codex"

// provider is constant for codex: every LLM op is an OpenAI Responses-API call
// (spec adapter-codex.md:322, "Cost calculation"). The pricing catalog is keyed
// on (provider="openai", model=turn_context.model).
const provider = "openai"

// fileMapper holds the per-file inference state needed to project one codex
// rollout's RolloutItem line stream onto the canonical session/turn/op model.
// One fileMapper processes exactly one rollout file (a root, a fork, or a
// sub-agent thread) start-to-finish; it is NOT reused across files because turn
// and op numbering is per-session.
//
// The mapper is PURE with respect to I/O: it reads no files and watches no
// directories. The scanner/tailer (Chunk C) drives it line-by-line via
// mapRecord and, at EOF, asks finalizeStale whether a hanging turn must be
// synthetically failed (the scanner owns file mtime; the mapper owns the
// open-turn state — spec rule #23, SOW C#3).
//
// State persists across Scan→Tail of the same file via the rebuild path
// (mirrors claude_code): the scanner replays the chain from offset 0 to
// reconstruct the per-file turn/op counters deterministically, gating emission
// to records at or after the resume offset, so a resume yields the SAME Seqs as
// a one-shot pass.
type fileMapper struct {
	sourceID string
	// nativeID is the canonical session id for THIS file: session_meta.id
	// (spec rule #1). For a fork or sub-agent it is still the child's own id;
	// the parent linkage is carried by parentNativeID.
	nativeID string
	// parentNativeID is empty for a root session; the parent thread id for a
	// sub-agent (source.subagent.thread_spawn.parent_thread_id) or the
	// forked_from_id for a fork (spec rule #1, "Sub-Agent Linkage").
	parentNativeID string
	// kind is root, sub_agent, fork, or tool_internal (spec rule #1).
	kind canonical.SessionKind
	// agentName seeds SessionStartedEvent.AgentName (sub-agent: agent_nickname
	// or agent_role; else "codex:" + originator) (spec rule #1).
	agentName string
	// absPath is the rollout's absolute path on disk, used to build the
	// PayloadRef LocationURI ("file://<abs>#L<line>") for inline bodies that
	// live in the jsonl (user/assistant messages, reasoning, tool I/O,
	// compaction summaries) and for log attribution. Empty in mapper-only unit
	// tests; the URI then carries the line anchor without an absolute prefix.
	absPath string

	// lineNo is the 1-based file line number of the record currently being
	// mapped. The scanner (Chunk C) sets it via setLineNo before each mapRecord
	// so the PayloadRef LocationURI can anchor "#L<line>" at the owning record
	// (spec rule #6/#7/#8, edge #7 — large bodies are referenced, never
	// inlined). 0 disables the anchor (mapper-only tests that do not set it).
	lineNo int

	// recordIdx is the 0-based ordinal of the record being mapped, used to
	// derive a stable per-file SourceSeq. Monotone within a streaming pass.
	recordIdx uint64

	// sessionStarted guards the once-per-file SessionStartedEvent (spec rule #1).
	sessionStarted bool
	// modelSeen records that a SessionUpdatedEvent(Model) has been emitted, so
	// the model is announced exactly once when first learned from a turn_context
	// (spec rule #2).
	modelSeen bool
	// model is the active turn's model (latest turn_context.model). Stamped onto
	// every LLM/reasoning op so cost can be computed downstream (spec rule #2,
	// #7). Empty until the first turn_context carrying a model.
	model string

	// turns maps a codex turn_id (UUID) to the synthesized 1-based turn state.
	// A turn_id of "" is the absent-turn_id fallback bucket (old CLI without
	// turn_id — spec edge #3); it shares the same map under the empty key.
	turns map[string]*turnState
	// turnOrder lists turn_ids in open order so finalizeStale can find the most
	// recent still-open turn deterministically (spec rule #23).
	turnOrder []string
	// turnSeqCounter is the last assigned 1-based turn Seq. Monotone per file.
	turnSeqCounter int
	// activeTurnID is the turn_id of the most-recently-opened turn, used to
	// attribute a session-level token_count (no turn_id) to the right turn
	// (spec rule #17, "Token accounting nuance") and to drive the absent-turn_id
	// fallback (spec edge #3).
	activeTurnID string
	// haveActiveTurn distinguishes activeTurnID=="" "no turn yet" from
	// activeTurnID=="" "the absent-turn_id fallback turn is active".
	haveActiveTurn bool

	// openOps maps an in-flight tool/llm op's call_id to where it was emitted so
	// the matching *_output (or enrichment event) finalizes/enriches the same
	// op (spec rule #9, #14, #15, #16). A call_id of "" is never tracked.
	openOps map[string]*openOp

	// seenUserCallIDs dedups user input across response_item.message(role=user)
	// and event_msg.user_message (spec rule #6, #18). Keyed on a content
	// fingerprint so the second arrival is suppressed regardless of order.
	seenUser map[string]struct{}

	// finalized guards finalizeStale so the synthetic finalize is emitted at
	// most once per file even if the scanner calls it more than once.
	staleFinalized bool

	// lastTsUs is the timestamp (micros) of the most recent record carrying one.
	// Observability only (cursor LastTsUs). Stays 0 for a file whose records all
	// lack timestamps.
	lastTsUs int64
}

// turnState tracks one synthesized turn's accumulation between its open
// (turn_context or task_started) and its close (task_complete / turn_aborted /
// stale finalize). Token rollup is the C#1 model: TokensIn/Out are the SUM of
// per-call last_token_usage over the token_count events attributed to this turn
// — never a delta of the cumulative total_token_usage (spec rule #4, #17).
type turnState struct {
	// seq is the canonical 1-based turn Seq.
	seq int
	// codexTurnID is the source turn_id (UUID), surfaced in
	// turns.extras_json.codex_turn_id (spec "Canonical Model Gaps" #2). Empty
	// for the absent-turn_id fallback turn.
	codexTurnID string
	// opSeq is the 1-based op counter within this turn.
	opSeq int
	// started reports whether a TurnStartedEvent was already emitted for this
	// turn (idempotency across turn_context + task_started — spec rule #2, #3).
	started bool
	// finalized reports whether a TurnFinalizedEvent was already emitted, so a
	// duplicate task_complete / a later stale finalize does not double-close.
	finalized bool
	// startTsUs is the turn's open timestamp (micros), used as a floor for the
	// synthetic stale-finalize EndTs (spec rule #23).
	startTsUs int64
	// tokensIn / tokensOut accumulate the C#1 per-call last_token_usage rollup
	// (spec rule #4, #17).
	tokensIn  int64
	tokensOut int64
	// tokensCacheRead / tokensCacheWrite accumulate the per-call cached-token
	// split when newer rollouts report it (canonical-events.md codex cache row).
	tokensCacheRead  int64
	tokensCacheWrite int64
	// ctxMax is the model_context_window stashed from task_started /
	// token_count, applied to the turn's last LLM op at finalize (spec rule #3,
	// #17).
	ctxMax int64
	// sandbox is the sandbox_policy.type snapshotted from the turn's
	// turn_context, surfaced in turns.extras_json.sandbox (spec rule #2,
	// "Canonical Model Gaps" #3).
	sandbox string
	// effort / approvalPolicy are turn_context policy snapshots for turn extras.
	effort         string
	approvalPolicy string
	// ttftMs is task_complete.time_to_first_token_ms, surfaced in
	// turns.extras_json.ttft_ms (spec "Canonical Model Gaps" #8).
	ttftMs int64
	// lastAgentMessage is event_msg.agent_message.message, surfaced in
	// TurnFinalized extras as the UI "latest answer" preview (spec rule #19).
	lastAgentMessage string
	// lastLLMOpSeq is the op Seq of the most recent LLM op in this turn, so a
	// trailing token_count attaches CtxUsed/CtxMax to it (spec rule #17). 0 when
	// no LLM op has been emitted yet.
	lastLLMOpSeq int
	// lastLLMEndTs is the EndTs of the turn's last LLM op, preserved so a
	// token_count re-finalize that adds CtxUsed/CtxMax does not clobber the op's
	// real end timestamp (the ingester reconciles fields on the (turn,seq)
	// upsert — canonical-events.md §Idempotency).
	lastLLMEndTs int64
	// lastLLMCtxUsed is the cumulative total_token_usage observed for the turn's
	// last LLM op (spec rule #17). The op's CtxUsed is set from this at finalize.
	lastLLMCtxUsed int64
}

// openOp records where an in-flight op was emitted so its finalize / enrichment
// lands under the same turn/op (spec rule #9, #14-16).
type openOp struct {
	turnID  string
	turnSeq int
	opSeq   int
	kind    canonical.OpKind
	name    string
	// extras accumulates enrichment (exec_command_end, mcp_tool_call_end,
	// patch_apply_end) merged onto the op's OpFinalized (spec rule #14-16). The
	// adapter does NOT emit a second op for an enrichment event.
	extras map[string]any
	// finalized guards against a second *_output finalizing the same op.
	finalized bool
}

// mapperConfig bundles the per-file inputs newFileMapper needs.
type mapperConfig struct {
	sourceID       string
	absPath        string
	nativeID       string
	parentNativeID string
	kind           canonical.SessionKind
	agentName      string
}

// newFileMapper constructs a mapper for one rollout file.
func newFileMapper(cfg mapperConfig) *fileMapper {
	return &fileMapper{
		sourceID:       cfg.sourceID,
		absPath:        cfg.absPath,
		nativeID:       cfg.nativeID,
		parentNativeID: cfg.parentNativeID,
		kind:           cfg.kind,
		agentName:      cfg.agentName,
		turns:          map[string]*turnState{},
		openOps:        map[string]*openOp{},
		seenUser:       map[string]struct{}{},
	}
}

// setLineNo records the 1-based file line number of the next record the scanner
// will feed to mapRecord, so a PayloadRef can anchor "#L<line>" at the owning
// record (spec rule #6/#7/#8). The scanner (Chunk C) calls this before each
// mapRecord; mapper-only unit tests may set it directly or leave it 0.
func (m *fileMapper) setLineNo(n int) { m.lineNo = n }

// mapRecord converts one parsed record into canonical events, advancing the
// mapper's inference state. Pure with respect to I/O; mutates only the
// receiver. The first call on any file emits the SessionStartedEvent (spec
// rule #1). Records that produce nothing actionable (e.g. an event_msg the
// adapter only uses for enrichment) return an empty slice.
func (m *fileMapper) mapRecord(rec record) ([]canonical.Event, error) {
	idx := m.recordIdx
	m.recordIdx++
	if ts := m.recordTs(rec); ts > m.lastTsUs {
		m.lastTsUs = ts
	}

	out := make([]canonical.Event, 0, 4)
	sub := uint64(0)
	advance := func(tsUs int64) canonical.EventBase {
		b := canonical.EventBase{
			SourceID:  m.sourceID,
			SourceSeq: packSeq(idx, sub),
			Ts:        tsUs,
		}
		sub++
		return b
	}

	// Bootstrap the session on the first record (spec rule #1). session_meta is
	// always line 1, but the bootstrap keys on sessionStarted (not on the record
	// type) so a corrupt file missing session_meta still anchors its events to a
	// session row (the scanner surfaces the missing-meta SourceError per rule
	// #24 before any mapping).
	if !m.sessionStarted {
		out = append(out, m.sessionStarted0(rec, advance))
		m.sessionStarted = true
	}

	switch rec.Type() {
	case recSessionMeta:
		// SessionStarted already emitted by the bootstrap above; a session_meta
		// arriving later (metadata-only append, recorder.rs:1610) carries no new
		// turn/op data the mapper acts on.
	case recTurnContext:
		out = append(out, m.mapTurnContext(rec, advance)...)
	case recResponseItem:
		evs, err := m.mapResponseItem(rec, advance)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	case recEventMsg:
		evs, err := m.mapEventMsg(rec, advance)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	case recCompacted:
		out = append(out, m.mapCompacted(rec, advance)...)
	default:
		// Unreachable: parseLine refuses unknown top-level types and skips
		// known no-op nested types before mapRecord is reached.
		return nil, fmt.Errorf("codex: unhandled record type %q", rec.Type())
	}
	return out, nil
}

// sessionStarted0 builds the once-per-file SessionStartedEvent (spec rule #1).
// Kind, parent linkage, AgentName, and Extras (cli_version, originator, source,
// cwd, git, sandbox, relationship) come from the session_meta payload when the
// first record IS a session_meta (the normal case); a non-meta first record
// (corrupt file) yields a minimal root session so its events still attach.
func (m *fileMapper) sessionStarted0(rec record, advance func(int64) canonical.EventBase) canonical.SessionStartedEvent {
	base := advance(m.recordTs(rec))
	ev := canonical.SessionStartedEvent{
		EventBase:      base,
		NativeID:       m.nativeID,
		RootNativeID:   m.rootNativeID(),
		ParentNativeID: m.parentNativeID,
		Kind:           m.kind,
		AgentName:      m.agentName,
	}
	if rec.SessionMeta != nil {
		applySessionMeta(&ev, rec.SessionMeta, m)
	} else if ev.Kind == "" {
		// Bootstrap fallback for a corrupt file whose first record is not a
		// session_meta (rule #24): a minimal root session so events still attach.
		// The scanner surfaces the missing-meta SourceError separately.
		ev.Kind = canonical.KindRoot
		m.kind = canonical.KindRoot
	}
	return ev
}

// rootNativeID returns the root of this session's tree. A child (fork or
// sub-agent) points at its parent so the ingester resolver has a meaningful
// root pointer even before the parent file lands (mirrors claude_code); a root
// session is its own root (spec rule #1, "Sub-Agent Linkage").
func (m *fileMapper) rootNativeID() string {
	if m.parentNativeID != "" {
		return m.parentNativeID
	}
	return m.nativeID
}

// recordTs parses the record's envelope timestamp to micros, or returns 0 when
// the record lacks one. The envelope timestamp is the canonical time source
// (spec adapter-codex.md:56-60).
func (m *fileMapper) recordTs(rec record) int64 {
	if rec.Timestamp() == "" {
		return 0
	}
	us, err := parseTsToMicros(rec.Timestamp())
	if err != nil {
		return 0
	}
	return us
}

// logEntry builds a LogEntryEvent attached to the current session and the
// active turn (TurnSeq 0 when no turn is open). kind is a short stable message
// label; severity is one of DBG/INF/WRN/ERR.
func (m *fileMapper) logEntry(base canonical.EventBase, severity, message string, extras map[string]any) canonical.LogEntryEvent {
	if extras == nil {
		extras = map[string]any{}
	}
	return canonical.LogEntryEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         m.activeTurnSeq(),
		Severity:        severity,
		Source:          Format,
		Message:         message,
		Extras:          extras,
	}
}

// activeTurnSeq returns the Seq of the most-recently-opened turn, or 0 when no
// turn is open. Used to scope LogEntry rows.
func (m *fileMapper) activeTurnSeq() int {
	if !m.haveActiveTurn {
		return 0
	}
	if ts, ok := m.turns[m.activeTurnID]; ok {
		return ts.seq
	}
	return 0
}

// payloadURI builds the PayloadRef LocationURI for a body inline in this
// rollout file at the given 1-based line number (spec rule #6/#7/#8, edge #7).
// The form is "file://<clean-abs>#L<line>" so the presenter reads the exact
// record on demand without ai-viewer ever copying the body into SQLite.
//
// NOTE (Chunk B↔D seam): this mapper-side builder is the minimal contract
// Chunk B needs to compile and be tested in isolation — it cleans the path and
// appends the line anchor but does NOT do symlink containment. Chunk D replaces
// it with the claude_code-verbatim containment version (payloads.go:
// resolveWithinRoot + EvalSymlinks), keeping the SAME "#L<line>" anchor so the
// emitted event stream is unchanged. When absPath is empty (mapper-only tests)
// the URI is just the line anchor.
func (m *fileMapper) payloadURI(lineNo int) string {
	anchor := ""
	if lineNo > 0 {
		anchor = fmt.Sprintf("#L%d", lineNo)
	}
	if m.absPath == "" {
		return anchor
	}
	cleaned := filepath.ToSlash(filepath.Clean(m.absPath))
	return "file://" + cleaned + anchor
}

// payloadRef builds a PayloadRefEvent for a body inline in this rollout at the
// record currently being mapped (m.lineNo). It is scoped to the owning op
// (turnSeq/opSeq) so it references an op that EXISTS — payload_refs.op_id is NOT
// NULL REFERENCES ops(id), so an orphan ref would FK-roll-back the ingest batch
// (mirrors claude_code's P1.1a discipline). OriginalBytes is the byte length of
// the verbatim line so the presenter can budget a read; -1 when unknown.
func (m *fileMapper) payloadRef(base canonical.EventBase, turnSeq, opSeq int, kind, format string, originalBytes int64) canonical.PayloadRefEvent {
	return canonical.PayloadRefEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PayloadKind:     kind,
		Format:          format,
		LocationURI:     m.payloadURI(m.lineNo),
		OriginalBytes:   originalBytes,
	}
}
