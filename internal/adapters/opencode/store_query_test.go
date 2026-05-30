package opencode

import (
	"database/sql"
	"sort"
	"strings"
	"testing"
)

// This file pins the delta-query layer: paged delta SELECTs (watermark advance,
// the LIMIT-1000 boundary, the time_updated tie-break) and the affected-session
// derivation across all four tables. time_updated is a required column, so there
// is no id-only delta fallback (SOW-0005 P3.1).

// scanMessagesFrom pages the message table from a watermark using scanTableDelta
// and returns the changed messageRows + the advanced watermark + row count.
func scanMessagesFrom(t *testing.T, db *sql.DB, schema schemaSet, from TableWatermark) ([]messageRow, tableDelta) {
	t.Helper()
	s := schema["message"]
	idx := newColumnIndex(s)
	n := len(s.Present)
	var got []messageRow
	scan, row := scanMessageRow(idx, n, nil)
	delta, err := scanTableDelta(ctxBG(), db, s, from, func(rows *sql.Rows) (rowKey, error) {
		k, err := scan(rows)
		if err != nil {
			return k, err
		}
		got = append(got, *row)
		return k, nil
	})
	if err != nil {
		t.Fatalf("scanTableDelta(message): %v", err)
	}
	return got, delta
}

// TestDeltaQuery_WatermarkFilters asserts rows past the watermark are returned
// and rows at/under it are not, and that the advanced watermark equals the max
// (time_updated, id) seen.
func TestDeltaQuery_WatermarkFilters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_1", "ses_a", 100, 100, 10, 5)
	insertAssistantMessage(t, rw, "msg_2", "ses_a", 200, 200, 10, 5)
	insertAssistantMessage(t, rw, "msg_3", "ses_a", 300, 300, 10, 5)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)

	// From zero: all three.
	got, delta := scanMessagesFrom(t, db, schema, TableWatermark{})
	if len(got) != 3 {
		t.Fatalf("from zero: got %d messages, want 3", len(got))
	}
	if delta.watermark.MaxTimeUpdatedMs != 300 || delta.watermark.MaxTimeUpdatedID != "msg_3" {
		t.Errorf("advanced paging position = %+v, want {300, msg_3}", delta.watermark)
	}
	// The monotonic high-water also reaches the greatest id paged (P1-A).
	if delta.watermark.MaxIDSeen != "msg_3" {
		t.Errorf("advanced MaxIDSeen = %q, want msg_3", delta.watermark.MaxIDSeen)
	}
	if delta.rowCount != 3 {
		t.Errorf("rowCount = %d, want 3", delta.rowCount)
	}

	// From msg_2's watermark: only msg_3.
	got2, _ := scanMessagesFrom(t, db, schema, TableWatermark{MaxTimeUpdatedMs: 200, MaxTimeUpdatedID: "msg_2"})
	if len(got2) != 1 || got2[0].ID != "msg_3" {
		t.Fatalf("from {200,msg_2}: got %+v, want [msg_3]", ids(got2))
	}
}

// TestDeltaQuery_TieBreak asserts the (time_updated = :u AND id > :id) tiebreak:
// two rows share a time_updated; from {tu, firstID} only the higher id returns.
func TestDeltaQuery_TieBreak(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 100, 100, 0)
	// Same time_updated=500, different ids — the Drizzle single-transaction case.
	insertAssistantMessage(t, rw, "msg_a", "ses_a", 500, 500, 1, 1)
	insertAssistantMessage(t, rw, "msg_b", "ses_a", 500, 500, 1, 1)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	// From {500, msg_a}: the tiebreak must return only msg_b (id > msg_a at the
	// same time_updated), NOT re-return msg_a.
	got, _ := scanMessagesFrom(t, db, schema, TableWatermark{MaxTimeUpdatedMs: 500, MaxTimeUpdatedID: "msg_a"})
	if len(got) != 1 || got[0].ID != "msg_b" {
		t.Fatalf("tiebreak from {500,msg_a}: got %v, want [msg_b]", ids(got))
	}

	// From {500, ""} (empty id at that time): both rows return.
	gotBoth, _ := scanMessagesFrom(t, db, schema, TableWatermark{MaxTimeUpdatedMs: 499, MaxTimeUpdatedID: ""})
	if len(gotBoth) != 2 {
		t.Fatalf("from {499,\"\"}: got %d, want 2", len(gotBoth))
	}
}

// TestDeltaQuery_PagesBeyond1000 inserts more than the LIMIT-1000 page size and
// asserts every row is returned across pages and the watermark equals the max.
func TestDeltaQuery_PagesBeyond1000(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	const total = 2500
	tx, err := rw.Begin()
	if err != nil {
		t.Fatalf("begin bulk: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`)
	if err != nil {
		t.Fatalf("prepare bulk: %v", err)
	}
	for i := 1; i <= total; i++ {
		// Monotonic time_updated AND lexicographic id so order is unambiguous.
		if _, err := stmt.Exec(fmtID("msg", i), "ses_a", int64(i), int64(i), `{"role":"assistant"}`); err != nil {
			t.Fatalf("bulk insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit bulk: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)
	got, delta := scanMessagesFrom(t, db, schema, TableWatermark{})
	if len(got) != total {
		t.Fatalf("paged %d messages across pages, want %d", len(got), total)
	}
	if delta.rowCount != total {
		t.Errorf("rowCount = %d, want %d", delta.rowCount, total)
	}
	if delta.watermark.MaxTimeUpdatedID != fmtID("msg", total) || delta.watermark.MaxTimeUpdatedMs != int64(total) || delta.watermark.MaxIDSeen != fmtID("msg", total) {
		t.Errorf("final watermark = %+v, want paging {%d, %s} + MaxIDSeen %s", delta.watermark, total, fmtID("msg", total), fmtID("msg", total))
	}
	// Rows must be globally ordered by (time_updated, id) across page seams.
	for i := 1; i < len(got); i++ {
		if got[i-1].TimeUpdatedMs > got[i].TimeUpdatedMs {
			t.Fatalf("rows not ordered across pages at %d: %d > %d", i, got[i-1].TimeUpdatedMs, got[i].TimeUpdatedMs)
		}
	}
}

// NOTE: the old TestDeltaQuery_OldSchemaIDFallback was removed with the
// buildSelectByID fallback it exercised (SOW-0005 P3.1). time_updated is a
// required column on every tracked table (introspectAll fails fast without it),
// so the id-only delta path was unreachable in production and its
// introspectAll-bypassing isolation test pinned dead code.

// TestAffectedSessions_AllTables asserts the affected-session derivation across
// every table: a changed session, message, part (denormalized session_id), and
// session_message each resolve to the right session id, and a session touched by
// multiple tables dedupes to one.
func TestAffectedSessions_AllTables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	insertSession(t, rw, "ses_b", "", 1, 1, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", 2, 2, 1, 1)
	insertPart(t, rw, "prt_a", "msg_a", "ses_a", 3, 3, textBody("x")) // touches ses_a again (dedupe)
	insertPart(t, rw, "prt_b", "msg_b", "ses_b", 4, 4, textBody("y"))
	_, err := rw.Exec(`INSERT INTO session_message (id, session_id, type, time_created, time_updated, data)
		VALUES ('evt_b','ses_b','model-switched',5,5,'{}')`)
	if err != nil {
		t.Fatalf("insert session_message: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)
	next, advanced, err := collectDeltasOnly(t, db, schema, newCursor())
	if err != nil {
		t.Fatalf("collectDeltasOnly: %v", err)
	}
	if !advanced {
		t.Fatal("expected advanced=true with new rows")
	}
	want := map[string]bool{"ses_a": true, "ses_b": true}
	got := map[string]bool{}
	for _, id := range next {
		got[id] = true
	}
	for id := range want {
		if !got[id] {
			t.Errorf("affected set missing %q (got %v)", id, sortedIDsTest(next))
		}
	}
	if len(next) != 2 {
		t.Errorf("affected set = %v, want exactly 2 (deduped)", sortedIDsTest(next))
	}
}

// collectDeltasOnly pages every tracked table forward from cur via the per-table
// deltaRowHandler, accumulating the combined affected-session set (first-seen
// order) and whether any watermark advanced — the affected-derivation half of the
// change pipeline, isolated for the affected-set test. It mirrors what
// batchProcessor.pageBatch does per table but across ALL tables into one set
// (the test only inspects the derived session set, not emission/checkpointing).
func collectDeltasOnly(t *testing.T, db *sql.DB, schema schemaSet, cur Cursor) ([]string, bool, error) {
	t.Helper()
	affected := newAffectedSet()
	msgSession := map[string]string{}
	advanced := false
	for _, table := range trackedTables {
		s := schema[table]
		from := cur.Tables[table]
		onRow := deltaRowHandler(ctxBG(), db, table, s, affected, msgSession, func(error) {})
		delta, err := scanTableDelta(ctxBG(), db, s, from, onRow)
		if err != nil {
			return affected.ids(), advanced, err
		}
		if delta.rowCount > 0 && watermarkAdvanced(from, delta.watermark) {
			advanced = true
		}
	}
	return affected.ids(), advanced, nil
}

// ids extracts message ids for assertion messages.
func ids(msgs []messageRow) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

// partIDs extracts part ids for assertion messages.
func partIDs(parts []partRow) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.ID
	}
	return out
}

// sortedIDsTest returns a sorted copy of session ids for stable assertion
// messages.
func sortedIDsTest(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// TestSessionMessage_UnknownTypeWarns is the P2.7 / spec Edge #1 proof: scanning a
// session_message delta emits exactly one structured WARN for an UNRECOGNIZED
// type and NONE for a known type, while BOTH rows still drive the affected-session
// set (the warn never blocks the cycle). The known type ("model-switched") and the
// unknown ("planned-future-thing") are scanned in one delta pass via the
// session_message deltaRowHandler with a warn-capturing onError.
func TestSessionMessage_UnknownTypeWarns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// Two session_message rows: a KNOWN type (no warn) and an UNKNOWN one (warn).
	if _, err := rw.Exec(`INSERT INTO session_message (id, session_id, type, time_created, time_updated, data)
		VALUES ('evt_known','ses_a','model-switched',1,1,'{}')`); err != nil {
		t.Fatalf("insert known: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO session_message (id, session_id, type, time_created, time_updated, data)
		VALUES ('evt_unknown','ses_b','planned-future-thing',2,2,'{}')`); err != nil {
		t.Fatalf("insert unknown: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db, schema := introspect(t, path)
	var ce collectErrs
	affected := newAffectedSet()
	s := schema["session_message"]
	onRow := deltaRowHandler(ctxBG(), db, "session_message", s, affected, map[string]string{}, ce.onError)
	if _, err := scanTableDelta(ctxBG(), db, s, TableWatermark{}, onRow); err != nil {
		t.Fatalf("scanTableDelta(session_message): %v", err)
	}

	// Exactly one WARN, naming the unknown type; none for the known type.
	if ce.count() != 1 {
		t.Fatalf("session_message scan produced %d warnings, want exactly 1 (only the unknown type)", ce.count())
	}
	ce.mu.Lock()
	msg := ce.errs[0].Error()
	ce.mu.Unlock()
	if !strings.Contains(msg, "planned-future-thing") || !strings.Contains(msg, "unknown session_message type") {
		t.Errorf("warn = %q, want one naming the unknown type", msg)
	}
	// Both rows still drove the affected set (the warn does not skip the session).
	got := map[string]bool{}
	for _, id := range affected.ids() {
		got[id] = true
	}
	if !got["ses_a"] || !got["ses_b"] {
		t.Errorf("affected set = %v, want both ses_a and ses_b", affected.ids())
	}
}
