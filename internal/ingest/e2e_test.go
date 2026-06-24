package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestE2E_SubAgentFixture runs the v3 adapter against the committed
// sub_agent fixture, drains it through the ingester into an in-memory
// SQLite store, and asserts the end-to-end shape of the resulting
// store rows.
//
// The fixture has two ledger files (root + sub-agent). The v3 adapter
// also synthesizes a SessionStartedEvent for the child from the parent's
// session_summary, but the child's own session_start carries the
// authoritative metadata so UPSERTs converge on it.
func TestE2E_SubAgentFixture(t *testing.T) {
	t.Parallel()
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "aiagent_v3", "sub_agent", "INPUT"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	a, err := aiagent_v3.New(fixtureDir, canonical.AdapterOptions{Logger: silentLogger()})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	_, db := openTestStore(t)
	ing, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2000),
		WithBatchInterval(50*time.Millisecond),
		WithSourceFormat("aiagent_v3:"+fixtureDir, "aiagent_v3"),
		WithLocation("aiagent_v3:"+fixtureDir, fixtureDir),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := make(chan canonical.Event, 256)
	scanDone := make(chan struct{})
	var scanErr error
	go func() {
		defer close(events)
		scanErr = a.Scan(ctx, nil, events)
		close(scanDone)
	}()
	if err := ing.Submit("aiagent_v3:"+fixtureDir, events); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	waitForScan(t, scanDone, "aiagent_v3 sub_agent")
	if scanErr != nil {
		t.Fatalf("Scan: %v", scanErr)
	}
	if err := ing.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Run the resolver once to backfill parent linkage now that both
	// parent + child are present.
	if err := ing.ResolveOrphans(ctx); err != nil {
		t.Fatalf("ResolveOrphans: %v", err)
	}

	// --- Assertions ---
	// Exactly two sessions: parent + sub-agent child.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions`); got != 2 {
		t.Fatalf("session count = %d, want 2", got)
	}
	// Parent session present and is a root.
	if got := scanString(t, db, `SELECT kind FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333331'`); got != "root" {
		t.Errorf("parent kind = %q, want root", got)
	}
	// Child session present and is a sub_agent.
	if got := scanString(t, db, `SELECT kind FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333332'`); got != "sub_agent" {
		t.Errorf("child kind = %q, want sub_agent", got)
	}
	// Child parent_session_id resolved to parent's id.
	wantParentID := canonicalSessionID("aiagent_v3:"+fixtureDir, "33333333-3333-3333-3333-333333333331")
	gotParent := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333332'`)
	if gotParent != wantParentID {
		t.Errorf("child parent_session_id = %q, want %q", gotParent, wantParentID)
	}
	// Each session has exactly one turn.
	if got := scanInt(t, db, `SELECT turn_count FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333331'`); got != 1 {
		t.Errorf("parent turn_count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT turn_count FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333332'`); got != 1 {
		t.Errorf("child turn_count = %d, want 1", got)
	}
	// Each session has at least one op.
	if got := scanInt(t, db, `SELECT op_count FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333331'`); got < 1 {
		t.Errorf("parent op_count = %d, want >=1", got)
	}
	if got := scanInt(t, db, `SELECT op_count FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333332'`); got < 1 {
		t.Errorf("child op_count = %d, want >=1", got)
	}
	// Both sessions are completed.
	if got := scanString(t, db, `SELECT status FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333331'`); got != "completed" {
		t.Errorf("parent status = %q, want completed", got)
	}
	if got := scanString(t, db, `SELECT status FROM sessions WHERE native_id='33333333-3333-3333-3333-333333333332'`); got != "completed" {
		t.Errorf("child status = %q, want completed", got)
	}
	// source_progress cursor persisted.
	cursor := scanString(t, db, `SELECT IFNULL(cursor,'') FROM source_progress WHERE source_id=?`, "aiagent_v3:"+fixtureDir)
	if cursor == "" {
		t.Errorf("source_progress.cursor not persisted")
	}
}

// TestE2E_SessionErrorFixture runs the session_error fixture and
// verifies a LogEntry row was written for the synthesized error.
func TestE2E_SessionErrorFixture(t *testing.T) {
	t.Parallel()
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "aiagent_v3", "session_error", "INPUT"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	a, err := aiagent_v3.New(fixtureDir, canonical.AdapterOptions{Logger: silentLogger()})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}
	_, db := openTestStore(t)
	sourceID := "aiagent_v3:" + fixtureDir
	ing, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2000), WithBatchInterval(50*time.Millisecond),
		WithSourceFormat(sourceID, "aiagent_v3"),
		WithLocation(sourceID, fixtureDir),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := make(chan canonical.Event, 256)
	scanDone := make(chan struct{})
	var scanErr error
	go func() {
		defer close(events)
		defer close(scanDone)
		scanErr = a.Scan(ctx, nil, events)
	}()
	if err := ing.Submit(sourceID, events); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForScan(t, scanDone, "aiagent_v3 session_error")
	if scanErr != nil {
		t.Fatalf("Scan: %v", scanErr)
	}
	if err := ing.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Session ended failed.
	if got := scanString(t, db, `SELECT status FROM sessions LIMIT 1`); got != "failed" {
		t.Errorf("status = %q, want failed", got)
	}
	// One ERR log row attached to the session.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='ERR' AND session_id IS NOT NULL`); got < 1 {
		t.Errorf("ERR log_entries = %d, want >=1", got)
	}
}

// TestE2E_HappyPathFixture verifies aggregates flow for the simplest
// non-trivial fixture.
func TestE2E_HappyPathFixture(t *testing.T) {
	t.Parallel()
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "aiagent_v3", "happy_single_turn", "INPUT"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	a, err := aiagent_v3.New(fixtureDir, canonical.AdapterOptions{Logger: silentLogger()})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}
	_, db := openTestStore(t)
	sourceID := "aiagent_v3:" + fixtureDir
	ing, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2000), WithBatchInterval(50*time.Millisecond),
		WithSourceFormat(sourceID, "aiagent_v3"),
		WithLocation(sourceID, fixtureDir),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := make(chan canonical.Event, 256)
	scanDone := make(chan struct{})
	var scanErr error
	go func() {
		defer close(events)
		defer close(scanDone)
		scanErr = a.Scan(ctx, nil, events)
	}()
	if err := ing.Submit(sourceID, events); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForScan(t, scanDone, "aiagent_v3 happy_single_turn")
	if scanErr != nil {
		t.Fatalf("Scan: %v", scanErr)
	}
	if err := ing.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions`); got != 1 {
		t.Errorf("session count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT turn_count FROM sessions`); got != 1 {
		t.Errorf("turn_count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT op_count FROM sessions`); got < 1 {
		t.Errorf("op_count = %d, want >=1", got)
	}
}
