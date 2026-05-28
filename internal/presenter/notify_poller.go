package presenter

import (
	"context"
	"log/slog"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
)

// notifyPollTimeout bounds each notify poll query so a wedged SQLite can
// never hang the poller goroutine. The poll is a short indexed range scan
// over a table the ingester keeps small, so a few seconds is generous.
const notifyPollTimeout = 5 * time.Second

// RunNotifyPoller is the exported entry point the serve binary calls to
// start the read-only notify poller. It blocks until ctx is cancelled
// (server shutdown), so the caller runs it in its own goroutine. The
// unexported runNotifyPoller carries the loop body so the package tests can
// drive it directly.
func (p *Presenter) RunNotifyPoller(ctx context.Context) {
	p.runNotifyPoller(ctx)
}

// runNotifyPoller is the read-only change-feed loop. On entry it sets the
// cursor to MAX(seq) so only changes that occur WHILE serve runs are
// delivered (clients reconcile historical state via REST — sse-protocol.md
// §Transport, data-model.md §notify). It then polls every
// notifyPollInterval until ctx is cancelled (server shutdown). The DB is
// the presenter's read-only handle; the poller never writes.
func (p *Presenter) runNotifyPoller(ctx context.Context) {
	if err := p.initNotifyCursor(ctx); err != nil {
		// A failed cursor init means the poller starts at 0 and would
		// replay every retained row to the first client. Log loudly (no
		// silent failure) and fall back to 0 — the rows are bounded by the
		// ingester's retention window, so this degrades gracefully.
		p.logger.LogAttrs(ctx, slog.LevelError, "presenter: notify cursor init failed",
			slog.Any("err", err))
	}
	ticker := time.NewTicker(p.notifyPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.pollNotifyOnce(ctx); err != nil {
				p.logger.LogAttrs(ctx, slog.LevelWarn, "presenter: notify poll failed",
					slog.Any("err", err))
			}
		}
	}
}

// initNotifyCursor sets the poll cursor to the current MAX(seq) of the
// notify table (0 when empty). Called once before the poll loop so the
// poller only delivers changes newer than boot.
func (p *Presenter) initNotifyCursor(ctx context.Context) error {
	qCtx, cancel := context.WithTimeout(ctx, notifyPollTimeout)
	defer cancel()
	var maxSeq int64
	if err := p.db.QueryRowContext(qCtx, `SELECT COALESCE(MAX(seq), 0) FROM notify`).Scan(&maxSeq); err != nil {
		return err
	}
	p.notifyMu.Lock()
	p.notifyCursor = maxSeq
	p.notifyMu.Unlock()
	return nil
}

// pollNotifyOnce reads every notify row past the cursor, fans matching rows
// out to subscriptions, advances the cursor, and records the last-applied
// seq/ts for /api/health. Matching SQL runs here (off the hub's fan-out
// path) as short point lookups. The subscription set is snapshotted ONCE so
// the registry lock is never held during SQL.
func (p *Presenter) pollNotifyOnce(ctx context.Context) error {
	cursor := p.cursor()
	rows, err := p.queryNotifyRows(ctx, cursor)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	subs := p.subs.snapshot()
	maxSeq := cursor
	var lastTS int64
	for _, row := range rows {
		p.fanOut(ctx, row, subs)
		if row.seq > maxSeq {
			maxSeq = row.seq
		}
		lastTS = row.ev.TS
	}
	p.advanceCursor(maxSeq, lastTS)
	return nil
}

// notifyRow is one decoded notify table row plus the notify.Event the
// poller derives from it. seq drives the cursor; ev carries the wire fields.
type notifyRow struct {
	seq int64
	ev  notify.Event
}

// queryNotifyRows reads the change rows past cursor in seq order. The query
// is bounded by notifyPollTimeout and rides the primary-key index
// (`WHERE seq > ?`).
func (p *Presenter) queryNotifyRows(ctx context.Context, cursor int64) ([]notifyRow, error) {
	qCtx, cancel := context.WithTimeout(ctx, notifyPollTimeout)
	defer cancel()
	sqlRows, err := p.db.QueryContext(qCtx, `
SELECT seq, ts_us, kind, IFNULL(session_id, ''), IFNULL(root_session_id, ''), IFNULL(source_id, '')
FROM notify WHERE seq > ? ORDER BY seq`, cursor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sqlRows.Close() }()

	var out []notifyRow
	for sqlRows.Next() {
		var r notifyRow
		if err := sqlRows.Scan(&r.seq, &r.ev.TS, &r.ev.Kind,
			&r.ev.SessionID, &r.ev.RootSessionID, &r.ev.SourceID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sqlRows.Err()
}

// fanOut evaluates one change row against every subscription and delivers
// the event to the matching ones. stats_invalidated is rate-limited per
// subscription to ≈1/statsCoalesceWindow. A match error is logged with
// structured context and that subscription is skipped (no silent failure),
// while the rest continue.
//
// The subscription set is a snapshot taken before any SQL; a subscription
// can be removed (retention expiry or DELETE) between the snapshot and
// delivery. When hub.Deliver reports the sub is gone, fanOut drops any
// statsCoalesce entry allowStatsEmit just recorded for it, so a removed
// subscription cannot leak coalesce state past the OnRemove cleanup.
func (p *Presenter) fanOut(ctx context.Context, row notifyRow, subs []subscriptionSnapshotItem) {
	for _, s := range subs {
		ok, err := p.matchOne(ctx, s, row.ev)
		if err != nil {
			p.logger.LogAttrs(ctx, slog.LevelWarn, "presenter: notify match failed",
				slog.String("subscription_id", s.id),
				slog.String("kind", row.ev.Kind),
				slog.Any("err", err))
			continue
		}
		if !ok {
			continue
		}
		if row.ev.Kind == "stats_invalidated" && !p.allowStatsEmit(s.id) {
			continue
		}
		if !p.hub.Deliver(s.id, row.ev) {
			// The subscription vanished between the snapshot and now; its
			// OnRemove cleanup has already run, so drop any coalesce entry
			// allowStatsEmit re-created above.
			p.forgetStatsCoalesce(s.id)
		}
	}
}

// forgetStatsCoalesce drops a subscription's stats-coalesce timestamp under
// the lock. Used both by the OnRemove hook and by fanOut when a delivery
// lands on an already-removed subscription, so the map cannot retain a
// dropped subscription.
func (p *Presenter) forgetStatsCoalesce(id string) {
	p.notifyMu.Lock()
	delete(p.statsCoalesce, id)
	p.notifyMu.Unlock()
}

// matchOne evaluates one subscription's filter against an event under its
// OWN timeout context derived from notifyPollTimeout. Bounding each match
// (not just the parent notify-row query) ensures a single wedged match
// SELECT cannot stall the poller goroutine indefinitely — the parent ctx is
// the poller's lifetime, which is effectively unbounded during normal
// operation.
func (p *Presenter) matchOne(ctx context.Context, s subscriptionSnapshotItem, ev notify.Event) (bool, error) {
	mCtx, cancel := context.WithTimeout(ctx, notifyPollTimeout)
	defer cancel()
	return s.filter.matches(mCtx, p.db, ev)
}

// allowStatsEmit reports whether a stats_invalidated event may be delivered
// to subscription id now, enforcing the ≈1/s coalesce window. It records the
// emit time when it returns true so subsequent rows in the same window are
// dropped.
func (p *Presenter) allowStatsEmit(id string) bool {
	now := p.notifyNow()
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()
	last, ok := p.statsCoalesce[id]
	if ok && now.Sub(last) < statsCoalesceWindow {
		return false
	}
	p.statsCoalesce[id] = now
	return true
}

// cursor returns the current poll cursor under the lock.
func (p *Presenter) cursor() int64 {
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()
	return p.notifyCursor
}

// advanceCursor moves the cursor to maxSeq and records the last-applied
// seq/ts for /api/health. lastTS is the ts_us of the highest-seq row applied
// this poll.
func (p *Presenter) advanceCursor(maxSeq, lastTS int64) {
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()
	p.notifyCursor = maxSeq
	p.lastAppliedSeq = maxSeq
	p.lastAppliedTS = lastTS
}

// notifyHealth returns the last-applied notify seq and ts_us for
// /api/health (both 0 before the poller applies any row).
func (p *Presenter) notifyHealth() (lastSeq, lastTS int64) {
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()
	return p.lastAppliedSeq, p.lastAppliedTS
}
