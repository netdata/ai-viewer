package parity

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestDiffCleanMatchPasses(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}

	result := Diff(source, canonical)
	if result.State != StatePass {
		t.Fatalf("state = %s, want %s; findings=%+v", result.State, StatePass, result.Findings)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestDiffSourceEmptyRawPayloadMatchPasses(t *testing.T) {
	t.Parallel()

	source := testArtifact("file:payloads/session/turn-0001/empty-response.sse.gz")
	source.Adapter = "aiagent_v3"
	source.SourceID = "aiagent_v3:testdata"
	source.Class = ClassLLMResponse
	source.Availability = AvailabilitySourceEmpty
	source.HashDomain = HashRawBytes
	source.Selector = Selector{URI: "file:///repo/payloads/session/turn-0001/empty-response.sse.gz"}
	source.Bytes = 0
	source.Chars = -1
	source.ComputedSHA256 = EmptySHA256
	canonical := source

	result := Diff([]Artifact{source}, []Artifact{canonical})
	if result.State != StatePass {
		t.Fatalf("state = %s, want %s; findings=%+v", result.State, StatePass, result.Findings)
	}
}

func TestDiffContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DiffContext(ctx, []Artifact{testArtifact("line:1:/msg/content/0/text")}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DiffContext error = %v, want context.Canceled", err)
	}
}

func TestDiffContextCappedKeepsTotalAndSummary(t *testing.T) {
	t.Parallel()

	source := []Artifact{
		testArtifact("line:1:/msg/content/0/text"),
		testArtifact("line:2:/msg/content/0/text"),
		testArtifact("line:3:/msg/content/0/text"),
		testArtifact("line:4:/msg/content/0/text"),
		testArtifact("line:5:/msg/content/0/text"),
	}

	result, err := DiffContextCapped(context.Background(), source, nil, 2)
	if err != nil {
		t.Fatalf("DiffContextCapped: %v", err)
	}
	if result.State != StateFail {
		t.Fatalf("state = %s, want %s", result.State, StateFail)
	}
	if result.TotalFindings != 5 {
		t.Fatalf("total_findings = %d, want 5", result.TotalFindings)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("detailed findings = %d, want cap 2", len(result.Findings))
	}
	if len(result.FindingSummary) != 1 {
		t.Fatalf("summary groups = %d, want 1: %+v", len(result.FindingSummary), result.FindingSummary)
	}
	summary := result.FindingSummary[0]
	if summary.Severity != SeverityP0 || summary.Code != CodeMissingCanonical || summary.Class != ClassAssistantMessage || summary.Count != 5 {
		t.Fatalf("summary = %+v, want P0 missing_canonical assistant_message count 5", summary)
	}
}

func TestDiffArtifactStreamsMatchesInMemoryDiff(t *testing.T) {
	t.Parallel()

	source := []Artifact{
		testArtifact("line:1:/msg/content/0/text"),
		testArtifact("line:2:/msg/content/0/text"),
		testArtifact("line:3:/msg/content/0/text"),
		testArtifact("line:4:/msg/content/0/text"),
		testArtifact("line:5:/msg/content/0/text"),
	}
	canonical := []Artifact{
		testArtifact("line:1:/msg/content/0/text"),
		testArtifact("line:2:/msg/content/0/text"),
		testArtifact("line:2:/msg/content/0/text"),
		testArtifact("line:3:/msg/content/0/text"),
		testArtifact("line:6:/msg/content/0/text"),
	}
	canonical[3].Class = ClassToolResponse
	canonical[4].Synthetic = true
	canonical[4].SyntheticReason = SyntheticAdapterHelper

	memory, err := DiffContextCapped(context.Background(), source, canonical, 20)
	if err != nil {
		t.Fatalf("DiffContextCapped: %v", err)
	}
	streamed, err := DiffArtifactStreamsContext(context.Background(),
		NewArtifactSliceReader(source),
		NewArtifactSliceReader(canonical),
		StreamDiffOptions{MaxFindings: 20, WorkDir: t.TempDir()},
	)
	if err != nil {
		t.Fatalf("DiffArtifactStreamsContext: %v", err)
	}
	if !reflect.DeepEqual(streamed, memory) {
		t.Fatalf("stream diff mismatch\nstreamed=%+v\nmemory=%+v", streamed, memory)
	}
}

func TestDiffArtifactStreamsContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DiffArtifactStreamsContext(ctx,
		NewArtifactSliceReader([]Artifact{testArtifact("line:1:/msg/content/0/text")}),
		NewArtifactSliceReader(nil),
		StreamDiffOptions{MaxFindings: 5, WorkDir: t.TempDir()},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DiffArtifactStreamsContext error = %v, want context.Canceled", err)
	}
}

func TestArtifactSliceReaderHonorsContextAndOrder(t *testing.T) {
	t.Parallel()

	reader := NewArtifactSliceReader([]Artifact{
		testArtifact("line:1:/msg/content/0/text"),
		testArtifact("line:2:/msg/content/0/text"),
	})
	first, ok, err := reader.NextArtifact(context.Background())
	if err != nil || !ok {
		t.Fatalf("first NextArtifact = %+v, %v, %v; want artifact", first, ok, err)
	}
	if first.NativeArtifactID != "line:1:/msg/content/0/text" {
		t.Fatalf("first native_artifact_id = %q", first.NativeArtifactID)
	}
	second, ok, err := reader.NextArtifact(context.Background())
	if err != nil || !ok {
		t.Fatalf("second NextArtifact = %+v, %v, %v; want artifact", second, ok, err)
	}
	if second.NativeArtifactID != "line:2:/msg/content/0/text" {
		t.Fatalf("second native_artifact_id = %q", second.NativeArtifactID)
	}
	_, ok, err = reader.NextArtifact(context.Background())
	if err != nil || ok {
		t.Fatalf("exhausted NextArtifact ok=%v err=%v, want ok=false nil error", ok, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = reader.NextArtifact(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled NextArtifact error = %v, want context.Canceled", err)
	}
}

func TestStreamDiffWritersCountsValidationAndWriteAfterFinish(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	diff, err := NewStreamDiff(ctx, StreamDiffOptions{MaxFindings: 10, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStreamDiff: %v", err)
	}
	defer diff.Close()

	source := testArtifact("line:1:/msg/content/0/text")
	canonical := source
	canonical.SchemaVersion = 0
	canonical.Class = ClassToolResponse

	if err := diff.SourceWriter().WriteArtifact(ctx, source); err != nil {
		t.Fatalf("SourceWriter.WriteArtifact: %v", err)
	}
	if err := diff.CanonicalWriter().WriteArtifact(ctx, canonical); err != nil {
		t.Fatalf("CanonicalWriter.WriteArtifact: %v", err)
	}
	if diff.SourceCount() != 1 || diff.CanonicalCount() != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", diff.SourceCount(), diff.CanonicalCount())
	}
	if err := diff.FinishWrites(ctx); err != nil {
		t.Fatalf("FinishWrites: %v", err)
	}
	if err := diff.FinishWrites(ctx); err != nil {
		t.Fatalf("second FinishWrites: %v", err)
	}
	if err := diff.WriteSourceArtifact(ctx, testArtifact("line:2:/msg/content/0/text")); err == nil {
		t.Fatal("WriteSourceArtifact after FinishWrites returned nil, want error")
	}
	result, err := diff.Result(ctx)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	assertFinding(t, result, StateFail, CodeInvalidCanonicalArtifact, SeverityP1)
	assertFinding(t, result, StateFail, CodeMissingCanonical, SeverityP0)
	assertFinding(t, result, StateFail, CodeExtraCanonical, SeverityP1)
}

func TestStreamDiffBuildsLookupIndexesAfterArtifactWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	diff, err := NewStreamDiff(ctx, StreamDiffOptions{MaxFindings: 5, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStreamDiff: %v", err)
	}
	defer diff.Close()

	assertStreamDiffIndexExists(t, diff.db, "idx_artifacts_side_match", false)
	assertStreamDiffIndexExists(t, diff.db, "idx_artifacts_side_classless", false)
	if err := diff.WriteSourceArtifact(ctx, testArtifact("line:1:/msg/content/0/text")); err != nil {
		t.Fatalf("WriteSourceArtifact: %v", err)
	}
	assertStreamDiffIndexExists(t, diff.db, "idx_artifacts_side_match", false)
	assertStreamDiffIndexExists(t, diff.db, "idx_artifacts_side_classless", false)

	if err := diff.FinishWrites(ctx); err != nil {
		t.Fatalf("FinishWrites: %v", err)
	}
	assertStreamDiffIndexExists(t, diff.db, "idx_artifacts_side_match", true)
	assertStreamDiffIndexExists(t, diff.db, "idx_artifacts_side_classless", true)
}

func assertStreamDiffIndexExists(t *testing.T, db *sql.DB, name string, want bool) {
	t.Helper()

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("query index %s: %v", name, err)
	}
	got := count > 0
	if got != want {
		t.Fatalf("index %s exists = %v, want %v", name, got, want)
	}
}

func TestDiffFailsOnMissingCanonicalArtifact(t *testing.T) {
	t.Parallel()

	result := Diff([]Artifact{testArtifact("line:1:/msg/content/0/text")}, nil)

	assertFinding(t, result, StateFail, CodeMissingCanonical, SeverityP0)
}

func TestDiffFailsOnMissingUserImageArtifactAsP0(t *testing.T) {
	t.Parallel()

	source := testArtifact("line:1:/message/content/0")
	source.Class = ClassUserImage
	source.HashDomain = HashCanonicalJSON

	result := Diff([]Artifact{source}, nil)

	assertFinding(t, result, StateFail, CodeMissingCanonical, SeverityP0)
	if len(result.Findings) != 1 || result.Findings[0].Class != ClassUserImage {
		t.Fatalf("findings = %+v, want one P0 missing user_image finding", result.Findings)
	}
}

func TestDiffFailsOnDuplicateCanonicalArtifact(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{
		testArtifact("line:1:/msg/content/0/text"),
		testArtifact("line:1:/msg/content/0/text"),
	}

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeDuplicateCanonical, SeverityP1)
}

func TestDiffReportsClassMismatchInsteadOfMissingAndExtra(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical[0].Class = ClassToolResponse

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeClassMismatch, SeverityP1)
	if len(result.Findings) != 1 {
		t.Fatalf("class mismatch should be a single finding, got %+v", result.Findings)
	}
}

func TestDiffFailsOnHashMismatch(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical[0].ComputedSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeHashMismatch, SeverityP0)
}

func TestDiffFailsOnHashDomainMismatch(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical[0].HashDomain = HashRawBytes

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeHashDomainMismatch, SeverityP1)
}

func TestDiffFailsOnLengthMismatch(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical[0].Bytes = 0

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeBytesMismatch, SeverityP0)
}

func TestDiffFailsOnCharsMismatch(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical[0].Chars = 12

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeCharsMismatch, SeverityP0)
}

func TestDiffRequiresEmptySourceArtifactToRemainPresent(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	source[0].Availability = AvailabilitySourceEmpty
	source[0].Bytes = 0
	source[0].Chars = 0
	source[0].ComputedSHA256 = EmptySHA256

	missing := Diff(source, nil)
	assertFinding(t, missing, StateFail, CodeMissingCanonical, SeverityP0)

	canonical := []Artifact{source[0]}
	clean := Diff(source, canonical)
	if clean.State != StatePass {
		t.Fatalf("empty artifact should pass when preserved, got %+v", clean)
	}
}

func TestDiffRequiresSourceUnavailableArtifactToRemainPresent(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("op:1:2:payload:tool_response:1")}
	source[0].Class = ClassToolResponse
	source[0].Availability = AvailabilitySourceUnavailable
	source[0].HashDomain = ""
	source[0].Selector = Selector{URI: "aiagent-v3://payloads/uncaptured/op-2/tool_response/1"}
	source[0].Bytes = 0
	source[0].Chars = -1
	source[0].ComputedSHA256 = ""

	missing := Diff(source, nil)
	assertFinding(t, missing, StateFail, CodeMissingCanonical, SeverityP1)

	canonical := []Artifact{source[0]}
	clean := Diff(source, canonical)
	if clean.State != StatePass {
		t.Fatalf("source_unavailable artifact should pass when preserved, got %+v", clean)
	}
}

func TestDiffAllowsDocumentedSyntheticCanonicalArtifact(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{
		testArtifact("line:1:/msg/content/0/text"),
		{
			SchemaVersion:    1,
			Adapter:          "codex",
			SourceID:         "codex:testdata",
			NativeSessionID:  "session-1",
			NativeArtifactID: "synthetic:turn_synthesis:turn-1",
			Class:            ClassTurnBoundary,
			Availability:     AvailabilityAvailable,
			HashDomain:       HashIdentityJSON,
			Selector:         Selector{URI: "synthetic://turn_synthesis/turn-1"},
			Bytes:            2,
			Chars:            -1,
			ComputedSHA256:   "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
			Synthetic:        true,
			SyntheticReason:  SyntheticTurnSynthesis,
		},
	}

	result := Diff(source, canonical)
	if result.State != StatePass {
		t.Fatalf("documented synthetic artifact should pass, got %+v", result)
	}
}

func TestDiffFailsOnUndocumentedExtraCanonicalArtifact(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := append([]Artifact{testArtifact("line:1:/msg/content/0/text")}, testArtifact("line:2:/msg/content/0/text"))

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeExtraCanonical, SeverityP1)
}

func TestDiffFailsOnUnverifiableCanonicalArtifact(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical[0].ComputedSHA256 = ""

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeUnverifiableCanonical, SeverityP1)
}

func TestDiffDoesNotRequireStructuralSelectorsToMatch(t *testing.T) {
	t.Parallel()

	source := testArtifact("session:session-1")
	source.Class = ClassSessionBoundary
	source.HashDomain = HashIdentityJSON
	source.Selector = Selector{URI: "file:///repo/session.jsonl#L1"}

	canonical := source
	canonical.Selector = Selector{URI: "canonical://sessions/session-1"}

	result := Diff([]Artifact{source}, []Artifact{canonical})
	if result.State != StatePass {
		t.Fatalf("structural selector mismatch should not fail diff, got %+v", result)
	}
}

func TestDiffFailsOnPayloadSelectorMismatch(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	canonical[0].Selector.JSONPointer = "/msg/content/1/text"

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeSelectorMismatch, SeverityP1)
}

func TestDiffFailsOnAttachmentMetadataSelectorMismatch(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("attachment:att-1")}
	source[0].Class = ClassAttachmentMetadata
	source[0].HashDomain = HashIdentityJSON
	source[0].Selector = Selector{URI: "opencode-sqlite://?part_id=att-1"}

	canonical := []Artifact{source[0]}
	canonical[0].Selector = Selector{URI: "canonical://attachments/att-1"}

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeSelectorMismatch, SeverityP1)
}

func TestDiffFailsOnPatchMetadataSelectorMismatch(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("patch:patch-1")}
	source[0].Class = ClassPatchMetadata
	source[0].HashDomain = HashIdentityJSON
	source[0].Selector = Selector{URI: "opencode-sqlite://?part_id=patch-1"}

	canonical := []Artifact{source[0]}
	canonical[0].Selector = Selector{URI: "canonical://patches/patch-1"}

	result := Diff(source, canonical)

	assertFinding(t, result, StateFail, CodeSelectorMismatch, SeverityP1)
}

func TestDiffMarksCorruptSourceIncomplete(t *testing.T) {
	t.Parallel()

	source := []Artifact{testArtifact("line:1:/msg/content/0/text")}
	source[0].Availability = AvailabilitySourceCorrupt
	source[0].IntegrityFailures = []IntegrityFailure{{
		Field:    "sha256",
		Expected: "producer-hash",
		Actual:   source[0].ComputedSHA256,
	}}

	result := Diff(source, nil)

	assertFinding(t, result, StateIncomplete, CodeSourceCorrupt, SeverityP1)
}

func TestDiffSourceCorruptFindingCarriesIntegrityFailures(t *testing.T) {
	t.Parallel()

	source := testArtifact("line:1:/msg/content/0/text")
	source.Availability = AvailabilitySourceCorrupt
	source.IntegrityFailures = []IntegrityFailure{
		{Field: "original_bytes", Expected: "108300", Actual: "209998"},
		{Field: "compressed_bytes", Expected: "2834", Actual: "4981"},
	}

	result := Diff([]Artifact{source}, nil)

	assertFinding(t, result, StateIncomplete, CodeSourceCorrupt, SeverityP1)
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %+v, want one source_corrupt finding", result.Findings)
	}
	if !reflect.DeepEqual(result.Findings[0].IntegrityFailures, source.IntegrityFailures) {
		t.Fatalf("integrity failures = %+v, want %+v", result.Findings[0].IntegrityFailures, source.IntegrityFailures)
	}
}

func testArtifact(nativeArtifactID string) Artifact {
	return Artifact{
		SchemaVersion:    1,
		Adapter:          "codex",
		SourceID:         "codex:testdata",
		SourceFile:       "sessions/rollout.jsonl",
		NativeSessionID:  "session-1",
		NativeTurnID:     "turn-1",
		NativeArtifactID: nativeArtifactID,
		Class:            ClassAssistantMessage,
		Availability:     AvailabilityAvailable,
		HashDomain:       HashSemanticText,
		Selector: Selector{
			URI:         "file:///repo/testdata/codex/session.jsonl#L1",
			JSONPointer: "/msg/content/0/text",
		},
		Bytes:          13,
		Chars:          13,
		ComputedSHA256: "315f5bdb76d078c43b8ac0064e4a0164612b1fce77c869345bfc94c75894edd3",
	}
}

func assertFinding(t *testing.T, result Result, state ResultState, code FindingCode, severity Severity) {
	t.Helper()

	if result.State != state {
		t.Fatalf("state = %s, want %s; findings=%+v", result.State, state, result.Findings)
	}
	for _, finding := range result.Findings {
		if finding.Code == code && finding.Severity == severity {
			return
		}
	}
	t.Fatalf("missing finding code=%s severity=%s in %+v", code, severity, result.Findings)
}
