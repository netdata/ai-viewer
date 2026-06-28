package ingest

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestRepairSourceReadModels_RebuildsDeferredSourceFTSAndFormatRollups(t *testing.T) {
	t.Parallel()

	const format = "codex"
	const srcA = "codex:/repair-a"
	const srcB = "codex:/repair-b"

	_, db := openTestStore(t)
	hour := ts(0, 10, 0)
	endA := ts(0, 10, 5)
	endB := ts(0, 10, 7)

	flushDeferredReadModelBatch(t, db, srcA, format, fixedNow(), []canonical.Event{
		sessionStartEvent(srcA, "sess-a", "agent-a", "/work/a", hour, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: srcA, SourceSeq: 2, Ts: hour},
			SessionNativeID: "sess-a",
			Seq:             1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: srcA, SourceSeq: 3, Ts: hour},
			SessionNativeID: "sess-a",
			TurnSeq:         1,
			Seq:             1,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Name:            "chat",
			Model:           "uniquerepairmodela",
			Provider:        "openai",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: srcA, SourceSeq: 4, Ts: endA},
			SessionNativeID: "sess-a",
			TurnSeq:         1,
			Seq:             1,
			Status:          "completed",
			EndTs:           endA,
			TokensIn:        10,
			TokensOut:       20,
			CostUSD:         0.01,
		},
		logEvent(srcA, "sess-a", "uniquerepairloga", hour, 5),
	})

	flushDeferredReadModelBatch(t, db, srcB, format, fixedNow(), []canonical.Event{
		sessionStartEvent(srcB, "sess-b", "agent-b", "/work/b", hour, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: srcB, SourceSeq: 2, Ts: hour},
			SessionNativeID: "sess-b",
			Seq:             1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: srcB, SourceSeq: 3, Ts: hour},
			SessionNativeID: "sess-b",
			TurnSeq:         1,
			Seq:             1,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Name:            "chat",
			Model:           "uniquerepairmodelb",
			Provider:        "openai",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: srcB, SourceSeq: 4, Ts: endB},
			SessionNativeID: "sess-b",
			TurnSeq:         1,
			Seq:             1,
			Status:          "completed",
			EndTs:           endB,
			TokensIn:        1,
			TokensOut:       2,
			CostUSD:         0.02,
		},
		logEvent(srcB, "sess-b", "uniquerepairlogb", hour, 5),
	})

	if got := scanInt(t, db, `SELECT COUNT(*) FROM fts_ops`); got != 0 {
		t.Fatalf("precondition fts_ops rows = %d, want 0 while read models deferred", got)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM fts_logs`); err != nil {
		t.Fatalf("clear fts_logs before repair: %v", err)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly`); got != 0 {
		t.Fatalf("precondition rollup_hourly rows = %d, want 0 while read models deferred", got)
	}

	ing, err := New(db, WithLogger(silentLogger()), WithNow(fixedNow))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats, err := ing.RepairSourceReadModels(context.Background(), srcA)
	if err != nil {
		t.Fatalf("RepairSourceReadModels: %v", err)
	}
	if stats.FTSOpRows != 1 || stats.FTSLogRows != 1 {
		t.Fatalf("FTS repair stats = ops:%d logs:%d, want 1/1 for repaired source", stats.FTSOpRows, stats.FTSLogRows)
	}
	if stats.HourlyBuckets != 1 || stats.DailyBuckets != 1 {
		t.Fatalf("rollup repair stats = hourly:%d daily:%d, want 1/1 touched buckets", stats.HourlyBuckets, stats.DailyBuckets)
	}

	if got := matchOpIDs(t, db, "uniquerepairmodela"); len(got) != 1 {
		t.Fatalf("source A FTS op match = %v, want one repaired op", got)
	}
	if got := matchOpIDs(t, db, "uniquerepairmodelb"); len(got) != 0 {
		t.Fatalf("source B FTS op match = %v, want none before source B repair", got)
	}
	if got := matchLogIDs(t, db, "uniquerepairloga"); len(got) != 1 {
		t.Fatalf("source A FTS log match = %v, want one repaired log", got)
	}
	if got := matchLogIDs(t, db, "uniquerepairlogb"); len(got) != 0 {
		t.Fatalf("source B FTS log match = %v, want none before source B repair", got)
	}

	hourly := readRollups(t, db, "rollup_hourly")
	total := hourly[rollupKey{bucketTS: hour, sourceFormat: format, dimension: "total"}]
	if total.opCount != 2 {
		t.Fatalf("hourly total op_count = %d, want 2 from both same-format sources", total.opCount)
	}
	if total.sessionStarts != 2 {
		t.Fatalf("hourly total session_starts = %d, want 2 from both same-format sources", total.sessionStarts)
	}
	if total.tokensIn != 11 || total.tokensOut != 22 {
		t.Fatalf("hourly tokens = in:%d out:%d, want 11/22 from both same-format sources", total.tokensIn, total.tokensOut)
	}

	daily := readRollups(t, db, "rollup_daily")
	day := ts(0, 0, 0)
	dailyTotal := daily[rollupKey{bucketTS: day, sourceFormat: format, dimension: "total"}]
	if dailyTotal.opCount != 2 {
		t.Fatalf("daily total op_count = %d, want 2 from both same-format sources", dailyTotal.opCount)
	}
}

func TestRepairSourceReadModels_UnknownSourceIsNoop(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ing, err := New(db, WithLogger(silentLogger()), WithNow(fixedNow))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats, err := ing.RepairSourceReadModels(context.Background(), "missing-source")
	if err != nil {
		t.Fatalf("RepairSourceReadModels missing source: %v", err)
	}
	if stats != (SourceReadModelRepairStats{}) {
		t.Fatalf("missing source stats = %+v, want zero", stats)
	}
}

func TestWorkerSkipsDerivedRefreshDuringGlobalRebuildAndMarksRepairPending(t *testing.T) {
	t.Parallel()

	const sourceID = "codex:/global-rebuild"
	const format = "codex"
	_, db := openTestStore(t)
	ing, err := New(db, WithLogger(silentLogger()), WithNow(fixedNow))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ing.readModelRebuildActive.Store(true)

	flag := &atomic.Bool{}
	wr := newWriter(sourceID, format, "/loc", NopPricer{})
	wr.now = fixedNow
	wr.deferReadModels = flag
	wr.readModelRebuildActive = &ing.readModelRebuildActive
	w := &worker{
		sourceID:               sourceID,
		sourceFormat:           format,
		location:               "/loc",
		fts5IndexLogs:          true,
		db:                     db,
		hwm:                    newHWMCache(),
		pricer:                 NopPricer{},
		logger:                 silentLogger(),
		batchSize:              defaultBatchSize,
		batchEvery:             defaultBatchInterval,
		now:                    fixedNow,
		deferReadModels:        flag,
		readModelRebuildActive: &ing.readModelRebuildActive,
	}
	var repairRequests atomic.Int64
	w.requestReadModelRepair = func(gotSourceID string) bool {
		if gotSourceID != sourceID {
			t.Fatalf("repair request source_id = %q, want %q", gotSourceID, sourceID)
		}
		repairRequests.Add(1)
		return true
	}
	if err := w.flush(context.Background(), wr, []canonical.Event{
		sessionStartEvent(sourceID, "sess-rebuild", "agent", "/work", ts(0, 10, 0), 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: sourceID, SourceSeq: 2, Ts: ts(0, 10, 0)},
			SessionNativeID: "sess-rebuild",
			Seq:             1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: sourceID, SourceSeq: 3, Ts: ts(0, 10, 1)},
			SessionNativeID: "sess-rebuild",
			TurnSeq:         1,
			Seq:             1,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Name:            "global-rebuild-op",
			Model:           "global-rebuild-model",
			Provider:        "openai",
		},
		canonical.SourceProgressEvent{
			EventBase: canonical.EventBase{SourceID: sourceID, SourceSeq: 4, Ts: ts(0, 10, 2)},
			Cursor:    `{"tail":1}`,
		},
	}); err != nil {
		t.Fatalf("worker.flush during global rebuild: %v", err)
	}

	if got := scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE name='global-rebuild-op'`); got != 1 {
		t.Fatalf("canonical ops rows = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='global-rebuild-op'`); got != 0 {
		t.Fatalf("fts_ops rows = %d, want 0 while global rebuild is active", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly WHERE source_format=?`, format); got != 0 {
		t.Fatalf("rollup_hourly rows = %d, want 0 while global rebuild is active", got)
	}
	if got := scanString(t, db, `SELECT read_model_state FROM source_progress WHERE source_id=?`, sourceID); got != string(ReadModelRepairPending) {
		t.Fatalf("read_model_state = %q, want %q", got, ReadModelRepairPending)
	}
	if got := repairRequests.Load(); got != 1 {
		t.Fatalf("repair requests after committed rebuild debt = %d, want 1", got)
	}
	if got := scanString(t, db, `SELECT IFNULL(cursor, '') FROM source_progress WHERE source_id=?`, sourceID); got != `{"tail":1}` {
		t.Fatalf("cursor = %q, want primary progress committed", got)
	}
}

func flushDeferredReadModelBatch(t *testing.T, db *sql.DB, sourceID, format string, now int64, batch []canonical.Event) {
	t.Helper()
	flag := &atomic.Bool{}
	flag.Store(true)
	wr := newWriter(sourceID, format, "/loc", NopPricer{})
	wr.now = func() int64 { return now }
	wr.deferReadModels = flag
	w := &worker{
		sourceID:        sourceID,
		sourceFormat:    format,
		location:        "/loc",
		fts5IndexLogs:   true,
		db:              db,
		hwm:             newHWMCache(),
		pricer:          NopPricer{},
		logger:          silentLogger(),
		batchSize:       defaultBatchSize,
		batchEvery:      defaultBatchInterval,
		now:             func() int64 { return now },
		deferReadModels: flag,
	}
	if err := w.flush(context.Background(), wr, batch); err != nil {
		t.Fatalf("worker.flush deferred read models: %v", err)
	}
	wr.resetBatch()
}
