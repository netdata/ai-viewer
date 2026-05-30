package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestDiscover_UnreadableRootFatal asserts an unreadable sessions root (not
// absent) is a FATAL error (the source is broken), distinct from the benign
// absent-root case. Skipped on filesystems that allow descending a 0o000 dir.
func TestDiscover_UnreadableRootFatal(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(root, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, derr := os.ReadDir(root); derr == nil {
		t.Skip("filesystem allowed reading a 0o000 dir; fatal-root seam not exercised")
	}

	_, err := discoverRollouts(root, func(error) {})
	if err == nil {
		t.Fatal("unreadable root must be a fatal error")
	}
	if !strings.Contains(err.Error(), "read sessions root") {
		t.Errorf("fatal error = %v, want 'read sessions root'", err)
	}
}

// TestScan_DiscoverFatalPropagates asserts scanAll returns the fatal discovery
// error (does not swallow it).
func TestScan_DiscoverFatalPropagates(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(root, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, derr := os.ReadDir(root); derr == nil {
		t.Skip("filesystem allowed reading a 0o000 dir; fatal-root seam not exercised")
	}

	out := make(chan canonical.Event, 4)
	_, err := scanAll(context.Background(), root, "codex:"+root, newCursor(), out, func(error) {})
	if err == nil {
		t.Fatal("scanAll must propagate the fatal discovery error")
	}
}

// TestScan_ReadErrorContinues asserts a per-file open error (unreadable rollout)
// surfaces a SourceError and the scan CONTINUES with the remaining files
// (fail-soft), exercising scanAll's onError(rerr)+continue branch. Skipped on
// filesystems that ignore 0o000 file perms.
func TestScan_ReadErrorContinues(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	root := t.TempDir()
	bad := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T09-00-00-"+uuid7(1)+".jsonl")
	good := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T10-00-00-"+uuid7(2)+".jsonl")
	writeFileBytes(t, bad, completeSession("sid-bad"))
	writeFileBytes(t, good, completeSession("sid-good"))
	setMtime(t, good, time.Minute)
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })
	if f, oerr := os.Open(bad); oerr == nil { // #nosec G304 -- test probe
		_ = f.Close()
		t.Skip("filesystem allowed opening a 0o000 file; read-error seam not exercised")
	}

	events, errs, _ := scanCollect(t, root, "codex:"+root, newCursor())
	if !hasKind(events, canonical.EvSessionStarted) {
		t.Error("the good file must ingest while the bad one errors")
	}
	openErr := false
	for _, e := range errs {
		if strings.Contains(e, "open ") {
			openErr = true
		}
	}
	if !openErr {
		t.Errorf("unreadable rollout did not surface an open SourceError; errs=%v", errs)
	}
}

// TestScan_ProgressCheckpointMidWalk drives >progressEveryEvents events across
// several files so scanAll emits an intermediate SourceProgress (not only the
// final one), exercising the mid-walk checkpoint branch.
func TestScan_ProgressCheckpointMidWalk(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Each session emits ~5 events (SessionStarted, TurnStarted, user op trio,
	// TurnFinalized). 60 files * ~5 > 200 → at least one mid-walk checkpoint.
	for i := 0; i < 60; i++ {
		// Unique rel per file even when uuid7 repeats mod 100 (the seconds field
		// is distinct per i).
		path := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T10-00-"+pad2(i)+"-"+uuid7(i)+".jsonl")
		writeFileBytes(t, path, busySession("sid-"+pad2(i)))
		setMtime(t, path, time.Minute)
	}
	events, _, _ := scanCollect(t, root, "codex:"+root, newCursor())
	if countKind(events, canonical.EvSourceProgress) < 2 {
		t.Errorf("SourceProgress count = %d, want >= 2 (mid-walk + final)", countKind(events, canonical.EvSourceProgress))
	}
}

// busySession returns a session with a user message op so each file emits enough
// events to push the progress counter.
func busySession(id string) []byte {
	lines := []string{
		metaLine(id, `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"user_message","message":"hello there"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// pad2 zero-pads n to two digits for unique synthetic filenames.
func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestReadRollout_StaleFinalizeCancel covers readRollout's ctx.Done branch in
// the stale-finalize emit loop (scanner.go:357): a stale hanging file is fully
// streamed under a LIVE context (so the mapper holds an open turn), then the
// synthetic finalize's first emit BLOCKS on an unbuffered, undrained channel
// while a goroutine cancels the context — so the select picks ctx.Done.
func TestReadRollout_StaleFinalizeCancel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, hangingSession("sid-cancel"))
	setMtime(t, path, 2*time.Hour)
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan canonical.Event) // unbuffered + never drained → emit blocks
	r := rollout{rel: rel, abs: filepath.Join(resolved, filepath.FromSlash(rel))}

	// Cancel shortly after the call starts; streaming the 3-line file completes
	// quickly, then the finalize emit blocks and observes the cancellation.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, _, err := readRollout(ctx, resolved, r, "codex:"+root, FileCursor{}, out, func(error) {})
	if err == nil {
		t.Fatal("readRollout with ctx cancelled during finalize emit should return ctx err")
	}
}

// TestReadRollout_ProbeSeekAfterTruncationReset asserts the truncation reset +
// re-probe path: a cursor recording a larger size resets to 0, re-probes the
// (still-valid) session_meta, and re-emits.
func TestReadRollout_TruncationResetReemits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	path := shardPath(root, uuid7(2))
	writeFileBytes(t, path, completeSession("sid-tr"))
	setMtime(t, path, time.Minute)
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(2) + ".jsonl"
	r := rollout{rel: rel, abs: filepath.Join(resolved, filepath.FromSlash(rel))}

	out := make(chan canonical.Event, 64)
	// Cursor claims a much larger size than the file → truncation reset to 0.
	var errs []string
	updated, n, err := readRollout(context.Background(), resolved, r, "codex:"+root, FileCursor{Offset: 99999, Size: 99999}, out, func(e error) { errs = append(errs, e.Error()) })
	if err != nil {
		t.Fatalf("readRollout: %v", err)
	}
	if n == 0 {
		t.Error("truncation reset should re-emit the session from 0")
	}
	if updated.Offset == 0 {
		t.Error("offset should advance after re-scan")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "shrank") {
			found = true
		}
	}
	if !found {
		t.Errorf("truncation SourceError not surfaced; errs=%v", errs)
	}
}
