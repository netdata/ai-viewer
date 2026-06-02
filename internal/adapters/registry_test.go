package adapters

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// fakeFactory returns a canonical.AdapterFactory that yields a sentinel
// adapter so tests can compare factories by the adapter they produce.
// Two factories built with distinct sentinel strings are distinguishable
// without depending on Go function-pointer equality (which is unspecified
// for closures).
func fakeFactory(sentinel string) canonical.AdapterFactory {
	return func(_ string, _ canonical.AdapterOptions) (canonical.Adapter, error) {
		return &fakeAdapter{name: sentinel}, nil
	}
}

type fakeAdapter struct{ name string }

func (f *fakeAdapter) Name() string   { return f.name }
func (f *fakeAdapter) Format() string { return f.name }
func (f *fakeAdapter) Scan(context.Context, canonical.Cursor, chan<- canonical.Event) error {
	return errors.New("not used")
}

func (f *fakeAdapter) Tail(context.Context, chan<- canonical.Event) error {
	return errors.New("not used")
}
func (f *fakeAdapter) ParseCursor(string) (canonical.Cursor, error) { return nil, nil }

// isolate snapshots and resets the registry for the duration of t. After
// the test ends, the original contents are restored. This keeps tests in
// package adapters from clobbering init-time registrations performed by
// blank-imports in sibling _test.go files (notably registry_init_test.go).
func isolate(t *testing.T) {
	t.Helper()
	snap := snapshotForTest()
	resetForTest()
	t.Cleanup(func() { restoreForTest(snap) })
}

// mustPanic asserts that fn panics with a message matching substring. The
// caller passes a substring rather than an exact match so tests survive
// minor wording changes in the panic message.
func mustPanic(t *testing.T, fn func()) (recovered any) {
	t.Helper()
	defer func() { recovered = recover() }()
	fn()
	return nil
}

func TestRegister_AddsFactory(t *testing.T) {
	isolate(t)

	f := fakeFactory("v3")
	Register("aiagent_v3", f)

	got, ok := Get("aiagent_v3")
	if !ok {
		t.Fatalf("Get(%q): not found after Register", "aiagent_v3")
	}
	if got == nil {
		t.Fatalf("Get(%q): factory is nil after Register", "aiagent_v3")
	}

	a, err := got("any-location", canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("factory(): unexpected error: %v", err)
	}
	if a.Name() != "v3" {
		t.Fatalf("factory produced wrong sentinel: got %q want %q", a.Name(), "v3")
	}
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	isolate(t)

	Register("dup", fakeFactory("a"))
	rec := mustPanic(t, func() { Register("dup", fakeFactory("b")) })
	if rec == nil {
		t.Fatalf("Register(duplicate) did not panic")
	}
	msg, _ := rec.(string)
	if msg == "" {
		t.Fatalf("expected string panic value; got %T %v", rec, rec)
	}
	if !contains(msg, "already registered") {
		t.Fatalf("panic message %q missing 'already registered'", msg)
	}
}

func TestRegister_PanicsOnEmptyFormat(t *testing.T) {
	isolate(t)

	rec := mustPanic(t, func() { Register("", fakeFactory("x")) })
	if rec == nil {
		t.Fatalf("Register(empty format) did not panic")
	}
	if msg, _ := rec.(string); !contains(msg, "non-empty") {
		t.Fatalf("panic message %q missing 'non-empty'", msg)
	}
}

func TestRegister_PanicsOnNilFactory(t *testing.T) {
	isolate(t)

	rec := mustPanic(t, func() { Register("nilfac", nil) })
	if rec == nil {
		t.Fatalf("Register(nil factory) did not panic")
	}
	if msg, _ := rec.(string); !contains(msg, "non-nil") {
		t.Fatalf("panic message %q missing 'non-nil'", msg)
	}
}

func TestGet_MissingReturnsFalse(t *testing.T) {
	isolate(t)

	f, ok := Get("nope")
	if ok {
		t.Fatalf("Get(%q) reported ok=true on empty registry", "nope")
	}
	if f != nil {
		t.Fatalf("Get(%q) returned non-nil factory %v on empty registry", "nope", f)
	}
}

func TestFormats_EmptyRegistry(t *testing.T) {
	isolate(t)

	got := Formats()
	if len(got) != 0 {
		t.Fatalf("Formats() on empty registry: got %v want []", got)
	}
}

func TestFormats_SortedAndComplete(t *testing.T) {
	isolate(t)

	// Register in deliberately scrambled order so the sort step is exercised.
	Register("opencode", fakeFactory("opencode"))
	Register("aiagent_v3", fakeFactory("v3"))
	Register("claude_code", fakeFactory("cc"))
	Register("aiagent_v2", fakeFactory("v2"))
	Register("codex", fakeFactory("codex"))

	got := Formats()
	want := []string{"aiagent_v2", "aiagent_v3", "claude_code", "codex", "opencode"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Formats() mismatch:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestFormats_ReturnsCopy(t *testing.T) {
	isolate(t)

	Register("a", fakeFactory("a"))
	Register("b", fakeFactory("b"))

	got := Formats()
	got[0] = "MUTATED"

	// A second call must reflect the registry, not the caller's mutation.
	want := []string{"a", "b"}
	got2 := Formats()
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("Formats() not a fresh copy:\ngot:  %v\nwant: %v", got2, want)
	}
}

// contains is a tiny substring helper to avoid pulling in strings for one call.
func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
