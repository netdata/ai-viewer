package aiagent_v2

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// cursorVersion is the on-disk version of the persisted v2 cursor.
// Bumped only if the shape changes; ParseCursor rejects unknown values
// so older or newer adapters refuse to misinterpret a schema mismatch.
const cursorVersion = 1

// Cursor is the resume token persisted in source_progress.cursor for the v2
// adapter. Because v2 rewrites the whole `<originId>.json.gz` on every
// snapshot, byte offsets are meaningless; the cursor instead pins a
// per-file `(content_hash, mtime_ns, size)` tuple so a re-scan can skip
// a file when nothing changed and still detect content rewrites that
// preserved mtime (rare but observed under fs touch).
//
// See `.agents/sow/specs/adapter-aiagent-v2.md` §Cursor.
type Cursor struct {
	// Files keys are basenames (no directory) of `<originId>.json.gz`.
	Files map[string]FileCursor `json:"files,omitempty"`
	// Version is the on-disk format version.
	Version int `json:"version"`
}

// FileCursor tracks consumption of a single snapshot file. ContentHash
// is the SHA-256 hex of the decompressed JSON bytes; LastMtime /
// LastSize capture stat-level identity so a stat-only check can short-
// circuit before decompression.
type FileCursor struct {
	// ContentHash is the SHA-256 hex of the decompressed JSON payload.
	ContentHash string `json:"content_hash,omitempty"`
	// LastMtime is the file mtime at hash time, in UNIX nanoseconds.
	LastMtime int64 `json:"last_mtime_ns,omitempty"`
	// LastSize is the file size at hash time, in bytes (compressed).
	LastSize int64 `json:"last_size,omitempty"`
}

// newCursor returns an empty Cursor ready for use.
func newCursor() Cursor {
	return Cursor{Files: map[string]FileCursor{}, Version: cursorVersion}
}

// String implements canonical.Cursor. The returned value is stable
// JSON (sorted map keys via encoding/json) suitable for persistence in
// `source_progress.cursor`.
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
		// json.Marshal of these types cannot fail in practice; surface a
		// sentinel so persistence never silently writes an empty string.
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// After implements canonical.Cursor. Reports whether c is strictly
// after other: at least one file's ContentHash differs (or appears in c
// but not in other) AND no file regressed on mtime. A file dropping out
// of c that existed in other counts as a regression because the
// ingester should not consider us "ahead" when we lost track of a file.
func (c Cursor) After(other canonical.Cursor) bool {
	o, ok := other.(Cursor)
	if !ok {
		// Alien cursor type — treat as comparable only by emptiness:
		// we are After it iff we have any progress.
		return len(c.Files) > 0
	}
	advancedOne := false
	for name, mine := range c.Files {
		theirs, present := o.Files[name]
		if !present {
			if mine.ContentHash != "" || mine.LastMtime > 0 {
				advancedOne = true
			}
			continue
		}
		if mine.LastMtime < theirs.LastMtime {
			return false
		}
		if mine.ContentHash != theirs.ContentHash {
			advancedOne = true
			continue
		}
		if mine.LastMtime > theirs.LastMtime {
			advancedOne = true
		}
	}
	// A file present in other but missing from c is a regression: we
	// have lost track of progress that other carried.
	for name := range o.Files {
		if _, present := c.Files[name]; !present {
			return false
		}
	}
	return advancedOne
}

// ParseCursor decodes a stored cursor JSON blob into a Cursor. An
// empty input yields an empty Cursor (first-run). Unknown versions are
// rejected.
func ParseCursor(stored string) (Cursor, error) {
	if stored == "" {
		return newCursor(), nil
	}
	var c Cursor
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		return Cursor{}, fmt.Errorf("aiagent_v2: decode cursor: %w", err)
	}
	if c.Version == 0 {
		c.Version = cursorVersion
	} else if c.Version != cursorVersion {
		return Cursor{}, fmt.Errorf("aiagent_v2: unsupported cursor version %d (want %d)", c.Version, cursorVersion)
	}
	if c.Files == nil {
		c.Files = map[string]FileCursor{}
	}
	return c, nil
}

// fileCursor returns a copy of the FileCursor for the given basename,
// or the zero value if not present. Pure read.
func (c Cursor) fileCursor(name string) FileCursor {
	if c.Files == nil {
		return FileCursor{}
	}
	return c.Files[name]
}

// withFile returns a new Cursor with the given basename's FileCursor
// replaced. The receiver is not mutated.
func (c Cursor) withFile(name string, fc FileCursor) Cursor {
	out := Cursor{Files: make(map[string]FileCursor, len(c.Files)+1), Version: cursorVersion}
	maps.Copy(out.Files, c.Files)
	out.Files[name] = fc
	return out
}
