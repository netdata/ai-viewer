package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// cursorVersion is the on-disk version of the persisted cursor. Bumped to 2 by
// SOW-0005 round-2 P1-A: the watermark split (a single MaxID conflated the
// monotonic insert-detection id with the (time_updated, id) paging position, so
// an in-place UPDATE of an OLD row regressed MaxID and re-armed the expensive
// idle scan forever). ParseCursor refuses unknown versions AND treats a v1 (or
// any old-shape) blob as a fresh zero cursor, forcing a one-time full re-scan
// that is idempotent (the ingester upserts). Mirrors codex/cursor.go.
const cursorVersion = 2

// trackedTables is the fixed set of opencode tables the cursor watermarks,
// in the order they are read. session/message/part are the canonical tree;
// session_message is the agent/model-switch sidecar. Any other opencode table
// is out of scope (adapter-opencode.md §"Tables we read").
var trackedTables = []string{"session", "message", "part", "session_message"}

// Cursor is the resume token persisted in sources.cursor for the opencode
// adapter. Unlike the four file adapters (byte-offset per file), opencode is
// a single SQLite database, so the cursor is a per-table watermark over
// time-prefixed Sonyflake IDs and the auto-bumped time_updated column.
// See adapter-opencode.md §"Cursor".
//
// There are NO byte offsets here: opencode never appends lines to a file the
// adapter tails. The watermarks are the only progress state.
type Cursor struct {
	// Version is the on-disk format version. Defaults to cursorVersion on
	// construction; ParseCursor refuses anything else.
	Version int `json:"version"`
	// SchemaHash is a digest of the applied-migration name list
	// (__drizzle_migrations.name). It detects that opencode applied a new
	// migration between runs; on mismatch the adapter re-probes the schema
	// (later chunks) but does NOT reset the watermarks — column drift is
	// handled per-column. Observability/invalidation only; NOT part of
	// After() ordering. Empty until the first probe records it.
	SchemaHash string `json:"schema_hash,omitempty"`
	// TargetHash is a digest of the adapter DB open target. It prevents
	// reusing watermarks persisted for a different physical SQLite target.
	// It is NOT part of After() ordering.
	TargetHash string `json:"target_hash,omitempty"`
	// Tables maps each tracked table name to its watermark. A table absent
	// from the map has had no rows observed yet (cold start).
	Tables map[string]TableWatermark `json:"tables,omitempty"`
}

// TableWatermark is the per-table progress state. SOW-0005 round-2 P1-A split
// the former single MaxID into TWO INDEPENDENT concepts because the old field
// conflated two roles that can move in opposite directions:
//
//   - MaxIDSeen — the monotonic highest id EVER observed for the table. It
//     drives the CHEAP, PK-indexed insert detection in detectChange
//     (MAX(id) > MaxIDSeen is a b-tree seek). It NEVER regresses: advancing it
//     with `if id > MaxIDSeen` keeps it the true high-water id even when an OLD
//     row is updated in place. The pre-P1-A code set MaxID to the LAST-PAGED
//     row's id (which sorts by (time_updated, id)); when an old low-id row was
//     re-stamped with a fresh time_updated it sorted LAST → MaxID regressed to
//     that small id → MAX(id) stayed permanently greater → every idle poll re-ran
//     the unindexed (time_updated, id) full scan on the live multi-GB DB,
//     defeating AC#6's gate. MaxIDSeen cannot regress, so that scan never re-arms.
//   - MaxTimeUpdatedMs + MaxTimeUpdatedID — the (time_updated, id) PAGING
//     POSITION: the last-paged row's pair, the source of truth for the delta
//     query's WHERE/ORDER (time_updated > :tu OR (time_updated = :tu AND
//     id > :tuid) ORDER BY time_updated, id). MaxTimeUpdatedID is the in-place
//     tie-break id (it MAY be small if an old row was just re-paged) and is the
//     only id the delta query binds. It is also the resume-ordering id for
//     cmpWatermark/After (the order rows are actually consumed in).
type TableWatermark struct {
	// MaxIDSeen is the monotonic highest opencode row id EVER observed for this
	// table (e.g. "prt_..."). It only ever increases (advanced via
	// `if id > MaxIDSeen`), so the cheap MAX(id) > MaxIDSeen insert check never
	// re-arms the expensive probe after an in-place update of an old row.
	// Empty means no row observed yet.
	MaxIDSeen string `json:"max_id_seen,omitempty"`
	// MaxTimeUpdatedMs is the highest time_updated reached by paging, in
	// milliseconds since the UNIX epoch (opencode's native unit — the mapper
	// converts to canonical microseconds, never the cursor). It is the delta
	// query's :tu bind. 0 means no row paged yet.
	MaxTimeUpdatedMs int64 `json:"max_time_updated,omitempty"`
	// MaxTimeUpdatedID is the id of the last row paged at MaxTimeUpdatedMs — the
	// (time_updated, id) tie-break the delta query binds as :tuid. It MAY be a
	// small id (an old row re-stamped with a new time_updated), which is exactly
	// why it is kept SEPARATE from MaxIDSeen. Empty means no row paged yet.
	MaxTimeUpdatedID string `json:"max_time_updated_id,omitempty"`
}

// newCursor returns an empty Cursor ready for use.
func newCursor() Cursor {
	return Cursor{
		Version: cursorVersion,
		Tables:  map[string]TableWatermark{},
	}
}

// String implements canonical.Cursor. Returns stable JSON (encoding/json
// sorts map keys) suitable for persistence. Mirrors codex/cursor.go.
func (c Cursor) String() string {
	out := c
	if out.Tables == nil {
		out.Tables = map[string]TableWatermark{}
	}
	if out.Version == 0 {
		out.Version = cursorVersion
	}
	b, err := json.Marshal(out)
	if err != nil {
		// json.Marshal on a struct of known-encodable types cannot fail; if it
		// ever does, surface a sentinel so callers don't silently persist an
		// empty value.
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// After implements canonical.Cursor. Reports whether c is strictly after
// other on at least one table's watermark, with NO table regressing. A
// table's watermark advances when its (MaxTimeUpdatedMs, MaxTimeUpdatedID) pair
// increases lexicographically — time first, then id as the tiebreaker,
// matching the delta-query ordering. A lower pair on any shared table, or a
// table the other has progress on that c lacks, defeats After. MaxIDSeen and
// SchemaHash are NOT part of the ordering: MaxIDSeen is the cheap-detect
// high-water and can advance independently of the paging position, and
// SchemaHash is observability-only. The discipline is codex/cursor.go's After
// verbatim, lifted from byte offsets to the paging-position pair.
func (c Cursor) After(other canonical.Cursor) bool {
	o, ok := other.(Cursor)
	if !ok {
		// A different cursor concrete type is comparable only by emptiness:
		// c is After it iff c has any table progress.
		return c.hasProgress()
	}

	advancedOne, valid := compareCursorProgress(c.Tables, o.Tables)
	return valid && advancedOne
}

func compareCursorProgress(mine, theirs map[string]TableWatermark) (advancedOne, valid bool) {
	for name, mineWatermark := range mine {
		theirWatermark, present := theirs[name]
		if !present {
			if mineWatermark.nonZero() {
				advancedOne = true
			}
			continue
		}
		switch cmpWatermark(mineWatermark, theirWatermark) {
		case -1:
			return false, false
		case 1:
			advancedOne = true
		}
	}
	return advancedOne, !missingProgressTable(mine, theirs)
}

func missingProgressTable(mine, theirs map[string]TableWatermark) bool {
	for name, theirWatermark := range theirs {
		if _, present := mine[name]; !present && theirWatermark.nonZero() {
			return true
		}
	}
	return false
}

// cmpWatermark orders two watermarks by the PAGING POSITION (MaxTimeUpdatedMs,
// MaxTimeUpdatedID): time first, then id as the tiebreaker. Returns -1 if a<b,
// 0 if equal, 1 if a>b. This is the same composite key the delta query sorts
// by, so After's notion of "advanced" matches the order in which rows are
// actually consumed. MaxIDSeen is intentionally NOT compared here — it is the
// cheap-detect high-water, not the resume position.
func cmpWatermark(a, b TableWatermark) int {
	switch {
	case a.MaxTimeUpdatedMs < b.MaxTimeUpdatedMs:
		return -1
	case a.MaxTimeUpdatedMs > b.MaxTimeUpdatedMs:
		return 1
	}
	switch {
	case a.MaxTimeUpdatedID < b.MaxTimeUpdatedID:
		return -1
	case a.MaxTimeUpdatedID > b.MaxTimeUpdatedID:
		return 1
	}
	return 0
}

// nonZero reports whether the watermark carries any progress (either the
// cheap-detect high-water or the paging position has moved).
func (w TableWatermark) nonZero() bool {
	return w.MaxIDSeen != "" || w.MaxTimeUpdatedID != "" || w.MaxTimeUpdatedMs != 0
}

// advanceMaxIDSeen returns a copy of the watermark whose MaxIDSeen is raised to
// id when id is lexicographically greater (the monotonic insert-detection
// high-water never regresses). A smaller/equal id leaves it unchanged. The
// paging-position fields are untouched.
func (w TableWatermark) advanceMaxIDSeen(id string) TableWatermark {
	if id > w.MaxIDSeen {
		w.MaxIDSeen = id
	}
	return w
}

// hasProgress reports whether any tracked table has a non-zero watermark.
func (c Cursor) hasProgress() bool {
	for _, w := range c.Tables {
		if w.nonZero() {
			return true
		}
	}
	return false
}

// ParseCursor decodes a stored cursor JSON blob into a Cursor. Empty input
// yields an empty Cursor (first run). An unknown version is rejected. A v1 (or
// any pre-P1-A old-shape) cursor is treated as a FRESH ZERO cursor: the
// watermark split (P1-A) changed the on-disk shape, and a one-time full re-scan
// from zero is idempotent (the ingester upserts) and is the safe migration —
// far cheaper to reason about than partially re-deriving the new MaxIDSeen from
// the old MaxID. Mirrors codex/cursor.go's version discipline.
func ParseCursor(stored string) (Cursor, error) {
	if stored == "" {
		return newCursor(), nil
	}
	var c Cursor
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		return Cursor{}, fmt.Errorf("opencode: decode cursor: %w", err)
	}
	if c.Version == 0 {
		// A version-less blob predates explicit versioning; treat it as the
		// retired v1 shape → fresh re-scan (P1-A migration).
		return newCursor(), nil
	}
	if c.Version != cursorVersion {
		// A v1 cursor (or any other retired version) re-scans from zero rather
		// than erroring: column/shape drift in OUR own cursor is recoverable by a
		// one-time idempotent backfill, unlike a corrupt blob.
		return newCursor(), nil
	}
	if c.Tables == nil {
		c.Tables = map[string]TableWatermark{}
	}
	return c, nil
}

// withTable returns a new Cursor with the given table's watermark replaced.
// The receiver is not mutated. Mirrors codex's withFile.
func (c Cursor) withTable(table string, w TableWatermark) Cursor {
	out := c.clone()
	out.Tables[table] = w
	return out
}

// withSchemaHash returns a new Cursor carrying the given schema hash. The
// receiver is not mutated. Used by later chunks when __drizzle_migrations is
// probed; lives here so the cursor stays the single owner of its fields.
func (c Cursor) withSchemaHash(hash string) Cursor {
	out := c.clone()
	out.SchemaHash = hash
	return out
}

func (c Cursor) withTargetHash(hash string) Cursor {
	out := c.clone()
	out.TargetHash = hash
	return out
}

func targetHashForDBPath(dbPath string) string {
	sum := sha256.Sum256([]byte(dbPath))
	return hex.EncodeToString(sum[:])
}

// clone deep-copies the cursor's map so callers can mutate the result without
// affecting the receiver. Mirrors codex/cursor.go.
func (c Cursor) clone() Cursor {
	out := Cursor{
		Version:    cursorVersion,
		SchemaHash: c.SchemaHash,
		TargetHash: c.TargetHash,
		Tables:     make(map[string]TableWatermark, len(c.Tables)+1),
	}
	maps.Copy(out.Tables, c.Tables)
	return out
}
