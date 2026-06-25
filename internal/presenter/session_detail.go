package presenter

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
)

// sessionDetail is the full session row returned by GET
// /api/sessions/:id. It carries the same scalar fields as the list item
// plus the columns the detail view surfaces (provider, error, cache
// tokens). The computed children list lives at the response top level
// (child_sessions), not nested here, matching rest-api.md.
//
// `effective_status` (SOW-0089 chunk 5a) is the derived status from
// deriveEffectiveStatus — same shape as `status` but NEVER returns
// "running" once the session has gone idle (see session_status.go for the
// rules). The frontend prefers `effective_status` over `status` for any
// UX decision; the persisted `status` is kept for backwards-compat + raw
// source reporting.
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
	ProviderAlias     *string `json:"provider_alias,omitempty"`
	Cwd               *string `json:"cwd,omitempty"`
	CallPath          *string `json:"call_path,omitempty"`
	Status            string  `json:"status"`
	EffectiveStatus   string  `json:"effective_status"`
	ErrorClass        *string `json:"error_class"`
	ErrorMessage      *string `json:"error_message"`
	StartTS           int64   `json:"start_ts"`
	EndTS             *int64  `json:"end_ts"`
	LastActivityTS    *int64  `json:"last_activity_ts"`
	DurationUS        *int64  `json:"duration_us,omitempty"`
	FirstUserHash     *string `json:"first_user_message_hash,omitempty"`
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
	ID               string     `json:"id"`
	Seq              int64      `json:"seq"`
	StartTS          int64      `json:"start_ts"`
	EndTS            *int64     `json:"end_ts"`
	Status           string     `json:"status"`
	ErrorClass       *string    `json:"error_class,omitempty"`
	TokensIn         int64      `json:"tokens_in"`
	TokensOut        int64      `json:"tokens_out"`
	TokensCacheRead  int64      `json:"tokens_cache_read"`
	TokensCacheWrite int64      `json:"tokens_cache_write"`
	CostUSD          float64    `json:"cost_usd"`
	OpCount          int64      `json:"op_count"`
	Ops              []opDetail `json:"ops"`
}

// opDetail is one ops row plus its payload_refs. ParentOpID is the canonical
// id of the op this op nests under (ops.parent_op_id) — NULL for a top-level
// op. The Trace view uses it to rebuild the authoritative span tree from the
// stored parentage the ingest writer records (rest-api.md §GET /api/sessions/:id).
type opDetail struct {
	ID               string       `json:"id"`
	Kind             string       `json:"kind"`
	Name             string       `json:"name"`
	Model            string       `json:"model"`
	Provider         string       `json:"provider"`
	ToolNamespace    *string      `json:"tool_namespace,omitempty"`
	ProviderAlias    *string      `json:"provider_alias,omitempty"`
	ReasoningKind    *string      `json:"reasoning_kind,omitempty"`
	ParentOpID       *string      `json:"parent_op_id"`
	StartTS          int64        `json:"start_ts"`
	EndTS            *int64       `json:"end_ts"`
	DurationUS       *int64       `json:"duration_us"`
	Status           string       `json:"status"`
	ErrorClass       *string      `json:"error_class"`
	ErrorMessage     *string      `json:"error_message"`
	TokensIn         int64        `json:"tokens_in"`
	TokensOut        int64        `json:"tokens_out"`
	TokensCacheRead  int64        `json:"tokens_cache_read"`
	TokensCacheWrite int64        `json:"tokens_cache_write"`
	CostUSD          float64      `json:"cost_usd"`
	BytesIn          int64        `json:"bytes_in"`
	BytesOut         int64        `json:"bytes_out"`
	CharsIn          *int64       `json:"chars_in,omitempty"`
	CharsOut         *int64       `json:"chars_out,omitempty"`
	CtxUsed          *int64       `json:"ctx_used"`
	CtxMax           *int64       `json:"ctx_max"`
	ChildSessionID   *string      `json:"child_session_id"`
	PayloadRefs      []payloadRef `json:"payload_refs,omitempty"`
}

// payloadRef is one payload_refs row. The byte-streaming route
// (GET /api/payloads/<id>, SOW-0033) IS registered, but session-detail does not
// advertise it: the row carries only metadata and no `url` field (the Trace
// drawer's Preview button constructs the URL client-side from the ref id).
type payloadRef struct {
	ID            int64   `json:"id"`
	OpID          string  `json:"op_id"`
	Kind          string  `json:"kind"`
	ArtifactClass string  `json:"artifact_class"`
	Format        string  `json:"format"`
	Compression   *string `json:"compression"`
	OriginalBytes *int64  `json:"original_bytes"`
	StoredBytes   *int64  `json:"stored_bytes"`
	LocationURI   *string `json:"location_uri,omitempty"`
	SHA256        *string `json:"sha256,omitempty"`
}

// childSummary is one node in the child_sessions tree. SOW-0069: child_sessions
// is a NESTED tree — each child carries its own descendants in Children (down
// to leaves), resolved server-side by loadChildTree, so the Overview can render
// the full execution tree (parent → children → grandchildren).
type childSummary struct {
	ID           string         `json:"id"`
	NativeID     string         `json:"native_id"`
	Kind         string         `json:"kind"`
	AgentName    string         `json:"agent_name"`
	Model        string         `json:"model"`
	Provider     string         `json:"provider"`
	Status       string         `json:"status"`
	ErrorClass   *string        `json:"error_class,omitempty"`
	StartTS      int64          `json:"start_ts"`
	EndTS        *int64         `json:"end_ts"`
	TokensIn     int64          `json:"tokens_in"`
	TokensOut    int64          `json:"tokens_out"`
	CostUSD      float64        `json:"cost_usd"`
	OpCount      int64          `json:"op_count"`
	FailureCount int64          `json:"failure_count"`
	Children     []childSummary `json:"child_sessions,omitempty"`
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
		p.writeSessionMethodNotAllowed(w, r)
		return
	}
	id, ok := p.sessionIDFromPath(w, r)
	if !ok {
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	includes, err := parseIncludeOptions(r.URL.Query().Get("include"), includeAllow("payload_refs", "proof"))
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	if err := requireProofPayloadRefs(includes); err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	resp, op, err := p.loadSessionDetailResponse(ctx, id, includes.PayloadRefs, includes.Proof)
	if isNoRows(err) {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "session not found", map[string]any{"id": id})
		return
	}
	if err != nil {
		p.writeDBError(ctx, w, r, op, err)
		return
	}

	writeJSON(w, r, p.logger, resp)
}

func (p *Presenter) writeSessionMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
		CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
}

func (p *Presenter) loadSessionDetailResponse(ctx context.Context, id string, includeRefs bool, includeProof bool) (sessionDetailResponse, string, error) {
	// Fan out the 3 sub-queries in parallel on the 8-connection reader
	// pool. The dominant query is loadTurnsWithOps (turns + ops can be
	// ~600ms on a 7k-op session), so running loadSession + loadChildTree
	// concurrently hides their latency behind it.
	type sessResult struct {
		sess sessionDetail
		err  error
	}
	type turnsResult struct {
		turns []turnDetail
		err   error
	}
	type childrenResult struct {
		children []childSummary
		err      error
	}
	var sr sessResult
	var tr turnsResult
	var cr childrenResult
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		sr.sess, sr.err = p.loadSession(ctx, id)
	}()
	go func() {
		defer wg.Done()
		tr.turns, tr.err = p.loadTurnsWithOps(ctx, id, includeRefs, includeProof)
	}()
	go func() {
		defer wg.Done()
		cr.children, cr.err = p.loadChildTree(ctx, id)
	}()
	wg.Wait()
	if sr.err != nil {
		return sessionDetailResponse{}, "session.detail.session", sr.err
	}
	if tr.err != nil {
		return sessionDetailResponse{}, "session.detail.turns", tr.err
	}
	if cr.err != nil {
		return sessionDetailResponse{}, "session.detail.children", cr.err
	}
	return sessionDetailResponse{
		Session:       sr.sess,
		Turns:         tr.turns,
		ChildSessions: cr.children,
	}, "", nil
}

// loadSession reads the single sessions row. Returns sql.ErrNoRows when
// the id is unknown so the handler can map it to 404.
func (p *Presenter) loadSession(ctx context.Context, id string) (sessionDetail, error) {
	var (
		s         sessionDetail
		parent    sql.NullString
		alias     sql.NullString
		cwd       sql.NullString
		callPath  sql.NullString
		errClass  sql.NullString
		errMsg    sql.NullString
		endTS     sql.NullInt64
		lastActTS sql.NullInt64
		duration  sql.NullInt64
		firstHash sql.NullString
	)
	err := p.db.QueryRowContext(ctx, `
SELECT
    id, native_id, root_session_id, parent_session_id, source_id, kind,
    IFNULL(agent_name, ''), IFNULL(model, ''), IFNULL(provider, ''),
    provider_alias, cwd, call_path,
    status, error_class, error_message, start_ts, end_ts, last_activity_ts, duration_us, first_user_message_hash,
    tokens_in, tokens_out,
    tokens_cache_read, tokens_cache_write, cost_usd,
    turn_count, op_count, failure_count,
    (SELECT COUNT(*) FROM sessions c WHERE c.parent_session_id = sessions.id)
FROM sessions WHERE id = ?`, id).Scan(
		&s.ID, &s.NativeID, &s.RootSessionID, &parent, &s.SourceID, &s.Kind,
		&s.AgentName, &s.Model, &s.Provider, &alias, &cwd, &callPath, &s.Status, &errClass, &errMsg,
		&s.StartTS, &endTS, &lastActTS, &duration, &firstHash, &s.TokensIn, &s.TokensOut,
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
	if errMsg.Valid {
		v := errMsg.String
		s.ErrorMessage = &v
	}
	if alias.Valid {
		v := alias.String
		s.ProviderAlias = &v
	}
	if cwd.Valid {
		v := cwd.String
		s.Cwd = &v
	}
	if callPath.Valid {
		v := callPath.String
		s.CallPath = &v
	}
	if endTS.Valid {
		v := endTS.Int64
		s.EndTS = &v
	}
	if lastActTS.Valid {
		v := lastActTS.Int64
		s.LastActivityTS = &v
	}
	if duration.Valid {
		v := duration.Int64
		s.DurationUS = &v
	}
	if firstHash.Valid {
		v := firstHash.String
		s.FirstUserHash = &v
	}
	// SOW-0089 chunk 5a: derive the operator-facing status from the snapshot
	// + freshness signals. Done on every read so a session that the watcher
	// reports as "running" but the file has gone stale on (most codex
	// sessions) flips to "stale" without a re-ingest.
	s.EffectiveStatus = string(deriveEffectiveStatus(
		s.Status,
		endTS.Int64,
		lastActTS.Int64,
		p.now().UnixMicro(),
	))
	return s, nil
}

// childTreeNode is the internal pointer-based node used to build the child
// sessions tree (loadChildTree). Pointer nesting lets a child's subtree
// complete before the child value is copied into its parent (Go value
// semantics would otherwise freeze a stale, child-less copy).
type childTreeNode struct {
	summary childSummary
	parent  string
	kids    []*childTreeNode
}

// copyChildTree converts a pointer node tree into the by-value childSummary
// tree the API emits. Each node's Children are fully resolved before the node
// itself is copied into its parent, so no stale (child-less) value survives.
func copyChildTree(n *childTreeNode) childSummary {
	out := n.summary
	if len(n.kids) > 0 {
		out.Children = make([]childSummary, 0, len(n.kids))
		for _, k := range n.kids {
			out.Children = append(out.Children, copyChildTree(k))
		}
	}
	return out
}

// childTreeMaxDepth caps the recursive descendant fetch (SOW-0069). Session
// trees are naturally shallow (typically 2-4 levels); the cap is cycle
// defense-in-depth against malformed parent_session_id data and bounds the
// nested payload size.
const childTreeMaxDepth = 20

// loadChildTree resolves the FULL descendant tree rooted at id's direct children
// (SOW-0069): one recursive CTE fetches every descendant (depth-capped), then
// the flat rows are nested in Go by parent_session_id. Returns the list of
// direct-children trees (each carrying its own nested Children). A leaf parent
// yields an empty (non-nil) slice.
func (p *Presenter) loadChildTree(ctx context.Context, id string) ([]childSummary, error) {
	// Anchor on the direct children (parent = id), recurse down via
	// parent_session_id. depth starts at 1 (direct child) and the WHERE
	// depth <= childTreeMaxDepth caps the traversal.
	//
	// GOTCHA: writing this as `FROM sessions s JOIN descendants d ON s.id = d.id`
	// makes the SQLite planner choose `SCAN s USING INDEX idx_sessions_start`
	// (530k rows on the production DB) as the driving table and filter against
	// the small descendants set, costing ~1.2s per request even when the queried
	// id has no descendants (404 path). Rewriting the join as
	// `WHERE s.id IN (SELECT id FROM descendants)` forces the planner to
	// execute the recursive CTE first and then look up each descendant by PK
	// (an index hit per descendant). Verified by EXPLAIN QUERY PLAN: the
	// IN-form drops from SCAN s (1.1-1.3s) to SEARCH s USING PRIMARY KEY
	// (a handful of microseconds).
	rows, err := p.db.QueryContext(ctx, `
WITH RECURSIVE descendants(id, parent, depth) AS (
    SELECT id, parent_session_id, 1 FROM sessions WHERE parent_session_id = ?
    UNION ALL
    SELECT s.id, s.parent_session_id, d.depth + 1
    FROM sessions s JOIN descendants d ON s.parent_session_id = d.id
    WHERE d.depth < ?
)
SELECT s.id, s.native_id, s.kind, IFNULL(s.agent_name, ''), IFNULL(s.model, ''),
       IFNULL(s.provider, ''), s.status, s.error_class, s.start_ts, s.end_ts, s.tokens_in, s.tokens_out, s.cost_usd,
       s.op_count, s.failure_count,
       (SELECT parent FROM descendants WHERE id = s.id) AS parent
FROM sessions s
WHERE s.id IN (SELECT id FROM descendants)
ORDER BY s.start_ts ASC, s.id ASC`, id, childTreeMaxDepth)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// First pass: read every descendant into a node keyed by id, and remember
	// each node's parent so the second pass can nest.
	type node struct {
		summary childSummary
		parent  string
	}
	byID := make(map[string]*node)
	var order []string // preserve first-seen (start_ts) order for stable child lists
	for rows.Next() {
		var (
			c        childSummary
			endTS    sql.NullInt64
			errClass sql.NullString
			parent   string
		)
		if err := rows.Scan(&c.ID, &c.NativeID, &c.Kind, &c.AgentName, &c.Model,
			&c.Provider, &c.Status, &errClass, &c.StartTS, &endTS, &c.TokensIn, &c.TokensOut, &c.CostUSD,
			&c.OpCount, &c.FailureCount, &parent); err != nil {
			return nil, err
		}
		if errClass.Valid {
			v := errClass.String
			c.ErrorClass = &v
		}
		if endTS.Valid {
			v := endTS.Int64
			c.EndTS = &v
		}
		// Defense-in-depth (SOW-0069): a session is never its own child. A
		// malformed parent_session_id cycle can otherwise surface the queried
		// id among its own descendants, nesting it as its own child. Skip it.
		if c.ID == id {
			continue
		}
		if _, ok := byID[c.ID]; !ok {
			byID[c.ID] = &node{summary: c, parent: parent}
			order = append(order, c.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Build the tree with pointers internally so a child's own subtree is
	// complete BEFORE the child value is copied into its parent's Children
	// (Go value semantics would otherwise freeze a stale, child-less copy).
	pnodes := make(map[string]*childTreeNode, len(order))
	for _, id := range order {
		pnodes[id] = &childTreeNode{summary: byID[id].summary, parent: byID[id].parent}
	}
	// Attach each node to its parent's kids (pointer append — propagates).
	var roots []*childTreeNode
	for _, id := range order {
		n := pnodes[id]
		if n.parent == id {
			continue // defensive: a self-parenting row (impossible) would loop
		}
		if parent, ok := pnodes[n.parent]; ok {
			parent.kids = append(parent.kids, n)
		} else {
			// Parent is the queried id (direct child) or otherwise not in the
			// descendant set (e.g. its branch was depth-capped) → root of the
			// returned forest.
			roots = append(roots, n)
		}
	}

	// Recursively copy the pointer tree into the value tree the JSON encoder
	// emits ([]childSummary by value). make already guarantees a non-nil slice.
	out := make([]childSummary, 0, len(roots))
	for _, r := range roots {
		out = append(out, copyChildTree(r))
	}
	return out, nil
}
