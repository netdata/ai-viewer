package opencode

import (
	"database/sql"
	"sort"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the delta-query layer: paged delta SELECTs (watermark advance,
// the LIMIT-1000 boundary, the time_updated tie-break), the old-schema
// id-only fallback, and the affected-session derivation across all four tables.

// scanMessagesFrom pages the message table from a watermark using scanTableDelta
// and returns the changed messageRows + the advanced watermark + row count.
func scanMessagesFrom(t *testing.T, db *sql.DB, schema schemaSet, from TableWatermark) ([]messageRow, tableDelta) {
	t.Helper()
	s := schema["message"]
	idx := newColumnIndex(s)
	n := len(s.Present)
	var got []messageRow
	scan, row := scanMessageRow(idx, n)
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
	if delta.watermark.MaxTimeUpdatedMs != 300 || delta.watermark.MaxID != "msg_3" {
		t.Errorf("advanced watermark = %+v, want {300, msg_3}", delta.watermark)
	}
	if delta.rowCount != 3 {
		t.Errorf("rowCount = %d, want 3", delta.rowCount)
	}

	// From msg_2's watermark: only msg_3.
	got2, _ := scanMessagesFrom(t, db, schema, TableWatermark{MaxTimeUpdatedMs: 200, MaxID: "msg_2"})
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
	got, _ := scanMessagesFrom(t, db, schema, TableWatermark{MaxTimeUpdatedMs: 500, MaxID: "msg_a"})
	if len(got) != 1 || got[0].ID != "msg_b" {
		t.Fatalf("tiebreak from {500,msg_a}: got %v, want [msg_b]", ids(got))
	}

	// From {500, ""} (empty id at that time): both rows return.
	gotBoth, _ := scanMessagesFrom(t, db, schema, TableWatermark{MaxTimeUpdatedMs: 499, MaxID: ""})
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
	if delta.watermark.MaxID != fmtID("msg", total) || delta.watermark.MaxTimeUpdatedMs != int64(total) {
		t.Errorf("final watermark = %+v, want {%d, %s}", delta.watermark, total, fmtID("msg", total))
	}
	// Rows must be globally ordered by (time_updated, id) across page seams.
	for i := 1; i < len(got); i++ {
		if got[i-1].TimeUpdatedMs > got[i].TimeUpdatedMs {
			t.Fatalf("rows not ordered across pages at %d: %d > %d", i, got[i-1].TimeUpdatedMs, got[i].TimeUpdatedMs)
		}
	}
}

// TestDeltaQuery_OldSchemaIDFallback builds a part table WITHOUT time_updated and
// asserts the id-only fallback pages it correctly (buildSelectByID), advancing
// the watermark on MaxID alone with MaxTimeUpdatedMs staying 0.
func TestDeltaQuery_OldSchemaIDFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/old.db"
	rw, err := sql.Open(driverName, rwDSNFor(path))
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	// A part table lacking time_updated (and session_id, to also exercise the
	// message-id fallback path) but keeping the required id/message_id/data.
	stmts := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, slug TEXT NOT NULL,
			directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		// part: required cols are id/message_id/session_id/time_updated/data per
		// requiredColumns; an old schema that lacks time_updated would be rejected
		// by introspectAll. So this fixture keeps session_id+data but we drop
		// time_updated to exercise the buildSelectByID branch directly (bypassing
		// introspectAll's required-column gate by testing the schema in isolation).
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES ('prt_1','msg_1','ses_a',1,'{"type":"text"}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES ('prt_2','msg_1','ses_a',2,'{"type":"text"}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES ('prt_3','msg_1','ses_a',3,'{"type":"text"}')`,
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			_ = rw.Close()
			t.Fatalf("seed old: %v\nstmt: %s", err, s)
		}
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	db := openRO(t, path)
	// Introspect the part table directly (introspectAll would reject it for the
	// missing required time_updated, which is exactly the gate; here we test the
	// fallback SELECT path in isolation against a schema without time_updated).
	ps, err := introspectTable(ctxBG(), db, "part")
	if err != nil {
		t.Fatalf("introspectTable(part): %v", err)
	}
	if ps.has("time_updated") {
		t.Fatal("fixture part table unexpectedly has time_updated")
	}

	var got []partRow
	idx := newColumnIndex(ps)
	scan, row := scanPartRow(idx, len(ps.Present))
	delta, err := scanTableDelta(ctxBG(), db, ps, TableWatermark{}, func(rows *sql.Rows) (rowKey, error) {
		k, err := scan(rows)
		if err != nil {
			return k, err
		}
		got = append(got, *row)
		return k, nil
	})
	if err != nil {
		t.Fatalf("scanTableDelta(old part): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("old-schema fallback returned %d parts, want 3", len(got))
	}
	if delta.watermark.MaxID != "prt_3" {
		t.Errorf("old-schema watermark MaxID = %q, want prt_3", delta.watermark.MaxID)
	}
	if delta.watermark.MaxTimeUpdatedMs != 0 {
		t.Errorf("old-schema watermark MaxTimeUpdatedMs = %d, want 0 (no time_updated)", delta.watermark.MaxTimeUpdatedMs)
	}

	// From {0, prt_1}: id-only fallback returns prt_2 and prt_3.
	got2 := []partRow{}
	scan2, row2 := scanPartRow(idx, len(ps.Present))
	if _, err := scanTableDelta(ctxBG(), db, ps, TableWatermark{MaxID: "prt_1"}, func(rows *sql.Rows) (rowKey, error) {
		k, err := scan2(rows)
		if err != nil {
			return k, err
		}
		got2 = append(got2, *row2)
		return k, nil
	}); err != nil {
		t.Fatalf("scanTableDelta(old part from prt_1): %v", err)
	}
	if len(got2) != 2 || got2[0].ID != "prt_2" || got2[1].ID != "prt_3" {
		t.Fatalf("old-schema fallback from prt_1: got %v, want [prt_2 prt_3]", partIDs(got2))
	}
}

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
		t.Fatalf("collectDeltas: %v", err)
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

// collectDeltasOnly runs collectDeltas with a generously buffered out channel
// (so the few SourceProgress checkpoints never block) and returns the affected
// ids + advanced flag (test convenience).
func collectDeltasOnly(t *testing.T, db *sql.DB, schema schemaSet, cur Cursor) ([]string, bool, error) {
	t.Helper()
	out := make(chan canonical.Event, 4096)
	_, ids, advanced, err := collectDeltas(ctxBG(), db, schema, cur, "opencode:test", out)
	return ids, advanced, err
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
