package parity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	aiAgentV3Format        = "aiagent_v3"
	aiAgentV3SourceLineMax = 4 * 1024 * 1024
)

// AIAgentV3SourceOptions configures source-manifest extraction for ai-agent v3 ledgers.
type AIAgentV3SourceOptions struct {
	Root     string
	SourceID string
}

// ExtractAIAgentV3Source builds source artifacts directly from ai-agent v3 JSONL ledgers.
func ExtractAIAgentV3Source(ctx context.Context, opts AIAgentV3SourceOptions) ([]Artifact, error) {
	var artifacts []Artifact
	err := ExtractAIAgentV3SourceToWriter(ctx, opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}))
	return artifacts, err
}

// ExtractAIAgentV3SourceToWriter streams source artifacts directly from ai-agent v3 JSONL ledgers.
func ExtractAIAgentV3SourceToWriter(ctx context.Context, opts AIAgentV3SourceOptions, writer ArtifactWriter) error {
	if opts.Root == "" {
		return fmt.Errorf("extract aiagent_v3 source: missing root")
	}
	if writer == nil {
		return fmt.Errorf("extract aiagent_v3 source: nil artifact writer")
	}
	sourceID := opts.SourceID
	if sourceID == "" {
		sourceID = "aiagent_v3:" + opts.Root
	}

	names, err := listAIAgentV3Ledgers(opts.Root)
	if err != nil {
		return fmt.Errorf("extract aiagent_v3 source: %w", err)
	}
	syntheticSessions := newAIAgentV3SyntheticSessionTracker(sourceID)
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(opts.Root))
	if err != nil && len(names) > 0 {
		return fmt.Errorf("extract aiagent_v3 source: resolve root %q: %w", opts.Root, err)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(opts.Root, "session", name)
		resolvedPath, ok, err := aiAgentV3SourcePathWithinRoot(resolvedRoot, path)
		if err != nil {
			return fmt.Errorf("extract aiagent_v3 source: %w", err)
		}
		if !ok {
			return fmt.Errorf("extract aiagent_v3 source: %s resolves to %s outside the sessions root; skipping (symlink escape)", path, resolvedPath)
		}
		if err := writeAIAgentV3SourceFile(ctx, opts.Root, resolvedPath, sourceID, syntheticSessions, writer); err != nil {
			return err
		}
	}
	return syntheticSessions.writeSessionArtifacts(ctx, writer)
}

func listAIAgentV3Ledgers(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "session"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".jsonl") && !strings.Contains(name, ".tmp-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func aiAgentV3SourcePathWithinRoot(resolvedRoot string, path string) (string, bool, error) {
	resolvedPath, err := evalSymlinksAllowingTail(filepath.Clean(path))
	if err != nil {
		return "", false, fmt.Errorf("resolve aiagent_v3 source path %q: %w", path, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return "", false, fmt.Errorf("relative aiagent_v3 source path %q under %q: %w", resolvedPath, resolvedRoot, err)
	}
	ok := rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	return resolvedPath, ok, nil
}

type aiAgentV3SourceRecord struct {
	Version         int                        `json:"version"`
	RecordType      string                     `json:"recordType"`
	Seq             uint64                     `json:"seq"`
	Ts              string                     `json:"ts"`
	OriginID        string                     `json:"originId"`
	SessionID       string                     `json:"sessionId"`
	AgentID         string                     `json:"agentId,omitempty"`
	CallPath        string                     `json:"callPath,omitempty"`
	ParentSessionID string                     `json:"parentSessionId,omitempty"`
	ParentOpID      string                     `json:"parentOpId,omitempty"`
	HeadendID       string                     `json:"headendId,omitempty"`
	CapturePayloads bool                       `json:"capturePayloads"`
	Attributes      map[string]json.RawMessage `json:"attributes,omitempty"`
	Turn            int                        `json:"turn,omitempty"`
	Status          string                     `json:"status,omitempty"`
	Ops             []aiAgentV3SourceOp        `json:"ops,omitempty"`
	Warnings        []string                   `json:"warnings,omitempty"`
	Errors          []string                   `json:"errors,omitempty"`
	ChildSessions   []aiAgentV3ChildSession    `json:"childSessions,omitempty"`
	Error           string                     `json:"error,omitempty"`
}

type aiAgentV3SourceOp struct {
	OpID          string                     `json:"opId"`
	OpIndex       int                        `json:"opIndex"`
	Kind          string                     `json:"kind"`
	Status        string                     `json:"status"`
	StartedAt     string                     `json:"startedAt,omitempty"`
	EndedAt       string                     `json:"endedAt,omitempty"`
	Name          string                     `json:"name,omitempty"`
	Provider      string                     `json:"provider,omitempty"`
	PayloadRefs   []aiAgentV3PayloadRef      `json:"payloadRefs,omitempty"`
	ChildSessions []aiAgentV3ChildSession    `json:"childSessions,omitempty"`
	Attributes    map[string]json.RawMessage `json:"attributes,omitempty"`
	Error         string                     `json:"error,omitempty"`
}

type aiAgentV3PayloadRef struct {
	Kind            string `json:"kind"`
	OpID            string `json:"opId"`
	Turn            int    `json:"turn"`
	OpIndex         int    `json:"opIndex"`
	Format          string `json:"format"`
	Compression     string `json:"compression,omitempty"`
	Path            string `json:"path,omitempty"`
	OriginalBytes   *int64 `json:"originalBytes,omitempty"`
	CompressedBytes *int64 `json:"compressedBytes,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	Captured        bool   `json:"captured"`
	Truncated       bool   `json:"truncated"`
	Redacted        bool   `json:"redacted"`
}

type aiAgentV3ChildSession struct {
	SessionID       string `json:"sessionId"`
	OriginID        string `json:"originId"`
	ParentSessionID string `json:"parentSessionId"`
	ParentOpID      string `json:"parentOpId"`
	LedgerPath      string `json:"ledgerPath"`
	Status          string `json:"status"`
}

type aiAgentV3SourceState struct {
	root                  string
	sourceID              string
	sourceFile            string
	nativeSessionID       string
	rootNativeSessionID   string
	parentNativeSessionID string
	parentOpID            string
	agentID               string
	callPath              string
	headendID             string
	capturePayloads       bool
	sessionAttributes     map[string]json.RawMessage
	sessionKind           string
	sessionStarted        bool
	sessionStartedAt      int64
	sessionLineNo         int64
	sessionStatus         string
	sessionEndedAt        *int64
	turns                 map[int]*aiAgentV3SourceTurn
	syntheticSessions     *aiAgentV3SyntheticSessionTracker
}

type aiAgentV3SourceTurn struct {
	seq       int64
	startedAt int64
	lineNo    int64
	status    string
	endedAt   *int64
	finalized bool
}

func newAIAgentV3SourceState(root string, sourceID string, sourceFile string) aiAgentV3SourceState {
	return aiAgentV3SourceState{
		root:          root,
		sourceID:      sourceID,
		sourceFile:    sourceFile,
		sessionStatus: "running",
		turns:         map[int]*aiAgentV3SourceTurn{},
	}
}

func writeAIAgentV3SourceFile(ctx context.Context, root string, path string, sourceID string, syntheticSessions *aiAgentV3SyntheticSessionTracker, writer ArtifactWriter) error {
	file, err := os.Open(path) // #nosec G304 -- path is resolved and containment-checked by aiAgentV3SourcePathWithinRoot before extraction.
	if err != nil {
		return fmt.Errorf("open aiagent_v3 source file read-only: %w", err)
	}
	defer func() { _ = file.Close() }()

	state := newAIAgentV3SourceState(root, sourceID, path)
	state.syntheticSessions = syntheticSessions
	reader := bufio.NewReader(file)
	var lineNo int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := readAIAgentV3SourceLine(reader)
		if readErr != nil && len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("read aiagent_v3 source line: %w", readErr)
		}
		lineNo++
		line = trimLineEnding(line)
		lineArtifacts, err := extractAIAgentV3LineArtifacts(&state, line, lineNo)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if err := writeArtifacts(ctx, writer, lineArtifacts); err != nil {
			return fmt.Errorf("%s:%d: write artifact: %w", path, lineNo, err)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	eofArtifacts, err := state.finalizeAIAgentV3AtEOF()
	if err != nil {
		return err
	}
	if err := writeArtifacts(ctx, writer, eofArtifacts); err != nil {
		return fmt.Errorf("%s: write eof artifact: %w", path, err)
	}
	return nil
}

func writeArtifacts(ctx context.Context, writer ArtifactWriter, artifacts []Artifact) error {
	for _, artifact := range artifacts {
		if err := writer.WriteArtifact(ctx, artifact); err != nil {
			return err
		}
	}
	return nil
}

func readAIAgentV3SourceLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > aiAgentV3SourceLineMax {
			return nil, fmt.Errorf("line exceeds %d bytes", aiAgentV3SourceLineMax)
		}
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func extractAIAgentV3LineArtifacts(state *aiAgentV3SourceState, line []byte, lineNo int64) ([]Artifact, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var rec aiAgentV3SourceRecord
	if err := decodeJSON(trimmed, &rec); err != nil {
		return nil, fmt.Errorf("decode record: %w", err)
	}
	if err := validateAIAgentV3SourceRecord(rec); err != nil {
		return nil, err
	}
	tsUs, err := parseAIAgentV3Timestamp(rec.Ts)
	if err != nil {
		return nil, err
	}

	switch rec.RecordType {
	case "session_start":
		state.recordAIAgentV3SessionStart(rec, tsUs, lineNo)
		return nil, nil
	case "turn_start":
		state.recordAIAgentV3TurnStart(rec, tsUs, lineNo)
		return nil, nil
	case "turn_end":
		return state.recordAIAgentV3TurnEnd(rec, tsUs, lineNo)
	case "session_summary":
		state.recordAIAgentV3SessionSummary(rec, tsUs, lineNo)
		if rec.Status == "failed" && rec.Error != "" {
			return []Artifact{state.aiAgentV3SessionLogArtifact(rec, "/error", rec.Error)}, nil
		}
		return nil, nil
	case "session_error":
		state.recordAIAgentV3SessionError(rec, tsUs)
		if rec.Error != "" {
			return []Artifact{state.aiAgentV3SessionLogArtifact(rec, "/error", rec.Error)}, nil
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown aiagent_v3 record type %q", rec.RecordType)
	}
}

func validateAIAgentV3SourceRecord(rec aiAgentV3SourceRecord) error {
	if rec.Version != 3 {
		return fmt.Errorf("version=%d not supported (want 3)", rec.Version)
	}
	if rec.RecordType == "" {
		return fmt.Errorf("recordType is required")
	}
	if rec.Seq < 1 {
		return fmt.Errorf("seq must be >= 1")
	}
	if rec.Ts == "" {
		return fmt.Errorf("ts is required")
	}
	if rec.OriginID == "" {
		return fmt.Errorf("originId is required")
	}
	if rec.SessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	return nil
}

func parseAIAgentV3Timestamp(timestamp string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return 0, fmt.Errorf("parse timestamp %q: %w", timestamp, err)
	}
	return parsed.UnixMicro(), nil
}

func (s *aiAgentV3SourceState) recordAIAgentV3SessionStart(rec aiAgentV3SourceRecord, tsUs int64, lineNo int64) {
	s.sessionStarted = true
	s.sessionStartedAt = tsUs
	s.sessionLineNo = lineNo
	s.nativeSessionID = rec.SessionID
	s.rootNativeSessionID = rec.OriginID
	if s.rootNativeSessionID == "" {
		s.rootNativeSessionID = rec.SessionID
	}
	s.parentNativeSessionID = rec.ParentSessionID
	s.parentOpID = rec.ParentOpID
	s.agentID = rec.AgentID
	s.callPath = rec.CallPath
	s.headendID = rec.HeadendID
	s.capturePayloads = rec.CapturePayloads
	s.sessionAttributes = rec.Attributes
	s.sessionKind = aiAgentV3SessionKind(rec.HeadendID)
	s.sessionStatus = "running"
}

func (s *aiAgentV3SourceState) recordAIAgentV3TurnStart(rec aiAgentV3SourceRecord, tsUs int64, lineNo int64) {
	turn := s.ensureAIAgentV3Turn(rec.Turn, tsUs, lineNo)
	turn.startedAt = tsUs
	turn.lineNo = lineNo
}

func (s *aiAgentV3SourceState) recordAIAgentV3SessionSummary(rec aiAgentV3SourceRecord, tsUs int64, lineNo int64) {
	s.nativeSessionID = rec.SessionID
	s.rootNativeSessionID = rec.OriginID
	s.sessionStatus = aiAgentV3SessionStatus(rec.Status)
	s.sessionEndedAt = ptrInt64(tsUs)
	if s.syntheticSessions != nil {
		for _, child := range rec.ChildSessions {
			s.syntheticSessions.noteChild(rec.SessionID, rec.OriginID, child, tsUs, s.sourceFile, lineNo)
		}
	}
}

func (s *aiAgentV3SourceState) recordAIAgentV3SessionError(rec aiAgentV3SourceRecord, tsUs int64) {
	s.nativeSessionID = rec.SessionID
	s.rootNativeSessionID = rec.OriginID
	s.sessionStatus = "failed"
	s.sessionEndedAt = ptrInt64(tsUs)
}

func (s *aiAgentV3SourceState) ensureAIAgentV3Turn(turnNo int, tsUs int64, lineNo int64) *aiAgentV3SourceTurn {
	if turnNo < 1 {
		turnNo = 1
	}
	if turn, ok := s.turns[turnNo]; ok {
		return turn
	}
	turn := &aiAgentV3SourceTurn{
		seq:       int64(turnNo),
		startedAt: tsUs,
		lineNo:    lineNo,
		status:    "running",
	}
	s.turns[turnNo] = turn
	return turn
}

func (s *aiAgentV3SourceState) sessionID() string {
	if s.nativeSessionID != "" {
		return s.nativeSessionID
	}
	return "source:" + s.sourceID
}

func aiAgentV3SessionKind(headend string) string {
	switch headend {
	case "cli", "api", "web", "embed", "slack":
		return "root"
	case "tool_output":
		return "tool_internal"
	default:
		return "sub_agent"
	}
}

func aiAgentV3SessionStatus(status string) string {
	if status == "failed" {
		return "failed"
	}
	return "completed"
}

func mapAIAgentV3Status(status string) string {
	switch status {
	case "ok":
		return "completed"
	case "failed":
		return "failed"
	case "running":
		return "running"
	default:
		return status
	}
}
