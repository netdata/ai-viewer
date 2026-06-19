package presenter

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// GET /api/sessions/:id/related — heuristic cross-harness links (SOW-0071).
// Sessions from a DIFFERENT harness (source_format) that started in the same
// working directory while this session was running. Neither harness records
// parent-child edges when one spawns another via a shell tool (e.g. claude-code
// running `codex` via Bash); this endpoint surfaces them as "possibly related"
// soft links, NOT deterministic edges.
//
// Detection: same cwd, different source_format, start_ts within [this.start_ts,
// COALESCE(this.end_ts, now)]. Ordered by start_ts ASC; LIMIT 10. All columns
// indexed (idx_sessions_cwd, start_ts).

// relatedSession is one heuristic cross-harness link.
type relatedSession struct {
	ID           string `json:"id"`
	SourceFormat string `json:"source_format"`
	AgentName    string `json:"agent_name"`
	Status       string `json:"status"`
	StartTS      int64  `json:"start_ts"`
	EndTS        *int64 `json:"end_ts"`
	Reason       string `json:"reason"`
}

// relatedResponse is the JSON envelope of GET /api/sessions/:id/related.
type relatedResponse struct {
	Related []relatedSession `json:"related"`
}

const relatedReason = "same cwd, started during this session (different harness)"

// handleSessionRelated answers GET /api/sessions/:id/related.
func (p *Presenter) handleSessionRelated(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	id, ok := p.sessionIDFromPath(w, r)
	if !ok {
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	resp, op, err := p.loadRelatedSessions(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, r, p.logger, http.StatusNotFound,
				CodeNotFound, "session not found", map[string]any{"id": id})
			return
		}
		p.writeDBError(ctx, w, r, op, err)
		return
	}
	writeJSON(w, r, p.logger, resp)
}

// loadRelatedSessions finds heuristic cross-harness links for the given session.
// Returns sql.ErrNoRows when the session itself is unknown (404). The query
// joins sources for the format label, restricts to a different source_format,
// and bounds the candidate's start_ts within the session's run window. The
// cursor is fully drained before return.
func (p *Presenter) loadRelatedSessions(ctx context.Context, id string) (relatedResponse, string, error) {
	q := `
SELECT r.id, src.format, IFNULL(r.agent_name, ''), r.status, r.start_ts, r.end_ts
FROM sessions s
JOIN sessions r ON r.cwd = s.cwd
                AND r.id != s.id
                AND r.source_id != s.source_id
JOIN sources src ON src.id = r.source_id AND src.format != (SELECT src2.format FROM sources src2 WHERE src2.id = s.source_id)
WHERE s.id = ?
  AND r.start_ts >= s.start_ts
  AND r.start_ts <= COALESCE(s.end_ts, ?)
ORDER BY r.start_ts ASC
LIMIT 10`
	nowUs := p.now().UnixMicro()
	rows, err := p.db.QueryContext(ctx, q, id, nowUs)
	if err != nil {
		return relatedResponse{}, "session.related.query", err
	}
	defer func() { _ = rows.Close() }()

	out := make([]relatedSession, 0, 4)
	for rows.Next() {
		var (
			rs    relatedSession
			endTS sql.NullInt64
		)
		if err := rows.Scan(&rs.ID, &rs.SourceFormat, &rs.AgentName, &rs.Status, &rs.StartTS, &endTS); err != nil {
			return relatedResponse{}, "session.related.scan", err
		}
		if endTS.Valid {
			v := endTS.Int64
			rs.EndTS = &v
		}
		rs.Reason = relatedReason
		out = append(out, rs)
	}
	if err := rows.Err(); err != nil {
		return relatedResponse{}, "session.related.rows", err
	}
	// Detect whether the session itself exists (the JOIN may return zero rows
	// for a valid session with no related sessions, or for a nonexistent id).
	if len(out) == 0 {
		var exists int
		if err := p.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return relatedResponse{}, "", sql.ErrNoRows
			}
			return relatedResponse{}, "session.related.exists", err
		}
	}
	return relatedResponse{Related: out}, "", nil
}
