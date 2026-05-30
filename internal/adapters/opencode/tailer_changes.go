package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the SHARED change-processing helper both poll loops use
// (processChanges) and the poll-loop cadence STATE MACHINE (pollState). It is
// split out of tailer.go to keep each file ≤400 lines. processChanges is the one
// "delta → affected sessions → reload → map → emit → advance cursor" pipeline:
// scanLoop runs it once over the whole backfill; tailLoop runs it per change
// cycle. The page-level SourceProgress checkpoints inside the delta phase let a
// restart resume mid-backfill (adapter-opencode.md §"Performance").

// processChanges pages every tracked table forward from `cur`, derives the
// affected session ids, reloads each affected session's full tree, maps it via
// the pure mapper, and emits the events — returning the advanced cursor and
// whether anything advanced. It is used by BOTH scanLoop (whole backfill) and
// tailLoop (one cycle). SourceProgress is checkpointed during the delta phase
// every ~progressEveryRows rows so a restart resumes mid-scan; the final
// checkpoint is the caller's job (scanLoop emits one at the end; tailLoop's
// pollOnce emits one after a productive cycle).
//
// Re-emitting an unchanged/partly-changed session is harmless: the ingester's
// idempotent upserts + the post-SOW-0004 idempotent catalog absorb it
// (adapter-opencode.md §"Read Strategy" → "Full-session-tree load + map").
func processChanges(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, sourceID string, out chan<- canonical.Event, onError func(error)) (Cursor, bool, error) {
	next, affected, advanced, err := collectDeltas(ctx, db, schema, cur, sourceID, out)
	if err != nil {
		return next, advanced, err
	}
	if len(affected) == 0 {
		return next, advanced, nil
	}
	if rerr := reloadAndEmit(ctx, db, schema, sourceID, affected, out, onError); rerr != nil {
		return next, advanced, rerr
	}
	return next, advanced, nil
}

// collectDeltas pages all four tracked tables forward from the cursor, advancing
// each table's watermark, accumulating the affected session ids, and emitting a
// SourceProgress checkpoint every ~progressEveryRows rows so a backfill resumes
// mid-scan. It returns the advanced cursor, the affected-session ids (first-seen
// order), and whether any watermark advanced. The message→session map built from
// the message delta lets a part with no denormalized session_id (old schema)
// resolve its owner without a query.
func collectDeltas(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, sourceID string, out chan<- canonical.Event) (Cursor, []string, bool, error) {
	affected := newAffectedSet()
	msgSession := map[string]string{}
	advanced := false
	rowsSinceCheckpoint := 0

	// Order matters for the part→session fallback: message before part, so the
	// message delta has populated msgSession when a part needs it. session and
	// session_message contribute their own session_id directly.
	for _, table := range trackedTables {
		s := schema[table]
		from := cur.Tables[table]
		onRow := deltaRowHandler(ctx, db, table, s, affected, msgSession)

		delta, err := scanTableDelta(ctx, db, s, from, onRow)
		if err != nil {
			return cur, affected.ids(), advanced, err
		}
		if delta.rowCount > 0 && watermarkAdvanced(from, delta.watermark) {
			cur = cur.withTable(table, delta.watermark)
			advanced = true
		}
		rowsSinceCheckpoint += delta.rowCount
		if rowsSinceCheckpoint >= progressEveryRows {
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return cur, affected.ids(), advanced, perr
			}
			rowsSinceCheckpoint = 0
		}
	}
	return cur, affected.ids(), advanced, nil
}

// deltaRowHandler returns the per-row scan callback for one table: it scans the
// row into its typed struct, records the owning session id into the affected
// set, and (for message rows) populates the message→session map the part
// fallback consults. The returned closure matches scanTableDelta's onRow type
// and reports the row's watermark key.
func deltaRowHandler(ctx context.Context, db *sql.DB, table string, s tableSchema, affected *affectedSet, msgSession map[string]string) func(*sql.Rows) (rowKey, error) {
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
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			affected.add(row.SessionID)
			return k, nil
		}
	}
}

// reloadAndEmit loads each affected session's full tree, maps it via the pure
// mapper, and emits the events. A session whose row cannot be loaded (deleted
// between the delta and the load, or an orphaned part/message) is skipped with
// one structured error and the cycle continues with the rest
// (adapter-opencode.md §"Read Strategy"). ctx cancellation stops promptly.
func reloadAndEmit(ctx context.Context, db *sql.DB, schema schemaSet, sourceID string, affected []string, out chan<- canonical.Event, onError func(error)) error {
	for _, sid := range affected {
		if err := ctx.Err(); err != nil {
			return err
		}
		evs, err := loadAndMapSession(ctx, db, schema, sourceID, sid)
		if err != nil {
			if isContextErr(err) {
				return err
			}
			onError(err)
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
// returning the mapped events. A nil event slice (with nil error) means the
// session row was not found (the caller surfaces errSessionGone). The mapper is
// invoked WITHOUT a production URI builder here (chunk D injects it at the
// adapter boundary); the deterministic default keeps op→payload linkage intact.
func loadAndMapSession(ctx context.Context, db *sql.DB, schema schemaSet, sourceID, sessionID string) ([]canonical.Event, error) {
	s, ok, err := loadSession(ctx, db, schema, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	tree, err := loadSessionTree(ctx, db, schema, sessionID)
	if err != nil {
		return nil, err
	}
	return mapSession(sourceID, s, tree)
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
