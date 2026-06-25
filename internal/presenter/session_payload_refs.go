// session_payload_refs.go (SOW-0092 chunk 3) — opt-in lazy endpoint for
// /api/sessions/:id/payload_refs. Lets the session-detail page ship the slim
// response (no payload_refs) by default, then fetch the refs in a separate
// request only when the operator focuses an op that needs payload previews.
//
// Why a separate endpoint (vs returning refs inline):
//   - The full ref set is ~1.5 refs/op × 7k ops = 11k rows (~1 MB JSON
//     before gzip). The TurnView only renders refs for the focused turn
//     (≤ ~50 refs in practice). Fetching the full set just to use ~0.5%
//     is wasteful.
//   - Fetching the refs for a single op (or a single turn) on demand is
//     a much smaller query + response.
//   - The endpoint is shape-compatible with the existing payload_refs
//     field on each op (same JSON, same fields), so the client can
//     splice fetched refs into op.payload_refs without any transformation.
//
// Query parameters:
//   - ?op=<id>      → refs for ONE op (tiny response, <1 KB)
//   - ?turn=<id>    → refs for ALL ops in ONE turn (small, ~5-50 refs)
//   - neither       → refs for ALL ops in the session (matches the
//     include=payload_refs behavior of /api/sessions/:id; included
//     for completeness — clients can use ?include=payload_refs on
//     the main endpoint instead and skip this call)
//
// The single-op and single-turn paths use indexed lookups (ops.id,
// turns.id, payload_refs.op_id); the no-filter path uses the same full
// scan as attachPayloadRefs. Both are bounded by payload_refs rows, not
// ops rows, so the cost scales with the ref count, not the session size.

package presenter

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

// handleSessionPayloadRefs answers GET /api/sessions/:id/payload_refs.
// Supports ?op=<id> and ?turn=<id> filters (mutually exclusive).
func (p *Presenter) handleSessionPayloadRefs(w http.ResponseWriter, r *http.Request) {
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

	q := r.URL.Query()
	opID := q.Get("op")
	turnID := q.Get("turn")
	if opID != "" && turnID != "" {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			"bad_request", "specify at most one of ?op or ?turn", nil)
		return
	}
	includes, err := parseIncludeOptions(q.Get("include"), includeAllow("payload_refs", "proof"))
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	refs, err := p.loadPayloadRefs(ctx, id, opID, turnID, includes.Proof)
	if err != nil {
		p.writeDBError(ctx, w, r, "session.payload_refs", err)
		return
	}

	writeJSON(w, r, p.logger, struct {
		Refs []payloadRef `json:"refs"`
	}{Refs: refs})
}

// loadPayloadRefs fetches the payload_refs rows for a session, optionally
// scoped to a single op or turn. Always non-nil (empty slice when no
// matches, marshalled as `[]` not null).
func (p *Presenter) loadPayloadRefs(ctx context.Context, sessionID, opID, turnID string, includeProof bool) ([]payloadRef, error) {
	var (
		q    string
		args []any
	)
	switch {
	case opID != "":
		// Single-op lookup. Indexed on (op_id, id).
		q = `
SELECT pr.id, pr.op_id, pr.kind, pr.format, pr.compression,
       pr.original_bytes, pr.stored_bytes, pr.location_uri, pr.sha256
FROM payload_refs pr
JOIN ops o ON o.id = pr.op_id
WHERE o.session_id = ? AND pr.op_id = ?
ORDER BY pr.op_id ASC, pr.id ASC`
		args = []any{sessionID, opID}
	case turnID != "":
		// Single-turn lookup. Filter by turn_id; ordered by op then ref id
		// to match the inline payload_refs ordering on each op.
		q = `
SELECT pr.id, pr.op_id, pr.kind, pr.format, pr.compression,
       pr.original_bytes, pr.stored_bytes, pr.location_uri, pr.sha256
FROM payload_refs pr
JOIN ops o ON o.id = pr.op_id
WHERE o.session_id = ? AND o.turn_id = ?
ORDER BY pr.op_id ASC, pr.id ASC`
		args = []any{sessionID, turnID}
	default:
		// Full-session fetch — same scope as ?include=payload_refs on the
		// session detail endpoint. Provided for completeness; most callers
		// should use the inline include flag instead.
		q = `
SELECT pr.id, pr.op_id, pr.kind, pr.format, pr.compression,
       pr.original_bytes, pr.stored_bytes, pr.location_uri, pr.sha256
FROM payload_refs pr
JOIN ops o ON o.id = pr.op_id
WHERE o.session_id = ?
ORDER BY pr.op_id ASC, pr.id ASC`
		args = []any{sessionID}
	}

	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load payload_refs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]payloadRef, 0)
	for rows.Next() {
		var (
			pr          payloadRef
			compression sql.NullString
			origBytes   sql.NullInt64
			storedBytes sql.NullInt64
			locationURI string
			sha256      sql.NullString
		)
		if err := rows.Scan(&pr.ID, &pr.OpID, &pr.Kind, &pr.Format, &compression,
			&origBytes, &storedBytes, &locationURI, &sha256); err != nil {
			return nil, err
		}
		applyPayloadRefScalars(&pr, compression, origBytes, storedBytes, locationURI, sha256, includeProof)
		out = append(out, pr)
	}
	return out, rows.Err()
}
