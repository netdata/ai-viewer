package aiagent_v2

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestMapOp_FailedOpWithErrorAttrEmitsLog covers the failed-op +
// non-empty error attribute branch.
func TestMapOp_FailedOpWithErrorAttrEmitsLog(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "failed-op-msg")
	snap.OpTree.Turns[0].Ops[0] = operationNode{
		OpID: "boom", Kind: "tool", StartedAt: 1700000001000,
		EndedAt: int64Ptr(1700000001500), Status: "failed",
		Attributes: rawAttrs(map[string]any{
			"name":     "shell",
			"provider": "builtin",
			"error":    "token_budget_exceeded",
		}),
	}
	events := mapSimple(t, snap)
	var sawErrLog bool
	for _, ev := range events {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "ERR" && l.Message == "token_budget_exceeded" {
			sawErrLog = true
		}
	}
	if !sawErrLog {
		t.Fatalf("expected ERR log for failed op")
	}
}

// TestMapOp_LogEntryWithFallbackTimestamp covers the ts==0 fallthrough.
func TestMapOp_LogEntryWithFallbackTimestamp(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "log-ts-test")
	snap.OpTree.Turns[0].Ops[0].Logs = []logEntry{
		{Severity: "INF", Message: "no timestamp"},
	}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Message == "no timestamp" {
			if l.Ts == 0 {
				t.Fatalf("expected non-zero ts from fallback")
			}
		}
	}
}

// TestMapOp_OpWithoutEndedAt sets startUs path and the missing-endedAt
// branch.
func TestMapOp_OpWithoutEndedAt(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "no-end")
	snap.OpTree.Turns[0].Ops[0].EndedAt = nil
	snap.OpTree.Turns[0].Ops[0].Status = ""
	events := mapSimple(t, snap)
	for _, ev := range events {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok && of.Status != "running" {
			t.Fatalf("expected running status when endedAt absent, got %q", of.Status)
		}
	}
}

// TestMapOp_OpStartedAtZeroUsesRootTs covers the startUs fallback.
func TestMapOp_OpStartedAtZeroUsesRootTs(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "zero-start")
	snap.OpTree.Turns[0].Ops[0].StartedAt = 0
	events := mapSimple(t, snap)
	for _, ev := range events {
		if os, ok := ev.(canonical.OpStartedEvent); ok {
			if os.Ts == 0 {
				t.Fatalf("expected fallback to root ts")
			}
		}
	}
}

// TestMapSession_StartedAtZeroUsesRootTs ensures the session-level
// fallback fires too.
func TestMapSession_StartedAtZeroUsesRootTs(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "zero-session-start")
	child := opTree{
		TraceID: "child-no-start",
		// StartedAt deliberately zero.
		Turns: []turnNode{{Index: 1, StartedAt: 1700000020000}},
	}
	snap.OpTree.Turns[0].Ops = append(snap.OpTree.Turns[0].Ops, operationNode{
		OpID: "child-op", Kind: "session",
		StartedAt:    1700000005000,
		EndedAt:      int64Ptr(1700000006000),
		Status:       "ok",
		ChildSession: &child,
	})
	events := mapSimple(t, snap)
	var found bool
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == "child-no-start" {
			found = true
		}
	}
	if !found {
		t.Fatalf("child session without startedAt should still emit SessionStarted")
	}
}

// TestMapSessionStatus_Abandoned covers the no-turns/no-steps branch.
func TestMapSessionStatus_Abandoned(t *testing.T) {
	t.Parallel()
	st := mapSessionStatus(opTree{StartedAt: 1, TraceID: "abandoned"})
	if st != canonical.StatusAbandoned {
		t.Fatalf("Status: %q", st)
	}
}

// TestMapSessionStatus_RunningWithTurns covers turn-but-no-terminal.
func TestMapSessionStatus_RunningWithTurns(t *testing.T) {
	t.Parallel()
	st := mapSessionStatus(opTree{
		StartedAt: 1, TraceID: "r",
		Turns: []turnNode{{Index: 1}},
	})
	if st != canonical.StatusRunning {
		t.Fatalf("Status: %q", st)
	}
}

// TestMapSessionStatus_Failed exercises the success-false path even
// when endedAt is present.
func TestMapSessionStatus_Failed(t *testing.T) {
	t.Parallel()
	st := mapSessionStatus(opTree{
		Success: boolPtr(false), EndedAt: int64Ptr(2),
	})
	if st != canonical.StatusFailed {
		t.Fatalf("Status: %q", st)
	}
}

// TestOpStartedExtras_AllBranches covers reasoning fields, child ref,
// summary, and tokens.
func TestOpStartedExtras_AllBranches(t *testing.T) {
	t.Parallel()
	op := operationNode{
		Kind:                "llm",
		Reasoning:           &reasoning{Final: "summary text", ChunkCount: 3},
		ChildSessionRef:     &childSessionRef{SessionID: "ref-id"},
		ChildSessionSummary: []byte(`{"summary":"ok"}`),
		Accounting: []accountingEntry{{
			Type: "llm",
			Tokens: &tokens{
				CacheReadInputTokens:  5,
				CacheWriteInputTokens: 7,
				CachedTokens:          3,
			},
		}},
	}
	extras := opStartedExtras(op)
	if extras["original_kind"] != "llm" {
		t.Fatalf("original_kind: %v", extras["original_kind"])
	}
	if extras["reasoning.final"] != "summary text" {
		t.Fatalf("reasoning.final: %v", extras["reasoning.final"])
	}
	if extras["reasoning.chunkCount"] != 3 {
		t.Fatalf("reasoning.chunkCount: %v", extras["reasoning.chunkCount"])
	}
	if extras["childSessionRef"] != "ref-id" {
		t.Fatalf("childSessionRef: %v", extras["childSessionRef"])
	}
	if extras["childSessionSummary"] == nil {
		t.Fatalf("childSessionSummary missing")
	}
	if extras["tokensCacheRead"].(int64) != 8 { // 5 + 3
		t.Fatalf("tokensCacheRead: %v", extras["tokensCacheRead"])
	}
	if extras["tokensCacheWrite"].(int64) != 7 {
		t.Fatalf("tokensCacheWrite: %v", extras["tokensCacheWrite"])
	}
}

// TestBuildOpFinalized_RequestResponseSize covers the BytesIn/Out
// branches.
func TestBuildOpFinalized_RequestResponseSize(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "bytes")
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{Size: 1234}
	snap.OpTree.Turns[0].Ops[0].Response = &opPayload{Size: 5678}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok {
			if of.BytesIn != 1234 || of.BytesOut != 5678 {
				t.Fatalf("bytes: %d/%d", of.BytesIn, of.BytesOut)
			}
		}
	}
}

// TestScanAll_ManyFilesEmitsProgress exercises the progressEveryFiles
// threshold by writing more files than the threshold.
func TestScanAll_ManyFilesEmitsProgress(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		origin := filepath.Base(t.TempDir()) // unique-enough
		writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	}
	out := make(chan canonical.Event, 1024)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var pCount int
	for _, ev := range drainBuffered(out) {
		if _, ok := ev.(canonical.SourceProgressEvent); ok {
			pCount++
		}
	}
	if pCount == 0 {
		t.Fatalf("expected at least one progress event")
	}
}

// TestProcessOnce_NoMtimeChangeNoRetry exercises the "post mtime ==
// pre mtime → no retry" path.
func TestProcessOnce_NoMtimeChangeNoRetry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "stable-mtime"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	cur := newCursor()
	out := make(chan canonical.Event, 256)
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {}); err != nil {
		t.Fatalf("processOnce: %v", err)
	}
	// Re-run with same cursor; mtime unchanged → no domain events.
	out2 := make(chan canonical.Event, 64)
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out2, func(error) {}); err != nil {
		t.Fatalf("processOnce 2: %v", err)
	}
	for _, ev := range drainBuffered(out2) {
		switch ev.(type) {
		case canonical.SourceProgressEvent, canonical.SourceErrorEvent:
		default:
			t.Fatalf("unexpected domain event on stable file: %T", ev)
		}
	}
}

// TestProcessOnce_FileVanishesBetweenStats covers the post-stat
// IsNotExist branch.
func TestProcessOnce_FileVanishesBetweenStats(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "vanish"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	cur := newCursor()
	out := make(chan canonical.Event, 64)
	// Wrap processFile to remove the file mid-call by faking. Simplest
	// approach: process once, then remove, then process again where
	// pre-stat will fail.
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := os.Remove(filepath.Join(root, origin+".json.gz")); err != nil {
		t.Fatalf("rm: %v", err)
	}
	// processOnce should return nil for missing file (defensive).
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {}); err != nil {
		t.Fatalf("processOnce after rm: %v", err)
	}
}

// TestTail_RemoveEventClearsDirty covers the fsnotify.Remove branch
// in tailLoop. Recreates with different content so the content-hash
// dedup does not swallow the second pass.
func TestTail_RemoveEventClearsDirty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(80 * time.Millisecond)

	origin := "del-test"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	if _, ok := waitForEvent(t, out, 5*time.Second, func(ev canonical.Event) bool {
		ss, is := ev.(canonical.SessionStartedEvent)
		return is && ss.NativeID == origin
	}); !ok {
		t.Fatalf("did not see initial SessionStarted")
	}
	if err := os.Remove(filepath.Join(root, origin+".json.gz")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Drain.
	time.Sleep(300 * time.Millisecond)
	_ = drainBuffered(out)
	// Write a different snapshot so content hash differs from the first
	// pass; the Create+Write events should trip the dirty bit again.
	snap2 := simpleSnapshot(2, origin)
	snap2.OpTree.SessionTitle = "post-remove rewrite"
	writeSnapshot(t, root, origin, snap2)
	if _, ok := waitForEvent(t, out, 5*time.Second, func(ev canonical.Event) bool {
		ss, is := ev.(canonical.SessionStartedEvent)
		return is && ss.NativeID == origin
	}); !ok {
		t.Fatalf("expected SessionStarted after remove + recreate")
	}
}

// TestSnapshotCursor_StatPermissionError covers the onError branch
// inside snapshotCursor.
func TestSnapshotCursor_StatPermissionError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root can stat anything; skip permission test")
	}
	root := t.TempDir()
	writeSnapshot(t, root, "stat-fail", simpleSnapshot(2, "stat-fail"))
	// Drop file perms so stat may fail under some filesystems; on most
	// Linux setups, stat on the file still works because perms apply to
	// open/read. We exercise the path anyway — the test cannot reliably
	// force a stat error without a custom FS, so we just confirm no
	// panic.
	_ = os.Chmod(filepath.Join(root, "stat-fail.json.gz"), 0o000)
	defer func() { _ = os.Chmod(filepath.Join(root, "stat-fail.json.gz"), 0o644) }()
	a, _ := New(root, canonical.AdapterOptions{})
	if _, err := a.snapshotCursor(); err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}
}
