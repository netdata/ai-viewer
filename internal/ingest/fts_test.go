package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// flushBatchFTS mirrors flushBatch (rollup_refresh_test.go) but sets the
// worker's resolved fts5IndexLogs flag, so the fts_logs gating in applyLogEntry
// is exercised. fts_ops is never gated, so it is populated regardless of the
// flag. It runs the production flush path (apply loop → refreshRollups →
// refreshFTS → ... → commit) exactly.
func flushBatchFTS(t *testing.T, db *sql.DB, sourceID, format string, indexLogs bool, batch []canonical.Event) {
	t.Helper()
	ctx := context.Background()
	wr := newWriter(sourceID, format, "/loc", NopPricer{})
	wr.now = fixedNow
	w := &worker{
		sourceID:      sourceID,
		sourceFormat:  format,
		location:      "/loc",
		fts5IndexLogs: indexLogs,
		db:            db,
		hwm:           newHWMCache(),
		pricer:        NopPricer{},
		logger:        silentLogger(),
		batchSize:     defaultBatchSize,
		batchEvery:    defaultBatchInterval,
	}
	if err := w.flush(ctx, wr, batch); err != nil {
		t.Fatalf("worker.flush: %v", err)
	}
	wr.resetBatch()
}

// seedSourceFlag inserts a sources row with an explicit fts5_index_logs value so
// the backfill's per-source gating can be exercised independently of the worker.
func seedSourceFlag(t *testing.T, db *sql.DB, id, format string, indexLogs bool) {
	t.Helper()
	flag := 0
	if indexLogs {
		flag = 1
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, enabled, parse_errors, fts5_index_logs, created_at)
		 VALUES (?, ?, '/loc', 1, 0, ?, 1)`, id, format, flag); err != nil {
		t.Fatalf("seed source %s (flag=%v): %v", id, indexLogs, err)
	}
}

// logEvent returns one session-scoped LogEntryEvent (the only log shape
// applyLogEntry — and therefore fts_logs — handles). seq keeps SourceSeq
// monotonic; message is the searchable text.
func logEvent(src, sess, msg string, ts int64, seq uint64) canonical.Event {
	return canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: seq, Ts: ts},
		SessionNativeID: sess,
		Severity:        "INF",
		Source:          "agent",
		Message:         msg,
	}
}

// matchOpIDs returns the op_ids fts_ops yields for a MATCH query, ranked by
// bm25 (rank), so a test can assert which ops a term resolves to.
func matchOpIDs(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT op_id FROM fts_ops WHERE fts_ops MATCH ? ORDER BY rank`, query)
	if err != nil {
		t.Fatalf("fts_ops MATCH %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan op_id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fts_ops MATCH: %v", err)
	}
	return ids
}

// TestFTS_IncrementalOpsMatchSnippetRank: ingested ops populate fts_ops so a
// MATCH resolves to the right op_ids, bm25(fts_ops) ranks, and snippet()
// renders the matched text — the exact read pattern GET /api/search relies on.
func TestFTS_IncrementalOpsMatchSnippetRank(t *testing.T) {
	const src, format = "claude_code:/loc", "claude_code"
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start, end := ts(0, 9, 0), ts(0, 9, 5)
	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
	}
	// Two llm ops: distinct models so a model MATCH selects exactly one op.
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, start, end, "sonnet", "anthropic", 1, 1, 0, false)...)
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 2, start, end, "haiku", "anthropic", 1, 1, 0, false)...)
	flushBatchFTS(t, db, src, format, true, batch)

	// op_id is the canonical id: canonicalOpID(canonicalTurnID(sessionID, turnSeq), seq).
	sessionID := canonicalSessionID(src, "sess-1")
	turnID := canonicalTurnID(sessionID, 1)
	wantSonnet := canonicalOpID(turnID, 1)

	got := matchOpIDs(t, db, "sonnet")
	if len(got) != 1 || got[0] != wantSonnet {
		t.Fatalf("MATCH 'sonnet' op_ids = %v, want [%s]", got, wantSonnet)
	}
	// Both ops share provider 'anthropic' → MATCH returns both.
	if all := matchOpIDs(t, db, "anthropic"); len(all) != 2 {
		t.Fatalf("MATCH 'anthropic' op_ids = %v, want 2 rows", all)
	}

	// snippet() + bm25 rank over the same row (GET /api/search shape).
	var snip string
	var rank float64
	if err := db.QueryRowContext(context.Background(),
		`SELECT snippet(fts_ops, -1, '[', ']', '…', 8), rank
		 FROM fts_ops WHERE fts_ops MATCH 'sonnet' ORDER BY rank LIMIT 1`).Scan(&snip, &rank); err != nil {
		t.Fatalf("snippet/rank: %v", err)
	}
	if !strings.Contains(snip, "sonnet") {
		t.Fatalf("snippet missing match marker: %q", snip)
	}
	if rank >= 0 {
		t.Fatalf("bm25 rank should be negative (better = more negative), got %v", rank)
	}
}

// TestFTS_OpReindexedOnFinalize: an op finalized in a LATER batch with an error
// message becomes findable by that error text (proves applyOpFinalized
// re-indexes from the FINAL persisted columns), and re-finalizing does NOT
// duplicate the op's fts_ops row (DELETE-then-INSERT on op_id).
func TestFTS_OpReindexedOnFinalize(t *testing.T) {
	const src, format = "codex:/loc", "codex"
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start, end := ts(0, 9, 0), ts(0, 9, 30)
	sessionID := canonicalSessionID(src, "sess-1")
	opID := canonicalOpID(canonicalTurnID(sessionID, 1), 1)

	// Batch 1: OpStarted only — no error text yet.
	b1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "agent", "/w", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: start},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Model: "gpt", Provider: "openai",
		},
	}
	flushBatchFTS(t, db, src, format, true, b1)
	if got := matchOpIDs(t, db, "saturated"); len(got) != 0 {
		t.Fatalf("pre-finalize MATCH 'saturated' = %v, want none (no error text yet)", got)
	}

	// Batch 2: OpFinalized with an error → error_text becomes searchable.
	b2 := []canonical.Event{
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: end},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, Status: "failed", EndTs: end,
			ErrorClass: "overloaded", ErrorMessage: "model saturated; retry later",
		},
	}
	flushBatchFTS(t, db, src, format, true, b2)

	if got := matchOpIDs(t, db, "saturated"); len(got) != 1 || got[0] != opID {
		t.Fatalf("post-finalize MATCH 'saturated' = %v, want [%s]", got, opID)
	}
	if got := matchOpIDs(t, db, "overloaded"); len(got) != 1 || got[0] != opID {
		t.Fatalf("MATCH 'overloaded' (error_class) = %v, want [%s]", got, opID)
	}
	// Exactly one fts_ops row for this op_id (no duplicate from re-index).
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE op_id = ?`, opID); n != 1 {
		t.Fatalf("fts_ops rows for op = %d, want exactly 1 (DELETE-then-INSERT)", n)
	}

	// Batch 3: re-finalize the SAME op (corrected error) — still one row.
	b3 := []canonical.Event{
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: end},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, Status: "failed", EndTs: end,
			ErrorClass: "timeout", ErrorMessage: "deadline exceeded",
		},
	}
	flushBatchFTS(t, db, src, format, true, b3)
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE op_id = ?`, opID); n != 1 {
		t.Fatalf("after re-finalize: fts_ops rows for op = %d, want exactly 1", n)
	}
	// The new error text is searchable; the old one is gone (row was replaced).
	if got := matchOpIDs(t, db, "deadline"); len(got) != 1 || got[0] != opID {
		t.Fatalf("MATCH 'deadline' (new error) = %v, want [%s]", got, opID)
	}
	if got := matchOpIDs(t, db, "saturated"); len(got) != 0 {
		t.Fatalf("MATCH 'saturated' (stale error) = %v, want none after re-index", got)
	}
}

// TestFTS_LogsGatingDisabled: a source with fts5_index_logs=false indexes ZERO
// logs into fts_logs, but its ops STILL populate fts_ops (fts_ops is never
// gated).
func TestFTS_LogsGatingDisabled(t *testing.T) {
	const src, format = "claude_code:/loc", "claude_code"
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start, end := ts(0, 9, 0), ts(0, 9, 5)
	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
	}
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, start, end, "sonnet", "anthropic", 1, 1, 0, false)...)
	batch = append(batch, logEvent(src, "sess-1", "disk almost full warning", ts(0, 9, 1), 50))
	// indexLogs=false → log not indexed.
	flushBatchFTS(t, db, src, format, false, batch)

	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`); n != 0 {
		t.Fatalf("fts_logs rows = %d, want 0 (source has fts5_index_logs=false)", n)
	}
	// But the log row itself WAS written to log_entries (only FTS is gated).
	if n := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE message='disk almost full warning'`); n != 1 {
		t.Fatalf("log_entries rows = %d, want 1 (log persisted; only FTS gated)", n)
	}
	// fts_ops is NOT gated → the op is indexed.
	if got := matchOpIDs(t, db, "sonnet"); len(got) != 1 {
		t.Fatalf("MATCH 'sonnet' = %v, want 1 (fts_ops always indexed)", got)
	}
}

// TestFTS_LogsGatingEnabled: a default (true) source indexes its logs into
// fts_logs and they are MATCH-able, resolving the documented log linkage.
func TestFTS_LogsGatingEnabled(t *testing.T) {
	const src, format = "claude_code:/loc", "claude_code"
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start := ts(0, 9, 0)
	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
		logEvent(src, "sess-1", "compilation failed unexpectedly", ts(0, 9, 1), 50),
	}
	flushBatchFTS(t, db, src, format, true, batch)

	sessionID := canonicalSessionID(src, "sess-1")
	var (
		gotSession sql.NullString
		gotSev     string
		gotSnippet string
	)
	if err := db.QueryRowContext(context.Background(),
		`SELECT session_id, severity, snippet(fts_logs, -1, '[', ']', '…', 8)
		 FROM fts_logs WHERE fts_logs MATCH 'compilation' ORDER BY rank LIMIT 1`).
		Scan(&gotSession, &gotSev, &gotSnippet); err != nil {
		t.Fatalf("fts_logs MATCH 'compilation': %v", err)
	}
	if gotSession.String != sessionID {
		t.Fatalf("fts_logs session_id = %q, want %q", gotSession.String, sessionID)
	}
	if gotSev != "INF" {
		t.Fatalf("fts_logs severity = %q, want INF", gotSev)
	}
	if !strings.Contains(gotSnippet, "compilation") {
		t.Fatalf("fts_logs snippet missing match marker: %q", gotSnippet)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`); n != 1 {
		t.Fatalf("fts_logs rows = %d, want 1", n)
	}
}

// TestFTS_LogReplayNoDuplicate: re-emitting a byte-identical log (replay) must
// not create a second fts_logs row — the ON CONFLICT DO NOTHING RETURNING gate
// indexes only newly-inserted logs.
func TestFTS_LogReplayNoDuplicate(t *testing.T) {
	const src, format = "claude_code:/loc", "claude_code"
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start := ts(0, 9, 0)
	mk := func() []canonical.Event {
		return []canonical.Event{
			sessionStartEvent(src, "sess-1", "claude", "/w", start, 1),
			canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
			logEvent(src, "sess-1", "repeated log line", ts(0, 9, 1), 50),
		}
	}
	flushBatchFTS(t, db, src, format, true, mk())
	flushBatchFTS(t, db, src, format, true, mk()) // replay identical batch.

	if n := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE message='repeated log line'`); n != 1 {
		t.Fatalf("log_entries rows = %d, want 1 (replay deduped)", n)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`); n != 1 {
		t.Fatalf("fts_logs rows = %d, want 1 (replay must not duplicate the FTS row)", n)
	}
}

// ftsOpsSnapshot reads fts_ops by its LOGICAL columns (NOT internal rowid),
// ordered by op_id, so two build paths (incremental vs backfill) can be compared
// for byte-identical row content despite different internal docids.
type ftsOpsLogical struct {
	name, model, provider, toolNS string
	errorText, opID, sessionID    string
}

func ftsOpsSnapshot(t *testing.T, db *sql.DB) []ftsOpsLogical {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, model, provider, tool_namespace, error_text, op_id, session_id
		 FROM fts_ops ORDER BY op_id`)
	if err != nil {
		t.Fatalf("snapshot fts_ops: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ftsOpsLogical
	for rows.Next() {
		var r ftsOpsLogical
		if err := rows.Scan(&r.name, &r.model, &r.provider, &r.toolNS, &r.errorText, &r.opID, &r.sessionID); err != nil {
			t.Fatalf("scan fts_ops: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fts_ops snapshot: %v", err)
	}
	return out
}

// TestFTS_Backfill: seed ops + logs (mixed fts5_index_logs flag) directly, run
// BackfillFTS, and assert fts_ops covers EVERY op while fts_logs covers ONLY the
// flag=true source's logs. Then run it again and assert identical output
// (idempotent / re-runnable).
func TestFTS_Backfill(t *testing.T) {
	_, db := openTestStore(t)
	const fmtCC = "claude_code"
	srcOn := "claude_code:/on"   // fts5_index_logs = true
	srcOff := "claude_code:/off" // fts5_index_logs = false
	seedSourceFlag(t, db, srcOn, fmtCC, true)
	seedSourceFlag(t, db, srcOff, fmtCC, false)

	start, end := ts(0, 9, 0), ts(0, 9, 5)
	// Source ON: one op + one session-scoped log.
	seedSession(t, db, "on-sess", srcOn, "claude", "/w", start)
	seedTurn(t, db, "on-turn", "on-sess", start)
	seedOp(t, db, opSpec{id: "on-op", turnID: "on-turn", sessionID: "on-sess", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: start, endTS: &end, durationUS: end - start, status: "completed"})
	seedLog(t, db, "on-sess", "indexed log message here", "INF", ts(0, 9, 1))

	// Source OFF: one op + one session-scoped log (log must NOT be indexed).
	seedSession(t, db, "off-sess", srcOff, "claude", "/w", start)
	seedTurn(t, db, "off-turn", "off-sess", start)
	seedOp(t, db, opSpec{id: "off-op", turnID: "off-turn", sessionID: "off-sess", seq: 1,
		kind: "llm", name: "chat", model: "haiku", provider: "anthropic",
		startTS: start, endTS: &end, durationUS: end - start, status: "failed",
		// error columns are NULL here (seedOp does not set them) → error_text "".
	})
	seedLog(t, db, "off-sess", "excluded log message here", "INF", ts(0, 9, 1))

	stats, err := BackfillFTS(context.Background(), db, silentLogger())
	if err != nil {
		t.Fatalf("BackfillFTS: %v", err)
	}
	if stats.OpRows != 2 {
		t.Fatalf("BackfillFTS OpRows = %d, want 2 (every op)", stats.OpRows)
	}
	if stats.LogRows != 1 {
		t.Fatalf("BackfillFTS LogRows = %d, want 1 (only the flag=true source's log)", stats.LogRows)
	}

	// fts_ops covers BOTH ops.
	if got := matchOpIDs(t, db, "sonnet"); len(got) != 1 || got[0] != "on-op" {
		t.Fatalf("MATCH 'sonnet' = %v, want [on-op]", got)
	}
	if got := matchOpIDs(t, db, "haiku"); len(got) != 1 || got[0] != "off-op" {
		t.Fatalf("MATCH 'haiku' = %v, want [off-op]", got)
	}
	// fts_logs covers ONLY the ON source's log.
	if got := matchLogIDs(t, db, "indexed"); len(got) != 1 {
		t.Fatalf("MATCH 'indexed' in fts_logs = %v, want 1 row", got)
	}
	if got := matchLogIDs(t, db, "excluded"); len(got) != 0 {
		t.Fatalf("MATCH 'excluded' in fts_logs = %v, want 0 rows (source flag=false)", got)
	}

	// Idempotent: a second run produces identical snapshots.
	snap1Ops := ftsOpsSnapshot(t, db)
	snap1Logs := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`)
	stats2, err := BackfillFTS(context.Background(), db, silentLogger())
	if err != nil {
		t.Fatalf("BackfillFTS (2nd run): %v", err)
	}
	if stats2 != stats {
		// Elapsed differs; compare the row counts only.
		if stats2.OpRows != stats.OpRows || stats2.LogRows != stats.LogRows {
			t.Fatalf("2nd run stats differ: %+v vs %+v", stats2, stats)
		}
	}
	snap2Ops := ftsOpsSnapshot(t, db)
	snap2Logs := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`)
	if len(snap1Ops) != len(snap2Ops) || snap1Logs != snap2Logs {
		t.Fatalf("idempotency: counts changed ops %d->%d logs %d->%d",
			len(snap1Ops), len(snap2Ops), snap1Logs, snap2Logs)
	}
	for i := range snap1Ops {
		if snap1Ops[i] != snap2Ops[i] {
			t.Fatalf("idempotency: fts_ops row %d changed: %+v -> %+v", i, snap1Ops[i], snap2Ops[i])
		}
	}
}

// TestFTS_BackfillEmptyStore: BackfillFTS on an empty store is a no-op (no error,
// zero rows in both tables).
func TestFTS_BackfillEmptyStore(t *testing.T) {
	_, db := openTestStore(t)
	stats, err := BackfillFTS(context.Background(), db, silentLogger())
	if err != nil {
		t.Fatalf("BackfillFTS on empty store: %v", err)
	}
	if stats.OpRows != 0 || stats.LogRows != 0 {
		t.Fatalf("empty store stats = %+v, want zero rows", stats)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_ops`); n != 0 {
		t.Fatalf("fts_ops rows = %d, want 0", n)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`); n != 0 {
		t.Fatalf("fts_logs rows = %d, want 0", n)
	}
}

// TestFTS_BackfillStreamsInBoundedBatches drives BackfillFTS with a tiny
// per-batch size (2) over 5 ops and 5 indexable logs so the keyset loop must
// cross >=3 batches ([1,2],[3,4],[5]). It pins the bounded-memory streaming
// contract: every op and every indexable log is indexed exactly once across
// batches (no duplicate from a >=-instead-of-> keyset bug), the final partial
// batch IS processed (a unique token in the largest-op_id row, guaranteed to
// fall alone in the last batch, is MATCH-able), and the fts5_index_logs=0
// opt-out still holds when streaming (the OFF source's logs never reach
// fts_logs). batchSize is a var so the test can force boundary crossings.
func TestFTS_BackfillStreamsInBoundedBatches(t *testing.T) {
	saved := ftsBackfillBatchSize
	ftsBackfillBatchSize = 2
	t.Cleanup(func() { ftsBackfillBatchSize = saved })

	_, db := openTestStore(t)
	const fmtCC = "claude_code"
	srcOn := "claude_code:/on"   // fts5_index_logs = true
	srcOff := "claude_code:/off" // fts5_index_logs = false
	seedSourceFlag(t, db, srcOn, fmtCC, true)
	seedSourceFlag(t, db, srcOff, fmtCC, false)

	start, end := ts(0, 9, 0), ts(0, 9, 5)

	// ON source: 4 ops with stable, lexically-ordered op_ids op-1..op-4 plus a
	// 5th op-5 carrying a UNIQUE model token only it has. Ordered by id ASC,
	// op-5 lands alone in the final partial batch ([op-1,op-2],[op-3,op-4],
	// [op-5]) — so the MATCH below proves the last batch is processed.
	seedSession(t, db, "on-sess", srcOn, "claude", "/w", start)
	seedTurn(t, db, "on-turn", "on-sess", start)
	for i := 1; i <= 4; i++ {
		seedOp(t, db, opSpec{id: fmt.Sprintf("op-%d", i), turnID: "on-turn", sessionID: "on-sess", seq: i,
			kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
			startTS: start, endTS: &end, durationUS: end - start, status: "completed"})
	}
	seedOp(t, db, opSpec{id: "op-5", turnID: "on-turn", sessionID: "on-sess", seq: 5,
		kind: "llm", name: "chat", model: "uniquelastmodel", provider: "anthropic",
		startTS: start, endTS: &end, durationUS: end - start, status: "completed"})

	// 4 indexable logs on the ON source, plus a 5th carrying a UNIQUE token so
	// the last log batch is provably processed too (le.id AUTOINCREMENT keyset).
	for i := 1; i <= 4; i++ {
		seedLog(t, db, "on-sess", fmt.Sprintf("indexed log line %d", i), "INF", ts(0, 9, i))
	}
	seedLog(t, db, "on-sess", "uniquelastlog token", "INF", ts(0, 9, 5))

	// OFF source: one op (always indexed into fts_ops) + one log that must NOT
	// reach fts_logs even though streaming crosses batches.
	seedSession(t, db, "off-sess", srcOff, "claude", "/w", start)
	seedTurn(t, db, "off-turn", "off-sess", start)
	seedOp(t, db, opSpec{id: "op-6-off", turnID: "off-turn", sessionID: "off-sess", seq: 1,
		kind: "llm", name: "chat", model: "haiku", provider: "anthropic",
		startTS: start, endTS: &end, durationUS: end - start, status: "completed"})
	seedLog(t, db, "off-sess", "excluded log line", "INF", ts(0, 9, 1))

	const wantOps = 6  // 5 ON + 1 OFF (fts_ops is never gated)
	const wantLogs = 5 // 5 ON only (OFF source's log excluded)

	stats, err := BackfillFTS(context.Background(), db, silentLogger())
	if err != nil {
		t.Fatalf("BackfillFTS: %v", err)
	}
	if stats.OpRows != wantOps {
		t.Fatalf("stats.OpRows = %d, want %d (every op across batches)", stats.OpRows, wantOps)
	}
	if stats.LogRows != wantLogs {
		t.Fatalf("stats.LogRows = %d, want %d (only fts5_index_logs=1 logs)", stats.LogRows, wantLogs)
	}

	// Persisted row counts match the returned stats.
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_ops`); n != wantOps {
		t.Fatalf("fts_ops count = %d, want %d", n, wantOps)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`); n != wantLogs {
		t.Fatalf("fts_logs count = %d, want %d", n, wantLogs)
	}

	// No duplicates: a >=-instead-of-> keyset bug would re-read boundary rows.
	if total, distinct := scanInt(t, db, `SELECT COUNT(*) FROM fts_ops`),
		scanInt(t, db, `SELECT COUNT(DISTINCT op_id) FROM fts_ops`); total != distinct {
		t.Fatalf("fts_ops has duplicates: count=%d distinct op_id=%d", total, distinct)
	}
	if total, distinct := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs`),
		scanInt(t, db, `SELECT COUNT(DISTINCT log_id) FROM fts_logs`); total != distinct {
		t.Fatalf("fts_logs has duplicates: count=%d distinct log_id=%d", total, distinct)
	}

	// The OFF source's log is absent (opt-out holds across batches).
	if got := matchLogIDs(t, db, "excluded"); len(got) != 0 {
		t.Fatalf("MATCH 'excluded' in fts_logs = %v, want 0 rows (source flag=false)", got)
	}

	// Final partial batch processed: the unique token in the largest-op_id row
	// (op-5) resolves to exactly that op.
	if got := matchOpIDs(t, db, "uniquelastmodel"); len(got) != 1 || got[0] != "op-5" {
		t.Fatalf("MATCH 'uniquelastmodel' = %v, want [op-5] (last partial op batch processed)", got)
	}
	// And the unique token in the last log batch resolves to exactly one log.
	if got := matchLogIDs(t, db, "uniquelastlog"); len(got) != 1 {
		t.Fatalf("MATCH 'uniquelastlog' in fts_logs = %v, want 1 row (last partial log batch processed)", got)
	}
}

// seedLog inserts one session-scoped log_entries row (source_id NULL), matching
// the shape applyLogEntry writes, so BackfillFTS's session-scoped gating sees it.
func seedLog(t *testing.T, db *sql.DB, sessionID, message, severity string, tsUS int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message)
VALUES (?, NULL, NULL, NULL, ?, ?, 'agent', ?)`,
		sessionID, tsUS, severity, message); err != nil {
		t.Fatalf("seed log (%s): %v", message, err)
	}
}

// matchLogIDs returns the log_ids fts_logs yields for a MATCH query.
func matchLogIDs(t *testing.T, db *sql.DB, query string) []int64 {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT log_id FROM fts_logs WHERE fts_logs MATCH ? ORDER BY rank`, query)
	if err != nil {
		t.Fatalf("fts_logs MATCH %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan log_id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fts_logs MATCH: %v", err)
	}
	slices.Sort(ids)
	return ids
}
