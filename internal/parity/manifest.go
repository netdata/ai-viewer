// Package parity compares source-native artifacts with canonical artifacts.
package parity

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// SchemaVersion is the first manifest schema version.
	SchemaVersion = 1
	// EmptySHA256 is SHA-256 over zero bytes.
	EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// ArtifactClass names a source-visible logical artifact type.
type ArtifactClass string

const (
	// ClassSessionBoundary represents a source-visible session lifecycle edge.
	ClassSessionBoundary ArtifactClass = "session_boundary"
	// ClassTurnBoundary represents a source-visible turn lifecycle edge.
	ClassTurnBoundary ArtifactClass = "turn_boundary"
	// ClassOpBoundary represents a source-visible operation lifecycle edge.
	ClassOpBoundary ArtifactClass = "op_boundary"
	// ClassUserPrompt represents user-authored text submitted to the agent.
	ClassUserPrompt ArtifactClass = "user_prompt"
	// ClassUserImage represents user-supplied image input.
	ClassUserImage ArtifactClass = "user_image"
	// ClassAssistantMessage represents assistant-authored response text.
	ClassAssistantMessage ArtifactClass = "assistant_message"
	// ClassReasoningText represents source-visible model reasoning text.
	ClassReasoningText ArtifactClass = "reasoning_text"
	// ClassLLMRequest represents a raw or reconstructed model request.
	ClassLLMRequest ArtifactClass = "llm_request"
	// ClassLLMResponse represents a raw or reconstructed model response.
	ClassLLMResponse ArtifactClass = "llm_response"
	// ClassLLMSDKRequest represents an SDK-level model request artifact.
	ClassLLMSDKRequest ArtifactClass = "llm_sdk_request"
	// ClassLLMSDKResponse represents an SDK-level model response artifact.
	ClassLLMSDKResponse ArtifactClass = "llm_sdk_response"
	// ClassToolRequest represents tool invocation input.
	ClassToolRequest ArtifactClass = "tool_request"
	// ClassToolResponse represents tool invocation output.
	ClassToolResponse ArtifactClass = "tool_response"
	// ClassLLMError represents a model-call error artifact.
	ClassLLMError ArtifactClass = "llm_error"
	// ClassToolError represents a tool-call error artifact.
	ClassToolError ArtifactClass = "tool_error"
	// ClassSubagentLink represents a source-visible parent/child session link.
	ClassSubagentLink ArtifactClass = "subagent_link"
	// ClassSystemOp represents a source-visible system operation.
	ClassSystemOp ArtifactClass = "system_op"
	// ClassCompactionEvent represents source-visible context compaction.
	ClassCompactionEvent ArtifactClass = "compaction_event"
	// ClassSessionMetadata represents source-visible session metadata.
	ClassSessionMetadata ArtifactClass = "session_metadata"
	// ClassLogEntry represents source-visible log or diagnostic text.
	ClassLogEntry ArtifactClass = "log_entry"
	// ClassAttachmentMetadata represents source-visible attachment metadata.
	ClassAttachmentMetadata ArtifactClass = "attachment_metadata"
	// ClassPatchMetadata represents source-visible file-change patch metadata.
	ClassPatchMetadata ArtifactClass = "patch_metadata"
	// ClassSourceCorruption represents parity-only evidence for corrupt source bytes.
	ClassSourceCorruption ArtifactClass = "source_corruption"
)

// Availability records what the source or canonical side can prove.
type Availability string

const (
	// AvailabilityAvailable means complete proof bytes are available.
	AvailabilityAvailable Availability = "available"
	// AvailabilitySourceUnavailable means the source explicitly lacks bytes.
	AvailabilitySourceUnavailable Availability = "source_unavailable"
	// AvailabilitySourceEmpty means the source artifact is present and empty.
	AvailabilitySourceEmpty Availability = "source_empty"
	// AvailabilityPartialSource means only partial source bytes are available.
	AvailabilityPartialSource Availability = "partial_source"
	// AvailabilityRedacted means source bytes were intentionally redacted.
	AvailabilityRedacted Availability = "redacted"
	// AvailabilityCompactedAway means source bytes were removed by compaction.
	AvailabilityCompactedAway Availability = "compacted_away"
	// AvailabilitySourceCorrupt means source bytes were present but corrupt.
	AvailabilitySourceCorrupt Availability = "source_corrupt"
	// AvailabilityUnverifiable means the side cannot prove comparable bytes.
	AvailabilityUnverifiable Availability = "unverifiable"
)

// HashDomain defines the bytes that are hashed for parity.
type HashDomain string

const (
	// HashSemanticText hashes normalized human-readable text bytes.
	HashSemanticText HashDomain = "semantic_text"
	// HashCanonicalJSON hashes canonical JSON bytes.
	HashCanonicalJSON HashDomain = "canonical_json"
	// HashRawBytes hashes raw source bytes.
	HashRawBytes HashDomain = "raw_bytes"
	// HashIdentityJSON hashes deterministic identity JSON.
	HashIdentityJSON HashDomain = "identity_json"
)

const (
	// SyntheticTurnSynthesis documents canonical turn artifacts synthesized from source order.
	SyntheticTurnSynthesis = "turn_synthesis"
	// SyntheticStatusInference documents canonical status inferred from partial source state.
	SyntheticStatusInference = "status_inference"
	// SyntheticLinkageResolution documents canonical linkage resolved across sessions.
	SyntheticLinkageResolution = "linkage_resolution"
	// SyntheticOrphanRepair documents canonical orphan-session repair artifacts.
	SyntheticOrphanRepair = "orphan_repair"
	// SyntheticAdapterHelper documents adapter helper artifacts without direct source bytes.
	SyntheticAdapterHelper = "adapter_helper"
)

// Selector identifies the exact logical artifact in the source.
type Selector struct {
	URI         string     `json:"uri"`
	JSONPointer string     `json:"json_pointer,omitempty"`
	FieldPath   string     `json:"field_path,omitempty"`
	ByteRange   *ByteRange `json:"byte_range,omitempty"`
}

// ByteRange is a [start,end) byte interval.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// IntegrityFailure records one producer-declared proof value that disagreed
// with the source bytes the parity gate resolved.
type IntegrityFailure struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

func int64IntegrityFailure(field string, expected int64, actual int64) IntegrityFailure {
	return IntegrityFailure{
		Field:    field,
		Expected: strconv.FormatInt(expected, 10),
		Actual:   strconv.FormatInt(actual, 10),
	}
}

func stringIntegrityFailure(field string, expected string, actual string) IntegrityFailure {
	return IntegrityFailure{
		Field:    field,
		Expected: expected,
		Actual:   actual,
	}
}

// Artifact is the common comparable shape for source and canonical manifests.
type Artifact struct {
	SchemaVersion      int                `json:"schema_version"`
	Adapter            string             `json:"adapter"`
	SourceID           string             `json:"source_id"`
	SourceFile         string             `json:"source_file"`
	CanonicalSessionID string             `json:"canonical_session_id,omitempty"`
	CanonicalTurnID    string             `json:"canonical_turn_id,omitempty"`
	CanonicalOpID      string             `json:"canonical_op_id,omitempty"`
	PayloadRefID       int64              `json:"payload_ref_id,omitempty"`
	NativeSessionID    string             `json:"native_session_id"`
	NativeTurnID       string             `json:"native_turn_id"`
	NativeArtifactID   string             `json:"native_artifact_id"`
	Class              ArtifactClass      `json:"class"`
	Availability       Availability       `json:"availability"`
	HashDomain         HashDomain         `json:"hash_domain"`
	Selector           Selector           `json:"selector"`
	Bytes              int64              `json:"bytes"`
	Chars              int64              `json:"chars"`
	ComputedSHA256     string             `json:"computed_sha256"`
	ProducerSHA256     string             `json:"producer_sha256"`
	IntegrityFailures  []IntegrityFailure `json:"integrity_failures,omitempty"`
	Synthetic          bool               `json:"synthetic"`
	SyntheticReason    string             `json:"synthetic_reason"`
}

// MatchKey is the exact parity identity key.
type MatchKey struct {
	SchemaVersion    int
	Adapter          string
	SourceID         string
	NativeSessionID  string
	Class            ArtifactClass
	NativeArtifactID string
}

// ClasslessKey is used to report class mismatches as one finding.
type ClasslessKey struct {
	SchemaVersion    int
	Adapter          string
	SourceID         string
	NativeSessionID  string
	NativeArtifactID string
}

// ArtifactKeyFilter filters streamed canonical artifacts by exact and
// classless parity identity.
type ArtifactKeyFilter interface {
	IncludeArtifactKey(key MatchKey, classless ClasslessKey) bool
	IncludeClasslessKey(classless ClasslessKey) bool
}

// Key returns the exact source-to-canonical matching key.
func (a Artifact) Key() MatchKey {
	return MatchKey{
		SchemaVersion:    a.SchemaVersion,
		Adapter:          a.Adapter,
		SourceID:         a.SourceID,
		NativeSessionID:  a.NativeSessionID,
		Class:            a.Class,
		NativeArtifactID: a.NativeArtifactID,
	}
}

// ClasslessKey returns the identity without class for mismatch reporting.
func (a Artifact) ClasslessKey() ClasslessKey {
	return ClasslessKey{
		SchemaVersion:    a.SchemaVersion,
		Adapter:          a.Adapter,
		SourceID:         a.SourceID,
		NativeSessionID:  a.NativeSessionID,
		NativeArtifactID: a.NativeArtifactID,
	}
}

// Validate checks whether one artifact carries the minimum proof material.
func (a Artifact) Validate() error {
	if err := a.validateRequiredIdentity(); err != nil {
		return err
	}
	if err := a.validateSourceCorruptProof(); err != nil {
		return err
	}
	if a.Availability == AvailabilitySourceUnavailable {
		return nil
	}
	if err := a.validateByteProofFields(); err != nil {
		return err
	}
	if a.Synthetic && !validSyntheticReason(a.SyntheticReason) {
		return fmt.Errorf("invalid synthetic_reason")
	}
	return nil
}

func (a Artifact) validateRequiredIdentity() error {
	if a.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version=%d, want %d", a.SchemaVersion, SchemaVersion)
	}
	if a.Adapter == "" {
		return fmt.Errorf("missing adapter")
	}
	if a.SourceID == "" {
		return fmt.Errorf("missing source_id")
	}
	if a.NativeSessionID == "" {
		return fmt.Errorf("missing native_session_id")
	}
	if a.NativeArtifactID == "" {
		return fmt.Errorf("missing native_artifact_id")
	}
	if a.Class == "" {
		return fmt.Errorf("missing class")
	}
	if a.Availability == "" {
		return fmt.Errorf("missing availability")
	}
	return nil
}

func (a Artifact) validateSourceCorruptProof() error {
	if a.Availability == AvailabilitySourceCorrupt {
		if len(a.IntegrityFailures) == 0 {
			return fmt.Errorf("source_corrupt artifact lacks integrity_failures")
		}
		for i, failure := range a.IntegrityFailures {
			if failure.Field == "" {
				return fmt.Errorf("integrity_failures[%d] missing field", i)
			}
			if failure.Expected == "" {
				return fmt.Errorf("integrity_failures[%d] missing expected", i)
			}
			if failure.Actual == "" {
				return fmt.Errorf("integrity_failures[%d] missing actual", i)
			}
		}
	}
	return nil
}

func (a Artifact) validateByteProofFields() error {
	if a.HashDomain == "" {
		return fmt.Errorf("missing hash_domain")
	}
	if a.Selector.URI == "" {
		return fmt.Errorf("missing selector.uri")
	}
	if a.ByteProofRequired() && a.ComputedSHA256 == "" {
		return fmt.Errorf("missing computed_sha256")
	}
	if a.Availability == AvailabilitySourceEmpty {
		if a.Bytes != 0 || a.ComputedSHA256 != EmptySHA256 {
			return fmt.Errorf("invalid source_empty proof")
		}
		if a.HashDomain == HashSemanticText && a.Chars != 0 {
			return fmt.Errorf("invalid source_empty proof")
		}
	}
	return nil
}

// ByteProofRequired reports whether selector, length, and hash proof is required.
func (a Artifact) ByteProofRequired() bool {
	switch a.Availability {
	case AvailabilityAvailable, AvailabilitySourceEmpty, AvailabilityPartialSource, AvailabilityRedacted, AvailabilityCompactedAway:
		return true
	default:
		return false
	}
}

// SelectorProofRequired reports whether byte-level source selectors must match.
func (a Artifact) SelectorProofRequired() bool {
	if !a.ByteProofRequired() {
		return false
	}
	switch a.Class {
	case ClassUserPrompt, ClassUserImage, ClassAssistantMessage, ClassReasoningText, ClassLLMRequest,
		ClassLLMResponse, ClassLLMSDKRequest, ClassLLMSDKResponse, ClassToolRequest,
		ClassToolResponse, ClassLogEntry, ClassAttachmentMetadata, ClassPatchMetadata:
		return true
	default:
		return false
	}
}

func validSyntheticReason(reason string) bool {
	switch reason {
	case SyntheticTurnSynthesis, SyntheticStatusInference, SyntheticLinkageResolution, SyntheticOrphanRepair, SyntheticAdapterHelper:
		return true
	default:
		return false
	}
}

func syntheticIsDocumented(a Artifact) bool {
	if !a.Synthetic || !validSyntheticReason(a.SyntheticReason) {
		return false
	}
	prefix := "synthetic:" + a.SyntheticReason + ":"
	return strings.HasPrefix(a.NativeArtifactID, prefix)
}
