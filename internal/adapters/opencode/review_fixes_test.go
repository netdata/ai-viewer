package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the SOW-0005 external-review fixes that are expressible at the
// PURE-MAPPER layer (no DB): the live-turn finalize predicate (P1.3), the
// step-start force-close (P2.5), and the load-bearing decode-failure warnings
// (P2.6 — malformed session.model JSON + malformed task metadata). The
// DB-level fixes (P1.1 checkpoint-after-emit, P2.4 nested root, P2.7
// session_message warn) are pinned in the tailer/store test files.

// --- P1.3: live in-progress turns are NOT finalized ---------------------------

// TestMapper_RunningTurnNotFinalized pins P1.3: an assistant message with NO
// data.time.completed, NO data.error, and NO step-finish part is a RUNNING turn —
// it emits TurnStarted but NOT TurnFinalized. opencode writes the message row live
// while the turn is still in progress; finalizing it would wrongly mark it
// completed. (A later poll re-emits + finalizes once it completes; idempotent.)
func TestMapper_RunningTurnNotFinalized(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_run", 0)
	// No completed ts, finish empty, and only a step-START (no step-finish).
	msg := asgMsg("msg_a", 2000, nil, "anthropic", "claude-x", tokenCounts{Input: 10, Output: 5}, 0.01, "", "")
	ss := stepStartAt("prt_ss", 2100)
	ev := run(t, s, []messageWithParts{mwp(msg, ss)})

	if got := countKind(ev, canonical.EvTurnStarted); got != 1 {
		t.Errorf("TurnStarted = %d, want 1", got)
	}
	if got := countKind(ev, canonical.EvTurnFinalized); got != 0 {
		t.Errorf("TurnFinalized = %d, want 0 (running turn: no completed ts, no error, no step-finish)", got)
	}
	// The open LLM op also stays running (no OpFinalized) per Edge #4.
	if got := countKind(ev, canonical.EvOpFinalized); got != 0 {
		t.Errorf("OpFinalized = %d, want 0 (the single open step has no finish)", got)
	}
}

// TestMapper_TurnFinalizedWhenTerminal pins the three terminal signals that DO
// finalize a turn (P1.3): a completed ts, OR a step-finish part, OR an error.
func TestMapper_TurnFinalizedWhenTerminal(t *testing.T) {
	t.Parallel()
	completed := int64(3000)

	cases := []struct {
		name  string
		msg   messageRow
		parts []partRow
	}{
		{
			name:  "completed ts set",
			msg:   asgMsg("msg_a", 2000, &completed, "anthropic", "claude-x", tokenCounts{Input: 10, Output: 5}, 0.01, "stop", ""),
			parts: []partRow{stepStartAt("prt_ss", 2100)},
		},
		{
			name:  "has step-finish part",
			msg:   asgMsg("msg_a", 2000, nil, "anthropic", "claude-x", tokenCounts{Input: 10, Output: 5}, 0.01, "stop", ""),
			parts: []partRow{stepStartAt("prt_ss", 2100), stepFinish("prt_sf", 10, 5, 0, 0, 0, 0.01)},
		},
		{
			name:  "has error",
			msg:   asgMsg("msg_a", 2000, nil, "anthropic", "claude-x", tokenCounts{Input: 10, Output: 5}, 0.01, "", "Overloaded"),
			parts: []partRow{stepStartAt("prt_ss", 2100)},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := run(t, rootSession("ses_done", 0), []messageWithParts{mwp(tc.msg, tc.parts...)})
			if got := countKind(ev, canonical.EvTurnFinalized); got != 1 {
				t.Errorf("TurnFinalized = %d, want 1 (terminal turn)", got)
			}
		})
	}
}

// --- P2.5: step-start force-closes the previous open LLM op -------------------

// TestMapper_TwoStepStartsForceCloseFirst pins P2.5 (spec Edge #5): two step-start
// parts with NO step-finish between them → the FIRST LLM op is force-closed with
// Status="cancelled" and EndTs = the second step-start's start ts; the SECOND op
// stays running (no finalize) because the turn ends with it still open.
func TestMapper_TwoStepStartsForceCloseFirst(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_orphan", 0)
	// Two step-starts at distinct times, no step-finish anywhere. completed set so
	// the TURN finalizes (isolating the op-level force-close from the turn gate).
	completed := int64(5000)
	msg := asgMsg("msg_a", 2000, &completed, "anthropic", "claude-x", tokenCounts{}, 0, "stop", "")
	ss1 := stepStartAt("prt_ss1", 2100)
	ss2 := stepStartAt("prt_ss2", 3300)
	ev := run(t, s, []messageWithParts{mwp(msg, ss1, ss2)})

	starts := llmOps(ev)
	if len(starts) != 2 {
		t.Fatalf("llm OpStarted count = %d, want 2", len(starts))
	}
	fins := opFinals(ev)
	if len(fins) != 1 {
		t.Fatalf("OpFinalized count = %d, want 1 (only the force-closed first op)", len(fins))
	}
	// The single finalize is the FIRST op (seq = first start's seq), cancelled,
	// EndTs = second step-start's start (3300 ms → µs).
	if fins[0].Seq != starts[0].Seq {
		t.Errorf("force-closed op Seq = %d, want first op Seq %d", fins[0].Seq, starts[0].Seq)
	}
	if fins[0].Status != "cancelled" {
		t.Errorf("force-closed op Status = %q, want cancelled", fins[0].Status)
	}
	if fins[0].EndTs != msToMicros(3300) {
		t.Errorf("force-closed op EndTs = %d, want %d (second step-start start)", fins[0].EndTs, msToMicros(3300))
	}
	// The SECOND op (the one still open at turn end) must have NO finalize.
	for _, f := range fins {
		if f.Seq == starts[1].Seq {
			t.Errorf("second op Seq %d was finalized; it must stay running", starts[1].Seq)
		}
	}
}

// TestMapper_NormalStepPairNotCancelled guards against over-firing P2.5: a normal
// step-start → step-finish → step-start → step-finish sequence finalizes BOTH ops
// "completed" with NO cancelled status (the first op closed normally before the
// second start, so Edge #5 must not trigger).
func TestMapper_NormalStepPairNotCancelled(t *testing.T) {
	t.Parallel()
	completed := int64(9000)
	msg := asgMsg("msg_a", 2000, &completed, "anthropic", "claude-x", tokenCounts{}, 0, "stop", "")
	parts := []partRow{
		stepStartAt("prt_ss1", 2100),
		stepFinish("prt_sf1", 100, 20, 0, 0, 0, 0.01),
		stepStartAt("prt_ss2", 4000),
		stepFinish("prt_sf2", 250, 50, 0, 0, 0, 0.02),
	}
	ev := run(t, rootSession("ses_pairs", 0), []messageWithParts{mwp(msg, parts...)})
	for _, f := range opFinals(ev) {
		if f.Status == "cancelled" {
			t.Errorf("op seq %d finalized cancelled; a normal step pair must close completed", f.Seq)
		}
	}
	if got := len(opFinals(ev)); got != 2 {
		t.Errorf("OpFinalized = %d, want 2 (both steps closed normally)", got)
	}
}

// --- P2.6: load-bearing decode failures surface a WARN ------------------------

// TestMapper_MalformedSessionModelWarns pins P2.6: a PRESENT-but-malformed
// session.model JSON degrades Model/provider to empty AND surfaces one WARN via
// the injected onWarn (rather than silently swallowing). The session is NOT
// aborted — the SessionStarted still emits.
func TestMapper_MalformedSessionModelWarns(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_badmodel", 0)
	s.Model = []byte(`{"id":`) // truncated JSON: present but unparseable

	var warns []error
	completed := int64(3000)
	msg := asgMsg("msg_a", 2000, &completed, "anthropic", "claude-x", tokenCounts{}, 0, "stop", "")
	evs, err := mapSession(testSourceID, s, []messageWithParts{mwp(msg, stepStartAt("prt_ss", 2100), stepFinish("prt_sf", 1, 1, 0, 0, 0, 0))},
		WithOnWarn(func(e error) { warns = append(warns, e) }))
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	if len(warns) == 0 {
		t.Fatal("malformed session.model produced no WARN (silent failure)")
	}
	ss := firstStarted(t, evs)
	if ss.Model != "" {
		t.Errorf("Model = %q, want empty (malformed model degraded)", ss.Model)
	}
	if got := countKind(evs, canonical.EvSessionStarted); got != 1 {
		t.Errorf("SessionStarted = %d, want 1 (session not aborted on malformed model)", got)
	}
}

// TestMapper_MalformedTaskMetadataWarns pins P2.6: a tool='task' part whose
// state.metadata is PRESENT but malformed surfaces one WARN (a possible sub-agent
// linkage was dropped) yet still emits the tool op for the task invocation.
func TestMapper_MalformedTaskMetadataWarns(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_badmeta", 0)
	completed := int64(5000)
	msg := asgMsg("msg_a", 2000, &completed, "anthropic", "claude-x", tokenCounts{}, 0, "stop", "")
	end := int64(4000)
	task := taskPartBadMetadata("prt_task", 3000, &end)

	var warns []error
	evs, err := mapSession(testSourceID, s,
		[]messageWithParts{mwp(msg, stepStartAt("prt_ss", 2100), task, stepFinish("prt_sf", 1, 1, 0, 0, 0, 0))},
		WithOnWarn(func(e error) { warns = append(warns, e) }))
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	if len(warns) == 0 {
		t.Fatal("malformed task metadata produced no WARN (silent linkage drop)")
	}
	// No session op (the child id could not be resolved) but the tool op survives.
	if got := countKindOpKind(evs, canonical.OpSession); got != 0 {
		t.Errorf("session ops = %d, want 0 (metadata unparseable → no child id)", got)
	}
	sawTask := false
	for _, op := range toolOps(evs) {
		if op.Name == "task" {
			sawTask = true
		}
	}
	if !sawTask {
		t.Error("tool op name=task missing; the invocation must still be recorded")
	}
}

// --- P2.4: resolveRootID chain walk edge cases (cycle / missing ancestor) -----

// TestResolveRootID_Edges pins resolveRootID's degrade paths (SOW-0005 P2.4): a
// root session resolves to itself; a clean 2-level chain resolves to the root; a
// MISSING ancestor row falls back to the furthest resolvable id + one WARN; a
// CYCLE is broken + one WARN. These are the branches the golden fixture (a clean
// 3-level tree) does not exercise.
func TestResolveRootID_Edges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// root <- child <- grand (clean chain); orphan -> missing parent; a 2-cycle.
	insertSession(t, rw, "ses_root", "", 1, 1, 0)
	insertSession(t, rw, "ses_child", "ses_root", 2, 2, 0)
	insertSession(t, rw, "ses_grand", "ses_child", 3, 3, 0)
	insertSession(t, rw, "ses_orphan", "ses_ghost", 4, 4, 0) // parent ses_ghost does not exist
	insertSession(t, rw, "ses_cycA", "ses_cycB", 5, 5, 0)
	insertSession(t, rw, "ses_cycB", "ses_cycA", 6, 6, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db := openRO(t, path)

	t.Run("root resolves to self with no query", func(t *testing.T) {
		var ce collectErrs
		if got := resolveRootID(ctxBG(), db, "ses_root", "", ce.onError); got != "ses_root" {
			t.Errorf("root = %q, want ses_root", got)
		}
		if ce.count() != 0 {
			t.Errorf("root resolution warned %d times, want 0", ce.count())
		}
	})
	t.Run("clean chain resolves to top", func(t *testing.T) {
		var ce collectErrs
		if got := resolveRootID(ctxBG(), db, "ses_grand", "ses_child", ce.onError); got != "ses_root" {
			t.Errorf("grand root = %q, want ses_root", got)
		}
		if ce.count() != 0 {
			t.Errorf("clean chain warned %d times, want 0", ce.count())
		}
	})
	t.Run("missing ancestor falls back + warns", func(t *testing.T) {
		var ce collectErrs
		// ses_orphan's parent ses_ghost is absent → fall back to ses_ghost (the
		// furthest resolvable ancestor) and warn.
		if got := resolveRootID(ctxBG(), db, "ses_orphan", "ses_ghost", ce.onError); got != "ses_ghost" {
			t.Errorf("orphan root = %q, want ses_ghost (furthest resolvable)", got)
		}
		if ce.count() != 1 {
			t.Errorf("missing-ancestor warned %d times, want 1", ce.count())
		}
	})
	t.Run("cycle is broken + warns", func(t *testing.T) {
		var ce collectErrs
		got := resolveRootID(ctxBG(), db, "ses_cycA", "ses_cycB", ce.onError)
		if got != "ses_cycA" && got != "ses_cycB" {
			t.Errorf("cycle root = %q, want one of the cycle ids (broken, not looping)", got)
		}
		if ce.count() != 1 {
			t.Errorf("cycle warned %d times, want 1", ce.count())
		}
	})
}

// --- P2.6: corrupt numeric cell surfaces a WARN, degrades to 0 ----------------

// TestLoadSession_CorruptNumericWarns pins the store-load half of P2.6: a session
// row whose numeric column holds non-numeric text (corrupt cell) degrades that
// field to 0 AND surfaces one WARN via onWarn — not silently swallowed. SQLite's
// flexible typing lets the fixture store a string in an INTEGER column.
func TestLoadSession_CorruptNumericWarns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// Insert a session with a NON-NUMERIC tokens_input (corrupt). All other
	// required columns are valid so the row loads; only the bad cell degrades.
	if _, err := rw.Exec(
		`INSERT INTO session (id, project_id, slug, directory, title, version, tokens_input, time_created, time_updated)
		 VALUES ('ses_corrupt','prj','slug','/w','T','9.9.9','not-a-number',100,100)`); err != nil {
		t.Fatalf("insert corrupt session: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	var warns []error
	s, ok, err := loadSession(ctxBG(), db, schema, "ses_corrupt", func(e error) { warns = append(warns, e) })
	if err != nil {
		t.Fatalf("loadSession: %v", err)
	}
	if !ok {
		t.Fatal("loadSession(ses_corrupt) ok=false, want true")
	}
	if s.TokensInput != 0 {
		t.Errorf("TokensInput = %d, want 0 (corrupt cell degraded)", s.TokensInput)
	}
	if len(warns) != 1 {
		t.Fatalf("corrupt cell produced %d warnings, want 1", len(warns))
	}
	if !strings.Contains(warns[0].Error(), "tokens_input") || !strings.Contains(warns[0].Error(), "corrupt numeric") {
		t.Errorf("warn = %q, want one naming the corrupt column", warns[0].Error())
	}
}

// TestParseCheckedAndPeek covers the small pure helpers' corrupt/malformed
// branches that the higher-level tests don't hit directly: parseInt64Checked /
// parseFloat64Checked on empty (ok), valid (ok), and corrupt (not ok); and
// peekPartType on empty/malformed/known bodies.
func TestParseCheckedAndPeek(t *testing.T) {
	t.Parallel()
	if v, ok := parseInt64Checked(""); v != 0 || !ok {
		t.Errorf("parseInt64Checked(empty) = (%d,%v), want (0,true)", v, ok)
	}
	if v, ok := parseInt64Checked("42"); v != 42 || !ok {
		t.Errorf("parseInt64Checked(42) = (%d,%v), want (42,true)", v, ok)
	}
	if v, ok := parseInt64Checked("nope"); v != 0 || ok {
		t.Errorf("parseInt64Checked(nope) = (%d,%v), want (0,false)", v, ok)
	}
	if v, ok := parseFloat64Checked(""); v != 0 || !ok {
		t.Errorf("parseFloat64Checked(empty) = (%v,%v), want (0,true)", v, ok)
	}
	if v, ok := parseFloat64Checked("1.5"); v != 1.5 || !ok {
		t.Errorf("parseFloat64Checked(1.5) = (%v,%v), want (1.5,true)", v, ok)
	}
	if v, ok := parseFloat64Checked("nope"); v != 0 || ok {
		t.Errorf("parseFloat64Checked(nope) = (%v,%v), want (0,false)", v, ok)
	}
	if got := peekPartType(nil); got != partUnknown {
		t.Errorf("peekPartType(nil) = %q, want unknown", got)
	}
	if got := peekPartType([]byte(`{bad json`)); got != partUnknown {
		t.Errorf("peekPartType(malformed) = %q, want unknown", got)
	}
	if got := peekPartType([]byte(`{"type":"step-finish"}`)); got != partStepFinish {
		t.Errorf("peekPartType(step-finish) = %q, want step-finish", got)
	}
}

// --- local synthetic-row builders for the review-fix scenarios ----------------

// stepStartAt builds a step-start partRow with an explicit time_created (ms), so a
// force-close EndTs (the NEXT step-start's start) is assertable. The shared
// stepStart helper carries no time.
func stepStartAt(id string, createdMs int64) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "step-start"})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", TimeCreatedMs: createdMs, TimeUpdatedMs: createdMs, Data: raw}
}

// taskPartBadMetadata builds a tool='task' part whose state.metadata is a JSON
// STRING (not the {sessionId:...} object the decoder expects), so
// subAgentSessionIDChecked reports malformed. completed state so the tool op
// finalizes.
func taskPartBadMetadata(id string, startMs int64, endMs *int64) partRow {
	state := map[string]any{
		"status":   "completed",
		"input":    map[string]any{"prompt": "x"},
		"output":   "done",
		"time":     map[string]any{"start": startMs, "end": *endMs},
		"metadata": "not-an-object", // present but the wrong shape → decode error
	}
	raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "call_" + id, "tool": "task", "state": state})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", TimeCreatedMs: startMs, TimeUpdatedMs: startMs, Data: raw}
}
