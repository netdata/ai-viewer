package aiagent_v3

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestDrainToNewline_HappyPath: a buffer-overflowing line followed by
// '\n' then another line. drainToNewline (called via readOneLine on the
// overflow branch) consumes the rest of the bad line so the next
// readOneLine starts at the second line.
func TestDrainToNewline_HappyPath(t *testing.T) {
	t.Parallel()

	huge := bytes.Repeat([]byte("x"), scanBufferMax+200)
	huge = append(huge, '\n')
	follow := []byte("good\n")
	br := bufio.NewReaderSize(bytes.NewReader(append(huge, follow...)), 64*1024)
	_, err := readOneLine(br)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("first read should overflow, got %v", err)
	}
	// Even though the overflow path returned errLineTooLong without
	// draining (since the overflow was detected on the successful read
	// before draining had to engage), the readOneLine path that goes
	// through ErrBufferFull DOES call drainToNewline. To explicitly
	// exercise drainToNewline we craft a line whose buffer-full path is
	// hit before its '\n' arrives.
	got, err := readOneLine(br)
	if err != nil {
		t.Fatalf("follow read err: %v", err)
	}
	if string(got) != "good\n" {
		t.Fatalf("unexpected follow line %q", got)
	}
}

// TestDrainToNewline_ViaBufferFullPath constructs a slow reader so
// ReadSlice hits ErrBufferFull. Each chunk is less than the bufio
// buffer (1 KB) and the line is large enough to overflow the size cap.
func TestDrainToNewline_ViaBufferFullPath(t *testing.T) {
	t.Parallel()

	// Use a tiny bufio buffer (1 KB) so ErrBufferFull triggers quickly,
	// but our line is just over the scanBufferMax → overflow on the
	// ErrBufferFull path, then drainToNewline takes over until '\n'.
	huge := bytes.Repeat([]byte("x"), scanBufferMax+200)
	huge = append(huge, '\n')
	follow := []byte("good\n")
	br := bufio.NewReaderSize(bytes.NewReader(append(huge, follow...)), 1024)
	_, err := readOneLine(br)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("first read should overflow via ErrBufferFull, got %v", err)
	}
	got, err := readOneLine(br)
	if err != nil {
		t.Fatalf("follow read err: %v", err)
	}
	if string(got) != "good\n" {
		t.Fatalf("unexpected follow line %q", got)
	}
}

// TestDrainToNewline_NoNewlineEOFReturnsErr: drainToNewline that runs
// off the end of input returns EOF (which the caller would surface).
func TestDrainToNewline_NoNewlineEOFReturnsErr(t *testing.T) {
	t.Parallel()

	// No newline ever; drainToNewline returns io.EOF.
	br := bufio.NewReaderSize(bytes.NewReader(bytes.Repeat([]byte("z"), 256)), 64)
	err := drainToNewline(br)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

// TestCursor_StringMarshalSuccess covers the happy path; the marshal
// error path is impossible to trigger with our struct types so we don't
// assert on it.
func TestCursor_StringMarshalSuccess(t *testing.T) {
	t.Parallel()

	c := newCursor()
	c.Files["x.jsonl"] = FileCursor{Offset: 1, LastTsUs: 12345}
	s := c.String()
	if s == "" || s[0] != '{' {
		t.Fatalf("expected JSON object: %q", s)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal([]byte(s), &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// TestSnapshotCursor_ReportsErrorViaOnError covers the stat-failure
// branch in snapshotCursor. We can't easily simulate a stat error on a
// file we just listed, but we can verify the happy path with multiple
// files of varying sizes.
func TestSnapshotCursor_MultipleFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	_ = mkdirAll(dir)
	_ = writeFileBytes(filepath.Join(dir, "a.jsonl"), []byte("line\n"))
	_ = writeFileBytes(filepath.Join(dir, "b.jsonl"), []byte("longer line here\n"))

	a, _ := New(root, canonical.AdapterOptions{})
	cur, err := a.snapshotCursor()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cur.Files["a.jsonl"].Offset != 5 {
		t.Fatalf("a offset: %d", cur.Files["a.jsonl"].Offset)
	}
	if cur.Files["b.jsonl"].Offset != 17 {
		t.Fatalf("b offset: %d", cur.Files["b.jsonl"].Offset)
	}
}

// TestSnapshotCursor_ListLedgersFailsBubbles exercises the propagation
// of listLedgers errors. We make the session/ path a regular file so
// ReadDir fails with ENOTDIR.
func TestSnapshotCursor_ListLedgersFailsBubbles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Make session a regular file instead of a directory.
	if err := os.WriteFile(filepath.Join(root, sessionDir), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, _ := New(root, canonical.AdapterOptions{})
	_, err := a.snapshotCursor()
	if err == nil {
		t.Fatalf("expected error")
	}
}

// TestListLedgers_TmpFilesIgnored uses real directory entries to cover
// the .tmp- skip branch.
func TestListLedgers_TmpFilesIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	_ = mkdirAll(dir)
	_ = writeFileBytes(filepath.Join(dir, "good.jsonl"), []byte("x"))
	_ = writeFileBytes(filepath.Join(dir, "skip.jsonl.tmp-1-2"), []byte("x"))
	_ = writeFileBytes(filepath.Join(dir, "notes.txt"), []byte("x"))
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)

	names, err := listLedgers(root)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(names) != 1 || names[0] != "good.jsonl" {
		t.Fatalf("unexpected names: %v", names)
	}
}

// TestListLedgers_ErrorPropagates: session is a file → ReadDir returns
// ENOTDIR.
func TestListLedgers_ErrorPropagates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, sessionDir), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := listLedgers(root); err == nil {
		t.Fatalf("expected error")
	}
}

// TestFileSize_HappyAndReturnSize confirms the non-zero path.
func TestFileSize_HappyAndReturnSize(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	_ = mkdirAll(dir)
	_ = writeFileBytes(filepath.Join(dir, "x.jsonl"), []byte("hello"))
	n, err := fileSize(root, "x.jsonl")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5, got %d", n)
	}
}

// TestTailableName_RealEvents drives the actual tailableName function
// (not the test helper). We can construct fsnotify.Event values directly.
func TestTailableName_RealEvents(t *testing.T) {
	t.Parallel()

	watchDir := "/tmp/root/session"
	cases := []struct {
		name string
		want string
	}{
		{watchDir + "/a.jsonl", "a.jsonl"},
		{watchDir + "/notes.txt", ""},
		{watchDir + "/x.jsonl.tmp-1-2", ""},
		{"/other/path/a.jsonl", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := tailableName(watchDir, fsnotifyEventForTest(c.name))
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// TestEmitProgress_OutBlockedAndCtxCancels exercises the select's
// ctx.Done path when the channel is full.
func TestEmitProgress_OutBlockedAndCtxCancels(t *testing.T) {
	t.Parallel()

	out := make(chan canonical.Event) // unbuffered, never received
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Wait briefly, then cancel.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := emitProgress(ctx, "src", newCursor(), out)
	if err == nil {
		t.Fatalf("expected ctx error")
	}
	wg.Wait()
}

// TestScanAll_ReadFileErrorBubblesAsOnError: a ledger directory that
// contains an unreadable file (e.g. a directory with the .jsonl
// suffix) surfaces via OnError but the scan completes.
func TestScanAll_BadFileSurfacedViaOnError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	_ = mkdirAll(dir)
	// Create a subdirectory with .jsonl suffix; listLedgers filters
	// directories out, but if the entry sneaks past the filter we'd
	// hit an error on readFile. Instead, we use a file we then chmod
	// to 000 so reading fails.
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(bad, 0o644) }()

	errs := []error{}
	a, _ := New(root, canonical.AdapterOptions{OnError: func(e error) { errs = append(errs, e) }})
	out := make(chan canonical.Event, 8)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan should not fail-out: %v", err)
	}
	// Running as root would see no permission error; in that case skip.
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 000 does not block reads")
	}
	if len(errs) == 0 {
		t.Fatalf("expected an OnError call from the unreadable file")
	}
}

// TestTailLoop_FsnotifyRemoveSurfacesError: simulate a remove via real
// fsnotify by creating a ledger then deleting it after Tail starts.
func TestTailLoop_FsnotifyRemoveSurfacesError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "doomed.jsonl")
	if err := writeFileBytes(path, []byte("seed\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	var mu sync.Mutex
	errs := []string{}
	a, _ := New(root, canonical.AdapterOptions{OnError: func(e error) {
		mu.Lock()
		errs = append(errs, e.Error())
		mu.Unlock()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan canonical.Event, 8)
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(50 * time.Millisecond)

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Wait for the error to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(errs)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) == 0 {
		t.Fatalf("expected a removed/renamed error from the watcher")
	}
	if !strings.Contains(errs[0], "removed") {
		t.Fatalf("unexpected error text: %v", errs)
	}
}

// fsnotifyEventForTest creates a real fsnotify.Event so we can drive
// tailableName directly without spinning up a watcher.
func fsnotifyEventForTest(name string) fsnotify.Event {
	return fsnotify.Event{Name: name, Op: fsnotify.Create}
}
