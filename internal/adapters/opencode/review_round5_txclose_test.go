package opencode

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins SOW-0005 ROUND-5 P2-1: NO warning/error/content emission happens
// while a source-DB read transaction is open, so a backpressured onError can never
// block with the WAL snapshot pinned on the live opencode database.
//
// The deterministic discriminator: open the DB read-only with a pool of exactly
// ONE connection (withMaxOpenConns(1)). While a read tx is OPEN it holds that one
// connection, so a query issued from inside the warn callback would have to WAIT
// for a free connection — and times out against a short context. After the tx is
// committed/rolled back the connection is free, so the same probe query succeeds
// immediately. The tests assert the warn callback fires AND its probe query
// SUCCEEDS, proving the tx was already closed when the callback ran. (If the fix
// regressed and the callback fired under the open tx, the probe would ctx-timeout
// and the test would FAIL rather than hang, thanks to the bounded probe context.)

// --- warnSink unit contract ---------------------------------------------------

// TestP2_1_WarnSinkBuffersFlushesResets pins the buffer's core contract: collect
// appends (non-blocking), flush emits in collection order through onError and
// resets so the sink is reusable for the next tx scope, and a nil onError drops.
func TestP2_1_WarnSinkBuffersFlushesResets(t *testing.T) {
	t.Parallel()
	s := &warnSink{}
	s.collect(nil) // nil is ignored
	if s.len() != 0 {
		t.Fatalf("len after nil collect = %d, want 0", s.len())
	}
	e1, e2 := errors.New("w1"), errors.New("w2")
	s.collect(e1)
	s.collect(e2)
	if s.len() != 2 {
		t.Fatalf("len = %d, want 2 (buffered, not emitted)", s.len())
	}
	var got []error
	if n := s.flush(func(e error) { got = append(got, e) }); n != 2 {
		t.Fatalf("flush returned %d, want 2", n)
	}
	if len(got) != 2 || got[0] != e1 || got[1] != e2 {
		t.Fatalf("flush order = %v, want [w1 w2]", got)
	}
	if s.len() != 0 {
		t.Fatalf("len after flush = %d, want 0 (reset for reuse)", s.len())
	}
	// Reuse: collect again, flush with nil onError drops without panicking.
	s.collect(errors.New("w3"))
	if n := s.flush(nil); n != 1 || s.len() != 0 {
		t.Fatalf("flush(nil) = %d, len = %d, want 1 dropped + reset", n, s.len())
	}
}

// txOpenProbe returns an onError callback that, on its FIRST invocation, probes
// whether the single-connection pool has a FREE connection (i.e. the read tx is
// already closed): it runs `SELECT 1` under a bounded context. txClosed is set
// true iff the probe succeeded (connection free → tx committed/rolled back before
// the callback fired); fired records that the callback ran at all. A second pool
// connection is impossible (MaxOpenConns(1)), so a still-open tx makes the probe
// ctx-timeout → txClosed stays false.
func txOpenProbe(db *sql.DB) (onError func(error), fired *atomic.Bool, txClosed *atomic.Bool) {
	fired = &atomic.Bool{}
	txClosed = &atomic.Bool{}
	onError = func(error) {
		if fired.Swap(true) {
			return // probe once
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var one int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err == nil && one == 1 {
			txClosed.Store(true)
		}
	}
	return onError, fired, txClosed
}

// openROConns opens the built DB path strictly read-only (the adapter's helper)
// with a pool of exactly n connections, registering cleanup. n=1 is the P2-1
// discriminator: a query inside the warn callback can only get a connection once
// the read tx has released it.
func openROConns(t *testing.T, path string, n int) *sql.DB {
	t.Helper()
	db, err := openReadOnly(context.Background(), path, withMaxOpenConns(n))
	if err != nil {
		t.Fatalf("openReadOnly %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestP2_1_DeltaWarnEmittedAfterTxClosed pins the DELTA-scan path (scanOnePage):
// a corrupt OPTIONAL cell raises a WARN, and that WARN is delivered through
// onError only AFTER the page tx is committed (the connection is free, so the
// probe query inside the callback succeeds).
func TestP2_1_DeltaWarnEmittedAfterTxClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	// A corrupt OPTIONAL cell (cost) → a degrade-to-0 WARN inside the page tx.
	insertSessionCorruptCol(t, path, "ses_warn", "cost", "not-a-number")

	db := openROConns(t, path, 1) // single-connection pool: the discriminator
	schema, err := introspectAll(ctxBG(), db)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}

	onError, fired, txClosed := txOpenProbe(db)
	sink := &warnSink{}
	affected := newAffectedSet()
	onRow := deltaRowHandler("session", schema["session"], affected, sink.collect)
	if _, err := scanTableDelta(ctxBG(), db, schema["session"], TableWatermark{}, onRow, sink, onError); err != nil {
		t.Fatalf("scanTableDelta (corrupt optional must not abort): %v", err)
	}
	if !fired.Load() {
		t.Fatal("expected a corrupt-cell WARN to fire through onError")
	}
	if !txClosed.Load() {
		t.Fatal("WARN fired while the page read tx was STILL OPEN (P2-1 violated): the probe query could not get the single pool connection")
	}
}

// TestP2_1_TreeLoadWarnEmittedAfterTxClosed pins the TREE-LOAD path
// (loadAndMapSession): a corrupt OPTIONAL session cell raises a WARN inside the
// single per-session read tx; the WARN reaches onError only after that tx is
// committed, and the mapped content events are returned (emitted by the caller)
// strictly after. The single-connection probe proves the tx was closed at flush.
func TestP2_1_TreeLoadWarnEmittedAfterTxClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	// A session whose OPTIONAL cost cell is corrupt (warns inside loadSession) plus a
	// minimal assistant turn so the tree maps to content events.
	insertSessionCorruptCol(t, path, "ses_tree", "cost", "garbage")
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("open rw2: %v", err)
	}
	insertAssistantMessage(t, rw2, "msg_t1", "ses_tree", 200, 900, 10, 5)
	insertPart(t, rw2, "prt_t1", "msg_t1", "ses_tree", 210, 210, stepStartBody())
	insertPart(t, rw2, "prt_t2", "msg_t1", "ses_tree", 900, 900, stepFinishBody(10, 5, 0.01))
	if err := rw2.Close(); err != nil {
		t.Fatalf("close rw2: %v", err)
	}

	db := openROConns(t, path, 1)
	schema, err := introspectAll(ctxBG(), db)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}

	onError, fired, txClosed := txOpenProbe(db)
	evs, skipped, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_tree", silentLogger(), onError)
	if err != nil {
		t.Fatalf("loadAndMapSession: %v", err)
	}
	if skipped {
		t.Fatal("session unexpectedly skipped")
	}
	if !fired.Load() {
		t.Fatal("expected a corrupt-cell WARN to fire through onError during tree load")
	}
	if !txClosed.Load() {
		t.Fatal("WARN fired while the per-session read tx was STILL OPEN (P2-1 violated)")
	}
	// Content events were produced (mapped AFTER the tx closed, emitted by caller).
	if countKind(evs, canonical.EvSessionStarted) != 1 {
		t.Fatalf("want 1 SessionStarted in mapped events, got %d events", len(evs))
	}
}
