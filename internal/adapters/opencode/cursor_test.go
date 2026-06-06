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
		withTargetHash("target-deadbeef").
		withSchemaHash("deadbeef").
		withTable("part", TableWatermark{MaxIDSeen: "prt_zzz", MaxTimeUpdatedMs: 1779793313250, MaxTimeUpdatedID: "prt_zzz"}).
		withTable("message", TableWatermark{MaxIDSeen: "msg_yyy", MaxTimeUpdatedMs: 1779793313106, MaxTimeUpdatedID: "msg_yyy"})
	encoded := orig.String()
	got, err := ParseCursor(encoded)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if got.SchemaHash != "deadbeef" {
		t.Errorf("schema_hash lost: %q", got.SchemaHash)
	}
	if got.TargetHash != "target-deadbeef" {
		t.Errorf("target_hash lost: %q", got.TargetHash)
	}
	w := got.Tables["part"]
	if w.MaxIDSeen != "prt_zzz" || w.MaxTimeUpdatedMs != 1779793313250 || w.MaxTimeUpdatedID != "prt_zzz" {
		t.Errorf("part watermark lost: %+v", w)
	}
	if got.Tables["message"].MaxTimeUpdatedMs != 1779793313106 {
		t.Errorf("message watermark lost: %+v", got.Tables["message"])
	}
}

// TestParseCursor_V1ReScans verifies a v1 (pre-P1-A) cursor — which used the
// conflated single max_id field — is treated as a FRESH ZERO cursor so the
// adapter does a one-time idempotent full re-scan onto the new split-watermark
// shape (SOW-0005 round-2 P1-A), rather than mis-loading the old max_id into the
// new fields or erroring.
func TestParseCursor_V1ReScans(t *testing.T) {
	t.Parallel()
	v1 := `{"version":1,"schema_hash":"abc","tables":{"part":{"max_id":"prt_a","max_time_updated":10}}}`
	got, err := ParseCursor(v1)
	if err != nil {
		t.Fatalf("ParseCursor(v1): %v", err)
	}
	if got.Version != cursorVersion {
		t.Errorf("v1 cursor not upgraded to version %d: %+v", cursorVersion, got)
	}
	if got.hasProgress() {
		t.Errorf("v1 cursor must re-scan from zero (no progress), got %+v", got.Tables)
	}
	if got.SchemaHash != "" {
		t.Errorf("v1 cursor must drop its old schema_hash on re-scan, got %q", got.SchemaHash)
	}
}

// TestParseCursor_VersionlessReScans verifies a cursor JSON that omits the
// version field (a pre-versioned or hand-written blob) is treated as the retired
// shape → fresh re-scan, not silently coerced to the current version with stale
// watermarks.
func TestParseCursor_VersionlessReScans(t *testing.T) {
	t.Parallel()
	got, err := ParseCursor(`{"tables":{"part":{"max_id":"prt_a","max_time_updated":7}}}`)
	if err != nil {
		t.Fatalf("ParseCursor(no version): %v", err)
	}
	if got.Version != cursorVersion {
		t.Errorf("version = %d, want defaulted to %d", got.Version, cursorVersion)
	}
	if got.hasProgress() {
		t.Errorf("versionless cursor must re-scan from zero, got %+v", got.Tables)
	}
}

// TestParseCursor_UnknownVersionReScans asserts a future/unknown version is
// treated as a fresh re-scan rather than erroring: our OWN cursor shape drifting
// is recoverable by an idempotent backfill (unlike a corrupt blob, which errors).
func TestParseCursor_UnknownVersionReScans(t *testing.T) {
	t.Parallel()
	got, err := ParseCursor(`{"version":99}`)
	if err != nil {
		t.Fatalf("ParseCursor(version 99): unexpected error %v", err)
	}
	if got.Version != cursorVersion || got.hasProgress() {
		t.Errorf("unknown version must yield a fresh zero cursor, got %+v", got)
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
		withTable("session", TableWatermark{MaxIDSeen: "ses_b", MaxTimeUpdatedMs: 2, MaxTimeUpdatedID: "ses_b"}).
		withTable("part", TableWatermark{MaxIDSeen: "prt_a", MaxTimeUpdatedMs: 1, MaxTimeUpdatedID: "prt_a"})
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
// to the paging-position pair (time_updated, MaxTimeUpdatedID).
func TestCursor_After(t *testing.T) {
	t.Parallel()
	base := newCursor().withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_m", MaxTimeUpdatedMs: 50})
	aheadByTime := newCursor().withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_m", MaxTimeUpdatedMs: 100})
	aheadByID := newCursor().withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_z", MaxTimeUpdatedMs: 50})
	behind := newCursor().withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_a", MaxTimeUpdatedMs: 10})

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

// TestCursor_AfterIgnoresMaxIDSeen asserts MaxIDSeen does NOT participate in
// After ordering (SOW-0005 round-2 P1-A): two cursors with the SAME paging
// position but different MaxIDSeen are equal under After (neither is after the
// other). MaxIDSeen is the cheap-detect high-water, not the resume position.
func TestCursor_AfterIgnoresMaxIDSeen(t *testing.T) {
	t.Parallel()
	lowSeen := newCursor().withTable("part", TableWatermark{MaxIDSeen: "prt_a", MaxTimeUpdatedMs: 50, MaxTimeUpdatedID: "prt_m"})
	highSeen := newCursor().withTable("part", TableWatermark{MaxIDSeen: "prt_z", MaxTimeUpdatedMs: 50, MaxTimeUpdatedID: "prt_m"})
	if lowSeen.After(highSeen) || highSeen.After(lowSeen) {
		t.Errorf("MaxIDSeen must not affect After: low.After(high)=%v high.After(low)=%v",
			lowSeen.After(highSeen), highSeen.After(lowSeen))
	}
}

// TestCursor_AfterMultiTable asserts After requires at least one table to
// advance with NO table regressing — the no-regression invariant that
// prevents a partial cursor from being treated as forward progress.
func TestCursor_AfterMultiTable(t *testing.T) {
	t.Parallel()
	base := newCursor().
		withTable("message", TableWatermark{MaxTimeUpdatedID: "msg_m", MaxTimeUpdatedMs: 50}).
		withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_m", MaxTimeUpdatedMs: 50})
	// One table advances, the other holds: After.
	oneAdvances := newCursor().
		withTable("message", TableWatermark{MaxTimeUpdatedID: "msg_z", MaxTimeUpdatedMs: 60}).
		withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_m", MaxTimeUpdatedMs: 50})
	if !oneAdvances.After(base) {
		t.Error("oneAdvances.After(base) = false, want true")
	}
	// One advances but the other regresses: NOT After.
	mixed := newCursor().
		withTable("message", TableWatermark{MaxTimeUpdatedID: "msg_z", MaxTimeUpdatedMs: 60}).
		withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_a", MaxTimeUpdatedMs: 40})
	if mixed.After(base) {
		t.Error("mixed (one regresses).After(base) = true, want false")
	}
	// Missing a table the other has progress on: regression, NOT After.
	missing := newCursor().withTable("message", TableWatermark{MaxTimeUpdatedID: "msg_z", MaxTimeUpdatedMs: 100})
	if missing.After(base) {
		t.Error("missing-table.After(base) = true, want false")
	}
}

func TestCursor_AfterAlienType(t *testing.T) {
	t.Parallel()
	type alien struct{ canonical.Cursor }
	c := newCursor().withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_a", MaxTimeUpdatedMs: 1})
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
	a := newCursor().withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_a", MaxTimeUpdatedMs: 50})
	b := newCursor().
		withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_a", MaxTimeUpdatedMs: 50}).
		withSchemaHash("changed")
	if a.After(b) || b.After(a) {
		t.Errorf("schema_hash must not affect After: a.After(b)=%v b.After(a)=%v", a.After(b), b.After(a))
	}
}

func TestCursor_TargetHashNotPartOfAfter(t *testing.T) {
	t.Parallel()
	a := newCursor().
		withTargetHash("target-a").
		withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_a", MaxTimeUpdatedMs: 50})
	b := newCursor().
		withTargetHash("target-b").
		withTable("part", TableWatermark{MaxTimeUpdatedID: "prt_a", MaxTimeUpdatedMs: 50})
	if a.After(b) || b.After(a) {
		t.Errorf("target_hash must not affect After: a.After(b)=%v b.After(a)=%v", a.After(b), b.After(a))
	}
}

func TestGuardCursorTarget_ResetsMismatchedHash(t *testing.T) {
	t.Parallel()
	var ce collectErrs
	cur := newCursor().
		withTargetHash("old-target").
		withTable("session", TableWatermark{MaxIDSeen: "ses_1", MaxTimeUpdatedMs: 10, MaxTimeUpdatedID: "ses_1"})

	got := guardCursorTarget(cur, "/abs/opencode.db", "opencode:/abs/opencode.db", ce.onError)
	if got.hasProgress() {
		t.Fatalf("mismatched target_hash cursor preserved progress: %+v", got.Tables)
	}
	if got.TargetHash != targetHashForDBPath("/abs/opencode.db") {
		t.Fatalf("target_hash = %q, want current target hash", got.TargetHash)
	}
	if ce.count() == 0 {
		t.Fatal("mismatched target_hash did not surface a WARN via onError")
	}
}

func TestGuardCursorTarget_ResetsLegacyBoundaryCursor(t *testing.T) {
	t.Parallel()
	var ce collectErrs
	cur := newCursor().
		withTable("session", TableWatermark{MaxIDSeen: "ses_1", MaxTimeUpdatedMs: 10, MaxTimeUpdatedID: "ses_1"})

	got := guardCursorTarget(cur, "/abs/file:opencode.db", "opencode:file:opencode.db", ce.onError)
	if got.hasProgress() {
		t.Fatalf("legacy source/open-path boundary cursor preserved progress: %+v", got.Tables)
	}
	if got.TargetHash != targetHashForDBPath("/abs/file:opencode.db") {
		t.Fatalf("target_hash = %q, want current target hash", got.TargetHash)
	}
	if ce.count() == 0 {
		t.Fatal("legacy source/open-path boundary reset did not surface a WARN via onError")
	}
}

func TestGuardCursorTarget_PreservesLegacyHistoricalFallbackCursor(t *testing.T) {
	t.Parallel()
	var ce collectErrs
	dbPath := "/abs/opencode.db"
	cur := newCursor().
		withTable("session", TableWatermark{MaxIDSeen: "ses_1", MaxTimeUpdatedMs: 10, MaxTimeUpdatedID: "ses_1"})

	got := guardCursorTarget(cur, dbPath, sourceIDPrefix+dbPath, ce.onError)
	if !got.hasProgress() {
		t.Fatal("legacy historical fallback cursor lost progress")
	}
	if got.Tables["session"].MaxIDSeen != "ses_1" {
		t.Fatalf("session watermark = %+v, want preserved", got.Tables["session"])
	}
	if got.TargetHash != targetHashForDBPath(dbPath) {
		t.Fatalf("target_hash = %q, want current target hash", got.TargetHash)
	}
	if ce.count() != 0 {
		t.Fatalf("preserved legacy fallback cursor surfaced %d errors, want 0", ce.count())
	}
}

// TestCursor_CloneIndependent asserts clone produces an independent map so
// mutating a derived cursor never affects the receiver.
func TestCursor_CloneIndependent(t *testing.T) {
	t.Parallel()
	orig := newCursor().
		withTargetHash("target-a").
		withTable("part", TableWatermark{MaxIDSeen: "prt_a", MaxTimeUpdatedMs: 10, MaxTimeUpdatedID: "prt_a"})
	derived := orig.
		withTable("part", TableWatermark{MaxIDSeen: "prt_z", MaxTimeUpdatedMs: 20, MaxTimeUpdatedID: "prt_z"}).
		withSchemaHash("x")
	if orig.Tables["part"].MaxIDSeen != "prt_a" {
		t.Errorf("receiver mutated: orig MaxIDSeen = %q, want prt_a", orig.Tables["part"].MaxIDSeen)
	}
	if orig.SchemaHash != "" {
		t.Errorf("receiver mutated: orig SchemaHash = %q, want empty", orig.SchemaHash)
	}
	if derived.TargetHash != "target-a" {
		t.Errorf("derived TargetHash = %q, want target-a", derived.TargetHash)
	}
	if derived.Tables["part"].MaxIDSeen != "prt_z" {
		t.Errorf("derived MaxIDSeen = %q, want prt_z", derived.Tables["part"].MaxIDSeen)
	}
}

// TestAdvanceMaxIDSeen asserts the monotonic high-water never regresses: a
// greater id raises it, a smaller/equal id leaves it unchanged, and the paging
// position is untouched (SOW-0005 round-2 P1-A).
func TestAdvanceMaxIDSeen(t *testing.T) {
	t.Parallel()
	w := TableWatermark{MaxIDSeen: "prt_m", MaxTimeUpdatedMs: 99, MaxTimeUpdatedID: "prt_m"}
	// A greater id raises the high-water.
	if got := w.advanceMaxIDSeen("prt_z"); got.MaxIDSeen != "prt_z" {
		t.Errorf("advanceMaxIDSeen(prt_z).MaxIDSeen = %q, want prt_z", got.MaxIDSeen)
	}
	// A SMALLER id (an old row re-stamped) must NOT pull the high-water back.
	got := w.advanceMaxIDSeen("prt_a")
	if got.MaxIDSeen != "prt_m" {
		t.Errorf("advanceMaxIDSeen(prt_a).MaxIDSeen = %q, want prt_m (no regression)", got.MaxIDSeen)
	}
	// The paging position is untouched by the high-water advance.
	if got.MaxTimeUpdatedMs != 99 || got.MaxTimeUpdatedID != "prt_m" {
		t.Errorf("advanceMaxIDSeen mutated the paging position: %+v", got)
	}
}

// TestCmpWatermark covers the composite ordering directly: time dominates, the
// PAGING-POSITION id (MaxTimeUpdatedID) breaks ties; MaxIDSeen is not compared.
func TestCmpWatermark(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b TableWatermark
		want int
	}{
		{"equal", TableWatermark{MaxTimeUpdatedID: "x", MaxTimeUpdatedMs: 5}, TableWatermark{MaxTimeUpdatedID: "x", MaxTimeUpdatedMs: 5}, 0},
		{"time less", TableWatermark{MaxTimeUpdatedMs: 4}, TableWatermark{MaxTimeUpdatedMs: 5}, -1},
		{"time greater", TableWatermark{MaxTimeUpdatedMs: 6}, TableWatermark{MaxTimeUpdatedMs: 5}, 1},
		{"id tiebreak less", TableWatermark{MaxTimeUpdatedID: "a", MaxTimeUpdatedMs: 5}, TableWatermark{MaxTimeUpdatedID: "b", MaxTimeUpdatedMs: 5}, -1},
		{"id tiebreak greater", TableWatermark{MaxTimeUpdatedID: "c", MaxTimeUpdatedMs: 5}, TableWatermark{MaxTimeUpdatedID: "b", MaxTimeUpdatedMs: 5}, 1},
		{"time beats id", TableWatermark{MaxTimeUpdatedID: "a", MaxTimeUpdatedMs: 6}, TableWatermark{MaxTimeUpdatedID: "z", MaxTimeUpdatedMs: 5}, 1},
		{"MaxIDSeen ignored", TableWatermark{MaxIDSeen: "z", MaxTimeUpdatedID: "a", MaxTimeUpdatedMs: 5}, TableWatermark{MaxIDSeen: "a", MaxTimeUpdatedID: "a", MaxTimeUpdatedMs: 5}, 0},
	}
	for _, tc := range cases {
		if got := cmpWatermark(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: cmpWatermark = %d, want %d", tc.name, got, tc.want)
		}
	}
}
