package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdapterAvailabilityMatrixCoversEveryAdapterAndClass(t *testing.T) {
	matrices := AdapterAvailabilityMatrices()
	expectedAdapters := []string{"aiagent_v2", "aiagent_v3", "claude-code", "codex", "opencode"}
	classes := AllArtifactClasses()

	for _, adapter := range expectedAdapters {
		rows, ok := matrices[adapter]
		if !ok {
			t.Fatalf("AdapterAvailabilityMatrices missing adapter %q", adapter)
		}
		if len(rows) != len(classes) {
			t.Fatalf("adapter %q has %d matrix rows, want %d", adapter, len(rows), len(classes))
		}
		seen := make(map[ArtifactClass]bool, len(rows))
		for _, row := range rows {
			if row.Adapter != adapter {
				t.Fatalf("adapter %q row for class %q has Adapter=%q", adapter, row.Class, row.Adapter)
			}
			if seen[row.Class] {
				t.Fatalf("adapter %q has duplicate matrix row for class %q", adapter, row.Class)
			}
			seen[row.Class] = true
			assertValidMatrixRow(t, row)
		}
		for _, class := range classes {
			if !seen[class] {
				t.Fatalf("adapter %q missing matrix row for class %q", adapter, class)
			}
		}
	}
}

func TestAdapterAvailabilityMatrixSpecTablesCoverEveryMachineRow(t *testing.T) {
	specFiles := map[string]string{
		"aiagent_v2":  "adapter-aiagent-v2.md",
		"aiagent_v3":  "adapter-aiagent-v3.md",
		"claude-code": "adapter-claude-code.md",
		"codex":       "adapter-codex.md",
		"opencode":    "adapter-opencode.md",
	}
	matrices := AdapterAvailabilityMatrices()

	for adapter, rows := range matrices {
		specFile, ok := specFiles[adapter]
		if !ok {
			t.Fatalf("no spec file registered for matrix adapter %q", adapter)
		}
		classes := parseSpecMatrixClasses(t, specFile)
		if len(classes) != len(rows) {
			t.Fatalf("spec %s has %d machine-readable matrix class rows, want %d", specFile, len(classes), len(rows))
		}
		for _, row := range rows {
			if !classes[row.Class] {
				t.Fatalf("spec %s missing machine-readable matrix row for class %q", specFile, row.Class)
			}
		}
	}
}

func TestAdapterAvailabilityMatrixHasNoOpenSOWGaps(t *testing.T) {
	for adapter, rows := range AdapterAvailabilityMatrices() {
		for _, row := range rows {
			if matrixRowAllows(row, MatrixUnknown) {
				t.Fatalf("adapter %q class %q still allows %q", adapter, row.Class, MatrixUnknown)
			}
			assertNoOpenMatrixGap(t, row)
		}
	}
}

func TestAIAgentV2UserArtifactsAreNotSourceVisible(t *testing.T) {
	for _, class := range []ArtifactClass{ClassUserPrompt, ClassUserImage} {
		row, ok := matrixRowFor("aiagent_v2", class)
		if !ok {
			t.Fatalf("missing aiagent_v2 matrix row for %s", class)
		}
		if !matrixRowAllows(row, MatrixNotSourceVisible) {
			t.Fatalf("aiagent_v2 %s availability = %v, want %s", class, row.Availability, MatrixNotSourceVisible)
		}
		if row.CanonicalRepresentation != "none" {
			t.Fatalf("aiagent_v2 %s canonical representation = %q, want none", class, row.CanonicalRepresentation)
		}
	}
}

func TestAIAgentV2SystemOpMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("aiagent_v2", ClassSystemOp)
	if !ok {
		t.Fatal("missing aiagent_v2 system_op matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("aiagent_v2 system_op availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("aiagent_v2 system_op hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "ops.kind=system row" {
		t.Fatalf("aiagent_v2 system_op canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestAIAgentV2SessionMetadataMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("aiagent_v2", ClassSessionMetadata)
	if !ok {
		t.Fatal("missing aiagent_v2 session_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("aiagent_v2 session_metadata availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("aiagent_v2 session_metadata hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "sessions row plus sessions.extras_json" {
		t.Fatalf("aiagent_v2 session_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestAIAgentV2CompactionEventMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("aiagent_v2", ClassCompactionEvent)
	if !ok {
		t.Fatal("missing aiagent_v2 compaction_event matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("aiagent_v2 compaction_event availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("aiagent_v2 compaction_event hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "history-compaction step session op row plus ops.extras_json" {
		t.Fatalf("aiagent_v2 compaction_event canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestAIAgentV2AttachmentMetadataMatrixNotSourceVisible(t *testing.T) {
	row, ok := matrixRowFor("aiagent_v2", ClassAttachmentMetadata)
	if !ok {
		t.Fatal("missing aiagent_v2 attachment_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixNotSourceVisible) {
		t.Fatalf("aiagent_v2 attachment_metadata availability = %v, want %s", row.Availability, MatrixNotSourceVisible)
	}
	if row.CanonicalRepresentation != "none" {
		t.Fatalf("aiagent_v2 attachment_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestAIAgentV3SystemOpMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("aiagent_v3", ClassSystemOp)
	if !ok {
		t.Fatal("missing aiagent_v3 system_op matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("aiagent_v3 system_op availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("aiagent_v3 system_op hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "ops.kind=system row" {
		t.Fatalf("aiagent_v3 system_op canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestAIAgentV3SessionMetadataMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("aiagent_v3", ClassSessionMetadata)
	if !ok {
		t.Fatal("missing aiagent_v3 session_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("aiagent_v3 session_metadata availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("aiagent_v3 session_metadata hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "sessions row plus sessions.extras_json" {
		t.Fatalf("aiagent_v3 session_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestAIAgentV3CompactionEventMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("aiagent_v3", ClassCompactionEvent)
	if !ok {
		t.Fatal("missing aiagent_v3 compaction_event matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("aiagent_v3 compaction_event availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("aiagent_v3 compaction_event hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "history-compaction session ops row plus ops.extras_json" {
		t.Fatalf("aiagent_v3 compaction_event canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestAIAgentV3PayloadMatrixAllowsSourceEmpty(t *testing.T) {
	t.Parallel()

	for _, class := range []ArtifactClass{
		ClassReasoningText,
		ClassLLMRequest,
		ClassLLMResponse,
		ClassLLMSDKRequest,
		ClassLLMSDKResponse,
		ClassToolRequest,
		ClassToolResponse,
	} {
		row, ok := matrixRowFor("aiagent_v3", class)
		if !ok {
			t.Fatalf("missing aiagent_v3 %s matrix row", class)
		}
		if !matrixRowAllows(row, MatrixSourceEmpty) {
			t.Fatalf("aiagent_v3 %s availability = %v, want %s", class, row.Availability, MatrixSourceEmpty)
		}
	}
}

func TestClaudeCodeSystemOpMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("claude-code", ClassSystemOp)
	if !ok {
		t.Fatal("missing claude-code system_op matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("claude-code system_op availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("claude-code system_op hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "logged system record row" {
		t.Fatalf("claude-code system_op canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestClaudeCodeSessionMetadataMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("claude-code", ClassSessionMetadata)
	if !ok {
		t.Fatal("missing claude-code session_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("claude-code session_metadata availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("claude-code session_metadata hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "sessions row plus sessions.extras_json" {
		t.Fatalf("claude-code session_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestCodexSessionMetadataMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("codex", ClassSessionMetadata)
	if !ok {
		t.Fatal("missing codex session_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("codex session_metadata availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("codex session_metadata hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "sessions row plus sessions.extras_json" {
		t.Fatalf("codex session_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestCodexSystemOpMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("codex", ClassSystemOp)
	if !ok {
		t.Fatal("missing codex system_op matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("codex system_op availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("codex system_op hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "log_entries rows for lifecycle/review/default metadata events" {
		t.Fatalf("codex system_op canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestCodexLLMErrorMatrixNotSourceVisible(t *testing.T) {
	row, ok := matrixRowFor("codex", ClassLLMError)
	if !ok {
		t.Fatal("missing codex llm_error matrix row")
	}
	if !matrixRowAllows(row, MatrixNotSourceVisible) {
		t.Fatalf("codex llm_error availability = %v, want %s", row.Availability, MatrixNotSourceVisible)
	}
	if row.CanonicalRepresentation != "none as an LLM op error; generic errors are log_entry" {
		t.Fatalf("codex llm_error canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestCodexUserImageMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("codex", ClassUserImage)
	if !ok {
		t.Fatal("missing codex user_image matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("codex user_image availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashCanonicalJSON) {
		t.Fatalf("codex user_image hash domains = %v, want %s", row.HashDomains, HashCanonicalJSON)
	}
	if row.CanonicalRepresentation != "internal user-input op plus payload_refs.kind=tool_request" {
		t.Fatalf("codex user_image canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestCodexAttachmentMetadataMatrixNotSourceVisible(t *testing.T) {
	row, ok := matrixRowFor("codex", ClassAttachmentMetadata)
	if !ok {
		t.Fatal("missing codex attachment_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixNotSourceVisible) {
		t.Fatalf("codex attachment_metadata availability = %v, want %s", row.Availability, MatrixNotSourceVisible)
	}
	if row.CanonicalRepresentation != "none" {
		t.Fatalf("codex attachment_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestOpencodeCompactionEventMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("opencode", ClassCompactionEvent)
	if !ok {
		t.Fatal("missing opencode compaction_event matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("opencode compaction_event availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("opencode compaction_event hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "compaction part log row plus part_id extras" {
		t.Fatalf("opencode compaction_event canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestOpencodeLogEntryMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("opencode", ClassLogEntry)
	if !ok {
		t.Fatal("missing opencode log_entry matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) || !matrixRowAllows(row, MatrixSourceEmpty) {
		t.Fatalf("opencode log_entry availability = %v, want available/source_empty", row.Availability)
	}
	if !matrixRowAllowsHash(row, HashSemanticText) {
		t.Fatalf("opencode log_entry hash domains = %v, want %s", row.HashDomains, HashSemanticText)
	}
	if row.CanonicalRepresentation != "log row for compaction/retry/file parts and known session_message rows" {
		t.Fatalf("opencode log_entry canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestOpencodeAttachmentMetadataMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("opencode", ClassAttachmentMetadata)
	if !ok {
		t.Fatal("missing opencode attachment_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("opencode attachment_metadata availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("opencode attachment_metadata hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "file part log row plus part_id extras" {
		t.Fatalf("opencode attachment_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestOpencodeUserImageMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("opencode", ClassUserImage)
	if !ok {
		t.Fatal("missing opencode user_image matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("opencode user_image availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashCanonicalJSON) {
		t.Fatalf("opencode user_image hash domains = %v, want %s", row.HashDomains, HashCanonicalJSON)
	}
	if row.CanonicalRepresentation != "internal user-input op plus payload_refs.kind=tool_request over image file objects" {
		t.Fatalf("opencode user_image canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestOpencodeLLMErrorMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("opencode", ClassLLMError)
	if !ok {
		t.Fatal("missing opencode llm_error matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("opencode llm_error availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("opencode llm_error hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "failed opencode turn plus terminal session error detail when present" {
		t.Fatalf("opencode llm_error canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestOpencodeSessionMetadataMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("opencode", ClassSessionMetadata)
	if !ok {
		t.Fatal("missing opencode session_metadata matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("opencode session_metadata availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("opencode session_metadata hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "sessions row plus first-class session columns and sessions.extras_json" {
		t.Fatalf("opencode session_metadata canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestOpencodeSystemOpMatrixAvailable(t *testing.T) {
	row, ok := matrixRowFor("opencode", ClassSystemOp)
	if !ok {
		t.Fatal("missing opencode system_op matrix row")
	}
	if !matrixRowAllows(row, MatrixAvailable) {
		t.Fatalf("opencode system_op availability = %v, want %s", row.Availability, MatrixAvailable)
	}
	if !matrixRowAllowsHash(row, HashIdentityJSON) {
		t.Fatalf("opencode system_op hash domains = %v, want %s", row.HashDomains, HashIdentityJSON)
	}
	if row.CanonicalRepresentation != "session-scoped log row with session_message_id parity extras" {
		t.Fatalf("opencode system_op canonical representation = %q", row.CanonicalRepresentation)
	}
}

func TestDiffReportsMatrixMismatch(t *testing.T) {
	result := Diff([]Artifact{
		{
			SchemaVersion:    SchemaVersion,
			Adapter:          "codex",
			SourceID:         "codex:/tmp/source",
			NativeSessionID:  "session-1",
			NativeArtifactID: "llm-request",
			Class:            ClassLLMRequest,
			Availability:     AvailabilityAvailable,
			HashDomain:       HashRawBytes,
			Selector: Selector{
				URI: "file:///tmp/source/rollout.jsonl#L1",
			},
			Bytes:          2,
			Chars:          -1,
			ComputedSHA256: "b413f47d13ee2fe6c845b2ee141e7c412c2ca2bdf05f45d8a7e02d27a7caf844",
		},
	}, nil)

	if !resultHasCode(result, CodeMatrixMismatch) {
		t.Fatalf("Diff missing %q finding; got %+v", CodeMatrixMismatch, result.Findings)
	}
}

func assertValidMatrixRow(t *testing.T, row MatrixRow) {
	t.Helper()
	if row.Adapter == "" {
		t.Fatal("matrix row has empty adapter")
	}
	if row.Class == "" {
		t.Fatalf("adapter %q matrix row has empty class", row.Adapter)
	}
	if len(row.Availability) == 0 {
		t.Fatalf("adapter %q class %q has no availability states", row.Adapter, row.Class)
	}
	if row.CanonicalRepresentation == "" {
		t.Fatalf("adapter %q class %q has empty canonical representation", row.Adapter, row.Class)
	}
	if row.SelectorRule == "" {
		t.Fatalf("adapter %q class %q has empty selector rule", row.Adapter, row.Class)
	}
	if row.Evidence == "" {
		t.Fatalf("adapter %q class %q has empty evidence", row.Adapter, row.Class)
	}
	if matrixRowNeedsHashDomain(row) && len(row.HashDomains) == 0 {
		t.Fatalf("adapter %q class %q has no hash domains", row.Adapter, row.Class)
	}
}

func assertNoOpenMatrixGap(t *testing.T, row MatrixRow) {
	t.Helper()
	if row.CanonicalRepresentation == "open SOW-0097 gap" {
		t.Fatalf("adapter %q class %q has open canonical representation placeholder", row.Adapter, row.Class)
	}
	if row.SelectorRule == "class contract not closed yet" {
		t.Fatalf("adapter %q class %q has open selector rule placeholder", row.Adapter, row.Class)
	}
	if row.Evidence == "adapter spec machine-readable matrix rows" {
		t.Fatalf("adapter %q class %q has open evidence placeholder", row.Adapter, row.Class)
	}
}

func matrixRowNeedsHashDomain(row MatrixRow) bool {
	for _, availability := range row.Availability {
		switch availability {
		case MatrixNotSourceVisible, MatrixUnknown, MatrixSourceUnavailable:
			continue
		default:
			return true
		}
	}
	return false
}

func parseSpecMatrixClasses(t *testing.T, specFile string) map[ArtifactClass]bool {
	t.Helper()
	path := filepath.Join("..", "..", ".agents", "sow", "specs", specFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	const marker = "Machine-readable matrix rows:"
	for i, line := range lines {
		if strings.TrimSpace(line) == marker {
			return parseSpecMatrixClassesAfterMarker(t, specFile, lines[i+1:])
		}
	}
	t.Fatalf("spec %s missing %q", specFile, marker)
	return nil
}

func parseSpecMatrixClassesAfterMarker(t *testing.T, specFile string, lines []string) map[ArtifactClass]bool {
	t.Helper()
	classes := make(map[ArtifactClass]bool)
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if inTable {
				break
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			if inTable {
				break
			}
			continue
		}
		if strings.Contains(trimmed, "|---") {
			inTable = true
			continue
		}
		if strings.Contains(trimmed, "| Class |") {
			inTable = true
			continue
		}
		inTable = true
		cells := strings.Split(trimmed, "|")
		if len(cells) < 3 {
			continue
		}
		classCell := strings.TrimSpace(cells[1])
		if !strings.HasPrefix(classCell, "`") || !strings.HasSuffix(classCell, "`") {
			t.Fatalf("spec %s has malformed matrix class cell %q", specFile, classCell)
		}
		class := ArtifactClass(strings.Trim(classCell, "`"))
		if classes[class] {
			t.Fatalf("spec %s has duplicate machine-readable matrix row for class %q", specFile, class)
		}
		classes[class] = true
	}
	if len(classes) == 0 {
		t.Fatalf("spec %s has empty machine-readable matrix table", specFile)
	}
	return classes
}

func resultHasCode(result Result, code FindingCode) bool {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
