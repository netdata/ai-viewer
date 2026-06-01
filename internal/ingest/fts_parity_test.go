package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestFTSParity_AllFixtures is the FTS-correctness diff gate, the sibling of
// TestRollupParity_AllFixtures. It proves the INCREMENTAL FTS maintenance (run
// inside each batch transaction during ingestion: refreshFTS for fts_ops, the
// inline insert in applyLogEntry for fts_logs) and the one-shot BackfillFTS
// (rebuild from scratch) produce BYTE-IDENTICAL fts_ops + fts_logs over the SAME
// data.
//
// Comparison is by LOGICAL columns, never internal rowid: content-owning FTS5
// assigns docids by insert order, which differs between the two build paths, so
// the indexed columns + UNINDEXED linkage keys are what must match. fts_ops is
// keyed/ordered by op_id; fts_logs by log_id (the AUTOINCREMENT log_entries.id,
// which is stable for a row across both paths because the backfill reads the
// same persisted rows the incremental path wrote).
//
// The fixtures are ingested under their NATURAL per-adapter source_format (so
// multiple source_ids share a format, exactly like the rollup gate) through the
// full ingester, whose resolveFTS5IndexLogs defaults to true — so logs ARE
// indexed and fts_logs is exercised.
func TestFTSParity_AllFixtures(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	now := func() int64 { return parityNow }

	ingested := 0
	formats := make(map[string]struct{})
	for _, fa := range parityAdapters() {
		base := filepath.Join("..", "..", "testdata", fa.subdir)
		entries, err := os.ReadDir(base)
		if err != nil {
			t.Fatalf("readdir %s: %v", base, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			inputDir, err := filepath.Abs(filepath.Join(base, e.Name(), "INPUT"))
			if err != nil {
				t.Fatalf("abs: %v", err)
			}
			if _, err := os.Stat(inputDir); err != nil {
				continue // not every layout uses INPUT/ (e.g. opencode fixture.sql).
			}
			ingestParityFixture(t, db, now, fa.format, fa, e.Name(), inputDir)
			ingested++
			formats[fa.format] = struct{}{}
		}
	}
	if ingested < 4 || len(formats) < 2 {
		t.Fatalf("ingested %d fixtures across %d formats; want >=4 fixtures and >=2 formats", ingested, len(formats))
	}

	// Snapshot the incrementally-built FTS tables (logical columns only), fully
	// drained before any further query (single pinned writer connection).
	incOps := readFTSOpsLogical(t, db)
	incLogs := readFTSLogsLogical(t, db)

	// Guard against a vacuous all-empty pass: the fixtures produce ops, so fts_ops
	// MUST be non-empty.
	if len(incOps) == 0 {
		t.Fatal("fts_ops empty after ingestion — fixtures must produce ops; vacuous parity check")
	}

	// Wipe, then rebuild from scratch.
	if _, err := db.ExecContext(context.Background(), `DELETE FROM fts_ops`); err != nil {
		t.Fatalf("wipe fts_ops: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM fts_logs`); err != nil {
		t.Fatalf("wipe fts_logs: %v", err)
	}
	stats, err := BackfillFTS(context.Background(), db, silentLogger())
	if err != nil {
		t.Fatalf("BackfillFTS: %v", err)
	}
	if stats.OpRows == 0 {
		t.Fatalf("backfill produced zero fts_ops rows: %+v", stats)
	}

	bfOps := readFTSOpsLogical(t, db)
	bfLogs := readFTSLogsLogical(t, db)

	diffFTSOps(t, incOps, bfOps)
	diffFTSLogs(t, incLogs, bfLogs)

	t.Logf("fts parity OK: %d fixtures across %d formats; fts_ops=%d fts_logs=%d rows (incremental == backfill, byte-identical by logical columns)",
		ingested, len(formats), len(incOps), len(incLogs))
}

// ftsOpsRow is one fts_ops row's logical content (indexed columns + UNINDEXED
// keys), without the internal docid.
type ftsOpsRow struct {
	name, model, provider, toolNS string
	errorText, opID, sessionID    string
}

// readFTSOpsLogical loads fts_ops by logical columns, ordered by op_id.
func readFTSOpsLogical(t *testing.T, db *sql.DB) []ftsOpsRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, model, provider, tool_namespace, error_text, op_id, session_id
		 FROM fts_ops ORDER BY op_id`)
	if err != nil {
		t.Fatalf("read fts_ops: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ftsOpsRow
	for rows.Next() {
		var r ftsOpsRow
		if err := rows.Scan(&r.name, &r.model, &r.provider, &r.toolNS, &r.errorText, &r.opID, &r.sessionID); err != nil {
			t.Fatalf("scan fts_ops: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fts_ops: %v", err)
	}
	return out
}

// ftsLogsRow is one fts_logs row's logical content. session_id/op_id are
// nullable UNINDEXED linkage columns.
type ftsLogsRow struct {
	message   string
	logID     int64
	sessionID sql.NullString
	opID      sql.NullString
	severity  string
	ts        int64
}

// readFTSLogsLogical loads fts_logs by logical columns, ordered by log_id.
func readFTSLogsLogical(t *testing.T, db *sql.DB) []ftsLogsRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT message, log_id, session_id, op_id, severity, ts
		 FROM fts_logs ORDER BY log_id`)
	if err != nil {
		t.Fatalf("read fts_logs: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ftsLogsRow
	for rows.Next() {
		var r ftsLogsRow
		if err := rows.Scan(&r.message, &r.logID, &r.sessionID, &r.opID, &r.severity, &r.ts); err != nil {
			t.Fatalf("scan fts_logs: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fts_logs: %v", err)
	}
	return out
}

// diffFTSOps asserts two fts_ops snapshots (ordered by op_id) are row-for-row
// identical. On mismatch it reports the precise op_id + column so the
// orchestrator can pin a real refreshFTS-vs-BackfillFTS parity bug.
func diffFTSOps(t *testing.T, incremental, backfill []ftsOpsRow) {
	t.Helper()
	if len(incremental) != len(backfill) {
		t.Fatalf("fts_ops row count: incremental=%d backfill=%d", len(incremental), len(backfill))
	}
	for i := range incremental {
		inc, bf := incremental[i], backfill[i]
		if inc.opID != bf.opID {
			t.Fatalf("fts_ops row %d op_id: incremental=%q backfill=%q", i, inc.opID, bf.opID)
		}
		if inc != bf {
			t.Errorf("fts_ops op_id=%q differs:\n  incremental=%+v\n  backfill   =%+v", inc.opID, inc, bf)
		}
	}
}

// diffFTSLogs asserts two fts_logs snapshots (ordered by log_id) are row-for-row
// identical.
func diffFTSLogs(t *testing.T, incremental, backfill []ftsLogsRow) {
	t.Helper()
	if len(incremental) != len(backfill) {
		t.Fatalf("fts_logs row count: incremental=%d backfill=%d", len(incremental), len(backfill))
	}
	for i := range incremental {
		inc, bf := incremental[i], backfill[i]
		if inc.logID != bf.logID {
			t.Fatalf("fts_logs row %d log_id: incremental=%d backfill=%d", i, inc.logID, bf.logID)
		}
		if inc != bf {
			t.Errorf("fts_logs log_id=%d differs:\n  incremental=%+v\n  backfill   =%+v", inc.logID, inc, bf)
		}
	}
}
