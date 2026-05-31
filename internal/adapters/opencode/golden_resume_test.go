package opencode

import (
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the AC#3 cumulative-token invariant (a direct, golden-text-
// independent assertion of the per-op delta sequence) and the SCENARIO-LEVEL
// resume golden (AC#6 durability over a committed fixture.sql).
//
// Relationship to chunk C's TestScanLoop_ResumeZeroDupesZeroGaps: that test
// builds the DB in two INSERT stages on a throwaway DB and proves
// union(part1,part2)==cold-baseline. This chunk-E test pins the COMPLEMENTARY
// resume properties the brief calls out for a STATIC fixture (where two-stage
// seeding is impossible): (1) a re-scan from the FINAL cursor emits ZERO new
// content events (idempotent re-scan), and (2) two cold scans from the zero
// cursor emit identical content — together "resume/re-scan never drops or
// duplicates a content event". It uses the same eventFingerprint/
// contentFingerprints/multisetDiff helpers (defined in tailer_resume_test.go).

// TestGoldenInvariant_ECumulativeTokens is the AC#3 regression, asserted on the
// scanned events (independent of the golden bytes). The four step-finish parts
// carry CUMULATIVE inputs 100/250/410/400 and outputs 20/50/90/80; the per-LLM-op
// deltas MUST be 100/150/160/0 and 20/30/40/0 (the 4th clamps because the
// cumulative decreased). A regression to raw-value emission would make the
// sequence 100/250/410/400 and fail here.
func TestGoldenInvariant_ECumulativeTokens(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "e_cumulative_tokens")

	// op_finalized events for the four LLM ops, in op-seq order.
	fins := opFinals(ev)
	bySeq := map[int]canonical.OpFinalizedEvent{}
	for _, f := range fins {
		bySeq[f.Seq] = f
	}
	wantIn := []int64{100, 150, 160, 0}
	wantOut := []int64{20, 30, 40, 0}
	for i := 0; i < 4; i++ {
		seq := i + 1
		f, ok := bySeq[seq]
		if !ok {
			t.Fatalf("missing op_finalized seq %d", seq)
		}
		if f.TokensIn != wantIn[i] {
			t.Errorf("op seq %d TokensIn = %d, want %d (cumulative->delta)", seq, f.TokensIn, wantIn[i])
		}
		if f.TokensOut != wantOut[i] {
			t.Errorf("op seq %d TokensOut = %d, want %d", seq, f.TokensOut, wantOut[i])
		}
	}

	// The per-turn rollup is the message-level cumulative (first turn = own total).
	tf := turnFinals(ev)
	if len(tf) != 1 {
		t.Fatalf("turn_finalized count = %d, want 1", len(tf))
	}
	if tf[0].TokensIn != 400 || tf[0].TokensOut != 80 {
		t.Errorf("turn tokens = %d/%d, want 400/80 (message-level cumulative)", tf[0].TokensIn, tf[0].TokensOut)
	}
}

// TestGoldenInvariant_ResumeIdempotentReScan pins AC#6 over a static fixture: a
// cold scan to the final cursor, then a SECOND scan from that persisted+reparsed
// cursor, emits ZERO new content events — every row is at-or-below the watermark,
// so nothing re-emits and nothing is dropped. This is the "no duplicate on
// restart" half of the durability contract for the SQLite adapter (the ingester's
// idempotent upserts would absorb a re-emission, but the cursor must prevent one
// in the first place when there is no new data).
func TestGoldenInvariant_ResumeIdempotentReScan(t *testing.T) {
	t.Parallel()
	dbPath := buildFixtureDB(t, fixtureSQLPath("a_happy"))

	// Cold scan from zero → final cursor + baseline content.
	out1 := make(chan canonical.Event, 8192)
	var ce1 collectErrs
	final, err := scanLoop(ctxBG(), dbPath, "opencode:x", newCursor(), out1, silentLogger(), ce1.onError)
	if err != nil {
		t.Fatalf("cold scanLoop: %v", err)
	}
	baseline := contentFingerprints(drainAll(out1))
	if len(baseline) == 0 {
		t.Fatal("cold scan produced no content events")
	}

	// Persist + reparse the cursor (the durable round-trip the ingester performs).
	reparsed, err := ParseCursor(final.String())
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}

	// Re-scan from the final cursor → expect ZERO content events (no new rows).
	out2 := make(chan canonical.Event, 8192)
	var ce2 collectErrs
	if _, err := scanLoop(ctxBG(), dbPath, "opencode:x", reparsed, out2, silentLogger(), ce2.onError); err != nil {
		t.Fatalf("re-scan from final cursor: %v", err)
	}
	resumed := contentFingerprints(drainAll(out2))
	if len(resumed) != 0 {
		t.Errorf("re-scan from final cursor emitted %d content events, want 0 (no dup on restart):\n%v", len(resumed), resumed)
	}
}

// TestGoldenInvariant_ResumeFromZeroIsDeterministic pins the other half of AC#6:
// two independent cold scans from the zero cursor over the SAME fixture emit the
// IDENTICAL content multiset (no gap, no nondeterministic drop/duplicate). Run on
// the two-turn multi-provider fixture so the determinism covers multi-turn
// ordering and the cumulative-delta math, not just a single turn.
func TestGoldenInvariant_ResumeFromZeroIsDeterministic(t *testing.T) {
	t.Parallel()
	dbPath := buildFixtureDB(t, fixtureSQLPath("c_multi_provider"))

	scan := func() []string {
		out := make(chan canonical.Event, 8192)
		var ce collectErrs
		if _, err := scanLoop(ctxBG(), dbPath, "opencode:x", newCursor(), out, silentLogger(), ce.onError); err != nil {
			t.Fatalf("scanLoop: %v", err)
		}
		return contentFingerprints(drainAll(out))
	}

	first := scan()
	second := scan()
	if len(first) == 0 {
		t.Fatal("scan produced no content events")
	}
	if diff := multisetDiff(first, second); diff != "" {
		t.Fatalf("two cold scans differ (nondeterministic drop/dup):\n%s", diff)
	}
}

// TestGoldenInvariant_ResumeMultiSessionFinalCursor pins the multi-session case: a
// cold scan over the parent+child fixture, then a re-scan from the final cursor
// re-emits NEITHER session (both fully consumed). This guards against a watermark
// that fails to advance past one of several sessions touched in a single cycle —
// which would re-walk a completed session on every poll.
func TestGoldenInvariant_ResumeMultiSessionFinalCursor(t *testing.T) {
	t.Parallel()
	dbPath := buildFixtureDB(t, fixtureSQLPath("b_subagent_task"))

	out1 := make(chan canonical.Event, 8192)
	var ce1 collectErrs
	final, err := scanLoop(ctxBG(), dbPath, "opencode:x", newCursor(), out1, silentLogger(), ce1.onError)
	if err != nil {
		t.Fatalf("cold scanLoop: %v", err)
	}
	base := drainAll(out1)
	// Sanity: the cold scan saw BOTH sessions.
	if !sessionPresent(base, "ses_parent01") || !sessionPresent(base, "ses_child01") {
		t.Fatalf("cold scan missing a session; parent=%v child=%v",
			sessionPresent(base, "ses_parent01"), sessionPresent(base, "ses_child01"))
	}

	reparsed, err := ParseCursor(final.String())
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	out2 := make(chan canonical.Event, 8192)
	var ce2 collectErrs
	if _, err := scanLoop(ctxBG(), dbPath, "opencode:x", reparsed, out2, silentLogger(), ce2.onError); err != nil {
		t.Fatalf("re-scan from final cursor: %v", err)
	}
	resumed := contentFingerprints(drainAll(out2))
	if len(resumed) != 0 {
		t.Errorf("re-scan re-emitted %d events across 2 fully-consumed sessions, want 0:\n%v", len(resumed), resumed)
	}
}

// sessionPresent reports whether a SessionStartedEvent for nativeID is in the slice.
func sessionPresent(events []canonical.Event, nativeID string) bool {
	for _, s := range sessionStarts(events) {
		if s.NativeID == nativeID {
			return true
		}
	}
	return false
}

// fixtureSQLPath returns the repo-relative path to a scenario's fixture.sql.
func fixtureSQLPath(scenario string) string {
	return filepath.Join("..", "..", "..", "testdata", "opencode", scenario, "fixture.sql")
}
