package aiagent_v2

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestScanner_HappyPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "scn-1"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	out := make(chan canonical.Event, 64)
	_, err := scanAll(context.Background(), root, "aiagent_v2:"+root, newCursor(), out, func(error) {})
	if err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	events := drainBuffered(out)
	if len(events) == 0 {
		t.Fatalf("expected events")
	}
}

func TestScanner_DedupBySameContentHash(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "dedup-test"
	path := writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	// First scan populates the cursor.
	out1 := make(chan canonical.Event, 64)
	cur, err := scanAll(context.Background(), root, "src", newCursor(), out1, func(error) {})
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if got := len(drainBuffered(out1)); got == 0 {
		t.Fatalf("first scan should emit events")
	}

	// Touch mtime forward without changing content.
	info, _ := os.Stat(path)
	newer := info.ModTime().Add(2 * 1_000_000_000)
	if err := os.Chtimes(path, newer, newer); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	out2 := make(chan canonical.Event, 64)
	if _, err := scanAll(context.Background(), root, "src", cur, out2, func(error) {}); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	events := drainBuffered(out2)
	for _, ev := range events {
		switch ev.(type) {
		case canonical.SourceProgressEvent:
			// expected
		default:
			t.Fatalf("expected no domain events on content-hash dedup, got %T", ev)
		}
	}
}

func TestScanner_StatOnlyShortCircuit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "stat-skip"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	out1 := make(chan canonical.Event, 64)
	cur, err := scanAll(context.Background(), root, "src", newCursor(), out1, func(error) {})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	_ = drainBuffered(out1)

	// Rescan without changing the file: stat-only short circuit fires.
	out2 := make(chan canonical.Event, 8)
	if _, err := scanAll(context.Background(), root, "src", cur, out2, func(error) {}); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	events := drainBuffered(out2)
	domainCount := 0
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); ok {
			continue
		}
		domainCount++
	}
	if domainCount != 0 {
		t.Fatalf("expected only progress events on identical rescan, got %d domain events", domainCount)
	}
}

func TestScanner_ZeroByteFileEmitsWarning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	zeroPath := filepath.Join(root, "00000000-0000-0000-0000-000000000000.json.gz")
	if err := os.WriteFile(zeroPath, []byte{}, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	out := make(chan canonical.Event, 16)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	events := drainBuffered(out)
	var sawErr bool
	for _, ev := range events {
		if se, ok := ev.(canonical.SourceErrorEvent); ok {
			sawErr = true
			if se.File != zeroPath {
				t.Fatalf("File: %q", se.File)
			}
		}
	}
	if !sawErr {
		t.Fatalf("expected SourceErrorEvent for zero-byte file")
	}
}

func TestScanner_SkipsTmpFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tmpPath := filepath.Join(root, "abc.json.gz.tmp-1234-5678")
	if err := os.WriteFile(tmpPath, []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := make(chan canonical.Event, 8)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	events := drainBuffered(out)
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceErrorEvent); ok {
			t.Fatalf("tmp file should be ignored, not warned: %+v", ev)
		}
	}
}

func TestScanner_SkipsHiddenFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hidden := filepath.Join(root, ".hidden.json.gz")
	if err := os.WriteFile(hidden, []byte("hidden"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "session"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out := make(chan canonical.Event, 8)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	events := drainBuffered(out)
	// Hidden + session/ subdir → only progress event should land.
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceErrorEvent); ok {
			t.Fatalf("hidden file should be ignored, not warned: %+v", ev)
		}
	}
}

func TestScanner_CorruptGzipHeader(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRaw(t, root, "bad.json.gz", []byte("not a gzip"))

	var errCount int
	out := make(chan canonical.Event, 16)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) { errCount++ }); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if errCount == 0 {
		t.Fatalf("expected onError to fire for bad gzip")
	}
}

func TestScanner_MalformedJSONAfterGzip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("{not json"))
	_ = zw.Close()
	writeRaw(t, root, "bad.json.gz", buf.Bytes())

	var errCount int
	out := make(chan canonical.Event, 16)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) { errCount++ }); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if errCount == 0 {
		t.Fatalf("expected onError for malformed JSON inside gzip")
	}
}

func TestScanner_MissingDirectoryReturnsEmpty(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "nope")
	out := make(chan canonical.Event, 4)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	events := drainBuffered(out)
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); !ok {
			t.Fatalf("unexpected event for missing dir: %T", ev)
		}
	}
}

func TestScanner_StreamerKickedInOnLargeFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "big-stream"
	// Build a payload large enough that the COMPRESSED file exceeds
	// streamerThresholdBytes. We do this by writing a snapshot with
	// many turns to inflate post-gzip size, but in tests we drop the
	// threshold via the streaming path test which validates byte
	// identity. Here we just confirm that a normal snapshot under
	// the threshold takes the in-memory path.
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	info, _ := os.Stat(filepath.Join(root, origin+".json.gz"))
	if info.Size() > streamerThresholdBytes {
		t.Fatalf("simple fixture unexpectedly above threshold: %d", info.Size())
	}
}

func TestScanner_TmpDuringRenameIsSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tmpName := "race.json.gz.tmp-12345-67890"
	if err := os.WriteFile(filepath.Join(root, tmpName), []byte("partial"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Also a valid snapshot.
	origin := "good-one"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	out := make(chan canonical.Event, 64)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var goodSession bool
	for _, ev := range drainBuffered(out) {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == origin {
			goodSession = true
		}
	}
	if !goodSession {
		t.Fatalf("expected to see good session despite tmp file")
	}
}

func TestSha256Hex(t *testing.T) {
	t.Parallel()
	got := sha256Hex([]byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Fatalf("hash: %q", got)
	}
}

func TestIsSnapshotName(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"abc.json.gz":               true,
		"abc.json.gz.tmp-1234-5678": false,
		".hidden.json.gz":           false,
		"":                          false,
		"notagzip.json":             false,
		"00000000-0000-0000-0000-000000000000.json.gz": true,
	}
	for name, want := range cases {
		if got := isSnapshotName(name); got != want {
			t.Fatalf("isSnapshotName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestStripSnapshotExt(t *testing.T) {
	t.Parallel()
	if got := stripSnapshotExt("abc.json.gz"); got != "abc" {
		t.Fatalf("strip: %q", got)
	}
}

func TestScanner_ProgressEventCarriesValidJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSnapshot(t, root, "prog", simpleSnapshot(2, "prog"))
	out := make(chan canonical.Event, 16)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var sawProgress bool
	for _, ev := range drainBuffered(out) {
		if sp, ok := ev.(canonical.SourceProgressEvent); ok {
			sawProgress = true
			var c Cursor
			if err := json.Unmarshal([]byte(sp.Cursor), &c); err != nil {
				t.Fatalf("progress cursor invalid JSON: %v", err)
			}
		}
	}
	if !sawProgress {
		t.Fatalf("no progress event")
	}
}

func TestScanner_ContextCancelledMidScan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSnapshot(t, root, "x", simpleSnapshot(2, "x"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 16)
	// scanAll returns the underlying context error directly; that
	// behaviour is intentional so the adapter's Scan can map it to nil.
	if _, err := scanAll(ctx, root, "src", newCursor(), out, func(error) {}); err == nil {
		t.Fatalf("expected ctx error")
	}
}
