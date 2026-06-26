package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestClaudeCodeCompaction_IngestsWithoutFKError guards the adapter→ingester
// seam an external review found broken: the claude-code adapter once emitted
// the compaction-summary PayloadRefEvent with OpSeq 0, so applyPayloadRef
// derived an op id with no ops row and the INSERT violated
// payload_refs.op_id NOT NULL REFERENCES ops(id) (migration 0001) — a
// FOREIGN KEY error that rolled back the entire ingest batch.
//
// The per-package tests cover each side in isolation (the adapter golden
// pins the ref's OpSeq; writer_orphan_payload_ref_test pins the writer's
// orphan guard). The bug lived in the COMPOSITION, so this drives the REAL
// claude-code adapter over the committed c_compaction fixture, feeds its
// events through the REAL ingester writer against an in-memory SQLite with
// the real migrations, and asserts the seam holds end-to-end:
//
//	(a) the batch commits with NO FOREIGN KEY constraint error,
//	(b) the compaction op row exists in ops,
//	(c) a payload_refs row's op_id equals that compaction op's id (the
//	    summary payload is correctly op-scoped, not orphaned), and
//	(d) sources.parse_errors is 0 — the orphan guard never had to fire
//	    because the ref was valid.
//
// Mirrors the established adapter→ingester harness in e2e_test.go.
func TestClaudeCodeCompaction_IngestsWithoutFKError(t *testing.T) {
	t.Parallel()
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "claude_code", "c_compaction", "INPUT"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	a, err := claude_code.New(fixtureDir, canonical.AdapterOptions{Logger: silentLogger()})
	if err != nil {
		t.Fatalf("claude_code.New: %v", err)
	}

	_, db := openTestStore(t)
	// The adapter prefixes SourceID with "claude-code:" (sourceIDPrefix);
	// register the same format/location so sources.format is populated.
	sourceID := "claude-code:" + fixtureDir
	ing, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(50*time.Millisecond),
		WithSourceFormat(sourceID, "claude-code"),
		WithLocation(sourceID, fixtureDir),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := make(chan canonical.Event)
	scanDone := make(chan struct{})
	var scanErr error
	go func() {
		defer close(scanDone)
		defer close(events)
		scanErr = a.Scan(ctx, nil, events)
	}()
	// Submit drives the real worker; the worker applies every event in one
	// transaction and drops the batch if commit fails. A FK rollback at the
	// seam would therefore leave the compaction op / payload_refs rows
	// absent and parse_errors untouched — all asserted below.
	if err := ing.Submit(sourceID, events); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	waitForScan(t, scanDone, "claude-code compaction")
	if scanErr != nil {
		t.Fatalf("Scan: %v", scanErr)
	}
	if !waitFor(20*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE kind='compaction'`) == 1
	}) {
		t.Fatalf("compaction op count before Stop = %d, want 1",
			scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE kind='compaction'`))
	}
	if err := ing.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE kind='compaction'`); got != 1 {
		t.Fatalf("compaction op count after Stop = %d, want 1", got)
	}

	// (a) The batch committed with no FOREIGN KEY error. The worker drops a
	// batch that fails to commit, so a FK rollback would leave parse_errors
	// untouched AND the compaction op / payload_refs rows absent. We assert
	// the positive outcome (rows present, parse_errors 0) below; here we
	// additionally confirm no source-scoped ERR log carrying a FK message
	// was written (defence against a future silent-swallow regression).
	if got := scanInt(t, db,
		`SELECT COUNT(*) FROM log_entries WHERE message LIKE '%FOREIGN KEY%'`); got != 0 {
		t.Errorf("found %d log rows mentioning FOREIGN KEY; the seam must not raise an FK error", got)
	}

	// (b) The compaction op row exists. Derive its canonical id from the
	// fixture's session native id + turn 1 + op seq 3 (the same derivation
	// the writer uses), then assert the row is present and is a compaction.
	const sessionNativeID = "33333333-3333-4333-8333-333333333333"
	compactionOpID := canonicalOpID(
		canonicalTurnID(canonicalSessionID(sourceID, sessionNativeID), 1), 3,
	)
	if got := scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE id=? AND kind='compaction'`, compactionOpID); got != 1 {
		t.Fatalf("compaction op row count = %d, want 1 (id=%s)", got, compactionOpID)
	}

	// (c) A payload_refs row is op-scoped to that compaction op — the bug's
	// fix. Before the fix the ref carried OpSeq 0 (an orphan op id) and the
	// insert raised the FK error; now it must reference the compaction op.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id=?`, compactionOpID); got != 1 {
		t.Errorf("payload_refs for compaction op = %d, want 1 (summary payload must be op-scoped)", got)
	}

	// (d) parse_errors is 0: the writer's orphan guard never fired because
	// the ref was valid. A non-zero count would mean the guard had to
	// rescue an orphan ref — i.e. the adapter regressed back to OpSeq 0.
	if got := scanInt(t, db, `SELECT IFNULL(parse_errors,0) FROM sources WHERE id=?`, sourceID); got != 0 {
		t.Errorf("sources.parse_errors = %d, want 0 (a valid op-scoped ref must not trip the orphan guard)", got)
	}
	// Defensive: confirm the session row actually ingested so the assertions
	// above are not vacuously passing on an empty store.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id=?`, sessionNativeID); got != 1 {
		t.Errorf("session row count = %d, want 1", got)
	}
}
