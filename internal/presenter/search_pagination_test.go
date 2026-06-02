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
	seedFTSOp(t, db, opRow{
		id: "p1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed",
	},
		"needle needle needle needle")
	seedFTSOp(t, db, opRow{
		id: "p2", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "B",
		startTS: base + 1300, endTS: base + 1400, durationUS: 100, status: "completed",
	},
		"needle needle needle")
	seedFTSOp(t, db, opRow{
		id: "p3", turnID: "t1", sessionID: "rootA", seq: 3, kind: "tool", name: "C",
		startTS: base + 1500, endTS: base + 1600, durationUS: 100, status: "completed",
	},
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

// TestSearch_ExactMultipleNoPhantomCursor pins the limit+1 PEEK (round-6 P3):
// when EXACTLY `limit` rows match (an exact-multiple last page), next_cursor must
// be EMPTY — emitting one would point at an empty next page, violating the global
// "next_cursor when more rows exist" contract (rest-api.md §Pagination). The
// pre-peek code minted a cursor whenever a side filled `limit`, so this is the
// red→green discriminator. Cross-checked against limit+1 matches, where the
// cursor MUST appear and replaying it returns exactly the one remaining row.
func TestSearch_ExactMultipleNoPhantomCursor(t *testing.T) {
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

	const limit = 3
	// Exactly `limit` matching ops, distinct relevance so the rank order is total
	// and there are NO logs (logs_indexed true but no log match) — so this isolates
	// the ops-side exact-multiple boundary.
	wantOps := []string{"e1", "e2", "e3"}
	for i, id := range wantOps {
		seedFTSOp(t, db, opRow{
			id: id, turnID: "t1", sessionID: "rootA", seq: int64(i + 1), kind: "tool", name: id,
			startTS: base + int64(i+1)*1000, endTS: base + int64(i+1)*1000 + 100, durationUS: 100, status: "completed",
		}, strings.Repeat("needle ", limit-i)) // e1 most relevant … e3 least.
	}

	// Exactly `limit` matches → a single FULL page, NO next_cursor (the peek saw
	// no extra row).
	code, body, env := getSearch(t, p, "q=needle&limit=3")
	if code != http.StatusOK {
		t.Fatalf("exact-multiple status=%d env=%+v", code, env)
	}
	if len(body.Ops) != limit {
		t.Fatalf("exact-multiple ops len=%d, want %d", len(body.Ops), limit)
	}
	if body.NextCursor != "" {
		t.Fatalf("exact-multiple: next_cursor=%q, want EMPTY (no further rows; phantom cursor)", body.NextCursor)
	}

	// Now add a 4th match (> limit) → next_cursor MUST appear, and replaying it
	// returns exactly the one remaining op, then no further cursor.
	seedFTSOp(t, db, opRow{
		id: "e4", turnID: "t1", sessionID: "rootA", seq: 4, kind: "tool", name: "e4",
		startTS: base + 4000, endTS: base + 4100, durationUS: 100, status: "completed",
	}, "needle") // least relevant → sorts last → lands on page 2.

	code, body, env = getSearch(t, p, "q=needle&limit=3")
	if code != http.StatusOK {
		t.Fatalf("over-limit page1 status=%d env=%+v", code, env)
	}
	if len(body.Ops) != limit {
		t.Fatalf("over-limit page1 ops len=%d, want %d", len(body.Ops), limit)
	}
	if body.NextCursor == "" {
		t.Fatal("over-limit page1: want a next_cursor (4 matches > limit 3)")
	}

	code, body, env = getSearch(t, p, "q=needle&limit=3&cursor="+body.NextCursor)
	if code != http.StatusOK {
		t.Fatalf("over-limit page2 status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 1 || body.Ops[0].OpID != "e4" {
		t.Fatalf("over-limit page2 ops=%+v, want [e4]", body.Ops)
	}
	if body.NextCursor != "" {
		t.Errorf("over-limit page2: next_cursor=%q, want EMPTY (last page exhausted)", body.NextCursor)
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
	seedFTSOp(t, db, opRow{
		id: "o1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed",
	},
		"alpha alpha beta")
	seedFTSOp(t, db, opRow{
		id: "o2", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "B",
		startTS: base + 1300, endTS: base + 1400, durationUS: 100, status: "completed",
	},
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
	seedFTSOp(t, db, opRow{
		id: "adjacent", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed",
	},
		"connection refused by peer")
	seedFTSOp(t, db, opRow{
		id: "scattered", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "B",
		startTS: base + 1300, endTS: base + 1400, durationUS: 100, status: "completed",
	},
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
	seedFTSOp(t, db, opRow{
		id: "o1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "A",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed",
	}, "needle once")
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

// TestSearch_RankTiePaginationStable pins the offset-pagination contract for
// EQUAL-rank rows. Both FTS queries order by rank first (bm25 best-first), but
// bm25 ties are common: rows whose indexed text is identical score identically.
// With ORDER BY rank alone the relative order WITHIN a tie group is unspecified,
// so it can differ between the page-1 query and the offset page-2 query and an
// offset split can then duplicate or skip a tied row. The fix appends a unique
// tie-breaker (fts_ops.op_id / fts_logs.log_id) so the total order is
// deterministic across queries.
//
// To force a provable bm25 tie every row is seeded with IDENTICAL indexed text
// (same term, same frequency, same document length) so each row's bm25 score is
// exactly equal. The op rows are inserted in DESCENDING op_id order so their
// rowid (insertion) order is the REVERSE of their op_id order — this is the
// discriminator: without the tie-breaker SQLite returns equal-rank rows in an
// unspecified order (here rowid/insertion order, o9..o1), so the assertion that
// pages arrive in ascending op_id order fails; with `ORDER BY rank, op_id` they
// arrive o1..o9 and it passes. Each page also feeds a union check that rejects
// any duplicate or skip across the full walk.
func TestSearch_RankTiePaginationStable(t *testing.T) {
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

	// Ops: identical indexed text (name "needle", every other column empty) so
	// bm25 is exactly equal. Inserted DESCENDING by op_id so rowid order is the
	// reverse of op_id order; the tie-breaker must re-sort them to ascending.
	descOpIDs := []string{"o9", "o7", "o5", "o3", "o1"}
	wantOpsAsc := []string{"o1", "o3", "o5", "o7", "o9"}
	for i, id := range descOpIDs {
		seedFTSOp(t, db, opRow{
			id: id, turnID: "t1", sessionID: "rootA", seq: int64(i + 1), kind: "tool", name: "needle",
			startTS: base + int64(i+1)*1000, endTS: base + int64(i+1)*1000 + 100, durationUS: 100, status: "completed",
		}, "")
	}
	// Logs: identical message → equal bm25. log_id is an autoincrement rowid
	// (assigned at insert), so log_id order == insertion order; the union check
	// below pins no-dup/no-skip for the logs side (the tie-breaker keeps the
	// total order stable across the page-1 and offset queries).
	wantLogs := map[int64]bool{}
	for i := 0; i < 5; i++ {
		id := seedFTSLog(t, db, logRow{
			sessionID: "rootA", ts: base + int64(i+1)*1000, severity: "ERR", source: "x", message: "needle",
		})
		wantLogs[id] = true
	}

	// Walk every page one row at a time, recording the ORDER ops arrive in and
	// the SET of ops/logs seen. The order list is the red→green discriminator for
	// the op tie-breaker; the sets reject any duplicate or skip on either side.
	var gotOpsOrder []string
	gotOps := map[string]bool{}
	gotLogs := map[int64]bool{}
	cursor := ""
	for page := 0; page < 100; page++ { // generous cap; loop breaks on empty cursor
		q := "q=needle&limit=1"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		code, body, env := getSearch(t, p, q)
		if code != http.StatusOK {
			t.Fatalf("page %d status=%d env=%+v", page, code, env)
		}
		for _, op := range body.Ops {
			if gotOps[op.OpID] {
				t.Fatalf("op %q DUPLICATED across pages (unstable tie order): order so far=%v", op.OpID, gotOpsOrder)
			}
			gotOps[op.OpID] = true
			gotOpsOrder = append(gotOpsOrder, op.OpID)
		}
		for _, lg := range body.Logs {
			if gotLogs[lg.LogID] {
				t.Fatalf("log %d DUPLICATED across pages (unstable tie order)", lg.LogID)
			}
			gotLogs[lg.LogID] = true
		}
		if body.NextCursor == "" {
			break
		}
		cursor = body.NextCursor
	}

	// Ops arrive in ascending op_id order across the paginated walk. Equal rank
	// for every row means only the op_id tie-breaker can produce this order;
	// without it the order is unspecified (insertion/rowid order here) and this
	// fails — the contract the fix establishes.
	if !equalStrSlice(gotOpsOrder, wantOpsAsc) {
		t.Fatalf("op tie order=%v, want %v (rank-tie rows must order by the unique op_id tie-breaker, stable across offset pages)",
			gotOpsOrder, wantOpsAsc)
	}
	// Logs side: the union is exactly the seeded set — no duplicate, no skip.
	if len(gotLogs) != len(wantLogs) {
		t.Fatalf("logs union n=%d, want %d (a SKIP or DUP under offset pagination): got=%v want=%v",
			len(gotLogs), len(wantLogs), gotLogs, wantLogs)
	}
	for id := range wantLogs {
		if !gotLogs[id] {
			t.Errorf("log %d SKIPPED across all pages", id)
		}
	}
}

// equalStrSlice reports whether two string slices have the same elements in the
// same order (test-local helper for the rank-tie ordering assertion).
func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
