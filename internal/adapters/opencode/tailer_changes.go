package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the SHARED change-processing helper both poll loops use
// (processChanges) and the poll-loop cadence STATE MACHINE (pollState). It is
// split out of tailer.go to keep each file ≤400 lines. processChanges is the one
// "delta → affected sessions → reload → map → emit → checkpoint" pipeline:
// scanLoop runs it once over the whole backfill; tailLoop runs it per change
// cycle.
//
// CHECKPOINT-AFTER-EMIT INVARIANT (SOW-0005 P1.1, data-loss fix): a
// SourceProgress checkpoint carrying cursor W is emitted ONLY after every session
// affected by rows ≤ W in this run has been reloaded, mapped, and emitted. The
// pipeline therefore runs in BOUNDED BATCHES: each batch pages ≤ progressEveryRows
// delta rows forward (advancing the per-table watermark), reloadAndEmits that
// batch's affected sessions, and ONLY THEN checkpoints the batch cursor. A crash
// or ctx-cancel mid-batch returns the LAST fully-committed cursor (the previous
// batch's), never the in-progress batch's scanned watermark — so a restart from
// the persisted cursor can never resume PAST rows whose canonical events were
// never emitted (adapter-opencode.md §"Read Strategy" → "checkpoint-after-emit").

// processChanges pages every tracked table forward from `cur` in bounded batches,
// reloading+emitting each batch's affected sessions BEFORE checkpointing that
// batch's cursor, and returns the advanced cursor + whether anything advanced. It
// is used by BOTH scanLoop (whole backfill) and tailLoop (one cycle). The
// returned cursor is always a checkpoint-safe one: every session for a row it
// covers has been emitted.
//
// Re-emitting an unchanged/partly-changed session is harmless: the ingester's
// idempotent upserts + the post-SOW-0004 idempotent catalog absorb it
// (adapter-opencode.md §"Read Strategy" → "Full-session-tree load + map").
func processChanges(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, sourceID string, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) (Cursor, bool, error) {
	bp := &batchProcessor{
		db:         db,
		schema:     schema,
		sourceID:   sourceID,
		out:        out,
		logger:     orDefaultLogger(logger),
		onError:    onError,
		committed:  cur,                 // last cursor whose sessions are fully emitted
		msgSession: map[string]string{}, // accumulates across batches (part→session fallback)
	}
	if err := bp.run(ctx); err != nil {
		// On error/cancel the committed cursor is the last batch whose content was
		// fully emitted — never the in-progress batch's scanned watermark.
		return bp.committed, bp.advanced, err
	}
	return bp.committed, bp.advanced, nil
}

// deltaRowHandler returns the per-row scan callback for one table: it scans the
// row into its typed struct, records the owning session id into the affected
// set, and (for message rows) populates the message→session map the part
// fallback consults. The returned closure matches scanTableDelta's onRow type
// and reports the row's watermark key. onError surfaces non-fatal per-row
// anomalies (a session_message with an unknown type — adapter-opencode.md
// §"Edge Cases" #1/§"session_message") without aborting the cycle.
func deltaRowHandler(ctx context.Context, db *sql.DB, table string, s tableSchema, affected *affectedSet, msgSession map[string]string, onError func(error)) func(*sql.Rows) (rowKey, error) {
	idx := newColumnIndex(s)
	n := len(s.Present)
	switch table {
	case "session":
		scan, row := scanSessionRow(idx, n)
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			affected.add(row.ID)
			return k, nil
		}
	case "message":
		scan, row := scanMessageRow(idx, n)
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			affected.add(row.SessionID)
			if row.ID != "" {
				msgSession[row.ID] = row.SessionID
			}
			return k, nil
		}
	case "part":
		scan, row := scanPartRow(idx, n)
		hasSessionID := s.has("session_id")
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			sid, rerr := resolvePartSession(ctx, db, hasSessionID, *row, msgSession)
			if rerr != nil {
				return k, rerr
			}
			affected.add(sid)
			return k, nil
		}
	default: // session_message
		scan, row := scanSessionMessageRow(idx, n)
		hasType := s.has("type")
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			affected.add(row.SessionID)
			// Spec Edge #1 (§"session_message"): warn on an unrecognized
			// session_message.type so a new opencode event variant is visible
			// rather than silently absorbed. Introspection-aware: if the schema
			// lacks the type column, skip silently (row.Type is "").
			if hasType {
				warnUnknownSessionMessageType(row.ID, row.Type, onError)
			}
			return k, nil
		}
	}
}

// knownSessionMessageTypes is the set of session_message.type discriminators the
// adapter recognizes today (adapter-opencode.md §"session_message": only
// agent-switched / model-switched ship currently; the upstream union is wider but
// unpopulated). Any other value is forward-compatibility data surfaced via a WARN.
var knownSessionMessageTypes = map[string]struct{}{
	"agent-switched": {},
	"model-switched": {},
}

// warnUnknownSessionMessageType emits one structured WARN via onError for a
// session_message row whose type is not recognized (spec Edge #1). An empty type
// (older schema with the column NULL, or absent) is not flagged — only a present
// but unrecognized value. The row's session_id still drives the affected set
// (the caller already added it), so the tree is reloaded regardless.
func warnUnknownSessionMessageType(id, typ string, onError func(error)) {
	if typ == "" {
		return
	}
	if _, ok := knownSessionMessageTypes[typ]; ok {
		return
	}
	onError(fmt.Errorf("opencode: unknown session_message type %q (table=session_message id=%s); skipping unrecognized event variant", typ, id))
}

// reloadAndEmit loads each affected session's full tree, maps it via the pure
// mapper, and emits the events. A session whose row cannot be loaded (deleted
// between the delta and the load, or an orphaned part/message) is skipped with
// one structured error and the cycle continues with the rest
// (adapter-opencode.md §"Read Strategy"). ctx cancellation stops promptly.
func reloadAndEmit(ctx context.Context, db *sql.DB, schema schemaSet, sourceID string, affected []string, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) error {
	for _, sid := range affected {
		if err := ctx.Err(); err != nil {
			return err
		}
		evs, skipped, err := loadAndMapSession(ctx, db, schema, sourceID, sid, logger, onError)
		if err != nil {
			if isContextErr(err) {
				return err
			}
			onError(err)
			continue
		}
		if skipped {
			// Session paused mid-compaction (time_compacting non-NULL). It is NOT
			// emitted this cycle and re-surfaces in a later delta when the column
			// clears (its time_updated bumps) — adapter-opencode.md §"Edge Cases" #8.
			continue
		}
		if evs == nil {
			// Session row gone — skipped with one structured error.
			onError(fmt.Errorf("opencode: affected session %s: %w", sid, errSessionGone))
			continue
		}
		if eerr := emitEvents(ctx, evs, out); eerr != nil {
			return eerr
		}
	}
	return nil
}

// loadAndMapSession loads one session's row + full ordered tree and maps it,
// returning (events, skipped, error). skipped=true means the session is paused
// mid-compaction (time_compacting non-NULL) and must NOT be emitted this cycle
// (SOW-0005 round-2 P2-E / adapter-opencode.md §"Edge Cases" #8); the caller
// continues without emitting and the session re-surfaces when the column clears.
// A nil event slice with skipped=false (and nil error) means the session row was
// not found (the caller surfaces errSessionGone). The mapper uses the
// deterministic default PayloadRef URI builder (the production tailer path injects
// no builder; defaultPayloadURI is the single source of truth). It also resolves
// the session's TRUE tree root by walking the parent_id chain (resolveRootID,
// SOW-0005 P2.4) and injects it so a nested sub-agent's RootNativeID is the whole
// tree's root rather than its direct parent.
func loadAndMapSession(ctx context.Context, db *sql.DB, schema schemaSet, sourceID, sessionID string, logger *slog.Logger, onError func(error)) (evs []canonical.Event, skipped bool, err error) {
	s, ok, err := loadSession(ctx, db, schema, sessionID, onError)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if s.TimeCompactingMs > 0 {
		// Pause: compaction is reshaping this session's message/part rows, so reading
		// now would emit partial/stale content. Skip emitting this cycle; the session
		// re-appears in a later delta when time_compacting clears (P2-E).
		orDefaultLogger(logger).Info("opencode: session compaction in progress; skipping tree emit this cycle (re-emits when time_compacting clears)",
			"session_id", sessionID)
		return nil, true, nil
	}
	tree, err := loadSessionTree(ctx, db, schema, sessionID, onError)
	if err != nil {
		return nil, false, err
	}
	root := resolveRootID(ctx, db, s.ID, s.ParentID, onError)
	evs, err = mapSession(sourceID, s, tree, WithRootNativeID(root), WithOnWarn(onError))
	return evs, false, err
}

// watermarkAdvanced reports whether b is strictly after a on the composite
// (time_updated, id) key — the same order the delta query and cursor.After use.
// Guards against re-recording an unchanged watermark (which would set advanced
// when nothing moved).
func watermarkAdvanced(a, b TableWatermark) bool {
	return cmpWatermark(b, a) > 0
}

// isContextErr reports whether err is a context cancellation/deadline (so the
// reload loop returns it rather than swallowing it via onError).
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// --- poll-loop cadence state machine ------------------------------------------

// pollState threads the realtime poll loop's cadence inputs: whether the last
// cycle was active (produced a change), the last WAL fsnotify event time, and
// the last MAX(time_updated) probe time. It is consulted by the gating predicate
// and the next-interval computation. Not safe for concurrent use; the tail loop
// owns one and mutates it single-threaded.
type pollState struct {
	active       bool
	lastWALEvent time.Time
	lastProbe    time.Time
	walFloorTill time.Time
}

// newPollState returns the initial state: idle, no WAL event yet, and a zero
// lastProbe so the FIRST poll's gate is open (the safety net is immediately due,
// guaranteeing the initial cycle reconciles in-place mutations that arrived
// before tailing started).
func newPollState() pollState {
	return pollState{}
}

// markWALEvent records a WAL fsnotify event at t, opening the 250 ms floor window
// for walFloorWindow and the probe gate (lastWALEvent advances past lastProbe).
func (s *pollState) markWALEvent(t time.Time) {
	s.lastWALEvent = t
	s.walFloorTill = t.Add(walFloorWindow)
}

// markProbe records that a MAX(time_updated) probe ran at t.
func (s *pollState) markProbe(t time.Time) { s.lastProbe = t }

// markCycle records the outcome of a poll cycle at t: active when it produced a
// change (switches to the 500 ms cadence), idle otherwise (2 s).
func (s *pollState) markCycle(advanced bool, _ time.Time) { s.active = advanced }

// nextInterval returns the wait before the next poll: the active/idle base
// interval, floored to walFloorInterval while the WAL-event window is open.
func (s *pollState) nextInterval(now time.Time) time.Duration {
	base := idlePollInterval
	if s.active {
		base = activePollInterval
	}
	if now.Before(s.walFloorTill) && base > walFloorInterval {
		return walFloorInterval
	}
	return base
}

// --- cursor shaping -----------------------------------------------------------

// coerceScanCursor normalises a cursor for use: a nil/zero Tables map is
// initialised and the version is set. The watermarks are NOT reset (column drift
// is handled per-column; only a depended-on column vanishing forces a re-ingest).
// The schema hash is recorded SEPARATELY by the poll loops via withSchemaHash
// after reading __drizzle_migrations (recordSchemaHash) — it is the REAL
// migration-name digest (schemaHash in migrations.go), replacing chunk C's
// interim present-column-shape fingerprint. Keeping the hash out of this function
// keeps it a pure cursor-shaping helper. Returns a ready-to-page cursor.
func coerceScanCursor(c Cursor) Cursor {
	if c.Tables == nil {
		c = newCursor()
	}
	if c.Version == 0 {
		c.Version = cursorVersion
	}
	return c
}

// recordSchemaHash reads __drizzle_migrations once and stamps the REAL
// migration-name schema hash (schemaHash) onto the cursor (adapter-opencode.md
// §"Cursor"). Called by scanLoop and tailLoop right after introspectAll, while
// the read-only DB is open.
//
// Mismatch behaviour (spec adapter-opencode.md §"Cursor"): when the incoming
// cursor already carries a different hash (opencode applied a migration between
// runs), the change is logged as a structured WARN via onError, the hash is
// re-read, and the loop CONTINUES without resetting watermarks — column drift is
// handled per-column by the dynamic SELECT, so a benign migration (a new column
// the adapter does not read, a data migration) never forces a re-ingest. A
// genuine read error (corrupt journal) is non-fatal here: the prior hash is kept
// and onError is notified, so the backfill/poll still proceeds.
func recordSchemaHash(ctx context.Context, db *sql.DB, c Cursor, onError func(error)) Cursor {
	hash, err := readSchemaHash(ctx, db)
	if err != nil {
		onError(fmt.Errorf("opencode: read schema hash (keeping prior, continuing): %w", err))
		return c
	}
	if hash == "" {
		// No __drizzle_migrations (foreign/old DB): leave any prior hash as-is;
		// there is nothing authoritative to record.
		return c
	}
	if c.SchemaHash != "" && c.SchemaHash != hash {
		onError(fmt.Errorf("opencode: schema hash changed (migration applied); re-reading, watermarks preserved: %.12s… → %.12s…", c.SchemaHash, hash))
	}
	return c.withSchemaHash(hash)
}
