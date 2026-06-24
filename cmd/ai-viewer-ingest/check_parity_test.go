package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/parity"
	"github.com/netdata/ai-viewer/internal/paritycheck"
	"github.com/netdata/ai-viewer/internal/store"
)

func TestRunCheckParityTempDBPasses(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{"check-parity", "--source", "aiagent_v3:" + root, "--json", "--log-level", "error"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("check-parity exit = %d, want 0; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StatePass {
		t.Fatalf("state = %q, want %q; findings=%+v", got.State, parity.StatePass, got.Findings)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(got.Sources))
	}
	if got.Sources[0].SourceArtifacts == 0 {
		t.Fatalf("source artifact count = 0, want >0")
	}
	if got.Sources[0].CanonicalArtifacts != got.Sources[0].SourceArtifacts {
		t.Fatalf("canonical artifacts = %d, source artifacts = %d",
			got.Sources[0].CanonicalArtifacts, got.Sources[0].SourceArtifacts)
	}
	if strings.Contains(got.Sources[0].SourceID, root) || strings.Contains(got.Sources[0].Location, root) {
		t.Fatalf("default JSON output leaked source root: %+v", got.Sources[0])
	}
}

func TestRunCheckParityExistingDBMismatchExitsNonZero(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	dbPath := migratedDBPath(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--db", dbPath,
		"--json",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity mismatch exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateFail {
		t.Fatalf("state = %q, want %q; findings=%+v", got.State, parity.StateFail, got.Findings)
	}
	if len(got.Findings) == 0 {
		t.Fatal("findings empty, want missing_canonical findings")
	}
	if got.Findings[0].Code != parity.CodeMissingCanonical {
		t.Fatalf("first finding code = %q, want %q", got.Findings[0].Code, parity.CodeMissingCanonical)
	}
	if raw := readStdout(); strings.Contains(raw, root) || strings.Contains(raw, "session-1") {
		t.Fatalf("default JSON mismatch output leaked private identifiers: %q", raw)
	}
}

func TestRunCheckParityDebugIDsPreservesRawIdentifiers(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	dbPath := migratedDBPath(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--db", dbPath,
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity debug-ids exit = %d, want 1; stderr=%q", code, readStderr())
	}

	raw := readStdout()
	if !strings.Contains(raw, root) || !strings.Contains(raw, "session-1") {
		t.Fatalf("debug JSON output did not preserve raw ids: %q", raw)
	}
}

func TestRedactCheckParityResultRedactsNativeIDsInErrors(t *testing.T) {
	result := paritycheck.CheckResult{
		Sources: []paritycheck.SourceResult{{
			Adapter:  "opencode",
			SourceID: "opencode:/private/state.db",
			Location: "/private/state.db",
			Findings: []parity.Finding{{
				Adapter:          "opencode",
				SourceID:         "opencode:/private/state.db",
				NativeSessionID:  "session-from-finding",
				NativeArtifactID: "artifact-from-finding",
			}},
			Errors: []string{
				`unknown opencode part type "future" in row part-secret-1`,
				`query opencode source parent session "session-secret-1": sql: no rows in result set`,
				`read opencode session_input "input-secret-1": sql: no rows in result set`,
				`structured error mentions session-from-finding and artifact-from-finding`,
				`open /private/state.db: permission denied`,
			},
		}},
	}

	redacted := redactCheckParityResult(result)
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal redacted result: %v", err)
	}
	text := fmt.Sprintf("%+v\n%s", redacted, raw)
	for _, leak := range []string{
		"/private/state.db",
		"part-secret-1",
		"session-secret-1",
		"input-secret-1",
		"session-from-finding",
		"artifact-from-finding",
	} {
		if strings.Contains(text, leak) {
			t.Fatalf("redacted output leaked %q: %s", leak, text)
		}
	}
	for _, marker := range []string{
		"<redacted-location:",
		"<redacted-artifact:",
		"<redacted-session:",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("redacted output missing marker %q: %s", marker, text)
		}
	}
	if !strings.Contains(text, `part type "future"`) {
		t.Fatalf("redaction damaged opencode part-type diagnostic: %s", text)
	}
}

func TestCheckParityRedactionsPreferLongerRawTokens(t *testing.T) {
	redactions := checkParityRedactions{}
	redactions.addRaw("artifact-prefix", "<short>")
	redactions.addRaw("artifact-prefix-long", "<long>")
	redactions.addRaw("zzz", "<zzz>")

	pairs := redactions.orderedPairs()
	if len(pairs) != 3 {
		t.Fatalf("orderedPairs len = %d, want 3", len(pairs))
	}
	if pairs[0].raw != "artifact-prefix-long" {
		t.Fatalf("first redaction = %q, want longest raw token first", pairs[0].raw)
	}

	got := redactCheckParityErrors(
		[]string{"structured error mentions artifact-prefix-long and artifact-prefix"},
		redactions,
	)
	if len(got) != 1 {
		t.Fatalf("redacted errors len = %d, want 1", len(got))
	}
	if strings.Contains(got[0], "artifact-prefix") || strings.Contains(got[0], "-long") {
		t.Fatalf("prefix-related raw id leaked through redaction: %q", got[0])
	}
	if !strings.Contains(got[0], "<long>") || !strings.Contains(got[0], "<short>") {
		t.Fatalf("redacted error missing replacements: %q", got[0])
	}
}

func TestRunCheckParityMaxFindingsCapsDetails(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	dbPath := migratedDBPath(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--db", dbPath,
		"--json",
		"--debug-ids",
		"--max-findings", "1",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity mismatch exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.TotalFindings <= len(got.Findings) {
		t.Fatalf("top-level total_findings=%d, detailed findings=%d; want capped details", got.TotalFindings, len(got.Findings))
	}
	if len(got.Findings) != 1 {
		t.Fatalf("top-level detailed findings = %d, want 1", len(got.Findings))
	}
	if len(got.FindingSummary) == 0 {
		t.Fatal("top-level finding_summary empty, want grouped counts")
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(got.Sources))
	}
	source := got.Sources[0]
	if source.TotalFindings <= len(source.Findings) {
		t.Fatalf("source total_findings=%d, detailed findings=%d; want capped details", source.TotalFindings, len(source.Findings))
	}
	if len(source.Findings) != 1 {
		t.Fatalf("source detailed findings = %d, want 1", len(source.Findings))
	}
	if len(source.FindingSummary) == 0 {
		t.Fatal("source finding_summary empty, want grouped counts")
	}
}

func TestRunCheckParitySampleModeIsNeverFullPass(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--sample", "1",
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity sample exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StateSampleOnly, got)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(got.Sources))
	}
	source := got.Sources[0]
	if source.State != parity.StateSampleOnly {
		t.Fatalf("source state = %q, want %q; source=%+v", source.State, parity.StateSampleOnly, source)
	}
	if source.SourceArtifacts != 1 {
		t.Fatalf("source artifacts = %d, want sampled 1", source.SourceArtifacts)
	}
	if source.CanonicalArtifacts != 1 {
		t.Fatalf("canonical artifacts = %d, want matching sampled 1", source.CanonicalArtifacts)
	}
	if source.TotalFindings != 0 {
		t.Fatalf("sample findings = %d, want clean sampled subset; findings=%+v", source.TotalFindings, source.Findings)
	}
}

func TestRunCheckParityConcurrencyFlagAccepted(t *testing.T) {
	rootA := writeCheckParityAIAgentV3Fixture(t)
	rootB := writeCheckParityAIAgentV3Fixture(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + rootA,
		"--source", "aiagent_v3:" + rootB,
		"--concurrency", "2",
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("check-parity concurrency exit = %d, want 0; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StatePass {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StatePass, got)
	}
	if len(got.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(got.Sources))
	}
	if got.Sources[0].SourceID != "aiagent_v3:"+rootA || got.Sources[1].SourceID != "aiagent_v3:"+rootB {
		t.Fatalf("source order = [%q %q], want requested order", got.Sources[0].SourceID, got.Sources[1].SourceID)
	}
}

func TestRunCheckParityHumanOutputRedactsIdentifiers(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{"check-parity", "--source", "aiagent_v3:" + root, "--log-level", "error"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("check-parity human exit = %d, want 0; stderr=%q", code, readStderr())
	}

	raw := readStdout()
	if !strings.Contains(raw, string(parity.StatePass)) {
		t.Fatalf("human output missing state %q: %q", parity.StatePass, raw)
	}
	if strings.Contains(raw, root) || strings.Contains(raw, "session-1") {
		t.Fatalf("default human output leaked private identifiers: %q", raw)
	}
}

func TestRunCheckParityHumanOutputUsesTotalFindings(t *testing.T) {
	root := writeCheckParityCodexTrailingCorruptFixture(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "codex:" + root,
		"--max-findings", "0",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity human corrupt-source exit = %d, want 1; stderr=%q", code, readStderr())
	}

	raw := readStdout()
	if !strings.Contains(raw, "findings=1") {
		t.Fatalf("human output = %q, want total finding count despite max-findings=0", raw)
	}
	if strings.Contains(raw, "findings=0") {
		t.Fatalf("human output used capped detail count instead of total findings: %q", raw)
	}
	if !strings.Contains(raw, "source_corrupt/source_corruption=1") {
		t.Fatalf("human output = %q, want grouped source_corrupt summary", raw)
	}
	if strings.Contains(raw, root) || strings.Contains(raw, "session-1") {
		t.Fatalf("default human output leaked private identifiers: %q", raw)
	}
}

func TestRunCheckParityUnknownClaudeCodeRecordIsIncomplete(t *testing.T) {
	root := writeCheckParityClaudeCodeUnknownFixture(t)
	dbPath := migratedDBPath(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "claude-code:" + root,
		"--db", dbPath,
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity unknown claude-code exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StateIncomplete, got)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(got.Sources))
	}
	source := got.Sources[0]
	if source.State != parity.StateIncomplete {
		t.Fatalf("source state = %q, want %q; source=%+v", source.State, parity.StateIncomplete, source)
	}
	if len(source.Errors) == 0 || !strings.Contains(source.Errors[0], `unknown claude-code source record type "future-source-artifact"`) {
		t.Fatalf("source errors = %+v, want unknown record type", source.Errors)
	}
}

func TestRunCheckParityPartialCodexSourceErrorStillBuildsTempCanonical(t *testing.T) {
	root := writeCheckParityCodexPartialCorruptFixture(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "codex:" + root,
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity partial codex exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StateIncomplete, got)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(got.Sources))
	}
	source := got.Sources[0]
	if source.State != parity.StateIncomplete {
		t.Fatalf("source state = %q, want %q; source=%+v", source.State, parity.StateIncomplete, source)
	}
	if source.SourceArtifacts == 0 {
		t.Fatalf("source artifacts = 0, want valid Codex artifacts preserved; errors=%+v", source.Errors)
	}
	if source.CanonicalArtifacts == 0 {
		t.Fatalf("canonical artifacts = 0, want temp canonical artifacts preserved despite adapter parse error; errors=%+v", source.Errors)
	}
	assertCheckParityErrorContains(t, source.Errors, "decode legacy flat JSON")
	assertCheckParityErrorContains(t, source.Errors, "adapter reported parse errors")
}

func TestRunCheckParityPartialCodexSourceErrorStillDiffsExistingDB(t *testing.T) {
	root := writeCheckParityCodexPartialCorruptFixture(t)
	dbPath := migratedDBPath(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "codex:" + root,
		"--db", dbPath,
		"--json",
		"--debug-ids",
		"--max-findings", "3",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity partial codex existing-db exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StateIncomplete, got)
	}
	source := got.Sources[0]
	if source.SourceArtifacts == 0 {
		t.Fatalf("source artifacts = 0, want partial source artifacts preserved; errors=%+v", source.Errors)
	}
	if source.TotalFindings == 0 {
		t.Fatalf("total findings = 0, want missing canonical findings from partial diff; source=%+v", source)
	}
	if len(source.FindingSummary) == 0 {
		t.Fatalf("finding summary empty, want grouped partial diff findings; source=%+v", source)
	}
	if len(source.Findings) == 0 || source.Findings[0].Code != parity.CodeMissingCanonical {
		t.Fatalf("first finding = %+v, want missing_canonical from partial diff", source.Findings)
	}
	assertCheckParityErrorContains(t, source.Errors, "decode legacy flat JSON")
}

func TestRunCheckParityTimeoutIsIncomplete(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--timeout", "0s",
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity timeout exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StateIncomplete, got)
	}
	if len(got.Sources) != 1 || got.Sources[0].State != parity.StateIncomplete {
		t.Fatalf("sources = %+v, want one incomplete source", got.Sources)
	}
	if len(got.Sources[0].Errors) == 0 || !strings.Contains(got.Sources[0].Errors[0], "context deadline exceeded") {
		t.Fatalf("source errors = %+v, want context deadline exceeded", got.Sources[0].Errors)
	}
}

func TestRunCheckParityInvalidTimeoutIsUsageError(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--timeout", "not-a-duration",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity invalid timeout exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "invalid --timeout") {
		t.Fatalf("stderr missing invalid timeout: %q", readStderr())
	}
}

func TestRunCheckParityInvalidMaxFindingsIsUsageError(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--max-findings", "-1",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity invalid max-findings exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "invalid --max-findings") {
		t.Fatalf("stderr missing invalid max-findings: %q", readStderr())
	}
}

func TestRunCheckParityInvalidSampleIsUsageError(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--sample", "-1",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity invalid sample exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "invalid --sample") {
		t.Fatalf("stderr missing invalid sample: %q", readStderr())
	}
}

func TestRunCheckParityInvalidConcurrencyIsUsageError(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--concurrency", "0",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity invalid concurrency exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "invalid --concurrency") {
		t.Fatalf("stderr missing invalid concurrency: %q", readStderr())
	}
}

func TestRunCheckParityRejectsRepoWorkDirByDefault(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	repoWorkDir := filepath.Join(".", fmt.Sprintf(".parity-output-forbidden-test-%d", os.Getpid()))
	if err := os.RemoveAll(repoWorkDir); err != nil {
		t.Fatalf("remove stale repo work dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(repoWorkDir) }()
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--work-dir", repoWorkDir,
		"--log-level", "error",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity repo work-dir exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "--work-dir resolves inside the repository") {
		t.Fatalf("stderr missing repo work-dir rejection: %q", readStderr())
	}
	if _, err := os.Stat(repoWorkDir); !os.IsNotExist(err) {
		t.Fatalf("repo work dir was created despite rejection: %v", err)
	}
}

func TestRunCheckParityAllowRepoOutputPermitsRepoWorkDir(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	repoWorkDir := filepath.Join(".", fmt.Sprintf(".parity-output-allowed-test-%d", os.Getpid()))
	if err := os.RemoveAll(repoWorkDir); err != nil {
		t.Fatalf("remove stale repo work dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(repoWorkDir) }()
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--work-dir", repoWorkDir,
		"--allow-repo-output",
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("check-parity allow repo output exit = %d, want 0; stderr=%q", code, readStderr())
	}
	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StatePass {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StatePass, got)
	}
	if _, err := os.Stat(repoWorkDir); err != nil {
		t.Fatalf("repo work dir should exist with override: %v", err)
	}
}

func TestRunCheckParityRejectsRepoWorkDirSymlinkByDefault(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	link := filepath.Join(t.TempDir(), "repo-work-dir")
	if err := os.Symlink(cwd, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--work-dir", link,
		"--log-level", "error",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity symlink repo work-dir exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "--work-dir resolves inside the repository") {
		t.Fatalf("stderr missing repo work-dir rejection: %q", readStderr())
	}
}

func TestRunCheckParityChangedSinceRequiresDB(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--changed-since", "1h",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity changed-since without db exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "--changed-since requires --db") {
		t.Fatalf("stderr missing changed-since db requirement: %q", readStderr())
	}
}

func TestRunCheckParityInvalidChangedSinceIsUsageError(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	dbPath := migratedDBPath(t)
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--db", dbPath,
		"--changed-since", "not-a-duration",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity invalid changed-since exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "invalid --changed-since") {
		t.Fatalf("stderr missing invalid changed-since: %q", readStderr())
	}
}

func TestRunCheckParityChangedSinceSkippedSourceIsSampleOnly(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	sourceID := "aiagent_v3:" + root
	dbPath := migratedDBPath(t)
	seedCheckParityOldSourceProgress(t, dbPath, sourceID, "aiagent_v3", root)
	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", sourceID,
		"--db", dbPath,
		"--changed-since", "1h",
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity changed-since skipped exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StateSampleOnly, got)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(got.Sources))
	}
	source := got.Sources[0]
	if !source.Skipped {
		t.Fatalf("source skipped = false; source=%+v", source)
	}
	if source.State != parity.StateSampleOnly {
		t.Fatalf("source state = %q, want %q", source.State, parity.StateSampleOnly)
	}
	if source.SourceArtifacts != 0 || source.CanonicalArtifacts != 0 {
		t.Fatalf("source artifacts = %d/%d, want skipped zeros", source.SourceArtifacts, source.CanonicalArtifacts)
	}
}

func TestRunCheckParityChangedSinceCursorDoesNotRequireDB(t *testing.T) {
	ctx := context.Background()
	root := writeCheckParityAIAgentV3Fixture(t)
	source := paritycheck.Source{Format: "aiagent_v3", Location: root, SourceID: "aiagent_v3:" + root}
	cursorPath := filepath.Join(t.TempDir(), "parity-resume.json")

	first, err := paritycheck.CheckSources(ctx, paritycheck.Options{
		Sources:     []paritycheck.Source{source},
		ResumePath:  cursorPath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("seed resume cursor CheckSources: %v", err)
	}
	if first.State != parity.StatePass {
		t.Fatalf("seed state = %q, want %q; result=%+v", first.State, parity.StatePass, first)
	}

	stdout, readStdout := captureStderr(t)
	stderr, readStderr := captureStderr(t)
	code := run([]string{
		"check-parity",
		"--source", source.SourceID,
		"--changed-since", "@" + cursorPath,
		"--json",
		"--debug-ids",
		"--log-level", "error",
	}, stdout, stderr)
	if code != 1 {
		t.Fatalf("check-parity changed-since cursor exit = %d, want 1; stderr=%q", code, readStderr())
	}

	got := decodeCheckParityOutput(t, readStdout())
	if got.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", got.State, parity.StateSampleOnly, got)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(got.Sources))
	}
	sourceResult := got.Sources[0]
	if !sourceResult.Skipped {
		t.Fatalf("source skipped = false; source=%+v", sourceResult)
	}
	if sourceResult.SourceArtifacts != 0 || sourceResult.CanonicalArtifacts != 0 {
		t.Fatalf("source artifacts = %d/%d, want skipped zeros", sourceResult.SourceArtifacts, sourceResult.CanonicalArtifacts)
	}
	if !strings.Contains(sourceResult.SkipReason, "changed-since cursor") {
		t.Fatalf("skip reason = %q, want changed-since cursor", sourceResult.SkipReason)
	}
}

func TestRunCheckParityChangedSinceCursorRejectsEmptyPath(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--changed-since", "@",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity empty changed-since cursor exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "invalid --changed-since cursor") {
		t.Fatalf("stderr missing invalid changed-since cursor: %q", readStderr())
	}
}

func TestRunCheckParityResumeRejectsSample(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	resumePath := filepath.Join(t.TempDir(), "resume.json")
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--resume", resumePath,
		"--sample", "1",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity resume with sample exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "--resume cannot be combined with --sample") {
		t.Fatalf("stderr missing resume sample rejection: %q", readStderr())
	}
}

func TestRunCheckParityResumeRejectsChangedSince(t *testing.T) {
	root := writeCheckParityAIAgentV3Fixture(t)
	dbPath := migratedDBPath(t)
	resumePath := filepath.Join(t.TempDir(), "resume.json")
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{
		"check-parity",
		"--source", "aiagent_v3:" + root,
		"--db", dbPath,
		"--resume", resumePath,
		"--changed-since", "1h",
	}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity resume with changed-since exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "--resume cannot be combined with --changed-since") {
		t.Fatalf("stderr missing resume changed-since rejection: %q", readStderr())
	}
}

func TestRunCheckParityRequiresSource(t *testing.T) {
	stdout, _ := captureStderr(t)
	stderr, readStderr := captureStderr(t)

	code := run([]string{"check-parity"}, stdout, stderr)
	if code != 2 {
		t.Fatalf("check-parity with no source exit = %d, want 2", code)
	}
	if !strings.Contains(readStderr(), "at least one --source") {
		t.Fatalf("stderr missing source requirement: %q", readStderr())
	}
}

func writeCheckParityAIAgentV3Fixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "session-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"session-1","sessionId":"session-1","headendId":"cli","capturePayloads":false}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"session-1","sessionId":"session-1","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:04.000Z","originId":"session-1","sessionId":"session-1","turn":1,"status":"ok","ops":[{"opId":"tool-1","opIndex":1,"kind":"tool","name":"read_file","provider":"filesystem","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:05.000Z","originId":"session-1","sessionId":"session-1","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	return root
}

func seedCheckParityOldSourceProgress(t *testing.T, dbPath string, sourceID string, format string, location string) {
	t.Helper()

	ctx := context.Background()
	st, err := store.OpenWriter(ctx, dbPath, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = st.Close() }()
	createdAtUS := time.Now().UTC().UnixMicro()
	oldUpdatedAtUS := time.Now().UTC().Add(-2 * time.Hour).UnixMicro()
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO sources (id, format, location, enabled, parse_errors, created_at) VALUES (?, ?, ?, 1, 0, ?)`,
		sourceID, format, location, createdAtUS); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO source_progress (source_id, last_seq, last_ts_us, updated_at) VALUES (?, 1, 1, ?)`,
		sourceID, oldUpdatedAtUS); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}
}

func writeCheckParityClaudeCodeUnknownFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	transcript := filepath.Join(root, "-repo", "session-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatalf("mkdir claude-code fixture: %v", err)
	}
	line := `{"type":"future-source-artifact","payload":{"text":"must not be ignored"}}`
	if err := os.WriteFile(transcript, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code fixture: %v", err)
	}
	return root
}

func writeCheckParityCodexPartialCorruptFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	valid := filepath.Join(root, "rollout-2025-06-26-11111111-1111-4111-8111-111111111111.json")
	malformed := filepath.Join(root, "rollout-2025-06-27-22222222-2222-4222-8222-222222222222.json")
	body := `{
		"session":{"timestamp":"2025-06-26T00:00:00Z","id":"legacy-session"},
		"items":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"local_shell_call","call_id":"call-1","action":{"cmd":"ls"}},
			{"type":"local_shell_call_output","call_id":"call-1","output":"done"}
		]
	}`
	if err := os.WriteFile(valid, []byte(body), 0o644); err != nil {
		t.Fatalf("write valid codex legacy fixture: %v", err)
	}
	if err := os.WriteFile(malformed, []byte(`{"session":{}, "items":[`), 0o644); err != nil {
		t.Fatalf("write malformed codex legacy fixture: %v", err)
	}
	return root
}

func writeCheckParityCodexTrailingCorruptFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "rollout-2025-07-01-0187df2e-dbd3-4fb3-a837-8e51233dd60a.json")
	validPrefix := `{"session":{"timestamp":"2025-07-01T20:52:59.003Z","id":"session-1"},"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`
	trailingCorruption := `{"type":"message","role":"user","content":[`
	if err := os.WriteFile(sessionFile, []byte(validPrefix+trailingCorruption), 0o644); err != nil {
		t.Fatalf("write trailing-corrupt codex legacy fixture: %v", err)
	}
	return root
}

func assertCheckParityErrorContains(t *testing.T, errors []string, want string) {
	t.Helper()
	for _, err := range errors {
		if strings.Contains(err, want) {
			return
		}
	}
	t.Fatalf("errors = %+v, want substring %q", errors, want)
}

func decodeCheckParityOutput(t *testing.T, raw string) paritycheck.CheckResult {
	t.Helper()

	var got paritycheck.CheckResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode check-parity JSON %q: %v", raw, err)
	}
	return got
}
