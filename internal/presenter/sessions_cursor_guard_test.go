package presenter

import (
	"net/http"
	"testing"
)

// TestSessions_CursorTamperedOrderRejected pins the explicit sort/order
// guard (codex iter-7): a cursor carrying the CORRECT live fingerprint but a
// tampered `Order` (asc when the live query is desc) must be rejected with 400
// rather than silently accepted. Before the guard only FP was checked, so the
// junk Order slipped through (200); after it the explicit
// cur.Order != f.order check fires first with the ordering-mismatch message —
// mirroring the logs endpoint and bringing sessions in line with presenter.md
// §"Filters, pagination, and cursors". The tamper leaves FP unchanged so the
// fingerprint still matches the live desc query, isolating the new guard.
func TestSessions_CursorTamperedOrderRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedFiveRoots(t, db, base)

	// Mint a real next_cursor under the default DESC ordering.
	code, page1, _ := getSessions(t, p, "limit=2")
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Decode it, flip Order to "asc" (FP untouched so it still matches the
	// live desc query), re-encode.
	cur, err := decodeCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("decode minted cursor: %v", err)
	}
	cur.Order = "asc"
	tampered := cur.encode()

	// Replay under the SAME default desc query: the fingerprint matches but
	// the explicit ordering guard must reject the tampered Order with 400.
	tamperCode, _, env := getSessions(t, p, "limit=2&cursor="+tampered)
	if tamperCode != http.StatusBadRequest {
		t.Fatalf("tampered-order status = %d, want 400 (explicit sort/order guard)", tamperCode)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("tampered-order code = %q, want %q", env.Error.Code, CodeBadRequest)
	}

	// A second tamper: junk Sort (FP still matches) is likewise a 400.
	cur2, err := decodeCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("decode minted cursor (2): %v", err)
	}
	cur2.Sort = "junk"
	tampered2 := cur2.encode()
	junkCode, _, junkEnv := getSessions(t, p, "limit=2&cursor="+tampered2)
	if junkCode != http.StatusBadRequest {
		t.Fatalf("tampered-sort status = %d, want 400 (explicit sort/order guard)", junkCode)
	}
	if junkEnv.Error.Code != CodeBadRequest {
		t.Fatalf("tampered-sort code = %q, want %q", junkEnv.Error.Code, CodeBadRequest)
	}

	// Control: the UNMODIFIED minted cursor still yields the correct next
	// page (s2, s1) — the guard must not break the happy path.
	okCode, page2, _ := getSessions(t, p, "limit=2&cursor="+page1.NextCursor)
	if okCode != http.StatusOK {
		t.Fatalf("unmodified-cursor status = %d, want 200", okCode)
	}
	if len(page2.Items) != 2 || page2.Items[0].ID != "s2" || page2.Items[1].ID != "s1" {
		t.Fatalf("unmodified-cursor page2 = %+v, want [s2 s1]", page2.Items)
	}
}

// TestSessions_CursorToRawMismatchRejected pins the supplied-`to` (toRaw)
// fingerprint binding end-to-end (glm iter-7): the fingerprint encodes the
// operator-SUPPLIED `to` (toRaw), NOT the now-defaulted `to`. A cursor minted
// WITH an explicit ?to and replayed WITHOUT it is a different query and must be
// rejected with 400. The explicit ?to is pinned to the harness clock
// (fixedTime) so this test specifically distinguishes a correct toRaw bind from
// a regression that fingerprints the now-defaulted `to`: under the bug the
// minted `to` (==fixedTime) would equal the replay's now-default (also
// fixedTime), the fingerprints would match, and the stale cursor would be
// wrongly accepted (200). The positive control (replay WITH the same ?to)
// proves the rejection is the changed `to` alone, not a flaky fingerprint.
func TestSessions_CursorToRawMismatchRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedFiveRoots(t, db, base) // s0..s4 at base, base+1k .. base+4k

	// Pin ?to to the harness "now" (fixedTime). The filter is `start_ts <= ?`
	// and every seeded row starts at base = fixedTime-1h, so all five stay in
	// range under both the explicit-`to` and the omitted-`to` (now-default)
	// windows — the ONLY variable is the presence of ?to. Choosing fixedTime
	// makes the buggy now-defaulted fingerprint collide with the omitted-`to`
	// replay, so a passing test proves the bind is on toRaw, not the default.
	to := itoa64(fixedTime.UnixMicro())

	// Mint page 1 (DESC default => s4, s3) WITH an explicit ?to=fixedTime.
	code, page1, _ := getSessions(t, p, "limit=2&to="+to)
	if code != http.StatusOK || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d cursor=%q", code, page1.NextCursor)
	}

	// Replay the cursor WITHOUT ?to: the live request defaults toRaw=nil, so
	// the recomputed toRaw fingerprint differs from the minted one => 400.
	dropCode, _, env := getSessions(t, p, "limit=2&cursor="+page1.NextCursor)
	if dropCode != http.StatusBadRequest {
		t.Fatalf("dropped-to status = %d, want 400 (toRaw fingerprint mismatch)", dropCode)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("dropped-to code = %q, want %q", env.Error.Code, CodeBadRequest)
	}

	// Positive control: replay WITH the SAME ?to => accepted, correct next
	// page (s2, s1). Proves the step-3 rejection is the changed `to`, not flake.
	okCode, page2, _ := getSessions(t, p, "limit=2&to="+to+"&cursor="+page1.NextCursor)
	if okCode != http.StatusOK {
		t.Fatalf("same-to status = %d, want 200", okCode)
	}
	if len(page2.Items) != 2 || page2.Items[0].ID != "s2" || page2.Items[1].ID != "s1" {
		t.Fatalf("same-to page2 = %+v, want [s2 s1]", page2.Items)
	}
}
