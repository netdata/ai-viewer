package opencode

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
	if len(c.Tables) != 0 || c.Version != cursorVersion {
		t.Fatalf("empty cursor wrong: %+v", c)
	}
}

func TestParseCursor_RoundTrip(t *testing.T) {
	t.Parallel()
	orig := newCursor().
		withSchemaHash("deadbeef").
		withTable("part", TableWatermark{MaxID: "prt_zzz", MaxTimeUpdatedMs: 1779793313250}).
		withTable("message", TableWatermark{MaxID: "msg_yyy", MaxTimeUpdatedMs: 1779793313106})
	encoded := orig.String()
	got, err := ParseCursor(encoded)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if got.SchemaHash != "deadbeef" {
		t.Errorf("schema_hash lost: %q", got.SchemaHash)
	}
	if got.Tables["part"].MaxID != "prt_zzz" || got.Tables["part"].MaxTimeUpdatedMs != 1779793313250 {
		t.Errorf("part watermark lost: %+v", got.Tables["part"])
	}
	if got.Tables["message"].MaxTimeUpdatedMs != 1779793313106 {
		t.Errorf("message watermark lost: %+v", got.Tables["message"])
	}
}

// TestParseCursor_OldCursorWithoutSchemaHash verifies a cursor persisted
// before schema_hash existed still parses (additive omitempty), yielding an
// empty hash — not an error.
func TestParseCursor_OldCursorWithoutSchemaHash(t *testing.T) {
	t.Parallel()
	old := `{"version":1,"tables":{"part":{"max_id":"prt_a","max_time_updated":10}}}`
	got, err := ParseCursor(old)
	if err != nil {
		t.Fatalf("ParseCursor(old): %v", err)
	}
	if got.Tables["part"].MaxID != "prt_a" {
		t.Errorf("old cursor watermark lost: %+v", got)
	}
	if got.SchemaHash != "" {
		t.Errorf("old cursor must yield empty schema_hash, got %q", got.SchemaHash)
	}
}

func TestParseCursor_RejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	if _, err := ParseCursor(`{"version":99}`); err == nil {
		t.Fatal("ParseCursor(version 99): want error")
	}
}

// TestParseCursor_DefaultsVersionWhenAbsent asserts a cursor JSON that omits
// the version field (version 0) is treated as the current version rather than
// rejected, so a hand-written or pre-versioned blob still loads.
func TestParseCursor_DefaultsVersionWhenAbsent(t *testing.T) {
	t.Parallel()
	got, err := ParseCursor(`{"tables":{"part":{"max_id":"prt_a","max_time_updated":7}}}`)
	if err != nil {
		t.Fatalf("ParseCursor(no version): %v", err)
	}
	if got.Version != cursorVersion {
		t.Errorf("version = %d, want defaulted to %d", got.Version, cursorVersion)
	}
	if got.Tables["part"].MaxTimeUpdatedMs != 7 {
		t.Errorf("watermark lost: %+v", got.Tables["part"])
	}
}

func TestParseCursor_Malformed(t *testing.T) {
	t.Parallel()
	if _, err := ParseCursor(`{not json`); err == nil {
		t.Fatal("ParseCursor(malformed): want error")
	}
}

func TestCursor_StringStableSortedKeys(t *testing.T) {
	t.Parallel()
	c := newCursor().
		withTable("session", TableWatermark{MaxID: "ses_b", MaxTimeUpdatedMs: 2}).
		withTable("part", TableWatermark{MaxID: "prt_a", MaxTimeUpdatedMs: 1})
	first := c.String()
	second := c.String()
	if first != second {
		t.Fatalf("cursor String() not stable:\n first:  %s\n second: %s", first, second)
	}
	var probe struct {
		Tables map[string]json.RawMessage `json:"tables"`
	}
	if err := json.Unmarshal([]byte(c.String()), &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(probe.Tables) != 2 {
		t.Fatalf("want 2 tables, got %d", len(probe.Tables))
	}
}

// TestCursor_After exercises the single-table advance/hold/regress cases and
// the empty-vs-progress comparisons, mirroring codex's After discipline lifted
// to the watermark pair.
func TestCursor_After(t *testing.T) {
	t.Parallel()
	base := newCursor().withTable("part", TableWatermark{MaxID: "prt_m", MaxTimeUpdatedMs: 50})
	aheadByTime := newCursor().withTable("part", TableWatermark{MaxID: "prt_m", MaxTimeUpdatedMs: 100})
	aheadByID := newCursor().withTable("part", TableWatermark{MaxID: "prt_z", MaxTimeUpdatedMs: 50})
	behind := newCursor().withTable("part", TableWatermark{MaxID: "prt_a", MaxTimeUpdatedMs: 10})

	if !aheadByTime.After(base) {
		t.Error("aheadByTime.After(base) = false, want true")
	}
	if !aheadByID.After(base) {
		t.Error("aheadByID.After(base) = false, want true (id is the tiebreaker at equal time)")
	}
	if base.After(aheadByTime) {
		t.Error("base.After(aheadByTime) = true, want false")
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

// TestCursor_AfterMultiTable asserts After requires at least one table to
// advance with NO table regressing — the no-regression invariant that
// prevents a partial cursor from being treated as forward progress.
func TestCursor_AfterMultiTable(t *testing.T) {
	t.Parallel()
	base := newCursor().
		withTable("message", TableWatermark{MaxID: "msg_m", MaxTimeUpdatedMs: 50}).
		withTable("part", TableWatermark{MaxID: "prt_m", MaxTimeUpdatedMs: 50})
	// One table advances, the other holds: After.
	oneAdvances := newCursor().
		withTable("message", TableWatermark{MaxID: "msg_z", MaxTimeUpdatedMs: 60}).
		withTable("part", TableWatermark{MaxID: "prt_m", MaxTimeUpdatedMs: 50})
	if !oneAdvances.After(base) {
		t.Error("oneAdvances.After(base) = false, want true")
	}
	// One advances but the other regresses: NOT After.
	mixed := newCursor().
		withTable("message", TableWatermark{MaxID: "msg_z", MaxTimeUpdatedMs: 60}).
		withTable("part", TableWatermark{MaxID: "prt_a", MaxTimeUpdatedMs: 40})
	if mixed.After(base) {
		t.Error("mixed (one regresses).After(base) = true, want false")
	}
	// Missing a table the other has progress on: regression, NOT After.
	missing := newCursor().withTable("message", TableWatermark{MaxID: "msg_z", MaxTimeUpdatedMs: 100})
	if missing.After(base) {
		t.Error("missing-table.After(base) = true, want false")
	}
}

func TestCursor_AfterAlienType(t *testing.T) {
	t.Parallel()
	type alien struct{ canonical.Cursor }
	c := newCursor().withTable("part", TableWatermark{MaxID: "prt_a", MaxTimeUpdatedMs: 1})
	if !c.After(alien{}) {
		t.Error("cursor with progress should be After an alien cursor type")
	}
	if newCursor().After(alien{}) {
		t.Error("empty cursor should not be After an alien cursor type")
	}
}

// TestCursor_SchemaHashNotPartOfAfter asserts schema_hash is
// observability/invalidation-only and does NOT participate in After ordering.
func TestCursor_SchemaHashNotPartOfAfter(t *testing.T) {
	t.Parallel()
	a := newCursor().withTable("part", TableWatermark{MaxID: "prt_a", MaxTimeUpdatedMs: 50})
	b := newCursor().
		withTable("part", TableWatermark{MaxID: "prt_a", MaxTimeUpdatedMs: 50}).
		withSchemaHash("changed")
	if a.After(b) || b.After(a) {
		t.Errorf("schema_hash must not affect After: a.After(b)=%v b.After(a)=%v", a.After(b), b.After(a))
	}
}

// TestCursor_CloneIndependent asserts clone produces an independent map so
// mutating a derived cursor never affects the receiver.
func TestCursor_CloneIndependent(t *testing.T) {
	t.Parallel()
	orig := newCursor().withTable("part", TableWatermark{MaxID: "prt_a", MaxTimeUpdatedMs: 10})
	derived := orig.
		withTable("part", TableWatermark{MaxID: "prt_z", MaxTimeUpdatedMs: 20}).
		withSchemaHash("x")
	if orig.Tables["part"].MaxID != "prt_a" {
		t.Errorf("receiver mutated: orig MaxID = %q, want prt_a", orig.Tables["part"].MaxID)
	}
	if orig.SchemaHash != "" {
		t.Errorf("receiver mutated: orig SchemaHash = %q, want empty", orig.SchemaHash)
	}
	if derived.Tables["part"].MaxID != "prt_z" {
		t.Errorf("derived MaxID = %q, want prt_z", derived.Tables["part"].MaxID)
	}
}

// TestCmpWatermark covers the composite ordering directly: time dominates, id
// breaks ties.
func TestCmpWatermark(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b TableWatermark
		want int
	}{
		{"equal", TableWatermark{MaxID: "x", MaxTimeUpdatedMs: 5}, TableWatermark{MaxID: "x", MaxTimeUpdatedMs: 5}, 0},
		{"time less", TableWatermark{MaxTimeUpdatedMs: 4}, TableWatermark{MaxTimeUpdatedMs: 5}, -1},
		{"time greater", TableWatermark{MaxTimeUpdatedMs: 6}, TableWatermark{MaxTimeUpdatedMs: 5}, 1},
		{"id tiebreak less", TableWatermark{MaxID: "a", MaxTimeUpdatedMs: 5}, TableWatermark{MaxID: "b", MaxTimeUpdatedMs: 5}, -1},
		{"id tiebreak greater", TableWatermark{MaxID: "c", MaxTimeUpdatedMs: 5}, TableWatermark{MaxID: "b", MaxTimeUpdatedMs: 5}, 1},
		{"time beats id", TableWatermark{MaxID: "a", MaxTimeUpdatedMs: 6}, TableWatermark{MaxID: "z", MaxTimeUpdatedMs: 5}, 1},
	}
	for _, tc := range cases {
		if got := cmpWatermark(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: cmpWatermark = %d, want %d", tc.name, got, tc.want)
		}
	}
}
