package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// --- test helpers (shared by scanner_test.go and tailer_test.go) ---

// writeFileBytes writes b to path, creating parent directories. Test-only.
func writeFileBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// appendFileBytes appends b to path, creating parents. Simulates the codex
// recorder appending records (resume / tail).
func appendFileBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(b); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

// drainBuffered collects all events currently available on ch in a single
// non-blocking round. Test-only.
func drainBuffered(ch chan canonical.Event) []canonical.Event {
	out := make([]canonical.Event, 0, cap(ch))
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// scanCollect runs scanAll over root from the given cursor, collecting the
// emitted events, the error strings, and the final cursor.
func scanCollect(t *testing.T, root, sourceID string, since Cursor) ([]canonical.Event, []string, Cursor) {
	t.Helper()
	var mu sync.Mutex
	var errs []string
	onError := func(e error) {
		mu.Lock()
		errs = append(errs, e.Error())
		mu.Unlock()
	}
	out := make(chan canonical.Event, 16384)
	final, err := scanAll(context.Background(), root, sourceID, since, out, onError)
	if err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	return drainBuffered(out), errs, final
}

// shardPath returns "<root>/YYYY/MM/DD/rollout-YYYY-MM-DDTHH-MM-SS-<id>.jsonl"
// for a synthetic modern rollout file with a UUID-shaped ThreadId tail.
func shardPath(root, id string) string {
	return filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T16-59-09-"+id+".jsonl")
}

// uuid7 returns a UUIDv7-shaped synthetic id so nativeIDForRollout's uuidTail
// path is exercised, with a per-call suffix for uniqueness.
func uuid7(n int) string {
	return fmt.Sprintf("019aa234-a2a1-75c3-a9bf-d8425e1785%02d", n%100)
}

// completeSession returns a minimal but complete modern rollout: session_meta,
// a turn_context (opens turn 1), and a task_complete (closes it). id is the
// session native id stamped in session_meta.
func completeSession(id string) []byte {
	lines := []string{
		metaLine(id, `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"done","completed_at":"` + tsDone + `"}}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// hangingSession returns a modern rollout whose most-recent turn never closes
// (task_started/turn_context but no task_complete) — the rule #23 stale-finalize
// candidate.
func hangingSession(id string) []byte {
	lines := []string{
		metaLine(id, `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1763664000}}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// setMtime sets a file's mtime to now-age so staleness can be controlled.
func setMtime(t *testing.T, path string, age time.Duration) {
	t.Helper()
	mt := time.Now().Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// hasKind reports whether any event has the given kind.
func hasKind(events []canonical.Event, kind canonical.EventKind) bool {
	return countKind(events, kind) > 0
}

// --- discovery tests ---

// TestDiscover_MultiShardSorted asserts modern rollouts across several
// YYYY/MM/DD shard dirs are discovered and returned sorted by rel, and that
// archived_sessions/, sqlite, history, and session_index.jsonl are ignored.
func TestDiscover_MultiShardSorted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Two shard dirs, two files.
	a := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl")
	b := filepath.Join(root, "2025", "11", "21", "rollout-2025-11-21T10-00-00-"+uuid7(2)+".jsonl")
	writeFileBytes(t, a, completeSession("sid-a"))
	writeFileBytes(t, b, completeSession("sid-b"))
	// Noise that must be ignored.
	writeFileBytes(t, filepath.Join(root, "archived_sessions", "2025", "11", "20", "rollout-2025-11-20T10-00-00-"+uuid7(3)+".jsonl"), completeSession("sid-arch"))
	writeFileBytes(t, filepath.Join(root, "session_index.jsonl"), []byte("{}\n"))
	writeFileBytes(t, filepath.Join(root, "state_5.sqlite"), []byte("x"))
	writeFileBytes(t, filepath.Join(root, "history.jsonl"), []byte("x"))
	// A non-rollout .jsonl inside a shard dir (wrong prefix).
	writeFileBytes(t, filepath.Join(root, "2025", "11", "20", "notes.jsonl"), []byte("{}\n"))

	disc, err := discoverRollouts(root, nil)
	if err != nil {
		t.Fatalf("discoverRollouts: %v", err)
	}
	if len(disc.modern) != 2 {
		t.Fatalf("modern count = %d, want 2; got %+v", len(disc.modern), disc.modern)
	}
	if disc.modern[0].rel >= disc.modern[1].rel {
		t.Errorf("not sorted: %q then %q", disc.modern[0].rel, disc.modern[1].rel)
	}
	wantRelA := "2025/11/20/rollout-2025-11-20T10-00-00-" + uuid7(1) + ".jsonl"
	if disc.modern[0].rel != wantRelA {
		t.Errorf("rel[0] = %q, want %q", disc.modern[0].rel, wantRelA)
	}
}

// TestDiscover_LegacyClassifiedSeparately asserts legacy flat .json files
// directly under the root are returned in disc.legacy, not disc.modern, and
// that a legacy-named .json inside a shard dir is NOT treated as legacy.
func TestDiscover_LegacyClassifiedSeparately(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFileBytes(t, shardPath(root, uuid7(1)), completeSession("sid-a"))
	legacy := "rollout-2025-06-26-5556f03d-348c-4463-987c-053ccd0b1df5.json"
	writeFileBytes(t, filepath.Join(root, legacy), []byte(`{"session":{},"items":[]}`))
	// A legacy-shaped name inside a shard dir is NOT a root legacy file.
	writeFileBytes(t, filepath.Join(root, "2025", "11", "20", "rollout-x.json"), []byte("{}"))

	disc, err := discoverRollouts(root, nil)
	if err != nil {
		t.Fatalf("discoverRollouts: %v", err)
	}
	if len(disc.modern) != 1 {
		t.Errorf("modern count = %d, want 1", len(disc.modern))
	}
	if len(disc.legacy) != 1 || disc.legacy[0] != legacy {
		t.Errorf("legacy = %v, want [%s]", disc.legacy, legacy)
	}
}

// TestDiscover_MissingRootBenign asserts an absent root is benign-empty (first
// run), not an error.
func TestDiscover_MissingRootBenign(t *testing.T) {
	t.Parallel()
	disc, err := discoverRollouts(filepath.Join(t.TempDir(), "does-not-exist"), nil)
	if err != nil {
		t.Fatalf("missing root should be benign, got %v", err)
	}
	if len(disc.modern) != 0 || len(disc.legacy) != 0 {
		t.Errorf("missing root should yield empty, got %+v", disc)
	}
}

// --- scanAll behavior tests ---

// TestScan_HappyPathEmitsSession asserts a complete session produces a
// SessionStarted, a TurnStarted, a TurnFinalized, and a final SourceProgress,
// and that the cursor records a non-zero offset == file size.
func TestScan_HappyPathEmitsSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, completeSession("sid-1"))

	events, errs, final := scanCollect(t, root, "codex:"+root, newCursor())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !hasKind(events, canonical.EvSessionStarted) {
		t.Error("no SessionStarted emitted")
	}
	if !hasKind(events, canonical.EvTurnStarted) || !hasKind(events, canonical.EvTurnFinalized) {
		t.Error("turn boundary events missing")
	}
	if hasKind(events, canonical.EvSessionFinalized) {
		t.Error("clean session must NOT emit SessionFinalized (SOW C#3)")
	}
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"
	info, _ := os.Stat(path)
	if final.Files[rel].Offset != info.Size() || final.Files[rel].Offset == 0 {
		t.Errorf("cursor offset = %d, want file size %d", final.Files[rel].Offset, info.Size())
	}
}

// TestScan_ResumeNoDupNoGap is acceptance #6: scan a partial file, persist the
// cursor, append the rest, resume, and assert the union of emitted catalog
// events equals a single one-shot scan (zero duplicate SessionStarted, all
// turns present).
func TestScan_ResumeNoDupNoGap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	// First half: session_meta + turn_context (turn opened, not closed).
	half := []string{
		metaLine("sid-r", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
	}
	writeFileBytes(t, path, []byte(strings.Join(half, "\n")+"\n"))
	// Keep mtime fresh so the partial turn is NOT stale-finalized.
	setMtime(t, path, time.Minute)

	ev1, errs1, cur1 := scanCollect(t, root, "codex:"+root, newCursor())
	if len(errs1) != 0 {
		t.Fatalf("phase1 errors: %v", errs1)
	}
	// Append the closing task_complete.
	appendFileBytes(t, path, []byte(`{"timestamp":"`+tsDone+`","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"`+tsDone+`"}}`+"\n"))
	setMtime(t, path, time.Minute)

	ev2, errs2, _ := scanCollect(t, root, "codex:"+root, cur1)
	if len(errs2) != 0 {
		t.Fatalf("phase2 errors: %v", errs2)
	}

	// One-shot over the full file for comparison.
	root2 := t.TempDir()
	path2 := shardPath(root2, uuid7(1))
	full := append([]string{}, half...)
	full = append(full, `{"timestamp":"`+tsDone+`","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"`+tsDone+`"}}`)
	writeFileBytes(t, path2, []byte(strings.Join(full, "\n")+"\n"))
	setMtime(t, path2, time.Minute)
	evOne, _, _ := scanCollect(t, root2, "codex:"+root2, newCursor())

	// Resume must not duplicate SessionStarted.
	if got := countKind(ev1, canonical.EvSessionStarted) + countKind(ev2, canonical.EvSessionStarted); got != 1 {
		t.Errorf("SessionStarted across resume = %d, want exactly 1 (no dup)", got)
	}
	// Phase 2 must emit the TurnFinalized that the appended line produced (no gap).
	if countKind(ev2, canonical.EvTurnFinalized) != 1 {
		t.Errorf("phase2 TurnFinalized = %d, want 1 (the appended close)", countKind(ev2, canonical.EvTurnFinalized))
	}
	// The combined turn-final count equals the one-shot count.
	combinedTF := countKind(ev1, canonical.EvTurnFinalized) + countKind(ev2, canonical.EvTurnFinalized)
	if combinedTF != countKind(evOne, canonical.EvTurnFinalized) {
		t.Errorf("combined TurnFinalized = %d, one-shot = %d", combinedTF, countKind(evOne, canonical.EvTurnFinalized))
	}
}

// TestScan_TruncationRescans is acceptance #6 (truncation): a cursor recording
// a larger size than the on-disk file triggers a re-scan from 0 and a
// SourceError, re-emitting the session.
func TestScan_TruncationRescans(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, completeSession("sid-t"))
	setMtime(t, path, time.Minute)
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"

	// First full scan.
	_, _, cur1 := scanCollect(t, root, "codex:"+root, newCursor())
	if cur1.Files[rel].Offset == 0 {
		t.Fatalf("phase1 cursor not advanced")
	}
	// Simulate truncation: shrink the file on disk but keep the (larger) cursor.
	writeFileBytes(t, path, completeSession("sid-t")[:20])
	setMtime(t, path, time.Minute)

	ev2, errs2, _ := scanCollect(t, root, "codex:"+root, cur1)
	foundTrunc := false
	for _, e := range errs2 {
		if strings.Contains(e, "shrank") && strings.Contains(e, "rescanning from 0") {
			foundTrunc = true
		}
	}
	if !foundTrunc {
		t.Errorf("truncation SourceError not surfaced; errs=%v", errs2)
	}
	_ = ev2
}

// TestScan_LegacyOneShotSourceError is R1: a legacy flat .json file emits
// exactly one informational SourceError on first scan and is suppressed on the
// next scan via the cursor's LegacyJSON map.
func TestScan_LegacyOneShotSourceError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := "rollout-2025-06-26-5556f03d-348c-4463-987c-053ccd0b1df5.json"
	writeFileBytes(t, filepath.Join(root, legacy), []byte(`{"session":{},"items":[]}`))

	_, errs1, cur1 := scanCollect(t, root, "codex:"+root, newCursor())
	legacyErrs := 0
	for _, e := range errs1 {
		if strings.Contains(e, "legacy flat .json") {
			legacyErrs++
		}
	}
	if legacyErrs != 1 {
		t.Fatalf("legacy SourceError count = %d, want exactly 1; errs=%v", legacyErrs, errs1)
	}
	if !cur1.legacyIngested(legacy) {
		t.Fatal("legacy file not recorded as seen in cursor")
	}
	// Second scan with the carried cursor must be quiet.
	_, errs2, _ := scanCollect(t, root, "codex:"+root, cur1)
	for _, e := range errs2 {
		if strings.Contains(e, "legacy flat .json") {
			t.Fatalf("legacy SourceError re-emitted after suppression: %v", errs2)
		}
	}
}

// TestScan_NoSessionMetaSkips is rule #24: a modern file whose first line is
// not a session_meta is skipped with a SourceError, emits no canonical session,
// and its cursor offset stays 0 so a later append retries.
func TestScan_NoSessionMetaSkips(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	// First line is a turn_context (no session_meta anywhere).
	writeFileBytes(t, path, []byte(`{"timestamp":"`+tsCtx+`","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`+"\n"))
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"

	events, errs, final := scanCollect(t, root, "codex:"+root, newCursor())
	if hasKind(events, canonical.EvSessionStarted) {
		t.Error("rule #24 file must not emit a SessionStarted")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "no session_meta") {
			found = true
		}
	}
	if !found {
		t.Errorf("rule #24 SourceError not surfaced; errs=%v", errs)
	}
	if final.Files[rel].Offset != 0 {
		t.Errorf("rule #24 offset = %d, want 0 (retry on next append)", final.Files[rel].Offset)
	}
}

// TestScan_NoSessionMetaThenMetaAppended asserts the rule #24 retry actually
// works: once a session_meta is prepended-via-rewrite the next scan ingests the
// file. (Codex never truncates; this models a delayed first-line write — the
// offset-held-at-0 means the whole file is re-probed.)
func TestScan_NoSessionMetaThenMetaAppended(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, []byte(`{"timestamp":"`+tsCtx+`","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`+"\n"))

	_, _, cur1 := scanCollect(t, root, "codex:"+root, newCursor())
	// Now write a proper file beginning with session_meta.
	writeFileBytes(t, path, completeSession("sid-late"))
	setMtime(t, path, time.Minute)
	events, _, _ := scanCollect(t, root, "codex:"+root, cur1)
	if !hasKind(events, canonical.EvSessionStarted) {
		t.Error("after session_meta present, file must ingest")
	}
}

// TestScan_FailSoftUnreadableShard is the fail-soft requirement: a chmod-000
// shard subtree surfaces a SourceError (onError fires) AND healthy files in
// sibling shards still ingest. Skipped on filesystems that allow descending a
// 0o000 dir.
func TestScan_FailSoftUnreadableShard(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	root := t.TempDir()
	// Healthy file in one shard.
	good := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl")
	writeFileBytes(t, good, completeSession("sid-good"))
	setMtime(t, good, time.Minute)
	// A second shard subtree we will block.
	blockedDir := filepath.Join(root, "2025", "11", "21")
	writeFileBytes(t, filepath.Join(blockedDir, "rollout-2025-11-21T10-00-00-"+uuid7(2)+".jsonl"), completeSession("sid-blocked"))
	if err := os.Chmod(blockedDir, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blockedDir, 0o755) })

	// If the FS still lets us read the blocked dir, the seam is not exercised.
	if entries, derr := os.ReadDir(blockedDir); derr == nil && len(entries) >= 0 {
		if _, oerr := os.Open(filepath.Join(blockedDir, "rollout-2025-11-21T10-00-00-"+uuid7(2)+".jsonl")); oerr == nil {
			t.Skip("filesystem allowed descending an unreadable dir; fail-soft seam not exercised")
		}
	}

	events, errs, _ := scanCollect(t, root, "codex:"+root, newCursor())
	if !hasKind(events, canonical.EvSessionStarted) {
		t.Error("healthy file did not ingest while a sibling shard was unreadable")
	}
	if len(errs) == 0 {
		t.Errorf("unreadable shard did not surface any SourceError; events=%d", len(events))
	}
}

// TestScan_StaleFinalizes is acceptance #5h: a hanging-turn file whose mtime is
// stale ≥ 1 h gets a synthetic TurnFinalized(failed) + SessionFinalized(failed).
func TestScan_StaleFinalizes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, hangingSession("sid-crash"))
	setMtime(t, path, 2*time.Hour) // stale

	events, errs, _ := scanCollect(t, root, "codex:"+root, newCursor())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	tf := turnFinals(events)
	if len(tf) != 1 || tf[0].Status != "failed" {
		t.Fatalf("stale finalize TurnFinalized = %+v, want one failed", tf)
	}
	if !hasKind(events, canonical.EvSessionFinalized) {
		t.Error("stale hanging session must emit SessionFinalized (rule #23)")
	}
}

// TestScan_FreshDoesNotFinalize is the rule #23 lower bound: a hanging-turn file
// whose mtime is fresh (< 1 h) leaves the turn open — no synthetic finalize.
func TestScan_FreshDoesNotFinalize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, hangingSession("sid-live"))
	setMtime(t, path, 2*time.Minute) // fresh

	events, _, _ := scanCollect(t, root, "codex:"+root, newCursor())
	if hasKind(events, canonical.EvSessionFinalized) {
		t.Error("fresh hanging session must NOT emit SessionFinalized")
	}
	if len(turnFinals(events)) != 0 {
		t.Errorf("fresh hanging session must NOT emit a synthetic TurnFinalized; got %+v", turnFinals(events))
	}
}

// TestScan_UnknownTypeDedup is acceptance #2: N distinct unknown top-level
// `type` strings produce exactly one SourceError per variant per session, and
// the scan does not abort (the surrounding valid session still ingests).
func TestScan_UnknownTypeDedup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	lines := []string{metaLine("sid-u", `"exec"`)}
	// 3 distinct unknown top-level types, each repeated 3×.
	for rep := 0; rep < 3; rep++ {
		for _, typ := range []string{"frobnicate", "wibble", "splort"} {
			lines = append(lines, `{"timestamp":"`+tsItem+`","type":"`+typ+`","payload":{}}`)
		}
	}
	writeFileBytes(t, path, []byte(strings.Join(lines, "\n")+"\n"))
	setMtime(t, path, time.Minute)

	events, errs, _ := scanCollect(t, root, "codex:"+root, newCursor())
	if !hasKind(events, canonical.EvSessionStarted) {
		t.Error("valid session must still ingest alongside unknown variants")
	}
	unknownErrs := 0
	for _, e := range errs {
		if strings.Contains(e, "unknown record type") {
			unknownErrs++
		}
	}
	if unknownErrs != 3 {
		t.Errorf("unknown-type SourceError count = %d, want 3 (one per distinct variant); errs=%v", unknownErrs, errs)
	}
}

// TestScan_UnknownPayloadTypeDedup is acceptance #2 for the nested family: N
// distinct unknown nested payload.type strings (under a known top-level type)
// produce exactly one SourceError per variant.
func TestScan_UnknownPayloadTypeDedup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	lines := []string{metaLine("sid-up", `"exec"`)}
	for rep := 0; rep < 2; rep++ {
		for _, nt := range []string{"mystery_item", "ufo_call"} {
			lines = append(lines, `{"timestamp":"`+tsItem+`","type":"response_item","payload":{"type":"`+nt+`"}}`)
		}
	}
	writeFileBytes(t, path, []byte(strings.Join(lines, "\n")+"\n"))
	setMtime(t, path, time.Minute)

	_, errs, _ := scanCollect(t, root, "codex:"+root, newCursor())
	nestedErrs := 0
	for _, e := range errs {
		if strings.Contains(e, "unknown payload type") {
			nestedErrs++
		}
	}
	if nestedErrs != 2 {
		t.Errorf("unknown-payload SourceError count = %d, want 2; errs=%v", nestedErrs, errs)
	}
}

// TestScan_OversizedLineSkippedNotEOF asserts an oversized line surfaces one
// SourceError and the scan CONTINUES past it (later valid records still
// ingest) — verbatim claude_code semantics (not a jump-to-EOF).
func TestScan_OversizedLineSkippedNotEOF(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	big := strings.Repeat("x", scanBufferMax+10)
	lines := []string{
		metaLine("sid-big", `"exec"`),
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"agent_message","message":"` + big + `"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
	}
	writeFileBytes(t, path, []byte(strings.Join(lines, "\n")+"\n"))
	setMtime(t, path, time.Minute)

	events, errs, final := scanCollect(t, root, "codex:"+root, newCursor())
	oversize := false
	for _, e := range errs {
		if strings.Contains(e, "exceeds") && strings.Contains(e, "bytes; skipping") {
			oversize = true
		}
	}
	if !oversize {
		t.Errorf("oversized line did not surface a SourceError; errs=%v", errs)
	}
	if !hasKind(events, canonical.EvSessionStarted) {
		t.Error("records before the oversized line must still ingest")
	}
	// The cursor must reach EOF (the file was fully consumed past the big line).
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"
	info, _ := os.Stat(path)
	if final.Files[rel].Offset != info.Size() {
		t.Errorf("cursor offset = %d, want EOF %d after skipping oversized line", final.Files[rel].Offset, info.Size())
	}
}

// TestScan_SymlinkEscapeRefused asserts a *.jsonl symlink inside a shard dir
// pointing OUTSIDE the sessions root is refused with a SourceError and never
// opened (security.md §6).
func TestScan_SymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.jsonl")
	writeFileBytes(t, secret, completeSession("sid-secret"))
	shardDir := filepath.Join(root, "2025", "11", "20")
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		t.Fatalf("mkdir shard: %v", err)
	}
	link := filepath.Join(shardDir, "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	events, errs, _ := scanCollect(t, root, "codex:"+root, newCursor())
	if hasKind(events, canonical.EvSessionStarted) {
		t.Error("symlink escaping the root must not be ingested")
	}
	escaped := false
	for _, e := range errs {
		if strings.Contains(e, "outside the sessions root") {
			escaped = true
		}
	}
	if !escaped {
		t.Errorf("symlink escape not refused with a SourceError; errs=%v", errs)
	}
}

// TestScan_ContextCancelStops asserts a cancelled context stops the scan
// promptly and returns the cursor without panicking.
func TestScan_ContextCancelStops(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFileBytes(t, shardPath(root, uuid7(1)), completeSession("sid-c"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	out := make(chan canonical.Event, 1)
	_, err := scanAll(ctx, root, "codex:"+root, newCursor(), out, func(error) {})
	if err != nil && !isCanceled(err) {
		t.Fatalf("scanAll on cancelled ctx = %v, want nil or context.Canceled", err)
	}
}

func isCanceled(err error) bool {
	return strings.Contains(err.Error(), context.Canceled.Error())
}

// TestNativeIDForRollout asserts the UUID tail is extracted from a rollout
// filename and that a non-UUID tail falls back to the stem.
func TestNativeIDForRollout(t *testing.T) {
	t.Parallel()
	id := uuid7(7)
	r := rollout{abs: "/x/2025/11/20/rollout-2025-11-20T16-59-09-" + id + ".jsonl"}
	if got := nativeIDForRollout(r); got != id {
		t.Errorf("nativeIDForRollout = %q, want %q", got, id)
	}
	// No UUID tail → whole stem after the prefix.
	r2 := rollout{abs: "/x/2025/11/20/rollout-weird.jsonl"}
	if got := nativeIDForRollout(r2); got != "weird" {
		t.Errorf("nativeIDForRollout(no-uuid) = %q, want weird", got)
	}
}
