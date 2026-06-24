package parity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// CodexSourceOptions configures source-manifest extraction for Codex JSONL files.
type CodexSourceOptions struct {
	Root     string
	SourceID string
}

// ExtractCodexSource builds source artifacts directly from Codex JSONL rollout files.
func ExtractCodexSource(ctx context.Context, opts CodexSourceOptions) ([]Artifact, error) {
	var artifacts []Artifact
	err := ExtractCodexSourceToWriter(ctx, opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}))
	return artifacts, err
}

// ExtractCodexSourceToWriter streams source artifacts directly from Codex JSONL rollout files.
func ExtractCodexSourceToWriter(ctx context.Context, opts CodexSourceOptions, writer ArtifactWriter) error {
	if opts.Root == "" {
		return fmt.Errorf("extract codex source: missing root")
	}
	if writer == nil {
		return fmt.Errorf("extract codex source: nil artifact writer")
	}
	sourceID := opts.SourceID
	if sourceID == "" {
		sourceID = "codex:" + opts.Root
	}

	files, sourceErrs, err := discoverCodexSourceFiles(ctx, opts.Root)
	if err != nil {
		return fmt.Errorf("extract codex source: %w", err)
	}
	for _, path := range files.legacy {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("extract codex source: %w", err)
		}
		if err := writeCodexLegacySourceFile(ctx, path, sourceID, writer); err != nil {
			if codexSourceContextError(err) {
				return fmt.Errorf("extract codex source: %w", err)
			}
			sourceErrs = append(sourceErrs, err)
		}
	}
	for _, path := range files.modern {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("extract codex source: %w", err)
		}
		if err := writeCodexSourceFile(ctx, path, sourceID, writer); err != nil {
			if codexSourceContextError(err) {
				return fmt.Errorf("extract codex source: %w", err)
			}
			sourceErrs = append(sourceErrs, err)
		}
	}
	if len(sourceErrs) > 0 {
		return fmt.Errorf("extract codex source: %w", errors.Join(sourceErrs...))
	}
	return nil
}

type codexSourceFiles struct {
	modern []string
	legacy []string
}

func discoverCodexSourceFiles(ctx context.Context, root string) (codexSourceFiles, []error, error) {
	var files codexSourceFiles
	var sourceErrs []error
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		if os.IsNotExist(err) {
			return files, nil, nil
		}
		return files, nil, fmt.Errorf("resolve codex source root %q: %w", root, err)
	}
	err = filepath.WalkDir(resolvedRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if codexSourceContextError(walkErr) {
				return walkErr
			}
			sourceErrs = append(sourceErrs, walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "archived_sessions" && filepath.Clean(path) != resolvedRoot {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case codexSourceModernJSONL(resolvedRoot, path, entry.Name()):
			resolvedPath, ok, err := codexSourceResolveWithinRoot(resolvedRoot, path)
			if err != nil {
				sourceErrs = append(sourceErrs, err)
				return nil
			}
			if !ok {
				sourceErrs = append(sourceErrs, fmt.Errorf("codex: %s resolves to %s outside the sessions root; skipping (symlink escape)", path, resolvedPath))
				return nil
			}
			files.modern = append(files.modern, resolvedPath)
		case codexSourceLegacyFlatJSON(resolvedRoot, path):
			resolvedPath, ok, err := codexSourceResolveWithinRoot(resolvedRoot, path)
			if err != nil {
				sourceErrs = append(sourceErrs, err)
				return nil
			}
			if !ok {
				sourceErrs = append(sourceErrs, fmt.Errorf("codex: %s resolves to %s outside the sessions root; skipping (symlink escape)", path, resolvedPath))
				return nil
			}
			files.legacy = append(files.legacy, resolvedPath)
		default:
			return nil
		}
		return nil
	})
	if err != nil {
		return codexSourceFiles{}, sourceErrs, err
	}
	sort.Strings(files.legacy)
	sort.Strings(files.modern)
	return files, sourceErrs, nil
}

func codexSourceResolveWithinRoot(resolvedRoot string, path string) (string, bool, error) {
	resolvedPath, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", false, fmt.Errorf("resolve codex source path %q: %w", path, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return "", false, fmt.Errorf("relative codex source path %q under %q: %w", resolvedPath, resolvedRoot, err)
	}
	ok := rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	return resolvedPath, ok, nil
}

func codexSourceModernJSONL(root string, path string, name string) bool {
	if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return false
	}
	return codexSourceShardDepth(filepath.ToSlash(rel))
}

func codexSourceShardDepth(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts[:3] {
		if !codexSourceNumericShard(part) {
			return false
		}
	}
	return true
}

func codexSourceNumericShard(part string) bool {
	if part == "" {
		return false
	}
	for _, r := range part {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func codexSourceContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type codexSourceState struct {
	sourceID              string
	sourceFile            string
	sourceMtimeUs         int64
	sourceStale           bool
	nativeSessionID       string
	sessionStarted        bool
	sessionLineNo         int64
	sessionStartedAt      int64
	sessionStatus         string
	sessionEndedAt        *int64
	sessionMetadata       codexSessionMetadataIdentity
	sessionMetadataOK     bool
	sessionMetadataLineNo int64
	lastContentTsUs       int64
	activeTurn            *codexSourceTurn
	turnSeqCounter        int64
	subTurnCounter        int64
	openTools             map[string]codexSourceOpenOp
	openWebSearch         []codexSourceOpenWebSearch
	lastCompactedLine     int64
	lastCompactedTsUs     int64
}

type codexSourceTurn struct {
	sourceTurnID   string
	seq            int64
	startedAt      int64
	status         string
	endedAt        *int64
	finalized      bool
	sawTaskStarted bool
	opSeq          int64
	userInputCount int64
}

type codexSourceOpenOp struct {
	turnSeq        int64
	opSeq          int64
	name           string
	namespace      string
	startedAt      int64
	enrichStatus   string
	enrichErrClass string
}

type codexSourceOpenWebSearch struct {
	callID string
}

type codexPayloadHeader struct {
	Type   string
	Role   string
	Name   string
	CallID string
}

const codexCompactionPreviewMax = 200

type codexCompactionEventMeta struct {
	Trigger                string
	ReplacementHistorySize int64
	MessagePreview         string
}

type codexCompactionEventIdentity struct {
	NativeSessionID        string `json:"native_session_id"`
	TurnSeq                int64  `json:"turn_seq"`
	OpSeq                  int64  `json:"op_seq"`
	Trigger                string `json:"trigger,omitempty"`
	ReplacementHistorySize int64  `json:"replacement_history_size,omitempty"`
	MessagePreviewSHA256   string `json:"message_preview_sha256,omitempty"`
	StartedAt              int64  `json:"started_at"`
	EndedAt                *int64 `json:"ended_at,omitempty"`
}

func newCodexSourceState(sourceID string, sourceFile string, sourceMtime time.Time) codexSourceState {
	return codexSourceState{
		sourceID:      sourceID,
		sourceFile:    sourceFile,
		sourceMtimeUs: sourceMtime.UnixMicro(),
		sourceStale:   time.Since(sourceMtime) >= time.Hour,
		openTools:     map[string]codexSourceOpenOp{},
	}
}

func extractCodexSourceFile(ctx context.Context, path string, sourceID string) ([]Artifact, error) {
	var artifacts []Artifact
	err := writeCodexSourceFile(ctx, path, sourceID, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}))
	return artifacts, err
}

func writeCodexSourceFile(ctx context.Context, path string, sourceID string, writer ArtifactWriter) error {
	file, err := os.Open(path) // #nosec G304 -- path comes from Codex parity discovery after source-root containment checks.
	if err != nil {
		return fmt.Errorf("open codex source file read-only: %w", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat codex source file: %w", err)
	}
	state := newCodexSourceState(sourceID, path, info.ModTime())
	reader := bufio.NewReader(file)
	var lineNo int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := readCodexSourceLine(reader)
		if readErr != nil && len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("read codex source line: %w", readErr)
		}
		lineNo++
		line = trimLineEnding(line)
		lineArtifacts, err := extractCodexLineArtifacts(&state, line, lineNo)
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
	eofArtifacts, err := state.finalizeAtEOF()
	if err != nil {
		return err
	}
	if err := writeArtifacts(ctx, writer, eofArtifacts); err != nil {
		return fmt.Errorf("%s: write eof artifact: %w", path, err)
	}
	return nil
}

type codexEnvelope struct {
	Timestamp  string          `json:"timestamp"`
	Type       string          `json:"type"`
	RecordType string          `json:"record_type"`
	ID         string          `json:"id"`
	Payload    json.RawMessage `json:"payload"`
}

func extractCodexLineArtifacts(state *codexSourceState, line []byte, lineNo int64) ([]Artifact, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var env codexEnvelope
	if err := decodeJSON(trimmed, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.RecordType == "state" && env.Type == "" {
		return nil, nil
	}
	if env.Type == "" {
		if env.ID == "" || env.Timestamp == "" {
			return nil, fmt.Errorf("record.type is required")
		}
		if state.sessionStarted {
			return nil, fmt.Errorf("legacy session header after session start")
		}
		env.Type = "session_meta"
	}
	tsUs, err := parseCodexSourceTimestamp(env.Timestamp)
	if err != nil {
		return nil, err
	}
	switch env.Type {
	case "session_meta":
		if err := updateCodexSourceSession(state, codexPayloadOrLine(env.Payload, trimmed), lineNo); err != nil {
			return nil, err
		}
		return state.sessionBoundary(tsUs, lineNo)
	case "response_item":
		return extractCodexResponseItemArtifacts(state, trimmed, lineNo, tsUs, env.Payload, "/payload")
	case "event_msg":
		return extractCodexEventMsgArtifacts(state, trimmed, lineNo, tsUs, env.Payload)
	case "turn_context":
		return state.recordTurnContext(tsUs, codexPayloadOrLine(env.Payload, trimmed))
	case "compacted":
		return state.recordCompacted(tsUs, env.Payload, trimmed, lineNo)
	default:
		if codexDirectResponseItemNoOp(env.Type) {
			return nil, nil
		}
		if codexDirectResponseItemType(env.Type) {
			return extractCodexResponseItemArtifacts(state, trimmed, lineNo, tsUs, trimmed, "")
		}
		return nil, fmt.Errorf("unknown codex record type %q", env.Type)
	}
}

func codexPayloadOrLine(payload json.RawMessage, line []byte) json.RawMessage {
	body := bytes.TrimSpace(payload)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return append(json.RawMessage(nil), line...)
	}
	return payload
}

func codexCompactionMetaFromPayload(payload json.RawMessage) (codexCompactionEventMeta, error) {
	meta := codexCompactionEventMeta{Trigger: "auto"}
	body := bytes.TrimSpace(payload)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return meta, nil
	}
	var compacted struct {
		Message            string            `json:"message"`
		ReplacementHistory []json.RawMessage `json:"replacement_history"`
	}
	if err := decodeJSONPayload(body, &compacted); err != nil {
		return codexCompactionEventMeta{}, fmt.Errorf("decode compacted payload: %w", err)
	}
	meta.ReplacementHistorySize = int64(len(compacted.ReplacementHistory))
	meta.MessagePreview = codexTrimPreview(compacted.Message, codexCompactionPreviewMax)
	return meta, nil
}

func codexCompactionEventIdentityFor(nativeSessionID string, turnSeq int64, opSeq int64, meta codexCompactionEventMeta, startedAt int64, endedAt *int64) codexCompactionEventIdentity {
	identity := codexCompactionEventIdentity{
		NativeSessionID:        nativeSessionID,
		TurnSeq:                turnSeq,
		OpSeq:                  opSeq,
		Trigger:                meta.Trigger,
		ReplacementHistorySize: meta.ReplacementHistorySize,
		StartedAt:              startedAt,
		EndedAt:                endedAt,
	}
	if meta.MessagePreview != "" {
		identity.MessagePreviewSHA256 = stringSHA256(meta.MessagePreview)
	}
	return identity
}

func codexTrimPreview(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

func parseCodexSourceTimestamp(timestamp string) (int64, error) {
	if timestamp == "" {
		return 0, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return 0, fmt.Errorf("parse timestamp %q: %w", timestamp, err)
	}
	return parsed.UnixMicro(), nil
}

func (s *codexSourceState) noteContentTimestamp(tsUs int64) {
	if tsUs > s.lastContentTsUs {
		s.lastContentTsUs = tsUs
	}
}

func updateCodexSourceSession(state *codexSourceState, payload json.RawMessage, lineNo int64) error {
	var body codexSessionMetaPayload
	if err := decodeJSONPayload(payload, &body); err != nil {
		return fmt.Errorf("decode session_meta payload: %w", err)
	}
	if body.ID != "" {
		state.nativeSessionID = body.ID
	}
	identity, ok, err := codexSessionMetadataIdentityFromSource(state.sessionID(), body)
	if err != nil {
		return err
	}
	state.sessionMetadata = identity
	state.sessionMetadataOK = ok
	state.sessionMetadataLineNo = lineNo
	return nil
}

func (s *codexSourceState) sessionBoundary(tsUs int64, lineNo int64) ([]Artifact, error) {
	if s.sessionStarted {
		return nil, nil
	}
	s.sessionStarted = true
	s.sessionStartedAt = tsUs
	s.sessionLineNo = lineNo
	s.sessionStatus = "running"
	return nil, nil
}

func (s *codexSourceState) finalizeSessionBoundary() ([]Artifact, error) {
	if !s.sessionStarted {
		return nil, nil
	}
	nativeID := s.sessionID()
	identity := sessionBoundaryIdentity{
		NativeSessionID:     nativeID,
		RootNativeSessionID: nativeID,
		Kind:                "root",
		Status:              s.sessionStatus,
		StartedAt:           s.sessionStartedAt,
		EndedAt:             s.sessionEndedAt,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "codex",
		sourceFile:       s.sourceFile,
		nativeSessionID:  nativeID,
		nativeArtifactID: "session:" + nativeID,
		class:            ClassSessionBoundary,
		selectorURI:      codexLineSelectorURI(s.sourceFile, s.sessionLineNo),
		identity:         identity,
	})
	if err != nil {
		return nil, err
	}
	return []Artifact{artifact}, nil
}

func (s *codexSourceState) finalizeSessionArtifacts() ([]Artifact, error) {
	artifacts, err := s.finalizeSessionBoundary()
	if err != nil {
		return nil, err
	}
	metadata, err := s.sessionMetadataArtifact()
	if err != nil {
		return nil, err
	}
	if metadata.NativeArtifactID != "" {
		artifacts = append(artifacts, metadata)
	}
	return artifacts, nil
}

func (s *codexSourceState) sessionMetadataArtifact() (Artifact, error) {
	if !s.sessionMetadataOK {
		return Artifact{}, nil
	}
	lineNo := s.sessionMetadataLineNo
	if lineNo == 0 {
		lineNo = s.sessionLineNo
	}
	identity := s.sessionMetadata
	identity.NativeSessionID = s.sessionID()
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          codexFormat,
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeArtifactID: "session:" + s.sessionID() + ":metadata",
		class:            ClassSessionMetadata,
		selectorURI:      codexLineSelectorURI(s.sourceFile, lineNo),
		identity:         identity,
	})
}

func (s *codexSourceState) recordTurnContext(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	s.noteContentTimestamp(tsUs)
	var body struct {
		TurnID string `json:"turn_id"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode turn_context payload: %w", err)
	}
	return s.activateTurn(body.TurnID, tsUs)
}

func (s *codexSourceState) activateTurn(sourceTurnID string, tsUs int64) ([]Artifact, error) {
	var artifacts []Artifact
	if s.activeTurn != nil && !s.activeTurn.finalized && sourceTurnID != "" && sourceTurnID != s.activeTurn.sourceTurnID {
		status := "completed"
		danglingStatus := "completed"
		if s.activeTurn.sawTaskStarted {
			status = "failed"
			danglingStatus = "cancelled"
		}
		toolArtifacts, err := s.finalizeOpenToolsForTurn(s.activeTurn.seq, danglingStatus, tsUs)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, toolArtifacts...)
		turnArtifacts, err := s.finalizeActiveTurn(status, tsUs)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, turnArtifacts...)
	}
	turn := s.ensureTurn(tsUs)
	if turn.sourceTurnID == "" {
		turn.sourceTurnID = sourceTurnID
	}
	return artifacts, nil
}

func (s *codexSourceState) recordCompaction(tsUs int64, lineNo int64, meta codexCompactionEventMeta) ([]Artifact, error) {
	turn := s.ensureTurn(tsUs)
	turn.opSeq++
	artifacts, err := s.opBoundary(turn.seq, turn.opSeq, "compaction", "compaction", "completed", tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	compactionArtifact, err := s.compactionEvent(turn.seq, turn.opSeq, tsUs, ptrInt64(tsUs), lineNo, meta)
	if err != nil {
		return nil, err
	}
	return append(artifacts, compactionArtifact), nil
}

func (s *codexSourceState) recordCompacted(tsUs int64, payload json.RawMessage, line []byte, lineNo int64) ([]Artifact, error) {
	s.noteContentTimestamp(tsUs)
	s.lastCompactedLine = lineNo
	s.lastCompactedTsUs = tsUs
	meta, err := codexCompactionMetaFromPayload(payload)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.recordCompaction(tsUs, lineNo, meta)
	if err != nil {
		return nil, err
	}
	return append(artifacts, codexSemanticLineArtifact(s, line, lineNo, ClassLogEntry)), nil
}

func (s *codexSourceState) recordContextCompacted(tsUs int64, line []byte, lineNo int64) ([]Artifact, error) {
	if s.lastCompactedLine == lineNo-1 && s.lastCompactedTsUs == tsUs {
		return nil, nil
	}
	s.noteContentTimestamp(tsUs)
	artifacts, err := s.recordCompaction(tsUs, lineNo, codexCompactionEventMeta{Trigger: "auto"})
	if err != nil {
		return nil, err
	}
	return append(artifacts, codexSemanticLineArtifact(s, line, lineNo, ClassLogEntry)), nil
}

func (s *codexSourceState) recordTaskStarted(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	var body struct {
		TurnID string `json:"turn_id"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode task_started payload: %w", err)
	}
	startUs, err := codexStartedAtMicros(payload)
	if err != nil {
		return nil, err
	}
	if startUs == 0 || startUs < tsUs {
		startUs = tsUs
	}
	artifacts, err := s.activateTurn(body.TurnID, startUs)
	if err != nil {
		return nil, err
	}
	turn := s.ensureTurn(startUs)
	turn.sawTaskStarted = true
	return artifacts, nil
}

func (s *codexSourceState) recordTaskComplete(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	if s.activeTurn == nil || s.activeTurn.finalized {
		return nil, nil
	}
	endUs, err := codexCompletedAtMicros(payload)
	if err != nil {
		return nil, err
	}
	if endUs == 0 {
		endUs = tsUs
	}
	toolArtifacts, err := s.finalizeOpenToolsForTurn(s.activeTurn.seq, "completed", endUs)
	if err != nil {
		return nil, err
	}
	turnArtifacts, err := s.finalizeActiveTurn("completed", endUs)
	if err != nil {
		return nil, err
	}
	return append(toolArtifacts, turnArtifacts...), nil
}

func (s *codexSourceState) recordTurnAborted(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	if s.activeTurn == nil || s.activeTurn.finalized {
		return nil, nil
	}
	endUs, err := codexCompletedAtMicros(payload)
	if err != nil {
		return nil, err
	}
	if endUs == 0 {
		endUs = tsUs
	}
	toolArtifacts, err := s.finalizeOpenToolsForTurn(s.activeTurn.seq, "cancelled", endUs)
	if err != nil {
		return nil, err
	}
	turnArtifacts, err := s.finalizeActiveTurn("failed", endUs)
	if err != nil {
		return nil, err
	}
	return append(toolArtifacts, turnArtifacts...), nil
}

func (s *codexSourceState) recordCompletedOp(tsUs int64, kind string, name string) ([]Artifact, error) {
	turn := s.ensureTurn(tsUs)
	turn.opSeq++
	return s.opBoundary(turn.seq, turn.opSeq, kind, name, "completed", tsUs, ptrInt64(tsUs))
}

func (s *codexSourceState) recordUserInput(tsUs int64) ([]Artifact, error) {
	artifacts, err := s.splitTurnForUserInput(tsUs)
	if err != nil {
		return nil, err
	}
	turn := s.ensureTurn(tsUs)
	turn.opSeq++
	turn.userInputCount++
	opArtifacts, err := s.opBoundary(turn.seq, turn.opSeq, "internal", "user_input", "completed", tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, opArtifacts...)
	return artifacts, nil
}

func (s *codexSourceState) splitTurnForUserInput(tsUs int64) ([]Artifact, error) {
	if s.activeTurn == nil || s.activeTurn.finalized || s.activeTurn.userInputCount == 0 || s.hasOpenToolsForTurn(s.activeTurn.seq) {
		return nil, nil
	}
	artifacts, err := s.finalizeActiveTurn("completed", tsUs)
	if err != nil {
		return nil, err
	}
	s.subTurnCounter++
	turn := s.ensureTurn(tsUs)
	turn.sourceTurnID = fmt.Sprintf("sub:%d", s.subTurnCounter)
	return artifacts, nil
}

func (s *codexSourceState) recordToolStart(tsUs int64, name string, namespace string, callID string) {
	turn := s.ensureTurn(tsUs)
	turn.opSeq++
	open := codexSourceOpenOp{turnSeq: turn.seq, opSeq: turn.opSeq, name: name, namespace: namespace, startedAt: tsUs}
	if callID != "" {
		s.openTools[callID] = open
	}
}

func (s *codexSourceState) recordToolOutput(tsUs int64, callID string) ([]Artifact, error) {
	if callID == "" {
		return nil, nil
	}
	open, ok := s.openTools[callID]
	if !ok {
		return nil, nil
	}
	delete(s.openTools, callID)
	status := "completed"
	if open.enrichStatus != "" {
		status = open.enrichStatus
	}
	opArtifacts, err := s.opBoundaryWithNamespace(open.turnSeq, open.opSeq, "tool", open.name, open.namespace, status, open.startedAt, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	if open.enrichErrClass == "" {
		return opArtifacts, nil
	}
	errorArtifact, err := s.toolError(open.turnSeq, open.opSeq, open.enrichErrClass)
	if err != nil {
		return nil, err
	}
	return append(opArtifacts, errorArtifact), nil
}

func (s *codexSourceState) recordWebSearchStart(tsUs int64) {
	turn := s.ensureTurn(tsUs)
	turn.opSeq++
	callID := fmt.Sprintf("ws#%d:%d", turn.seq, turn.opSeq)
	s.openTools[callID] = codexSourceOpenOp{
		turnSeq:   turn.seq,
		opSeq:     turn.opSeq,
		name:      "web_search",
		namespace: "web",
		startedAt: tsUs,
	}
	s.openWebSearch = append(s.openWebSearch, codexSourceOpenWebSearch{callID: callID})
}

func (s *codexSourceState) recordWebSearchEnd(tsUs int64) ([]Artifact, error) {
	for len(s.openWebSearch) > 0 {
		search := s.openWebSearch[0]
		s.openWebSearch = s.openWebSearch[1:]
		open, ok := s.openTools[search.callID]
		if !ok {
			continue
		}
		delete(s.openTools, search.callID)
		return s.opBoundaryWithNamespace(open.turnSeq, open.opSeq, "tool", open.name, open.namespace, "completed", open.startedAt, ptrInt64(tsUs))
	}
	return nil, nil
}

func (s *codexSourceState) recordCollabSpawn(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	var body struct {
		NewThreadID string `json:"new_thread_id"`
		Status      string `json:"status"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode collab_agent_spawn_end payload: %w", err)
	}
	if body.NewThreadID == "" {
		return nil, nil
	}
	turn := s.ensureTurn(tsUs)
	turn.opSeq++
	status := codexSpawnStatus(body.Status)
	nativeOpID := fmt.Sprintf("op:%d:%d", turn.seq, turn.opSeq)
	opArtifacts, err := s.opBoundary(turn.seq, turn.opSeq, "session", "spawn", status, tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	linkArtifact, err := s.subagentLink(turn.seq, turn.opSeq, nativeOpID, body.NewThreadID)
	if err != nil {
		return nil, err
	}
	return append(opArtifacts, linkArtifact), nil
}

func (s *codexSourceState) recordPatchApplyEnd(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	var body struct {
		CallID  string `json:"call_id"`
		Success *bool  `json:"success"`
		Status  string `json:"status"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode patch_apply_end payload: %w", err)
	}
	if body.CallID == "" {
		return nil, nil
	}
	open, ok := s.openTools[body.CallID]
	if !ok {
		return nil, nil
	}
	delete(s.openTools, body.CallID)
	status, errClass := codexPatchApplyStatus(body.Success, body.Status)
	opArtifacts, err := s.opBoundaryWithNamespace(open.turnSeq, open.opSeq, "tool", open.name, open.namespace, status, open.startedAt, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	if errClass == "" {
		return opArtifacts, nil
	}
	errorArtifact, err := s.toolError(open.turnSeq, open.opSeq, errClass)
	if err != nil {
		return nil, err
	}
	return append(opArtifacts, errorArtifact), nil
}

func (s *codexSourceState) recordExecCommandEnd(payload json.RawMessage) ([]Artifact, error) {
	var body struct {
		CallID   string `json:"call_id"`
		ExitCode *int64 `json:"exit_code"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode exec_command_end payload: %w", err)
	}
	if body.CallID == "" {
		return nil, nil
	}
	open, ok := s.openTools[body.CallID]
	if !ok {
		return nil, nil
	}
	status, errClass := codexExecCommandStatus(body.ExitCode)
	if status == "" {
		return nil, nil
	}
	open.enrichStatus = status
	open.enrichErrClass = errClass
	s.openTools[body.CallID] = open
	return nil, nil
}

func (s *codexSourceState) recordMcpToolCallEnd(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	var body struct {
		CallID     string `json:"call_id"`
		Invocation struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
		} `json:"invocation"`
		Result json.RawMessage `json:"result"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode mcp_tool_call_end payload: %w", err)
	}
	if body.CallID == "" {
		return nil, nil
	}
	open, ok := s.openTools[body.CallID]
	if !ok {
		return nil, nil
	}
	delete(s.openTools, body.CallID)
	if body.Invocation.Tool != "" {
		open.name = body.Invocation.Tool
	}
	if body.Invocation.Server != "" {
		open.namespace = "mcp:" + body.Invocation.Server
	}
	status, errClass := codexMcpResultStatus(body.Result)
	opArtifacts, err := s.opBoundaryWithNamespace(open.turnSeq, open.opSeq, "tool", open.name, open.namespace, status, open.startedAt, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	if errClass == "" {
		return opArtifacts, nil
	}
	errorArtifact, err := s.toolError(open.turnSeq, open.opSeq, errClass)
	if err != nil {
		return nil, err
	}
	return append(opArtifacts, errorArtifact), nil
}

func (s *codexSourceState) recordImageGenerationEnd(tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	var body struct {
		CallID string `json:"call_id"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return nil, fmt.Errorf("decode image_generation_end payload: %w", err)
	}
	if body.CallID == "" {
		return nil, nil
	}
	open, ok := s.openTools[body.CallID]
	if !ok {
		return nil, nil
	}
	delete(s.openTools, body.CallID)
	return s.opBoundaryWithNamespace(open.turnSeq, open.opSeq, "tool", open.name, open.namespace, "completed", open.startedAt, ptrInt64(tsUs))
}

func (s *codexSourceState) finalizeAtEOF() ([]Artifact, error) {
	var artifacts []Artifact

	if s.activeTurn == nil || s.activeTurn.finalized {
		toolArtifacts, err := s.finalizeOpenToolsForTurn(0, "completed", s.lastContentTsUs)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, toolArtifacts...)
		sessionArtifacts, err := s.finalizeSessionArtifacts()
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, sessionArtifacts...)
		return artifacts, nil
	}

	if s.activeTurn.sawTaskStarted {
		if s.sourceStale {
			endUs := s.normalizeActiveTurnEndUs(s.sourceMtimeUs)
			toolArtifacts, err := s.finalizeOpenToolsForTurn(s.activeTurn.seq, "cancelled", endUs)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, toolArtifacts...)
			turnArtifacts, err := s.finalizeActiveTurn("failed", endUs)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, turnArtifacts...)
			s.sessionStatus = "failed"
			s.sessionEndedAt = ptrInt64(endUs)
		} else {
			turnArtifact, err := s.turnBoundary(s.activeTurn)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, turnArtifact)
		}
		sessionArtifacts, err := s.finalizeSessionArtifacts()
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, sessionArtifacts...)
		return artifacts, nil
	}

	endUs := s.lastContentTsUs
	if endUs == 0 {
		endUs = s.sourceMtimeUs
	}
	endUs = s.normalizeActiveTurnEndUs(endUs)
	toolArtifacts, err := s.finalizeOpenToolsForTurn(s.activeTurn.seq, "completed", endUs)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, toolArtifacts...)
	turnArtifacts, err := s.finalizeActiveTurn("completed", endUs)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, turnArtifacts...)
	sessionArtifacts, err := s.finalizeSessionArtifacts()
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, sessionArtifacts...)
	return artifacts, nil
}

func (s *codexSourceState) normalizeActiveTurnEndUs(endUs int64) int64 {
	if s.activeTurn != nil && endUs < s.activeTurn.startedAt {
		return s.activeTurn.startedAt
	}
	return endUs
}

func (s *codexSourceState) finalizeOpenToolsForTurn(turnSeq int64, status string, endUs int64) ([]Artifact, error) {
	type pendingTool struct {
		callID string
		open   codexSourceOpenOp
	}
	pending := make([]pendingTool, 0, len(s.openTools))
	for callID, open := range s.openTools {
		if turnSeq != 0 && open.turnSeq != turnSeq {
			continue
		}
		pending = append(pending, pendingTool{callID: callID, open: open})
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].open.turnSeq != pending[j].open.turnSeq {
			return pending[i].open.turnSeq < pending[j].open.turnSeq
		}
		if pending[i].open.opSeq != pending[j].open.opSeq {
			return pending[i].open.opSeq < pending[j].open.opSeq
		}
		return pending[i].callID < pending[j].callID
	})
	artifacts := make([]Artifact, 0, len(pending))
	for _, tool := range pending {
		toolStatus := status
		if tool.open.enrichStatus != "" {
			toolStatus = tool.open.enrichStatus
		}
		opArtifacts, err := s.opBoundaryWithNamespace(tool.open.turnSeq, tool.open.opSeq, "tool", tool.open.name, tool.open.namespace, toolStatus, tool.open.startedAt, ptrInt64(endUs))
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, opArtifacts...)
		if tool.open.enrichErrClass != "" {
			errorArtifact, err := s.toolError(tool.open.turnSeq, tool.open.opSeq, tool.open.enrichErrClass)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, errorArtifact)
		}
		delete(s.openTools, tool.callID)
	}
	return artifacts, nil
}

func (s *codexSourceState) hasOpenToolsForTurn(turnSeq int64) bool {
	for _, open := range s.openTools {
		if open.turnSeq == turnSeq {
			return true
		}
	}
	return false
}

func (s *codexSourceState) finalizeActiveTurn(status string, endUs int64) ([]Artifact, error) {
	if s.activeTurn == nil || s.activeTurn.finalized {
		return nil, nil
	}
	if endUs < s.activeTurn.startedAt {
		endUs = s.activeTurn.startedAt
	}
	s.activeTurn.status = status
	s.activeTurn.endedAt = ptrInt64(endUs)
	s.activeTurn.finalized = true
	artifact, err := s.turnBoundary(s.activeTurn)
	if err != nil {
		return nil, err
	}
	return []Artifact{artifact}, nil
}

func (s *codexSourceState) ensureTurn(tsUs int64) *codexSourceTurn {
	if s.activeTurn != nil && !s.activeTurn.finalized {
		return s.activeTurn
	}
	s.turnSeqCounter++
	turn := &codexSourceTurn{
		seq:       s.turnSeqCounter,
		startedAt: tsUs,
		status:    "running",
	}
	s.activeTurn = turn
	return turn
}

func (s *codexSourceState) sessionID() string {
	if s.nativeSessionID != "" {
		return s.nativeSessionID
	}
	return "source:" + s.sourceID
}

func (s *codexSourceState) turnBoundary(turn *codexSourceTurn) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turn.seq)
	identity := turnBoundaryIdentity{
		NativeSessionID: s.sessionID(),
		TurnSeq:         turn.seq,
		Status:          turn.status,
		StartedAt:       turn.startedAt,
		EndedAt:         turn.endedAt,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "codex",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeTurnID,
		class:            ClassTurnBoundary,
		selectorURI:      "codex-source://turns/" + url.PathEscape(nativeTurnID),
		identity:         identity,
	})
}

func (s *codexSourceState) opBoundary(turnSeq int64, opSeq int64, kind string, name string, status string, startedAt int64, endedAt *int64) ([]Artifact, error) {
	return s.opBoundaryWithNamespace(turnSeq, opSeq, kind, name, "", status, startedAt, endedAt)
}

func (s *codexSourceState) opBoundaryWithNamespace(turnSeq int64, opSeq int64, kind string, name string, namespace string, status string, startedAt int64, endedAt *int64) ([]Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
	if endedAt != nil && *endedAt == 0 {
		endedAt = nil
	}
	identity := opBoundaryIdentity{
		NativeSessionID: s.sessionID(),
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		Kind:            kind,
		Name:            name,
		ToolNamespace:   parityMCPToolNamespace(namespace),
		Status:          status,
		StartedAt:       startedAt,
		EndedAt:         endedAt,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "codex",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID,
		class:            ClassOpBoundary,
		selectorURI:      "codex-source://ops/" + url.PathEscape(nativeOpID),
		identity:         identity,
	})
	if err != nil {
		return nil, err
	}
	return []Artifact{artifact}, nil
}

func (s *codexSourceState) compactionEvent(turnSeq int64, opSeq int64, startedAt int64, endedAt *int64, lineNo int64, meta codexCompactionEventMeta) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
	identity := codexCompactionEventIdentityFor(s.sessionID(), turnSeq, opSeq, meta, startedAt, endedAt)
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "codex",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID + ":compaction",
		class:            ClassCompactionEvent,
		selectorURI:      codexLineSelectorURI(s.sourceFile, lineNo),
		identity:         identity,
	})
}

func (s *codexSourceState) subagentLink(turnSeq int64, opSeq int64, nativeOpID string, childNativeID string) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	nativeArtifactID := nativeOpID + ":child_session:" + childNativeID
	identity := subagentLinkIdentity{
		ParentNativeSessionID: s.sessionID(),
		ParentTurnSeq:         turnSeq,
		ParentOpSeq:           opSeq,
		ChildNativeSessionID:  childNativeID,
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "codex",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeArtifactID,
		class:            ClassSubagentLink,
		selectorURI:      "codex-source://ops/" + url.PathEscape(nativeOpID) + "#child_session",
		identity:         identity,
	})
}

func (s *codexSourceState) toolError(turnSeq int64, opSeq int64, errorClass string) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
	identity := opErrorIdentity{
		NativeSessionID:    s.sessionID(),
		TurnSeq:            turnSeq,
		OpSeq:              opSeq,
		OpKind:             "tool",
		ErrorClass:         errorClass,
		ErrorMessageSHA256: stringSHA256(""),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "codex",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.sessionID(),
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID + ":error",
		class:            ClassToolError,
		selectorURI:      "codex-source://ops/" + url.PathEscape(nativeOpID) + "#error",
		identity:         identity,
	})
}

func ptrInt64(value int64) *int64 {
	return &value
}

func extractCodexResponseItemArtifacts(state *codexSourceState, line []byte, lineNo int64, tsUs int64, payload json.RawMessage, pointerPrefix string) ([]Artifact, error) {
	payloadDoc, err := decodeCodexPayloadDocument(payload)
	if err != nil {
		return nil, fmt.Errorf("decode response_item payload document: %w", err)
	}
	body := codexPayloadHeaderFromDocument(payloadDoc)
	if body.Type == "ghost_snapshot" {
		return nil, nil
	}
	state.noteContentTimestamp(tsUs)
	switch body.Type {
	case "message":
		var structural []Artifact
		var err error
		var payloadArtifacts []Artifact
		if body.Role == "user" {
			structural, err = state.recordUserInput(tsUs)
			if err != nil {
				return nil, err
			}
			promptArtifacts, err := codexPointerArtifactsFromDocument(state, lineNo, ClassUserPrompt, pointerPrefix, payloadDoc, textPointersFromDocument(payloadDoc, "content", pointerPrefix))
			if err != nil {
				return nil, err
			}
			imageArtifacts, err := codexPointerArtifactsFromDocument(state, lineNo, ClassUserImage, pointerPrefix, payloadDoc, imageContentPointersFromDocument(payloadDoc, "content", pointerPrefix))
			if err != nil {
				return nil, err
			}
			payloadArtifacts = append(payloadArtifacts, promptArtifacts...)
			payloadArtifacts = append(payloadArtifacts, imageArtifacts...)
		} else {
			structural, err = state.recordCompletedOp(tsUs, "llm", "message")
			if err != nil {
				return nil, err
			}
			payloadArtifacts, err = codexPointerArtifactsFromDocument(state, lineNo, ClassAssistantMessage, pointerPrefix, payloadDoc, textPointersFromDocument(payloadDoc, "content", pointerPrefix))
			if err != nil {
				return nil, err
			}
		}
		return append(structural, payloadArtifacts...), nil
	case "reasoning":
		structural, err := state.recordCompletedOp(tsUs, "reasoning", "reasoning")
		if err != nil {
			return nil, err
		}
		pointers := append(textPointersFromDocument(payloadDoc, "summary", pointerPrefix), textPointersFromDocument(payloadDoc, "content", pointerPrefix)...)
		payloadArtifacts, err := codexPointerArtifactsFromDocument(state, lineNo, ClassReasoningText, pointerPrefix, payloadDoc, pointers)
		return append(structural, payloadArtifacts...), err
	case "function_call", "custom_tool_call", "local_shell_call", "tool_search_call",
		"image_generation_call":
		name, namespace := codexSourceToolNameNamespace(body.Type, body.Name)
		state.recordToolStart(tsUs, name, namespace, body.CallID)
		field := "arguments"
		if body.Type == "local_shell_call" {
			field = "action"
		}
		return codexPointerArtifactsFromDocument(state, lineNo, ClassToolRequest, pointerPrefix, payloadDoc, scalarPointersFromDocument(payloadDoc, field, pointerPrefix))
	case "web_search_call":
		state.recordWebSearchStart(tsUs)
		return []Artifact{codexRawLineArtifact(state, line, lineNo, ClassToolRequest)}, nil
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "tool_search_output":
		structural, err := state.recordToolOutput(tsUs, body.CallID)
		if err != nil {
			return nil, err
		}
		payloadArtifacts, err := codexPointerArtifactsFromDocument(state, lineNo, ClassToolResponse, pointerPrefix, payloadDoc, scalarPointersFromDocument(payloadDoc, "output", pointerPrefix))
		return append(structural, payloadArtifacts...), err
	case "compaction", "context_compaction":
		return state.recordCompaction(tsUs, lineNo, codexCompactionEventMeta{Trigger: "auto"})
	default:
		return nil, fmt.Errorf("unknown codex response_item payload type %q", body.Type)
	}
}

func codexDirectResponseItemType(kind string) bool {
	switch kind {
	case "message", "reasoning", "function_call", "function_call_output",
		"custom_tool_call", "custom_tool_call_output", "tool_search_call",
		"tool_search_output", "web_search_call", "image_generation_call",
		"compaction", "context_compaction", "local_shell_call",
		"local_shell_call_output":
		return true
	default:
		return false
	}
}

func codexDirectResponseItemNoOp(kind string) bool {
	return kind == "ghost_snapshot"
}

func codexSourceToolNameNamespace(kind string, name string) (string, string) {
	switch kind {
	case "web_search_call":
		return "web_search", "web"
	case "image_generation_call":
		return "image_generation", "media"
	case "custom_tool_call":
		return name, "custom"
	case "local_shell_call":
		if name != "" {
			return name, "shell"
		}
		return "shell", "shell"
	case "tool_search_call":
		if name != "" {
			return name, "custom"
		}
		return "tool_search", "custom"
	default:
		if name != "" {
			return name, codexSourceNamespaceForName(name)
		}
		return kind, codexSourceNamespaceForName(kind)
	}
}

func codexPayloadHeaderFromDocument(document interface{}) codexPayloadHeader {
	return codexPayloadHeader{
		Type:   codexDocumentStringField(document, "type"),
		Role:   codexDocumentStringField(document, "role"),
		Name:   codexDocumentStringField(document, "name"),
		CallID: codexDocumentStringField(document, "call_id"),
	}
}

func codexDocumentStringField(document interface{}, field string) string {
	obj, ok := document.(map[string]interface{})
	if !ok {
		return ""
	}
	value, ok := obj[field].(string)
	if !ok {
		return ""
	}
	return value
}

func codexSourceNamespaceForName(name string) string {
	switch {
	case name == "shell" || name == "shell_command" || strings.HasPrefix(name, "exec"):
		return "shell"
	case name == "apply_patch":
		return "fs"
	case name == "read" || name == "write" || name == "edit" || name == "list_dir":
		return "fs"
	case name == "view_image":
		return "fs"
	default:
		return "custom"
	}
}

func extractCodexEventMsgArtifacts(state *codexSourceState, line []byte, lineNo int64, tsUs int64, payload json.RawMessage) ([]Artifact, error) {
	payloadDoc, err := decodeCodexPayloadDocument(payload)
	if err != nil {
		return nil, fmt.Errorf("decode event_msg payload document: %w", err)
	}
	bodyType := codexDocumentStringField(payloadDoc, "type")
	if bodyType == "ghost_snapshot" {
		return nil, nil
	}
	state.noteContentTimestamp(tsUs)
	switch bodyType {
	case "user_message":
		return extractCodexUserMessageEventArtifacts(state, lineNo, payloadDoc)
	case "agent_message", "agent_reasoning", "agent_reasoning_raw_content":
		return codexAgentEventLog(state, tsUs, bodyType), nil
	case "error", "collab_close_end", "collab_waiting_end":
		return extractCodexMessageEventLog(state, lineNo, tsUs, payloadDoc, bodyType)
	case "task_started", "turn_started":
		return state.recordTaskStarted(tsUs, payload)
	case "task_complete", "turn_complete":
		return state.recordTaskComplete(tsUs, payload)
	case "turn_aborted":
		return state.recordTurnAborted(tsUs, payload)
	case "web_search_end":
		return state.recordWebSearchEnd(tsUs)
	case "collab_agent_spawn_end":
		return state.recordCollabSpawn(tsUs, payload)
	case "patch_apply_end":
		return state.recordPatchApplyEnd(tsUs, payload)
	case "exec_command_end":
		return state.recordExecCommandEnd(payload)
	case "mcp_tool_call_end":
		return state.recordMcpToolCallEnd(tsUs, payload)
	case "image_generation_end":
		return state.recordImageGenerationEnd(tsUs, payload)
	case "context_compacted":
		return state.recordContextCompacted(tsUs, line, lineNo)
	case "token_count":
		return nil, nil
	case "thread_rolled_back":
		return state.genericLogEntryWithSystemOp(tsUs, "INF", "thread_rolled_back")
	case "entered_review_mode", "exited_review_mode":
		return state.genericLogEntryWithSystemOp(tsUs, "INF", bodyType)
	case "item_completed":
		return state.genericLogEntryWithSystemOp(tsUs, "INF", "item_completed")
	case "thread_goal_updated", "guardian_assessment",
		"view_image_tool_call", "dynamic_tool_call_request",
		"dynamic_tool_call_response":
		return state.genericLogEntryWithSystemOp(tsUs, "DBG", "event_msg:"+bodyType)
	default:
		return nil, fmt.Errorf("unknown codex event_msg payload type %q", bodyType)
	}
}

func extractCodexUserMessageEventArtifacts(state *codexSourceState, lineNo int64, payloadDoc interface{}) ([]Artifact, error) {
	promptArtifacts, err := codexPointerArtifactsFromDocument(state, lineNo, ClassUserPrompt, "/payload", payloadDoc, scalarPointersFromDocument(payloadDoc, "message", "/payload"))
	if err != nil {
		return nil, err
	}
	imageArtifacts, err := codexPointerArtifactsFromDocument(state, lineNo, ClassUserImage, "/payload", payloadDoc, userMessageImagePointersFromDocument(payloadDoc, "/payload"))
	if err != nil {
		return nil, err
	}
	out := make([]Artifact, 0, len(promptArtifacts)+len(imageArtifacts))
	out = append(out, promptArtifacts...)
	out = append(out, imageArtifacts...)
	return out, nil
}

func codexAgentEventLog(state *codexSourceState, tsUs int64, typ string) []Artifact {
	message := "agent_reasoning"
	if typ == "agent_message" {
		message = "agent_message"
	}
	return []Artifact{state.genericLogEntry(tsUs, "DBG", message)}
}

func extractCodexMessageEventLog(state *codexSourceState, lineNo int64, tsUs int64, payloadDoc interface{}, typ string) ([]Artifact, error) {
	pointers := scalarPointersFromDocument(payloadDoc, "message", "/payload")
	if len(pointers) > 0 {
		return codexPointerArtifactsFromDocument(state, lineNo, ClassLogEntry, "/payload", payloadDoc, pointers)
	}
	severity := "DBG"
	message := "event_msg:" + typ
	if typ == "error" {
		severity = "ERR"
		message = "error"
	}
	return []Artifact{state.genericLogEntry(tsUs, severity, message)}, nil
}

func codexExecCommandStatus(exitCode *int64) (string, string) {
	if exitCode == nil {
		return "", ""
	}
	if *exitCode == 0 {
		return "completed", ""
	}
	return "failed", "command_failed"
}

func codexMcpResultStatus(result json.RawMessage) (string, string) {
	body := bytes.TrimSpace(result)
	if len(body) == 0 {
		return "completed", ""
	}
	var res struct {
		Err json.RawMessage `json:"Err"`
		Ok  struct {
			IsError bool `json:"is_error"`
		} `json:"Ok"`
	}
	if json.Unmarshal(body, &res) != nil {
		return "completed", ""
	}
	if len(bytes.TrimSpace(res.Err)) > 0 || res.Ok.IsError {
		return "failed", "tool_error"
	}
	return "completed", ""
}

func codexPatchApplyStatus(success *bool, status string) (string, string) {
	if success != nil && !*success {
		return "failed", "patch_failed"
	}
	switch status {
	case "failed", "error":
		return "failed", "patch_failed"
	default:
		return "completed", ""
	}
}

func codexSpawnStatus(status string) string {
	switch status {
	case "failed", "error":
		return "failed"
	default:
		return "completed"
	}
}

func codexCompletedAtMicros(payload json.RawMessage) (int64, error) {
	var body struct {
		CompletedAt json.RawMessage `json:"completed_at"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return 0, fmt.Errorf("decode task_complete payload: %w", err)
	}
	raw := bytes.TrimSpace(body.CompletedAt)
	if len(raw) == 0 {
		return 0, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return parseCodexSourceTimestamp(text)
	}
	var seconds int64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		return seconds * 1_000_000, nil
	}
	return 0, fmt.Errorf("decode task_complete completed_at: unsupported value %s", string(raw))
}

func codexStartedAtMicros(payload json.RawMessage) (int64, error) {
	var body struct {
		StartedAt json.RawMessage `json:"started_at"`
	}
	if err := decodeJSONPayload(payload, &body); err != nil {
		return 0, fmt.Errorf("decode task_started payload: %w", err)
	}
	raw := bytes.TrimSpace(body.StartedAt)
	if len(raw) == 0 {
		return 0, nil
	}
	var seconds int64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		return seconds * 1_000_000, nil
	}
	return 0, fmt.Errorf("decode task_started started_at: unsupported value %s", string(raw))
}

func codexPointerArtifactsFromDocument(state *codexSourceState, lineNo int64, class ArtifactClass, pointerPrefix string, document interface{}, pointers []string) ([]Artifact, error) {
	artifacts := make([]Artifact, 0, len(pointers))
	for _, pointer := range pointers {
		documentPointer, pointerErr := codexDocumentPointer(pointer, pointerPrefix)
		var resolved resolvedPayload
		resolveErr := pointerErr
		if resolveErr == nil {
			resolved, resolveErr = resolveDecodedJSONPointerPayload(document, documentPointer)
		}
		artifacts = append(artifacts, codexPointerArtifactFromResolved(state, lineNo, class, pointer, resolved, resolveErr))
	}
	return artifacts, nil
}

func codexDocumentPointer(pointer string, pointerPrefix string) (string, error) {
	if pointerPrefix == "" {
		return pointer, nil
	}
	if pointer == pointerPrefix {
		return "", nil
	}
	prefix := pointerPrefix + "/"
	if strings.HasPrefix(pointer, prefix) {
		return strings.TrimPrefix(pointer, pointerPrefix), nil
	}
	return "", fmt.Errorf("json pointer %q does not match payload prefix %q", pointer, pointerPrefix)
}

func codexRawLineArtifact(state *codexSourceState, line []byte, lineNo int64, class ArtifactClass) Artifact {
	nativeSessionID := state.nativeSessionID
	if nativeSessionID == "" {
		nativeSessionID = "source:" + state.sourceID
	}
	availability := AvailabilityAvailable
	if len(line) == 0 {
		availability = AvailabilitySourceEmpty
	}
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "codex",
		SourceID:         state.sourceID,
		SourceFile:       state.sourceFile,
		NativeSessionID:  nativeSessionID,
		NativeArtifactID: fmt.Sprintf("line:%d", lineNo),
		Class:            class,
		Availability:     availability,
		HashDomain:       HashRawBytes,
		Selector:         Selector{URI: codexLineSelectorURI(state.sourceFile, lineNo)},
		Bytes:            int64(len(line)),
		Chars:            -1,
		ComputedSHA256:   stringSHA256(string(line)),
		Synthetic:        false,
		SyntheticReason:  "",
	}
}

func codexSemanticLineArtifact(state *codexSourceState, line []byte, lineNo int64, class ArtifactClass) Artifact {
	nativeSessionID := state.nativeSessionID
	if nativeSessionID == "" {
		nativeSessionID = "source:" + state.sourceID
	}
	availability := AvailabilityAvailable
	if len(line) == 0 {
		availability = AvailabilitySourceEmpty
	}
	chars := int64(-1)
	if utf8.Valid(line) {
		chars = int64(utf8.RuneCount(line))
	}
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "codex",
		SourceID:         state.sourceID,
		SourceFile:       state.sourceFile,
		NativeSessionID:  nativeSessionID,
		NativeArtifactID: fmt.Sprintf("line:%d", lineNo),
		Class:            class,
		Availability:     availability,
		HashDomain:       HashSemanticText,
		Selector:         Selector{URI: codexLineSelectorURI(state.sourceFile, lineNo)},
		Bytes:            int64(len(line)),
		Chars:            chars,
		ComputedSHA256:   stringSHA256(string(line)),
		Synthetic:        false,
		SyntheticReason:  "",
	}
}

func (s *codexSourceState) genericLogEntry(tsUs int64, severity string, message string) Artifact {
	nativeSessionID := s.nativeSessionID
	if nativeSessionID == "" {
		nativeSessionID = "source:" + s.sourceID
	}
	scope := "source"
	nativeTurnID := ""
	if nativeSessionID != "source:"+s.sourceID {
		scope = "session"
	}
	if s.activeTurn != nil && s.activeTurn.seq > 0 {
		nativeTurnID = fmt.Sprintf("turn:%d", s.activeTurn.seq)
		scope = nativeTurnID
	}
	nativeArtifactID := logNativeArtifactID(scope, tsUs, severity, "codex", message)
	messageBytes := []byte(message)
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "codex",
		SourceID:         s.sourceID,
		SourceFile:       s.sourceFile,
		NativeSessionID:  nativeSessionID,
		NativeTurnID:     nativeTurnID,
		NativeArtifactID: nativeArtifactID,
		Class:            ClassLogEntry,
		Availability:     logAvailability(messageBytes),
		HashDomain:       HashSemanticText,
		Selector:         Selector{URI: logSelectorURI(s.sourceID, nativeArtifactID)},
		Bytes:            int64(len(messageBytes)),
		Chars:            int64(utf8.RuneCount(messageBytes)),
		ComputedSHA256:   stringSHA256(message),
		Synthetic:        false,
		SyntheticReason:  "",
	}
}

func codexPointerArtifactFromResolved(state *codexSourceState, lineNo int64, class ArtifactClass, pointer string, resolved resolvedPayload, err error) Artifact {
	hashDomain := resolved.hashDomain
	if hashDomain == "" {
		hashDomain = HashSemanticText
	}
	availability := AvailabilityAvailable
	bytesLen := int64(len(resolved.bytes))
	hash := stringSHA256(string(resolved.bytes))
	chars := int64(-1)
	if err != nil {
		availability = AvailabilityUnverifiable
		bytesLen = -1
		hash = ""
	} else if bytesLen == 0 {
		availability = AvailabilitySourceEmpty
	}
	if err == nil && resolved.hashDomain == HashSemanticText && utf8.Valid(resolved.bytes) {
		chars = int64(utf8.RuneCount(resolved.bytes))
	}
	nativeSessionID := state.nativeSessionID
	if nativeSessionID == "" {
		nativeSessionID = "source:" + state.sourceID
	}
	selectorURI := codexLineSelectorURI(state.sourceFile, lineNo)
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "codex",
		SourceID:         state.sourceID,
		SourceFile:       state.sourceFile,
		NativeSessionID:  nativeSessionID,
		NativeArtifactID: fmt.Sprintf("line:%d:%s", lineNo, pointer),
		Class:            class,
		Availability:     availability,
		HashDomain:       hashDomain,
		Selector:         Selector{URI: selectorURI, JSONPointer: pointer},
		Bytes:            bytesLen,
		Chars:            chars,
		ComputedSHA256:   hash,
		Synthetic:        false,
		SyntheticReason:  "",
	}
}

func resolveDecodedJSONPointerPayload(document interface{}, pointer string) (resolvedPayload, error) {
	value, err := jsonPointerValue(document, pointer)
	if err != nil {
		return resolvedPayload{}, err
	}
	if text, ok := value.(string); ok {
		return resolvedPayload{bytes: []byte(text), hashDomain: HashSemanticText}, nil
	}
	canonical, err := canonicalIdentityBytes(value)
	if err != nil {
		return resolvedPayload{}, err
	}
	return resolvedPayload{bytes: canonical, hashDomain: HashCanonicalJSON}, nil
}

func codexLineSelectorURI(path string, lineNo int64) string {
	return (&url.URL{
		Scheme:   "file",
		Path:     path,
		Fragment: fmt.Sprintf("L%d", lineNo),
	}).String()
}

func textPointers(payload json.RawMessage, field string, prefix string) []string {
	document, err := decodeCodexPayloadDocument(payload)
	if err != nil {
		return nil
	}
	return textPointersFromDocument(document, field, prefix)
}

func textPointersFromDocument(document interface{}, field string, prefix string) []string {
	obj, ok := document.(map[string]interface{})
	if !ok {
		return nil
	}
	rawItems, ok := obj[field].([]interface{})
	if !ok {
		return nil
	}
	pointers := make([]string, 0, len(rawItems))
	for i, rawItem := range rawItems {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := item["text"].(string); ok {
			pointers = append(pointers, fmt.Sprintf("%s/%s/%d/text", prefix, field, i))
		}
	}
	return pointers
}

func imageContentPointersFromDocument(document interface{}, field string, prefix string) []string {
	obj, ok := document.(map[string]interface{})
	if !ok {
		return nil
	}
	rawItems, ok := obj[field].([]interface{})
	if !ok {
		return nil
	}
	pointers := make([]string, 0, len(rawItems))
	for i, rawItem := range rawItems {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		if codexSourceImageContentItem(item) {
			pointers = append(pointers, fmt.Sprintf("%s/%s/%d", prefix, field, i))
		}
	}
	return pointers
}

func userMessageImagePointersFromDocument(document interface{}, prefix string) []string {
	obj, ok := document.(map[string]interface{})
	if !ok {
		return nil
	}
	var pointers []string
	for _, field := range []string{"images", "local_images", "image_details"} {
		pointers = append(pointers, indexedFieldPointers(obj, field, prefix)...)
	}
	return pointers
}

func indexedFieldPointers(obj map[string]interface{}, field string, prefix string) []string {
	raw, ok := obj[field]
	if !ok {
		return nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return []string{prefix + "/" + field}
	}
	pointers := make([]string, 0, len(items))
	for i := range items {
		pointers = append(pointers, fmt.Sprintf("%s/%s/%d", prefix, field, i))
	}
	return pointers
}

func codexSourceImageContentItem(item map[string]interface{}) bool {
	if typ, ok := item["type"].(string); ok && strings.Contains(strings.ToLower(typ), "image") {
		return true
	}
	for _, field := range []string{"image", "image_url", "image_urls", "local_image", "local_images", "image_details"} {
		if _, ok := item[field]; ok {
			return true
		}
	}
	return false
}

func scalarPointers(payload json.RawMessage, field string, prefix string) []string {
	document, err := decodeCodexPayloadDocument(payload)
	if err != nil {
		return nil
	}
	return scalarPointersFromDocument(document, field, prefix)
}

func scalarPointersFromDocument(document interface{}, field string, prefix string) []string {
	obj, ok := document.(map[string]interface{})
	if !ok {
		return nil
	}
	if _, ok := obj[field]; !ok {
		return nil
	}
	return []string{prefix + "/" + field}
}

func decodeCodexPayloadDocument(payload json.RawMessage) (interface{}, error) {
	body := bytes.TrimSpace(payload)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return nil, nil
	}
	var document interface{}
	if err := decodeJSON(body, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func decodeJSONPayload(payload json.RawMessage, dst interface{}) error {
	body := bytes.TrimSpace(payload)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return nil
	}
	return decodeJSON(body, dst)
}

func decodeJSON(body []byte, dst interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	return nil
}
