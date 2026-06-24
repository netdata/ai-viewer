package parity

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

type claudeCodeAttachmentMetadataIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq"`
	AttachmentType  string `json:"attachment_type,omitempty"`
	Filename        string `json:"filename,omitempty"`
	DisplayPath     string `json:"display_path,omitempty"`
}

type claudeCodePRLinkIdentity struct {
	Number     int64  `json:"number"`
	URL        string `json:"url,omitempty"`
	Repository string `json:"repository,omitempty"`
}

type claudeCodeSessionMetadataIdentity struct {
	NativeSessionID       string                     `json:"native_session_id"`
	LastPromptSHA256      string                     `json:"last_prompt_sha256,omitempty"`
	CustomTitle           string                     `json:"custom_title,omitempty"`
	AITitle               string                     `json:"ai_title,omitempty"`
	PermissionMode        string                     `json:"permission_mode,omitempty"`
	BridgeSessionID       string                     `json:"bridge_session_id,omitempty"`
	BridgeLastSequenceNum int64                      `json:"bridge_last_sequence_num,omitempty"`
	FileHistorySHA256     string                     `json:"file_history_sha256,omitempty"`
	PRLinks               []claudeCodePRLinkIdentity `json:"pr_links,omitempty"`
}

type claudeCodeSessionMetadataState struct {
	lastPromptSHA256      string
	customTitle           string
	aiTitle               string
	permissionMode        string
	bridgeSessionID       string
	bridgeLastSequenceNum int64
	fileHistory           map[string]any
	fileHistorySHA256     string
	prLinks               []claudeCodePRLinkIdentity
}

type compactionEventIdentity struct {
	NativeSessionID         string `json:"native_session_id"`
	TurnSeq                 int64  `json:"turn_seq"`
	OpSeq                   int64  `json:"op_seq"`
	Trigger                 string `json:"trigger,omitempty"`
	PreTokens               int64  `json:"pre_tokens"`
	PostTokens              int64  `json:"post_tokens"`
	MetadataPreTokens       int64  `json:"metadata_pre_tokens"`
	MetadataPostTokens      int64  `json:"metadata_post_tokens"`
	DurationMs              int64  `json:"duration_ms"`
	StartedAt               int64  `json:"started_at"`
	EndedAt                 *int64 `json:"ended_at,omitempty"`
	PreservedSegmentSHA256  string `json:"preserved_segment_sha256,omitempty"`
	PreservedMessagesSHA256 string `json:"preserved_messages_sha256,omitempty"`
}

func (s *claudeCodeSourceState) sessionBoundary() (Artifact, error) {
	identity := sessionBoundaryIdentity{
		NativeSessionID:       s.nativeSessionID,
		ParentNativeSessionID: s.parentNativeSessionID,
		RootNativeSessionID:   s.rootNativeSessionID,
		Kind:                  s.sessionKind,
		Status:                "running",
		StartedAt:             s.sessionStartedAt,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeArtifactID: "session:" + s.nativeSessionID,
		class:            ClassSessionBoundary,
		selectorURI:      s.lineSelectorURI(1),
		identity:         identity,
	})
}

func (s *claudeCodeSourceState) recordClaudeCodeSessionMetadata(rec claudeCodeSourceRecord) error {
	switch rec.Type {
	case "last-prompt":
		if rec.LastPrompt != "" {
			s.sessionMetadata.lastPromptSHA256 = stringSHA256(rec.LastPrompt)
		}
	case "ai-title":
		s.sessionMetadata.aiTitle = rec.AITitle
	case "custom-title":
		s.sessionMetadata.customTitle = rec.CustomTitle
	case "permission-mode":
		s.sessionMetadata.permissionMode = rec.PermissionMode
	case "bridge-session":
		s.sessionMetadata.bridgeSessionID = rec.BridgeSessionID
		if rec.LastSequenceNum != nil {
			s.sessionMetadata.bridgeLastSequenceNum = *rec.LastSequenceNum
		}
	case "pr-link":
		link := claudeCodePRLinkIdentity{
			URL:        rec.PRURL,
			Repository: rec.PRRepository,
		}
		if rec.PRNumber != nil {
			link.Number = *rec.PRNumber
		}
		if link.Number != 0 || link.URL != "" || link.Repository != "" {
			s.sessionMetadata.prLinks = append(s.sessionMetadata.prLinks, link)
		}
	case "file-history-snapshot":
		if len(bytes.TrimSpace(rec.Snapshot.TrackedFileBackups)) > 0 && !bytes.Equal(bytes.TrimSpace(rec.Snapshot.TrackedFileBackups), []byte("{}")) {
			backups, err := claudeCodeTrackedFileBackups(rec.Snapshot.TrackedFileBackups)
			if err != nil {
				return fmt.Errorf("decode file-history metadata: %w", err)
			}
			s.sessionMetadata.fileHistory = mergeClaudeCodeJSONPatchObject(s.sessionMetadata.fileHistory, backups)
			if len(s.sessionMetadata.fileHistory) == 0 {
				s.sessionMetadata.fileHistorySHA256 = ""
				return nil
			}
			hash, err := canonicalJSONHashValue(s.sessionMetadata.fileHistory)
			if err != nil {
				return fmt.Errorf("hash file-history metadata: %w", err)
			}
			s.sessionMetadata.fileHistorySHA256 = hash
		}
	}
	return nil
}

func (s *claudeCodeSourceState) sessionMetadataArtifact() (Artifact, bool, error) {
	identity, ok := s.claudeCodeSessionMetadataIdentity()
	if !ok {
		return Artifact{}, false, nil
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          claudeCodeFormat,
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeArtifactID: "session:" + s.nativeSessionID + ":metadata",
		class:            ClassSessionMetadata,
		selectorURI:      (&url.URL{Scheme: "file", Path: s.sourceFile, Fragment: "metadata"}).String(),
		identity:         identity,
	})
	return artifact, true, err
}

func (s *claudeCodeSourceState) claudeCodeSessionMetadataIdentity() (claudeCodeSessionMetadataIdentity, bool) {
	meta := s.sessionMetadata
	ok := meta.lastPromptSHA256 != "" ||
		meta.customTitle != "" ||
		meta.aiTitle != "" ||
		meta.permissionMode != "" ||
		meta.bridgeSessionID != "" ||
		meta.bridgeLastSequenceNum != 0 ||
		meta.fileHistorySHA256 != "" ||
		len(meta.prLinks) > 0
	if !ok {
		return claudeCodeSessionMetadataIdentity{}, false
	}
	return claudeCodeSessionMetadataIdentity{
		NativeSessionID:       s.nativeSessionID,
		LastPromptSHA256:      meta.lastPromptSHA256,
		CustomTitle:           meta.customTitle,
		AITitle:               meta.aiTitle,
		PermissionMode:        meta.permissionMode,
		BridgeSessionID:       meta.bridgeSessionID,
		BridgeLastSequenceNum: meta.bridgeLastSequenceNum,
		FileHistorySHA256:     meta.fileHistorySHA256,
		PRLinks:               meta.prLinks,
	}, true
}

func canonicalJSONHash(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	body, err := canonicalIdentityBytes(value)
	if err != nil {
		return "", err
	}
	return stringSHA256(string(body)), nil
}

func canonicalJSONHashValue(value any) (string, error) {
	body, err := canonicalIdentityBytes(value)
	if err != nil {
		return "", err
	}
	return stringSHA256(string(body)), nil
}

func claudeCodeTrackedFileBackups(raw json.RawMessage) (map[string]any, error) {
	var backups map[string]any
	if err := json.Unmarshal(raw, &backups); err != nil {
		return nil, err
	}
	return backups, nil
}

func mergeClaudeCodeJSONPatchObject(base map[string]any, patch map[string]any) map[string]any {
	out := cloneClaudeCodeJSONMap(base)
	for key, value := range patch {
		if value == nil {
			delete(out, key)
			continue
		}
		patchMap, patchIsMap := value.(map[string]any)
		if patchIsMap {
			var existingMap map[string]any
			if typed, ok := out[key].(map[string]any); ok {
				existingMap = typed
			}
			out[key] = mergeClaudeCodeJSONPatchObject(existingMap, patchMap)
			continue
		}
		out[key] = cloneClaudeCodeJSONValue(value)
	}
	return out
}

func cloneClaudeCodeJSONMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneClaudeCodeJSONValue(value)
	}
	return out
}

func cloneClaudeCodeJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneClaudeCodeJSONMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneClaudeCodeJSONValue(item)
		}
		return out
	default:
		return value
	}
}

func (s *claudeCodeSourceState) turnBoundary(status string, endedAt int64) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", s.turnSeq)
	var end *int64
	if endedAt > 0 {
		end = &endedAt
	}
	identity := turnBoundaryIdentity{
		NativeSessionID: s.nativeSessionID,
		TurnSeq:         s.turnSeq,
		Status:          status,
		StartedAt:       s.turnStartedAt,
		EndedAt:         end,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeTurnID,
		class:            ClassTurnBoundary,
		selectorURI:      s.lineSelectorURI(1),
		identity:         identity,
	})
}

func (s *claudeCodeSourceState) opBoundary(opSeq int64, kind string, name string, status string, startedAt int64, endedAt *int64) (Artifact, error) {
	return s.opBoundaryAtTurn(s.turnSeq, opSeq, kind, name, "", status, startedAt, endedAt)
}

func (s *claudeCodeSourceState) opBoundaryAtTurn(turnSeq int64, opSeq int64, kind string, name string, namespace string, status string, startedAt int64, endedAt *int64) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
	identity := opBoundaryIdentity{
		NativeSessionID: s.nativeSessionID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		Kind:            kind,
		Name:            name,
		ToolNamespace:   namespace,
		Status:          status,
		StartedAt:       startedAt,
		EndedAt:         endedAt,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID,
		class:            ClassOpBoundary,
		selectorURI:      s.lineSelectorURI(1),
		identity:         identity,
	})
}

func (s *claudeCodeSourceState) subagentLink(parentOpSeq int64, childNativeID string, lineNo int64) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", s.turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", s.turnSeq, parentOpSeq)
	nativeArtifactID := nativeOpID + ":child_session:" + childNativeID
	identity := subagentLinkIdentity{
		ParentNativeSessionID: s.nativeSessionID,
		ParentTurnSeq:         s.turnSeq,
		ParentOpSeq:           parentOpSeq,
		ChildNativeSessionID:  childNativeID,
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeArtifactID,
		class:            ClassSubagentLink,
		selectorURI:      s.lineSelectorURI(lineNo),
		identity:         identity,
	})
}

func (s *claudeCodeSourceState) attachmentMetadata(line []byte, lineNo int64) (Artifact, error) {
	identity, err := claudeCodeAttachmentMetadataFromSource(line, s.nativeSessionID, s.turnSeq)
	if err != nil {
		return Artifact{}, err
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeTurnID:     fmt.Sprintf("turn:%d", s.turnSeq),
		nativeArtifactID: fmt.Sprintf("line:%d:/attachment", lineNo),
		class:            ClassAttachmentMetadata,
		selectorURI:      s.lineSelectorURI(lineNo),
		identity:         identity,
	})
}

func (s *claudeCodeSourceState) compactionEvent(opSeq int64, meta *claudeCodeCompactMeta, startedAt int64, endedAt int64, lineNo int64) (Artifact, error) {
	identity, err := claudeCodeCompactionEventIdentity(s.nativeSessionID, s.turnSeq, opSeq, meta, metaPreTokens(meta), metaPostTokens(meta), startedAt, &endedAt)
	if err != nil {
		return Artifact{}, err
	}
	nativeTurnID := fmt.Sprintf("turn:%d", s.turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", s.turnSeq, opSeq)
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID + ":compaction",
		class:            ClassCompactionEvent,
		selectorURI:      s.lineSelectorURI(lineNo),
		identity:         identity,
	})
}

func (s *claudeCodeSourceState) systemOp(rec claudeCodeSourceRecord, lineNo int64, tsUs int64) (Artifact, error) {
	severity, message := claudeCodeSystemLogSeverityMessage(rec.Subtype)
	nativeArtifactID := claudeCodeSourceSystemOpNativeArtifactID(lineNo)
	identity := claudeCodeSystemOpIdentity{
		NativeSessionID: s.nativeSessionID,
		TurnSeq:         s.turnSeq,
		Subtype:         rec.Subtype,
		Severity:        severity,
		Message:         message,
		Timestamp:       tsUs,
		ContentSHA256:   optionalStringSHA256(rec.Content),
	}
	nativeTurnID := ""
	if s.turnSeq > 0 {
		nativeTurnID = fmt.Sprintf("turn:%d", s.turnSeq)
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "claude-code",
		sourceFile:       s.sourceFile,
		nativeSessionID:  s.nativeSessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeArtifactID,
		class:            ClassSystemOp,
		selectorURI:      s.lineSelectorURI(lineNo),
		identity:         identity,
	})
}

func (s *claudeCodeSourceState) logEntry(message string, lineNo int64) Artifact {
	nativeArtifactID := claudeCodeSourceLogNativeArtifactID(lineNo)
	nativeTurnID := ""
	if s.turnSeq > 0 {
		nativeTurnID = fmt.Sprintf("turn:%d", s.turnSeq)
	}
	messageBytes := []byte(message)
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "claude-code",
		SourceID:         s.sourceID,
		SourceFile:       s.sourceFile,
		NativeSessionID:  s.nativeSessionID,
		NativeTurnID:     nativeTurnID,
		NativeArtifactID: nativeArtifactID,
		Class:            ClassLogEntry,
		Availability:     logAvailability(messageBytes),
		HashDomain:       HashSemanticText,
		Selector: Selector{
			URI: s.lineSelectorURI(lineNo),
		},
		Bytes:          int64(len(messageBytes)),
		Chars:          int64(utf8.RuneCount(messageBytes)),
		ComputedSHA256: stringSHA256(message),
	}
}

func claudeCodeSourceLogNativeArtifactID(lineNo int64) string {
	return fmt.Sprintf("line:%d:/log", lineNo)
}

func claudeCodeSourceSystemOpNativeArtifactID(lineNo int64) string {
	return fmt.Sprintf("line:%d:/system", lineNo)
}

func claudeCodeSystemLogSeverityMessage(subtype string) (string, string) {
	if subtype == "stop_hook_summary" {
		return "DBG", "stop_hook_summary"
	}
	return "INF", "system:" + subtype
}

func (s *claudeCodeSourceState) inlineArtifact(line []byte, lineNo int64, pointer string, class ArtifactClass) ([]Artifact, error) {
	return s.inlineArtifactAtTurn(s.turnSeq, line, lineNo, pointer, class)
}

func (s *claudeCodeSourceState) inlineArtifactAtTurn(turnSeq int64, line []byte, lineNo int64, pointer string, class ArtifactClass) ([]Artifact, error) {
	resolved, err := resolveJSONPointerPayload(line, pointer)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(resolved.bytes)
	hash := fmt.Sprintf("%x", sum)
	availability := AvailabilityAvailable
	if len(resolved.bytes) == 0 && hash == EmptySHA256 {
		availability = AvailabilitySourceEmpty
	}
	return []Artifact{{
		SchemaVersion:    SchemaVersion,
		Adapter:          "claude-code",
		SourceID:         s.sourceID,
		SourceFile:       s.sourceFile,
		NativeSessionID:  s.nativeSessionID,
		NativeTurnID:     fmt.Sprintf("turn:%d", turnSeq),
		NativeArtifactID: fmt.Sprintf("line:%d:%s", lineNo, pointer),
		Class:            class,
		Availability:     availability,
		HashDomain:       resolved.hashDomain,
		Selector: Selector{
			URI:         s.lineSelectorURI(lineNo),
			JSONPointer: pointer,
		},
		Bytes:          int64(len(resolved.bytes)),
		Chars:          claudeCodePayloadChars(resolved),
		ComputedSHA256: hash,
	}}, nil
}

func (s *claudeCodeSourceState) sourceCorruptionArtifact(line []byte, lineNo int64, field string, expected string, actual string) Artifact {
	sum := sha256.Sum256(line)
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "claude-code",
		SourceID:         s.sourceID,
		SourceFile:       s.sourceFile,
		NativeSessionID:  s.nativeSessionID,
		NativeArtifactID: fmt.Sprintf("source_corruption:line:%d", lineNo),
		Class:            ClassSourceCorruption,
		Availability:     AvailabilitySourceCorrupt,
		HashDomain:       HashRawBytes,
		Selector: Selector{
			URI: s.lineSelectorURI(lineNo),
		},
		Bytes:          int64(len(line)),
		Chars:          -1,
		ComputedSHA256: fmt.Sprintf("%x", sum),
		IntegrityFailures: []IntegrityFailure{{
			Field:    field,
			Expected: expected,
			Actual:   actual,
		}},
	}
}

func (s *claudeCodeSourceState) lineSelectorURI(lineNo int64) string {
	return (&url.URL{Scheme: "file", Path: s.sourceFile, Fragment: fmt.Sprintf("L%d", lineNo)}).String()
}

func claudeCodeCompactionEventIdentity(
	nativeSessionID string,
	turnSeq int64,
	opSeq int64,
	meta *claudeCodeCompactMeta,
	preTokens int64,
	postTokens int64,
	startedAt int64,
	endedAt *int64,
) (compactionEventIdentity, error) {
	identity := compactionEventIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PreTokens:       preTokens,
		PostTokens:      postTokens,
		StartedAt:       startedAt,
		EndedAt:         endedAt,
	}
	if meta == nil {
		return identity, nil
	}
	identity.Trigger = meta.Trigger
	identity.MetadataPreTokens = meta.PreTokens
	identity.MetadataPostTokens = meta.PostTokens
	identity.DurationMs = meta.DurationMs
	preservedSegmentHash, err := optionalCanonicalJSONSHA256(meta.PreservedSegment)
	if err != nil {
		return compactionEventIdentity{}, fmt.Errorf("hash preservedSegment: %w", err)
	}
	preservedMessagesHash, err := optionalCanonicalJSONSHA256(meta.PreservedMessages)
	if err != nil {
		return compactionEventIdentity{}, fmt.Errorf("hash preservedMessages: %w", err)
	}
	identity.PreservedSegmentSHA256 = preservedSegmentHash
	identity.PreservedMessagesSHA256 = preservedMessagesHash
	return identity, nil
}

func metaPreTokens(meta *claudeCodeCompactMeta) int64 {
	if meta == nil {
		return 0
	}
	return meta.PreTokens
}

func metaPostTokens(meta *claudeCodeCompactMeta) int64 {
	if meta == nil {
		return 0
	}
	return meta.PostTokens
}

func optionalCanonicalJSONSHA256(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var value interface{}
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", err
	}
	body, err := canonicalIdentityBytes(value)
	if err != nil {
		return "", err
	}
	return stringSHA256(string(body)), nil
}

func claudeCodeAttachmentMetadataFromSource(line []byte, nativeSessionID string, turnSeq int64) (claudeCodeAttachmentMetadataIdentity, error) {
	var body struct {
		Attachment struct {
			Type        string `json:"type"`
			Filename    string `json:"filename"`
			DisplayPath string `json:"displayPath"`
		} `json:"attachment"`
	}
	if err := json.Unmarshal(line, &body); err != nil {
		return claudeCodeAttachmentMetadataIdentity{}, fmt.Errorf("decode attachment metadata: %w", err)
	}
	return claudeCodeAttachmentMetadataIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
		AttachmentType:  body.Attachment.Type,
		Filename:        body.Attachment.Filename,
		DisplayPath:     body.Attachment.DisplayPath,
	}, nil
}

func boolPtr(v *bool) bool {
	return v != nil && *v
}

func claudeCodeRawJSONString(raw json.RawMessage) bool {
	var text string
	return len(raw) > 0 && json.Unmarshal(raw, &text) == nil
}

func claudeCodeContentBlocks(raw json.RawMessage) ([]claudeCodeContentBlock, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var blocks []claudeCodeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("decode user message content blocks: %w", err)
	}
	return blocks, nil
}

func claudeCodeSplitToolName(name string) (string, string) {
	if strings.HasPrefix(name, "mcp__") {
		rest := strings.TrimPrefix(name, "mcp__")
		if i := strings.Index(rest, "__"); i >= 0 {
			return rest[i+2:], "mcp:" + rest[:i]
		}
	}
	return name, "builtin"
}

func claudeCodeAgentDescription(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var payload struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return ""
	}
	return payload.Description
}

func claudeCodePayloadChars(resolved resolvedPayload) int64 {
	if resolved.hashDomain != HashSemanticText || !utf8.Valid(resolved.bytes) {
		return -1
	}
	return int64(utf8.RuneCount(resolved.bytes))
}
