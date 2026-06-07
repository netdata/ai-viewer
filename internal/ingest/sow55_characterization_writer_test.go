package ingest

import (
	"database/sql"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/rollups"
)

// SOW-0055 characterization pins for writer.go apply-op and apply-log behaviour.

// TestWriter_CharacterizationOpStartNilExtrasReEmitPreservesResolverStash pins
// the applyOpStarted contract: a re-emit with nil extras must yield a
// stash-only extras_json — non-aiViewer keys are dropped, but resolver join
// keys (childNativeId / toolUseId) survive under $.aiViewer.
func TestWriter_CharacterizationOpStartNilExtrasReEmitPreservesResolverStash(t *testing.T) {
	t.Parallel()
	const src = "claude-code:/tmp"
	w, db, ctx := sow55SetupWriter(t, src, "claude-code", "/tmp")

	tx1 := sow55Begin(t, ctx, db)
	sow55ApplyBatch(t, ctx, w, tx1, sow55NilExtrasPhase1Events(src))
	sow55CommitTx(t, tx1)

	tx2 := sow55Begin(t, ctx, db)
	sow55ApplyEvent(t, ctx, w, tx2, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1100},
		SessionNativeID: "parent", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpSession, Name: "explore",
	})
	sow55CommitTx(t, tx2)

	turnID := canonicalTurnID(canonicalSessionID(src, "parent"), 1)
	opID := canonicalOpID(turnID, 1)
	assertNilExtrasReEmitStash(t, db, opID)
}

// TestWriter_CharacterizationOpStartReEmitMarksRollupBucketAndCatalog pins the
// applyOpStarted post-upsert hooks: the op's start bucket is added to the
// dirty rollup set, the dirty op id is recorded for FTS, and catalog call_count
// does NOT double-count a same-identity re-emit (SOW-0004 H1a).
func TestWriter_CharacterizationOpStartReEmitMarksRollupBucketAndCatalog(t *testing.T) {
	t.Parallel()
	const src = "codex:/tmp"
	w, db, ctx := sow55SetupWriter(t, src, "codex", "/tmp")
	tx := sow55Begin(t, ctx, db)
	sow55ApplyBatch(t, ctx, w, tx, sow55ReEmitRollupEvents(src))
	sow55CommitTx(t, tx)

	turnID := canonicalTurnID(canonicalSessionID(src, "s"), 1)
	wantOpID := canonicalOpID(turnID, 1)
	assertReEmitRollupAndCatalog(t, db, w, wantOpID)
}

// TestWriter_CharacterizationLogEntryDuplicateReplayNoDuplicateFTS pins the
// applyLogEntry contract: a byte-identical log_entries replay must leave
// log_entries with exactly one row AND fts_logs with exactly one row. The
// INSERT ... ON CONFLICT DO NOTHING ... RETURNING id returns sql.ErrNoRows on
// conflict; that signal gates the fts_logs insert.
func TestWriter_CharacterizationLogEntryDuplicateReplayNoDuplicateFTS(t *testing.T) {
	t.Parallel()
	const src = "claude_code:/tmp"
	w, db, ctx := sow55SetupWriter(t, src, "claude_code", "/tmp")
	w.fts5IndexLogs = true

	tx1 := sow55Begin(t, ctx, db)
	sow55ApplyBatch(t, ctx, w, tx1, sow55LogBatch(src, 1, 1000, "replayed line"))
	sow55CommitTx(t, tx1)

	tx2 := sow55Begin(t, ctx, db)
	sow55ApplyBatch(t, ctx, w, tx2, sow55LogBatch(src, 3, 1000, "replayed line"))
	sow55CommitTx(t, tx2)

	assertLogDupDedup(t, db)
}

// TestWriter_CharacterizationLogEntryFTSGateRespectsFlag pins the second half
// of the applyLogEntry FTS hook: when fts5IndexLogs=false no fts_logs row is
// inserted even for a first-seen log row, while log_entries still receives the
// row.
func TestWriter_CharacterizationLogEntryFTSGateRespectsFlag(t *testing.T) {
	t.Parallel()
	const src = "claude_code:/tmp"
	w, db, ctx := sow55SetupWriter(t, src, "claude_code", "/tmp")
	w.fts5IndexLogs = false

	tx := sow55Begin(t, ctx, db)
	sow55ApplyBatch(t, ctx, w, tx, sow55LogBatch(src, 1, 1000, "gated message"))
	sow55CommitTx(t, tx)

	assertLogFTSGated(t, db, "gated message")
}

// ----- writer extras / rollup / log assertions -----

func assertNilExtrasReEmitStash(t *testing.T, db *sql.DB, opID string) {
	t.Helper()
	gotTU := scanString(t, db, `SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM ops WHERE id=?`, opID)
	if gotTU != "toolu_abc" {
		t.Errorf("toolUseId stash = %q, want %q (must survive nil-extras re-emit)", gotTU, "toolu_abc")
	}
	gotCN := scanString(t, db, `SELECT IFNULL(json_extract(extras_json,'$.aiViewer.childNativeId'),'') FROM ops WHERE id=?`, opID)
	if gotCN != "parent:agent:xyz" {
		t.Errorf("childNativeId stash = %q, want %q (must survive nil-extras re-emit)", gotCN, "parent:agent:xyz")
	}
	gotAttr := scanString(t, db, `SELECT IFNULL(json_extract(extras_json,'$.attr.x'),'') FROM ops WHERE id=?`, opID)
	if gotAttr != "" {
		t.Errorf("attr.x present after nil-extras re-emit = %q, want empty (graft must drop non-aiViewer keys)", gotAttr)
	}
}

func assertReEmitRollupAndCatalog(t *testing.T, db *sql.DB, w *writer, wantOpID string) {
	t.Helper()
	wantBucket := rollups.BucketTS(3_600_000_005, rollups.Hourly)
	if _, ok := w.dirtyRollupBuckets[wantBucket]; !ok {
		t.Fatalf("dirtyRollupBuckets missing bucket %d after applyOpStarted (post-upsert marking lost)", wantBucket)
	}
	wantDay := rollups.BucketTS(3_600_000_005, rollups.Daily)
	if _, ok := w.dirtyRollupDays[wantDay]; !ok {
		t.Fatalf("dirtyRollupDays missing day %d after applyOpStarted (daily marking lost)", wantDay)
	}
	if _, ok := w.dirtyOpIDs[wantOpID]; !ok {
		t.Fatalf("dirtyOpIDs missing %s after applyOpStarted (FTS marking lost)", wantOpID)
	}
	if got := scanInt(t, db, `SELECT call_count FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != 1 {
		t.Errorf("catalog_models call_count = %d, want 1 (one physical op, re-emit is UPDATE)", got)
	}
	if got := scanInt(t, db, `SELECT call_count FROM catalog_providers WHERE name='openai'`); got != 1 {
		t.Errorf("catalog_providers call_count = %d, want 1 (one physical op, re-emit is UPDATE)", got)
	}
}

func assertLogDupDedup(t *testing.T, db *sql.DB) {
	t.Helper()
	const msg = "replayed line"
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE message=?`, msg); got != 1 {
		t.Errorf("log_entries rows = %d, want 1 (replay must dedup via idx_log_entries_identity)", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs WHERE message=?`, msg); got != 1 {
		t.Errorf("fts_logs rows = %d, want 1 (replay must not duplicate the FTS row — ErrNoRows gate must hold)", got)
	}
}

func assertLogFTSGated(t *testing.T, db *sql.DB, message string) {
	t.Helper()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE message=?`, message); got != 1 {
		t.Errorf("log_entries rows = %d, want 1 (log_entries always written)", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM fts_logs WHERE message=?`, message); got != 0 {
		t.Errorf("fts_logs rows = %d, want 0 (fts5IndexLogs=false must skip indexing)", got)
	}
}
