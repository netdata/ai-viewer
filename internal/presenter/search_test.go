package presenter

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// searchBody mirrors GET /api/search (rest-api.md §GET /api/search).
type searchBody struct {
	Ops []struct {
		OpID      string  `json:"op_id"`
		SessionID string  `json:"session_id"`
		Kind      string  `json:"kind"`
		Name      string  `json:"name"`
		Model     string  `json:"model"`
		Snippet   string  `json:"snippet"`
		Rank      float64 `json:"rank"`
	} `json:"ops"`
	Logs []struct {
		LogID     int64   `json:"log_id"`
		SessionID string  `json:"session_id"`
		OpID      *string `json:"op_id"`
		Severity  string  `json:"severity"`
		TS        int64   `json:"ts"`
		Snippet   string  `json:"snippet"`
		Rank      float64 `json:"rank"`
	} `json:"logs"`
	LogsIndexed bool   `json:"logs_indexed"`
	NextCursor  string `json:"next_cursor"`
}

// getSearch issues GET /api/search?<query> and decodes either the body or the
// error envelope (mirrors doStatsGet but for the search shape, which carries a
// next_cursor we want to read back).
func getSearch(t *testing.T, p *Presenter, query string) (int, searchBody, errorEnvelope) {
	t.Helper()
	path := "/api/search"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body searchBody
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

// seedFTSOp inserts an op (ops row) AND its content-owning fts_ops row, so a
// search both matches the indexed text and can JOIN back to ops⋈sessions for
// the rendered kind/name/model + the parseSessionFilter constraints. The
// op/session rows are required because handleSearch JOINs the FTS match back to
// them; the FTS row carries the searchable text + linkage exactly as the
// ingester writes it (fts_backfill.go: name, model, provider, tool_namespace,
// error_text, op_id, session_id).
func seedFTSOp(t *testing.T, db *sql.DB, o opRow, errorText string) {
	t.Helper()
	seedOp(t, db, o)
	model := o.model
	provider := o.provider
	if _, err := db.Exec(
		`INSERT INTO fts_ops (name, model, provider, tool_namespace, error_text, op_id, session_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.name, model, provider, o.toolNamespace, errorText, o.id, o.sessionID,
	); err != nil {
		t.Fatalf("seed fts_ops %s: %v", o.id, err)
	}
}

// seedFTSLog inserts a log_entries row AND its fts_logs row (session-scoped,
// mirroring the ingester's only fts_logs writer). It is the caller's job to
// only call this for a source whose fts5_index_logs=1, matching production.
func seedFTSLog(t *testing.T, db *sql.DB, l logRow) int64 {
	t.Helper()
	seedLog(t, db, l)
	var logID int64
	if err := db.QueryRow(
		`SELECT id FROM log_entries WHERE session_id=? AND ts=? AND message=?`,
		l.sessionID, l.ts, l.message,
	).Scan(&logID); err != nil {
		t.Fatalf("read seeded log id: %v", err)
	}
	var opID any
	if l.opID != "" {
		opID = l.opID
	}
	if _, err := db.Exec(
		`INSERT INTO fts_logs (message, log_id, session_id, op_id, severity, ts)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		l.message, logID, l.sessionID, opID, l.severity, l.ts,
	); err != nil {
		t.Fatalf("seed fts_logs %d: %v", logID, err)
	}
	return logID
}

// setSourceIndexLogs flips a source's fts5_index_logs flag so a test can prove
// the logs_indexed scoping (rest-api.md §GET /api/search §logs_indexed).
func setSourceIndexLogs(t *testing.T, db *sql.DB, sourceID string, on bool) {
	t.Helper()
	v := 0
	if on {
		v = 1
	}
	if _, err := db.Exec(`UPDATE sources SET fts5_index_logs=? WHERE id=?`, v, sourceID); err != nil {
		t.Fatalf("set fts5_index_logs(%s=%v): %v", sourceID, on, err)
	}
}

// seedSearchBasic seeds one source + root session + turn, an op whose error
// text contains "timeout", and a session-scoped indexed log whose message
// contains "parse". It is the minimal graph the happy-path tests assert on.
func seedSearchBasic(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA",
		kind: "root", agent: "nedi", model: "claude-opus-4-7", provider: "anthropic",
		status: "completed", startTS: base + 1000, endTS: base + 9000,
	})
	seedTurn(t, db, turnRow{id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1000, status: "completed"})
	seedFTSOp(t, db, opRow{
		id: "o1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "Bash",
		toolNamespace: "shell", model: "", startTS: base + 1100, endTS: base + 2100,
		durationUS: 1000, status: "failed", errorClass: "io_error",
	}, "connection timeout while running command")
	return seedFTSLog(t, db, logRow{
		sessionID: "rootA", opID: "o1", ts: base + 1200, severity: "ERR",
		source: "aiagent_v3", message: "parse error at line 5",
	})
}

// TestSearch_BasicOpAndLogMatch asserts an op error-text match and a log
// message match each surface with a non-empty snippet and a rank, and that the
// op row carries the JOINed kind/name and the log carries severity/ts/op_id.
func TestSearch_BasicOpAndLogMatch(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	logID := seedSearchBasic(t, db)

	code, body, env := getSearch(t, p, "q=timeout")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if !body.LogsIndexed {
		t.Errorf("logs_indexed=false, want true (default-on source)")
	}
	if len(body.Ops) != 1 {
		t.Fatalf("ops=%+v, want 1", body.Ops)
	}
	op := body.Ops[0]
	if op.OpID != "o1" || op.SessionID != "rootA" || op.Kind != "tool" || op.Name != "Bash" {
		t.Errorf("op linkage/render = %+v", op)
	}
	if op.Snippet == "" {
		t.Errorf("op snippet empty: %+v", op)
	}
	if !strings.Contains(strings.ToLower(op.Snippet), "timeout") {
		t.Errorf("op snippet missing match: %q", op.Snippet)
	}

	// A log-message match.
	code, body, env = getSearch(t, p, "q=parse")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if len(body.Logs) != 1 {
		t.Fatalf("logs=%+v, want 1", body.Logs)
	}
	lg := body.Logs[0]
	if lg.LogID != logID || lg.SessionID != "rootA" || lg.Severity != "ERR" {
		t.Errorf("log linkage = %+v (want id=%d)", lg, logID)
	}
	if lg.OpID == nil || *lg.OpID != "o1" {
		t.Errorf("log op_id = %v, want o1", lg.OpID)
	}
	if lg.Snippet == "" || !strings.Contains(strings.ToLower(lg.Snippet), "parse") {
		t.Errorf("log snippet = %q", lg.Snippet)
	}
}

// TestSearch_BM25Ordering asserts the more-relevant op (the search term appears
// more densely) ranks before the less-relevant one. BM25 is the FTS5 default;
// the handler ORDERs by it best-first (rank ascending — bm25 returns negative
// scores, more-relevant = more-negative).
func TestSearch_BM25Ordering(t *testing.T) {
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
	// o_dense: "timeout" appears many times → more relevant.
	seedFTSOp(t, db, opRow{
		id: "o_dense", turnID: "t1", sessionID: "rootA", seq: 1, kind: "tool", name: "X",
		startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed",
	}, "timeout timeout timeout timeout timeout")
	// o_sparse: "timeout" once, padded with unrelated text → less relevant.
	seedFTSOp(t, db, opRow{
		id: "o_sparse", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "Y",
		startTS: base + 1300, endTS: base + 1400, durationUS: 100, status: "completed",
	}, "timeout then a long tail of unrelated words filler filler filler filler filler")

	code, body, env := getSearch(t, p, "q=timeout")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if len(body.Ops) != 2 {
		t.Fatalf("ops=%+v, want 2", body.Ops)
	}
	if body.Ops[0].OpID != "o_dense" || body.Ops[1].OpID != "o_sparse" {
		t.Errorf("ordering = [%s, %s], want [o_dense, o_sparse]", body.Ops[0].OpID, body.Ops[1].OpID)
	}
	if !(body.Ops[0].Rank <= body.Ops[1].Rank) {
		t.Errorf("rank not best-first: %v then %v", body.Ops[0].Rank, body.Ops[1].Rank)
	}
}

// TestSearch_FiltersNarrowOps asserts each parseSessionFilter dimension excludes
// an otherwise-matching op: a from/to window, a sources mismatch, and the
// agent/model/tool/status dimensions.
func TestSearch_FiltersNarrowOps(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	// Two sources, two sessions with distinct agent/model, ops with distinct
	// tool name + status; both ops' error text matches "needle".
	seedSource(t, db, "srcA", "aiagent_v3", "/tmp/a", base)
	seedSource(t, db, "srcB", "codex", "/tmp/b", base)
	seedSession(t, db, sessionRow{
		id: "sessA", sourceID: "srcA", nativeID: "nA", rootID: "sessA", kind: "root",
		agent: "nedi", model: "claude-opus", status: "completed", startTS: base + 1000, endTS: base + 2000,
	})
	seedSession(t, db, sessionRow{
		id: "sessB", sourceID: "srcB", nativeID: "nB", rootID: "sessB", kind: "root",
		agent: "worker", model: "gpt-5", status: "failed", startTS: base + 50_000, endTS: base + 60_000,
	})
	seedTurn(t, db, turnRow{id: "tA", sessionID: "sessA", seq: 1, startTS: base + 1000, status: "completed"})
	seedTurn(t, db, turnRow{id: "tB", sessionID: "sessB", seq: 1, startTS: base + 50_000, status: "completed"})
	seedFTSOp(t, db, opRow{
		id: "opA", turnID: "tA", sessionID: "sessA", seq: 1, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed",
	}, "needle in op A")
	seedFTSOp(t, db, opRow{
		id: "opB", turnID: "tB", sessionID: "sessB", seq: 1, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 50_100, endTS: base + 50_200, durationUS: 100, status: "completed",
	}, "needle in op B")

	// No filter: both match.
	if code, body, env := getSearch(t, p, "q=needle"); code != http.StatusOK || len(body.Ops) != 2 {
		t.Fatalf("unfiltered: code=%d ops=%+v env=%+v", code, body.Ops, env)
	}

	cases := []struct {
		name, query, wantOp string
	}{
		{"sources", "q=needle&sources=srcA", "opA"},
		{"agents", "q=needle&agents=worker", "opB"},
		{"models", "q=needle&models=claude-opus", "opA"},
		{"tools", "q=needle&tools=Read", "opB"},
		{"status", "q=needle&status=failed", "opB"},
		// to< sessB.start_ts excludes opB; from> sessA.start_ts excludes opA.
		{"to-window", "q=needle&to=" + i64(base+10_000), "opA"},
		{"from-window", "q=needle&from=" + i64(base+10_000), "opB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body, env := getSearch(t, p, tc.query)
			if code != http.StatusOK {
				t.Fatalf("status=%d env=%+v", code, env)
			}
			if len(body.Ops) != 1 || body.Ops[0].OpID != tc.wantOp {
				t.Fatalf("%s: ops=%+v, want exactly [%s]", tc.name, body.Ops, tc.wantOp)
			}
		})
	}
}

// TestSearch_FindsSubAgentSessionContent pins F2 for /api/search: search spans
// ALL sessions (root + sub-agent), not just roots. parseSessionFilter defaults
// group=root, whose whereClause adds s.kind='root' — handleSearch must force
// group=all so an op/log living in a sub-agent session is still found. Without
// the fix the sub-agent op and log are silently excluded.
func TestSearch_FindsSubAgentSessionContent(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	// Root session with a matching op.
	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA", kind: "root",
		agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 9000,
	})
	seedTurn(t, db, turnRow{id: "tR", sessionID: "rootA", seq: 1, startTS: base + 1000, status: "completed"})
	seedFTSOp(t, db, opRow{
		id: "opRoot", turnID: "tR", sessionID: "rootA", seq: 1, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 1100, endTS: base + 1200, durationUS: 100, status: "completed",
	}, "needle in root")
	// Sub-agent session (child of rootA) with a matching op AND a matching log.
	seedSession(t, db, sessionRow{
		id: "subA", sourceID: "src1", nativeID: "nS", parentID: "rootA", rootID: "rootA", kind: "sub_agent",
		agent: "worker", status: "completed", startTS: base + 2000, endTS: base + 4000,
	})
	seedTurn(t, db, turnRow{id: "tS", sessionID: "subA", seq: 1, startTS: base + 2000, status: "completed"})
	seedFTSOp(t, db, opRow{
		id: "opSub", turnID: "tS", sessionID: "subA", seq: 1, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 2100, endTS: base + 2200, durationUS: 100, status: "completed",
	}, "needle in sub-agent")
	seedFTSLog(t, db, logRow{sessionID: "subA", opID: "opSub", ts: base + 2150, severity: "ERR",
		source: "aiagent_v3", message: "needle log in sub-agent"})

	code, body, env := getSearch(t, p, "q=needle")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	// Both the root op and the sub-agent op must be returned (F2: all sessions).
	var sawRootOp, sawSubOp bool
	for _, op := range body.Ops {
		switch op.OpID {
		case "opRoot":
			sawRootOp = true
		case "opSub":
			sawSubOp = true
		}
	}
	if !sawRootOp {
		t.Errorf("root op missing from search: %+v", body.Ops)
	}
	if !sawSubOp {
		t.Errorf("sub-agent op missing from search (F2: search must span all sessions): %+v", body.Ops)
	}
	// The sub-agent log must also be found.
	var sawSubLog bool
	for _, lg := range body.Logs {
		if lg.SessionID == "subA" {
			sawSubLog = true
		}
	}
	if !sawSubLog {
		t.Errorf("sub-agent log missing from search (F2): %+v", body.Logs)
	}
}

// TestSearch_LogsIndexedScoping asserts the logs_indexed contract: when the
// in-scope source has fts5_index_logs=0, a log that WOULD match is not returned
// and logs_indexed=false; flipping it on returns the log and logs_indexed=true.
func TestSearch_LogsIndexedScoping(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedSearchBasic(t, db)

	// Disable log indexing on the only source. The fts_logs row physically
	// exists (seeded), but logs_indexed reflects the per-source flag, so the
	// handler must report false and return no logs.
	setSourceIndexLogs(t, db, "src1", false)
	code, body, env := getSearch(t, p, "q=parse")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if body.LogsIndexed {
		t.Errorf("logs_indexed=true, want false (source opted out)")
	}
	if len(body.Logs) != 0 {
		t.Errorf("logs=%+v, want empty (indexing disabled)", body.Logs)
	}

	// Re-enable: the log is returned and logs_indexed=true.
	setSourceIndexLogs(t, db, "src1", true)
	code, body, env = getSearch(t, p, "q=parse")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if !body.LogsIndexed {
		t.Errorf("logs_indexed=false, want true (source opted in)")
	}
	if len(body.Logs) != 1 {
		t.Errorf("logs=%+v, want 1", body.Logs)
	}
}

// TestSearch_LogsIndexedScopedToSourcesFilter asserts logs_indexed is scoped to
// the in-scope source set: with one indexed source and one opted-out source,
// restricting ?sources= to the opted-out source yields logs_indexed=false even
// though another (out-of-scope) source has indexing on.
func TestSearch_LogsIndexedScopedToSourcesFilter(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcOn", "aiagent_v3", "/tmp/on", base)
	seedSource(t, db, "srcOff", "codex", "/tmp/off", base)
	setSourceIndexLogs(t, db, "srcOff", false)
	for _, s := range []struct{ id, src string }{{"sOn", "srcOn"}, {"sOff", "srcOff"}} {
		seedSession(t, db, sessionRow{
			id: s.id, sourceID: s.src, nativeID: s.id, rootID: s.id, kind: "root",
			agent: "nedi", status: "completed", startTS: base + 1000, endTS: base + 2000,
		})
		seedFTSLog(t, db, logRow{sessionID: s.id, ts: base + 1200, severity: "ERR",
			source: "x", message: "parse needle"})
	}

	// Scope to the opted-out source only → false, no logs.
	code, body, _ := getSearch(t, p, "q=needle&sources=srcOff")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if body.LogsIndexed || len(body.Logs) != 0 {
		t.Errorf("sources=srcOff: logs_indexed=%v logs=%+v, want false/empty", body.LogsIndexed, body.Logs)
	}

	// Scope to the indexed source only → true, its log returned.
	code, body, _ = getSearch(t, p, "q=needle&sources=srcOn")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if !body.LogsIndexed || len(body.Logs) != 1 {
		t.Errorf("sources=srcOn: logs_indexed=%v logs=%+v, want true/1", body.LogsIndexed, body.Logs)
	}
}
