package presenter

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// healthStatus values for /api/health per observability.md §`/api/health`.
const (
	healthStatusOK       = "ok"
	healthStatusDegraded = "degraded"
	healthStatusDown     = "down"
)

// degradedLagThresholdUS is the maximum permissible source lag before
// /api/health flips to "degraded". 60 s in microseconds, per
// observability.md.
const degradedLagThresholdUS = int64(60_000_000)

// recentParseErrorWindow is the look-back window for recent parse
// errors in the "degraded" rule. Anything inside an hour counts.
const recentParseErrorWindow = time.Hour

// healthResponse is the JSON envelope of /api/health. JSON tags match
// the shape documented in observability.md §`/api/health` so external
// dashboards can rely on the field names.
type healthResponse struct {
	Status        string         `json:"status"`
	Version       string         `json:"version"`
	SchemaVersion int            `json:"schema_version"`
	UptimeS       int64          `json:"uptime_s"`
	DBPath        string         `json:"db_path"`
	DBSizeBytes   int64          `json:"db_size_bytes"`
	Sources       []healthSource `json:"sources"`
	Notify        healthNotify   `json:"notify"`
	SSE           healthSSE      `json:"sse"`
}

// healthNotify is the notify-poller window in /api/health per
// observability.md §`/api/health`. LastSeq is the high-water notify.seq the
// read-only poller has applied (0 before the first row, since the cursor
// starts at MAX(seq) on boot); LagUS is now − ts_us of that last applied
// row, and 0 when the poller has not applied any row.
type healthNotify struct {
	LastSeq int64 `json:"last_seq"`
	LagUS   int64 `json:"lag_us"`
}

// healthSSE reports the active SSE subscription count (including those in
// the 60s reconnect-retention window) per observability.md §`/api/health`.
type healthSSE struct {
	Subscriptions int `json:"subscriptions"`
}

// healthSource is the per-source health row reported alongside the
// global status.
//
// LastSeq is the adapter's opaque per-source observability counter (max
// SourceSeq seen) from source_progress.last_seq; NOT a dedup gate.
// Semantics depend on the adapter and are NOT a portable event count:
//   - aiagent_v3 packs `ledgerSeq << 12 | subIdx`, so it grows
//     roughly linearly with events emitted by that source.
//   - aiagent_v2 packs FNV-64(originId, opTree path), which is an
//     opaque 64-bit hash — it bears no relation to event count.
//
// Do NOT compare last_seq across formats; it is only meaningful as
// "the most recent cursor position the writer committed".
type healthSource struct {
	ID          string `json:"id"`
	Format      string `json:"format"`
	Location    string `json:"location"`
	Enabled     bool   `json:"enabled"`
	LastSeenAt  *int64 `json:"last_seen_at"`
	LagUS       int64  `json:"lag_us"`
	ParseErrors int64  `json:"parse_errors"`
	LastSeq     int64  `json:"last_seq"`
}

// handleHealth answers GET /api/health. The handler runs three short
// queries against the read-only DB: source list, recent log-error
// count, and the DB file size for diagnostics. Each query is bounded by
// a 5 s context so a misbehaving SQLite cannot hang the health probe.
//
// The status rules per observability.md §`/api/health`:
//   - "down" when SQLite is unreachable (every query errors).
//   - "degraded" when any enabled source has lag_us > 60_000_000 OR
//     parse_errors > 0 in the last hour.
//   - "ok" otherwise.
//
// The handler intentionally does not refuse to answer when one query
// errors — that would render /api/health useless for triage. Errored
// queries log a warning and surface zero values; the status
// short-circuits to "down" only when every query fails.
func (p *Presenter) handleHealth(w http.ResponseWriter, r *http.Request) {
	// HEAD is mandatory per RFC 9110 §9.3.2 — every resource that
	// supports GET must answer HEAD with identical headers and an
	// empty body. writeJSON skips the body when r.Method == HEAD.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	now := p.now()
	resp := healthResponse{
		Version:       p.version,
		SchemaVersion: p.schemaVersion,
		UptimeS:       int64(now.Sub(p.startedAt).Seconds()),
		DBPath:        p.dbPath,
	}

	queriesFailed := 0
	totalQueries := 2 // sources query + parse-error rollup

	sources, lagDegraded, srcErr := p.collectSources(ctx, now)
	if srcErr != nil {
		queriesFailed++
		p.logger.LogAttrs(ctx, slog.LevelWarn, "presenter: health source query failed",
			slog.Any("err", srcErr),
			slog.String("request_id", requestIDFromContext(ctx)))
	}
	resp.Sources = sources

	recentParseErrors, perErr := p.recentParseErrorCount(ctx, now)
	if perErr != nil {
		queriesFailed++
		p.logger.LogAttrs(ctx, slog.LevelWarn, "presenter: health parse-error query failed",
			slog.Any("err", perErr),
			slog.String("request_id", requestIDFromContext(ctx)))
	}

	resp.DBSizeBytes = p.dbSizeBytesOrZero(ctx)
	resp.Notify = p.collectNotifyHealth(now)
	resp.SSE = healthSSE{Subscriptions: p.subs.count()}

	switch {
	case queriesFailed >= totalQueries:
		resp.Status = healthStatusDown
	case lagDegraded || recentParseErrors > 0:
		resp.Status = healthStatusDegraded
	default:
		resp.Status = healthStatusOK
	}

	writeJSON(w, r, p.logger, http.StatusOK, resp)
}

// collectNotifyHealth derives the notify-poller window from the poller's
// last-applied seq/ts. LagUS is now − lastTS, clamped at 0, and is 0 when
// no row has been applied (lastTS == 0). This is a pure in-memory read; the
// poller updates the fields under its own lock (notify_poller.go).
func (p *Presenter) collectNotifyHealth(now time.Time) healthNotify {
	lastSeq, lastTS := p.notifyHealth()
	out := healthNotify{LastSeq: lastSeq}
	if lastTS > 0 {
		lag := now.UnixMicro() - lastTS
		if lag < 0 {
			lag = 0
		}
		out.LagUS = lag
	}
	return out
}

// collectSources returns the source rows used by /api/health and a
// flag indicating whether any enabled source exceeds the degraded lag
// threshold.
func (p *Presenter) collectSources(ctx context.Context, now time.Time) ([]healthSource, bool, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT
    s.id,
    s.format,
    s.location,
    s.enabled,
    s.last_seen_at,
    s.parse_errors,
    IFNULL(sp.last_seq, 0)
FROM sources s
LEFT JOIN source_progress sp ON sp.source_id = s.id
ORDER BY s.created_at, s.id
`)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]healthSource, 0, 8)
	nowUS := now.UnixMicro()
	lagDegraded := false

	for rows.Next() {
		var (
			id, format, location string
			enabled              int64
			lastSeenAt           sql.NullInt64
			parseErrors          int64
			lastSeq              int64
		)
		if err := rows.Scan(&id, &format, &location, &enabled, &lastSeenAt, &parseErrors, &lastSeq); err != nil {
			return out, lagDegraded, err
		}
		hs := healthSource{
			ID:          id,
			Format:      format,
			Location:    location,
			Enabled:     enabled != 0,
			ParseErrors: parseErrors,
			LastSeq:     lastSeq,
		}
		if lastSeenAt.Valid {
			v := lastSeenAt.Int64
			hs.LastSeenAt = &v
			lag := nowUS - v
			if lag < 0 {
				lag = 0
			}
			hs.LagUS = lag
		}
		if hs.Enabled && hs.LastSeenAt != nil && hs.LagUS > degradedLagThresholdUS {
			lagDegraded = true
		}
		out = append(out, hs)
	}
	if err := rows.Err(); err != nil {
		return out, lagDegraded, err
	}
	return out, lagDegraded, nil
}

// recentParseErrorCount returns the count of source-scoped parse-error
// rows produced in the last hour. Per observability.md §`/api/health`
// the degraded trigger is "any source has parse_errors > 0 in the last
// hour" — so the query MUST be restricted to source-scoped errors
// (source_id IS NOT NULL AND session_id IS NULL), the exact row shape
// writer.applySourceError and writer.emitPricingMiss emit. Session-
// scoped agent / tool errors (the LogEntryEvent path inserts session_id
// NOT NULL) live in the same table but are unrelated to ingest-source
// health and would otherwise produce false-positive "degraded" reports.
//
// The session_id IS NULL clause is the discriminator: applySourceError
// at internal/ingest/writer.go inserts NULL session_id, NULL turn_id,
// NULL op_id with a non-NULL source_id; applyLogEntry inserts a
// session_id of the affected session and a NULL source_id. Filtering on
// both columns is therefore unambiguous (codex iter-3 P2#4).
func (p *Presenter) recentParseErrorCount(ctx context.Context, now time.Time) (int64, error) {
	since := now.Add(-recentParseErrorWindow).UnixMicro()
	var count int64
	err := p.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM log_entries
WHERE severity IN ('ERR', 'WRN')
  AND ts >= ?
  AND source_id IS NOT NULL
  AND session_id IS NULL
`, since).Scan(&count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return count, nil
}

// dbSizeBytesOrZero returns the SQLite database file size via the
// page_count * page_size PRAGMA pair. Failures are logged but return 0
// so /api/health still responds — diagnostics, not a hard gate.
func (p *Presenter) dbSizeBytesOrZero(ctx context.Context) int64 {
	var pageCount, pageSize int64
	if err := p.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		p.logger.LogAttrs(ctx, slog.LevelDebug, "presenter: page_count probe failed",
			slog.Any("err", err),
			slog.String("request_id", requestIDFromContext(ctx)))
		return 0
	}
	if err := p.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		p.logger.LogAttrs(ctx, slog.LevelDebug, "presenter: page_size probe failed",
			slog.Any("err", err),
			slog.String("request_id", requestIDFromContext(ctx)))
		return 0
	}
	if pageCount < 0 || pageSize < 0 {
		return 0
	}
	return pageCount * pageSize
}
