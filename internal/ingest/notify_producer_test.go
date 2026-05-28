package ingest

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// notifyRow mirrors one row of the notify change-log table for test
// assertions.
type notifyRow struct {
	seq    int64
	tsUS   int64
	kind   string
	sess   string
	root   string
	source string
}

// readNotify returns every notify row ordered by seq.
func readNotify(t *testing.T, db *sql.DB) []notifyRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT seq, ts_us, kind, IFNULL(session_id,''), IFNULL(root_session_id,''), IFNULL(source_id,'') FROM notify ORDER BY seq`)
	if err != nil {
		t.Fatalf("query notify: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []notifyRow
	for rows.Next() {
		var r notifyRow
		if err := rows.Scan(&r.seq, &r.tsUS, &r.kind, &r.sess, &r.root, &r.source); err != nil {
			t.Fatalf("scan notify: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate notify: %v", err)
	}
	return out
}

// TestEmitNotify_SessionChangedPerAffectedSession asserts that after a
// batch touching two distinct sessions, emitNotify writes one
// session_changed row per session (root_session_id + batch commit ts_us)
// plus one stats_invalidated row (rollups changed via the session writes).
func TestEmitNotify_SessionChangedPerAffectedSession(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	const commitTS = int64(9_000_000)

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// Two root sessions; root_session_id == id for each.
	for _, nid := range []string{"s1", "s2"} {
		if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
			NativeID:     nid,
			RootNativeID: nid,
			Kind:         canonical.KindRoot,
		}); err != nil {
			t.Fatalf("apply %s: %v", nid, err)
		}
	}
	if err := w.emitNotify(ctx, tx, commitTS); err != nil {
		t.Fatalf("emitNotify: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got := readNotify(t, db)

	sessChanged := map[string]notifyRow{}
	statsCount := 0
	for _, r := range got {
		switch r.kind {
		case "session_changed":
			sessChanged[r.sess] = r
		case "stats_invalidated":
			statsCount++
		default:
			t.Errorf("unexpected notify kind %q", r.kind)
		}
	}

	if len(sessChanged) != 2 {
		t.Fatalf("session_changed rows = %d, want 2 (%+v)", len(sessChanged), got)
	}
	for _, nid := range []string{"s1", "s2"} {
		wantID := canonicalSessionID(src, nid)
		r, ok := sessChanged[wantID]
		if !ok {
			t.Fatalf("missing session_changed for %s (id=%s); got %+v", nid, wantID, got)
		}
		if r.root != wantID {
			t.Errorf("%s root_session_id = %q, want %q", nid, r.root, wantID)
		}
		if r.tsUS != commitTS {
			t.Errorf("%s ts_us = %d, want %d (batch commit ts)", nid, r.tsUS, commitTS)
		}
	}
	if statsCount != 1 {
		t.Errorf("stats_invalidated rows = %d, want exactly 1 per batch", statsCount)
	}
}

// TestEmitNotify_RootSessionIDFromRow asserts session_changed carries the
// child's resolved root_session_id (which differs from its own id), read
// back from the sessions row written in this tx.
func TestEmitNotify_RootSessionIDFromRow(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// Parent first so the child's RootNativeID resolves to the parent id.
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "parent",
		RootNativeID: "parent",
		Kind:         canonical.KindRoot,
	}); err != nil {
		t.Fatalf("apply parent: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1100},
		NativeID:       "child",
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
	}); err != nil {
		t.Fatalf("apply child: %v", err)
	}
	if err := w.emitNotify(ctx, tx, 5000); err != nil {
		t.Fatalf("emitNotify: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	parentID := canonicalSessionID(src, "parent")
	childID := canonicalSessionID(src, "child")
	for _, r := range readNotify(t, db) {
		if r.kind != "session_changed" {
			continue
		}
		switch r.sess {
		case parentID:
			if r.root != parentID {
				t.Errorf("parent root = %q, want %q", r.root, parentID)
			}
		case childID:
			if r.root != parentID {
				t.Errorf("child root = %q, want %q (parent id)", r.root, parentID)
			}
		}
	}
}

// TestEmitNotify_SourceStatusChangedOnParseError asserts a batch that
// bumps a source's parse_errors (via a SourceErrorEvent) emits exactly
// one source_status_changed row carrying the source_id.
func TestEmitNotify_SourceStatusChangedOnParseError(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SourceErrorEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 2000},
		File:      "broken.jsonl",
		Offset:    -1,
		Message:   "bad json",
	}); err != nil {
		t.Fatalf("apply source error: %v", err)
	}
	if err := w.emitNotify(ctx, tx, 7000); err != nil {
		t.Fatalf("emitNotify: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	statusRows := 0
	for _, r := range readNotify(t, db) {
		if r.kind == "source_status_changed" {
			statusRows++
			if r.source != src {
				t.Errorf("source_status_changed source_id = %q, want %q", r.source, src)
			}
		}
	}
	if statusRows != 1 {
		t.Fatalf("source_status_changed rows = %d, want exactly 1", statusRows)
	}
}

// TestEmitNotify_RollbackLeavesNoRows is the critical atomicity test:
// notify rows live inside the batch tx, so a rollback must leave the
// notify table empty.
func TestEmitNotify_RollbackLeavesNoRows(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "s1",
		RootNativeID: "s1",
		Kind:         canonical.KindRoot,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := w.emitNotify(ctx, tx, 5000); err != nil {
		t.Fatalf("emitNotify: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify`); got != 0 {
		t.Fatalf("notify rows after rollback = %d, want 0 (rows must be tx-scoped)", got)
	}
}

// TestPruneNotify_RemovesOldKeepsRecent asserts the prune deletes rows
// older than the retention window relative to a supplied "now", keeping
// recent rows.
func TestPruneNotify_RemovesOldKeepsRecent(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()

	nowUS := int64(1_000_000_000_000)
	retUS := notifyRetention.Microseconds()

	// old: just past the retention edge. recent: well inside it.
	oldTS := nowUS - retUS - 1
	recentTS := nowUS - retUS/2
	for _, ts := range []int64{oldTS, recentTS} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO notify (ts_us, kind) VALUES (?, 'stats_invalidated')`, ts); err != nil {
			t.Fatalf("seed notify ts=%d: %v", ts, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := pruneNotify(ctx, tx, nowUS); err != nil {
		t.Fatalf("pruneNotify: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	rows := readNotify(t, db)
	if len(rows) != 1 {
		t.Fatalf("notify rows after prune = %d, want 1 (recent kept, old pruned): %+v", len(rows), rows)
	}
	if rows[0].tsUS != recentTS {
		t.Errorf("surviving row ts_us = %d, want recent %d", rows[0].tsUS, recentTS)
	}
}

// TestEmitNotify_SeqStrictlyIncreasesAcrossBatches asserts the
// AUTOINCREMENT seq never goes backwards across separate committed
// batches — the serve poll cursor relies on a strictly increasing seq.
func TestEmitNotify_SeqStrictlyIncreasesAcrossBatches(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, src, "aiagent_v3", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "aiagent_v3", "/tmp", NopPricer{})

	emitBatch := func(nid string, ts int64) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: ts},
			NativeID:     nid,
			RootNativeID: nid,
			Kind:         canonical.KindRoot,
		}); err != nil {
			t.Fatalf("apply %s: %v", nid, err)
		}
		if err := w.emitNotify(ctx, tx, ts); err != nil {
			t.Fatalf("emitNotify %s: %v", nid, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit %s: %v", nid, err)
		}
		w.resetBatch()
	}

	emitBatch("a", 1000)
	maxAfterFirst := scanInt(t, db, `SELECT MAX(seq) FROM notify`)
	emitBatch("b", 2000)
	minOfSecond := scanInt(t, db, `SELECT MIN(seq) FROM notify WHERE session_id = ?`, canonicalSessionID(src, "b"))

	if minOfSecond <= maxAfterFirst {
		t.Fatalf("seq did not strictly increase across batches: first max=%d, second min=%d", maxAfterFirst, minOfSecond)
	}
}

// TestWorker_FlushWritesNotifyRows is the integration test: a full async
// batch driven through the worker must land notify rows atomically with
// the session data (proves the producer is wired into flush, not just
// unit-testable in isolation).
func TestWorker_FlushWritesNotifyRows(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 2)
	ch <- canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "s",
		RootNativeID: "s",
		Kind:         canonical.KindRoot,
	}
	if err := i.Submit(src, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='session_changed'`) == 1
	}) {
		t.Fatalf("worker flush did not write a session_changed notify row; rows=%d",
			scanInt(t, db, `SELECT COUNT(*) FROM notify`))
	}
	wantID := canonicalSessionID(src, "s")
	if got := scanString(t, db, `SELECT session_id FROM notify WHERE kind='session_changed' LIMIT 1`); got != wantID {
		t.Errorf("notify session_id = %q, want %q", got, wantID)
	}
}
