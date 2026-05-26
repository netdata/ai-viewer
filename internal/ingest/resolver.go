package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// resolver runs a background loop that retries parent linkage for
// orphan child sessions. A child arrives as a sub-agent whose parent
// session has not yet been ingested → the SessionStartedEvent writer
// inserts the row with parent_session_id = NULL and stashes
// parentNativeId in extras_json. The resolver loop reads that JSON
// pointer on every tick and links any orphan whose parent has since
// landed.
//
// The resolver is owned by the Ingester; tests can construct one
// directly via newResolver and drive it with a manual ticker via the
// linkOrphans method.
type resolver struct {
	db       *sql.DB
	logger   *slog.Logger
	interval time.Duration
	// stop signals loop to exit on Stop(); buffered so a non-blocking
	// send always succeeds even if loop isn't yet started.
	stop chan struct{}
}

func newResolver(db *sql.DB, logger *slog.Logger, interval time.Duration) *resolver {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &resolver{
		db:       db,
		logger:   logger,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// loop blocks until ctx is cancelled or Stop is called, running
// linkOrphans every interval. Errors are logged but never returned —
// the loop continues running on transient SQL failures because the
// alternative (the loop dying silently) is worse.
func (r *resolver) loop(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
			if err := r.linkOrphans(ctx); err != nil && r.logger != nil {
				r.logger.Warn("resolver: link orphans failed", "err", err)
			}
		}
	}
}

// Stop signals the loop to exit at the next tick. Idempotent.
func (r *resolver) Stop() {
	select {
	case <-r.stop:
		// already closed
	default:
		close(r.stop)
	}
}

// linkOrphans walks every session with parent_session_id IS NULL and a
// non-empty extras_json.aiViewer.parentNativeId, and links it to the
// parent if one with the matching (source_id, native_id) now exists.
// It also re-resolves root_session_id when the orphan recorded a
// rootNativeId in extras_json that initially fell back to self (because
// the root row was not yet in the store).
//
// Two statements run per pass: one for parent_session_id, one for
// root_session_id. Each is a single UPDATE with a correlated subquery;
// the bounded WHERE clauses keep the work proportional to the orphan
// count rather than the full session table.
func (r *resolver) linkOrphans(ctx context.Context) error {
	if r.db == nil {
		return errors.New("resolver: nil db")
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE sessions SET parent_session_id = (
    SELECT p.id FROM sessions p
     WHERE p.source_id = sessions.source_id
       AND p.native_id = json_extract(sessions.extras_json, '$.aiViewer.parentNativeId')
)
WHERE sessions.parent_session_id IS NULL
  AND json_extract(sessions.extras_json, '$.aiViewer.parentNativeId') IS NOT NULL
  AND json_extract(sessions.extras_json, '$.aiViewer.parentNativeId') <> ''
  AND EXISTS (
      SELECT 1 FROM sessions p
       WHERE p.source_id = sessions.source_id
         AND p.native_id = json_extract(sessions.extras_json, '$.aiViewer.parentNativeId')
  )
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE parent: %w", err)
	}
	if r.logger != nil {
		if n, errAff := res.RowsAffected(); errAff == nil && n > 0 {
			r.logger.Debug("resolver: linked orphan parents", "count", n)
		}
	}
	res, err = r.db.ExecContext(ctx, `
UPDATE sessions SET root_session_id = (
    SELECT r.id FROM sessions r
     WHERE r.source_id = sessions.source_id
       AND r.native_id = json_extract(sessions.extras_json, '$.aiViewer.rootNativeId')
)
WHERE sessions.root_session_id = sessions.id
  AND json_extract(sessions.extras_json, '$.aiViewer.rootNativeId') IS NOT NULL
  AND json_extract(sessions.extras_json, '$.aiViewer.rootNativeId') <> ''
  AND json_extract(sessions.extras_json, '$.aiViewer.rootNativeId') <> sessions.native_id
  AND EXISTS (
      SELECT 1 FROM sessions r
       WHERE r.source_id = sessions.source_id
         AND r.native_id = json_extract(sessions.extras_json, '$.aiViewer.rootNativeId')
  )
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE root: %w", err)
	}
	if r.logger != nil {
		if n, errAff := res.RowsAffected(); errAff == nil && n > 0 {
			r.logger.Debug("resolver: linked orphan roots", "count", n)
		}
	}
	return nil
}
