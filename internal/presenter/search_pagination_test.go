package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Pagination, cursor-stability, validation, and FTS5-syntax tests for GET
// /api/search. The basic match / filter / logs_indexed tests live in
// search_test.go alongside the shared seed helpers; this file is the second
// half, split to keep each test file within the project's file-size budget.

// TestSearch_Pagination asserts limit caps each result array and a minted
// cursor advances to the next page over the SAME ranked set.
func TestSearch_Pagination(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA", kind: "root",
		agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 99_000,
	})
	seedTurn(t, db, turnRow{id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1000, status: "completed"})
	// Three matching ops, distinct relevance so the rank order is total.
	seedFTSOp(t, db, opRow{id: "p1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed"},
		"needle needle needle needle")
	seedFTSOp(t, db, opRow{id: "p2", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "B",
		startTS: base + 1300, endTS: base + 1400, durationUS: 100, status: "completed"},
		"needle needle needle")
	seedFTSOp(t, db, opRow{id: "p3", turnID: "t1", sessionID: "rootA", seq: 3, kind: "tool", name: "C",
		startTS: base + 1500, endTS: base + 1600, durationUS: 100, status: "completed"},
		"needle needle")

	// Page 1: limit=2 → first two by rank, plus a next_cursor.
	code, body, env := getSearch(t, p, "q=needle&limit=2")
	if code != http.StatusOK {
		t.Fatalf("page1 status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 2 || body.Ops[0].OpID != "p1" || body.Ops[1].OpID != "p2" {
		t.Fatalf("page1 ops=%+v, want [p1,p2]", body.Ops)
	}
	if body.NextCursor == "" {
		t.Fatal("page1: want a next_cursor")
	}

	// Page 2: same query + cursor → the third op, no further cursor.
	code, body, env = getSearch(t, p, "q=needle&limit=2&cursor="+body.NextCursor)
	if code != http.StatusOK {
		t.Fatalf("page2 status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 1 || body.Ops[0].OpID != "p3" {
		t.Fatalf("page2 ops=%+v, want [p3]", body.Ops)
	}
	if body.NextCursor != "" {
		t.Errorf("page2: want no next_cursor, got %q", body.NextCursor)
	}
}

// TestSearch_CursorFingerprintMismatch asserts a cursor minted on one query is
// rejected when replayed against a DIFFERENT query (changed q), mirroring the
// other paginated endpoints' fingerprint-stability guard.
func TestSearch_CursorFingerprintMismatch(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA", kind: "root",
		agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 9000,
	})
	seedTurn(t, db, turnRow{id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1000, status: "completed"})
	seedFTSOp(t, db, opRow{id: "o1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed"},
		"alpha alpha beta")
	seedFTSOp(t, db, opRow{id: "o2", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "B",
		startTS: base + 1300, endTS: base + 1400, durationUS: 100, status: "completed"},
		"alpha")

	_, body, _ := getSearch(t, p, "q=alpha&limit=1")
	if body.NextCursor == "" {
		t.Fatal("want a next_cursor to replay")
	}
	// Replay the alpha-cursor against a different q → BAD_REQUEST.
	code, _, env := getSearch(t, p, "q=beta&limit=1&cursor="+body.NextCursor)
	if code != http.StatusBadRequest {
		t.Fatalf("fingerprint mismatch: status=%d, want 400", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Errorf("error code=%q, want BAD_REQUEST", env.Error.Code)
	}
}

// TestSearch_CursorTamperedOrOversized asserts a malformed/oversized/foreign
// cursor is a BAD_REQUEST (mirrors decodeCursor's strict validation + the
// search cursor's own sort/order check).
func TestSearch_CursorTamperedOrOversized(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedSearchBasic(t, db)

	cases := []struct {
		name, cursor string
	}{
		{"garbage", "!!!not-base64!!!"},
		{"truncated", b64Cursor(t, `{"ts":2,"id":`)},
		{"unknown-field", b64Cursor(t, `{"ts":2,"id":"off","sort":"search","order":"rank","fp":"x","evil":1}`)},
		{"foreign-ordering", b64Cursor(t, `{"ts":2,"id":"x","sort":"start_ts","order":"desc","fp":"x"}`)},
		{"oversized", b64Cursor(t, `{"ts":2,"id":"`+strings.Repeat("A", 9000)+`","sort":"search","order":"rank","fp":"x"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, env := getSearch(t, p, "q=timeout&cursor="+tc.cursor)
			if code != http.StatusBadRequest {
				t.Fatalf("%s: status=%d, want 400", tc.name, code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Errorf("%s: error code=%q, want BAD_REQUEST", tc.name, env.Error.Code)
			}
		})
	}
}

// TestSearch_Validation covers q presence/whitespace/control-char, limit clamp,
// the 405 on POST, and HEAD parity.
func TestSearch_Validation(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedSearchBasic(t, db)

	// q presence + control chars are hard errors (rest-api.md §GET /api/search:
	// "non-empty after trim; control chars rejected").
	badCases := []struct{ name, query string }{
		{"missing-q", ""},
		{"empty-q", "q="},
		{"whitespace-q", "q=%20%20"},
		{"control-char-q", "q=ab%09cd"}, // embedded TAB
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, env := getSearch(t, p, tc.query)
			if code != http.StatusBadRequest {
				t.Fatalf("%s: status=%d, want 400", tc.name, code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Errorf("%s: code=%q, want BAD_REQUEST", tc.name, env.Error.Code)
			}
		})
	}

	// limit is CLAMPED, not rejected (design: "limit clamp [1,200] default 50";
	// mirrors the stats-top ?n clamp). An invalid or out-of-range limit falls
	// back to the default / ceiling and still returns 200.
	clampCases := []struct{ name, query string }{
		{"limit-zero", "q=timeout&limit=0"},
		{"limit-negative", "q=timeout&limit=-3"},
		{"limit-nan", "q=timeout&limit=abc"},
		{"limit-over-max", "q=timeout&limit=9999"},
	}
	for _, tc := range clampCases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, env := getSearch(t, p, tc.query); code != http.StatusOK {
				t.Fatalf("%s: status=%d env=%+v, want 200 (clamp)", tc.name, code, env)
			}
		})
	}

	// POST → 405.
	req := httptest.NewRequest(http.MethodPost, "/api/search?q=timeout", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status=%d, want 405", rr.Code)
	}

	// HEAD → 200, empty body, JSON content-type.
	hreq := httptest.NewRequest(http.MethodHead, "/api/search?q=timeout", nil)
	hrr := httptest.NewRecorder()
	p.Handler().ServeHTTP(hrr, hreq)
	if hrr.Code != http.StatusOK {
		t.Fatalf("HEAD: status=%d, want 200", hrr.Code)
	}
	if hrr.Body.Len() != 0 {
		t.Errorf("HEAD body=%q, want empty", hrr.Body.String())
	}
	if ct := hrr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("HEAD Content-Type=%q", ct)
	}
}

// TestSearch_LimitClampMaxReturns confirms the limit clamp is a real ceiling:
// with > maxSearchLimit matching ops, exactly maxSearchLimit are returned.
func TestSearch_LimitClampMaxReturns(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA", kind: "root",
		agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 9_000_000,
	})
	seedTurn(t, db, turnRow{id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1000, status: "completed"})
	for i := 0; i < maxSearchLimit+5; i++ {
		id := "op" + i64(int64(i))
		seedFTSOp(t, db, opRow{
			id: id, turnID: "t1", sessionID: "rootA", seq: int64(i + 1), kind: "tool", name: "T",
			startTS: base + int64(i+1)*1000, endTS: base + int64(i+1)*1000 + 100, durationUS: 100, status: "completed",
		}, "needle text")
	}
	code, body, env := getSearch(t, p, "q=needle&limit=9999")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if len(body.Ops) != maxSearchLimit {
		t.Fatalf("ops len=%d, want clamp to %d", len(body.Ops), maxSearchLimit)
	}
}

// TestSearch_FTS5SyntaxHonored proves the q string reaches MATCH unescaped but
// bound: a phrase query "a b" matches only the adjacent phrase, and a prefix
// query foo* matches by prefix — both FTS5 operators the operator can use.
func TestSearch_FTS5SyntaxHonored(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA", kind: "root",
		agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 9000,
	})
	seedTurn(t, db, turnRow{id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1000, status: "completed"})
	// adjacent: contains the phrase "connection refused"; scattered: has both
	// words but not adjacent.
	seedFTSOp(t, db, opRow{id: "adjacent", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed"},
		"connection refused by peer")
	seedFTSOp(t, db, opRow{id: "scattered", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "B",
		startTS: base + 1300, endTS: base + 1400, durationUS: 100, status: "completed"},
		"connection was later refused")

	// Phrase query: only the adjacent op matches. q is URL-encoded ("..." → %22).
	code, body, env := getSearch(t, p, "q=%22connection+refused%22")
	if code != http.StatusOK {
		t.Fatalf("phrase status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 1 || body.Ops[0].OpID != "adjacent" {
		t.Fatalf("phrase ops=%+v, want [adjacent]", body.Ops)
	}

	// Prefix query: "conn*" matches both (prefix of "connection").
	code, body, env = getSearch(t, p, "q=conn*")
	if code != http.StatusOK {
		t.Fatalf("prefix status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 2 {
		t.Fatalf("prefix ops=%+v, want 2", body.Ops)
	}
}

// TestSearch_LogsPaginationAndFingerprint asserts the cursor advances LOGS as
// well (pagination advances both arrays by the same offset), and a log-only
// match paginates correctly.
func TestSearch_LogsPaginationAndFingerprint(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA", kind: "root",
		agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 99_000,
	})
	// Three indexed logs, distinct relevance.
	seedFTSLog(t, db, logRow{sessionID: "rootA", ts: base + 1100, severity: "ERR", source: "x", message: "needle needle needle"})
	seedFTSLog(t, db, logRow{sessionID: "rootA", ts: base + 1200, severity: "WRN", source: "x", message: "needle needle"})
	seedFTSLog(t, db, logRow{sessionID: "rootA", ts: base + 1300, severity: "INF", source: "x", message: "needle here"})

	code, body, env := getSearch(t, p, "q=needle&limit=2")
	if code != http.StatusOK {
		t.Fatalf("page1 status=%d env=%+v", code, env)
	}
	if len(body.Logs) != 2 {
		t.Fatalf("page1 logs=%+v, want 2", body.Logs)
	}
	if body.NextCursor == "" {
		t.Fatal("page1: want next_cursor (3 logs > limit 2)")
	}
	code, body, env = getSearch(t, p, "q=needle&limit=2&cursor="+body.NextCursor)
	if code != http.StatusOK {
		t.Fatalf("page2 status=%d env=%+v", code, env)
	}
	if len(body.Logs) != 1 {
		t.Fatalf("page2 logs=%+v, want 1", body.Logs)
	}
}

// TestSearch_AsymmetricPaginationOpsRunDry asserts the offset advances ops and
// logs INDEPENDENTLY: with fewer ops than logs, the ops array simply runs dry on
// later pages (no duplicate, no skip) while the logs array keeps paginating. The
// next_cursor exists as long as EITHER array filled the page.
func TestSearch_AsymmetricPaginationOpsRunDry(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA", kind: "root",
		agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 99_000,
	})
	seedTurn(t, db, turnRow{id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1000, status: "completed"})
	// One matching op, three matching logs.
	seedFTSOp(t, db, opRow{id: "o1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed"}, "needle once")
	seedFTSLog(t, db, logRow{sessionID: "rootA", ts: base + 1100, severity: "ERR", source: "x", message: "needle needle needle"})
	seedFTSLog(t, db, logRow{sessionID: "rootA", ts: base + 1200, severity: "WRN", source: "x", message: "needle needle"})
	seedFTSLog(t, db, logRow{sessionID: "rootA", ts: base + 1300, severity: "INF", source: "x", message: "needle x"})

	// Page 1 (limit 2): 1 op, 2 logs, cursor minted (logs filled).
	code, body, env := getSearch(t, p, "q=needle&limit=2")
	if code != http.StatusOK {
		t.Fatalf("page1 status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 1 || len(body.Logs) != 2 || body.NextCursor == "" {
		t.Fatalf("page1: ops=%d logs=%d cursor=%q, want 1/2/non-empty", len(body.Ops), len(body.Logs), body.NextCursor)
	}

	// Page 2 (offset 2): ops run dry (only 1 total), 1 log remains, no cursor.
	code, body, env = getSearch(t, p, "q=needle&limit=2&cursor="+body.NextCursor)
	if code != http.StatusOK {
		t.Fatalf("page2 status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 0 {
		t.Fatalf("page2 ops=%+v, want empty (ops exhausted, not repeated)", body.Ops)
	}
	if len(body.Logs) != 1 {
		t.Fatalf("page2 logs=%+v, want 1", body.Logs)
	}
	if body.NextCursor != "" {
		t.Errorf("page2: want no further cursor")
	}
}

// TestSearch_NoMatchEmptyArrays asserts a query that matches nothing returns
// empty (non-nil) ops/logs arrays and no cursor, with logs_indexed still
// reflecting the source flag.
func TestSearch_NoMatchEmptyArrays(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedSearchBasic(t, db)

	code, body, env := getSearch(t, p, "q=zzzznotpresent")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 0 || len(body.Logs) != 0 {
		t.Errorf("ops=%+v logs=%+v, want both empty", body.Ops, body.Logs)
	}
	if body.NextCursor != "" {
		t.Errorf("next_cursor=%q, want empty", body.NextCursor)
	}
	if !body.LogsIndexed {
		t.Errorf("logs_indexed=false, want true")
	}
	// Empty arrays must serialize as [] not null: re-marshal and check.
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"ops":[]`) || !strings.Contains(string(raw), `"logs":[]`) {
		t.Errorf("arrays not serialized as []: %s", raw)
	}
}
