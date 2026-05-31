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
	// boundaryReal reports whether the cursor's current boundary ms is a position
	// whose bucket was ALREADY emitted — so the round-7 P1-1 unified boundary re-scan
	// (which runs before the forward delta whenever changed==true on ANY path OR the
	// probe gate is open) may run without replaying a never-emitted cold-Tail HEAD
	// snapshot. It is the SINGLE cold-`Tail` guard (round-7 P2-1): it gates the WHOLE
	// trigger — both the changed path and the gate-open path — replacing the old,
	// partial `priorProbe` flag that guarded only the changed==false path (and could
	// be defeated by a cold Tail's first WAL-driven or safety-net probe). It starts
	// true for a WARM Tail (resumed from a Scan cursor: Scan already emitted the
	// boundary) and false for a COLD Tail (HEAD snapshot: follow-from-now, boundary
	// never emitted); pollOnce flips it true once the cursor first advances (the new
	// boundary is the just-emitted forward position, so re-scanning it is idempotent).
	boundaryReal bool
}

// newPollState returns the initial state: idle, no WAL event yet, and a zero
// lastProbe so the FIRST poll's gate is open (the safety net is immediately due,
// guaranteeing the initial cycle reconciles in-place mutations that arrived
// before tailing started).
//
// warmStart seeds boundaryReal (SOW-0005 round-6 P1; round-7 P2-1 makes it the
// single cold-Tail guard): a WARM Tail resumed from a Scan cursor inherits a
// boundary whose bucket Scan already emitted, so the boundary re-scan may run from
// the first poll; a COLD Tail (HEAD snapshot, follow-from-now) starts
// boundaryReal=false so it does NOT replay the never-emitted snapshot boundary on
// ANY path (the changed path, the gated-probe path, or the WAL-driven/safety-net
// gate-open path) until the cursor first advances — pollOnce flips boundaryReal
// true at that point.
func newPollState(warmStart bool) pollState {
	return pollState{boundaryReal: warmStart}
}

// markWALEvent records a WAL fsnotify event at t, opening the 250 ms floor window
// for walFloorWindow and the probe gate (lastWALEvent advances past lastProbe).
func (s *pollState) markWALEvent(t time.Time) {
	s.lastWALEvent = t
	s.walFloorTill = t.Add(walFloorWindow)
}

// markProbe records that a MAX(time_updated) probe ran at t (advancing lastProbe
// re-closes the gate until the next WAL event or the next 60 s net tick).
func (s *pollState) markProbe(t time.Time) {
	s.lastProbe = t
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
