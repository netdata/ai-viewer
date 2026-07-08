package ingest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
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

// TestResolver_LoopSkipsSessionPassesWhenOnlyOps pins SOW-0117's session-pass
// gate: when the ingester's generation counter advances (new batch) but the
// session watermark (MAX(last_activity_ts)) is UNCHANGED, the loop must run
// linkOrphansGated with sessionsChanged=false — i.e. skip the two session
// passes (linkParents + the O(sessions) linkRoots recursive CTE) and run only
// the op-child passes. This is the common op-heavy ingestion case.
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
