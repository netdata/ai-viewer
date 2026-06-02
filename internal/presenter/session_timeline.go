package presenter

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// timelineSpan is one op rendered as a span on a lane. end_ts is null ONLY
// for a still-running op; a POINT EVENT emits end_ts == start_ts (the server
// returns the stored end_ts as-is, never null-ing a recorded point). The client
// draws a null OR a <= start_ts end as an instant marker at start_ts —
// source-aware, NOT a viewport-edge bar (rest-api.md, ui-pages.md). kind 'compaction' is emitted
// as an ordinary span; the frontend keys on kind to draw the full-height
// breakpoint (rest-api.md, ui-pages.md).
type timelineSpan struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	StartTS int64  `json:"start_ts"`
	EndTS   *int64 `json:"end_ts"`
	Status  string `json:"status"`
}

// timelineLane is one session's lane: a key, a label, and its ordered
// spans. spans is always a non-nil slice so an op-less session serialises
// as [] not null.
type timelineLane struct {
	Key   string         `json:"key"`
	Label string         `json:"label"`
	Spans []timelineSpan `json:"spans"`
}

// timelineResponse is the JSON envelope of GET /api/sessions/:id/timeline.
// lanes is always non-nil; t_start/t_end are the min start / max end
// across every span (0/0 when the tree has no ops).
type timelineResponse struct {
	Lanes  []timelineLane `json:"lanes"`
	TStart int64          `json:"t_start"`
	TEnd   int64          `json:"t_end"`
}

// handleSessionTimeline answers GET /api/sessions/:id/timeline. It
// resolves :id to its session tree (root + all sessions sharing the
// root), emits one lane per session, and fills each lane's spans from that
// session's ops. 404 NOT_FOUND for an unknown id; method/HEAD/control-char
// handling mirrors handleSessionDetail.
func (p *Presenter) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
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

	rootID, err := p.resolveRootSessionID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "session not found", map[string]any{"id": id})
		return
	}
	if err != nil {
		p.writeDBError(ctx, w, r, "session.timeline.root", err)
		return
	}

	resp, err := p.buildTimeline(ctx, rootID)
	if err != nil {
		p.writeDBError(ctx, w, r, "session.timeline.build", err)
		return
	}
	writeJSON(w, r, p.logger, resp)
}

// buildTimeline assembles the lanes + spans for a resolved session tree in
// two bounded queries: one for the lanes (sessions), one for the spans
// (ops). Spans are grouped into lanes in Go; t_start/t_end are computed
// during the span scan.
func (p *Presenter) buildTimeline(ctx context.Context, rootID string) (timelineResponse, error) {
	lanes, laneIndex, err := p.loadTimelineLanes(ctx, rootID)
	if err != nil {
		return timelineResponse{}, err
	}
	resp := timelineResponse{Lanes: lanes}
	if len(lanes) == 0 {
		return resp, nil
	}
	tStart, tEnd, seen, err := p.attachTimelineSpans(ctx, rootID, lanes, laneIndex)
	if err != nil {
		return timelineResponse{}, err
	}
	if seen {
		resp.TStart = tStart
		resp.TEnd = tEnd
	}
	return resp, nil
}

// loadTimelineLanes reads every session in the tree (root + all sessions
// sharing root_session_id) and returns the lane slice plus a session_id →
// lane-index map for span grouping. The root session's label carries the
// " (root)" suffix. Ordered by start_ts so lanes are stable and root-first
// when it starts first.
func (p *Presenter) loadTimelineLanes(ctx context.Context, rootID string) ([]timelineLane, map[string]int, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, IFNULL(agent_name, ''), kind
FROM sessions WHERE root_session_id = ?
ORDER BY start_ts ASC, id ASC`, rootID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	lanes := make([]timelineLane, 0, 4)
	index := map[string]int{}
	for rows.Next() {
		var id, agent, kind string
		if err := rows.Scan(&id, &agent, &kind); err != nil {
			return nil, nil, err
		}
		index[id] = len(lanes)
		lanes = append(lanes, timelineLane{
			Key:   "session:" + id,
			Label: agentLabel(agent, kind, id == rootID),
			Spans: []timelineSpan{},
		})
	}
	return lanes, index, rows.Err()
}

// attachTimelineSpans streams every op of the tree's sessions in one
// query, appends each as a span to its lane via the laneIndex map, and
// tracks the min start / max end across all spans. A null end_ts
// contributes its start_ts to the t_end computation so the window always
// covers a running op's known extent. Returns seen=false when the tree has
// no ops (so the caller leaves t_start/t_end at 0).
func (p *Presenter) attachTimelineSpans(ctx context.Context, rootID string, lanes []timelineLane, laneIndex map[string]int) (tStart, tEnd int64, seen bool, err error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT o.session_id, o.id, o.kind, o.name, o.start_ts, o.end_ts, o.status
FROM ops o
WHERE o.session_id IN (SELECT id FROM sessions WHERE root_session_id = ?)
ORDER BY o.session_id ASC, o.start_ts ASC, o.seq ASC, o.id ASC`, rootID)
	if err != nil {
		return 0, 0, false, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			sessionID string
			span      timelineSpan
			endTS     sql.NullInt64
		)
		if err := rows.Scan(&sessionID, &span.ID, &span.Kind, &span.Name,
			&span.StartTS, &endTS, &span.Status); err != nil {
			return 0, 0, false, err
		}
		spanEnd := span.StartTS
		if endTS.Valid {
			v := endTS.Int64
			span.EndTS = &v
			spanEnd = v
		}
		if !seen || span.StartTS < tStart {
			tStart = span.StartTS
		}
		if !seen || spanEnd > tEnd {
			tEnd = spanEnd
		}
		seen = true
		li, ok := laneIndex[sessionID]
		if !ok {
			continue
		}
		lanes[li].Spans = append(lanes[li].Spans, span)
	}
	return tStart, tEnd, seen, rows.Err()
}
