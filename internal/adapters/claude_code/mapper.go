package claude_code

import (
	"fmt"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// subEventBits bounds the number of sub-events a single record may emit.
// claude-code records fan out to at most a handful of events (an assistant
// record with N tool_use blocks emits 1 LLM op pair + N tool op-starts +
// thinking pairs); 12 bits (4096) is ample headroom. SourceSeq is an
// observability counter only (Gate decision #2); the durable resume key is
// the byte offset in the cursor.
const subEventBits = 12

// maxSubEventsPerRecord is the cap subEventBits implies.
const maxSubEventsPerRecord = 1 << subEventBits

// provider is constant for claude-code: every LLM op is an Anthropic call
// (spec §5.6).
const provider = "anthropic"

// syntheticModel is the producer's marker for assistant turns it injects
// locally rather than from the LLM (spec §3.2). Such records emit a
// LogEntry, never an LLM op.
const syntheticModel = "<synthetic>"

// fileMapper holds the per-file inference state needed to convert a
// claude-code transcript's uuid-chained records into the canonical
// session/turn/op model. One fileMapper processes exactly one transcript
// file (main or subagent) start-to-finish; it is NOT reused across files
// because turn/op numbering is per-session.
//
// State persists across Scan→Tail of the same file via the rebuild path:
// the adapter reconstructs a fileMapper at the cursor offset by replaying
// is cheap for tail (the offset is mid-file but turn/op counters restart;
// see the adapter's note on counter durability). Within one streaming
// pass the counters are monotone.
type fileMapper struct {
	sourceID string
	// nativeID is the canonical session id for THIS file: the raw
	// sessionId for a main transcript, or "<parentSessionId>:agent:<agentId>"
	// for a subagent sidechain.
	nativeID string
	// parentNativeID is empty for main transcripts; the parent's sessionId
	// for subagents.
	parentNativeID string
	// kind is root or sub_agent.
	kind canonical.SessionKind
	// agentName seeds SessionStartedEvent.AgentName (subagent: agentType
	// from .meta.json; main: filled later from custom/ai title).
	agentName string
	// absPath is the transcript's absolute path on disk, used as the
	// PayloadRef LocationURI for inline bodies (toolUseResult, file
	// attachments, compaction summaries live inline in the jsonl) and for
	// log attribution.
	absPath string
	// root is the configured projects root (absolute), used to enforce
	// symlink/path containment on any PayloadRef LocationURI (spec §6.1,
	// security.md §6). Empty disables the check (mapper-only unit tests).
	root string
	// sessionDir is the absolute <projDir>/<sessionId>/ dir, used to locate
	// spill files referenced by compact_file_reference attachments
	// (<sessionDir>/tool-results/<id>.txt). Empty for mapper-only tests.
	sessionDir string

	// recordIdx is the 0-based ordinal of the record being mapped, used to
	// derive a stable per-file SourceSeq. Monotone within a streaming pass.
	recordIdx uint64

	// sessionStarted guards the once-per-file SessionStartedEvent.
	sessionStarted bool
	// turnSeq is the current 1-based turn counter. 0 means no turn open yet.
	turnSeq int
	// opSeqInTurn is the 1-based op counter within the current turn.
	opSeqInTurn int
	// toolOps maps an open tool_use id to the turn/op it was emitted under,
	// so the matching user.tool_result can finalize it.
	toolOps map[string]openToolOp
	// toolUseToAgent maps a parent Agent tool_use id to the spawned
	// subagent's agentId (recovered from sidecar .meta.json). Empty for
	// subagent files. Used to set ChildSessionNativeID on the Agent op.
	toolUseToAgent map[string]string
	// modelSeen is the first non-synthetic model observed, used to emit a
	// single SessionUpdatedEvent carrying the model.
	modelSeen bool
	// customTitleSeen records that a custom-title snapshot has set the
	// AgentName, so a later ai-title does NOT clobber the operator's chosen
	// title (spec §3.7 precedence: custom wins regardless of arrival order).
	customTitleSeen bool
	// seenUnknownType dedups unknown-`type` SourceErrors to one per distinct
	// variant per file (spec §3.12, acceptance #2): a transcript with many
	// records of one unrecognized type must surface exactly one error, not
	// one per occurrence.
	seenUnknownType map[string]struct{}
	// agentOps records every parent `Agent` tool_use op the mapper emitted,
	// keyed by the child session's native id. The caller finalizes these on
	// the child sidechain's EOF (spec §8.1, P1b): the parent transcript has no
	// tool_result for the Agent tool, so the op is otherwise stuck running.
	agentOps map[string]agentOpRef
	// fullyRead reports whether this file was streamed to EOF with no parked
	// partial trailing line. Set by readTranscript. A fully-read subagent
	// sidechain in Scan finalizes its parent's Agent op (§8.1).
	fullyRead bool
	// lastTsUs is the timestamp (micros) of the most recent record carrying
	// one; the child's end time used to stamp the deferred Agent-op finalize
	// (spec §8.1). Stays 0 for a file whose records all lack timestamps.
	lastTsUs int64
}

// agentOpRef locates a parent `Agent` op so it can be finalized when its child
// sidechain ends (spec §8.1).
type agentOpRef struct {
	turnSeq int
	opSeq   int
}

// firstUnknownType reports whether typ is the first occurrence of this
// unknown record type on this file, recording it so subsequent occurrences
// are suppressed (spec §3.12). Lazily allocates the set.
func (m *fileMapper) firstUnknownType(typ string) bool {
	if m.seenUnknownType == nil {
		m.seenUnknownType = map[string]struct{}{}
	}
	if _, seen := m.seenUnknownType[typ]; seen {
		return false
	}
	m.seenUnknownType[typ] = struct{}{}
	return true
}

// openToolOp records where an in-flight tool op was emitted so its
// finalization (on the matching tool_result) lands under the same turn/op.
type openToolOp struct {
	turnSeq int
	opSeq   int
	name    string
}

// mapperConfig bundles the per-file inputs newFileMapper needs. Grouping them
// keeps the constructor readable as the field count grew (PayloadRef support
// added root + sessionDir).
type mapperConfig struct {
	sourceID       string
	absPath        string
	nativeID       string
	parentNativeID string
	kind           canonical.SessionKind
	agentName      string
	toolUseToAgent map[string]string
	// root is the configured projects root (absolute) for PayloadRef
	// containment; sessionDir locates spill files. Both may be empty in
	// mapper-only unit tests (PayloadRef LocationURI containment then relies
	// solely on the absPath being absolute).
	root       string
	sessionDir string
}

// newFileMapper constructs a mapper for one transcript file.
func newFileMapper(cfg mapperConfig) *fileMapper {
	toolUseToAgent := cfg.toolUseToAgent
	if toolUseToAgent == nil {
		toolUseToAgent = map[string]string{}
	}
	return &fileMapper{
		sourceID:       cfg.sourceID,
		nativeID:       cfg.nativeID,
		parentNativeID: cfg.parentNativeID,
		kind:           cfg.kind,
		agentName:      cfg.agentName,
		absPath:        cfg.absPath,
		root:           cfg.root,
		sessionDir:     cfg.sessionDir,
		toolOps:        map[string]openToolOp{},
		toolUseToAgent: toolUseToAgent,
	}
}

// mapRecord converts one parsed record into canonical events, advancing the
// mapper's inference state. Pure with respect to I/O; mutates only the
// receiver's counters. The first call on any file emits the
// SessionStartedEvent.
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

	// Bootstrap the session on the first record (spec §5.2).
	if !m.sessionStarted {
		tsUs := m.recordTs(rec)
		out = append(out, m.sessionStarted0(rec, advance(tsUs)))
		m.sessionStarted = true
	}

	switch rec.Env.Type {
	case recUser:
		evs, err := m.mapUser(rec, advance)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	case recAssistant:
		evs, err := m.mapAssistant(rec, advance)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	case recSystem:
		evs, err := m.mapSystem(rec, advance)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	case recAttachment:
		ts := m.recordTs(rec)
		out = append(out, m.logEntry(advance(ts), "DBG", "attachment", rec))
		// A `file` attachment carrying inline content also emits a PayloadRef
		// so the UI can surface the attached file (spec §5.4, P2b). Other
		// subtypes (incl. compact_file_reference, whose target lives outside
		// the served root) emit no ref — see §3.4.
		ref, ok, perr := m.emitFileAttachmentPayload(rec, advance(ts))
		if perr != nil {
			return nil, perr
		}
		if ok {
			out = append(out, ref)
		}
	case recQueueOperation:
		out = append(out, m.logEntry(advance(m.recordTs(rec)), "INF", "queue-operation", rec))
	case recPRLink:
		// Has a timestamp; surface as INF and also as a session-extras
		// update so the prLink is visible without joining logs.
		evs := m.mapPRLink(rec, advance)
		out = append(out, evs...)
	case recLastPrompt, recAITitle, recCustomTitle, recPermissionMode,
		recBridgeSession, recFileHistorySnapshot:
		// Last-wins metadata snapshots: no timeline event. Emit a
		// SessionUpdatedEvent so extras_json/title/model converge. These
		// records lack a timestamp; use the mapper's last-known ts (0 is
		// acceptable — the ingester applies a partial UPDATE keyed on
		// NativeID, not ordered by Ts for these).
		if ev := m.mapSnapshot(rec, advance(0)); ev != nil {
			out = append(out, ev)
		}
	default:
		// Unreachable: parseLine refuses unknown types and skips known
		// no-op types before mapRecord is reached.
		return nil, fmt.Errorf("claude_code: unhandled record type %q", rec.Env.Type)
	}
	return out, nil
}

// sessionStarted0 builds the once-per-file SessionStartedEvent.
func (m *fileMapper) sessionStarted0(rec record, base canonical.EventBase) canonical.SessionStartedEvent {
	rootNativeID := m.nativeID
	if m.parentNativeID != "" {
		// A subagent's root is its parent's root; the ingester resolver
		// walks the chain. We set rootNativeID to the parent so the
		// resolver's rootNativeId pointer is meaningful even before the
		// parent lands.
		rootNativeID = m.parentNativeID
	}
	extras := map[string]any{}
	if rec.Env.Cwd != "" {
		extras["cwd"] = rec.Env.Cwd
	}
	if rec.Env.Version != "" {
		extras["version"] = rec.Env.Version
	}
	if rec.Env.Entrypoint != "" {
		extras["entrypoint"] = rec.Env.Entrypoint
	}
	if rec.Env.GitBranch != "" {
		extras["gitBranch"] = rec.Env.GitBranch
	}
	if rec.Env.Slug != "" {
		extras["slug"] = rec.Env.Slug
	}
	return canonical.SessionStartedEvent{
		EventBase:      base,
		NativeID:       m.nativeID,
		RootNativeID:   rootNativeID,
		ParentNativeID: m.parentNativeID,
		Kind:           m.kind,
		AgentName:      m.agentName,
		Cwd:            rec.Env.Cwd,
		Extras:         extras,
	}
}

// recordTs parses the record's timestamp to micros, or returns 0 when the
// record lacks one (metadata snapshots).
func (m *fileMapper) recordTs(rec record) int64 {
	if rec.Env.Timestamp == "" {
		return 0
	}
	us, err := parseTsToMicros(rec.Env.Timestamp)
	if err != nil {
		return 0
	}
	return us
}

// logEntry builds a LogEntryEvent attached to the current session/turn.
func (m *fileMapper) logEntry(base canonical.EventBase, severity, kind string, rec record) canonical.LogEntryEvent {
	extras := map[string]any{"recordType": string(rec.Env.Type)}
	if rec.Env.Subtype != "" {
		extras["subtype"] = rec.Env.Subtype
	}
	return canonical.LogEntryEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Severity:        severity,
		Source:          Format,
		Message:         kind,
		Extras:          extras,
	}
}

// packSeq packs (recordIdx, subIdx) into a single uint64 that is monotone
// per file. subIdx is masked to subEventBits.
func packSeq(recordIdx, subIdx uint64) uint64 {
	return recordIdx<<subEventBits | (subIdx & (maxSubEventsPerRecord - 1))
}

// parseTsToMicros decodes an ISO-8601 timestamp into UNIX microseconds. The
// producer writes UTC RFC3339 with millisecond precision; nano precision is
// accepted too.
func parseTsToMicros(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("ts %q: %w", s, err)
	}
	return t.UnixMicro(), nil
}

// splitToolName derives the canonical op name and namespace from a
// claude-code tool name (spec §10.10). MCP tools are "mcp__<server>__<tool>"
// → name=<tool>, namespace="mcp:<server>". Built-ins → namespace="builtin".
func splitToolName(name string) (opName, namespace string) {
	if strings.HasPrefix(name, "mcp__") {
		rest := strings.TrimPrefix(name, "mcp__")
		if i := strings.Index(rest, "__"); i >= 0 {
			server := rest[:i]
			tool := rest[i+2:]
			return tool, "mcp:" + server
		}
		// Malformed mcp name; keep verbatim under builtin.
		return name, "builtin"
	}
	return name, "builtin"
}
