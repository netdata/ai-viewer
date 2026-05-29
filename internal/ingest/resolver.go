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

// linkOrphans links every orphan child whose parent (and/or root) has since
// landed, and — crucially — emits the notify rows that tell an already-open
// UI to refetch. The linkage mutates parent_session_id / root_session_id
// OUTSIDE the batch-writer path, so the notify rows the writer would normally
// emit are absent; without them the SSE contract (sse-protocol.md
// §session_changed) is silently broken for child-first ingestions.
//
// Everything runs in ONE transaction so the two UPDATEs and the notify rows
// commit atomically: the serve poller can never observe a linkage without its
// notification, nor a notification before the linked rows are visible. Each
// UPDATE uses RETURNING to capture exactly the rows it changed (modernc.org/
// sqlite supports UPDATE … RETURNING); the affected set is the union of every
// changed child, its newly-linked parent, and its root. When nothing links,
// the transaction makes no notify rows — an open poller is not spammed for a
// no-op pass.
func (r *resolver) linkOrphans(ctx context.Context) error {
	if r.db == nil {
		return errors.New("resolver: nil db")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("resolver: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// affected accumulates the distinct session ids whose row is now part of
	// a newly-resolved parent/child/root relationship and therefore needs a
	// session_changed notification.
	affected := map[string]struct{}{}
	if err := r.linkParents(ctx, tx, affected); err != nil {
		return err
	}
	if err := r.linkRoots(ctx, tx, affected); err != nil {
		return err
	}
	if err := r.emitResolverNotify(ctx, tx, affected); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("resolver: commit: %w", err)
	}
	committed = true
	return nil
}

// linkParents runs the parent-link UPDATE and records, for every child it
// links, both the child id and its newly-set parent id into affected. A
// changed child means its parent now has a visible new child, so an open
// parent-detail view must refetch too (sse-protocol.md §session_changed).
func (r *resolver) linkParents(ctx context.Context, tx *sql.Tx, affected map[string]struct{}) error {
	before := len(affected)
	rows, err := tx.QueryContext(ctx, `
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
RETURNING id, parent_session_id
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE parent: %w", err)
	}
	if err := scanLinkedPairs(rows, affected); err != nil {
		return fmt.Errorf("resolver UPDATE parent: %w", err)
	}
	if r.logger != nil && len(affected) > before {
		r.logger.Debug("resolver: linked orphan parents", "affected", len(affected)-before)
	}
	return nil
}

// linkRoots runs the root-re-resolution UPDATE and records, for every child
// it re-roots, both the child id and its newly-set root id into affected.
func (r *resolver) linkRoots(ctx context.Context, tx *sql.Tx, affected map[string]struct{}) error {
	before := len(affected)
	rows, err := tx.QueryContext(ctx, `
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
RETURNING id, root_session_id
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE root: %w", err)
	}
	if err := scanLinkedPairs(rows, affected); err != nil {
		return fmt.Errorf("resolver UPDATE root: %w", err)
	}
	if r.logger != nil && len(affected) > before {
		r.logger.Debug("resolver: linked orphan roots", "affected", len(affected)-before)
	}
	return nil
}

// scanLinkedPairs reads (changedID, linkedID) pairs from an UPDATE …
// RETURNING result and adds BOTH ids of every pair to affected. The linked id
// (parent or root) is non-NULL on every returned row because the WHERE clause
// only matches rows whose correlated subquery has a hit, but it is scanned as
// nullable defensively. Closes rows before returning.
func scanLinkedPairs(rows *sql.Rows, affected map[string]struct{}) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var changed string
		var linked sql.NullString
		if err := rows.Scan(&changed, &linked); err != nil {
			return err
		}
		affected[changed] = struct{}{}
		if linked.Valid && linked.String != "" {
			affected[linked.String] = struct{}{}
		}
	}
	return rows.Err()
}

// emitResolverNotify writes the change-log rows for a resolver pass INSIDE the
// pass's transaction: one session_changed per affected session (carrying that
// session's current root_session_id, read back from the row just updated in
// this tx), plus exactly one stats_invalidated when any session was affected
// (child-count / topology aggregates changed). It mirrors the producer rules
// and INSERT shape of the batch writer's emitNotify (notify_producer.go).
// Emits nothing when affected is empty so a no-op pass is silent.
func (r *resolver) emitResolverNotify(ctx context.Context, tx *sql.Tx, affected map[string]struct{}) error {
	if len(affected) == 0 {
		return nil
	}
	tsUS := time.Now().UTC().UnixMicro()
	for id := range affected {
		rootID, err := resolverLookupRoot(ctx, tx, id)
		if err != nil {
			// The row is guaranteed present (it is either the child the
			// resolver just updated or its now-landed parent/root). A miss
			// means the database is unhealthy; surfacing the error rolls back
			// the whole pass rather than dropping a notification (no silent
			// failures).
			return fmt.Errorf("resolver notify: lookup root for session %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO notify (ts_us, kind, session_id, root_session_id)
VALUES (?, 'session_changed', ?, ?)
`, tsUS, id, rootID); err != nil {
			return fmt.Errorf("resolver notify: insert session_changed: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO notify (ts_us, kind) VALUES (?, 'stats_invalidated')
`, tsUS); err != nil {
		return fmt.Errorf("resolver notify: insert stats_invalidated: %w", err)
	}
	return nil
}

// resolverLookupRoot returns the current root_session_id of the sessions row
// identified by canonical id, read inside the caller's tx so it reflects the
// linkage just applied. sql.ErrNoRows is an integrity failure (the row is
// guaranteed present) and is returned to the caller.
func resolverLookupRoot(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var root string
	if err := tx.QueryRowContext(ctx,
		`SELECT root_session_id FROM sessions WHERE id = ?`, id).Scan(&root); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("sessions row %s absent in resolver tx: %w", id, err)
		}
		return "", err
	}
	return root, nil
}
