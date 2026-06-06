package aiagent_v3

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// Compile-time conformance: the test file restates the assertion so any
// future drift in canonical.Adapter is caught even if someone removes
// the production-side compile-time check.
var _ canonical.Adapter = (*Adapter)(nil)

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	a, err := New(t.TempDir(), canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestNew_RejectsEmptyRoot(t *testing.T) {
	t.Parallel()

	if _, err := New("", canonical.AdapterOptions{}); err == nil {
		t.Fatalf("New(\"\"): expected error, got nil")
	}
}

func TestNew_SubstitutesNilOnError(t *testing.T) {
	t.Parallel()

	a, err := New("/some/root", canonical.AdapterOptions{OnError: nil})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.onError == nil {
		t.Fatalf("expected onError substitute, got nil")
	}
	// Should be safe to call.
	a.onError(errors.New("ignored"))
}

func TestNew_SourceIDOptionWins(t *testing.T) {
	t.Parallel()

	a, err := New("/some/root", canonical.AdapterOptions{SourceID: "source/custom"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := a.sourceID; got != "source/custom" {
		t.Fatalf("sourceID = %q, want %q", got, "source/custom")
	}
}

func TestNew_SourceIDDefaultsToHistoricalFallback(t *testing.T) {
	t.Parallel()

	root := "/some/root"
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := sourceIDPrefix + root
	if got := a.sourceID; got != want {
		t.Fatalf("sourceID = %q, want %q", got, want)
	}
}

func TestAdapter_NameAndFormat(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	if got := a.Name(); got != Format {
		t.Fatalf("Name() = %q, want %q", got, Format)
	}
	if got := a.Format(); got != Format {
		t.Fatalf("Format() = %q, want %q", got, Format)
	}
	if Format != "aiagent_v3" {
		t.Fatalf("Format constant drifted: got %q want %q", Format, "aiagent_v3")
	}
}

func TestAdapter_ScanEmptyRootIsNoop(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	out := make(chan canonical.Event, 8)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// One SourceProgress (final flush) is expected.
	close(out)
	count := 0
	for ev := range out {
		count++
		if _, ok := ev.(canonical.SourceProgressEvent); !ok {
			t.Fatalf("unexpected event on empty Scan: %T", ev)
		}
	}
	if count == 0 {
		t.Fatalf("Scan: expected at least one SourceProgress event")
	}
}

func TestAdapter_ScanRespectsCanceledCtx(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 8)
	if err := a.Scan(ctx, nil, out); err != nil {
		t.Fatalf("Scan: expected nil error on cancelled ctx, got %v", err)
	}
}

func TestAdapter_ParseCursorEmptyYieldsEmpty(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	cur, err := a.ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if cur == nil {
		t.Fatalf("ParseCursor: expected non-nil cursor")
	}
	c, ok := cur.(Cursor)
	if !ok {
		t.Fatalf("ParseCursor: wrong concrete type %T", cur)
	}
	if len(c.Files) != 0 {
		t.Fatalf("ParseCursor empty: expected zero files, got %d", len(c.Files))
	}
}

func TestAdapter_ParseCursorRoundTrip(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	original := newCursor()
	original.Files["one.jsonl"] = FileCursor{Offset: 42, Size: 100, LastSeq: 7}
	encoded := original.String()

	got, err := a.ParseCursor(encoded)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	c, ok := got.(Cursor)
	if !ok {
		t.Fatalf("ParseCursor: wrong concrete type %T", got)
	}
	if c.Files["one.jsonl"].Offset != 42 {
		t.Fatalf("round-trip lost offset: %+v", c.Files["one.jsonl"])
	}
	if c.Version != cursorVersion {
		t.Fatalf("unexpected version %d", c.Version)
	}
}

func TestAdapter_ParseCursorRejectsBadJSON(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	if _, err := a.ParseCursor("{not json"); err == nil {
		t.Fatalf("ParseCursor: expected error on malformed JSON")
	}
}

func TestAdapter_ParseCursorRejectsUnknownVersion(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	if _, err := a.ParseCursor(`{"version":99,"files":{}}`); err == nil {
		t.Fatalf("ParseCursor: expected error on unknown version")
	}
}

func TestAdapter_SnapshotCursorMatchesFileSizes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "x.jsonl")
	if err := writeFileBytes(path, []byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cur, err := a.snapshotCursor()
	if err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}
	got := cur.Files["x.jsonl"]
	if got.Offset != int64(len("hello\n")) || got.Size != int64(len("hello\n")) {
		t.Fatalf("unexpected snapshot cursor: %+v", got)
	}
}

func TestFactory_RejectsEmptyLocation(t *testing.T) {
	t.Parallel()

	if _, err := Factory("", canonical.AdapterOptions{}); err == nil {
		t.Fatalf("Factory(\"\"): expected error")
	}
}

func TestFactory_BuildsAdapter(t *testing.T) {
	t.Parallel()

	a, err := Factory(t.TempDir(), canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	if a == nil {
		t.Fatalf("Factory: nil adapter, nil error")
	}
	if a.Name() != Format {
		t.Fatalf("Name() = %q, want %q", a.Name(), Format)
	}
	if a.Format() != Format {
		t.Fatalf("Format() = %q, want %q", a.Format(), Format)
	}
}
