package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestClaudeCodeSubAgent_OpChildLinkedViaToolUseId drives the REAL claude-code
// adapter over a synthetic parent + sub-agent sidechain + sidecar `.meta.json`
// through the REAL ingester writer and resolver, and asserts the end-to-end
// op→child linkage holds with the Round-6 re-emit-free design:
//
//	(a) the parent Agent op and the child session BOTH carry the toolUseId stash
//	    in their extras_json (the meta-independent join key), and
//	(b) after the resolver runs, ops.child_session_id is linked to the child
//	    session row — proving the toolUseId bridge works through the real schema.
//
// This is the composition test for P1.6: the adapter stashes the join key on both
// ends; the resolver links them at the DB layer. Mirrors the adapter→ingester
// harness in e2e_test.go / e2e_claude_code_compaction_test.go.
func TestClaudeCodeSubAgent_OpChildLinkedViaToolUseId(t *testing.T) {
	t.Parallel()

	const (
		parentSession = "11111111-1111-4111-8111-111111111111"
		agentID       = "aaa111bbb222ccc"
		toolUseID     = "toolu_agent_e2e"
	)
	root := t.TempDir()
	proj := filepath.Join(root, "-home-user-demo")

	// Parent transcript: a user prompt + an assistant Agent tool_use (the op that
	// spawns the sub-agent). Its tool_use id is the toolUseId join key.
	parentLines := "" +
		`{"type":"user","uuid":"pu1","sessionId":"` + parentSession + `","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z","cwd":"/home/user/src/demo"}` + "\n" +
		`{"type":"assistant","uuid":"pa1","sessionId":"` + parentSession + `","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"` + toolUseID + `","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}` + "\n"
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, parentSession+".jsonl"), []byte(parentLines), 0o644); err != nil {
		t.Fatalf("write parent: %v", err)
	}

	// Child sidechain + sidecar meta (the meta carries the SAME toolUseId).
	subDir := filepath.Join(proj, parentSession, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subagents: %v", err)
	}
	childLines := "" +
		`{"type":"user","uuid":"cu1","isSidechain":true,"agentId":"` + agentID + `","sessionId":"` + parentSession + `","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}` + "\n" +
		`{"type":"assistant","uuid":"ca1","isSidechain":true,"agentId":"` + agentID + `","sessionId":"` + parentSession + `","message":{"id":"cm1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3},"content":[{"type":"text","text":"done"}]},"timestamp":"2026-05-26T10:00:09.000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(subDir, "agent-"+agentID+".jsonl"), []byte(childLines), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "agent-"+agentID+".meta.json"),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+toolUseID+`"}`), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	a, err := claude_code.New(root, canonical.AdapterOptions{Logger: silentLogger()})
	if err != nil {
		t.Fatalf("claude_code.New: %v", err)
	}

	_, db := openTestStore(t)
	sourceID := "claude-code:" + root
	ing, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(50*time.Millisecond),
		WithSourceFormat(sourceID, "claude-code"),
		WithLocation(sourceID, root),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := make(chan canonical.Event)
	scanDone := make(chan struct{})
	var scanErr error
	go func() {
		defer close(scanDone)
		defer close(events)
		scanErr = a.Scan(ctx, nil, events)
	}()
	if err := ing.Submit(sourceID, events); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForScan(t, scanDone, "claude-code sub-agent")
	if scanErr != nil {
		t.Fatalf("Scan: %v", scanErr)
	}
	if !waitFor(20*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions`) == 2
	}) {
		t.Fatalf("session count before Stop = %d, want 2", scanInt(t, db, `SELECT COUNT(*) FROM sessions`))
	}
	if err := ing.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions`); got != 2 {
		t.Fatalf("session count after Stop = %d, want 2", got)
	}

	parentID := canonicalSessionID(sourceID, parentSession)
	childID := canonicalSessionID(sourceID, parentSession+":agent:"+agentID)
	opID := canonicalOpID(canonicalTurnID(parentID, 1), 3)

	// (a) both rows carry the toolUseId stash.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM ops WHERE id=?`, opID); got != toolUseID {
		t.Fatalf("parent Agent op toolUseId stash = %q, want %q", got, toolUseID)
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.toolUseId'),'') FROM sessions WHERE id=?`, childID); got != toolUseID {
		t.Fatalf("child session toolUseId stash = %q, want %q", got, toolUseID)
	}

	// (b) after the resolver runs, ops.child_session_id is linked to the child row.
	if err := ing.ResolveOrphans(ctx); err != nil {
		t.Fatalf("ResolveOrphans: %v", err)
	}
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != childID {
		t.Fatalf("op child_session_id after resolver = %q, want %q (op→child link via toolUseId or childNativeId)", got, childID)
	}
}
