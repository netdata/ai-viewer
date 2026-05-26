package aiagent_v2

import (
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestCursor_StringRoundtrip(t *testing.T) {
	t.Parallel()
	c := newCursor()
	c = c.withFile("a.json.gz", FileCursor{ContentHash: "abc", LastMtime: 100, LastSize: 9})
	s := c.String()
	parsed, err := ParseCursor(s)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	got := parsed.fileCursor("a.json.gz")
	if got.ContentHash != "abc" || got.LastMtime != 100 || got.LastSize != 9 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestCursor_ParseEmptyYieldsEmpty(t *testing.T) {
	t.Parallel()
	c, err := ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if len(c.Files) != 0 || c.Version != cursorVersion {
		t.Fatalf("expected zero cursor, got %+v", c)
	}
}

func TestCursor_ParseRejectsBadJSON(t *testing.T) {
	t.Parallel()
	if _, err := ParseCursor("not json"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestCursor_ParseRejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	if _, err := ParseCursor(`{"version":99}`); err == nil {
		t.Fatalf("expected error")
	}
}

func TestCursor_ParseAcceptsZeroVersion(t *testing.T) {
	t.Parallel()
	c, err := ParseCursor(`{"files":{"x.json.gz":{"content_hash":"abc"}}}`)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if c.Version != cursorVersion {
		t.Fatalf("Version: got %d want %d", c.Version, cursorVersion)
	}
}

func TestCursor_After_DetectsContentDiff(t *testing.T) {
	t.Parallel()
	older := newCursor().withFile("a", FileCursor{ContentHash: "old", LastMtime: 1})
	newer := newCursor().withFile("a", FileCursor{ContentHash: "new", LastMtime: 2})
	if !newer.After(older) {
		t.Fatalf("expected newer.After(older) = true")
	}
	if older.After(newer) {
		t.Fatalf("expected older.After(newer) = false")
	}
}

func TestCursor_After_MtimeRegressionDefeats(t *testing.T) {
	t.Parallel()
	a := newCursor().withFile("x", FileCursor{ContentHash: "h", LastMtime: 100})
	b := newCursor().withFile("x", FileCursor{ContentHash: "h", LastMtime: 50})
	if b.After(a) {
		t.Fatalf("regression should defeat After")
	}
}

func TestCursor_After_MissingFileInTargetIsRegression(t *testing.T) {
	t.Parallel()
	full := newCursor().
		withFile("a", FileCursor{ContentHash: "h", LastMtime: 1}).
		withFile("b", FileCursor{ContentHash: "h", LastMtime: 1})
	partial := newCursor().withFile("a", FileCursor{ContentHash: "h", LastMtime: 1})
	if partial.After(full) {
		t.Fatalf("losing a file is a regression")
	}
}

func TestCursor_After_AlienTypeFallsBackToEmptiness(t *testing.T) {
	t.Parallel()
	a := newCursor().withFile("x", FileCursor{ContentHash: "h"})
	if !a.After(alienCursor{}) {
		t.Fatalf("non-empty cursor should be After alien empty")
	}
	if newCursor().After(alienCursor{}) {
		t.Fatalf("empty cursor should not be After alien")
	}
}

func TestCursor_After_EqualReturnsFalse(t *testing.T) {
	t.Parallel()
	a := newCursor().withFile("x", FileCursor{ContentHash: "h", LastMtime: 1})
	b := newCursor().withFile("x", FileCursor{ContentHash: "h", LastMtime: 1})
	if a.After(b) {
		t.Fatalf("equal cursors should not be After each other")
	}
}

func TestCursor_After_NewFileAdvances(t *testing.T) {
	t.Parallel()
	a := newCursor().withFile("x", FileCursor{ContentHash: "h", LastMtime: 1})
	b := a.withFile("y", FileCursor{ContentHash: "g", LastMtime: 1})
	if !b.After(a) {
		t.Fatalf("adding a new file should advance")
	}
}

func TestCursor_FileCursor_NilSafe(t *testing.T) {
	t.Parallel()
	var c Cursor
	fc := c.fileCursor("x")
	if fc != (FileCursor{}) {
		t.Fatalf("expected zero FileCursor, got %+v", fc)
	}
}

func TestCursor_String_HandlesNilFiles(t *testing.T) {
	t.Parallel()
	var c Cursor
	s := c.String()
	if !strings.Contains(s, `"version"`) {
		t.Fatalf("String should always include version: %q", s)
	}
}

// _alienCursorCompiles asserts the helper type from adapter_test.go
// continues to satisfy canonical.Cursor as the interface evolves.
var _alienCursorCompiles canonical.Cursor = alienCursor{}
var _ = _alienCursorCompiles
