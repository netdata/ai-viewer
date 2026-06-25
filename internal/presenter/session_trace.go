package presenter

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
)

// GET /api/sessions/:id/trace — the whole-tree trace (SOW-0070). Every op of
// every session in the resolved tree (root + all sessions sharing its
// root_session_id), each tagged with the session it belongs to, in ONE flat
// ordered list. The client builds a merged op tree from it (sub-session ops
// splice beneath the child_session_id op that spawned them). Mirrors the
// root-resolution + tree scope of /timeline and /topology (resolveRootSessionID
// + WHERE root_session_id = ?) so a sub-session id returns its whole tree.
//
// The flat op list carries the full opDetail field set PLUS the session tags
// session_id / session_agent_name / session_kind and the op's turn_seq, so the
// client colors/filters by sub-agent and orders within a session without a
// second round-trip. error_message is surfaced for failed ops (AC3).

// traceOp is one op in the whole-tree trace, carrying ONLY the fields the
// trace consumers (Waterfall, FlameGraph, EventList, ByTurnWaterfall) use.
// The full opDetail shape (tokens, cost, ctx, provider, model) is delivered
// via /api/sessions/:id (session detail) — the trace endpoint is hit on
// initial page load and shared by every viz tab, so keeping it minimal is
// the highest-leverage perf change. The trace shape carries:
//
//   - id, kind, name, status, start_ts, end_ts, duration_us (the per-op span)
//   - error_class (the failed-op filter + FlameGraph red styling)
//   - error_message (rendered inline on failed ops in EventList)
//   - parent_op_id, child_session_id (the tree structure)
//   - session_id, session_agent_name, session_kind (the sub-agent tags)
//   - turn_seq (the within-session turn ordering)
//
// Fields that the trace does NOT carry (and that the operator can fetch from
// /api/sessions/:id when they click an op): model, provider, tokens_in/out,
// cost_usd, ctx_used/max, payload_refs. Dropping them cuts the trace
// response ~50% (3.9 MB → ~2 MB on a 7 680-op session).
type traceOp struct {
	ID             string  `json:"id"`
	TurnSeq        int64   `json:"turn_seq"`
	Kind           string  `json:"kind"`
	Name           string  `json:"name"`
	ParentOpID     *string `json:"parent_op_id"`
	StartTS        int64   `json:"start_ts"`
	EndTS          *int64  `json:"end_ts"`
	DurationUS     *int64  `json:"duration_us"`
	Status         string  `json:"status"`
	ErrorClass     *string `json:"error_class"`
	ErrorMessage   *string `json:"error_message"`
	ChildSessionID *string `json:"child_session_id"`
	SessionID      string  `json:"session_id"`
	SessionAgent   string  `json:"session_agent_name"`
	SessionKind    string  `json:"session_kind"`
	// PayloadRefs is OMITTED by default — the trace is consumed by
	// Waterfall/FlameGraph/EventList/ByTurnWaterfall, none of which render
	// payload previews. The 1.5-refs/op average * 7k ops × 5KB/serialized-ref
	// makes the default response ~5 MB; adding payload_refs only when a
	// caller explicitly opts in via ?include=payload_refs cuts the default
	// ~3× AND skips a full-table-scan query (attachTracePayloadRefs).
	// Marshal as null when omitted (Go nil-slice semantics) so a client can
	// distinguish "not requested" from "requested but empty".
	PayloadRefs []payloadRef `json:"payload_refs,omitempty"`
}

// traceResponse is the JSON envelope of GET /api/sessions/:id/trace. ops is
// always non-nil so an op-less tree serializes as [] not null.
type traceResponse struct {
	RootID string    `json:"root_id"`
	Ops    []traceOp `json:"ops"`
}

// handleSessionTrace answers GET /api/sessions/:id/trace.
func (p *Presenter) handleSessionTrace(w http.ResponseWriter, r *http.Request) {
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
		p.writeDBError(ctx, w, r, "session.trace.root", err)
		return
	}

	includes, err := parseIncludeOptions(r.URL.Query().Get("include"), includeAllow("payload_refs", "proof"))
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	if err := requireProofPayloadRefs(includes); err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	resp, err := p.buildTrace(ctx, rootID, includes.PayloadRefs, includes.Proof)
	if err != nil {
		p.writeDBError(ctx, w, r, "session.trace.build", err)
		return
	}
	writeJSON(w, r, p.logger, resp)
}

// buildTrace assembles the whole-tree trace for the resolved root: one query
// over every op of every session in the tree, ordered deterministically, with
// each op tagged by its owning session. payload_refs are attached in a second
// (also tree-scoped) query when includeRefs is true; the default omits them
// (see handleSessionTrace's include=payload_refs opt-in).
//
// The two reads (ops over the tree + refs over the tree) run in parallel
// via a goroutine when includeRefs is true — the reader's pool has 8
// connections and the two queries are independent. On the production DB
// this halves the wall-clock latency of the include=payload_refs path.
func (p *Presenter) buildTrace(ctx context.Context, rootID string, includeRefs bool, includeProof bool) (traceResponse, error) {
	ops, err := p.loadTraceOps(ctx, rootID)
	if err != nil {
		return traceResponse{}, err
	}
	if includeRefs {
		var refsErr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			refsErr = p.attachTracePayloadRefs(ctx, rootID, ops, includeProof)
		}()
		wg.Wait()
		if refsErr != nil {
			return traceResponse{}, refsErr
		}
	}
	return traceResponse{RootID: rootID, Ops: ops}, nil
}

// loadTraceOps reads every op of every session sharing root_session_id = rootID,
// joined to its session (for the agent/kind tags) and turn (for turn_seq). The
// join supplies session_id / session_agent_name / session_kind directly, so the
// client can color and filter by sub-agent without a second round-trip. Ordered
// by (session start, session id, op start, op seq, op id) for a deterministic
// feed the client's per-session tree builder consumes. The cursor is fully
// drained before return.
func (p *Presenter) loadTraceOps(ctx context.Context, rootID string) ([]traceOp, error) {
	q := `
SELECT o.id, t.seq, o.kind, o.name,
       o.parent_op_id, o.start_ts, o.end_ts, o.duration_us, o.status,
       o.error_class, o.error_message, o.child_session_id,
       o.session_id, IFNULL(s.agent_name, ''), IFNULL(s.kind, '')
FROM ops o
JOIN sessions s ON s.id = o.session_id
LEFT JOIN turns t ON t.id = o.turn_id
WHERE s.root_session_id = ?
ORDER BY s.start_ts ASC, s.id ASC, o.start_ts ASC, o.seq ASC, o.id ASC`
	rows, err := p.db.QueryContext(ctx, q, rootID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]traceOp, 0, 64)
	for rows.Next() {
		var (
			op         traceOp
			parentID   sql.NullString
			endTS      sql.NullInt64
			duration   sql.NullInt64
			errClass   sql.NullString
			errMessage sql.NullString
			childID    sql.NullString
		)
		if err := rows.Scan(&op.ID, &op.TurnSeq, &op.Kind, &op.Name,
			&parentID, &op.StartTS, &endTS, &duration, &op.Status,
			&errClass, &errMessage, &childID,
			&op.SessionID, &op.SessionAgent, &op.SessionKind); err != nil {
			return nil, err
		}
		if parentID.Valid {
			v := parentID.String
			op.ParentOpID = &v
		}
		if endTS.Valid {
			v := endTS.Int64
			op.EndTS = &v
		}
		if duration.Valid {
			v := duration.Int64
			op.DurationUS = &v
		}
		if errClass.Valid {
			v := errClass.String
			op.ErrorClass = &v
		}
		if errMessage.Valid {
			v := errMessage.String
			op.ErrorMessage = &v
		}
		if childID.Valid {
			v := childID.String
			op.ChildSessionID = &v
		}
		op.PayloadRefs = []payloadRef{}
		out = append(out, op)
	}
	return out, rows.Err()
}

// attachTracePayloadRefs reads every payload_ref for every op in the tree (one
// query joined on the tree's sessions) and appends each to its op by id. ops is
// keyed into a map first so the attachment is O(refs) not O(ops×refs). The
// cursor is fully drained before return.
func (p *Presenter) attachTracePayloadRefs(ctx context.Context, rootID string, ops []traceOp, includeProof bool) error {
	byID := make(map[string]int, len(ops))
	for i := range ops {
		byID[ops[i].ID] = i
	}
	q := `
SELECT pr.id, pr.op_id, pr.kind, pr.format, pr.compression,
       pr.original_bytes, pr.stored_bytes, pr.location_uri, pr.sha256
FROM payload_refs pr
JOIN ops o ON o.id = pr.op_id
JOIN sessions s ON s.id = o.session_id
WHERE s.root_session_id = ?
ORDER BY pr.op_id ASC, pr.id ASC`
	rows, err := p.db.QueryContext(ctx, q, rootID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			pr          payloadRef
			opID        string
			compression sql.NullString
			origBytes   sql.NullInt64
			storedBytes sql.NullInt64
			locationURI string
			sha256      sql.NullString
		)
		if err := rows.Scan(&pr.ID, &opID, &pr.Kind, &pr.Format, &compression,
			&origBytes, &storedBytes, &locationURI, &sha256); err != nil {
			return err
		}
		pr.OpID = opID
		applyPayloadRefScalars(&pr, compression, origBytes, storedBytes, locationURI, sha256, includeProof)
		if idx, ok := byID[opID]; ok {
			ops[idx].PayloadRefs = append(ops[idx].PayloadRefs, pr)
		}
	}
	return rows.Err()
}
