package ingest

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// recordingHandler is a slog.Handler that records every formatted
// message so a test can assert no "FOREIGN KEY constraint failed"
// surfaced through the worker's error path (worker.report falls back to
// logger.Error when onErr is nil). Safe for concurrent use because the
// worker logs from its own goroutine.
type recordingHandler struct {
	mu   *sync.Mutex
	msgs *[]string
}

func newRecordingLogger() (*slog.Logger, func(substr string) bool) {
	var (
		mu   sync.Mutex
		msgs []string
	)
	h := recordingHandler{mu: &mu, msgs: &msgs}
	contains := func(substr string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range msgs {
			if strings.Contains(m, substr) {
				return true
			}
		}
		return false
	}
	return slog.New(h), contains
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(a.Value.String())
		return true
	})
	h.mu.Lock()
	*h.msgs = append(*h.msgs, b.String())
	h.mu.Unlock()
	return nil
}

func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

// v3PackSeq mirrors internal/adapters/aiagent_v3/mapper.go's
// packSeq(ledgerSeq, subIdx) = ledgerSeq<<12 | subIdx. The regression
// tests construct canonical events with hand-set SourceSeq values that
// match the real adapter's packing so the bug surfaces in the ingester,
// not the adapter.
func v3PackSeq(ledgerSeq, subIdx uint64) uint64 { return ledgerSeq<<12 | subIdx }

// submitEvents pushes events on a fresh buffered channel and submits it
// to the ingester. The channel is left open; the caller closes it once
// the assertions are made so a final flush drains any partial batch.
func submitEvents(t *testing.T, i *Ingester, sourceID string, events []canonical.Event) chan canonical.Event {
	t.Helper()
	ch := make(chan canonical.Event, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	if err := i.Submit(sourceID, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return ch
}

// TestWorker_OrphanFK_V2ParentBelowChildAbove pins SOW-0015's primary
// failure shape for aiagent_v2: the parent OpStartedEvent carries a
// SourceSeq BELOW the worker's high-water-mark while a child
// PayloadRefEvent for the SAME op carries a SourceSeq ABOVE it.
//
// Under the old scalar-HWM dedup, the OpStarted is dropped (no ops row)
// and the PayloadRef trips FOREIGN KEY constraint failed (787). After
// the fix the HWM no longer gates events, so both rows land and no FK
// error is logged.
//
// The asymmetry (parent below, child above) is mandatory: if both fell
// below the HWM the old code would drop both and the test would pass by
// accident, proving nothing.
func TestWorker_OrphanFK_V2ParentBelowChildAbove(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	logger, logged := newRecordingLogger()
	i, err := New(db,
		WithLogger(logger),
		WithBatchSize(2),
		WithBatchInterval(10*time.Second), // size triggers, not the timer
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	const src = "aiagent_v2:/tmp"
	// Batch 1: two warm-up sessions; the larger SourceSeq seeds the HWM
	// at 0x000000FFFFFF after the size=2 batch commits.
	ch := submitEvents(t, i, src, []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 0x000000FFFFFF, Ts: 100},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 0x0000000000FF, Ts: 110},
			NativeID:  "s2", RootNativeID: "s2", Kind: canonical.KindRoot,
		},
	})
	defer close(ch)

	if !waitFor(2*time.Second, func() bool { return i.HWM(src) == 0x000000FFFFFF }) {
		t.Fatalf("warm-up HWM not seeded; HWM=%d", i.HWM(src))
	}

	// Batch 2: OpStarted BELOW HWM (dropped by the old dedup),
	// PayloadRef ABOVE HWM (survives and trips the FK under the bug).
	ch <- canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 0x0000000000A0, Ts: 200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
	}
	ch <- canonical.PayloadRefEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 0x0000FFFFFF00, Ts: 210},
		SessionNativeID: "s", TurnSeq: 1, OpSeq: 1,
		PayloadKind: "llm_request", Format: "json",
		LocationURI: "file:///tmp/payload-a.json",
	}
	wantOpID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "s"), 1), 1)

	if !waitFor(3*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE id = ?`, wantOpID) == 1 &&
			scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id = ?`, wantOpID) == 1
	}) {
		t.Fatalf("orphan FK bug present: ops=%d payload_refs=%d (want 1/1)",
			scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE id = ?`, wantOpID),
			scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id = ?`, wantOpID))
	}
	if logged("FOREIGN KEY constraint failed") {
		t.Errorf("worker logged a FK constraint failure; the parent ops row was dropped")
	}
}

// TestWorker_OrphanFK_V3CrossFileInterleaving pins the v3 variant: two
// session files under ONE sourceID. File A's events carry packed seqs
// that advance the HWM high; file B's OpStarted + PayloadRef carry
// packed seqs from a FRESH per-file ledger (low values) that land in a
// later batch. Under the old scalar HWM, file B's low-seq events are
// dropped and the PayloadRef trips the FK.
func TestWorker_OrphanFK_V3CrossFileInterleaving(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	logger, logged := newRecordingLogger()
	i, err := New(db,
		WithLogger(logger),
		WithBatchSize(2),
		WithBatchInterval(10*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	const src = "aiagent_v3:/tmp/sessions"
	// File A drives the HWM high: ledgerSeq=5000, two sub-events.
	ch := submitEvents(t, i, src, []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(5000, 0), Ts: 100},
			NativeID:  "fileA", RootNativeID: "fileA", Kind: canonical.KindRoot,
		},
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(5000, 1), Ts: 110},
			NativeID:  "fileA2", RootNativeID: "fileA2", Kind: canonical.KindRoot,
		},
	})
	defer close(ch)

	hwmAfterA := v3PackSeq(5000, 1)
	if !waitFor(2*time.Second, func() bool { return i.HWM(src) == hwmAfterA }) {
		t.Fatalf("file A HWM not seeded; HWM=%d want %d", i.HWM(src), hwmAfterA)
	}

	// File B is a fresh file whose ledger restarts at 1. Its OpStarted
	// (ledgerSeq=1, sub 0) and PayloadRef (ledgerSeq=1, sub 1) are far
	// BELOW the HWM seeded by file A — exactly the cross-file collision
	// the scalar HWM cannot handle.
	ch <- canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(1, 0), Ts: 200},
		SessionNativeID: "fileB", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
	}
	ch <- canonical.PayloadRefEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(1, 1), Ts: 210},
		SessionNativeID: "fileB", TurnSeq: 1, OpSeq: 1,
		PayloadKind: "llm_response", Format: "json",
		LocationURI: "file:///tmp/sessions/fileB-resp.json",
	}
	wantOpID := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "fileB"), 1), 1)

	if !waitFor(3*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE id = ?`, wantOpID) == 1 &&
			scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id = ?`, wantOpID) == 1
	}) {
		t.Fatalf("v3 cross-file orphan FK bug present: ops=%d payload_refs=%d (want 1/1)",
			scanInt(t, db, `SELECT COUNT(*) FROM ops WHERE id = ?`, wantOpID),
			scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id = ?`, wantOpID))
	}
	if logged("FOREIGN KEY constraint failed") {
		t.Errorf("worker logged a FK constraint failure for the v3 cross-file case")
	}
}

// TestWorker_ReScanIdempotency pins the SQL-layer idempotency contract:
// pushing the SAME event set (including payload_refs and log_entries)
// through the worker TWICE must not duplicate rows. Under the old plain
// INSERTs (no ON CONFLICT) the second pass doubled payload_refs and
// log_entries; after migration 0003 + ON CONFLICT DO NOTHING the counts
// are identical across passes.
func TestWorker_ReScanIdempotency(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const src = "aiagent_v3:/tmp/rescan"

	// A fixed event set: session, op start, op finalize, one
	// payload_ref, one log_entry. Each call returns fresh events so a
	// second pass replays identical values.
	build := func() []canonical.Event {
		return []canonical.Event{
			canonical.SessionStartedEvent{
				EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 100},
				NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
			},
			canonical.OpStartedEvent{
				EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 110},
				SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
				Kind: canonical.OpLLM, Name: "call",
			},
			canonical.OpFinalizedEvent{
				EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 120},
				SessionNativeID: "s", TurnSeq: 1, Seq: 1, EndTs: 120, Status: "completed",
			},
			canonical.PayloadRefEvent{
				EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 130},
				SessionNativeID: "s", TurnSeq: 1, OpSeq: 1,
				PayloadKind: "llm_request", Format: "json",
				LocationURI: "file:///tmp/rescan/p.json",
			},
			canonical.LogEntryEvent{
				EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 140},
				SessionNativeID: "s", TurnSeq: 1, OpSeq: 1,
				Severity: "INF", Source: "aiagent_v3", Message: "tool finished",
			},
		}
	}

	runPass := func(label string) {
		t.Helper()
		i, err := New(db,
			WithLogger(silentLogger()),
			WithBatchSize(100),
			WithBatchInterval(10*time.Second),
		)
		if err != nil {
			t.Fatalf("%s New: %v", label, err)
		}
		if err := i.Start(context.Background()); err != nil {
			t.Fatalf("%s Start: %v", label, err)
		}
		ch := make(chan canonical.Event, 8)
		for _, ev := range build() {
			ch <- ev
		}
		if err := i.Submit(src, ch); err != nil {
			t.Fatalf("%s Submit: %v", label, err)
		}
		close(ch)
		// Stop drains the worker and runs the final flush synchronously,
		// so the batch is durably committed when Stop returns.
		if err := i.Stop(); err != nil {
			t.Fatalf("%s Stop: %v", label, err)
		}
	}

	runPass("pass 1")
	if got := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`); got != 1 {
		t.Fatalf("pass 1 payload_refs = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries`); got != 1 {
		t.Fatalf("pass 1 log_entries = %d, want 1", got)
	}

	runPass("pass 2") // re-scan of the same file
	if got := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`); got != 1 {
		t.Errorf("payload_refs after re-scan = %d, want 1 (duplicate inserted)", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries`); got != 1 {
		t.Errorf("log_entries after re-scan = %d, want 1 (duplicate inserted)", got)
	}
}

// TestWorker_TwoInterleavedFilesBothPersist replaces the coverage lost
// from the old HWM-drop tests: two files under one sourceID, their
// events interleaved across batches, must BOTH fully persist (every op
// and payload) with no FK error. This is the positive counterpart to
// the orphan-FK regression tests above.
func TestWorker_TwoInterleavedFilesBothPersist(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	logger, logged := newRecordingLogger()
	i, err := New(db,
		WithLogger(logger),
		WithBatchSize(3),
		WithBatchInterval(10*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	const src = "aiagent_v3:/tmp/interleave"
	// File A: high ledgerSeq. File B: fresh ledger (low). Events are
	// interleaved A,B,A,B so each batch mixes high and low seqs.
	ch := submitEvents(t, i, src, []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(9000, 0), Ts: 100},
			NativeID:  "A", RootNativeID: "A", Kind: canonical.KindRoot,
		},
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(1, 0), Ts: 105},
			NativeID:  "B", RootNativeID: "B", Kind: canonical.KindRoot,
		},
		// This op-start trips size=3 and commits, seeding the HWM at
		// packSeq(9000,1).
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(9000, 1), Ts: 110},
			SessionNativeID: "A", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "callA",
		},
		// B op-start (ledger 1) — BELOW the HWM now.
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(1, 1), Ts: 115},
			SessionNativeID: "B", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "callB",
		},
		canonical.PayloadRefEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(9000, 2), Ts: 120},
			SessionNativeID: "A", TurnSeq: 1, OpSeq: 1,
			PayloadKind: "llm_request", Format: "json",
			LocationURI: "file:///tmp/interleave/A.json",
		},
		// B payload (ledger 1) — BELOW the HWM; would orphan under the bug.
		canonical.PayloadRefEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: v3PackSeq(1, 2), Ts: 125},
			SessionNativeID: "B", TurnSeq: 1, OpSeq: 1,
			PayloadKind: "llm_request", Format: "json",
			LocationURI: "file:///tmp/interleave/B.json",
		},
	})
	defer close(ch)

	opA := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "A"), 1), 1)
	opB := canonicalOpID(canonicalTurnID(canonicalSessionID(src, "B"), 1), 1)

	if !waitFor(3*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops`) == 2 &&
			scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`) == 2
	}) {
		t.Fatalf("interleaved files did not both persist: ops=%d payload_refs=%d (want 2/2)",
			scanInt(t, db, `SELECT COUNT(*) FROM ops`),
			scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`))
	}
	if scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id = ?`, opA) != 1 {
		t.Errorf("file A payload missing")
	}
	if scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id = ?`, opB) != 1 {
		t.Errorf("file B payload missing (orphaned by HWM drop)")
	}
	if logged("FOREIGN KEY constraint failed") {
		t.Errorf("worker logged a FK constraint failure during interleaving")
	}
}

// TestWorker_LogEntryTurnScopedNotFalseDeduped pins SOW-0015 iter-2's
// turn_id correctness fix: two genuinely distinct turn-scoped log
// entries in the SAME session, with op_id NULL (OpSeq=0) but DIFFERENT
// turns, sharing identical ts/severity/source/message, must BOTH
// persist. v3 emits turn-scoped warnings/errors with turn_id set but no
// op (aiagent_v3/mapper.go), so without turn_id in the idempotency key
// the second row is silently dropped as a false duplicate — the exact
// data-loss class SOW-0015 exists to kill.
//
// Mutation check: drop COALESCE(turn_id, ”) from idx_log_entries_identity
// (migration 0003) and the matching logEntryOnConflict target, and the
// COUNT(*)==2 assertion fails at 1 (the second turn's log collapses).
func TestWorker_LogEntryTurnScopedNotFalseDeduped(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const src = "aiagent_v3:/tmp/turnlogs"

	// One session, two turns. Both logs carry op_id NULL (OpSeq=0) and
	// the SAME ts/severity/source/message; only TurnSeq differs, so the
	// derived turn_id is the only distinguishing field. The two
	// TurnStartedEvents satisfy the log_entries.turn_id FK to turns(id).
	events := []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 100},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 150},
			SessionNativeID: "s", Seq: 1,
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 160},
			SessionNativeID: "s", Seq: 2,
		},
		canonical.LogEntryEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 200},
			SessionNativeID: "s", TurnSeq: 1, OpSeq: 0,
			Severity: "WRN", Source: "aiagent_v3", Message: "rate limit hit",
		},
		canonical.LogEntryEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 200},
			SessionNativeID: "s", TurnSeq: 2, OpSeq: 0,
			Severity: "WRN", Source: "aiagent_v3", Message: "rate limit hit",
		},
	}

	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(100),
		WithBatchInterval(10*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ch := make(chan canonical.Event, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	if err := i.Submit(src, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	close(ch)
	if err := i.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	turn1 := canonicalTurnID(canonicalSessionID(src, "s"), 1)
	turn2 := canonicalTurnID(canonicalSessionID(src, "s"), 2)
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries`); got != 2 {
		t.Fatalf("turn-scoped logs false-deduped: log_entries = %d, want 2 "+
			"(turn_id missing from idempotency key)", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE turn_id = ?`, turn1); got != 1 {
		t.Errorf("turn 1 log missing: count = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE turn_id = ?`, turn2); got != 1 {
		t.Errorf("turn 2 log missing: count = %d, want 1", got)
	}

	// A TRUE re-emit (identical turn_id too) must still collapse to one
	// row — turn_id in the key tightens the identity, it does not weaken
	// idempotency.
	reCh := make(chan canonical.Event, 2)
	reCh <- canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 6, Ts: 200},
		SessionNativeID: "s", TurnSeq: 1, OpSeq: 0,
		Severity: "WRN", Source: "aiagent_v3", Message: "rate limit hit",
	}
	i2, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(100),
		WithBatchInterval(10*time.Second),
	)
	if err != nil {
		t.Fatalf("New (re-emit): %v", err)
	}
	if err := i2.Start(context.Background()); err != nil {
		t.Fatalf("Start (re-emit): %v", err)
	}
	if err := i2.Submit(src, reCh); err != nil {
		t.Fatalf("Submit (re-emit): %v", err)
	}
	close(reCh)
	if err := i2.Stop(); err != nil {
		t.Fatalf("Stop (re-emit): %v", err)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries`); got != 2 {
		t.Errorf("re-emit of turn 1 log duplicated: log_entries = %d, want 2", got)
	}
}

// TestWorker_LogEntryExtrasNotFalseDeduped pins SOW-0015 iter-4's
// extras_json correctness fix: two genuinely distinct log entries on the
// SAME owner with identical ts/severity/source/message but DIFFERENT
// extras_json must BOTH persist. v2 stores the source `path` in extras
// (aiagent_v2/mapper.go), so without extras_json in the idempotency key
// the second row is silently dropped as a false duplicate — the same
// data-loss class as the turn_id gap (iter-2).
//
// Mutation check: drop COALESCE(extras_json, ”) from
// idx_log_entries_identity (migration 0003) and the matching
// logEntryOnConflict target, and the COUNT(*)==2 assertion fails at 1
// (the second log collapses onto the first).
func TestWorker_LogEntryExtrasNotFalseDeduped(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const src = "aiagent_v2:/tmp/extralogs"

	// One session, two session-scoped logs that match on every persisted
	// column EXCEPT extras_json: same ts/severity/source/message, op/turn
	// NULL. Only the extras differ (distinct `path` values), so extras_json
	// is the sole distinguishing field.
	events := []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 100},
			NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
		},
		canonical.LogEntryEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 200},
			SessionNativeID: "s",
			Severity:        "WRN", Source: "aiagent_v2", Message: "decode warning",
			Extras: map[string]any{"path": "T:0/O:0"},
		},
		canonical.LogEntryEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 200},
			SessionNativeID: "s",
			Severity:        "WRN", Source: "aiagent_v2", Message: "decode warning",
			Extras: map[string]any{"path": "T:0/O:1"},
		},
	}

	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(100),
		WithBatchInterval(10*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ch := make(chan canonical.Event, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	if err := i.Submit(src, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	close(ch)
	if err := i.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries`); got != 2 {
		t.Fatalf("extras-distinct logs false-deduped: log_entries = %d, want 2 "+
			"(extras_json missing from idempotency key)", got)
	}

	// A TRUE re-emit (identical extras too) must still collapse — adding
	// extras_json to the key tightens identity without weakening
	// idempotency.
	reCh := make(chan canonical.Event, 2)
	reCh <- canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 200},
		SessionNativeID: "s",
		Severity:        "WRN", Source: "aiagent_v2", Message: "decode warning",
		Extras: map[string]any{"path": "T:0/O:0"},
	}
	i2, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(100),
		WithBatchInterval(10*time.Second),
	)
	if err != nil {
		t.Fatalf("New (re-emit): %v", err)
	}
	if err := i2.Start(context.Background()); err != nil {
		t.Fatalf("Start (re-emit): %v", err)
	}
	if err := i2.Submit(src, reCh); err != nil {
		t.Fatalf("Submit (re-emit): %v", err)
	}
	close(reCh)
	if err := i2.Stop(); err != nil {
		t.Fatalf("Stop (re-emit): %v", err)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries`); got != 2 {
		t.Errorf("re-emit of identical-extras log duplicated: log_entries = %d, want 2", got)
	}
}
