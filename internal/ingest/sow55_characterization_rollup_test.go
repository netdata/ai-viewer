package ingest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/rollups"
)

// SOW-0055 characterization pins for rollup_refresh.go carry-forward behaviour.

// TestRefreshRollups_CharacterizationCarriedSetAcrossMultipleRefreshes pins
// the rollup_refresh.go carry-forward invariant: dirty HOUR and DAY buckets
// carried across multiple refresh passes remain independently tracked. Three
// refreshes are run: open hour+day (both skipped), hour closed (hour
// materialized, day retained), day closed (day materialized).
func TestRefreshRollups_CharacterizationCarriedSetAcrossMultipleRefreshes(t *testing.T) {
	const src, format = "claude_code:/tmp", "claude_code"
	hourH := ts(0, 10, 0)
	nowOpenBoth := ts(0, 10, 15)
	nowHourClosed := ts(0, 11, 5)
	nowDayClosed := ts(1, 0, 30)

	wr, db, clk := sow55SetupRollupRefreshCarried(t, src, format, hourH, nowOpenBoth)

	assertCarriedState1BothOpen(t, db, wr, hourH, format)

	clk.now = nowHourClosed
	mustRefreshRollups(t, db, wr)
	assertCarriedState2HourClosed(t, db, wr, hourH, format)

	clk.now = nowDayClosed
	mustRefreshRollups(t, db, wr)
	assertCarriedState3DayClosed(t, db, wr, hourH, format)
}

// ----- rollup refresh helpers -----

func sow55SetupRollupRefreshCarried(t *testing.T, src, format string, hourH, nowOpenBoth int64) (*writer, *sql.DB, *mutableClock) {
	t.Helper()
	_, db := openTestStore(t)
	seedSource(t, db, src, format)
	clk := &mutableClock{now: nowOpenBoth}
	wr := newWriter(src, format, "/loc", NopPricer{})
	wr.now = clk.Now

	hourHEnd := ts(0, 10, 30)
	batch := []canonical.Event{
		sessionStartEvent(src, "s", "claude", "/w", hourH, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
			SessionNativeID: "s", Seq: 1,
		},
	}
	batch = append(batch, llmOpEvents(src, "s", 1, 1, hourH, hourHEnd, "m", "p", 5, 5, 0, false)...)
	flushBatchReuse(t, db, wr, src, format, batch)
	return wr, db, clk
}

// mustRefreshRollups opens a tx, runs wr.refreshRollups, commits, promotes the
// staged carry-set removals, and resets per-batch state — mirroring
// worker.refreshRollupsOnly's production semantics.
func mustRefreshRollups(t *testing.T, db *sql.DB, wr *writer) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := wr.refreshRollups(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("refreshRollups: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	wr.promoteMaterializedRollupBuckets()
	wr.resetBatch()
}

func assertCarriedState1BothOpen(t *testing.T, db *sql.DB, wr *writer, hourH int64, format string) {
	t.Helper()
	if _, ok := wr.dirtyRollupBuckets[rollups.BucketTS(hourH, rollups.Hourly)]; !ok {
		t.Fatal("expected hour H carried after first flush (open hour)")
	}
	if _, ok := wr.dirtyRollupDays[rollups.BucketTS(hourH, rollups.Daily)]; !ok {
		t.Fatal("expected day0 carried after first flush (open day)")
	}
	if _, ok := readRollups(t, db, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; ok {
		t.Fatal("premise broken: open hour materialized")
	}
}

func assertCarriedState2HourClosed(t *testing.T, db *sql.DB, wr *writer, hourH int64, format string) {
	t.Helper()
	if _, ok := readRollups(t, db, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; !ok {
		t.Fatal("hour H not materialized after it closed (SOW-0055 hour pass split must keep this)")
	}
	if _, ok := wr.dirtyRollupBuckets[rollups.BucketTS(hourH, rollups.Hourly)]; ok {
		t.Fatal("hour H still in carried HOUR set after materialization (must be removed)")
	}
	if _, ok := wr.dirtyRollupDays[rollups.BucketTS(hourH, rollups.Daily)]; !ok {
		t.Fatal("day0 dropped from carried DAY set while still open (round-8 P1 regression)")
	}
	if _, ok := readRollups(t, db, "rollup_daily")[rollupKey{rollups.BucketTS(hourH, rollups.Daily), format, "total", ""}]; ok {
		t.Fatal("open day materialized in rollup_daily (open-day cutoff broken)")
	}
}

func assertCarriedState3DayClosed(t *testing.T, db *sql.DB, wr *writer, hourH int64, format string) {
	t.Helper()
	dayBucket := rollups.BucketTS(hourH, rollups.Daily)
	if _, ok := readRollups(t, db, "rollup_daily")[rollupKey{dayBucket, format, "total", ""}]; !ok {
		t.Fatal("day0 not materialized after it closed (SOW-0055 day pass split must keep this)")
	}
	if _, ok := wr.dirtyRollupDays[dayBucket]; ok {
		t.Fatal("day0 still in carried DAY set after materialization (must be removed)")
	}
	if wr.hasPendingRollupBuckets() {
		t.Fatal("carried sets not empty after both hour and day materialized")
	}
}
