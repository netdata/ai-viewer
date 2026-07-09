package codex

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
	rel := "2025/11/20/rollout-2025-11-20T18-59-09-019aa234.jsonl"
	orig := newCursor()
	orig.withFile(rel, FileCursor{Offset: 100, Size: 100, MtimeUs: 42, LastTsUs: 123})
	orig.withLegacyIngested("rollout-2025-06-26-5556f03d.json")
	encoded := orig.String()
	got, err := ParseCursor(encoded)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if got.Files[rel].Offset != 100 || got.Files[rel].MtimeUs != 42 {
		t.Fatalf("offset/mtime lost: %+v", got.Files[rel])
	}
	if !got.legacyIngested("rollout-2025-06-26-5556f03d.json") {
		t.Fatalf("legacyJSON ingested flag lost: %+v", got.LegacyJSON)
	}
}

// TestParseCursor_OldCursorWithoutLegacyJSON verifies a cursor persisted BEFORE
// the legacy_json field existed still parses (additive omitempty, unchanged
// version), yielding an empty/nil legacy map — not an error.
func TestParseCursor_OldCursorWithoutLegacyJSON(t *testing.T) {
	t.Parallel()
	old := `{"version":1,"files":{"2025/11/20/r.jsonl":{"offset":10,"size":10}}}`
	got, err := ParseCursor(old)
	if err != nil {
		t.Fatalf("ParseCursor(old cursor): %v", err)
	}
	if got.Files["2025/11/20/r.jsonl"].Offset != 10 {
		t.Fatalf("old cursor offset lost: %+v", got)
	}
	if len(got.LegacyJSON) != 0 {
		t.Fatalf("old cursor must yield empty legacyJSON, got %+v", got.LegacyJSON)
	}
}

// TestCursor_LegacyDefaultOff asserts the suppression flag defaults off: a
// legacy file never seen is not reported as ingested.
func TestCursor_LegacyDefaultOff(t *testing.T) {
	t.Parallel()
	c := newCursor()
	if c.legacyIngested("never-seen.json") {
		t.Fatal("unseen legacy file must not report ingested")
	}
}

func TestParseCursor_RejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	_, err := ParseCursor(`{"version":99}`)
	if err == nil {
		t.Fatal("ParseCursor(version 99): want error")
	}
}

func TestParseCursor_Malformed(t *testing.T) {
	t.Parallel()
	_, err := ParseCursor(`{not json`)
	if err == nil {
		t.Fatal("ParseCursor(malformed): want error")
	}
}

func TestCursor_StringStableSortedKeys(t *testing.T) {
	t.Parallel()
	c := newCursor()
	c.withFile("2025/11/20/b.jsonl", FileCursor{Offset: 2})
	c.withFile("2025/11/20/a.jsonl", FileCursor{Offset: 1})
	first := c.String()
	second := c.String()
	if first != second {
		t.Fatalf("cursor String() not stable:\n first:  %s\n second: %s", first, second)
	}
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
	rel := "2025/11/20/r.jsonl"
	base := newCursor()
	base.withFile(rel, FileCursor{Offset: 50, Size: 50})
	ahead := newCursor()
	ahead.withFile(rel, FileCursor{Offset: 100, Size: 100})
	behind := newCursor()
	behind.withFile(rel, FileCursor{Offset: 10, Size: 10})

	if !ahead.After(base) {
		t.Error("ahead.After(base) = false, want true")
	}
	if base.After(ahead) {
		t.Error("base.After(ahead) = true, want false")
	}
	if behind.After(base) {
		t.Error("behind.After(base) = true, want false")
	}
	if newCursor().After(base) {
		t.Error("empty.After(base) = true, want false")
	}
	if !base.After(newCursor()) {
		t.Error("base.After(empty) = false, want true")
	}
}

// TestCursor_AfterMultiFile asserts After requires at least one file to advance
// with NO file regressing (verbatim semantics from claude_code).
func TestCursor_AfterMultiFile(t *testing.T) {
	t.Parallel()
	a := "2025/11/20/a.jsonl"
	b := "2025/11/20/b.jsonl"
	base := newCursor()
	base.withFile(a, FileCursor{Offset: 50})
	base.withFile(b, FileCursor{Offset: 50})
	// One file advances, the other holds: After.
	oneAdvances := newCursor()
	oneAdvances.withFile(a, FileCursor{Offset: 60})
	oneAdvances.withFile(b, FileCursor{Offset: 50})
	if !oneAdvances.After(base) {
		t.Error("oneAdvances.After(base) = false, want true")
	}
	// One advances but the other regresses: NOT After.
	mixed := newCursor()
	mixed.withFile(a, FileCursor{Offset: 60})
	mixed.withFile(b, FileCursor{Offset: 40})
	if mixed.After(base) {
		t.Error("mixed (one regresses).After(base) = true, want false")
	}
	// Missing a file the other has progress on: regression, NOT After.
	missing := newCursor()
	missing.withFile(a, FileCursor{Offset: 100})
	if missing.After(base) {
		t.Error("missing-file.After(base) = true, want false")
	}
}

func TestCursor_AfterAlienType(t *testing.T) {
	t.Parallel()
	type alien struct{ canonical.Cursor }
	c := newCursor()
	c.withFile("2025/11/20/r.jsonl", FileCursor{Offset: 1})
	if !c.After(alien{}) {
		t.Error("cursor with progress should be After an alien cursor type")
	}
	if newCursor().After(alien{}) {
		t.Error("empty cursor should not be After an alien cursor type")
	}
}

// TestCursor_LegacyNotPartOfAfter asserts the legacyJSON suppression map is
// observability-only and does NOT participate in After ordering (mirrors how
// claude_code excludes MetaSeen/Parked/Finalized from After).
func TestCursor_LegacyNotPartOfAfter(t *testing.T) {
	t.Parallel()
	rel := "2025/11/20/r.jsonl"
	a := newCursor()
	a.withFile(rel, FileCursor{Offset: 50, Size: 50})
	// Same byte progress, but b additionally marked a legacy file ingested.
	b := newCursor()
	b.withFile(rel, FileCursor{Offset: 50, Size: 50})
	b.withLegacyIngested("legacy.json")
	if a.After(b) || b.After(a) {
		t.Errorf("legacyJSON must not affect After ordering: a.After(b)=%v b.After(a)=%v", a.After(b), b.After(a))
	}
}

// TestCursor_CloneIndependent asserts clone produces independent maps so
// mutating the clone never affects the receiver (truncation-defense + tail
// rely on this immutability, verbatim from claude_code).
func TestCursor_CloneIndependent(t *testing.T) {
	t.Parallel()
	rel := "2025/11/20/r.jsonl"
	orig := newCursor()
	orig.withFile(rel, FileCursor{Offset: 10})
	derived := orig.clone()
	derived.withFile(rel, FileCursor{Offset: 20})
	derived.withLegacyIngested("x.json")
	if orig.Files[rel].Offset != 10 {
		t.Errorf("receiver mutated: orig offset = %d, want 10", orig.Files[rel].Offset)
	}
	if orig.legacyIngested("x.json") {
		t.Error("receiver mutated: orig should not have legacy flag")
	}
	if derived.Files[rel].Offset != 20 {
		t.Errorf("derived offset = %d, want 20", derived.Files[rel].Offset)
	}
}
