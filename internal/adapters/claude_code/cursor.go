package claude_code

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// cursorVersion is the on-disk version of the persisted cursor. Bumped if
// the shape ever changes; ParseCursor refuses unknown versions.
const cursorVersion = 1

// Cursor is the resume token persisted in sources.cursor for the
// claude-code adapter. Keys in Files and MetaSeen are paths RELATIVE to the
// configured projects root (e.g. "<sanitized-cwd>/<sessionId>.jsonl"), so
// the cursor survives a move of the projects dir and Bun/Node duplicate
// project dirs never collide. See spec §7.
type Cursor struct {
	// Files maps a transcript's relative path to its consumption state.
	Files map[string]FileCursor `json:"files,omitempty"`
	// MetaSeen maps a subagent .meta.json relative path to the sha256 of
	// its last-seen content, so a sidecar rewrite (description change on
	// resume) is re-read exactly once.
	MetaSeen map[string]string `json:"metaSeen,omitempty"`
	// Version is the on-disk format version. Defaults to cursorVersion on
	// construction; ParseCursor refuses anything else.
	Version int `json:"version"`
}

// FileCursor tracks consumption of a single transcript file.
type FileCursor struct {
	// Offset is the byte offset of the next unread byte. Always points to
	// the start of a line; trailing partial lines are held back (spec §6.3).
	Offset int64 `json:"offset"`
	// Size is the file size at which Offset was last recorded. Used to
	// detect truncation on resume (spec §7 step 2).
	Size int64 `json:"size,omitempty"`
	// LastTsUs is the timestamp of the last record consumed, in
	// microseconds since UNIX epoch. Observability only.
	LastTsUs int64 `json:"last_ts_us,omitempty"`
}

// newCursor returns an empty Cursor ready for use.
func newCursor() Cursor {
	return Cursor{
		Files:    map[string]FileCursor{},
		MetaSeen: map[string]string{},
		Version:  cursorVersion,
	}
}

// String implements canonical.Cursor. Returns stable JSON (sorted map keys
// via encoding/json) suitable for persistence.
func (c Cursor) String() string {
	out := c
	if out.Files == nil {
		out.Files = map[string]FileCursor{}
	}
	if out.MetaSeen == nil {
		out.MetaSeen = map[string]string{}
	}
	if out.Version == 0 {
		out.Version = cursorVersion
	}
	b, err := json.Marshal(out)
	if err != nil {
		// json.Marshal on a struct of known-encodable types cannot fail;
		// if it ever does, surface a sentinel so callers don't silently
		// persist an empty value.
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// After implements canonical.Cursor. Reports whether c is strictly after
// other on at least one file's byte offset, with no file regressing. A
// regression (lower Offset on any shared file, or a file the other has
// progress on that c lacks) defeats After. MetaSeen is observability-only
// and does not participate in ordering.
func (c Cursor) After(other canonical.Cursor) bool {
	o, ok := other.(Cursor)
	if !ok {
		// A different cursor concrete type is comparable only by emptiness:
		// c is After it iff c has any file progress.
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
		return Cursor{}, fmt.Errorf("claude_code: decode cursor: %w", err)
	}
	if c.Version == 0 {
		c.Version = cursorVersion
	} else if c.Version != cursorVersion {
		return Cursor{}, fmt.Errorf("claude_code: unsupported cursor version %d (want %d)", c.Version, cursorVersion)
	}
	if c.Files == nil {
		c.Files = map[string]FileCursor{}
	}
	if c.MetaSeen == nil {
		c.MetaSeen = map[string]string{}
	}
	return c, nil
}

// fileCursor returns a copy of the FileCursor for the given relative path,
// or the zero value if absent. Pure read; no map mutation.
func (c Cursor) fileCursor(rel string) FileCursor {
	if c.Files == nil {
		return FileCursor{}
	}
	return c.Files[rel]
}

// withFile returns a new Cursor with the given relative path's FileCursor
// replaced. The receiver is not mutated.
func (c Cursor) withFile(rel string, fc FileCursor) Cursor {
	out := c.clone()
	out.Files[rel] = fc
	return out
}

// withMetaSeen returns a new Cursor recording that the .meta.json at rel
// has been seen with the given content hash. The receiver is not mutated.
func (c Cursor) withMetaSeen(rel, hash string) Cursor {
	out := c.clone()
	out.MetaSeen[rel] = hash
	return out
}

// metaSeen reports the last-seen content hash for a .meta.json relative
// path, or "" if never seen.
func (c Cursor) metaSeen(rel string) string {
	if c.MetaSeen == nil {
		return ""
	}
	return c.MetaSeen[rel]
}

// clone deep-copies the cursor's maps so callers can mutate the result
// without affecting the receiver.
func (c Cursor) clone() Cursor {
	out := Cursor{
		Files:    make(map[string]FileCursor, len(c.Files)+1),
		MetaSeen: make(map[string]string, len(c.MetaSeen)+1),
		Version:  cursorVersion,
	}
	maps.Copy(out.Files, c.Files)
	maps.Copy(out.MetaSeen, c.MetaSeen)
	return out
}
