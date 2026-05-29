package claude_code

import (
	"context"
	"os"
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

// TestTail_SymlinkTranscriptEscapeRefused pins P1.3 for the TAIL read path: a
// `*.jsonl` symlink created in a watched directory AFTER Tail starts, resolving
// OUTSIDE the projects root, must be refused before it is opened — its content
// must never be emitted. A legitimate transcript created the same way IS
// ingested, proving the guard rejects only the escape.
func TestTail_SymlinkTranscriptEscapeRefused(t *testing.T) {
	t.Parallel()
	// A secret transcript OUTSIDE the root, shaped to emit a session if read.
	// (The canonical session native id derives from the FILENAME stem, so the
	// symlink's name — not the record's sessionId — is what would surface.)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.jsonl")
	writeFileBytes(t, secret,
		[]byte(`{"type":"user","uuid":"x1","sessionId":"ignored","message":{"role":"user","content":"leak"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))

	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-x")
	// Seed a benign transcript so the project dir exists and is watched.
	writeFileBytes(t, filepath.Join(proj, "seed.jsonl"),
		[]byte(`{"type":"user","uuid":"s1","sessionId":"seed","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
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
	// After the watch is live: a legitimate new transcript AND a *.jsonl symlink
	// escaping the root, both in the watched project dir. The legit file's stem
	// "legit-tail" is its session native id; the symlink "escape.jsonl" would
	// surface as session "escape" if the guard failed.
	writeFileBytes(t, filepath.Join(proj, "legit-tail.jsonl"),
		[]byte(`{"type":"user","uuid":"u9","sessionId":"ok","message":{"role":"user","content":"ok"},"timestamp":"2026-05-26T10:00:09.000Z"}`+"\n"))
	if err := os.Symlink(secret, filepath.Join(proj, "escape.jsonl")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	// Collect until the legitimate session arrives, then assert the escape did not.
	deadline := time.After(6 * time.Second)
	var got []canonical.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
			if _, ok := sessionStartedByNative(got, "legit-tail"); ok {
				// Give a brief grace for any (erroneous) escape emission to flow.
				time.Sleep(200 * time.Millisecond)
				got = append(got, drainBuffered(out)...)
				cancel()
				wg.Wait()
				for _, e := range got {
					if ss, ok := e.(canonical.SessionStartedEvent); ok && ss.NativeID == "escape" {
						t.Fatal("symlinked transcript escaping the root was read by Tail (P1.3 breach)")
					}
				}
				return
			}
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("timeout: legitimate tail transcript not picked up; got %d events", len(got))
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
	resolvedRoot, rrErr := filepath.EvalSymlinks(filepath.Clean(tmp))
	if rrErr != nil {
		t.Fatalf("resolve root: %v", rrErr)
	}
	if err := flushDirty(context.Background(), resolvedRoot, tmp, sourceID, map[string]struct{}{}, metaDirty, &cur, newTailDeferral(), out, func(error) {}); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	// The hash is unchanged, so metaSeen must still hold the same hash.
	if cur.metaSeen(metaRel) != h {
		t.Fatalf("metaSeen changed for an unchanged meta: %q", cur.metaSeen(metaRel))
	}
}

// TestMetaParentRel verifies the subagent-meta → parent-transcript relative
// path derivation used by the late-meta re-read (P1.3b): a meta under
// "<proj>/<sessionId>/subagents/.../agent-<id>.meta.json" maps to
// "<proj>/<sessionId>.jsonl"; a non-subagent path is rejected.
func TestMetaParentRel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		metaRel    string
		wantParent string
		wantOK     bool
	}{
		{"-home-x/sess-1/subagents/agent-abc.meta.json", "-home-x/sess-1.jsonl", true},
		{"-home-x/sess-1/subagents/wf/agent-def.meta.json", "-home-x/sess-1.jsonl", true},
		{"deep/proj/sess-9/subagents/agent-z.meta.json", "deep/proj/sess-9.jsonl", true},
		{"-home-x/sess-1.jsonl", "", false},        // not a meta
		{"-home-x/loose.meta.json", "", false},     // meta without a subagents/ segment
		{"subagents/agent-x.meta.json", "", false}, // no session-id parent before subagents
	}
	for _, c := range cases {
		got, ok := metaParentRel(c.metaRel)
		if ok != c.wantOK || got != c.wantParent {
			t.Errorf("metaParentRel(%q) = (%q, %v), want (%q, %v)", c.metaRel, got, ok, c.wantParent, c.wantOK)
		}
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
