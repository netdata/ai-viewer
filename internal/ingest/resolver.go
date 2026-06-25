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
// Everything runs in ONE transaction so the link UPDATEs and the notify rows
// commit atomically: the serve poller can never observe a linkage without its
// notification, nor a notification before the linked rows are visible. Each
// UPDATE uses RETURNING to capture exactly the rows it changed (modernc.org/
// sqlite supports UPDATE … RETURNING); the affected set is the union of every
// changed child, its newly-linked parent, and its root. When nothing links,
// the transaction makes no notify rows — an open poller is not spammed for a
// no-op pass.
//
// The link passes are organized as a slice of named steps so the iteration
// stays trivial and the per-step rollback contract (any error rolls the WHOLE
// tx back) lives in one place; the resolverStep type captures both the SQL
// pass and its emit hook through the same signature.
func (r *resolver) linkOrphans(ctx context.Context) error {
	if r.db == nil {
		return errors.New("resolver: nil db")
	}
	return r.runResolverTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		affected := map[string]struct{}{}
		for _, step := range r.resolverSteps() {
			if err := step(ctx, tx, affected); err != nil {
				return err
			}
		}
		return r.emitResolverNotify(ctx, tx, affected)
	})
}

// resolverStep is one phase of a linkOrphans pass: it accumulates the session
// ids it changes into the shared affected set. All four link passes share this
// signature so the resolver loop is a uniform for-range over a step slice.
type resolverStep func(ctx context.Context, tx *sql.Tx, affected map[string]struct{}) error

// resolverSteps returns the ordered list of link passes a linkOrphans run
// must execute. The order is significant: parent-link must run before root
// re-resolution so root_session_id can re-point at the just-linked parent in
// the same pass (see linkRoots's WHERE clause). Op-child passes follow the
// session passes because they read the now-resolved session tree.
func (r *resolver) resolverSteps() []resolverStep {
	return []resolverStep{
		r.linkParents,
		r.linkRoots,
		r.linkOpChildren,
		r.linkOpChildrenByToolUse,
	}
}

// runResolverTx wraps body in the resolver's single-tx contract: BeginTx,
// run body (which executes every link pass + the notify emit), Commit on
// success, Rollback on any error (including a successful body whose Commit
// fails). The defer/committed pattern is the same one the worker uses; it is
// extracted here so linkOrphans stays orchestration-only.
func (r *resolver) runResolverTx(ctx context.Context, body func(context.Context, *sql.Tx) error) error {
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
	if err := body(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("resolver: commit: %w", err)
	}
	committed = true
	return nil
}

// linkParents runs the parent-link UPDATE and records, for every child it
// links, the child id, its newly-set parent id, AND its current root id into
// affected. A changed child means its parent now has a visible new child, so an
// open parent-detail view must refetch; and because a detail-page subscriber
// filters by EXACT session_id, the root must be signalled too even when the
// child was already correctly rooted (root present, parent absent — the root
// re-resolution UPDATE never fires for that child). Returning the root column
// here is what guarantees the root gets a session_changed on a parent-only
// link (sse-protocol.md §session_changed; ingester.md §resolver pass).
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
RETURNING id, parent_session_id, root_session_id
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE parent: %w", err)
	}
	if err := scanLinkedRows(rows, affected); err != nil {
		return fmt.Errorf("resolver UPDATE parent: %w", err)
	}
	if r.logger != nil && len(affected) > before {
		r.logger.Debug("resolver: linked orphan parents", "affected", len(affected)-before)
	}
	return nil
}

// linkRoots runs the root-re-resolution UPDATEs and records, for every child
// it re-roots, the changed child and the related parent/root ids into affected.
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
		return fmt.Errorf("resolver UPDATE explicit root: %w", err)
	}
	if err := scanLinkedRows(rows, affected); err != nil {
		return fmt.Errorf("resolver UPDATE explicit root: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `
WITH RECURSIVE session_roots(id, root_id) AS (
    SELECT id, id
      FROM sessions
     WHERE parent_session_id IS NULL
    UNION ALL
    SELECT child.id, session_roots.root_id
      FROM sessions child
      JOIN session_roots ON child.parent_session_id = session_roots.id
)
UPDATE sessions SET root_session_id = (
    SELECT root_id FROM session_roots
     WHERE session_roots.id = sessions.id
)
WHERE EXISTS (
    SELECT 1 FROM session_roots
     WHERE session_roots.id = sessions.id
       AND session_roots.root_id <> sessions.root_session_id
)
RETURNING id, parent_session_id, root_session_id
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE transitive root: %w", err)
	}
	if err := scanLinkedRows(rows, affected); err != nil {
		return fmt.Errorf("resolver UPDATE transitive root: %w", err)
	}
	if r.logger != nil && len(affected) > before {
		r.logger.Debug("resolver: linked orphan roots", "affected", len(affected)-before)
	}
	return nil
}

// linkOpChildren re-links ops.child_session_id from the child native id stashed
// in ops.extras_json.aiViewer.childNativeId once the referenced child session
// lands (P1a). The parent Agent op is written before its child sidechain
// session exists, so child_session_id starts NULL and the native id is stashed;
// without this pass the parent op would permanently show no child (the
// session-only linkParents/linkRoots passes never touch ops). It records the
// op's OWNING session id — the PARENT session — into affected so an open
// parent-detail view refetches and renders the now-linked child op
// (sse-protocol.md §session_changed). The child session is matched on
// (source_id, native_id); source_id is resolved by joining through the op's
// session row since ops carry no source_id column. modernc.org/sqlite supports
// UPDATE … FROM and UPDATE … RETURNING.
func (r *resolver) linkOpChildren(ctx context.Context, tx *sql.Tx, affected map[string]struct{}) error {
	before := len(affected)
	rows, err := tx.QueryContext(ctx, `
UPDATE ops SET child_session_id = (
    SELECT c.id FROM sessions c
     JOIN sessions parent ON parent.id = ops.session_id
     WHERE c.source_id = parent.source_id
       AND c.native_id = json_extract(ops.extras_json, '$.aiViewer.childNativeId')
)
WHERE ops.child_session_id IS NULL
  AND json_extract(ops.extras_json, '$.aiViewer.childNativeId') IS NOT NULL
  AND json_extract(ops.extras_json, '$.aiViewer.childNativeId') <> ''
  AND EXISTS (
      SELECT 1 FROM sessions c
       JOIN sessions parent ON parent.id = ops.session_id
       WHERE c.source_id = parent.source_id
         AND c.native_id = json_extract(ops.extras_json, '$.aiViewer.childNativeId')
  )
RETURNING session_id
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE op child: %w", err)
	}
	if err := scanLinkedRows(rows, affected); err != nil {
		return fmt.Errorf("resolver UPDATE op child: %w", err)
	}
	if r.logger != nil && len(affected) > before {
		r.logger.Debug("resolver: linked orphan op children", "affected", len(affected)-before)
	}
	return nil
}

// linkOpChildrenByToolUse re-links ops.child_session_id by matching the parent op's
// stashed toolUseId (ops.extras_json.aiViewer.toolUseId) to a child session's stashed
// toolUseId (sessions.extras_json.aiViewer.toolUseId) in the SAME source. This is the
// additive, meta-independent bridge the claude-code adapter relies on (SOW-0003 P1.6):
// a parent `Agent` op tailed BEFORE its sidecar `.meta.json` carries no
// ChildSessionNativeID (so the childNativeId pass above cannot link it), but it does
// carry the toolUseId from its own assistant.tool_use block, and the child session —
// once read with its sidecar — carries the same toolUseId. Linking them at the DB
// layer needs no transcript re-read (a re-read would double-count the accumulating
// catalog_* rollups). It records the op's OWNING (parent) session id into affected so
// an open parent-detail view refetches (sse-protocol.md §session_changed).
//
// The pass is purely additive: an op with no aiViewer.toolUseId stash matches nothing,
// so adapters that do not stash it (ai-agent v2/v3) are unaffected. source_id is
// resolved by joining through the op's session row (ops carry no source_id). The
// child is matched on (same source, matching toolUseId) and must NOT be the op's own
// session (c.id <> ops.session_id) — defence against a degenerate self-match.
//
// The match is ADDITIONALLY constrained to the op's STRUCTURAL child (SOW-0003 P2.7e):
// the matched child must descend from the op's owning (parent) session — either its
// resolved foreign key already points at the parent (c.parent_session_id = parent.id)
// or its stashed parent native id names the parent (c.extras_json.$.aiViewer.parentNativeId
// = parent.native_id, the pre-resolution stash). Without this, two sessions in ONE
// source sharing (or forging) the same toolUseId let the scalar subquery pick an
// arbitrary same-source child; the structural constraint forces each parent op to link
// to ITS OWN child. The constraint stays additive: claude-code sub-agents always carry
// parentNativeId (and the parent FK resolves shortly after), so genuine links still
// match; an op without a toolUseId stash still matches zero rows (aiagent unaffected).
// modernc.org/sqlite supports UPDATE … FROM and UPDATE … RETURNING.
func (r *resolver) linkOpChildrenByToolUse(ctx context.Context, tx *sql.Tx, affected map[string]struct{}) error {
	before := len(affected)
	rows, err := tx.QueryContext(ctx, `
UPDATE ops SET child_session_id = (
    SELECT c.id FROM sessions c
     JOIN sessions parent ON parent.id = ops.session_id
     WHERE c.source_id = parent.source_id
       AND c.id <> ops.session_id
       AND json_extract(c.extras_json, '$.aiViewer.toolUseId') = json_extract(ops.extras_json, '$.aiViewer.toolUseId')
       AND (
            c.parent_session_id = parent.id
            OR json_extract(c.extras_json, '$.aiViewer.parentNativeId') = parent.native_id
       )
)
WHERE ops.child_session_id IS NULL
  AND json_extract(ops.extras_json, '$.aiViewer.toolUseId') IS NOT NULL
  AND json_extract(ops.extras_json, '$.aiViewer.toolUseId') <> ''
  AND EXISTS (
      SELECT 1 FROM sessions c
       JOIN sessions parent ON parent.id = ops.session_id
       WHERE c.source_id = parent.source_id
         AND c.id <> ops.session_id
         AND json_extract(c.extras_json, '$.aiViewer.toolUseId') = json_extract(ops.extras_json, '$.aiViewer.toolUseId')
         AND (
              c.parent_session_id = parent.id
              OR json_extract(c.extras_json, '$.aiViewer.parentNativeId') = parent.native_id
         )
  )
RETURNING session_id
`)
	if err != nil {
		return fmt.Errorf("resolver UPDATE op child by toolUseId: %w", err)
	}
	if err := scanLinkedRows(rows, affected); err != nil {
		return fmt.Errorf("resolver UPDATE op child by toolUseId: %w", err)
	}
	if r.logger != nil && len(affected) > before {
		r.logger.Debug("resolver: linked orphan op children by toolUseId", "affected", len(affected)-before)
	}
	return nil
}

// scanLinkedRows reads an UPDATE … RETURNING result and adds every id it
// carries to affected. The FIRST column is the changed child id (always added);
// every REMAINING column is a related id (newly-linked parent and/or current
// root) added when non-empty. It adapts to the column count so both shapes work
// unchanged: the parent-link UPDATE returns (id, parent_session_id,
// root_session_id) and the root-link UPDATE returns (id, root_session_id). The
// related ids are non-NULL on every matched row (the WHERE clause requires the
// correlated subquery to hit) but are scanned as nullable defensively. Closes
// rows before returning.
func scanLinkedRows(rows *sql.Rows, affected map[string]struct{}) error {
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		var changed string
		related := make([]sql.NullString, len(cols)-1)
		dest := make([]any, len(cols))
		dest[0] = &changed
		for i := range related {
			dest[i+1] = &related[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		affected[changed] = struct{}{}
		for _, rel := range related {
			if rel.Valid && rel.String != "" {
				affected[rel.String] = struct{}{}
			}
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
