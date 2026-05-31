package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// timelineBody mirrors GET /api/sessions/:id/timeline.
type timelineBody struct {
	Lanes []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Spans []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Name    string `json:"name"`
			StartTS int64  `json:"start_ts"`
			EndTS   *int64 `json:"end_ts"`
			Status  string `json:"status"`
		} `json:"spans"`
	} `json:"lanes"`
	TStart int64 `json:"t_start"`
	TEnd   int64 `json:"t_end"`
}

func getTimeline(t *testing.T, p *Presenter, id string) (int, timelineBody, errorEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/timeline", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body timelineBody
	var env errorEnvelope
	raw := rr.Body.Bytes()
	if len(raw) > 0 {
		if rr.Code >= 400 {
			_ = json.Unmarshal(raw, &env)
		} else if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body: %v (raw=%q)", err, raw)
		}
	}
	return rr.Code, body, env
}

// laneByKey returns the lane with the given key, or fails the test.
func laneByKey(t *testing.T, body timelineBody, key string) int {
	t.Helper()
	for i, l := range body.Lanes {
		if l.Key == key {
			return i
		}
	}
	t.Fatalf("lane %q not found in %+v", key, body.Lanes)
	return -1
}

// TestTimeline_LanesAndSpans asserts one lane per session in the tree
// (root + child), each lane's spans ordered by start_ts, and the compaction
// op surfaced as a kind='compaction' span. t_start/t_end span the whole
// tree's ops.
func TestTimeline_LanesAndSpans(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedTopoTree(t, db, base)

	code, body, _ := getTimeline(t, p, "rootA")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Lanes) != 2 {
		t.Fatalf("lanes = %d, want 2 (%+v)", len(body.Lanes), body.Lanes)
	}

	rootLane := body.Lanes[laneByKey(t, body, "session:rootA")]
	if rootLane.Label != "nedi (root)" {
		t.Fatalf("root lane label = %q, want 'nedi (root)'", rootLane.Label)
	}
	// rootA has five ops: o1,o2,o3,o5(compaction),o4 — ordered by start_ts.
	if len(rootLane.Spans) != 5 {
		t.Fatalf("root spans = %d, want 5 (%+v)", len(rootLane.Spans), rootLane.Spans)
	}
	// Spans ordered by start_ts: o1(1100), o3(2000), o2(2200), o5(4500), o4(5100).
	wantOrder := []string{"o1", "o3", "o2", "o5", "o4"}
	for i, id := range wantOrder {
		if rootLane.Spans[i].ID != id {
			t.Fatalf("span[%d] = %q, want %q (%+v)", i, rootLane.Spans[i].ID, id, rootLane.Spans)
		}
	}

	childLane := body.Lanes[laneByKey(t, body, "session:childA1")]
	if childLane.Label != "worker" {
		t.Fatalf("child lane label = %q, want 'worker'", childLane.Label)
	}
	if len(childLane.Spans) != 1 || childLane.Spans[0].ID != "oc1" {
		t.Fatalf("child spans = %+v", childLane.Spans)
	}

	// t_start = earliest op start (o1 = base+1100); t_end = latest end.
	// o4 ends at base+5200, but o3 (session) ends at base+4000; the max end
	// across all ops is o4 at base+5200.
	if body.TStart != base+1_100 {
		t.Fatalf("t_start = %d, want %d", body.TStart, base+1_100)
	}
	if body.TEnd != base+5_200 {
		t.Fatalf("t_end = %d, want %d", body.TEnd, base+5_200)
	}
}

// TestTimeline_CompactionSpanPresent asserts the compaction op is present
// with kind='compaction' so the frontend can draw the breakpoint.
func TestTimeline_CompactionSpanPresent(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	_, body, _ := getTimeline(t, p, "rootA")
	var found bool
	for _, l := range body.Lanes {
		for _, s := range l.Spans {
			if s.ID == "o5" {
				found = true
				if s.Kind != "compaction" {
					t.Fatalf("o5 kind = %q, want compaction", s.Kind)
				}
			}
		}
	}
	if !found {
		t.Fatal("compaction span o5 not found")
	}
}

// TestTimeline_RunningOpNullEnd asserts an op with no end_ts serialises
// end_ts as null and still contributes its start_ts to t_end.
func TestTimeline_RunningOpNullEnd(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "runA", sourceID: "src1", nativeID: "nR", rootID: "runA",
		kind: "root", agent: "nedi", status: "running",
		startTS: base + 1_000, // no endTS
	})
	seedTurn(t, db, turnRow{
		id: "rt1", sessionID: "runA", seq: 1, startTS: base + 1_000, status: "running", opCount: 1,
	})
	// A running op: no endTS, no durationUS, status running.
	seedOp(t, db, opRow{
		id: "ro1", turnID: "rt1", sessionID: "runA", seq: 1, kind: "llm", name: "model",
		startTS: base + 1_500, status: "running",
	})

	code, body, _ := getTimeline(t, p, "runA")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Lanes) != 1 || len(body.Lanes[0].Spans) != 1 {
		t.Fatalf("lanes/spans = %+v", body.Lanes)
	}
	span := body.Lanes[0].Spans[0]
	if span.EndTS != nil {
		t.Fatalf("running op end_ts = %v, want null", *span.EndTS)
	}
	// t_end falls back to the running op's start_ts.
	if body.TEnd != base+1_500 || body.TStart != base+1_500 {
		t.Fatalf("t_start/t_end = %d/%d, want %d/%d", body.TStart, body.TEnd, base+1_500, base+1_500)
	}
}

// TestTimeline_ScopedToTreeViaChildID asserts requesting by the child id
// still returns the whole-tree lanes (resolves to root_session_id).
func TestTimeline_ScopedToTreeViaChildID(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	code, body, _ := getTimeline(t, p, "childA1")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Lanes) != 2 {
		t.Fatalf("lanes = %d, want 2 (whole tree via child id)", len(body.Lanes))
	}
}

// TestTimeline_EmptyTreeZeroBounds asserts a session with no ops returns
// its lane (empty spans) and t_start/t_end = 0.
func TestTimeline_EmptyTreeZeroBounds(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "solo", sourceID: "src1", nativeID: "nS", rootID: "solo",
		kind: "root", agent: "lonely", status: "completed",
		startTS: base + 1_000, endTS: base + 2_000,
	})

	code, body, _ := getTimeline(t, p, "solo")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Lanes) != 1 {
		t.Fatalf("lanes = %d, want 1", len(body.Lanes))
	}
	if body.Lanes[0].Spans == nil {
		t.Fatal("spans is null, want empty array")
	}
	if len(body.Lanes[0].Spans) != 0 {
		t.Fatalf("spans = %d, want 0", len(body.Lanes[0].Spans))
	}
	if body.TStart != 0 || body.TEnd != 0 {
		t.Fatalf("t_start/t_end = %d/%d, want 0/0", body.TStart, body.TEnd)
	}
}

// TestTimeline_NotFound asserts an unknown id is 404 NOT_FOUND.
func TestTimeline_NotFound(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	code, _, env := getTimeline(t, p, "nope")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotFound)
	}
}

// TestTimeline_HeadParity asserts HEAD returns 200 + empty body.
func TestTimeline_HeadParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodHead, "/api/sessions/rootA/timeline", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
	}
}

// TestTimeline_MethodNotAllowed asserts a non-GET/HEAD method is 405.
func TestTimeline_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/rootA/timeline", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestTimeline_ControlCharRejected asserts a control byte in :id is a 400.
func TestTimeline_ControlCharRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/a%09b/timeline", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestTimeline_DBFailureReturns503 asserts a query error (DB closed before
// the request) surfaces as DB_UNAVAILABLE — exercises the writeDBError
// branch in the timeline path.
func TestTimeline_DBFailureReturns503(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/rootA/timeline", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeDBUnavailable)
	}
}
