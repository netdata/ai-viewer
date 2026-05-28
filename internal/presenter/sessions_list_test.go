package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sessionListBody mirrors the JSON shape of GET /api/sessions so test
// failures cite the wire contract rather than the production struct.
type sessionListBody struct {
	Items []struct {
		ID                string  `json:"id"`
		NativeID          string  `json:"native_id"`
		RootSessionID     string  `json:"root_session_id"`
		ParentSessionID   *string `json:"parent_session_id"`
		SourceID          string  `json:"source_id"`
		Kind              string  `json:"kind"`
		AgentName         string  `json:"agent_name"`
		Model             string  `json:"model"`
		Status            string  `json:"status"`
		StartTS           int64   `json:"start_ts"`
		EndTS             *int64  `json:"end_ts"`
		TokensIn          int64   `json:"tokens_in"`
		TokensOut         int64   `json:"tokens_out"`
		CostUSD           float64 `json:"cost_usd"`
		TurnCount         int64   `json:"turn_count"`
		OpCount           int64   `json:"op_count"`
		FailureCount      int64   `json:"failure_count"`
		ChildSessionCount int64   `json:"child_session_count"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// getSessions issues GET /api/sessions?<query> against the handler.
func getSessions(t *testing.T, p *Presenter, query string) (int, sessionListBody, errorEnvelope) {
	t.Helper()
	path := "/api/sessions"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body sessionListBody
	var env errorEnvelope
	raw := rr.Body.Bytes()
	if len(raw) > 0 {
		if rr.Code >= 400 {
			_ = json.Unmarshal(raw, &env)
		} else {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode body: %v (raw=%q)", err, raw)
			}
		}
	}
	return rr.Code, body, env
}

// TestSessions_EmptyReturns200 asserts an empty DB returns 200 with an
// empty items array and no cursor.
func TestSessions_EmptyReturns200(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	code, body, _ := getSessions(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 0 {
		t.Fatalf("items = %d, want 0", len(body.Items))
	}
	if body.NextCursor != "" {
		t.Fatalf("next_cursor = %q, want empty", body.NextCursor)
	}
}

// TestSessions_GroupRootDefault asserts the default (group=root) returns
// only root sessions, each carrying its child_session_count, and that
// the full-row fields match the fixture.
func TestSessions_GroupRootDefault(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getSessions(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 1 {
		t.Fatalf("group=root items = %d, want 1 (root only)", len(body.Items))
	}
	it := body.Items[0]
	if it.ID != "rootA" {
		t.Fatalf("id = %q, want rootA", it.ID)
	}
	if it.ChildSessionCount != 2 {
		t.Fatalf("child_session_count = %d, want 2", it.ChildSessionCount)
	}
	if it.AgentName != "nedi" || it.Model != "claude-opus-4-7" {
		t.Fatalf("agent/model = %q/%q", it.AgentName, it.Model)
	}
	if it.TurnCount != 2 || it.OpCount != 4 || it.FailureCount != 1 {
		t.Fatalf("counts turn=%d op=%d fail=%d", it.TurnCount, it.OpCount, it.FailureCount)
	}
	if it.ParentSessionID != nil {
		t.Fatalf("parent_session_id = %v, want nil for root", *it.ParentSessionID)
	}
}

// TestSessions_GroupAll asserts group=all returns root + child sessions.
func TestSessions_GroupAll(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getSessions(t, p, "group=all")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 3 {
		t.Fatalf("group=all items = %d, want 3", len(body.Items))
	}
}

// TestSessions_OrderDescDefaultAndAsc asserts default order is start_ts
// DESC and order=asc flips it.
func TestSessions_OrderDescDefaultAndAsc(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	_, descBody, _ := getSessions(t, p, "group=all")
	if len(descBody.Items) != 3 {
		t.Fatalf("desc items = %d", len(descBody.Items))
	}
	// childA2 starts last (base+5000) so it is first under DESC.
	if descBody.Items[0].ID != "childA2" {
		t.Fatalf("desc first = %q, want childA2", descBody.Items[0].ID)
	}

	_, ascBody, _ := getSessions(t, p, "group=all&order=asc")
	if ascBody.Items[0].ID != "rootA" {
		t.Fatalf("asc first = %q, want rootA", ascBody.Items[0].ID)
	}
}

// TestSessions_FilterStatus asserts the status filter narrows the set.
func TestSessions_FilterStatus(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getSessions(t, p, "group=all&status=failed")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 1 || body.Items[0].ID != "childA2" {
		t.Fatalf("status=failed items = %+v", body.Items)
	}
}

// TestSessions_FilterModelsCommaAndRepeated asserts both array syntaxes
// (comma-separated and repeated params) filter on model.
func TestSessions_FilterModelsCommaAndRepeated(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	_, comma, _ := getSessions(t, p, "group=all&models=gpt-5,claude-haiku")
	if len(comma.Items) != 2 {
		t.Fatalf("comma models items = %d, want 2", len(comma.Items))
	}
	_, repeated, _ := getSessions(t, p, "group=all&models=gpt-5&models=claude-haiku")
	if len(repeated.Items) != 2 {
		t.Fatalf("repeated models items = %d, want 2", len(repeated.Items))
	}
}

// TestSessions_FilterAgentsSourcesQ asserts agents, sources, and q
// filters work.
func TestSessions_FilterAgentsSourcesQ(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	_, agentBody, _ := getSessions(t, p, "group=all&agents=worker")
	if len(agentBody.Items) != 2 {
		t.Fatalf("agents=worker items = %d, want 2", len(agentBody.Items))
	}
	_, srcBody, _ := getSessions(t, p, "group=all&sources=src1")
	if len(srcBody.Items) != 3 {
		t.Fatalf("sources=src1 items = %d, want 3", len(srcBody.Items))
	}
	_, qBody, _ := getSessions(t, p, "group=all&q=ned")
	if len(qBody.Items) != 1 || qBody.Items[0].ID != "rootA" {
		t.Fatalf("q=ned items = %+v", qBody.Items)
	}
}

// TestSessions_FilterTools asserts the tools filter matches sessions
// that have any op using one of the named tools.
func TestSessions_FilterTools(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// rootA has ops named Bash and Read; childA1/A2 have none seeded.
	_, body, _ := getSessions(t, p, "group=all&tools=Bash")
	if len(body.Items) != 1 || body.Items[0].ID != "rootA" {
		t.Fatalf("tools=Bash items = %+v, want [rootA]", body.Items)
	}
}

// TestSessions_FilterTimeRange asserts from/to bound on start_ts.
func TestSessions_FilterTimeRange(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// rootA starts at base+1000, childA1 at base+2000, childA2 at base+5000.
	from := base + 1_500
	to := base + 4_000
	_, body, _ := getSessions(t, p, "group=all&from="+itoa64(from)+"&to="+itoa64(to))
	if len(body.Items) != 1 || body.Items[0].ID != "childA1" {
		t.Fatalf("time range items = %+v, want [childA1]", body.Items)
	}
}

// TestSessions_BadRequests asserts the 400 paths: from>to, limit>1000,
// unknown sort/order/group, malformed cursor.
func TestSessions_BadRequests(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	cases := []struct {
		name, query string
	}{
		{"from after to", "from=9&to=1"},
		{"limit over max", "limit=1001"},
		{"bad sort", "sort=cost_usd"},
		{"bad order", "order=sideways"},
		{"bad group", "group=clusters"},
		{"malformed cursor", "cursor=not-base64!!"},
		{"non-numeric from", "from=abc"},
		{"non-numeric limit", "limit=xyz"},
		{"empty-only models filter", "models="},
		{"comma-only agents filter", "agents=,"},
		{"empty-only status filter", "status=,,"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, env := getSessions(t, p, tc.query)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
			}
		})
	}
}

// TestSessions_HeadParity asserts HEAD returns the same status with an
// empty body.
func TestSessions_HeadParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	getReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	getRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(getRR, getReq)

	headReq := httptest.NewRequest(http.MethodHead, "/api/sessions", nil)
	headRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(headRR, headReq)

	if getRR.Code != http.StatusOK || headRR.Code != http.StatusOK {
		t.Fatalf("GET=%d HEAD=%d, want 200/200", getRR.Code, headRR.Code)
	}
	if headRR.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", headRR.Body.String())
	}
}

// TestSessions_MethodNotAllowed asserts non-GET/HEAD is rejected.
func TestSessions_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// itoa64 is a tiny base-10 int64 formatter for building query strings in
// tests without pulling strconv into every test file.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
