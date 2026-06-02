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

// TestScan_AgentOpNotFinalizedTrailingNoOp pins P1.4: a child whose PHYSICAL
// last record is a skipped known-no-op (e.g. a trailing `summary` record) after
// an assistant-text record must NOT finalize its parent Agent op. The
// assistant-text record is not the physical last line, so the completion flag
// must reflect that and the parent op stays running. Before the fix, streamLines
// `continue`d past the skipped record without clearing lastRecordAssistantText,
// leaving it stale-true from the preceding assistant-text record.
func TestScan_AgentOpNotFinalizedTrailingNoOp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)
	// Child: a task, then an assistant-text record, then a trailing `summary`
	// no-op record (parseLine skips it). The physical last record is the no-op.
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
		`{"type":"summary","leafUuid":"cu1","summary":"trailing no-op"}`,
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	if of, ok := childOpFinalized(events); ok {
		t.Fatalf("Agent op finalized though the child's PHYSICAL last record is a skipped no-op, not assistant-text (P1.4): EndTs=%d", of.EndTs)
	}
}

// TestScan_AgentOpNotFinalizedTrailingMalformed pins P1.4 for the parse-error
// path: a child whose PHYSICAL last record is a malformed JSON line after an
// assistant-text record must NOT finalize its parent Agent op. streamLines must
// clear lastRecordAssistantText on the parse-error `continue` too.
func TestScan_AgentOpNotFinalizedTrailingMalformed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)
	// Child: a task, an assistant-text record, then a malformed JSON line as the
	// physical last record.
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
		`{"type":"assistant","this is not valid json`,
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	if of, ok := childOpFinalized(events); ok {
		t.Fatalf("Agent op finalized though the child's PHYSICAL last record is a malformed line, not assistant-text (P1.4): EndTs=%d", of.EndTs)
	}
}

// TestReadTranscript_OpensResolvedPath pins P2.4a: readTranscript opens the
// symlink-RESOLVED path, so the bytes read are the resolved target's, not the
// original link's. A symlinked transcript that resolves INSIDE the root is read
// correctly via its resolved path (the resolved path is what the containment
// guard returns and what is opened — no second, unresolved open).
func TestReadTranscript_OpensResolvedPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-x")
	// A real transcript, and a symlink to it WITHIN the same project dir. Both
	// resolve inside the root; the symlink must be read via its resolved target.
	realPath := filepath.Join(proj, "real.jsonl")
	writeFileBytes(t, realPath,
		[]byte(`{"type":"user","uuid":"u1","sessionId":"real","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	linkPath := filepath.Join(proj, "link.jsonl")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	events, errs := collectErrors(t, root)
	// Both the real and the (in-root) symlinked transcript are ingested; the
	// symlink's filename stem is its session native id.
	if _, ok := sessionStartedByNative(events, "real"); !ok {
		t.Fatal("real transcript not ingested")
	}
	if _, ok := sessionStartedByNative(events, "link"); !ok {
		t.Fatalf("in-root symlinked transcript not ingested via its resolved path; errs=%v", errs)
	}
}

// TestScan_MalformedMetaSurfacesError pins P2.4b: a PRESENT but malformed
// subagent `.meta.json` (invalid JSON) must surface a SourceError (no silent
// failure), so the failed toolUseId→agentId linkage repair is visible in
// /api/health. The legitimate transcript is still ingested.
func TestScan_MalformedMetaSurfacesError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-x")
	subDir := filepath.Join(proj, "sess-1", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.jsonl"),
		[]byte(`{"type":"user","uuid":"su1","isSidechain":true,"agentId":"aaa111bbb222ccc","sessionId":"sess-1","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}`+"\n"))
	// A present-but-malformed meta sidecar (invalid JSON).
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.meta.json"),
		[]byte(`{"agentType":"general-purpose", THIS IS NOT JSON`))

	_, errs := collectErrors(t, root)
	var sawMetaErr bool
	for _, e := range errs {
		if strings.Contains(e, "agent-aaa111bbb222ccc.meta.json") &&
			(strings.Contains(e, "parse") || strings.Contains(e, "meta")) {
			sawMetaErr = true
		}
	}
	if !sawMetaErr {
		t.Fatalf("malformed .meta.json did not surface a SourceError (P2.4b no silent failure); errs=%v", errs)
	}
}

// TestScan_OversizedMetaSurfacesError pins P2.6b (SOW-0003 Round 6): a PRESENT
// `.meta.json` sidecar larger than metaReadMax must NOT be read into memory — it is
// skipped with a SourceError. A `.meta.json` is normally a tiny object; an oversized
// one is pathological/hostile and must not force an unbounded allocation. Without the
// cap, os.ReadFile would slurp the whole file.
func TestScan_OversizedMetaSurfacesError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-x")
	subDir := filepath.Join(proj, "sess-1", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.jsonl"),
		[]byte(`{"type":"user","uuid":"su1","isSidechain":true,"agentId":"aaa111bbb222ccc","sessionId":"sess-1","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}`+"\n"))
	// A present meta sidecar whose size exceeds metaReadMax (valid JSON, but
	// padded past the cap so the only reason to reject it is the size guard).
	pad := make([]byte, metaReadMax+1)
	for i := range pad {
		pad[i] = 'x'
	}
	oversized := []byte(`{"agentType":"general-purpose","toolUseId":"toolu_1","pad":"` + string(pad) + `"}`)
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.meta.json"), oversized)

	_, errs := collectErrors(t, root)
	var sawCapErr bool
	for _, e := range errs {
		if strings.Contains(e, "agent-aaa111bbb222ccc.meta.json") &&
			(strings.Contains(e, "too large") || strings.Contains(e, "exceeds")) {
			sawCapErr = true
		}
	}
	if !sawCapErr {
		t.Fatalf("oversized .meta.json did not surface a size-cap SourceError (P2.6b); errs=%v", errs)
	}
}

// TestMetaHashes_SymlinkedRootDescends pins P2.6c (SOW-0003 Round 6): metaHashes must
// walk the symlink-RESOLVED root, because filepath.WalkDir does not descend INTO a
// symlinked walk-root. A projects root that is itself a symlink (e.g. ~/.claude → an
// external volume) would otherwise hash ZERO sidecars, silently breaking the
// rewrite-detection that drives the late-meta AgentName repair.
func TestMetaHashes_SymlinkedRootDescends(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	subDir := filepath.Join(realRoot, "-home-user-x", "sess-1", "subagents")
	writeFileBytes(t, filepath.Join(subDir, "agent-aaa111bbb222ccc.meta.json"),
		[]byte(`{"agentType":"Explore","toolUseId":"toolu_1"}`))

	// A symlinked projects root pointing at realRoot.
	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(linkRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	// Walk the UNRESOLVED (symlinked) root as the configured root; the resolved
	// root is threaded for containment + descent. With the fix, the walk descends
	// the resolved tree and finds the sidecar.
	hashes := metaHashes(linkRoot, resolved, func(error) {})
	if len(hashes) == 0 {
		t.Fatalf("metaHashes found 0 sidecars under a symlinked projects root (P2.6c — WalkDir did not descend the resolved root)")
	}
}

// TestCollectMetaPaths_WalkErrorSurfaced pins P2.9 (SOW-0003 Round 9): a
// non-IsNotExist WalkDir error over an unreadable meta subtree is surfaced via
// onError (no-silent-failures contract, so it shows in /api/health and the
// Sources panel) while the walk continues past it and still collects the
// sidecars it CAN read. An ABSENT dir is NOT an error. Tests the smallest seam
// (the walk callback's error handling) because forcing a real permission error
// is the most reliable way to exercise it in the sandbox.
func TestCollectMetaPaths_WalkErrorSurfaced(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission restrictions")
	}
	base := t.TempDir()
	subDir := filepath.Join(base, "sess-1", "subagents")
	// A readable sidecar alongside an unreadable nested subtree.
	readable := filepath.Join(subDir, "agent-readable000.meta.json")
	writeFileBytes(t, readable, []byte(`{"agentType":"Explore","toolUseId":"toolu_1"}`))
	blocked := filepath.Join(subDir, "nested")
	writeFileBytes(t, filepath.Join(blocked, "agent-hidden00000.meta.json"),
		[]byte(`{"agentType":"Plan","toolUseId":"toolu_2"}`))
	// Drop read+exec on the nested dir so WalkDir cannot descend into it: the
	// callback is invoked with a non-nil err for that path.
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	defer func() { _ = os.Chmod(blocked, 0o755) }() // restore so TempDir cleanup works

	var mu sync.Mutex
	var errs []string
	paths := collectMetaPaths(subDir, func(e error) {
		mu.Lock()
		errs = append(errs, e.Error())
		mu.Unlock()
	})

	// Some filesystems still allow descending a 0o000 dir; only assert the
	// error path when the unreadable subtree truly blocked the walk.
	if len(errs) == 0 {
		t.Skip("filesystem allowed descending an unreadable dir; walk-error seam not exercised")
	}
	sawWalkErr := false
	for _, e := range errs {
		if strings.Contains(e, "walk metas under") {
			sawWalkErr = true
		}
	}
	if !sawWalkErr {
		t.Fatalf("unreadable meta subtree did not surface a walk SourceError (P2.9); errs=%v", errs)
	}
	// Happy path unchanged: the readable sibling is still collected.
	foundReadable := false
	for _, p := range paths {
		if p == readable {
			foundReadable = true
		}
	}
	if !foundReadable {
		t.Fatalf("walk did not continue past the unreadable subtree to collect the readable sidecar; paths=%v", paths)
	}
}

// TestCollectMetaPaths_AbsentDirNoError pins the other half of P2.9: an absent
// meta dir (os.IsNotExist) is NOT an error — no onError call, empty result.
func TestCollectMetaPaths_AbsentDirNoError(t *testing.T) {
	t.Parallel()
	absent := filepath.Join(t.TempDir(), "does-not-exist", "subagents")
	var errs []string
	paths := collectMetaPaths(absent, func(e error) { errs = append(errs, e.Error()) })
	if len(errs) != 0 {
		t.Fatalf("absent meta dir surfaced %d error(s), want 0; errs=%v", len(errs), errs)
	}
	if len(paths) != 0 {
		t.Fatalf("absent meta dir returned %d paths, want 0", len(paths))
	}
}

// TestScan_DiscoveryFailsSoftOnBrokenSubtree pins P2.10 (SOW-0003 Round 10): a
// single unreadable subagent subtree must NOT abort discovery of the whole
// project/source. Discovery must (a) surface a SourceError for the broken entry
// (no-silent-failures contract → /api/health + Sources panel) AND (b) still
// discover the healthy main session and healthy subagent in the SAME project.
//
// Before the fix, discoverSessionSubagents returned the WalkDir error up,
// discoverProject did `return nil, serr`, and discoverTranscripts did
// `return nil, derr` — so ONE chmod-0o000 subagent dir zeroed out the entire
// scan (no sessions at all). Mirrors the Round-9 walk-error chmod seam
// (TestCollectMetaPaths_WalkErrorSurfaced) at the discovery level.
func TestScan_DiscoveryFailsSoftOnBrokenSubtree(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission restrictions")
	}
	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-src-demo")

	// A healthy main session transcript.
	writeFileBytes(t, filepath.Join(proj, "sess-ok.jsonl"),
		[]byte(`{"type":"user","uuid":"u1","sessionId":"sess-ok","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	// A healthy subagent sidechain under that session.
	okSub := filepath.Join(proj, "sess-ok", "subagents")
	writeFileBytes(t, filepath.Join(okSub, "agent-aaa111bbb222ccc.jsonl"),
		[]byte(`{"type":"user","uuid":"su1","isSidechain":true,"agentId":"aaa111bbb222ccc","sessionId":"sess-ok","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}`+"\n"))

	// A SECOND session whose subagents/ subtree is unreadable: a nested dir
	// chmod 0o000 so WalkDir cannot descend into it. The bad subtree is in a
	// DIFFERENT session dir, proving one broken entry does not poison the
	// healthy sibling's discovery.
	badSub := filepath.Join(proj, "sess-broken", "subagents", "nested")
	writeFileBytes(t, filepath.Join(badSub, "agent-hidden00000.jsonl"),
		[]byte(`{"type":"user","uuid":"hu1","isSidechain":true,"agentId":"hidden00000","sessionId":"sess-broken","message":{"role":"user","content":"x"},"timestamp":"2026-05-26T10:00:09.000Z"}`+"\n"))
	if err := os.Chmod(badSub, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	defer func() { _ = os.Chmod(badSub, 0o755) }() // restore so TempDir cleanup works

	events, errs := collectErrors(t, root)

	// Some filesystems still allow descending a 0o000 dir; only assert the
	// fail-soft error path when the unreadable subtree truly blocked the walk.
	sawWalkErr := false
	for _, e := range errs {
		if strings.Contains(e, "walk subagents under") {
			sawWalkErr = true
		}
	}
	if !sawWalkErr {
		t.Skip("filesystem allowed descending an unreadable dir; discovery walk-error seam not exercised")
	}

	// (b) Discovery must NOT have aborted: the healthy main session AND the
	// healthy subagent are still ingested despite the broken sibling subtree.
	if _, ok := sessionStartedByNative(events, "sess-ok"); !ok {
		t.Fatal("healthy main session was lost — one broken subagent subtree aborted the whole scan (P2.10 breach)")
	}
	if _, ok := sessionStartedByNative(events, "sess-ok:agent:aaa111bbb222ccc"); !ok {
		t.Fatal("healthy subagent was lost — one broken subagent subtree aborted discovery (P2.10 breach)")
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

// TestScan_AgentOpNotFinalizedTrailingOversized pins P1.5a: a child whose
// PHYSICAL last record is an oversized (> scanBufferMax) line after an
// assistant-text record must NOT finalize its parent Agent op. The
// errLineTooLong skip path must clear lastRecordAssistantText (like the
// parse-error and skipped-no-op paths) so the stale-true flag from the preceding
// assistant-text record does not wrongly complete the child.
func TestScan_AgentOpNotFinalizedTrailingOversized(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)
	// Child: a task, an assistant-text record (the §485 marker), then an oversized
	// line as the physical last record (with a trailing newline so the offset
	// advances cleanly past it).
	big := strings.Repeat("x", scanBufferMax+1024)
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
		big,
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	if of, ok := childOpFinalized(events); ok {
		t.Fatalf("Agent op finalized though the child's PHYSICAL last record is an oversized line, not assistant-text (P1.5a): EndTs=%d", of.EndTs)
	}
}

// TestScan_StaleParkedCompletionRetracted pins P1.5b: a child that completes
// (terminal assistant-text) BEFORE its parent Agent op is known parks its
// completion; if the child then GROWS a non-text terminal record (a trailing
// tool_use) BEFORE the parent op appears, the park must be RETRACTED so the
// parent op is NOT finalized when it finally lands. Without the delete-on-
// not-complete in collectAgentDeferral the stale park would wrongly finalize.
func TestScan_StaleParkedCompletionRetracted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Step 1: child complete, parent transcript + meta absent → the child parks.
	// (The meta carries the toolUseId join; without the parent transcript there is
	// no Agent op yet, so the completed child has nowhere to pair → it parks.)
	writeFileBytes(t, metaPath(root),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+parentAgentToolUseID+`"}`))
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))

	a1, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	out1 := make(chan canonical.Event, 256)
	if err := a1.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	events1 := drainBuffered(out1)
	cursor := lastCursor(t, events1)
	parsed1, err := ParseCursor(cursor)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	wantChild := childNativeID(durParentSession, durAgentID)
	if _, ok := parsed1.Parked[wantChild]; !ok {
		t.Fatalf("child did not park after completing with no known parent op; parked=%v", parsed1.Parked)
	}

	// Step 2: the child GROWS a trailing tool_use record → it is no longer
	// complete (its physical last record is now a tool_use, not assistant-text).
	appendFileBytes(t, childPath(root),
		[]byte(childToolUseLine("ct1", "toolu_child_grow", "Read", "2026-05-26T10:00:11.000Z")+"\n"))
	// And NOW the parent transcript appears (its Agent tool_use op).
	writeParentWithAgentOp(t, root)

	// Step 3: resume Scan from the persisted cursor. The child is re-read (now NOT
	// complete) → its stale park must be RETRACTED; the parent op is read → known.
	// The parent op must NOT finalize (the child is no longer complete).
	a2, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	out2 := make(chan canonical.Event, 256)
	if err := a2.Scan(context.Background(), parsed1, out2); err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	events2 := drainBuffered(out2)
	if of, ok := childOpFinalized(events2); ok {
		t.Fatalf("parent Agent op finalized from a STALE parked completion though the child grew a non-text terminal record (P1.5b): EndTs=%d", of.EndTs)
	}
	// The retraction must also clear the persisted park so a later restart cannot
	// resurrect it.
	cursor2 := lastCursor(t, events2)
	parsed2, err := ParseCursor(cursor2)
	if err != nil {
		t.Fatalf("ParseCursor #2: %v", err)
	}
	if _, ok := parsed2.Parked[wantChild]; ok {
		t.Fatalf("stale park survived in the cursor after retraction (P1.5b); parked=%v", parsed2.Parked)
	}
}

// TestEarliestTs_OpensResolvedPathAndRefusesEscape pins P2.5b: earliestTs opens
// the symlink-RESOLVED path within the resolved root, and refuses (returns 0) a
// path that resolves OUTSIDE the root. An in-root real file yields its first
// record's ts; an out-of-root symlink target is not read.
func TestEarliestTs_OpensResolvedPathAndRefusesEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	// In-root real transcript: earliestTs returns its first record's ts.
	proj := filepath.Join(root, "-home-user-x")
	inRoot := filepath.Join(proj, "real.jsonl")
	writeFileBytes(t, inRoot,
		[]byte(`{"type":"user","uuid":"u1","sessionId":"real","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`+"\n"))
	wantTs, _ := parseTsToMicros("2026-05-26T10:00:00.000Z")
	if got := earliestTs(resolvedRoot, inRoot); got != wantTs {
		t.Fatalf("earliestTs(in-root) = %d, want %d", got, wantTs)
	}

	// Out-of-root file, reached via an in-tree symlink: must be refused (0).
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeFileBytes(t, outside,
		[]byte(`{"type":"user","uuid":"u2","sessionId":"evil","message":{"role":"user","content":"x"},"timestamp":"2026-05-26T11:00:00.000Z"}`+"\n"))
	link := filepath.Join(proj, "escape.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	if got := earliestTs(resolvedRoot, link); got != 0 {
		t.Fatalf("earliestTs(out-of-root symlink) = %d, want 0 (P2.5b containment)", got)
	}
}
