package aiagent_v3

import (
	"context"
	"errors"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// Compile-time conformance: the test file restates the assertion so any
// future drift in canonical.Adapter is caught even if someone removes
// the production-side compile-time check.
var _ canonical.Adapter = (*Adapter)(nil)

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	a, err := New("/tmp/aiagent_v3_test_root", canonical.AdapterOptions{})
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

func TestAdapter_ScanReturnsNotImplemented(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	out := make(chan canonical.Event, 1)
	err := a.Scan(context.Background(), nil, out)
	if err == nil {
		t.Fatalf("Scan: expected error, got nil")
	}
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("Scan: expected error wrapping errNotImplemented, got %v", err)
	}
}

func TestAdapter_TailReturnsNotImplemented(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	out := make(chan canonical.Event, 1)
	err := a.Tail(context.Background(), out)
	if err == nil {
		t.Fatalf("Tail: expected error, got nil")
	}
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("Tail: expected error wrapping errNotImplemented, got %v", err)
	}
}

func TestAdapter_ParseCursorReturnsNotImplemented(t *testing.T) {
	t.Parallel()

	a := newTestAdapter(t)
	cur, err := a.ParseCursor("")
	if err == nil {
		t.Fatalf("ParseCursor: expected error, got nil")
	}
	if cur != nil {
		t.Fatalf("ParseCursor: expected nil cursor, got %v", cur)
	}
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("ParseCursor: expected error wrapping errNotImplemented, got %v", err)
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

	a, err := Factory("/some/root", canonical.AdapterOptions{})
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
