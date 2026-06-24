package parity

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (s *aiAgentV3SourceState) aiAgentV3PayloadArtifact(turnSeq int64, opSeq int64, ref aiAgentV3PayloadRef, ordinal int64) (Artifact, error) {
	return s.aiAgentV3PayloadArtifactWithLimit(turnSeq, opSeq, ref, ordinal, canonicalPayloadArtifactMaxBytes)
}

func (s *aiAgentV3SourceState) aiAgentV3PayloadArtifactWithLimit(turnSeq int64, opSeq int64, ref aiAgentV3PayloadRef, ordinal int64, maxBytes int64) (Artifact, error) {
	class, err := aiAgentV3PayloadClass(ref.Kind)
	if err != nil {
		return Artifact{}, err
	}
	nativeTurnID := fmt.Sprintf("turn:%d", turnSeq)
	if !ref.Captured || ref.Path == "" {
		return Artifact{
			SchemaVersion:    SchemaVersion,
			Adapter:          "aiagent_v3",
			SourceID:         s.sourceID,
			SourceFile:       s.sourceFile,
			NativeSessionID:  s.sessionID(),
			NativeTurnID:     nativeTurnID,
			NativeArtifactID: opPayloadNativeID(turnSeq, opSeq, ref.Kind, ordinal),
			Class:            class,
			Availability:     AvailabilitySourceUnavailable,
			ProducerSHA256:   ref.SHA256,
		}, nil
	}

	absPath, locationURI, err := resolveAIAgentV3PayloadPath(s.root, ref.Path)
	if err != nil {
		return Artifact{}, err
	}
	compressedPayload, err := readFileSelectorWithLimit(absPath, "", maxBytes)
	if err != nil {
		return Artifact{}, fmt.Errorf("read aiagent_v3 payload %s: %w", ref.Path, err)
	}
	payload, err := decompressPayloadWithLimit(compressedPayload, ref.Compression, maxBytes)
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.Sum256(payload)
	hashString := fmt.Sprintf("%x", hash)
	availability := payloadAvailability(int64(len(payload)), hashString)
	integrityFailures := aiAgentV3PayloadRefIntegrityFailures(ref, int64(len(payload)), int64(len(compressedPayload)), hashString)
	if len(integrityFailures) > 0 {
		availability = AvailabilitySourceCorrupt
	}
	hashDomain := aiAgentV3PayloadHashDomain(ref.Kind)
	return Artifact{
		SchemaVersion:     SchemaVersion,
		Adapter:           "aiagent_v3",
		SourceID:          s.sourceID,
		SourceFile:        absPath,
		NativeSessionID:   s.sessionID(),
		NativeTurnID:      nativeTurnID,
		NativeArtifactID:  payloadFileNativeID(s.root, absPath),
		Class:             class,
		Availability:      availability,
		HashDomain:        hashDomain,
		Selector:          Selector{URI: locationURI},
		Bytes:             int64(len(payload)),
		Chars:             aiAgentV3PayloadChars(hashDomain, payload),
		ComputedSHA256:    hashString,
		ProducerSHA256:    ref.SHA256,
		IntegrityFailures: integrityFailures,
	}, nil
}

func aiAgentV3PayloadRefIntegrityFailures(ref aiAgentV3PayloadRef, originalBytes int64, compressedBytes int64, sha256Hex string) []IntegrityFailure {
	var failures []IntegrityFailure
	if ref.OriginalBytes != nil && *ref.OriginalBytes != originalBytes {
		failures = append(failures, int64IntegrityFailure("original_bytes", *ref.OriginalBytes, originalBytes))
	}
	if ref.CompressedBytes != nil && *ref.CompressedBytes != compressedBytes {
		failures = append(failures, int64IntegrityFailure("compressed_bytes", *ref.CompressedBytes, compressedBytes))
	}
	if ref.SHA256 != "" && ref.SHA256 != sha256Hex {
		failures = append(failures, stringIntegrityFailure("sha256", ref.SHA256, sha256Hex))
	}
	return failures
}

func aiAgentV3PayloadClass(kind string) (ArtifactClass, error) {
	switch kind {
	case "llm_request":
		return ClassLLMRequest, nil
	case "llm_response":
		return ClassLLMResponse, nil
	case "sdk_request", "llm_sdk_request":
		return ClassLLMSDKRequest, nil
	case "sdk_response", "llm_sdk_response":
		return ClassLLMSDKResponse, nil
	case "reasoning_stream", "llm_reasoning":
		return ClassReasoningText, nil
	case "tool_request":
		return ClassToolRequest, nil
	case "tool_response":
		return ClassToolResponse, nil
	default:
		return "", fmt.Errorf("aiagent_v3 payload kind %q is not mapped to a parity class", kind)
	}
}

func aiAgentV3PayloadHashDomain(kind string) HashDomain {
	switch kind {
	case "reasoning_stream", "llm_reasoning":
		return HashSemanticText
	default:
		return HashRawBytes
	}
}

func aiAgentV3PayloadChars(hashDomain HashDomain, payload []byte) int64 {
	if hashDomain != HashSemanticText || !utf8.Valid(payload) {
		return -1
	}
	return int64(utf8.RuneCount(payload))
}

func resolveAIAgentV3PayloadPath(root string, relPath string) (string, string, error) {
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return "", "", fmt.Errorf("payload path must be relative: %q", relPath)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	cleanedRoot := filepath.Clean(absRoot)
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
