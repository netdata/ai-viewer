package aiagent_v2

import (
	"encoding/json"
	"path/filepath"
	"strings"
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
	var (
		llmSeq             int
		sawReasoningStart  bool
		sawReasoningFinish bool
		reasoningExtras    map[string]any
		reasoningParentSeq int
		reasoningKind      string
	)
	for _, ev := range events {
		switch v := ev.(type) {
		case canonical.OpStartedEvent:
			if v.Kind == canonical.OpLLM {
				llmSeq = v.Seq
			}
			if v.Kind == canonical.OpReasoning {
				sawReasoningStart = true
				reasoningExtras = v.Extras
				reasoningParentSeq = v.ParentOpSeq
				reasoningKind = v.ReasoningKind
			}
		case canonical.OpFinalizedEvent:
			// The reasoning OpFinalized shares Seq with the LLM op (it
			// pins the same nesting key). We detect it by an
			// immediately-following finalize after the reasoning start
			// — checking Status alone is enough for v2 (no error path).
			if sawReasoningStart && !sawReasoningFinish && v.Status == "completed" {
				sawReasoningFinish = true
			}
		}
	}
	if !sawReasoningStart {
		t.Fatalf("expected OpStarted with Kind=reasoning, none found")
	}
	if !sawReasoningFinish {
		t.Fatalf("expected OpFinalized for the reasoning op, none found")
	}
	if reasoningParentSeq != llmSeq {
		t.Fatalf("reasoning ParentOpSeq = %d, want LLM op seq %d", reasoningParentSeq, llmSeq)
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

// TestMap_PayloadRefEmittedForRequestAndResponse covers the ref-form
// payload path. The op carries both `request.payload.ref` and
// `response.payload.ref`; we expect one PayloadRefEvent per side.
func TestMap_PayloadRefEmittedForRequestAndResponse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "payload-ref")
	// Payload bodies don't need to exist on disk — the adapter never
	// reads them, only resolves the path. Build the relative paths the
	// way the producer does.
	reqRef := json.RawMessage(`{"ref":"payloads/req.http.gz","format":"http","compression":"gzip","originalBytes":1500,"storedBytes":420,"sha256":"abc"}`)
	respRef := json.RawMessage(`{"ref":"payloads/resp.http.gz","format":"http","compression":"gzip","originalBytes":4500,"storedBytes":1100,"sha256":"def"}`)
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{Kind: "llm", Payload: reqRef, Size: 1500}
	snap.OpTree.Turns[0].Ops[0].Response = &opPayload{Payload: respRef, Size: 4500}

	events := mapSnapshot(snap, "test-source", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(error) {})

	var refs []canonical.PayloadRefEvent
	for _, ev := range events {
		if pr, ok := ev.(canonical.PayloadRefEvent); ok {
			refs = append(refs, pr)
		}
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 PayloadRefEvent, got %d", len(refs))
	}
	wantPrefix := "file://" + filepath.ToSlash(filepath.Clean(root))
	for _, r := range refs {
		if !strings.HasPrefix(r.LocationURI, wantPrefix) {
			t.Fatalf("LocationURI %q missing prefix %q", r.LocationURI, wantPrefix)
		}
		if r.SHA256 == "" {
			t.Fatalf("PayloadRefEvent missing SHA256")
		}
	}
	// Validate kinds map to llm_request/llm_response for an LLM op.
	kinds := map[string]bool{refs[0].PayloadKind: true, refs[1].PayloadKind: true}
	if !kinds["llm_request"] || !kinds["llm_response"] {
		t.Fatalf("PayloadKinds = %v, want llm_request + llm_response", kinds)
	}
}

// TestMap_PayloadRefForToolOpUsesToolKinds verifies tool ops get
// tool_request / tool_response, not llm_*.
func TestMap_PayloadRefForToolOpUsesToolKinds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "tool-payload")
	reqRef := json.RawMessage(`{"ref":"payloads/tool-req.json.gz","format":"json","compression":"gzip","originalBytes":80}`)
	snap.OpTree.Turns[0].Ops[0] = operationNode{
		OpID: "tool-op", Kind: "tool", StartedAt: 1700000001500,
		EndedAt: int64Ptr(1700000001600), Status: "ok",
		Attributes: rawAttrs(map[string]any{"name": "shell", "provider": "builtin"}),
		Request:    &opPayload{Payload: reqRef, Size: 80},
	}
	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(error) {})
	var got string
	for _, ev := range events {
		if pr, ok := ev.(canonical.PayloadRefEvent); ok {
			got = pr.PayloadKind
		}
	}
	if got != "tool_request" {
		t.Fatalf("PayloadKind = %q, want %q", got, "tool_request")
	}
}

// TestMap_PayloadRefTraversalGuardRejects validates that a relative
// path escaping the root surfaces a SourceError via onError and that
// no PayloadRefEvent is emitted.
func TestMap_PayloadRefTraversalGuardRejects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "evil-ref")
	evilRef := json.RawMessage(`{"ref":"../../../etc/passwd","format":"text"}`)
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{Payload: evilRef, Size: 10}
	var errs []error
	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(e error) { errs = append(errs, e) })
	for _, ev := range events {
		if _, ok := ev.(canonical.PayloadRefEvent); ok {
			t.Fatalf("escaping ref should not emit PayloadRefEvent")
		}
	}
	if len(errs) == 0 {
		t.Fatalf("expected onError for path-escape ref, none raised")
	}
}

// TestMap_PayloadInlineSkipsRefEmission confirms that an inline
// (non-ref) payload is silently skipped — no PayloadRefEvent and no
// onError. Inline payloads are deferred per spec §Canonical Model
// Gaps item 10.
func TestMap_PayloadInlineSkipsRefEmission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "inline")
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{
		Payload: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`),
		Size:    20,
	}
	snap.OpTree.Turns[0].Ops[0].Response = &opPayload{
		Payload: json.RawMessage(`"base64-blob..."`),
		Size:    40,
	}
	var errs []error
	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(e error) { errs = append(errs, e) })
	for _, ev := range events {
		if _, ok := ev.(canonical.PayloadRefEvent); ok {
			t.Fatalf("inline payload should not emit PayloadRefEvent")
		}
	}
	if len(errs) != 0 {
		t.Fatalf("inline payload should not produce errors, got %v", errs)
	}
}

// TestExtractPayloadRef_VariousShapes hits the JSON-shape probe paths
// directly so they stay covered when the calling sites are rare in
// fixtures.
func TestExtractPayloadRef_VariousShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		ok   bool
		path string
	}{
		{"empty", "", false, ""},
		{"string scalar", `"opaque-base64-blob"`, false, ""},
		{"array scalar", `[1,2,3]`, false, ""},
		{"object no ref/path", `{"messages":[]}`, false, ""},
		{"ref-only", `{"ref":"a/b/c.gz"}`, true, "a/b/c.gz"},
		{"path-only", `{"path":"x/y/z.json"}`, true, "x/y/z.json"},
		{"both ref and path prefers path", `{"ref":"X","path":"Y"}`, true, "Y"},
		{"malformed json", `{not json`, false, ""},
		{"whitespace then string", "   \"x\"", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, ok := extractPayloadRef(json.RawMessage(c.in))
			if ok != c.ok {
				t.Fatalf("extractPayloadRef(%q) ok = %v, want %v", c.in, ok, c.ok)
			}
			if c.ok && ref.Path != c.path {
				t.Fatalf("extractPayloadRef(%q) path = %q, want %q", c.in, ref.Path, c.path)
			}
		})
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

// TestResolvePayloadPath_RootHandling exercises empty inputs and
// traversal-guard rejections without requiring a real file.
func TestResolvePayloadPath_RootHandling(t *testing.T) {
	t.Parallel()
	if uri, err := resolvePayloadPath("", "x/y.bin"); err != nil || uri != "" {
		t.Fatalf("empty root should yield ('', nil), got (%q, %v)", uri, err)
	}
	if uri, err := resolvePayloadPath("/tmp", ""); err != nil || uri != "" {
		t.Fatalf("empty refPath should yield ('', nil), got (%q, %v)", uri, err)
	}
	if _, err := resolvePayloadPath("/tmp", "../etc/passwd"); err == nil {
		t.Fatalf("expected traversal-guard rejection for ../etc/passwd")
	}
	uri, err := resolvePayloadPath("/tmp", "payloads/x/y.bin")
	if err != nil {
		t.Fatalf("legit ref: %v", err)
	}
	if !strings.HasPrefix(uri, "file:///tmp/payloads/x/y.bin") && uri != "file:///tmp/payloads/x/y.bin" {
		t.Fatalf("uri = %q, want file:///tmp/payloads/x/y.bin", uri)
	}
}
