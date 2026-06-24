package claude_code

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

func TestReadTranscript_ReplayAtEOFRebuildsParentAgentOpNoEvents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)

	parentPath := filepath.Join(root, "-home-user-x", durParentSession+".jsonl")
	info := statFile(t, parentPath)
	mm := metaMap{toolUseToAgent: map[string]string{parentAgentToolUseID: durAgentID}}
	out := make(chan canonical.Event, 16)
	tr := transcript{
		rel:      "-home-user-x/" + durParentSession + ".jsonl",
		abs:      parentPath,
		nativeID: durParentSession,
		kind:     canonical.KindRoot,
	}

	cur, emitted, mapper, err := readTranscript(context.Background(), root, tr, "src", mm, eofCursor(info.Size()), out, testOnError(t))
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	assertNoReplayEvents(t, emitted, out)
	assertCursorAtSize(t, cur, info.Size())
	childID := childNativeID(durParentSession, durAgentID)
	assertAgentReplayRef(t, mapper, childID)
}

func TestReadTranscript_ReplayAtEOFDoesNotMarkChildComplete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n") + "\n"
	writeFileBytes(t, childPath(root), []byte(body))

	info := statFile(t, childPath(root))
	out := make(chan canonical.Event, 16)
	tr := transcript{
		rel:            "-home-user-x/" + durParentSession + "/subagents/agent-" + durAgentID + ".jsonl",
		abs:            childPath(root),
		nativeID:       childNativeID(durParentSession, durAgentID),
		parentNativeID: durParentSession,
		kind:           canonical.KindSubAgent,
	}

	_, emitted, mapper, err := readTranscript(context.Background(), root, tr, "src", metaMap{}, eofCursor(info.Size()), out, testOnError(t))
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	wantTs, _ := parseTsToMicros("2026-05-26T10:00:09.000Z")
	assertNoReplayEvents(t, emitted, out)
	assertChildReplayState(t, mapper, wantTs)
	completed := map[string]completionState{}
	collectAgentDeferral(mapper, tr, map[string]agentOpFinalize{}, completed, map[string]struct{}{})
	if len(completed) != 0 {
		t.Fatalf("EOF replay marked child complete despite emit gate: %+v", completed)
	}
}

func TestReadOneLineOversizedCompleteLine(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", scanBufferMax+1024) + "\n"
	line, consumed, err := readOneLine(bufio.NewReaderSize(strings.NewReader(body), 64*1024))

	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("readOneLine err = %v, want errLineTooLong", err)
	}
	if line != nil {
		t.Fatalf("readOneLine line length = %d, want nil", len(line))
	}
	if consumed != int64(len(body)) {
		t.Fatalf("readOneLine consumed = %d, want %d", consumed, len(body))
	}
}

func TestReadOneLineOversizedPartialEOFHoldsBack(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", scanBufferMax+128*1024)
	line, consumed, err := readOneLine(bufio.NewReaderSize(strings.NewReader(body), 64*1024))

	if !errors.Is(err, io.EOF) {
		t.Fatalf("readOneLine err = %v, want io.EOF", err)
	}
	if line != nil {
		t.Fatalf("readOneLine line length = %d, want nil", len(line))
	}
	if consumed != 0 {
		t.Fatalf("readOneLine consumed = %d, want 0", consumed)
	}
}

func TestReadOneLinePropagatesUnderlyingReadError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("synthetic read failure")
	body := strings.Repeat("x", scanBufferMax+1024)
	reader := failingReader{body: body, err: wantErr}
	line, consumed, err := readOneLine(bufio.NewReaderSize(&reader, scanBufferMax+2048))

	if !errors.Is(err, wantErr) {
		t.Fatalf("readOneLine err = %v, want %v", err, wantErr)
	}
	if line != nil {
		t.Fatalf("readOneLine line length = %d, want nil", len(line))
	}
	if consumed != 0 {
		t.Fatalf("readOneLine consumed = %d, want 0", consumed)
	}
}

func TestScan_OrphanRootUsesEarliestParseableChildTimestamp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	subDir := filepath.Join(root, "-home-user-old", "orphan-sess", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-late000late111.jsonl"), []byte(strings.Join([]string{
		`{"type":"assistant","malformed"`,
		`{"type":"summary","summary":"ignored leading no-op"}`,
		orphanChildLine("late000late111", "lu1", "2026-02-10T09:05:00.000Z"),
	}, "\n")+"\n"))
	writeFileBytes(t, filepath.Join(subDir, "agent-early000early1.jsonl"), []byte(strings.Join([]string{
		`not-json`,
		`{"type":"task-summary","summary":"ignored leading no-op"}`,
		orphanChildLine("early000early1", "eu1", "2026-02-10T09:00:00.000Z"),
	}, "\n")+"\n"))

	events, _ := collectErrors(t, root)
	orphan, ok := sessionStartedByNative(events, "orphan-sess")
	if !ok {
		t.Fatal("orphan root session not synthesized")
	}
	wantTs, _ := parseTsToMicros("2026-02-10T09:00:00.000Z")
	if orphan.Ts != wantTs {
		t.Fatalf("orphan root Ts = %d, want earliest parseable child ts %d", orphan.Ts, wantTs)
	}
}

func TestScan_OrphanRootIgnoresTimestamplessChildWhenTimestampedChildExists(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	subDir := filepath.Join(root, "-home-user-old", "orphan-sess", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-empty000empty1.jsonl"), []byte(strings.Join([]string{
		`{"type":"summary","summary":"ignored no-op"}`,
		`{"type":"user","uuid":"eu1","isSidechain":true,"agentId":"empty000empty1","sessionId":"orphan-sess","message":{"role":"user","content":"task"}}`,
	}, "\n")+"\n"))
	writeFileBytes(t, filepath.Join(subDir, "agent-timed000timed1.jsonl"), []byte(orphanChildLine(
		"timed000timed1", "tu1", "2026-02-10T09:07:00.000Z")+"\n"))

	events, errs := collectErrors(t, root)
	if len(errs) != 0 {
		t.Fatalf("unexpected SourceErrors: %v", errs)
	}
	orphan, ok := sessionStartedByNative(events, "orphan-sess")
	if !ok {
		t.Fatal("orphan root session not synthesized")
	}
	wantTs, _ := parseTsToMicros("2026-02-10T09:07:00.000Z")
	if orphan.Ts != wantTs {
		t.Fatalf("orphan root Ts = %d, want timestamped child ts %d", orphan.Ts, wantTs)
	}
}

func TestScan_OrphanRootSkipsOversizedLeadingChildLineForTimestamp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	subDir := filepath.Join(root, "-home-user-old", "orphan-sess", "subagents")
	oversized := strings.Repeat("x", scanBufferMax+1024)
	writeFileBytes(t, filepath.Join(subDir, "agent-timed000timed1.jsonl"), []byte(strings.Join([]string{
		oversized,
		orphanChildLine("timed000timed1", "tu1", "2026-02-10T09:07:00.000Z"),
	}, "\n")+"\n"))

	events, _ := collectErrors(t, root)
	orphan, ok := sessionStartedByNative(events, "orphan-sess")
	if !ok {
		t.Fatal("orphan root session not synthesized")
	}
	wantTs, _ := parseTsToMicros("2026-02-10T09:07:00.000Z")
	if orphan.Ts != wantTs {
		t.Fatalf("orphan root Ts = %d, want timestamp after oversized child line %d", orphan.Ts, wantTs)
	}
}

func TestScan_OrphanRootUsesZeroTimestampWithoutParseableChildTimestamp(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("x", scanBufferMax+1024)
	cases := []struct {
		name string
		body string
	}{
		{
			name: "all oversized lines",
			body: strings.Join([]string{oversized, oversized}, "\n") + "\n",
		},
		{
			name: "empty transcript",
			body: "",
		},
		{
			name: "timestampless records",
			body: strings.Join([]string{
				`{"type":"summary","summary":"ignored no-op"}`,
				`{"type":"user","uuid":"tu1","isSidechain":true,"agentId":"empty000empty1","sessionId":"orphan-sess","message":{"role":"user","content":"task"}}`,
			}, "\n") + "\n",
		},
		{
			name: "non-positive timestamps",
			body: strings.Join([]string{
				orphanChildLine("empty000empty1", "nu1", "1969-12-31T23:59:59.000Z"),
				orphanChildLine("empty000empty1", "zu1", "1970-01-01T00:00:00.000Z"),
			}, "\n") + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			subDir := filepath.Join(root, "-home-user-old", "orphan-sess", "subagents")
			writeFileBytes(t, filepath.Join(subDir, "agent-empty000empty1.jsonl"), []byte(tc.body))

			events, _ := collectErrors(t, root)
			assertOrphanRootTs(t, events, 0)
		})
	}
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func eofCursor(size int64) FileCursor {
	return FileCursor{Offset: size, Size: size}
}

type failingReader struct {
	body string
	err  error
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.body), r.err
}

func assertNoReplayEvents(t *testing.T, emitted int, out chan canonical.Event) {
	t.Helper()
	if emitted != 0 {
		t.Fatalf("EOF replay emitted %d events", emitted)
	}
	if got := len(drainBuffered(out)); got != 0 {
		t.Fatalf("EOF replay delivered %d events", got)
	}
}

func assertCursorAtSize(t *testing.T, cur FileCursor, size int64) {
	t.Helper()
	if cur.Offset != size {
		t.Fatalf("cursor offset = %d, want %d", cur.Offset, size)
	}
	if cur.Size != size {
		t.Fatalf("cursor size = %d, want %d", cur.Size, size)
	}
}

func assertAgentReplayRef(t *testing.T, mapper *fileMapper, childID string) {
	t.Helper()
	ref, ok := mapper.agentOps[childID]
	if !ok {
		t.Fatalf("agent op replay ref for %q missing", childID)
	}
	if ref.turnSeq != 1 {
		t.Fatalf("agent op replay turn = %d, want 1", ref.turnSeq)
	}
	if ref.opSeq != parentAgentOpSeq {
		t.Fatalf("agent op replay op = %d, want %d", ref.opSeq, parentAgentOpSeq)
	}
}

func assertChildReplayState(t *testing.T, mapper *fileMapper, wantTs int64) {
	t.Helper()
	if !mapper.fullyRead {
		t.Fatal("child replay did not mark the transcript fully read")
	}
	if !mapper.lastRecordAssistantText {
		t.Fatal("child replay did not rebuild terminal assistant-text state")
	}
	if mapper.lastRecordEmitted {
		t.Fatal("child replay marked the terminal record as newly emitted")
	}
	if mapper.lastAssistantTextTsUs != wantTs {
		t.Fatalf("last assistant-text ts = %d, want %d", mapper.lastAssistantTextTsUs, wantTs)
	}
}

func assertOrphanRootTs(t *testing.T, events []canonical.Event, want int64) {
	t.Helper()
	orphan, ok := sessionStartedByNative(events, "orphan-sess")
	if !ok {
		t.Fatal("orphan root session not synthesized")
	}
	if orphan.Ts != want {
		t.Fatalf("orphan root Ts = %d, want %d", orphan.Ts, want)
	}
}

func testOnError(t *testing.T) func(error) {
	t.Helper()
	return func(err error) {
		t.Fatalf("unexpected SourceError: %v", err)
	}
}

func orphanChildLine(agentID, uuid, ts string) string {
	return `{"type":"user","uuid":"` + uuid + `","isSidechain":true,"agentId":"` + agentID + `","sessionId":"orphan-sess","message":{"role":"user","content":"task"},"timestamp":"` + ts + `"}`
}
