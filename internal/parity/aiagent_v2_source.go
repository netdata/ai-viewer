package parity

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	aiAgentV2Format          = "aiagent_v2"
	aiAgentV2SnapshotExt     = ".json.gz"
	aiAgentV2TempMarker      = ".tmp-"
	aiAgentV2StepIndexOffset = 10000
	aiAgentV2MaxChildDepth   = 32
)

// AIAgentV2SourceOptions configures aiagent_v2 source-manifest extraction.
type AIAgentV2SourceOptions struct {
	Root     string
	SourceID string
}

// ExtractAIAgentV2Source reads root-level v2 snapshots directly and emits a
// source-native parity manifest. It does not call the aiagent_v2 mapper.
func ExtractAIAgentV2Source(ctx context.Context, opts AIAgentV2SourceOptions) ([]Artifact, error) {
	var artifacts []Artifact
	err := ExtractAIAgentV2SourceToWriter(ctx, opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}))
	return artifacts, err
}

// ExtractAIAgentV2SourceToWriter streams source-native parity artifacts from
// root-level v2 snapshots. It does not call the aiagent_v2 mapper.
func ExtractAIAgentV2SourceToWriter(ctx context.Context, opts AIAgentV2SourceOptions, writer ArtifactWriter) error {
	if writer == nil {
		return fmt.Errorf("extract aiagent_v2 source: nil artifact writer")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return fmt.Errorf("resolve aiagent_v2 root %q: %w", opts.Root, err)
	}
	if opts.SourceID == "" {
		opts.SourceID = aiAgentV2Format + ":" + root
	}
	names, err := listAIAgentV2SourceSnapshots(root)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		fileArtifacts, err := extractAIAgentV2SourceFile(root, opts.SourceID, name)
		if err != nil {
			return err
		}
		if err := writeArtifacts(ctx, writer, fileArtifacts); err != nil {
			return fmt.Errorf("%s: write artifact: %w", name, err)
		}
	}
	return nil
}

func listAIAgentV2SourceSnapshots(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read aiagent_v2 root %s: %w", root, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, aiAgentV2SnapshotExt) || strings.Contains(name, aiAgentV2TempMarker) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func extractAIAgentV2SourceFile(root string, sourceID string, name string) ([]Artifact, error) {
	path := filepath.Join(root, name)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat aiagent_v2 snapshot %s: %w", path, err)
	}
	if info.Size() > canonicalPayloadArtifactMaxBytes {
		return nil, payloadCapError{label: "aiagent_v2 snapshot", maxBytes: canonicalPayloadArtifactMaxBytes}
	}
	if info.Size() == 0 {
		return []Artifact{aiAgentV2SourceCorruptionArtifact(sourceID, path, name, nil, "zero_bytes")}, nil
	}
	snapshot, err := readAIAgentV2SourceSnapshot(path)
	if err != nil {
		if isPayloadCapError(err) {
			return nil, err
		}
		raw, readErr := readFileSelectorWithLimit(path, "", canonicalPayloadArtifactMaxBytes)
		if readErr != nil {
			return nil, fmt.Errorf("read corrupt aiagent_v2 snapshot bytes %s: %w", path, readErr)
		}
		return []Artifact{aiAgentV2SourceCorruptionArtifact(sourceID, path, name, raw, aiAgentV2SnapshotCorruptionActual(err))}, nil
	}
	originID := strings.TrimSuffix(name, aiAgentV2SnapshotExt)
	state := aiAgentV2SourceState{
		root:       root,
		sourceID:   sourceID,
		sourceFile: path,
		originID:   originID,
		rootTrace:  snapshot.OpTree.TraceID,
		rootTs:     aiAgentV2TimestampUS(snapshot.OpTree.StartedAt),
		version:    snapshot.Version,
	}
	if err := state.recordSession(snapshot.OpTree, aiAgentV2SessionVisit{
		kind:        "root",
		jsonPointer: "/opTree",
	}); err != nil {
		return nil, err
	}
	return state.artifacts, nil
}

func aiAgentV2SourceCorruptionArtifact(sourceID string, path string, name string, raw []byte, actual string) Artifact {
	sum := sha256.Sum256(raw)
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          aiAgentV2Format,
		SourceID:         sourceID,
		SourceFile:       path,
		NativeSessionID:  strings.TrimSuffix(name, aiAgentV2SnapshotExt),
		NativeArtifactID: fmt.Sprintf("source_corruption:file:%s:snapshot", filepath.Base(path)),
		Class:            ClassSourceCorruption,
		Availability:     AvailabilitySourceCorrupt,
		HashDomain:       HashRawBytes,
		Selector: Selector{
			URI: (&url.URL{Scheme: "file", Path: path}).String(),
		},
		Bytes:          int64(len(raw)),
		Chars:          -1,
		ComputedSHA256: fmt.Sprintf("%x", sum),
		IntegrityFailures: []IntegrityFailure{{
			Field:    "snapshot",
			Expected: "valid aiagent_v2 gzip JSON snapshot",
			Actual:   actual,
		}},
	}
}

func aiAgentV2SnapshotCorruptionActual(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "open aiagent_v2 gzip"):
		return "gzip_error"
	case strings.Contains(msg, "decode aiagent_v2 snapshot"):
		return "decode_error"
	case strings.Contains(msg, "malformed aiagent_v2 snapshot"):
		return "schema_error"
	default:
		return "read_error"
	}
}

func readAIAgentV2SourceSnapshot(path string) (aiAgentV2Snapshot, error) {
	return readAIAgentV2SourceSnapshotWithLimit(path, canonicalPayloadArtifactMaxBytes)
}

func readAIAgentV2SourceSnapshotWithLimit(path string, maxBytes int64) (aiAgentV2Snapshot, error) {
	file, err := os.Open(path) // #nosec G304 -- caller gets paths from the configured source root.
	if err != nil {
		return aiAgentV2Snapshot{}, fmt.Errorf("open aiagent_v2 snapshot read-only: %w", err)
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err == nil && info.Mode().IsRegular() && info.Size() > maxBytes {
		return aiAgentV2Snapshot{}, payloadCapError{label: "aiagent_v2 snapshot", maxBytes: maxBytes}
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		return aiAgentV2Snapshot{}, fmt.Errorf("open aiagent_v2 gzip %s: %w", path, err)
	}
	defer func() { _ = reader.Close() }()
	body, err := readAllWithLimit(reader, maxBytes, "decompressed aiagent_v2 snapshot")
	if err != nil {
		return aiAgentV2Snapshot{}, fmt.Errorf("read aiagent_v2 gzip %s: %w", path, err)
	}
	var snapshot aiAgentV2Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return aiAgentV2Snapshot{}, fmt.Errorf("decode aiagent_v2 snapshot %s: %w", path, err)
	}
	if snapshot.Version == 0 || snapshot.OpTree.TraceID == "" {
		return aiAgentV2Snapshot{}, fmt.Errorf("malformed aiagent_v2 snapshot %s", path)
	}
	return snapshot, nil
}

type aiAgentV2SourceState struct {
	root       string
	sourceID   string
	sourceFile string
	originID   string
	rootTrace  string
	rootTs     int64
	version    int
	artifacts  []Artifact
}

type aiAgentV2Snapshot struct {
	Version int             `json:"version"`
	Reason  string          `json:"reason,omitempty"`
	OpTree  aiAgentV2OpTree `json:"opTree"`
}

type aiAgentV2OpTree struct {
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
	FinalReport  json.RawMessage            `json:"finalReport,omitempty"`
	Totals       json.RawMessage            `json:"totals,omitempty"`
	PluginMetas  json.RawMessage            `json:"pluginMetas,omitempty"`
	Turns        []aiAgentV2Turn            `json:"turns,omitempty"`
	Steps        []aiAgentV2Step            `json:"steps,omitempty"`
}

type aiAgentV2Turn struct {
	ID        string               `json:"id,omitempty"`
	Index     int                  `json:"index"`
	StartedAt int64                `json:"startedAt,omitempty"`
	EndedAt   *int64               `json:"endedAt,omitempty"`
	Ops       []aiAgentV2Operation `json:"ops,omitempty"`
}

type aiAgentV2Step struct {
	ID         string                     `json:"id,omitempty"`
	Index      int                        `json:"index"`
	Kind       string                     `json:"kind,omitempty"`
	StartedAt  int64                      `json:"startedAt,omitempty"`
	EndedAt    *int64                     `json:"endedAt,omitempty"`
	Attributes map[string]json.RawMessage `json:"attributes,omitempty"`
	Ops        []aiAgentV2Operation       `json:"ops,omitempty"`
}

type aiAgentV2Operation struct {
	OpID                string                     `json:"opId,omitempty"`
	Kind                string                     `json:"kind,omitempty"`
	StartedAt           int64                      `json:"startedAt,omitempty"`
	EndedAt             *int64                     `json:"endedAt,omitempty"`
	Status              string                     `json:"status,omitempty"`
	Attributes          map[string]json.RawMessage `json:"attributes,omitempty"`
	Logs                []aiAgentV2LogEntry        `json:"logs,omitempty"`
	Reasoning           *aiAgentV2Reasoning        `json:"reasoning,omitempty"`
	Request             *aiAgentV2OpPayload        `json:"request,omitempty"`
	Response            *aiAgentV2OpPayload        `json:"response,omitempty"`
	ChildSession        *aiAgentV2OpTree           `json:"childSession,omitempty"`
	ChildSessionRef     *aiAgentV2ChildSessionRef  `json:"childSessionRef,omitempty"`
	ChildSessionSummary json.RawMessage            `json:"childSessionSummary,omitempty"`
}

type aiAgentV2LogEntry struct {
	Timestamp int64  `json:"timestamp,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Message   string `json:"message,omitempty"`
	Path      string `json:"path,omitempty"`
}

type aiAgentV2Reasoning struct {
	Final string `json:"final,omitempty"`
}

type aiAgentV2OpPayload struct {
	Kind      string          `json:"kind,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Size      int64           `json:"size,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

type aiAgentV2ChildSessionRef struct {
	SessionID string `json:"sessionId,omitempty"`
}
