package canonical

// EventKind identifies a canonical event variant. The string values are
// stable and are used in logs, debug dumps, and adapter golden fixtures.
// See .agents/sow/specs/canonical-events.md.
type EventKind string

const (
	EvSessionStarted   EventKind = "session_started"
	EvSessionUpdated   EventKind = "session_updated"
	EvSessionFinalized EventKind = "session_finalized"
	EvTurnStarted      EventKind = "turn_started"
	EvTurnFinalized    EventKind = "turn_finalized"
	EvOpStarted        EventKind = "op_started"
	EvOpFinalized      EventKind = "op_finalized"
	EvPayloadRef       EventKind = "payload_ref"
	EvLogEntry         EventKind = "log_entry"
	EvSourceProgress   EventKind = "source_progress"
	EvSourceError      EventKind = "source_error"
)

// String returns the textual representation of the EventKind, identical
// to the underlying constant value. Implemented for clarity in log
// output and error messages.
func (k EventKind) String() string { return string(k) }

// SessionKind enumerates the variants of session topology recorded in
// the canonical model. See sessions.kind in data-model.md.
type SessionKind string

const (
	// KindRoot is a top-level interactive session.
	KindRoot SessionKind = "root"
	// KindSubAgent is a sub-agent / Task / Agent tool spawn.
	KindSubAgent SessionKind = "sub_agent"
	// KindToolInternal is an internal helper session created by a tool.
	KindToolInternal SessionKind = "tool_internal"
	// KindFork is a session forked from another (e.g. codex forked_from_id).
	KindFork SessionKind = "fork"
)

// String returns the textual representation of the SessionKind.
func (k SessionKind) String() string { return string(k) }

// SessionStatus is the terminal state classification for a session.
// See sessions.status in data-model.md and the SessionFinalizedEvent
// notes in canonical-events.md for per-source semantics.
type SessionStatus string

const (
	// StatusRunning marks a session with no terminal signal observed yet.
	// Used for in-flight sessions and for sources without a native
	// terminal signal (e.g. claude-code).
	StatusRunning SessionStatus = "running"
	// StatusCompleted marks a session with a clean terminal signal.
	StatusCompleted SessionStatus = "completed"
	// StatusFailed marks a session with an explicit error record.
	StatusFailed SessionStatus = "failed"
	// StatusAbandoned marks a session that started but produced no turns
	// (orphan session_start only).
	StatusAbandoned SessionStatus = "abandoned"
	// StatusInterrupted marks a session that started turns but produced
	// no terminal record (process killed mid-turn).
	StatusInterrupted SessionStatus = "interrupted"
)

// String returns the textual representation of the SessionStatus.
func (s SessionStatus) String() string { return string(s) }

// OpKind enumerates the variants of canonical ops. Every span in
// ai-viewer (LLM call, tool call, child session, reasoning block,
// compaction event, system housekeeping op) is one of these kinds.
// See ops.kind in data-model.md.
type OpKind string

const (
	// OpLLM is a model API call.
	OpLLM OpKind = "llm"
	// OpTool is a tool invocation (shell, fs, MCP, builtin).
	OpTool OpKind = "tool"
	// OpSession is a child-session attachment (sub-agent / Task / Agent tool).
	OpSession OpKind = "session"
	// OpReasoning is a model reasoning block / chain-of-thought.
	OpReasoning OpKind = "reasoning"
	// OpInternal is adapter-internal housekeeping. UI hides by default.
	OpInternal OpKind = "internal"
	// OpSystem is a session-level system op (init/fin/handoff).
	OpSystem OpKind = "system"
	// OpCompaction is a history compaction event (claude-code, codex).
	OpCompaction OpKind = "compaction"
)

// String returns the textual representation of the OpKind.
func (k OpKind) String() string { return string(k) }

// Event is the contract every concrete canonical event satisfies. The
// four accessors are sufficient for the ingester to dedup, order, and
// route events without reflection.
type Event interface {
	// EventKind returns the discriminator for this concrete event type.
	EventKind() EventKind
	// EventSourceID returns the source the event originated from.
	EventSourceID() string
	// EventSourceSeq returns the monotonic-per-source sequence number
	// used as the dedup key.
	EventSourceSeq() uint64
	// EventTs returns the event timestamp in UNIX-microseconds (UTC).
	EventTs() int64
}

// EventBase carries the three fields common to every canonical event.
// Concrete event types embed it; the EventKind discriminator is encoded
// in the concrete type's own EventKind method rather than in a mutable
// field, so an adapter cannot construct an event whose declared kind
// disagrees with its Go type.
type EventBase struct {
	// SourceID identifies the source the event was produced from
	// (e.g. "aiagent-v3:/home/user/.ai-agent/sessions").
	SourceID string
	// SourceSeq is monotonic per source and used for dedup, not
	// ordering. The ingester maintains a high-water-mark per source.
	SourceSeq uint64
	// Ts is the event timestamp in UNIX-microseconds (UTC). The
	// ingester orders events by Ts within a session.
	Ts int64
}

// EventSourceID implements Event.
func (b EventBase) EventSourceID() string { return b.SourceID }

// EventSourceSeq implements Event.
func (b EventBase) EventSourceSeq() uint64 { return b.SourceSeq }

// EventTs implements Event.
func (b EventBase) EventTs() int64 { return b.Ts }

// SessionStartedEvent announces that a new session has been observed by
// the adapter. The ingester upserts the corresponding sessions row.
type SessionStartedEvent struct {
	EventBase
	// NativeID is the originId/sessionId/uuid from the source.
	NativeID string
	// RootNativeID is the root of the session tree; equal to NativeID
	// for top-level sessions.
	RootNativeID string
	// ParentNativeID is empty for root sessions. When non-empty, the
	// ingester links the row to the parent session.
	ParentNativeID string
	// ParentOpKey is an opaque adapter-specific identifier of the parent
	// op that spawned this child (e.g. "<turnSeq>:<opSeq>" or the
	// parent's opId string). Used for cross-referencing only.
	ParentOpKey string
	// Kind classifies the session as root, sub_agent, tool_internal, or fork.
	Kind SessionKind
	// AgentName may be empty if not yet known at session start.
	AgentName string
	// Model may be empty if not yet known at session start.
	Model string
	// Cwd is the working directory at session start (claude-code, codex,
	// opencode). May be empty for ai-agent sources.
	Cwd string
	// CallPath is the durable agent-chain string (ai-agent v3 'callPath').
	// May be empty.
	CallPath string
	// Extras carries format-specific extras that land in sessions.extras_json.
	// The use of map[string]any here is intentional and documented; see
	// AGENTS.md and canonical-events.md.
	Extras map[string]any
}

// EventKind implements Event.
func (SessionStartedEvent) EventKind() EventKind { return EvSessionStarted }

// SessionUpdatedEvent surfaces metadata that became known after the
// session started (model name from the first LLM call, agent name from
// a later record, etc.). The ingester applies a partial UPDATE; fields
// left empty are not changed.
type SessionUpdatedEvent struct {
	EventBase
	NativeID  string
	AgentName string
	Model     string
	Cwd       string
	// Status is empty when no status update is intended; otherwise one
	// of the SessionStatus constants encoded as a string for forward
	// compatibility with adapter-specific intermediate states.
	Status string
	Extras map[string]any
}

// EventKind implements Event.
func (SessionUpdatedEvent) EventKind() EventKind { return EvSessionUpdated }

// SessionFinalizedEvent records the terminal classification of a session.
// See canonical-events.md for the per-source availability of terminal
// signals.
type SessionFinalizedEvent struct {
	EventBase
	NativeID     string
	Status       SessionStatus
	ErrorClass   string
	ErrorMessage string
	EndTs        int64
}

// EventKind implements Event.
func (SessionFinalizedEvent) EventKind() EventKind { return EvSessionFinalized }

// TurnStartedEvent marks the beginning of a turn within a session. Seq
// is 1-based within a session; seq 0 is reserved for init turns (ai-agent v2).
type TurnStartedEvent struct {
	EventBase
	SessionNativeID string
	Seq             int
}

// EventKind implements Event.
func (TurnStartedEvent) EventKind() EventKind { return EvTurnStarted }

// TurnFinalizedEvent records the terminal classification of a turn and
// per-turn aggregate token / cost numbers. Token counts are always
// deltas (the adapter converts cumulative source counters before
// emitting; see canonical-events.md).
type TurnFinalizedEvent struct {
	EventBase
	SessionNativeID  string
	Seq              int
	Status           string
	ErrorClass       string
	EndTs            int64
	TokensIn         int64
	TokensOut        int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	CostUSD          float64
}

// EventKind implements Event.
func (TurnFinalizedEvent) EventKind() EventKind { return EvTurnFinalized }

// OpStartedEvent announces an op (the universal span) is starting.
// Adapters MUST emit ops in chronological order within a session.
type OpStartedEvent struct {
	EventBase
	SessionNativeID string
	TurnSeq         int
	Seq             int
	// ParentOpSeq is -1 when this op is top-level within its turn.
	ParentOpSeq int
	Kind        OpKind
	Name        string
	// ToolNamespace applies to OpTool ops: 'mcp:<server>' | 'shell' |
	// 'fs' | 'builtin' | format-specific.
	ToolNamespace string
	// Model applies to OpLLM ops.
	Model string
	// Provider applies to OpLLM ops: 'anthropic' | 'openai' | 'google' |
	// 'openrouter' | ...
	Provider string
	// ProviderAlias is the user-defined provider alias surfaced by
	// opencode; empty otherwise.
	ProviderAlias string
	// ReasoningKind applies to OpReasoning ops: 'summary' | 'raw'.
	ReasoningKind string
	// ChildSessionNativeID applies to OpSession ops and references the
	// child's native id.
	ChildSessionNativeID string
	Extras               map[string]any
}

// EventKind implements Event.
func (OpStartedEvent) EventKind() EventKind { return EvOpStarted }

// OpFinalizedEvent records the terminal classification of an op together
// with per-op token / cost / payload-size accounting. Deltas only (the
// adapter converts cumulative source counters before emitting).
type OpFinalizedEvent struct {
	EventBase
	SessionNativeID  string
	TurnSeq          int
	Seq              int
	Status           string
	ErrorClass       string
	ErrorMessage     string
	EndTs            int64
	TokensIn         int64
	TokensOut        int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	CostUSD          float64
	BytesIn          int64
	BytesOut         int64
	// CharsIn / CharsOut are populated when the source records UTF-8
	// chars instead of bytes (ai-agent v2 tool accounting).
	CharsIn  int64
	CharsOut int64
	// CtxUsed and CtxMax are populated on OpLLM ops when the source
	// reports context-window utilization.
	CtxUsed int64
	CtxMax  int64
}

// EventKind implements Event.
func (OpFinalizedEvent) EventKind() EventKind { return EvOpFinalized }

// PayloadRefEvent records a pointer to a payload artifact on disk. The
// ingester writes a payload_refs row; ai-viewer never copies the
// underlying file.
type PayloadRefEvent struct {
	EventBase
	SessionNativeID string
	TurnSeq         int
	OpSeq           int
	// PayloadKind is one of: 'llm_request' | 'llm_response' |
	// 'llm_sdk_request' | 'llm_sdk_response' | 'llm_reasoning' |
	// 'tool_request' | 'tool_response' | 'log'.
	PayloadKind string
	// Format is one of: 'http' | 'sse' | 'json' | 'jsonrpc' | 'text' | 'binary'.
	Format string
	// Compression is 'gzip' or empty.
	Compression string
	// LocationURI references the source file using the 'file://<absolute-path>' scheme.
	LocationURI   string
	OriginalBytes int64
	StoredBytes   int64
	// SHA256 is hex-encoded; empty when the source does not provide a hash.
	SHA256 string
}

// EventKind implements Event.
func (PayloadRefEvent) EventKind() EventKind { return EvPayloadRef }

// LogEntryEvent surfaces a structured log line attached to a session,
// turn, or op. Powers the per-session Logs tab in the UI.
type LogEntryEvent struct {
	EventBase
	SessionNativeID string
	// TurnSeq is 0 when the log is not turn-scoped.
	TurnSeq int
	// OpSeq is 0 when the log is not op-scoped.
	OpSeq int
	// Severity is one of: 'DBG' | 'INF' | 'WRN' | 'ERR'.
	Severity string
	Source   string
	Message  string
	Extras   map[string]any
}

// EventKind implements Event.
func (LogEntryEvent) EventKind() EventKind { return EvLogEntry }

// SourceProgressEvent checkpoints the adapter's opaque cursor. The
// ingester persists this into sources.cursor so restarts resume in place.
type SourceProgressEvent struct {
	EventBase
	// Cursor is the adapter-specific opaque cursor encoded as JSON.
	Cursor string
}

// EventKind implements Event.
func (SourceProgressEvent) EventKind() EventKind { return EvSourceProgress }

// SourceErrorEvent surfaces a non-fatal parse error from an adapter.
// The ingester increments sources.parse_errors and writes a log_entries
// row with severity 'ERR' (session_id NULL, source_id set). Surfaced
// in /api/health.
type SourceErrorEvent struct {
	EventBase
	// File identifies the source file where the parse error occurred.
	File string
	// Offset is the byte offset within File, when meaningful (-1 otherwise).
	Offset int64
	// Message is the human-readable error message.
	Message string
}

// EventKind implements Event.
func (SourceErrorEvent) EventKind() EventKind { return EvSourceError }
