package claude_code

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type tailTestRun struct {
	out    chan canonical.Event
	cancel context.CancelFunc
	done   chan struct{}
}

func startTailTest(t *testing.T, a *Adapter) *tailTestRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &tailTestRun{
		out:    make(chan canonical.Event, 256),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() {
		_ = a.Tail(ctx, run.out)
		close(run.done)
	}()
	return run
}

func (r *tailTestRun) stop() {
	r.cancel()
	<-r.done
}

type tailEventMatch func([]canonical.Event, canonical.Event) bool

func waitForTailEvents(t *testing.T, out <-chan canonical.Event, timeout time.Duration, match tailEventMatch, fail string) []canonical.Event {
	t.Helper()
	deadline := time.After(timeout)
	var got []canonical.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
			if match(got, ev) {
				return got
			}
		case <-deadline:
			t.Fatalf("%s; got %d events", fail, len(got))
		}
	}
}

func waitForHeartbeat(t *testing.T, count *atomic.Int64, want int64, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if count.Load() >= want {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-tick.C:
		}
	}
}

func TestTail_HeartbeatOnIdleTick(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var heartbeats atomic.Int64
	a, err := New(root, canonical.AdapterOptions{
		OnTailHeartbeat: func() { heartbeats.Add(1) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run := startTailTest(t, a)
	defer run.stop()

	if !waitForHeartbeat(t, &heartbeats, 1, 2*time.Second) {
		t.Fatalf("tail startup catch-up did not call tail heartbeat")
	}
	next := heartbeats.Load() + 1
	if !waitForHeartbeat(t, &heartbeats, next, tailTickInterval+3*time.Second) {
		t.Fatalf("idle tail tick did not call tail heartbeat")
	}
}

// TestTail_PicksUpAppendedRecords verifies the fsnotify tail loop emits
// events for records appended to an existing transcript after Tail starts
// (the realtime path). The seeded record is below the snapshot cursor and
// must NOT be re-emitted; only the appended assistant produces an LLM op.
func TestTail_PicksUpAppendedRecords(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	path := filepath.Join(proj, "sess-1.jsonl")
	// Seed with one complete record so Tail's snapshot cursor starts past it.
	writeFileBytes(t, path,
		[]byte(`{"type":"user","uuid":"u1","sessionId":"sess-1","message":{"role":"user","content":"first"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))

	a, err := New(tmp, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan canonical.Event, 256)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = a.Tail(ctx, out)
	}()

	// Give Tail a moment to register the watch, then append a complete
	// assistant record.
	time.Sleep(150 * time.Millisecond)
	appendFileBytes(t, path, []byte(`{"type":"assistant","uuid":"a1","sessionId":"sess-1","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"hi"}]}}`+"\n"))

	// Wait for the appended LLM op to flow. Collect generously; assert on
	// presence in the full set so debounce/scheduler jitter is not flaky.
	deadline := time.After(5 * time.Second)
	var got []canonical.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
			if hasOpKind(got, canonical.OpLLM) {
				cancel()
				wg.Wait()
				return
			}
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("timeout waiting for appended LLM op; got %d events", len(got))
		}
	}
}

// TestReadOneLine_HoldsBackPartialLine pins the partial-line parking
// invariant directly (spec §6.3, R2): a reader over bytes with no trailing
// newline returns io.EOF without surfacing the partial line, and the offset
// the caller would advance to excludes the partial bytes.
func TestReadOneLine_HoldsBackPartialLine(t *testing.T) {
	t.Parallel()
	fixture := writePartialLineFixture(t)
	events, _ := collectErrors(t, fixture.root)
	assertPartialLineParked(t, events, len(fixture.complete))

	cur := requireProgressCursor(t, events)
	appendFileBytes(t, fixture.path, []byte(fixture.fullSecond[50:]+"\n"))
	assertResumeReadsCompletedPartial(t, fixture.root, cur)
}

type partialLineFixture struct {
	root       string
	path       string
	complete   string
	fullSecond string
}

func writePartialLineFixture(t *testing.T) partialLineFixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "-home-user-x", "sess-1.jsonl")
	complete := `{"type":"user","uuid":"u1","sessionId":"sess-1","message":{"role":"user","content":"a"},"timestamp":"2026-05-26T10:00:00.000Z"}` + "\n"
	fullSecond := `{"type":"user","uuid":"u2","sessionId":"sess-1","message":{"role":"user","content":"b"},"timestamp":"2026-05-26T10:00:01.000Z"}`
	writeFileBytes(t, path, []byte(complete+fullSecond[:50]))
	return partialLineFixture{root: root, path: path, complete: complete, fullSecond: fullSecond}
}

func assertPartialLineParked(t *testing.T, events []canonical.Event, completeLen int) {
	t.Helper()
	if n := countKind(events, canonical.EvTurnStarted); n != 1 {
		t.Fatalf("turn_started = %d, want 1 (partial line must be parked, not parsed)", n)
	}
	assertCursorOffset(t, requireProgressCursor(t, events), int64(completeLen))
}

func requireProgressCursor(t *testing.T, events []canonical.Event) string {
	t.Helper()
	for _, ev := range events {
		if sp, ok := ev.(canonical.SourceProgressEvent); ok {
			return sp.Cursor
		}
	}
	t.Fatal("no SourceProgress cursor captured for resume")
	return ""
}

func assertCursorOffset(t *testing.T, cur string, want int64) {
	t.Helper()
	parsed, err := ParseCursor(cur)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	if got := parsed.fileCursor("-home-user-x/sess-1.jsonl").Offset; got != want {
		t.Fatalf("cursor offset = %d, want %d (stop at complete-line boundary, park partial)", got, want)
	}
}

func assertResumeReadsCompletedPartial(t *testing.T, root, cur string) {
	t.Helper()
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parsed, err := a.ParseCursor(cur)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	out := make(chan canonical.Event, 64)
	if err := a.Scan(context.Background(), parsed, out); err != nil {
		t.Fatalf("resume Scan: %v", err)
	}
	if n := countKind(drainBuffered(out), canonical.EvTurnStarted); n != 1 {
		t.Fatalf("resume after completing parked line: turn_started = %d, want exactly 1 (the newly-completed record, no dup of the first)", n)
	}
}

// TestTail_NewProjectDirWatched verifies the tail loop picks up a transcript
// created in a brand-new project dir AFTER Tail starts (exercises the
// CREATE-dir → addWatchTree branch; fsnotify is non-recursive on Linux).
func TestTail_NewProjectDirWatched(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	a, err := New(tmp, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan canonical.Event, 256)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = a.Tail(ctx, out) }()

	time.Sleep(150 * time.Millisecond)
	// Create a new project dir + transcript after the watch is live.
	proj := filepath.Join(tmp, "-home-user-new")
	writeFileBytes(t, filepath.Join(proj, "sess-new.jsonl"),
		[]byte(`{"type":"user","uuid":"u1","sessionId":"sess-new","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))

	deadline := time.After(6 * time.Second)
	var got []canonical.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
			if _, ok := sessionStartedByNative(got, "sess-new"); ok {
				cancel()
				wg.Wait()
				return
			}
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("timeout: new-project-dir transcript not picked up; got %d events", len(got))
		}
	}
}

// TestTail_SymlinkedProjectsRootWatched pins P2.6c (SOW-0003 Round 6): when the
// configured projects root is ITSELF a symlink (e.g. ~/.claude → an external volume),
// the Tail watch tree must walk the symlink-RESOLVED root — filepath.WalkDir does not
// descend INTO a symlinked walk-root, so watching the unresolved root would Add() zero
// directories and silently miss every transcript. A transcript created under the
// resolved tree after Tail starts must still be picked up.
func TestTail_SymlinkedProjectsRootWatched(t *testing.T) {
	t.Parallel()
	realRoot, linkRoot := writeSymlinkedRootFixture(t)
	a, err := New(linkRoot, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run := startTailTest(t, a)
	defer run.stop()

	time.Sleep(200 * time.Millisecond)
	appendFileBytes(t, filepath.Join(realRoot, "-home-user-x", "seed.jsonl"),
		[]byte(`{"type":"assistant","uuid":"a1","sessionId":"seed","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3},"content":[{"type":"text","text":"hello"}]},"timestamp":"2026-05-26T10:00:09.000Z"}`+"\n"))

	waitForTailEvents(t, run.out, 8*time.Second, hasSeedLLMOp, "timeout: append under a symlinked projects root not picked up (P2.6c — Tail watch did not descend the resolved root)")
}

func writeSymlinkedRootFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	writeFileBytes(t, filepath.Join(realRoot, "-home-user-x", "seed.jsonl"),
		[]byte(`{"type":"user","uuid":"s1","sessionId":"seed","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	return realRoot, linkRoot
}

func hasSeedLLMOp(events []canonical.Event, _ canonical.Event) bool {
	for _, ev := range events {
		if op, ok := ev.(canonical.OpStartedEvent); ok && op.SessionNativeID == "seed" && op.Kind == canonical.OpLLM {
			return true
		}
	}
	return false
}

// TestTail_SymlinkTranscriptEscapeRefused pins P1.3 for the TAIL read path: a
// `*.jsonl` symlink created in a watched directory AFTER Tail starts, resolving
// OUTSIDE the projects root, must be refused before it is opened — its content
// must never be emitted. A legitimate transcript created the same way IS
// ingested, proving the guard rejects only the escape.
func TestTail_SymlinkTranscriptEscapeRefused(t *testing.T) {
	t.Parallel()
	root, proj, secret := writeSymlinkEscapeFixture(t)
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run := startTailTest(t, a)
	defer run.stop()

	time.Sleep(150 * time.Millisecond)
	writeFileBytes(t, filepath.Join(proj, "legit-tail.jsonl"),
		[]byte(`{"type":"user","uuid":"u9","sessionId":"ok","message":{"role":"user","content":"ok"},"timestamp":"2026-05-26T10:00:09.000Z"}`+"\n"))
	if err := os.Symlink(secret, filepath.Join(proj, "escape.jsonl")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	got := waitForTailEvents(t, run.out, 6*time.Second, hasLegitTailSession, "timeout: legitimate tail transcript not picked up")
	time.Sleep(200 * time.Millisecond)
	assertNoEscapeSession(t, append(got, drainBuffered(run.out)...))
}

func writeSymlinkEscapeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.jsonl")
	writeFileBytes(t, secret,
		[]byte(`{"type":"user","uuid":"x1","sessionId":"ignored","message":{"role":"user","content":"leak"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-x")
	writeFileBytes(t, filepath.Join(proj, "seed.jsonl"),
		[]byte(`{"type":"user","uuid":"s1","sessionId":"seed","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	return root, proj, secret
}

func hasLegitTailSession(events []canonical.Event, _ canonical.Event) bool {
	_, ok := sessionStartedByNative(events, "legit-tail")
	return ok
}

func assertNoEscapeSession(t *testing.T, events []canonical.Event) {
	t.Helper()
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == "escape" {
			t.Fatal("symlinked transcript escaping the root was read by Tail (P1.3 breach)")
		}
	}
}

// TestTail_MissingRootCleanReturn verifies Tail returns nil (not an error)
// when the projects root is absent — the daemon keeps running for other
// sources (security.md read-only-on-sources).
func TestTail_MissingRootCleanReturn(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "nope")
	var errs []string
	a, _ := New(missing, canonical.AdapterOptions{OnError: func(e error) { errs = append(errs, e.Error()) }})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := make(chan canonical.Event, 4)
	if err := a.Tail(ctx, out); err != nil {
		t.Fatalf("Tail on missing root should return nil, got %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected a SourceError for the missing root")
	}
}

// TestTranscriptForRel verifies the relative-path → transcript descriptor
// reconstruction used by the tail flush path, for both main and subagent
// shapes.
func TestTranscriptForRel(t *testing.T) {
	t.Parallel()
	root := "/root"
	cases := []struct {
		rel        string
		wantNative string
		wantParent string
		wantKind   canonical.SessionKind
		wantOK     bool
	}{
		{"-home-x/sess-1.jsonl", "sess-1", "", canonical.KindRoot, true},
		{"-home-x/sess-1/subagents/agent-abc123.jsonl", "sess-1:agent:abc123", "sess-1", canonical.KindSubAgent, true},
		{"-home-x/sess-1/subagents/wf/agent-def456.jsonl", "sess-1:agent:def456", "sess-1", canonical.KindSubAgent, true},
		{"-home-x/sess-1/subagents/workflows/wf-1/journal.jsonl", "", "", "", false},
	}
	for _, c := range cases {
		got, ok := transcriptForRel(root, c.rel)
		if ok != c.wantOK {
			t.Errorf("transcriptForRel(%q): ok=%v, want %v", c.rel, ok, c.wantOK)
			continue
		}
		if !c.wantOK {
			continue
		}
		if !ok {
			t.Errorf("transcriptForRel(%q): ok=false", c.rel)
			continue
		}
		if got.nativeID != c.wantNative || got.parentNativeID != c.wantParent || got.kind != c.wantKind {
			t.Errorf("transcriptForRel(%q) = (native=%q parent=%q kind=%q), want (%q,%q,%q)",
				c.rel, got.nativeID, got.parentNativeID, got.kind, c.wantNative, c.wantParent, c.wantKind)
		}
	}
	if _, ok := transcriptForRel(root, "-home-x/notes.txt"); ok {
		t.Error("transcriptForRel on non-jsonl should return ok=false")
	}
}

// TestRelOrBase verifies relOrBase returns the root-relative path when the
// path is under root, and the basename otherwise.
func TestRelOrBase(t *testing.T) {
	t.Parallel()
	if got := relOrBase("/root", "/root/a/b.jsonl"); got != "a/b.jsonl" {
		t.Errorf("relOrBase under root = %q, want a/b.jsonl", got)
	}
	// An absolute path outside root still yields a relative form on most
	// platforms; the function never panics. Just assert it returns
	// something non-empty.
	if got := relOrBase("/root", "/other/c.jsonl"); got == "" {
		t.Error("relOrBase returned empty")
	}
}

// TestReadMetaCapped verifies readMetaCapped reads and returns a small sidecar's
// bytes, rejects an oversized sidecar with errMetaTooLarge WITHOUT reading it all,
// and returns a read error for a missing path (no silent skip — P2.5a/P2.6b).
func TestReadMetaCapped(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.json")
	writeFileBytes(t, path, []byte(`{"a":1}`))
	raw, err := readMetaCapped(path)
	if err != nil || string(raw) != `{"a":1}` {
		t.Fatalf("readMetaCapped = (%q,%v), want the file bytes", raw, err)
	}
	// A read error is RETURNED, not swallowed: the caller surfaces a SourceError.
	if _, err := readMetaCapped(filepath.Join(tmp, "missing")); err == nil {
		t.Fatal("readMetaCapped(missing) should return a read error, not nil")
	}
	// An oversized sidecar is rejected with errMetaTooLarge.
	big := filepath.Join(tmp, "big.json")
	writeFileBytes(t, big, make([]byte, metaReadMax+1))
	if _, err := readMetaCapped(big); !errors.Is(err, errMetaTooLarge) {
		t.Fatalf("readMetaCapped(oversized) err = %v, want errMetaTooLarge", err)
	}
}

// TestFlushDirty_MetaSeenSkipsUnchanged verifies the cursor's metaSeen map
// suppresses re-hashing a meta file whose content has not changed (spec §7
// step 4): a flush over an unchanged meta does not alter the recorded hash.
func TestFlushDirty_MetaSeenSkipsUnchanged(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	metaRel := "-home-user-x/sess-1/subagents/agent-abc.meta.json"
	metaAbs := filepath.Join(proj, "sess-1", "subagents", "agent-abc.meta.json")
	writeFileBytes(t, metaAbs, []byte(`{"agentType":"general-purpose","description":"d"}`))
	h := hashBytes([]byte(`{"agentType":"general-purpose","description":"d"}`))

	// Pre-seed the cursor's metaSeen with the current hash.
	cur := newCursor().withMetaSeen(metaRel, h)
	sourceID := "claude-code:" + tmp
	out := make(chan canonical.Event, 16)
	metaDirty := map[string]struct{}{metaRel: {}}
	resolvedRoot, rrErr := filepath.EvalSymlinks(filepath.Clean(tmp))
	if rrErr != nil {
		t.Fatalf("resolve root: %v", rrErr)
	}
	flush := newTailFlush(context.Background(), resolvedRoot, tmp, sourceID, &cur, newTailDeferral(), out, func(error) {})
	if err := flush.flushDirty(map[string]struct{}{}, metaDirty); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	// The hash is unchanged, so metaSeen must still hold the same hash.
	if cur.metaSeen(metaRel) != h {
		t.Fatalf("metaSeen changed for an unchanged meta: %q", cur.metaSeen(metaRel))
	}
}

// TestScanThenTail_LateMetaRepairsChildAgentName pins the Round-6 catalog-safe
// AgentName repair: a child subagent transcript read BEFORE its `.meta.json` exists
// emits its SessionStarted with an empty AgentName. When the meta arrives in a later
// Tail flush, the adapter must repair the child's AgentName by emitting a
// catalog-safe SessionUpdatedEvent{AgentName=agentType} — NOT by re-reading the child
// transcript (which would re-emit SessionStarted/OpStarted/OpFinalized and
// double-count the catalog rollups, P1.6). Without the repair the AgentName stays
// permanently empty.
func TestScanThenTail_LateMetaRepairsChildAgentName(t *testing.T) {
	t.Parallel()
	root, child := writeLateMetaRepairFixture(t)
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	assertChildScannedWithoutAgentName(t, a, child)

	run := startTailTest(t, a)
	defer run.stop()

	time.Sleep(150 * time.Millisecond)
	writeFileBytes(t, metaPath(root),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+parentAgentToolUseID+`"}`))

	waitForLateMetaRepair(t, run.out, child)
}

func writeLateMetaRepairFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	child := childNativeID(durParentSession, durAgentID)
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))
	return root, child
}

func assertChildScannedWithoutAgentName(t *testing.T, a *Adapter, child string) {
	t.Helper()
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	cs, ok := sessionStartedByNative(drainBuffered(scanOut), child)
	if !ok {
		t.Fatalf("child session %q not started during Scan", child)
	}
	if cs.AgentName != "" {
		t.Fatalf("child AgentName = %q before meta exists; test premise broken", cs.AgentName)
	}
}

func waitForLateMetaRepair(t *testing.T, out <-chan canonical.Event, child string) {
	t.Helper()
	match := func(_ []canonical.Event, ev canonical.Event) bool {
		return isLateMetaRepairEvent(t, ev, child)
	}
	waitForTailEvents(t, out, 8*time.Second, match, "child session "+child+" AgentName never repaired via SessionUpdated after the late .meta.json")
}

func isLateMetaRepairEvent(t *testing.T, ev canonical.Event, child string) bool {
	t.Helper()
	if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == child {
		t.Fatalf("late-meta repair re-emitted a SessionStartedEvent for child %q (P1.6 regression — must be a SessionUpdatedEvent)", child)
	}
	su, ok := ev.(canonical.SessionUpdatedEvent)
	return ok && su.NativeID == child && su.AgentName == "Explore"
}

// TestFlushDirty_UnreadableMetaSurfacesError pins P2.5a: a PRESENT but unreadable
// subagent .meta.json on the Tail meta-change path surfaces a SourceError (no
// silent failure). A meta path that exists in-root but cannot be read (here: a
// directory at the meta path, so the read fails) must drive an onError, not be
// silently skipped — otherwise a broken sidecar whose rewrite should drive the
// AgentName repair is masked.
func TestFlushDirty_UnreadableMetaSurfacesError(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	metaRel := "-home-user-x/sess-1/subagents/agent-abc.meta.json"
	// Create a DIRECTORY at the meta path: it exists (containment passes) but
	// os.ReadFile on it returns an error → the hash read fails.
	if err := os.MkdirAll(filepath.Join(proj, "sess-1", "subagents", "agent-abc.meta.json"), 0o755); err != nil {
		t.Fatalf("mkdir meta-as-dir: %v", err)
	}

	resolvedRoot, rrErr := filepath.EvalSymlinks(filepath.Clean(tmp))
	if rrErr != nil {
		t.Fatalf("resolve root: %v", rrErr)
	}
	var mu sync.Mutex
	var errs []string
	onError := func(e error) {
		mu.Lock()
		errs = append(errs, e.Error())
		mu.Unlock()
	}
	cur := newCursor()
	out := make(chan canonical.Event, 16)
	metaDirty := map[string]struct{}{metaRel: {}}
	flush := newTailFlush(context.Background(), resolvedRoot, tmp, "claude-code:"+tmp, &cur, newTailDeferral(), out, onError)
	if err := flush.flushDirty(map[string]struct{}{}, metaDirty); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	var sawReadErr bool
	for _, e := range errs {
		if strings.Contains(e, "agent-abc.meta.json") && strings.Contains(e, "read meta") {
			sawReadErr = true
		}
	}
	if !sawReadErr {
		t.Fatalf("unreadable meta on the Tail meta-change path did not surface a SourceError (P2.5a); errs=%v", errs)
	}
}

// hasOpKind reports whether any OpStartedEvent of the given kind is present.
func hasOpKind(events []canonical.Event, kind canonical.OpKind) bool {
	for _, ev := range events {
		if op, ok := ev.(canonical.OpStartedEvent); ok && op.Kind == kind {
			return true
		}
	}
	return false
}
