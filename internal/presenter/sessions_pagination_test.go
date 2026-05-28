package presenter

import (
	"database/sql"
	"net/http"
	"testing"
)

// seedFiveRoots seeds five root sessions s0..s4 with distinct ascending
// start timestamps under one source, so DESC pagination yields s4..s0.
// Shared by the keyset and order-mismatch pagination tests.
func seedFiveRoots(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	for i := 0; i < 5; i++ {
		id := "s" + itoa64(int64(i))
		seedSession(t, db, sessionRow{
			id: id, sourceID: "src1", nativeID: "n" + id, rootID: id, kind: "root",
			agent: "a", model: "m", provider: "p", status: "completed",
			startTS: base + int64(i)*1_000,
		})
	}
}

// assertNoOverlap fails if any id repeats across the given pages or if the
// total distinct count differs from want.
func assertNoOverlap(t *testing.T, want int, pages ...sessionListBody) {
	t.Helper()
	seen := map[string]bool{}
	for _, pg := range pages {
		for _, it := range pg.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate id %q across pages", it.ID)
			}
			seen[it.ID] = true
		}
	}
	if len(seen) != want {
		t.Fatalf("saw %d distinct ids across pages, want %d", len(seen), want)
	}
}

// TestSessions_PaginationKeyset asserts limit + next_cursor round-trips
// to the next page with no overlap and no gap.
func TestSessions_PaginationKeyset(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedFiveRoots(t, db, base)

	// Page 1: limit 2, default DESC order => s4, s3.
	code, page1, _ := getSessions(t, p, "limit=2")
	if code != http.StatusOK {
		t.Fatalf("page1 status = %d", code)
	}
	if len(page1.Items) != 2 || page1.Items[0].ID != "s4" || page1.Items[1].ID != "s3" {
		t.Fatalf("page1 = %+v, want [s4 s3]", page1.Items)
	}
	if page1.NextCursor == "" {
		t.Fatal("page1 next_cursor empty, want a cursor")
	}

	// Page 2 via cursor => s2, s1.
	_, page2, _ := getSessions(t, p, "limit=2&cursor="+page1.NextCursor)
	if len(page2.Items) != 2 || page2.Items[0].ID != "s2" || page2.Items[1].ID != "s1" {
		t.Fatalf("page2 = %+v, want [s2 s1]", page2.Items)
	}

	// Page 3 via cursor => s0, no next_cursor.
	_, page3, _ := getSessions(t, p, "limit=2&cursor="+page2.NextCursor)
	if len(page3.Items) != 1 || page3.Items[0].ID != "s0" {
		t.Fatalf("page3 items = %+v, want [s0]", page3.Items)
	}
	if page3.NextCursor != "" {
		t.Fatalf("page3 next_cursor = %q, want empty (last page)", page3.NextCursor)
	}

	assertNoOverlap(t, 5, page1, page2, page3)
}

// TestSessions_CursorOrderMismatch400 asserts a cursor minted under one
// order cannot be replayed under a different order: that would flip the
// row-value comparison and silently duplicate/skip rows, so the server
// rejects it with 400. Replaying with the SAME order yields the correct
// next page.
func TestSessions_CursorOrderMismatch400(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedFiveRoots(t, db, base)

	// Page 1 under default DESC => cursor bound to (start_ts, desc).
	code, page1, _ := getSessions(t, p, "limit=2")
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Replay the desc-bound cursor with order=asc => 400, no corrupt page.
	mismatchCode, _, env := getSessions(t, p, "limit=2&order=asc&cursor="+page1.NextCursor)
	if mismatchCode != http.StatusBadRequest {
		t.Fatalf("order-mismatch status = %d, want 400", mismatchCode)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("order-mismatch code = %q, want %q", env.Error.Code, CodeBadRequest)
	}

	// Replay with the matching order => correct next page (s2, s1).
	matchCode, page2, _ := getSessions(t, p, "limit=2&order=desc&cursor="+page1.NextCursor)
	if matchCode != http.StatusOK {
		t.Fatalf("matching-order status = %d, want 200", matchCode)
	}
	if len(page2.Items) != 2 || page2.Items[0].ID != "s2" || page2.Items[1].ID != "s1" {
		t.Fatalf("matching-order page2 = %+v, want [s2 s1]", page2.Items)
	}
}

// TestSessions_CursorMalformed400 asserts structurally-invalid cursors
// (partial, unknown-field, trailing-junk, bad-base64) are 400, while an
// explicit empty cursor is treated as "first page".
func TestSessions_CursorMalformed400(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	bad := []struct {
		name, query string
	}{
		{"bad base64", "cursor=not-base64!!"},
		{"empty object", "cursor=" + b64Cursor(t, `{}`)},
		{"missing id", "cursor=" + b64Cursor(t, `{"ts":1,"sort":"start_ts","order":"desc"}`)},
		{"unknown field", "cursor=" + b64Cursor(t, `{"ts":1,"id":"x","sort":"start_ts","order":"desc","x":1}`)},
		{"trailing junk", "cursor=" + b64Cursor(t, `{"ts":1,"id":"x","sort":"start_ts","order":"desc"} z`)},
	}
	for _, tc := range bad {
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

	// Explicit empty cursor = first page, not malformed.
	code, body, _ := getSessions(t, p, "cursor=")
	if code != http.StatusOK {
		t.Fatalf("empty cursor status = %d, want 200", code)
	}
	if len(body.Items) == 0 {
		t.Fatal("empty cursor should return the first page, got 0 items")
	}
}

// TestSessions_CursorFingerprintGroupMismatch400 asserts a cursor minted
// under group=root cannot be replayed under group=all: the keyset
// watermark is meaningless against the different (larger) result set, so
// the server rejects it with 400 rather than silently skipping the child
// rows that sort above the cursor.
func TestSessions_CursorFingerprintGroupMismatch400(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base) // rootA + two child sessions
	// A second root so group=root has 2 rows (=> a cursor + a next page),
	// while group=all has 4 (the two roots + two children) — a genuinely
	// different result set that the cursor's keyset watermark cannot span.
	seedSession(t, db, sessionRow{
		id: "rootB", sourceID: "src1", nativeID: "nB", rootID: "rootB", kind: "root",
		agent: "nedi", model: "m", provider: "p", status: "completed",
		startTS: base + 10_000,
	})

	// Page 1 under group=root => cursor bound to the root-only query.
	code, page1, _ := getSessions(t, p, "group=root&limit=1")
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Replay the root-bound cursor with group=all => 400, no corrupt page.
	mismatchCode, _, env := getSessions(t, p, "group=all&limit=1&cursor="+page1.NextCursor)
	if mismatchCode != http.StatusBadRequest {
		t.Fatalf("group-mismatch status = %d, want 400", mismatchCode)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("group-mismatch code = %q, want %q", env.Error.Code, CodeBadRequest)
	}

	// Replay with the matching group => accepted (200), valid next page.
	matchCode, _, _ := getSessions(t, p, "group=root&limit=1&cursor="+page1.NextCursor)
	if matchCode != http.StatusOK {
		t.Fatalf("matching-group status = %d, want 200", matchCode)
	}
}

// TestSessions_CursorFingerprintFilterChange asserts the fingerprint binds
// the array filters too: minting under models=a then replaying with a
// superset (models=a,b) is a 400, while replaying with the identical
// filter yields the correct next page, and replaying with the SAME SET in
// a different order (models=b,a) is accepted because the fingerprint
// normalizes (sorts) the slice.
func TestSessions_CursorFingerprintFilterChange(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	// Two models "a" and "b"; three sessions on "a" so a cursor + a next page exist.
	for i := 0; i < 3; i++ {
		id := "a" + itoa64(int64(i))
		seedSession(t, db, sessionRow{
			id: id, sourceID: "src1", nativeID: "n" + id, rootID: id, kind: "root",
			agent: "ag", model: "a", provider: "p", status: "completed",
			startTS: base + int64(i)*1_000,
		})
	}
	seedSession(t, db, sessionRow{
		id: "b0", sourceID: "src1", nativeID: "nb0", rootID: "b0", kind: "root",
		agent: "ag", model: "b", provider: "p", status: "completed", startTS: base + 9_000,
	})

	// Page 1 under models=a => cursor bound to the {a} filter.
	code, page1, _ := getSessions(t, p, "models=a&limit=1")
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Replay with a superset models=a,b => 400 (different result set).
	supersetCode, _, env := getSessions(t, p, "models=a,b&limit=1&cursor="+page1.NextCursor)
	if supersetCode != http.StatusBadRequest {
		t.Fatalf("filter-superset status = %d, want 400", supersetCode)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("filter-superset code = %q, want %q", env.Error.Code, CodeBadRequest)
	}

	// Replay with the identical filter => correct next page.
	sameCode, page2, _ := getSessions(t, p, "models=a&limit=1&cursor="+page1.NextCursor)
	if sameCode != http.StatusOK {
		t.Fatalf("identical-filter status = %d, want 200", sameCode)
	}
	if len(page2.Items) != 1 {
		t.Fatalf("identical-filter page2 items = %d, want 1", len(page2.Items))
	}
}

// TestSessions_CursorFingerprintTimeWindow asserts the fingerprint binds
// the supplied time window: minting under ?from=X then replaying with a
// different ?from is a 400, while replaying with the identical ?from
// yields the next page. This also exercises the supplied-bound (non-nil)
// path of the fingerprint's microsecond rendering.
func TestSessions_CursorFingerprintTimeWindow(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedFiveRoots(t, db, base) // s0..s4 at base, base+1k .. base+4k

	from := itoa64(base) // include all five rows.

	// Page 1 with an explicit from => cursor bound to that window.
	code, page1, _ := getSessions(t, p, "from="+from+"&limit=2")
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Replay with a different from => 400 (different window => different set).
	other := itoa64(base + 2_000)
	mismatchCode, _, env := getSessions(t, p, "from="+other+"&limit=2&cursor="+page1.NextCursor)
	if mismatchCode != http.StatusBadRequest {
		t.Fatalf("from-change status = %d, want 400", mismatchCode)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("from-change code = %q, want %q", env.Error.Code, CodeBadRequest)
	}

	// Replay with the identical from => accepted, correct next page (s2,s1).
	matchCode, page2, _ := getSessions(t, p, "from="+from+"&limit=2&cursor="+page1.NextCursor)
	if matchCode != http.StatusOK {
		t.Fatalf("identical-from status = %d, want 200", matchCode)
	}
	if len(page2.Items) != 2 || page2.Items[0].ID != "s2" || page2.Items[1].ID != "s1" {
		t.Fatalf("identical-from page2 = %+v, want [s2 s1]", page2.Items)
	}
}

// TestSessions_ControlCharFilterRejected asserts the full GET /api/sessions
// path rejects a filter value carrying a control character with 400
// BAD_REQUEST (defense in depth). %1E is the percent-encoded
// record separator that the old fingerprint scheme used as an element
// delimiter; the parser now rejects it before it can reach the SQL or the
// cursor fingerprint.
func TestSessions_ControlCharFilterRejected(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	for _, q := range []string{"models=a%1Eb", "agents=x%1Fy", "q=foo%1Ebar"} {
		t.Run(q, func(t *testing.T) {
			code, _, env := getSessions(t, p, q)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
			}
		})
	}
}

// TestSessions_ControlCharRawBeforeTrim pins that the control-char
// check runs on the RAW query value BEFORE TrimSpace. A leading/trailing
// control byte that is also whitespace (\t=0x09, \n=0x0A, \r=0x0D) would be
// erased by a trim-first order and silently accepted; checking the raw value
// catches it. The positive control (?q=%20abc%20) proves ordinary spaces are
// still trimmed and accepted — only control bytes are rejected.
func TestSessions_ControlCharRawBeforeTrim(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	reject := []string{
		"q=%09abc",    // leading TAB on q — trimmed away under the old order
		"q=abc%09",    // trailing TAB on q
		"q=%0Aabc",    // leading LF on q
		"models=%09a", // leading TAB on an array value
		"agents=a%0D", // trailing CR on an array value
	}
	for _, q := range reject {
		t.Run("reject/"+q, func(t *testing.T) {
			code, _, env := getSessions(t, p, q)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (raw control byte before trim)", code)
			}
			if env.Error.Code != CodeBadRequest {
				t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
			}
		})
	}

	// Positive control: spaces (0x20) are NOT control bytes; they are still
	// trimmed and the request succeeds.
	code, _, _ := getSessions(t, p, "q=%20abc%20")
	if code != http.StatusOK {
		t.Fatalf("space-padded q status = %d, want 200 (spaces are trimmed, not control)", code)
	}
}

// TestSessions_CursorFingerprintOrderInsensitive asserts a multi-value
// filter replayed in a DIFFERENT order but with the SAME SET is accepted:
// the fingerprint sorts each array dimension before encoding, so
// models=a,b and models=b,a fingerprint identically.
func TestSessions_CursorFingerprintOrderInsensitive(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	// Three sessions across models a and b so {a,b} yields a page + a next page.
	specs := []struct {
		id, model string
		off       int64
	}{
		{"x0", "a", 0}, {"x1", "b", 1}, {"x2", "a", 2},
	}
	for _, s := range specs {
		seedSession(t, db, sessionRow{
			id: s.id, sourceID: "src1", nativeID: "n" + s.id, rootID: s.id, kind: "root",
			agent: "ag", model: s.model, provider: "p", status: "completed",
			startTS: base + s.off*1_000,
		})
	}

	// Mint under models=a,b.
	code, page1, _ := getSessions(t, p, "models=a,b&limit=1")
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Replay with models=b,a (reordered, same set) => accepted.
	reorderCode, page2, _ := getSessions(t, p, "models=b,a&limit=1&cursor="+page1.NextCursor)
	if reorderCode != http.StatusOK {
		t.Fatalf("reordered-filter status = %d, want 200 (fingerprint is order-insensitive)", reorderCode)
	}
	if len(page2.Items) != 1 {
		t.Fatalf("reordered-filter page2 items = %d, want 1", len(page2.Items))
	}
}
