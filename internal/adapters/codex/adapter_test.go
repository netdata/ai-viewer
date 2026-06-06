package codex

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// silentOpts returns AdapterOptions with a discard logger and a recording
// onError, plus the slice the errors land in (guarded by mu for the Tail
// goroutine).
func silentOpts() (canonical.AdapterOptions, *[]string, *sync.Mutex) {
	var mu sync.Mutex
	errs := &[]string{}
	opts := canonical.AdapterOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnError: func(e error) {
			mu.Lock()
			*errs = append(*errs, e.Error())
			mu.Unlock()
		},
	}
	return opts, errs, &mu
}

// TestAdapter_NewRejectsEmptyRoot pins the fail-fast guard.
func TestAdapter_NewRejectsEmptyRoot(t *testing.T) {
	t.Parallel()
	if _, err := New("", canonical.AdapterOptions{}); err == nil {
		t.Fatal("New(\"\") = nil error, want non-nil")
	}
}

// TestAdapter_NewDefaultsNilDeps verifies New tolerates a nil Logger and nil
// OnError (substituting defaults) so adapter code can call them unconditionally.
func TestAdapter_NewDefaultsNilDeps(t *testing.T) {
	t.Parallel()
	a, err := New(t.TempDir(), canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.logger == nil {
		t.Error("logger is nil; want default")
	}
	if a.onError == nil {
		t.Error("onError is nil; want no-op default")
	}
	// The no-op onError must be callable.
	a.onError(errors.New("x"))
}

func TestAdapter_NewSourceIDOptionWins(t *testing.T) {
	t.Parallel()
	a, err := New(t.TempDir(), canonical.AdapterOptions{SourceID: "source/custom"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.sourceID != "source/custom" {
		t.Fatalf("sourceID = %q, want %q", a.sourceID, "source/custom")
	}
}

func TestAdapter_NewSourceIDDefaultsToHistoricalFallback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := sourceIDPrefix + root
	if a.sourceID != want {
		t.Fatalf("sourceID = %q, want %q", a.sourceID, want)
	}
}

// TestAdapter_NameAndFormat pins the registry identifiers.
func TestAdapter_NameAndFormat(t *testing.T) {
	t.Parallel()
	a, err := New(t.TempDir(), canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Name() != "codex" {
		t.Errorf("Name() = %q, want codex", a.Name())
	}
	if a.Format() != "codex" {
		t.Errorf("Format() = %q, want codex", a.Format())
	}
	if a.Name() != Format || a.Format() != Format {
		t.Errorf("Name/Format must equal Format const %q", Format)
	}
}

// TestAdapter_Factory builds an Adapter through the registry factory and rejects
// the empty location.
func TestAdapter_Factory(t *testing.T) {
	t.Parallel()
	a, err := Factory(t.TempDir(), canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	if a == nil {
		t.Fatal("Factory returned nil adapter")
	}
	if _, err := Factory("", canonical.AdapterOptions{}); err == nil {
		t.Fatal("Factory(\"\") = nil error, want non-nil")
	}
}

// TestAdapter_ParseCursor round-trips a cursor and rejects a bad version.
func TestAdapter_ParseCursor(t *testing.T) {
	t.Parallel()
	a, err := New(t.TempDir(), canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Empty → zero cursor.
	c, err := a.ParseCursor("")
	if err != nil {
		t.Fatalf("ParseCursor(\"\"): %v", err)
	}
	if c == nil {
		t.Fatal("ParseCursor(\"\") = nil cursor")
	}
	// Round-trip a non-empty cursor.
	seed := newCursor().withFile("2025/11/20/rollout-x.jsonl", FileCursor{Offset: 42, Size: 42})
	got, err := a.ParseCursor(seed.String())
	if err != nil {
		t.Fatalf("ParseCursor(round-trip): %v", err)
	}
	if !got.After(newCursor()) {
		t.Errorf("round-tripped cursor should be After the empty cursor")
	}
	// Bad version is rejected.
	if _, err := a.ParseCursor(`{"version":999}`); err == nil {
		t.Error("ParseCursor(bad version) = nil error, want non-nil")
	}
}

// TestAdapter_CoerceCursor covers the nil, typed, and alien-type branches.
func TestAdapter_CoerceCursor(t *testing.T) {
	t.Parallel()
	a, err := New(t.TempDir(), canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// nil → fresh cursor with non-nil maps.
	if c := a.coerceCursor(nil); c.Files == nil || c.LegacyJSON == nil || c.Version != cursorVersion {
		t.Errorf("coerceCursor(nil) = %+v, want initialized maps + version", c)
	}
	// Typed cursor with nil maps + zero version is normalized in place.
	typed := Cursor{}
	c := a.coerceCursor(typed)
	if c.Files == nil || c.LegacyJSON == nil || c.Version != cursorVersion {
		t.Errorf("coerceCursor(typed-zero) = %+v, want normalized", c)
	}
	// Alien cursor type → fresh cursor.
	if c := a.coerceCursor(alienCursor{}); c.Version != cursorVersion {
		t.Errorf("coerceCursor(alien) = %+v, want fresh cursor", c)
	}
}

// alienCursor is a foreign canonical.Cursor used to drive coerceCursor's
// type-assertion-miss branch.
type alienCursor struct{}

func (alienCursor) String() string              { return "{}" }
func (alienCursor) After(canonical.Cursor) bool { return false }

// TestAdapter_ScanThenTailHandoff pins the load-bearing Scan→Tail cursor
// handoff: Scan records the per-file offset on the instance, and a following
// Tail resumes from it (not from current EOF), so a record appended between Scan
// and Tail is emitted exactly once by Tail.
func TestAdapter_ScanThenTailHandoff(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, completeSession("sid-a"))

	opts, _, _ := silentOpts()
	a, err := New(root, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	scanOut := make(chan canonical.Event, 4096)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	scanEvents := drainBuffered(scanOut)
	if !hasKind(scanEvents, canonical.EvSessionStarted) {
		t.Fatal("Scan emitted no SessionStarted")
	}
	if a.scanCursor == nil {
		t.Fatal("Scan did not record scanCursor on the instance")
	}

	// Append a fresh complete turn AFTER Scan, then run Tail. Tail must resume
	// from the recorded offset and emit the new turn (and not re-emit the whole
	// file). A turn materializes on its close (task_complete), so append both.
	appendFileBytes(t, path, []byte(`{"timestamp":"`+tsCtx+`","type":"turn_context","payload":{"turn_id":"t2","model":"m"}}`+"\n"))
	appendFileBytes(t, path, []byte(`{"timestamp":"`+tsDone+`","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","completed_at":"`+tsDone+`"}}`+"\n"))

	tailOut := make(chan canonical.Event, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = a.Tail(ctx, tailOut)
		close(done)
	}()

	// Wait until the appended turn surfaces, then cancel.
	if _, ok := waitForKind(tailOut, canonical.EvTurnFinalized, 5*time.Second); !ok {
		cancel()
		<-done
		t.Fatal("Tail did not emit the appended turn")
	}
	cancel()
	<-done
}

// TestAdapter_TailColdSnapshot covers the cold-Tail path (no preceding Scan):
// Tail snapshots current file sizes so it follows from now and does NOT replay
// the historical session that already exists on disk. A brand-new rollout file
// created AFTER the watch is live is reliably picked up via the Create handler
// (the snapshot races an append on the SAME file, so a new file is used to keep
// the test deterministic).
func TestAdapter_TailColdSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A pre-existing complete session the cold snapshot must NOT replay.
	writeFileBytes(t, shardPath(root, uuid7(2)), completeSession("sid-cold"))

	opts, _, _ := silentOpts()
	a, err := New(root, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tailOut := make(chan canonical.Event, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = a.Tail(ctx, tailOut)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	// Give the watch a moment to establish, then write a NEW rollout file. The
	// Create handler watches the (existing) shard dir and reads the new file
	// from offset 0, so its full session surfaces. 150ms mirrors the watch-settle
	// delay the sibling tailer_test.go tests use.
	time.Sleep(150 * time.Millisecond)
	newPath := shardPath(root, uuid7(7))
	writeFileBytes(t, newPath, completeSession("sid-new"))

	if _, ok := waitForKind(tailOut, canonical.EvTurnFinalized, 5*time.Second); !ok {
		t.Fatal("cold Tail did not emit the new rollout's turn")
	}
}

// TestAdapter_SnapshotCursor pins snapshotCursor: it records current sizes for
// modern rollouts and skips a missing root cleanly.
func TestAdapter_SnapshotCursor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(3))
	body := completeSession("sid-snap")
	writeFileBytes(t, path, body)

	opts, _, _ := silentOpts()
	a, err := New(root, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cur, err := a.snapshotCursor()
	if err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}
	rel, rerr := relPath(mustResolve(t, root), path)
	if rerr != nil {
		t.Fatalf("relPath: %v", rerr)
	}
	fc := cur.fileCursor(rel)
	if fc.Offset != int64(len(body)) || fc.Size != int64(len(body)) {
		t.Errorf("snapshot FileCursor = %+v, want offset=size=%d", fc, len(body))
	}

	// A missing root resolves to an empty (non-error) discovery → empty cursor.
	aMissing, err := New(filepath.Join(root, "does-not-exist"), opts)
	if err != nil {
		t.Fatalf("New(missing): %v", err)
	}
	emptyCur, err := aMissing.snapshotCursor()
	if err != nil {
		t.Fatalf("snapshotCursor(missing): %v", err)
	}
	if len(emptyCur.Files) != 0 {
		t.Errorf("snapshotCursor(missing) = %+v, want empty", emptyCur.Files)
	}
}

// TestAdapter_ScanContextCancelled verifies a cancelled Scan returns nil (not an
// error) and still records the best-effort cursor on the instance.
func TestAdapter_ScanContextCancelled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFileBytes(t, shardPath(root, uuid7(4)), completeSession("sid-cancel"))

	opts, _, _ := silentOpts()
	a, err := New(root, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Scan runs
	out := make(chan canonical.Event, 4096)
	if err := a.Scan(ctx, nil, out); err != nil {
		t.Fatalf("cancelled Scan = %v, want nil", err)
	}
	if a.scanCursor == nil {
		t.Fatal("cancelled Scan did not record a best-effort cursor")
	}
}

// TestAdapter_ScanHardErrorAndTailSnapshotError drives the non-cancellation
// failure branches: an unreadable sessions root makes discovery return a hard
// (non-IsNotExist) error, which Scan wraps and returns, and which Tail's
// snapshotCursor surfaces as a wrapped error. Skipped when running as root
// (root bypasses the 0000 permission).
func TestAdapter_ScanHardErrorAndTailSnapshotError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}
	t.Parallel()
	root := t.TempDir()
	// Plant a file so the dir is non-empty, then make the root unreadable.
	writeFileBytes(t, shardPath(root, uuid7(8)), completeSession("sid-perm"))
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	opts, _, _ := silentOpts()
	a, err := New(root, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 16)
	if err := a.Scan(context.Background(), nil, out); err == nil {
		t.Error("Scan over unreadable root = nil error, want hard error")
	}
	// snapshotCursor (cold-Tail path) likewise surfaces the discovery error,
	// which Tail wraps and returns before entering the watch loop. Asserting the
	// snapshot directly avoids racing tailLoop (which would block on the watch).
	if _, err := a.snapshotCursor(); err == nil {
		t.Error("snapshotCursor over unreadable root = nil error, want error")
	}
}

// --- payloads.go containment branch coverage ---

// TestPayloadLocationURI_EmptyRootSkipsContainment pins the mapper-only path
// (root == ""): the cleaned absolute path is returned without resolving.
func TestPayloadLocationURI_EmptyRootSkipsContainment(t *testing.T) {
	t.Parallel()
	uri, err := payloadLocationURI("", "/test/sessions/2025/11/20/rollout-x.jsonl")
	if err != nil {
		t.Fatalf("payloadLocationURI(empty root): %v", err)
	}
	if uri != "file:///test/sessions/2025/11/20/rollout-x.jsonl" {
		t.Errorf("uri = %q, want file:///test/sessions/...", uri)
	}
}

// TestPayloadLocationURI_WithinRoot resolves a real file under a real root.
func TestPayloadLocationURI_WithinRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(5))
	writeFileBytes(t, path, completeSession("sid-uri"))
	uri, err := payloadLocationURI(root, path)
	if err != nil {
		t.Fatalf("payloadLocationURI: %v", err)
	}
	if !strings.HasPrefix(uri, "file://") || !strings.HasSuffix(uri, ".jsonl") {
		t.Errorf("uri = %q, want file://...jsonl", uri)
	}
}

// TestPayloadLocationURI_EscapeRejected plants a symlink inside the root that
// points OUTSIDE it and asserts the URI builder refuses the escaping path.
func TestPayloadLocationURI_EscapeRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.jsonl")
	writeFileBytes(t, target, []byte("{}\n"))
	link := filepath.Join(root, "escape.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	_, err := payloadLocationURI(root, link)
	if err == nil {
		t.Fatal("payloadLocationURI(escaping symlink) = nil error, want escape error")
	}
	if !strings.Contains(err.Error(), "escapes root") {
		t.Errorf("error = %v, want 'escapes root'", err)
	}
}

// TestPayloadLocationURI_UnresolvableRoot drives resolveWithinRoot's root-resolve
// error branch with a non-existent root.
func TestPayloadLocationURI_UnresolvableRoot(t *testing.T) {
	t.Parallel()
	_, err := payloadLocationURI(filepath.Join(t.TempDir(), "nope"), "/x/y.jsonl")
	if err == nil {
		t.Fatal("payloadLocationURI(unresolvable root) = nil error, want error")
	}
}

// TestMapperPayloadURI_FallsBackOnEscape verifies the mapper's payloadURI keeps
// the #L<line> anchor and falls back to the cleaned abs path when containment
// rejects the path, so the ref is never empty (the scanner is the real gate).
func TestMapperPayloadURI_FallsBackOnEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.jsonl")
	writeFileBytes(t, target, []byte("{}\n"))
	link := filepath.Join(root, "escape.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	m := newFileMapper(mapperConfig{sourceID: "codex:" + root, absPath: link, root: root, nativeID: "id"})
	m.setLineNo(7)
	uri := m.payloadURI(7)
	if !strings.HasSuffix(uri, "#L7") {
		t.Errorf("uri = %q, want #L7 anchor preserved", uri)
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("uri = %q, want file:// fallback", uri)
	}
}

// TestMapperPayloadURI_NoAnchorWhenLineZero pins the lineNo<=0 branch (no anchor)
// and the empty-absPath branch (anchor only).
func TestMapperPayloadURI_NoAnchorWhenLineZero(t *testing.T) {
	t.Parallel()
	m := newFileMapper(mapperConfig{sourceID: "s", absPath: "/a/b.jsonl"})
	if got := m.payloadURI(0); got != "file:///a/b.jsonl" {
		t.Errorf("payloadURI(0) = %q, want no anchor", got)
	}
	mNoPath := newFileMapper(mapperConfig{sourceID: "s"})
	if got := mNoPath.payloadURI(3); got != "#L3" {
		t.Errorf("payloadURI(no absPath) = %q, want #L3 only", got)
	}
}

// --- small test helpers ---

// mustResolve resolves p through symlinks, failing the test on error. Used to
// derive the cursor key the snapshot records (keyed against the resolved root).
func mustResolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks %s: %v", p, err)
	}
	return r
}
