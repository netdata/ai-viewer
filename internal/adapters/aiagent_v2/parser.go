package aiagent_v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// snapshotVersionsSupported enumerates the v2 envelope versions the
// adapter accepts; out-of-range values are surfaced as parse errors.
// Per `adapter-aiagent-v2.md` only 1 and 2 occur in operator data.
var snapshotVersionsSupported = map[int]bool{1: true, 2: true}

// snapshot is the top-level v2 file envelope. The producer at
// `ai-agent.git/src/persistence.ts:53-57` writes exactly these three
// fields and nothing else.
type snapshot struct {
	Version int    `json:"version"`
	Reason  string `json:"reason,omitempty"`
	OpTree  opTree `json:"opTree"`
}

// opTree mirrors the SessionNode shape from
// `ai-agent.git/src/session-tree.ts:109-127`. Fields are optional except
// where the spec says otherwise; the parser tolerates absence.
type opTree struct {
	ID           string                     `json:"id,omitempty"`
	TraceID      string                     `json:"traceId,omitempty"`
	AgentID      string                     `json:"agentId,omitempty"`
	CallPath     string                     `json:"callPath,omitempty"`
	SessionTitle string                     `json:"sessionTitle,omitempty"`
	LatestStatus string                     `json:"latestStatus,omitempty"`
	StartedAt    int64                      `json:"startedAt,omitempty"`
	EndedAt      *int64                     `json:"endedAt,omitempty"`
	Success      *bool                      `json:"success,omitempty"`
	Error        string                     `json:"error,omitempty"`
	Attributes   map[string]json.RawMessage `json:"attributes,omitempty"`
	Totals       json.RawMessage            `json:"totals,omitempty"`
	Turns        []turnNode                 `json:"turns,omitempty"`
	Steps        []stepNode                 `json:"steps,omitempty"`
	FinalReport  json.RawMessage            `json:"finalReport,omitempty"`
	PluginMetas  json.RawMessage            `json:"pluginMetas,omitempty"`
}

// turnNode mirrors `session-tree.ts:82-89`. Index is 0-based per
// observed reality; spec doc was wrong.
type turnNode struct {
	ID         string                     `json:"id,omitempty"`
	Index      int                        `json:"index"`
	StartedAt  int64                      `json:"startedAt,omitempty"`
	EndedAt    *int64                     `json:"endedAt,omitempty"`
	Attributes map[string]json.RawMessage `json:"attributes,omitempty"`
	Ops        []operationNode            `json:"ops,omitempty"`
}

// stepNode mirrors `session-tree.ts:99-107`. v2-only; absent in v1.
type stepNode struct {
	ID         string                     `json:"id,omitempty"`
	Index      int                        `json:"index"`
	Kind       string                     `json:"kind,omitempty"`
	StartedAt  int64                      `json:"startedAt,omitempty"`
	EndedAt    *int64                     `json:"endedAt,omitempty"`
	Attributes map[string]json.RawMessage `json:"attributes,omitempty"`
	Ops        []operationNode            `json:"ops,omitempty"`
}

// operationNode mirrors `session-tree.ts:58-80`. childSession is a
// nested opTree fragment; the adapter recursively descends.
type operationNode struct {
	OpID                string                     `json:"opId,omitempty"`
	Kind                string                     `json:"kind,omitempty"`
	StartedAt           int64                      `json:"startedAt,omitempty"`
	EndedAt             *int64                     `json:"endedAt,omitempty"`
	Status              string                     `json:"status,omitempty"`
	Attributes          map[string]json.RawMessage `json:"attributes,omitempty"`
	Logs                []logEntry                 `json:"logs,omitempty"`
	Accounting          []accountingEntry          `json:"accounting,omitempty"`
	Reasoning           *reasoning                 `json:"reasoning,omitempty"`
	Request             *opPayload                 `json:"request,omitempty"`
	Response            *opPayload                 `json:"response,omitempty"`
	ChildSession        *opTree                    `json:"childSession,omitempty"`
	ChildSessionRef     *childSessionRef           `json:"childSessionRef,omitempty"`
	ChildSessionSummary json.RawMessage            `json:"childSessionSummary,omitempty"`
}

// logEntry mirrors observed v2 log shape; only the canonical-relevant
// keys are typed.
type logEntry struct {
	Timestamp int64  `json:"timestamp,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Message   string `json:"message,omitempty"`
	Path      string `json:"path,omitempty"`
}

// accountingEntry mirrors observed v2 accounting shape.
type accountingEntry struct {
	Type          string  `json:"type,omitempty"`
	Provider      string  `json:"provider,omitempty"`
	Model         string  `json:"model,omitempty"`
	Tokens        *tokens `json:"tokens,omitempty"`
	CostUSD       float64 `json:"costUsd,omitempty"`
	StopReason    string  `json:"stopReason,omitempty"`
	Latency       int64   `json:"latency,omitempty"`
	Status        string  `json:"status,omitempty"`
	Command       string  `json:"command,omitempty"`
	MCPServer     string  `json:"mcpServer,omitempty"`
	CharactersIn  int64   `json:"charactersIn,omitempty"`
	CharactersOut int64   `json:"charactersOut,omitempty"`
	Error         string  `json:"error,omitempty"`
	Timestamp     int64   `json:"timestamp,omitempty"`
}

// tokens accepts both Anthropic (cache-read/write) and OpenAI (cached)
// naming conventions per `adapter-aiagent-v2.md` §AccountingEntry.
type tokens struct {
	InputTokens           int64 `json:"inputTokens,omitempty"`
	OutputTokens          int64 `json:"outputTokens,omitempty"`
	CacheReadInputTokens  int64 `json:"cacheReadInputTokens,omitempty"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens,omitempty"`
	CachedTokens          int64 `json:"cachedTokens,omitempty"`
	TotalTokens           int64 `json:"totalTokens,omitempty"`
}

// reasoning mirrors `op.reasoning` shape; `final` is preferred over
// legacy `chunks` per `ai-agent.git/.agents/sow/specs/snapshots.md`.
type reasoning struct {
	Chunks     []reasoningChunk `json:"chunks,omitempty"`
	Final      string           `json:"final,omitempty"`
	ChunkCount int              `json:"chunkCount,omitempty"`
	CharCount  int              `json:"charCount,omitempty"`
}

type reasoningChunk struct {
	Text string `json:"text,omitempty"`
	Ts   int64  `json:"ts,omitempty"`
}

// opPayload is shared by request/response. `payload` is left raw so
// the adapter can inspect for `{ref: ...}` cheaply and otherwise
// discard the body without rebuilding it.
type opPayload struct {
	Kind      string          `json:"kind,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Size      int64           `json:"size,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

// childSessionRef is the lightweight reference variant of childSession
// emitted when the producer was on the v3 evidence path but still
// writing a v2 snapshot. Observed in 0/50 random samples; supported for
// correctness only.
type childSessionRef struct {
	SessionID string `json:"sessionId,omitempty"`
	OriginID  string `json:"originId,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	CallPath  string `json:"callPath,omitempty"`
	Status    string `json:"status,omitempty"`
}

// errMalformedSnapshot is returned by parseSnapshot when the bytes
// decode but the top-level envelope is missing required fields.
var errMalformedSnapshot = errors.New("aiagent_v2: malformed snapshot envelope")

// parseSnapshot decodes the full decompressed JSON byte slice into a
// snapshot. Validates that `version` is supported and that an opTree is
// present. Returns errMalformedSnapshot when the envelope is invalid.
func parseSnapshot(data []byte) (snapshot, error) {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return snapshot{}, fmt.Errorf("aiagent_v2: decode snapshot: %w", err)
	}
	if err := validateSnapshot(snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

// parseSnapshotStream reads JSON from r into a snapshot via a streaming
// json.Decoder so very large files do not need to be fully buffered
// before decoding. The decoder still builds an in-memory tree; the
// streamer in streamer.go is used for files where even that is too
// large.
func parseSnapshotStream(r io.Reader) (snapshot, error) {
	dec := json.NewDecoder(r)
	var snap snapshot
	if err := dec.Decode(&snap); err != nil {
		return snapshot{}, fmt.Errorf("aiagent_v2: decode snapshot stream: %w", err)
	}
	if err := validateSnapshot(snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

// validateSnapshot enforces the minimum invariants the spec promises
// for v2 envelopes. Per `adapter-aiagent-v2.md` §Snapshot Shape, every
// snapshot has `{version, reason, opTree}`; the parser tolerates
// missing `reason` because tests in the wild omit it.
func validateSnapshot(snap snapshot) error {
	if snap.Version == 0 {
		return fmt.Errorf("%w: missing version", errMalformedSnapshot)
	}
	if !snapshotVersionsSupported[snap.Version] {
		return fmt.Errorf("%w: unsupported version %d", errMalformedSnapshot, snap.Version)
	}
	if snap.OpTree.TraceID == "" {
		return fmt.Errorf("%w: opTree.traceId is required", errMalformedSnapshot)
	}
	return nil
}
