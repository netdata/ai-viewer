package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// session_related_test.go — tests for GET /api/sessions/:id/related (SOW-0071).
// The heuristic cross-harness link: a session from a DIFFERENT harness that
// started in the same cwd while this session was running.

// relatedBody mirrors GET /api/sessions/:id/related.
type relatedBody struct {
	Related []struct {
		ID           string `json:"id"`
		SourceFormat string `json:"source_format"`
		AgentName    string `json:"agent_name"`
		Status       string `json:"status"`
		Reason       string `json:"reason"`
	} `json:"related"`
}

func getRelated(t *testing.T, p *Presenter, id string) (int, relatedBody, errorEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/related", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body relatedBody
	var env errorEnvelope
	raw := rr.Body.Bytes()
	if len(raw) > 0 {
		if rr.Code >= 400 {
			_ = json.Unmarshal(raw, &env)
		} else if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body: %v (raw=%q)", err, raw)
		}
	}
	return rr.Code, body, env
}

// TestRelated_FindsCrossHarnessLink pins AC1: a session from a different harness
// that started in the same cwd during the current session's run is detected as
// "possibly related."
func TestRelated_FindsCrossHarnessLink(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcClaude", "claude-code", "/tmp/a", base)
	seedSource(t, db, "srcCodex", "codex", "/tmp/b", base)
	// A claude-code session running from 1000 to 9000 in /tmp/work.
	seedSessionWithCwd(t, db, sessionRow{
		id: "claudeSess", sourceID: "srcClaude", nativeID: "nC", rootID: "claudeSess",
		kind: "root", agent: "claude", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 1,
	}, "/tmp/work")
	// A codex session in the SAME cwd that started during the claude session.
	seedSessionWithCwd(t, db, sessionRow{
		id: "codexSess", sourceID: "srcCodex", nativeID: "nCx", rootID: "codexSess",
		kind: "root", agent: "codex", status: "completed",
		startTS: base + 3_000, endTS: base + 7_000, opCount: 1,
	}, "/tmp/work")

	code, body, _ := getRelated(t, p, "claudeSess")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Related) != 1 {
		t.Fatalf("related = %d, want 1 (the codex session)", len(body.Related))
	}
	r := body.Related[0]
	if r.ID != "codexSess" {
		t.Errorf("related id = %q, want codexSess", r.ID)
	}
	if r.SourceFormat != "codex" {
		t.Errorf("related format = %q, want codex", r.SourceFormat)
	}
	if r.Reason == "" {
		t.Error("related reason is empty")
	}
}

// TestRelated_ExcludesSameHarness: a same-harness session in the same cwd is NOT
// a cross-harness link.
func TestRelated_ExcludesSameHarness(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcA", "claude-code", "/tmp/a", base)
	seedSource(t, db, "srcB", "claude-code", "/tmp/b", base)
	seedSessionWithCwd(t, db, sessionRow{
		id: "first", sourceID: "srcA", nativeID: "nA", rootID: "first",
		kind: "root", agent: "claude", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 1,
	}, "/tmp/work")
	seedSessionWithCwd(t, db, sessionRow{
		id: "second", sourceID: "srcB", nativeID: "nB", rootID: "second",
		kind: "root", agent: "claude", status: "completed",
		startTS: base + 3_000, endTS: base + 7_000, opCount: 1,
	}, "/tmp/work")

	code, body, _ := getRelated(t, p, "first")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Related) != 0 {
		t.Fatalf("related = %d, want 0 (same harness is not cross-harness)", len(body.Related))
	}
}

// TestRelated_ExcludesNonOverlapping: a different-harness session that started
// AFTER the current session ended is NOT related.
func TestRelated_ExcludesNonOverlapping(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcA", "claude-code", "/tmp/a", base)
	seedSource(t, db, "srcB", "codex", "/tmp/b", base)
	seedSessionWithCwd(t, db, sessionRow{
		id: "early", sourceID: "srcA", nativeID: "nA", rootID: "early",
		kind: "root", agent: "claude", status: "completed",
		startTS: base + 1_000, endTS: base + 2_000, opCount: 1,
	}, "/tmp/work")
	seedSessionWithCwd(t, db, sessionRow{
		id: "late", sourceID: "srcB", nativeID: "nB", rootID: "late",
		kind: "root", agent: "codex", status: "completed",
		startTS: base + 5_000, endTS: base + 9_000, opCount: 1,
	}, "/tmp/work")

	code, body, _ := getRelated(t, p, "early")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Related) != 0 {
		t.Fatalf("related = %d, want 0 (started after the session ended)", len(body.Related))
	}
}

// TestRelated_NotFound: an unknown id → 404.
func TestRelated_NotFound(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())

	code, _, env := getRelated(t, p, "does-not-exist")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotFound)
	}
}

// TestRelated_Empty: a valid session with no cross-harness links → empty array.
func TestRelated_Empty(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcSolo", "claude-code", "/tmp/a", base)
	seedSessionWithCwd(t, db, sessionRow{
		id: "solo", sourceID: "srcSolo", nativeID: "nS", rootID: "solo",
		kind: "root", agent: "claude", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 1,
	}, "/tmp/solo")

	code, body, _ := getRelated(t, p, "solo")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Related == nil {
		t.Fatal("related must serialize as [] not null")
	}
	if len(body.Related) != 0 {
		t.Fatalf("related = %d, want 0 (no cross-harness links)", len(body.Related))
	}
}

// TestRelated_MethodNotAllowed: non-GET/HEAD is rejected.
func TestRelated_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/x/related", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestRelated_NullCwdDoesNotMatch: two sessions with a NULL cwd (the schema
// default) must NOT match each other — SQL NULL = NULL is UNKNOWN, so the JOIN
// ON r.cwd = s.cwd excludes them. This is the conservative choice: sessions
// without a recorded cwd should not be flagged as "possibly related" based on
// a shared (absent) working directory.
func TestRelated_NullCwdDoesNotMatch(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcA", "claude-code", "/tmp/a", base)
	seedSource(t, db, "srcB", "codex", "/tmp/b", base)
	// Both sessions have cwd = NULL (seedSession without cwd). Different
	// harnesses, overlapping time — but no cwd to match on.
	seedSession(t, db, sessionRow{
		id: "nullA", sourceID: "srcA", nativeID: "nA", rootID: "nullA",
		kind: "root", agent: "claude", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "nullB", sourceID: "srcB", nativeID: "nB", rootID: "nullB",
		kind: "root", agent: "codex", status: "completed",
		startTS: base + 3_000, endTS: base + 7_000, opCount: 1,
	})

	code, body, _ := getRelated(t, p, "nullA")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Related) != 0 {
		t.Fatalf("related = %d, want 0 (NULL cwd must not match — SQL NULL != NULL)", len(body.Related))
	}
}

// TestRelated_RunningSessionUsesNowForEndTs: a still-running session (end_ts =
// NULL) finds related sessions via COALESCE(end_ts, now). A candidate that
// started after the session started is detected because the window extends to
// the injected "now".
func TestRelated_RunningSessionUsesNowForEndTs(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcA", "claude-code", "/tmp/a", base)
	seedSource(t, db, "srcB", "codex", "/tmp/b", base)
	// A still-running session (end_ts = 0 → NULL in the seed). Its window
	// extends to "now" (the injected fixedTime, well past base).
	seedSessionWithCwd(t, db, sessionRow{
		id: "running", sourceID: "srcA", nativeID: "nR", rootID: "running",
		kind: "root", agent: "claude", status: "running",
		startTS: base + 1_000, opCount: 1,
	}, "/tmp/work")
	// A codex session that started while "running" is active.
	seedSessionWithCwd(t, db, sessionRow{
		id: "spawned", sourceID: "srcB", nativeID: "nS", rootID: "spawned",
		kind: "root", agent: "codex", status: "completed",
		startTS: base + 3_000, endTS: base + 7_000, opCount: 1,
	}, "/tmp/work")

	code, body, _ := getRelated(t, p, "running")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Related) != 1 || body.Related[0].ID != "spawned" {
		t.Fatalf("related = %+v, want [spawned] (running session via COALESCE(end_ts, now))", body.Related)
	}
}
