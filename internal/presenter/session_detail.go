package presenter

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// sessionDetail is the full session row returned by GET
// /api/sessions/:id. It carries the same scalar fields as the list item
// plus the columns the detail view surfaces (provider, error, cache
// tokens). The computed children list lives at the response top level
// (child_sessions), not nested here, matching rest-api.md.
type sessionDetail struct {
	ID                string  `json:"id"`
	NativeID          string  `json:"native_id"`
	RootSessionID     string  `json:"root_session_id"`
	ParentSessionID   *string `json:"parent_session_id"`
	SourceID          string  `json:"source_id"`
	Kind              string  `json:"kind"`
	AgentName         string  `json:"agent_name"`
	Model             string  `json:"model"`
	Provider          string  `json:"provider"`
	Status            string  `json:"status"`
	ErrorClass        *string `json:"error_class"`
	StartTS           int64   `json:"start_ts"`
	EndTS             *int64  `json:"end_ts"`
	TokensIn          int64   `json:"tokens_in"`
	TokensOut         int64   `json:"tokens_out"`
	TokensCacheRead   int64   `json:"tokens_cache_read"`
	TokensCacheWrite  int64   `json:"tokens_cache_write"`
	CostUSD           float64 `json:"cost_usd"`
	TurnCount         int64   `json:"turn_count"`
	OpCount           int64   `json:"op_count"`
	FailureCount      int64   `json:"failure_count"`
	ChildSessionCount int64   `json:"child_session_count"`
}

// turnDetail is one turns row with its ordered ops.
type turnDetail struct {
	ID        string     `json:"id"`
	Seq       int64      `json:"seq"`
	StartTS   int64      `json:"start_ts"`
	EndTS     *int64     `json:"end_ts"`
	Status    string     `json:"status"`
	TokensIn  int64      `json:"tokens_in"`
	TokensOut int64      `json:"tokens_out"`
	CostUSD   float64    `json:"cost_usd"`
	OpCount   int64      `json:"op_count"`
	Ops       []opDetail `json:"ops"`
}

// opDetail is one ops row plus its payload_refs.
type opDetail struct {
	ID             string       `json:"id"`
	Kind           string       `json:"kind"`
	Name           string       `json:"name"`
	Model          string       `json:"model"`
	Provider       string       `json:"provider"`
	StartTS        int64        `json:"start_ts"`
	EndTS          *int64       `json:"end_ts"`
	DurationUS     *int64       `json:"duration_us"`
	Status         string       `json:"status"`
	ErrorClass     *string      `json:"error_class"`
	TokensIn       int64        `json:"tokens_in"`
	TokensOut      int64        `json:"tokens_out"`
	CostUSD        float64      `json:"cost_usd"`
	CtxUsed        *int64       `json:"ctx_used"`
	CtxMax         *int64       `json:"ctx_max"`
	ChildSessionID *string      `json:"child_session_id"`
	PayloadRefs    []payloadRef `json:"payload_refs"`
}

// payloadRef is one payload_refs row. The byte-streaming route
// (GET /api/payloads/<id>) is Phase 2 and not yet registered, so the row
// carries only its metadata — no `url` field is emitted until the route and
// a view that consumes it land together (rest-api.md §GET /api/payloads).
type payloadRef struct {
	ID            int64   `json:"id"`
	Kind          string  `json:"kind"`
	Format        string  `json:"format"`
	Compression   *string `json:"compression"`
	OriginalBytes *int64  `json:"original_bytes"`
	StoredBytes   *int64  `json:"stored_bytes"`
}

// childSummary is one direct-child session in the child_sessions list.
type childSummary struct {
	ID           string  `json:"id"`
	NativeID     string  `json:"native_id"`
	Kind         string  `json:"kind"`
	AgentName    string  `json:"agent_name"`
	Model        string  `json:"model"`
	Status       string  `json:"status"`
	StartTS      int64   `json:"start_ts"`
	EndTS        *int64  `json:"end_ts"`
	TokensIn     int64   `json:"tokens_in"`
	TokensOut    int64   `json:"tokens_out"`
	CostUSD      float64 `json:"cost_usd"`
	OpCount      int64   `json:"op_count"`
	FailureCount int64   `json:"failure_count"`
}

// sessionDetailResponse is the JSON envelope of GET /api/sessions/:id.
type sessionDetailResponse struct {
	Session       sessionDetail  `json:"session"`
	Turns         []turnDetail   `json:"turns"`
	ChildSessions []childSummary `json:"child_sessions"`
}

// handleSessionDetail answers GET /api/sessions/:id. 404 NOT_FOUND when
// the id is unknown; otherwise loads the session, its turns+ops+payloads,
// and its direct children in four bounded queries.
func (p *Presenter) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	// Check control chars on the RAW path id before TrimSpace so a control
	// byte that is also whitespace (\t, \n, \r) is a loud 400 rather than
	// silently trimmed away into a doomed lookup (404). Mirrors the
	// query-value rule (rejectControlChars) and the logs handler.
	idRaw := r.PathValue("id")
	if err := rejectControlChars("id", idRaw); err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	id := strings.TrimSpace(idRaw)
	if id == "" {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "session id is required", nil)
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	sess, err := p.loadSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "session not found", map[string]any{"id": id})
		return
	}
	if err != nil {
		p.writeDBError(w, r, ctx, "session.detail.session", err)
		return
	}

	turns, err := p.loadTurnsWithOps(ctx, id)
	if err != nil {
		p.writeDBError(w, r, ctx, "session.detail.turns", err)
		return
	}
	children, err := p.loadChildSessions(ctx, id)
	if err != nil {
		p.writeDBError(w, r, ctx, "session.detail.children", err)
		return
	}

	writeJSON(w, r, p.logger, http.StatusOK, sessionDetailResponse{
		Session:       sess,
		Turns:         turns,
		ChildSessions: children,
	})
}

// loadSession reads the single sessions row. Returns sql.ErrNoRows when
// the id is unknown so the handler can map it to 404.
func (p *Presenter) loadSession(ctx context.Context, id string) (sessionDetail, error) {
	var (
		s        sessionDetail
		parent   sql.NullString
		errClass sql.NullString
		endTS    sql.NullInt64
	)
	err := p.db.QueryRowContext(ctx, `
SELECT
    id, native_id, root_session_id, parent_session_id, source_id, kind,
    IFNULL(agent_name, ''), IFNULL(model, ''), IFNULL(provider, ''),
    status, error_class, start_ts, end_ts, tokens_in, tokens_out,
    tokens_cache_read, tokens_cache_write, cost_usd,
    turn_count, op_count, failure_count,
    (SELECT COUNT(*) FROM sessions c WHERE c.parent_session_id = sessions.id)
FROM sessions WHERE id = ?`, id).Scan(
		&s.ID, &s.NativeID, &s.RootSessionID, &parent, &s.SourceID, &s.Kind,
		&s.AgentName, &s.Model, &s.Provider, &s.Status, &errClass,
		&s.StartTS, &endTS, &s.TokensIn, &s.TokensOut,
		&s.TokensCacheRead, &s.TokensCacheWrite, &s.CostUSD,
		&s.TurnCount, &s.OpCount, &s.FailureCount, &s.ChildSessionCount,
	)
	if err != nil {
		return s, err
	}
	if parent.Valid {
		v := parent.String
		s.ParentSessionID = &v
	}
	if errClass.Valid {
		v := errClass.String
		s.ErrorClass = &v
	}
	if endTS.Valid {
		v := endTS.Int64
		s.EndTS = &v
	}
	return s, nil
}

// loadChildSessions reads the direct children (parent_session_id = id)
// ordered by start_ts.
func (p *Presenter) loadChildSessions(ctx context.Context, id string) ([]childSummary, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, native_id, kind, IFNULL(agent_name, ''), IFNULL(model, ''), status,
       start_ts, end_ts, tokens_in, tokens_out, cost_usd, op_count, failure_count
FROM sessions WHERE parent_session_id = ? ORDER BY start_ts ASC, id ASC`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]childSummary, 0, 4)
	for rows.Next() {
		var (
			c     childSummary
			endTS sql.NullInt64
		)
		if err := rows.Scan(&c.ID, &c.NativeID, &c.Kind, &c.AgentName, &c.Model,
			&c.Status, &c.StartTS, &endTS, &c.TokensIn, &c.TokensOut, &c.CostUSD,
			&c.OpCount, &c.FailureCount); err != nil {
			return nil, err
		}
		if endTS.Valid {
			v := endTS.Int64
			c.EndTS = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
