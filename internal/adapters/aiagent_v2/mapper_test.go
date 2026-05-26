package aiagent_v2

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func mapSimple(t *testing.T, snap snapshot) []canonical.Event {
	t.Helper()
	onErr := func(error) {}
	return mapSnapshot(snap, "test-source", snap.OpTree.TraceID, "", snap.OpTree.TraceID+".json.gz", onErr)
}

func TestMap_RootSessionEmitsExpectedEvents(t *testing.T) {
	t.Parallel()
	events := mapSimple(t, simpleSnapshot(2, "root-uuid"))

	var (
		sawStart, sawFinal       bool
		sawTurnStart, sawTurnEnd bool
		sawOpStart, sawOpEnd     bool
	)
	for _, ev := range events {
		switch v := ev.(type) {
		case canonical.SessionStartedEvent:
			sawStart = true
			if v.NativeID != "root-uuid" {
				t.Fatalf("NativeID: %q", v.NativeID)
			}
		case canonical.SessionFinalizedEvent:
			sawFinal = true
			if v.Status != canonical.StatusCompleted {
				t.Fatalf("Status: %q", v.Status)
			}
		case canonical.TurnStartedEvent:
			sawTurnStart = true
		case canonical.TurnFinalizedEvent:
			sawTurnEnd = true
		case canonical.OpStartedEvent:
			sawOpStart = true
		case canonical.OpFinalizedEvent:
			sawOpEnd = true
			if v.TokensIn != 100 || v.TokensOut != 50 {
				t.Fatalf("op tokens: %d/%d", v.TokensIn, v.TokensOut)
			}
		}
	}
	if !sawStart || !sawFinal || !sawTurnStart || !sawTurnEnd || !sawOpStart || !sawOpEnd {
		t.Fatalf("missing expected events; got %d", len(events))
	}
}

func TestMap_EmbeddedSubAgentEmitsChildSession(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "parent-uuid")
	child := opTree{
		ID:        "child-tree",
		TraceID:   "child-uuid",
		AgentID:   "sub-agent",
		StartedAt: 1700000002000,
		EndedAt:   int64Ptr(1700000003000),
		Success:   boolPtr(true),
		Turns: []turnNode{
			{Index: 1, StartedAt: 1700000002100, EndedAt: int64Ptr(1700000002900)},
		},
	}
	snap.OpTree.Turns[0].Ops = append(snap.OpTree.Turns[0].Ops, operationNode{
		OpID:      "sub-op",
		Kind:      "session",
		StartedAt: 1700000002000,
		EndedAt:   int64Ptr(1700000003000),
		Status:    "ok",
		Attributes: rawAttrs(map[string]any{
			"name":     "sub-agent",
			"provider": "subagent",
			"kind":     "agent",
		}),
		ChildSession: &child,
	})

	events := mapSimple(t, snap)
	var childStart bool
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == "child-uuid" {
			childStart = true
			if ss.ParentNativeID != "parent-uuid" {
				t.Fatalf("ParentNativeID: %q", ss.ParentNativeID)
			}
			if ss.Kind != canonical.KindSubAgent {
				t.Fatalf("Kind: %q", ss.Kind)
			}
			if ss.RootNativeID != "parent-uuid" {
				t.Fatalf("RootNativeID: %q", ss.RootNativeID)
			}
		}
	}
	if !childStart {
		t.Fatalf("child SessionStarted not emitted")
	}
}

func TestMap_InitTurnZeroIsPreserved(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "init-test")
	snap.OpTree.Turns = []turnNode{
		{
			ID:         "turn-0",
			Index:      0,
			StartedAt:  1700000000000,
			EndedAt:    int64Ptr(1700000000500),
			Attributes: rawAttrs(map[string]any{"system": true, "label": "init"}),
			Ops: []operationNode{
				{
					OpID: "init-op", Kind: "system", StartedAt: 1700000000050,
					EndedAt: int64Ptr(1700000000450), Status: "ok",
					Attributes: rawAttrs(map[string]any{"label": "init"}),
				},
			},
		},
	}
	events := mapSimple(t, snap)
	var sawTurn0 bool
	for _, ev := range events {
		if ts, ok := ev.(canonical.TurnStartedEvent); ok && ts.Seq == 0 {
			sawTurn0 = true
		}
	}
	if !sawTurn0 {
		t.Fatalf("expected TurnStarted Seq=0, not found")
	}
}

func TestMap_SystemOpKindMapsToOpSystem(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "sys-op")
	snap.OpTree.Turns[0].Ops[0] = operationNode{
		OpID: "system-op", Kind: "system", StartedAt: 1700000001500,
		EndedAt: int64Ptr(1700000001600), Status: "ok",
		Attributes: rawAttrs(map[string]any{"label": "tick"}),
	}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if os, ok := ev.(canonical.OpStartedEvent); ok {
			if os.Kind != canonical.OpSystem {
				t.Fatalf("OpKind: got %q want %q", os.Kind, canonical.OpSystem)
			}
			if got, _ := os.Extras["original_kind"].(string); got != "system" {
				t.Fatalf("extras.original_kind: %v", os.Extras["original_kind"])
			}
		}
	}
}

func TestMap_ToolOpUsesCharsAccounting(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "tool-test")
	snap.OpTree.Turns[0].Ops[0] = operationNode{
		OpID: "tool-op", Kind: "tool", StartedAt: 1700000001500,
		EndedAt: int64Ptr(1700000002500), Status: "ok",
		Attributes: rawAttrs(map[string]any{
			"name":     "shell",
			"provider": "builtin",
		}),
		Accounting: []accountingEntry{
			{
				Type:          "tool",
				CharactersIn:  120,
				CharactersOut: 4500,
				Status:        "ok",
			},
		},
	}
	events := mapSimple(t, snap)
	var sawTool bool
	for _, ev := range events {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok {
			sawTool = true
			if of.CharsIn != 120 || of.CharsOut != 4500 {
				t.Fatalf("chars: %d/%d", of.CharsIn, of.CharsOut)
			}
			if of.TokensIn != 0 || of.TokensOut != 0 {
				t.Fatalf("tool op should have no tokens: %d/%d", of.TokensIn, of.TokensOut)
			}
		}
	}
	if !sawTool {
		t.Fatalf("OpFinalized missing")
	}
}

func TestMap_FailedSessionEmitsErrorLog(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "fail-test")
	snap.OpTree.Success = boolPtr(false)
	snap.OpTree.Error = "Turn 1 failed after 1 attempt of 1"

	events := mapSimple(t, snap)
	var sawErrLog bool
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Severity == "ERR" {
			sawErrLog = true
			if le.Message != "Turn 1 failed after 1 attempt of 1" {
				t.Fatalf("log message: %q", le.Message)
			}
		}
		if sf, ok := ev.(canonical.SessionFinalizedEvent); ok {
			if sf.Status != canonical.StatusFailed {
				t.Fatalf("Status: %q", sf.Status)
			}
		}
	}
	if !sawErrLog {
		t.Fatalf("missing ERR log for failed session")
	}
}

func TestMap_InterruptedSessionStatus(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "interrupted")
	snap.OpTree.Success = nil
	// endedAt set, no success/error
	events := mapSimple(t, snap)
	for _, ev := range events {
		if sf, ok := ev.(canonical.SessionFinalizedEvent); ok {
			if sf.Status != canonical.StatusInterrupted {
				t.Fatalf("Status: %q want %q", sf.Status, canonical.StatusInterrupted)
			}
		}
	}
}

func TestMap_RunningSessionEmitsNoFinalize(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "running")
	snap.OpTree.EndedAt = nil
	snap.OpTree.Success = nil
	events := mapSimple(t, snap)
	for _, ev := range events {
		if _, ok := ev.(canonical.SessionFinalizedEvent); ok {
			t.Fatalf("running session should not emit SessionFinalized")
		}
	}
}

func TestMap_AbandonedSession(t *testing.T) {
	t.Parallel()
	snap := snapshot{
		Version: 2, Reason: "final",
		OpTree: opTree{TraceID: "abandoned", StartedAt: 1700000000000},
	}
	events := mapSimple(t, snap)
	// Abandoned session emits SessionStartedEvent only (no Finalized).
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if _, ok := events[0].(canonical.SessionStartedEvent); !ok {
		t.Fatalf("expected SessionStartedEvent, got %T", events[0])
	}
}

func TestMap_ChildSessionRefWithoutEmbedded(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "ref-only")
	snap.OpTree.Turns[0].Ops[0] = operationNode{
		OpID: "ref-op", Kind: "session", StartedAt: 1700000001500,
		EndedAt: int64Ptr(1700000002500), Status: "ok",
		Attributes:      rawAttrs(map[string]any{"name": "sub", "kind": "agent"}),
		ChildSessionRef: &childSessionRef{SessionID: "ref-uuid", OriginID: "ref-origin"},
	}
	events := mapSimple(t, snap)
	var found bool
	for _, ev := range events {
		if os, ok := ev.(canonical.OpStartedEvent); ok && os.ChildSessionNativeID == "ref-uuid" {
			found = true
		}
	}
	if !found {
		t.Fatalf("childSessionRef should populate ChildSessionNativeID")
	}
}

func TestMap_DepthCapExceededEmitsError(t *testing.T) {
	t.Parallel()
	// Build a deeply nested chain.
	leaf := opTree{TraceID: "leaf", StartedAt: 1700000000000}
	current := &leaf
	for i := 0; i < maxChildSessionDepth+5; i++ {
		parent := opTree{
			TraceID:   "lvl",
			StartedAt: 1700000000000,
			Turns: []turnNode{
				{Index: 1, StartedAt: 1700000000001, Ops: []operationNode{
					{
						OpID: "child-op", Kind: "session", StartedAt: 1700000000001,
						Attributes:   rawAttrs(map[string]any{"name": "c"}),
						ChildSession: current,
					},
				}},
			},
		}
		current = &parent
	}
	snap := snapshot{Version: 2, Reason: "final", OpTree: *current}

	var errCount int
	mapSnapshot(snap, "test-source", "deep-root", "", "deep.json.gz", func(error) { errCount++ })
	if errCount == 0 {
		t.Fatalf("expected at least one depth-cap error")
	}
}

func TestMap_StepsEmitWithReservedSeqBand(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "steps-test")
	snap.OpTree.Steps = []stepNode{
		{ID: "s-1", Index: 0, Kind: "internal", StartedAt: 1700000006000, EndedAt: int64Ptr(1700000007000)},
	}
	events := mapSimple(t, snap)
	var sawStepTurn bool
	for _, ev := range events {
		if ts, ok := ev.(canonical.TurnStartedEvent); ok && ts.Seq >= stepIndexOffset {
			sawStepTurn = true
		}
	}
	if !sawStepTurn {
		t.Fatalf("step should emit a TurnStarted with seq >= %d", stepIndexOffset)
	}
}

func TestMap_AccountingTokenNormalization(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "tok-test")
	snap.OpTree.Turns[0].Ops[0].Accounting[0] = accountingEntry{
		Type:     "llm",
		Provider: "anthropic",
		Model:    "claude-3",
		CostUSD:  0.05,
		Tokens: &tokens{
			InputTokens:           1000,
			OutputTokens:          200,
			CacheReadInputTokens:  150,
			CacheWriteInputTokens: 25,
			CachedTokens:          50, // openai-style alias; should add to cache-read
		},
	}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok {
			if of.TokensCacheRead != 200 { // 150 + 50
				t.Fatalf("CacheRead normalisation: got %d want 200", of.TokensCacheRead)
			}
			if of.TokensCacheWrite != 25 {
				t.Fatalf("CacheWrite: got %d", of.TokensCacheWrite)
			}
		}
	}
}

func TestMap_OpStatusMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"ok", "completed"},
		{"failed", "failed"},
		{"", "running"},
		{"weird", "weird"},
	}
	for _, c := range cases {
		if got := mapOpStatus(c.in); got != c.want {
			t.Fatalf("mapOpStatus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMap_OpKindMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want canonical.OpKind
	}{
		{"llm", canonical.OpLLM},
		{"tool", canonical.OpTool},
		{"session", canonical.OpSession},
		{"system", canonical.OpSystem},
		{"future", canonical.OpKind("future")},
	}
	for _, c := range cases {
		if got := mapOpKind(c.in); got != c.want {
			t.Fatalf("mapOpKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMap_NormaliseSeverity(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"VRB": "DBG", "DBG": "DBG", "debug": "DBG",
		"INF": "INF", "info": "INF", "anything": "INF",
		"WRN": "WRN", "warn": "WRN", "WARNING": "WRN",
		"ERR": "ERR", "error": "ERR", "FATAL": "ERR",
	}
	for in, want := range cases {
		if got := normaliseSeverity(in); got != want {
			t.Fatalf("normaliseSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMap_SeqForPathDeterministic(t *testing.T) {
	t.Parallel()
	a := seqForPath("origin-1", "path-x")
	b := seqForPath("origin-1", "path-x")
	if a != b {
		t.Fatalf("expected determinism: %d vs %d", a, b)
	}
	if seqForPath("origin-1", "path-y") == a {
		t.Fatalf("different paths should hash differently")
	}
	// Sign bit must be clear.
	if a&(1<<63) != 0 {
		t.Fatalf("expected sign bit clear: %d", a)
	}
}

func TestMap_TurnStatusFromOps(t *testing.T) {
	t.Parallel()
	if turnStatusFromOps(nil) != "completed" {
		t.Fatalf("empty ops should yield completed")
	}
	failed := []operationNode{{Status: "ok"}, {Status: "failed"}}
	if turnStatusFromOps(failed) != "failed" {
		t.Fatalf("any failed → failed")
	}
	all := []operationNode{{Status: "ok"}, {Status: "ok"}}
	if turnStatusFromOps(all) != "completed" {
		t.Fatalf("all ok → completed")
	}
	mixed := []operationNode{{Status: "ok"}, {Status: ""}}
	if turnStatusFromOps(mixed) != "running" {
		t.Fatalf("unfinished → running")
	}
}

func TestMap_FinalReportLandsInExtras(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "fr-test")
	snap.OpTree.FinalReport = []byte(`{"summary":"all good"}`)
	snap.OpTree.PluginMetas = []byte(`{"plugin-a":{"ok":true}}`)
	events := mapSimple(t, snap)
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok {
			if ss.Extras["final_report"] == nil {
				t.Fatalf("missing final_report in extras")
			}
			if ss.Extras["plugin_metas"] == nil {
				t.Fatalf("missing plugin_metas in extras")
			}
		}
	}
}
