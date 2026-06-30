package presenter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// healthStatus values for /api/health per observability.md §`/api/health`.
const (
	healthStatusOK       = "ok"
	healthStatusDegraded = "degraded"
	healthStatusDown     = "down"
)

const healthStatusDetailNoSourcesConfigured = "no_sources_configured"

// degradedLagThresholdUS is retained for legacy lag display and tests. Source
// freshness is decided from lifecycle/read-model state after SOW-0114.
const degradedLagThresholdUS = int64(60_000_000)

const (
	preTailGraceThresholdUS         = degradedLagThresholdUS
	longScanThresholdUS             = int64(600_000_000)
	tailRestartGraceThresholdUS     = longScanThresholdUS
	tailStaleThresholdUS            = int64(300_000_000)
	readModelRepairGraceThresholdUS = int64(300_000_000)
)

// recentParseErrorWindow is the look-back window for recent parse
// errors in the "degraded" rule. Anything inside an hour counts.
const recentParseErrorWindow = time.Hour

const healthCoreQueryCount = 2

// healthResponse is the JSON envelope of /api/health. JSON tags match
// the shape documented in observability.md §`/api/health` so external
// dashboards can rely on the field names.
type healthResponse struct {
	Status        string         `json:"status"`
	StatusDetail  string         `json:"status_detail,omitempty"`
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
	ID                         string          `json:"id"`
	Format                     string          `json:"format"`
	Location                   string          `json:"location"`
	Enabled                    bool            `json:"enabled"`
	LastSeenAt                 *int64          `json:"last_seen_at"`
	LagUS                      int64           `json:"lag_us"`
	ParseErrors                int64           `json:"parse_errors"`
	LastSeq                    int64           `json:"last_seq"`
	ProgressUpdatedAt          *int64          `json:"progress_updated_at,omitempty"`
	LifecycleState             string          `json:"lifecycle_state"`
	LifecycleStateAt           *int64          `json:"lifecycle_state_at,omitempty"`
	ScanStartedAt              *int64          `json:"scan_started_at,omitempty"`
	ScanCompletedAt            *int64          `json:"scan_completed_at,omitempty"`
	TailStartedAt              *int64          `json:"tail_started_at,omitempty"`
	TailHeartbeatAt            *int64          `json:"tail_heartbeat_at,omitempty"`
	TailFailedAt               *int64          `json:"tail_failed_at,omitempty"`
	TailRestartCount           int64           `json:"tail_restart_count"`
	LifecycleError             string          `json:"lifecycle_error,omitempty"`
	ReadModelState             string          `json:"read_model_state"`
	ReadModelStateAt           *int64          `json:"read_model_state_at,omitempty"`
	ReadModelRepairStartedAt   *int64          `json:"read_model_repair_started_at,omitempty"`
	ReadModelRepairCompletedAt *int64          `json:"read_model_repair_completed_at,omitempty"`
	ReadModelRepairFailedAt    *int64          `json:"read_model_repair_failed_at,omitempty"`
	ReadModelRepairAttempts    int64           `json:"read_model_repair_attempts"`
	ReadModelError             string          `json:"read_model_error,omitempty"`
	Meta                       json.RawMessage `json:"meta,omitempty"`
}

type healthSignals struct {
	queriesFailed       int
	sourceDegraded      bool
	noSourcesConfigured bool
	recentParseErrors   int64
}

type healthSourceRow struct {
	id                         string
	format                     string
	location                   string
	enabled                    int64
	lastSeenAt                 sql.NullInt64
	parseErrors                int64
	lastSeq                    int64
	progressUpdatedAt          sql.NullInt64
	lifecycleState             string
	lifecycleStateAt           sql.NullInt64
	scanStartedAt              sql.NullInt64
	scanCompletedAt            sql.NullInt64
	tailStartedAt              sql.NullInt64
	tailHeartbeatAt            sql.NullInt64
	tailFailedAt               sql.NullInt64
	tailRestartCount           int64
	lifecycleError             sql.NullString
	readModelState             string
	readModelStateAt           sql.NullInt64
	readModelRepairStartedAt   sql.NullInt64
	readModelRepairCompletedAt sql.NullInt64
	readModelRepairFailedAt    sql.NullInt64
	readModelRepairAttempts    int64
	readModelError             sql.NullString
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
//   - "down" when SQLite is unreachable (every core query errors).
//   - "degraded" when any enabled source has a degraded lifecycle/read-model
//     state, no sources are configured, or source-scoped parse errors occurred
//     in the last hour.
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
		signals.sourceDegraded || signals.noSourcesConfigured,
		signals.recentParseErrors,
	)
	if resp.Status != healthStatusDown && signals.noSourcesConfigured {
		resp.StatusDetail = healthStatusDetailNoSourcesConfigured
	}

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
	signals.sourceDegraded = lagDegraded
	signals.noSourcesConfigured = srcErr == nil && len(sources) == 0

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

func healthStatusFromSignals(queriesFailed, totalQueries int, sourceDegraded bool, recentParseErrors int64) string {
	switch {
	case queriesFailed >= totalQueries:
		return healthStatusDown
	case sourceDegraded || recentParseErrors > 0:
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

// collectSources returns the source rows used by /api/health and a flag
// indicating whether any enabled source is degraded by lifecycle/read-model
// state.
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
    NULLIF(sp.updated_at, 0),
    IFNULL(sp.lifecycle_state, 'unknown'),
    NULLIF(sp.lifecycle_state_at, 0),
    NULLIF(sp.scan_started_at, 0),
    NULLIF(sp.scan_completed_at, 0),
    NULLIF(sp.tail_started_at, 0),
    NULLIF(sp.tail_heartbeat_at, 0),
    NULLIF(sp.tail_failed_at, 0),
    IFNULL(sp.tail_restart_count, 0),
    sp.lifecycle_error,
    IFNULL(sp.read_model_state, 'unknown'),
    NULLIF(sp.read_model_state_at, 0),
    NULLIF(sp.read_model_repair_started_at, 0),
    NULLIF(sp.read_model_repair_completed_at, 0),
    NULLIF(sp.read_model_repair_failed_at, 0),
    IFNULL(sp.read_model_repair_attempts, 0),
    sp.read_model_error,
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
		hs.Meta = metaFromColumn(logger, row.id, row.metaJSON)
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
		&row.progressUpdatedAt,
		&row.lifecycleState,
		&row.lifecycleStateAt,
		&row.scanStartedAt,
		&row.scanCompletedAt,
		&row.tailStartedAt,
		&row.tailHeartbeatAt,
		&row.tailFailedAt,
		&row.tailRestartCount,
		&row.lifecycleError,
		&row.readModelState,
		&row.readModelStateAt,
		&row.readModelRepairStartedAt,
		&row.readModelRepairCompletedAt,
		&row.readModelRepairFailedAt,
		&row.readModelRepairAttempts,
		&row.readModelError,
		&row.metaJSON,
	)
	return row, err
}

func buildHealthSource(row healthSourceRow, nowUS int64) (healthSource, bool) {
	source := healthSource{
		ID:                      row.id,
		Format:                  row.format,
		Location:                row.location,
		Enabled:                 row.enabled != 0,
		ParseErrors:             row.parseErrors,
		LastSeq:                 row.lastSeq,
		LifecycleState:          effectiveHealthLifecycleState(row, nowUS),
		TailRestartCount:        row.tailRestartCount,
		LifecycleError:          sanitizePresenterDiagnostic(row.lifecycleError, row.location),
		ReadModelState:          defaultReadModelState(row.readModelState),
		ReadModelRepairAttempts: row.readModelRepairAttempts,
		ReadModelError:          sanitizePresenterDiagnostic(row.readModelError, row.location),
	}
	if row.lastSeenAt.Valid {
		v := row.lastSeenAt.Int64
		source.LastSeenAt = &v
		source.LagUS = sourceLagUS(nowUS, v)
	}
	source.ProgressUpdatedAt = ptrNonZero(row.progressUpdatedAt)
	source.LifecycleStateAt = ptrNonZero(row.lifecycleStateAt)
	source.ScanStartedAt = ptrNonZero(row.scanStartedAt)
	source.ScanCompletedAt = ptrNonZero(row.scanCompletedAt)
	source.TailStartedAt = ptrNonZero(row.tailStartedAt)
	source.TailHeartbeatAt = ptrNonZero(row.tailHeartbeatAt)
	source.TailFailedAt = ptrNonZero(row.tailFailedAt)
	source.ReadModelStateAt = ptrNonZero(row.readModelStateAt)
	source.ReadModelRepairStartedAt = ptrNonZero(row.readModelRepairStartedAt)
	source.ReadModelRepairCompletedAt = ptrNonZero(row.readModelRepairCompletedAt)
	source.ReadModelRepairFailedAt = ptrNonZero(row.readModelRepairFailedAt)
	// Meta is set by the caller (readHealthSourceRows) via metaFromColumn so
	// the json.Valid defence + WARN live in one shared helper (SOW-0024).
	degraded := source.Enabled && sourceLifecycleDegraded(row, source.LifecycleState, source.ReadModelState, nowUS)
	return source, degraded
}

// metaFromColumn renders the sources.meta_json column as the optional `meta`
// field shared by /api/health.sources[] and /api/sources.items[] (SOW-0024).
//
//   - NULL or empty string → nil json.RawMessage, which `omitempty` omits.
//     Absence — not zero, not {} — is the "adapter did not populate" signal.
//   - Valid JSON → the blob verbatim. The presenter never decodes the shape.
//   - Malformed → nil (omitted) PLUS a WARN with the source id and value_len
//     (never the raw value, so a large blob cannot flood the log). The sole
//     writer is the ingester, which marshals via encoding/json, so a malformed
//     value is a contract violation; omitting it keeps the response intact
//     rather than 500-ing the whole health/sources endpoint on one bad row.
//
// json.Valid is computed exactly once per row (the build and the warn share it).
func metaFromColumn(logger *slog.Logger, sourceID string, col sql.NullString) json.RawMessage {
	if !col.Valid || col.String == "" {
		return nil
	}
	if json.Valid([]byte(col.String)) {
		return json.RawMessage(col.String)
	}
	if logger != nil {
		logger.Warn("presenter: dropping malformed sources.meta_json (sole-writer contract violation)",
			slog.String("source_id", sourceID),
			slog.Int("value_len", len(col.String)))
	}
	return nil
}

func ptrNonZero(v sql.NullInt64) *int64 {
	if !v.Valid || v.Int64 == 0 {
		return nil
	}
	out := v.Int64
	return &out
}

func sanitizePresenterDiagnostic(v sql.NullString, sourceLocation string) string {
	if !v.Valid {
		return ""
	}
	out := strings.ToValidUTF8(v.String, "")
	replacements := []struct {
		from string
		to   string
	}{
		{from: sourceLocation, to: "[source]"},
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		replacements = append(replacements, struct {
			from string
			to   string
		}{from: home, to: "$HOME"})
	}
	for _, repl := range replacements {
		if repl.from == "" {
			continue
		}
		out = strings.ReplaceAll(out, repl.from, repl.to)
	}
	var b strings.Builder
	b.Grow(len(out))
	lastSpace := false
	for _, r := range out {
		if unicode.IsControl(r) {
			continue
		}
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	out = strings.TrimSpace(b.String())
	if len(out) <= 1024 {
		return out
	}
	end := 1024
	for end > 0 && !utf8.ValidString(out[:end]) {
		end--
	}
	return strings.TrimSpace(out[:end])
}

func effectiveHealthLifecycleState(row healthSourceRow, nowUS int64) string {
	state := defaultSourceState(row.lifecycleState)
	if state != "tailing" {
		return state
	}
	refUS, ok := tailHeartbeatReferenceUS(row.tailStartedAt, row.tailHeartbeatAt)
	if !ok || ageExceedsUS(refUS, nowUS, tailStaleThresholdUS) {
		return "tail_stale"
	}
	return state
}

func sourceLifecycleDegraded(row healthSourceRow, lifecycleState, readModelState string, nowUS int64) bool {
	switch lifecycleState {
	case "start_failed", "construct_failed", "scan_failed", "tail_stale", "tail_failed":
		return true
	case "tail_restarting":
		return row.tailRestartCount > 1 ||
			nullableAgeExceedsUS(row.lifecycleStateAt, nowUS, tailRestartGraceThresholdUS)
	case "unknown", "starting", "scan_complete", "tail_starting":
		if nullableAgeExceedsUS(row.lifecycleStateAt, nowUS, preTailGraceThresholdUS) {
			return true
		}
	case "scanning":
		if nullableAgeExceedsUS(firstValidInt64(row.scanStartedAt, row.lifecycleStateAt), nowUS, longScanThresholdUS) {
			return true
		}
	case "stopped":
		return false
	}
	return readModelDegraded(row, readModelState, nowUS)
}

func readModelDegraded(row healthSourceRow, readModelState string, nowUS int64) bool {
	switch readModelState {
	case "repair_timeout", "repair_failed":
		return true
	case "repair_pending":
		return nullableAgeExceedsUS(row.readModelStateAt, nowUS, readModelRepairGraceThresholdUS)
	case "repairing":
		return nullableAgeExceedsUS(firstValidInt64(row.readModelRepairStartedAt, row.readModelStateAt), nowUS, readModelRepairGraceThresholdUS)
	}
	return false
}

func defaultSourceState(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

func defaultReadModelState(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

func tailHeartbeatReferenceUS(tailStartedAt, tailHeartbeatAt sql.NullInt64) (int64, bool) {
	if tailHeartbeatAt.Valid && tailHeartbeatAt.Int64 > 0 {
		return tailHeartbeatAt.Int64, true
	}
	if tailStartedAt.Valid && tailStartedAt.Int64 > 0 {
		return tailStartedAt.Int64, true
	}
	return 0, false
}

func firstValidInt64(values ...sql.NullInt64) sql.NullInt64 {
	for _, v := range values {
		if v.Valid && v.Int64 > 0 {
			return v
		}
	}
	return sql.NullInt64{}
}

func nullableAgeExceedsUS(v sql.NullInt64, nowUS, thresholdUS int64) bool {
	if !v.Valid || v.Int64 <= 0 {
		return false
	}
	return ageExceedsUS(v.Int64, nowUS, thresholdUS)
}

func ageExceedsUS(thenUS, nowUS, thresholdUS int64) bool {
	return nowUS-thenUS > thresholdUS
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
