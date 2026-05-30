package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
)

// errEmptyData marks a message.data or part.data blob that is empty or
// whitespace-only. The columns are NOT NULL in opencode's schema, so an empty
// body is a corruption signal the caller surfaces as one structured error and
// skips, rather than decoding into a misleading zero value.
var errEmptyData = errors.New("opencode: empty data blob")

// This file defines the typed row structs for the four opencode tables and
// the discriminated JSON bodies carried in message.data and part.data. Only
// the load-bearing fields the later mapper consumes are decoded; every struct
// tolerates unknown sibling fields (encoding/json drops them) and unknown
// discriminator values (decode helpers return an "unknown" marker rather than
// erroring), so a newer opencode schema never hard-fails ingest
// (adapter-opencode.md §"Edge Cases" #1, #11; §"session_message").
//
// Times in these structs are opencode's native MILLISECONDS since the epoch.
// The mapper multiplies by 1000 to reach canonical microseconds; this file
// never converts (adapter-opencode.md §"Edge Cases" #6).

// sessionRow is one row of the session table. Columns added by later
// migrations (path/agent/model/cost/tokens_*/time_archived/...) are pointers
// or zero-valued so an older-schema row that lacks them decodes cleanly; the
// dynamic SELECT (schema.go) only ever names columns that exist.
type sessionRow struct {
	ID             string
	ProjectID      string
	ParentID       string // empty => root session; set => sub-agent
	Slug           string
	Directory      string // sensitive: working dir at session start
	Title          string // sensitive: operator-facing title
	Version        string // opencode CLI version that wrote the row
	Agent          string // agent name (e.g. "code-reviewer"); may be empty
	Model          []byte // raw JSON {"id","providerID","variant?"} or nil
	Cost           float64
	TokensInput    int64
	TokensOutput   int64
	TokensReason   int64
	TokensCacheRd  int64
	TokensCacheWr  int64
	TimeCreatedMs  int64
	TimeUpdatedMs  int64
	TimeArchivedMs int64 // 0 => not archived; set => SessionFinalized completed
	// TimeCompactingMs is non-zero WHILE a compaction is running on this session
	// (opencode sets session.time_compacting). The tailer PAUSES emitting a
	// session's tree while this is non-zero — compaction reshapes message/part rows,
	// so reading mid-compaction would emit partial/stale content. It re-emits once
	// the column clears (its time_updated bumps, re-surfacing the session in a later
	// delta) (adapter-opencode.md §"Edge Cases" #8; SOW-0005 round-2 P2-E).
	TimeCompactingMs int64
}

// messageRow is one row of the message table. data is the raw discriminated
// union (user | assistant) decoded by decodeMessageData.
type messageRow struct {
	ID            string
	SessionID     string
	TimeCreatedMs int64
	TimeUpdatedMs int64
	Data          []byte
}

// partRow is one row of the part table. data is the raw discriminated union
// (12 variants on $.type) decoded by decodePartData.
type partRow struct {
	ID            string
	MessageID     string
	SessionID     string
	TimeCreatedMs int64
	TimeUpdatedMs int64
	Data          []byte
}

// isStepFinish reports whether the part's body is a step-finish part, by peeking
// ONLY at its $.type discriminator (a cheap decode of one field, not the whole
// body). The turn-finalize predicate (turnIsTerminal) needs to know a message
// carries ≥1 step-finish part even when the part's tokens fail to decode, so this
// keys on type presence — not on the full partData decode succeeding. A malformed
// body yields false (it has no recognizable type).
func (p partRow) isStepFinish() bool {
	return peekPartType(p.Data) == partStepFinish
}

// peekPartType decodes ONLY the $.type field of a part.data blob, returning the
// classified partType (partUnknown for a malformed/typeless body). It is the
// minimal-cost type probe the turn-finalize predicate uses, avoiding a second
// full decodePartData on every part.
func peekPartType(raw []byte) partType {
	if len(bytes.TrimSpace(raw)) == 0 {
		return partUnknown
	}
	var d struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return partUnknown
	}
	return classifyPartType(d.Type)
}

// sessionMessageRow is one row of the session_message sidecar (agent/model
// switches). type is the discriminator; data is its raw body.
type sessionMessageRow struct {
	ID            string
	SessionID     string
	Type          string // "agent-switched" | "model-switched" | future
	TimeCreatedMs int64
	TimeUpdatedMs int64
	Data          []byte
}

// modelRef is the session.model JSON ({id, providerID, variant?}) and the
// assistant message's nested model object. Decoded best-effort; absent fields
// stay empty.
type modelRef struct {
	ID         string `json:"id"`
	ModelID    string `json:"modelID"` // assistant user-message variant
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant"`
}

// modelID returns the model identifier, preferring the session-style "id"
// field and falling back to the assistant user-message "modelID" field.
func (m modelRef) modelID() string {
	if m.ID != "" {
		return m.ID
	}
	return m.ModelID
}

// --- message.data discriminated union (role: user | assistant) ---------------

// messageRole classifies a decoded message.data body. roleUnknown is returned
// for any unrecognised role so the mapper can skip-with-WARN rather than crash
// (forward compatibility).
type messageRole string

const (
	roleUser      messageRole = "user"
	roleAssistant messageRole = "assistant"
	roleUnknown   messageRole = "unknown"
)

// tokenCounts is the nested tokens object on an assistant message and on a
// step-finish part. NOTE: on step-finish parts these values are CUMULATIVE
// within a message — the mapper computes per-op deltas (adapter-opencode.md
// §"Tool calls and Models", §"Canonical Model Gaps" #3). This struct only
// carries the values; the cumulative-vs-delta arithmetic lives in the mapper.
type tokenCounts struct {
	Total     int64       `json:"total"`
	Input     int64       `json:"input"`
	Output    int64       `json:"output"`
	Reasoning int64       `json:"reasoning"`
	Cache     cacheTokens `json:"cache"`
}

// cacheTokens is the nested cache read/write split opencode tracks and the
// canonical model now carries (SOW-0002).
type cacheTokens struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

// messageTime is the {created, completed?} block. Completed is a pointer so
// the mapper distinguishes "still running" (nil) from "completed at ms 0".
type messageTime struct {
	Created   int64  `json:"created"`
	Completed *int64 `json:"completed"`
}

// assistantError is the tagged AssistantError union. Only Name is load-bearing
// (it becomes the canonical ErrorClass when a session is finalized failed);
// the rest of the body stays in Raw for the mapper's Extras path.
type assistantError struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

// messageData is the decoded message.data body covering BOTH user and
// assistant variants. Unused-by-this-variant fields stay zero. The mapper
// reads Role first, then the fields relevant to that role. Unknown sibling
// keys are dropped by encoding/json.
type messageData struct {
	Role       string          `json:"role"`
	ParentID   string          `json:"parentID"` // assistant: the user msg that triggered the turn
	Agent      string          `json:"agent"`
	ModelID    string          `json:"modelID"`    // assistant
	ProviderID string          `json:"providerID"` // assistant (user-defined alias, e.g. "my-provider-alias")
	Mode       string          `json:"mode"`       // assistant (deprecated alias of agent)
	Cost       float64         `json:"cost"`       // assistant
	Tokens     tokenCounts     `json:"tokens"`     // assistant (turn rollup)
	Time       messageTime     `json:"time"`
	Finish     string          `json:"finish"` // assistant: "stop" | "tool-calls" | ...
	Model      *modelRef       `json:"model"`  // user-variant nested model object
	Error      *assistantError `json:"error"`  // assistant: failure marker
}

// role returns the typed role of the message, mapping unrecognised values to
// roleUnknown for forward-compatible skipping.
func (d messageData) role() messageRole {
	switch d.Role {
	case "user":
		return roleUser
	case "assistant":
		return roleAssistant
	default:
		return roleUnknown
	}
}

// decodeMessageData parses a message.data blob. A malformed body returns a
// zero messageData with role roleUnknown and the decode error, so callers can
// surface one structured error and skip the row rather than abort the table.
func decodeMessageData(raw []byte) (messageData, error) {
	var d messageData
	if len(bytes.TrimSpace(raw)) == 0 {
		return d, errEmptyData
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return messageData{}, err
	}
	return d, nil
}

// --- part.data discriminated union ($.type, 12 variants) ---------------------

// partType is the part.data $.type discriminator. partUnknown is returned for
// any value not in the known set, so the mapper skips-with-WARN.
type partType string

const (
	partStepStart  partType = "step-start"
	partStepFinish partType = "step-finish"
	partText       partType = "text"
	partReasoning  partType = "reasoning"
	partTool       partType = "tool"
	partPatch      partType = "patch"
	partCompaction partType = "compaction"
	partRetry      partType = "retry"
	partFile       partType = "file"
	partSnapshot   partType = "snapshot"
	partSubtask    partType = "subtask"
	partAgent      partType = "agent"
	partUnknown    partType = "unknown"
)

// knownPartTypes is the set of recognised part.data $.type values
// (adapter-opencode.md §"part" distribution table; message-v2.ts:352-378).
// A type absent here is forward-compatibility data: the mapper skips it with
// one structured WARN.
var knownPartTypes = map[partType]struct{}{
	partStepStart:  {},
	partStepFinish: {},
	partText:       {},
	partReasoning:  {},
	partTool:       {},
	partPatch:      {},
	partCompaction: {},
	partRetry:      {},
	partFile:       {},
	partSnapshot:   {},
	partSubtask:    {},
	partAgent:      {},
}

// classifyPartType returns the typed discriminator for a $.type string,
// mapping unknown values to partUnknown.
func classifyPartType(t string) partType {
	pt := partType(t)
	if _, ok := knownPartTypes[pt]; ok {
		return pt
	}
	return partUnknown
}

// partTimes is the {start, end?} block shared by reasoning parts and tool
// state. End is a pointer so the mapper distinguishes "still running" (nil)
// from "ended at ms 0".
type partTimes struct {
	Start int64  `json:"start"`
	End   *int64 `json:"end"`
}

// toolState is the part.data.state tagged union for a tool part
// (message-v2.ts:248-308). Only the load-bearing fields are typed; Input and
// Metadata stay raw for the mapper (bytes_in approximation, sub-agent
// sessionId extraction). Status is the discriminator
// (pending|running|completed|error).
type toolState struct {
	Status   string          `json:"status"`
	Input    json.RawMessage `json:"input"`
	Output   string          `json:"output"`
	Title    string          `json:"title"`
	Error    string          `json:"error"`
	Time     partTimes       `json:"time"`
	Metadata json.RawMessage `json:"metadata"`
}

// subAgentSessionID extracts state.metadata.sessionId — set on a tool part
// where tool=="task" to name the spawned child session (adapter-opencode.md
// §"Sub-Agent Linkage"). Returns "" when absent or malformed. Retained for the
// fuzz contract (it must never panic); callers that need to distinguish a
// malformed-but-present blob use subAgentSessionIDChecked.
func (s toolState) subAgentSessionID() string {
	id, _ := s.subAgentSessionIDChecked()
	return id
}

// subAgentSessionIDChecked extracts state.metadata.sessionId AND reports whether
// the metadata was PRESENT but failed to decode (malformed). An absent/null
// metadata yields ("", false) — nothing to warn about; a present-but-unparseable
// blob yields ("", true) so the caller can surface a structured WARN rather than
// silently dropping a possible sub-agent linkage (SOW-0005 P2.6).
func (s toolState) subAgentSessionIDChecked() (id string, malformed bool) {
	body := bytes.TrimSpace(s.Metadata)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return "", false
	}
	var m struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(body, &m) != nil {
		return "", true
	}
	return m.SessionID, false
}

// partData is the decoded part.data body covering every variant. Type is
// always set (classified); the remaining fields are populated only for the
// variants that carry them. Unknown sibling keys are dropped by
// encoding/json. The mapper switches on Type().
type partData struct {
	RawType string `json:"type"`
	// step-finish:
	Reason string      `json:"reason"`
	Cost   float64     `json:"cost"`
	Tokens tokenCounts `json:"tokens"`
	// text / reasoning:
	Text string    `json:"text"`
	Time partTimes `json:"time"`
	// tool:
	CallID string     `json:"callID"`
	Tool   string     `json:"tool"`
	State  *toolState `json:"state"`
	// patch:
	Hash  string   `json:"hash"`
	Files []string `json:"files"` // sensitive: absolute paths
	// compaction:
	Auto bool `json:"auto"`
	// retry:
	Attempt int `json:"attempt"`
	// file:
	MIME     string `json:"mime"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

// kind returns the typed discriminator for the part body.
func (d partData) kind() partType { return classifyPartType(d.RawType) }

// decodePartData parses a part.data blob. A malformed body returns a zero
// partData with kind partUnknown and the decode error, so callers surface one
// structured error and skip the row rather than abort the table.
func decodePartData(raw []byte) (partData, error) {
	var d partData
	if len(bytes.TrimSpace(raw)) == 0 {
		return d, errEmptyData
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return partData{}, err
	}
	return d, nil
}
