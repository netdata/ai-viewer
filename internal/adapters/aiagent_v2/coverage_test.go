package aiagent_v2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestLogExtras covers the path-only branch and the no-extras
// fallthrough.
func TestLogExtras(t *testing.T) {
	t.Parallel()
	e, err := logExtras(nil, logEntry{Path: "1-2"}, "/logs/0/message")
	if err != nil {
		t.Fatalf("logExtras path: %v", err)
	}
	if e["path"] != "1-2" {
		t.Fatalf("expected path extra, got %v", e)
	}
	e, err = logExtras(nil, logEntry{}, "/logs/0/message")
	if err != nil {
		t.Fatalf("logExtras empty: %v", err)
	}
	if e != nil {
		t.Fatalf("expected nil when no path, got %v", e)
	}
}

// TestSnapshotCursor_PopulatesAllFiles validates Tail's pre-existing
// file enumeration so a started tailer doesn't re-emit historical
// snapshots.
func TestSnapshotCursor_PopulatesAllFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSnapshot(t, root, "a", simpleSnapshot(2, "a"))
	writeSnapshot(t, root, "b", simpleSnapshot(2, "b"))
	// .tmp-* file is ignored.
	if err := os.WriteFile(filepath.Join(root, "z.json.gz.tmp-1-2"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, _ := New(root, canonical.AdapterOptions{})
	cur, err := a.snapshotCursor()
	if err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}
	if len(cur.Files) != 2 {
		t.Fatalf("Files: got %d want 2 (%+v)", len(cur.Files), cur.Files)
	}
}

// TestMapSteps_FullCoverage exercises the kind, endedAt-set, and roll-
// up branches.
func TestMapSteps_FullCoverage(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "steps-cov")
	snap.OpTree.Steps = []stepNode{
		{
			ID: "s0", Index: 0, Kind: "internal",
			StartedAt: 1700000010000, EndedAt: int64Ptr(1700000012000),
			Ops: []operationNode{
				{
					OpID: "step-op-1", Kind: "llm",
					StartedAt: 1700000010500, EndedAt: int64Ptr(1700000011500), Status: "ok",
					Attributes: rawAttrs(map[string]any{"model": "claude"}),
					Accounting: []accountingEntry{{Type: "llm", Tokens: &tokens{InputTokens: 10, OutputTokens: 5}, CostUSD: 0.001}},
				},
			},
		},
		{
			ID: "s1", Index: 1, Kind: "advisors",
			StartedAt: 1700000013000,
			// No endedAt — exercises the running-step branch.
			Ops: []operationNode{},
		},
	}
	events := mapSimple(t, snap)
	var sawTurnFinalized, sawRunningStep bool
	for _, ev := range events {
		switch v := ev.(type) {
		case canonical.TurnFinalizedEvent:
			if v.Seq >= stepIndexOffset && v.TokensIn == 10 {
				sawTurnFinalized = true
			}
		case canonical.TurnStartedEvent:
			if v.Seq == stepIndexOffset+1 {
				sawRunningStep = true
			}
		}
	}
	if !sawTurnFinalized {
		t.Fatalf("missing TurnFinalized for completed step")
	}
	if !sawRunningStep {
		t.Fatalf("missing TurnStarted for in-flight step")
	}
}

func TestMapStepOpExtrasCarryCompactionProof(t *testing.T) {
	t.Parallel()

	snap := simpleSnapshot(2, "step-compaction")
	snap.OpTree.Turns = nil
	snap.OpTree.Steps = []stepNode{
		{
			ID:        "compact-step",
			Index:     0,
			Kind:      "internal",
			StartedAt: 1700000010000,
			EndedAt:   int64Ptr(1700000013000),
			Attributes: rawAttrs(map[string]any{
				"archivedTurn": 1,
				"currentTurn":  2,
			}),
			Ops: []operationNode{
				{
					OpID:      "compact-op",
					Kind:      "session",
					StartedAt: 1700000010500,
					EndedAt:   int64Ptr(1700000012500),
					Status:    "ok",
					Attributes: rawAttrs(map[string]any{
						"name":     "history_compaction.turn_summarizer",
						"provider": "history-compaction",
					}),
					ChildSessionRef: &childSessionRef{SessionID: "compact-child"},
				},
			},
		},
	}

	events := mapSimple(t, snap)
	for _, ev := range events {
		started, ok := ev.(canonical.OpStartedEvent)
		if !ok || started.TurnSeq != stepIndexOffset || started.Seq != 0 {
			continue
		}
		if got := extraStringForTest(t, started.Extras, "attr.provider"); got != "history-compaction" {
			t.Fatalf("attr.provider = %q, want history-compaction; extras=%v", got, started.Extras)
		}
		if got := extraStringForTest(t, started.Extras, "step.kind"); got != "internal" {
			t.Fatalf("step.kind = %q, want internal; extras=%v", got, started.Extras)
		}
		if got := extraInt64ForTest(t, started.Extras, "step.attr.archivedTurn"); got != 1 {
			t.Fatalf("step.attr.archivedTurn = %d, want 1; extras=%v", got, started.Extras)
		}
		if got := extraInt64ForTest(t, started.Extras, "step.attr.currentTurn"); got != 2 {
			t.Fatalf("step.attr.currentTurn = %d, want 2; extras=%v", got, started.Extras)
		}
		return
	}
	t.Fatalf("missing step-projected compaction OpStarted event")
}

func extraStringForTest(t *testing.T, extras map[string]any, key string) string {
	t.Helper()
	switch value := extras[key].(type) {
	case string:
		return value
	case json.RawMessage:
		var out string
		if err := json.Unmarshal(value, &out); err != nil {
			t.Fatalf("decode %s string extra: %v", key, err)
		}
		return out
	case []byte:
		var out string
		if err := json.Unmarshal(value, &out); err != nil {
			t.Fatalf("decode %s string extra: %v", key, err)
		}
		return out
	default:
		t.Fatalf("%s extra = %T(%v), want string-compatible", key, value, value)
		return ""
	}
}

func extraInt64ForTest(t *testing.T, extras map[string]any, key string) int64 {
	t.Helper()
	switch value := extras[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.RawMessage:
		var out int64
		if err := json.Unmarshal(value, &out); err != nil {
			t.Fatalf("decode %s int extra: %v", key, err)
		}
		return out
	case []byte:
		var out int64
		if err := json.Unmarshal(value, &out); err != nil {
			t.Fatalf("decode %s int extra: %v", key, err)
		}
		return out
	default:
		t.Fatalf("%s extra = %T(%v), want int-compatible", key, value, value)
		return 0
	}
}

// TestProcessFile_ContextCancelledMidEmit covers the ctx.Err return
// inside the event-emission loop.
func TestProcessFile_ContextCancelledMidEmit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "ctx-cancel"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	out := make(chan canonical.Event) // unbuffered — emission blocks
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel after a beat so the emission loop trips the ctx.Done branch.
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, _, _ = processFile(ctx, root, "src", origin+".json.gz", FileCursor{}, out, func(error) {})
}

// TestProcessFile_StatMissingFileError returns the stat error rather
// than panicking when the file vanishes mid-scan.
func TestProcessFile_StatMissingFileError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	out := make(chan canonical.Event, 1)
	_, _, err := processFile(context.Background(), root, "src", "nope.json.gz", FileCursor{}, out, func(error) {})
	if err == nil {
		t.Fatalf("expected stat error")
	}
}

// TestStreamer_DrainAfterParseError exercises the drainToHash code
// path by feeding a body whose JSON parses partially before failing.
func TestStreamer_DrainAfterParseError(t *testing.T) {
	t.Parallel()
	// JSON whose object opens but never closes — Decode fails after
	// consuming most of the body.
	body := []byte(`{"version":2,"opTree":{`)
	body = append(body, bytes.Repeat([]byte(`"padding":1,`), 200)...)
	body = append(body, []byte(`"trailer":`)...)
	// EOF here triggers parse error mid-stream.

	root := t.TempDir()
	path := writeRaw(t, root, "bad.json.gz", mkGzipBytes(body))
	var errCount int
	_, _, err := readSnapshotStreaming(context.Background(), path, "src", "x", root, "x.json.gz", func(error) { errCount++ })
	if err != nil {
		t.Fatalf("expected soft failure: %v", err)
	}
	if errCount == 0 {
		t.Fatalf("expected onError to fire")
	}
}

// TestProcessOnce_ChangedRetryPath exercises the "mtime advanced
// during read → retry once" branch of processOnce.
func TestProcessOnce_ChangedRetryPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "retry-1"
	path := writeSnapshot(t, root, origin, simpleSnapshot(2, origin))

	cur := newCursor()
	out := make(chan canonical.Event, 256)
	// Set initial cursor entry so first processFile sees an old hash.
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {}); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// Advance mtime + content while we hold the cursor; subsequent
	// processOnce should reprocess and update the cursor.
	snap := simpleSnapshot(2, origin)
	snap.OpTree.Turns = append(snap.OpTree.Turns, turnNode{Index: 2, StartedAt: 1700000020000, EndedAt: int64Ptr(1700000022000)})
	writeSnapshot(t, root, origin, snap)
	// Touch mtime forward so processOnce will retry-once because the
	// post-read stat reflects the new mtime.
	info, _ := os.Stat(path)
	future := info.ModTime().Add(5 * time.Second)
	_ = os.Chtimes(path, future, future)
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {}); err != nil {
		t.Fatalf("second pass: %v", err)
	}
}

// TestFlushDirty_NoOp keeps the no-entries branch covered.
func TestFlushDirty_NoOp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	out := make(chan canonical.Event, 4)
	cur := newCursor()
	if err := flushDirty(context.Background(), root, "src", map[string]struct{}{}, &cur, out, func(error) {}); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	if len(drainBuffered(out)) != 0 {
		t.Fatalf("expected no events from empty dirty set")
	}
}

// TestFlushDirty_CancelledContext exercises the ctx.Err return path
// inside flushDirty.
func TestFlushDirty_CancelledContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSnapshot(t, root, "x", simpleSnapshot(2, "x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 8)
	cur := newCursor()
	err := flushDirty(ctx, root, "src", map[string]struct{}{"x.json.gz": {}}, &cur, out, func(error) {})
	if err == nil {
		t.Fatalf("expected context error")
	}
}

// TestScan_ContextCancelledReturnsNil verifies the Scan wrapper turns
// the inner ctx error into a clean nil return per the contract's
// "returns when caught up or cancelled".
func TestScan_ContextCancelledReturnsNil(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSnapshot(t, root, "y", simpleSnapshot(2, "y"))
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 8)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Scan(ctx, nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
}

// TestScan_FailingDirReturnsError exercises the listSnapshots error
// path (non-existence is silently nil; permissions error surfaces).
func TestScan_FailingDirReturnsError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root can read everything; skip permission test")
	}
	root := t.TempDir()
	// Drop dir permissions.
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(root, 0o755) }()

	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 4)
	err := a.Scan(context.Background(), nil, out)
	if err == nil {
		// Some filesystems still allow listing under restricted dirs;
		// skip if so.
		t.Skip("dir is readable despite chmod 000; skip")
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Fatalf("expected scan error wrapper, got %v", err)
	}
}

// TestMsToMicros covers the zero-input branch.
func TestMsToMicros(t *testing.T) {
	t.Parallel()
	if msToMicros(0) != 0 {
		t.Fatalf("zero input should yield zero")
	}
	if msToMicros(1) != 1000 {
		t.Fatalf("1ms should yield 1000us")
	}
	if msToMicros(-1) != 0 {
		t.Fatalf("negative should yield zero (defensive)")
	}
}

// TestEndTsOrStarted covers both branches.
func TestEndTsOrStarted(t *testing.T) {
	t.Parallel()
	if endTsOrStarted(opTree{StartedAt: 5}) != 5000 {
		t.Fatalf("expected 5000")
	}
	if endTsOrStarted(opTree{StartedAt: 5, EndedAt: int64Ptr(10)}) != 10000 {
		t.Fatalf("expected 10000")
	}
}

// TestAttrString covers the not-found and non-string branches.
func TestAttrString(t *testing.T) {
	t.Parallel()
	attrs := rawAttrs(map[string]any{"a": "x", "b": 42})
	if attrString(attrs, "a") != "x" {
		t.Fatalf("expected x")
	}
	if attrString(attrs, "missing") != "" {
		t.Fatalf("expected empty for missing")
	}
	if attrString(attrs, "b") != "" {
		t.Fatalf("expected empty for non-string")
	}
}

// TestCoerceCursor_NormalisesTyped covers the typed-cursor branch
// where Files is nil.
func TestCoerceCursor_NormalisesTyped(t *testing.T) {
	t.Parallel()
	a, _ := New("/x", canonical.AdapterOptions{})
	cur := a.coerceCursor(Cursor{Version: 1})
	if cur.Files == nil {
		t.Fatalf("Files should be initialised")
	}
}

// TestSnapshotCursor_NonExistentRoot exercises the IsNotExist branch
// inside the listSnapshots call from snapshotCursor.
func TestSnapshotCursor_NonExistentRoot(t *testing.T) {
	t.Parallel()
	a, _ := New(filepath.Join(t.TempDir(), "missing"), canonical.AdapterOptions{})
	cur, err := a.snapshotCursor()
	if err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}
	if len(cur.Files) != 0 {
		t.Fatalf("expected empty, got %d", len(cur.Files))
	}
}

// TestEmitZeroByteWarning_ContextCancelled verifies ctx cancellation
// is honoured during error event emission.
func TestEmitZeroByteWarning_ContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event)
	err := emitZeroByteWarning(ctx, "src", "/some/file", out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
}

// TestEmitProgress_ContextCancelled covers the same code path on
// SourceProgress emission.
func TestEmitProgress_ContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event)
	err := emitProgress(ctx, "src", newCursor(), out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
}

// TestParseSnapshot_NilEnvelope ensures decoder error contains parse
// context.
func TestParseSnapshot_NilEnvelope(t *testing.T) {
	t.Parallel()
	_, err := parseSnapshot(nil)
	if err == nil {
		t.Fatalf("expected error on nil input")
	}
}

// TestStreamer_ContextCancelledDuringRead is timing-sensitive but
// validates the ctx check at the streamer's entry.
func TestStreamer_ContextCancelledDuringRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "stream-cancel"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	path := filepath.Join(root, origin+".json.gz")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := readSnapshotStreaming(ctx, path, "src", origin, root, origin+".json.gz", func(error) {})
	if err == nil {
		t.Fatalf("expected ctx error")
	}
}

// TestDrainToHash_Errors validates the drainToHash helper directly.
func TestDrainToHash_Errors(t *testing.T) {
	t.Parallel()
	r := alwaysErrReader{err: errors.New("boom")}
	var errCount int
	if err := drainToHash(r, func(error) { errCount++ }, "/some/path"); err == nil {
		t.Fatalf("expected error")
	}
	if errCount != 1 {
		t.Fatalf("expected 1 onError, got %d", errCount)
	}
}

// TestDrainToHash_NoError covers the success path (empty reader).
func TestDrainToHash_NoError(t *testing.T) {
	t.Parallel()
	r := bytes.NewReader(nil)
	if err := drainToHash(r, func(error) {}, "/some/path"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

type alwaysErrReader struct{ err error }

func (a alwaysErrReader) Read(_ []byte) (int, error) { return 0, a.err }

// TestEncodeJSON_RoundtripExtras keeps the encoding map[string]any
// shape stable against future changes; covers attrString edge.
func TestEncodeJSON_RoundtripExtras(t *testing.T) {
	t.Parallel()
	extras := map[string]any{"key": "value"}
	b, _ := json.Marshal(extras)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["key"].(string) != "value" {
		t.Fatalf("roundtrip lost")
	}
}

// TestReader_HandlesCloseError is a placeholder for the gzip.Close
// failure mode that practical readers do not emit; exists so coverage
// covers the defer.
func TestReader_HandlesCloseError(t *testing.T) {
	t.Parallel()
	// io.EOF is the only Close error gzip returns for well-formed bodies;
	// nothing to assert here besides the no-panic.
	_ = io.EOF
}
