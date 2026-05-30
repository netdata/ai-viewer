package opencode

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// cursorVersion is the on-disk version of the persisted cursor. Bumped if the
// shape ever changes; ParseCursor refuses unknown versions so a schema
// mismatch is never silently misinterpreted. Mirrors codex/cursor.go.
const cursorVersion = 1

// trackedTables is the fixed set of opencode tables the cursor watermarks,
// in the order they are read. session/message/part are the canonical tree;
// session_message is the agent/model-switch sidecar. Any other opencode table
// is out of scope (adapter-opencode.md §"Tables we read").
var trackedTables = []string{"session", "message", "part", "session_message"}

// Cursor is the resume token persisted in sources.cursor for the opencode
// adapter. Unlike the four file adapters (byte-offset per file), opencode is
// a single SQLite database, so the cursor is a per-table pair of watermarks
// over time-prefixed Sonyflake IDs and the auto-bumped time_updated column.
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
	// Tables maps each tracked table name to its watermark. A table absent
	// from the map has had no rows observed yet (cold start).
	Tables map[string]TableWatermark `json:"tables,omitempty"`
}

// TableWatermark is the per-table progress pair. MaxID is the primary,
// PK-indexed watermark (id > :max_id is a cheap b-tree seek on the
// time-prefixed Sonyflake PK). MaxTimeUpdatedMs is the unindexed fallback
// that catches in-place row mutations (token totals, status changes) that an
// id-only cursor would miss; it is the tiebreaker in the delta query
// (... time_updated > :u OR (time_updated = :u AND id > :id) ...).
type TableWatermark struct {
	// MaxID is the highest opencode row id observed for this table
	// (e.g. "prt_..."). Lexicographic order equals time order. Empty means
	// no row observed yet.
	MaxID string `json:"max_id,omitempty"`
	// MaxTimeUpdatedMs is the highest time_updated observed for this table,
	// in milliseconds since the UNIX epoch (opencode's native unit — the
	// mapper converts to canonical microseconds, never the cursor). 0 means
	// no row observed yet.
	MaxTimeUpdatedMs int64 `json:"max_time_updated,omitempty"`
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
		// json.Marshal on a struct of known-encodable types cannot fail; if
		// it ever does, surface a sentinel so callers don't silently persist
		// an empty value.
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// After implements canonical.Cursor. Reports whether c is strictly after
// other on at least one table's watermark, with NO table regressing. A
// table's watermark advances when its (MaxTimeUpdatedMs, MaxID) pair
// increases lexicographically — time first, then id as the tiebreaker,
// matching the delta-query ordering. A lower pair on any shared table, or a
// table the other has progress on that c lacks, defeats After. SchemaHash is
// observability-only and does not participate in ordering. The discipline is
// codex/cursor.go's After verbatim, lifted from byte offsets to the
// watermark pair.
func (c Cursor) After(other canonical.Cursor) bool {
	o, ok := other.(Cursor)
	if !ok {
		// A different cursor concrete type is comparable only by emptiness:
		// c is After it iff c has any table progress.
		return c.hasProgress()
	}
	advancedOne := false
	for name, mine := range c.Tables {
		theirs, present := o.Tables[name]
		if !present {
			if mine.nonZero() {
				advancedOne = true
			}
			continue
		}
		switch cmpWatermark(mine, theirs) {
		case -1:
			return false
		case 1:
			advancedOne = true
		}
	}
	// Missing any table the other has progress on is a regression.
	for name, theirs := range o.Tables {
		if _, present := c.Tables[name]; present {
			continue
		}
		if theirs.nonZero() {
			return false
		}
	}
	return advancedOne
}

// cmpWatermark orders two watermarks by (MaxTimeUpdatedMs, MaxID): time
// first, then id as the tiebreaker. Returns -1 if a<b, 0 if equal, 1 if a>b.
// This is the same composite key the delta query sorts by, so After's notion
// of "advanced" matches the order in which rows are actually consumed.
func cmpWatermark(a, b TableWatermark) int {
	switch {
	case a.MaxTimeUpdatedMs < b.MaxTimeUpdatedMs:
		return -1
	case a.MaxTimeUpdatedMs > b.MaxTimeUpdatedMs:
		return 1
	}
	switch {
	case a.MaxID < b.MaxID:
		return -1
	case a.MaxID > b.MaxID:
		return 1
	}
	return 0
}

// nonZero reports whether the watermark carries any progress.
func (w TableWatermark) nonZero() bool {
	return w.MaxID != "" || w.MaxTimeUpdatedMs != 0
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
// yields an empty Cursor (first run). An unknown version is rejected so a
// schema mismatch is never silently misinterpreted. Mirrors codex/cursor.go.
func ParseCursor(stored string) (Cursor, error) {
	if stored == "" {
		return newCursor(), nil
	}
	var c Cursor
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		return Cursor{}, fmt.Errorf("opencode: decode cursor: %w", err)
	}
	if c.Version == 0 {
		c.Version = cursorVersion
	} else if c.Version != cursorVersion {
		return Cursor{}, fmt.Errorf("opencode: unsupported cursor version %d (want %d)", c.Version, cursorVersion)
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

// clone deep-copies the cursor's map so callers can mutate the result without
// affecting the receiver. Mirrors codex/cursor.go.
func (c Cursor) clone() Cursor {
	out := Cursor{
		Version:    cursorVersion,
		SchemaHash: c.SchemaHash,
		Tables:     make(map[string]TableWatermark, len(c.Tables)+1),
	}
	maps.Copy(out.Tables, c.Tables)
	return out
}
