package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// This file holds the realtime poll-loop CADENCE STATE MACHINE (pollState) and the
// cursor-shaping helpers (coerceScanCursor / recordSchemaHash). Split out of
// tailer_changes.go to keep each file ≤400 lines (SOW-0005 round-5: the P2-1
// post-tx warning flush grew loadAndMapSession). These are pure cadence/cursor
// concerns, distinct from the delta→emit→checkpoint pipeline in tailer_changes.go.

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
	// priorProbe records that at least one MAX(time_updated) probe has already
	// completed in this tailer's life. It gates the safety-net boundary re-scan
	// (SOW-0005 round-4 P1): the very FIRST probe of a cold Tail is a HEAD-snapshot
	// reconciliation, and replaying its boundary bucket there would re-emit the
	// snapshot's boundary session for no reason; once a real cycle has probed, a
	// subsequent SAFETY-NET probe (no WAL hint) is allowed to run the boundary
	// re-scan so a same-ms in-place update that arrived with a DROPPED/absent WAL
	// event is still eventually surfaced. Set by markProbe.
	priorProbe bool
}

// newPollState returns the initial state: idle, no WAL event yet, a zero
// lastProbe so the FIRST poll's gate is open (the safety net is immediately due,
// guaranteeing the initial cycle reconciles in-place mutations that arrived
// before tailing started), and priorProbe=false so that first probe does NOT run
// the safety-net boundary re-scan (cold-Tail replay guard, round-4 P1).
func newPollState() pollState {
	return pollState{}
}

// markWALEvent records a WAL fsnotify event at t, opening the 250 ms floor window
// for walFloorWindow and the probe gate (lastWALEvent advances past lastProbe).
func (s *pollState) markWALEvent(t time.Time) {
	s.lastWALEvent = t
	s.walFloorTill = t.Add(walFloorWindow)
}

// markProbe records that a MAX(time_updated) probe ran at t and marks that a
// probe has now completed (priorProbe) so a later safety-net probe may run the
// boundary re-scan (round-4 P1; the very first probe still does not, by the
// captured-before-markProbe read in pollOnce).
func (s *pollState) markProbe(t time.Time) {
	s.lastProbe = t
	s.priorProbe = true
}

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
