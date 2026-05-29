package claude_code

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestAdapter_NameAndFormat(t *testing.T) {
	t.Parallel()
	a, err := New("/some/root", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Name() != "claude-code" {
		t.Errorf("Name() = %q, want claude-code", a.Name())
	}
	if a.Format() != "claude-code" {
		t.Errorf("Format() = %q, want claude-code", a.Format())
	}
}

func TestAdapter_NewRejectsEmptyRoot(t *testing.T) {
	t.Parallel()
	if _, err := New("", canonical.AdapterOptions{}); err == nil {
		t.Fatal("New(\"\") should error")
	}
}

func TestAdapter_FactoryConstructs(t *testing.T) {
	t.Parallel()
	a, err := Factory("/some/root", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	if a.Name() != "claude-code" {
		t.Fatalf("Factory adapter Name = %q", a.Name())
	}
	if _, err := Factory("", canonical.AdapterOptions{}); err == nil {
		t.Fatal("Factory(\"\") should error")
	}
}

func TestAdapter_ParseCursorErrorPropagates(t *testing.T) {
	t.Parallel()
	a, _ := New("/r", canonical.AdapterOptions{})
	if _, err := a.ParseCursor(`{"version":99}`); err == nil {
		t.Fatal("ParseCursor(bad version) should error")
	}
	c, err := a.ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor(empty): %v", err)
	}
	if c == nil {
		t.Fatal("ParseCursor(empty) returned nil cursor")
	}
}

func TestAdapter_CoerceCursorAlienTypeStartsFresh(t *testing.T) {
	t.Parallel()
	a, _ := New("/r", canonical.AdapterOptions{})
	type alien struct{ canonical.Cursor }
	got := a.coerceCursor(alien{})
	if len(got.Files) != 0 || got.Version != cursorVersion {
		t.Fatalf("coerceCursor(alien) should be fresh, got %+v", got)
	}
	// A real cursor missing maps/version is normalized.
	normalized := a.coerceCursor(Cursor{})
	if normalized.Files == nil || normalized.MetaSeen == nil || normalized.Version != cursorVersion {
		t.Fatalf("coerceCursor(zero) not normalized: %+v", normalized)
	}
}

// TestAdapter_TailSnapshotSkipsHistory verifies a cold Tail starts past the
// current file sizes (no historical re-emit): we feed a populated root and a
// short-lived Tail, then assert no session_started arrives within the window
// (because the snapshot cursor is already at EOF).
func TestAdapter_TailSnapshotSkipsHistory(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	writeFileBytes(t, filepath.Join(proj, "sess-1.jsonl"),
		[]byte(`{"type":"user","uuid":"u1","sessionId":"sess-1","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))

	a, _ := New(tmp, canonical.AdapterOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan canonical.Event, 64)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, out); close(done) }()

	// snapshotCursor is built synchronously inside Tail before the watch
	// loop; cancel quickly and confirm no historical session was emitted.
	cancel()
	<-done
	for _, ev := range drainBuffered(out) {
		if ev.EventKind() == canonical.EvSessionStarted {
			t.Fatal("cold Tail must not re-emit historical sessions")
		}
	}
}

// TestSnapshotCursor verifies the Tail snapshot records each transcript at
// its current size.
func TestSnapshotCursor(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	body := []byte(`{"type":"user","uuid":"u1","sessionId":"sess-1","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}` + "\n")
	writeFileBytes(t, filepath.Join(proj, "sess-1.jsonl"), body)

	a, _ := New(tmp, canonical.AdapterOptions{})
	cur, err := a.snapshotCursor()
	if err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}
	fc := cur.fileCursor("-home-user-x/sess-1.jsonl")
	if fc.Offset != int64(len(body)) || fc.Size != int64(len(body)) {
		t.Fatalf("snapshot offset/size = (%d,%d), want (%d,%d)", fc.Offset, fc.Size, len(body), len(body))
	}
}
