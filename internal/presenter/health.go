package presenter

import (
	"context"
	"database/sql"
	"encoding/json"
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

const healthCoreQueryCount = 2

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
//
// Meta is the adapter-owned JSON metadata blob (SOW-0024), rendered
// verbatim from sources.meta_json. The field is OMITTED (omitempty) when
// the adapter did not populate the column — absence is the "not
// populated" signal, not zero / {}.
type healthSource struct {
	ID          string          `json:"id"`
	Format      string          `json:"format"`
	Location    string          `json:"location"`
	Enabled     bool            `json:"enabled"`
	LastSeenAt  *int64          `json:"last_seen_at"`
	LagUS       int64           `json:"lag_us"`
	ParseErrors int64           `json:"parse_errors"`
	LastSeq     int64           `json:"last_seq"`
	Meta        json.RawMessage `json:"meta,omitempty"`
}

type healthSignals struct {
	queriesFailed     int
	lagDegraded       bool
	recentParseErrors int64
}

type healthSourceRow struct {
	id          string
	format      string
	location    string
	enabled     int64
	lastSeenAt  sql.NullInt64
	parseErrors int64
	lastSeq     int64
	// metaJSON is the raw sources.meta_json column. Valid==false means the
	// adapter did not populate it (the "not populated" signal). String is
	// empty when Valid==true AND the column was bound to ''; the worker
	// guarantees a NULL bind for the empty override so String==""
	// together with Valid==true should never appear in practice, but the
	// build path treats both cases as "omit the field".
	metaJSON sql.NullString
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
	resp, signals := p.collectHealthResponse(ctx, now)
	resp.Status = healthStatusFromSignals(
		signals.queriesFailed,
		healthCoreQueryCount,
		signals.lagDegraded,
		signals.recentParseErrors,
	)

	writeJSON(w, r, p.logger, resp)
}

func (p *Presenter) collectHealthResponse(ctx context.Context, now time.Time) (healthResponse, healthSignals) {
	resp := p.baseHealthResponse(now)
	var signals healthSignals
	sources, lagDegraded, srcErr := p.collectSources(ctx, now)
	if srcErr != nil {
		signals.queriesFailed++
		p.logHealthQueryWarning(ctx, "presenter: health source query failed", srcErr)
	}
	resp.Sources = sources
	signals.lagDegraded = lagDegraded

	recentParseErrors, perErr := p.recentParseErrorCount(ctx, now)
	if perErr != nil {
		signals.queriesFailed++
		p.logHealthQueryWarning(ctx, "presenter: health parse-error query failed", perErr)
	}
	signals.recentParseErrors = recentParseErrors

	resp.DBSizeBytes = p.dbSizeBytesOrZero(ctx)
	resp.Notify = p.collectNotifyHealth(now)
	resp.SSE = healthSSE{Subscriptions: p.subs.count()}
	return resp, signals
}

func (p *Presenter) baseHealthResponse(now time.Time) healthResponse {
	return healthResponse{
		Version:       p.version,
		SchemaVersion: p.schemaVersion,
		UptimeS:       int64(now.Sub(p.startedAt).Seconds()),
		DBPath:        p.dbPath,
	}
}

func (p *Presenter) logHealthQueryWarning(ctx context.Context, msg string, err error) {
	p.logger.LogAttrs(ctx, slog.LevelWarn, msg,
		slog.Any("err", err),
		slog.String("request_id", requestIDFromContext(ctx)))
}

func healthStatusFromSignals(queriesFailed, totalQueries int, lagDegraded bool, recentParseErrors int64) string {
	switch {
	case queriesFailed >= totalQueries:
		return healthStatusDown
	case lagDegraded || recentParseErrors > 0:
		return healthStatusDegraded
	default:
		return healthStatusOK
	}
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
    IFNULL(sp.last_seq, 0),
    s.meta_json
FROM sources s
LEFT JOIN source_progress sp ON sp.source_id = s.id
ORDER BY s.created_at, s.id
`)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	return readHealthSourceRows(rows, p.logger, now.UnixMicro())
}

func readHealthSourceRows(rows *sql.Rows, logger *slog.Logger, nowUS int64) ([]healthSource, bool, error) {
	out := make([]healthSource, 0, 8)
	lagDegraded := false
	for rows.Next() {
		row, err := scanHealthSourceRow(rows)
		if err != nil {
			return nil, lagDegraded, err
		}
		hs, degraded := buildHealthSource(row, nowUS)
		if degraded {
			lagDegraded = true
		}
		warnOnMalformedHealthMeta(logger, row.id, row.metaJSON)
		out = append(out, hs)
	}
	if err := rows.Err(); err != nil {
		return nil, lagDegraded, err
	}
	return out, lagDegraded, nil
}

func scanHealthSourceRow(rows *sql.Rows) (healthSourceRow, error) {
	var row healthSourceRow
	err := rows.Scan(
		&row.id,
		&row.format,
		&row.location,
		&row.enabled,
		&row.lastSeenAt,
		&row.parseErrors,
		&row.lastSeq,
		&row.metaJSON,
	)
	return row, err
}

func buildHealthSource(row healthSourceRow, nowUS int64) (healthSource, bool) {
	source := healthSource{
		ID:          row.id,
		Format:      row.format,
		Location:    row.location,
		Enabled:     row.enabled != 0,
		ParseErrors: row.parseErrors,
		LastSeq:     row.lastSeq,
	}
	if row.lastSeenAt.Valid {
		v := row.lastSeenAt.Int64
		source.LastSeenAt = &v
		source.LagUS = sourceLagUS(nowUS, v)
	}
	if row.metaJSON.Valid && row.metaJSON.String != "" && json.Valid([]byte(row.metaJSON.String)) {
		source.Meta = json.RawMessage(row.metaJSON.String)
	}
	degraded := source.Enabled && source.LastSeenAt != nil &&
		source.LagUS > degradedLagThresholdUS
	return source, degraded
}

// warnOnMalformedHealthMeta logs a WARN (source id + subsystem) when the
// sources.meta_json column is non-NULL but fails json.Valid. The sole writer
// is the ingester (which marshals via encoding/json), so a malformed value
// is a sole-writer contract violation; the presenter trusts the blob is
// valid and, as defence-in-depth (SOW-0024), omits the field rather than
// corrupting the response. The build path has already omitted the field on
// invalid JSON; this site is the only place the WARN is emitted so the
// operator can see the violation. Slog is best-effort: a nil logger is
// tolerated (the constructor falls back to slog.Default() but tests inject
// a nil-capturing handler).
func warnOnMalformedHealthMeta(logger *slog.Logger, sourceID string, metaJSON sql.NullString) {
	if !metaJSON.Valid || metaJSON.String == "" {
		return
	}
	if json.Valid([]byte(metaJSON.String)) {
		return
	}
	if logger == nil {
		return
	}
	logger.Warn("presenter: dropping malformed sources.meta_json (sole-writer contract violation)",
		"source_id", sourceID,
		"value_len", len(metaJSON.String))
}

func sourceLagUS(nowUS, lastSeenUS int64) int64 {
	lag := nowUS - lastSeenUS
	if lag < 0 {
		return 0
	}
	return lag
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
// both columns is therefore unambiguous.
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
