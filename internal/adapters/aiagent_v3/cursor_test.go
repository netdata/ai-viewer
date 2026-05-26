package aiagent_v3

import (
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestCursor_EmptyStringRoundTrip(t *testing.T) {
	t.Parallel()

	c := newCursor()
	s := c.String()
	// Empty Files map should still produce parseable JSON; spec says
	// String() is opaque but stable.
	parsed, err := ParseCursor(s)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if len(parsed.Files) != 0 {
		t.Fatalf("expected zero files, got %d", len(parsed.Files))
	}
	if parsed.Version != cursorVersion {
		t.Fatalf("unexpected version %d", parsed.Version)
	}
}

func TestCursor_PopulatedRoundTrip(t *testing.T) {
	t.Parallel()

	c := newCursor()
	c.Files["a.jsonl"] = FileCursor{Offset: 100, Size: 100, LastSeq: 4, LastTsUs: 1716724800000000, SeenSummary: true}
	c.Files["b.jsonl"] = FileCursor{Offset: 50, Size: 50, LastSeq: 2}
	s := c.String()
	parsed, err := ParseCursor(s)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if len(parsed.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(parsed.Files))
	}
	if parsed.Files["a.jsonl"] != c.Files["a.jsonl"] {
		t.Fatalf("a.jsonl mismatch: %+v != %+v", parsed.Files["a.jsonl"], c.Files["a.jsonl"])
	}
	if parsed.Files["b.jsonl"] != c.Files["b.jsonl"] {
		t.Fatalf("b.jsonl mismatch: %+v != %+v", parsed.Files["b.jsonl"], c.Files["b.jsonl"])
	}
}

func TestCursor_ParseRejectsBadJSON(t *testing.T) {
	t.Parallel()

	if _, err := ParseCursor("{garbage"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestCursor_ParseRejectsUnknownVersion(t *testing.T) {
	t.Parallel()

	if _, err := ParseCursor(`{"version":42,"files":{}}`); err == nil {
		t.Fatalf("expected error for unknown version")
	}
}

func TestCursor_ParseAcceptsLegacyMissingVersion(t *testing.T) {
	t.Parallel()

	c, err := ParseCursor(`{"files":{"a.jsonl":{"offset":7}}}`)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if c.Version != cursorVersion {
		t.Fatalf("expected version coerced to %d, got %d", cursorVersion, c.Version)
	}
	if c.Files["a.jsonl"].Offset != 7 {
		t.Fatalf("offset lost: %+v", c.Files["a.jsonl"])
	}
}

func TestCursor_AfterSameCursorIsFalse(t *testing.T) {
	t.Parallel()

	c := newCursor()
	c.Files["a.jsonl"] = FileCursor{Offset: 10}
	if c.After(c) {
		t.Fatalf("After(self) should be false")
	}
}

func TestCursor_AfterAdvancedOnOneFile(t *testing.T) {
	t.Parallel()

	older := newCursor()
	older.Files["a.jsonl"] = FileCursor{Offset: 5}
	newer := newCursor()
	newer.Files["a.jsonl"] = FileCursor{Offset: 10}
	if !newer.After(older) {
		t.Fatalf("newer should be After older")
	}
	if older.After(newer) {
		t.Fatalf("older.After(newer) should be false")
	}
}

func TestCursor_AfterRegressionIsFalse(t *testing.T) {
	t.Parallel()

	a := newCursor()
	a.Files["x.jsonl"] = FileCursor{Offset: 10}
	a.Files["y.jsonl"] = FileCursor{Offset: 20}

	b := newCursor()
	b.Files["x.jsonl"] = FileCursor{Offset: 11}
	b.Files["y.jsonl"] = FileCursor{Offset: 15} // regressed

	if b.After(a) {
		t.Fatalf("regression on one file should defeat After")
	}
}

func TestCursor_AfterAdditionalFileCountsAsAdvance(t *testing.T) {
	t.Parallel()

	older := newCursor()
	older.Files["a.jsonl"] = FileCursor{Offset: 5}

	newer := newCursor()
	newer.Files["a.jsonl"] = FileCursor{Offset: 5} // same
	newer.Files["b.jsonl"] = FileCursor{Offset: 1} // new file with progress

	if !newer.After(older) {
		t.Fatalf("adding a new file with progress should count as After")
	}
}

func TestCursor_AfterMissingFileWithProgressIsFalse(t *testing.T) {
	t.Parallel()

	older := newCursor()
	older.Files["a.jsonl"] = FileCursor{Offset: 5}
	older.Files["b.jsonl"] = FileCursor{Offset: 5}

	newer := newCursor()
	newer.Files["a.jsonl"] = FileCursor{Offset: 6}
	// missing b entirely — should be a regression
	if newer.After(older) {
		t.Fatalf("dropping a known-progressed file should defeat After")
	}
}

func TestCursor_AfterUsesLastSeqAsTiebreaker(t *testing.T) {
	t.Parallel()

	// Same per-file Offset but newer has higher LastSeq → forward progress.
	older := newCursor()
	older.Files["a.jsonl"] = FileCursor{Offset: 100, Size: 100, LastSeq: 4}
	newer := newCursor()
	newer.Files["a.jsonl"] = FileCursor{Offset: 100, Size: 100, LastSeq: 5}
	if !newer.After(older) {
		t.Fatalf("higher LastSeq at same Offset should count as After")
	}
	// And the reverse must be false — a lower LastSeq at the same Offset
	// is a regression.
	if older.After(newer) {
		t.Fatalf("lower LastSeq at same Offset must defeat After")
	}
	// Equal Offset and equal LastSeq → not strictly after.
	same := newCursor()
	same.Files["a.jsonl"] = FileCursor{Offset: 100, Size: 100, LastSeq: 4}
	if same.After(older) {
		t.Fatalf("equal cursors should not be After each other")
	}
}

// fakeCursor implements canonical.Cursor but is not aiagent_v3.Cursor.
// Used to verify the type-mismatch branch of After.
type fakeCursor struct{}

func (fakeCursor) String() string                { return "{}" }
func (fakeCursor) After(_ canonical.Cursor) bool { return false }

func TestCursor_AfterAlienCursorType(t *testing.T) {
	t.Parallel()

	c := newCursor()
	c.Files["x.jsonl"] = FileCursor{Offset: 10}
	if !c.After(fakeCursor{}) {
		t.Fatalf("a cursor with progress should be After an alien type")
	}
	empty := newCursor()
	if empty.After(fakeCursor{}) {
		t.Fatalf("empty cursor should not be After anything")
	}
}

func TestCursor_StringStableShape(t *testing.T) {
	t.Parallel()

	c := newCursor()
	c.Files["x.jsonl"] = FileCursor{Offset: 1, Size: 2}
	s := c.String()
	if !strings.Contains(s, `"version":1`) {
		t.Fatalf("expected version=1 in encoding, got %q", s)
	}
	if !strings.Contains(s, `"files":{`) {
		t.Fatalf("expected files object, got %q", s)
	}
}

func TestCursor_WithFileDoesNotMutateReceiver(t *testing.T) {
	t.Parallel()

	a := newCursor()
	a.Files["a.jsonl"] = FileCursor{Offset: 1}
	b := a.withFile("b.jsonl", FileCursor{Offset: 2})
	if _, ok := a.Files["b.jsonl"]; ok {
		t.Fatalf("receiver was mutated by withFile")
	}
	if b.Files["b.jsonl"].Offset != 2 {
		t.Fatalf("withFile lost the new entry: %+v", b)
	}
	if b.Files["a.jsonl"].Offset != 1 {
		t.Fatalf("withFile dropped existing entries: %+v", b)
	}
}

func TestCursor_FileCursorMissingReturnsZero(t *testing.T) {
	t.Parallel()

	c := newCursor()
	got := c.fileCursor("never.jsonl")
	if got != (FileCursor{}) {
		t.Fatalf("expected zero value, got %+v", got)
	}
	// Also exercises Cursor with nil map field.
	zero := Cursor{}
	if zero.fileCursor("any.jsonl") != (FileCursor{}) {
		t.Fatalf("zero Cursor should return zero FileCursor")
	}
}
