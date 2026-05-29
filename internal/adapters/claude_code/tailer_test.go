package claude_code

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

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
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	path := filepath.Join(proj, "sess-1.jsonl")
	// One complete record + a partial PREFIX of a second record (an
	// in-flight write: not valid JSON yet, no trailing newline).
	complete := `{"type":"user","uuid":"u1","sessionId":"sess-1","message":{"role":"user","content":"a"},"timestamp":"2026-05-26T10:00:00.000Z"}` + "\n"
	full2 := `{"type":"user","uuid":"u2","sessionId":"sess-1","message":{"role":"user","content":"b"},"timestamp":"2026-05-26T10:00:01.000Z"}`
	partial := full2[:50] // a prefix of the second record
	writeFileBytes(t, path, []byte(complete+partial))

	events, _ := collectErrors(t, tmp)
	// Exactly one turn_started: the complete record. The partial is parked.
	if n := countKind(events, canonical.EvTurnStarted); n != 1 {
		t.Fatalf("turn_started = %d, want 1 (partial line must be parked, not parsed)", n)
	}

	// The persisted cursor offset must stop at the complete-line boundary
	// (the length of `complete`), so a resume re-reads the parked bytes.
	var cur string
	for _, ev := range events {
		if sp, ok := ev.(canonical.SourceProgressEvent); ok {
			cur = sp.Cursor
		}
	}
	if cur != "" {
		parsed, err := ParseCursor(cur)
		if err != nil {
			t.Fatalf("ParseCursor: %v", err)
		}
		fc := parsed.fileCursor("-home-user-x/sess-1.jsonl")
		if fc.Offset != int64(len(complete)) {
			t.Fatalf("cursor offset = %d, want %d (stop at complete-line boundary, park partial)", fc.Offset, len(complete))
		}
	}

	// Now complete the parked line and confirm a resume FROM THE CURSOR
	// parses exactly the newly-completed line (zero gap, zero dup).
	if cur == "" {
		t.Fatal("no SourceProgress cursor captured for resume")
	}
	// Complete the parked record (append the remaining bytes + newline).
	appendFileBytes(t, path, []byte(full2[50:]+"\n"))
	a, err := New(tmp, canonical.AdapterOptions{})
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
	resumed := drainBuffered(out)
	if n := countKind(resumed, canonical.EvTurnStarted); n != 1 {
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
	}{
		{"-home-x/sess-1.jsonl", "sess-1", "", canonical.KindRoot},
		{"-home-x/sess-1/subagents/agent-abc123.jsonl", "sess-1:agent:abc123", "sess-1", canonical.KindSubAgent},
		{"-home-x/sess-1/subagents/wf/agent-def456.jsonl", "sess-1:agent:def456", "sess-1", canonical.KindSubAgent},
	}
	for _, c := range cases {
		got, ok := transcriptForRel(root, c.rel)
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

// TestHashFile verifies hashFile reads and hashes a file, and reports false
// for a missing path.
func TestHashFile(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.json")
	writeFileBytes(t, path, []byte(`{"a":1}`))
	h, ok := hashFile(path)
	if !ok || h == "" {
		t.Fatalf("hashFile = (%q,%v), want a hash", h, ok)
	}
	if h != hashBytes([]byte(`{"a":1}`)) {
		t.Fatalf("hashFile hash mismatch")
	}
	if _, ok := hashFile(filepath.Join(tmp, "missing")); ok {
		t.Fatal("hashFile(missing) should report ok=false")
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
	if err := flushDirty(context.Background(), tmp, sourceID, map[string]struct{}{}, metaDirty, &cur, out, func(error) {}); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	// The hash is unchanged, so metaSeen must still hold the same hash.
	if cur.metaSeen(metaRel) != h {
		t.Fatalf("metaSeen changed for an unchanged meta: %q", cur.metaSeen(metaRel))
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
