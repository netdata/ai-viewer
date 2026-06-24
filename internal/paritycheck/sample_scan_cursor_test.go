package paritycheck

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v2"
	"github.com/netdata/ai-viewer/internal/adapters/codex"
	"github.com/netdata/ai-viewer/internal/parity"
	"github.com/netdata/ai-viewer/internal/store"
)

func TestSampledCodexTempCanonicalCursorSkipsUnsampledRollouts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	sampledPath := filepath.Join(root, "2026", "06", "22", "rollout-2026-06-22T00-00-00-11111111-1111-1111-1111-111111111111.jsonl")
	unsampledPath := filepath.Join(root, "2026", "06", "23", "rollout-2026-06-23T00-00-00-22222222-2222-2222-2222-222222222222.jsonl")
	writeParityCheckTestFile(t, sampledPath, parityCheckCodexRollout("sampled-session", "sampled"))
	writeParityCheckTestFile(t, unsampledPath, parityCheckCodexRollout("unsampled-session", "unsampled"))

	source := Source{
		Format:   "codex",
		Location: root,
		SourceID: "codex:" + root,
	}
	cursor, ok, err := sampledTempCanonicalScanCursor(ctx, source, []parity.Artifact{{
		SourceFile: sampledPath,
	}})
	if err != nil {
		t.Fatalf("sampled temp canonical scan cursor: %v", err)
	}
	if !ok {
		t.Fatal("sampled temp canonical scan cursor was not prepared")
	}
	codexCursor, ok := cursor.(codex.Cursor)
	if !ok {
		t.Fatalf("cursor type = %T, want codex.Cursor", cursor)
	}
	if _, sampledMarked := codexCursor.Files["2026/06/22/"+filepath.Base(sampledPath)]; sampledMarked {
		t.Fatalf("sampled rollout was marked consumed: %+v", codexCursor.Files)
	}
	unsampledRel := "2026/06/23/" + filepath.Base(unsampledPath)
	unsampledInfo := mustStatParityCheckFile(t, unsampledPath)
	unsampledCursor, ok := codexCursor.Files[unsampledRel]
	if !ok {
		t.Fatalf("unsampled rollout %q missing from cursor: %+v", unsampledRel, codexCursor.Files)
	}
	if unsampledCursor.Offset != unsampledInfo.Size() || unsampledCursor.EOFFinalizedSize != unsampledInfo.Size() {
		t.Fatalf("unsampled cursor = %+v, want offset/eof_finalized_size=%d", unsampledCursor, unsampledInfo.Size())
	}

	dbPath := filepath.Join(t.TempDir(), "index.db")
	writer, err := store.OpenWriter(ctx, dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if err := scanSourceIntoDBWithCursor(ctx, writer.DB(), checkLogger(nil), source, cursor); err != nil {
		t.Fatalf("scanSourceIntoDBWithCursor: %v", err)
	}

	nativeIDs := selectParityCheckSessionNativeIDs(t, writer.DB(), source.SourceID)
	if strings.Join(nativeIDs, ",") != "sampled-session" {
		t.Fatalf("ingested session native ids = %v, want only sampled-session", nativeIDs)
	}
}

func TestSampledAIAgentV2TempCanonicalCursorSkipsUnsampledSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	sampledPath := filepath.Join(root, "sampled-session.json.gz")
	unsampledPath := filepath.Join(root, "unsampled-session.json.gz")
	writeParityCheckAIAgentV2Snapshot(t, sampledPath, "sampled-session")
	writeParityCheckAIAgentV2Snapshot(t, unsampledPath, "unsampled-session")

	source := Source{
		Format:   "aiagent_v2",
		Location: root,
		SourceID: "aiagent_v2:" + root,
	}
	cursor, ok, err := sampledTempCanonicalScanCursor(ctx, source, []parity.Artifact{{
		SourceFile: sampledPath,
	}})
	if err != nil {
		t.Fatalf("sampled temp canonical scan cursor: %v", err)
	}
	if !ok {
		t.Fatal("sampled temp canonical scan cursor was not prepared")
	}
	v2Cursor, ok := cursor.(aiagent_v2.Cursor)
	if !ok {
		t.Fatalf("cursor type = %T, want aiagent_v2.Cursor", cursor)
	}
	if _, sampledMarked := v2Cursor.Files[filepath.Base(sampledPath)]; sampledMarked {
		t.Fatalf("sampled snapshot was marked consumed: %+v", v2Cursor.Files)
	}
	unsampledInfo := mustStatParityCheckFile(t, unsampledPath)
	unsampledCursor, ok := v2Cursor.Files[filepath.Base(unsampledPath)]
	if !ok {
		t.Fatalf("unsampled snapshot missing from cursor: %+v", v2Cursor.Files)
	}
	if unsampledCursor.LastSize != unsampledInfo.Size() || unsampledCursor.LastMtime != unsampledInfo.ModTime().UnixNano() || unsampledCursor.ContentHash == "" {
		t.Fatalf("unsampled cursor = %+v, want stable stat identity and non-empty content hash", unsampledCursor)
	}

	dbPath := filepath.Join(t.TempDir(), "index.db")
	writer, err := store.OpenWriter(ctx, dbPath, checkLogger(nil))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if err := scanSourceIntoDBWithCursor(ctx, writer.DB(), checkLogger(nil), source, cursor); err != nil {
		t.Fatalf("scanSourceIntoDBWithCursor: %v", err)
	}

	nativeIDs := selectParityCheckSessionNativeIDs(t, writer.DB(), source.SourceID)
	if strings.Join(nativeIDs, ",") != "sampled-session" {
		t.Fatalf("ingested session native ids = %v, want only sampled-session", nativeIDs)
	}
}

func parityCheckCodexRollout(sessionID string, text string) string {
	lines := []string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `","timestamp":"2026-06-22T00:00:00Z"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}}`,
	}
	return strings.Join(lines, "\n") + "\n"
}

func writeParityCheckAIAgentV2Snapshot(t *testing.T, path string, sessionID string) {
	t.Helper()

	writeParityCheckGzipJSON(t, path, map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        sessionID + "-node",
			"traceId":   sessionID,
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_001_000),
			"success":   true,
		},
	})
}

func mustStatParityCheckFile(t *testing.T, path string) os.FileInfo {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func selectParityCheckSessionNativeIDs(t *testing.T, db *sql.DB, sourceID string) []string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
SELECT native_id
FROM sessions
WHERE source_id = ?
ORDER BY native_id`, sourceID)
	if err != nil {
		t.Fatalf("select sessions: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var nativeID string
		if err := rows.Scan(&nativeID); err != nil {
			t.Fatalf("scan native id: %v", err)
		}
		out = append(out, nativeID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("session rows: %v", err)
	}
	return out
}
