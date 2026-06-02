package aiagent_v2

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

var _ canonical.Adapter = (*Adapter)(nil)

func TestNew_RejectsEmptyRoot(t *testing.T) {
	t.Parallel()
	if _, err := New("", canonical.AdapterOptions{}); err == nil {
		t.Fatalf("expected error on empty root")
	}
}

func TestNew_SubstitutesNilOnError(t *testing.T) {
	t.Parallel()
	a, err := New("/some/root", canonical.AdapterOptions{OnError: nil})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.onError == nil {
		t.Fatalf("expected non-nil onError")
	}
	a.onError(errors.New("noop"))
}

func TestAdapter_NameAndFormat(t *testing.T) {
	t.Parallel()
	a, err := New("/tmp/x", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Name() != Format {
		t.Fatalf("Name: %q", a.Name())
	}
	if a.Format() != Format {
		t.Fatalf("Format: %q", a.Format())
	}
	if Format != "aiagent_v2" {
		t.Fatalf("Format constant drifted: %q", Format)
	}
}

func TestFactory_BuildsAdapter(t *testing.T) {
	t.Parallel()
	a, err := Factory("/some/root", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	if a.Name() != Format {
		t.Fatalf("Name: %q", a.Name())
	}
}

func TestFactory_RejectsEmptyLocation(t *testing.T) {
	t.Parallel()
	if _, err := Factory("", canonical.AdapterOptions{}); err == nil {
		t.Fatalf("expected error")
	}
}

func TestParseCursor_EmptyYieldsZero(t *testing.T) {
	t.Parallel()
	a, _ := New("/x", canonical.AdapterOptions{})
	cur, err := a.ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if cur == nil {
		t.Fatalf("expected non-nil cursor")
	}
	if cur.String() == "" {
		t.Fatalf("zero cursor should serialise non-empty JSON")
	}
}

func TestParseCursor_RejectsBadJSON(t *testing.T) {
	t.Parallel()
	a, _ := New("/x", canonical.AdapterOptions{})
	if _, err := a.ParseCursor("not json"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestScan_EmptyDirectoryEmitsProgressOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 16)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	if len(events) != 1 {
		t.Fatalf("want 1 progress event, got %d (%T...)", len(events), events)
	}
	if _, ok := events[0].(canonical.SourceProgressEvent); !ok {
		t.Fatalf("want SourceProgressEvent, got %T", events[0])
	}
}

func TestScan_ProducesSessionStartedForFixture(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "11111111-1111-1111-1111-111111111111"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 64)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)

	var sawSession bool
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok {
			sawSession = true
			if ss.NativeID != origin {
				t.Fatalf("NativeID: got %q want %q", ss.NativeID, origin)
			}
			if ss.Kind != canonical.KindRoot {
				t.Fatalf("Kind: got %q want %q", ss.Kind, canonical.KindRoot)
			}
		}
	}
	if !sawSession {
		t.Fatalf("no SessionStartedEvent in %d events", len(events))
	}
}

func TestScan_CancellableContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "22222222-2222-2222-2222-222222222222"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event) // unbuffered so emission blocks
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Cancelled before Scan starts → Scan returns nil per our wrapping
	// (context.Canceled mapped to "caught up" since the contract says
	// Scan returns when caught up *or* cancelled).
	err := a.Scan(ctx, nil, out)
	if err != nil {
		t.Fatalf("Scan on cancelled ctx: %v", err)
	}
}

func TestCoerceCursor_AlienTypeYieldsEmpty(t *testing.T) {
	t.Parallel()
	a, _ := New("/x", canonical.AdapterOptions{})
	cur := a.coerceCursor(alienCursor{})
	if len(cur.Files) != 0 {
		t.Fatalf("expected empty Files, got %d", len(cur.Files))
	}
}

func TestCoerceCursor_NormalisesEmptyVersion(t *testing.T) {
	t.Parallel()
	a, _ := New("/x", canonical.AdapterOptions{})
	cur := a.coerceCursor(Cursor{})
	if cur.Version != cursorVersion {
		t.Fatalf("Version: got %d want %d", cur.Version, cursorVersion)
	}
}

type alienCursor struct{}

func (alienCursor) String() string                { return "{}" }
func (alienCursor) After(_ canonical.Cursor) bool { return false }

func TestTail_SnapshotCursorIgnoresMissingDir(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "missing")
	a, _ := New(root, canonical.AdapterOptions{})
	cur, err := a.snapshotCursor()
	if err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}
	if len(cur.Files) != 0 {
		t.Fatalf("expected zero entries, got %d", len(cur.Files))
	}
}
