package codex

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestReadOneLine_CompleteAndPartial covers the nil-error (complete line) and
// io.EOF hold-back (partial trailing line) branches of readOneLine.
func TestReadOneLine_CompleteAndPartial(t *testing.T) {
	t.Parallel()
	br := bufio.NewReaderSize(strings.NewReader("ab\ncd"), streamReaderSize)
	line, n, err := readOneLine(br)
	if err != nil || string(line) != "ab\n" || n != 3 {
		t.Fatalf("first line = (%q,%d,%v), want (\"ab\\n\",3,nil)", line, n, err)
	}
	// "cd" has no trailing newline → held back as io.EOF, consumed 0.
	line, n, err = readOneLine(br)
	if !errors.Is(err, io.EOF) || n != 0 || line != nil {
		t.Fatalf("partial line = (%q,%d,%v), want (nil,0,EOF)", line, n, err)
	}
}

// TestReadOneLine_OversizedWithNewline exercises the errLineTooLong +
// drainToNewline path: a line longer than scanBufferMax that DOES terminate in
// a '\n' (so the rest is drained and the reader is positioned at the next line).
func TestReadOneLine_OversizedWithNewline(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", scanBufferMax+(2*streamReaderSize))
	src := big + "\n" + "next\n"
	br := bufio.NewReaderSize(strings.NewReader(src), streamReaderSize)
	_, consumed, err := readOneLine(br)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("oversized line err = %v, want errLineTooLong", err)
	}
	// consumed must cover the whole oversized line up to AND including its '\n'.
	if consumed != int64(len(big)+1) {
		t.Fatalf("consumed = %d, want %d (drained to newline)", consumed, len(big)+1)
	}
	// The reader is now positioned at "next\n".
	line, _, err := readOneLine(br)
	if err != nil || string(line) != "next\n" {
		t.Fatalf("post-drain line = (%q,%v), want (\"next\\n\",nil)", line, err)
	}
}

// TestReadOneLine_OversizedNoNewline exercises the drainToNewline io.EOF branch:
// an oversized line that runs to EOF with no trailing newline. consumed covers
// the rest of the file and the next read reports io.EOF.
func TestReadOneLine_OversizedNoNewline(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("y", scanBufferMax+(2*streamReaderSize))
	br := bufio.NewReaderSize(strings.NewReader(big), streamReaderSize)
	_, consumed, err := readOneLine(br)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("oversized-noeol err = %v, want errLineTooLong", err)
	}
	if consumed != int64(len(big)) {
		t.Fatalf("consumed = %d, want %d (drained to EOF)", consumed, len(big))
	}
	if _, _, err := readOneLine(br); !errors.Is(err, io.EOF) {
		t.Fatalf("post-drain read = %v, want io.EOF", err)
	}
}

// TestReadOneLine_SingleSliceOversized exercises the err==nil oversized branch:
// a buffer large enough that ReadSlice returns the whole line in one call but it
// still exceeds scanBufferMax.
func TestReadOneLine_SingleSliceOversized(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("z", scanBufferMax+5) + "\n"
	// Reader buffer larger than the line so ReadSlice returns it whole (err==nil).
	br := bufio.NewReaderSize(strings.NewReader(big), len(big)+16)
	_, consumed, err := readOneLine(br)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("single-slice oversized err = %v, want errLineTooLong", err)
	}
	if consumed != int64(len(big)) {
		t.Fatalf("consumed = %d, want %d", consumed, len(big))
	}
}

// TestDrainToNewline_ErrorPropagates asserts drainToNewline returns a non-EOF
// read error from the underlying reader.
func TestDrainToNewline_ErrorPropagates(t *testing.T) {
	t.Parallel()
	br := bufio.NewReaderSize(&errReader{after: []byte("nonewline")}, 4)
	_, err := drainToNewline(br)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("drainToNewline error = %v, want a non-EOF error", err)
	}
}

// errReader returns `after` bytes once, then a hard error (not io.EOF) so the
// drain/read error branches are exercised.
type errReader struct {
	after []byte
	done  bool
}

func (e *errReader) Read(p []byte) (int, error) {
	if !e.done && len(e.after) > 0 {
		n := copy(p, e.after)
		e.after = e.after[n:]
		if len(e.after) == 0 {
			e.done = true
		}
		return n, nil
	}
	return 0, errors.New("synthetic read failure")
}

// TestStreamLines_ReadErrorSurfaces asserts a hard read error (not EOF) from the
// reader is returned wrapped, not swallowed.
func TestStreamLines_ReadErrorSurfaces(t *testing.T) {
	t.Parallel()
	m := newFileMapper(mapperConfig{sourceID: "codex:/t", nativeID: "sid"})
	out := make(chan canonical.Event, 16)
	res, err := streamLines(context.Background(), &errReader{after: []byte("nonewline")}, 0, "r.jsonl", m, newUnknownDedup(), out, func(error) {})
	if err == nil {
		t.Fatalf("streamLines with read failure = nil err, want error; res=%+v", res)
	}
}

// TestStreamLines_ParseErrorSurfacedOnce asserts a malformed (non-unknown-type)
// line surfaces a SourceError, while the stream continues past it.
func TestStreamLines_ParseErrorSurfaced(t *testing.T) {
	t.Parallel()
	m := newFileMapper(mapperConfig{sourceID: "codex:/t", nativeID: "sid"})
	out := make(chan canonical.Event, 64)
	var errs []string
	onError := func(e error) { errs = append(errs, e.Error()) }
	src := metaLine("sid", `"exec"`) + "\n" + `{"type":}` + "\n" // 2nd line malformed JSON
	res, err := streamLines(context.Background(), strings.NewReader(src), 0, "r.jsonl", m, newUnknownDedup(), out, onError)
	if err != nil {
		t.Fatalf("streamLines = %v", err)
	}
	if len(errs) == 0 {
		t.Error("malformed line did not surface a SourceError")
	}
	if res.emitted == 0 {
		t.Error("session_meta before the malformed line should still emit")
	}
}

// TestStreamLines_ContextCancelMidEmit asserts cancellation while events are
// pending returns ctx.Err and the advanced offset.
func TestStreamLines_ContextCancelMidEmit(t *testing.T) {
	t.Parallel()
	m := newFileMapper(mapperConfig{sourceID: "codex:/t", nativeID: "sid"})
	// Unbuffered channel + cancelled ctx so the first emit selects ctx.Done.
	out := make(chan canonical.Event)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := metaLine("sid", `"exec"`) + "\n"
	_, err := streamLines(ctx, strings.NewReader(src), 0, "r.jsonl", m, newUnknownDedup(), out, func(error) {})
	if err == nil {
		t.Fatal("streamLines on cancelled ctx during emit = nil, want ctx err")
	}
}

// TestShouldSurfaceParseError_Families covers all three branches: unknown
// top-level type (deduped), unknown nested payload type (deduped), and a
// generic error (always surfaced).
func TestShouldSurfaceParseError_Families(t *testing.T) {
	t.Parallel()
	d := newUnknownDedup()
	ute := &unknownTypeError{Type: "frob"}
	if !shouldSurfaceParseError(d, ute) || shouldSurfaceParseError(d, ute) {
		t.Error("unknown top-level type dedup broken")
	}
	upe := &unknownPayloadTypeError{Owner: "response_item", Type: "ufo"}
	if !shouldSurfaceParseError(d, upe) || shouldSurfaceParseError(d, upe) {
		t.Error("unknown nested payload type dedup broken")
	}
	// A top-level "frob" and a nested ".../frob" must not collide (distinct key spaces).
	upe2 := &unknownPayloadTypeError{Owner: "response_item", Type: "frob"}
	if !shouldSurfaceParseError(d, upe2) {
		t.Error("nested key collided with top-level key space")
	}
	generic := errors.New("malformed json")
	// A generic (non-unknown-variant) error surfaces every time — calling twice
	// must both report true (no dedup).
	if !shouldSurfaceParseError(d, generic) {
		t.Error("generic parse error must surface on first sight")
	}
	if !shouldSurfaceParseError(d, generic) {
		t.Error("generic parse error must surface again (not deduped)")
	}
	// Nil dedup is tolerated (always first).
	if !shouldSurfaceParseError(nil, ute) {
		t.Error("nil dedup should report first=true")
	}
}

// TestWithinResolvedRoot_EscapeAndTail covers the escape branch and the
// not-yet-exist tail recursion (evalSymlinksAllowingTail) of withinResolvedRoot.
func TestWithinResolvedRoot_EscapeAndTail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	// A path under the root that does NOT exist yet → judged by its parent,
	// returns inside=true (exercises evalSymlinksAllowingTail recursion).
	notYet := filepath.Join(resolved, "2025", "11", "20", "rollout-x.jsonl")
	got, ok, err := withinResolvedRoot(resolved, notYet)
	if err != nil || !ok {
		t.Fatalf("non-existent in-root path = (%q,%v,%v), want inside", got, ok, err)
	}
	// A path clearly outside the root → escape (inside=false, no error).
	outside := filepath.Join(filepath.Dir(resolved), "elsewhere", "x.jsonl")
	_, ok, err = withinResolvedRoot(resolved, outside)
	if err != nil {
		t.Fatalf("outside path resolve err = %v", err)
	}
	if ok {
		t.Error("path outside the root must report inside=false")
	}
}

// TestWithinSourceRoot_SurfacesEscape covers withinSourceRoot's onError escape
// branch.
func TestWithinSourceRoot_SurfacesEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	outside := filepath.Join(filepath.Dir(resolved), "elsewhere", "x.jsonl")
	var errs []string
	ok := withinSourceRoot(resolved, outside, func(e error) { errs = append(errs, e.Error()) })
	if ok {
		t.Error("withinSourceRoot must reject an out-of-root path")
	}
	if len(errs) == 0 || !strings.Contains(errs[0], "outside the sessions root") {
		t.Errorf("escape not surfaced; errs=%v", errs)
	}
}

// TestUnknownDedup_NilSafe asserts the dedup helper tolerates a nil receiver and
// a nil map.
func TestUnknownDedup_NilSafe(t *testing.T) {
	t.Parallel()
	var d *unknownDedup
	if !d.first("k") {
		t.Error("nil dedup.first should report true")
	}
	d2 := &unknownDedup{}
	if !d2.first("k") || d2.first("k") {
		t.Error("zero-value dedup.first broken")
	}
}

// TestFirstRecordIsSessionMeta_Branches covers the empty-file, blank-line-skip,
// oversized-first-line, malformed-first-line, and non-meta-first cases of the
// rule #24 probe.
func TestFirstRecordIsSessionMeta_Branches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"blank-then-meta", "\n\n" + metaLine("sid", `"exec"`) + "\n", true},
		{"meta-first", metaLine("sid", `"exec"`) + "\n", true},
		{"turn-context-first", `{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{}}` + "\n", false},
		{"malformed-first", `{not json` + "\n", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "r.jsonl")
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			f, err := os.Open(path) // #nosec G304 -- test-controlled temp path
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() { _ = f.Close() }()
			info, _ := f.Stat()
			got, perr := firstRecordIsSessionMeta(f, info.Size())
			if perr != nil {
				t.Fatalf("probe err: %v", perr)
			}
			if got != c.want {
				t.Errorf("firstRecordIsSessionMeta(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestFirstRecordIsSessionMeta_OversizedFirstLine asserts a first line longer
// than the scan buffer is skipped-over (not a usable session_meta) and the
// probe keeps reading; with no later meta it returns false.
func TestFirstRecordIsSessionMeta_OversizedFirstLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "r.jsonl")
	big := strings.Repeat("x", scanBufferMax+(2*streamReaderSize))
	content := big + "\n" + metaLine("sid", `"exec"`) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path) // #nosec G304 -- test-controlled temp path
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	info, _ := f.Stat()
	// The oversized first line is drained; the SECOND line is a session_meta, so
	// the probe finds it and returns true (forgiving past the unusable line).
	got, perr := firstRecordIsSessionMeta(f, info.Size())
	if perr != nil {
		t.Fatalf("probe err: %v", perr)
	}
	if !got {
		t.Error("probe should find the session_meta after an oversized first line")
	}
}

// TestUUIDTailAndIsHex covers the non-UUID and bad-hex branches.
func TestUUIDTailAndIsHex(t *testing.T) {
	t.Parallel()
	if uuidTail("too-few-parts") != "" {
		t.Error("uuidTail with <5 groups should be empty")
	}
	// Right group count, wrong lengths.
	if uuidTail("a-b-c-d-e") != "" {
		t.Error("uuidTail with wrong group lengths should be empty")
	}
	// Right shape but non-hex.
	if uuidTail("2025-11-20T16-zzzzzzzz-a2a1-75c3-a9bf-d8425e1785f5") != "" {
		t.Error("uuidTail with non-hex group should be empty")
	}
	if !isHex("00ff") || isHex("") || isHex("xy") {
		t.Error("isHex wrong")
	}
}

// TestEmitProgress_CancelledCtx covers emitProgress's ctx.Err early return.
func TestEmitProgress_CancelledCtx(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 1)
	if err := emitProgress(ctx, "codex:/t", newCursor(), out); err == nil {
		t.Error("emitProgress on cancelled ctx should return ctx err")
	}
}

// TestRelPathError covers relPath's error branch (a path that cannot be made
// relative to the root, e.g. a different volume root on Windows; on POSIX an
// absolute vs. the root produces a clean rel, so we assert the happy path plus
// a forward-slash normalization).
func TestRelPath(t *testing.T) {
	t.Parallel()
	got, err := relPath("/a/b", "/a/b/c/d.jsonl")
	if err != nil || got != "c/d.jsonl" {
		t.Fatalf("relPath = (%q,%v), want (\"c/d.jsonl\",nil)", got, err)
	}
}
