package parity

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type aiAgentV2PayloadRef struct {
	Ref           string
	Path          string
	Format        string
	Compression   string
	OriginalBytes int64
	OriginalSet   bool
	StoredBytes   int64
	StoredSet     bool
	SHA256        string
	Captured      *bool
	SDK           bool
}

type aiAgentV2PayloadRefEnvelope struct {
	Ref             json.RawMessage         `json:"ref,omitempty"`
	SDK             *aiAgentV2SDKPayloadRef `json:"sdk,omitempty"`
	Path            string                  `json:"path,omitempty"`
	Format          string                  `json:"format,omitempty"`
	Compression     string                  `json:"compression,omitempty"`
	OriginalBytes   *int64                  `json:"originalBytes,omitempty"`
	StoredBytes     *int64                  `json:"storedBytes,omitempty"`
	CompressedBytes *int64                  `json:"compressedBytes,omitempty"`
	SHA256          string                  `json:"sha256,omitempty"`
	Captured        *bool                   `json:"captured,omitempty"`
}

type aiAgentV2SDKPayloadRef struct {
	Ref json.RawMessage `json:"ref,omitempty"`
}

func (s *aiAgentV2SourceState) aiAgentV2PayloadArtifacts(op aiAgentV2Operation, sessionTrace string, turnSeq int64, opSeq int64, opKind string, opPointer string) ([]Artifact, error) {
	var artifacts []Artifact
	ordinals := map[string]int64{}
	for _, item := range []struct {
		side    string
		payload *aiAgentV2OpPayload
	}{
		{side: "request", payload: op.Request},
		{side: "response", payload: op.Response},
	} {
		if item.payload == nil {
			continue
		}
		refs := extractAIAgentV2PayloadRefs(item.payload.Payload)
		if len(refs) == 0 {
			payloadKind := aiAgentV2PayloadKind(opKind, item.side, aiAgentV2PayloadRef{})
			artifact, ok, err := s.aiAgentV2InlinePayloadArtifact(item.payload, sessionTrace, turnSeq, payloadKind, opPointer+"/"+item.side+"/payload")
			if err != nil {
				return nil, err
			}
			if ok {
				artifacts = append(artifacts, artifact)
			}
			continue
		}
		for _, ref := range refs {
			payloadKind := aiAgentV2PayloadKind(opKind, item.side, ref)
			ordinals[payloadKind]++
			artifact, err := s.aiAgentV2PayloadArtifact(ref, sessionTrace, turnSeq, opSeq, payloadKind, ordinals[payloadKind])
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, artifact)
		}
	}
	return artifacts, nil
}

func (s *aiAgentV2SourceState) aiAgentV2InlinePayloadArtifact(payload *aiAgentV2OpPayload, sessionTrace string, turnSeq int64, payloadKind string, pointer string) (Artifact, bool, error) {
	if !aiAgentV2InlinePayloadPresent(payload.Payload) {
		return Artifact{}, false, nil
	}
	class, err := aiAgentV2PayloadClass(payloadKind)
	if err != nil {
		return Artifact{}, false, err
	}
	resolved, err := resolveJSONPointerPayload(payload.Payload, "")
	if err != nil {
		return Artifact{}, false, fmt.Errorf("resolve aiagent_v2 inline payload %s: %w", pointer, err)
	}
	hash := stringSHA256(string(resolved.bytes))
	availability := payloadAvailability(int64(len(resolved.bytes)), hash)
	if payload.Truncated {
		availability = AvailabilityPartialSource
	}
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          aiAgentV2Format,
		SourceID:         s.sourceID,
		SourceFile:       s.sourceFile,
		NativeSessionID:  sessionTrace,
		NativeTurnID:     aiAgentV2NativeTurnID(turnSeq),
		NativeArtifactID: fmt.Sprintf("file:%s:%s", filepath.Base(s.sourceFile), pointer),
		Class:            class,
		Availability:     availability,
		HashDomain:       resolved.hashDomain,
		Selector: Selector{
			URI:         (&url.URL{Scheme: "file", Path: s.sourceFile}).String(),
			JSONPointer: pointer,
		},
		Bytes:          int64(len(resolved.bytes)),
		Chars:          aiAgentV2InlinePayloadChars(resolved),
		ComputedSHA256: hash,
	}, true, nil
}

func aiAgentV2InlinePayloadPresent(raw json.RawMessage) bool {
	_, ok := aiAgentV2FirstNonSpaceByte(raw)
	return ok
}

func aiAgentV2InlinePayloadChars(resolved resolvedPayload) int64 {
	if resolved.hashDomain == HashSemanticText && utf8.Valid(resolved.bytes) {
		return int64(utf8.RuneCount(resolved.bytes))
	}
	return -1
}

func (s *aiAgentV2SourceState) aiAgentV2PayloadArtifact(ref aiAgentV2PayloadRef, sessionTrace string, turnSeq int64, opSeq int64, payloadKind string, ordinal int64) (Artifact, error) {
	return s.aiAgentV2PayloadArtifactWithLimit(ref, sessionTrace, turnSeq, opSeq, payloadKind, ordinal, canonicalPayloadArtifactMaxBytes)
}

func (s *aiAgentV2SourceState) aiAgentV2PayloadArtifactWithLimit(ref aiAgentV2PayloadRef, sessionTrace string, turnSeq int64, opSeq int64, payloadKind string, ordinal int64, maxBytes int64) (Artifact, error) {
	class, err := aiAgentV2PayloadClass(payloadKind)
	if err != nil {
		return Artifact{}, err
	}
	nativeTurnID := aiAgentV2NativeTurnID(turnSeq)
	if !aiAgentV2PayloadRefCaptured(ref) || ref.Path == "" {
		return Artifact{
			SchemaVersion:    SchemaVersion,
			Adapter:          aiAgentV2Format,
			SourceID:         s.sourceID,
			SourceFile:       s.sourceFile,
			NativeSessionID:  sessionTrace,
			NativeTurnID:     nativeTurnID,
			NativeArtifactID: opPayloadNativeID(turnSeq, opSeq, payloadKind, ordinal),
			Class:            class,
			Availability:     AvailabilitySourceUnavailable,
			ProducerSHA256:   ref.SHA256,
		}, nil
	}

	absPath, locationURI, err := resolveAIAgentV2PayloadPath(s.root, ref.Path)
	if err != nil {
		return Artifact{}, err
	}
	compressedPayload, err := readFileSelectorWithLimit(absPath, "", maxBytes)
	if err != nil {
		return Artifact{}, fmt.Errorf("read aiagent_v2 payload %s: %w", ref.Path, err)
	}
	payload, err := decompressPayloadWithLimit(compressedPayload, ref.Compression, maxBytes)
	if err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(payload)
	hashString := fmt.Sprintf("%x", sum)
	availability := payloadAvailability(int64(len(payload)), hashString)
	integrityFailures := aiAgentV2PayloadRefIntegrityFailures(ref, int64(len(payload)), int64(len(compressedPayload)), hashString)
	if len(integrityFailures) > 0 {
		availability = AvailabilitySourceCorrupt
	}
	return Artifact{
		SchemaVersion:     SchemaVersion,
		Adapter:           aiAgentV2Format,
		SourceID:          s.sourceID,
		SourceFile:        absPath,
		NativeSessionID:   sessionTrace,
		NativeTurnID:      nativeTurnID,
		NativeArtifactID:  payloadFileNativeID(s.root, absPath),
		Class:             class,
		Availability:      availability,
		HashDomain:        HashRawBytes,
		Selector:          Selector{URI: locationURI},
		Bytes:             int64(len(payload)),
		Chars:             -1,
		ComputedSHA256:    hashString,
		ProducerSHA256:    ref.SHA256,
		IntegrityFailures: integrityFailures,
	}, nil
}

func aiAgentV2PayloadRefIntegrityFailures(ref aiAgentV2PayloadRef, originalBytes int64, storedBytes int64, sha256Hex string) []IntegrityFailure {
	var failures []IntegrityFailure
	if ref.OriginalSet && ref.OriginalBytes != originalBytes {
		failures = append(failures, int64IntegrityFailure("original_bytes", ref.OriginalBytes, originalBytes))
	}
	if ref.StoredSet && ref.StoredBytes != storedBytes {
		failures = append(failures, int64IntegrityFailure("compressed_bytes", ref.StoredBytes, storedBytes))
	}
	if ref.SHA256 != "" && ref.SHA256 != sha256Hex {
		failures = append(failures, stringIntegrityFailure("sha256", ref.SHA256, sha256Hex))
	}
	return failures
}

func extractAIAgentV2PayloadRefs(raw json.RawMessage) []aiAgentV2PayloadRef {
	if !aiAgentV2PayloadRefCandidate(raw) {
		return nil
	}
	var env aiAgentV2PayloadRefEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	refs := make([]aiAgentV2PayloadRef, 0, 2)
	if ref, ok := aiAgentV2RegularPayloadRef(env); ok {
		refs = append(refs, ref)
	}
	if env.SDK != nil {
		if ref, ok := decodeAIAgentV2WrappedPayloadRef(env.SDK.Ref, true); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func aiAgentV2RegularPayloadRef(env aiAgentV2PayloadRefEnvelope) (aiAgentV2PayloadRef, bool) {
	if aiAgentV2LegacyPayloadRefCandidate(env) && !aiAgentV2RawPayloadRefIsObject(env.Ref) {
		return aiAgentV2LegacyPayloadRef(env)
	}
	return decodeAIAgentV2WrappedPayloadRef(env.Ref, false)
}

func aiAgentV2LegacyPayloadRefCandidate(env aiAgentV2PayloadRefEnvelope) bool {
	if aiAgentV2StringPayloadRef(env.Ref) != "" {
		return true
	}
	return !aiAgentV2RawPayloadRefPresent(env.Ref) && env.Path != "" && aiAgentV2HasLegacyPayloadEvidence(env)
}

func aiAgentV2HasLegacyPayloadEvidence(env aiAgentV2PayloadRefEnvelope) bool {
	return env.Captured != nil || env.Format != "" || env.Compression != "" ||
		env.OriginalBytes != nil || env.StoredBytes != nil || env.CompressedBytes != nil || env.SHA256 != ""
}

func aiAgentV2RawPayloadRefPresent(raw json.RawMessage) bool {
	_, ok := aiAgentV2FirstNonSpaceByte(raw)
	return ok
}

func aiAgentV2RawPayloadRefIsObject(raw json.RawMessage) bool {
	first, ok := aiAgentV2FirstNonSpaceByte(raw)
	return ok && first == '{'
}

func aiAgentV2PayloadRefCandidate(raw json.RawMessage) bool {
	first, ok := aiAgentV2FirstNonSpaceByte(raw)
	return ok && first == '{'
}

func decodeAIAgentV2WrappedPayloadRef(raw json.RawMessage, sdk bool) (aiAgentV2PayloadRef, bool) {
	first, ok := aiAgentV2FirstNonSpaceByte(raw)
	if !ok {
		return aiAgentV2PayloadRef{}, false
	}
	if first == '"' {
		return decodeAIAgentV2StringPayloadRef(raw, sdk)
	}
	if first == '{' {
		return decodeAIAgentV2EvidencePayloadRef(raw, sdk)
	}
	return aiAgentV2PayloadRef{}, false
}

func decodeAIAgentV2StringPayloadRef(raw json.RawMessage, sdk bool) (aiAgentV2PayloadRef, bool) {
	var path string
	if err := json.Unmarshal(raw, &path); err != nil || path == "" {
		return aiAgentV2PayloadRef{}, false
	}
	return aiAgentV2PayloadRef{Ref: path, Path: path, SDK: sdk}, true
}

func decodeAIAgentV2EvidencePayloadRef(raw json.RawMessage, sdk bool) (aiAgentV2PayloadRef, bool) {
	var env aiAgentV2PayloadRefEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return aiAgentV2PayloadRef{}, false
	}
	ref := aiAgentV2PayloadRefFromEnvelope(env)
	ref.SDK = sdk
	if ref.Path == "" && ref.Captured == nil {
		return aiAgentV2PayloadRef{}, false
	}
	return ref, true
}

func aiAgentV2LegacyPayloadRef(env aiAgentV2PayloadRefEnvelope) (aiAgentV2PayloadRef, bool) {
	ref := aiAgentV2PayloadRefFromEnvelope(env)
	ref.Ref = aiAgentV2StringPayloadRef(env.Ref)
	if ref.Path == "" {
		ref.Path = ref.Ref
	}
	if ref.Path == "" {
		return aiAgentV2PayloadRef{}, false
	}
	return ref, true
}

func aiAgentV2PayloadRefFromEnvelope(env aiAgentV2PayloadRefEnvelope) aiAgentV2PayloadRef {
	originalBytes, originalSet := aiAgentV2OriginalPayloadBytes(env)
	storedBytes, storedSet := aiAgentV2StoredPayloadBytes(env)
	return aiAgentV2PayloadRef{
		Path:          env.Path,
		Format:        env.Format,
		Compression:   env.Compression,
		OriginalBytes: originalBytes,
		OriginalSet:   originalSet,
		StoredBytes:   storedBytes,
		StoredSet:     storedSet,
		SHA256:        env.SHA256,
		Captured:      env.Captured,
	}
}

func aiAgentV2OriginalPayloadBytes(env aiAgentV2PayloadRefEnvelope) (int64, bool) {
	if env.OriginalBytes == nil {
		return 0, false
	}
	return *env.OriginalBytes, true
}

func aiAgentV2StoredPayloadBytes(env aiAgentV2PayloadRefEnvelope) (int64, bool) {
	if env.CompressedBytes != nil {
		return *env.CompressedBytes, true
	}
	if env.StoredBytes != nil {
		return *env.StoredBytes, true
	}
	return 0, false
}

func aiAgentV2StringPayloadRef(raw json.RawMessage) string {
	var path string
	if err := json.Unmarshal(raw, &path); err != nil {
		return ""
	}
	return path
}

func aiAgentV2PayloadKind(opKind string, side string, ref aiAgentV2PayloadRef) string {
	if ref.SDK {
		if side == "request" {
			return "llm_sdk_request"
		}
		return "llm_sdk_response"
	}
	if opKind == "tool" {
		if side == "request" {
			return "tool_request"
		}
		return "tool_response"
	}
	if side == "request" {
		return "llm_request"
	}
	return "llm_response"
}

func aiAgentV2PayloadClass(kind string) (ArtifactClass, error) {
	switch kind {
	case "llm_request":
		return ClassLLMRequest, nil
	case "llm_response":
		return ClassLLMResponse, nil
	case "llm_sdk_request":
		return ClassLLMSDKRequest, nil
	case "llm_sdk_response":
		return ClassLLMSDKResponse, nil
	case "tool_request":
		return ClassToolRequest, nil
	case "tool_response":
		return ClassToolResponse, nil
	default:
		return "", fmt.Errorf("aiagent_v2 payload kind %q is not mapped to a parity class", kind)
	}
}

func aiAgentV2PayloadRefCaptured(ref aiAgentV2PayloadRef) bool {
	return ref.Captured == nil || *ref.Captured
}

func resolveAIAgentV2PayloadPath(root string, relPath string) (string, string, error) {
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return "", "", fmt.Errorf("payload path must be relative: %q", relPath)
	}
	cleanedRoot := filepath.Clean(root)
	parts := strings.Split(relPath, "/")
	cleaned := filepath.Clean(filepath.Join(append([]string{cleanedRoot}, parts...)...))
	rel, err := filepath.Rel(cleanedRoot, cleaned)
	if err != nil {
		return "", "", fmt.Errorf("relative %q: %w", relPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("payload path escapes root: %q", relPath)
	}
	return cleaned, (&url.URL{Scheme: "file", Path: cleaned}).String(), nil
}

func aiAgentV2FirstNonSpaceByte(raw json.RawMessage) (byte, bool) {
	for _, b := range raw {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		return b, true
	}
	return 0, false
}
