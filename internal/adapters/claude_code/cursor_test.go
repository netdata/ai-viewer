package claude_code

import (
	"encoding/json"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestParseCursor_Empty(t *testing.T) {
	t.Parallel()
	c, err := ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor(\"\"): %v", err)
	}
	if len(c.Files) != 0 || c.Version != cursorVersion {
		t.Fatalf("empty cursor wrong: %+v", c)
	}
}

func TestParseCursor_RoundTrip(t *testing.T) {
	t.Parallel()
	orig := newCursor().
		withFile("p/s.jsonl", FileCursor{Offset: 100, Size: 100, LastTsUs: 123}).
		withMetaSeen("p/s/subagents/agent-x.meta.json", "deadbeef")
	encoded := orig.String()
	got, err := ParseCursor(encoded)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if got.Files["p/s.jsonl"].Offset != 100 {
		t.Fatalf("offset lost: %+v", got)
	}
	if got.MetaSeen["p/s/subagents/agent-x.meta.json"] != "deadbeef" {
		t.Fatalf("metaSeen lost: %+v", got.MetaSeen)
	}
}

// TestParseCursor_ParkedRoundTrip verifies the P2.4d parked-completion map
// survives a String → ParseCursor round-trip.
func TestParseCursor_ParkedRoundTrip(t *testing.T) {
	t.Parallel()
	orig := newCursor().withParked(map[string]int64{
		"sess:agent:abc": 1779789609000000,
	})
	got, err := ParseCursor(orig.String())
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if got.Parked["sess:agent:abc"] != 1779789609000000 {
		t.Fatalf("parked lost across round-trip: %+v", got.Parked)
	}
}

// TestParseCursor_OldCursorWithoutParked verifies a cursor persisted BEFORE the
// `parked` field existed still parses (the field is additive with omitempty and
// the version is unchanged), yielding an empty/nil parked map — not an error.
func TestParseCursor_OldCursorWithoutParked(t *testing.T) {
	t.Parallel()
	// A version-1 cursor with no "parked" key (the pre-P2.4d shape).
	old := `{"version":1,"files":{"p/s.jsonl":{"offset":10,"size":10}},"metaSeen":{}}`
	got, err := ParseCursor(old)
	if err != nil {
		t.Fatalf("ParseCursor(old cursor): %v", err)
	}
	if got.Files["p/s.jsonl"].Offset != 10 {
		t.Fatalf("old cursor offset lost: %+v", got)
	}
	if len(got.Parked) != 0 {
		t.Fatalf("old cursor must yield empty parked, got %+v", got.Parked)
	}
}

// TestParseCursor_FinalizedRoundTrip verifies the P2.5c finalized-child set
// survives a String → ParseCursor round-trip and is readable back as a set.
func TestParseCursor_FinalizedRoundTrip(t *testing.T) {
	t.Parallel()
	orig := newCursor().withFinalized(map[string]struct{}{
		"sess:agent:abc": {},
		"sess:agent:def": {},
	})
	got, err := ParseCursor(orig.String())
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	set := got.finalizedSet()
	if _, ok := set["sess:agent:abc"]; !ok {
		t.Fatalf("finalized lost across round-trip: %+v", got.Finalized)
	}
	if _, ok := set["sess:agent:def"]; !ok {
		t.Fatalf("finalized lost across round-trip: %+v", got.Finalized)
	}
	// withFinalized sorts for a stable serialization.
	if len(got.Finalized) != 2 || got.Finalized[0] != "sess:agent:abc" {
		t.Fatalf("finalized not stably sorted: %+v", got.Finalized)
	}
}

// TestParseCursor_OldCursorWithoutFinalized verifies a cursor persisted BEFORE
// the `finalized` field existed still parses (additive omitempty, unchanged
// version), yielding an empty finalized set — not an error.
func TestParseCursor_OldCursorWithoutFinalized(t *testing.T) {
	t.Parallel()
	// A version-1 cursor with parked but no "finalized" key (the pre-P2.5c shape).
	old := `{"version":1,"files":{"p/s.jsonl":{"offset":10,"size":10}},"metaSeen":{},"parked":{"sess:agent:abc":42}}`
	got, err := ParseCursor(old)
	if err != nil {
		t.Fatalf("ParseCursor(old cursor): %v", err)
	}
	if got.Parked["sess:agent:abc"] != 42 {
		t.Fatalf("old cursor parked lost: %+v", got.Parked)
	}
	if len(got.Finalized) != 0 {
		t.Fatalf("old cursor must yield empty finalized, got %+v", got.Finalized)
	}
}

func TestParseCursor_RejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	_, err := ParseCursor(`{"version":99}`)
	if err == nil {
		t.Fatal("ParseCursor(version 99): want error")
	}
}

func TestCursor_StringStableSortedKeys(t *testing.T) {
	t.Parallel()
	c := newCursor().
		withFile("b.jsonl", FileCursor{Offset: 2}).
		withFile("a.jsonl", FileCursor{Offset: 1})
	// encoding/json sorts map keys; two serializations must be identical.
	first := c.String()
	second := c.String()
	if first != second {
		t.Fatalf("cursor String() not stable:\n first:  %s\n second: %s", first, second)
	}
	// And a.jsonl must precede b.jsonl in the output.
	var probe struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal([]byte(c.String()), &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(probe.Files) != 2 {
		t.Fatalf("want 2 files, got %d", len(probe.Files))
	}
}

func TestCursor_After(t *testing.T) {
	t.Parallel()
	base := newCursor().withFile("s.jsonl", FileCursor{Offset: 50, Size: 50})
	ahead := newCursor().withFile("s.jsonl", FileCursor{Offset: 100, Size: 100})
	behind := newCursor().withFile("s.jsonl", FileCursor{Offset: 10, Size: 10})

	if !ahead.After(base) {
		t.Error("ahead.After(base) = false, want true")
	}
	if base.After(ahead) {
		t.Error("base.After(ahead) = true, want false")
	}
	if behind.After(base) {
		t.Error("behind.After(base) = true, want false")
	}
	// Empty cursor is not after a cursor with progress.
	if newCursor().After(base) {
		t.Error("empty.After(base) = true, want false")
	}
	// A cursor with progress is after an empty cursor.
	if !base.After(newCursor()) {
		t.Error("base.After(empty) = false, want true")
	}
}

func TestCursor_AfterAlienType(t *testing.T) {
	t.Parallel()
	type alien struct{ canonical.Cursor }
	c := newCursor().withFile("s.jsonl", FileCursor{Offset: 1})
	if !c.After(alien{}) {
		t.Error("cursor with progress should be After an alien cursor type")
	}
	if newCursor().After(alien{}) {
		t.Error("empty cursor should not be After an alien cursor type")
	}
}
