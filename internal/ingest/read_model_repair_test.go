package ingest

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

func TestRepairSourceReadModels_SourceFTSQueriesUseSessionScopedIndexes(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	assertRepairPlanUsesIndex(t, db, sourceSessionsForRepairQuery, "idx_sessions_source_id", "src", "", ftsBackfillBatchSize)
	assertRepairPlanUsesIndex(t, db, sessionOpsForFTSQuery, "idx_ops_session", "sess", int64(0), ftsBackfillBatchSize)
	assertRepairPlanUsesIndex(t, db, sessionLogsForFTSQuery, "idx_log_session", "sess", int64(0), ftsBackfillBatchSize)
}

func TestRepairSourceFTSYieldsBeforeDBWork(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	errYield := errors.New("yield before repair db work")
	_, err := repairSourceFTSOps(context.Background(), db, "src", func(context.Context) error {
		return errYield
	})
	if !errors.Is(err, errYield) {
		t.Fatalf("repairSourceFTSOps error = %v, want %v", err, errYield)
	}
}

func TestRepairSourceReadModelsRestoresFTSAfterDerivedTableClear(t *testing.T) {
	t.Parallel()

	const (
		format = "claude_code"
		src    = "claude_code:/repair-derived-clear"
	)
	ctx := context.Background()
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start, end := ts(0, 9, 0), ts(0, 9, 5)
	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/work", start, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start},
			SessionNativeID: "sess-1",
			Seq:             1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: start},
			SessionNativeID: "sess-1",
			TurnSeq:         1,
			Seq:             1,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Name:            "chat",
			Model:           "sonnetrepair",
			Provider:        "anthropic",
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: end},
			SessionNativeID: "sess-1",
			TurnSeq:         1,
			Seq:             1,
			Status:          "completed",
			EndTs:           end,
		},
		logEvent(src, "sess-1", "repair clear searchable log", ts(0, 9, 1), 5),
	}
	flushBatchFTS(t, db, src, format, true, batch)
	if got := matchOpIDs(t, db, "sonnetrepair"); len(got) != 1 {
		t.Fatalf("pre-clear MATCH sonnetrepair = %v, want one op", got)
	}
	if got := matchLogIDs(t, db, "searchable"); len(got) != 1 {
		t.Fatalf("pre-clear MATCH searchable = %v, want one log", got)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM fts_ops; DELETE FROM fts_logs`); err != nil {
		t.Fatalf("clear derived FTS rows: %v", err)
	}
	if got := matchOpIDs(t, db, "sonnetrepair"); len(got) != 0 {
		t.Fatalf("after clear MATCH sonnetrepair = %v, want none before repair", got)
	}
	if got := matchLogIDs(t, db, "searchable"); len(got) != 0 {
		t.Fatalf("after clear MATCH searchable = %v, want none before repair", got)
	}

	ing, err := New(db, WithLogger(silentLogger()), WithNow(fixedNow))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats, err := ing.RepairSourceReadModels(ctx, src)
	if err != nil {
		t.Fatalf("RepairSourceReadModels: %v", err)
	}
	if stats.FTSOpRows != 1 || stats.FTSLogRows != 1 {
		t.Fatalf("repair stats FTS rows = ops:%d logs:%d, want 1/1", stats.FTSOpRows, stats.FTSLogRows)
	}
	if got := matchOpIDs(t, db, "sonnetrepair"); len(got) != 1 {
		t.Fatalf("post-repair MATCH sonnetrepair = %v, want one op", got)
	}
	if got := matchLogIDs(t, db, "searchable"); len(got) != 1 {
		t.Fatalf("post-repair MATCH searchable = %v, want one log", got)
	}
}

func assertRepairPlanUsesIndex(t *testing.T, db *sql.DB, query, indexName string, args ...any) {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	joined := strings.Join(details, "\n")
	if strings.Contains(joined, "USE TEMP B-TREE") {
		t.Fatalf("source repair query uses temp sort:\n%s", joined)
	}
	if !strings.Contains(joined, indexName) {
		t.Fatalf("source repair query does not use %s:\n%s", indexName, joined)
	}
	if strings.Contains(joined, "SCAN ops") || strings.Contains(joined, "SCAN log_entries") {
		t.Fatalf("source repair query scans a full primary table:\n%s", joined)
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

func TestBackfillReadModelsDoesNotClearSourceDeferralFlags(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ing, err := New(db, WithLogger(silentLogger()), WithNow(fixedNow))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const deferredSource = "codex:/still-scanning"
	const readySource = "codex:/ready"
	if wasDeferred := ing.SetSourceReadModelsDeferred(deferredSource, true); wasDeferred {
		t.Fatal("new source deferral was already true")
	}
	if wasDeferred := ing.SetSourceReadModelsDeferred(readySource, false); wasDeferred {
		t.Fatal("ready source deferral was already true")
	}

	if err := ing.BackfillReadModels(context.Background()); err != nil {
		t.Fatalf("BackfillReadModels: %v", err)
	}

	if wasDeferred := ing.SetSourceReadModelsDeferred(deferredSource, true); !wasDeferred {
		t.Fatal("BackfillReadModels cleared a source-owned deferral flag")
	}
	if wasDeferred := ing.SetSourceReadModelsDeferred(readySource, false); wasDeferred {
		t.Fatal("BackfillReadModels changed a non-deferred source flag")
	}
}

func TestBackfillReadModelsFailureMarksAllSourcesRepairPending(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, db := openTestStore(t)
	const repairPendingAt = int64(9000)
	ing, err := New(db, WithLogger(silentLogger()), WithNow(func() int64 { return repairPendingAt }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sources := []string{"codex:/a", "claude-code:/b"}
	for idx, sourceID := range sources {
		if err := ensureSourceRowDirect(ctx, db, sourceID, strings.Split(sourceID, ":")[0], "/tmp/"+sourceID); err != nil {
			t.Fatalf("ensure source %s: %v", sourceID, err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO source_progress
    (source_id, updated_at, read_model_state, read_model_state_at, read_model_error)
VALUES (?, ?, ?, ?, ?)`,
			sourceID, int64(1000+idx), string(ReadModelReady), int64(2000+idx), "stale repair error"); err != nil {
			t.Fatalf("seed source_progress %s: %v", sourceID, err)
		}
	}
	chA := make(chan struct{}, 1)
	chB := make(chan struct{}, 1)
	unregisterA := ing.RegisterSourceReadModelRepair(sources[0], chA)
	defer unregisterA()
	unregisterB := ing.RegisterSourceReadModelRepair(sources[1], chB)
	defer unregisterB()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ing.BackfillReadModels(cancelledCtx); err == nil {
		t.Fatal("BackfillReadModels with cancelled context returned nil")
	}

	for idx, sourceID := range sources {
		var got struct {
			updatedAt        int64
			readModelState   string
			readModelStateAt int64
			readModelError   sql.NullString
			notifyCount      int64
		}
		if err := db.QueryRowContext(ctx, `
SELECT
  sp.updated_at,
  sp.read_model_state,
  sp.read_model_state_at,
  sp.read_model_error,
  (SELECT COUNT(*) FROM notify n
   WHERE n.kind='source_status_changed'
     AND n.source_id=sp.source_id
     AND n.ts_us=?)
FROM source_progress sp
WHERE sp.source_id=?`, repairPendingAt, sourceID).Scan(
			&got.updatedAt,
			&got.readModelState,
			&got.readModelStateAt,
			&got.readModelError,
			&got.notifyCount,
		); err != nil {
			t.Fatalf("read source_progress %s: %v", sourceID, err)
		}
		if got.updatedAt != int64(1000+idx) {
			t.Fatalf("%s updated_at = %d, want preserved %d", sourceID, got.updatedAt, int64(1000+idx))
		}
		if got.readModelState != string(ReadModelRepairPending) {
			t.Fatalf("%s read_model_state = %q, want %q", sourceID, got.readModelState, ReadModelRepairPending)
		}
		if got.readModelStateAt != repairPendingAt {
			t.Fatalf("%s read_model_state_at = %d, want %d", sourceID, got.readModelStateAt, repairPendingAt)
		}
		if got.readModelError.Valid {
			t.Fatalf("%s read_model_error = %q, want NULL", sourceID, got.readModelError.String)
		}
		if got.notifyCount != 1 {
			t.Fatalf("%s source_status_changed notify rows = %d, want 1", sourceID, got.notifyCount)
		}
	}
	select {
	case <-chA:
	default:
		t.Fatal("repair request missing for first source")
	}
	select {
	case <-chB:
	default:
		t.Fatal("repair request missing for second source")
	}
}

func TestBackfillReadModelsFailureDoesNotRefreshAlreadyPendingSources(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, db := openTestStore(t)
	const (
		originalPendingAt = int64(2000)
		globalFailureAt   = int64(9000)
		sourceID          = "codex:/already-pending"
	)
	ing, err := New(db, WithLogger(silentLogger()), WithNow(func() int64 { return globalFailureAt }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", "/tmp/already-pending"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO source_progress
    (source_id, updated_at, read_model_state, read_model_state_at, read_model_error)
VALUES (?, ?, ?, ?, ?)`,
		sourceID, int64(1000), string(ReadModelRepairPending), originalPendingAt, "existing repair evidence"); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}
	repairCh := make(chan struct{}, 1)
	unregister := ing.RegisterSourceReadModelRepair(sourceID, repairCh)
	defer unregister()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ing.BackfillReadModels(cancelledCtx); err == nil {
		t.Fatal("BackfillReadModels with cancelled context returned nil")
	}

	var got struct {
		state      string
		stateAt    int64
		errorText  sql.NullString
		notifyRows int64
	}
	if err := db.QueryRowContext(ctx, `
SELECT read_model_state,
       read_model_state_at,
       read_model_error,
       (SELECT COUNT(*) FROM notify WHERE kind='source_status_changed' AND source_id=? AND ts_us=?)
FROM source_progress
WHERE source_id=?`, sourceID, globalFailureAt, sourceID).Scan(
		&got.state,
		&got.stateAt,
		&got.errorText,
		&got.notifyRows,
	); err != nil {
		t.Fatalf("read source_progress: %v", err)
	}
	if got.state != string(ReadModelRepairPending) {
		t.Fatalf("read_model_state = %q, want %q", got.state, ReadModelRepairPending)
	}
	if got.stateAt != originalPendingAt {
		t.Fatalf("read_model_state_at = %d, want preserved %d", got.stateAt, originalPendingAt)
	}
	if !got.errorText.Valid || got.errorText.String != "existing repair evidence" {
		t.Fatalf("read_model_error = %+v, want existing repair evidence preserved", got.errorText)
	}
	if got.notifyRows != 0 {
		t.Fatalf("new source_status_changed rows = %d, want 0 for already repair_pending", got.notifyRows)
	}
	select {
	case <-repairCh:
	default:
		t.Fatal("repair request missing for already repair_pending source")
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
