package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file is the executable contract for the pure row→event mapper (SOW-0005
// chunk B). Every test feeds SYNTHETIC typed rows (sessionRow / messageRow /
// partRow built by the helpers below) directly to mapSession and asserts the
// exact emitted canonical event stream. No DB, no operator data, no AI-vendor
// names — only schema-shaped synthetic values (adapter-opencode.md §"Sensitive
// content"; SOW-0005 R5).

const testSourceID = "opencode:/test/opencode.db"

// canonicalPayloadKinds is the EXACT canonical PayloadRefEvent.PayloadKind set
// (internal/canonical/events.go:323-326). The adapter must never emit a kind
// outside this set (SOW-0005 round-4 P2-3 removed the non-canonical
// "user_attachment"); tests assert every emitted PayloadRef's kind is a member.
var canonicalPayloadKinds = map[string]bool{
	"llm_request":      true,
	"llm_response":     true,
	"llm_sdk_request":  true,
	"llm_sdk_response": true,
	"llm_reasoning":    true,
	"tool_request":     true,
	"tool_response":    true,
	"log":              true,
}

// --- synthetic-row builders ---------------------------------------------------

// asgMsg builds an assistant messageRow with the given id/time and a data body
// carrying providerID/modelID/tokens/cost/finish and an optional completed ms.
// The tokens object is the TURN ROLLUP (cumulative across the session per the
// SOW decision); per-op step deltas live on the step-finish parts.
func asgMsg(id string, createdMs int64, completedMs *int64, provider, model string, tok tokenCounts, cost float64, finish string, errName string) messageRow {
	d := map[string]any{
		"role":       "assistant",
		"providerID": provider,
		"modelID":    model,
		"agent":      "test-agent",
		"cost":       cost,
		"tokens":     tok,
		"time":       map[string]any{"created": createdMs},
		"finish":     finish,
	}
	if completedMs != nil {
		d["time"] = map[string]any{"created": createdMs, "completed": *completedMs}
	}
	if errName != "" {
		d["error"] = map[string]any{"name": errName}
	}
	raw, _ := json.Marshal(d)
	return messageRow{ID: id, SessionID: "ses_x", TimeCreatedMs: createdMs, TimeUpdatedMs: createdMs, Data: raw}
}

// usrMsg builds a user messageRow (the mapper emits no turn for it; it only
// anchors the assistant turn that follows).
func usrMsg(id string, createdMs int64) messageRow {
	raw, _ := json.Marshal(map[string]any{"role": "user", "time": map[string]any{"created": createdMs}})
	return messageRow{ID: id, SessionID: "ses_x", TimeCreatedMs: createdMs, TimeUpdatedMs: createdMs, Data: raw}
}

// stepStart / stepFinish build the LLM-op delimiter parts. stepFinish carries
// CUMULATIVE token counts within the message (the mapper deltas them).
func stepStart(id string) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "step-start"})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

func stepFinish(id string, inCum, outCum, reasonCum, cacheRdCum, cacheWrCum int64, cost float64) partRow {
	raw, _ := json.Marshal(map[string]any{
		"type":   "step-finish",
		"reason": "stop",
		"cost":   cost,
		"tokens": map[string]any{
			"input":     inCum,
			"output":    outCum,
			"reasoning": reasonCum,
			"cache":     map[string]any{"read": cacheRdCum, "write": cacheWrCum},
		},
	})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

// toolPart builds a tool partRow with the given tool name and state status.
func toolPart(id, tool, status string, startMs int64, endMs *int64, metadata map[string]any) partRow {
	state := map[string]any{
		"status": status,
		"input":  map[string]any{"q": "x"},
		"output": "result-bytes",
		"time":   map[string]any{"start": startMs},
	}
	if endMs != nil {
		state["time"] = map[string]any{"start": startMs, "end": *endMs}
	}
	if metadata != nil {
		state["metadata"] = metadata
	}
	raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "call_" + id, "tool": tool, "state": state})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

// reasoningPart builds a reasoning partRow. summary=true sets metadata.summary.
func reasoningPart(id string, startMs int64, endMs *int64, summary bool) partRow {
	d := map[string]any{"type": "reasoning", "text": "thinking", "time": map[string]any{"start": startMs}}
	if endMs != nil {
		d["time"] = map[string]any{"start": startMs, "end": *endMs}
	}
	if summary {
		d["metadata"] = map[string]any{"summary": true}
	}
	raw, _ := json.Marshal(d)
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

func textPart(id string) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "text", "text": "final answer"})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

func patchPart(id string) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "patch", "hash": "deadbeef", "files": []string{"/x/a.go", "/x/b.go"}})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

func compactionPart(id string, auto bool) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "compaction", "auto": auto})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

func retryPart(id string, attempt int) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "retry", "attempt": attempt, "error": map[string]any{"name": "RateLimit"}})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

func filePart(id, url string) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "file", "mime": "image/png", "filename": "x.png", "url": url})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

func unknownPart(id string) partRow {
	raw, _ := json.Marshal(map[string]any{"type": "future-thing", "foo": 1})
	return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
}

// rootSession builds a root sessionRow with the given id and optional model.
func rootSession(id string, archivedMs int64) sessionRow {
	model, _ := json.Marshal(map[string]any{"id": "the-model", "providerID": "the-alias"})
	return sessionRow{
		ID: id, ProjectID: "prj_1", Slug: "test-slug", Directory: "/work/dir",
		Title: "Test Title", Version: "9.9.9", Agent: "test-agent", Model: model,
		TimeCreatedMs: 1000, TimeUpdatedMs: 1000, TimeArchivedMs: archivedMs,
	}
}

// run drives mapSession with a fresh mapper and returns the emitted stream.
func run(t *testing.T, s sessionRow, msgs []messageWithParts) []canonical.Event {
	t.Helper()
	evs, err := mapSession(testSourceID, s, msgs)
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	return evs
}

// mwp pairs a message with its ordered parts (the unit mapSession consumes).
func mwp(m messageRow, parts ...partRow) messageWithParts {
	return messageWithParts{Message: m, Parts: parts}
}

// --- assertion helpers (mirror codex/mapper_helpers_test.go) ------------------

func countKind(events []canonical.Event, kind canonical.EventKind) int {
	n := 0
	for _, ev := range events {
		if ev.EventKind() == kind {
			n++
		}
	}
	return n
}

func opStarts(events []canonical.Event) []canonical.OpStartedEvent {
	var out []canonical.OpStartedEvent
	for _, ev := range events {
		if s, ok := ev.(canonical.OpStartedEvent); ok {
			out = append(out, s)
		}
	}
	return out
}

func opFinals(events []canonical.Event) []canonical.OpFinalizedEvent {
	var out []canonical.OpFinalizedEvent
	for _, ev := range events {
		if f, ok := ev.(canonical.OpFinalizedEvent); ok {
			out = append(out, f)
		}
	}
	return out
}

func turnFinals(events []canonical.Event) []canonical.TurnFinalizedEvent {
	var out []canonical.TurnFinalizedEvent
	for _, ev := range events {
		if f, ok := ev.(canonical.TurnFinalizedEvent); ok {
			out = append(out, f)
		}
	}
	return out
}

func firstStarted(t *testing.T, events []canonical.Event) canonical.SessionStartedEvent {
	t.Helper()
	for _, ev := range events {
		if s, ok := ev.(canonical.SessionStartedEvent); ok {
			return s
		}
	}
	t.Fatal("no SessionStartedEvent in stream")
	return canonical.SessionStartedEvent{}
}

func llmOps(events []canonical.Event) []canonical.OpStartedEvent {
	var out []canonical.OpStartedEvent
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpLLM {
			out = append(out, s)
		}
	}
	return out
}

func toolOps(events []canonical.Event) []canonical.OpStartedEvent {
	var out []canonical.OpStartedEvent
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpTool {
			out = append(out, s)
		}
	}
	return out
}

// --- SessionStarted + terminal status ----------------------------------------

func TestMapSession_RootSessionStarted(t *testing.T) {
	s := rootSession("ses_root", 0)
	evs := run(t, s, nil)
	st := firstStarted(t, evs)
	if st.NativeID != "ses_root" {
		t.Fatalf("NativeID = %q, want ses_root", st.NativeID)
	}
	if st.Kind != canonical.KindRoot {
		t.Fatalf("Kind = %q, want root", st.Kind)
	}
	if st.RootNativeID != "ses_root" {
		t.Fatalf("RootNativeID = %q, want ses_root (self)", st.RootNativeID)
	}
	if st.ParentNativeID != "" {
		t.Fatalf("ParentNativeID = %q, want empty", st.ParentNativeID)
	}
	if st.AgentName != "test-agent" {
		t.Fatalf("AgentName = %q, want test-agent", st.AgentName)
	}
	if st.Model != "the-model" {
		t.Fatalf("Model = %q, want the-model (from session.model $.id)", st.Model)
	}
	if st.Cwd != "/work/dir" {
		t.Fatalf("Cwd = %q, want /work/dir", st.Cwd)
	}
	// Ts is ms→µs.
	if st.Ts != 1000*1000 {
		t.Fatalf("Ts = %d, want %d (ms→µs)", st.Ts, 1000*1000)
	}
	// Extras carry providerID/version/slug/title/project_id/directory.
	for _, k := range []string{"providerID", "version", "slug", "title", "project_id", "directory"} {
		if _, ok := st.Extras[k]; !ok {
			t.Fatalf("Extras missing %q: %v", k, st.Extras)
		}
	}
	// Running session (no archive, no error) => NO SessionFinalized.
	if n := countKind(evs, canonical.EvSessionFinalized); n != 0 {
		t.Fatalf("SessionFinalized count = %d, want 0 (running)", n)
	}
}

func TestMapSession_SubAgentLinkage(t *testing.T) {
	s := rootSession("ses_child", 0)
	s.ParentID = "ses_parent"
	evs := run(t, s, nil)
	st := firstStarted(t, evs)
	if st.Kind != canonical.KindSubAgent {
		t.Fatalf("Kind = %q, want sub_agent", st.Kind)
	}
	if st.ParentNativeID != "ses_parent" {
		t.Fatalf("ParentNativeID = %q, want ses_parent", st.ParentNativeID)
	}
	if st.RootNativeID != "ses_parent" {
		t.Fatalf("RootNativeID = %q, want ses_parent", st.RootNativeID)
	}
}

func TestMapSession_TerminalArchivedCompleted(t *testing.T) {
	s := rootSession("ses_arch", 5000)
	evs := run(t, s, nil)
	fins := finalizes(evs)
	if len(fins) != 1 {
		t.Fatalf("SessionFinalized count = %d, want 1", len(fins))
	}
	if fins[0].Status != canonical.StatusCompleted {
		t.Fatalf("Status = %q, want completed", fins[0].Status)
	}
	if fins[0].EndTs != 5000*1000 {
		t.Fatalf("EndTs = %d, want %d (archived ms→µs)", fins[0].EndTs, 5000*1000)
	}
}

func TestMapSession_TerminalFailedFromError(t *testing.T) {
	s := rootSession("ses_fail", 0)
	completed := int64(2000)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, &completed, "the-alias", "the-model", tokenCounts{Input: 10}, 0.1, "error", "ProviderError")),
	}
	evs := run(t, s, msgs)
	fins := finalizes(evs)
	if len(fins) != 1 {
		t.Fatalf("SessionFinalized count = %d, want 1", len(fins))
	}
	if fins[0].Status != canonical.StatusFailed {
		t.Fatalf("Status = %q, want failed", fins[0].Status)
	}
	if fins[0].ErrorClass != "ProviderError" {
		t.Fatalf("ErrorClass = %q, want ProviderError", fins[0].ErrorClass)
	}
}

func TestMapSession_RunningNoTerminalWhenIncomplete(t *testing.T) {
	s := rootSession("ses_run", 0)
	// assistant message with no completed ts and no error => session stays running.
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{Input: 10}, 0.1, "", "")),
	}
	evs := run(t, s, msgs)
	if n := countKind(evs, canonical.EvSessionFinalized); n != 0 {
		t.Fatalf("SessionFinalized count = %d, want 0 (running)", n)
	}
}

func finalizes(events []canonical.Event) []canonical.SessionFinalizedEvent {
	var out []canonical.SessionFinalizedEvent
	for _, ev := range events {
		if f, ok := ev.(canonical.SessionFinalizedEvent); ok {
			out = append(out, f)
		}
	}
	return out
}

// --- Turn synthesis + per-turn token deltas -----------------------------------

func TestMapSession_UserMessageEmitsNothing(t *testing.T) {
	s := rootSession("ses_x", 0)
	c := int64(3000)
	// A user message precedes the assistant turn (opencode pairs user→assistant;
	// the assistant message IS the turn). The user message must emit nothing and
	// must NOT consume a turn Seq.
	msgs := []messageWithParts{
		mwp(usrMsg("msg_u", 1400)),
		mwp(asgMsg("msg_a", 1500, &c, "the-alias", "the-model", tokenCounts{Input: 50, Output: 5}, 0.1, "stop", "")),
	}
	evs := run(t, s, msgs)
	if n := countKind(evs, canonical.EvTurnStarted); n != 1 {
		t.Fatalf("TurnStarted = %d want 1 (user message must not open a turn)", n)
	}
	tf := turnFinals(evs)
	if len(tf) != 1 || tf[0].Seq != 1 {
		t.Fatalf("turn finals = %+v want one turn with Seq=1", tf)
	}
}

func TestMapSession_TurnNumberingAndTokenDeltas(t *testing.T) {
	s := rootSession("ses_x", 0)
	c1 := int64(2000)
	c2 := int64(4000)
	// Turn 1 cumulative-at-completion tokens: in=100,out=20. Turn 2: in=260,out=55.
	// Per-turn DELTA: turn1 = 100/20; turn2 = 160/35 (SOW decision: message-level
	// cumulative → delta from prior assistant message).
	t1 := tokenCounts{Input: 100, Output: 20, Cache: cacheTokens{Read: 5, Write: 1}}
	t2 := tokenCounts{Input: 260, Output: 55, Cache: cacheTokens{Read: 30, Write: 4}}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, &c1, "the-alias", "the-model", t1, 0.10, "stop", "")),
		mwp(asgMsg("msg_b", 3500, &c2, "the-alias", "the-model", t2, 0.25, "stop", "")),
	}
	evs := run(t, s, msgs)
	if n := countKind(evs, canonical.EvTurnStarted); n != 2 {
		t.Fatalf("TurnStarted count = %d, want 2", n)
	}
	tf := turnFinals(evs)
	if len(tf) != 2 {
		t.Fatalf("TurnFinalized count = %d, want 2", len(tf))
	}
	if tf[0].Seq != 1 || tf[1].Seq != 2 {
		t.Fatalf("turn seqs = %d,%d want 1,2", tf[0].Seq, tf[1].Seq)
	}
	if tf[0].TokensIn != 100 || tf[0].TokensOut != 20 {
		t.Fatalf("turn1 tokens = %d/%d want 100/20", tf[0].TokensIn, tf[0].TokensOut)
	}
	if tf[1].TokensIn != 160 || tf[1].TokensOut != 35 {
		t.Fatalf("turn2 tokens = %d/%d want 160/35 (delta)", tf[1].TokensIn, tf[1].TokensOut)
	}
	// Per-turn cache deltas work via TurnFinalizedEvent (SOW decision #4).
	if tf[1].TokensCacheRead != 25 || tf[1].TokensCacheWrite != 3 {
		t.Fatalf("turn2 cache = %d/%d want 25/3 (delta)", tf[1].TokensCacheRead, tf[1].TokensCacheWrite)
	}
	// Per-turn cost is the message-level cost verbatim (not a delta — cost is
	// already per-message in opencode).
	if tf[1].CostUSD != 0.25 {
		t.Fatalf("turn2 cost = %v want 0.25", tf[1].CostUSD)
	}
	if tf[0].Status != "completed" {
		t.Fatalf("turn1 status = %q want completed", tf[0].Status)
	}
}

// --- computeStepDeltas (AC#3) -------------------------------------------------

func TestComputeStepDeltas_AC3(t *testing.T) {
	// AC#3: three step-finish parts with cumulative input 100,250,410 → per-op
	// deltas 100,150,160.
	cums := []tokenCounts{
		{Input: 100, Output: 10, Reasoning: 1, Cache: cacheTokens{Read: 1000, Write: 5}},
		{Input: 250, Output: 25, Reasoning: 3, Cache: cacheTokens{Read: 2500, Write: 5}},
		{Input: 410, Output: 40, Reasoning: 6, Cache: cacheTokens{Read: 4100, Write: 5}},
	}
	got := computeStepDeltas(cums, nil)
	wantIn := []int64{100, 150, 160}
	wantOut := []int64{10, 15, 15}
	wantReason := []int64{1, 2, 3}
	wantCacheRd := []int64{1000, 1500, 1600}
	wantCacheWr := []int64{5, 0, 0}
	if len(got) != 3 {
		t.Fatalf("deltas len = %d want 3", len(got))
	}
	for i := range got {
		if got[i].Input != wantIn[i] {
			t.Errorf("delta[%d].Input = %d want %d", i, got[i].Input, wantIn[i])
		}
		if got[i].Output != wantOut[i] {
			t.Errorf("delta[%d].Output = %d want %d", i, got[i].Output, wantOut[i])
		}
		if got[i].Reasoning != wantReason[i] {
			t.Errorf("delta[%d].Reasoning = %d want %d", i, got[i].Reasoning, wantReason[i])
		}
		if got[i].Cache.Read != wantCacheRd[i] {
			t.Errorf("delta[%d].Cache.Read = %d want %d", i, got[i].Cache.Read, wantCacheRd[i])
		}
		if got[i].Cache.Write != wantCacheWr[i] {
			t.Errorf("delta[%d].Cache.Write = %d want %d", i, got[i].Cache.Write, wantCacheWr[i])
		}
	}
}

func TestComputeStepDeltas_NonMonotonicClampsToZero(t *testing.T) {
	// Defensive: a non-monotonic sequence (a reset / out-of-order observation)
	// must never emit a negative delta. The delta clamps to 0 (spec gap #3 —
	// reconciliation recomputes the whole message; a clamp keeps a transient
	// observation from corrupting cost with negatives).
	cums := []tokenCounts{
		{Input: 300},
		{Input: 100}, // regression
		{Input: 150},
	}
	got := computeStepDeltas(cums, nil)
	want := []int64{300, 0, 50}
	for i := range got {
		if got[i].Input != want[i] {
			t.Errorf("delta[%d].Input = %d want %d", i, got[i].Input, want[i])
		}
	}
}

func TestComputeStepDeltas_Empty(t *testing.T) {
	if got := computeStepDeltas(nil, nil); len(got) != 0 {
		t.Fatalf("computeStepDeltas(nil) len = %d want 0", len(got))
	}
}

// --- LLM ops from step-start/step-finish + per-op token deltas ----------------

func TestMapSession_LLMOpsStepDeltas(t *testing.T) {
	s := rootSession("ses_x", 0)
	c1 := int64(4000)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, &c1, "the-alias", "the-model", tokenCounts{Input: 410, Output: 40}, 0.3, "stop", ""),
			stepStart("prt_1"),
			stepFinish("prt_2", 100, 10, 0, 0, 0, 0.1),
			stepStart("prt_3"),
			stepFinish("prt_4", 250, 25, 0, 0, 0, 0.2),
			stepStart("prt_5"),
			stepFinish("prt_6", 410, 40, 0, 0, 0, 0.3),
		),
	}
	evs := run(t, s, msgs)
	llm := llmOps(evs)
	if len(llm) != 3 {
		t.Fatalf("LLM op count = %d want 3", len(llm))
	}
	for _, op := range llm {
		if op.Model != "the-model" {
			t.Errorf("LLM op Model = %q want the-model", op.Model)
		}
		if op.ProviderAlias != "the-alias" {
			t.Errorf("LLM op ProviderAlias = %q want the-alias", op.ProviderAlias)
		}
		if op.Provider == "" {
			t.Errorf("LLM op Provider must be non-empty (catalog seeding requires it)")
		}
	}
	fin := opFinals(evs)
	// Collect LLM finalize token deltas in op order.
	var inDeltas []int64
	for _, f := range fin {
		// LLM ops are seqs 1,3,5 (steps interleave with finishes); match by
		// presence of token deltas — only LLM finalizes carry tokens here.
		if f.TokensIn > 0 || f.TokensOut > 0 {
			inDeltas = append(inDeltas, f.TokensIn)
		}
	}
	if len(inDeltas) != 3 || inDeltas[0] != 100 || inDeltas[1] != 150 || inDeltas[2] != 160 {
		t.Fatalf("LLM op token-in deltas = %v want [100 150 160]", inDeltas)
	}
}

// --- Tool ops + namespace derivation ------------------------------------------

func TestMapSession_ToolOpNamespaceMCP(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2500)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			toolPart("prt_2", "github_get_file_contents", "completed", 2000, &end, nil),
			stepFinish("prt_3", 10, 1, 0, 0, 0, 0.01),
		),
	}
	evs := run(t, s, msgs)
	tools := toolOps(evs)
	if len(tools) != 1 {
		t.Fatalf("tool op count = %d want 1", len(tools))
	}
	if tools[0].Name != "get_file_contents" {
		t.Fatalf("tool Name = %q want get_file_contents", tools[0].Name)
	}
	if tools[0].ToolNamespace != "github" {
		t.Fatalf("tool ToolNamespace = %q want github", tools[0].ToolNamespace)
	}
	// ParentOpSeq must point at the open LLM op (the step-start's seq).
	if tools[0].ParentOpSeq <= 0 {
		t.Fatalf("tool ParentOpSeq = %d want >0 (under LLM op)", tools[0].ParentOpSeq)
	}
}

func TestMapSession_ToolOpBuiltinNoNamespace(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2500)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			toolPart("prt_2", "bash", "completed", 2000, &end, nil),
		),
	}
	evs := run(t, s, msgs)
	tools := toolOps(evs)
	if len(tools) != 1 {
		t.Fatalf("tool op count = %d want 1", len(tools))
	}
	if tools[0].Name != "bash" {
		t.Fatalf("tool Name = %q want bash", tools[0].Name)
	}
	if tools[0].ToolNamespace != "" {
		t.Fatalf("tool ToolNamespace = %q want empty (builtin)", tools[0].ToolNamespace)
	}
}

// TestMapSession_ToolOpStatusError pins P1-C (SOW-0005 round-2): an opencode tool
// whose state.status == "error" must finalize with the CANONICAL op status
// "failed" (NOT the non-canonical "error"), carrying the opencode detail in
// ErrorClass (a class label) + ErrorMessage (state.error). canonical op statuses
// are running|completed|failed|cancelled|truncated (canonical-events.md:196).
func TestMapSession_ToolOpStatusError(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2500)
	errState := func(id string) partRow {
		state := map[string]any{
			"status": "error",
			"input":  map[string]any{},
			"error":  "boom",
			"time":   map[string]any{"start": int64(2000), "end": end},
		}
		raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "c", "tool": "bash", "state": state})
		return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
	}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			errState("prt_2"),
		),
	}
	evs := run(t, s, msgs)
	fin := opFinals(evs)
	// No finalize may carry the non-canonical "error" status.
	for i := range fin {
		if fin[i].Status == "error" {
			t.Fatalf("tool OpFinalized carries non-canonical status %q (P1-C: must be 'failed')", fin[i].Status)
		}
	}
	var toolFin *canonical.OpFinalizedEvent
	for i := range fin {
		if fin[i].Status == "failed" {
			toolFin = &fin[i]
		}
	}
	if toolFin == nil {
		t.Fatalf("no tool OpFinalized with status=failed in %d finals (P1-C: opencode 'error' → canonical 'failed')", len(fin))
	}
	if toolFin.ErrorMessage != "boom" {
		t.Fatalf("tool error message = %q want boom", toolFin.ErrorMessage)
	}
	if toolFin.ErrorClass != defaultErrorClass {
		t.Fatalf("tool ErrorClass = %q want %q (P1-C carries a class label)", toolFin.ErrorClass, defaultErrorClass)
	}
	// SOW-0005 round-6 P2-1: this failed tool carries ONLY state.error (no
	// state.output), so NO tool_response PayloadRef may be emitted — the future
	// resolver would fetch a state.output body that does not exist. The detail is
	// already in ErrorMessage above.
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok && p.PayloadKind == "tool_response" {
			t.Fatalf("failed tool with only state.error emitted a tool_response PayloadRef (uri=%q); none must be emitted (round-6 P2-1)", p.LocationURI)
		}
	}
}

// TestMapSession_FailedToolNoOutputNoPayloadRef pins SOW-0005 round-6 P2-1 directly:
// a failed tool (state.status="error") whose state has ONLY an error string and NO
// state.output emits NO tool_response PayloadRef (its detail rides in ErrorMessage),
// while a COMPLETED tool WITH state.output still emits the tool_response ref. The two
// cases share one mapSession run so the gate (state.output != "", not the status) is
// pinned against both shapes at once.
func TestMapSession_FailedToolNoOutputNoPayloadRef(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2600)
	// A failed tool with only state.error (no output).
	failNoOut := func(id string) partRow {
		state := map[string]any{
			"status": "error",
			"input":  map[string]any{"command": "make"},
			"error":  "command failed",
			"time":   map[string]any{"start": int64(2000), "end": end},
		}
		raw, _ := json.Marshal(map[string]any{"type": "tool", "callID": "c1", "tool": "bash", "state": state})
		return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
	}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			failNoOut("prt_fail"), // failed, only state.error → NO ref
			toolPart("prt_ok", "read", "completed", 2700, &end, nil), // completed WITH output → ref
		),
	}
	evs := run(t, s, msgs)

	// Collect tool_response refs and the part ids they point at.
	var refParts []string
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok && p.PayloadKind == "tool_response" {
			refParts = append(refParts, p.LocationURI)
		}
	}
	// Exactly ONE tool_response ref, for the completed tool (prt_ok) — never for the
	// failed-no-output tool (prt_fail).
	if len(refParts) != 1 {
		t.Fatalf("tool_response PayloadRef count = %d (%v), want exactly 1 (the completed tool with output)", len(refParts), refParts)
	}
	if !strings.Contains(refParts[0], "part_id=prt_ok") {
		t.Errorf("the sole tool_response ref points at %q, want the completed tool prt_ok", refParts[0])
	}
	for _, u := range refParts {
		if strings.Contains(u, "part_id=prt_fail") {
			t.Fatalf("failed tool prt_fail (only state.error) emitted a tool_response ref %q (round-6 P2-1: must not)", u)
		}
	}

	// The failed tool still finalizes failed with ErrorMessage carrying state.error.
	var failFin *canonical.OpFinalizedEvent
	for i, f := range opFinals(evs) {
		if f.Status == "failed" {
			ff := opFinals(evs)[i]
			failFin = &ff
		}
	}
	if failFin == nil {
		t.Fatal("failed tool produced no OpFinalized with status=failed")
	}
	if failFin.ErrorMessage != "command failed" {
		t.Errorf("failed tool ErrorMessage = %q, want %q (detail rides in ErrorMessage, not a payload ref)", failFin.ErrorMessage, "command failed")
	}
}

func TestMapSession_RunningToolNoFinalize(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			toolPart("prt_2", "bash", "running", 2000, nil, nil), // no end => running
		),
	}
	evs := run(t, s, msgs)
	// A running tool emits OpStarted but no OpFinalized for that op.
	tools := toolOps(evs)
	if len(tools) != 1 {
		t.Fatalf("tool op count = %d want 1", len(tools))
	}
	for _, f := range opFinals(evs) {
		if f.Seq == tools[0].Seq && f.TurnSeq == tools[0].TurnSeq {
			t.Fatalf("running tool op %d:%d must NOT be finalized", f.TurnSeq, f.Seq)
		}
	}
}

// --- task tool → session op (AC#4) --------------------------------------------

func TestMapSession_TaskToolEmitsSessionOp(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2500)
	md := map[string]any{"sessionId": "ses_child"}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			toolPart("prt_2", "task", "completed", 2000, &end, md),
		),
	}
	evs := run(t, s, msgs)
	// Must emit BOTH a tool op (Name=task) AND a session op (ChildSessionNativeID).
	var sawTool, sawSession bool
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpTool && op.Name == "task" {
			sawTool = true
		}
		if op.Kind == canonical.OpSession && op.ChildSessionNativeID == "ses_child" {
			sawSession = true
		}
	}
	if !sawTool {
		t.Fatal("missing tool op for task")
	}
	if !sawSession {
		t.Fatal("missing session op with ChildSessionNativeID=ses_child")
	}
}

func TestMapSession_TaskToolNoSessionIDNoSessionOp(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2500)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			toolPart("prt_2", "task", "completed", 2000, &end, nil), // no sessionId
		),
	}
	evs := run(t, s, msgs)
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpSession {
			t.Fatal("task with no sessionId must NOT emit a session op")
		}
	}
}

// --- reasoning op (AC: ParentOpSeq, ReasoningKind) ----------------------------

func TestMapSession_ReasoningOpDefaultRaw(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2200)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			reasoningPart("prt_2", 2000, &end, false),
		),
	}
	evs := run(t, s, msgs)
	var r *canonical.OpStartedEvent
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpReasoning {
			o := op
			r = &o
		}
	}
	if r == nil {
		t.Fatal("no reasoning op")
	}
	if r.ReasoningKind != "raw" {
		t.Fatalf("ReasoningKind = %q want raw (default)", r.ReasoningKind)
	}
	if r.ParentOpSeq <= 0 {
		t.Fatalf("reasoning ParentOpSeq = %d want >0 (under LLM op)", r.ParentOpSeq)
	}
}

func TestMapSession_ReasoningOpSummaryFromMetadata(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2200)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			reasoningPart("prt_2", 2000, &end, true),
		),
	}
	evs := run(t, s, msgs)
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpReasoning && op.ReasoningKind == "summary" {
			return
		}
	}
	t.Fatal("no reasoning op with ReasoningKind=summary")
}

func TestMapSession_ReasoningRunningWhenNoEnd(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			reasoningPart("prt_2", 2000, nil, false), // no end
		),
	}
	evs := run(t, s, msgs)
	var rSeq, rTurn int
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpReasoning {
			rSeq, rTurn = op.Seq, op.TurnSeq
		}
	}
	for _, f := range opFinals(evs) {
		if f.Seq == rSeq && f.TurnSeq == rTurn {
			t.Fatal("reasoning op with no end must NOT be finalized")
		}
	}
}

// --- text → PayloadRef (no op) ------------------------------------------------

func TestMapSession_TextEmitsPayloadRefNotOp(t *testing.T) {
	s := rootSession("ses_x", 0)
	end := int64(2200)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			stepFinish("prt_2", 10, 1, 0, 0, 0, 0.01),
			textPart("prt_3"),
		),
	}
	_ = end
	evs := run(t, s, msgs)
	// text must NOT add an op (only the one LLM op exists).
	if n := len(llmOps(evs)); n != 1 {
		t.Fatalf("LLM op count = %d want 1 (text is not an op)", n)
	}
	for _, op := range opStarts(evs) {
		if op.Kind != canonical.OpLLM {
			t.Fatalf("unexpected non-LLM op kind %q (text must not be an op)", op.Kind)
		}
	}
	// text DOES emit a PayloadRef (llm_response, field=text) attached to the LLM op.
	var sawTextRef bool
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok && p.PayloadKind == "llm_response" {
			sawTextRef = true
			if p.OpSeq <= 0 {
				t.Fatalf("text PayloadRef OpSeq = %d want >0 (attached to LLM op)", p.OpSeq)
			}
		}
	}
	if !sawTextRef {
		t.Fatal("no llm_response PayloadRef for text part")
	}
}

func TestMapSession_TextBeforeAnyLLMOpDropped(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			textPart("prt_1"), // before any step-start => no op to attach to
		),
	}
	evs := run(t, s, msgs)
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok && p.PayloadKind == "llm_response" {
			t.Fatal("text PayloadRef before any LLM op must be dropped (op_id NOT NULL)")
		}
	}
}

// --- patch → op extras (not an op) --------------------------------------------

func TestMapSession_PatchNotAnOpAddsExtras(t *testing.T) {
	s := rootSession("ses_x", 0)
	c := int64(3000)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, &c, "the-alias", "the-model", tokenCounts{Input: 10}, 0.1, "stop", ""),
			stepStart("prt_1"),
			patchPart("prt_2"),
			stepFinish("prt_3", 10, 1, 0, 0, 0, 0.1),
		),
	}
	evs := run(t, s, msgs)
	// patch must NOT add a DISTINCT op. The mapper re-emits the LLM OpStarted
	// (idempotent UPDATE on (turn,seq)) to graft the patch extras before the
	// finalize — mirrors codex's enrichment re-emit — so count DISTINCT (turn,seq)
	// ops, not raw OpStarted events.
	distinct := map[[2]int]canonical.OpKind{}
	for _, op := range opStarts(evs) {
		distinct[[2]int{op.TurnSeq, op.Seq}] = op.Kind
	}
	if len(distinct) != 1 {
		t.Fatalf("distinct op count = %d want 1 (patch is not an op)", len(distinct))
	}
	for _, k := range distinct {
		if k != canonical.OpLLM {
			t.Fatalf("only op must be the LLM op, got kind %q", k)
		}
	}
	// The LLM op's re-emit carries patch info in Extras.
	var found bool
	for _, op := range opStarts(evs) {
		if op.Kind == canonical.OpLLM {
			if files, ok := op.Extras["patch_files"]; ok {
				found = true
				arr, _ := files.([]string)
				if len(arr) != 2 {
					t.Fatalf("patch_files len = %d want 2", len(arr))
				}
			}
		}
	}
	if !found {
		t.Fatal("LLM op Extras missing patch_files")
	}
}

// --- compaction → INF log; retry → WRN log ------------------------------------

func TestMapSession_CompactionInfoLog(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			compactionPart("prt_2", true),
		),
	}
	evs := run(t, s, msgs)
	var found bool
	for _, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "INF" {
			found = true
			if l.Source != Format {
				t.Fatalf("log Source = %q want %q", l.Source, Format)
			}
		}
	}
	if !found {
		t.Fatal("no INF LogEntry for compaction")
	}
}

// TestMapSession_RetryWarnLog pins the retry → WRN LogEntry, including the
// triggering error's name in the message AND extras (SOW-0005 round-6 P3-1).
// retryPart builds an error with name "RateLimit".
func TestMapSession_RetryWarnLog(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			retryPart("prt_2", 3),
		),
	}
	evs := run(t, s, msgs)
	var retryLog *canonical.LogEntryEvent
	for i, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "WRN" {
			le := evs[i].(canonical.LogEntryEvent)
			retryLog = &le
		}
	}
	if retryLog == nil {
		t.Fatal("no WRN LogEntry for retry")
	}
	// The message carries the attempt AND the error name (P3-1).
	if retryLog.Message != "API retry attempt 3: RateLimit" {
		t.Errorf("retry WRN message = %q, want %q (attempt + error.name)", retryLog.Message, "API retry attempt 3: RateLimit")
	}
	if retryLog.Extras["error.name"] != "RateLimit" {
		t.Errorf("retry WRN extras[error.name] = %v, want RateLimit", retryLog.Extras["error.name"])
	}
	if retryLog.Extras["attempt"] != 3 {
		t.Errorf("retry WRN extras[attempt] = %v, want 3", retryLog.Extras["attempt"])
	}
}

// TestMapSession_RetryWarnLogNoErrorName pins the forward-compat fallback (P3-1):
// a retry part with NO error.name emits the bare "API retry attempt <n>" message
// with no trailing ": " and no error.name extra (an older/partial retry part).
func TestMapSession_RetryWarnLogNoErrorName(t *testing.T) {
	s := rootSession("ses_x", 0)
	bareRetry := func(id string, attempt int) partRow {
		raw, _ := json.Marshal(map[string]any{"type": "retry", "attempt": attempt})
		return partRow{ID: id, MessageID: "msg_a", SessionID: "ses_x", Data: raw}
	}
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			bareRetry("prt_2", 2),
		),
	}
	evs := run(t, s, msgs)
	var retryLog *canonical.LogEntryEvent
	for i, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "WRN" {
			le := evs[i].(canonical.LogEntryEvent)
			retryLog = &le
		}
	}
	if retryLog == nil {
		t.Fatal("no WRN LogEntry for retry")
	}
	if retryLog.Message != "API retry attempt 2" {
		t.Errorf("retry WRN message = %q, want bare %q (no error.name → no ': ' suffix)", retryLog.Message, "API retry attempt 2")
	}
	if _, ok := retryLog.Extras["error.name"]; ok {
		t.Errorf("retry WRN extras must omit error.name when absent; got %v", retryLog.Extras["error.name"])
	}
}

// --- file part → INF LogEntry (round-4 P2-3) ----------------------------------

// TestMapSession_FilePartLogEntry pins SOW-0005 round-4 P2-3: a file part emits an
// INF LogEntry carrying filename/url/mime in its extras (an attachment record),
// and NO PayloadRefEvent with a non-canonical PayloadKind. The old "user_attachment"
// PayloadKind is not in the canonical PayloadRefEvent set (internal/canonical/
// events.go), so the adapter must not emit it.
func TestMapSession_FilePartLogEntry(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			filePart("prt_2", "https://cdn.example.invalid/x.png"),
		),
	}
	evs := run(t, s, msgs)

	// Every PayloadRef in the stream must carry a CANONICAL PayloadKind (the
	// internal/canonical/events.go set); in particular the removed "user_attachment"
	// kind must never appear.
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok {
			if !canonicalPayloadKinds[p.PayloadKind] {
				t.Fatalf("non-canonical PayloadRef kind=%q emitted (round-4 P2-3); canonical set only", p.PayloadKind)
			}
		}
	}

	// Exactly one INF LogEntry "file attachment" with filename/url/mime in extras.
	var found int
	for _, ev := range evs {
		l, ok := ev.(canonical.LogEntryEvent)
		if !ok || l.Message != "file attachment" {
			continue
		}
		found++
		if l.Severity != "INF" {
			t.Errorf("file-attachment LogEntry severity = %q, want INF", l.Severity)
		}
		if l.Extras["url"] != "https://cdn.example.invalid/x.png" {
			t.Errorf("file-attachment extras.url = %v, want the verbatim data.url", l.Extras["url"])
		}
		if l.Extras["filename"] != "x.png" {
			t.Errorf("file-attachment extras.filename = %v, want x.png", l.Extras["filename"])
		}
		if l.Extras["mime"] != "image/png" {
			t.Errorf("file-attachment extras.mime = %v, want image/png", l.Extras["mime"])
		}
		// Scoped to the turn and the open LLM op (prt_1 opened one).
		if l.TurnSeq != 1 {
			t.Errorf("file-attachment LogEntry TurnSeq = %d, want 1", l.TurnSeq)
		}
	}
	if found != 1 {
		t.Fatalf("file-attachment INF LogEntry count = %d, want 1", found)
	}
}

// --- unknown part → forward-compat skip + one WARN ----------------------------

func TestMapSession_UnknownPartSkippedWithWarn(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			unknownPart("prt_2"),
		),
	}
	evs := run(t, s, msgs)
	// No op for the unknown part; exactly one WRN log.
	warns := 0
	for _, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "WRN" {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("WRN count = %d want 1 for unknown part", warns)
	}
}

// --- provider alias canonicalization (AC#7) -----------------------------------

func TestCanonicalProvider(t *testing.T) {
	cases := []struct{ alias, want string }{
		{"openrouter", "openrouter"},             // known passthrough
		{"my-private-alias", "my-private-alias"}, // unknown → alias verbatim (default)
		{"", ""},
	}
	for _, c := range cases {
		if got := canonicalProvider(c.alias); got != c.want {
			t.Errorf("canonicalProvider(%q) = %q want %q", c.alias, got, c.want)
		}
	}
}

// --- empty/malformed message data is skipped, not fatal -----------------------

func TestMapSession_EmptyMessageDataSkipped(t *testing.T) {
	s := rootSession("ses_x", 0)
	msgs := []messageWithParts{
		{Message: messageRow{ID: "msg_bad", SessionID: "ses_x", Data: []byte("   ")}},
	}
	evs := run(t, s, msgs)
	// A blank data body must not produce a turn; one WRN log surfaces it.
	if n := countKind(evs, canonical.EvTurnStarted); n != 0 {
		t.Fatalf("TurnStarted = %d want 0 for empty message data", n)
	}
	warns := 0
	for _, ev := range evs {
		if l, ok := ev.(canonical.LogEntryEvent); ok && l.Severity == "WRN" {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("WRN count = %d want 1 for empty message data", warns)
	}
}

// --- ordering / determinism: re-emission yields identical streams -------------

func TestMapSession_Deterministic(t *testing.T) {
	s := rootSession("ses_x", 0)
	c := int64(3000)
	end := int64(2500)
	build := func() []messageWithParts {
		return []messageWithParts{
			mwp(asgMsg("msg_a", 1500, &c, "the-alias", "the-model", tokenCounts{Input: 100, Output: 20}, 0.2, "stop", ""),
				stepStart("prt_1"),
				reasoningPart("prt_2", 1800, &end, false),
				toolPart("prt_3", "github_search", "completed", 2000, &end, nil),
				stepFinish("prt_4", 100, 20, 0, 0, 0, 0.2),
				textPart("prt_5"),
			),
		}
	}
	a := run(t, s, build())
	b := run(t, s, build())
	if len(a) != len(b) {
		t.Fatalf("non-deterministic length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].EventKind() != b[i].EventKind() {
			t.Fatalf("event %d kind differs: %q vs %q", i, a[i].EventKind(), b[i].EventKind())
		}
		if a[i].EventSourceSeq() != b[i].EventSourceSeq() {
			t.Fatalf("event %d SourceSeq differs: %d vs %d", i, a[i].EventSourceSeq(), b[i].EventSourceSeq())
		}
	}
}
