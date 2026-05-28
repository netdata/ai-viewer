package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sessionLogsBody mirrors GET /api/sessions/:id/logs.
type sessionLogsBody struct {
	Items []struct {
		TS       int64          `json:"ts"`
		Severity string         `json:"severity"`
		Source   string         `json:"source"`
		OpID     *string        `json:"op_id"`
		Message  string         `json:"message"`
		Extras   map[string]any `json:"extras"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func getSessionLogs(t *testing.T, p *Presenter, id, query string) (int, sessionLogsBody, errorEnvelope) {
	t.Helper()
	path := "/api/sessions/" + id + "/logs"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body sessionLogsBody
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

// TestSessionLogs_HappyPathOrdered asserts all log rows for the session
// come back ordered by (ts, id), with extras decoded.
func TestSessionLogs_HappyPathOrdered(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getSessionLogs(t, p, "rootA", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(body.Items))
	}
	// Ascending ts: INF(base+1200), ERR(base+5150), WRN(base+8000).
	if body.Items[0].Severity != "INF" || body.Items[1].Severity != "ERR" || body.Items[2].Severity != "WRN" {
		t.Fatalf("order = %q,%q,%q", body.Items[0].Severity, body.Items[1].Severity, body.Items[2].Severity)
	}
	if body.Items[1].Extras["path"] != "/x" {
		t.Fatalf("err extras = %v, want path=/x", body.Items[1].Extras)
	}
	if body.Items[0].OpID == nil || *body.Items[0].OpID != "o1" {
		t.Fatalf("first op_id = %v, want o1", body.Items[0].OpID)
	}
	if body.Items[2].OpID != nil {
		t.Fatalf("third op_id = %v, want nil", body.Items[2].OpID)
	}
}

// TestSessionLogs_SeverityFilter asserts severity=WRN,ERR narrows to the
// matching rows.
func TestSessionLogs_SeverityFilter(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getSessionLogs(t, p, "rootA", "severity=WRN,ERR")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items = %d, want 2 (WRN+ERR)", len(body.Items))
	}
	for _, it := range body.Items {
		if it.Severity != "WRN" && it.Severity != "ERR" {
			t.Fatalf("unexpected severity %q", it.Severity)
		}
	}
}

// TestSessionLogs_Pagination asserts keyset pagination on (ts, id).
func TestSessionLogs_Pagination(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, page1, _ := getSessionLogs(t, p, "rootA", "limit=2")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("page1 items = %d, want 2", len(page1.Items))
	}
	if page1.NextCursor == "" {
		t.Fatal("page1 next_cursor empty")
	}
	_, page2, _ := getSessionLogs(t, p, "rootA", "limit=2&cursor="+page1.NextCursor)
	if len(page2.Items) != 1 {
		t.Fatalf("page2 items = %d, want 1", len(page2.Items))
	}
	if page2.NextCursor != "" {
		t.Fatalf("page2 next_cursor = %q, want empty", page2.NextCursor)
	}
}

// TestSessionLogs_NotFound asserts an unknown session id returns 404.
func TestSessionLogs_NotFound(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, _, env := getSessionLogs(t, p, "nope", "")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotFound)
	}
}

// TestSessionLogs_BadSeverity asserts an unknown severity token is 400.
func TestSessionLogs_BadSeverity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, _, env := getSessionLogs(t, p, "rootA", "severity=LOUD")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestSessionLogs_EmptySeverityRejected asserts logs severity follows the
// SAME present-but-empty rule as the session array filters (rest-api.md
// §Conventions): `?severity=` or `?severity=,` is a 400, not a silent
// "no filter" (codex iter-4 P3). An ABSENT severity key remains "all
// severities" and is verified by the other logs tests (e.g. _Pagination,
// which passes no severity and expects 200).
func TestSessionLogs_EmptySeverityRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	for _, q := range []string{"severity=", "severity=,", "severity=%2C%2C"} {
		t.Run(q, func(t *testing.T) {
			code, _, env := getSessionLogs(t, p, "rootA", q)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
			}
		})
	}

	// Sanity: an absent severity key is accepted (all severities).
	code, _, _ := getSessionLogs(t, p, "rootA", "limit=2")
	if code != http.StatusOK {
		t.Fatalf("absent-severity status = %d, want 200", code)
	}
}

// TestSessionLogs_SeverityControlCharRawBeforeTrim pins codex iter-5 for the
// logs endpoint: ?severity carries a control byte that is also whitespace
// (\t=0x09, \r=0x0D), which a trim-first order would erase and silently accept.
// The shared parseRequiredNonEmptyArray now rejects control chars on the RAW
// entry before splitting/trimming, so ?severity=%09ERROR is a 400. An absent
// severity key remains "all severities" (200) — verified here too so the new
// raw check does not over-reject.
func TestSessionLogs_SeverityControlCharRawBeforeTrim(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base) // rootA exists, so the request reaches severity parsing

	for _, q := range []string{"severity=%09ERR", "severity=ERR%0D", "severity=%0AWRN"} {
		t.Run("reject/"+q, func(t *testing.T) {
			code, _, env := getSessionLogs(t, p, "rootA", q)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (raw control byte before trim)", code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
			}
		})
	}

	// Absent severity => all severities, 200 (not over-rejected).
	code, _, _ := getSessionLogs(t, p, "rootA", "")
	if code != http.StatusOK {
		t.Fatalf("absent-severity status = %d, want 200", code)
	}
}

// TestSessionLogs_BadPaging asserts the paging 400 paths (bad limit,
// over-max limit, malformed cursor) are rejected by the shared rules.
func TestSessionLogs_BadPaging(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	cases := []string{"limit=abc", "limit=0", "limit=1001", "cursor=not-base64!!"}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			code, _, env := getSessionLogs(t, p, "rootA", q)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
			}
		})
	}
}

// TestSessionLogs_CrossEndpointCursorRejected asserts a cursor minted
// under a foreign ordering (e.g. a /api/sessions cursor with
// sort=start_ts) cannot be replayed against the logs endpoint, whose
// ordering is fixed at (ts, asc). Replaying it is a 400, not a corrupt
// page.
func TestSessionLogs_CrossEndpointCursorRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// A structurally-valid cursor minted under the sessions ordering.
	foreign := b64Cursor(t, `{"ts":`+itoa64(base)+`,"id":"42","sort":"start_ts","order":"desc"}`)
	code, _, env := getSessionLogs(t, p, "rootA", "cursor="+foreign)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestSessionLogs_CursorFingerprintSeverityMismatch400 asserts the logs
// cursor is bound to the severity filter too: minting a cursor with no
// severity filter and replaying it with severity=ERR (a different result
// set) is a 400, while replaying with the identical (absent) severity
// yields the next page.
func TestSessionLogs_CursorFingerprintSeverityMismatch400(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base) // rootA has 3 logs: INF, ERR, WRN

	// Page 1 with a TWO-severity filter (ERR,WRN => 2 rows) so the cursor is
	// bound to a multi-element severity set (exercises the sorted-join path)
	// and a next page exists.
	code, page1, _ := getSessionLogs(t, p, "rootA", "limit=1&severity=ERR,WRN")
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Replay with a different severity set (ERR only) => 400.
	mismatchCode, _, env := getSessionLogs(t, p, "rootA", "limit=1&severity=ERR&cursor="+page1.NextCursor)
	if mismatchCode != http.StatusBadRequest {
		t.Fatalf("severity-mismatch status = %d, want 400", mismatchCode)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("severity-mismatch code = %q, want %q", env.Error.Code, CodeBadRequest)
	}

	// Replay with the SAME set in reverse order (WRN,ERR) => accepted: the
	// fingerprint sorts the severity set before encoding.
	matchCode, page2, _ := getSessionLogs(t, p, "rootA", "limit=1&severity=WRN,ERR&cursor="+page1.NextCursor)
	if matchCode != http.StatusOK {
		t.Fatalf("reordered-severity status = %d, want 200", matchCode)
	}
	if len(page2.Items) != 1 {
		t.Fatalf("reordered-severity page2 items = %d, want 1", len(page2.Items))
	}
}

// TestSessionLogs_CursorNonNumericIDRejected pins codex iter-6 P2 / glm P2-2:
// the logs keyset id is the log_entries.id INTEGER column, so a cursor whose
// decoded id is not a decimal int64 must be a loud 400 (BAD_REQUEST) rather
// than being bound into the (ts, id) > (?, ?) keyset comparison as a string,
// where SQLite affinity would silently coerce it and yield a wrong/empty page.
// The cursor carries the LIVE logFilter.fingerprint() so it passes the
// fingerprint gate and reaches the new int64-id validation. A VALID numeric-id
// logs cursor still paginates (TestSessionLogs_Pagination covers that).
func TestSessionLogs_CursorNonNumericIDRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// Forge a structurally-valid logs cursor (correct sort/order + the live
	// query fingerprint) whose id is non-numeric, so only the int64-id check
	// can reject it.
	lf := logFilter{id: "rootA", severities: []string{"ERR", "WRN"}}
	forged := pageCursor{
		TS: base, ID: "abc", Sort: logsSort, Order: logsOrder, FP: lf.fingerprint(),
	}.encode()

	code, _, env := getSessionLogs(t, p, "rootA", "severity=ERR,WRN&cursor="+forged)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (non-numeric logs cursor id)", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestSessionLogs_PathControlCharRejected pins glm iter-6 P2-1 for the logs
// endpoint: a control byte in the path :id (e.g. a%09b => "a\tb") must be a
// loud 400 (BAD_REQUEST) checked on the RAW PathValue before TrimSpace, not a
// silent doomed lookup that returns 404. Mirrors the query-value control-char
// rule so every user-supplied value on the surface is covered.
func TestSessionLogs_PathControlCharRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/a%09b/logs", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (control byte in path id)", rr.Code)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err: %v (raw=%q)", err, rr.Body.Bytes())
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestSessionLogs_HeadParity asserts HEAD on the logs route.
func TestSessionLogs_HeadParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	headReq := httptest.NewRequest(http.MethodHead, "/api/sessions/rootA/logs", nil)
	headRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(headRR, headReq)
	if headRR.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", headRR.Code)
	}
	if headRR.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", headRR.Body.String())
	}
}
