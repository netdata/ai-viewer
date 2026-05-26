package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// writer applies one canonical event to one *sql.Tx. The writer is
// stateless — every method is a function of (tx, event, sourceID,
// sourceFormat). State that spans events within a batch (dirty sets,
// HWM advance) is owned by the worker that drives this writer.
type writer struct {
	sourceID     string
	sourceFormat string
	location     string
	pricer       Pricer
	catalog      *catalogWriter
	// dirty tracks session/turn IDs touched by the current batch so the
	// aggregate refresh runs over the bounded set. Cleared after each
	// flush.
	dirtySessionIDs map[string]struct{}
	dirtyTurnIDs    map[string]struct{}
	// batchMaxSeq tracks the maximum SourceSeq applied in the current
	// batch so the worker can advance the HWM atomically.
	batchMaxSeq uint64
	// lastCursor records the most recent SourceProgressEvent.Cursor
	// applied in the current batch so source_progress.cursor advances
	// at commit.
	lastCursor string
	// hasCursor flips to true when at least one SourceProgressEvent was
	// observed in the current batch; the worker uses this to decide
	// whether to write cursor at flush.
	hasCursor bool
	// affectedSessionIDs records canonical session IDs touched by this
	// batch (used by Chunk 11 to publish notify-pings; recorded here so
	// the seam is in place).
	affectedSessionIDs map[string]struct{}
}

func newWriter(sourceID, sourceFormat, location string, pricer Pricer) *writer {
	return &writer{
		sourceID:           sourceID,
		sourceFormat:       sourceFormat,
		location:           location,
		pricer:             pricer,
		catalog:            newCatalogWriter(),
		dirtySessionIDs:    make(map[string]struct{}),
		dirtyTurnIDs:       make(map[string]struct{}),
		affectedSessionIDs: make(map[string]struct{}),
	}
}

// resetBatch clears per-batch state. Called by the worker after each
// successful commit so the writer can be re-used for the next batch.
func (w *writer) resetBatch() {
	clear(w.dirtySessionIDs)
	clear(w.dirtyTurnIDs)
	clear(w.affectedSessionIDs)
	w.batchMaxSeq = 0
	w.lastCursor = ""
	w.hasCursor = false
}

// apply dispatches one event to its kind-specific writer.
func (w *writer) apply(ctx context.Context, tx *sql.Tx, ev canonical.Event) error {
	seq := ev.EventSourceSeq()
	if seq > w.batchMaxSeq {
		w.batchMaxSeq = seq
	}
	switch e := ev.(type) {
	case canonical.SessionStartedEvent:
		return w.applySessionStarted(ctx, tx, e)
	case canonical.SessionUpdatedEvent:
		return w.applySessionUpdated(ctx, tx, e)
	case canonical.SessionFinalizedEvent:
		return w.applySessionFinalized(ctx, tx, e)
	case canonical.TurnStartedEvent:
		return w.applyTurnStarted(ctx, tx, e)
	case canonical.TurnFinalizedEvent:
		return w.applyTurnFinalized(ctx, tx, e)
	case canonical.OpStartedEvent:
		return w.applyOpStarted(ctx, tx, e)
	case canonical.OpFinalizedEvent:
		return w.applyOpFinalized(ctx, tx, e)
	case canonical.PayloadRefEvent:
		return w.applyPayloadRef(ctx, tx, e)
	case canonical.LogEntryEvent:
		return w.applyLogEntry(ctx, tx, e)
	case canonical.SourceProgressEvent:
		return w.applySourceProgress(e)
	case canonical.SourceErrorEvent:
		return w.applySourceError(ctx, tx, e)
	default:
		return fmt.Errorf("writer: unknown event kind %s", ev.EventKind())
	}
}

// applySessionStarted inserts or updates a sessions row. Parent linkage
// is best-effort: if the parent is already present we set
// parent_session_id directly; otherwise we leave it NULL and stash
// parent_native_id in extras_json.aiViewer.parentNativeId so the
// resolver can backfill later.
func (w *writer) applySessionStarted(ctx context.Context, tx *sql.Tx, ev canonical.SessionStartedEvent) error {
	if ev.NativeID == "" {
		return errors.New("writer: SessionStartedEvent missing NativeID")
	}
	id := canonicalSessionID(w.sourceID, ev.NativeID)
	rootID := id
	if ev.RootNativeID != "" && ev.RootNativeID != ev.NativeID {
		// Only point root_session_id at another session when that
		// session is already present; otherwise the FK constraint
		// fires. The resolver pass fixes both parent_session_id and
		// root_session_id once the missing rows land.
		if rid, err := w.lookupSessionID(ctx, tx, ev.RootNativeID); err == nil {
			rootID = rid
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var parentID sql.NullString
	if ev.ParentNativeID != "" {
		if pid, err := w.lookupSessionID(ctx, tx, ev.ParentNativeID); err == nil {
			parentID = sql.NullString{String: pid, Valid: true}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	extras := mergeExtras(ev.Extras, map[string]any{
		"aiViewer": map[string]any{
			"parentNativeId": ev.ParentNativeID,
			"parentOpKey":    ev.ParentOpKey,
			"rootNativeId":   ev.RootNativeID,
		},
	})
	extrasJSON, err := marshalExtras(extras)
	if err != nil {
		return fmt.Errorf("writer: marshal session extras: %w", err)
	}
	kind := string(ev.Kind)
	if kind == "" {
		kind = string(canonical.KindRoot)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
    id, source_id, native_id, parent_session_id, root_session_id,
    kind, agent_name, model, cwd, call_path, status,
    start_ts, last_activity_ts, extras_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_id, native_id) DO UPDATE SET
    parent_session_id = COALESCE(sessions.parent_session_id, excluded.parent_session_id),
    root_session_id   = excluded.root_session_id,
    kind              = excluded.kind,
    agent_name        = COALESCE(NULLIF(excluded.agent_name, ''), sessions.agent_name),
    model             = COALESCE(NULLIF(excluded.model, ''), sessions.model),
    cwd               = COALESCE(NULLIF(excluded.cwd, ''), sessions.cwd),
    call_path         = COALESCE(NULLIF(excluded.call_path, ''), sessions.call_path),
    start_ts          = MIN(sessions.start_ts, excluded.start_ts),
    last_activity_ts  = MAX(sessions.last_activity_ts, excluded.last_activity_ts),
    extras_json       = excluded.extras_json
`,
		id, w.sourceID, ev.NativeID, parentID, rootID,
		kind, nullIfEmpty(ev.AgentName), nullIfEmpty(ev.Model), nullIfEmpty(ev.Cwd), nullIfEmpty(ev.CallPath), string(canonical.StatusRunning),
		ev.Ts, ev.Ts, extrasJSON,
	); err != nil {
		return fmt.Errorf("writer: insert session: %w", err)
	}
	w.markDirtySession(id)
	if err := w.catalog.onSessionStarted(ctx, tx, w.sourceFormat, ev); err != nil {
		return err
	}
	return nil
}

func (w *writer) applySessionUpdated(ctx context.Context, tx *sql.Tx, ev canonical.SessionUpdatedEvent) error {
	id := canonicalSessionID(w.sourceID, ev.NativeID)
	// Merge extras into existing JSON. SQLite's json_patch is exactly
	// what we need: it walks both trees and merges objects.
	extrasJSON, err := marshalExtras(ev.Extras)
	if err != nil {
		return fmt.Errorf("writer: marshal session updated extras: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions SET
    agent_name        = COALESCE(NULLIF(?, ''), agent_name),
    model             = COALESCE(NULLIF(?, ''), model),
    cwd               = COALESCE(NULLIF(?, ''), cwd),
    status            = COALESCE(NULLIF(?, ''), status),
    last_activity_ts  = MAX(last_activity_ts, ?),
    extras_json       = CASE
        WHEN ? IS NULL THEN extras_json
        WHEN extras_json IS NULL THEN ?
        ELSE json_patch(extras_json, ?)
    END
WHERE id = ?
`,
		ev.AgentName, ev.Model, ev.Cwd, ev.Status, ev.Ts,
		extrasJSON, extrasJSON, extrasJSON,
		id,
	); err != nil {
		return fmt.Errorf("writer: update session: %w", err)
	}
	w.markDirtySession(id)
	return nil
}

func (w *writer) applySessionFinalized(ctx context.Context, tx *sql.Tx, ev canonical.SessionFinalizedEvent) error {
	id := canonicalSessionID(w.sourceID, ev.NativeID)
	status := string(ev.Status)
	if status == "" {
		status = string(canonical.StatusCompleted)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions SET
    status           = ?,
    error_class      = NULLIF(?, ''),
    error_message    = NULLIF(?, ''),
    end_ts           = ?,
    last_activity_ts = MAX(last_activity_ts, ?)
WHERE id = ?
`,
		status, ev.ErrorClass, ev.ErrorMessage, nullIfZero(ev.EndTs), ev.Ts, id,
	); err != nil {
		return fmt.Errorf("writer: finalize session: %w", err)
	}
	w.markDirtySession(id)
	return nil
}

func (w *writer) applyTurnStarted(ctx context.Context, tx *sql.Tx, ev canonical.TurnStartedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.Seq)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (session_id, seq) DO UPDATE SET
    start_ts = MIN(turns.start_ts, excluded.start_ts)
`,
		turnID, sessionID, ev.Seq, ev.Ts, string(canonical.StatusRunning),
	); err != nil {
		return fmt.Errorf("writer: insert turn: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	return nil
}

func (w *writer) applyTurnFinalized(ctx context.Context, tx *sql.Tx, ev canonical.TurnFinalizedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.Seq)
	// Insert if missing (e.g. TurnFinalized arrives without a TurnStarted).
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status, error_class)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''))
ON CONFLICT (session_id, seq) DO UPDATE SET
    end_ts      = excluded.end_ts,
    status      = excluded.status,
    error_class = excluded.error_class
`,
		turnID, sessionID, ev.Seq, ev.Ts, nullIfZero(ev.EndTs),
		nonEmpty(ev.Status, string(canonical.StatusCompleted)), ev.ErrorClass,
	); err != nil {
		return fmt.Errorf("writer: finalize turn: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	return nil
}

func (w *writer) applyOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	// Ensure parent turn row exists — synthesize a running turn if no
	// TurnStarted arrived first. This matches the spec: turns may be
	// implicit when the source format doesn't emit explicit start
	// records.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (session_id, seq) DO NOTHING
`, turnID, sessionID, ev.TurnSeq, ev.Ts, string(canonical.StatusRunning)); err != nil {
		return fmt.Errorf("writer: synthesize turn for op: %w", err)
	}
	opID := canonicalOpID(turnID, ev.Seq)
	var parentOpID sql.NullString
	if ev.ParentOpSeq >= 0 {
		parentOpID = sql.NullString{String: canonicalOpID(turnID, ev.ParentOpSeq), Valid: true}
	}
	var childSessionID sql.NullString
	if ev.ChildSessionNativeID != "" {
		// Only point child_session_id at another session when that row
		// is already in the store; otherwise the FK fires. The child
		// session will eventually arrive; for now leave the link NULL
		// and stash the child native id in extras for a future
		// resolver pass (extension beyond Chunk 7 scope).
		if cid, err := w.lookupSessionID(ctx, tx, ev.ChildSessionNativeID); err == nil {
			childSessionID = sql.NullString{String: cid, Valid: true}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	extras, err := marshalExtras(ev.Extras)
	if err != nil {
		return fmt.Errorf("writer: marshal op extras: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO ops (
    id, turn_id, session_id, parent_op_id, seq,
    kind, name, tool_namespace, model, provider, provider_alias,
    reasoning_kind, start_ts, status,
    child_session_id, extras_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (turn_id, seq) DO UPDATE SET
    kind           = excluded.kind,
    name           = excluded.name,
    tool_namespace = COALESCE(NULLIF(excluded.tool_namespace, ''), ops.tool_namespace),
    model          = COALESCE(NULLIF(excluded.model, ''), ops.model),
    provider       = COALESCE(NULLIF(excluded.provider, ''), ops.provider),
    provider_alias = COALESCE(NULLIF(excluded.provider_alias, ''), ops.provider_alias),
    reasoning_kind = COALESCE(NULLIF(excluded.reasoning_kind, ''), ops.reasoning_kind),
    start_ts       = MIN(ops.start_ts, excluded.start_ts),
    parent_op_id   = COALESCE(ops.parent_op_id, excluded.parent_op_id),
    child_session_id = COALESCE(ops.child_session_id, excluded.child_session_id),
    extras_json    = excluded.extras_json
`,
		opID, turnID, sessionID, parentOpID, ev.Seq,
		string(ev.Kind), ev.Name, nullIfEmpty(ev.ToolNamespace), nullIfEmpty(ev.Model), nullIfEmpty(ev.Provider), nullIfEmpty(ev.ProviderAlias),
		nullIfEmpty(ev.ReasoningKind), ev.Ts, string(canonical.StatusRunning),
		childSessionID, extras,
	); err != nil {
		return fmt.Errorf("writer: insert op: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	if err := w.catalog.onOpStarted(ctx, tx, ev); err != nil {
		return err
	}
	return nil
}

func (w *writer) applyOpFinalized(ctx context.Context, tx *sql.Tx, ev canonical.OpFinalizedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	opID := canonicalOpID(turnID, ev.Seq)
	cost := ev.CostUSD
	if cost == 0 && w.pricer != nil {
		// Resolve provider/model from the row we know exists (or will
		// exist) for the matching OpStartedEvent. We do not require
		// they be set — providers without recorded cost AND without
		// known pricing yield zero, which is correct.
		var provider, model sql.NullString
		// sql.ErrNoRows is expected (op start may have been ingested in
		// a prior batch that's been pruned, or skipped for dedup). Any
		// other error means the database is unhealthy and silently
		// returning zero cost would violate the "no silent failures"
		// invariant in AGENTS.md.
		if lookupErr := tx.QueryRowContext(ctx, `SELECT provider, model FROM ops WHERE id = ?`, opID).
			Scan(&provider, &model); lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return fmt.Errorf("ingest writer: lookup op %s for pricing: %w", opID, lookupErr)
		}
		cost = w.pricer.Cost(provider.String, model.String, ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite)
	}
	durUS := sql.NullInt64{}
	if ev.EndTs > 0 && ev.Ts > 0 && ev.EndTs >= ev.Ts {
		durUS = sql.NullInt64{Int64: ev.EndTs - ev.Ts, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE ops SET
    end_ts             = ?,
    duration_us        = COALESCE(?, duration_us),
    status             = ?,
    error_class        = NULLIF(?, ''),
    error_message      = NULLIF(?, ''),
    tokens_in          = ?,
    tokens_out         = ?,
    tokens_cache_read  = ?,
    tokens_cache_write = ?,
    cost_usd           = ?,
    bytes_in           = ?,
    bytes_out          = ?,
    chars_in           = NULLIF(?, 0),
    chars_out          = NULLIF(?, 0),
    ctx_used           = NULLIF(?, 0),
    ctx_max            = NULLIF(?, 0)
WHERE id = ?
`,
		nullIfZero(ev.EndTs), durUS, nonEmpty(ev.Status, string(canonical.StatusCompleted)),
		ev.ErrorClass, ev.ErrorMessage,
		ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite, cost,
		ev.BytesIn, ev.BytesOut, ev.CharsIn, ev.CharsOut, ev.CtxUsed, ev.CtxMax,
		opID,
	); err != nil {
		return fmt.Errorf("writer: finalize op: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	if err := w.catalog.onOpFinalized(ctx, tx, opID, ev); err != nil {
		return err
	}
	return nil
}

func (w *writer) applyPayloadRef(ctx context.Context, tx *sql.Tx, ev canonical.PayloadRefEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	opID := canonicalOpID(turnID, ev.OpSeq)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO payload_refs (op_id, kind, format, compression, location_uri, original_bytes, stored_bytes, sha256)
VALUES (?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, ''))
`,
		opID, ev.PayloadKind, ev.Format, ev.Compression, ev.LocationURI,
		ev.OriginalBytes, ev.StoredBytes, ev.SHA256,
	); err != nil {
		return fmt.Errorf("writer: insert payload_ref: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	return nil
}

func (w *writer) applyLogEntry(ctx context.Context, tx *sql.Tx, ev canonical.LogEntryEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	var turnID sql.NullString
	if ev.TurnSeq > 0 {
		turnID = sql.NullString{String: canonicalTurnID(sessionID, ev.TurnSeq), Valid: true}
	}
	var opID sql.NullString
	if ev.OpSeq > 0 && turnID.Valid {
		opID = sql.NullString{String: canonicalOpID(turnID.String, ev.OpSeq), Valid: true}
	}
	extras, err := marshalExtras(ev.Extras)
	if err != nil {
		return fmt.Errorf("writer: marshal log extras: %w", err)
	}
	severity := strings.ToUpper(strings.TrimSpace(ev.Severity))
	if severity == "" {
		severity = "INF"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?)
`, sessionID, turnID, opID, ev.Ts, severity, ev.Source, ev.Message, extras); err != nil {
		return fmt.Errorf("writer: insert log_entry: %w", err)
	}
	w.markDirtySession(sessionID)
	return nil
}

// applySourceProgress records the cursor checkpoint and the wall-clock
// timestamp the adapter emitted at. The actual write to source_progress
// is deferred to the worker's flush so it happens once per batch.
func (w *writer) applySourceProgress(ev canonical.SourceProgressEvent) error {
	w.lastCursor = ev.Cursor
	w.hasCursor = true
	return nil
}

// applySourceError records a parse error against the source: increments
// sources.parse_errors and writes a log_entries row with session_id NULL.
func (w *writer) applySourceError(ctx context.Context, tx *sql.Tx, ev canonical.SourceErrorEvent) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE sources SET parse_errors = parse_errors + 1, last_seen_at = MAX(COALESCE(last_seen_at, 0), ?)
WHERE id = ?
`, ev.Ts, w.sourceID); err != nil {
		return fmt.Errorf("writer: bump parse_errors: %w", err)
	}
	extras, err := json.Marshal(map[string]any{
		"file":   ev.File,
		"offset": ev.Offset,
	})
	if err != nil {
		return fmt.Errorf("writer: marshal source error extras: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES (NULL, ?, NULL, NULL, ?, 'ERR', ?, ?, ?)
`, w.sourceID, ev.Ts, w.sourceFormat, ev.Message, string(extras)); err != nil {
		return fmt.Errorf("writer: insert source error log: %w", err)
	}
	return nil
}

// requireSessionID looks up the canonical session id for nativeID. If
// the session has not yet been observed (events ordered out-of-order),
// a stub row is inserted with start_ts = ts so foreign-key constraints
// on the dependent row succeed. The session row's metadata is filled
// later by the SessionStarted event (UPSERT on conflict).
func (w *writer) requireSessionID(ctx context.Context, tx *sql.Tx, nativeID string, ts int64) (string, error) {
	if nativeID == "" {
		return "", errors.New("writer: empty session native id")
	}
	id, err := w.lookupSessionID(ctx, tx, nativeID)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = canonicalSessionID(w.sourceID, nativeID)
	if _, insErr := tx.ExecContext(ctx, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_id, native_id) DO NOTHING
`,
		id, w.sourceID, nativeID, id, string(canonical.KindRoot), string(canonical.StatusRunning), ts, ts,
	); insErr != nil {
		return "", fmt.Errorf("writer: stub session: %w", insErr)
	}
	w.markDirtySession(id)
	return id, nil
}

// lookupSessionID returns the canonical sessions.id for (sourceID,
// nativeID). Returns sql.ErrNoRows if not present.
func (w *writer) lookupSessionID(ctx context.Context, tx *sql.Tx, nativeID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM sessions WHERE source_id = ? AND native_id = ?`,
		w.sourceID, nativeID).Scan(&id)
	return id, err
}

func (w *writer) markDirtySession(id string) {
	w.dirtySessionIDs[id] = struct{}{}
	w.affectedSessionIDs[id] = struct{}{}
}

func (w *writer) markDirtyTurn(id string) { w.dirtyTurnIDs[id] = struct{}{} }

// nullIfEmpty returns a sql.NullString backed by s when s != "" and
// NULL otherwise. Used to keep TEXT columns null when the source did
// not provide a value, so distinct downstream queries can rely on IS
// NULL rather than IS NULL OR = ”.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// marshalExtras encodes a map[string]any to JSON, returning NULL (nil)
// when the map is empty so the column stores SQL NULL. Used by every
// extras_json writer.
func marshalExtras(extras map[string]any) (any, error) {
	if len(extras) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(extras)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// mergeExtras returns the union of the two maps with values from b
// taking precedence on collisions. Nested map[string]any objects are
// merged recursively one level deep — sufficient for the
// aiViewer.parentNativeId / aiViewer.parentOpKey pattern used by the
// SessionStarted writer without pulling in a generic deep-merge.
func mergeExtras(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if av, ok := out[k]; ok {
			am, aIsMap := av.(map[string]any)
			bm, bIsMap := v.(map[string]any)
			if aIsMap && bIsMap {
				merged := make(map[string]any, len(am)+len(bm))
				for kk, vv := range am {
					merged[kk] = vv
				}
				for kk, vv := range bm {
					merged[kk] = vv
				}
				out[k] = merged
				continue
			}
		}
		out[k] = v
	}
	return out
}
