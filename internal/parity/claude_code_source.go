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
	claudeCodeFormat        = "claude-code"
	claudeCodeSourceLineMax = 8 * 1024 * 1024
)

// ClaudeCodeSourceOptions configures source-manifest extraction for claude-code transcripts.
type ClaudeCodeSourceOptions struct {
	Root     string
	SourceID string
}

// ExtractClaudeCodeSource builds source artifacts directly from claude-code JSONL transcripts.
func ExtractClaudeCodeSource(ctx context.Context, opts ClaudeCodeSourceOptions) ([]Artifact, error) {
	var artifacts []Artifact
	err := ExtractClaudeCodeSourceToWriter(ctx, opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}))
	return artifacts, err
}

// ExtractClaudeCodeSourceToWriter streams source artifacts directly from claude-code JSONL transcripts.
func ExtractClaudeCodeSourceToWriter(ctx context.Context, opts ClaudeCodeSourceOptions, writer ArtifactWriter) error {
	if opts.Root == "" {
		return fmt.Errorf("extract claude-code source: missing root")
	}
	if writer == nil {
		return fmt.Errorf("extract claude-code source: nil artifact writer")
	}
	sourceID := opts.SourceID
	if sourceID == "" {
		sourceID = "claude-code:" + opts.Root
	}
	transcripts, err := listClaudeCodeTranscripts(opts.Root)
	if err != nil {
		return fmt.Errorf("extract claude-code source: %w", err)
	}
	sourceContext, err := buildClaudeCodeSourceContext(opts.Root, transcripts)
	if err != nil {
		return fmt.Errorf("extract claude-code source context: %w", err)
	}
	coalescer := newClaudeCodeBoundaryCoalescer(writer)
	for _, transcript := range transcripts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeClaudeCodeSourceFile(ctx, sourceID, transcript, sourceContext, coalescer); err != nil {
			return err
		}
	}
	return coalescer.flush(ctx)
}

type claudeCodeTranscript struct {
	path                  string
	nativeSessionID       string
	parentNativeSessionID string
	rootNativeSessionID   string
	kind                  string
	agentID               string
}

func listClaudeCodeTranscripts(root string) ([]claudeCodeTranscript, error) {
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve root %q: %w", root, err)
	}
	var transcripts []claudeCodeTranscript
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") || strings.Contains(entry.Name(), ".tmp-") {
			return nil
		}
		if !claudeCodePathWithinRoot(resolvedRoot, path) {
			return fmt.Errorf("transcript %q resolves outside root %q", path, root)
		}
		transcript, ok := claudeCodeTranscriptForPath(root, path)
		if ok {
			transcripts = append(transcripts, transcript)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(transcripts, func(i, j int) bool { return transcripts[i].path < transcripts[j].path })
	return transcripts, nil
}

func claudeCodePathWithinRoot(resolvedRoot string, path string) bool {
	_, ok, err := claudeCodeResolveWithinRoot(resolvedRoot, path)
	return err == nil && ok
}

func claudeCodeResolveWithinRoot(resolvedRoot string, path string) (string, bool, error) {
	resolvedPath, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return "", false, err
	}
	ok := rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	return resolvedPath, ok, nil
}

func claudeCodeTranscriptForPath(root string, path string) (claudeCodeTranscript, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return claudeCodeTranscript{}, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return claudeCodeTranscript{}, false
	}
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if strings.HasPrefix(base, "agent-") {
		agentID := strings.TrimPrefix(base, "agent-")
		parentSessionID := claudeCodeParentSessionID(parts)
		if parentSessionID == "" {
			return claudeCodeTranscript{}, false
		}
		nativeID := parentSessionID + ":agent:" + agentID
		return claudeCodeTranscript{
			path:                  path,
			nativeSessionID:       nativeID,
			parentNativeSessionID: parentSessionID,
			rootNativeSessionID:   parentSessionID,
			kind:                  "sub_agent",
			agentID:               agentID,
		}, true
	}
	if claudeCodePathUnderSubagents(parts) {
		return claudeCodeTranscript{}, false
	}
	return claudeCodeTranscript{
		path:                path,
		nativeSessionID:     base,
		rootNativeSessionID: base,
		kind:                "root",
	}, true
}

func claudeCodePathUnderSubagents(parts []string) bool {
	for i, part := range parts {
		if part == "subagents" && i > 0 {
			return true
		}
	}
	return false
}

func claudeCodeParentSessionID(parts []string) string {
	for i, part := range parts {
		if part == "subagents" && i > 0 {
			return parts[i-1]
		}
	}
	return ""
}

type claudeCodeSourceRecord struct {
	Type             string                   `json:"type"`
	Subtype          string                   `json:"subtype"`
	SessionID        string                   `json:"sessionId"`
	Timestamp        string                   `json:"timestamp"`
	IsMeta           *bool                    `json:"isMeta"`
	IsCompactSummary *bool                    `json:"isCompactSummary"`
	Message          json.RawMessage          `json:"message"`
	Content          string                   `json:"content"`
	APIError         json.RawMessage          `json:"error"`
	ToolUseResult    json.RawMessage          `json:"toolUseResult"`
	CompactMetadata  *claudeCodeCompactMeta   `json:"compactMetadata"`
	DurationMs       *int64                   `json:"durationMs"`
	LastPrompt       string                   `json:"lastPrompt"`
	AITitle          string                   `json:"aiTitle"`
	CustomTitle      string                   `json:"customTitle"`
	PermissionMode   string                   `json:"permissionMode"`
	BridgeSessionID  string                   `json:"bridgeSessionId"`
	LastSequenceNum  *int64                   `json:"lastSequenceNum"`
	PRNumber         *int64                   `json:"prNumber"`
	PRURL            string                   `json:"prUrl"`
	PRRepository     string                   `json:"prRepository"`
	Snapshot         claudeCodeSourceSnapshot `json:"snapshot"`
}

type claudeCodeSourceSnapshot struct {
	TrackedFileBackups json.RawMessage `json:"trackedFileBackups"`
}

type claudeCodeUserMessage struct {
	Content json.RawMessage `json:"content"`
}

type claudeCodeAssistantMessage struct {
	Model   string                    `json:"model"`
	Content []claudeCodeContentBlock  `json:"content"`
	Usage   *claudeCodeAssistantUsage `json:"usage"`
}

type claudeCodeAssistantUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type claudeCodeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type claudeCodeCompactMeta struct {
	Trigger           string          `json:"trigger"`
	DurationMs        int64           `json:"durationMs"`
	PreTokens         int64           `json:"preTokens"`
	PostTokens        int64           `json:"postTokens"`
	PreservedSegment  json.RawMessage `json:"preservedSegment"`
	PreservedMessages json.RawMessage `json:"preservedMessages"`
}

type claudeCodeSourceState struct {
	sourceID              string
	sourceFile            string
	nativeSessionID       string
	parentNativeSessionID string
	rootNativeSessionID   string
	sessionKind           string
	sessionStarted        bool
	sessionStartedAt      int64
	turnOpen              bool
	turnSeq               int64
	turnStartedAt         int64
	turnFinalized         bool
	opSeq                 int64
	openTools             map[string]claudeCodeOpenTool
	toolUseToAgent        map[string]string
	childCompletions      map[string]claudeCodeChildCompletion
	sessionMetadata       claudeCodeSessionMetadataState
}

type claudeCodeOpenTool struct {
	turnSeq       int64
	opSeq         int64
	kind          string
	name          string
	toolNamespace string
	startedAt     int64
	childNativeID string
}

func newClaudeCodeSourceState(sourceID string, transcript claudeCodeTranscript, sourceContext claudeCodeSourceContext) claudeCodeSourceState {
	return claudeCodeSourceState{
		sourceID:              sourceID,
		sourceFile:            transcript.path,
		nativeSessionID:       transcript.nativeSessionID,
		parentNativeSessionID: transcript.parentNativeSessionID,
		rootNativeSessionID:   transcript.rootNativeSessionID,
		sessionKind:           transcript.kind,
		openTools:             map[string]claudeCodeOpenTool{},
		toolUseToAgent:        sourceContext.toolUseToAgentByParent[transcript.nativeSessionID],
		childCompletions:      sourceContext.childCompletions,
	}
}

func writeClaudeCodeSourceFile(ctx context.Context, sourceID string, transcript claudeCodeTranscript, sourceContext claudeCodeSourceContext, writer ArtifactWriter) error {
	file, err := os.Open(transcript.path)
	if err != nil {
		return fmt.Errorf("open claude-code source file read-only: %w", err)
	}
	defer func() { _ = file.Close() }()

	state := newClaudeCodeSourceState(sourceID, transcript, sourceContext)
	reader := bufio.NewReader(file)
	var lineNo int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := readClaudeCodeSourceLine(reader)
		if readErr != nil && len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("read claude-code source line: %w", readErr)
		}
		lineNo++
		line = trimLineEnding(line)
		lineArtifacts, err := extractClaudeCodeLineArtifacts(&state, line, lineNo)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", transcript.path, lineNo, err)
		}
		if err := writeArtifacts(ctx, writer, lineArtifacts); err != nil {
			return fmt.Errorf("%s:%d: write artifact: %w", transcript.path, lineNo, err)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	eofArtifacts, err := state.finalizeClaudeCodeAtEOF()
	if err != nil {
		return err
	}
	if err := writeArtifacts(ctx, writer, eofArtifacts); err != nil {
		return fmt.Errorf("%s: write eof artifact: %w", transcript.path, err)
	}
	return nil
}

type claudeCodeBoundaryCoalescer struct {
	downstream ArtifactWriter
	pending    map[MatchKey]Artifact
}

func newClaudeCodeBoundaryCoalescer(downstream ArtifactWriter) *claudeCodeBoundaryCoalescer {
	return &claudeCodeBoundaryCoalescer{
		downstream: downstream,
		pending:    map[MatchKey]Artifact{},
	}
}

func (w *claudeCodeBoundaryCoalescer) WriteArtifact(ctx context.Context, artifact Artifact) error {
	if claudeCodeCoalescedBoundaryClass(artifact.Class) {
		w.pending[artifact.Key()] = artifact
		return nil
	}
	return w.downstream.WriteArtifact(ctx, artifact)
}

func (w *claudeCodeBoundaryCoalescer) flush(ctx context.Context) error {
	keys := make([]MatchKey, 0, len(w.pending))
	for key := range w.pending {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return matchKeyString(keys[i]) < matchKeyString(keys[j])
	})
	for _, key := range keys {
		if err := w.downstream.WriteArtifact(ctx, w.pending[key]); err != nil {
			return err
		}
	}
	return nil
}

func claudeCodeCoalescedBoundaryClass(class ArtifactClass) bool {
	switch class {
	case ClassSessionBoundary, ClassTurnBoundary, ClassOpBoundary:
		return true
	default:
		return false
	}
}

func readClaudeCodeSourceLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > claudeCodeSourceLineMax {
			return nil, fmt.Errorf("line exceeds %d bytes", claudeCodeSourceLineMax)
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

func extractClaudeCodeLineArtifacts(state *claudeCodeSourceState, line []byte, lineNo int64) ([]Artifact, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var rec claudeCodeSourceRecord
	if !decodeClaudeCodeSourceRecord(trimmed, &rec) {
		return []Artifact{state.sourceCorruptionArtifact(line, lineNo, "json", "valid claude-code JSONL record", "decode_error")}, nil
	}
	if claudeCodeSourceIgnoredRecordType(rec.Type) {
		if claudeCodeSourceMetadataRecordType(rec.Type) {
			if err := state.recordClaudeCodeSessionMetadata(rec); err != nil {
				return nil, err
			}
		}
		if rec.Type == "pr-link" {
			tsUs, err := parseClaudeCodeRecordTimestamp(rec)
			if err != nil {
				return nil, err
			}
			state.observeSessionStart(tsUs)
			log := state.logEntry("pr-link", lineNo)
			return []Artifact{log}, nil
		}
		return nil, nil
	}
	if !claudeCodeSourceMappedRecordType(rec.Type) {
		return nil, fmt.Errorf("unknown claude-code source record type %q", rec.Type)
	}
	tsUs, err := parseClaudeCodeRecordTimestamp(rec)
	if err != nil {
		return nil, err
	}
	state.observeSessionStart(tsUs)

	switch rec.Type {
	case "user":
		return state.recordClaudeCodeUser(rec, line, lineNo, tsUs)
	case "assistant":
		return state.recordClaudeCodeAssistant(rec, line, lineNo, tsUs)
	case "system":
		return state.recordClaudeCodeSystem(rec, line, lineNo, tsUs)
	case "attachment":
		return state.recordClaudeCodeAttachment(line, lineNo)
	case "queue-operation":
		log := state.logEntry("queue-operation", lineNo)
		return []Artifact{log}, nil
	default:
		return nil, nil
	}
}

func decodeClaudeCodeSourceRecord(raw []byte, rec *claudeCodeSourceRecord) bool {
	return decodeJSON(raw, rec) == nil
}

func claudeCodeSourceMetadataRecordType(recordType string) bool {
	switch recordType {
	case "last-prompt",
		"ai-title",
		"custom-title",
		"permission-mode",
		"pr-link",
		"bridge-session",
		"file-history-snapshot":
		return true
	default:
		return false
	}
}

func claudeCodeSourceMappedRecordType(recordType string) bool {
	switch recordType {
	case "user", "assistant", "system", "attachment", "queue-operation":
		return true
	default:
		return false
	}
}

func claudeCodeSourceIgnoredRecordType(recordType string) bool {
	switch recordType {
	case "last-prompt",
		"ai-title",
		"custom-title",
		"permission-mode",
		"pr-link",
		"bridge-session",
		"file-history-snapshot",
		"summary",
		"task-summary",
		"tag",
		"agent-name",
		"agent-color",
		"agent-setting",
		"mode",
		"worktree-state",
		"content-replacement",
		"attribution-snapshot",
		"speculation-accept",
		"fork-context-ref",
		"marble-origami-commit",
		"marble-origami-snapshot":
		return true
	default:
		return false
	}
}

func parseClaudeCodeRecordTimestamp(rec claudeCodeSourceRecord) (int64, error) {
	if rec.Timestamp == "" {
		return 0, nil
	}
	return parseClaudeCodeSourceTimestamp(rec.Timestamp)
}

func parseClaudeCodeSourceTimestamp(timestamp string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return 0, fmt.Errorf("ts %q: %w", timestamp, err)
	}
	return t.UnixMicro(), nil
}

func (s *claudeCodeSourceState) observeSessionStart(tsUs int64) {
	if s.sessionStarted || tsUs == 0 {
		return
	}
	s.sessionStarted = true
	s.sessionStartedAt = tsUs
}
