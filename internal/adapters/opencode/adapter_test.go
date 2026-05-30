package opencode

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the opencode Adapter's CONSTRUCTION + cursor tests
// (New/Name/Format/Factory/ParseCursor/coerceCursor) plus the shared test
// helpers. The Scan/Tail/snapshot lifecycle tests live in
// adapter_lifecycle_test.go (split for the 400-line budget).

// discardOpts returns AdapterOptions with a discard logger and a recording
// onError, plus the slice the errors land in (guarded by mu for the Tail
// goroutine). Mirrors codex's silentOpts.
func discardOpts() (canonical.AdapterOptions, *[]string, *sync.Mutex) {
	var mu sync.Mutex
	errs := &[]string{}
	opts := canonical.AdapterOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnError: func(e error) {
			mu.Lock()
			*errs = append(*errs, e.Error())
			mu.Unlock()
		},
	}
	return opts, errs, &mu
}

// alienCursor is a foreign canonical.Cursor used to drive coerceCursor's
// type-assertion-miss branch.
type alienCursor struct{}

func (alienCursor) String() string              { return "{}" }
func (alienCursor) After(canonical.Cursor) bool { return false }

// TestAdapter_NewRejectsEmptyLocation pins the fail-fast guard on an empty DB path.
func TestAdapter_NewRejectsEmptyLocation(t *testing.T) {
	t.Parallel()
	if _, err := New("", canonical.AdapterOptions{}); err == nil {
		t.Fatal("New(\"\") = nil error, want non-nil")
	}
}

// TestAdapter_NewDefaultsNilDeps verifies New tolerates a nil Logger and nil
// OnError (substituting defaults) so adapter code can call them unconditionally.
func TestAdapter_NewDefaultsNilDeps(t *testing.T) {
	t.Parallel()
	a, err := New("/some/opencode.db", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.logger == nil {
		t.Error("logger is nil; want default")
	}
	if a.onError == nil {
		t.Error("onError is nil; want no-op default")
	}
	a.onError(errors.New("x")) // the no-op must be callable
	if a.sourceID != "opencode:/some/opencode.db" {
		t.Errorf("sourceID = %q, want opencode:/some/opencode.db", a.sourceID)
	}
}

// TestAdapter_NameAndFormat pins the registry identifiers.
func TestAdapter_NameAndFormat(t *testing.T) {
	t.Parallel()
	a, err := New("/some/opencode.db", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Name() != "opencode" || a.Format() != "opencode" {
		t.Errorf("Name()/Format() = %q/%q, want opencode/opencode", a.Name(), a.Format())
	}
	if a.Name() != Format || a.Format() != Format {
		t.Errorf("Name/Format must equal Format const %q", Format)
	}
}

// TestAdapter_FactoryAndRegistry builds an Adapter through the registry factory,
// proving init() ran (acceptance #1), and rejects the empty location.
func TestAdapter_FactoryAndRegistry(t *testing.T) {
	t.Parallel()
	factory, ok := adapters.Get("opencode")
	if !ok {
		t.Fatal("opencode factory not registered (init did not run)")
	}
	a, err := factory(t.TempDir()+"/opencode.db", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("registry factory: %v", err)
	}
	if a == nil {
		t.Fatal("registry factory returned nil adapter")
	}
	if a.Name() != "opencode" || a.Format() != "opencode" {
		t.Errorf("factory adapter Name()/Format() = %q/%q, want opencode", a.Name(), a.Format())
	}
	// The package-level Factory rejects an empty location.
	if _, err := Factory("", canonical.AdapterOptions{}); err == nil {
		t.Fatal("Factory(\"\") = nil error, want non-nil")
	}
}

// TestAdapter_ParseCursor round-trips a cursor and rejects a bad version.
func TestAdapter_ParseCursor(t *testing.T) {
	t.Parallel()
	a, err := New("/some/opencode.db", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Empty → zero cursor (not nil).
	c, err := a.ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor(\"\"): %v", err)
	}
	if c == nil {
		t.Fatal("ParseCursor(\"\") = nil cursor")
	}
	// Round-trip a non-empty cursor (current v2 split-watermark shape).
	seed := newCursor().withTable("session", TableWatermark{MaxIDSeen: "ses_9", MaxTimeUpdatedMs: 42, MaxTimeUpdatedID: "ses_9"})
	got, err := a.ParseCursor(seed.String())
	if err != nil {
		t.Fatalf("ParseCursor(round-trip): %v", err)
	}
	if !got.After(newCursor()) {
		t.Error("round-tripped cursor should be After the empty cursor")
	}
	// A future/unknown version is NOT an error — it re-scans from zero (our own
	// cursor shape drifting is recoverable by an idempotent backfill; SOW-0005 P1-A).
	reScan, err := a.ParseCursor(`{"version":999}`)
	if err != nil {
		t.Errorf("ParseCursor(unknown version) = %v, want nil (re-scan from zero)", err)
	}
	if tc, ok := reScan.(Cursor); !ok || tc.hasProgress() {
		t.Errorf("ParseCursor(unknown version) = %+v, want a fresh zero cursor", reScan)
	}
}

// TestAdapter_CoerceCursor covers the nil, typed-zero, and alien-type branches.
func TestAdapter_CoerceCursor(t *testing.T) {
	t.Parallel()
	a, err := New("/some/opencode.db", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// nil → fresh cursor with non-nil map + version.
	if c := a.coerceCursor(nil); c.Tables == nil || c.Version != cursorVersion {
		t.Errorf("coerceCursor(nil) = %+v, want initialized map + version", c)
	}
	// Typed cursor with nil map + zero version is normalized in place.
	if c := a.coerceCursor(Cursor{}); c.Tables == nil || c.Version != cursorVersion {
		t.Errorf("coerceCursor(typed-zero) = %+v, want normalized", c)
	}
	// Alien cursor type → fresh cursor (full re-scan; never skips data).
	c := a.coerceCursor(alienCursor{})
	if c.Version != cursorVersion || c.hasProgress() {
		t.Errorf("coerceCursor(alien) = %+v, want fresh zero-watermark cursor", c)
	}
}

// hasSession reports whether evs contains a SessionStarted for nativeID. Shared
// with the lifecycle tests in adapter_lifecycle_test.go.
func hasSession(evs []canonical.Event, nativeID string) bool {
	for _, ev := range evs {
		if s, ok := ev.(canonical.SessionStartedEvent); ok && s.NativeID == nativeID {
			return true
		}
	}
	return false
}
