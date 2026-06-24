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

func TestLoadAndMapSession_SessionMessageSystemLogs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_switch", "", 1000, 1700, 0)
	if _, err := rw.Exec(`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
		VALUES
		('evt_agent', 'ses_switch', 'agent-switched', 1, 1600, 1600, '{"agent":"reviewer"}'),
		('evt_model', 'ses_switch', 'model-switched', 2, 1700, 1700, '{"model":{"id":"gpt-5","providerID":"openai","variant":"fast"}}')`); err != nil {
		t.Fatalf("insert session_message rows: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	evs, skipped, err := loadAndMapSession(ctxBG(), db, schema, "opencode:test", "ses_switch", silentLogger(), func(error) {})
	if err != nil {
		t.Fatalf("loadAndMapSession: %v", err)
	}
	if skipped {
		t.Fatal("loadAndMapSession reported skipped; want emit")
	}

	logs := logEntries(evs)
	if len(logs) != 2 {
		t.Fatalf("session_message log entries = %d, want 2: %#v", len(logs), logs)
	}
	assertSessionMessageLog(t, logs[0], "evt_agent", "agent-switched", 1, "session agent switched", map[string]any{
		"agent": "reviewer",
	})
	assertSessionMessageLog(t, logs[1], "evt_model", "model-switched", 2, "session model switched", map[string]any{
		"model_id":    "gpt-5",
		"provider_id": "openai",
		"variant":     "fast",
	})
}

func logEntries(evs []canonical.Event) []canonical.LogEntryEvent {
	var out []canonical.LogEntryEvent
	for _, ev := range evs {
		if log, ok := ev.(canonical.LogEntryEvent); ok {
			out = append(out, log)
		}
	}
	return out
}

func assertSessionMessageLog(t *testing.T, log canonical.LogEntryEvent, id, typ string, seq int64, message string, fields map[string]any) {
	t.Helper()
	if log.Message != message {
		t.Fatalf("log message = %q, want %q", log.Message, message)
	}
	if log.Severity != "INF" {
		t.Fatalf("log severity = %q, want INF", log.Severity)
	}
	if log.SessionNativeID != "ses_switch" {
		t.Fatalf("log session = %q, want ses_switch", log.SessionNativeID)
	}
	if log.Extras["session_message_id"] != id {
		t.Fatalf("log session_message_id = %#v, want %q", log.Extras["session_message_id"], id)
	}
	if log.Extras["session_message_type"] != typ {
		t.Fatalf("log session_message_type = %#v, want %q", log.Extras["session_message_type"], typ)
	}
	if log.Extras["seq"] != seq {
		t.Fatalf("log seq = %#v, want %d", log.Extras["seq"], seq)
	}
	if log.Extras["data_sha256"] == "" {
		t.Fatal("log data_sha256 is empty")
	}
	for key, want := range fields {
		if got := log.Extras[key]; got != want {
			t.Fatalf("log extra %s = %#v, want %#v", key, got, want)
		}
	}
}
