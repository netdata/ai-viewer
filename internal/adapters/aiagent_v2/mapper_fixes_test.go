package aiagent_v2

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestMap_CtxUsedIncludesOutputTokens pins the "context window
// consumed" definition: input + cache-read + output. A regression on
// any one of the three trips the assertion.
func TestMap_CtxUsedIncludesOutputTokens(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "ctx-used")
	snap.OpTree.Turns[0].Ops[0].Accounting[0] = accountingEntry{
		Type: "llm",
		Tokens: &tokens{
			InputTokens:          1000,
			OutputTokens:         200,
			CacheReadInputTokens: 100,
			CachedTokens:         50,
		},
	}
	events := mapSimple(t, snap)
	var got int64
	for _, ev := range events {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok && of.TokensIn > 0 {
			got = of.CtxUsed
		}
	}
	const want = 1000 + 100 + 50 + 200
	if got != want {
		t.Fatalf("CtxUsed = %d, want %d (input + cacheRead + output)", got, want)
	}
}

// TestMap_SessionStartedCarriesModelFromFirstLLMOp ensures the
// pre-pass populates SessionStartedEvent.Model when at least one LLM
// op is present in the snapshot. v2 is a full-snapshot format so the
// model is known at session-start time.
func TestMap_SessionStartedCarriesModelFromFirstLLMOp(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "model-pre-pass")
	events := mapSimple(t, snap)
	var got string
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == "model-pre-pass" {
			got = ss.Model
		}
	}
	if got != "claude-3-5-sonnet" {
		t.Fatalf("SessionStartedEvent.Model = %q, want %q", got, "claude-3-5-sonnet")
	}
}

// TestMap_SessionStartedModelEmptyWithoutLLMOp confirms the pre-pass
// stays empty when no LLM op is present; we must not invent a value.
func TestMap_SessionStartedModelEmptyWithoutLLMOp(t *testing.T) {
	t.Parallel()
	snap := snapshot{
		Version: 2, Reason: "final",
		OpTree: opTree{
			TraceID: "no-llm", AgentID: "tool-only", StartedAt: 1700000000000,
			EndedAt: int64Ptr(1700000001000), Success: boolPtr(true),
			Turns: []turnNode{{
				Index: 1, StartedAt: 1700000000100, EndedAt: int64Ptr(1700000000900),
				Ops: []operationNode{{
					OpID: "tool-1", Kind: "tool", StartedAt: 1700000000200,
					EndedAt: int64Ptr(1700000000800), Status: "ok",
					Attributes: rawAttrs(map[string]any{"name": "shell", "provider": "builtin"}),
				}},
			}},
		},
	}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.Model != "" {
			t.Fatalf("SessionStartedEvent.Model should be empty without LLM op, got %q", ss.Model)
		}
	}
}

// TestMap_ReasoningOpEmittedNestedUnderLLM verifies that an LLM op
// with reasoning.final spawns a nested OpReasoning span whose
// ParentOpSeq points at the LLM op's seq.
func TestMap_ReasoningOpEmittedNestedUnderLLM(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "reasoning-op")
	llmOp := &snap.OpTree.Turns[0].Ops[0]
	llmOp.Reasoning = &reasoning{Final: "thought about it carefully"}

	events := mapSimple(t, snap)
	llmSeq, reasoningSeq, reasoningParentSeq, reasoningKind, reasoningExtras, sawReasoningStart := findReasoningStart(events)
	sawReasoningFinish := hasReasoningFinalize(events)
	assertReasoningObserved(t, sawReasoningStart, sawReasoningFinish)
	assertReasoningFields(t, llmSeq, reasoningSeq, reasoningParentSeq, reasoningKind, reasoningExtras)
}

func findReasoningStart(events []canonical.Event) (int, int, int, string, map[string]any, bool) {
	var llmSeq int
	for _, ev := range events {
		started, ok := ev.(canonical.OpStartedEvent)
		if !ok {
			continue
		}
		if started.Kind == canonical.OpLLM {
			llmSeq = started.Seq
		}
		if started.Kind == canonical.OpReasoning {
			return llmSeq, started.Seq, started.ParentOpSeq, started.ReasoningKind, started.Extras, true
		}
	}
	return llmSeq, 0, 0, "", nil, false
}

func hasReasoningFinalize(events []canonical.Event) bool {
	sawReasoningStart := false
	for _, ev := range events {
		if started, ok := ev.(canonical.OpStartedEvent); ok && started.Kind == canonical.OpReasoning {
			sawReasoningStart = true
			continue
		}
		if finalized, ok := ev.(canonical.OpFinalizedEvent); sawReasoningStart && ok && finalized.Status == "completed" {
			return true
		}
	}
	return false
}

func assertReasoningObserved(t *testing.T, sawReasoningStart, sawReasoningFinish bool) {
	t.Helper()
	if !sawReasoningStart {
		t.Fatalf("expected OpStarted with Kind=reasoning, none found")
	}
	if !sawReasoningFinish {
		t.Fatalf("expected OpFinalized for the reasoning op, none found")
	}
}

func assertReasoningFields(t *testing.T, llmSeq, reasoningSeq, reasoningParentSeq int, reasoningKind string, reasoningExtras map[string]any) {
	t.Helper()
	if reasoningParentSeq != llmSeq {
		t.Fatalf("reasoning ParentOpSeq = %d, want LLM op seq %d", reasoningParentSeq, llmSeq)
	}
	if reasoningSeq == llmSeq {
		t.Fatalf("reasoning Seq collides with LLM seq %d", llmSeq)
	}
	if reasoningKind != "summary" {
		t.Fatalf("reasoning ReasoningKind = %q, want %q", reasoningKind, "summary")
	}
	if text, _ := reasoningExtras["reasoning.final"].(string); text != "thought about it carefully" {
		t.Fatalf("reasoning extras missing reasoning.final, got %v", reasoningExtras)
	}
}

// TestMap_ReasoningNotEmittedWhenFinalEmpty makes sure we don't emit
// a phantom reasoning op when only chunks/counters are populated.
func TestMap_ReasoningNotEmittedWhenFinalEmpty(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "no-reasoning")
	snap.OpTree.Turns[0].Ops[0].Reasoning = &reasoning{ChunkCount: 4, CharCount: 100}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if os, ok := ev.(canonical.OpStartedEvent); ok && os.Kind == canonical.OpReasoning {
			t.Fatalf("OpReasoning emitted without reasoning.final; extras=%v", os.Extras)
		}
	}
}

// TestMap_FirstLLMModelFromChildSession ensures the DFS finds an LLM
// model nested inside a child session when the root session has no
// LLM ops of its own (only a session-kind op delegating to a sub-agent).
func TestMap_FirstLLMModelFromChildSession(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "delegated")
	// Replace the root LLM op with a session-kind op whose child has
	// the LLM. The root opTree now carries no LLM op directly.
	child := opTree{
		ID: "delegated-child", TraceID: "child-trace", AgentID: "sub",
		StartedAt: 1700000002000, EndedAt: int64Ptr(1700000003000),
		Success: boolPtr(true),
		Turns: []turnNode{{
			Index: 1, StartedAt: 1700000002100, EndedAt: int64Ptr(1700000002900),
			Ops: []operationNode{{
				OpID: "child-llm", Kind: "llm", StartedAt: 1700000002200,
				EndedAt: int64Ptr(1700000002800), Status: "ok",
				Attributes: rawAttrs(map[string]any{
					"provider": "openai", "model": "gpt-4o",
				}),
			}},
		}},
	}
	snap.OpTree.Turns[0].Ops = []operationNode{{
		OpID: "delegate", Kind: "session", StartedAt: 1700000002000,
		EndedAt: int64Ptr(1700000003000), Status: "ok",
		Attributes:   rawAttrs(map[string]any{"name": "sub", "kind": "agent"}),
		ChildSession: &child,
	}}
	events := mapSimple(t, snap)
	var got string
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == "delegated" {
			got = ss.Model
		}
	}
	if got != "gpt-4o" {
		t.Fatalf("Model = %q, want %q (DFS into child)", got, "gpt-4o")
	}
}

func TestMap_FirstLLMModelSkipsOverDepthCapChild(t *testing.T) {
	t.Parallel()
	snap := snapshot{Version: 2, Reason: "final", OpTree: overDepthModelRoot()}

	var errCount int
	events := mapSnapshot(snap, "test-source", "root-model-cap", "", "root-model-cap.json.gz", func(error) {
		errCount++
	})
	if errCount == 0 {
		t.Fatalf("expected depth-cap error")
	}
	if got := sessionStartedModel(events, "root-model-cap"); got != "" {
		t.Fatalf("root SessionStartedEvent.Model = %q, want empty when only over-cap child has model", got)
	}
}

func overDepthModelRoot() opTree {
	leaf := opTree{
		TraceID:   "model-leaf",
		StartedAt: 1700000000000,
		Turns: []turnNode{{Index: 1, StartedAt: 1700000000001, Ops: []operationNode{{
			OpID: "leaf-llm", Kind: "llm", StartedAt: 1700000000002,
			Attributes: rawAttrs(map[string]any{"model": "over-cap-model"}),
		}}}},
	}
	current := &leaf
	for i := 0; i < maxChildSessionDepth+1; i++ {
		current = &opTree{
			TraceID:   "depth-node",
			StartedAt: 1700000000000,
			Turns: []turnNode{{Index: 1, StartedAt: 1700000000001, Ops: []operationNode{{
				OpID: "child-op", Kind: "session", StartedAt: 1700000000001,
				Attributes: rawAttrs(map[string]any{"name": "child"}), ChildSession: current,
			}}}},
		}
	}
	current.TraceID = "root-model-cap"
	return *current
}

func sessionStartedModel(events []canonical.Event, nativeID string) string {
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == nativeID {
			return ss.Model
		}
	}
	return ""
}

// TestMap_FirstLLMModelFromStep covers the steps[] arm of the DFS.
func TestMap_FirstLLMModelFromStep(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "step-llm")
	// Empty the turn-level LLM so the model can only come from a step.
	snap.OpTree.Turns[0].Ops = nil
	snap.OpTree.Steps = []stepNode{{
		ID: "s-1", Index: 0, Kind: "internal", StartedAt: 1700000006000,
		EndedAt: int64Ptr(1700000007000),
		Ops: []operationNode{{
			OpID: "step-llm", Kind: "llm", StartedAt: 1700000006100,
			EndedAt: int64Ptr(1700000006900), Status: "ok",
			Attributes: rawAttrs(map[string]any{"model": "step-model-x"}),
		}},
	}}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.Model != "step-model-x" {
			t.Fatalf("Model = %q, want step-model-x", ss.Model)
		}
	}
}

// TestMap_FirstLLMModelFallsBackToAccountingModel covers the case
// where attributes.model is empty but accounting[0].model is set.
func TestMap_FirstLLMModelFallsBackToAccountingModel(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "acct-model")
	snap.OpTree.Turns[0].Ops[0].Attributes = rawAttrs(map[string]any{"provider": "anthropic"})
	snap.OpTree.Turns[0].Ops[0].Accounting = []accountingEntry{{
		Type: "llm", Model: "fallback-model",
	}}
	events := mapSimple(t, snap)
	for _, ev := range events {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.Model != "fallback-model" {
			t.Fatalf("Model = %q, want fallback-model", ss.Model)
		}
	}
}
