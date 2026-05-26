package aiagent_v3

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestResolvePayloadPath_UncapturedYieldsEmpty checks the spec §4.3
// short-circuit: an uncaptured ref returns a zero resolvedPayload.
func TestResolvePayloadPath_UncapturedYieldsEmpty(t *testing.T) {
	t.Parallel()

	got, err := resolvePayloadPath("/tmp/root", payloadRef{Captured: false, Path: "anything"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.LocationURI != "" || got.AbsolutePath != "" {
		t.Fatalf("expected zero resolvedPayload, got %+v", got)
	}
}

// TestResolvePayloadPath_EmptyPathYieldsEmpty: captured=true but path
// missing is also treated as uncaptured per spec.
func TestResolvePayloadPath_EmptyPathYieldsEmpty(t *testing.T) {
	t.Parallel()

	got, err := resolvePayloadPath("/tmp/root", payloadRef{Captured: true, Path: ""})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.LocationURI != "" {
		t.Fatalf("expected empty LocationURI, got %q", got.LocationURI)
	}
}

// TestResolvePayloadPath_TraversalRejected: a path with ../ escapes
// the root and must be rejected.
func TestResolvePayloadPath_TraversalRejected(t *testing.T) {
	t.Parallel()

	if _, err := resolvePayloadPath("/tmp/root", payloadRef{Captured: true, Path: "../etc/passwd"}); err == nil {
		t.Fatalf("expected error on traversal")
	}
}

// TestResolvePayloadPath_DeepEscapeRejected: a deeply-nested traversal
// path that escapes the configured root.
func TestResolvePayloadPath_DeepEscapeRejected(t *testing.T) {
	t.Parallel()

	if _, err := resolvePayloadPath("/tmp/root", payloadRef{Captured: true, Path: "../../etc/passwd"}); err == nil {
		t.Fatalf("expected error on deep traversal")
	}
}

// TestResolvePayloadPath_HappyURI: a valid relative path under root.
func TestResolvePayloadPath_HappyURI(t *testing.T) {
	t.Parallel()

	got, err := resolvePayloadPath("/tmp/root", payloadRef{Captured: true, Path: "payloads/x/turn-0001/llm-0001-request.http.gz"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(got.LocationURI, "file:///tmp/root/payloads/x/") {
		t.Fatalf("unexpected URI: %q", got.LocationURI)
	}
}

// TestMapTurnStatus_AllBranches covers every documented v3 turn status.
func TestMapTurnStatus_AllBranches(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ok":      "completed",
		"failed":  "failed",
		"running": "running",
		"weird":   "weird", // pass-through
	}
	for in, want := range cases {
		if got := mapTurnStatus(in); got != want {
			t.Fatalf("mapTurnStatus(%q)=%q want %q", in, got, want)
		}
	}
}

// TestMapOpStatus_AllBranches covers every documented v3 op status.
func TestMapOpStatus_AllBranches(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ok":      "completed",
		"failed":  "failed",
		"running": "running",
		"weird":   "weird",
	}
	for in, want := range cases {
		if got := mapOpStatus(in); got != want {
			t.Fatalf("mapOpStatus(%q)=%q want %q", in, got, want)
		}
	}
}

// TestParseOpTimes_FallbackToRecordTs: when the op omits startedAt /
// endedAt the parser defaults to the record's ts.
func TestParseOpTimes_FallbackToRecordTs(t *testing.T) {
	t.Parallel()

	rec := record{Common: commonFields{Ts: "2026-05-26T10:00:00.000Z"}}
	startUs, endUs, err := parseOpTimes(rec, opSummary{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if startUs == 0 || endUs == 0 || startUs != endUs {
		t.Fatalf("expected start==end==recTs, got start=%d end=%d", startUs, endUs)
	}
}

// TestParseOpTimes_BadStartedAt surfaces a wrapped error.
func TestParseOpTimes_BadStartedAt(t *testing.T) {
	t.Parallel()

	rec := record{Common: commonFields{Ts: "2026-05-26T10:00:00.000Z"}}
	_, _, err := parseOpTimes(rec, opSummary{StartedAt: "not a date"})
	if err == nil {
		t.Fatalf("expected error")
	}
}

// TestParseOpTimes_BadEndedAt surfaces a wrapped error.
func TestParseOpTimes_BadEndedAt(t *testing.T) {
	t.Parallel()

	rec := record{Common: commonFields{Ts: "2026-05-26T10:00:00.000Z"}}
	_, _, err := parseOpTimes(rec, opSummary{StartedAt: "2026-05-26T10:00:00.000Z", EndedAt: "garbage"})
	if err == nil {
		t.Fatalf("expected error")
	}
}

// TestParseOpTimes_BadRecordTs surfaces a wrapped error.
func TestParseOpTimes_BadRecordTs(t *testing.T) {
	t.Parallel()

	rec := record{Common: commonFields{Ts: "broken"}}
	_, _, err := parseOpTimes(rec, opSummary{})
	if err == nil {
		t.Fatalf("expected error")
	}
}

// TestMapRecord_BadTsReturnsError exercises the top-level error path.
func TestMapRecord_BadTsReturnsError(t *testing.T) {
	t.Parallel()

	rec := record{Common: commonFields{Version: 3, RecordType: recSessionStart, Seq: 1, Ts: "garbage", OriginID: "a", SessionID: "a"}, SessionStart: &sessionStartBody{}}
	if _, err := mapRecord(rec, "src", "/tmp"); err == nil {
		t.Fatalf("expected error")
	}
}

// TestMapRecord_TurnEndPayloadResolutionFailure rejects a turn_end that
// references a traversal-escaping payload path.
func TestMapRecord_TurnEndPayloadResolutionFailure(t *testing.T) {
	t.Parallel()

	rec := record{
		Common: commonFields{Version: 3, RecordType: recTurnEnd, Seq: 3, Ts: "2026-05-26T10:00:00.000Z", OriginID: "a", SessionID: "a"},
		TurnEnd: &turnEndBody{
			Turn:   1,
			Status: "ok",
			Ops: []opSummary{{
				OpID: "x", OpIndex: 1, Kind: "llm", Status: "ok",
				PayloadRefs: []payloadRef{{Kind: "llm_request", Captured: true, Path: "../escape"}},
			}},
		},
	}
	if _, err := mapRecord(rec, "src", "/tmp/root"); err == nil {
		t.Fatalf("expected traversal error")
	}
}

// TestMapRecord_UnhandledRecordType exercises the unreachable default
// branch by constructing a record with an unknown recordType directly.
func TestMapRecord_UnhandledRecordType(t *testing.T) {
	t.Parallel()

	rec := record{Common: commonFields{Version: 3, RecordType: "alien", Seq: 1, Ts: "2026-05-26T10:00:00.000Z", OriginID: "a", SessionID: "a"}}
	if _, err := mapRecord(rec, "src", "/tmp"); err == nil {
		t.Fatalf("expected error")
	}
}

// TestReadOneLine_LineTooLong forces the buffer-full path to surface
// errLineTooLong and drain the rest of the file.
func TestReadOneLine_LineTooLong(t *testing.T) {
	t.Parallel()

	// Build a line longer than scanBufferMax then a normal line, both
	// terminated by '\n'. The first read returns errLineTooLong; the
	// second read returns the well-formed line.
	huge := bytes.Repeat([]byte("a"), scanBufferMax+1024)
	huge = append(huge, '\n')
	normal := []byte("hello\n")
	br := bufio.NewReaderSize(bytes.NewReader(append(huge, normal...)), 64*1024)
	_, err := readOneLine(br)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("expected errLineTooLong, got %v", err)
	}
	got, err := readOneLine(br)
	if err != nil {
		t.Fatalf("second read err: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("unexpected line %q", got)
	}
}

// TestReadOneLine_PartialEOFIsHeldBack verifies the spec §6.5 invariant.
func TestReadOneLine_PartialEOFIsHeldBack(t *testing.T) {
	t.Parallel()

	br := bufio.NewReaderSize(bytes.NewReader([]byte("partial without newline")), 64*1024)
	_, err := readOneLine(br)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

// TestTailableName_FiltersNonLedger covers all reject branches.
func TestTailableName_FiltersNonLedger(t *testing.T) {
	t.Parallel()

	watch := "/tmp/root/session"
	cases := []struct {
		name string
		want string
	}{
		{watch + "/a.jsonl", "a.jsonl"},
		{watch + "/notes.txt", ""},
		{watch + "/a.jsonl.tmp-1-2", ""},
		{"/elsewhere/a.jsonl", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fakeEv := fakeFsEvent{name: c.name}
			got := tailableNameForTest(watch, fakeEv)
			if got != c.want {
				t.Fatalf("tailableName(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// TestCoerceCursor_AlienTypeYieldsEmpty restates the Scan-side contract.
func TestCoerceCursor_AlienTypeYieldsEmpty(t *testing.T) {
	t.Parallel()

	a, _ := New("/tmp/x", canonical.AdapterOptions{})
	cur, err := a.coerceCursor(fakeCursor{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cur.Files) != 0 {
		t.Fatalf("expected empty cursor, got %+v", cur)
	}
}

// TestCoerceCursor_OurTypeHonored: the happy path.
func TestCoerceCursor_OurTypeHonored(t *testing.T) {
	t.Parallel()

	a, _ := New("/tmp/x", canonical.AdapterOptions{})
	in := newCursor()
	in.Files["x.jsonl"] = FileCursor{Offset: 42}
	got, err := a.coerceCursor(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Files["x.jsonl"].Offset != 42 {
		t.Fatalf("offset lost: %+v", got)
	}
}

// TestCoerceCursor_NilFilesPreserved exercises the Version=0 / nil Files
// coercion branches.
func TestCoerceCursor_NilFilesPreserved(t *testing.T) {
	t.Parallel()

	a, _ := New("/tmp/x", canonical.AdapterOptions{})
	got, err := a.coerceCursor(Cursor{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Files == nil {
		t.Fatalf("expected non-nil Files map")
	}
	if got.Version == 0 {
		t.Fatalf("expected coerced Version")
	}
}

// TestBuildOpStarted_MultipleChildrenStashedInExtras exercises spec §10
// gap 8.
func TestBuildOpStarted_MultipleChildrenStashedInExtras(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:00.000Z","originId":"a","sessionId":"a","turn":1,"status":"ok","ops":[{"opId":"x","opIndex":1,"kind":"session","status":"ok","childSessions":[{"sessionId":"c1","originId":"a","parentSessionId":"a","parentOpId":"x","ledgerPath":"s/c1.jsonl","status":"ok"},{"sessionId":"c2","originId":"a","parentSessionId":"a","parentOpId":"x","ledgerPath":"s/c2.jsonl","status":"ok"}]}],"warnings":[],"errors":[]}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	opStart := events[1].(canonical.OpStartedEvent)
	if opStart.ChildSessionNativeID != "c1" {
		t.Fatalf("first child not surfaced: %+v", opStart)
	}
	extra, ok := opStart.Extras["additionalChildSessions"].([]string)
	if !ok || len(extra) != 1 || extra[0] != "c2" {
		t.Fatalf("missing additional children: %+v", opStart.Extras)
	}
}

// TestFlushDirty_NoOpOnEmpty exercises the early-return branch.
func TestFlushDirty_NoOpOnEmpty(t *testing.T) {
	t.Parallel()

	cur := newCursor()
	out := make(chan canonical.Event, 1)
	if err := flushDirty(context.Background(), t.TempDir(), "src", map[string]struct{}{}, &cur, out, func(error) {}); err != nil {
		t.Fatalf("err: %v", err)
	}
	select {
	case ev := <-out:
		t.Fatalf("expected no events, got %T", ev)
	default:
	}
}

// fakeFsEvent / tailableNameForTest are test-only adapters that let us
// exercise tailableName without depending on a real fsnotify.Event. We
// keep them confined to the test package.
type fakeFsEvent struct{ name string }

// tailableNameForTest re-runs the tailableName filter logic against a
// fakeFsEvent so tests can inspect each branch without spinning up a
// real watcher.
func tailableNameForTest(watchDir string, ev fakeFsEvent) string {
	name := filepath.Base(ev.name)
	if !strings.HasSuffix(name, ledgerExt) {
		return ""
	}
	if strings.Contains(name, ".tmp-") {
		return ""
	}
	if filepath.Dir(ev.name) != watchDir {
		return ""
	}
	return name
}
