package presenter

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// GET /api/sessions/:id/related — deterministic cross-harness links (SOW-0089
// chunk 5b, replacing SOW-0071's heuristic). Two sessions are linked when
// they share a `first_user_message_hash` — i.e. the operator ran the same
// initial prompt across two harnesses (e.g. claude-code + codex) on the same
// task. This is the operator's verbatim ask:
//
//   "related sessions are 'possibly related', although they could probably be
//    matched accurately, because we have the prompt the agent was run, so
//    we can do this match deterministically"
//
// Detection rules:
//   1. PREFERRED: same first_user_message_hash, different source_id,
//      no overlap with self. This is the deterministic match.
//   2. FALLBACK (when the source session has no first_user_message_hash
//      computed yet, or the hash is rare enough that no other session shares
//      it): the legacy cwd + overlapping-window heuristic. Marked in the
//      response's `reason` field as "heuristic" so the UI can render it as
//      "possibly related" vs the deterministic match's "same initial prompt".
//
// Ordered by start_ts ASC; LIMIT 10. Indexed on first_user_message_hash
// (idx_sessions_first_user_message_hash) + cwd + start_ts (existing).

// relatedSession is one deterministic OR heuristic cross-harness link.
type relatedSession struct {
	ID           string `json:"id"`
	SourceFormat string `json:"source_format"`
	AgentName    string `json:"agent_name"`
	Status       string `json:"status"`
	StartTS      int64  `json:"start_ts"`
	EndTS        *int64 `json:"end_ts"`
	// Reason is a short tag identifying which matching rule fired. The UI
	// renders "same initial prompt" for `reasonDeterministic` and
	// "possibly related" for `reasonHeuristic`. The tag is intentionally
	// machine-readable; the UI maps it to a human phrase.
	Reason string `json:"reason"`
}

// relatedResponse is the JSON envelope of GET /api/sessions/:id/related.
type relatedResponse struct {
	Related []relatedSession `json:"related"`
}

const (
	reasonDeterministic = "same initial prompt"
	reasonHeuristic     = "same cwd, started during this session (different harness)"
)

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

// loadRelatedSessions finds cross-harness links for the given session. The
// query runs TWO sub-queries (deterministic + heuristic) UNION'd and ordered:
//
//  1. Deterministic: same first_user_message_hash, different source_id.
//     Marked `reason = 'same initial prompt'`.
//  2. Heuristic: same cwd, different source_id, start_ts within the source
//     session's run window. Marked `reason = 'same cwd, ...'`. ONLY runs
//     when the source session has first_user_message_hash IS NULL (so a
//     session with a real hash never sees the noisy cwd matches); otherwise
//     a deterministic match for session A would still return heuristic cwd
//     matches that the operator explicitly asked us to stop showing.
//
// Each sub-query is bound by LIMIT 10 and the outer UNION keeps the LIMIT 10
// (we don't want to show 20 results). The cursor is fully drained before
// return. Returns sql.ErrNoRows when the session itself is unknown (404).
func (p *Presenter) loadRelatedSessions(ctx context.Context, id string) (relatedResponse, string, error) {
	nowUs := p.now().UnixMicro()
	q := `
SELECT * FROM (
  -- Deterministic match: same first_user_message_hash, different harness.
  -- The harness filter (src.format != s_src.format) excludes same-harness
  -- sessions (which are usually child-sessions already linked via
  -- parent_session_id, not "related" cross-harness links).
  SELECT r.id, src.format, IFNULL(r.agent_name, ''), r.status, r.start_ts, r.end_ts,
         'same initial prompt' AS reason, 1 AS sort_order
  FROM sessions s
  JOIN sources s_src ON s_src.id = s.source_id
  JOIN sessions r ON r.first_user_message_hash IS NOT NULL
                  AND r.first_user_message_hash = s.first_user_message_hash
                  AND r.id != s.id
                  AND r.source_id != s.source_id
  JOIN sources src ON src.id = r.source_id
                 AND src.format != s_src.format
  WHERE s.id = ?
    AND s.first_user_message_hash IS NOT NULL

  UNION ALL

  -- Heuristic fallback (SOW-0071): same cwd, different harness, overlapping
  -- run window. ONLY fires when the source session has no hash yet (the
  -- deterministic match is strictly better; we don't want noisy cwd matches
  -- competing with deterministic ones for the same session).
  SELECT r.id, src.format, IFNULL(r.agent_name, ''), r.status, r.start_ts, r.end_ts,
         'same cwd' AS reason, 2 AS sort_order
  FROM sessions s
  JOIN sources s_src ON s_src.id = s.source_id
  JOIN sessions r ON r.cwd = s.cwd
                  AND r.id != s.id
                  AND r.source_id != s.source_id
  JOIN sources src ON src.id = r.source_id
                 AND src.format != s_src.format
  WHERE s.id = ?
    AND s.first_user_message_hash IS NULL
    AND r.start_ts >= s.start_ts
    AND r.start_ts <= COALESCE(s.end_ts, ?)
)
ORDER BY sort_order ASC, start_ts ASC
LIMIT 10`
	rows, err := p.db.QueryContext(ctx, q, id, id, nowUs)
	if err != nil {
		return relatedResponse{}, "session.related.query", err
	}
	defer func() { _ = rows.Close() }()

	out := make([]relatedSession, 0, 4)
	for rows.Next() {
		var (
			rs      relatedSession
			endTS   sql.NullInt64
			raw     string
			sortOrd int64
		)
		if err := rows.Scan(&rs.ID, &rs.SourceFormat, &rs.AgentName, &rs.Status, &rs.StartTS, &endTS, &raw, &sortOrd); err != nil {
			return relatedResponse{}, "session.related.scan", err
		}
		_ = sortOrd // only used in SQL ORDER BY, not exposed in the response
		if endTS.Valid {
			v := endTS.Int64
			rs.EndTS = &v
		}
		switch raw {
		case "same initial prompt":
			rs.Reason = reasonDeterministic
		default:
			rs.Reason = reasonHeuristic
		}
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
