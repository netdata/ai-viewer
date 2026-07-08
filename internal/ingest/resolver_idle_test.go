package ingest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestResolver_LoopSkipsWhenNoNewIngestion pins SOW-0117's idle-skip: the
// resolver loop must NOT advance lastSeenGen (i.e. must skip linkOrphans) when
// the ingester's ingestion generation counter is unchanged since the last pass
// (nothing committed → nothing could need linking). It runs on the first tick
// (lastSeenGen starts at -1) and again only after the counter advances.
func TestResolver_LoopSkipsWhenNoNewIngestion(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), 20*time.Millisecond)

	// Generation counter stable at 5: after the first tick consumes it, every
	// subsequent tick must skip (lastSeenGen must NOT drift).
	var gen atomic.Int64
	gen.Store(5)
	r.ingestionGenNow = gen.Load

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.loop(ctx)

	if !waitFor(time.Second, func() bool { return r.lastSeenGen.Load() == 5 }) {
		t.Fatalf("first tick did not consume gen 5: lastSeenGen=%d", r.lastSeenGen.Load())
	}
	// Several more ticks with the counter UNCHANGED → all must skip (no drift).
	time.Sleep(80 * time.Millisecond)
	if r.lastSeenGen.Load() != 5 {
		t.Fatalf("lastSeenGen drifted without a counter advance: got %d, want 5 (skip failed)", r.lastSeenGen.Load())
	}
}

// TestResolver_LoopRunsWhenGenerationAdvances pins that a counter advance
// between ticks causes the next tick to run linkOrphans (no skip).
func TestResolver_LoopRunsWhenGenerationAdvances(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), 20*time.Millisecond)

	var gen atomic.Int64
	gen.Store(10)
	r.ingestionGenNow = gen.Load

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.loop(ctx)

	// first tick runs (lastSeenGen -1 → 10)
	if !waitFor(time.Second, func() bool { return r.lastSeenGen.Load() == 10 }) {
		t.Fatalf("first tick did not consume gen 10: lastSeenGen=%d", r.lastSeenGen.Load())
	}
	// Advance: next tick must consume it.
	gen.Store(11)
	if !waitFor(time.Second, func() bool { return r.lastSeenGen.Load() == 11 }) {
		t.Fatalf("tick after advance did not consume gen 11: lastSeenGen=%d", r.lastSeenGen.Load())
	}
}

// TestResolver_NilGenerationAlwaysRuns pins the test/default contract: a
// resolver with no ingestionGenNow wired (bare newResolver, tests, direct
// linkOrphans callers) runs linkOrphans unconditionally on every tick — the
// skip is opt-in via the ingester wiring, never a silent default.
func TestResolver_NilGenerationAlwaysRuns(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), 20*time.Millisecond)
	if r.ingestionGenNow != nil {
		t.Fatalf("bare newResolver must leave ingestionGenNow nil (default = always run)")
	}
	if r.lastSeenGen.Load() != -1 {
		t.Fatalf("bare newResolver lastSeenGen: want -1 (first tick runs), got %d", r.lastSeenGen.Load())
	}
}

// TestResolver_LinkOrphansGatedSkipsSessionPasses pins that sessionsChanged=false
// ACTUALLY skips the session link passes (linkParents + linkRoots), not just the
// watermark bookkeeping. It seeds a nested parent/child whose root the resolver
// would transitively fix, then asserts linkOrphansGated(false) leaves it unfixed
// (session passes skipped) while linkOrphansGated(true) fixes it.
func TestResolver_LinkOrphansGatedSkipsSessionPasses(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "codex", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "codex", "/tmp", NopPricer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for _, ev := range []canonical.SessionStartedEvent{
		{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000}, NativeID: "root", RootNativeID: "root", Kind: canonical.KindRoot, AgentName: "root"},
		{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 2000}, NativeID: "parent", RootNativeID: "root", ParentNativeID: "root", Kind: canonical.KindSubAgent, AgentName: "parent"},
		{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 3000}, NativeID: "child", RootNativeID: "parent", ParentNativeID: "parent", Kind: canonical.KindSubAgent, AgentName: "child"},
	} {
		if err := w.apply(ctx, tx, ev); err != nil {
			t.Fatalf("apply %s: %v", ev.NativeID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rootID := canonicalSessionID(src, "root")
	childID := canonicalSessionID(src, "child")

	r := newResolver(db, silentLogger(), time.Minute)
	// sessionsChanged=false → session passes skipped → child stays provisionally
	// rooted at its direct parent, NOT the transitive root.
	if err := r.linkOrphansGated(ctx, false); err != nil {
		t.Fatalf("linkOrphansGated(false): %v", err)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got == rootID {
		t.Fatalf("sessionsChanged=false must SKIP linkRoots, but child root was transitively fixed to %q", got)
	}
	// sessionsChanged=true → session passes run → child re-rooted to the true root.
	if err := r.linkOrphansGated(ctx, true); err != nil {
		t.Fatalf("linkOrphansGated(true): %v", err)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != rootID {
		t.Fatalf("sessionsChanged=true must run linkRoots: child root = %q, want %q", got, rootID)
	}
}

// TestResolver_LoopSkipsSessionPassesWhenOnlyOps pins the loop's WATERMARK
// BOOKKEEPING when an op-only batch committed: the generation counter advanced
// (so linkOrphansGated runs) but a mocked session watermark is static (so the
// loop records it once and does not drift). It uses a MOCK sessionWatermarkNow
// by design — it tests the loop's gating logic, not the real MAX(last_activity_ts)
// behavior. The real session-pass skip is pinned by
// TestResolver_LinkOrphansGatedSkipsSessionPasses; the real watermark signal
// (monotonic, advances on session insert/update + aggregate refresh) is a
// property of the writer/aggregate refresh, exercised by the writer tests and
// the production measurement in SOW-0117.
func TestResolver_LoopSkipsSessionPassesWhenOnlyOps(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), 20*time.Millisecond)

	// gen keeps advancing (ops committed) but session watermark is static.
	var gen atomic.Int64
	gen.Store(100)
	r.ingestionGenNow = func() int64 { return gen.Add(1) }                            // always advances
	r.sessionWatermarkNow = func(context.Context) (int64, error) { return 9999, nil } // static

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.loop(ctx)

	// After the first tick the session watermark (9999) is recorded. Subsequent
	// ticks: gen advances (so linkOrphansGated runs) but session watermark is
	// unchanged → sessionsChanged=false → session passes skipped.
	waitFor(time.Second, func() bool { return r.lastSeenSession.Load() == 9999 })
	time.Sleep(80 * time.Millisecond) // several more ticks with static watermark
	if r.lastSeenSession.Load() != 9999 {
		t.Fatalf("session watermark drifted without a session change: got %d, want 9999", r.lastSeenSession.Load())
	}
}

// TestResolver_LoopRunsSessionPassesWhenWatermarkAdvances pins that a session
// watermark advance causes the next tick to run the session passes.
func TestResolver_LoopRunsSessionPassesWhenWatermarkAdvances(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), 20*time.Millisecond)

	var gen atomic.Int64
	gen.Store(200)
	r.ingestionGenNow = func() int64 { return gen.Add(1) }
	var sessWM atomic.Int64
	sessWM.Store(1)
	r.sessionWatermarkNow = func(context.Context) (int64, error) { return sessWM.Load(), nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.loop(ctx)

	waitFor(time.Second, func() bool { return r.lastSeenSession.Load() == 1 })
	// Session change: watermark advances → next tick records it.
	sessWM.Store(2)
	waitFor(time.Second, func() bool { return r.lastSeenSession.Load() == 2 })
}
