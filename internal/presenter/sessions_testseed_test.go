package presenter

import (
	"database/sql"
	"encoding/base64"
	"testing"
	"time"
)

// seedBase is the wall-clock anchor for seeded fixtures: one hour BEFORE
// fixedTime (the injected "now"). Sessions must start before now or the
// default time filter (`to` omitted => now) excludes them, so every
// endpoint test anchors its rows here rather than at fixedTime itself.
func seedBase() int64 {
	return fixedTime.Add(-time.Hour).UnixMicro()
}

// b64Cursor base64url-encodes a raw cursor JSON body so tests can craft
// malformed/partial cursors and assert the handler rejects them with 400.
func b64Cursor(t *testing.T, rawJSON string) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString([]byte(rawJSON))
}

// Shared fixture-seeding helpers for the Chunk 12 read-side endpoint
// tests. The graph is deliberately small but covers the cross-cutting
// shapes the endpoints must handle: a root session with two turns and
// mixed-kind/mixed-status ops, two child sessions linked through a
// kind='session' op, payload_refs hanging off an op, and log_entries of
// varied severity. Tests seed exactly what they assert against; this
// file owns only the low-level INSERT helpers so each test reads as a
// declarative graph rather than a wall of SQL.

// seedSource inserts one sources row. created_at doubles as last_seen_at
// unless the caller overrides via seedSourceFull.
func seedSource(t *testing.T, db *sql.DB, id, format, location string, createdAt int64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, parse_errors, created_at)
		 VALUES (?, ?, ?, 1, ?, 0, ?)`,
		id, format, location, createdAt, createdAt,
	); err != nil {
		t.Fatalf("seed source %s: %v", id, err)
	}
}

// sessionRow is the declarative seed input for one sessions row. Only
// the fields the Chunk 12 endpoints read are exposed; the rest take
// schema defaults.
type sessionRow struct {
	id, sourceID, nativeID            string
	parentID                          string // "" => NULL
	rootID                            string
	kind, agent, model                string
	provider, providerAlias           string
	callPath                          string
	errorClass, errorMessage          string
	firstUserMessageHash              string
	status                            string
	startTS, endTS                    int64
	tokensIn, tokensOut               int64
	tokensCacheRead, tokensCacheWrite int64
	costUSD                           float64
	turnCount, opCount                int64
	failureCount                      int64
	cwd                               string // "" => NULL (the schema default)
}

// seedSessionWithCwd inserts one sessions row with an explicit cwd (SOW-0071
// cross-harness detection joins on cwd). Mirrors seedSession but adds the cwd
// column; tests that don't need cwd use seedSession (cwd = NULL).
func seedSessionWithCwd(t *testing.T, db *sql.DB, s sessionRow, cwd string) {
	t.Helper()
	s.cwd = cwd
	seedSession(t, db, s)
}

// seedSession inserts one sessions row from a sessionRow.
func seedSession(t *testing.T, db *sql.DB, s sessionRow) {
	t.Helper()
	// Default to op_count=1 so the session is visible in the default list view
	// (which excludes 0-op sessions — SOW-0063). Tests that need genuinely-empty
	// sessions must set opCount explicitly and add include_empty=1 to the URL.
	if s.opCount == 0 && s.turnCount == 0 {
		s.opCount = 1
	}
	var parent any
	if s.parentID != "" {
		parent = s.parentID
	}
	var endTS any
	if s.endTS != 0 {
		endTS = s.endTS
	}
	var cwd any
	if s.cwd != "" {
		cwd = s.cwd
	}
	nullStr := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	if _, err := db.Exec(`
INSERT INTO sessions (
    id, source_id, native_id, parent_session_id, root_session_id, kind,
    agent_name, model, provider, provider_alias, cwd, call_path,
    status, error_class, error_message, start_ts, end_ts, last_activity_ts,
    first_user_message_hash,
    tokens_in, tokens_out, tokens_cache_read, tokens_cache_write,
    cost_usd, turn_count, op_count, failure_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.id, s.sourceID, s.nativeID, parent, s.rootID, s.kind,
		s.agent, s.model, s.provider, nullStr(s.providerAlias), cwd, nullStr(s.callPath),
		s.status, nullStr(s.errorClass), nullStr(s.errorMessage), s.startTS, endTS, s.startTS,
		nullStr(s.firstUserMessageHash),
		s.tokensIn, s.tokensOut, s.tokensCacheRead, s.tokensCacheWrite,
		s.costUSD, s.turnCount, s.opCount, s.failureCount,
	); err != nil {
		t.Fatalf("seed session %s: %v", s.id, err)
	}
	// Mirror migration 0011's backfill: populate duration_us from end_ts -
	// start_ts (NULL when end_ts IS NULL). The cross-topology default
	// metric (duration) reads this column directly.
	if _, err := db.Exec(
		`UPDATE sessions SET duration_us = CASE WHEN end_ts IS NOT NULL THEN end_ts - start_ts ELSE NULL END WHERE id = ?`,
		s.id); err != nil {
		t.Fatalf("seed session %s: backfill duration_us: %v", s.id, err)
	}
}

// turnRow is the declarative seed input for one turns row.
type turnRow struct {
	id, sessionID       string
	seq                 int64
	startTS, endTS      int64
	status              string
	errorClass          string
	tokensIn, tokensOut int64
	tokensCacheRead     int64
	tokensCacheWrite    int64
	costUSD             float64
	opCount             int64
}

// seedTurn inserts one turns row.
func seedTurn(t *testing.T, db *sql.DB, tr turnRow) {
	t.Helper()
	var endTS any
	if tr.endTS != 0 {
		endTS = tr.endTS
	}
	nullStr := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	if _, err := db.Exec(`
INSERT INTO turns (
    id, session_id, seq, start_ts, end_ts, status, error_class,
    tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, op_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tr.id, tr.sessionID, tr.seq, tr.startTS, endTS, tr.status,
		nullStr(tr.errorClass), tr.tokensIn, tr.tokensOut, tr.tokensCacheRead, tr.tokensCacheWrite,
		tr.costUSD, tr.opCount,
	); err != nil {
		t.Fatalf("seed turn %s: %v", tr.id, err)
	}
}

// opRow is the declarative seed input for one ops row.
type opRow struct {
	id, turnID, sessionID string
	parentOpID            string // "" => NULL (top-level op); else FK to ops(id)
	seq                   int64
	kind, name            string
	toolNamespace         string
	model, provider       string
	providerAlias         string
	reasoningKind         string
	startTS, endTS        int64
	durationUS            int64
	status                string
	errorClass            string
	tokensIn, tokensOut   int64
	tokensCacheRead       int64 // SOW-0067: op-level cache tokens (by_model cache-hit)
	tokensCacheWrite      int64
	costUSD               float64
	bytesIn, bytesOut     int64
	charsIn, charsOut     int64
	ctxUsed, ctxMax       int64
	childSessionID        string
}

// seedOp inserts one ops row.
func seedOp(t *testing.T, db *sql.DB, o opRow) {
	t.Helper()
	nullStr := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	nullInt := func(v int64) any {
		if v == 0 {
			return nil
		}
		return v
	}
	var endTS any
	if o.endTS != 0 {
		endTS = o.endTS
	}
	if _, err := db.Exec(`
INSERT INTO ops (
    id, turn_id, session_id, parent_op_id, seq, kind, name, tool_namespace, model, provider,
    provider_alias, reasoning_kind,
    start_ts, end_ts, duration_us, status, error_class,
    tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd,
    bytes_in, bytes_out, chars_in, chars_out, ctx_used, ctx_max, child_session_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.id, o.turnID, o.sessionID, nullStr(o.parentOpID), o.seq, o.kind, o.name,
		nullStr(o.toolNamespace), nullStr(o.model), nullStr(o.provider),
		nullStr(o.providerAlias), nullStr(o.reasoningKind),
		o.startTS, endTS, nullInt(o.durationUS), o.status, nullStr(o.errorClass),
		o.tokensIn, o.tokensOut, o.tokensCacheRead, o.tokensCacheWrite,
		o.costUSD, o.bytesIn, o.bytesOut, nullInt(o.charsIn), nullInt(o.charsOut),
		nullInt(o.ctxUsed), nullInt(o.ctxMax),
		nullStr(o.childSessionID),
	); err != nil {
		t.Fatalf("seed op %s: %v", o.id, err)
	}
}

// payloadRow is the declarative seed input for one payload_refs row.
type payloadRow struct {
	opID                       string
	kind, format, compression  string
	locationURI                string
	sha256                     string
	originalBytes, storedBytes int64
}

// seedPayload inserts one payload_refs row and returns its rowid.
func seedPayload(t *testing.T, db *sql.DB, p payloadRow) int64 {
	t.Helper()
	var compression any
	if p.compression != "" {
		compression = p.compression
	}
	nullStr := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	res, err := db.Exec(`
INSERT INTO payload_refs (op_id, kind, format, compression, location_uri, original_bytes, stored_bytes, sha256)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.opID, p.kind, p.format, compression, p.locationURI, p.originalBytes, p.storedBytes,
		nullStr(p.sha256),
	)
	if err != nil {
		t.Fatalf("seed payload for op %s: %v", p.opID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("payload last insert id: %v", err)
	}
	return id
}

// logRow is the declarative seed input for one log_entries row.
type logRow struct {
	sessionID, opID     string
	ts                  int64
	severity, source    string
	message, extrasJSON string
}

// seedLog inserts one log_entries row.
func seedLog(t *testing.T, db *sql.DB, l logRow) {
	t.Helper()
	var opID any
	if l.opID != "" {
		opID = l.opID
	}
	var extras any
	if l.extrasJSON != "" {
		extras = l.extrasJSON
	}
	if _, err := db.Exec(`
INSERT INTO log_entries (session_id, op_id, ts, severity, source, message, extras_json)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.sessionID, opID, l.ts, l.severity, l.source, l.message, extras,
	); err != nil {
		t.Fatalf("seed log for session %s: %v", l.sessionID, err)
	}
}

// seedGraph seeds the canonical Chunk-12 fixture graph used by the
// happy-path tests: one source, a root session (rootA) with two turns
// and four ops (llm completed, tool completed, tool failed, session op
// linking childA1), plus two child sessions (childA1, childA2). rootA's
// first llm op carries two payload_refs. Three log_entries of varied
// severity hang off rootA. base is the wall-clock anchor in µs.
func seedGraph(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)

	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA",
		kind: "root", agent: "nedi", model: "claude-opus-4-7", provider: "anthropic",
		providerAlias: "claude", callPath: "rootA", errorMessage: "root warning",
		firstUserMessageHash: "hash-root-a",
		status:               "completed", startTS: base + 1_000, endTS: base + 9_000,
		tokensIn: 1000, tokensOut: 2000, tokensCacheRead: 3000, tokensCacheWrite: 500, costUSD: 0.30,
		turnCount: 2, opCount: 4, failureCount: 1,
		cwd: "/workspace/root-a",
	})
	seedSession(t, db, sessionRow{
		id: "childA1", sourceID: "src1", nativeID: "nC1", parentID: "rootA", rootID: "rootA",
		kind: "sub_agent", agent: "worker", model: "claude-haiku", provider: "anthropic",
		status: "completed", startTS: base + 2_000, endTS: base + 4_000,
		tokensIn: 100, tokensOut: 200, costUSD: 0.02, turnCount: 1, opCount: 1, failureCount: 0,
	})
	seedSession(t, db, sessionRow{
		id: "childA2", sourceID: "src1", nativeID: "nC2", parentID: "rootA", rootID: "rootA",
		kind: "sub_agent", agent: "worker", model: "gpt-5", provider: "openai",
		status: "failed", errorClass: "child_error", startTS: base + 5_000, endTS: base + 6_000,
		tokensIn: 50, tokensOut: 60, costUSD: 0.01, turnCount: 1, opCount: 1, failureCount: 1,
	})

	seedTurn(t, db, turnRow{
		id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1_000, endTS: base + 5_000,
		status: "completed", tokensIn: 600, tokensOut: 1200, tokensCacheRead: 900,
		tokensCacheWrite: 90, costUSD: 0.18, opCount: 3,
	})
	seedTurn(t, db, turnRow{
		id: "t2", sessionID: "rootA", seq: 2, startTS: base + 5_000, endTS: base + 9_000,
		status: "completed", errorClass: "io_error", tokensIn: 400, tokensOut: 800,
		tokensCacheRead: 300, tokensCacheWrite: 30, costUSD: 0.12, opCount: 1,
	})

	seedGraphOps(t, db, base)
	seedGraphPayloadsAndLogs(t, db, base)
}

// seedGraphOps seeds rootA's four ops across its two turns: an llm op (o1,
// completed, carries payload_refs), a completed tool op (o2), a session op
// (o3) linking childA1, and a failed tool op (o4).
func seedGraphOps(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedOp(t, db, opRow{
		id: "o1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "llm", name: "claude-opus-4-7",
		model: "claude-opus-4-7", provider: "anthropic", providerAlias: "claude",
		reasoningKind: "summary", startTS: base + 1_100, endTS: base + 2_100,
		durationUS: 1_000, status: "completed", tokensIn: 500, tokensOut: 1000,
		tokensCacheRead: 3000, tokensCacheWrite: 500, costUSD: 0.15,
		bytesIn: 2048, bytesOut: 4096, charsIn: 1200, charsOut: 2400,
		ctxUsed: 12000, ctxMax: 200000,
	})
	seedOp(t, db, opRow{
		id: "o2", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 2_200, endTS: base + 2_500, durationUS: 300,
		status: "completed", tokensIn: 50, tokensOut: 100, costUSD: 0.01,
		bytesIn: 18, bytesOut: 42, charsIn: 18, charsOut: 42,
	})
	seedOp(t, db, opRow{
		id: "o3", turnID: "t1", sessionID: "rootA", seq: 3, kind: "session", name: "worker",
		startTS: base + 2_000, endTS: base + 4_000, durationUS: 2_000, status: "completed",
		childSessionID: "childA1",
	})
	seedOp(t, db, opRow{
		id: "o4", turnID: "t2", sessionID: "rootA", seq: 1, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 5_100, endTS: base + 5_200, durationUS: 100,
		status: "failed", errorClass: "io_error", tokensIn: 50, tokensOut: 100, costUSD: 0.01,
	})
}

// seedGraphPayloadsAndLogs seeds rootA's two payload_refs (both on o1) and
// three log_entries of varied severity (INF on o1, ERR on o4 with extras,
// WRN unattached) in ascending ts order.
func seedGraphPayloadsAndLogs(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedPayload(t, db, payloadRow{
		opID: "o1", kind: "llm_request", format: "http", compression: "gzip",
		locationURI:   "file:///tmp/a/req.gz",
		sha256:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		originalBytes: 1234, storedBytes: 456,
	})
	seedPayload(t, db, payloadRow{
		opID: "o1", kind: "llm_response", format: "sse", compression: "gzip",
		locationURI: "file:///tmp/a/resp.gz", originalBytes: 5678, storedBytes: 999,
	})

	seedLog(t, db, logRow{sessionID: "rootA", opID: "o1", ts: base + 1_200, severity: "INF", source: "aiagent_v3", message: "llm started"})
	seedLog(t, db, logRow{sessionID: "rootA", opID: "o4", ts: base + 5_150, severity: "ERR", source: "aiagent_v3", message: "read failed", extrasJSON: `{"path":"/x"}`})
	seedLog(t, db, logRow{sessionID: "rootA", ts: base + 8_000, severity: "WRN", source: "aiagent_v3", message: "slow turn"})
}
