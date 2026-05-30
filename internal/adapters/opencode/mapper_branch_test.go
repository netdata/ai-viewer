package opencode

import (
	"encoding/json"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the mapper's defensive / edge branches that the happy-path
// tests in mapper_test.go do not reach: malformed bodies, orphan steps,
// missing-timestamp fallbacks, the chunk-D PayloadRef-URI seam, known-but-unused
// part types, and the top-level (no-LLM-op) op-parent case. Each test asserts a
// real behavior, not just line coverage.

// --- chunk-D PayloadRef URI seam (WithPayloadURIBuilder, payloadURI inject) ---

func TestMapSession_InjectedPayloadURIBuilder(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2200)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			reasoningPart("prt_2", 2000, &end, false),
		),
	}
	// Chunk D injects a builder that prefixes the resolved db basename.
	builder := func(partID, field string) string {
		return "opencode-sqlite://opencode.db?part_id=" + partID + "&field=" + field
	}
	evs, err := mapSession(testSourceID, s, msgs, WithPayloadURIBuilder(builder))
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	var found bool
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok && p.PayloadKind == "llm_reasoning" {
			found = true
			want := "opencode-sqlite://opencode.db?part_id=prt_2&field=text"
			if p.LocationURI != want {
				t.Fatalf("LocationURI = %q want %q (injected builder)", p.LocationURI, want)
			}
		}
	}
	if !found {
		t.Fatal("no llm_reasoning PayloadRef")
	}
}

func TestDefaultPayloadURI(t *testing.T) {
	got := defaultPayloadURI("prt_9", "state.output")
	want := "opencode-sqlite://?part_id=prt_9&field=state.output"
	if got != want {
		t.Fatalf("defaultPayloadURI = %q want %q", got, want)
	}
}

// --- orphan step-finish (no matching step-start) ------------------------------

func TestMapSession_OrphanStepFinishNoCrash(t *testing.T) {
	s := rootSession("ses_x", 0)
	// A step-finish with no preceding step-start: no LLM op to close. Must be a
	// no-op (adapter-opencode.md §"Edge Cases" #5), emitting no op finalize.
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepFinish("prt_1", 100, 10, 0, 0, 0, 0.1),
		),
	}
	evs := run(t, s, msgs)
	if n := len(opStarts(evs)); n != 0 {
		t.Fatalf("op starts = %d want 0 (orphan step-finish opens nothing)", n)
	}
	if n := len(opFinals(evs)); n != 0 {
		t.Fatalf("op finals = %d want 0 (orphan step-finish closes nothing)", n)
	}
}

// --- nextStepDelta out-of-range (more step-finishes than deltas) --------------

func TestNextStepDelta_OutOfRange(t *testing.T) {
	tc := &turnContext{stepDeltas: []tokenCounts{{Input: 5}}}
	if d := tc.nextStepDelta(); d.Input != 5 {
		t.Fatalf("first delta = %d want 5", d.Input)
	}
	// Second call is past the end → zero delta, no panic.
	if d := tc.nextStepDelta(); d != (tokenCounts{}) {
		t.Fatalf("out-of-range delta = %+v want zero", d)
	}
}

// --- missing-timestamp fallbacks ----------------------------------------------

func TestMapSession_ReasoningStartFallsBackToPartCreated(t *testing.T) {
	s := rootSession("ses_x", 0)
	// reasoning part with no time.start → falls back to the part's time_created.
	raw, _ := json.Marshal(map[string]any{"type": "reasoning", "text": "x"})
	p := partRow{ID: "prt_2", MessageID: "msg_a", SessionID: "ses_x", TimeCreatedMs: 1900, Data: raw}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"), p),
	}
	evs := run(t, s, msgs)
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpReasoning {
			if op.Ts != 1900*1000 {
				t.Fatalf("reasoning Ts = %d want %d (part time_created fallback)", op.Ts, 1900*1000)
			}
			return
		}
	}
	t.Fatal("no reasoning op")
}

func TestMapSession_ToolStartFallsBackToPartCreated(t *testing.T) {
	s := rootSession("ses_x", 0)
	// tool state with no time.start → op start falls back to part time_created.
	state := map[string]any{"status": "running", "input": map[string]any{}}
	raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "c", "tool": "bash", "state": state})
	p := partRow{ID: "prt_2", MessageID: "msg_a", SessionID: "ses_x", TimeCreatedMs: 1950, Data: raw}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"), p),
	}
	evs := run(t, s, msgs)
	tools := toolOps(evs)
	if len(tools) != 1 {
		t.Fatalf("tool op count = %d want 1", len(tools))
	}
	if tools[0].Ts != 1950*1000 {
		t.Fatalf("tool Ts = %d want %d (part time_created fallback)", tools[0].Ts, 1950*1000)
	}
}

// --- malformed part body → one WRN, no op -------------------------------------

func TestMapSession_MalformedPartSkippedWithWarn(t *testing.T) {
	s := rootSession("ses_x", 0)
	bad := partRow{ID: "prt_bad", MessageID: "msg_a", SessionID: "ses_x", Data: []byte("{not json")}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			bad),
	}
	evs := run(t, s, msgs)
	warns := 0
	for _, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "WRN" {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("WRN count = %d want 1 for malformed part", warns)
	}
	if n := len(opStarts(evs)); n != 0 {
		t.Fatalf("op starts = %d want 0 for malformed part", n)
	}
}

// --- unknown message role → one WRN -------------------------------------------

func TestMapSession_UnknownRoleSkippedWithWarn(t *testing.T) {
	s := rootSession("ses_x", 0)
	raw, _ := json.Marshal(map[string]any{"role": "system", "time": map[string]any{"created": 1500}})
	msgs := []messageWithParts{
		{Message: messageRow{ID: "msg_sys", SessionID: "ses_x", TimeCreatedMs: 1500, Data: raw}},
	}
	evs := run(t, s, msgs)
	if n := countKind(evs, canonical.EvTurnStarted); n != 0 {
		t.Fatalf("TurnStarted = %d want 0 for unknown role", n)
	}
	warns := 0
	for _, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "WRN" {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("WRN count = %d want 1 for unknown role", warns)
	}
}

// --- known-but-unused part types (snapshot/subtask/agent) → no-op -------------

func TestMapSession_KnownNoOpParts(t *testing.T) {
	s := rootSession("ses_x", 0)
	mk := func(id, typ string) partRow {
		raw, _ := json.Marshal(map[string]any{"type": typ})
		return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
	}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			mk("prt_2", "snapshot"),
			mk("prt_3", "subtask"),
			mk("prt_4", "agent"),
		),
	}
	evs := run(t, s, msgs)
	// Only the LLM op from step-start; no ops or logs from the known-no-op parts.
	for _, op := range opStarts(evs) {
		if op.Kind != canonical.OpLLM {
			t.Fatalf("unexpected op kind %q from known-no-op part", op.Kind)
		}
	}
	for _, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok {
			t.Fatalf("unexpected log %q for known-no-op part (must be silent)", l.Message)
		}
	}
}

// --- tool with unknown status + an end → finalized at that end ----------------

func TestMapSession_ToolUnknownStatusWithEnd(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2600)
	state := map[string]any{
		"status": "weird-future-status",
		"input":  map[string]any{"k": "v"},
		"output": "out",
		"time":   map[string]any{"start": int64(2000), "end": end},
	}
	raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "c", "tool": "bash", "state": state})
	p := partRow{ID: "prt_2", MessageID: "msg_a", SessionID: "ses_x", Data: raw}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"), p),
	}
	evs := run(t, s, msgs)
	var fin *canonical.OpFinalizedEvent
	for i, f := range opFinals(evs) {
		if f.Status == "weird-future-status" {
			fin = &opFinals(evs)[i]
		}
	}
	if fin == nil {
		t.Fatal("unknown-status tool with an end must still finalize")
	}
	if fin.EndTs != end*1000 {
		t.Fatalf("EndTs = %d want %d", fin.EndTs, end*1000)
	}
}

// --- tool with nil state → no finalize ----------------------------------------

func TestMapSession_ToolNilStateNoFinalize(t *testing.T) {
	s := rootSession("ses_x", 0)
	raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "c", "tool": "bash"}) // no state
	p := partRow{ID: "prt_2", MessageID: "msg_a", SessionID: "ses_x", TimeCreatedMs: 2000, Data: raw}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"), p),
	}
	evs := run(t, s, msgs)
	tools := toolOps(evs)
	if len(tools) != 1 {
		t.Fatalf("tool op count = %d want 1", len(tools))
	}
	for _, f := range opFinals(evs) {
		if f.Seq == tools[0].Seq && f.TurnSeq == tools[0].TurnSeq {
			t.Fatal("tool with nil state must NOT finalize")
		}
	}
}

// --- tool op before any step-start → ParentOpSeq = -1 (top-level) -------------

func TestMapSession_ToolBeforeStepIsTopLevel(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2500)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			toolPart("prt_1", "bash", "completed", 2000, &end, nil), // no preceding step-start
		),
	}
	evs := run(t, s, msgs)
	tools := toolOps(evs)
	if len(tools) != 1 {
		t.Fatalf("tool op count = %d want 1", len(tools))
	}
	if tools[0].ParentOpSeq != -1 {
		t.Fatalf("ParentOpSeq = %d want -1 (top-level, no LLM op open)", tools[0].ParentOpSeq)
	}
}

// --- file part before any LLM op → dropped (op_id NOT NULL) -------------------

func TestMapSession_FileBeforeLLMOpDropped(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			filePart("prt_1", "https://cdn.example.invalid/x.png"), // before any step-start
		),
	}
	evs := run(t, s, msgs)
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok && p.PayloadKind == "user_attachment" {
			t.Fatal("file PayloadRef before any LLM op must be dropped (op_id NOT NULL)")
		}
	}
}

// --- patch before any step-start → dropped (no LLM op to attach extras) -------

func TestMapSession_PatchBeforeStepDropped(t *testing.T) {
	s := rootSession("ses_x", 0)
	c := int64(3000)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, &c, "the-alias", "the-model", tokenCounts{Input: 10}, 0.1, "stop", ""),
			patchPart("prt_1"), // before any step-start → no LLM op, patch dropped
			stepStart("prt_2"),
			stepFinish("prt_3", 10, 1, 0, 0, 0, 0.1),
		),
	}
	evs := run(t, s, msgs)
	// The LLM op opens AFTER the patch, so its extras must NOT carry patch_files.
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpLLM {
			if _, ok := op.Extras["patch_files"]; ok {
				t.Fatal("patch before any step-start must be dropped, not grafted onto a later op")
			}
		}
	}
}

// --- jsonTrimBytes null/empty handling ----------------------------------------

func TestJSONTrimBytes(t *testing.T) {
	if b := jsonTrimBytes([]byte("  null  ")); b != nil {
		t.Fatalf("null → %q want nil", b)
	}
	if b := jsonTrimBytes([]byte("   ")); b != nil {
		t.Fatalf("blank → %q want nil", b)
	}
	if b := jsonTrimBytes([]byte(` {"a":1} `)); string(b) != `{"a":1}` {
		t.Fatalf("object → %q want trimmed object", b)
	}
}

// --- byte-accounting + logEntry defensive guards (nil state / nil extras) -----

func TestToolBytes_NilState(t *testing.T) {
	// A partData with no state contributes zero bytes either way.
	if n := toolBytesIn(partData{}); n != 0 {
		t.Fatalf("toolBytesIn(nil state) = %d want 0", n)
	}
	if n := toolBytesOut(partData{}); n != 0 {
		t.Fatalf("toolBytesOut(nil state) = %d want 0", n)
	}
}

func TestLogEntry_NilExtrasDefaulted(t *testing.T) {
	m := newSessionMapper(testSourceID, rootSession("ses_x", 0))
	// A nil extras map must be defaulted to an empty (non-nil) map so the event
	// carries an addressable Extras.
	ev := m.logEntry(m.nextBase(0), "INF", 0, 0, "x", nil)
	if ev.Extras == nil {
		t.Fatal("logEntry Extras must be non-nil even when passed nil")
	}
}

// --- bytes_in / bytes_out on a completed tool ---------------------------------

func TestMapSession_ToolBytesAccounting(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2500)
	state := map[string]any{
		"status": "completed",
		"input":  map[string]any{"command": "ls"},
		"output": "file1\nfile2",
		"time":   map[string]any{"start": int64(2000), "end": end},
	}
	raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "c", "tool": "bash", "state": state})
	p := partRow{ID: "prt_2", MessageID: "msg_a", SessionID: "ses_x", Data: raw}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"), p),
	}
	evs := run(t, s, msgs)
	for _, f := range opFinals(evs) {
		if f.Status == "completed" && f.BytesOut > 0 {
			if int(f.BytesOut) != len("file1\nfile2") {
				t.Fatalf("BytesOut = %d want %d", f.BytesOut, len("file1\nfile2"))
			}
			if f.BytesIn <= 0 {
				t.Fatalf("BytesIn = %d want >0 (serialized input length)", f.BytesIn)
			}
			return
		}
	}
	t.Fatal("no completed tool finalize with byte accounting")
}

// --- malformed session.model is tolerated (zero model) ------------------------

func TestMapSession_MalformedSessionModelTolerated(t *testing.T) {
	s := rootSession("ses_x", 0)
	s.Model = []byte("{not json")
	evs := run(t, s, nil)
	st := firstStarted(t, evs)
	if st.Model != "" {
		t.Fatalf("Model = %q want empty for malformed session.model", st.Model)
	}
}

// --- session with no extras at all → nil Extras -------------------------------

func TestMapSession_NoExtrasYieldsNil(t *testing.T) {
	// A bare session row (older schema, only required cols) carries no extras.
	s := sessionRow{ID: "ses_bare", TimeCreatedMs: 1000}
	evs := run(t, s, nil)
	st := firstStarted(t, evs)
	if st.Extras != nil {
		t.Fatalf("Extras = %v want nil for a bare session", st.Extras)
	}
}
