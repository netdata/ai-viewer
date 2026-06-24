package paritycheck

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	_ "github.com/netdata/ai-viewer/internal/adapters/claude_code"
	_ "github.com/netdata/ai-viewer/internal/adapters/opencode"
	"github.com/netdata/ai-viewer/internal/parity"
	"github.com/netdata/ai-viewer/internal/store"
	_ "modernc.org/sqlite"
)

func TestSourceSnapshotDetectsModifiedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourceFile := filepath.Join(root, "session", "session-1.jsonl")
	writeParityCheckTestFile(t, sourceFile, "before\n")

	snapshot, err := captureSourceSnapshot(context.Background(), Source{
		Format:   "aiagent_v3",
		Location: root,
	})
	if err != nil {
		t.Fatalf("capture source snapshot: %v", err)
	}

	writeParityCheckTestFile(t, sourceFile, "after\n")

	err = snapshot.Verify(context.Background())
	if err == nil {
		t.Fatal("snapshot Verify returned nil, want mutation error")
	}
	if !strings.Contains(err.Error(), "source snapshot mutated") {
		t.Fatalf("snapshot mutation error = %q, want source snapshot mutated", err)
	}
}

func TestSourceSnapshotSupportsRegularFileRootsAndDetectsRemoval(t *testing.T) {
	t.Parallel()

	sourceFile := filepath.Join(t.TempDir(), "single.jsonl")
	writeParityCheckTestFile(t, sourceFile, "before\n")

	snapshot, err := captureSourceSnapshot(context.Background(), Source{
		Format:   "codex",
		Location: sourceFile,
	})
	if err != nil {
		t.Fatalf("capture source snapshot: %v", err)
	}
	if _, ok := snapshot.Files["."]; !ok {
		t.Fatalf("regular-file snapshot keys = %+v, want dot entry", snapshot.Files)
	}
	if err := snapshot.Verify(context.Background()); err != nil {
		t.Fatalf("verify unchanged regular-file snapshot: %v", err)
	}

	if err := os.Remove(sourceFile); err != nil {
		t.Fatalf("remove source file: %v", err)
	}
	err = snapshot.Verify(context.Background())
	if err == nil {
		t.Fatal("snapshot Verify returned nil after source file removal, want mutation error")
	}
	if !strings.Contains(err.Error(), "stat source snapshot root") {
		t.Fatalf("snapshot removal error = %q, want stat source snapshot root", err)
	}
}

func TestPrepareWorkDirConfiguredAndTemporaryCleanup(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	configured, cleanup, err := prepareWorkDir(parent)
	if err != nil {
		t.Fatalf("prepare configured work dir: %v", err)
	}
	if filepath.Dir(configured) != parent {
		t.Fatalf("configured work dir parent = %q, want %q", filepath.Dir(configured), parent)
	}
	if _, err := os.Stat(configured); err != nil {
		t.Fatalf("stat configured work dir: %v", err)
	}
	cleanup()
	if _, err := os.Stat(configured); err != nil {
		t.Fatalf("configured cleanup should be caller-owned no-op, stat: %v", err)
	}

	temporary, cleanup, err := prepareWorkDir("")
	if err != nil {
		t.Fatalf("prepare temporary work dir: %v", err)
	}
	if _, err := os.Stat(temporary); err != nil {
		t.Fatalf("stat temporary work dir: %v", err)
	}
	cleanup()
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary cleanup stat error = %v, want not exist", err)
	}
}

func TestResolveWorkDirForRepoCheckResolvesExistingSymlinkParent(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	realParent := filepath.Join(base, "real")
	if err := os.MkdirAll(realParent, 0o700); err != nil {
		t.Fatalf("mkdir real parent: %v", err)
	}
	linkParent := filepath.Join(base, "link")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatalf("symlink work parent: %v", err)
	}
	configured := filepath.Join(linkParent, "missing", "child")

	got, err := resolveWorkDirForRepoCheck(configured)
	if err != nil {
		t.Fatalf("resolve work dir: %v", err)
	}
	want := filepath.Join(realParent, "missing", "child")
	if got != want {
		t.Fatalf("resolved work dir = %q, want %q", got, want)
	}
}

func TestValidateWorkDirRejectsRepoContainedOutput(t *testing.T) {
	t.Parallel()

	repoRoot, ok := detectRepoRoot()
	if !ok {
		t.Skip("not running inside a git repository")
	}
	err := validateWorkDir(filepath.Join(repoRoot, ".tmp-parity-workdir-test", t.Name()), false)
	if err == nil {
		t.Fatal("validateWorkDir returned nil for repo-contained output, want error")
	}
	if !strings.Contains(err.Error(), "inside the repository") {
		t.Fatalf("validateWorkDir error = %q, want inside repository", err)
	}
	if err := validateWorkDir(filepath.Join(repoRoot, ".tmp-parity-workdir-test", t.Name()), true); err != nil {
		t.Fatalf("validateWorkDir with allowRepoOutput: %v", err)
	}
}

func TestPathWithinHonorsPathBoundaries(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "tmp", "repo")
	if !pathWithin(root, root) {
		t.Fatal("pathWithin(root, root) = false, want true")
	}
	if !pathWithin(root, filepath.Join(root, "child")) {
		t.Fatal("pathWithin(root, child) = false, want true")
	}
	if pathWithin(root, filepath.Join(string(filepath.Separator), "tmp", "repo-other")) {
		t.Fatal("pathWithin(root, sibling-prefix) = true, want false")
	}
	if pathWithin(root, filepath.Join(string(filepath.Separator), "tmp")) {
		t.Fatal("pathWithin(root, parent) = true, want false")
	}
}

func TestCopySourceSnapshotCopiesDirectoryAndRegularFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	writeParityCheckTestFile(t, filepath.Join(root, "session", "a.jsonl"), "alpha\n")
	writeParityCheckTestFile(t, filepath.Join(root, "session", "nested", "b.jsonl"), "bravo\n")
	frozenRoot := filepath.Join(t.TempDir(), "frozen")

	files, frozenLocation, err := copySourceSnapshot(ctx, root, frozenRoot)
	if err != nil {
		t.Fatalf("copy source snapshot directory: %v", err)
	}
	if frozenLocation != frozenRoot {
		t.Fatalf("frozen directory location = %q, want %q", frozenLocation, frozenRoot)
	}
	for rel, wantBody := range map[string]string{
		"session/a.jsonl":        "alpha\n",
		"session/nested/b.jsonl": "bravo\n",
	} {
		if _, ok := files[rel]; !ok {
			t.Fatalf("snapshot files missing %q: %+v", rel, files)
		}
		body, err := os.ReadFile(filepath.Join(frozenRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read frozen %s: %v", rel, err)
		}
		if string(body) != wantBody {
			t.Fatalf("frozen %s = %q, want %q", rel, string(body), wantBody)
		}
	}

	sourceFile := filepath.Join(t.TempDir(), "single.jsonl")
	writeParityCheckTestFile(t, sourceFile, "single\n")
	fileFrozenRoot := filepath.Join(t.TempDir(), "frozen-file")
	files, frozenLocation, err = copySourceSnapshot(ctx, sourceFile, fileFrozenRoot)
	if err != nil {
		t.Fatalf("copy source snapshot file: %v", err)
	}
	if _, ok := files["."]; !ok {
		t.Fatalf("single-file snapshot missing dot entry: %+v", files)
	}
	if frozenLocation != filepath.Join(fileFrozenRoot, "single.jsonl") {
		t.Fatalf("frozen file location = %q, want %q", frozenLocation, filepath.Join(fileFrozenRoot, "single.jsonl"))
	}
	body, err := os.ReadFile(frozenLocation)
	if err != nil {
		t.Fatalf("read frozen single file: %v", err)
	}
	if string(body) != "single\n" {
		t.Fatalf("frozen single file = %q, want single", string(body))
	}
}

func TestCheckSourcesReportsSnapshotMutation(t *testing.T) {
	t.Parallel()

	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{
		Format:   "aiagent_v3",
		Location: root,
		SourceID: "aiagent_v3:" + root,
	}
	mutationFile := filepath.Join(root, "mutation.tmp")
	dbPath := filepath.Join(t.TempDir(), "index.db")

	writer, err := store.OpenWriter(context.Background(), dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if err := scanSourceIntoDB(context.Background(), writer.DB(), checkLogger(nil), source); err != nil {
		t.Fatalf("scanSourceIntoDB: %v", err)
	}

	result, err := CheckSources(context.Background(), Options{
		DBPath:      dbPath,
		Sources:     []Source{source},
		MaxFindings: 5,
		snapshotHooks: sourceSnapshotHooks{
			afterCapture: func(Source) {
				writeParityCheckTestFile(t, mutationFile, "changed\n")
			},
		},
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateIncomplete, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	got := result.Sources[0]
	if got.State != parity.StateIncomplete {
		t.Fatalf("source state = %q, want %q; source=%+v", got.State, parity.StateIncomplete, got)
	}
	if got.SourceArtifacts == 0 {
		t.Fatalf("source artifacts = 0, want partial artifacts retained; errors=%+v", got.Errors)
	}
	assertParityCheckErrorContains(t, got.Errors, "source snapshot mutated")
}

func TestCheckSourcesNoDBFileBackedUsesFrozenSnapshot(t *testing.T) {
	t.Parallel()

	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{
		Format:   "aiagent_v3",
		Location: root,
		SourceID: "aiagent_v3:" + root,
	}
	mutationFile := filepath.Join(root, "mutation.tmp")

	result, err := CheckSources(context.Background(), Options{
		Sources:     []Source{source},
		MaxFindings: 5,
		snapshotHooks: sourceSnapshotHooks{
			afterCapture: func(Source) {
				writeParityCheckTestFile(t, mutationFile, "changed after frozen snapshot\n")
			},
		},
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StatePass {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StatePass, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	got := result.Sources[0]
	if got.Location != root {
		t.Fatalf("reported location = %q, want original source %q", got.Location, root)
	}
	if got.State != parity.StatePass {
		t.Fatalf("source state = %q, want %q; source=%+v", got.State, parity.StatePass, got)
	}
	if len(got.Errors) != 0 {
		t.Fatalf("source errors = %+v, want none from post-freeze mutation", got.Errors)
	}
	if got.SourceArtifacts == 0 || got.CanonicalArtifacts == 0 {
		t.Fatalf("artifacts = %d/%d, want frozen source and temp canonical evidence", got.SourceArtifacts, got.CanonicalArtifacts)
	}
	if got.TotalFindings != 0 {
		t.Fatalf("findings = %d, want clean frozen-image parity", got.TotalFindings)
	}
}

func TestCheckSourcesReportsNoDBFrozenStageTimings(t *testing.T) {
	t.Parallel()

	root := writeParityCheckAIAgentV3Fixture(t)
	result, err := CheckSources(context.Background(), Options{
		Sources: []Source{{
			Format:   "aiagent_v3",
			Location: root,
			SourceID: "aiagent_v3:" + root,
		}},
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	timings := result.Sources[0].StageTimingsMS
	for _, stage := range []string{
		"capture_source_snapshot",
		"extract_source_manifest",
		"extract_canonical_manifest",
		"scan_temp_canonical_db",
		"extract_canonical_artifacts",
		"diff_manifests",
	} {
		if _, ok := timings[stage]; !ok {
			t.Fatalf("stage_timings_ms missing %q: %+v", stage, timings)
		}
	}
	if _, ok := timings["verify_source_snapshot"]; ok {
		t.Fatalf("stage_timings_ms includes verify_source_snapshot for frozen no-DB run: %+v", timings)
	}
}

func TestCheckSourcesExistingDBReportsVerifyStageTiming(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{
		Format:   "aiagent_v3",
		Location: root,
		SourceID: "aiagent_v3:" + root,
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")

	writer, err := store.OpenWriter(ctx, dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if err := scanSourceIntoDB(ctx, writer.DB(), checkLogger(nil), source); err != nil {
		t.Fatalf("scanSourceIntoDB: %v", err)
	}

	result, err := CheckSources(ctx, Options{
		DBPath:      dbPath,
		Sources:     []Source{source},
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	timings := result.Sources[0].StageTimingsMS
	for _, stage := range []string{
		"capture_source_snapshot",
		"extract_source_manifest",
		"extract_canonical_manifest",
		"verify_source_snapshot",
		"diff_manifests",
	} {
		if _, ok := timings[stage]; !ok {
			t.Fatalf("stage_timings_ms missing %q: %+v", stage, timings)
		}
	}
	for _, stage := range []string{"scan_temp_canonical_db", "extract_canonical_artifacts"} {
		if _, ok := timings[stage]; ok {
			t.Fatalf("stage_timings_ms includes temp canonical stage %q for existing-DB run: %+v", stage, timings)
		}
	}
}

func TestBeginCanonicalReadSnapshotPinsReadableTransaction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+url.QueryEscape(t.Name()+"-snapshot")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Up(ctx, db, checkLogger(nil)); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	tx, err := beginCanonicalReadSnapshot(ctx, db)
	if err != nil {
		t.Fatalf("begin canonical read snapshot: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources`).Scan(&count); err != nil {
		t.Fatalf("query pinned snapshot: %v", err)
	}
	if count != 0 {
		t.Fatalf("source count = %d, want empty migrated db", count)
	}
}

func TestCheckSourcesExistingDBUsesPinnedCanonicalSnapshot(t *testing.T) {
	root := writeParityCheckAIAgentV3Fixture(t)
	sourceID := "aiagent_v3:" + root
	dbPath := filepath.Join(t.TempDir(), "index.db")

	writer, err := store.OpenWriter(context.Background(), dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()

	hookCalled := false
	result, err := CheckSources(context.Background(), Options{
		DBPath: dbPath,
		Sources: []Source{{
			Format:   "aiagent_v3",
			Location: root,
			SourceID: sourceID,
		}},
		MaxFindings: 5,
		canonicalSnapshotHooks: canonicalSnapshotHooks{
			afterPin: func(source Source) {
				hookCalled = true
				if err := scanSourceIntoDB(context.Background(), writer.DB(), checkLogger(nil), source); err != nil {
					t.Fatalf("scanSourceIntoDB after canonical snapshot pin: %v", err)
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if !hookCalled {
		t.Fatal("canonical snapshot hook was not called")
	}
	if result.State != parity.StateFail {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateFail, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	source := result.Sources[0]
	if source.State != parity.StateFail {
		t.Fatalf("source state = %q, want %q; source=%+v", source.State, parity.StateFail, source)
	}
	if source.SourceArtifacts == 0 {
		t.Fatalf("source artifacts = 0, want source manifest evidence")
	}
	if source.CanonicalArtifacts != 0 {
		t.Fatalf("canonical artifacts = %d, want 0 from pre-write pinned snapshot", source.CanonicalArtifacts)
	}
	if source.TotalFindings == 0 {
		t.Fatalf("total findings = 0, want missing canonical findings")
	}
	if len(source.Findings) == 0 || source.Findings[0].Code != parity.CodeMissingCanonical {
		t.Fatalf("first finding = %+v, want missing_canonical", source.Findings)
	}
}

func TestWriteExistingCanonicalArtifactsStreamsScopedSource(t *testing.T) {
	t.Parallel()

	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{
		Format:   "aiagent_v3",
		Location: root,
		SourceID: "aiagent_v3:" + root,
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")

	writer, err := store.OpenWriter(context.Background(), dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if err := scanSourceIntoDB(context.Background(), writer.DB(), checkLogger(nil), source); err != nil {
		t.Fatalf("scanSourceIntoDB: %v", err)
	}

	want, err := parity.ExtractCanonicalForSourceIDs(context.Background(), writer.DB(), []string{source.SourceID})
	if err != nil {
		t.Fatalf("extract scoped canonical manifest: %v", err)
	}

	var got []parity.Artifact
	count, err := writeExistingCanonicalArtifacts(context.Background(), writer.DB(), source, canonicalSnapshotHooks{}, parity.ArtifactWriterFunc(func(ctx context.Context, artifact parity.Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("write existing canonical artifacts: %v", err)
	}
	if count != len(want) {
		t.Fatalf("streamed count = %d, want %d", count, len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed canonical artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestWriteTempCanonicalArtifactsStreamsScopedSource(t *testing.T) {
	t.Parallel()

	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{
		Format:   "aiagent_v3",
		Location: root,
		SourceID: "aiagent_v3:" + root,
	}
	opts := Options{WorkDir: t.TempDir()}

	want, err := extractTempCanonicalArtifacts(context.Background(), opts, checkLogger(nil), source)
	if err != nil {
		t.Fatalf("extract temp canonical artifacts: %v", err)
	}

	var got []parity.Artifact
	count, err := writeTempCanonicalArtifacts(context.Background(), opts, checkLogger(nil), source, parity.ArtifactWriterFunc(func(ctx context.Context, artifact parity.Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("write temp canonical artifacts: %v", err)
	}
	if count != len(want) {
		t.Fatalf("streamed count = %d, want %d", count, len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed temp canonical artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestWriteTempCanonicalArtifactsRunsResolverBeforeExtraction(t *testing.T) {
	t.Parallel()

	root := writeParityCheckClaudeCodeSubagentFixture(t)
	source := Source{
		Format:   "claude-code",
		Location: root,
		SourceID: "claude-code:" + root,
	}

	var got []parity.Artifact
	_, err := writeTempCanonicalArtifacts(context.Background(), Options{WorkDir: t.TempDir()}, checkLogger(nil), source, parity.ArtifactWriterFunc(func(ctx context.Context, artifact parity.Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("write temp canonical artifacts: %v", err)
	}
	if countParityArtifacts(got, parity.ClassSubagentLink) != 1 {
		t.Fatalf("canonical subagent_link artifacts = %d, want 1 after resolver; artifacts=%+v", countParityArtifacts(got, parity.ClassSubagentLink), got)
	}
}

func TestCheckSourcesSampleModeSkipsUnsampledCanonicalPayloadKind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{
		Format:   "aiagent_v3",
		Location: root,
		SourceID: "aiagent_v3:" + root,
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")

	writer, err := store.OpenWriter(ctx, dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if err := scanSourceIntoDB(ctx, writer.DB(), checkLogger(nil), source); err != nil {
		t.Fatalf("scanSourceIntoDB: %v", err)
	}

	var opID string
	err = writer.DB().QueryRowContext(ctx, `
SELECT o.id
FROM ops o
JOIN sessions sess ON sess.id = o.session_id
WHERE sess.source_id = ?
ORDER BY o.seq
LIMIT 1`, source.SourceID).Scan(&opID)
	if err != nil {
		t.Fatalf("select op id: %v", err)
	}
	if _, err := writer.DB().ExecContext(ctx, `
INSERT INTO payload_refs (op_id, kind, format, location_uri)
VALUES (?, 'unexpected_future_kind', 'json', '')`, opID); err != nil {
		t.Fatalf("insert unsampled payload ref: %v", err)
	}

	result, err := CheckSources(ctx, Options{
		DBPath:      dbPath,
		Sources:     []Source{source},
		SampleSize:  1,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateSampleOnly, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	got := result.Sources[0]
	if got.State != parity.StateSampleOnly {
		t.Fatalf("source state = %q, want %q; source=%+v", got.State, parity.StateSampleOnly, got)
	}
	if len(got.Errors) > 0 {
		t.Fatalf("sample mode errors = %+v, want none", got.Errors)
	}
	if got.SourceArtifacts != 1 {
		t.Fatalf("sample source artifacts = %d, want 1", got.SourceArtifacts)
	}
}

func countParityArtifacts(artifacts []parity.Artifact, class parity.ArtifactClass) int {
	var count int
	for _, artifact := range artifacts {
		if artifact.Class == class {
			count++
		}
	}
	return count
}

func TestCapFindingsCopiesAndHandlesNegativeLimit(t *testing.T) {
	t.Parallel()

	findings := []parity.Finding{
		{Code: parity.CodeMissingCanonical},
		{Code: parity.CodeExtraCanonical},
	}
	if got := capFindings(findings, -1); len(got) != 0 {
		t.Fatalf("negative cap findings = %+v, want empty", got)
	}
	got := capFindings(findings, 5)
	if len(got) != len(findings) {
		t.Fatalf("uncapped findings len = %d, want %d", len(got), len(findings))
	}
	got[0].Code = parity.CodeHashMismatch
	if findings[0].Code != parity.CodeMissingCanonical {
		t.Fatalf("capFindings returned aliased slice: source=%+v got=%+v", findings, got)
	}
	got = capFindings(findings, 1)
	if len(got) != 1 || got[0].Code != parity.CodeMissingCanonical {
		t.Fatalf("capped findings = %+v, want first finding only", got)
	}
}

func TestAdapterErrorCollectorIgnoresNilAndJoinsErrors(t *testing.T) {
	t.Parallel()

	var collector adapterErrorCollector
	collector.add(nil)
	if err := collector.err(); err != nil {
		t.Fatalf("empty collector err = %v, want nil", err)
	}
	collector.add(os.ErrNotExist)
	err := collector.err()
	if err == nil {
		t.Fatal("collector err = nil, want joined adapter error")
	}
	if !strings.Contains(err.Error(), "adapter reported parse errors") || !strings.Contains(err.Error(), os.ErrNotExist.Error()) {
		t.Fatalf("collector err = %q, want adapter context and child error", err)
	}
}

func TestAggregateStatePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []SourceResult
		want parity.ResultState
	}{
		{name: "pass", in: []SourceResult{{State: parity.StatePass}}, want: parity.StatePass},
		{name: "sample only", in: []SourceResult{{State: parity.StatePass}, {State: parity.StateSampleOnly}}, want: parity.StateSampleOnly},
		{name: "fail beats sample", in: []SourceResult{{State: parity.StateSampleOnly}, {State: parity.StateFail}}, want: parity.StateFail},
		{name: "incomplete beats fail", in: []SourceResult{{State: parity.StateFail}, {State: parity.StateIncomplete}}, want: parity.StateIncomplete},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := aggregateState(tt.in); got != tt.want {
				t.Fatalf("aggregateState = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeSourcesDefaultsSourceIDAndValidatesRequiredFields(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sources, err := normalizeSources([]Source{{Format: "aiagent_v3", Location: root}})
	if err != nil {
		t.Fatalf("normalizeSources: %v", err)
	}
	if len(sources) != 1 || sources[0].SourceID != "aiagent_v3:"+root {
		t.Fatalf("normalized sources = %+v, want default source_id", sources)
	}
	if _, err := normalizeSources([]Source{{Location: root}}); err == nil {
		t.Fatal("normalizeSources missing format returned nil, want error")
	}
	if _, err := normalizeSources([]Source{{Format: "aiagent_v3"}}); err == nil {
		t.Fatal("normalizeSources missing location returned nil, want error")
	}
}

func TestCheckSourcesSampledCodexLegacyTrailingCorruptionIsStructuredFinding(t *testing.T) {
	t.Parallel()

	root := writeParityCheckCodexLegacyTrailingCorruptionFixture(t)
	result, err := CheckSources(context.Background(), Options{
		Sources: []Source{{
			Format:   "codex",
			Location: root,
			SourceID: "codex:" + root,
		}},
		SampleSize:  1,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateIncomplete, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	source := result.Sources[0]
	if source.State != parity.StateIncomplete {
		t.Fatalf("source state = %q, want %q; source=%+v", source.State, parity.StateIncomplete, source)
	}
	assertParityCheckErrorContains(t, source.Errors, "trailing non-whitespace")
	if source.SourceArtifacts < 2 || source.CanonicalArtifacts == 0 {
		t.Fatalf("artifacts = %d/%d, want sampled source, retained corruption, and canonical artifacts", source.SourceArtifacts, source.CanonicalArtifacts)
	}
	if source.TotalFindings == 0 {
		t.Fatalf("total findings = 0, want source_corrupt finding; source=%+v", source)
	}
	got := source.Findings[0]
	if got.Code != parity.CodeSourceCorrupt || got.Class != parity.ClassSourceCorruption {
		t.Fatalf("first finding = %+v, want source_corrupt source_corruption", got)
	}
}

func TestCheckSourcesSampledAIAgentV2StopsAfterSampledSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeParityCheckAIAgentV2Snapshot(t, filepath.Join(root, "aaa-sampled.json.gz"), "sampled-session")
	writeParityCheckAIAgentV2Snapshot(t, filepath.Join(root, "zzz-unsampled.json.gz"), "unsampled-session")

	result, err := CheckSources(context.Background(), Options{
		Sources: []Source{{
			Format:   "aiagent_v2",
			Location: root,
			SourceID: "aiagent_v2:" + root,
		}},
		SampleSize:  1,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateSampleOnly, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	source := result.Sources[0]
	if source.State != parity.StateSampleOnly {
		t.Fatalf("source state = %q, want %q; source=%+v", source.State, parity.StateSampleOnly, source)
	}
	if len(source.Errors) != 0 {
		t.Fatalf("source errors = %+v, want none", source.Errors)
	}
	if source.SourceArtifacts != 1 || source.CanonicalArtifacts != 1 {
		t.Fatalf("artifacts = %d/%d, want sampled source and canonical only", source.SourceArtifacts, source.CanonicalArtifacts)
	}
	if source.TotalFindings != 0 {
		t.Fatalf("findings = %d, want clean sampled subset; findings=%+v", source.TotalFindings, source.Findings)
	}
}

func TestCheckSourcesSampledOpencodeTempCanonicalSkipsUnsampledRuntimeWarnings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeParityCheckOpencodeSampleScopeFixture(t)
	source := Source{
		Format:   "opencode",
		Location: dbPath,
		SourceID: "opencode:" + dbPath,
	}

	result, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		WorkDir:     t.TempDir(),
		MaxFindings: 5,
		SampleSize:  1,
		Logger:      checkLogger(nil),
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(result.Sources))
	}
	got := result.Sources[0]
	if got.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; errors=%+v findings=%+v", got.State, parity.StateSampleOnly, got.Errors, got.Findings)
	}
	if len(got.Errors) != 0 {
		t.Fatalf("sampled opencode errors = %+v, want none from unsampled malformed session.model", got.Errors)
	}
	if got.SourceArtifacts != 1 || got.CanonicalArtifacts != 1 {
		t.Fatalf("artifacts = %d/%d, want sampled source and canonical only", got.SourceArtifacts, got.CanonicalArtifacts)
	}
	if got.TotalFindings != 0 {
		t.Fatalf("findings = %d, want clean sampled subset; findings=%+v", got.TotalFindings, got.Findings)
	}
}

func TestWriteSourceArtifactsStreamsAIAgentV3(t *testing.T) {
	t.Parallel()

	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{
		Format:   "aiagent_v3",
		Location: root,
		SourceID: "aiagent_v3:" + root,
	}

	want, err := extractSourceArtifacts(context.Background(), source)
	if err != nil {
		t.Fatalf("extract source artifacts: %v", err)
	}

	var got []parity.Artifact
	count, err := writeSourceArtifacts(context.Background(), source, parity.ArtifactWriterFunc(func(ctx context.Context, artifact parity.Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("write source artifacts: %v", err)
	}
	if count != len(want) {
		t.Fatalf("streamed count = %d, want %d", count, len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed source artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestWriteSourceArtifactsStreamsClaudeCodeAndCodex(t *testing.T) {
	t.Parallel()

	claudeRoot := writeParityCheckClaudeCodeFixture(t)
	codexRoot := writeParityCheckCodexFixture(t)
	tests := []Source{
		{Format: "claude-code", Location: claudeRoot, SourceID: "claude-code:" + claudeRoot},
		{Format: "codex", Location: codexRoot, SourceID: "codex:" + codexRoot},
	}

	for _, source := range tests {
		source := source
		t.Run(source.Format, func(t *testing.T) {
			t.Parallel()

			want, err := extractSourceArtifacts(context.Background(), source)
			if err != nil {
				t.Fatalf("extract source artifacts: %v", err)
			}

			var got []parity.Artifact
			count, err := writeSourceArtifacts(context.Background(), source, parity.ArtifactWriterFunc(func(ctx context.Context, artifact parity.Artifact) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				got = append(got, artifact)
				return nil
			}))
			if err != nil {
				t.Fatalf("write source artifacts: %v", err)
			}
			if count != len(want) {
				t.Fatalf("streamed count = %d, want %d", count, len(want))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("streamed source artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
			}
		})
	}
}

func TestWriteSourceArtifactsStreamsAIAgentV2AndOpencode(t *testing.T) {
	t.Parallel()

	aiAgentV2Root := writeParityCheckAIAgentV2Fixture(t)
	opencodeDB := writeParityCheckOpencodeFixture(t)
	tests := []Source{
		{Format: "aiagent_v2", Location: aiAgentV2Root, SourceID: "aiagent_v2:" + aiAgentV2Root},
		{Format: "opencode", Location: opencodeDB, SourceID: "opencode:" + opencodeDB},
	}

	for _, source := range tests {
		source := source
		t.Run(source.Format, func(t *testing.T) {
			t.Parallel()

			want, err := extractSourceArtifacts(context.Background(), source)
			if err != nil {
				t.Fatalf("extract source artifacts: %v", err)
			}

			var got []parity.Artifact
			count, err := writeSourceArtifacts(context.Background(), source, parity.ArtifactWriterFunc(func(ctx context.Context, artifact parity.Artifact) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				got = append(got, artifact)
				return nil
			}))
			if err != nil {
				t.Fatalf("write source artifacts: %v", err)
			}
			if count != len(want) {
				t.Fatalf("streamed count = %d, want %d", count, len(want))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("streamed source artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
			}
		})
	}
}

func TestCheckSourcesRunsSourcesConcurrently(t *testing.T) {
	t.Parallel()

	rootA := writeParityCheckAIAgentV3Fixture(t)
	rootB := writeParityCheckAIAgentV3Fixture(t)
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan struct{})

	var result CheckResult
	var err error
	go func() {
		defer close(done)
		result, err = CheckSources(context.Background(), Options{
			Sources: []Source{
				{Format: "aiagent_v3", Location: rootA, SourceID: "aiagent_v3:" + rootA},
				{Format: "aiagent_v3", Location: rootB, SourceID: "aiagent_v3:" + rootB},
			},
			Concurrency: 2,
			MaxFindings: 5,
			snapshotHooks: sourceSnapshotHooks{
				afterCapture: func(source Source) {
					started <- source.SourceID
					<-release
				},
			},
		})
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("timed out waiting for both source checks to start concurrently")
		}
	}
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent parity check to finish")
	}
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StatePass {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StatePass, result)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(result.Sources))
	}
	if result.Sources[0].SourceID != "aiagent_v3:"+rootA || result.Sources[1].SourceID != "aiagent_v3:"+rootB {
		t.Fatalf("source order = [%q %q], want requested order", result.Sources[0].SourceID, result.Sources[1].SourceID)
	}
}

func TestCheckSourcesChangedSinceSkipsOldProgressAndChecksMissingProgress(t *testing.T) {
	ctx := context.Background()
	oldRoot := writeParityCheckAIAgentV3Fixture(t)
	missingRoot := writeParityCheckAIAgentV3Fixture(t)
	oldSource := Source{Format: "aiagent_v3", Location: oldRoot, SourceID: "aiagent_v3:" + oldRoot}
	missingSource := Source{Format: "aiagent_v3", Location: missingRoot, SourceID: "aiagent_v3:" + missingRoot}
	dbPath := filepath.Join(t.TempDir(), "index.db")

	writer, err := store.OpenWriter(ctx, dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if err := scanSourceIntoDB(ctx, writer.DB(), checkLogger(nil), oldSource); err != nil {
		t.Fatalf("scan old source: %v", err)
	}
	if err := scanSourceIntoDB(ctx, writer.DB(), checkLogger(nil), missingSource); err != nil {
		t.Fatalf("scan missing-progress source: %v", err)
	}

	cutoffUS := time.Now().UTC().UnixMicro()
	oldUpdatedAtUS := cutoffUS - int64(time.Hour/time.Microsecond)
	if _, err := writer.DB().ExecContext(ctx,
		`UPDATE source_progress SET updated_at = ? WHERE source_id = ?`, oldUpdatedAtUS, oldSource.SourceID); err != nil {
		t.Fatalf("age old source progress: %v", err)
	}
	if _, err := writer.DB().ExecContext(ctx,
		`DELETE FROM source_progress WHERE source_id = ?`, missingSource.SourceID); err != nil {
		t.Fatalf("delete missing source progress: %v", err)
	}

	result, err := CheckSources(ctx, Options{
		DBPath:               dbPath,
		Sources:              []Source{oldSource, missingSource},
		MaxFindings:          5,
		ChangedSinceCutoffUS: cutoffUS,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateSampleOnly, result)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(result.Sources))
	}
	oldResult := result.Sources[0]
	if !oldResult.Skipped {
		t.Fatalf("old source skipped = false; source=%+v", oldResult)
	}
	if oldResult.State != parity.StateSampleOnly {
		t.Fatalf("old source state = %q, want %q", oldResult.State, parity.StateSampleOnly)
	}
	if oldResult.SourceArtifacts != 0 || oldResult.CanonicalArtifacts != 0 {
		t.Fatalf("old source artifact counts = %d/%d, want skipped zeros", oldResult.SourceArtifacts, oldResult.CanonicalArtifacts)
	}
	if !strings.Contains(oldResult.SkipReason, "source_progress.updated_at") {
		t.Fatalf("old source skip reason = %q, want source_progress.updated_at", oldResult.SkipReason)
	}

	missingResult := result.Sources[1]
	if missingResult.Skipped {
		t.Fatalf("missing-progress source skipped = true; source=%+v", missingResult)
	}
	if missingResult.State != parity.StateSampleOnly {
		t.Fatalf("missing-progress state = %q, want %q", missingResult.State, parity.StateSampleOnly)
	}
	if missingResult.SourceArtifacts == 0 || missingResult.CanonicalArtifacts == 0 {
		t.Fatalf("missing-progress artifacts = %d/%d, want checked source", missingResult.SourceArtifacts, missingResult.CanonicalArtifacts)
	}
	if missingResult.TotalFindings != 0 {
		t.Fatalf("missing-progress findings = %d, want clean checked subset", missingResult.TotalFindings)
	}
}

func TestCheckSourcesChangedSinceCursorSkipsMatchingAndChecksChanged(t *testing.T) {
	ctx := context.Background()
	unchangedRoot := writeParityCheckAIAgentV3Fixture(t)
	changedRoot := writeParityCheckAIAgentV3Fixture(t)
	sources := []Source{
		{Format: "aiagent_v3", Location: unchangedRoot, SourceID: "aiagent_v3:" + unchangedRoot},
		{Format: "aiagent_v3", Location: changedRoot, SourceID: "aiagent_v3:" + changedRoot},
	}
	cursorPath := filepath.Join(t.TempDir(), "parity-resume.json")

	first, err := CheckSources(ctx, Options{
		Sources:     sources,
		ResumePath:  cursorPath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("first CheckSources: %v", err)
	}
	if first.State != parity.StatePass {
		t.Fatalf("first state = %q, want %q; result=%+v", first.State, parity.StatePass, first)
	}

	writeParityCheckTestFile(t, filepath.Join(changedRoot, "mutation.tmp"), "source changed\n")

	result, err := CheckSources(ctx, Options{
		Sources:                sources,
		ChangedSinceCursorPath: cursorPath,
		MaxFindings:            5,
	})
	if err != nil {
		t.Fatalf("changed-since cursor CheckSources: %v", err)
	}
	if result.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateSampleOnly, result)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(result.Sources))
	}

	unchanged := result.Sources[0]
	if !unchanged.Skipped {
		t.Fatalf("unchanged source skipped = false; source=%+v", unchanged)
	}
	if unchanged.State != parity.StateSampleOnly {
		t.Fatalf("unchanged state = %q, want %q", unchanged.State, parity.StateSampleOnly)
	}
	if unchanged.SourceArtifacts != 0 || unchanged.CanonicalArtifacts != 0 {
		t.Fatalf("unchanged artifacts = %d/%d, want skipped zeros", unchanged.SourceArtifacts, unchanged.CanonicalArtifacts)
	}
	if !strings.Contains(unchanged.SkipReason, "changed-since cursor") {
		t.Fatalf("unchanged skip reason = %q, want changed-since cursor", unchanged.SkipReason)
	}

	changed := result.Sources[1]
	if changed.Skipped {
		t.Fatalf("changed source skipped = true; source=%+v", changed)
	}
	if changed.State != parity.StateSampleOnly {
		t.Fatalf("changed state = %q, want %q", changed.State, parity.StateSampleOnly)
	}
	if changed.SourceArtifacts == 0 || changed.CanonicalArtifacts == 0 {
		t.Fatalf("changed artifacts = %d/%d, want checked source", changed.SourceArtifacts, changed.CanonicalArtifacts)
	}
	if changed.TotalFindings != 0 {
		t.Fatalf("changed findings = %d, want clean checked source", changed.TotalFindings)
	}
}

func TestCheckSourcesChangedSinceCursorMissingFileChecksAll(t *testing.T) {
	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{Format: "aiagent_v3", Location: root, SourceID: "aiagent_v3:" + root}
	cursorPath := filepath.Join(t.TempDir(), "missing", "parity-resume.json")

	result, err := CheckSources(ctx, Options{
		Sources:                []Source{source},
		ChangedSinceCursorPath: cursorPath,
		MaxFindings:            5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateSampleOnly {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateSampleOnly, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	got := result.Sources[0]
	if got.Skipped {
		t.Fatalf("source skipped = true; source=%+v", got)
	}
	if got.SourceArtifacts == 0 || got.CanonicalArtifacts == 0 {
		t.Fatalf("artifacts = %d/%d, want checked source", got.SourceArtifacts, got.CanonicalArtifacts)
	}
}

func TestCheckSourcesChangedSinceCursorCorruptIsIncomplete(t *testing.T) {
	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{Format: "aiagent_v3", Location: root, SourceID: "aiagent_v3:" + root}
	cursorPath := filepath.Join(t.TempDir(), "parity-resume.json")
	if err := os.WriteFile(cursorPath, []byte(`{not-json`), 0o600); err != nil {
		t.Fatalf("write corrupt cursor: %v", err)
	}

	result, err := CheckSources(ctx, Options{
		Sources:                []Source{source},
		ChangedSinceCursorPath: cursorPath,
		MaxFindings:            5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateIncomplete, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	got := result.Sources[0]
	if got.State != parity.StateIncomplete {
		t.Fatalf("source state = %q, want %q", got.State, parity.StateIncomplete)
	}
	assertParityCheckErrorContains(t, got.Errors, "decode resume cursor")
}

func TestCheckSourcesResumeSkipsUnchangedCompletedSource(t *testing.T) {
	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{Format: "aiagent_v3", Location: root, SourceID: "aiagent_v3:" + root}
	resumePath := filepath.Join(t.TempDir(), "parity-resume.json")

	first, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		ResumePath:  resumePath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("first CheckSources: %v", err)
	}
	if first.State != parity.StatePass {
		t.Fatalf("first state = %q, want %q; result=%+v", first.State, parity.StatePass, first)
	}
	if len(first.Sources) != 1 {
		t.Fatalf("first sources len = %d, want 1", len(first.Sources))
	}
	if first.Sources[0].Skipped {
		t.Fatalf("first source skipped = true; source=%+v", first.Sources[0])
	}
	if first.Sources[0].SourceArtifacts == 0 {
		t.Fatalf("first source artifacts = 0, want completed source evidence")
	}

	second, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		ResumePath:  resumePath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("second CheckSources: %v", err)
	}
	if second.State != parity.StatePass {
		t.Fatalf("second state = %q, want %q; result=%+v", second.State, parity.StatePass, second)
	}
	if len(second.Sources) != 1 {
		t.Fatalf("second sources len = %d, want 1", len(second.Sources))
	}
	skipped := second.Sources[0]
	if !skipped.Skipped {
		t.Fatalf("second source skipped = false; source=%+v", skipped)
	}
	if !strings.Contains(skipped.SkipReason, "resume cursor") {
		t.Fatalf("skip reason = %q, want resume cursor", skipped.SkipReason)
	}
	if skipped.SourceArtifacts != first.Sources[0].SourceArtifacts || skipped.CanonicalArtifacts != first.Sources[0].CanonicalArtifacts {
		t.Fatalf("resumed counts = %d/%d, want first counts %d/%d",
			skipped.SourceArtifacts, skipped.CanonicalArtifacts,
			first.Sources[0].SourceArtifacts, first.Sources[0].CanonicalArtifacts)
	}
}

func TestCheckSourcesResumeRerunsChangedSourceSnapshot(t *testing.T) {
	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{Format: "aiagent_v3", Location: root, SourceID: "aiagent_v3:" + root}
	resumePath := filepath.Join(t.TempDir(), "parity-resume.json")

	first, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		ResumePath:  resumePath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("first CheckSources: %v", err)
	}
	if first.State != parity.StatePass {
		t.Fatalf("first state = %q, want %q; result=%+v", first.State, parity.StatePass, first)
	}

	writeParityCheckTestFile(t, filepath.Join(root, "mutation.tmp"), "source changed\n")

	second, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		ResumePath:  resumePath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("second CheckSources: %v", err)
	}
	if second.State != parity.StatePass {
		t.Fatalf("second state = %q, want %q; result=%+v", second.State, parity.StatePass, second)
	}
	if len(second.Sources) != 1 {
		t.Fatalf("second sources len = %d, want 1", len(second.Sources))
	}
	if second.Sources[0].Skipped {
		t.Fatalf("changed source reused resume cursor; source=%+v", second.Sources[0])
	}
}

func TestCheckSourcesResumeSkippedSourceStillVerifiesSnapshotMutation(t *testing.T) {
	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{Format: "aiagent_v3", Location: root, SourceID: "aiagent_v3:" + root}
	resumePath := filepath.Join(t.TempDir(), "parity-resume.json")

	first, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		ResumePath:  resumePath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("first CheckSources: %v", err)
	}
	if first.State != parity.StatePass {
		t.Fatalf("first state = %q, want %q; result=%+v", first.State, parity.StatePass, first)
	}

	result, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		ResumePath:  resumePath,
		MaxFindings: 5,
		snapshotHooks: sourceSnapshotHooks{
			afterCapture: func(Source) {
				writeParityCheckTestFile(t, filepath.Join(root, "mutation.tmp"), "changed during resumed run\n")
			},
		},
	})
	if err != nil {
		t.Fatalf("resumed CheckSources: %v", err)
	}
	if result.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateIncomplete, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	sourceResult := result.Sources[0]
	if !sourceResult.Skipped {
		t.Fatalf("source skipped = false; source=%+v", sourceResult)
	}
	assertParityCheckErrorContains(t, sourceResult.Errors, "source snapshot mutated")
}

func TestCheckSourcesResumeCorruptCursorIsIncomplete(t *testing.T) {
	ctx := context.Background()
	root := writeParityCheckAIAgentV3Fixture(t)
	source := Source{Format: "aiagent_v3", Location: root, SourceID: "aiagent_v3:" + root}
	resumePath := filepath.Join(t.TempDir(), "parity-resume.json")
	if err := os.WriteFile(resumePath, []byte(`{not-json`), 0o600); err != nil {
		t.Fatalf("write corrupt resume cursor: %v", err)
	}

	result, err := CheckSources(ctx, Options{
		Sources:     []Source{source},
		ResumePath:  resumePath,
		MaxFindings: 5,
	})
	if err != nil {
		t.Fatalf("CheckSources: %v", err)
	}
	if result.State != parity.StateIncomplete {
		t.Fatalf("state = %q, want %q; result=%+v", result.State, parity.StateIncomplete, result)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(result.Sources))
	}
	if result.Sources[0].State != parity.StateIncomplete {
		t.Fatalf("source state = %q, want %q", result.Sources[0].State, parity.StateIncomplete)
	}
	if len(result.Sources[0].Errors) == 0 || !strings.Contains(result.Sources[0].Errors[0], "decode resume cursor") {
		t.Fatalf("source errors = %+v, want decode resume cursor", result.Sources[0].Errors)
	}
}

func writeParityCheckAIAgentV3Fixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "session-1.jsonl")
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"session-1","sessionId":"session-1","headendId":"cli","capturePayloads":false}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"session-1","sessionId":"session-1","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:04.000Z","originId":"session-1","sessionId":"session-1","turn":1,"status":"ok","ops":[{"opId":"tool-1","opIndex":1,"kind":"tool","name":"read_file","provider":"filesystem","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:05.000Z","originId":"session-1","sessionId":"session-1","status":"ok"}`,
	}
	writeParityCheckTestFile(t, sessionFile, strings.Join(lines, "\n")+"\n")
	return root
}

func writeParityCheckClaudeCodeFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	transcript := filepath.Join(root, "-repo", "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"answer"}]}}`,
	}
	writeParityCheckTestFile(t, transcript, strings.Join(lines, "\n")+"\n")
	return root
}

func writeParityCheckCodexFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "sessions", "2026", "06", "22", "rollout.jsonl")
	lines := []string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1","timestamp":"2026-06-22T00:00:00Z"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
	}
	writeParityCheckTestFile(t, sessionFile, strings.Join(lines, "\n")+"\n")
	return root
}

func writeParityCheckCodexLegacyTrailingCorruptionFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "rollout-2025-07-01-0187df2e-dbd3-4fb3-a837-8e51233dd60a.json")
	validPrefix := `{"session":{"timestamp":"2025-07-01T20:52:59.003Z","id":"session-1"},"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`
	trailingCorruption := `{"type":"message","role":"user","content":[`
	writeParityCheckTestFile(t, sessionFile, validPrefix+trailingCorruption)
	return root
}

func writeParityCheckAIAgentV2Fixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "root-node",
			"traceId":   "session-1",
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_001_000),
			"success":   true,
		},
	}
	writeParityCheckGzipJSON(t, filepath.Join(root, "session-1.json.gz"), snapshot)
	return root
}

func writeParityCheckClaudeCodeSubagentFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	parentSessionID := "parent-session"
	agentID := "a1b2c3d4e5f6071"
	toolUseID := "toolu_agent_1"
	subagentDir := filepath.Join(projectDir, parentSessionID, "subagents")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code subagent fixture: %v", err)
	}
	parentLines := []string{
		`{"type":"user","uuid":"u1","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"delegate"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"parent-session","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"` + toolUseID + `","name":"Agent","input":{"description":"explore repository","subagent_type":"general-purpose","prompt":"inspect"}}]}}`,
	}
	childLines := []string{
		`{"type":"user","uuid":"cu1","parentUuid":null,"isSidechain":true,"agentId":"` + agentID + `","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","uuid":"ca1","parentUuid":"cu1","isSidechain":true,"agentId":"` + agentID + `","sessionId":"parent-session","requestId":"req-2","timestamp":"2026-06-22T00:00:05.000Z","message":{"id":"m2","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":4,"output_tokens":2},"content":[{"type":"text","text":"done"}]}}`,
	}
	writeParityCheckTestFile(t, filepath.Join(projectDir, parentSessionID+".jsonl"), strings.Join(parentLines, "\n")+"\n")
	writeParityCheckTestFile(t, filepath.Join(subagentDir, "agent-"+agentID+".jsonl"), strings.Join(childLines, "\n")+"\n")
	writeParityCheckTestFile(t, filepath.Join(subagentDir, "agent-"+agentID+".meta.json"), `{"agentType":"general-purpose","toolUseId":"`+toolUseID+`"}`)
	return root
}

func writeParityCheckGzipJSON(t *testing.T, path string, value interface{}) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	gz := gzip.NewWriter(file)
	if err := json.NewEncoder(gz).Encode(value); err != nil {
		_ = gz.Close()
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip %s: %v", path, err)
	}
}

func writeParityCheckOpencodeFixture(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			seq INTEGER NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('ses_open01', 'prj_x', '', 'calm-otter', '/work/proj', 'Opencode parity', '1.0.0', 'general',
			 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES
			('msg_assistant', 'ses_open01', 2000, 9000,
			 '{"role":"assistant","parentID":"","providerID":"anthropic","modelID":"claude-x","time":{"created":2000,"completed":9000}}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES
			('prt_text', 'msg_assistant', 'ses_open01', 2400, 2400, '{"type":"text","text":"answer"}')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	return dbPath
}

func writeParityCheckOpencodeSampleScopeFixture(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode sample fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			seq INTEGER NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('aaa_sample', 'prj_x', '', 'sample', '/work/sample', 'Sample', '1.0.0', 'general',
			 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL),
			('zzz_bad', 'prj_x', '', 'bad', '/work/bad', 'Bad', '1.0.0', 'general',
			 '{not json', 2000, 9000, NULL)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES
			('msg_sample', 'aaa_sample', 2100, 9000,
			 '{"role":"assistant","parentID":"","providerID":"anthropic","modelID":"claude-x","time":{"created":2100,"completed":9000}}'),
			('msg_bad', 'zzz_bad', 2200, 9000,
			 '{"role":"assistant","parentID":"","providerID":"anthropic","modelID":"claude-x","time":{"created":2200,"completed":9000}}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES
			('prt_sample', 'msg_sample', 'aaa_sample', 2400, 2400, '{"type":"text","text":"sampled"}'),
			('prt_bad', 'msg_bad', 'zzz_bad', 2500, 2500, '{"type":"text","text":"unsampled"}')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode sample fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	return dbPath
}

func writeParityCheckTestFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertParityCheckErrorContains(t *testing.T, errors []string, want string) {
	t.Helper()
	for _, err := range errors {
		if strings.Contains(err, want) {
			return
		}
	}
	t.Fatalf("errors = %+v, want substring %q", errors, want)
}
