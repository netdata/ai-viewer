package opencode

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the tree-load layer: loadSession (found / not-found),
// loadSessionTree ordering (messages by (time_created,id), parts by id), and a
// zero-message session loading cleanly — the []messageWithParts contract the
// pure mapper consumes.

// TestLoadSession_FoundAndMissing asserts loadSession returns the row when it
// exists and (zero, false, nil) when it does not.
func TestLoadSession_FoundAndMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "ses_parent", 100, 150, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	s, ok, err := loadSession(ctxBG(), db, schema, "ses_a", func(error) {})
	if err != nil {
		t.Fatalf("loadSession: %v", err)
	}
	if !ok {
		t.Fatal("loadSession(ses_a) ok=false, want true")
	}
	if s.ID != "ses_a" || s.ParentID != "ses_parent" || s.TimeCreatedMs != 100 {
		t.Errorf("loaded session = %+v, want id=ses_a parent=ses_parent created=100", s)
	}

	_, ok, err = loadSession(ctxBG(), db, schema, "ses_nope", func(error) {})
	if err != nil {
		t.Fatalf("loadSession(missing): %v", err)
	}
	if ok {
		t.Error("loadSession(missing) ok=true, want false")
	}
}

// TestLoadSessionTree_Ordering builds a session with two messages (inserted
// out of time order) and parts (inserted out of id order), then asserts
// loadSessionTree returns messages by (time_created,id) and parts by id.
func TestLoadSessionTree_Ordering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	// Insert msg_2 (later) BEFORE msg_1 (earlier) so insertion order != time order.
	insertAssistantMessage(t, rw, "msg_2", "ses_a", 200, 200, 1, 1)
	insertAssistantMessage(t, rw, "msg_1", "ses_a", 100, 100, 1, 1)
	// Parts of msg_1 inserted out of id order: prt_b then prt_a.
	insertPart(t, rw, "prt_b", "msg_1", "ses_a", 110, 110, textBody("second"))
	insertPart(t, rw, "prt_a", "msg_1", "ses_a", 105, 105, stepStartBody())
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	tree, err := loadSessionTree(ctxBG(), db, schema, "ses_a", func(error) {})
	if err != nil {
		t.Fatalf("loadSessionTree: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("tree has %d messages, want 2", len(tree))
	}
	// Messages ordered by (time_created, id): msg_1 (100) then msg_2 (200).
	if tree[0].Message.ID != "msg_1" || tree[1].Message.ID != "msg_2" {
		t.Errorf("message order = [%s %s], want [msg_1 msg_2]", tree[0].Message.ID, tree[1].Message.ID)
	}
	// Parts of msg_1 ordered by id: prt_a then prt_b.
	parts := tree[0].Parts
	if len(parts) != 2 || parts[0].ID != "prt_a" || parts[1].ID != "prt_b" {
		t.Errorf("part order = %v, want [prt_a prt_b]", partIDs(parts))
	}
	// msg_2 has no parts.
	if len(tree[1].Parts) != 0 {
		t.Errorf("msg_2 parts = %d, want 0", len(tree[1].Parts))
	}
}

// TestLoadSessionTree_ZeroMessages asserts a session with no messages loads as an
// empty tree (not an error), so mapSession emits just the SessionStarted.
func TestLoadSessionTree_ZeroMessages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_empty", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	tree, err := loadSessionTree(ctxBG(), db, schema, "ses_empty", func(error) {})
	if err != nil {
		t.Fatalf("loadSessionTree(empty): %v", err)
	}
	if len(tree) != 0 {
		t.Errorf("empty-session tree has %d messages, want 0", len(tree))
	}

	// And loadAndMapSession over it yields exactly one SessionStarted, no more.
	evs, skipped, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_empty", silentLogger(), func(error) {})
	if err != nil {
		t.Fatalf("loadAndMapSession(empty): %v", err)
	}
	if skipped {
		t.Fatal("loadAndMapSession(empty) reported skipped; want emit")
	}
	if n := countKind(evs, canonical.EvSessionStarted); n != 1 {
		t.Errorf("empty session emitted %d SessionStarted, want 1", n)
	}
}
