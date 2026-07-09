package aiagent_v3

import (
	"encoding/json"
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// cursorVersion is the on-disk version of the persisted cursor. Bumped if
// the shape ever changes; ParseCursor refuses unknown versions.
const cursorVersion = 1

// Cursor is the resume token persisted in source_progress.cursor for the v3
// adapter. It is keyed by the ledger filename (e.g. "<sessionId>.jsonl")
// and tracks per-file byte offset plus the highest ledger sequence and
// timestamp consumed. See specs/adapter-aiagent-v3.md §7.
type Cursor struct {
	// Files keys are ledger filenames (basename only, no directory).
	Files map[string]FileCursor `json:"files,omitempty"`
	// Version is the on-disk format version. Defaults to cursorVersion
	// on construction; ParseCursor refuses anything else.
	Version int `json:"version"`
}

// FileCursor tracks consumption of a single ledger file.
type FileCursor struct {
	// Offset is the byte offset of the next unread byte. Always points
	// to the start of a line; trailing partial lines are held back.
	Offset int64 `json:"offset"`
	// Size is the file size at which Offset was last recorded. Used to
	// detect truncation on resume.
	Size int64 `json:"size,omitempty"`
	// LastSeq is the highest ledger seq consumed from this file.
	LastSeq uint64 `json:"last_seq,omitempty"`
	// LastTsUs is the timestamp of the last record consumed, in
	// microseconds since UNIX epoch.
	LastTsUs int64 `json:"last_ts_us,omitempty"`
	// SeenSummary is true once a session_summary record has been
	// consumed for this file; the adapter still tails the file for
	// any trailing data but the session is terminal.
	SeenSummary bool `json:"seen_summary,omitempty"`
}

// newCursor returns an empty Cursor ready for use.
func newCursor() Cursor {
	return Cursor{Files: map[string]FileCursor{}, Version: cursorVersion}
}

// String implements canonical.Cursor. The returned value is stable JSON
// (sorted map keys via encoding/json) suitable for persistence.
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
		// json.Marshal on a struct with known-encodable types cannot
		// fail; if it ever does, surface a sentinel so callers don't
		// silently persist an empty value.
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// After implements canonical.Cursor. Reports whether c is strictly after
// other on at least one file's progress, with no file regressing on
// either Offset or LastSeq. Per the contract, the ingester uses this
// only for resume-ordering comparison.
//
// Equal byte offsets are tie-broken by LastSeq so a re-emit (e.g. a
// snapshot rewrite that produced the same offset but a higher record
// count) still registers as forward progress. A regression on either
// dimension (lower Offset OR lower LastSeq with the same Offset)
// defeats After.
func (c Cursor) After(other canonical.Cursor) bool {
	o, ok := other.(Cursor)
	if !ok {
		// A different cursor concrete type is treated as comparable
		// only by emptiness: c is After it iff c has any progress.
		return len(c.Files) > 0
	}
	advancedOne := false
	for name, mine := range c.Files {
		theirs, present := o.Files[name]
		if !present {
			if mine.Offset > 0 || mine.LastSeq > 0 {
				advancedOne = true
			}
			continue
		}
		if mine.Offset < theirs.Offset {
			return false
		}
		if mine.Offset > theirs.Offset {
			advancedOne = true
			continue
		}
		// Offsets equal — tie-break on LastSeq. A lower LastSeq at the
		// same offset is a regression; a higher LastSeq is real progress
		// (e.g. cursor recorded after a no-byte-advance checkpoint).
		if mine.LastSeq < theirs.LastSeq {
			return false
		}
		if mine.LastSeq > theirs.LastSeq {
			advancedOne = true
		}
	}
	// If we are missing any file the other has progress on, we regressed.
	for name, theirs := range o.Files {
		if _, present := c.Files[name]; present {
			continue
		}
		if theirs.Offset > 0 || theirs.LastSeq > 0 {
			return false
		}
	}
	return advancedOne
}

// ParseCursor decodes a stored cursor JSON blob into a Cursor. An empty
// input yields an empty Cursor (first-run). An unknown version is
// rejected so older or newer adapters refuse to silently misinterpret a
// schema mismatch.
func ParseCursor(stored string) (Cursor, error) {
	if stored == "" {
		return newCursor(), nil
	}
	var c Cursor
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		return Cursor{}, fmt.Errorf("aiagent_v3: decode cursor: %w", err)
	}
	if c.Version == 0 {
		// Legacy / first-write: accept and treat as the current version.
		c.Version = cursorVersion
	} else if c.Version != cursorVersion {
		return Cursor{}, fmt.Errorf("aiagent_v3: unsupported cursor version %d (want %d)", c.Version, cursorVersion)
	}
	if c.Files == nil {
		c.Files = map[string]FileCursor{}
	}
	return c, nil
}

// fileCursor returns a copy of the FileCursor for the given basename, or
// the zero value if not present. Pure read; no map mutation.
func (c Cursor) fileCursor(name string) FileCursor {
	if c.Files == nil {
		return FileCursor{}
	}
	return c.Files[name]
}

// withFile returns a new Cursor with the given basename's FileCursor
// replaced. The receiver is not mutated.
// withFile sets a file's cursor entry, mutating in-place (O(1)). SOW-0118: the
// old value-receiver copy was O(map_size) per call — at 325K files it was the
// dominant single-core cost (~38% of CPU in maps.Copy).
func (c *Cursor) withFile(name string, fc FileCursor) {
	if c.Files == nil {
		c.Files = map[string]FileCursor{}
	}
	c.Version = cursorVersion
	c.Files[name] = fc
}
