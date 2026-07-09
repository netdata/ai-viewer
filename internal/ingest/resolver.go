package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
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
	// waitBeforeDBWork lets tail lifecycle writes take priority before the
	// resolver opens its lower-priority maintenance transaction.
	waitBeforeDBWork func(context.Context) error
	// deferredNow, when it returns true, makes the loop skip linkOrphans
	// entirely (SOW-0118): during the startup scan + read-model rebuild the
	// resolver's slow link passes monopolize the single writer connection and
	// starve every worker flush. nil (tests) = never defer.
	deferredNow func() bool
	// hasNewIngestion reports whether any canonical rows were committed since
	// the resolver's last completed pass. The resolver's four link passes are
	// O(all-rows) scans; running them every tick when nothing changed is the
	// unconditional ~1-core idle burn (SOW-0117). The ingester bumps an atomic
	// generation counter on every committed batch; the resolver records the
	// counter after each pass and skips the whole pass when it is unchanged.
	// nil (tests) means "always run" so a bare newResolver keeps the old
	// behavior and linkOrphans stays directly drivable.
	ingestionGenNow func() int64
	// lastSeenGen is the generation counter value after the resolver's last
	// completed linkOrphans pass. The next tick skips when ingestionGenNow()
	// still equals it (nothing committed in between). Starts at -1 so the
	// first tick after Start always runs (links any orphans from the scan).
	// atomic so tests can observe it without racing the loop goroutine; in
	// production it is only touched from the single resolver loop goroutine.
	lastSeenGen atomic.Int64
	// sessionWatermarkNow returns MAX(sessions.last_activity_ts), a cheap
	// covering-index probe (idx_sessions_activity) used as a CONSERVATIVE signal
	// for whether the two SESSION link passes (linkParents, linkRoots — the
	// latter builds an O(sessions) recursive CTE) need to run. It advances when a
	// session is inserted/updated, and also when a session gains an op with a
	// newer end_ts (aggregate refresh), so it OVER-approximates "a session
	// changed": the session passes may run a little more than needed (harmless,
	// they find nothing) but never miss a real session change. Effective where it
	// matters — idle and resume scans of unchanged sessions. nil (tests) means
	// "sessions may have changed" so the session passes always run and
	// linkOrphans stays directly drivable.
	sessionWatermarkNow func(context.Context) (int64, error)
	// lastSeenSession is the session watermark after the last pass that ran the
	// session link passes. Starts at -1 so the first pass always runs them.
	lastSeenSession atomic.Int64
	// lastRootsGen is the generation counter value at the last linkRoots run.
	// linkRoots is skipped on subsequent ticks until the generation advances.
	// SOW-0118: at 552K sessions the recursive CTE takes ~30s (longer than the
	// 5s resolver interval), creating a busy-loop that dominated CPU. This flag
	// limits it to once per generation advance.
	lastRootsGen atomic.Int64
	// stop signals loop to exit on Stop(); buffered so a non-blocking
	// send always succeeds even if loop isn't yet started.
	stop chan struct{}
}

func newResolver(db *sql.DB, logger *slog.Logger, interval time.Duration) *resolver {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	r := &resolver{
		db:       db,
		logger:   logger,
		interval: interval,
		stop:     make(chan struct{}),
	}
	r.lastSeenGen.Store(-1)     // first tick always runs
	r.lastRootsGen.Store(-1)    // first pass always runs linkRoots
	r.lastSeenSession.Store(-1) // first pass always runs the session passes
	return r
}

// loop blocks until ctx is cancelled or Stop is called, running
// linkOrphans every interval. Errors are logged but never returned —
// the loop continues running on transient SQL failures because the
// alternative (the loop dying silently) is worse.
//
// When hasNewIngestion is wired (production), a tick with NO committed
// canonical rows since the last pass skips linkOrphans entirely: the four
// link passes are O(all-rows) scans, and re-running them when nothing could
// have changed is pure idle cost (SOW-0117). hasNewIngestion records the
// ingester's generation counter internally; it advances on every committed
// batch (sessions OR ops), so any pending link has a chance to run within one
// interval of its parent/root landing.
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
			// SOW-0118: during the startup scan (+ post-scan read-model rebuild),
			// the resolver's link passes do slow B-tree scans on the large DB and
			// MONOPOLIZE the single writer connection (SetMaxOpenConns(1)),
			// starving every worker flush (measured: begin-wait 68–93% of flush
			// time, resolver holding the connection in 10/10 samples). Linkage is
			// an eventually-consistent read model; defer it until the scan+rebuild
			// settle, then one pass links all scan-time orphans. No correctness
			// loss — the resolver is idempotent.
			if r.deferredNow != nil && r.deferredNow() {
				continue
			}
			// Capture the current signals BEFORE running. The watermarks advance only
			// on a SUCCESSFUL pass so a transient linkOrphans failure is retried on
			// the next tick (preserving the pre-SOW-0117 retry-every-5s contract)
			// rather than being silently skipped until more data arrives.
			var genNow int64
			if r.ingestionGenNow != nil {
				genNow = r.ingestionGenNow()
				if genNow == r.lastSeenGen.Load() {
					continue
				}
			}
			sessionsChanged := true
			wmAdvanced := false
			var wmNow int64
			if r.sessionWatermarkNow != nil {
				wm, err := r.sessionWatermarkNow(ctx)
				switch {
				case err != nil:
					if r.logger != nil {
						r.logger.Warn("resolver: session watermark probe failed; running all passes", "err", err)
					}
				case wm == r.lastSeenSession.Load():
					sessionsChanged = false
				default:
					wmNow = wm
					wmAdvanced = true
				}
			}
			if err := r.linkOrphansGated(ctx, sessionsChanged); err != nil {
				if r.logger != nil {
					r.logger.Warn("resolver: link orphans failed", "err", err)
				}
				// Do NOT advance the watermarks: retry on the next tick.
				continue
			}
			if r.ingestionGenNow != nil {
				r.lastSeenGen.Store(genNow)
			}
			if wmAdvanced {
				r.lastSeenSession.Store(wmNow)
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
// sessionsChanged gates the two SESSION passes (linkParents, linkRoots). They
// can only do useful work when a session row changed since the last pass (a
// new orphan, a newly-landed parent/root, or a provisional root to correct);
// ops never affect session parentage or roots, so during op-heavy ingestion
// (the common scan case) skipping them avoids the O(sessions) linkRoots
// recursive CTE that otherwise dominated CPU (SOW-0117). The op-child passes
// always run because a newly-arrived op can carry a pending child link.
//
// The link passes are organized as a slice of named steps so the iteration
// stays trivial and the per-step rollback contract (any error rolls the WHOLE
// tx back) lives in one place; the resolverStep type captures both the SQL
// pass and its emit hook through the same signature.
func (r *resolver) linkOrphansGated(ctx context.Context, sessionsChanged bool) error {
	if r.db == nil {
		return errors.New("resolver: nil db")
	}
	return r.runResolverTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		affected := map[string]struct{}{}
		// Session passes (linkParents, linkRoots) only do useful work when a
		// session row changed since the last pass; skip them when the loop has
		// determined only ops committed (ops cannot change session parentage or
		// roots). The op-child passes always run because a newly-arrived op can
		// carry a pending child link.
		//
		// SOW-0118: linkRoots' transitive recursive CTE is O(sessions) and takes
		// ~30s on 552K rows. It is gated on lastRootsGen: only run when the
		// generation counter has advanced since the last roots run. This breaks
		// the busy-loop where the CTE takes longer than the 5s resolver interval.
		// linkParents (index-backed, fast) always runs when sessionsChanged.
		if sessionsChanged {
			if err := r.linkParents(ctx, tx, affected); err != nil {
				return err
			}
			genNow := int64(0)
			if r.ingestionGenNow != nil {
				genNow = r.ingestionGenNow()
			}
			if genNow != r.lastRootsGen.Load() {
				if err := r.linkRoots(ctx, tx, affected); err != nil {
					return err
				}
				r.lastRootsGen.Store(genNow)
			}
		}
		for _, step := range r.opSteps() {
			if err := step(ctx, tx, affected); err != nil {
				return err
			}
		}
		return r.emitResolverNotify(ctx, tx, affected)
	})
}

func (r *resolver) linkOrphans(ctx context.Context) error {
	return r.linkOrphansGated(ctx, true)
}

// resolverStep is one phase of a linkOrphans pass: it accumulates the session
// ids it changes into the shared affected set. All four link passes share this
// signature so the resolver loop is a uniform for-range over a step slice.
type resolverStep func(ctx context.Context, tx *sql.Tx, affected map[string]struct{}) error

// sessionSteps returns the link passes that only do useful work when a session
// row changed: linkParents (parent linkage) and linkRoots (root re-resolution,
// including the O(sessions) recursive CTE). linkParents must run before
// linkRoots so root_session_id can re-point at the just-linked parent in the
// same pass (see linkRoots's WHERE clause).

// opSteps returns the link passes that must run whenever any canonical row was
// committed, because a newly-arrived op can carry a pending child link. They
// follow the session passes because they read the now-resolved session tree.
func (r *resolver) opSteps() []resolverStep {
	return []resolverStep{r.linkOpChildren, r.linkOpChildrenByToolUse}
}

// runResolverTx wraps body in the resolver's single-tx contract: BeginTx,
// run body (which executes every link pass + the notify emit), Commit on
// success, Rollback on any error (including a successful body whose Commit
// fails). The defer/committed pattern is the same one the worker uses; it is
// extracted here so linkOrphans stays orchestration-only.
func (r *resolver) runResolverTx(ctx context.Context, body func(context.Context, *sql.Tx) error) error {
	if err := r.waitForDBWork(ctx); err != nil {
		return err
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
	if err := body(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("resolver: commit: %w", err)
	}
	committed = true
	return nil
}

func (r *resolver) waitForDBWork(ctx context.Context) error {
	if r.waitBeforeDBWork == nil {
		return nil
	}
	if err := r.waitBeforeDBWork(ctx); err != nil {
		return fmt.Errorf("resolver: wait for tail-state priority: %w", err)
	}
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
