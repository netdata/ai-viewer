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

// TestScan_UnknownTypePerVariantDedup verifies acceptance #2's stricter
// requirement (P2d): many occurrences of ONE unknown `type` surface exactly
// one SourceError, not one per occurrence. The prior implementation emitted
// per-occurrence, flooding /health for a transcript full of one new type.
func TestScan_UnknownTypePerVariantDedup(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-home-user-x")
	var b strings.Builder
	b.WriteString(`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}` + "\n")
	// 50 occurrences of the SAME unknown type.
	const occurrences = 50
	for i := 0; i < occurrences; i++ {
		b.WriteString(`{"type":"brand-new-future-type","sessionId":"s"}` + "\n")
	}
	writeFileBytes(t, filepath.Join(projDir, "s.jsonl"), []byte(b.String()))

	_, errs := collectErrors(t, tmp)
	unknownErrs := 0
	for _, e := range errs {
		if strings.Contains(e, "unknown record type") && strings.Contains(e, "brand-new-future-type") {
			unknownErrs++
		}
	}
	if unknownErrs != 1 {
		t.Fatalf("one unknown type repeated %d times surfaced %d SourceErrors, want exactly 1", occurrences, unknownErrs)
	}
}

// TestScan_SymlinkEscapeRefused pins P2e (spec §6.1, security.md §6): a
// transcript that is a symlink resolving OUTSIDE the projects root is refused
// with a SourceError and never read. A legitimate transcript in the same
// project dir is still ingested, proving the guard rejects only the escape.
func TestScan_SymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	// A secret file OUTSIDE the projects root, shaped like a real transcript
	// so it WOULD parse and emit a session if the guard failed to stop it.
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.jsonl")
	writeFileBytes(t, secretPath,
		[]byte(`{"type":"user","uuid":"x1","sessionId":"escaped","message":{"role":"user","content":"leak"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))

	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-x")
	// A legitimate in-root transcript.
	writeFileBytes(t, filepath.Join(proj, "ok.jsonl"),
		[]byte(`{"type":"user","uuid":"u1","sessionId":"ok","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	// A *.jsonl symlink inside the project dir pointing at the outside secret.
	escapeLink := filepath.Join(proj, "escape.jsonl")
	if err := os.Symlink(secretPath, escapeLink); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	events, errs := collectErrors(t, root)

	// The escaping session must NOT be ingested.
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == "escaped" {
			t.Fatalf("symlink escaping the root was read and emitted session %q (P2e breach)", ss.NativeID)
		}
	}
	// The legitimate session must still be ingested.
	var sawOK bool
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == "ok" {
			sawOK = true
		}
	}
	if !sawOK {
		t.Fatal("legitimate in-root transcript was not ingested; guard too aggressive")
	}
	// A containment SourceError must surface for the escape.
	var sawEscapeErr bool
	for _, e := range errs {
		if strings.Contains(e, "escape.jsonl") && (strings.Contains(e, "outside the projects root") || strings.Contains(e, "symlink escape")) {
			sawEscapeErr = true
		}
	}
	if !sawEscapeErr {
		t.Fatalf("no symlink-escape SourceError surfaced; errors=%v", errs)
	}
}

// TestScan_SymlinkMetaEscapeRefused pins P1.3 for the META read path: a
// subagent `.meta.json` that is a symlink resolving OUTSIDE the projects root
// must be refused with a SourceError and never read, so its (potentially
// sensitive) agentType/toolUseId is not absorbed into a session's extras. The
// in-root transcript and a legitimate sibling meta are still ingested.
func TestScan_SymlinkMetaEscapeRefused(t *testing.T) {
	t.Parallel()
	// A secret meta OUTSIDE the root, shaped like a real sidecar.
	outside := t.TempDir()
	secretMeta := filepath.Join(outside, "secret.meta.json")
	writeFileBytes(t, secretMeta, []byte(`{"agentType":"LEAKED-AGENT-TYPE","toolUseId":"toolu_leak"}`))

	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-x")
	// A legitimate subagent sidechain + a legitimate sibling meta.
	subDir := filepath.Join(proj, "sess-1", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.jsonl"),
		[]byte(`{"type":"user","uuid":"su1","isSidechain":true,"agentId":"aaa111bbb222ccc","sessionId":"sess-1","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}`+"\n"))
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.meta.json"),
		[]byte(`{"agentType":"general-purpose","toolUseId":"toolu_ok"}`))
	// A meta sidecar that is a SYMLINK escaping the root.
	escapeMeta := filepath.Join(subDir, "agent-evil000evil111e.meta.json")
	if err := os.Symlink(secretMeta, escapeMeta); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	events, errs := collectErrors(t, root)

	// No session's AgentName may carry the leaked agentType.
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.AgentName == "LEAKED-AGENT-TYPE" {
			t.Fatal("symlinked .meta.json escaping the root was read (P1.3 breach)")
		}
	}
	// The legitimate subagent (general-purpose) must still be ingested.
	child := "sess-1:agent:aaa111bbb222ccc"
	cs, ok := sessionStartedByNative(events, child)
	if !ok {
		t.Fatalf("legitimate subagent %q not started; guard too aggressive", child)
	}
	if cs.AgentName != "general-purpose" {
		t.Fatalf("legitimate subagent AgentName = %q, want general-purpose", cs.AgentName)
	}
	// A containment SourceError must surface for the escaping meta.
	var sawEscapeErr bool
	for _, e := range errs {
		if strings.Contains(e, "evil000evil111e.meta.json") &&
			(strings.Contains(e, "outside the projects root") || strings.Contains(e, "symlink escape")) {
			sawEscapeErr = true
		}
	}
	if !sawEscapeErr {
		t.Fatalf("no symlink-escape SourceError surfaced for the meta; errors=%v", errs)
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

// TestScan_OversizedLineSkippedContinues pins P2.5: a single line exceeding the
// scan buffer is surfaced as exactly one SourceError, skipped (bytes discarded
// up to and including the next newline), and reading CONTINUES — the valid
// record AFTER the oversized line must still be ingested. The pre-fix behavior
// jumped to EOF, silently discarding every later record in the file.
func TestScan_OversizedLineSkippedContinues(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	// [valid string-content user, > scanBufferMax line, valid string-content
	// user]: both valid records open a turn, so two TurnStarted events must
	// survive. (The canonical session native id derives from the FILENAME, so
	// both records map to one session; turn count is the observable that proves
	// the record AFTER the oversized line was still read.)
	big := strings.Repeat("x", scanBufferMax+1024)
	body := `{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"a"},"timestamp":"2026-05-26T10:00:00.000Z"}` + "\n" +
		big + "\n" +
		`{"type":"user","uuid":"u2","sessionId":"s","message":{"role":"user","content":"b"},"timestamp":"2026-05-26T10:00:02.000Z"}` + "\n"
	writeFileBytes(t, filepath.Join(proj, "s.jsonl"), []byte(body))

	events, errs := collectErrors(t, tmp)

	// Exactly one 'exceeds' SourceError for the single oversized line.
	tooLong := 0
	for _, e := range errs {
		if strings.Contains(e, "exceeds") {
			tooLong++
		}
	}
	if tooLong != 1 {
		t.Fatalf("oversized line surfaced %d 'exceeds' errors, want exactly 1; errs=%v", tooLong, errs)
	}
	// The session must be started (the record before the oversized line).
	if _, ok := sessionStartedByNative(events, "s"); !ok {
		t.Fatal("record before the oversized line was lost")
	}
	// BOTH valid records must open a turn — turn 2 proves the record AFTER the
	// oversized line was still read (P2.5: oversized line must not jump to EOF
	// and discard the rest of the file).
	if n := countKind(events, canonical.EvTurnStarted); n != 2 {
		t.Fatalf("turn_started = %d, want 2 (both valid records; oversized-line skip must continue, not jump to EOF, P2.5)", n)
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
