package presenter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// compareSessionMaxIDs is the upper bound on the number of ids the
// /api/sessions/compare endpoint accepts in v1. Keeping the cap at 4
// makes the response size predictable (4 × summary + 4 diff dimensions
// × per-session breakdown) and the UI renderable in a single row of
// cards. Lifting the cap is a v2 task; the wire format is the only
// change.
const compareSessionMaxIDs = 4

// compareMinIDs is the lower bound. 1-id comparisons are not
// meaningful (no diff) and 0-id requests are malformed.
const compareMinIDs = 2

// compareResponse is the JSON shape of GET /api/sessions/compare.
// See rest-api.md §"GET /api/sessions/compare" for the full schema.
type compareResponse struct {
	Sessions         []sessionListItem             `json:"sessions"`
	Summary          compareSummary                `json:"summary"`
	ToolUsage        compareToolBucket             `json:"tool_usage"`
	Errors           compareErrorBucket            `json:"errors"`
	Models           compareStringBucket           `json:"models"`
	Agents           compareStringBucket           `json:"agents"`
	KindDistribution compareKindDistributionBucket `json:"kind_distribution"`
}

// compareSummary is the per-metric best/worst/per-session block. The
// directional metrics (duration_us, cost_usd) populate Best and Worst;
// the neutral metrics (op_count, tokens) leave them nil and the
// client decides what "best" means.
type compareSummary struct {
	DurationUS compareMetricInt   `json:"duration_us"`
	CostUSD    compareMetricFloat `json:"cost_usd"`
	OpCount    compareMetricInt   `json:"op_count"`
	Tokens     compareMetricInt   `json:"tokens"`
}

// compareMetricInt is the per-metric block when the metric is integer-
// valued. Best/Worst are pointers so they can be omitted in JSON when
// the metric is neutral (no winner).
type compareMetricInt struct {
	Best       *string          `json:"best,omitempty"`
	Worst      *string          `json:"worst,omitempty"`
	PerSession map[string]int64 `json:"per_session"`
}

// compareMetricFloat is the float-valued variant (cost_usd is a
// float64 in the schema).
type compareMetricFloat struct {
	Best       *string            `json:"best,omitempty"`
	Worst      *string            `json:"worst,omitempty"`
	PerSession map[string]float64 `json:"per_session"`
}

// compareToolBucket is the per-tool histogram + the added/removed
// diffs. Common is sorted lexicographically for stable output.
type compareToolBucket struct {
	Common     []string                    `json:"common"`
	Added      map[string][]string         `json:"added"`
	Removed    map[string][]string         `json:"removed"`
	PerSession map[string]map[string]int64 `json:"per_session"`
}

// compareErrorBucket groups errors by their presence across sessions.
// Common is sorted by started_at_us ASC; OnlyIn groups are sorted
// within each bucket by the same key.
type compareErrorBucket struct {
	Common []opErrorRef            `json:"common"`
	OnlyIn map[string][]opErrorRef `json:"only_in"`
}

// opErrorRef is a compact reference to a failed op, suitable for the
// compare endpoint's per-session error list. It carries enough
// context to identify the op without re-querying the session.
type opErrorRef struct {
	OpID        string `json:"op_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ErrorClass  string `json:"error_class"`
	StartedAtUS int64  `json:"started_at_us"`
}

// compareStringBucket is the simple {shared, diverged} structure for
// string-valued fields (model, agent_name). Diverged maps session_id
// to its unique value(s); for a typical session, that's exactly one
// value, but the list form is forward-compatible with sessions that
// have multiple models / agents.
type compareStringBucket struct {
	Shared   []string            `json:"shared"`
	Diverged map[string][]string `json:"diverged"`
}

// compareKindDistributionBucket is the per-session kind histogram.
// PerSession[id] is a kind -> count map; absent kinds are reported as
// zero in the client (the JSON omits them to keep the payload small).
type compareKindDistributionBucket struct {
	PerSession map[string]map[string]int64 `json:"per_session"`
}

// handleCompareSessions answers GET /api/sessions/compare?ids=<csv>.
// Returns a structured diff between 2 and 4 sessions. See rest-api.md
// §"GET /api/sessions/compare" for the wire contract.
func (p *Presenter) handleCompareSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		// OPTIONS preflight handled by the global middleware; this
		// guard is here only to make the routing table self-documenting.
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"only GET is allowed on /api/sessions/compare", nil)
		return
	}

	ids, err := parseCompareIDs(r.URL.Query().Get("ids"))
	if err != nil {
		writeJSONError(w, r, p.logger, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	if len(ids) < compareMinIDs {
		writeJSONError(w, r, p.logger, http.StatusBadRequest, CodeBadRequest,
			fmt.Sprintf("at least %d ids required; got %d", compareMinIDs, len(ids)), nil)
		return
	}
	if len(ids) > compareSessionMaxIDs {
		writeJSONError(w, r, p.logger, http.StatusBadRequest, CodeBadRequest,
			fmt.Sprintf("at most %d ids allowed; got %d", compareSessionMaxIDs, len(ids)), nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	nowUs := p.now().UnixMicro()
	resp, status, code, err := buildCompareResponse(ctx, p.db, ids, nowUs)
	if err != nil {
		writeJSONError(w, r, p.logger, status, code, err.Error(), nil)
		return
	}
	writeJSON(w, r, p.logger, resp)
}

// parseCompareIDs splits a CSV of session ids and trims whitespace.
// Empty entries ("a,,b") are dropped with a 400. The returned slice
// preserves the request order — compare.go relies on it for column
// alignment in the UI.
func parseCompareIDs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("ids parameter is required")
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id == "" {
			return nil, errors.New("ids parameter contains an empty id")
		}
		out = append(out, id)
	}
	return out, nil
}

// buildCompareResponse loads the per-session summary + the diff
// dimensions in parallel. Errors from any dimension surface as a 500
// (the only legitimate non-200 path from this function is 404 for an
// unknown id; everything else is a server fault).
//
// `nowUs` is the wall clock to use when computing derived fields
// (effective_status in scanSessionListItem). It MUST be consistent
// across the response so sessions crossing the stale threshold
// mid-comparison report the same status on every row.
func buildCompareResponse(ctx context.Context, db *sql.DB, ids []string, nowUs int64) (compareResponse, int, string, error) {
	sessions, err := loadCompareSessionSummaries(ctx, db, ids, nowUs)
	if err != nil {
		return compareResponse{}, http.StatusInternalServerError, "DB_ERROR", err
	}
	// Validate that every requested id was found. The SQL filter
	// silently drops unknown ids; we must catch that and 404 instead.
	if len(sessions) != len(ids) {
		found := make(map[string]struct{}, len(sessions))
		for _, s := range sessions {
			found[s.ID] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				return compareResponse{}, http.StatusNotFound, CodeNotFound,
					fmt.Errorf("session not found: %s", id)
			}
		}
	}
	// Each diff dimension is one SQL query; run them serially for now
	// (parallelizing saves ~5ms on the largest sessions; the
	// simplification is worth it). If a future SOW needs the perf, all
	// four are independent and can be fanned out via a sync.WaitGroup
	// (the same pattern used in /api/search).
	tools, err := loadCompareToolUsage(ctx, db, sessions)
	if err != nil {
		return compareResponse{}, http.StatusInternalServerError, "DB_ERROR", err
	}
	errs, err := loadCompareErrors(ctx, db, sessions)
	if err != nil {
		return compareResponse{}, http.StatusInternalServerError, "DB_ERROR", err
	}
	models := bucketStrings(sessions, func(s sessionListItem) string { return s.Model })
	agents := bucketStrings(sessions, func(s sessionListItem) string { return s.AgentName })
	kinds, err := loadCompareKindDistribution(ctx, db, sessions)
	if err != nil {
		return compareResponse{}, http.StatusInternalServerError, "DB_ERROR", err
	}
	summary := buildCompareSummary(sessions)

	return compareResponse{
		Sessions:         sessions,
		Summary:          summary,
		ToolUsage:        tools,
		Errors:           errs,
		Models:           models,
		Agents:           agents,
		KindDistribution: kinds,
	}, 0, "", nil
}

// loadCompareSessionSummaries runs the per-session summary query
// against the same SELECT clause /api/sessions uses, filtered by
// `id IN (?,?,...)`. The order in the response matches the request
// order (we re-sort by a position map because SQL with IN does not
// guarantee any particular order).
func loadCompareSessionSummaries(ctx context.Context, db *sql.DB, ids []string, nowUs int64) ([]sessionListItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// The SELECT clause mirrors buildSessionListQuery; the scan
	// (scanSessionListItem) is the SAME function /api/sessions uses, so
	// the two endpoints cannot drift on the per-session row shape.
	// Drift would be a contract bug — integration tests
	// (TestCompare_OrderPreserved, TestSessionsList_*) catch it.
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	// #nosec G202 -- static SQL + ?-placeholders; values bound via args below
	query := `
SELECT
    s.id, s.native_id, s.root_session_id, s.parent_session_id, s.source_id,
    s.kind, IFNULL(s.agent_name, ''), IFNULL(s.model, ''), s.status,
    s.start_ts, s.end_ts, s.last_activity_ts,
    s.tokens_in, s.tokens_out, s.cost_usd,
    s.turn_count, s.op_count, s.failure_count,
    IFNULL(s.error_class, '') AS error_class,
    (SELECT COUNT(*) FROM sessions c WHERE c.parent_session_id = s.id) AS child_session_count
FROM sessions s
WHERE s.id IN (` + placeholders + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, query, args...) // #nosec G202 -- static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, fmt.Errorf("loadCompareSessionSummaries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byID := make(map[string]sessionListItem, len(ids))
	for rows.Next() {
		item, err := scanSessionListItem(rows, nowUs)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		byID[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	// Re-emit in the request order so the compare page's column
	// alignment is deterministic. Missing ids are silently dropped here;
	// the caller (buildCompareResponse) catches the count mismatch and
	// 404s with the missing id.
	out := make([]sessionListItem, 0, len(ids))
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

// loadCompareToolUsage runs a single GROUP BY across the N sessions
// and pivots in Go. Output: per-session tool histograms; the
// intersection across all sessions is `common`; per-session diffs
// relative to the intersection are `added` / `removed`.
func loadCompareToolUsage(ctx context.Context, db *sql.DB, sessions []sessionListItem) (compareToolBucket, error) {
	out := compareToolBucket{
		Added:      make(map[string][]string, len(sessions)),
		Removed:    make(map[string][]string, len(sessions)),
		PerSession: make(map[string]map[string]int64, len(sessions)),
	}
	if len(sessions) == 0 {
		return out, nil
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, name, COUNT(*)
		FROM ops INDEXED BY idx_ops_session_start
		WHERE session_id IN (`+placeholders+`) AND kind = 'tool'
		GROUP BY session_id, name
		ORDER BY name, session_id`, // #nosec G202 -- static SQL + ?-placeholders; values bound via args
		toAny(ids)...)
	if err != nil {
		return out, fmt.Errorf("loadCompareToolUsage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// name -> session_id -> count
	pivot := make(map[string]map[string]int64)
	for rows.Next() {
		var sid, name string
		var count int64
		if err := rows.Scan(&sid, &name, &count); err != nil {
			return out, fmt.Errorf("scan: %w", err)
		}
		if _, ok := pivot[name]; !ok {
			pivot[name] = make(map[string]int64, len(sessions))
		}
		pivot[name][sid] = count
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("rows: %w", err)
	}

	// Per-session: every tool name that this session used, with count.
	for _, s := range sessions {
		hist := make(map[string]int64, len(pivot))
		for name, bySession := range pivot {
			if count, ok := bySession[s.ID]; ok {
				hist[name] = count
			}
		}
		out.PerSession[s.ID] = hist
	}
	// Common: tools that appear in EVERY session.
	common := make([]string, 0, len(pivot))
	for name, bySession := range pivot {
		if len(bySession) == len(sessions) {
			common = append(common, name)
		}
	}
	sort.Strings(common)
	out.Common = common
	// Added/Removed: per-session diff relative to the intersection.
	//   added[X]   = tools used by X but not by at least one other session
	//   removed[X] = tools used by some other session but not by X
	commonSet := make(map[string]struct{}, len(common))
	for _, n := range common {
		commonSet[n] = struct{}{}
	}
	for _, s := range sessions {
		var added, removed []string
		for name, bySession := range pivot {
			_, inX := bySession[s.ID]
			inCommon := false
			if _, ok := commonSet[name]; ok {
				inCommon = true
			}
			switch {
			case inX && !inCommon:
				added = append(added, name)
			case !inX && inCommon:
				removed = append(removed, name)
			}
		}
		sort.Strings(added)
		sort.Strings(removed)
		out.Added[s.ID] = added
		out.Removed[s.ID] = removed
	}
	return out, nil
}

// loadCompareErrors returns the per-session error buckets + the
// intersection. Only ops with a non-empty error_class are considered.
func loadCompareErrors(ctx context.Context, db *sql.DB, sessions []sessionListItem) (compareErrorBucket, error) {
	out := compareErrorBucket{
		OnlyIn: make(map[string][]opErrorRef, len(sessions)),
	}
	if len(sessions) == 0 {
		return out, nil
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	rows, err := db.QueryContext(ctx, `
		SELECT id, session_id, kind, name, error_class, start_ts
		FROM ops INDEXED BY idx_ops_session_start
		WHERE session_id IN (`+placeholders+`)
		  AND error_class IS NOT NULL AND error_class != '' AND error_class != 'none'
		ORDER BY session_id, start_ts`, // #nosec G202 -- static SQL + ?-placeholders; values bound via args
		toAny(ids)...)
	if err != nil {
		return out, fmt.Errorf("loadCompareErrors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// session_id -> []opErrorRef
	bySession := make(map[string][]opErrorRef, len(sessions))
	// composite key for "common" detection: error_class|kind|name|session_id
	// (we use per-session occurrence so the same error in two sessions
	// counts as two distinct refs that may or may not intersect)
	compositeBySession := make(map[string]map[string]struct{}, len(sessions))
	for rows.Next() {
		var ref opErrorRef
		var sid string
		if err := rows.Scan(&ref.OpID, &sid, &ref.Kind, &ref.Name, &ref.ErrorClass, &ref.StartedAtUS); err != nil {
			return out, fmt.Errorf("scan: %w", err)
		}
		bySession[sid] = append(bySession[sid], ref)
		composite := ref.ErrorClass + "|" + ref.Kind + "|" + ref.Name
		if _, ok := compositeBySession[sid]; !ok {
			compositeBySession[sid] = make(map[string]struct{})
		}
		compositeBySession[sid][composite] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("rows: %w", err)
	}

	// Common: composite keys present in EVERY session.
	var commonSet map[string]struct{}
	if len(sessions) > 0 {
		// Initialize with the first session's set, then intersect.
		first := sessions[0].ID
		commonSet = make(map[string]struct{}, len(compositeBySession[first]))
		for k := range compositeBySession[first] {
			commonSet[k] = struct{}{}
		}
		for _, s := range sessions[1:] {
			next := make(map[string]struct{}, len(compositeBySession[s.ID]))
			for k := range compositeBySession[s.ID] {
				if _, ok := commonSet[k]; ok {
					next[k] = struct{}{}
				}
			}
			commonSet = next
			if len(commonSet) == 0 {
				break
			}
		}
	}
	// Emit common refs by walking any session's slice (they all share
	// the same composite keys when in commonSet). Use the first
	// session that contributed a key; the refs are identical enough
	// (error_class + kind + name) that picking the first occurrence
	// gives a stable, informative representation.
	commonRefByKey := make(map[string]opErrorRef)
	for _, refs := range bySession {
		for _, r := range refs {
			composite := r.ErrorClass + "|" + r.Kind + "|" + r.Name
			if _, ok := commonSet[composite]; ok {
				if _, present := commonRefByKey[composite]; !present {
					commonRefByKey[composite] = r
				}
			}
		}
	}
	commonList := make([]opErrorRef, 0, len(commonRefByKey))
	for _, r := range commonRefByKey {
		commonList = append(commonList, r)
	}
	sort.Slice(commonList, func(i, j int) bool { return commonList[i].StartedAtUS < commonList[j].StartedAtUS })
	out.Common = commonList

	// OnlyIn: per-session refs whose composite is not in the common set.
	for _, s := range sessions {
		only := make([]opErrorRef, 0)
		for _, r := range bySession[s.ID] {
			composite := r.ErrorClass + "|" + r.Kind + "|" + r.Name
			if _, ok := commonSet[composite]; !ok {
				only = append(only, r)
			}
		}
		sort.Slice(only, func(i, j int) bool { return only[i].StartedAtUS < only[j].StartedAtUS })
		out.OnlyIn[s.ID] = only
	}
	return out, nil
}

// bucketStrings produces the {shared, diverged} structure for a
// string-valued field. The accessor maps each session to the field's
// value (e.g. s.Model, s.AgentName).
func bucketStrings(sessions []sessionListItem, accessor func(sessionListItem) string) compareStringBucket {
	out := compareStringBucket{
		Shared:   []string{},
		Diverged: make(map[string][]string, len(sessions)),
	}
	if len(sessions) == 0 {
		return out
	}
	// count occurrences; only "shared" entries have count == N.
	counts := make(map[string]int, len(sessions))
	for _, s := range sessions {
		v := accessor(s)
		if v == "" {
			continue
		}
		counts[v]++
	}
	shared := make([]string, 0, len(counts))
	for v, c := range counts {
		if c == len(sessions) {
			shared = append(shared, v)
		}
	}
	sort.Strings(shared)
	out.Shared = shared
	// Diverged: per-session values not in the shared set. Sessions
	// whose value is in the shared set are omitted (their value is
	// "common to all" and not interesting for the diverged view).
	sharedSet := make(map[string]struct{}, len(shared))
	for _, v := range shared {
		sharedSet[v] = struct{}{}
	}
	for _, s := range sessions {
		v := accessor(s)
		if v == "" {
			continue
		}
		if _, ok := sharedSet[v]; ok {
			continue
		}
		out.Diverged[s.ID] = append(out.Diverged[s.ID], v)
		sort.Strings(out.Diverged[s.ID])
	}
	return out
}

// loadCompareKindDistribution returns the per-session op-kind
// histogram. Absent kinds are reported as zero on the client (the
// JSON omits them to keep the payload compact).
func loadCompareKindDistribution(ctx context.Context, db *sql.DB, sessions []sessionListItem) (compareKindDistributionBucket, error) {
	out := compareKindDistributionBucket{
		PerSession: make(map[string]map[string]int64, len(sessions)),
	}
	if len(sessions) == 0 {
		return out, nil
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, kind, COUNT(*)
		FROM ops INDEXED BY idx_ops_session_start
		WHERE session_id IN (`+placeholders+`)
		GROUP BY session_id, kind
		ORDER BY session_id, kind`, // #nosec G202 -- static SQL + ?-placeholders; values bound via args
		toAny(ids)...)
	if err != nil {
		return out, fmt.Errorf("loadCompareKindDistribution: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sid, kind string
		var count int64
		if err := rows.Scan(&sid, &kind, &count); err != nil {
			return out, fmt.Errorf("scan: %w", err)
		}
		if _, ok := out.PerSession[sid]; !ok {
			out.PerSession[sid] = make(map[string]int64)
		}
		out.PerSession[sid][kind] = count
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// buildCompareSummary computes the per-metric best/worst/per-session
// block from the loaded session summaries. The directional rule:
// duration_us + cost_usd are "lower is better" (Best / Worst set);
// op_count + tokens are "neutral" (Best / Worst omitted; client
// decides).
func buildCompareSummary(sessions []sessionListItem) compareSummary {
	dur := compareMetricInt{PerSession: make(map[string]int64, len(sessions))}
	cost := compareMetricFloat{PerSession: make(map[string]float64, len(sessions))}
	op := compareMetricInt{PerSession: make(map[string]int64, len(sessions))}
	tok := compareMetricInt{PerSession: make(map[string]int64, len(sessions))}
	for _, s := range sessions {
		durVal := s.EndTS
		if s.EndTS != nil {
			if d := *s.EndTS - s.StartTS; d > 0 {
				durVal = &d
			}
		}
		if durVal != nil {
			dur.PerSession[s.ID] = *durVal
		} else {
			dur.PerSession[s.ID] = 0
		}
		cost.PerSession[s.ID] = s.CostUSD
		op.PerSession[s.ID] = s.OpCount
		tok.PerSession[s.ID] = s.TokensIn + s.TokensOut
	}
	dur.Best, dur.Worst = pickLowerIsBetterInt(dur.PerSession)
	cost.Best, cost.Worst = pickLowerIsBetterFloat(cost.PerSession)
	// OpCount and tokens: neutral, omit Best/Worst.
	return compareSummary{
		DurationUS: dur,
		CostUSD:    cost,
		OpCount:    op,
		Tokens:     tok,
	}
}

// pickLowerIsBetterInt returns pointers to the session ids with the
// min and max values; nil if the map is empty. Used for the
// "lower is better" metrics (duration, cost).
func pickLowerIsBetterInt(perSession map[string]int64) (best, worst *string) {
	if len(perSession) == 0 {
		return nil, nil
	}
	var minVal, maxVal int64
	var minID, maxID string
	first := true
	for id, v := range perSession {
		if first {
			minVal, maxVal = v, v
			minID, maxID = id, id
			first = false
			continue
		}
		if v < minVal {
			minVal = v
			minID = id
		}
		if v > maxVal {
			maxVal = v
			maxID = id
		}
	}
	return &minID, &maxID
}

// pickLowerIsBetterFloat is the float64 counterpart of
// pickLowerIsBetterInt.
func pickLowerIsBetterFloat(perSession map[string]float64) (best, worst *string) {
	if len(perSession) == 0 {
		return nil, nil
	}
	var minVal, maxVal float64
	var minID, maxID string
	first := true
	for id, v := range perSession {
		if first {
			minVal, maxVal = v, v
			minID, maxID = id, id
			first = false
			continue
		}
		if v < minVal {
			minVal = v
			minID = id
		}
		if v > maxVal {
			maxVal = v
			maxID = id
		}
	}
	return &minID, &maxID
}

// toAny is a tiny convenience for spreading a []string into []any
// for QueryContext.
func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
