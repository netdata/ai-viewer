package canonical_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/store"
)

// These property-based tests (pgregory.net/rapid) pin the five canonical
// event-model invariants from SOW-0011 AC#4. Each invariant is one
// TestPropertyXxx whose doc comment names it so `go doc` surfaces the
// contract.
//
// internal/canonical owns the event types + the Event interface; it has no
// decoders/encoders (all parsing lives in the adapters). The invariants are
// therefore exercised through the REAL production write path: each test
// opens a per-iteration in-memory store.OpenWriter, drives generated
// canonical events through the real ingest pipeline (ingest.New / Start /
// Submit / Stop), and reads the persisted SQL rows back to assert the
// production writer + schema honour the contract. The only exception is the
// type-level accessor sweep in TestPropertyRoundTripEquality part 1, which
// checks the in-package EventBase/EventKind surface directly (no store
// needed for an accessor that takes no DB). Routing the rest through the
// ingester means each test fails if the writer drops, mangles, rescales, or
// reorders a field on the way to SQLite — not merely if a generator or a Go
// stdlib call misbehaves.

// silentLogger returns a discarding slog.Logger so the property tests do
// not paint the terminal with structured ingest output across many rapid
// iterations.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ingestEvents drives one batch of canonical events through the REAL ingest
// pipeline into db: it builds an ingester with a bounded batch, submits the
// events on a buffered channel, and Stop()s — which blocks until the worker
// has drained the channel and run its final flush, so every row is committed
// before the caller reads it back. sourceID encodes the format prefix the
// ingester binds the source to (e.g. "aiagent_v3:/property/...") so the
// writer resolves the right source row. Shared by every store-backed
// property test so they all exercise the identical production path the
// idempotency test does.
func ingestEvents(rt *rapid.T, db *sql.DB, sourceID, format, location string, events []canonical.Event) {
	rt.Helper()
	ing, err := ingest.New(
		db,
		ingest.WithLogger(silentLogger()),
		ingest.WithBatchSize(10000),
		ingest.WithBatchInterval(5*time.Millisecond),
		ingest.WithSourceFormat(sourceID, format),
		ingest.WithLocation(sourceID, location),
	)
	if err != nil {
		rt.Fatalf("ingest.New: %v", err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		rt.Fatalf("ingest.Start: %v", err)
	}
	ch := make(chan canonical.Event, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	if err := ing.Submit(sourceID, ch); err != nil {
		rt.Fatalf("ingest.Submit: %v", err)
	}
	// Stop blocks until the worker drains the channel and runs its final
	// flush, so all rows are committed before we count/read.
	if err := ing.Stop(); err != nil {
		rt.Fatalf("ingest.Stop: %v", err)
	}
}

// genEventBase draws a canonical.EventBase with a non-empty SourceID, a
// full-range SourceSeq, and a non-negative microsecond Ts (timestamps in
// the canonical model are UNIX-microseconds UTC and never predate the
// epoch in practice).
func genEventBase(rt *rapid.T) canonical.EventBase {
	return canonical.EventBase{
		SourceID:  rapid.StringN(1, 64, 64).Draw(rt, "SourceID"),
		SourceSeq: rapid.Uint64().Draw(rt, "SourceSeq"),
		Ts:        rapid.Int64Range(0, maxMicros).Draw(rt, "Ts"),
	}
}

// maxMicros is an upper bound for generated microsecond timestamps: year
// ~2262, comfortably covering any real session while staying far from
// int64 overflow.
const maxMicros = int64(9_223_372_036_854_775)

// genSessionKind / genOpKind draw from the closed sets of discriminator
// values defined in canonical-events.md.
func genSessionKind(rt *rapid.T) canonical.SessionKind {
	return rapid.SampledFrom([]canonical.SessionKind{
		canonical.KindRoot, canonical.KindSubAgent,
		canonical.KindToolInternal, canonical.KindFork,
	}).Draw(rt, "SessionKind")
}

func genOpKind(rt *rapid.T) canonical.OpKind {
	return rapid.SampledFrom([]canonical.OpKind{
		canonical.OpLLM, canonical.OpTool, canonical.OpSession,
		canonical.OpReasoning, canonical.OpInternal, canonical.OpSystem,
		canonical.OpCompaction,
	}).Draw(rt, "OpKind")
}

// TestPropertyRoundTripEquality asserts the canonical round-trip-equality
// invariant in two parts:
//
//	Part 1 (type-level accessor sweep): a canonical event constructed from a
//	set of field values reports EXACTLY those values back through its
//	accessor methods (EventSourceID/EventSourceSeq/EventTs/EventKind), with
//	no lossy transform. This pins that EventBase storage and the per-type
//	EventKind() discriminator never mutate or drop the values an adapter
//	supplies — an in-package check that needs no store.
//
//	Part 2 (store round-trip): a generated SessionStartedEvent ingested
//	through the REAL pipeline persists its fields verbatim into the sessions
//	row. The test reads the row back via SQL and asserts native_id, kind,
//	agent_name, model, cwd, call_path, status, start_ts and last_activity_ts
//	equal what the event carried (writer.go applySessionStarted). This fails
//	if the writer drops or mangles a session column on the way to SQLite.
func TestPropertyRoundTripEquality(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		base := genEventBase(rt)

		// Every concrete event type embeds EventBase; each must echo the
		// three base fields back unchanged and report the kind fixed by its
		// Go type (not by any settable field).
		type kindedEvent struct {
			ev   canonical.Event
			kind canonical.EventKind
		}
		events := []kindedEvent{
			{canonical.SessionStartedEvent{EventBase: base}, canonical.EvSessionStarted},
			{canonical.SessionUpdatedEvent{EventBase: base}, canonical.EvSessionUpdated},
			{canonical.SessionFinalizedEvent{EventBase: base}, canonical.EvSessionFinalized},
			{canonical.TurnStartedEvent{EventBase: base}, canonical.EvTurnStarted},
			{canonical.TurnFinalizedEvent{EventBase: base}, canonical.EvTurnFinalized},
			{canonical.OpStartedEvent{EventBase: base}, canonical.EvOpStarted},
			{canonical.OpFinalizedEvent{EventBase: base}, canonical.EvOpFinalized},
			{canonical.PayloadRefEvent{EventBase: base}, canonical.EvPayloadRef},
			{canonical.LogEntryEvent{EventBase: base}, canonical.EvLogEntry},
			{canonical.SourceProgressEvent{EventBase: base}, canonical.EvSourceProgress},
			{canonical.SourceErrorEvent{EventBase: base}, canonical.EvSourceError},
		}
		for _, ke := range events {
			if got := ke.ev.EventSourceID(); got != base.SourceID {
				rt.Fatalf("%s EventSourceID = %q, want %q", ke.kind, got, base.SourceID)
			}
			if got := ke.ev.EventSourceSeq(); got != base.SourceSeq {
				rt.Fatalf("%s EventSourceSeq = %d, want %d", ke.kind, got, base.SourceSeq)
			}
			if got := ke.ev.EventTs(); got != base.Ts {
				rt.Fatalf("%s EventTs = %d, want %d", ke.kind, got, base.Ts)
			}
			if got := ke.ev.EventKind(); got != ke.kind {
				rt.Fatalf("EventKind = %q, want %q", got, ke.kind)
			}
		}

		// Part 2: a field-rich SessionStartedEvent must round-trip its
		// persisted payload through the REAL writer. Generate a root session
		// (RootNativeID == NativeID so the self-referential root_session_id
		// FK is satisfiable without a parent) with non-empty values for every
		// column the writer persists, ingest it, then read the sessions row
		// back and assert each column equals the emitted field. start_ts and
		// last_activity_ts are both written from ev.Ts (writer.go
		// applySessionStarted), so they pin the microsecond timestamp too.
		want := canonical.SessionStartedEvent{
			EventBase: base,
			NativeID:  rapid.StringN(1, 64, 64).Draw(rt, "NativeID"),
			Kind:      genSessionKind(rt),
			AgentName: rapid.StringN(1, 32, 32).Draw(rt, "AgentName"),
			Model:     rapid.StringN(1, 32, 32).Draw(rt, "Model"),
			Cwd:       rapid.StringN(1, 32, 32).Draw(rt, "Cwd"),
			CallPath:  rapid.StringN(1, 32, 32).Draw(rt, "CallPath"),
		}
		want.RootNativeID = want.NativeID // self-root: keep the FK satisfiable

		ctx := context.Background()
		s, err := store.OpenWriter(ctx, ":memory:", silentLogger())
		if err != nil {
			rt.Fatalf("store.OpenWriter: %v", err)
		}
		defer func() { _ = s.Close() }()
		db := s.DB()

		const sourceID = "aiagent_v3:/property/roundtrip"
		ingestEvents(rt, db, sourceID, "aiagent_v3", "/property/roundtrip", []canonical.Event{want})

		var (
			gotNative   string
			gotKind     string
			gotAgent    sql.NullString
			gotModel    sql.NullString
			gotCwd      sql.NullString
			gotCallPath sql.NullString
			gotStatus   string
			gotStart    int64
			gotActivity int64
		)
		if err := db.QueryRowContext(ctx, `
SELECT native_id, kind, agent_name, model, cwd, call_path, status, start_ts, last_activity_ts
FROM sessions WHERE source_id = ? AND native_id = ?`, sourceID, want.NativeID).
			Scan(&gotNative, &gotKind, &gotAgent, &gotModel, &gotCwd, &gotCallPath,
				&gotStatus, &gotStart, &gotActivity); err != nil {
			rt.Fatalf("read persisted session: %v", err)
		}
		if gotNative != want.NativeID {
			rt.Fatalf("native_id = %q, want %q", gotNative, want.NativeID)
		}
		if gotKind != string(want.Kind) {
			rt.Fatalf("kind = %q, want %q", gotKind, want.Kind)
		}
		if gotAgent.String != want.AgentName {
			rt.Fatalf("agent_name = %q, want %q", gotAgent.String, want.AgentName)
		}
		if gotModel.String != want.Model {
			rt.Fatalf("model = %q, want %q", gotModel.String, want.Model)
		}
		if gotCwd.String != want.Cwd {
			rt.Fatalf("cwd = %q, want %q", gotCwd.String, want.Cwd)
		}
		if gotCallPath.String != want.CallPath {
			rt.Fatalf("call_path = %q, want %q", gotCallPath.String, want.CallPath)
		}
		if gotStatus != string(canonical.StatusRunning) {
			rt.Fatalf("status = %q, want %q (a fresh SessionStarted is running)",
				gotStatus, canonical.StatusRunning)
		}
		if gotStart != want.Ts {
			rt.Fatalf("start_ts = %d, want %d (ev.Ts persisted verbatim)", gotStart, want.Ts)
		}
		if gotActivity != want.Ts {
			rt.Fatalf("last_activity_ts = %d, want %d (ev.Ts persisted verbatim)", gotActivity, want.Ts)
		}
	})
}

// TestPropertyOpsReturnedInSeqOrder asserts the production op-ordering
// invariant: the ops of a turn are read back in ascending `seq` order
// regardless of the order their OpStarted/OpFinalized events were ingested.
// This guards the ORDER BY the presenter uses to assemble the per-session
// trace (internal/presenter/session_detail_ops.go loadOps:
// `ORDER BY turn_id ASC, seq ASC, id ASC`) — the documented op order in
// data-model.md §ops (UNIQUE (turn_id, seq)) and presenter.md.
//
// The test generates one turn's worth of ops (each with a distinct,
// strictly-increasing canonical Seq), SHUFFLES the emitted event slice so
// the ingester sees them out of order, ingests them through the real
// pipeline, then reads the ops back with the SAME query string production
// uses and asserts the returned seq column is strictly increasing. It was
// previously named TestPropertyMonotoneOrdering and re-implemented
// sort.SliceStable over data engineered so SourceSeq co-increased with Ts —
// that asserted the test's own sort, not production. canonical-events.md
// §Ordering Guarantees documents SourceSeq as a per-file observability
// counter that the ingester does NOT use for ordering or persistence, so it
// has no single queryable production read path; the durable, queryable order
// production actually relies on is (turn_id, seq), which is what this guards.
func TestPropertyOpsReturnedInSeqOrder(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		const (
			nativeID = "ordering-session"
			turnSeq  = 1
		)
		nOps := rapid.IntRange(2, 12).Draw(rt, "nOps")

		// Build a single turn whose ops have strictly-increasing Seq and
		// non-decreasing timestamps. Each op is a start+finalize pair. seqs
		// is the ASCENDING ground-truth order we expect to read back.
		var srcSeq uint64
		var ts int64
		next := func() (uint64, int64) {
			srcSeq++
			ts += rapid.Int64Range(1, 100_000).Draw(rt, "tsStep")
			return srcSeq, ts
		}

		emitted := make([]canonical.Event, 0, nOps*2+1)
		// A SessionStarted anchors the session (root self-ref) so the ops
		// attach to a real session row, but requireSessionID would synthesize
		// one anyway — including it exercises the documented well-formed shape.
		s0, sts := next()
		emitted = append(emitted, canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceSeq: s0, Ts: sts},
			NativeID:     nativeID,
			RootNativeID: nativeID,
			Kind:         canonical.KindRoot,
		})
		seqs := make([]int, 0, nOps)
		for i := range nOps {
			opSeq := i + 1
			seqs = append(seqs, opSeq)
			os, ots := next()
			emitted = append(emitted, canonical.OpStartedEvent{
				EventBase:       canonical.EventBase{SourceSeq: os, Ts: ots},
				SessionNativeID: nativeID,
				TurnSeq:         turnSeq,
				Seq:             opSeq,
				ParentOpSeq:     -1,
				Kind:            genOpKind(rt),
				Name:            rapid.StringN(0, 16, 16).Draw(rt, "OpName"),
			})
			of, ofts := next()
			emitted = append(emitted, canonical.OpFinalizedEvent{
				EventBase:       canonical.EventBase{SourceSeq: of, Ts: ofts},
				SessionNativeID: nativeID,
				TurnSeq:         turnSeq,
				Seq:             opSeq,
				Status:          string(canonical.StatusCompleted),
				EndTs:           ofts,
			})
		}

		// Shuffle the receive order: the ingester must re-establish the
		// (turn_id, seq) order on read, not depend on arrival order.
		shuffled := rapid.Permutation(emitted).Draw(rt, "perm")

		ctx := context.Background()
		s, err := store.OpenWriter(ctx, ":memory:", silentLogger())
		if err != nil {
			rt.Fatalf("store.OpenWriter: %v", err)
		}
		defer func() { _ = s.Close() }()
		db := s.DB()

		const sourceID = "aiagent_v3:/property/ordering"
		ingestEvents(rt, db, sourceID, "aiagent_v3", "/property/ordering", shuffled)

		// Read ops back with the EXACT order production uses
		// (session_detail_ops.go loadOps). The canonical session id is an
		// ingest-package internal, so scope by (source_id, native_id) through
		// a join on sessions instead — same ORDER BY production applies.
		rows, err := db.QueryContext(ctx, `
SELECT o.seq FROM ops o
JOIN sessions s ON s.id = o.session_id
WHERE s.source_id = ? AND s.native_id = ?
ORDER BY o.turn_id ASC, o.seq ASC, o.id ASC`, sourceID, nativeID)
		if err != nil {
			rt.Fatalf("query ops: %v", err)
		}
		defer func() { _ = rows.Close() }()
		got := make([]int, 0, nOps)
		for rows.Next() {
			var seq int
			if scanErr := rows.Scan(&seq); scanErr != nil {
				rt.Fatalf("scan op seq: %v", scanErr)
			}
			got = append(got, seq)
		}
		if iterErr := rows.Err(); iterErr != nil {
			rt.Fatalf("iterate ops: %v", iterErr)
		}
		if diff := cmp.Diff(seqs, got); diff != "" {
			rt.Fatalf("ops not returned in ascending seq order (-want +got):\n%s", diff)
		}
	})
}

// TestPropertyIdempotentIngestion asserts the idempotent-ingestion
// invariant: ingesting the same batch of canonical events into a fresh
// Store TWICE produces zero additional rows on the second pass. This is the
// AGENTS.md "idempotent ingest" guarantee — re-scanning the same source
// produces no duplicate canonical rows — enforced at the SQL layer via
// idempotent upserts keyed on natural identity (ingester.md §Dedup and
// Idempotency).
//
// The batch is a single well-formed session (start → turn start → ops →
// turn finalize → session finalize) so the events exercise the upsert paths
// for sessions, turns, and ops together. Both passes run against ONE store
// (the natural-identity upserts must collapse the replay), matching how a
// re-scan replays into the live store.
func TestPropertyIdempotentIngestion(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		batch := genSessionBatch(rt)

		ctx := context.Background()
		s, err := store.OpenWriter(ctx, ":memory:", silentLogger())
		if err != nil {
			rt.Fatalf("store.OpenWriter: %v", err)
		}
		defer func() { _ = s.Close() }()
		db := s.DB()

		const sourceID = "aiagent_v3:/property/idempotent"
		ingestOnce := func() {
			ingestEvents(rt, db, sourceID, "aiagent_v3", "/property/idempotent", batch)
		}

		ingestOnce()
		first := countRows(rt, db)
		ingestOnce()
		second := countRows(rt, db)

		if diff := cmp.Diff(first, second); diff != "" {
			rt.Fatalf("re-ingest changed row counts (-first +second):\n%s", diff)
		}
	})
}

// genSessionBatch draws a well-formed, chronologically-ordered single
// session: SessionStarted, TurnStarted, 1..n OpStarted/OpFinalized pairs,
// TurnFinalized, SessionFinalized. SourceSeq increases monotonically and Ts
// is non-decreasing, matching the adapter emission contract the writer
// relies on. All native ids are fixed within the batch so a replay collides
// on natural identity.
func genSessionBatch(rt *rapid.T) []canonical.Event {
	const (
		nativeID = "prop-session-1"
		turnSeq  = 1
	)
	nOps := rapid.IntRange(1, 6).Draw(rt, "nOps")

	var seq uint64
	var ts int64
	next := func() (uint64, int64) {
		seq++
		ts += rapid.Int64Range(1, 100_000).Draw(rt, "tsStep")
		return seq, ts
	}

	out := make([]canonical.Event, 0, nOps*2+4)

	s, sts := next()
	out = append(out, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: "", SourceSeq: s, Ts: sts},
		NativeID:     nativeID,
		RootNativeID: nativeID,
		Kind:         canonical.KindRoot,
		AgentName:    rapid.StringN(0, 16, 16).Draw(rt, "AgentName"),
	})

	ts0, tts := next()
	out = append(out, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceSeq: ts0, Ts: tts},
		SessionNativeID: nativeID,
		Seq:             turnSeq,
	})

	for i := range nOps {
		os, ots := next()
		out = append(out, canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceSeq: os, Ts: ots},
			SessionNativeID: nativeID,
			TurnSeq:         turnSeq,
			Seq:             i + 1,
			ParentOpSeq:     -1,
			Kind:            genOpKind(rt),
			Name:            rapid.StringN(0, 16, 16).Draw(rt, "OpName"),
		})
		of, ofts := next()
		out = append(out, canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceSeq: of, Ts: ofts},
			SessionNativeID: nativeID,
			TurnSeq:         turnSeq,
			Seq:             i + 1,
			Status:          string(canonical.StatusCompleted),
			EndTs:           ofts,
		})
	}

	tf, tfts := next()
	out = append(out, canonical.TurnFinalizedEvent{
		EventBase:       canonical.EventBase{SourceSeq: tf, Ts: tfts},
		SessionNativeID: nativeID,
		Seq:             turnSeq,
		Status:          string(canonical.StatusCompleted),
		EndTs:           tfts,
	})

	sf, sfts := next()
	out = append(out, canonical.SessionFinalizedEvent{
		EventBase: canonical.EventBase{SourceSeq: sf, Ts: sfts},
		NativeID:  nativeID,
		Status:    canonical.StatusCompleted,
		EndTs:     sfts,
	})
	return out
}

// rowCounts captures the populated-table cardinalities the idempotency
// invariant compares across the two ingest passes.
type rowCounts struct {
	Sessions int64
	Turns    int64
	Ops      int64
	Logs     int64
}

// countRows reads the row counts for the data tables a session batch
// populates. A second ingest of the same batch must leave every count
// unchanged (idempotent upserts).
func countRows(rt *rapid.T, db *sql.DB) rowCounts {
	count := func(table string) int64 {
		var n int64
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			rt.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	return rowCounts{
		Sessions: count("sessions"),
		Turns:    count("turns"),
		Ops:      count("ops"),
		Logs:     count("log_entries"),
	}
}

// TestPropertySchemaCompleteness asserts the schema-completeness invariant
// against the WRITER, not the generator: a full generated session — driven
// through the real ingest pipeline — persists EVERY column
// canonical-events.md (and the schema's NOT NULL constraints) mark REQUIRED
// as non-null / non-empty in SQLite. The previous version drew the "required"
// values from rapid.StringN(1,…) (always non-empty) and then asserted they
// were non-empty — it tested the generator. This version reads the persisted
// rows back and fails if the writer drops a required field on the way to the
// database (e.g. a regression that stopped persisting ops.kind, log.message,
// or payload_ref.location_uri).
//
// Persisted required columns checked, by table (canonical-events.md + the
// schema's NOT NULL columns in store/migrations/0001_initial.sql):
//   - sessions:     native_id, kind, status, root_session_id, start_ts,
//     last_activity_ts.
//   - turns:        session_id, seq, status, start_ts.
//   - ops:          session_id, turn_id, kind, status, start_ts.
//   - payload_refs: op_id, kind, format, location_uri.
//   - log_entries:  severity, source, message, ts (+ session/source owner).
//
// Every event type that the ingester actually persists is driven here
// (SessionStarted, TurnStarted, OpStarted, OpFinalized, TurnFinalized,
// SessionFinalized, PayloadRef, LogEntry). SourceProgress/SourceError write
// cursor/parse-error state rather than these data rows and are covered by the
// ingester's own tests; SessionUpdated only UPDATEs an existing row.
func TestPropertySchemaCompleteness(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		batch, want := genFullSession(rt)

		ctx := context.Background()
		s, err := store.OpenWriter(ctx, ":memory:", silentLogger())
		if err != nil {
			rt.Fatalf("store.OpenWriter: %v", err)
		}
		defer func() { _ = s.Close() }()
		db := s.DB()

		const sourceID = "aiagent_v3:/property/schema"
		ingestEvents(rt, db, sourceID, "aiagent_v3", "/property/schema", batch)

		assertSessionRequired(rt, db, sourceID, want)
		assertTurnRequired(rt, db, sourceID, want)
		assertOpRequired(rt, db, sourceID, want)
		assertPayloadRefRequired(rt, db, sourceID)
		assertLogRequired(rt, db, sourceID, want)
	})
}

// fullSessionWant records the generated values the schema-completeness
// assertions compare the persisted rows against (the values the writer must
// preserve verbatim, plus expected counts).
type fullSessionWant struct {
	nativeID string
	kind     canonical.SessionKind
	turnSeq  int
	opSeq    int
	severity string
	logMsg   string
}

// genFullSession draws a single well-formed session covering every event the
// ingester persists into a data row: SessionStarted → TurnStarted →
// OpStarted/OpFinalized → PayloadRef (on the op) → LogEntry → TurnFinalized →
// SessionFinalized. Required fields carry generated NON-EMPTY values;
// genuinely-optional fields are drawn from the may-be-empty generator so the
// optional path is exercised too. Returns the event slice plus the expected
// values for the read-back assertions.
func genFullSession(rt *rapid.T) ([]canonical.Event, fullSessionWant) {
	nonEmpty := rapid.StringN(1, 32, 32)
	optional := rapid.String()

	want := fullSessionWant{
		nativeID: nonEmpty.Draw(rt, "nativeID"),
		kind:     genSessionKind(rt),
		turnSeq:  1,
		opSeq:    1,
		severity: rapid.SampledFrom([]string{"DBG", "INF", "WRN", "ERR"}).Draw(rt, "severity"),
		logMsg:   nonEmpty.Draw(rt, "logMsg"),
	}

	var srcSeq uint64
	var ts int64
	next := func() (uint64, int64) {
		srcSeq++
		ts += rapid.Int64Range(1, 100_000).Draw(rt, "tsStep")
		return srcSeq, ts
	}

	out := make([]canonical.Event, 0, 8)

	s0, sts := next()
	out = append(out, canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceSeq: s0, Ts: sts},
		NativeID:     want.nativeID,
		RootNativeID: want.nativeID, // self-root keeps root_session_id FK satisfiable
		Kind:         want.kind,
		AgentName:    optional.Draw(rt, "agentName"),
		Model:        optional.Draw(rt, "model"),
	})

	ts0, tts := next()
	out = append(out, canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceSeq: ts0, Ts: tts},
		SessionNativeID: want.nativeID,
		Seq:             want.turnSeq,
	})

	os, ots := next()
	out = append(out, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceSeq: os, Ts: ots},
		SessionNativeID: want.nativeID,
		TurnSeq:         want.turnSeq,
		Seq:             want.opSeq,
		ParentOpSeq:     -1,
		Kind:            genOpKind(rt),
		Name:            optional.Draw(rt, "opName"),
	})
	of, ofts := next()
	out = append(out, canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceSeq: of, Ts: ofts},
		SessionNativeID: want.nativeID,
		TurnSeq:         want.turnSeq,
		Seq:             want.opSeq,
		Status:          string(canonical.StatusCompleted),
		EndTs:           ofts,
	})

	// PayloadRef hangs off the op above (TurnSeq/OpSeq must match), so the
	// writer's op-existence guard passes and the row persists.
	pr, prts := next()
	out = append(out, canonical.PayloadRefEvent{
		EventBase:       canonical.EventBase{SourceSeq: pr, Ts: prts},
		SessionNativeID: want.nativeID,
		TurnSeq:         want.turnSeq,
		OpSeq:           want.opSeq,
		PayloadKind: rapid.SampledFrom([]string{
			"llm_request", "llm_response", "tool_request", "tool_response", "log",
		}).Draw(rt, "payloadKind"),
		Format: rapid.SampledFrom([]string{
			"http", "sse", "json", "jsonrpc", "text", "binary",
		}).Draw(rt, "payloadFormat"),
		LocationURI: "file://" + nonEmpty.Draw(rt, "locationURI"),
	})

	le, lets := next()
	out = append(out, canonical.LogEntryEvent{
		EventBase:       canonical.EventBase{SourceSeq: le, Ts: lets},
		SessionNativeID: want.nativeID,
		TurnSeq:         want.turnSeq,
		OpSeq:           want.opSeq,
		Severity:        want.severity,
		Source:          nonEmpty.Draw(rt, "logSource"),
		Message:         want.logMsg,
	})

	tf, tfts := next()
	out = append(out, canonical.TurnFinalizedEvent{
		EventBase:       canonical.EventBase{SourceSeq: tf, Ts: tfts},
		SessionNativeID: want.nativeID,
		Seq:             want.turnSeq,
		Status:          string(canonical.StatusCompleted),
		EndTs:           tfts,
	})

	sf, sfts := next()
	out = append(out, canonical.SessionFinalizedEvent{
		EventBase: canonical.EventBase{SourceSeq: sf, Ts: sfts},
		NativeID:  want.nativeID,
		Status:    canonical.StatusCompleted,
		EndTs:     sfts,
	})

	return out, want
}

// assertSessionRequired reads the persisted sessions row and asserts every
// REQUIRED column is populated. native_id and kind must equal the generated
// values; status/root_session_id must be non-empty; start_ts/last_activity_ts
// must be positive (writer.go applySessionStarted persists ev.Ts into both).
func assertSessionRequired(rt *rapid.T, db *sql.DB, sourceID string, want fullSessionWant) {
	rt.Helper()
	var (
		nativeID, kind, status, rootID string
		startTS, activityTS            int64
	)
	if err := db.QueryRowContext(context.Background(), `
SELECT native_id, kind, status, root_session_id, start_ts, last_activity_ts
FROM sessions WHERE source_id = ? AND native_id = ?`, sourceID, want.nativeID).
		Scan(&nativeID, &kind, &status, &rootID, &startTS, &activityTS); err != nil {
		rt.Fatalf("read session: %v", err)
	}
	if nativeID != want.nativeID {
		rt.Fatalf("sessions.native_id = %q, want %q", nativeID, want.nativeID)
	}
	if kind != string(want.kind) {
		rt.Fatalf("sessions.kind = %q, want %q", kind, want.kind)
	}
	if status == "" {
		rt.Fatalf("sessions.status empty (required)")
	}
	if rootID == "" {
		rt.Fatalf("sessions.root_session_id empty (required)")
	}
	if startTS <= 0 {
		rt.Fatalf("sessions.start_ts not positive: %d", startTS)
	}
	if activityTS <= 0 {
		rt.Fatalf("sessions.last_activity_ts not positive: %d", activityTS)
	}
}

// assertTurnRequired reads the persisted turns row and asserts its required
// columns: seq matches, status non-empty, start_ts positive.
func assertTurnRequired(rt *rapid.T, db *sql.DB, sourceID string, want fullSessionWant) {
	rt.Helper()
	var (
		seq     int
		status  string
		startTS int64
	)
	if err := db.QueryRowContext(context.Background(), `
SELECT t.seq, t.status, t.start_ts
FROM turns t JOIN sessions s ON s.id = t.session_id
WHERE s.source_id = ? AND s.native_id = ?`, sourceID, want.nativeID).
		Scan(&seq, &status, &startTS); err != nil {
		rt.Fatalf("read turn: %v", err)
	}
	if seq != want.turnSeq {
		rt.Fatalf("turns.seq = %d, want %d", seq, want.turnSeq)
	}
	if status == "" {
		rt.Fatalf("turns.status empty (required)")
	}
	if startTS <= 0 {
		rt.Fatalf("turns.start_ts not positive: %d", startTS)
	}
}

// assertOpRequired reads the persisted ops row and asserts its required
// columns: kind non-empty (it must equal one of the canonical OpKind values
// the generator drew), status non-empty, start_ts positive, seq matches.
func assertOpRequired(rt *rapid.T, db *sql.DB, sourceID string, want fullSessionWant) {
	rt.Helper()
	var (
		seq     int
		kind    string
		status  string
		startTS int64
	)
	if err := db.QueryRowContext(context.Background(), `
SELECT o.seq, o.kind, o.status, o.start_ts
FROM ops o JOIN sessions s ON s.id = o.session_id
WHERE s.source_id = ? AND s.native_id = ?`, sourceID, want.nativeID).
		Scan(&seq, &kind, &status, &startTS); err != nil {
		rt.Fatalf("read op: %v", err)
	}
	if seq != want.opSeq {
		rt.Fatalf("ops.seq = %d, want %d", seq, want.opSeq)
	}
	if kind == "" {
		rt.Fatalf("ops.kind empty (required)")
	}
	if status == "" {
		rt.Fatalf("ops.status empty (required)")
	}
	if startTS <= 0 {
		rt.Fatalf("ops.start_ts not positive: %d", startTS)
	}
}

// assertPayloadRefRequired reads the persisted payload_refs row and asserts
// its required columns (kind, format, location_uri) are non-empty.
func assertPayloadRefRequired(rt *rapid.T, db *sql.DB, sourceID string) {
	rt.Helper()
	var kind, format, locationURI string
	if err := db.QueryRowContext(context.Background(), `
SELECT pr.kind, pr.format, pr.location_uri
FROM payload_refs pr
JOIN ops o ON o.id = pr.op_id
JOIN sessions s ON s.id = o.session_id
WHERE s.source_id = ?`, sourceID).
		Scan(&kind, &format, &locationURI); err != nil {
		rt.Fatalf("read payload_ref: %v", err)
	}
	if kind == "" || format == "" || locationURI == "" {
		rt.Fatalf("payload_refs required field empty: kind=%q format=%q location_uri=%q",
			kind, format, locationURI)
	}
}

// assertLogRequired reads the persisted log_entries row and asserts its
// required columns: severity and message equal the generated values, source
// non-empty, ts positive, and exactly one owner (session_id) is set.
func assertLogRequired(rt *rapid.T, db *sql.DB, sourceID string, want fullSessionWant) {
	rt.Helper()
	var (
		severity, source, message string
		ts                        int64
		sessionID                 sql.NullString
	)
	if err := db.QueryRowContext(context.Background(), `
SELECT l.severity, l.source, l.message, l.ts, l.session_id
FROM log_entries l JOIN sessions s ON s.id = l.session_id
WHERE s.source_id = ? AND s.native_id = ?`, sourceID, want.nativeID).
		Scan(&severity, &source, &message, &ts, &sessionID); err != nil {
		rt.Fatalf("read log_entry: %v", err)
	}
	if severity != want.severity {
		rt.Fatalf("log_entries.severity = %q, want %q", severity, want.severity)
	}
	if message != want.logMsg {
		rt.Fatalf("log_entries.message = %q, want %q", message, want.logMsg)
	}
	if source == "" {
		rt.Fatalf("log_entries.source empty (required)")
	}
	if ts <= 0 {
		rt.Fatalf("log_entries.ts not positive: %d", ts)
	}
	if !sessionID.Valid || sessionID.String == "" {
		rt.Fatalf("log_entries.session_id empty (required owner)")
	}
}

// TestPropertyTimestampPrecision asserts the timestamp-precision invariant
// against the WRITER + STORE: a microsecond-precision timestamp carried on a
// canonical event survives the write path into SQLite with ZERO drift. The
// canonical model stores time as int64 UNIX-microseconds (EventBase.Ts) and
// the schema declares every timestamp column INTEGER UNIX-microseconds
// (store/migrations/0001_initial.sql); the writer binds ev.Ts directly with
// no rescale (writer.go applySessionStarted persists ev.Ts into
// sessions.start_ts; applyOpStarted persists ev.Ts into ops.start_ts).
//
// The test ingests a SessionStarted + an OpStarted both carrying the SAME
// generated µs value, reads sessions.start_ts and ops.start_ts back, and
// asserts both equal the emitted µs EXACTLY. It fails if the writer (or a
// future schema/driver change) truncates to seconds/milliseconds, rescales,
// or otherwise loses microsecond resolution. The previous version round-
// tripped a value through Go's own time.Format/time.Parse — pure stdlib, no
// production code — so it could never catch a writer/storage precision bug.
func TestPropertyTimestampPrecision(t *testing.T) {
	t.Parallel()
	nowMicros := time.Now().UTC().UnixMicro()
	rapid.Check(t, func(rt *rapid.T) {
		// A positive µs value anywhere in (epoch, now]. Microsecond
		// granularity is the precision the canonical model and schema store;
		// it must reach SQLite unchanged.
		micros := rapid.Int64Range(1, nowMicros).Draw(rt, "micros")

		const (
			nativeID = "ts-session"
			turnSeq  = 1
			opSeq    = 1
		)
		batch := []canonical.Event{
			canonical.SessionStartedEvent{
				EventBase:    canonical.EventBase{SourceSeq: 1, Ts: micros},
				NativeID:     nativeID,
				RootNativeID: nativeID,
				Kind:         canonical.KindRoot,
			},
			canonical.OpStartedEvent{
				EventBase:       canonical.EventBase{SourceSeq: 2, Ts: micros},
				SessionNativeID: nativeID,
				TurnSeq:         turnSeq,
				Seq:             opSeq,
				ParentOpSeq:     -1,
				Kind:            canonical.OpLLM,
			},
		}

		ctx := context.Background()
		s, err := store.OpenWriter(ctx, ":memory:", silentLogger())
		if err != nil {
			rt.Fatalf("store.OpenWriter: %v", err)
		}
		defer func() { _ = s.Close() }()
		db := s.DB()

		const sourceID = "aiagent_v3:/property/timestamp"
		ingestEvents(rt, db, sourceID, "aiagent_v3", "/property/timestamp", batch)

		var sessionStart int64
		if err := db.QueryRowContext(ctx,
			`SELECT start_ts FROM sessions WHERE source_id = ? AND native_id = ?`,
			sourceID, nativeID).Scan(&sessionStart); err != nil {
			rt.Fatalf("read session start_ts: %v", err)
		}
		if sessionStart != micros {
			rt.Fatalf("sessions.start_ts drift: in=%d out=%d (drift %d µs)",
				micros, sessionStart, sessionStart-micros)
		}

		var opStart int64
		if err := db.QueryRowContext(ctx, `
SELECT o.start_ts FROM ops o JOIN sessions s ON s.id = o.session_id
WHERE s.source_id = ? AND s.native_id = ?`, sourceID, nativeID).Scan(&opStart); err != nil {
			rt.Fatalf("read op start_ts: %v", err)
		}
		if opStart != micros {
			rt.Fatalf("ops.start_ts drift: in=%d out=%d (drift %d µs)",
				micros, opStart, opStart-micros)
		}
	})
}
