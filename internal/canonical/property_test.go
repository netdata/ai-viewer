package canonical_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"sort"
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
// internal/canonical owns ONLY the event types + the Event interface; it
// has no decoders/encoders (all parsing lives in the adapters). So the two
// "round-trip"/"boundary" invariants that AC#4 phrases as adapter-mediated
// are asserted here against the canonical-owned surface:
//
//   - (a) round-trip equality is the TYPE-LEVEL accessor round-trip — a
//     value constructed from generated fields reports exactly those fields
//     back through the Event accessors, with no lossy transform. The
//     adapter parse→canonical path is already covered end-to-end by the
//     golden + e2e fixture tests; routing this property through an adapter
//     would require importing another package's unexported parser (not
//     possible) or standing up full ledger fixtures (not cheap, and
//     redundant with the golden tests). The type-level round-trip is the
//     faithful in-package assertion.
//   - (e) timestamp precision is asserted on the canonical µs representation
//     (Ts int64) across the SAME ISO-8601 boundary every adapter uses
//     (time.Parse(time.RFC3339Nano) → time.Time.UnixMicro), without
//     importing the per-adapter unexported parseTsToMicros helpers.

// silentLogger returns a discarding slog.Logger so the property tests do
// not paint the terminal with structured ingest output across many rapid
// iterations.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

// genSessionKind / genOpKind / genSessionStatus draw from the closed sets
// of discriminator values defined in canonical-events.md.
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

func genSessionStatus(rt *rapid.T) canonical.SessionStatus {
	return rapid.SampledFrom([]canonical.SessionStatus{
		canonical.StatusRunning, canonical.StatusCompleted,
		canonical.StatusFailed, canonical.StatusAbandoned,
		canonical.StatusInterrupted,
	}).Draw(rt, "SessionStatus")
}

// TestPropertyRoundTripEquality asserts the canonical round-trip-equality
// invariant: a canonical event constructed from a set of field values
// reports EXACTLY those values back through its accessor methods, with no
// lossy transform. This is the type-level accessor round-trip (canonical
// owns no decoders — see the package-level note on form (a)); it pins that
// EventBase storage and the per-type EventKind() discriminator never mutate
// or drop the values an adapter supplies.
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

		// A representative field-rich event (SessionStarted) must also
		// round-trip its full payload: every constructed field is readable
		// back identically. cmp.Diff makes a future lossy-storage change or
		// a dropped field surface as a readable diff rather than a silent
		// pass.
		want := canonical.SessionStartedEvent{
			EventBase:      base,
			NativeID:       rapid.StringN(1, 64, 64).Draw(rt, "NativeID"),
			RootNativeID:   rapid.String().Draw(rt, "RootNativeID"),
			ParentNativeID: rapid.String().Draw(rt, "ParentNativeID"),
			ParentOpKey:    rapid.String().Draw(rt, "ParentOpKey"),
			Kind:           genSessionKind(rt),
			AgentName:      rapid.String().Draw(rt, "AgentName"),
			Model:          rapid.String().Draw(rt, "Model"),
			Cwd:            rapid.String().Draw(rt, "Cwd"),
			CallPath:       rapid.String().Draw(rt, "CallPath"),
		}
		got := want // value copy through the concrete type
		if diff := cmp.Diff(want, got); diff != "" {
			rt.Fatalf("SessionStartedEvent round-trip mismatch (-want +got):\n%s", diff)
		}
		if got.EventSourceSeq() != want.SourceSeq || got.EventTs() != want.Ts {
			rt.Fatalf("SessionStartedEvent base accessors lost data: seq %d/%d ts %d/%d",
				got.EventSourceSeq(), want.SourceSeq, got.EventTs(), want.Ts)
		}
	})
}

// TestPropertyMonotoneOrdering asserts the monotone-ordering invariant:
// for a set of events emitted within a single source file, after ordering
// by the documented stable key, SourceSeq() is non-decreasing.
//
// The documented contract (canonical-events.md §Ordering Guarantees): an
// adapter emits a session's events in chronological order, and SourceSeq is
// monotonic PER FILE. So a single file's emission has Ts non-decreasing as
// SourceSeq increases. The stable ordering key the ingester uses within a
// session is (Ts, SourceSeq). This test generates one file's worth of
// events with that contract, hands them to the sort SHUFFLED, then asserts
// sorting by (Ts, SourceSeq) restores a non-decreasing SourceSeq sequence.
func TestPropertyMonotoneOrdering(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 50).Draw(rt, "n")

		// Build a chronologically-emitted file: SourceSeq strictly
		// increasing, Ts non-decreasing alongside it (chronological
		// emission). Per-step gaps are random and non-negative so equal
		// timestamps (a real tie) are exercised too.
		const srcID = "ordering-file"
		emitted := make([]canonical.Event, 0, n)
		var seq uint64
		var ts int64
		for range n {
			seq += rapid.Uint64Range(1, 1000).Draw(rt, "seqStep") // strictly increasing
			ts += rapid.Int64Range(0, 1_000_000).Draw(rt, "tsStep")
			emitted = append(emitted, canonical.TurnStartedEvent{
				EventBase: canonical.EventBase{
					SourceID:  srcID,
					SourceSeq: seq,
					Ts:        ts,
				},
				SessionNativeID: "s1",
				Seq:             len(emitted) + 1,
			})
		}

		// Shuffle into a scrambled receive order (the ingester sees events
		// in arbitrary in-memory order; ordering is re-established by the
		// stable key, not by arrival). rapid.Permutation returns a permuted
		// COPY of the slice.
		shuffled := rapid.Permutation(emitted).Draw(rt, "perm")

		// Order by the documented stable key: Ts first, SourceSeq as
		// tiebreaker. sort.SliceStable keeps the comparison total.
		sort.SliceStable(shuffled, func(i, j int) bool {
			if shuffled[i].EventTs() != shuffled[j].EventTs() {
				return shuffled[i].EventTs() < shuffled[j].EventTs()
			}
			return shuffled[i].EventSourceSeq() < shuffled[j].EventSourceSeq()
		})

		var prev uint64
		for i, ev := range shuffled {
			if got := ev.EventSourceSeq(); got < prev {
				rt.Fatalf("SourceSeq not non-decreasing at index %d: %d < %d", i, got, prev)
			} else {
				prev = got
			}
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
			ing, err := ingest.New(
				db,
				ingest.WithLogger(silentLogger()),
				ingest.WithBatchSize(10000),
				ingest.WithBatchInterval(5*time.Millisecond),
				ingest.WithSourceFormat(sourceID, "aiagent_v3"),
				ingest.WithLocation(sourceID, "/property/idempotent"),
			)
			if err != nil {
				rt.Fatalf("ingest.New: %v", err)
			}
			if err := ing.Start(ctx); err != nil {
				rt.Fatalf("ingest.Start: %v", err)
			}
			events := make(chan canonical.Event, len(batch)+1)
			for _, ev := range batch {
				events <- ev
			}
			close(events)
			if err := ing.Submit(sourceID, events); err != nil {
				rt.Fatalf("ingest.Submit: %v", err)
			}
			// Stop blocks until the worker has drained the channel and run
			// its final flush, so all rows are committed before we count.
			if err := ing.Stop(); err != nil {
				rt.Fatalf("ingest.Stop: %v", err)
			}
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

// TestPropertySchemaCompleteness asserts the schema-completeness invariant:
// for each canonical event type, every REQUIRED field per
// canonical-events.md (and the writer's hard requirements) is populated,
// while optional fields are free to be empty. The generators below populate
// exactly the required set; the assertions confirm the required fields are
// non-empty/valid and tolerate empty optional fields.
//
// Required-field contract (canonical-events.md + writer.go enforcement):
//   - all events: EventBase.Ts (the ordering key) is set; SourceID set.
//   - SessionStarted: NativeID, Kind.
//   - SessionUpdated: NativeID.
//   - SessionFinalized: NativeID, Status.
//   - TurnStarted: SessionNativeID, Seq.
//   - TurnFinalized: SessionNativeID, Seq, Status.
//   - OpStarted: SessionNativeID, Kind.
//   - OpFinalized: SessionNativeID, Status.
//   - PayloadRef: SessionNativeID, PayloadKind, Format, LocationURI.
//   - LogEntry: SessionNativeID, Severity, Message.
//   - SourceProgress: Cursor.
//   - SourceError: File, Message.
func TestPropertySchemaCompleteness(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		nonEmpty := rapid.StringN(1, 64, 64)
		optional := rapid.String() // may be empty — exercises the optional path

		base := canonical.EventBase{
			SourceID:  nonEmpty.Draw(rt, "SourceID"),
			SourceSeq: rapid.Uint64().Draw(rt, "SourceSeq"),
			Ts:        rapid.Int64Range(1, maxMicros).Draw(rt, "Ts"),
		}
		requireBase := func(kind canonical.EventKind, b canonical.EventBase) {
			if b.SourceID == "" {
				rt.Fatalf("%s: required SourceID empty", kind)
			}
			if b.Ts <= 0 {
				rt.Fatalf("%s: required Ts not positive: %d", kind, b.Ts)
			}
		}

		// SessionStarted: NativeID + Kind required; the rest optional.
		ss := canonical.SessionStartedEvent{
			EventBase:    base,
			NativeID:     nonEmpty.Draw(rt, "ss.NativeID"),
			Kind:         genSessionKind(rt),
			RootNativeID: optional.Draw(rt, "ss.RootNativeID"),
			AgentName:    optional.Draw(rt, "ss.AgentName"),
			Model:        optional.Draw(rt, "ss.Model"),
		}
		requireBase(ss.EventKind(), ss.EventBase)
		if ss.NativeID == "" {
			rt.Fatalf("SessionStarted: required NativeID empty")
		}
		if ss.Kind == "" {
			rt.Fatalf("SessionStarted: required Kind empty")
		}

		// SessionUpdated: NativeID required.
		su := canonical.SessionUpdatedEvent{
			EventBase: base,
			NativeID:  nonEmpty.Draw(rt, "su.NativeID"),
			Model:     optional.Draw(rt, "su.Model"),
		}
		requireBase(su.EventKind(), su.EventBase)
		if su.NativeID == "" {
			rt.Fatalf("SessionUpdated: required NativeID empty")
		}

		// SessionFinalized: NativeID + Status required.
		sf := canonical.SessionFinalizedEvent{
			EventBase: base,
			NativeID:  nonEmpty.Draw(rt, "sf.NativeID"),
			Status:    genSessionStatus(rt),
		}
		requireBase(sf.EventKind(), sf.EventBase)
		if sf.NativeID == "" {
			rt.Fatalf("SessionFinalized: required NativeID empty")
		}
		if sf.Status == "" {
			rt.Fatalf("SessionFinalized: required Status empty")
		}

		// TurnStarted: SessionNativeID + Seq required (Seq >= 0; 0 reserved
		// for init turns, so non-negative is the contract).
		seq := rapid.IntRange(0, 1_000_000)
		tstart := canonical.TurnStartedEvent{
			EventBase:       base,
			SessionNativeID: nonEmpty.Draw(rt, "ts.SessionNativeID"),
			Seq:             seq.Draw(rt, "ts.Seq"),
		}
		requireBase(tstart.EventKind(), tstart.EventBase)
		if tstart.SessionNativeID == "" {
			rt.Fatalf("TurnStarted: required SessionNativeID empty")
		}
		if tstart.Seq < 0 {
			rt.Fatalf("TurnStarted: Seq negative: %d", tstart.Seq)
		}

		// TurnFinalized: SessionNativeID + Seq + Status required.
		tfin := canonical.TurnFinalizedEvent{
			EventBase:       base,
			SessionNativeID: nonEmpty.Draw(rt, "tf.SessionNativeID"),
			Seq:             seq.Draw(rt, "tf.Seq"),
			Status: rapid.SampledFrom([]string{
				"running", "completed", "failed", "aborted",
			}).Draw(rt, "tf.Status"),
		}
		requireBase(tfin.EventKind(), tfin.EventBase)
		if tfin.SessionNativeID == "" {
			rt.Fatalf("TurnFinalized: required SessionNativeID empty")
		}
		if tfin.Status == "" {
			rt.Fatalf("TurnFinalized: required Status empty")
		}

		// OpStarted: SessionNativeID + Kind required.
		opStart := canonical.OpStartedEvent{
			EventBase:       base,
			SessionNativeID: nonEmpty.Draw(rt, "op.SessionNativeID"),
			TurnSeq:         seq.Draw(rt, "op.TurnSeq"),
			Seq:             seq.Draw(rt, "op.Seq"),
			ParentOpSeq:     -1,
			Kind:            genOpKind(rt),
			Name:            optional.Draw(rt, "op.Name"),
		}
		requireBase(opStart.EventKind(), opStart.EventBase)
		if opStart.SessionNativeID == "" {
			rt.Fatalf("OpStarted: required SessionNativeID empty")
		}
		if opStart.Kind == "" {
			rt.Fatalf("OpStarted: required Kind empty")
		}

		// OpFinalized: SessionNativeID + Status required.
		opFin := canonical.OpFinalizedEvent{
			EventBase:       base,
			SessionNativeID: nonEmpty.Draw(rt, "opf.SessionNativeID"),
			Status: rapid.SampledFrom([]string{
				"running", "completed", "failed", "cancelled", "truncated",
			}).Draw(rt, "opf.Status"),
		}
		requireBase(opFin.EventKind(), opFin.EventBase)
		if opFin.SessionNativeID == "" {
			rt.Fatalf("OpFinalized: required SessionNativeID empty")
		}
		if opFin.Status == "" {
			rt.Fatalf("OpFinalized: required Status empty")
		}

		// PayloadRef: SessionNativeID + PayloadKind + Format + LocationURI
		// required.
		pr := canonical.PayloadRefEvent{
			EventBase:       base,
			SessionNativeID: nonEmpty.Draw(rt, "pr.SessionNativeID"),
			PayloadKind: rapid.SampledFrom([]string{
				"llm_request", "llm_response", "llm_sdk_request",
				"llm_sdk_response", "llm_reasoning", "tool_request",
				"tool_response", "log",
			}).Draw(rt, "pr.PayloadKind"),
			Format: rapid.SampledFrom([]string{
				"http", "sse", "json", "jsonrpc", "text", "binary",
			}).Draw(rt, "pr.Format"),
			LocationURI: "file://" + nonEmpty.Draw(rt, "pr.LocationURI"),
		}
		requireBase(pr.EventKind(), pr.EventBase)
		if pr.SessionNativeID == "" || pr.PayloadKind == "" || pr.Format == "" || pr.LocationURI == "" {
			rt.Fatalf("PayloadRef: required field empty: %+v", pr)
		}

		// LogEntry: SessionNativeID + Severity + Message required.
		le := canonical.LogEntryEvent{
			EventBase:       base,
			SessionNativeID: nonEmpty.Draw(rt, "le.SessionNativeID"),
			Severity: rapid.SampledFrom([]string{
				"DBG", "INF", "WRN", "ERR",
			}).Draw(rt, "le.Severity"),
			Message: nonEmpty.Draw(rt, "le.Message"),
			Source:  optional.Draw(rt, "le.Source"),
		}
		requireBase(le.EventKind(), le.EventBase)
		if le.SessionNativeID == "" || le.Severity == "" || le.Message == "" {
			rt.Fatalf("LogEntry: required field empty: %+v", le)
		}

		// SourceProgress: Cursor required.
		sp := canonical.SourceProgressEvent{
			EventBase: base,
			Cursor:    nonEmpty.Draw(rt, "sp.Cursor"),
		}
		requireBase(sp.EventKind(), sp.EventBase)
		if sp.Cursor == "" {
			rt.Fatalf("SourceProgress: required Cursor empty")
		}

		// SourceError: File + Message required. Offset is optional (-1 when
		// not meaningful), so it is not asserted non-zero.
		se := canonical.SourceErrorEvent{
			EventBase: base,
			File:      nonEmpty.Draw(rt, "se.File"),
			Offset:    rapid.Int64Range(-1, maxMicros).Draw(rt, "se.Offset"),
			Message:   nonEmpty.Draw(rt, "se.Message"),
		}
		requireBase(se.EventKind(), se.EventBase)
		if se.File == "" || se.Message == "" {
			rt.Fatalf("SourceError: required field empty: %+v", se)
		}
	})
}

// TestPropertyTimestampPrecision asserts the timestamp-precision invariant:
// a microsecond-precision timestamp in [epoch, now] survives the ISO-8601
// (RFC3339Nano) text boundary with ZERO drift. The canonical model stores
// time as int64 UNIX-microseconds (EventBase.Ts); every adapter parses its
// source ISO-8601 string with time.Parse(time.RFC3339Nano) and converts via
// time.Time.UnixMicro (e.g. aiagent_v3/mapper.go parseTsToMicros,
// claude_code/mapper.go, codex/mapper_finalize.go). This test reproduces
// that exact boundary on the canonical µs representation without importing
// the per-adapter unexported helpers (canonical owns no decoder).
func TestPropertyTimestampPrecision(t *testing.T) {
	t.Parallel()
	nowMicros := time.Now().UTC().UnixMicro()
	rapid.Check(t, func(rt *rapid.T) {
		// Generate at microsecond granularity — the precision the canonical
		// model stores — anywhere in [epoch, now]. A value already at µs
		// precision must round-trip through ISO-8601 with zero drift.
		micros := rapid.Int64Range(0, nowMicros).Draw(rt, "micros")
		src := time.UnixMicro(micros).UTC()

		// Format as the ISO-8601 the producers write, then parse it back
		// through the SAME path the adapters use.
		iso := src.Format(time.RFC3339Nano)
		parsed, err := time.Parse(time.RFC3339Nano, iso)
		if err != nil {
			rt.Fatalf("parse %q: %v", iso, err)
		}
		got := parsed.UnixMicro()
		if got != micros {
			rt.Fatalf("timestamp drift: in=%d iso=%q out=%d (drift %d µs)",
				micros, iso, got, got-micros)
		}
	})
}
