package presenter

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// compareBody mirrors GET /api/sessions/compare. The shape is the wire
// contract; the tests assert the diff structure, not the underlying
// row types. Field tags follow the JSON wire form.
type compareBody struct {
	Sessions []struct {
		ID        string  `json:"id"`
		AgentName string  `json:"agent_name"`
		Model     string  `json:"model"`
		Provider  string  `json:"provider"`
		Status    string  `json:"status"`
		OpCount   int64   `json:"op_count"`
		TokensIn  int64   `json:"tokens_in"`
		TokensOut int64   `json:"tokens_out"`
		CostUSD   float64 `json:"cost_usd"`
	} `json:"sessions"`
	Summary struct {
		DurationUS struct {
			Best       *string          `json:"best"`
			Worst      *string          `json:"worst"`
			PerSession map[string]int64 `json:"per_session"`
		} `json:"duration_us"`
		CostUSD struct {
			Best       *string            `json:"best"`
			Worst      *string            `json:"best_worst"`
			PerSession map[string]float64 `json:"per_session"`
		} `json:"cost_usd"`
		OpCount struct {
			PerSession map[string]int64 `json:"per_session"`
		} `json:"op_count"`
		Tokens struct {
			PerSession map[string]int64 `json:"per_session"`
		} `json:"tokens"`
	} `json:"summary"`
	ToolUsage struct {
		Common     []string                    `json:"common"`
		Added      map[string][]string         `json:"added"`
		Removed    map[string][]string         `json:"removed"`
		PerSession map[string]map[string]int64 `json:"per_session"`
	} `json:"tool_usage"`
	Errors struct {
		Common []opErrorRefWire            `json:"common"`
		OnlyIn map[string][]opErrorRefWire `json:"only_in"`
	} `json:"errors"`
	Models struct {
		Shared   []string            `json:"shared"`
		Diverged map[string][]string `json:"diverged"`
	} `json:"models"`
	Agents struct {
		Shared   []string            `json:"shared"`
		Diverged map[string][]string `json:"diverged"`
	} `json:"agents"`
	KindDistribution struct {
		PerSession map[string]map[string]int64 `json:"per_session"`
	} `json:"kind_distribution"`
}

type opErrorRefWire struct {
	OpID        string `json:"op_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ErrorClass  string `json:"error_class"`
	StartedAtUS int64  `json:"started_at_us"`
}

// getCompare hits GET /api/sessions/compare?ids=...
func getCompare(t *testing.T, p *Presenter, ids ...string) (int, compareBody, errorEnvelope) {
	t.Helper()
	url := "/api/sessions/compare?ids=" + strings.Join(ids, ",")
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body compareBody
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

// TestCompare_Empty asserts the handler rejects requests with no ids
// (missing or empty ?ids=... parameter) with 400.
func TestCompare_Empty(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	for _, url := range []string{
		"/api/sessions/compare",
		"/api/sessions/compare?ids=",
		"/api/sessions/compare?ids=,",
	} {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s: code = %d, want 400", url, rr.Code)
		}
	}
}

// TestCompare_OneID asserts the handler rejects 1-id requests with 400
// (the contract requires 2-4 ids).
func TestCompare_OneID(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, _, _ := getCompare(t, p, "rootA")
	if code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
}

// TestCompare_FiveIDs asserts the handler rejects 5-id requests with 400.
func TestCompare_FiveIDs(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, _, _ := getCompare(t, p, "rootA", "childA1", "childA2", "extra-1", "extra-2")
	if code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
}

// TestCompare_UnknownID asserts the handler returns 404 with the missing
// id in the error message when at least one id is not in the DB.
func TestCompare_UnknownID(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, _, env := getCompare(t, p, "rootA", "sess-does-not-exist")
	if code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", code)
	}
	if env.Error.Code != CodeNotFound {
		t.Errorf("error code = %q, want %q", env.Error.Code, CodeNotFound)
	}
	if !strings.Contains(env.Error.Message, "sess-does-not-exist") {
		t.Errorf("error message %q should mention the missing id", env.Error.Message)
	}
}

// TestCompare_OrderPreserved asserts the response.sessions array preserves
// the order of ids in the request — the compare page relies on this for
// column alignment.
func TestCompare_OrderPreserved(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// Request the ids in a specific non-sorted order.
	code, body, _ := getCompare(t, p, "childA1", "rootA")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(body.Sessions))
	}
	if body.Sessions[0].ID != "childA1" {
		t.Errorf("session[0].id = %q, want childA1 (order preserved)", body.Sessions[0].ID)
	}
	if body.Sessions[0].Provider != "anthropic" {
		t.Errorf("session[0].provider = %q, want anthropic", body.Sessions[0].Provider)
	}
	if body.Sessions[1].ID != "rootA" {
		t.Errorf("session[1].id = %q, want rootA (order preserved)", body.Sessions[1].ID)
	}
}

// TestCompare_SummaryStats asserts the summary block: per_session is
// populated for all four metrics, and best/worst are populated for
// duration_us + cost_usd (lower is better) but neutral (null) for
// op_count + tokens.
func TestCompare_SummaryStats(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getCompare(t, p, "rootA", "childA1")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	// DurationUs: best/worst are populated (lower is better).
	if body.Summary.DurationUS.Best == nil || body.Summary.DurationUS.Worst == nil {
		t.Errorf("duration_us best/worst should be set; got %+v", body.Summary.DurationUS)
	}
	// OpCount: neutral — best/worst omitted.
	if body.Summary.OpCount.PerSession == nil {
		t.Errorf("op_count.per_session should be populated")
	}
}

// TestCompare_ToolUsage_AddedRemoved asserts tools unique to a session
// (relative to the intersection) appear in added; tools used by other
// sessions but not this one appear in removed.
func TestCompare_ToolUsage_AddedRemoved(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	// Seed two sessions with disjoint tool sets.
	seedTwoSessionsDisjointTools(t, db, base)

	code, body, _ := getCompare(t, p, "sess-A", "sess-B")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	// Common should be empty (disjoint tool sets).
	if len(body.ToolUsage.Common) != 0 {
		t.Errorf("common = %v, want []", body.ToolUsage.Common)
	}
	// Each session should have its tools in added.
	if len(body.ToolUsage.Added["sess-A"]) == 0 {
		t.Errorf("added[sess-A] should not be empty; got %+v", body.ToolUsage.Added)
	}
	if len(body.ToolUsage.Added["sess-B"]) == 0 {
		t.Errorf("added[sess-B] should not be empty; got %+v", body.ToolUsage.Added)
	}
	// per_session should carry the tool call counts.
	if got := body.ToolUsage.PerSession["sess-A"]; len(got) == 0 {
		t.Errorf("per_session[sess-A] should not be empty")
	}
}

// TestCompare_Errors_OnlyIn asserts errors that appear in only one
// session end up in errors.only_in[session_id].
func TestCompare_Errors_OnlyIn(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedTwoSessionsDivergingErrors(t, db, base)

	code, body, _ := getCompare(t, p, "sess-X", "sess-Y")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if got := body.Errors.OnlyIn["sess-X"]; len(got) == 0 {
		t.Errorf("errors.only_in[sess-X] should not be empty; got %+v", body.Errors.OnlyIn)
	}
	if got := body.Errors.OnlyIn["sess-Y"]; len(got) == 0 {
		t.Errorf("errors.only_in[sess-Y] should not be empty; got %+v", body.Errors.OnlyIn)
	}
}

// TestCompare_ModelsDiverged asserts the models.diverged map is populated
// when the two sessions use different models.
func TestCompare_ModelsDiverged(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedTwoSessionsDifferentModels(t, db, base)

	code, body, _ := getCompare(t, p, "sess-M1", "sess-M2")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if len(body.Models.Shared) != 0 {
		t.Errorf("models.shared = %v, want []", body.Models.Shared)
	}
	if len(body.Models.Diverged["sess-M1"]) == 0 {
		t.Errorf("models.diverged[sess-M1] should not be empty; got %+v", body.Models.Diverged)
	}
}

// TestCompare_ThreeSessions asserts the N=3 path: order preserved, all
// three per-session summaries present, and the diff dimensions are
// cross-computed across all three.
func TestCompare_ThreeSessions(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedThreeSessions(t, db, base)

	code, body, _ := getCompare(t, p, "sess-T1", "sess-T2", "sess-T3")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if len(body.Sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(body.Sessions))
	}
	if body.Sessions[0].ID != "sess-T1" || body.Sessions[2].ID != "sess-T3" {
		t.Errorf("session order not preserved: %+v", body.Sessions)
	}
	// All three should have kind_distribution entries.
	for _, id := range []string{"sess-T1", "sess-T2", "sess-T3"} {
		if _, ok := body.KindDistribution.PerSession[id]; !ok {
			t.Errorf("kind_distribution.per_session missing %s", id)
		}
	}
}

// TestCompare_KindDistribution asserts the kind histogram: per_session
// carries a kind → count map for each session.
func TestCompare_KindDistribution(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getCompare(t, p, "rootA", "childA1")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	// rootA has at least one llm op (from seedGraph's default).
	if got := body.KindDistribution.PerSession["rootA"]["llm"]; got == 0 {
		t.Errorf("kind_distribution[rootA][llm] should be > 0; got %+v", body.KindDistribution.PerSession)
	}
}

// TestCompare_HEAD asserts HEAD returns 200 with no body (so polling
// clients can check availability cheaply).
func TestCompare_HEAD(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	req := httptest.NewRequest(http.MethodHead, "/api/sessions/compare?ids=rootA,childA1", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", rr.Body.Len())
	}
}

// TestCompare_MethodNotAllowed asserts POST is 405.
func TestCompare_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/compare?ids=rootA,childA1", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rr.Code)
	}
}

// seedTwoSessionsDisjointTools creates two sessions ("sess-A", "sess-B"),
// each with one turn, and tool ops with disjoint name sets: sess-A uses
// only "Bash", sess-B uses only "Read". Used to assert that
// tool_usage.added correctly populates the per-session unique tools.
func seedTwoSessionsDisjointTools(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src-cmp", "aiagent_v3", "/tmp/cmp", base)
	// sess-A: 1 llm op + 1 Bash tool op
	seedSession(t, db, sessionRow{
		id: "sess-A", sourceID: "src-cmp", nativeID: "nA", rootID: "sess-A",
		kind: "root", agent: "agent-a", model: "claude-opus-4-7", provider: "anthropic",
		status: "completed", startTS: base + 1_000, endTS: base + 5_000,
		tokensIn: 100, tokensOut: 200, costUSD: 0.05, turnCount: 1, opCount: 2,
	})
	seedTurn(t, db, turnRow{
		id: "t-A", sessionID: "sess-A", seq: 1, startTS: base + 1_000, endTS: base + 5_000,
		status: "completed", tokensIn: 100, tokensOut: 200, costUSD: 0.05, opCount: 2,
	})
	seedOp(t, db, opRow{
		id: "o-A1", turnID: "t-A", sessionID: "sess-A", seq: 1, kind: "llm", name: "claude-opus-4-7",
		model: "claude-opus-4-7", provider: "anthropic", startTS: base + 1_100, endTS: base + 2_500,
		durationUS: 1_400, status: "completed", tokensIn: 100, tokensOut: 200, costUSD: 0.05,
	})
	seedOp(t, db, opRow{
		id: "o-A2", turnID: "t-A", sessionID: "sess-A", seq: 2, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 2_600, endTS: base + 4_000, durationUS: 1_400,
		status: "completed",
	})
	// sess-B: 1 llm op + 1 Read tool op
	seedSession(t, db, sessionRow{
		id: "sess-B", sourceID: "src-cmp", nativeID: "nB", rootID: "sess-B",
		kind: "root", agent: "agent-b", model: "claude-opus-4-7", provider: "anthropic",
		status: "completed", startTS: base + 1_000, endTS: base + 5_000,
		tokensIn: 100, tokensOut: 200, costUSD: 0.05, turnCount: 1, opCount: 2,
	})
	seedTurn(t, db, turnRow{
		id: "t-B", sessionID: "sess-B", seq: 1, startTS: base + 1_000, endTS: base + 5_000,
		status: "completed", tokensIn: 100, tokensOut: 200, costUSD: 0.05, opCount: 2,
	})
	seedOp(t, db, opRow{
		id: "o-B1", turnID: "t-B", sessionID: "sess-B", seq: 1, kind: "llm", name: "claude-opus-4-7",
		model: "claude-opus-4-7", provider: "anthropic", startTS: base + 1_100, endTS: base + 2_500,
		durationUS: 1_400, status: "completed", tokensIn: 100, tokensOut: 200, costUSD: 0.05,
	})
	seedOp(t, db, opRow{
		id: "o-B2", turnID: "t-B", sessionID: "sess-B", seq: 2, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 2_600, endTS: base + 4_000, durationUS: 1_400,
		status: "completed",
	})
}

// seedTwoSessionsDivergingErrors creates two sessions with disjoint error
// sets: sess-X has one failed op (rate_limit), sess-Y has one failed op
// (io_error). Neither shares the other's error.
func seedTwoSessionsDivergingErrors(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src-err", "aiagent_v3", "/tmp/err", base)
	seedSession(t, db, sessionRow{
		id: "sess-X", sourceID: "src-err", nativeID: "nX", rootID: "sess-X",
		kind: "root", agent: "agent-x", model: "claude-opus-4-7", provider: "anthropic",
		status: "failed", startTS: base + 1_000, endTS: base + 3_000,
	})
	seedTurn(t, db, turnRow{
		id: "t-X", sessionID: "sess-X", seq: 1, startTS: base + 1_000, endTS: base + 3_000,
	})
	seedOp(t, db, opRow{
		id: "o-X1", turnID: "t-X", sessionID: "sess-X", seq: 1, kind: "llm", name: "claude-opus-4-7",
		model: "claude-opus-4-7", provider: "anthropic", startTS: base + 1_100, endTS: base + 2_500,
		durationUS: 1_400, status: "failed", errorClass: "rate_limit",
		tokensIn: 50, tokensOut: 50, costUSD: 0.01,
	})
	seedSession(t, db, sessionRow{
		id: "sess-Y", sourceID: "src-err", nativeID: "nY", rootID: "sess-Y",
		kind: "root", agent: "agent-y", model: "claude-opus-4-7", provider: "anthropic",
		status: "failed", startTS: base + 1_000, endTS: base + 3_000,
	})
	seedTurn(t, db, turnRow{
		id: "t-Y", sessionID: "sess-Y", seq: 1, startTS: base + 1_000, endTS: base + 3_000,
	})
	seedOp(t, db, opRow{
		id: "o-Y1", turnID: "t-Y", sessionID: "sess-Y", seq: 1, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 1_100, endTS: base + 2_500, durationUS: 1_400,
		status: "failed", errorClass: "io_error",
	})
}

// seedTwoSessionsDifferentModels creates two sessions using different
// models: sess-M1 uses claude-opus-4-7, sess-M2 uses gpt-5.
func seedTwoSessionsDifferentModels(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src-mod", "aiagent_v3", "/tmp/mod", base)
	seedSession(t, db, sessionRow{
		id: "sess-M1", sourceID: "src-mod", nativeID: "nM1", rootID: "sess-M1",
		kind: "root", agent: "agent-m", model: "claude-opus-4-7", provider: "anthropic",
		status: "completed", startTS: base + 1_000, endTS: base + 3_000,
		tokensIn: 50, tokensOut: 50, costUSD: 0.01, turnCount: 1, opCount: 1,
	})
	seedTurn(t, db, turnRow{
		id: "t-M1", sessionID: "sess-M1", seq: 1, startTS: base + 1_000, endTS: base + 3_000,
		status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01, opCount: 1,
	})
	seedOp(t, db, opRow{
		id: "o-M1", turnID: "t-M1", sessionID: "sess-M1", seq: 1, kind: "llm", name: "claude-opus-4-7",
		model: "claude-opus-4-7", provider: "anthropic", startTS: base + 1_100, endTS: base + 2_500,
		durationUS: 1_400, status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01,
	})
	seedSession(t, db, sessionRow{
		id: "sess-M2", sourceID: "src-mod", nativeID: "nM2", rootID: "sess-M2",
		kind: "root", agent: "agent-m", model: "gpt-5", provider: "openai",
		status: "completed", startTS: base + 1_000, endTS: base + 3_000,
		tokensIn: 50, tokensOut: 50, costUSD: 0.01, turnCount: 1, opCount: 1,
	})
	seedTurn(t, db, turnRow{
		id: "t-M2", sessionID: "sess-M2", seq: 1, startTS: base + 1_000, endTS: base + 3_000,
		status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01, opCount: 1,
	})
	seedOp(t, db, opRow{
		id: "o-M2", turnID: "t-M2", sessionID: "sess-M2", seq: 1, kind: "llm", name: "gpt-5",
		model: "gpt-5", provider: "openai", startTS: base + 1_100, endTS: base + 2_500,
		durationUS: 1_400, status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01,
	})
}

// seedThreeSessions creates three sessions ("sess-T1", "sess-T2",
// "sess-T3") with cross-diverging kinds, models, and tools. Used to
// exercise the N=3 path of the compare endpoint.
func seedThreeSessions(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src-3", "aiagent_v3", "/tmp/3", base)
	// T1: claude-opus, Bash + Read
	seedSession(t, db, sessionRow{
		id: "sess-T1", sourceID: "src-3", nativeID: "nT1", rootID: "sess-T1",
		kind: "root", agent: "agent-t", model: "claude-opus-4-7", provider: "anthropic",
		status: "completed", startTS: base + 1_000, endTS: base + 3_000,
		tokensIn: 50, tokensOut: 50, costUSD: 0.01, turnCount: 1, opCount: 3,
	})
	seedTurn(t, db, turnRow{
		id: "t-T1", sessionID: "sess-T1", seq: 1, startTS: base + 1_000, endTS: base + 3_000,
		status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01, opCount: 3,
	})
	seedOp(t, db, opRow{
		id: "o-T1-1", turnID: "t-T1", sessionID: "sess-T1", seq: 1, kind: "llm", name: "claude-opus-4-7",
		model: "claude-opus-4-7", provider: "anthropic", startTS: base + 1_100, endTS: base + 1_500,
		durationUS: 400, status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01,
	})
	seedOp(t, db, opRow{
		id: "o-T1-2", turnID: "t-T1", sessionID: "sess-T1", seq: 2, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 1_600, endTS: base + 2_000, durationUS: 400,
		status: "completed",
	})
	seedOp(t, db, opRow{
		id: "o-T1-3", turnID: "t-T1", sessionID: "sess-T1", seq: 3, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 2_100, endTS: base + 2_500, durationUS: 400,
		status: "completed",
	})
	// T2: claude-haiku, Bash only
	seedSession(t, db, sessionRow{
		id: "sess-T2", sourceID: "src-3", nativeID: "nT2", rootID: "sess-T2",
		kind: "root", agent: "agent-t", model: "claude-haiku", provider: "anthropic",
		status: "completed", startTS: base + 1_000, endTS: base + 3_000,
		tokensIn: 50, tokensOut: 50, costUSD: 0.01, turnCount: 1, opCount: 2,
	})
	seedTurn(t, db, turnRow{
		id: "t-T2", sessionID: "sess-T2", seq: 1, startTS: base + 1_000, endTS: base + 3_000,
		status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01, opCount: 2,
	})
	seedOp(t, db, opRow{
		id: "o-T2-1", turnID: "t-T2", sessionID: "sess-T2", seq: 1, kind: "llm", name: "claude-haiku",
		model: "claude-haiku", provider: "anthropic", startTS: base + 1_100, endTS: base + 1_500,
		durationUS: 400, status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01,
	})
	seedOp(t, db, opRow{
		id: "o-T2-2", turnID: "t-T2", sessionID: "sess-T2", seq: 2, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 1_600, endTS: base + 2_000, durationUS: 400,
		status: "completed",
	})
	// T3: gpt-5, Read + Write
	seedSession(t, db, sessionRow{
		id: "sess-T3", sourceID: "src-3", nativeID: "nT3", rootID: "sess-T3",
		kind: "root", agent: "agent-t", model: "gpt-5", provider: "openai",
		status: "completed", startTS: base + 1_000, endTS: base + 3_000,
		tokensIn: 50, tokensOut: 50, costUSD: 0.01, turnCount: 1, opCount: 3,
	})
	seedTurn(t, db, turnRow{
		id: "t-T3", sessionID: "sess-T3", seq: 1, startTS: base + 1_000, endTS: base + 3_000,
		status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01, opCount: 3,
	})
	seedOp(t, db, opRow{
		id: "o-T3-1", turnID: "t-T3", sessionID: "sess-T3", seq: 1, kind: "llm", name: "gpt-5",
		model: "gpt-5", provider: "openai", startTS: base + 1_100, endTS: base + 1_500,
		durationUS: 400, status: "completed", tokensIn: 50, tokensOut: 50, costUSD: 0.01,
	})
	seedOp(t, db, opRow{
		id: "o-T3-2", turnID: "t-T3", sessionID: "sess-T3", seq: 2, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 1_600, endTS: base + 2_000, durationUS: 400,
		status: "completed",
	})
	seedOp(t, db, opRow{
		id: "o-T3-3", turnID: "t-T3", sessionID: "sess-T3", seq: 3, kind: "tool", name: "Write",
		toolNamespace: "fs", startTS: base + 2_100, endTS: base + 2_500, durationUS: 400,
		status: "completed",
	})
}
