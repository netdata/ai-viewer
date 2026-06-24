package opencode

import (
	"database/sql"
	"strings"
	"testing"
)

// This file pins the SOW-0005 ROUND-5 STORE-layer fixes:
//   - P2-2: the REQUIRED owning-id columns (message.session_id, part.message_id,
//     part.session_id, session_message.session_id) ERROR the delta page on an
//     empty/corrupt value rather than deriving an empty affected session id (which
//     affectedSet.add("") silently drops while the row "succeeds", advancing the
//     cursor past an un-emitted change — a permanent, health-invisible gap). A
//     valid row is unaffected, and affectedSet NEVER receives "".
//
// (P2-1's no-emission-while-a-source-DB-read-tx-is-open fix is pinned in
// review_round5_txclose_test.go.)
//
// SQLite TEXT NOT NULL rejects a NULL but ACCEPTS an empty string ''. opencode's
// owning-id columns are TEXT NOT NULL, so the realistic corruption shape is an
// empty cell, which these tests insert directly.

// insertRowEmptyOwner inserts ONE row into the named table whose owning-id column
// `ownerCol` is the empty string (the corruption shape), with all other required
// columns set to valid values. It uses a fresh read-write handle (the test-fixture
// writer; production never opens opencode.db read-write).
func insertRowEmptyOwner(t *testing.T, path, table, ownerCol string) {
	t.Helper()
	rw, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() { _ = rw.Close() }()

	var stmt string
	var args []any
	switch table {
	case "message":
		// session_id is the only owning id; empty it.
		sid := "ses_ok"
		if ownerCol == "session_id" {
			sid = ""
		}
		stmt = `INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`
		args = []any{"msg_bad", sid, int64(100), int64(100), `{"role":"assistant"}`}
	case "part":
		mid, sid := "msg_ok", "ses_ok"
		if ownerCol == "message_id" {
			mid = ""
		}
		if ownerCol == "session_id" {
			sid = ""
		}
		stmt = `INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`
		args = []any{"prt_bad", mid, sid, int64(100), int64(100), `{"type":"step-start"}`}
	case "session_message":
		sid := "ses_ok"
		if ownerCol == "session_id" {
			sid = ""
		}
		stmt = `INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES (?,?,?,?,?,?,?)`
		args = []any{"evt_bad", sid, "agent-switched", int64(1), int64(100), int64(100), `{}`}
	default:
		t.Fatalf("unsupported table %q", table)
	}
	if _, err := rw.Exec(stmt, args...); err != nil {
		t.Fatalf("insert %s empty %s: %v", table, ownerCol, err)
	}
}

// TestP2_2_EmptyOwningIDErrorsNoCursorAdvance pins, per (table, owning column),
// that a delta row with an EMPTY owning id ERRORS the page (no cursor advance, no
// affected session, error surfaced) rather than silently dropping the change.
func TestP2_2_EmptyOwningIDErrorsNoCursorAdvance(t *testing.T) {
	cases := []struct {
		table    string
		ownerCol string
	}{
		{"message", "session_id"},
		{"part", "message_id"},
		{"part", "session_id"},
		{"session_message", "session_id"},
	}
	for _, tc := range cases {
		t.Run(tc.table+"/"+tc.ownerCol, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path, rw := newEmptyDB(t, dir, "opencode.db")
			if err := rw.Close(); err != nil {
				t.Fatalf("close rw: %v", err)
			}
			insertRowEmptyOwner(t, path, tc.table, tc.ownerCol)
			db, schema := introspect(t, path)

			affected := newAffectedSet()
			sink := &warnSink{}
			onRow := deltaRowHandler(tc.table, schema[tc.table], affected, sink.collect)
			from := TableWatermark{MaxTimeUpdatedMs: 50, MaxTimeUpdatedID: "aaa", MaxIDSeen: "aaa"}
			delta, err := scanTableDelta(ctxBG(), db, schema[tc.table], from, onRow, sink, func(error) {})
			if err == nil {
				t.Fatalf("empty %s.%s must ERROR the page (no silent cursor gap)", tc.table, tc.ownerCol)
			}
			// The refusal must name the missing required column.
			if !strings.Contains(err.Error(), "required column") || !strings.Contains(err.Error(), tc.ownerCol) {
				t.Errorf("error = %v, want a required-column refusal naming %q", err, tc.ownerCol)
			}
			// The cursor must NOT have advanced past the corrupt row.
			if delta.watermark.MaxTimeUpdatedMs != from.MaxTimeUpdatedMs || delta.watermark.MaxTimeUpdatedID != from.MaxTimeUpdatedID {
				t.Errorf("watermark advanced past the corrupt row: got (%d,%q), want input (%d,%q)",
					delta.watermark.MaxTimeUpdatedMs, delta.watermark.MaxTimeUpdatedID, from.MaxTimeUpdatedMs, from.MaxTimeUpdatedID)
			}
			// affectedSet NEVER received "" (nor any id): the row aborted before add.
			if ids := affected.ids(); len(ids) != 0 {
				t.Errorf("affected = %v, want empty (corrupt owning id must not derive a session)", ids)
			}
		})
	}
}

// TestP2_2_ValidOwningIDUnaffected is the control: a row with valid owning ids
// scans cleanly, derives its affected session, and advances the watermark — the
// P2-2 guard does not regress the happy path.
func TestP2_2_ValidOwningIDUnaffected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertAssistantMessage(t, rw, "msg_ok", "ses_ok", 100, 100, 10, 5)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	affected := newAffectedSet()
	sink := &warnSink{}
	onRow := deltaRowHandler("message", schema["message"], affected, sink.collect)
	delta, err := scanTableDelta(ctxBG(), db, schema["message"], TableWatermark{}, onRow, sink, func(error) {})
	if err != nil {
		t.Fatalf("valid row must scan cleanly, got %v", err)
	}
	if ids := affected.ids(); len(ids) != 1 || ids[0] != "ses_ok" {
		t.Fatalf("affected = %v, want [ses_ok]", affected.ids())
	}
	if delta.watermark.MaxTimeUpdatedID != "msg_ok" {
		t.Errorf("watermark id = %q, want msg_ok (valid row advances)", delta.watermark.MaxTimeUpdatedID)
	}
}

// TestP2_2_RequiredOwnerAccessorGuard pins the requiredOwner accessor directly: an
// empty/NULL/absent owning cell returns an error; a valid one returns the value.
// This is the chokepoint the message/part/session_message delta scanners share.
func TestP2_2_RequiredOwnerAccessorGuard(t *testing.T) {
	t.Parallel()
	idx := columnIndex{"id": 0, "session_id": 1}

	// Empty owning id → error.
	dEmpty := &scanDest{holders: []sql.NullString{{String: "x1", Valid: true}, {String: "", Valid: true}}, table: "message"}
	if _, err := requiredOwner(dEmpty, idx, "session_id"); err == nil {
		t.Error("requiredOwner on an empty session_id returned nil error (must refuse the silent gap)")
	}
	// NULL owning id → error.
	dNull := &scanDest{holders: []sql.NullString{{String: "x1", Valid: true}, {Valid: false}}, table: "message"}
	if _, err := requiredOwner(dNull, idx, "session_id"); err == nil {
		t.Error("requiredOwner on a NULL session_id returned nil error")
	}
	// Absent column (not in index) → error.
	if _, err := requiredOwner(dEmpty, columnIndex{"id": 0}, "session_id"); err == nil {
		t.Error("requiredOwner on an absent session_id returned nil error")
	}
	// Valid owning id → value, no error.
	dOK := &scanDest{holders: []sql.NullString{{String: "x1", Valid: true}, {String: "ses_1", Valid: true}}, table: "message"}
	sid, err := requiredOwner(dOK, idx, "session_id")
	if err != nil || sid != "ses_1" {
		t.Errorf("requiredOwner(valid) = (%q,%v), want (ses_1,nil)", sid, err)
	}
}
