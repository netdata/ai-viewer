package parity

// ResultState is the top-level outcome of one parity diff.
type ResultState string

const (
	// StatePass means every reachable source artifact matched canonical.
	StatePass ResultState = "PASS full parity"
	// StateFail means blocking source/canonical parity findings exist.
	StateFail ResultState = "FAIL parity"
	// StateIncomplete means the gate could not fully scan or verify inputs.
	StateIncomplete ResultState = "INCOMPLETE"
	// StateSampleOnly means the result came from a diagnostic subset.
	StateSampleOnly ResultState = "SAMPLE ONLY"
)

// Severity is the blocking level for a finding.
type Severity string

const (
	// SeverityP0 marks direct data loss, corruption, or goal failure.
	SeverityP0 Severity = "P0"
	// SeverityP1 marks a blocking contract, verification, or design defect.
	SeverityP1 Severity = "P1"
	// SeverityP2 marks an important quality/completeness issue.
	SeverityP2 Severity = "P2"
	// SeverityP3 marks cosmetic or non-blocking review feedback.
	SeverityP3 Severity = "P3"
)

// FindingCode identifies one deterministic parity mismatch class.
type FindingCode string

const (
	// CodeMissingCanonical means a source artifact has no canonical match.
	CodeMissingCanonical FindingCode = "missing_canonical"
	// CodeExtraCanonical means a canonical artifact has no source match.
	CodeExtraCanonical FindingCode = "extra_canonical"
	// CodeDuplicateSource means source emitted duplicate parity keys.
	CodeDuplicateSource FindingCode = "duplicate_source"
	// CodeDuplicateCanonical means canonical emitted duplicate parity keys.
	CodeDuplicateCanonical FindingCode = "duplicate_canonical"
	// CodeClassMismatch means source and canonical disagree on artifact class.
	CodeClassMismatch FindingCode = "class_mismatch"
	// CodeHashMismatch means source and canonical byte hashes differ.
	CodeHashMismatch FindingCode = "hash_mismatch"
	// CodeHashDomainMismatch means source and canonical hash different byte domains.
	CodeHashDomainMismatch FindingCode = "hash_domain_mismatch"
	// CodeBytesMismatch means source and canonical byte lengths differ.
	CodeBytesMismatch FindingCode = "bytes_mismatch"
	// CodeCharsMismatch means source and canonical character lengths differ.
	CodeCharsMismatch FindingCode = "chars_mismatch"
	// CodeAvailabilityMismatch means source and canonical proof availability differ.
	CodeAvailabilityMismatch FindingCode = "availability_mismatch"
	// CodeSelectorMismatch means canonical points at a different source selector.
	CodeSelectorMismatch FindingCode = "selector_mismatch"
	// CodeUnverifiableCanonical means canonical lacks enough proof to compare.
	CodeUnverifiableCanonical FindingCode = "unverifiable_canonical"
	// CodeInvalidSourceArtifact means a source artifact is malformed.
	CodeInvalidSourceArtifact FindingCode = "invalid_source_artifact"
	// CodeInvalidCanonicalArtifact means a canonical artifact is malformed.
	CodeInvalidCanonicalArtifact FindingCode = "invalid_canonical_artifact"
	// CodeUndocumentedSynthetic means a canonical-only artifact lacks its required reason.
	CodeUndocumentedSynthetic FindingCode = "undocumented_synthetic"
	// CodeSourceCorrupt means source evidence was explicitly corrupt.
	CodeSourceCorrupt FindingCode = "source_corrupt"
	// CodeMatrixMismatch means an emitted artifact violates its adapter matrix row.
	CodeMatrixMismatch FindingCode = "matrix_mismatch"
)

// Result is the deterministic diff result.
type Result struct {
	State          ResultState      `json:"state"`
	TotalFindings  int              `json:"total_findings,omitempty"`
	FindingSummary []FindingSummary `json:"finding_summary,omitempty"`
	Findings       []Finding        `json:"findings"`
}

// Finding is one actionable mismatch.
type Finding struct {
	Severity          Severity           `json:"severity"`
	Code              FindingCode        `json:"code"`
	Adapter           string             `json:"adapter"`
	SourceID          string             `json:"source_id"`
	NativeSessionID   string             `json:"native_session_id"`
	NativeArtifactID  string             `json:"native_artifact_id"`
	Class             ArtifactClass      `json:"class"`
	Message           string             `json:"message"`
	IntegrityFailures []IntegrityFailure `json:"integrity_failures,omitempty"`
}

// FindingSummary is a grouped finding count.
type FindingSummary struct {
	Severity Severity      `json:"severity"`
	Code     FindingCode   `json:"code"`
	Class    ArtifactClass `json:"class"`
	Count    int           `json:"count"`
}
