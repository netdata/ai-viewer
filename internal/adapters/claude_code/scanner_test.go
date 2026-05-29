package claude_code

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// collectErrors runs a fresh adapter Scan over root with an OnError that
// records every error message, and returns the events plus the error list.
func collectErrors(t *testing.T, root string) ([]canonical.Event, []string) {
	t.Helper()
	var mu sync.Mutex
	var errs []string
	a, err := New(root, canonical.AdapterOptions{OnError: func(e error) {
		mu.Lock()
		errs = append(errs, e.Error())
		mu.Unlock()
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 8192)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return drainBuffered(out), errs
}

// TestScan_UnknownTypeTolerance verifies acceptance #2: feeding N distinct
// unknown `type` strings produces exactly one SourceError-bearing parse
// error per unknown variant, and the scan does not abort.
func TestScan_UnknownTypeTolerance(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-home-user-x")
	var b strings.Builder
	// One valid record so a session exists, then 20 distinct unknown types.
	b.WriteString(`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}` + "\n")
	const n = 20
	for i := 0; i < n; i++ {
		b.WriteString(fmt.Sprintf(`{"type":"unknown-variant-%02d","sessionId":"s"}`, i) + "\n")
	}
	writeFileBytes(t, filepath.Join(projDir, "s.jsonl"), []byte(b.String()))

	_, errs := collectErrors(t, tmp)
	unknownErrs := 0
	seen := map[string]int{}
	for _, e := range errs {
		if strings.Contains(e, "unknown record type") {
			unknownErrs++
			// Extract the variant suffix for the per-variant count.
			for i := 0; i < n; i++ {
				v := fmt.Sprintf("unknown-variant-%02d", i)
				if strings.Contains(e, v) {
					seen[v]++
				}
			}
		}
	}
	if unknownErrs != n {
		t.Fatalf("unknown-type errors = %d, want %d", unknownErrs, n)
	}
	if len(seen) != n {
		t.Fatalf("distinct unknown variants surfaced = %d, want %d", len(seen), n)
	}
	for v, c := range seen {
		if c != 1 {
			t.Errorf("variant %s surfaced %d times, want exactly 1", v, c)
		}
	}
}

// TestScan_DiscoversMainAndSubagent verifies discovery walks both the main
// transcript and the subagent sidechain, emitting two sessions.
func TestScan_DiscoversMainAndSubagent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	writeFileBytes(t, filepath.Join(proj, "sess-1.jsonl"),
		[]byte(`{"type":"user","uuid":"u1","sessionId":"sess-1","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	subDir := filepath.Join(proj, "sess-1", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.jsonl"),
		[]byte(`{"type":"user","uuid":"su1","isSidechain":true,"agentId":"aaa111bbb222ccc","sessionId":"sess-1","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}`+"\n"))
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.meta.json"),
		[]byte(`{"agentType":"general-purpose","description":"d","toolUseId":"toolu_x"}`+"\n"))

	events, _ := collectErrors(t, tmp)
	if _, ok := sessionStartedByNative(events, "sess-1"); !ok {
		t.Fatal("main session sess-1 not started")
	}
	child := "sess-1:agent:aaa111bbb222ccc"
	cs, ok := sessionStartedByNative(events, child)
	if !ok {
		t.Fatalf("subagent session %q not started", child)
	}
	if cs.Kind != canonical.KindSubAgent || cs.AgentName != "general-purpose" {
		t.Fatalf("subagent session fields wrong: kind=%q agentName=%q", cs.Kind, cs.AgentName)
	}
}

// TestScan_OrphanRootSynthesized verifies a <sessionId>/subagents/ dir with
// no parent .jsonl produces a synthetic root session (spec §10.1).
func TestScan_OrphanRootSynthesized(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	subDir := filepath.Join(tmp, "-home-user-old", "orphan-sess", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-dead000beef111c.jsonl"),
		[]byte(`{"type":"user","uuid":"su1","isSidechain":true,"agentId":"dead000beef111c","sessionId":"orphan-sess","message":{"role":"user","content":"task"},"timestamp":"2026-02-10T09:00:00.000Z"}`+"\n"))

	events, _ := collectErrors(t, tmp)
	root, ok := sessionStartedByNative(events, "orphan-sess")
	if !ok {
		t.Fatal("orphan root session not synthesized")
	}
	if root.Kind != canonical.KindRoot {
		t.Fatalf("orphan root Kind = %q, want root", root.Kind)
	}
	if root.Extras["orphanRoot"] != true {
		t.Fatalf("orphan root missing orphanRoot=true extra: %+v", root.Extras)
	}
	if _, ok := sessionStartedByNative(events, "orphan-sess:agent:dead000beef111c"); !ok {
		t.Fatal("orphan's subagent child not started")
	}
}

// TestScan_OversizedLineSkipped verifies a single line exceeding the scan
// buffer is surfaced as a SourceError and skipped to EOF (exercises
// errLineTooLong + drainToNewline), without aborting the scan.
func TestScan_OversizedLineSkipped(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	// A valid record, then an oversized garbage line (> scanBufferMax),
	// then another valid record after a newline.
	big := strings.Repeat("x", scanBufferMax+1024)
	body := `{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"a"},"timestamp":"2026-05-26T10:00:00.000Z"}` + "\n" +
		big + "\n"
	writeFileBytes(t, filepath.Join(proj, "s.jsonl"), []byte(body))

	events, errs := collectErrors(t, tmp)
	var sawTooLong bool
	for _, e := range errs {
		if strings.Contains(e, "exceeds") {
			sawTooLong = true
		}
	}
	if !sawTooLong {
		t.Fatalf("expected an 'exceeds' SourceError for the oversized line; got %v", errs)
	}
	// The first valid record's session must still have been emitted.
	if _, ok := sessionStartedByNative(events, "s"); !ok {
		t.Fatal("oversized line aborted the scan; the prior valid record was lost")
	}
}

// TestScan_EmptyRootNoEvents verifies a missing projects root is tolerated.
func TestScan_EmptyRootNoEvents(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist")
	a, err := New(missing, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 16)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan on missing root should be nil err, got %v", err)
	}
	// Only SourceProgress (final) may be emitted; no session events.
	for _, ev := range drainBuffered(out) {
		if ev.EventKind() == canonical.EvSessionStarted {
			t.Fatal("missing root should emit no sessions")
		}
	}
}

// TestScan_ContextCancel verifies Scan returns nil (not an error) when its
// context is cancelled mid-stream — the cancel branches in scanAll/Scan.
func TestScan_ContextCancel(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	// Several sessions so there is work to interrupt.
	for i := 0; i < 5; i++ {
		writeFileBytes(t, filepath.Join(proj, fmt.Sprintf("sess-%d.jsonl", i)),
			[]byte(`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	}
	a, _ := New(tmp, canonical.AdapterOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Scan starts
	out := make(chan canonical.Event, 256)
	if err := a.Scan(ctx, nil, out); err != nil {
		t.Fatalf("Scan(cancelled) should return nil, got %v", err)
	}
}

// TestScan_TruncationRescans verifies a shrunken file is re-scanned from 0
// (spec §7 step 2): a cursor recording a larger size triggers a rescan.
func TestScan_TruncationRescans(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	path := filepath.Join(proj, "sess-1.jsonl")
	writeFileBytes(t, path,
		[]byte(`{"type":"user","uuid":"u1","sessionId":"sess-1","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	info, _ := os.Stat(path)

	// A cursor claiming a larger prior size than the file now has → shrink.
	rel := "-home-user-x/sess-1.jsonl"
	cur := newCursor().withFile(rel, FileCursor{Offset: info.Size(), Size: info.Size() + 999})

	var errs []string
	a, _ := New(tmp, canonical.AdapterOptions{OnError: func(e error) { errs = append(errs, e.Error()) }})
	out := make(chan canonical.Event, 64)
	if err := a.Scan(context.Background(), cur, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	if _, ok := sessionStartedByNative(events, "sess-1"); !ok {
		t.Fatal("truncation rescan did not re-emit the session")
	}
	var sawShrink bool
	for _, e := range errs {
		if strings.Contains(e, "shrank") {
			sawShrink = true
		}
	}
	if !sawShrink {
		t.Fatalf("expected a 'shrank' SourceError; got %v", errs)
	}
}
