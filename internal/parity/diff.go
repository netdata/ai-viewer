package parity

import (
	"context"
	"fmt"
	"sort"
)

const diffContextCheckInterval = 4096

// Diff compares independently-built source and canonical manifests.
func Diff(source []Artifact, canonical []Artifact) Result {
	result, err := DiffContext(context.Background(), source, canonical)
	if err != nil {
		return Result{State: StateIncomplete}
	}
	return result
}

// DiffContext compares manifests while honoring cancellation.
func DiffContext(ctx context.Context, source []Artifact, canonical []Artifact) (Result, error) {
	return DiffContextCapped(ctx, source, canonical, -1)
}

// DiffContextCapped compares manifests while retaining at most maxFindings
// detailed findings. It still counts and summarizes every finding observed.
func DiffContextCapped(ctx context.Context, source []Artifact, canonical []Artifact, maxFindings int) (Result, error) {
	findings := newFindingAccumulator(maxFindings)

	sourceByKey, sourceByClassless, err := indexArtifacts(ctx, source, "source", findings)
	if err != nil {
		return Result{State: StateIncomplete}, err
	}
	canonicalByKey, canonicalByClassless, err := indexArtifacts(ctx, canonical, "canonical", findings)
	if err != nil {
		return Result{State: StateIncomplete}, err
	}

	for i, src := range source {
		if err := checkDiffContext(ctx, i); err != nil {
			return Result{State: StateIncomplete}, err
		}
		if src.Availability == AvailabilitySourceCorrupt {
			findings.add(newFinding(src, SeverityP1, CodeSourceCorrupt, "source artifact is corrupt"))
			continue
		}
		if duplicateKey(sourceByKey[src.Key()]) {
			continue
		}

		matches := canonicalByKey[src.Key()]
		if len(matches) == 1 {
			findings.addAll(compareMatchedArtifacts(src, matches[0]))
			continue
		}
		if len(matches) > 1 {
			findings.add(newFinding(src, SeverityP1, CodeDuplicateCanonical, "more than one canonical artifact matches source artifact"))
			continue
		}

		classlessMatches := canonicalByClassless[src.ClasslessKey()]
		if len(classlessMatches) > 0 {
			findings.add(newFinding(src, SeverityP1, CodeClassMismatch, fmt.Sprintf("canonical class=%s, source class=%s", classlessMatches[0].Class, src.Class)))
			continue
		}

		findings.add(newFinding(src, missingSeverity(src), CodeMissingCanonical, "source artifact has no canonical match"))
	}

	for i, can := range canonical {
		if err := checkDiffContext(ctx, i); err != nil {
			return Result{State: StateIncomplete}, err
		}
		if duplicateKey(canonicalByKey[can.Key()]) {
			continue
		}
		if _, matched := sourceByKey[can.Key()]; matched {
			continue
		}
		if _, classMismatch := sourceByClassless[can.ClasslessKey()]; classMismatch {
			continue
		}
		if syntheticIsDocumented(can) {
			continue
		}
		if can.Synthetic {
			findings.add(newFinding(can, SeverityP1, CodeUndocumentedSynthetic, "synthetic canonical artifact lacks a documented reason or id prefix"))
			continue
		}
		findings.add(newFinding(can, SeverityP1, CodeExtraCanonical, "canonical artifact has no source match"))
	}

	return findings.result(), nil
}

func indexArtifacts(ctx context.Context, artifacts []Artifact, side string, findings *findingAccumulator) (map[MatchKey][]Artifact, map[ClasslessKey][]Artifact, error) {
	byKey := make(map[MatchKey][]Artifact, len(artifacts))
	byClassless := make(map[ClasslessKey][]Artifact, len(artifacts))
	for i, artifact := range artifacts {
		if err := checkDiffContext(ctx, i); err != nil {
			return nil, nil, err
		}
		if err := artifact.Validate(); err != nil {
			code := CodeInvalidSourceArtifact
			if side == "canonical" {
				code = CodeInvalidCanonicalArtifact
			}
			findings.add(newFinding(artifact, SeverityP1, code, err.Error()))
		}
		if err := validateArtifactAgainstMatrix(artifact); err != nil {
			findings.add(newFinding(artifact, SeverityP2, CodeMatrixMismatch, err.Error()))
		}
		byKey[artifact.Key()] = append(byKey[artifact.Key()], artifact)
		byClassless[artifact.ClasslessKey()] = append(byClassless[artifact.ClasslessKey()], artifact)
	}
	i := 0
	for _, artifacts := range byKey {
		if err := checkDiffContext(ctx, i); err != nil {
			return nil, nil, err
		}
		i++
		if len(artifacts) <= 1 {
			continue
		}
		code := CodeDuplicateSource
		if side == "canonical" {
			code = CodeDuplicateCanonical
		}
		findings.add(newFinding(artifacts[0], SeverityP1, code, fmt.Sprintf("duplicate %s artifact key", side)))
	}
	return byKey, byClassless, nil
}

func checkDiffContext(ctx context.Context, iteration int) error {
	if iteration%diffContextCheckInterval != 0 {
		return nil
	}
	return ctx.Err()
}

func compareMatchedArtifacts(source Artifact, canonical Artifact) []Finding {
	var findings []Finding
	if canonical.Availability == AvailabilityUnverifiable {
		findings = append(findings, newFinding(source, SeverityP1, CodeUnverifiableCanonical, "canonical artifact is marked unverifiable"))
		return findings
	}
	if err := canonical.Validate(); err != nil {
		findings = append(findings, newFinding(source, SeverityP1, CodeUnverifiableCanonical, err.Error()))
		return findings
	}
	if canonical.Availability != source.Availability {
		findings = append(findings, newFinding(source, SeverityP1, CodeAvailabilityMismatch, fmt.Sprintf("canonical availability=%s, source availability=%s", canonical.Availability, source.Availability)))
	}
	if source.ByteProofRequired() {
		if canonical.HashDomain != source.HashDomain {
			findings = append(findings, newFinding(source, SeverityP1, CodeHashDomainMismatch, fmt.Sprintf("canonical hash_domain=%s, source hash_domain=%s", canonical.HashDomain, source.HashDomain)))
		}
		if canonical.ComputedSHA256 == "" {
			findings = append(findings, newFinding(source, SeverityP1, CodeUnverifiableCanonical, "canonical artifact lacks computed_sha256"))
		} else if canonical.ComputedSHA256 != source.ComputedSHA256 {
			findings = append(findings, newFinding(source, SeverityP0, CodeHashMismatch, "canonical hash differs from source hash"))
		}
		if canonical.Bytes != source.Bytes {
			findings = append(findings, newFinding(source, SeverityP0, CodeBytesMismatch, fmt.Sprintf("canonical bytes=%d, source bytes=%d", canonical.Bytes, source.Bytes)))
		}
		if source.Chars >= 0 && canonical.Chars != source.Chars {
			findings = append(findings, newFinding(source, SeverityP0, CodeCharsMismatch, fmt.Sprintf("canonical chars=%d, source chars=%d", canonical.Chars, source.Chars)))
		}
		if source.SelectorProofRequired() && !selectorsEqual(canonical.Selector, source.Selector) {
			findings = append(findings, newFinding(source, SeverityP1, CodeSelectorMismatch, "canonical selector differs from source selector"))
		}
	}
	return findings
}

func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i int, j int) bool {
		return findingSortKey(findings[i]) < findingSortKey(findings[j])
	})
}

func findingSortKey(f Finding) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", f.Adapter, f.SourceID, f.NativeSessionID, f.Class, f.NativeArtifactID, f.Code)
}

func duplicateKey(artifacts []Artifact) bool {
	return len(artifacts) > 1
}

func selectorsEqual(a Selector, b Selector) bool {
	if a.URI != b.URI || a.JSONPointer != b.JSONPointer || a.FieldPath != b.FieldPath {
		return false
	}
	if a.ByteRange == nil || b.ByteRange == nil {
		return a.ByteRange == nil && b.ByteRange == nil
	}
	return a.ByteRange.Start == b.ByteRange.Start && a.ByteRange.End == b.ByteRange.End
}

type findingAccumulator struct {
	maxFindings int
	total       int
	state       ResultState
	details     []Finding
	summary     map[findingSummaryKey]int
}

type findingSummaryKey struct {
	severity Severity
	code     FindingCode
	class    ArtifactClass
}

func newFindingAccumulator(maxFindings int) *findingAccumulator {
	return &findingAccumulator{
		maxFindings: maxFindings,
		state:       StatePass,
		summary:     map[findingSummaryKey]int{},
	}
}

func (a *findingAccumulator) addAll(findings []Finding) {
	for _, finding := range findings {
		a.add(finding)
	}
}

func (a *findingAccumulator) add(finding Finding) {
	a.total++
	key := findingSummaryKey{
		severity: finding.Severity,
		code:     finding.Code,
		class:    finding.Class,
	}
	a.summary[key]++
	if a.maxFindings < 0 || len(a.details) < a.maxFindings {
		a.details = append(a.details, finding)
	}
	if finding.Code == CodeSourceCorrupt {
		a.state = StateIncomplete
		return
	}
	if a.state == StatePass && (finding.Severity == SeverityP0 || finding.Severity == SeverityP1 || finding.Severity == SeverityP2) {
		a.state = StateFail
	}
}

func (a *findingAccumulator) result() Result {
	sortFindings(a.details)
	summary := make([]FindingSummary, 0, len(a.summary))
	for key, count := range a.summary {
		summary = append(summary, FindingSummary{
			Severity: key.severity,
			Code:     key.code,
			Class:    key.class,
			Count:    count,
		})
	}
	sort.Slice(summary, func(i int, j int) bool {
		return findingSummarySortKey(summary[i]) < findingSummarySortKey(summary[j])
	})
	return Result{
		State:          a.state,
		TotalFindings:  a.total,
		FindingSummary: summary,
		Findings:       a.details,
	}
}

func findingSummarySortKey(summary FindingSummary) string {
	return string(summary.Severity) + "\x00" + string(summary.Code) + "\x00" + string(summary.Class)
}

func missingSeverity(a Artifact) Severity {
	if a.Availability == AvailabilitySourceUnavailable {
		return SeverityP1
	}
	switch a.Class {
	case ClassUserPrompt, ClassUserImage, ClassAssistantMessage, ClassReasoningText, ClassLLMRequest, ClassLLMResponse,
		ClassLLMSDKRequest, ClassLLMSDKResponse, ClassToolRequest, ClassToolResponse, ClassSubagentLink:
		return SeverityP0
	default:
		return SeverityP1
	}
}

func newFinding(a Artifact, severity Severity, code FindingCode, message string) Finding {
	return Finding{
		Severity:          severity,
		Code:              code,
		Adapter:           a.Adapter,
		SourceID:          a.SourceID,
		NativeSessionID:   a.NativeSessionID,
		NativeArtifactID:  a.NativeArtifactID,
		Class:             a.Class,
		Message:           message,
		IntegrityFailures: a.IntegrityFailures,
	}
}
