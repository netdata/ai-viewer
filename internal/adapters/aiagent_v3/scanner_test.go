package aiagent_v3

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// drainBuffered is defined in scanner_helpers_test.go.

func TestScan_HappySingleTurn(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "testdata", "aiagent_v3", "happy_single_turn", "INPUT")
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 64)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	// Expected: SessionStarted + TurnStarted + TurnFinalized + OpStarted + OpFinalized + SessionFinalized + SessionUpdated + SourceProgress
	if len(events) < 7 {
		t.Fatalf("expected ≥ 7 events, got %d", len(events))
	}
	// Confirm we have one SessionStarted with the expected NativeID.
	var ss canonical.SessionStartedEvent
	for _, ev := range events {
		if v, ok := ev.(canonical.SessionStartedEvent); ok {
			ss = v
			break
		}
	}
	if ss.NativeID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("missing/expected SessionStarted: %+v", ss)
	}
	if ss.Kind != canonical.KindRoot {
		t.Fatalf("kind: %q", ss.Kind)
	}
}

func TestScan_AdvancesCursorAndIsIdempotent(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "testdata", "aiagent_v3", "happy_single_turn", "INPUT")
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// First scan from zero cursor.
	out := make(chan canonical.Event, 64)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	events := drainBuffered(out)
	// Find the final SourceProgress to extract the cursor.
	var lastProg canonical.SourceProgressEvent
	for _, ev := range events {
		if sp, ok := ev.(canonical.SourceProgressEvent); ok {
			lastProg = sp
		}
	}
	if lastProg.Cursor == "" {
		t.Fatalf("expected non-empty progress cursor")
	}
	cur, err := ParseCursor(lastProg.Cursor)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if len(cur.Files) == 0 {
		t.Fatalf("expected non-empty cursor.Files")
	}

	// Second scan from that cursor should yield zero new data events
	// (only the closing SourceProgress).
	out2 := make(chan canonical.Event, 16)
	if err := a.Scan(context.Background(), cur, out2); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	events2 := drainBuffered(out2)
	for _, ev := range events2 {
		if _, ok := ev.(canonical.SourceProgressEvent); !ok {
			t.Fatalf("re-scan emitted non-progress event: %T", ev)
		}
	}
}

func TestScan_MissingSessionDirEmitsOnlyProgress(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 4)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); !ok {
			t.Fatalf("unexpected: %T", ev)
		}
	}
	if len(events) == 0 {
		t.Fatalf("expected at least one SourceProgress")
	}
}

func TestScan_MalformedLineSurfacesError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// One bad line + one good line; both have a trailing newline.
	bad := []byte("{not json\n")
	good := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"x","sessionId":"x","capturePayloads":true}` + "\n")
	if err := writeFileBytes(filepath.Join(dir, "a.jsonl"), append(bad, good...)); err != nil {
		t.Fatalf("write: %v", err)
	}

	errs := make([]error, 0, 2)
	a, err := New(root, canonical.AdapterOptions{OnError: func(e error) { errs = append(errs, e) }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 8)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(errs) == 0 {
		t.Fatalf("expected at least one parse error surfaced via OnError")
	}
	// Ensure the good line still produced its SessionStarted event.
	events := drainBuffered(out)
	found := false
	for _, ev := range events {
		if _, ok := ev.(canonical.SessionStartedEvent); ok {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected SessionStarted from the valid line; got %d events", len(events))
	}
}

func TestScan_HoldsBackPartialTrailingLine(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	good := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"x","sessionId":"x","capturePayloads":true}` + "\n")
	partial := []byte(`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-05-26T10:00:01.000Z","originId":"x","sessionId":"x","turn":1`) // no newline
	if err := writeFileBytes(filepath.Join(dir, "a.jsonl"), append(good, partial...)); err != nil {
		t.Fatalf("write: %v", err)
	}

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 8)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	// Only SessionStarted (+ SourceProgress) — the partial turn_start
	// must not be emitted.
	for _, ev := range events {
		if _, ok := ev.(canonical.TurnStartedEvent); ok {
			t.Fatalf("partial turn_start should not have been emitted")
		}
	}
	// Now complete the line and re-scan from the previously emitted cursor.
	if err := appendFileBytes(filepath.Join(dir, "a.jsonl"), []byte("}\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	var lastProg canonical.SourceProgressEvent
	for _, ev := range events {
		if sp, ok := ev.(canonical.SourceProgressEvent); ok {
			lastProg = sp
		}
	}
	cur, err := ParseCursor(lastProg.Cursor)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	out2 := make(chan canonical.Event, 8)
	if err := a.Scan(context.Background(), cur, out2); err != nil {
		t.Fatalf("Scan2: %v", err)
	}
	events2 := drainBuffered(out2)
	found := false
	for _, ev := range events2 {
		if _, ok := ev.(canonical.TurnStartedEvent); ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected TurnStarted after completing the line, got %d events", len(events2))
	}
}

func TestScan_IgnoresNonLedgerFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeFileBytes(filepath.Join(dir, "notes.txt"), []byte("nothing here")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writeFileBytes(filepath.Join(dir, "a.jsonl.tmp-42-1"), []byte("partial")); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 4)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	// Only progress should be emitted; no data events.
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); !ok {
			t.Fatalf("unexpected: %T", ev)
		}
	}
}

func TestScan_DetectsTruncation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"x","sessionId":"x","capturePayloads":true}` + "\n")
	if err := writeFileBytes(filepath.Join(dir, "a.jsonl"), line); err != nil {
		t.Fatalf("write: %v", err)
	}
	errs := make([]error, 0, 2)
	a, _ := New(root, canonical.AdapterOptions{OnError: func(e error) { errs = append(errs, e) }})
	// Pre-populated cursor claiming a larger size — simulates truncation.
	cur := newCursor()
	cur.Files["a.jsonl"] = FileCursor{Offset: 100, Size: 999}
	out := make(chan canonical.Event, 8)
	if err := a.Scan(context.Background(), cur, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(errs) == 0 {
		t.Fatalf("expected truncation warning")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "shrank") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'shrank' error message; errs=%v", errs)
	}
}

// TestStreamLines_ContextCanceled exercises the ctx-check branch.
func TestStreamLines_ContextCanceled(t *testing.T) {
	t.Parallel()

	r := bytes.NewReader([]byte("anything\n"))
	cur := FileCursor{}
	out := make(chan canonical.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := streamLines(ctx, r, 0, int64(len("anything\n")), "x", "src", "/tmp", &cur, out, func(error) {})
	if err == nil {
		t.Fatalf("expected ctx err")
	}
}

// TestStreamLines_ContextCanceledDuringSend covers the send-blocked
// branch where the channel is full and ctx cancels.
func TestStreamLines_ContextCanceledDuringSend(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"x","sessionId":"x","capturePayloads":true}` + "\n")
	r := bytes.NewReader(line)
	cur := FileCursor{}
	out := make(chan canonical.Event) // unbuffered → first send blocks
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, _, err := streamLines(ctx, r, 0, int64(len(line)), "x", "src", "/tmp", &cur, out, func(error) {})
	if err == nil {
		t.Fatalf("expected ctx error from blocked send")
	}
}

// TestReadFile_FileMissing returns a wrapped error.
func TestReadFile_FileMissing(t *testing.T) {
	t.Parallel()

	_, _, err := readFile(context.Background(), "/no/such/file", "src", "/tmp", FileCursor{}, make(chan canonical.Event, 1), func(error) {})
	if err == nil {
		t.Fatalf("expected error")
	}
}

// TestReadFile_OffsetEqualsSizeIsNoop returns immediately without
// emitting events.
func TestReadFile_OffsetEqualsSizeIsNoop(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	if err := writeFileBytes(path, []byte("data\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := make(chan canonical.Event, 4)
	cur := FileCursor{Offset: 5, Size: 5}
	updated, n, err := readFile(context.Background(), path, "src", dir, cur, out, func(error) {})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 events, got %d", n)
	}
	if updated.Size != 5 {
		t.Fatalf("expected Size=5, got %d", updated.Size)
	}
}

// TestListLedgers_RootNotExistReturnsNilNoError mirrors spec §6.4
// (missing session/ dir is acceptable: no ledgers yet).
func TestListLedgers_RootNotExistReturnsNilNoError(t *testing.T) {
	t.Parallel()

	names, err := listLedgers(t.TempDir())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if names != nil {
		t.Fatalf("expected nil names for missing dir, got %v", names)
	}
}

// TestFileSize_Missing returns 0 with no error.
func TestFileSize_Missing(t *testing.T) {
	t.Parallel()

	n, err := fileSize(t.TempDir(), "absent.jsonl")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

// TestReadFile_LineTooLongAdvancesToEOF verifies that when a single
// ledger line exceeds scanBufferMax, the cursor advances past it
// (specifically to the file size) so the next Tail pass does not
// re-scan the same oversized line and re-emit the same warning.
func TestReadFile_LineTooLongAdvancesToEOF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root := dir
	sessionPath := filepath.Join(root, sessionDir)
	if err := mkdirAll(sessionPath); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// One oversized line followed by EOF (no good line behind it; the
	// behavior under test is "skip the offending line entirely").
	huge := bytes.Repeat([]byte("a"), scanBufferMax+1024)
	huge = append(huge, '\n')
	path := filepath.Join(sessionPath, "a.jsonl")
	if err := writeFileBytes(path, huge); err != nil {
		t.Fatalf("write: %v", err)
	}

	errs := make([]error, 0, 2)
	out := make(chan canonical.Event, 8)
	updated, _, err := readFile(context.Background(), path, "src", root, FileCursor{}, out, func(e error) { errs = append(errs, e) })
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if len(errs) == 0 {
		t.Fatalf("expected line-too-long warning")
	}
	wantSize := int64(len(huge))
	if updated.Offset != wantSize {
		t.Fatalf("expected Offset advanced to file size %d after oversized line, got %d", wantSize, updated.Offset)
	}
	if updated.Size != wantSize {
		t.Fatalf("expected Size=%d, got %d", wantSize, updated.Size)
	}

	// Re-scan with the advanced cursor: no new events, no new errors.
	errs2 := make([]error, 0, 2)
	out2 := make(chan canonical.Event, 8)
	again, n, err := readFile(context.Background(), path, "src", root, updated, out2, func(e error) { errs2 = append(errs2, e) })
	if err != nil {
		t.Fatalf("readFile (rescan): %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 events on rescan, got %d", n)
	}
	if len(errs2) != 0 {
		t.Fatalf("expected no errors on rescan, got %v", errs2)
	}
	if again.Offset != wantSize {
		t.Fatalf("rescan changed Offset: %d", again.Offset)
	}
	// Drain the events channel for completeness.
	close(out)
	close(out2)
	for range out {
	}
	for range out2 {
	}
}

func TestEmitProgress_RespectsCanceledCtx(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 1)
	if err := emitProgress(ctx, "src", newCursor(), out); err == nil {
		t.Fatalf("expected ctx.Err()")
	}
}
