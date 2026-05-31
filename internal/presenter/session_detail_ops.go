package presenter

import (
	"context"
	"database/sql"
)

// opLoc locates an op inside the turns tree: turn index + op index within
// that turn's Ops slice. payload_refs are attached by looking the op id up
// in this map and indexing back into the shared turns slice.
type opLoc struct {
	turnIdx int
	opIdx   int
}

// loadTurnsWithOps assembles the turns→ops→payload_refs tree for a
// session in three bounded queries (turns, all ops, all payload_refs for
// those ops) and groups them in Go. This avoids the N+1 query pattern a
// per-turn / per-op fetch would create. Turns are ordered by seq; ops by
// (turn_id, seq); payload_refs by (op_id, id) (insertion order).
func (p *Presenter) loadTurnsWithOps(ctx context.Context, sessionID string) ([]turnDetail, error) {
	turns, turnIndex, err := p.loadTurns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return turns, nil
	}

	opIndex, err := p.loadOps(ctx, sessionID, turns, turnIndex)
	if err != nil {
		return nil, err
	}
	if err := p.attachPayloadRefs(ctx, sessionID, turns, opIndex); err != nil {
		return nil, err
	}
	return turns, nil
}

// loadTurns reads the turns for a session ordered by seq and returns the
// slice plus a turn_id → slice-index map for op grouping.
func (p *Presenter) loadTurns(ctx context.Context, sessionID string) ([]turnDetail, map[string]int, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, seq, start_ts, end_ts, status, tokens_in, tokens_out, cost_usd, op_count
FROM turns WHERE session_id = ? ORDER BY seq ASC, id ASC`, sessionID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	turns := make([]turnDetail, 0, 8)
	index := map[string]int{}
	for rows.Next() {
		var (
			td    turnDetail
			endTS sql.NullInt64
		)
		if err := rows.Scan(&td.ID, &td.Seq, &td.StartTS, &endTS, &td.Status,
			&td.TokensIn, &td.TokensOut, &td.CostUSD, &td.OpCount); err != nil {
			return nil, nil, err
		}
		if endTS.Valid {
			v := endTS.Int64
			td.EndTS = &v
		}
		td.Ops = []opDetail{}
		index[td.ID] = len(turns)
		turns = append(turns, td)
	}
	return turns, index, rows.Err()
}

// loadOps reads every op for the session ordered by (turn_id, seq),
// appends each into its turn's Ops slice, and returns an op-id → opLoc
// map so attachPayloadRefs can hang refs off the right op. An op whose
// turn is not in the index (should not happen given the FK) is skipped
// defensively.
func (p *Presenter) loadOps(ctx context.Context, sessionID string, turns []turnDetail, turnIndex map[string]int) (map[string]opLoc, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, turn_id, parent_op_id, kind, name, IFNULL(model, ''), IFNULL(provider, ''),
       start_ts, end_ts, duration_us, status, error_class,
       tokens_in, tokens_out, cost_usd, ctx_used, ctx_max, child_session_id
FROM ops WHERE session_id = ? ORDER BY turn_id ASC, seq ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	opIndex := map[string]opLoc{}
	for rows.Next() {
		var (
			op       opDetail
			turnID   string
			parentID sql.NullString
			endTS    sql.NullInt64
			duration sql.NullInt64
			errClass sql.NullString
			ctxUsed  sql.NullInt64
			ctxMax   sql.NullInt64
			childID  sql.NullString
		)
		if err := rows.Scan(&op.ID, &turnID, &parentID, &op.Kind, &op.Name, &op.Model, &op.Provider,
			&op.StartTS, &endTS, &duration, &op.Status, &errClass,
			&op.TokensIn, &op.TokensOut, &op.CostUSD, &ctxUsed, &ctxMax, &childID); err != nil {
			return nil, err
		}
		fillOpNullables(&op, parentID, endTS, duration, errClass, ctxUsed, ctxMax, childID)
		op.PayloadRefs = []payloadRef{}
		ti, ok := turnIndex[turnID]
		if !ok {
			continue
		}
		turns[ti].Ops = append(turns[ti].Ops, op)
		opIndex[op.ID] = opLoc{turnIdx: ti, opIdx: len(turns[ti].Ops) - 1}
	}
	return opIndex, rows.Err()
}

// fillOpNullables copies the scanned sql.Null* values into the opDetail
// pointer fields. Extracted so loadOps stays within the function-length
// budget.
func fillOpNullables(op *opDetail, parentID sql.NullString, endTS, duration sql.NullInt64, errClass sql.NullString, ctxUsed, ctxMax sql.NullInt64, childID sql.NullString) {
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
	if ctxUsed.Valid {
		v := ctxUsed.Int64
		op.CtxUsed = &v
	}
	if ctxMax.Valid {
		v := ctxMax.Int64
		op.CtxMax = &v
	}
	if childID.Valid {
		v := childID.String
		op.ChildSessionID = &v
	}
}

// attachPayloadRefs reads every payload_ref for the session's ops in one
// query (joined on ops.session_id) and appends each to its op via the
// opIndex map, indexing back into the shared turns slice. Only the ref
// metadata is surfaced; the byte-streaming route (GET /api/payloads/<id>) is
// Phase 2 and unregistered, so no url is built here (rest-api.md §GET
// /api/payloads).
func (p *Presenter) attachPayloadRefs(ctx context.Context, sessionID string, turns []turnDetail, opIndex map[string]opLoc) error {
	rows, err := p.db.QueryContext(ctx, `
SELECT pr.id, pr.op_id, pr.kind, pr.format, pr.compression,
       pr.original_bytes, pr.stored_bytes
FROM payload_refs pr
JOIN ops o ON o.id = pr.op_id
WHERE o.session_id = ?
ORDER BY pr.op_id ASC, pr.id ASC`, sessionID)
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
		)
		if err := rows.Scan(&pr.ID, &opID, &pr.Kind, &pr.Format, &compression,
			&origBytes, &storedBytes); err != nil {
			return err
		}
		if compression.Valid {
			v := compression.String
			pr.Compression = &v
		}
		if origBytes.Valid {
			v := origBytes.Int64
			pr.OriginalBytes = &v
		}
		if storedBytes.Valid {
			v := storedBytes.Int64
			pr.StoredBytes = &v
		}
		loc, ok := opIndex[opID]
		if !ok {
			continue
		}
		op := &turns[loc.turnIdx].Ops[loc.opIdx]
		op.PayloadRefs = append(op.PayloadRefs, pr)
	}
	return rows.Err()
}
