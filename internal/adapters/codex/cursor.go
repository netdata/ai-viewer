package codex

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// cursorVersion is the on-disk version of the persisted cursor. Bumped if the
// shape ever changes; ParseCursor refuses unknown versions.
const cursorVersion = 1

// Cursor is the resume token persisted in source_progress.cursor for the codex
// adapter. Keys in Files are paths RELATIVE to the configured sessions root
// (e.g. "YYYY/MM/DD/rollout-...UUIDv7.jsonl"), so the cursor survives a move of
// $CODEX_HOME. Keys in LegacyJSON are the basenames of legacy flat .json files
// directly under sessions/. See adapter-codex.md §"Cursor".
//
// Unlike claude_code's cursor, codex has no sidecar/sub-agent deferral (forks
// and sub-agents are separate top-level rollout files linked by id), so the
// MetaSeen/Parked/Finalized fields are intentionally absent here. The codex
// addition is LegacyJSON: a per-file one-shot map for static legacy .json files
// consumed during full scans.
type Cursor struct {
	// Files maps a rollout's relative path to its consumption state.
	Files map[string]FileCursor `json:"files,omitempty"`
	// LegacyJSON records which legacy flat .json files have already been consumed
	// by a full scan. Default off: a file absent from this map has not been
	// consumed yet. Observability/suppression only — not part of After() ordering.
	LegacyJSON map[string]LegacyFile `json:"legacy_json,omitempty"`
	// Version is the on-disk format version. Defaults to cursorVersion on
	// construction; ParseCursor refuses anything else.
	Version int `json:"version"`
}

// FileCursor tracks consumption of a single rollout file. The shape mirrors
// claude_code's FileCursor (byte-offset + truncation-defense size) with one
// codex-specific addition (MtimeUs) matching the cursor JSON in
// adapter-codex.md §"Cursor".
type FileCursor struct {
	// Offset is the byte offset of the next unread byte. Always points to the
	// start of a line; trailing partial lines are held back (spec §"Atomicity").
	Offset int64 `json:"offset"`
	// Size is the file size at which Offset was last recorded. Used to detect
	// truncation on resume (spec §"Cursor" restart logic).
	Size int64 `json:"size,omitempty"`
	// MtimeUs is the file mtime when Offset was last recorded, in microseconds
	// since the UNIX epoch. Observability + staleness heuristic (rule #23).
	MtimeUs int64 `json:"mtime_us,omitempty"`
	// LastTsUs is the timestamp of the last record consumed, in microseconds
	// since the UNIX epoch. Observability only.
	LastTsUs int64 `json:"last_ts_us,omitempty"`
	// EOFFinalizedSize is the file size at which an EOF-finalize already fired
	// (the mapper made a terminal decision at full-read EOF: an OLD-format turn
	// closed completed, a stale NEW-format turn closed failed/incomplete, or a
	// clean session with no open turn). It is the DURABLE marker that prevents
	// finalizeAtEOF from re-firing the same TurnFinalized / SessionFinalized on
	// every unchanged rescan or daemon restart (H2): the mapper's own
	// eofFinalized guard is per-instance and a replay-from-0 rebuilds a fresh
	// mapper each scan, so the marker must live in the cursor. A genuine append
	// grows Size past this value, so the equality check fails and the new EOF is
	// finalized normally. 0 means no EOF-finalize has fired yet for this file.
	EOFFinalizedSize int64 `json:"eof_finalized_size,omitempty"`
}

// LegacyFile is the per-legacy-file consumption record. It keeps the original
// cursor JSON field name from the spec and records that the static legacy file
// has already been scanned.
type LegacyFile struct {
	Ingested bool `json:"ingested"`
}

// newCursor returns an empty Cursor ready for use.
func newCursor() Cursor {
	return Cursor{
		Files:      map[string]FileCursor{},
		LegacyJSON: map[string]LegacyFile{},
		Version:    cursorVersion,
	}
}

// String implements canonical.Cursor. Returns stable JSON (sorted map keys via
// encoding/json) suitable for persistence.
func (c Cursor) String() string {
	out := c
	if out.Files == nil {
		out.Files = map[string]FileCursor{}
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

// After implements canonical.Cursor. Reports whether c is strictly after other
// on at least one file's byte offset, with no file regressing. A regression
// (lower Offset on any shared file, or a file the other has progress on that c
// lacks) defeats After. LegacyJSON is observability-only and does not
// participate in ordering. Mechanics are verbatim from claude_code.
func (c Cursor) After(other canonical.Cursor) bool {
	o, ok := other.(Cursor)
	if !ok {
		// A different cursor concrete type is comparable only by emptiness: c is
		// After it iff c has any file progress.
		return len(c.Files) > 0
	}
	advancedOne := false
	for name, mine := range c.Files {
		theirs, present := o.Files[name]
		if !present {
			if mine.Offset > 0 {
				advancedOne = true
			}
			continue
		}
		if mine.Offset < theirs.Offset {
			return false
		}
		if mine.Offset > theirs.Offset {
			advancedOne = true
		}
	}
	// Missing any file the other has progress on is a regression.
	for name, theirs := range o.Files {
		if _, present := c.Files[name]; present {
			continue
		}
		if theirs.Offset > 0 {
			return false
		}
	}
	return advancedOne
}

// ParseCursor decodes a stored cursor JSON blob into a Cursor. Empty input
// yields an empty Cursor (first run). An unknown version is rejected so a
// schema mismatch is never silently misinterpreted.
func ParseCursor(stored string) (Cursor, error) {
	if stored == "" {
		return newCursor(), nil
	}
	var c Cursor
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		return Cursor{}, fmt.Errorf("codex: decode cursor: %w", err)
	}
	if c.Version == 0 {
		c.Version = cursorVersion
	} else if c.Version != cursorVersion {
		return Cursor{}, fmt.Errorf("codex: unsupported cursor version %d (want %d)", c.Version, cursorVersion)
	}
	if c.Files == nil {
		c.Files = map[string]FileCursor{}
	}
	if c.LegacyJSON == nil {
		c.LegacyJSON = map[string]LegacyFile{}
	}
	return c, nil
}

// withFile returns a new Cursor with the given relative path's FileCursor
// replaced. The receiver is not mutated.
func (c Cursor) withFile(rel string, fc FileCursor) Cursor {
	out := c.clone()
	out.Files[rel] = fc
	return out
}

// legacyIngested reports whether the legacy .json file at basename has already
// been consumed by a full scan. Defaults to false (off).
func (c Cursor) legacyIngested(basename string) bool {
	if c.LegacyJSON == nil {
		return false
	}
	return c.LegacyJSON[basename].Ingested
}

// withLegacyIngested returns a new Cursor recording that the legacy .json file
// at basename has been consumed by a full scan. The receiver is not mutated.
func (c Cursor) withLegacyIngested(basename string) Cursor {
	out := c.clone()
	out.LegacyJSON[basename] = LegacyFile{Ingested: true}
	return out
}

// clone deep-copies the cursor's maps so callers can mutate the result without
// affecting the receiver.
func (c Cursor) clone() Cursor {
	out := Cursor{
		Files:      make(map[string]FileCursor, len(c.Files)+1),
		LegacyJSON: make(map[string]LegacyFile, len(c.LegacyJSON)+1),
		Version:    cursorVersion,
	}
	maps.Copy(out.Files, c.Files)
	maps.Copy(out.LegacyJSON, c.LegacyJSON)
	return out
}
