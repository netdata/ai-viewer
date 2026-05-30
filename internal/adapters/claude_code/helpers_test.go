package claude_code

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// drainBuffered collects all events currently available on ch in a single
// non-blocking round. Test-only helper.
func drainBuffered(ch chan canonical.Event) []canonical.Event {
	out := make([]canonical.Event, 0, cap(ch))
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// writeFileBytes writes b to path, creating parent directories. Test-only.
func writeFileBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// appendFileBytes appends b to path, creating parents. Used to simulate a
// producer appending records (resume / tail).
func appendFileBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(b); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

// countKind returns the number of events of the given EventKind.
func countKind(events []canonical.Event, kind canonical.EventKind) int {
	n := 0
	for _, ev := range events {
		if ev.EventKind() == kind {
			n++
		}
	}
	return n
}

// firstOfKind returns the first event of the given concrete type T, or the
// zero value + false.
func sessionStartedByNative(events []canonical.Event, nativeID string) (canonical.SessionStartedEvent, bool) {
	for _, ev := range events {
		if s, ok := ev.(canonical.SessionStartedEvent); ok && s.NativeID == nativeID {
			return s, true
		}
	}
	return canonical.SessionStartedEvent{}, false
}
